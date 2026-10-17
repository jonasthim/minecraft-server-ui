package launcher

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func TestJVMHelper(t *testing.T) {
	if os.Getenv("MINECRAFT_TEST_JVM") != "1" {
		return
	}
	fmt.Fprintln(os.Stdout, "helper JVM started")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		command := strings.TrimSpace(scanner.Text())
		if command == "" {
			continue
		}
		fmt.Fprintln(os.Stdout, "received "+command)
		if command == "stop" {
			if os.Getenv("MINECRAFT_TEST_STOP") == "ignore" {
				continue
			}
			time.Sleep(100 * time.Millisecond)
			os.Exit(0)
		}
	}
	os.Exit(0)
}
func launchHelper(t *testing.T, mode string, eof bool) (InstancePaths, domain.ProcessIdentity, <-chan error, *os.File) {
	t.Helper()
	data := t.TempDir()
	p, err := Paths(data, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err = prepare(p); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec := domain.LaunchSpec{Executable: exe, WorkingDir: p.Server, Args: []string{"-test.run=^TestJVMHelper$"}, Env: map[string]string{"MINECRAFT_TEST_JVM": "1", "MINECRAFT_TEST_STOP": mode}}
	if err = WriteSpec(data, "test", spec); err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	stopTimeout := 2 * time.Second
	if mode == "ignore" {
		stopTimeout = 100 * time.Millisecond
	}
	go func() {
		defer close(done)
		done <- run(ctx, Config{DataDir: data, InstanceID: "test", StopTimeout: stopTimeout, StopOnStdinEOF: eof}, reader, func(context.Context, Config, domain.LaunchSpec) error { return nil })
	}()
	t.Cleanup(func() {
		cancel()
		writer.Close()
		reader.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})
	var control Control
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err = ReadJSON(filepath.Join(p.State, "control.json"), &control); err == nil {
			break
		}
		select {
		case launchErr := <-done:
			t.Fatalf("launcher exited before publishing control metadata: %v", launchErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("no control metadata: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return p, control.Identity, done, writer
}
func dataRoot(p InstancePaths) string { return filepath.Dir(filepath.Dir(p.Root)) }
func waitLog(t *testing.T, p InstancePaths, contains string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for {
		data, err := os.ReadFile(filepath.Join(p.Logs, "console.log"))
		if err == nil && strings.Contains(string(data), contains) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("console missing %q; %s (%v)", contains, data, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func TestLauncherRealProcessControlLeaseAndWait(t *testing.T) {
	p, id, done, _ := launchHelper(t, "", false)
	if id.PID == os.Getpid() || id.LauncherPID != os.Getpid() {
		t.Fatal("identity does not distinguish JVM and launcher", id)
	}
	for _, command := range []string{"save-off", "/SAVE-OFF", "minecraft:save-off", "STOP", "restart", "hello\nstop"} {
		if _, err := Send(context.Background(), dataRoot(p), id, "/command", Request{Command: command}); err == nil {
			t.Fatalf("unsafe command %q delivered", command)
		}
	}
	if _, err := Send(context.Background(), dataRoot(p), id, "/command", Request{Command: "list"}); err != nil {
		t.Fatal(err)
	}
	waitLog(t, p, "received list")
	if _, err := Send(context.Background(), dataRoot(p), id, "/lease/arm", Request{LeaseID: "test-lease", ExpiresAt: time.Now().Add(100 * time.Millisecond)}); err != nil {
		t.Fatal(err)
	}
	waitLog(t, p, "received save-off")
	waitLog(t, p, "received save-on")
	if _, err := Send(context.Background(), dataRoot(p), id, "/stop", Request{}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("launcher did not wait and exit")
	}
	alive, err := ProbeIdentity(id)
	if err != nil || alive {
		t.Fatalf("JVM still alive: %v %v", alive, err)
	}
	var exit Exit
	if err = ReadJSON(filepath.Join(p.State, "exit.json"), &exit); err != nil {
		t.Fatal(err)
	}
	if !exit.Deliberate || exit.Forced || exit.ExitCode != 0 {
		t.Fatal(exit)
	}
}

type blockingResponseWriter struct {
	http.ResponseWriter
	entered chan struct{}
	release chan struct{}
	buffer  bytes.Buffer
	blocked bool
}

func (w *blockingResponseWriter) Write(p []byte) (int, error) {
	return w.buffer.Write(p)
}

func (w *blockingResponseWriter) Flush() {
	if !w.blocked {
		w.blocked = true
		close(w.entered)
		<-w.release
		_, _ = w.ResponseWriter.Write(w.buffer.Bytes())
	}
	w.ResponseWriter.(http.Flusher).Flush()
}

func TestStopResponseIsWrittenBeforeChildCanExit(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestJVMHelper$")
	cmd.Env = append(os.Environ(), "MINECRAFT_TEST_JVM=1", "MINECRAFT_TEST_STOP=")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			<-waitDone
		}
	}()
	childStdin, ok := stdin.(*os.File)
	if !ok {
		t.Fatal("child stdin is not a native file")
	}
	spec := domain.LaunchSpec{Executable: exe, WorkingDir: cwd}
	identity, err := CaptureIdentity("stop-order", cmd.Process.Pid, spec)
	if err != nil {
		t.Fatal(err)
	}
	s := &session{identity: identity, stdin: childStdin, stop: make(chan string, 1), persistStop: func(string) error { return nil }}
	s.lease = newLeaseGuard(s.write)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buffered := &blockingResponseWriter{ResponseWriter: w, entered: entered, release: release}
		s.handle(buffered, r)
		// net/http flushes a buffered handler response after the handler returns.
		// Holding this flush models an acknowledgement delayed after handle has
		// released s.mu, which permits the production run loop to take the lock.
		buffered.Flush()
	}))
	defer server.Close()
	defer releaseOnce.Do(func() { close(release) })
	consumerDone := make(chan error, 1)
	go func() {
		reason := <-s.stop
		s.mu.Lock()
		s.stopping = true
		if s.stopReason == "" {
			s.stopReason = reason
		}
		if s.lease.id != "" {
			if err := s.lease.restore(); err != nil {
				s.watchdogError = err.Error()
			}
		}
		err := s.write("stop")
		s.mu.Unlock()
		consumerDone <- err
	}()
	body, err := json.Marshal(Request{Identity: identity})
	if err != nil {
		t.Fatal(err)
	}
	responseDone := make(chan error, 1)
	go func() {
		response, requestErr := http.Post(server.URL+"/stop", "application/json", bytes.NewReader(body))
		if requestErr == nil {
			_, requestErr = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		responseDone <- requestErr
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("stop response was not being written")
	}
	select {
	case <-consumerDone:
		select {
		case <-waitDone:
			waited = true
			releaseOnce.Do(func() { close(release) })
			<-responseDone
			t.Fatal("harmless child exited while HTTP stop response was blocked")
		case <-time.After(500 * time.Millisecond):
			t.Fatal("stop was delivered before the HTTP response was writable")
		}
	case <-time.After(250 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err = <-responseDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stop response did not complete")
	}
	select {
	case err = <-consumerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stop was not delivered after response")
	}
	if err = <-waitDone; err != nil {
		t.Fatal(err)
	}
	waited = true
}
func TestLauncherEOFGracefulStopWaitsForJVM(t *testing.T) {
	p, id, done, writer := launchHelper(t, "", true)
	writer.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EOF shutdown did not wait")
	}
	alive, err := ProbeIdentity(id)
	if err != nil || alive {
		t.Fatal("EOF returned with live JVM", err)
	}
	var exit Exit
	if err = ReadJSON(filepath.Join(p.State, "exit.json"), &exit); err != nil {
		t.Fatal(err)
	}
	if exit.Reason != "manager_eof" || !exit.Deliberate {
		t.Fatal(exit)
	}
}
func TestLauncherTimeoutReportsForcedStop(t *testing.T) {
	p, id, done, writer := launchHelper(t, "ignore", true)
	writer.Close()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "forced") {
			t.Fatal("forced stop hidden", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("forced shutdown did not wait")
	}
	var exit Exit
	if err := ReadJSON(filepath.Join(p.State, "exit.json"), &exit); err != nil {
		t.Fatal(err)
	}
	if !exit.Forced || !exit.Deliberate {
		t.Fatal(exit)
	}
	alive, err := ProbeIdentity(id)
	if err != nil || alive {
		t.Fatal("forced stop left JVM", err)
	}
}
func TestControlRejectsUnauthenticatedRequests(t *testing.T) {
	p, id, _, _ := launchHelper(t, "", false)
	var control Control
	if err := ReadJSON(filepath.Join(p.State, "control.json"), &control); err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(control.URL+"/command", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal(response.StatusCode)
	}
	stale := id
	stale.StartIdentity += "stale"
	if _, err = Send(context.Background(), dataRoot(p), stale, "/command", Request{Command: "list"}); err == nil {
		t.Fatal("stale PID identity accepted")
	}
	alive, err := ProbeIdentity(id)
	if err != nil || !alive {
		t.Fatal("identity check operated on unrelated process", err)
	}
}
func TestLogsRemainBoundedAcrossRotation(t *testing.T) {
	log, err := openLog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	data := []byte(strings.Repeat("x", LogMaxBytes+23))
	for i := 0; i < 4; i++ {
		if _, err = log.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{log.path, log.path + ".1", log.path + ".2"} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Size() > LogMaxBytes {
			t.Fatal("unbounded log", fi.Size())
		}
	}
	if _, err = os.Stat(log.path + ".3"); !os.IsNotExist(err) {
		t.Fatal("unexpected fourth log")
	}
}
func TestAdmissionBlocksConcurrentLauncher(t *testing.T) {
	p, id, _, _ := launchHelper(t, "", false)
	if release, err := AcquireAdmission(dataRoot(p), "test"); err == nil {
		release()
		t.Fatal("active launcher lock was bypassed")
	}
	stale := id
	stale.StartIdentity = "previous-boot:1"
	alive, err := ProbeIdentity(stale)
	if err != nil || alive {
		t.Fatal("conclusive stale identity not reconciled", err)
	}
	if err = VerifyIdentity(id); err != nil {
		t.Fatal("stale check changed live foreign process", err)
	}
}

func TestOperatorStopRequiresDurableIntent(t *testing.T) {
	p, id, _, _ := launchHelper(t, "", false)
	var control Control
	if err := ReadJSON(filepath.Join(p.State, "control.json"), &control); err != nil {
		t.Fatal(err)
	}
	saved := p.State + "-saved"
	if err := os.Rename(p.State, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.State, []byte("block state writes"), 0600); err != nil {
		t.Fatal(err)
	}
	defer func() { os.Remove(p.State); os.Rename(saved, p.State) }()
	body, err := json.Marshal(Request{Identity: id})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, control.URL+"/stop", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+control.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("operator stop accepted without durable intent: HTTP %d", response.StatusCode)
	}
	alive, err := ProbeIdentity(id)
	if err != nil || !alive {
		t.Fatal("rejected stop operated on JVM", err)
	}
}

func TestAcceptedStopClassifiesExitBeforeQueueConsumption(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := CaptureIdentity("race", os.Getpid(), domain.LaunchSpec{Executable: exe, WorkingDir: cwd})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "stop.json")
	session := &session{identity: identity, stop: make(chan string, 1), persistStop: func(reason string) error {
		return WriteJSON(path, Exit{Identity: identity, Deliberate: true, Reason: reason})
	}}
	body, err := json.Marshal(Request{Identity: identity})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/stop", bytes.NewReader(body))
	response := httptest.NewRecorder()
	session.handle(response, request)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	if len(session.stop) != 1 {
		t.Fatal("stop request not queued")
	}
	// Model the done-winning select: the stop queue has deliberately not been
	// consumed. Final classification must inherit the already accepted intent.
	final := session.finalizeExit(Exit{Identity: identity, ExitedAt: time.Now().UTC(), ExitCode: 0})
	if !final.Deliberate || final.Reason != "operator" {
		t.Fatalf("accepted stop classified as crash: %+v", final)
	}
}

func TestEOFReportsRecoveryFailureAfterCleanJVMExit(t *testing.T) {
	p, _, done, writer := launchHelper(t, "", true)
	saved := p.State + "-saved"
	if err := os.Rename(p.State, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.State, []byte("block stop intent writes"), 0600); err != nil {
		t.Fatal(err)
	}
	restored := false
	restore := func() {
		if !restored {
			os.Remove(p.State)
			os.Rename(saved, p.State)
			restored = true
		}
	}
	defer restore()
	writer.Close()
	waitLog(t, p, "received stop")
	restore()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "recovery") {
			t.Fatalf("EOF hid failed durable recovery: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EOF did not wait for JVM")
	}
	var exit Exit
	if err := ReadJSON(filepath.Join(p.State, "exit.json"), &exit); err != nil {
		t.Fatal(err)
	}
	if exit.ExitCode != 0 || exit.WatchdogError == "" {
		t.Fatal(exit)
	}
}

func TestProductionRunRejectsUnmanagedExecutableBeforeSpawn(t *testing.T) {
	root := t.TempDir()
	p, err := Paths(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err = prepare(p); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec := domain.LaunchSpec{Executable: exe, WorkingDir: p.Server, Args: []string{"-test.run=^TestJVMHelper$"}, Env: map[string]string{"MINECRAFT_TEST_JVM": "1"}}
	if err = WriteSpec(root, "test", spec); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err = Run(ctx, Config{DataDir: root, InstanceID: "test", StopTimeout: time.Second}, nil)
	if err == nil || !strings.Contains(err.Error(), "managed runtime") {
		t.Fatalf("production launcher executed unmanaged helper: %v", err)
	}
	if _, err = os.Stat(filepath.Join(p.State, "identity.json")); !os.IsNotExist(err) {
		t.Fatal("unmanaged process was spawned", err)
	}
}
