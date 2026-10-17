//go:build linux

package selfupdate

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
)

func TestLinuxRecoveryGraphRetainsEveryStartCheck(t *testing.T) {
	unit, e := os.ReadFile("../../deploy/linux/minecraft-ui-update-recovery.service")
	if e != nil {
		t.Fatal(e)
	}
	drop, e := os.ReadFile("../../deploy/linux/minecraft-ui-update.conf")
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(unit), "Type=oneshot") || strings.Contains(string(unit), "RemainAfterExit=yes") || !strings.Contains(string(drop), "Requires=minecraft-ui-update-recovery.service") || !strings.Contains(string(drop), "After=minecraft-ui-update-recovery.service") {
		t.Fatal("every ordinary start must check recovery first")
	}
	source, e := os.ReadFile("native_linux.go")
	if e != nil {
		t.Fatal(e)
	}
	s := string(source)
	if !strings.Contains(s, "return d.startWithPermit(ctx)") || !strings.Contains(s, "return recoverLinux(ctx, paths)") {
		t.Fatal("fixed manager start/recovery dispatcher was bypassed")
	}
	source, e = os.ReadFile("launch_linux.go")
	if e != nil {
		t.Fatal(e)
	}
	s = string(source)
	core := s[strings.Index(s, "func recoverLinuxAt("):]
	if strings.Index(core, "permitted, err := readLaunchAt(ctx, paths, state, bin)") < 0 || strings.Index(core, "permitted, err := readLaunchAt(ctx, paths, state, bin)") > strings.Index(core, "release, err := launcher.AcquireManager(paths.State)") {
		t.Fatal("start reenters recovery lock before live-owner permit")
	}
}
func TestLinuxLaunchPermitRequiresLiveExactJournalImageAndKernelOwner(t *testing.T) {
	w, c, j, _ := nativeFixture(t)
	ctx := context.Background()
	if err := w.bin.Remove(w.binary); err != nil {
		t.Fatal(err)
	}
	if err := copyVerified(ctx, c, w.binary, w.bin, w.binary, j.Candidate); err != nil {
		t.Fatal(err)
	}
	j.Format = 1
	j.Phase = "candidate"
	if err := w.save(ctx, j); err != nil {
		t.Fatal(err)
	}
	release, err := launcher.AcquireManager(w.state.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	exe, _ := os.Executable()
	paths := nativePaths{State: w.state.Name(), Bin: w.bin.Name(), Binary: w.binary, Updater: exe}
	start, err := processStart(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	valid := launchPermit{Format: 1, PID: os.Getpid(), Start: start, StageID: j.Request.StageID, Nonce: j.Nonce, Phase: j.Phase, SHA256: j.Candidate.SHA256}
	if err := validateLaunch(ctx, paths, w.state, w.bin, valid); err != nil {
		t.Fatal("active owned start refused", err)
	}
	for _, mutation := range []string{"nonce", "stage", "hash", "start", "phase", "pid", "updater"} {
		t.Run(mutation, func(t *testing.T) {
			p := valid
			copyPaths := paths
			switch mutation {
			case "nonce":
				p.Nonce = digest([]byte("foreign"))
			case "stage":
				p.StageID = digest([]byte("foreign"))
			case "hash":
				p.SHA256 = j.Prior.SHA256
			case "start":
				p.Start = "0"
			case "phase":
				p.Phase = "rollback"
			case "pid":
				p.PID = 2147483647
			case "updater":
				copyPaths.Updater = "/usr/bin/true"
			}
			if err := validateLaunch(ctx, copyPaths, w.state, w.bin, p); err == nil {
				t.Fatal("invalid launch permit accepted")
			}
		})
	}
	// Same boot, worker may remain alive after releasing ownership: no permit is
	// valid anymore. A stale successful oneshot must never hide this condition.
	release()
	if err := validateLaunch(ctx, paths, w.state, w.bin, valid); err == nil {
		t.Fatal("permit survived native transaction lock release")
	}
	// Model ordinary recovery after the worker crash: no launch is authorized,
	// and the actual interrupted candidate is replaced with its prior executable.
	if err := writeFile(ctx, w.bin, w.binary+".previous", strings.NewReader("old"), 3, j.Prior.SHA256); err != nil {
		t.Fatal(err)
	}
	if err := w.recover(ctx); err != nil {
		t.Fatal(err)
	}
	h, err := hashFile(ctx, w.bin, w.binary)
	if err != nil || h != j.Prior.SHA256 {
		t.Fatal("same-boot recovery lost prior executable", h, err)
	}
}
func TestLaunchUnrelatedWorker(t *testing.T) {
	if os.Getenv("MUI_LAUNCH_FIXTURE") != "1" {
		return
	}
	if path := os.Getenv("MUI_LAUNCH_LOCK"); path != "" {
		release, err := launcher.AcquireManager(path)
		if err != nil {
			panic(err)
		}
		defer release()
	}
	fmt.Println("ready")
	_, _ = bufio.NewReader(os.Stdin).ReadByte()
	os.Exit(0)
}
func TestLinuxLaunchPermitRejectsLiveUnrelatedUpdater(t *testing.T) {
	w, _, j, _ := nativeFixture(t)
	j.Format = 1
	j.Phase = "aborted"
	ctx := context.Background()
	if err := w.save(ctx, j); err != nil {
		t.Fatal(err)
	}
	release, err := launcher.AcquireManager(w.state.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	child := exec.Command(os.Args[0], "-test.run=^TestLaunchUnrelatedWorker$")
	child.Env = append(os.Environ(), "MUI_LAUNCH_FIXTURE=1")
	input, _ := child.StdinPipe()
	output, _ := child.StdoutPipe()
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { input.Close(); child.Wait() }()
	if line, _ := bufio.NewReader(output).ReadString('\n'); line != "ready\n" {
		t.Fatal(line)
	}
	start, err := processStart(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	p := launchPermit{Format: 1, PID: child.Process.Pid, Start: start, StageID: j.Request.StageID, Nonce: j.Nonce, Phase: j.Phase, SHA256: j.Prior.SHA256}
	if err := validateLaunch(ctx, nativePaths{Binary: w.binary, Updater: exe}, w.state, w.bin, p); err == nil {
		t.Fatal("unrelated updater inherited launch permission")
	}
	b, _ := json.Marshal(p)
	b = append(b, []byte("garbage")...)
	var malformed launchPermit
	if strictJSON(b, &malformed) == nil {
		t.Fatal("malformed permit parsed")
	}
}

func TestLinuxLaunchPermitPublicationAndCleanupFailures(t *testing.T) {
	for _, mode := range []string{"success", "start_failure", "publish_fence", "cleanup_fence", "cleanup_remove"} {
		t.Run(mode, func(t *testing.T) {
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			fences, starts := 0, 0
			fence := func(ctx context.Context, _ *os.Root) error {
				fences++
				if ctx.Err() != nil {
					t.Fatal("cleanup inherited canceled context")
				}
				if mode == "publish_fence" && fences == 1 || mode == "cleanup_fence" && fences == 2 {
					return fmt.Errorf("injected fence failure")
				}
				return nil
			}
			start := func(context.Context) error {
				starts++
				b, err := readChecked(context.Background(), root, "launch.json", 2048)
				if err != nil || len(b) == 0 {
					t.Fatal("start lacked published permit", err)
				}
				if mode == "cleanup_remove" {
					if err := root.Remove("launch.json"); err != nil {
						t.Fatal(err)
					}
					root.Mkdir("launch.json", 0700)
					child, _ := root.Create("launch.json/child")
					child.Close()
				}
				if mode == "start_failure" {
					return fmt.Errorf("injected service start failure")
				}
				return nil
			}
			err = publishLaunch(context.Background(), root, launchPermit{Format: 1}, fence, start)
			if (err == nil) != (mode == "success") {
				t.Fatal("publication/cleanup error ignored", mode, err)
			}
			if mode == "publish_fence" {
				if starts != 0 {
					t.Fatal("started without durable permit")
				}
			} else if starts != 1 {
				t.Fatal("start count", starts)
			}
			if fences != 2 {
				t.Fatal("missing publication/cleanup fence", fences)
			}
			if _, e := root.Lstat("launch.json"); mode != "cleanup_remove" && !os.IsNotExist(e) {
				t.Fatal("permit remained after start", e)
			}
		})
	}
}

func TestLinuxSameBootCrashLeavesNoValidLaunchBypassAndRecoversPrior(t *testing.T) {
	w, c, j, d := nativeFixture(t)
	ctx := context.Background()
	if err := copyVerified(ctx, w.bin, w.binary, w.bin, w.binary+".previous", j.Prior); err != nil {
		t.Fatal(err)
	}
	if err := w.bin.Remove(w.binary); err != nil {
		t.Fatal(err)
	}
	if err := copyVerified(ctx, c, w.binary, w.bin, w.binary, j.Candidate); err != nil {
		t.Fatal(err)
	}
	j.Format = 1
	j.Phase = "candidate"
	if err := w.save(ctx, j); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestLaunchUnrelatedWorker$")
	child.Env = append(os.Environ(), "MUI_LAUNCH_FIXTURE=1", "MUI_LAUNCH_LOCK="+w.state.Name())
	input, _ := child.StdinPipe()
	output, _ := child.StdoutPipe()
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if line, _ := bufio.NewReader(output).ReadString('\n'); line != "ready\n" {
		child.Process.Kill()
		child.Wait()
		t.Fatal(line)
	}
	start, err := processStart(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	paths := nativePaths{Binary: w.binary, Updater: exe}
	p := launchPermit{Format: 1, PID: child.Process.Pid, Start: start, StageID: j.Request.StageID, Nonce: j.Nonce, Phase: j.Phase, SHA256: j.Candidate.SHA256}
	if err := validateLaunch(ctx, paths, w.state, w.bin, p); err != nil {
		child.Process.Kill()
		child.Wait()
		t.Fatal("owned live worker refused", err)
	}
	b, _ := json.Marshal(p)
	if err := writeFile(ctx, w.state, "launch.json", bytesReader(b), int64(len(b)), ""); err != nil {
		t.Fatal(err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	if err := validateLaunch(ctx, paths, w.state, w.bin, p); err == nil {
		t.Fatal("crashed worker permit remained valid")
	}
	release, err := launcher.AcquireManager(w.state.Name())
	if err != nil {
		t.Fatal("crash did not release kernel lock", err)
	}
	defer release()
	if err := w.recover(ctx); err != nil {
		t.Fatal(err)
	}
	hash, err := hashFile(ctx, w.bin, w.binary)
	if err != nil || hash != j.Prior.SHA256 || d.starts != 0 {
		t.Fatal("ordinary dependency recovery failed", hash, err, d.starts)
	}
}

func TestLinuxRecoverySelectionReconcilesPersistedReusedPIDPermit(t *testing.T) {
	for _, mode := range []string{"reused_pid", "wrong_executable", "released_lock", "no_journal"} {
		t.Run(mode, func(t *testing.T) {
			w, c, j, d := nativeFixture(t)
			ctx := context.Background()
			if err := copyVerified(ctx, w.bin, w.binary, w.bin, w.binary+".previous", j.Prior); err != nil {
				t.Fatal(err)
			}
			if err := w.bin.Remove(w.binary); err != nil {
				t.Fatal(err)
			}
			if err := copyVerified(ctx, c, w.binary, w.bin, w.binary, j.Candidate); err != nil {
				t.Fatal(err)
			}
			j.Format = 1
			j.Phase = "candidate"
			if err := w.save(ctx, j); err != nil {
				t.Fatal(err)
			}
			priorRelease, err := launcher.AcquireManager(w.state.Name())
			if err != nil {
				t.Fatal(err)
			}
			priorRelease()
			child := exec.Command(os.Args[0], "-test.run=^TestLaunchUnrelatedWorker$")
			child.Env = append(os.Environ(), "MUI_LAUNCH_FIXTURE=1")
			input, _ := child.StdinPipe()
			output, _ := child.StdoutPipe()
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { input.Close(); child.Wait() }()
			if line, _ := bufio.NewReader(output).ReadString('\n'); line != "ready\n" {
				t.Fatal(line)
			}
			start, err := processStart(child.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "reused_pid" {
				number, _ := strconv.ParseUint(start, 10, 64)
				start = strconv.FormatUint(number+1, 10)
			}
			p := launchPermit{Format: 1, PID: child.Process.Pid, Start: start, StageID: j.Request.StageID, Nonce: j.Nonce, Phase: j.Phase, SHA256: j.Candidate.SHA256}
			body, _ := json.Marshal(p)
			if err := writeFile(ctx, w.state, "launch.json", bytesReader(body), int64(len(body)), ""); err != nil {
				t.Fatal(err)
			}
			exe, _ := os.Executable()
			paths := nativePaths{State: w.state.Name(), Bin: w.bin.Name(), Binary: w.binary, Updater: exe}
			if mode == "wrong_executable" {
				paths.Updater = "/usr/bin/true"
			}
			if mode == "no_journal" {
				if err := w.bin.Rename(w.binary+".previous", w.binary); err != nil {
					t.Fatal(err)
				}
				if err := w.state.Remove("activation.json"); err != nil {
					t.Fatal(err)
				}
			}
			verified := 0
			verify := func(context.Context) error {
				verified++
				if release, err := launcher.AcquireManager(paths.State); err == nil {
					release()
					t.Fatal("recovery verifier ran outside native lock")
				}
				return nil
			}
			if err := recoverLinuxAt(ctx, paths, w.state, w.bin, d, verify); err != nil {
				t.Fatal("stale permit prevented serialized recovery", err)
			}
			if _, err := w.state.Lstat("launch.json"); !os.IsNotExist(err) {
				t.Fatal("stale permit remained", err)
			}
			if mode != "no_journal" {
				h, err := hashFile(ctx, w.bin, w.binary)
				if err != nil || h != j.Prior.SHA256 || verified != 1 {
					t.Fatal("prior recovery failed", h, err, verified)
				}
			} else if verified != 0 {
				t.Fatal("no-journal first use required trust")
			}
			if err := recoverLinuxAt(ctx, paths, w.state, w.bin, d, verify); err != nil {
				t.Fatal("stale permit poisoned subsequent start", err)
			}
			if err := d.Start(ctx); err != nil {
				t.Fatal(err)
			}
			if err := d.WaitHealthy(ctx, j.Prior, j.Nonce); err != nil {
				t.Fatal(err)
			}
			if d.starts != 1 {
				t.Fatal("subsequent manager restart was not bounded", d.starts)
			}
		})
	}
}

func TestLinuxRecoverySelectionKeepsInvalidOrAmbiguousAuthorityClosed(t *testing.T) {
	for _, mode := range []string{"malformed", "unsafe_mode", "invalid_journal", "active_wrong_nonce", "stale_cleanup_fence"} {
		t.Run(mode, func(t *testing.T) {
			w, _, j, d := nativeFixture(t)
			ctx := context.Background()
			j.Format = 1
			j.Phase = "aborted"
			if err := w.save(ctx, j); err != nil {
				t.Fatal(err)
			}
			start, err := processStart(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			p := launchPermit{Format: 1, PID: os.Getpid(), Start: start, StageID: j.Request.StageID, Nonce: j.Nonce, Phase: j.Phase, SHA256: j.Prior.SHA256}
			release, err := launcher.AcquireManager(w.state.Name())
			if err != nil {
				t.Fatal(err)
			}
			if mode != "active_wrong_nonce" {
				release()
			}
			if mode == "active_wrong_nonce" {
				defer release()
				p.Nonce = digest([]byte("foreign"))
			} else {
				ticks, _ := strconv.ParseUint(p.Start, 10, 64)
				p.Start = strconv.FormatUint(ticks+1, 10)
			}
			body, _ := json.Marshal(p)
			if mode == "malformed" {
				body = []byte("{not-json}")
			}
			if err := writeFile(ctx, w.state, "launch.json", bytesReader(body), int64(len(body)), ""); err != nil {
				t.Fatal(err)
			}
			if mode == "unsafe_mode" {
				file, _ := w.state.Open("launch.json")
				file.Chmod(0644)
				file.Close()
			}
			if mode == "invalid_journal" {
				w.state.Remove("activation.json")
				bad := []byte("{}")
				if err := writeFile(ctx, w.state, "activation.json", bytesReader(bad), int64(len(bad)), ""); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "stale_cleanup_fence" {
				d.failFence = d.fences + 1
			}
			exe, _ := os.Executable()
			paths := nativePaths{State: w.state.Name(), Bin: w.bin.Name(), Binary: w.binary, Updater: exe}
			if err := recoverLinuxAt(ctx, paths, w.state, w.bin, d, func(context.Context) error { return nil }); err == nil {
				t.Fatal("invalid or ambiguous recovery authority accepted", mode)
			}
			if mode != "stale_cleanup_fence" {
				if _, err := w.state.Lstat("launch.json"); err != nil {
					t.Fatal("invalid authority was silently removed", err)
				}
			}
			if d.starts != 0 {
				t.Fatal("refused recovery started manager", d.starts)
			}
		})
	}
}

type serializedRecoveryFixture struct {
	*fixtureDriver
	t             *testing.T
	state         *os.Root
	cleanupFenced bool
}

func (d *serializedRecoveryFixture) Fence(ctx context.Context, root *os.Root) error {
	if root == d.state {
		if _, err := root.Lstat("launch.json"); os.IsNotExist(err) {
			if release, e := launcher.AcquireManager(root.Name()); e == nil {
				release()
				d.t.Fatal("stale permit fence ran outside serialization")
			}
			d.cleanupFenced = true
		}
	}
	return d.fixtureDriver.Fence(ctx, root)
}
func TestLinuxStalePermitCleanupFenceRetainsSerialization(t *testing.T) {
	w, _, j, d := nativeFixture(t)
	ctx := context.Background()
	j.Format = 1
	j.Phase = "aborted"
	if err := w.save(ctx, j); err != nil {
		t.Fatal(err)
	}
	release, err := launcher.AcquireManager(w.state.Name())
	if err != nil {
		t.Fatal(err)
	}
	release()
	start, err := processStart(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	ticks, _ := strconv.ParseUint(start, 10, 64)
	p := launchPermit{Format: 1, PID: os.Getpid(), Start: strconv.FormatUint(ticks+1, 10), StageID: j.Request.StageID, Nonce: j.Nonce, Phase: j.Phase, SHA256: j.Prior.SHA256}
	b, _ := json.Marshal(p)
	if err := writeFile(ctx, w.state, "launch.json", bytesReader(b), int64(len(b)), ""); err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	paths := nativePaths{State: w.state.Name(), Bin: w.bin.Name(), Binary: w.binary, Updater: exe}
	driver := &serializedRecoveryFixture{fixtureDriver: d, t: t, state: w.state}
	if err := recoverLinuxAt(ctx, paths, w.state, w.bin, driver, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !driver.cleanupFenced {
		t.Fatal("stale permit removal was not fenced")
	}
}
