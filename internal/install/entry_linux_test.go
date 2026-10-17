//go:build linux

package install

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type entryTestDriver struct {
	driver
	events      *[]string
	tx          *transaction
	loadTx      *transaction
	unchanged   *Result
	recordErr   error
	closeErr    error
	verifyCount int
}

func (d *entryTestDriver) Load(context.Context) (*transaction, error) {
	*d.events = append(*d.events, "load")
	return cloneTransaction(d.loadTx), nil
}

func (d *entryTestDriver) Prepare(_ context.Context, _ mode, _ ApplyOptions) (*transaction, *Result, error) {
	*d.events = append(*d.events, "prepare")
	return cloneTransaction(d.tx), cloneResultPtr(d.unchanged), nil
}

func (d *entryTestDriver) Record(_ context.Context, _ *transaction, next transaction) error {
	*d.events = append(*d.events, "record:"+string(next.Phase))
	return d.recordErr
}

func (d *entryTestDriver) StageExecutor(_ context.Context, tx transaction) (string, error) {
	*d.events = append(*d.events, "stage")
	if tx.ID == "" || d.tx != nil && tx.ID != d.tx.ID {
		return "", errors.New("wrong transaction")
	}
	return filepath.Join("/fixture/state", installDirectory, tx.ID, linuxExecutorName), nil
}

func (d *entryTestDriver) VerifyExecutor(context.Context, transaction) error {
	d.verifyCount++
	return nil
}

func (d *entryTestDriver) Close() error {
	*d.events = append(*d.events, "close")
	return d.closeErr
}

type entryPendingRecoveryDriver struct {
	*entryTestDriver
	recoveryErr error
}

func (d *entryPendingRecoveryDriver) RecoverPending(context.Context) error {
	*d.events = append(*d.events, "recover-pending")
	return d.recoveryErr
}

func TestLinuxInstallerPendingRecoveryRequiresExplicitRecover(t *testing.T) {
	for _, test := range []struct {
		name    string
		command installerCommandKind
		failure bool
		want    []string
	}{
		{"recover", installerRecover, false, []string{"recover-pending", "load", "close"}},
		{"refuse-ambiguous", installerRecover, true, []string{"recover-pending", "close"}},
		{"apply-does-not-recover", installerApply, true, []string{"load", "prepare", "close"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := []string{}
			base := &entryTestDriver{events: &events, unchanged: &Result{Outcome: OutcomeUnchanged, Image: testEntryTransaction().Target}}
			d := &entryPendingRecoveryDriver{entryTestDriver: base}
			if test.failure {
				d.recoveryErr = errors.New("foreign pending state")
			}
			hooks := linuxInstallerEntryHooks{newDriver: func(context.Context) (linuxEntryDriver, error) { return d, nil }}
			ctx := withLinuxInstallerEntryHooks(context.Background(), hooks)
			_, err := runLinuxInstallerCommand(ctx, installerCommand{kind: test.command, options: ApplyOptions{Prepared: "/tmp/prepared", TrustFile: "/tmp/trust", Version: "v1.2.3"}})
			wantError := test.command == installerRecover && test.failure
			if (err != nil) != wantError || !reflect.DeepEqual(events, test.want) {
				t.Fatalf("events=%v err=%v; want events=%v error=%v", events, err, test.want, wantError)
			}
		})
	}
}

func testEntryTransaction() *transaction {
	image := &Image{Version: "v1.2.3", Schema: 7, SHA256: strings.Repeat("a", 64)}
	return &transaction{Format: 1, ID: strings.Repeat("b", 64), Mode: modeApply, Phase: phasePrepared, IntentSHA256: strings.Repeat("c", 64), Target: image}
}

func cloneResultPtr(result *Result) *Result {
	if result == nil {
		return nil
	}
	copy := cloneResult(*result)
	return &copy
}

func TestLinuxInstallerApplyPersistsThenReleasesBeforeDispatch(t *testing.T) {
	tx := testEntryTransaction()
	events := []string{}
	d := &entryTestDriver{events: &events, tx: tx}
	ctx := withLinuxInstallerEntryHooks(context.Background(), linuxInstallerEntryHooks{
		stateRoot: "/fixture/state",
		newDriver: func(context.Context) (linuxEntryDriver, error) { return d, nil },
		dispatch: func(_ context.Context, id, executor string, recover bool) error {
			events = append(events, "dispatch")
			if id != tx.ID || executor != filepath.Join("/fixture/state", installDirectory, tx.ID, linuxExecutorName) {
				t.Fatalf("id=%q executor=%q", id, executor)
			}
			if recover {
				t.Fatal("apply dispatched the recovery worker")
			}
			return nil
		},
		read: func(context.Context, string) (Result, bool, error) {
			events = append(events, "read-result")
			return Result{Outcome: OutcomeInstalled, OperationID: tx.ID, Image: cloneInstallerImage(tx.Target)}, true, nil
		},
	})
	result, err := linuxInstallerApply(ctx, linuxEntryHooksFrom(ctx), ApplyOptions{Prepared: "/tmp/prepared", TrustFile: "/tmp/trust", Version: "v1.2.3"})
	if err != nil || result.Outcome != OutcomeInstalled {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	want := []string{"load", "prepare", "stage", "record:prepared", "close", "dispatch", "read-result"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events=%v want=%v", events, want)
	}
}

func TestLinuxInstallerApplyRecordUncertaintyDoesNotDispatch(t *testing.T) {
	tx := testEntryTransaction()
	events := []string{}
	d := &entryTestDriver{events: &events, tx: tx, recordErr: errors.New("publication uncertain")}
	dispatched := false
	ctx := withLinuxInstallerEntryHooks(context.Background(), linuxInstallerEntryHooks{
		stateRoot: "/fixture/state",
		newDriver: func(context.Context) (linuxEntryDriver, error) { return d, nil },
		dispatch:  func(context.Context, string, string, bool) error { dispatched = true; return nil },
		read: func(context.Context, string) (Result, bool, error) {
			t.Fatal("unexpected result read")
			return Result{}, false, nil
		},
	})
	result, err := linuxInstallerApply(ctx, linuxEntryHooksFrom(ctx), ApplyOptions{Prepared: "/tmp/prepared", TrustFile: "/tmp/trust", Version: "v1.2.3"})
	if err == nil || result.Outcome != OutcomeManualRepair || dispatched {
		t.Fatalf("result=%+v err=%v dispatched=%v", result, err, dispatched)
	}
	if events[len(events)-1] != "close" {
		t.Fatalf("driver did not close: %v", events)
	}
}

func TestLinuxInstallerApplyUnchangedDoesNotDispatch(t *testing.T) {
	events := []string{}
	unchanged := Result{Outcome: OutcomeUnchanged, Image: &Image{Version: "v1.2.3", Schema: 7, SHA256: strings.Repeat("a", 64)}}
	d := &entryTestDriver{events: &events, unchanged: &unchanged}
	dispatched := false
	ctx := withLinuxInstallerEntryHooks(context.Background(), linuxInstallerEntryHooks{
		newDriver: func(context.Context) (linuxEntryDriver, error) { return d, nil },
		dispatch:  func(context.Context, string, string, bool) error { dispatched = true; return nil },
	})
	result, err := linuxInstallerApply(ctx, linuxEntryHooksFrom(ctx), ApplyOptions{Prepared: "/tmp/prepared", TrustFile: "/tmp/trust", Version: "v1.2.3"})
	if err != nil || result.Outcome != OutcomeUnchanged || dispatched {
		t.Fatalf("result=%+v err=%v dispatched=%v", result, err, dispatched)
	}
	if !reflect.DeepEqual(events, []string{"load", "prepare", "close"}) {
		t.Fatalf("unexpected unchanged operation events: %v", events)
	}
}

func TestLinuxInstallerApplyCancellationReturnsPendingWithoutRollback(t *testing.T) {
	tx := testEntryTransaction()
	events := []string{}
	d := &entryTestDriver{events: &events, tx: tx}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = withLinuxInstallerEntryHooks(ctx, linuxInstallerEntryHooks{
		stateRoot: "/fixture/state",
		newDriver: func(context.Context) (linuxEntryDriver, error) { return d, nil },
		dispatch: func(ctx context.Context, _, _ string, recover bool) error {
			if recover {
				t.Fatal("apply dispatched recovery mode")
			}
			cancel()
			return ctx.Err()
		},
		read: func(context.Context, string) (Result, bool, error) {
			t.Fatal("cancelled dispatch must remain pending")
			return Result{}, false, nil
		},
	})
	result, err := linuxInstallerApply(ctx, linuxEntryHooksFrom(ctx), ApplyOptions{Prepared: "/tmp/prepared", TrustFile: "/tmp/trust", Version: "v1.2.3"})
	if err == nil || result.Outcome != OutcomePending || result.OperationID != tx.ID {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, event := range events {
		if strings.Contains(event, "rollback") {
			t.Fatalf("parent attempted rollback after handoff: %v", events)
		}
	}
}

func TestLinuxInstallerApplyAmbiguousDispatchPreservesPendingIdentity(t *testing.T) {
	tx := testEntryTransaction()
	events := []string{}
	d := &entryTestDriver{events: &events, tx: tx}
	ctx := withLinuxInstallerEntryHooks(context.Background(), linuxInstallerEntryHooks{
		stateRoot: "/fixture/state",
		newDriver: func(context.Context) (linuxEntryDriver, error) { return d, nil },
		dispatch:  func(context.Context, string, string, bool) error { return errors.New("ambiguous systemd status") },
		read:      func(context.Context, string) (Result, bool, error) { return Result{}, false, nil },
	})
	result, err := linuxInstallerApply(ctx, linuxEntryHooksFrom(ctx), ApplyOptions{Prepared: "/tmp/prepared", TrustFile: "/tmp/trust", Version: "v1.2.3"})
	if err == nil || result.Outcome != OutcomePending || result.OperationID != tx.ID {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestLinuxInstallerDispatchFailurePreservesPublishedTerminalResult(t *testing.T) {
	tx := testEntryTransaction()
	result := Result{Outcome: OutcomeInstalled, OperationID: tx.ID, Image: cloneInstallerImage(tx.Target)}
	ctx := withLinuxInstallerEntryHooks(context.Background(), linuxInstallerEntryHooks{
		dispatch: func(context.Context, string, string, bool) error { return errors.New("systemd-run status unavailable") },
		read:     func(context.Context, string) (Result, bool, error) { return result, true, nil },
	})
	got, err := dispatchInstallerWorker(ctx, linuxEntryHooksFrom(ctx), *tx, "/fixture/executor", false)
	if err == nil || got.Outcome != OutcomeInstalled || got.OperationID != tx.ID || got.FailedStage != "worker" {
		t.Fatalf("result=%+v err=%v", got, err)
	}
}

func TestLinuxInstallerWorkerBindsIDAndPreparedPhaseBeforeContinue(t *testing.T) {
	tx := testEntryTransaction()
	tx.Phase = phaseQuiescing
	events := []string{}
	d := &entryTestDriver{events: &events, loadTx: tx}
	ctx := withLinuxInstallerEntryHooks(context.Background(), linuxInstallerEntryHooks{
		newDriver: func(context.Context) (linuxEntryDriver, error) { return d, nil },
	})
	wrongID := installerCommand{kind: installerWorker, id: strings.Repeat("f", 64)}
	if result, err := linuxInstallerWorker(ctx, linuxEntryHooksFrom(ctx), wrongID); err == nil || result.OperationID != wrongID.id || d.verifyCount != 0 {
		t.Fatalf("wrong ID result=%+v err=%v verify=%d", result, err, d.verifyCount)
	}
	d.loadTx = tx
	command := installerCommand{kind: installerWorker, id: tx.ID}
	if result, err := linuxInstallerWorker(ctx, linuxEntryHooksFrom(ctx), command); err == nil || result.FailedStage != "recover" || d.verifyCount != 1 {
		t.Fatalf("wrong phase result=%+v err=%v verify=%d", result, err, d.verifyCount)
	}
}

func TestLinuxInstallerRecoveryDispatchesRecoveryWorker(t *testing.T) {
	tx := testEntryTransaction()
	tx.Phase = phaseQuiescing
	events := []string{}
	d := &entryTestDriver{events: &events, loadTx: tx, tx: tx}
	dispatchedRecovery := false
	ctx := withLinuxInstallerEntryHooks(context.Background(), linuxInstallerEntryHooks{
		stateRoot: "/fixture/state",
		newDriver: func(context.Context) (linuxEntryDriver, error) { return d, nil },
		dispatch: func(_ context.Context, id, executor string, recover bool) error {
			if id != tx.ID || executor != filepath.Join("/fixture/state", installDirectory, tx.ID, linuxExecutorName) {
				t.Fatalf("id=%q executor=%q", id, executor)
			}
			dispatchedRecovery = recover
			return nil
		},
		read: func(context.Context, string) (Result, bool, error) {
			return Result{Outcome: OutcomeRolledBack, OperationID: tx.ID}, true, nil
		},
	})
	value, err := runLinuxInstallerCommand(ctx, installerCommand{kind: installerRecover})
	result, ok := value.(Result)
	if err != nil || !ok || result.Outcome != OutcomeRolledBack || !dispatchedRecovery {
		t.Fatalf("result=%+v err=%v recover-dispatch=%v", result, err, dispatchedRecovery)
	}
	if !reflect.DeepEqual(events, []string{"load", "stage", "close"}) {
		t.Fatalf("unexpected recovery events: %v", events)
	}
}

func TestLinuxInstallerWorkerArgsAreFixedAndDoNotUseCallerEnvironment(t *testing.T) {
	id := strings.Repeat("d", 64)
	executor := filepath.Join(linuxInstallerState, installDirectory, id, linuxExecutorName)
	want := []string{
		"--quiet", "--wait", "--collect", "--unit=" + linuxInstallerUnitPrefix + id,
		"--service-type=exec", "--property=User=root", "--property=Group=root",
		"--property=UMask=0077", "--property=RuntimeMaxSec=15min", "--property=TimeoutStopSec=30s",
		"--", executor, "install", "worker", id,
	}
	if got := linuxInstallerWorkerArgs(id, executor, false); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv=%q want=%q", got, want)
	}
	path, gotArgs, env := linuxInstallerWorkerSpec(id, executor, false)
	if path != "/usr/bin/systemd-run" || !reflect.DeepEqual(gotArgs, want) || !reflect.DeepEqual(env, []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}) {
		t.Fatalf("worker spec path=%q args=%q env=%q", path, gotArgs, env)
	}
	if strings.Contains(strings.Join(gotArgs, " "), "SHELL") || strings.Contains(strings.Join(env, " "), "TOKEN") {
		t.Fatal("worker spec contains a caller-selected shell or secret environment")
	}
	recoveryWant := append([]string(nil), want...)
	recoveryWant[len(recoveryWant)-2] = "recover-worker"
	if got := linuxInstallerWorkerArgs(id, executor, true); !reflect.DeepEqual(got, recoveryWant) {
		t.Fatalf("recovery argv=%q want=%q", got, recoveryWant)
	}
	_, recoveryArgs, recoveryEnv := linuxInstallerWorkerSpec(id, executor, true)
	if !reflect.DeepEqual(recoveryArgs, recoveryWant) || !reflect.DeepEqual(recoveryEnv, env) {
		t.Fatalf("recovery spec args=%q env=%q", recoveryArgs, recoveryEnv)
	}
}

func TestInstallerResultValidationBindsIdentityAndKnownOutcome(t *testing.T) {
	id := strings.Repeat("e", 64)
	good := Result{Outcome: OutcomeInstalled, OperationID: id, Image: &Image{Version: "v1.2.3", Schema: 7, SHA256: strings.Repeat("a", 64)}}
	if !validInstallerResult(good, id) {
		t.Fatal("valid installed result rejected")
	}
	foreign := good
	foreign.OperationID = strings.Repeat("f", 64)
	if validInstallerResult(foreign, id) {
		t.Fatal("foreign operation result accepted")
	}
	unknown := good
	unknown.Outcome = "finished"
	if validInstallerResult(unknown, id) {
		t.Fatal("unknown outcome accepted")
	}
	unsafeStage := good
	unsafeStage.FailedStage = "raw /path/secret"
	if validInstallerResult(unsafeStage, id) {
		t.Fatal("unrecognized failed stage accepted")
	}
}

func TestLinuxInstallerResultReadRequiresOwnedBoundFile(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, installDirectory), 0700); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("9", 64)
	operation := filepath.Join(root, installDirectory, id)
	if err := os.Mkdir(operation, 0700); err != nil {
		t.Fatal(err)
	}
	result := Result{Outcome: OutcomeInstalled, OperationID: id, Image: &Image{Version: "v1.2.3", Schema: 7, SHA256: strings.Repeat("a", 64)}}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(operation, linuxResultName)
	if err := os.WriteFile(resultPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	got, present, err := readLinuxInstallerResultAt(context.Background(), root, id, uint32(os.Geteuid()))
	if err != nil || !present || got.Outcome != OutcomeInstalled || got.OperationID != id {
		t.Fatalf("result=%+v present=%v err=%v", got, present, err)
	}
	if err := os.Chmod(resultPath, 0640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readLinuxInstallerResultAt(context.Background(), root, id, uint32(os.Geteuid())); err == nil {
		t.Fatal("unsafe result mode accepted")
	}
	if err := os.Remove(resultPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultPath, make([]byte, maxInstallerResponseBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readLinuxInstallerResultAt(context.Background(), root, id, uint32(os.Geteuid())); err == nil {
		t.Fatal("oversized result accepted")
	}
}
