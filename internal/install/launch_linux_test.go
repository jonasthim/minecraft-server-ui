//go:build linux

package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
)

func TestCheckLinuxInstallerStartupAbsentIsReadOnly(t *testing.T) {
	state := filepath.Join(t.TempDir(), "missing-state")
	ctx := withLaunchLinuxConfig(context.Background(), launchLinuxConfig{
		stateRoot: state,
		manager:   filepath.Join(t.TempDir(), "manager"),
		procRoot:  "/proc",
		uid:       uint32(os.Geteuid()),
		authority: func() error { return nil },
		testRoot:  true,
	})
	allowed, err := CheckLinuxInstallerStartup(ctx)
	if err != nil || allowed {
		t.Fatalf("absent installer state should select ordinary recovery: allowed=%v err=%v", allowed, err)
	}
	if _, err := os.Lstat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("startup inspection created the absent state root: %v", err)
	}
}

func TestCheckLinuxInstallerStartupRequiresAuthorityBeforeFilesystemIO(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	ctx := withLaunchLinuxConfig(context.Background(), launchLinuxConfig{
		stateRoot: state,
		manager:   filepath.Join(t.TempDir(), "manager"),
		procRoot:  "/proc",
		uid:       uint32(os.Geteuid()),
		authority: func() error { return errors.New("authority denied") },
		testRoot:  true,
	})
	allowed, err := CheckLinuxInstallerStartup(ctx)
	if err == nil || allowed {
		t.Fatalf("startup inspection without authority was accepted: allowed=%v err=%v", allowed, err)
	}
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 0 {
		t.Fatalf("authority refusal touched installer state: entries=%v err=%v", entries, err)
	}
}

func TestCheckLinuxInstallerStartupAcceptsOnlyCompleteTerminalOperationResidue(t *testing.T) {
	t.Run("empty install directory selects ordinary recovery", func(t *testing.T) {
		state := filepath.Join(t.TempDir(), "state")
		if err := os.Mkdir(state, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(state, installDirectory), 0700); err != nil {
			t.Fatal(err)
		}
		ctx := withLaunchLinuxConfig(context.Background(), launchLinuxConfig{
			stateRoot: state, manager: filepath.Join(t.TempDir(), "manager"),
			procRoot: "/proc", uid: uint32(os.Geteuid()), authority: func() error { return nil }, testRoot: true,
		})
		allowed, err := CheckLinuxInstallerStartup(ctx)
		if err != nil || allowed {
			t.Fatalf("empty installer operation directory blocked ordinary recovery: allowed=%v err=%v", allowed, err)
		}
	})

	t.Run("complete terminal result selects ordinary recovery", func(t *testing.T) {
		fixture := newLinuxLaunchFlowFixture(t)
		defer fixture.release()
		if err := os.Remove(filepath.Join(fixture.config.stateRoot, journalName)); err != nil {
			t.Fatal(err)
		}
		operationPath := fixture.operationPath
		effects := []byte(`{"format":1,"operation_id":"` + fixture.tx.ID + `","effects":[]}`)
		if err := os.WriteFile(filepath.Join(operationPath, linuxEffectsName), effects, 0600); err != nil {
			t.Fatal(err)
		}
		result, err := json.Marshal(struct {
			Outcome     Outcome `json:"outcome"`
			OperationID string  `json:"operation_id"`
			Image       *Image  `json:"image,omitempty"`
			FailedStage string  `json:"failed_stage"`
		}{OutcomeInstalled, fixture.tx.ID, &fixture.image, ""})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(operationPath, linuxResultName), result, 0600); err != nil {
			t.Fatal(err)
		}

		ctx := withLaunchLinuxConfig(context.Background(), fixture.config)
		allowed, err := CheckLinuxInstallerStartup(ctx)
		if err != nil || allowed {
			t.Fatalf("valid terminal installer residue blocked ordinary recovery: allowed=%v err=%v", allowed, err)
		}
	})

	t.Run("operation without terminal result still requires recovery", func(t *testing.T) {
		fixture := newLinuxLaunchFlowFixture(t)
		defer fixture.release()
		if err := os.Remove(filepath.Join(fixture.config.stateRoot, journalName)); err != nil {
			t.Fatal(err)
		}
		effects := []byte(`{"format":1,"operation_id":"` + fixture.tx.ID + `","effects":[]}`)
		if err := os.WriteFile(filepath.Join(fixture.operationPath, linuxEffectsName), effects, 0600); err != nil {
			t.Fatal(err)
		}

		ctx := withLaunchLinuxConfig(context.Background(), fixture.config)
		allowed, err := CheckLinuxInstallerStartup(ctx)
		if allowed || !errors.Is(err, errLinuxInstallerRecoveryRequired) {
			t.Fatalf("operation without terminal result was accepted: allowed=%v err=%v", allowed, err)
		}
	})

	t.Run("pending result still requires recovery", func(t *testing.T) {
		fixture := newLinuxLaunchFlowFixture(t)
		defer fixture.release()
		if err := os.Remove(filepath.Join(fixture.config.stateRoot, journalName)); err != nil {
			t.Fatal(err)
		}
		operationPath := fixture.operationPath
		effects := []byte(`{"format":1,"operation_id":"` + fixture.tx.ID + `","effects":[]}`)
		if err := os.WriteFile(filepath.Join(operationPath, linuxEffectsName), effects, 0600); err != nil {
			t.Fatal(err)
		}
		result, err := json.Marshal(struct {
			Outcome     Outcome `json:"outcome"`
			OperationID string  `json:"operation_id"`
			Image       *Image  `json:"image,omitempty"`
			FailedStage string  `json:"failed_stage"`
		}{OutcomePending, fixture.tx.ID, &fixture.image, ""})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(operationPath, linuxResultName), result, 0600); err != nil {
			t.Fatal(err)
		}
		ctx := withLaunchLinuxConfig(context.Background(), fixture.config)
		allowed, err := CheckLinuxInstallerStartup(ctx)
		if allowed || !errors.Is(err, errLinuxInstallerRecoveryRequired) {
			t.Fatalf("pending operation result was accepted as terminal: allowed=%v err=%v", allowed, err)
		}
	})

	t.Run("unknown operation entry still requires recovery", func(t *testing.T) {
		fixture := newLinuxLaunchFlowFixture(t)
		defer fixture.release()
		if err := os.Remove(filepath.Join(fixture.config.stateRoot, journalName)); err != nil {
			t.Fatal(err)
		}
		operationPath := fixture.operationPath
		effects := []byte(`{"format":1,"operation_id":"` + fixture.tx.ID + `","effects":[]}`)
		if err := os.WriteFile(filepath.Join(operationPath, linuxEffectsName), effects, 0600); err != nil {
			t.Fatal(err)
		}
		result, err := json.Marshal(struct {
			Outcome     Outcome `json:"outcome"`
			OperationID string  `json:"operation_id"`
			Image       *Image  `json:"image,omitempty"`
			FailedStage string  `json:"failed_stage"`
		}{OutcomeInstalled, fixture.tx.ID, &fixture.image, ""})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(operationPath, linuxResultName), result, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(operationPath, "foreign.json"), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		ctx := withLaunchLinuxConfig(context.Background(), fixture.config)
		allowed, err := CheckLinuxInstallerStartup(ctx)
		if allowed || !errors.Is(err, errLinuxInstallerRecoveryRequired) {
			t.Fatalf("unknown operation entry was accepted: allowed=%v err=%v", allowed, err)
		}
	})
}

func TestReadLinuxLaunchPermitStrictFormat(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, _, err := readLinuxLaunchPermit(context.Background(), root, uint32(os.Geteuid())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing permit should be absent: %v", err)
	}
	for _, raw := range [][]byte{
		[]byte(`{"format":1,"operation_id":"x"}`),
		[]byte(`{"format":1,"format":1,"operation_id":"x"}`),
		[]byte(`{"format":1,"operation_id":"x"} {}`),
	} {
		if err := os.WriteFile(filepath.Join(root.Name(), launchLinuxPermitName), raw, 0600); err != nil {
			t.Fatal(err)
		}
		body, _, err := readLinuxLaunchPermit(context.Background(), root, uint32(os.Geteuid()))
		if err != nil {
			t.Fatalf("well-shaped test file should be read before decode: %v", err)
		}
		if _, err := decodeLinuxLaunchPermit(body); err == nil {
			t.Fatalf("malformed permit accepted: raw=%s", raw)
		}
		if err := root.Remove(launchLinuxPermitName); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLinuxInstallerLaunchLockRequiresActualHeldKernelFlock(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	config := launchLinuxConfig{stateRoot: state, procRoot: "/proc", uid: uint32(os.Geteuid()), testRoot: true}
	tx := transaction{ID: "operation"}
	release, err := launcher.AcquireManager(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := launchLinuxOwnsUpdaterLock(context.Background(), config, tx, os.Getpid()); err != nil {
		release()
		t.Fatalf("actual current-process flock was not recognized: %v", err)
	}
	if err := launchLinuxOwnsUpdaterLock(context.Background(), config, tx, os.Getpid()+1); err == nil {
		release()
		t.Fatal("lock accepted a different process owner")
	}
	release()
	if err := launchLinuxOwnsUpdaterLock(context.Background(), config, tx, os.Getpid()); err == nil {
		t.Fatal("released kernel flock remained authorized")
	}
}

func TestLinuxInstallerUpdaterLockRejectsCanonicalIdentityReplacementDuringProcRead(t *testing.T) {
	tests := []struct {
		name    string
		replace func(*testing.T, string) error
	}{
		{
			name: "lock file",
			replace: func(t *testing.T, state string) error {
				if err := os.Rename(filepath.Join(state, "manager.lock"), filepath.Join(state, "old-manager.lock")); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(state, "manager.lock"), []byte("same lock bytes are irrelevant"), 0600)
			},
		},
		{
			name: "state root",
			replace: func(t *testing.T, state string) error {
				moved := state + ".held"
				if err := os.Rename(state, moved); err != nil {
					return err
				}
				if err := os.Mkdir(state, 0700); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(state, "manager.lock"), []byte("replacement root lock"), 0600)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(state, 0700); err != nil {
				t.Fatal(err)
			}
			release, err := launcher.AcquireManager(state)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			config := launchLinuxConfig{
				stateRoot: state,
				procRoot:  "/proc",
				uid:       uint32(os.Geteuid()),
				testRoot:  true,
				readProc: func(name string, limit int64) ([]byte, error) {
					if name != "locks" {
						return nil, errors.New("unexpected proc read")
					}
					body, err := os.ReadFile("/proc/locks")
					if err != nil {
						return nil, err
					}
					if int64(len(body)) > limit {
						return nil, errors.New("proc fixture exceeds bound")
					}
					if err := test.replace(t, state); err != nil {
						return nil, err
					}
					return body, nil
				},
			}
			if err := launchLinuxOwnsUpdaterLock(context.Background(), config, transaction{ID: "operation"}, os.Getpid()); err == nil {
				t.Fatal("lock proof accepted after canonical lock identity was replaced")
			}
		})
	}
}

func TestLinuxInstallerStartupBlocksJournalWithoutPermitAndUnknownPending(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing permit", true: "unknown pending"}[pending], func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(state, 0700); err != nil {
				t.Fatal(err)
			}
			linuxLaunchWriteJournalFixture(t, state)
			if pending {
				if err := os.WriteFile(filepath.Join(state, launchLinuxPendingName), []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx := withLaunchLinuxConfig(context.Background(), launchLinuxConfig{
				stateRoot: state, manager: filepath.Join(t.TempDir(), "manager"),
				procRoot: "/proc", uid: uint32(os.Geteuid()), authority: func() error { return nil }, testRoot: true,
			})
			allowed, err := CheckLinuxInstallerStartup(ctx)
			if err == nil || allowed || !errors.Is(err, errLinuxInstallerRecoveryRequired) {
				t.Fatalf("incomplete installer state bypassed recovery: allowed=%v err=%v", allowed, err)
			}
		})
	}
}

func TestLinuxInstallerLaunchGrantStartupRevokeFlow(t *testing.T) {
	fixture := newLinuxLaunchFlowFixture(t)
	defer fixture.release()
	ctx := withLaunchLinuxConfig(context.Background(), fixture.config)
	if err := grantLinuxInstallerLaunch(ctx, fixture.tx, fixture.image); err != nil {
		t.Fatalf("grant valid launch: %v", err)
	}
	if err := grantLinuxInstallerLaunch(ctx, fixture.tx, fixture.image); err != nil {
		t.Fatalf("repeat exact valid grant: %v", err)
	}
	allowed, err := CheckLinuxInstallerStartup(ctx)
	if err != nil || !allowed {
		t.Fatalf("live exact permit was rejected: allowed=%v err=%v", allowed, err)
	}
	if err := revokeLinuxInstallerLaunch(ctx, fixture.tx); err != nil {
		t.Fatalf("revoke valid permit: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(fixture.config.stateRoot, launchLinuxPermitName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("revoked permit remains: %v", err)
	}
	allowed, err = CheckLinuxInstallerStartup(ctx)
	if err == nil || allowed || !errors.Is(err, errLinuxInstallerRecoveryRequired) {
		t.Fatalf("journal without permit did not require ordinary recovery: allowed=%v err=%v", allowed, err)
	}
}

func TestLinuxInstallerRollbackRevokesExactStaleCandidatePermit(t *testing.T) {
	fixture := newLinuxLaunchFlowFixture(t)
	defer fixture.release()
	ctx := withLaunchLinuxConfig(context.Background(), fixture.config)
	if err := grantLinuxInstallerLaunch(ctx, fixture.tx, fixture.image); err != nil {
		t.Fatalf("grant candidate permit before failed readiness: %v", err)
	}

	root, err := os.OpenRoot(fixture.config.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := newJournalStoreWithConfig(context.Background(), root, journalStoreConfig{testRoot: true})
	if err != nil {
		root.Close()
		t.Fatal(err)
	}
	current, err := store.Load(context.Background())
	if err != nil || current == nil {
		store.Close()
		root.Close()
		t.Fatalf("load verifying journal: tx=%v err=%v", current, err)
	}
	rollingBack := *current
	rollingBack.Phase = phaseRollingBack
	rollingBack.FailedStage = "verify"
	if err := store.Record(context.Background(), current, rollingBack); err != nil {
		store.Close()
		root.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		root.Close()
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}

	if err := revokeLinuxInstallerLaunch(ctx, rollingBack); err != nil {
		t.Fatalf("rollback could not remove its exact stale candidate permit: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(fixture.config.stateRoot, launchLinuxPermitName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("candidate permit remains after rollback revocation: %v", err)
	}
}

func TestLinuxInstallerRollbackPreservesForeignLaunchPermit(t *testing.T) {
	fixture := newLinuxLaunchFlowFixture(t)
	defer fixture.release()
	ctx := withLaunchLinuxConfig(context.Background(), fixture.config)
	if err := grantLinuxInstallerLaunch(ctx, fixture.tx, fixture.image); err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(fixture.config.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := newJournalStoreWithConfig(context.Background(), root, journalStoreConfig{testRoot: true})
	if err != nil {
		root.Close()
		t.Fatal(err)
	}
	current, err := store.Load(context.Background())
	if err != nil || current == nil {
		store.Close()
		root.Close()
		t.Fatalf("load verifying journal: tx=%v err=%v", current, err)
	}
	rollingBack := *current
	rollingBack.Phase = phaseRollingBack
	rollingBack.FailedStage = "verify"
	if err := store.Record(context.Background(), current, rollingBack); err != nil {
		store.Close()
		root.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		root.Close()
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}

	permitPath := filepath.Join(fixture.config.stateRoot, launchLinuxPermitName)
	raw, err := os.ReadFile(permitPath)
	if err != nil {
		t.Fatal(err)
	}
	foreignID := strings.Repeat("f", 64)
	foreign := bytes.Replace(raw, []byte(fixture.tx.ID), []byte(foreignID), 1)
	if bytes.Equal(raw, foreign) {
		t.Fatal("fixture permit did not contain operation identity")
	}
	if err := os.WriteFile(permitPath, foreign, 0600); err != nil {
		t.Fatal(err)
	}
	if err := revokeLinuxInstallerLaunch(ctx, rollingBack); !errors.Is(err, errLinuxInstallerRecoveryRequired) {
		t.Fatalf("rollback accepted a foreign permit: %v", err)
	}
	after, err := os.ReadFile(permitPath)
	if err != nil || !bytes.Equal(after, foreign) {
		t.Fatalf("foreign permit was not preserved: err=%v", err)
	}
}

func TestLinuxInstallerLaunchRejectsWrongPhaseImageExecutorAndReusedPID(t *testing.T) {
	t.Run("wrong phase", func(t *testing.T) {
		fixture := newLinuxLaunchFlowFixture(t)
		defer fixture.release()
		wrong := fixture.tx
		wrong.Phase = phasePrepared
		if err := grantLinuxInstallerLaunch(withLaunchLinuxConfig(context.Background(), fixture.config), wrong, fixture.image); !errors.Is(err, errLinuxInstallerRecoveryRequired) {
			t.Fatalf("prepared phase grant was not blocked: %v", err)
		}
	})
	t.Run("wrong image", func(t *testing.T) {
		fixture := newLinuxLaunchFlowFixture(t)
		defer fixture.release()
		wrong := fixture.image
		wrong.SHA256 = strings.Repeat("f", 64)
		if err := grantLinuxInstallerLaunch(withLaunchLinuxConfig(context.Background(), fixture.config), fixture.tx, wrong); !errors.Is(err, errLinuxInstallerRecoveryRequired) {
			t.Fatalf("wrong requested image was not blocked: %v", err)
		}
	})
	t.Run("copied equal executor bytes have a different inode", func(t *testing.T) {
		fixture := newLinuxLaunchFlowFixture(t)
		defer fixture.release()
		path := filepath.Join(fixture.operationPath, launchLinuxExecutorName)
		// Keep the original inode allocated while replacing its directory entry.
		// Otherwise a filesystem may immediately reuse the inode number, making
		// this identity test nondeterministic even though the file was recreated.
		original, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer original.Close()
		originalInfo, err := original.Stat()
		if err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0755); err != nil {
			t.Fatal(err)
		}
		replacementInfo, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if os.SameFile(originalInfo, replacementInfo) {
			t.Fatal("test filesystem reused the still-open executor inode")
		}
		if err := grantLinuxInstallerLaunch(withLaunchLinuxConfig(context.Background(), fixture.config), fixture.tx, fixture.image); !errors.Is(err, errLinuxInstallerRecoveryRequired) {
			t.Fatalf("same-byte executor replacement was accepted: %v", err)
		}
	})
	t.Run("process start changed", func(t *testing.T) {
		fixture := newLinuxLaunchFlowFixture(t)
		defer fixture.release()
		ctx := withLaunchLinuxConfig(context.Background(), fixture.config)
		if err := grantLinuxInstallerLaunch(ctx, fixture.tx, fixture.image); err != nil {
			t.Fatal(err)
		}
		fixture.startTicks = "43"
		allowed, err := CheckLinuxInstallerStartup(ctx)
		if err == nil || allowed || !errors.Is(err, errLinuxInstallerRecoveryRequired) {
			t.Fatalf("reused PID/start token was accepted: allowed=%v err=%v", allowed, err)
		}
	})
	t.Run("lock released", func(t *testing.T) {
		fixture := newLinuxLaunchFlowFixture(t)
		ctx := withLaunchLinuxConfig(context.Background(), fixture.config)
		if err := grantLinuxInstallerLaunch(ctx, fixture.tx, fixture.image); err != nil {
			fixture.release()
			t.Fatal(err)
		}
		fixture.release()
		allowed, err := CheckLinuxInstallerStartup(ctx)
		if err == nil || allowed || !errors.Is(err, errLinuxInstallerRecoveryRequired) {
			t.Fatalf("permit survived release of actual kernel lock: allowed=%v err=%v", allowed, err)
		}
	})
}

func TestLinuxInstallerLaunchPublicationFailuresPreserveTruth(t *testing.T) {
	t.Run("prepublication failure removes owned pending", func(t *testing.T) {
		fixture := newLinuxLaunchFlowFixture(t)
		defer fixture.release()
		fixture.config.beforePublish = func() error { return errors.New("injected publication fence failure") }
		err := grantLinuxInstallerLaunch(withLaunchLinuxConfig(context.Background(), fixture.config), fixture.tx, fixture.image)
		if !errors.Is(err, errLinuxInstallerRecoveryRequired) {
			t.Fatalf("publication failure did not block launch: %v", err)
		}
		for _, name := range []string{launchLinuxPendingName, launchLinuxPermitName} {
			if _, err := os.Lstat(filepath.Join(fixture.config.stateRoot, name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("owned failed publication was not cleaned at %s: %v", name, err)
			}
		}
	})
	t.Run("postpublication uncertainty keeps both links", func(t *testing.T) {
		fixture := newLinuxLaunchFlowFixture(t)
		defer fixture.release()
		fixture.config.afterPublish = func() error { return errors.New("injected postpublication fence failure") }
		err := grantLinuxInstallerLaunch(withLaunchLinuxConfig(context.Background(), fixture.config), fixture.tx, fixture.image)
		if !errors.Is(err, errLinuxInstallerRecoveryRequired) {
			t.Fatalf("postpublication uncertainty was not surfaced: %v", err)
		}
		permitInfo, err := os.Stat(filepath.Join(fixture.config.stateRoot, launchLinuxPermitName))
		if err != nil {
			t.Fatalf("uncertain permit was discarded: %v", err)
		}
		pendingInfo, err := os.Stat(filepath.Join(fixture.config.stateRoot, launchLinuxPendingName))
		if err != nil || !os.SameFile(permitInfo, pendingInfo) {
			t.Fatalf("uncertain pending link was not preserved: %v", err)
		}
		stat, ok := permitInfo.Sys().(*syscall.Stat_t)
		if !ok || stat.Nlink != 2 {
			t.Fatalf("uncertain publication link count is not preserved: %#v", permitInfo.Sys())
		}
	})
}

type linuxLaunchFlowFixture struct {
	config        launchLinuxConfig
	tx            transaction
	image         Image
	operationPath string
	startTicks    string
	release       func()
}

func newLinuxLaunchFlowFixture(t *testing.T) *linuxLaunchFlowFixture {
	t.Helper()
	base := t.TempDir()
	state := filepath.Join(base, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executableBytes, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(executableBytes)
	executableHash := hex.EncodeToString(sum[:])
	managerPath := filepath.Join(base, "bin", "minecraft-ui")
	if err := os.Mkdir(filepath.Dir(managerPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managerPath, executableBytes, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(managerPath, 0755); err != nil {
		t.Fatal(err)
	}
	intent, tx := freshLinuxApplyFixture()
	intent.Target.SHA256 = executableHash
	intent.Assets[0].After.SHA256 = executableHash
	intent.ExecutorSHA256 = executableHash
	intent.Assets[1].After.SHA256 = executableHash
	tx.Target.SHA256 = executableHash
	image := *intent.Target
	rawIntent, err := marshalLinuxIntent(intent, tx)
	if err != nil {
		t.Fatal(err)
	}
	intentDigest := sha256.Sum256(rawIntent)
	tx.IntentSHA256 = hex.EncodeToString(intentDigest[:])
	operationPath := filepath.Join(state, installDirectory, tx.ID)
	if err := os.MkdirAll(operationPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(operationPath, intentFileName), rawIntent, 0600); err != nil {
		t.Fatal(err)
	}
	executorPath := filepath.Join(operationPath, launchLinuxExecutorName)
	if err := os.WriteFile(executorPath, executableBytes, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(executorPath, 0755); err != nil {
		t.Fatal(err)
	}
	executorInfo, err := os.Stat(executorPath)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		t.Fatal(err)
	}
	store, err := newJournalStoreWithConfig(context.Background(), root, journalStoreConfig{testRoot: true})
	if err != nil {
		root.Close()
		t.Fatal(err)
	}
	if err := store.Record(context.Background(), nil, tx); err != nil {
		store.Close()
		root.Close()
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		store.Close()
		root.Close()
		t.Fatal(err)
	}
	verified := *loaded
	verified.Phase = phaseVerifying
	if err := store.Record(context.Background(), loaded, verified); err != nil {
		store.Close()
		root.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		root.Close()
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	release, err := launcher.AcquireManager(state)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &linuxLaunchFlowFixture{tx: verified, image: image, operationPath: operationPath, startTicks: "42", release: release}
	fixture.config = launchLinuxConfig{
		stateRoot: state, manager: managerPath, procRoot: "/proc", uid: uint32(os.Geteuid()),
		authority: func() error { return nil }, testRoot: true,
		processStart: func(pid int) (string, error) {
			if pid != os.Getpid() {
				return "", errors.New("unexpected test PID")
			}
			return fixture.startTicks, nil
		},
		processExe: func(pid int, expected os.FileInfo) error {
			if pid != os.Getpid() || !os.SameFile(expected, executorInfo) {
				return errors.New("test process does not match pinned executor inode")
			}
			return nil
		},
	}
	return fixture
}

func linuxLaunchWriteJournalFixture(t *testing.T, statePath string) {
	t.Helper()
	intent, tx := freshLinuxApplyFixture()
	raw, err := marshalLinuxIntent(intent, tx)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	tx.IntentSHA256 = hex.EncodeToString(digest[:])
	intentRoot := filepath.Join(statePath, installDirectory, tx.ID)
	if err := os.MkdirAll(intentRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(intentRoot, intentFileName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	store, err := newJournalStoreWithConfig(context.Background(), root, journalStoreConfig{testRoot: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Record(context.Background(), nil, tx); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	next := *loaded
	next.Phase = phaseVerifying
	if err := store.Record(context.Background(), loaded, next); err != nil {
		t.Fatal(err)
	}
}
