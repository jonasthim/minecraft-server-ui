package selfupdate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
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
	cancelHealth          context.CancelFunc
	cancelDrain           context.CancelFunc
	candidateCapability   *Capability
}

func (d *fixtureDriver) Fence(ctx context.Context, _ *os.Root) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.fences++
	if d.failFence == d.fences {
		return errors.New("injected durable fence failure")
	}
	return nil
}
func (d *fixtureDriver) Stop(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.stops++
	if d.failStop {
		return errors.New("stop failed after exit")
	}
	return nil
}
func (d *fixtureDriver) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.starts++
	return nil
}
func (d *fixtureDriver) ProbeSchema(ctx context.Context, previous bool) (Capability, error) {
	if err := ctx.Err(); err != nil {
		return Capability{}, err
	}
	authorized := d.authorized
	version := d.prior.Version
	schema := d.prior.Schema
	if d.schema != 0 {
		schema = d.schema
	}
	return Capability{Format: 1, Version: version, Schema: schema, ActorAuthorized: &authorized}, nil
}
func (d *fixtureDriver) WaitHealthy(ctx context.Context, i Installed, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.health++
	if d.cancelHealth != nil && i.Version == d.candidate.Version {
		d.cancelHealth()
		return context.Canceled
	}
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
	if d.cancelDrain != nil {
		d.cancelDrain()
		return DrainReceipt{}, context.Canceled
	}
	return DrainReceipt{Format: 1, Nonce: d.nonce, Version: d.prior.Version, Schema: d.prior.Schema, ExecutableSHA256: d.prior.SHA256, Drained: d.drained, CreatedAt: time.Now()}, nil
}
func (d *fixtureDriver) ClearControl(context.Context) error { return nil }
func (d *fixtureDriver) LockManager(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return func() {}, nil
}
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

func TestCanceledActivationRestoresAndRestartsPrior(t *testing.T) {
	for _, point := range []string{"health", "drain"} {
		t.Run(point, func(t *testing.T) {
			w, c, j, d := nativeFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if point == "health" {
				d.cancelHealth = cancel
			} else {
				d.cancelDrain = cancel
			}
			_ = w.activate(ctx, j, c)
			body, err := readChecked(context.Background(), w.bin, w.binary, 64)
			stored, loadErr := w.load(context.Background())
			wantStarts := 1
			wantPhase := "aborted"
			if point == "health" {
				wantStarts = 2
				wantPhase = "rolled_back"
			}
			if err != nil || string(body) != "old" || loadErr != nil || stored.Phase != wantPhase || d.starts != wantStarts {
				t.Fatalf("binary=%q read=%v journal=%+v load=%v starts=%d want=%d", body, err, stored, loadErr, d.starts, wantStarts)
			}
		})
	}
}

func (d *fixtureDriver) ProbeCandidate(ctx context.Context, expected Installed) (Capability, error) {
	if err := ctx.Err(); err != nil {
		return Capability{}, err
	}
	if d.candidateCapability != nil {
		return *d.candidateCapability, nil
	}
	return Capability{Format: 1, Version: expected.Version, Schema: expected.Schema}, nil
}
func TestCandidateEmbeddedCapabilityMustMatchBeforeReplacement(t *testing.T) {
	for _, mismatch := range []string{"schema", "version"} {
		t.Run(mismatch, func(t *testing.T) {
			w, c, j, d := nativeFixture(t)
			cap := Capability{Format: 1, Version: j.Candidate.Version, Schema: j.Candidate.Schema}
			if mismatch == "schema" {
				cap.Schema++
			} else {
				cap.Version = "v1.2.4"
			}
			d.candidateCapability = &cap
			if err := w.activate(context.Background(), j, c); err == nil {
				t.Error("incompatible candidate accepted")
			}
			body, err := readChecked(context.Background(), w.bin, w.binary, 64)
			if err != nil || string(body) != "old" || d.starts != 1 || d.health != 0 {
				t.Fatalf("binary=%q err=%v starts=%d health=%d", body, err, d.starts, d.health)
			}
		})
	}
}

// A deterministic expiring context exercises DeadlineExceeded at the exact
// fixture boundary without timing races or sleeps.
type fixtureDeadline struct {
	context.Context
	expired atomic.Bool
}

func (c *fixtureDeadline) Err() error {
	if c.expired.Load() {
		return context.DeadlineExceeded
	}
	return c.Context.Err()
}
func TestActivationDeadlineCleanupRestoresPriorExactlyOnce(t *testing.T) {
	for _, point := range []string{"health", "drain"} {
		t.Run(point, func(t *testing.T) {
			w, c, j, d := nativeFixture(t)
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &fixtureDeadline{Context: base}
			expire := func() { ctx.expired.Store(true); cancel() }
			if point == "health" {
				d.cancelHealth = expire
			} else {
				d.cancelDrain = expire
			}
			_ = w.activate(ctx, j, c)
			h, err := hashFile(context.Background(), w.bin, w.binary)
			stored, e := w.load(context.Background())
			want := 1
			phase := "aborted"
			if point == "health" {
				want = 2
				phase = "rolled_back"
			}
			if err != nil || h != j.Prior.SHA256 || e != nil || stored.Phase != phase || d.starts != want {
				t.Fatal(h, err, stored, e, d.starts)
			}
		})
	}
}
func TestFailedRollbackIntentSaveDoesNotClaimDurableAttempt(t *testing.T) {
	w, _, j, d := nativeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.restorePrior(ctx, &j, true); err == nil {
		t.Fatal("canceled intent write succeeded")
	}
	if j.RollbackAttempted || j.Phase == "rollback" || d.starts != 0 {
		t.Fatal("unsaved intent claimed cleanup ownership", j, d.starts)
	}
}
func TestSignedUnchangedSchemaCandidateWithHigherEmbeddedSchemaLeavesDBAndBinaryUntouched(t *testing.T) {
	w, c, j, d := nativeFixture(t)
	ctx := context.Background()
	archive := archiveFixture(t, "linux", []string{"minecraft-ui", "LICENSE", "THIRD_PARTY_NOTICES.txt"})
	trust, b, sig := stageFixture(t, "linux", archive)
	manifest, err := VerifyManifest(trust, b, sig, "v1.2.3", j.Prior.Version, j.Prior.Schema, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := manifest.Artifact("linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	j.Candidate = Installed{Format: 1, Version: manifest.Version, Schema: manifest.SchemaAfter, SHA256: artifact.BinarySHA256}
	d.candidate = j.Candidate
	if err := c.Remove(w.binary); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(ctx, c, w.binary, bytesReader([]byte("binary")), 6, artifact.BinarySHA256); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manager.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE goose_db_version(id INTEGER PRIMARY KEY,version_id INTEGER,is_applied INTEGER);INSERT INTO goose_db_version VALUES(1,16,1)`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	d.candidateCapability = &Capability{Format: 1, Version: manifest.Version, Schema: manifest.SchemaAfter + 1}
	if err := w.activate(ctx, j, c); err == nil {
		t.Fatal("signed packaging mismatch was accepted")
	}
	schema, err := ReadOnlySchema(ctx, path)
	after, e := os.ReadFile(path)
	h, hashErr := hashFile(ctx, w.bin, w.binary)
	if err != nil || e != nil || hashErr != nil || schema != 16 || digest(before) != digest(after) || h != j.Prior.SHA256 || d.starts != 1 || d.health != 0 {
		t.Fatal("candidate reached normal serving or changed prior state", schema, err, e, h, hashErr, d.starts, d.health)
	}
}
