package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

type fixtureDriver struct {
	failStop              bool
	fences                int
	failFence             int
	prior                 Installed
	candidate             Installed
	nonce                 string
	stops, starts, health int
	failHealth            bool
	authorized            bool
	drained               bool
	schema                int64
}

func (d *fixtureDriver) Fence(context.Context, *os.Root) error {
	d.fences++
	if d.failFence == d.fences {
		return errors.New("injected durable fence failure")
	}
	return nil
}
func (d *fixtureDriver) Stop(context.Context) error {
	d.stops++
	if d.failStop {
		return errors.New("stop failed after exit")
	}
	return nil
}
func (d *fixtureDriver) Start(context.Context) error { d.starts++; return nil }
func (d *fixtureDriver) ProbeSchema(_ context.Context, previous bool) (Capability, error) {
	authorized := d.authorized
	version := d.prior.Version
	schema := d.prior.Schema
	if d.schema != 0 {
		schema = d.schema
	}
	return Capability{Format: 1, Version: version, Schema: schema, ActorAuthorized: &authorized}, nil
}
func (d *fixtureDriver) WaitHealthy(_ context.Context, i Installed, _ string) error {
	d.health++
	if d.failHealth && i.Version == d.candidate.Version {
		return errors.New("candidate not ready")
	}
	return nil
}
func (d *fixtureDriver) WriteChallenge(_ context.Context, c Challenge) error {
	d.nonce = c.Nonce
	return nil
}
func (d *fixtureDriver) ReadDrain(context.Context) (DrainReceipt, error) {
	return DrainReceipt{Format: 1, Nonce: d.nonce, Version: d.prior.Version, Schema: d.prior.Schema, ExecutableSHA256: d.prior.SHA256, Drained: d.drained, CreatedAt: time.Now()}, nil
}
func (d *fixtureDriver) ClearControl(context.Context) error          { return nil }
func (d *fixtureDriver) LockManager(context.Context) (func(), error) { return func() {}, nil }
func nativeFixture(t *testing.T) (*activationWorker, *os.Root, activationJournal, *fixtureDriver) {
	t.Helper()
	state, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	bin, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bin.Close() })
	candidate, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { candidate.Close() })
	prior := Installed{Format: 1, Version: "v1.2.2", Schema: 16, SHA256: digest([]byte("old"))}
	next := Installed{Format: 1, Version: "v1.2.3", Schema: 16, SHA256: digest([]byte("new"))}
	for _, entry := range []struct {
		root *os.Root
		body string
	}{{bin, "old"}, {candidate, "new"}} {
		if err := writeFile(context.Background(), entry.root, "minecraft-ui", bytesReader([]byte(entry.body)), 3, ""); err != nil {
			t.Fatal(err)
		}
	}
	d := &fixtureDriver{prior: prior, candidate: next, authorized: true, drained: true}
	w := &activationWorker{state: state, bin: bin, binary: "minecraft-ui", driver: d}
	j := activationJournal{Request: ActivationRequest{StageID: digest([]byte("stage")), ActorID: 1, RequestedAt: time.Now()}, Nonce: digest([]byte("nonce")), Prior: prior, Candidate: next}
	return w, candidate, j, d
}
func TestNativeActivationAndHealthFailureRestoreOnlyExecutable(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy", true: "failed_health"}[fail], func(t *testing.T) {
			w, c, j, d := nativeFixture(t)
			d.failHealth = fail
			if err := w.activate(context.Background(), j, c); err != nil {
				t.Fatal(err)
			}
			body, err := readChecked(context.Background(), w.bin, "minecraft-ui", 64)
			if err != nil {
				t.Fatal(err)
			}
			want := "new"
			phase := "healthy"
			if fail {
				want = "old"
				phase = "rolled_back"
			}
			if string(body) != want {
				t.Fatal(string(body), want)
			}
			stored, err := w.load(context.Background())
			if err != nil || stored.Phase != phase {
				t.Fatal(stored, err)
			}
			if fail && d.health != 2 {
				t.Fatal("old executable readiness not checked", d.health)
			}
			if fail && d.starts != 2 {
				t.Fatal("rollback not bounded", d.starts)
			}
		})
	}
}
func TestNativeRefusesRevokedActorUnsafeDrainAndSchema(t *testing.T) {
	for _, mode := range []string{"revoked", "drain", "schema"} {
		t.Run(mode, func(t *testing.T) {
			w, c, j, d := nativeFixture(t)
			switch mode {
			case "revoked":
				d.authorized = false
			case "drain":
				d.drained = false
			case "schema":
				d.schema = 17
			}
			if err := w.activate(context.Background(), j, c); err == nil {
				t.Fatal("unsafe update activated")
			}
			got, _ := readChecked(context.Background(), w.bin, "minecraft-ui", 64)
			if string(got) != "old" || d.starts != 1 {
				t.Fatal(string(got), d.starts)
			}
		})
	}
}
func TestNativeInterruptedReplacementRecoversOldExecutableAndRefusesSecondRollback(t *testing.T) {
	w, _, j, d := nativeFixture(t)
	ctx := context.Background()
	if err := copyVerified(ctx, w.bin, "minecraft-ui", w.bin, "minecraft-ui.previous", j.Prior); err != nil {
		t.Fatal(err)
	}
	if err := w.bin.Remove("minecraft-ui"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(ctx, w.bin, "minecraft-ui", bytesReader([]byte("new")), 3, ""); err != nil {
		t.Fatal(err)
	}
	j.Format = 1
	j.Phase = "replacing"
	if err := w.save(ctx, j); err != nil {
		t.Fatal(err)
	}
	if err := w.recover(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := readChecked(ctx, w.bin, "minecraft-ui", 64)
	if string(got) != "old" {
		t.Fatal(string(got))
	}
	if d.starts != 0 {
		t.Fatal("boot recovery tried starting manager before recovery unit completed", d.starts)
	}
	stored, err := w.load(ctx)
	if err != nil || !stored.RollbackAttempted {
		t.Fatal(stored, err)
	}
	before := d.starts
	if err := w.rollback(ctx, &stored); err == nil || d.starts != before {
		t.Fatal("repeated rollback permitted", err)
	}
}
func TestNativeFailureAfterStopRestartsUnchangedManager(t *testing.T) {
	w, c, j, d := nativeFixture(t)
	ctx := context.Background()
	if err := writeFile(ctx, w.bin, "minecraft-ui.next", bytesReader([]byte("foreign")), 7, ""); err != nil {
		t.Fatal(err)
	}
	if err := w.activate(ctx, j, c); err == nil {
		t.Fatal("unsafe scratch accepted")
	}
	if d.starts != 1 {
		t.Fatal("unchanged manager not restarted after preparation failure", d.starts)
	}
	got, _ := readChecked(ctx, w.bin, "minecraft-ui", 64)
	if string(got) != "old" {
		t.Fatal(string(got))
	}
}
func TestNativeDurabilityFenceFailuresBlockPublicationOrRestorePrior(t *testing.T) {
	for _, failure := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			w, c, j, d := nativeFixture(t)
			d.failFence = failure
			err := w.activate(context.Background(), j, c)
			if err == nil {
				t.Fatal("injected fence failure was ignored", failure, d.fences)
			}
			got, e := readChecked(context.Background(), w.bin, "minecraft-ui", 64)
			if e != nil || string(got) != "old" {
				t.Fatal("failed fence did not preserve/restore prior executable", failure, string(got), e)
			}
			if d.starts > 2 {
				t.Fatal("unbounded recovery restart", d.starts)
			}
		})
	}
}

func TestNativeStopFailureRestartsCapturedUnchangedManager(t *testing.T) {
	w, c, j, d := nativeFixture(t)
	d.failStop = true
	if err := w.activate(context.Background(), j, c); err == nil {
		t.Fatal("stop failure ignored")
	}
	if d.starts != 1 {
		t.Fatal("old manager left stopped", d.starts)
	}
}
