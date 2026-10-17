//go:build linux

package install

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/installinputs"
	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
	"github.com/jonasthim/minecraft-server-ui/release/candidate"
)

type nativeFlowSession struct {
	statePath  string
	locked     bool
	snapshots  int
	reconciles int
}

func (s *nativeFlowSession) OpenState(context.Context) (*os.Root, error) {
	return os.OpenRoot(s.statePath)
}
func (s *nativeFlowSession) SnapshotUpdater(context.Context) (*selfupdate.LinuxUpdaterSnapshot, error) {
	s.snapshots++
	return nil, nil
}
func (s *nativeFlowSession) LockStoppedManager(context.Context) error { s.locked = true; return nil }
func (s *nativeFlowSession) UnlockManager() error                     { s.locked = false; return nil }
func (s *nativeFlowSession) ReconcileUpdater(context.Context, *selfupdate.LinuxUpdaterSnapshot) error {
	s.reconciles++
	return nil
}
func (s *nativeFlowSession) Receipt(context.Context) (*selfupdate.MaintenanceReceipt, error) {
	return nil, nil
}
func (s *nativeFlowSession) CompareAndSwapInstalled(context.Context, *selfupdate.MaintenanceReceipt, selfupdate.Installed) error {
	return nil
}
func (s *nativeFlowSession) RemoveInstalledForRollback(context.Context, *selfupdate.MaintenanceReceipt, selfupdate.Installed) error {
	return nil
}
func (s *nativeFlowSession) Close() error { return nil }

type nativeFlowInputs struct {
	summary installinputs.Summary
	members []candidate.Member
	paths   map[string]string
	closed  bool
}

func TestStageExecutorHonorsRestrictiveUmask(t *testing.T) {
	const helperEnv = "MCSUI_STAGE_EXECUTOR_UMASK_HELPER"
	if os.Getenv(helperEnv) == "1" {
		syscall.Umask(0077)
		assertStageExecutorWithRestrictiveUmask(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestStageExecutorHonorsRestrictiveUmask$")
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("StageExecutor failed in umask 0077 subprocess: %v\n%s", err, output)
	}
}

const (
	linuxWorkerFixturePhaseEnv = "MCSUI_LINUX_WORKER_EXEC_PHASE"
	linuxWorkerFixtureStateEnv = "MCSUI_LINUX_WORKER_EXEC_STATE"
)

func TestLinuxWorkerLoadsAndVerifiesStagedExecutorAfterExec(t *testing.T) {
	const phaseEnv = linuxWorkerFixturePhaseEnv
	const stateEnv = linuxWorkerFixtureStateEnv
	switch os.Getenv(phaseEnv) {
	case "stage":
		stageLinuxWorkerExecFixture(t, os.Getenv(stateEnv))
	case "verify":
		verifyLinuxWorkerExecFixture(t, os.Getenv(stateEnv))
	default:
		stateRoot := filepath.Join(t.TempDir(), "state")
		cmd := exec.Command(os.Args[0], "-test.run=^TestLinuxWorkerLoadsAndVerifiesStagedExecutorAfterExec$")
		cmd.Env = linuxWorkerFixtureEnv(phaseEnv, stateEnv, "stage", stateRoot)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("stage/exec/load/verify child failed: %v\n%s", err, output)
		}
	}
}

func stageLinuxWorkerExecFixture(t *testing.T, stateRoot string) {
	t.Helper()
	syscall.Umask(0077)
	if stateRoot == "" || filepath.Clean(stateRoot) != stateRoot {
		t.Fatal("worker fixture state root is invalid")
	}
	if err := os.MkdirAll(filepath.Join(stateRoot, installDirectory), 0700); err != nil {
		t.Fatal(err)
	}
	intent, tx := freshLinuxApplyFixture()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	_, executableHash, err := hashExecutor(context.Background(), file)
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	intent.ExecutorSHA256 = executableHash
	for i := range intent.Assets {
		if intent.Assets[i].Role == linuxAssetUpdater {
			info, err := file.Stat()
			if err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			intent.Assets[i].After.SHA256 = executableHash
			intent.Assets[i].After.Size = info.Size()
			intent.Assets[i].After.Mode = 0755
		}
	}
	rawIntent, err := marshalLinuxIntent(intent, tx)
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	intentDigest := sha256.Sum256(rawIntent)
	tx.IntentSHA256 = hex.EncodeToString(intentDigest[:])

	opPath := filepath.Join(stateRoot, installDirectory, tx.ID)
	if err := os.Mkdir(opPath, 0700); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	sourcePath := filepath.Join(stateRoot, "executor-source")
	source, err := os.Create(sourcePath)
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if _, err := io.Copy(source, file); err != nil {
		_ = source.Close()
		_ = file.Close()
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := os.OpenRoot(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := newJournalStoreWithConfig(context.Background(), state, journalStoreConfig{testRoot: true})
	if err != nil {
		_ = state.Close()
		t.Fatal(err)
	}
	operation, err := os.OpenRoot(opPath)
	if err != nil {
		_ = store.Close()
		_ = state.Close()
		t.Fatal(err)
	}
	driver := &linuxInstallDriver{
		state: state, store: store, operation: operation,
		inputs: &nativeFlowInputs{paths: map[string]string{"selfupdate": sourcePath}},
		intent: &intent, tx: &tx,
		hooks: linuxNativeHooks{stateRoot: stateRoot, testRoot: true},
	}
	executor, err := driver.StageExecutor(context.Background(), tx)
	if err != nil {
		_ = driver.Close()
		t.Fatal(err)
	}
	if err := os.Remove(sourcePath); err != nil {
		_ = driver.Close()
		t.Fatal(err)
	}
	writeIntent, err := operation.OpenFile(intentFileName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		_ = driver.Close()
		t.Fatal(err)
	}
	if err := writeAll(writeIntent, rawIntent); err != nil {
		_ = writeIntent.Close()
		_ = driver.Close()
		t.Fatal(err)
	}
	if err := writeIntent.Sync(); err != nil {
		_ = writeIntent.Close()
		_ = driver.Close()
		t.Fatal(err)
	}
	if err := writeIntent.Close(); err != nil {
		_ = driver.Close()
		t.Fatal(err)
	}
	if err := syncDirectory(operation); err != nil {
		_ = driver.Close()
		t.Fatal(err)
	}
	if err := driver.Record(context.Background(), nil, tx); err != nil {
		_ = driver.Close()
		t.Fatal(err)
	}
	if err := driver.Close(); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Exec(executor, []string{executor, "-test.run=^TestLinuxWorkerLoadsAndVerifiesStagedExecutorAfterExec$"}, linuxWorkerFixtureEnv(linuxWorkerFixturePhaseEnv, linuxWorkerFixtureStateEnv, "verify", stateRoot)); err != nil {
		t.Fatal(err)
	}
}

func linuxWorkerFixtureEnv(phaseKey, stateKey, phase, state string) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, phaseKey+"=") || strings.HasPrefix(entry, stateKey+"=") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, phaseKey+"="+phase, stateKey+"="+state)
}

func verifyLinuxWorkerExecFixture(t *testing.T, stateRoot string) {
	t.Helper()
	session := &nativeFlowSession{statePath: stateRoot}
	driver, err := newLinuxInstallDriverWithSession(context.Background(), session, linuxNativeHooks{
		stateRoot: stateRoot, testRoot: true, authority: func() error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	tx, err := driver.Load(context.Background())
	if err != nil || tx == nil {
		t.Fatalf("Load after staged executor exec: tx=%v err=%v", tx, err)
	}
	if err := driver.VerifyExecutor(context.Background(), *tx); err != nil {
		t.Fatalf("VerifyExecutor after staged executor exec: %v", err)
	}
}

func assertStageExecutorWithRestrictiveUmask(t *testing.T) {
	t.Helper()
	base := t.TempDir()
	stateRoot := filepath.Join(base, "state")
	operationPath := filepath.Join(stateRoot, installDirectory, strings.Repeat("1", 64))
	if err := os.MkdirAll(operationPath, 0700); err != nil {
		t.Fatal(err)
	}
	state, err := os.OpenRoot(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	operation, err := os.OpenRoot(operationPath)
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Close()

	intent, tx := freshLinuxApplyFixture()
	executor := []byte("verified updater executor fixture")
	sum := sha256.Sum256(executor)
	executorHash := hex.EncodeToString(sum[:])
	intent.ExecutorSHA256 = executorHash
	for i := range intent.Assets {
		if intent.Assets[i].Role == linuxAssetUpdater {
			intent.Assets[i].After.SHA256 = executorHash
			intent.Assets[i].After.Size = int64(len(executor))
			intent.Assets[i].After.Mode = 0755
		}
	}
	rawIntent, err := marshalLinuxIntent(intent, tx)
	if err != nil {
		t.Fatal(err)
	}
	intentDigest := sha256.Sum256(rawIntent)
	tx.IntentSHA256 = hex.EncodeToString(intentDigest[:])
	executorSource := filepath.Join(base, "executor-source")
	if err := os.WriteFile(executorSource, executor, 0600); err != nil {
		t.Fatal(err)
	}
	driver := &linuxInstallDriver{
		state: state, store: &journalStore{}, operation: operation,
		inputs: &nativeFlowInputs{paths: map[string]string{"selfupdate": executorSource}},
		intent: &intent, tx: &tx,
		hooks: linuxNativeHooks{stateRoot: stateRoot, testRoot: true},
	}
	path, err := driver.StageExecutor(context.Background(), tx)
	if err != nil {
		t.Fatalf("StageExecutor: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0755 {
		t.Fatalf("staged executor mode=%v; want regular mode 0755 under umask 0077", info.Mode())
	}
}

func (i *nativeFlowInputs) Summary() installinputs.Summary { return i.summary }
func (i *nativeFlowInputs) Members() []candidate.Member {
	return append([]candidate.Member(nil), i.members...)
}
func (i *nativeFlowInputs) OpenMember(ctx context.Context, name string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if i.closed {
		return nil, os.ErrClosed
	}
	path, ok := i.paths[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return os.Open(path)
}
func (i *nativeFlowInputs) Close() error { i.closed = true; return nil }

func TestFreshNativePrepareCloseLoadQuiesceReplaceComposition(t *testing.T) {
	base := t.TempDir()
	stateRoot := filepath.Join(base, "state")
	dataRoot := filepath.Join(base, "manager-data")
	prepared := filepath.Join(base, "prepared")
	if err := os.Mkdir(stateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(prepared, 0700); err != nil {
		t.Fatal(err)
	}
	trustBytes, trustPath, trust := writeNativeTrust(t, base)
	members, memberPaths := writeNativeMembers(t, base, prepared)
	manifestHash := strings.Repeat("a", 64)
	preparedInputs := &nativeFlowInputs{summary: installinputs.Summary{Version: "v1.2.3", Schema: 18, OS: "linux", Arch: "amd64", ManifestSHA256: manifestHash, MemberCount: len(members)}, members: members, paths: memberPaths}
	assetRoot := filepath.Join(base, "assets")
	if err := os.Mkdir(assetRoot, 0755); err != nil {
		t.Fatal(err)
	}
	paths := make(map[linuxAssetRole]string, len(fixedLinuxAssets))
	for _, spec := range fixedLinuxAssets {
		paths[spec.Role] = filepath.Join(assetRoot, string(spec.Role), filepath.Base(spec.Path))
	}
	previousAssets := linuxAssetTestPolicy
	linuxAssetTestPolicy = &linuxAssetTestPolicyConfig{root: assetRoot, uid: uint32(os.Geteuid()), paths: paths}
	t.Cleanup(func() { linuxAssetTestPolicy = previousAssets })

	principal := linuxPrincipal{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())}
	accountPresent := false
	status := linuxServiceStatus{LoadState: "not-found", ActiveState: "inactive", SubState: "dead", UnitFileState: "disabled"}
	reopened := false
	var firstDriver *linuxInstallDriver
	hooks := linuxNativeHooks{
		stateRoot: stateRoot, dataRoot: dataRoot, testRoot: true,
		account:       func(context.Context) (linuxPrincipal, bool, error) { return principal, accountPresent, nil },
		createUser:    func(context.Context) (linuxPrincipal, error) { accountPresent = true; return principal, nil },
		service:       func(context.Context) (linuxServiceStatus, error) { return status, nil },
		reload:        func(context.Context) error { return nil },
		probeSchema:   func(context.Context, string, linuxPrincipal, int64) error { return nil },
		configuration: func(context.Context) error { return nil },
		now:           func() time.Time { return time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC) },
		importInputs: func(_ context.Context, _ *os.Root, options installinputs.Options) (linuxInstallerInputs, error) {
			if options.Prepared != prepared || options.Version != "v1.2.3" || options.Trust.Repository != trust.Repository {
				t.Fatalf("import options=%+v", options)
			}
			return preparedInputs, nil
		},
		openRecorded: func(_ context.Context, _ *os.Root, options installinputs.RecordedOptions) (linuxInstallerInputs, error) {
			if firstDriver == nil || !preparedInputs.closed {
				t.Fatal("recorded inputs opened before the parent released its pinned copy")
			}
			if options.Version != "v1.2.3" || options.ManifestSHA256 != manifestHash || options.Trust.Repository != trust.Repository {
				t.Fatalf("recorded input options=%+v", options)
			}
			reopened = true
			return &nativeFlowInputs{summary: preparedInputs.summary, members: preparedInputs.members, paths: preparedInputs.paths}, nil
		},
	}
	newDriver := func() *linuxInstallDriver {
		d, err := newLinuxInstallDriverWithSession(context.Background(), &nativeFlowSession{statePath: stateRoot}, hooks)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	d1 := newDriver()
	firstDriver = d1
	tx, unchanged, err := d1.Prepare(context.Background(), modeApply, ApplyOptions{Prepared: prepared, TrustFile: trustPath, Version: "v1.2.3"})
	if err != nil || unchanged != nil || tx == nil {
		_ = d1.Close()
		t.Fatalf("Prepare tx=%+v unchanged=%+v err=%v", tx, unchanged, err)
	}
	if err := d1.Record(context.Background(), nil, *tx); err != nil {
		t.Fatal(err)
	}
	if err := d1.Close(); err != nil {
		t.Fatal(err)
	}
	d2 := newDriver()
	defer d2.Close()
	loaded, err := d2.Load(context.Background())
	if err != nil || loaded == nil || loaded.ID != tx.ID || loaded.IntentSHA256 != tx.IntentSHA256 {
		t.Fatalf("Load tx=%+v err=%v", loaded, err)
	}
	if err := d2.Quiesce(context.Background(), *loaded); err != nil {
		t.Fatal(err)
	}
	if !reopened || !d2.session.(*nativeFlowSession).locked {
		t.Fatal("Quiesce did not reopen authenticated inputs before holding the stopped-manager lock")
	}
	evidence, _, _, evidenceErr := d2.readEvidence(context.Background())
	if evidenceErr != nil || !evidence.UpdaterReconciled || d2.session.(*nativeFlowSession).reconciles != 1 {
		t.Fatalf("Quiesce did not durably record updater reconciliation: evidence=%+v reconciles=%d err=%v", evidence, d2.session.(*nativeFlowSession).reconciles, evidenceErr)
	}
	control, err := d2.openControl(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := control.ReadFile("maintenance-challenge.json")
	_ = control.Close()
	if err != nil || len(challenge) == 0 {
		t.Fatalf("fresh apply did not publish its challenge before start: %v", err)
	}
	if err := d2.Replace(context.Background(), *loaded); err != nil {
		t.Fatal(err)
	}
	// The independent worker starts with no asset snapshots. Replacement must
	// bind every role, including absent optional trust whose parent was created
	// during this operation, so publication can validate the entire inventory.
	for _, plan := range d2.intent.Assets {
		current, err := currentLinuxAsset(context.Background(), d2.assets[plan.Role])
		if err != nil || !matchesPlannedLinuxAsset(current, plan.After) {
			t.Fatalf("publication snapshot for %s: state=%+v err=%v", plan.Role, current, err)
		}
	}
	// A persisted marker means updater reconciliation completed before native
	// publication. Rollback must not take a fresh updater snapshot against the
	// candidate manager image with the prior receipt still present.
	beforeSnapshots := d2.session.(*nativeFlowSession).snapshots
	if err := d2.recoverUpdaterBeforeNativeRollback(context.Background(), *d2.intent, principal); err != nil {
		t.Fatalf("candidate image/prior receipt recovery: %v", err)
	}
	if d2.session.(*nativeFlowSession).snapshots != beforeSnapshots {
		t.Fatal("phase-replacing rollback attempted an invalid fresh updater snapshot")
	}
	for _, spec := range fixedLinuxAssets {
		if spec.Member == "" {
			continue
		}
		actual, err := inspectLinuxAsset(context.Background(), spec.Role)
		if err != nil {
			t.Fatal(err)
		}
		state := actual.State()
		_ = actual.Close()
		var member candidate.Member
		for _, value := range members {
			if value.Name == spec.Member {
				member = value
				break
			}
		}
		if !state.Present || state.SHA256 != member.SHA256 || state.Size != member.Size || state.Mode != member.Mode {
			t.Fatalf("published %s state=%+v member=%+v", spec.Role, state, member)
		}
	}
	if !bytes.Equal(trustBytes, d2.intent.BootstrapTrust) {
		t.Fatal("immutable intent lost the independent bootstrap trust bytes")
	}
}

func TestCheckAcceptsOnlyTerminalManagerAssetSupersession(t *testing.T) {
	inventory := linuxInventoryFixture("installed")
	receipt := selfupdate.Installed{Format: 1, Version: "v1.2.4", Schema: inventory.Image.Schema, SHA256: strings.Repeat("c", 64)}
	assets := make(map[linuxAssetRole]*linuxAssetSnapshot, len(fixedLinuxAssets))
	for i, spec := range fixedLinuxAssets {
		state := inventory.Assets[i].State
		if spec.Role == linuxAssetManager {
			state.SHA256 = receipt.SHA256
			state.Size = 4096
			state.Inode++
		}
		assets[spec.Role] = &linuxAssetSnapshot{role: spec.Role, state: state}
	}
	d := &linuxInstallDriver{assets: assets}
	if !d.assetsMatchTerminalUpdaterSupersession(inventory, receipt) {
		t.Fatal("valid terminal manager-only supersession was not recognized")
	}
	changedHelper := *assets[linuxAssetUpdater]
	changedHelper.state.Inode++
	assets[linuxAssetUpdater] = &changedHelper
	if d.assetsMatchTerminalUpdaterSupersession(inventory, receipt) {
		t.Fatal("terminal manager supersession accepted a changed updater helper")
	}
	assets[linuxAssetUpdater] = &linuxAssetSnapshot{role: linuxAssetUpdater, state: inventory.Assets[1].State}
	if d.assetsMatchTerminalUpdaterSupersession(inventory, selfupdate.Installed{Format: 1, Version: receipt.Version, Schema: receipt.Schema + 1, SHA256: receipt.SHA256}) {
		t.Fatal("terminal manager supersession accepted a schema change")
	}
}

func writeNativeTrust(t *testing.T, base string) ([]byte, string, candidate.Trust) {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(struct {
		Format     int               `json:"format"`
		Repository string            `json:"repository"`
		PublicKeys map[string]string `json:"public_keys"`
	}{1, "jonasthim/minecraft-server-ui", map[string]string{"fixture": base64.StdEncoding.EncodeToString(public)}})
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, '\n')
	path := filepath.Join(base, "bootstrap-trust.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	trust, err := candidate.ParsePublicTrust(body, "jonasthim/minecraft-server-ui")
	if err != nil {
		t.Fatal(err)
	}
	return body, path, trust
}

func writeNativeMembers(t *testing.T, base, prepared string) ([]candidate.Member, map[string]string) {
	t.Helper()
	paths := make(map[string]string)
	var members []candidate.Member
	for _, spec := range fixedLinuxAssets {
		if spec.Member == "" {
			continue
		}
		body := []byte("fixture authenticated member " + spec.Member + "\n")
		sum := sha256.Sum256(body)
		member := candidate.Member{Name: spec.Member, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:]), Mode: spec.Mode}
		members = append(members, member)
		path := filepath.Join(base, "members", spec.Member)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		paths[spec.Member] = path
		if err := os.WriteFile(filepath.Join(prepared, spec.Member), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// These extra authenticated members are allowed by the bundle but are never
	// installed as privileged assets.
	for _, name := range []string{"install.sh", "update-trust.example.json"} {
		body := []byte("fixture extra member " + name + "\n")
		sum := sha256.Sum256(body)
		members = append(members, candidate.Member{Name: name, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:]), Mode: 0644})
		path := filepath.Join(base, "members", name)
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		paths[name] = path
		if err := os.WriteFile(filepath.Join(prepared, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// OpenMember uses descriptors; leave the source file modes private.
	for _, path := range paths {
		if err := os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return members, paths
}
