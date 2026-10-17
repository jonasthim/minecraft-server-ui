package selfupdate

import (
	"context"
	"errors"
	"testing"
	"time"
)

type diagnosticDriver struct {
	*fixtureDriver
	fault   string
	markers chan string
	cleanup chan struct{}
	resume  chan struct{}
}

func (d *diagnosticDriver) ActivationFailure(stage string) { d.markers <- stage }
func (d *diagnosticDriver) ReadDrain(ctx context.Context) (DrainReceipt, error) {
	if d.fault == "drain_read" {
		return DrainReceipt{}, errors.New("private path /private/nonce credential actor request")
	}
	return d.fixtureDriver.ReadDrain(ctx)
}
func (d *diagnosticDriver) ProbeSchema(ctx context.Context, previous bool) (Capability, error) {
	if d.fault == "schema_probe" {
		return Capability{}, errors.New("private path /private/nonce credential actor request")
	}
	return d.fixtureDriver.ProbeSchema(ctx, previous)
}
func (d *diagnosticDriver) Start(ctx context.Context) error {
	close(d.cleanup)
	<-d.resume
	return d.fixtureDriver.Start(ctx)
}

func TestActivationReportsFixedFailureBeforeBlockedCleanup(t *testing.T) {
	for _, stage := range []string{"manager_stop", "drain_read", "drain_validate", "schema_probe", "schema_validate", "stopped_journal", "scratch_check", "candidate_copy", "executable_fence", "candidate_validate"} {
		t.Run(stage, func(t *testing.T) {
			w, candidate, j, fixture := nativeFixture(t)
			driver := &diagnosticDriver{fixtureDriver: fixture, fault: stage, markers: make(chan string, 2), cleanup: make(chan struct{}), resume: make(chan struct{})}
			w.driver = driver
			switch stage {
			case "manager_stop":
				fixture.failStop = true
			case "drain_validate":
				fixture.drained = false
			case "schema_validate":
				fixture.authorized = false
			case "stopped_journal":
				fixture.failFence = 3
			case "scratch_check":
				if err := w.bin.Mkdir(w.binary+".previous", 0700); err != nil {
					t.Fatal(err)
				}
			case "candidate_copy":
				if err := candidate.Remove(w.binary); err != nil {
					t.Fatal(err)
				}
			case "executable_fence":
				fixture.failFence = 4
			case "candidate_validate":
				fixture.candidateCapability = &Capability{Format: 1, Version: "v9.9.9", Schema: j.Prior.Schema}
			}
			finished := make(chan error, 1)
			go func() { finished <- w.activate(context.Background(), j, candidate) }()
			// This is the actual cleanup Start callback, held until the marker is
			// inspected. Logging only after activate returns cannot satisfy this.
			select {
			case <-driver.cleanup:
			case err := <-finished:
				t.Fatal("activation returned before expected cleanup", err)
			case <-time.After(5 * time.Second):
				t.Fatal("activation did not reach expected cleanup")
			}
			var marker string
			select {
			case marker = <-driver.markers:
			default:
			}
			close(driver.resume)
			if err := <-finished; err == nil {
				t.Fatal("injected failure was ignored")
			}
			if marker != stage {
				t.Errorf("marker before cleanup = %q, want fixed stage %q", marker, stage)
			}
			select {
			case extra := <-driver.markers:
				t.Errorf("duplicate failure marker: %q", extra)
			default:
			}
			stored, err := w.load(context.Background())
			if err != nil || stored.Phase != "aborted" || stored.RollbackAttempted {
				t.Fatal("failure diagnostics changed abort behavior", stored, err)
			}
			hash, err := hashFile(context.Background(), w.bin, w.binary)
			if err != nil || hash != j.Prior.SHA256 {
				t.Fatal("prepublication failure changed executable", hash, err)
			}
		})
	}
}

func TestActivationSuccessDoesNotReportFailure(t *testing.T) {
	w, candidate, j, fixture := nativeFixture(t)
	driver := &diagnosticDriver{fixtureDriver: fixture, markers: make(chan string, 2), cleanup: make(chan struct{}), resume: make(chan struct{})}
	close(driver.resume)
	w.driver = driver
	if err := w.activate(context.Background(), j, candidate); err != nil {
		t.Fatal(err)
	}
	select {
	case marker := <-driver.markers:
		t.Fatal("successful activation reported failure", marker)
	default:
	}
}

func TestActivationDiagnosticsOmitUnknownStageAndOriginalCause(t *testing.T) {
	w, _, _, fixture := nativeFixture(t)
	driver := &diagnosticDriver{fixtureDriver: fixture, markers: make(chan string, 2)}
	w.driver = driver
	cause := errors.New("/private/path nonce actor request credential")
	if got := w.failActivation("schema_probe", cause); got != cause {
		t.Fatal("diagnostic replaced the original error")
	}
	if marker := <-driver.markers; marker != "schema_probe" {
		t.Fatal("diagnostic included private cause", marker)
	}
	if got := w.failActivation("/private/path nonce actor request credential", cause); got != cause {
		t.Fatal("unknown stage changed the error")
	}
	if got := w.failActivation("schema_probe", nil); got != nil {
		t.Fatal("diagnostic introduced an error")
	}
	select {
	case marker := <-driver.markers:
		t.Fatal("unknown or successful stage emitted a marker", marker)
	default:
	}
}
