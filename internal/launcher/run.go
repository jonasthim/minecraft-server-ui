package launcher

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

type Config struct {
	DataDir, InstanceID string
	StopOnStdinEOF      bool
	StopTimeout         time.Duration
}
type Control struct {
	URL      string                 `json:"url"`
	Token    string                 `json:"token"`
	Identity domain.ProcessIdentity `json:"identity"`
}
type Exit struct {
	Identity      domain.ProcessIdentity `json:"identity"`
	ExitedAt      time.Time              `json:"exited_at"`
	ExitCode      int                    `json:"exit_code"`
	Deliberate    bool                   `json:"deliberate"`
	Forced        bool                   `json:"forced"`
	Reason        string                 `json:"reason,omitempty"`
	WatchdogError string                 `json:"watchdog_error,omitempty"`
}
type Request struct {
	Identity  domain.ProcessIdentity `json:"identity"`
	Command   string                 `json:"command,omitempty"`
	LeaseID   string                 `json:"lease_id,omitempty"`
	ExpiresAt time.Time              `json:"expires_at,omitempty"`
}
type Response struct {
	Delivered     bool   `json:"delivered"`
	Stopping      bool   `json:"stopping"`
	WatchdogError string `json:"watchdog_error,omitempty"`
}
type session struct {
	mu            sync.Mutex
	identity      domain.ProcessIdentity
	stdin         *os.File
	lease         *leaseGuard
	stop          chan string
	stopping      bool
	stopReason    string
	watchdogError string
	persistStop   func(string) error
}

// ValidateCommand is an internal transport fence, not a public command safety
// policy. Task10 must add role/version allowlists; unknown plugin commands may
// have arbitrary behavior and are not made safe by this blocklist.
func ValidateCommand(command string) error {
	if len(command) == 0 || len(command) > 4096 || strings.ContainsAny(command, "\r\n\x00") {
		return fmt.Errorf("command must be one nonempty line of at most 4096 bytes")
	}
	fields := strings.Fields(strings.TrimLeft(strings.TrimSpace(command), "/"))
	if len(fields) == 0 {
		return fmt.Errorf("empty command")
	}
	verb := strings.ToLower(fields[0])
	if i := strings.LastIndexByte(verb, ':'); i >= 0 {
		verb = verb[i+1:]
	}
	switch verb {
	case "execute", "function", "schedule", "return":
		return fmt.Errorf("indirect execution commands are not allowed by the launcher transport")
	case "debug":
		if len(fields) > 1 {
			sub := strings.ToLower(fields[1])
			if i := strings.LastIndexByte(sub, ':'); i >= 0 {
				sub = sub[i+1:]
			}
			if sub == "function" {
				return fmt.Errorf("debug function is not allowed by the launcher transport")
			}
		}
	case "save-off":
		return fmt.Errorf("save-off requires a watchdog lease")
	case "stop", "restart", "shutdown":
		return fmt.Errorf("process lifecycle command requires supervisor Stop")
	}
	return nil
}
func (s *session) write(command string) error {
	n, err := writeStdin(s.stdin, []byte("\n"+command+"\n"))
	if err == nil && n != len(command)+2 {
		return io.ErrShortWrite
	}
	return err
}
func (s *session) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req Request
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		http.Error(w, "invalid control request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.Identity != s.identity {
		http.Error(w, "process identity mismatch", http.StatusConflict)
		return
	}
	if err := verifyOwnedChild(r.Context(), req.Identity); err != nil {
		http.Error(w, "process identity no longer matches", http.StatusConflict)
		return
	}
	var err error
	if s.stopping && r.URL.Path != "/status" {
		http.Error(w, "process stopping", http.StatusConflict)
		return
	}
	switch r.URL.Path {
	case "/command":
		err = ValidateCommand(req.Command)
		if err == nil {
			err = s.write(req.Command)
		}
	case "/lease/arm":
		err = s.lease.arm(req.LeaseID, req.ExpiresAt, time.Now())
	case "/lease/release":
		err = s.lease.release(req.LeaseID, time.Now())
	case "/stop":
		if s.persistStop == nil {
			err = fmt.Errorf("durable stop recording unavailable")
			break
		}
		if err = s.persistStop("operator"); err != nil {
			err = fmt.Errorf("durable stop recording failed: %w", err)
			break
		}
		s.stopping = true
		s.stopReason = "operator"
		select {
		case s.stop <- "operator":
		default:
		}
	case "/status":
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(Response{Delivered: r.URL.Path != "/status", Stopping: s.stopping, WatchdogError: s.watchdogError})
}

// Run owns the child until Wait returns. ctx controls launcher shutdown only;
// a manager request's context is never inherited by the JVM.
func Run(ctx context.Context, cfg Config, parentStdin io.Reader) error {
	return run(ctx, cfg, parentStdin, verifyProductionLaunch)
}

// The verifier argument is private to package tests. No CLI, Config, environment
// variable or exported constructor can disable the production verification gate.
func run(ctx context.Context, cfg Config, parentStdin io.Reader, verify func(context.Context, Config, domain.LaunchSpec) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := Paths(cfg.DataDir, cfg.InstanceID)
	if err != nil {
		return err
	}
	unlock, err := AcquireAdmission(cfg.DataDir, cfg.InstanceID)
	if err != nil {
		return err
	}
	defer unlock()
	if err = prepare(p); err != nil {
		return err
	}
	if err = CheckActivationReady(ctx, cfg.DataDir, cfg.InstanceID); err != nil {
		return err
	}
	var disk diskSpec
	if err = ReadJSON(filepath.Join(p.State, "launch.json"), &disk); err != nil {
		return fmt.Errorf("read launch specification: %w", err)
	}
	disk.Spec.Env = disk.Env
	if err = ValidateSpec(disk.Spec, p); err != nil {
		return err
	}
	if err = verify(ctx, cfg, disk.Spec); err != nil {
		return err
	}
	if cfg.StopTimeout <= 0 {
		cfg.StopTimeout = 90 * time.Second
	}
	if cfg.StopTimeout > 2*time.Minute {
		return fmt.Errorf("stop timeout exceeds two minutes")
	}
	log, err := openLog(p.Logs)
	if err != nil {
		return err
	}
	defer log.Close()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	token := make([]byte, 32)
	if _, err = rand.Read(token); err != nil {
		return err
	}
	cmd := exec.Command(disk.Spec.Executable, disk.Spec.Args...) // no shell; independent lifetime
	cmd.Dir = disk.Spec.WorkingDir
	cmd.Env = environment(disk.Env)
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.WaitDelay = 2 * time.Second
	pipe, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdin, ok := pipe.(*os.File)
	if !ok {
		return fmt.Errorf("stdin pipe is not a native file")
	}
	defer stdin.Close()
	if err = configureStdin(stdin); err != nil {
		return fmt.Errorf("configure bounded stdin: %w", err)
	}
	prepareChild(cmd)
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("start JVM: %w", err)
	}
	cleanup, err := confineChild(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("confine JVM: %w", err)
	}
	defer cleanup()
	identity, err := CaptureIdentity(cfg.InstanceID, cmd.Process.Pid, disk.Spec)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	s := &session{identity: identity, stdin: stdin, stop: make(chan string, 1)}
	s.lease = newLeaseGuard(s.write)
	s.persistStop = func(reason string) error {
		return WriteJSON(filepath.Join(p.State, "stop.json"), Exit{Identity: identity, Deliberate: true, Reason: reason})
	}
	ctl := Control{URL: "http://" + listener.Addr().String(), Token: hex.EncodeToString(token), Identity: identity}
	if err = WriteJSON(filepath.Join(p.State, "identity.json"), identity); err == nil {
		err = WriteJSON(filepath.Join(p.State, "control.json"), ctl)
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("publish process control: %w", err)
	}
	defer os.Remove(filepath.Join(p.State, "control.json"))
	server := &http.Server{ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 8192, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := "Bearer " + ctl.Token
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(expected)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		s.handle(w, r)
	})}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	defer server.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if cfg.StopOnStdinEOF && parentStdin != nil {
		go func() {
			scanner := bufio.NewScanner(parentStdin)
			for scanner.Scan() {
			}
			select {
			case s.stop <- "manager_eof":
			case <-ctx.Done():
			}
		}()
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var stopTimer *time.Timer
	var stopDeadline <-chan time.Time
	exit := Exit{Identity: identity}
	var waitErr error
	stop := func(reason string) {
		if stopTimer != nil {
			return
		}
		exit.Deliberate = true
		exit.Reason = reason
		// Persist intent before sending stop. Failure remains visible in exit error;
		// shutdown still attempts save-on and stop so a service cannot orphan a JVM.
		if e := WriteJSON(filepath.Join(p.State, "stop.json"), exit); e != nil {
			exit.WatchdogError = "persist stop intention failed"
		}
		s.mu.Lock()
		s.stopping = true
		if s.stopReason == "" {
			s.stopReason = reason
		}
		if s.lease.id != "" {
			if e := s.lease.restore(); e != nil {
				s.watchdogError = e.Error()
			}
		}
		if e := s.write("stop"); e != nil {
			s.watchdogError = "stop delivery failed"
		}
		s.mu.Unlock()
		stopTimer = time.NewTimer(cfg.StopTimeout)
		stopDeadline = stopTimer.C
	}
	ctxDone := ctx.Done()
loop:
	for {
		select {
		case waitErr = <-done:
			break loop
		case reason := <-s.stop:
			stop(reason)
		case <-ctxDone:
			ctxDone = nil
			stop("launcher_shutdown")
		case <-stopDeadline:
			exit.Forced = true
			stopDeadline = nil
			if e := cmd.Process.Kill(); e != nil && !errors.Is(e, os.ErrProcessDone) {
				exit.WatchdogError = "forced termination failed"
			}
		case e := <-serveErr:
			if e != nil && !errors.Is(e, http.ErrServerClosed) {
				stop("control_failed")
			}
		case now := <-tick.C:
			s.mu.Lock()
			if e := s.lease.expire(now); e != nil {
				s.watchdogError = e.Error()
			}
			s.mu.Unlock()
		}
	}
	if stopTimer != nil {
		stopTimer.Stop()
	}
	exit.ExitedAt = time.Now().UTC()
	if cmd.ProcessState != nil {
		exit.ExitCode = cmd.ProcessState.ExitCode()
	}
	exit = s.finalizeExit(exit)
	if err = WriteJSON(filepath.Join(p.State, "exit.json"), exit); err != nil {
		return fmt.Errorf("persist process exit: %w", err)
	}
	if exit.Forced {
		return fmt.Errorf("JVM required forced termination after graceful stop timeout")
	}
	if exit.WatchdogError != "" {
		return fmt.Errorf("launcher recovery failed: %s", exit.WatchdogError)
	}
	if waitErr != nil {
		return fmt.Errorf("JVM exit: %w", waitErr)
	}
	return nil
}
func environment(overrides map[string]string) []string {
	values := map[string]string{}
	for _, s := range os.Environ() {
		k, v, ok := strings.Cut(s, "=")
		switch strings.ToUpper(k) {
		case "JAVA_TOOL_OPTIONS", "JDK_JAVA_OPTIONS", "_JAVA_OPTIONS", "CLASSPATH", "DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH", "GCONV_PATH":
			continue
		}
		if strings.HasPrefix(strings.ToUpper(k), "LD_") {
			continue
		}
		if ok {
			values[k] = v
		}
	}
	for k, v := range overrides {
		values[k] = v
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+values[k])
	}
	return out
}

// finalizeExit shares the acceptance mutex with /stop. A done-winning select
// cannot erase an operator intent that was already durably accepted.
func (s *session) finalizeExit(exit Exit) Exit {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopReason != "" {
		exit.Deliberate = true
		exit.Reason = s.stopReason
	}
	if s.watchdogError != "" {
		if exit.WatchdogError != "" && exit.WatchdogError != s.watchdogError {
			exit.WatchdogError += "; " + s.watchdogError
		} else {
			exit.WatchdogError = s.watchdogError
		}
	}
	return exit
}
