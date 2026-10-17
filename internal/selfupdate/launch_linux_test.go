//go:build linux

package selfupdate

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
	if !strings.Contains(s, "return d.startWithPermit(ctx)") || strings.Index(s, "permitted, e := readLaunch(ctx, paths)") > strings.Index(s, "release, err := launcher.AcquireManager(paths.State)") {
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
