package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
)

func TestNewRejectsRelativePaths(t *testing.T) {
	if _, err := New(Config{DataDir: "relative"}); err == nil {
		t.Fatal("relative data root accepted")
	}
}
func TestCancelledStartNeverWritesSpec(t *testing.T) {
	root := t.TempDir()
	s, err := New(Config{DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.Start(ctx, "test", domain.LaunchSpec{})
	if err == nil {
		t.Fatal("canceled start accepted")
	}
	if _, err = os.Stat(filepath.Join(root, "instances", "test", "state", "launch.json")); !os.IsNotExist(err) {
		t.Fatal("canceled start mutated disk", err)
	}
}
func TestInvalidIDNeverReachesUnitControl(t *testing.T) {
	s, err := New(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetAutostart(context.Background(), "../manager", true); err == nil {
		t.Fatal("unsafe id")
	}
}
func TestParseSystemdUnknownFailsClosed(t *testing.T) {
	if got := parseSystemd("ActiveState=surprising\nLoadState=loaded\n"); got.State != domain.StateUnknown {
		t.Fatal(got)
	}
	if got := parseSystemd("ActiveState=failed\nLoadState=loaded\nResult=timeout\n"); got.State != domain.StateCrashed || got.Result != "timeout" {
		t.Fatal(got)
	}
}

func TestShutdownRejectsNewStarts(t *testing.T) {
	s, err := New(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = s.Start(context.Background(), "test", domain.LaunchSpec{})
	if err == nil || domain.AsError(err).Code != domain.CodeUnavailable {
		t.Fatal("start admitted after shutdown", err)
	}
}

func staleIdentity(t *testing.T) domain.ProcessIdentity {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := launcher.CaptureIdentity("test", os.Getpid(), domain.LaunchSpec{Executable: exe, WorkingDir: cwd})
	if err != nil {
		t.Fatal(err)
	}
	identity.StartIdentity += "previous"
	return identity
}
func TestAlreadyDeadStopWaitsForLauncherCompletion(t *testing.T) {
	root := t.TempDir()
	s, err := New(Config{DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	identity := staleIdentity(t)
	release, err := launcher.AcquireAdmission(root, identity.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err = s.Stop(ctx, identity); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dead JVM bypassed active launcher completion: %v", err)
	}
	if got := StopFailureCategory(err); got != StopFailureAdmissionWaitTimeout {
		t.Fatalf("category = %q, want admission wait timeout", got)
	}
}

func TestWaitExitClassifiesLiveProcessTimeout(t *testing.T) {
	root := t.TempDir()
	p, err := launcher.Paths(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(p.Server, 0700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestSupervisorStopChild$")
	cmd.Dir = p.Server
	cmd.Env = append(os.Environ(), "MINECRAFT_TEST_SUPERVISOR_CHILD=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	identity, err := launcher.CaptureIdentity("test", cmd.Process.Pid, domain.LaunchSpec{Executable: exe, WorkingDir: p.Server})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	err = s.waitExit(ctx, identity)
	cancel()
	_ = stdin.Close()
	if !errors.Is(err, context.DeadlineExceeded) || StopFailureCategory(err) != StopFailureProcessWaitTimeout {
		t.Fatalf("waitExit err=%v category=%q", err, StopFailureCategory(err))
	}
	if err = cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	waited = true
}

func TestWaitExitClassifiesExpiredAndCancelledContextsBeforeProbe(t *testing.T) {
	s, err := New(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	id := staleIdentity(t)
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	err = s.waitExit(expired, id)
	if !errors.Is(err, context.DeadlineExceeded) || StopFailureCategory(err) != StopFailureProcessWaitTimeout {
		t.Fatalf("expired wait err=%v category=%q", err, StopFailureCategory(err))
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	err = s.waitExit(cancelled, id)
	if !errors.Is(err, context.Canceled) || StopFailureCategory(err) != StopFailureContextCanceled {
		t.Fatalf("cancelled wait err=%v category=%q", err, StopFailureCategory(err))
	}
}
func TestAlreadyDeadStopPreservesFinalFailure(t *testing.T) {
	for _, kind := range []string{"forced", "recovery", "missing", "crashed", "clean"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			s, err := New(Config{DataDir: root})
			if err != nil {
				t.Fatal(err)
			}
			identity := staleIdentity(t)
			p, err := launcher.Paths(root, identity.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(p.State, 0700); err != nil {
				t.Fatal(err)
			}
			final := launcher.Exit{Identity: identity, Deliberate: true, ExitedAt: time.Now().UTC()}
			switch kind {
			case "forced":
				final.Forced = true
			case "recovery":
				final.WatchdogError = "save-on failed"
			case "crashed":
				final.ExitCode = 2
				final.Deliberate = false
			}
			if kind != "missing" {
				if err = launcher.WriteJSON(filepath.Join(p.State, "exit.json"), final); err != nil {
					t.Fatal(err)
				}
			}
			err = s.Stop(context.Background(), identity)
			wantCategory := StopFailureUnknown
			switch kind {
			case "forced":
				wantCategory = StopFailureForcedTermination
			case "recovery":
				wantCategory = StopFailureWatchdogFailure
			case "missing":
				wantCategory = StopFailureMissingFinalMetadata
			case "crashed":
				wantCategory = StopFailureUncleanExit
			}
			if kind == "clean" && err != nil {
				t.Fatal(err)
			}
			if kind != "clean" && err == nil {
				t.Fatalf("already dead Stop hid %s failure", kind)
			}
			if kind != "clean" {
				if got := StopFailureCategory(err); got != wantCategory {
					t.Fatalf("category = %q, want %q (err %v)", got, wantCategory, err)
				}
			}
		})
	}
}

func TestStopFailureCategoryIsFiniteAndPreservesCause(t *testing.T) {
	want := StopFailureProcessWaitTimeout
	err := &stopFailure{category: want, err: fmt.Errorf("private detail: %w", context.DeadlineExceeded)}
	if got := StopFailureCategory(err); got != want {
		t.Fatalf("category = %q, want %q", got, want)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("typed failure did not preserve wrapped cause")
	}
	if got := StopFailureCategory(errors.New("password=private")); got != StopFailureUnknown {
		t.Fatalf("unknown error mapped to %q", got)
	}
	if got := StopFailureCategory(&stopFailure{category: StopFailureCode("raw private text"), err: errors.New("x")}); got != StopFailureUnknown {
		t.Fatalf("malformed category mapped to %q", got)
	}
	var nilFailure *stopFailure
	if got := StopFailureCategory(nilFailure); got != StopFailureUnknown {
		t.Fatalf("typed nil category mapped to %q", got)
	}
}

func TestStopInitialProbeFailureIsClassified(t *testing.T) {
	s, err := New(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := launcher.CaptureIdentity("test", os.Getpid(), domain.LaunchSpec{Executable: exe, WorkingDir: cwd})
	if err != nil {
		t.Fatal(err)
	}
	identity.WorkingDir = filepath.Dir(cwd)
	err = s.Stop(context.Background(), identity)
	if err == nil || StopFailureCategory(err) != StopFailureInitialProbe {
		t.Fatalf("Stop err=%v category=%q", err, StopFailureCategory(err))
	}
}

func TestStopDeliveryFailureNeedsMatchingCleanFinalMetadata(t *testing.T) {
	for _, kind := range []string{"clean", "missing", "forced", "wrong-identity"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			s, err := New(Config{DataDir: root, StopTimeout: 10 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			identity := staleIdentity(t)
			p, err := launcher.Paths(root, identity.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(p.State, 0700); err != nil {
				t.Fatal(err)
			}
			if kind != "missing" {
				final := launcher.Exit{Identity: identity, Deliberate: true, ExitedAt: time.Now().UTC()}
				if kind == "forced" {
					final.Forced = true
				}
				if kind == "wrong-identity" {
					final.Identity.StartIdentity += "other"
				}
				if err = launcher.WriteJSON(filepath.Join(p.State, "exit.json"), final); err != nil {
					t.Fatal(err)
				}
			}
			deliveryErr := errors.New("control connection failed")
			err = s.reconcileStopDeliveryFailure(context.Background(), identity, deliveryErr)
			if kind == "clean" && err != nil {
				t.Fatal(err)
			}
			if kind != "clean" && err == nil {
				t.Fatal("failed or mismatched completion was accepted")
			}
			if kind != "clean" {
				want := StopFailureControlLostMetadata
				if kind == "forced" {
					want = StopFailureControlLostForced
				}
				if got := StopFailureCategory(err); got != want {
					t.Fatalf("category = %q, want %q", got, want)
				}
				if !errors.Is(err, deliveryErr) {
					t.Fatal("lost response diagnostic dropped the control error cause")
				}
			}
		})
	}
}

func TestSupervisorStopChild(t *testing.T) {
	if os.Getenv("MINECRAFT_TEST_SUPERVISOR_CHILD") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "stop" {
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func TestStopReconcilesLostAcknowledgementAfterAcceptedRequest(t *testing.T) {
	root := t.TempDir()
	p, err := launcher.Paths(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(p.State, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(p.Server, 0700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestSupervisorStopChild$")
	cmd.Dir = p.Server
	cmd.Env = append(os.Environ(), "MINECRAFT_TEST_SUPERVISOR_CHILD=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	launcherDone := make(chan error, 1)
	childExited := make(chan struct{})
	go func() { launcherDone <- cmd.Wait(); close(childExited) }()
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			<-launcherDone
		}
	}()
	identity, err := launcher.CaptureIdentity("test", cmd.Process.Pid, domain.LaunchSpec{Executable: exe, WorkingDir: p.Server})
	if err != nil {
		t.Fatal(err)
	}
	releaseAdmission, err := launcher.AcquireAdmission(root, identity.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	releaseLock := func() { releaseOnce.Do(releaseAdmission) }
	defer releaseLock()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/stop" {
			http.NotFound(w, r)
			return
		}
		var request launcher.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Identity != identity || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) {
			http.Error(w, "rejected", http.StatusConflict)
			return
		}
		if _, err := io.WriteString(stdin, "\nstop\n"); err != nil {
			http.Error(w, "delivery failed", http.StatusInternalServerError)
			return
		}
		<-childExited
		final := launcher.Exit{Identity: identity, ExitedAt: time.Now().UTC(), ExitCode: 0, Deliberate: true, Reason: "operator"}
		if err := launcher.WriteJSON(filepath.Join(p.State, "exit.json"), final); err != nil {
			http.Error(w, "record failed", http.StatusInternalServerError)
			return
		}
		releaseLock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(launcher.Response{Delivered: true, Stopping: true})
	})}
	go func() { serverDone <- server.Serve(listener) }()
	defer func() { _ = server.Close(); <-serverDone }()
	control := launcher.Control{URL: "http://" + listener.Addr().String(), Token: strings.Repeat("a", 64), Identity: identity}
	if err = launcher.WriteJSON(filepath.Join(p.State, "control.json"), control); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{DataDir: root, StopTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	s.sendControl = func(ctx context.Context, got domain.ProcessIdentity, path string, req launcher.Request) (launcher.Response, error) {
		called = true
		if got != identity || path != "/stop" {
			t.Fatalf("unexpected stop control request: identity=%+v path=%q", got, path)
		}
		if _, sendErr := launcher.Send(ctx, root, got, path, req); sendErr != nil {
			return launcher.Response{}, sendErr
		}
		// Model loss of the acknowledgement after the real launcher accepted
		// the request; Stop must still verify its normal completion gates.
		return launcher.Response{}, errors.New("simulated lost acknowledgement")
	}
	if err = s.Stop(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	if err = <-launcherDone; err != nil {
		t.Fatal(err)
	}
	waited = true
	if !called {
		t.Fatal("Stop did not invoke the failed control exchange")
	}
	var final launcher.Exit
	if err = launcher.ReadJSON(filepath.Join(p.State, "exit.json"), &final); err != nil {
		t.Fatal(err)
	}
	if final.Identity != identity || !final.Deliberate || final.Forced || final.ExitCode != 0 || final.WatchdogError != "" {
		t.Fatalf("stop accepted without a matching clean final record: %+v", final)
	}
}
