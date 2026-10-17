//go:build linux

package selfupdate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func platformPaths() nativePaths {
	return nativePaths{Data: "/var/lib/minecraft-ui", Trust: "/etc/minecraft-ui/update-trust.json", State: "/var/lib/minecraft-ui-update", Bin: "/usr/local/bin", Binary: "minecraft-ui", Updater: "/usr/local/lib/minecraft-ui/selfupdate"}
}
func protectedInfo(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0 && info.Mode().Perm()&0022 == 0 && info.Mode()&os.ModeSymlink == 0
}
func openProtectedRoot(ctx context.Context, path string) (*os.Root, error) {
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || !protectedInfo(info) {
			return nil, errors.New("native updater directory is not root-owned and protected")
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return safepath.OpenRoot(ctx, path)
}
func protectedFile(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !protectedInfo(info) || !ok || stat.Nlink != 1 {
		return errors.New("native update file must be a protected root-owned regular file")
	}
	return nil
}
func brokerAvailable(ctx context.Context, paths nativePaths) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	root, err := openProtectedRoot(ctx, filepath.Dir(paths.Updater))
	if err != nil {
		return false
	}
	defer root.Close()
	if protectedFile(root, filepath.Base(paths.Updater)) != nil {
		return false
	}
	_, err = runFixed(ctx, "/usr/bin/sudo", "-n", paths.Updater, "status")
	return err == nil
}
func brokerRequest(ctx context.Context, paths nativePaths, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := runFixed(ctx, "/usr/bin/sudo", "-n", paths.Updater, "request", id)
	return err
}
func runFixed(ctx context.Context, binary string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	var output boundedOutput
	command.Stdout = &output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return nil, errors.New("fixed native update operation failed")
	}
	if output.exceeded {
		return nil, errors.New("native update response exceeded bound")
	}
	return output.body, nil
}

type boundedOutput struct {
	body     []byte
	exceeded bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if len(b.body)+n > 8192 {
		b.exceeded = true
		return n, nil
	}
	b.body = append(b.body, p...)
	return n, nil
}

type linuxDriver struct{ paths nativePaths }

func (d linuxDriver) ActivationFailure(stage string) {
	// The worker supplies an allowlisted fixed stage, never raw error text.
	_, _ = fmt.Fprintln(os.Stderr, "native-update-failure:"+stage)
}

func (d linuxDriver) Fence(ctx context.Context, root *os.Root) error {
	return syncPrivilegedRoot(ctx, root)
}
func (d linuxDriver) Stop(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 155*time.Second)
	defer cancel()
	_, err := runFixed(ctx, "/usr/bin/systemctl", "stop", "minecraft-ui.service")
	if err != nil {
		return err
	}
	out, err := runFixed(ctx, "/usr/bin/systemctl", "show", "--property=MainPID", "--value", "minecraft-ui.service")
	if err != nil || strings.TrimSpace(string(out)) != "0" {
		return errors.New("manager service has not stopped")
	}
	return nil
}
func (d linuxDriver) Start(ctx context.Context) error {
	return d.startWithPermit(ctx)
}
func (d linuxDriver) ProbeSchema(ctx context.Context, previous bool) (Capability, error) {
	binary := filepath.Join(d.paths.Bin, d.paths.Binary)
	if previous {
		binary += ".previous"
	}
	command := exec.CommandContext(ctx, "/usr/sbin/runuser", "-u", "minecraft-ui", "--", binary, "update-schema")
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "MINECRAFT_UI_DATA_DIR=" + d.paths.Data}
	return runLinuxSchemaProbe(ctx, command, os.Stderr)
}

// The caller constructs the fixed manager-account command above. This helper
// captures bounded stderr only to recognize exact authored child diagnostics;
// all other bytes, including the child's existing raw error log, are discarded.
func runLinuxSchemaProbe(ctx context.Context, command *exec.Cmd, diagnostics io.Writer) (Capability, error) {
	var capability Capability
	var output, stderr boundedOutput
	command.Stdout = &output
	command.Stderr = &stderr
	err := command.Run()
	if err != nil || output.exceeded {
		if stderr.exceeded {
			_, _ = fmt.Fprintln(diagnostics, "native-update-schema-diagnostic:command:stderr_overflow")
		} else {
			for _, line := range strings.Split(string(stderr.body), "\n") {
				if validSchemaProbeDiagnostic(line) {
					_, _ = fmt.Fprintln(diagnostics, line)
					break // At most one fixed child boundary per failed probe.
				}
			}
		}
		writeLinuxSchemaCommandFailure(diagnostics, ctx, err, output.exceeded)
		return capability, errors.New("manager-account schema probe failed")
	}
	if err := strictJSON(output.body, &capability); err != nil {
		_, _ = fmt.Fprintln(diagnostics, "native-update-schema-diagnostic:command:invalid_json")
		return capability, errors.New("invalid manager schema probe")
	}
	return capability, nil
}

func validSchemaProbeDiagnostic(line string) bool {
	fields := strings.Split(line, ":")
	if len(fields) < 3 || len(fields) > 4 || fields[0] != "native-update-schema-diagnostic" {
		return false
	}
	switch fields[1] {
	case "config", "schema_read", "challenge_read", "challenge_version", "actor_read", "encode":
	default:
		return false
	}
	if fields[2] == "sqlite" && len(fields) == 4 {
		code, err := strconv.Atoi(fields[3])
		return err == nil && code > 0 && code <= 65535 && strconv.Itoa(code) == fields[3]
	}
	if len(fields) != 3 {
		return false
	}
	switch fields[2] {
	case "permission", "not_exist", "canceled", "deadline", "other":
		return true
	}
	return false
}

func writeLinuxSchemaCommandFailure(out io.Writer, ctx context.Context, err error, overflow bool) {
	reason := "launch_other"
	var exit *exec.ExitError
	switch {
	case overflow:
		reason = "output_overflow"
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		reason = "context_deadline"
	case errors.Is(ctx.Err(), context.Canceled):
		reason = "context_canceled"
	case errors.As(err, &exit):
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() > 0 && status.Signal() <= 64 {
			reason = "signal:" + strconv.Itoa(int(status.Signal()))
		} else if code := exit.ExitCode(); code > 0 && code <= 255 {
			reason = "exit:" + strconv.Itoa(code)
		}
	case errors.Is(err, os.ErrPermission):
		reason = "launch_permission"
	case errors.Is(err, os.ErrNotExist), errors.Is(err, exec.ErrNotFound):
		reason = "launch_not_exist"
	}
	_, _ = fmt.Fprintln(out, "native-update-schema-diagnostic:command:"+reason)
}
func (d linuxDriver) ProbeCandidate(ctx context.Context, expected Installed) (Capability, error) {
	var capability Capability
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// The worker copied and independently verified this root-only fixed path.
	command := exec.CommandContext(ctx, "/usr/sbin/runuser", "-u", "minecraft-ui", "--", filepath.Join(d.paths.Bin, d.paths.Binary)+".next", "update-capability")
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	var output boundedOutput
	command.Stdout = &output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil || output.exceeded {
		return capability, errors.New("manager-account candidate capability probe failed")
	}
	if err := strictJSON(output.body, &capability); err != nil {
		return capability, errors.New("invalid candidate capability")
	}
	return capability, nil
}
func (d linuxDriver) LockManager(ctx context.Context) (func(), error) {
	for {
		release, err := launcher.AcquireManager(d.paths.Data)
		if err == nil {
			return release, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
func (d linuxDriver) WriteChallenge(ctx context.Context, c Challenge) error {
	root, err := safepath.OpenRoot(ctx, controlDirectory(d.paths.Data))
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range []string{"challenge.json", "drained.json", "drained.pending"} {
		if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	body, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := writeFile(ctx, root, "challenge.json", bytesReader(body), int64(len(body)), ""); err != nil {
		return err
	}
	account, err := user.Lookup("minecraft-ui")
	if err != nil {
		return err
	}
	uid, e := strconv.Atoi(account.Uid)
	if e != nil {
		return e
	}
	gid, e := strconv.Atoi(account.Gid)
	if e != nil {
		return e
	}
	file, err := root.Open("challenge.json")
	if err != nil {
		return err
	}
	return errors.Join(file.Chown(uid, gid), file.Sync(), file.Close(), syncRoot(root))
}
func (d linuxDriver) ReadDrain(ctx context.Context) (DrainReceipt, error) {
	var receipt DrainReceipt
	root, err := safepath.OpenRoot(ctx, controlDirectory(d.paths.Data))
	if err != nil {
		return receipt, err
	}
	defer root.Close()
	body, err := readChecked(ctx, root, "drained.json", 2048)
	if err != nil {
		return receipt, err
	}
	if strictJSON(body, &receipt) != nil || receipt.Format != 1 || time.Since(receipt.CreatedAt) < 0 || time.Since(receipt.CreatedAt) > 3*time.Minute {
		return receipt, errors.New("manager drain receipt is invalid")
	}
	return receipt, nil
}
func (d linuxDriver) ClearControl(ctx context.Context) error {
	root, err := safepath.OpenRoot(ctx, controlDirectory(d.paths.Data))
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range []string{"challenge.json", "drained.json", "drained.pending", "request.json"} {
		if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return syncRoot(root)
}
func (d linuxDriver) WaitHealthy(ctx context.Context, installed Installed, nonce string) error {
	deadline, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("health redirects forbidden") }}
	defer client.CloseIdleConnections()
	for {
		if err := d.serviceImage(deadline, installed); err == nil {
			req, err := http.NewRequestWithContext(deadline, http.MethodGet, "http://127.0.0.1:8080/internal/update-ready", nil)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+nonce)
			resp, err := client.Do(req)
			if err == nil {
				body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4097))
				resp.Body.Close()
				var ready Readiness
				if readErr == nil && len(body) <= 4096 && resp.StatusCode == 200 && strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") && strictJSON(body, &ready) == nil && ready.Format == 1 && ready.Status == "ready" && ready.Version == installed.Version && ready.Schema == installed.Schema && ready.ExecutableSHA256 == installed.SHA256 {
					return nil
				}
			}
		}
		select {
		case <-deadline.Done():
			return errors.New("candidate manager readiness gate failed")
		case <-time.After(250 * time.Millisecond):
		}
	}
}
func (d linuxDriver) serviceImage(ctx context.Context, installed Installed) error {
	out, err := runFixed(ctx, "/usr/bin/systemctl", "show", "--property=MainPID", "--value", "minecraft-ui.service")
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid < 1 {
		return errors.New("manager service generation unavailable")
	}
	running, err := os.Stat("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return err
	}
	current, err := os.Stat(filepath.Join(d.paths.Bin, d.paths.Binary))
	if err != nil || !os.SameFile(running, current) {
		return errors.New("manager service executable generation mismatch")
	}
	return nil
}

// RunNativeUpdater is a separate root-only entrypoint. It never accepts paths,
// commands, URLs, users, service names or environment overrides from callers.
func RunNativeUpdater(ctx context.Context, args []string, out io.Writer) error {
	if os.Geteuid() != 0 {
		return errors.New("native updater requires root")
	}
	if len(args) < 1 {
		return errors.New("expected status, request <stage>, run or recover")
	}
	paths := platformPaths()
	if args[0] == "bootstrap" {
		if len(args) != 2 || len(args[1]) > 2048 {
			return errors.New("invalid installer capability receipt")
		}
		var capability Capability
		if strictJSON([]byte(args[1]), &capability) != nil || capability.Format != 1 || capability.Schema < 1 {
			return errors.New("invalid installer capability receipt")
		}
		state, e := openProtectedRoot(ctx, paths.State)
		if e != nil {
			return e
		}
		defer state.Close()
		if _, e := state.Lstat("installed.json"); !errors.Is(e, os.ErrNotExist) {
			return errors.New("bootstrap receipt already exists or is unsafe; use reviewed manual maintenance")
		}
		bin, e := openProtectedRoot(ctx, paths.Bin)
		if e != nil {
			return e
		}
		defer bin.Close()
		if e := protectedFile(bin, paths.Binary); e != nil {
			return e
		}
		hash, e := hashFile(ctx, bin, paths.Binary)
		if e != nil {
			return e
		}
		worker := activationWorker{state: state}
		return worker.recordInstalled(ctx, Installed{Format: 1, Version: capability.Version, Schema: capability.Schema, SHA256: hash})
	}
	if len(args) == 1 && args[0] == "recover" {
		return recoverLinux(ctx, paths)
	}
	trust, err := LoadNativeTrust(ctx)
	if err != nil {
		return err
	}
	state, err := openProtectedRoot(ctx, paths.State)
	if err != nil {
		return err
	}
	defer state.Close()
	// Caller-side sudo inherits the manager's read-only mount namespace. These
	// two operations perform no root-state mutation or lock creation. The fixed
	// root template imports/locks only after systemd starts it independently.
	if args[0] == "status" || args[0] == "request" {
		installed, e := readInstalled(ctx, state)
		if e != nil {
			return e
		}
		bin, e := openProtectedRoot(ctx, paths.Bin)
		if e != nil {
			return e
		}
		defer bin.Close()
		hash, e := hashFile(ctx, bin, paths.Binary)
		if e != nil || hash != installed.SHA256 {
			return errors.New("native installed receipt mismatch")
		}
		if args[0] == "status" {
			if e := checkPendingNative(ctx, state); e != nil {
				return e
			}
			if len(args) != 1 {
				return errors.New("invalid status arguments")
			}
			_, e = fmt.Fprintln(out, "ready")
			return e
		}
		if len(args) != 2 || !validHash(args[1]) {
			return errors.New("invalid request stage")
		}
		control, e := safepath.OpenRoot(ctx, controlDirectory(paths.Data))
		if e != nil {
			return e
		}
		defer control.Close()
		body, e := readChecked(ctx, control, "request.json", 2048)
		if e != nil {
			return e
		}
		var request ActivationRequest
		if strictJSON(body, &request) != nil || request.StageID != args[1] || request.ActorID < 1 || request.RequestedAt.After(time.Now()) || time.Since(request.RequestedAt) > time.Minute {
			return errors.New("invalid or stale activation request")
		}
		_, e = runFixed(ctx, "/usr/bin/systemctl", "start", "--no-block", "minecraft-ui-update@"+args[1]+".service")
		return e
	}
	release, err := launcher.AcquireManager(paths.State)
	if err != nil {
		return errors.New("another native update operation is active")
	}
	defer release()
	if err := protectedFile(state, "installed.json"); err != nil {
		return err
	}
	body, err := readChecked(ctx, state, "installed.json", 2048)
	if err != nil {
		return err
	}
	var installed Installed
	if strictJSON(body, &installed) != nil || installed.Format != 1 || !validHash(installed.SHA256) || installed.Schema < 1 {
		return errors.New("installed manager bootstrap receipt is invalid")
	}
	if _, err := ParseStable(installed.Version); err != nil {
		return errors.New("native self-update requires an initially qualified stable installation")
	}
	bin, err := openProtectedRoot(ctx, paths.Bin)
	if err != nil {
		return err
	}
	defer bin.Close()
	if err := protectedFile(bin, paths.Binary); err != nil {
		return err
	}
	worker := activationWorker{state: state, bin: bin, binary: paths.Binary, driver: linuxDriver{paths}}
	switch args[0] {
	case "run":
		if len(args) != 2 || !validHash(args[1]) {
			return errors.New("invalid run arguments")
		}
		if err := worker.cleanupTerminal(ctx, installed); err != nil {
			return err
		}
		if _, err := copyRequest(ctx, paths, state, args[1]); err != nil {
			return err
		}
		body, err := readChecked(ctx, state, "request.json", 2048)
		if err != nil {
			return err
		}
		var request ActivationRequest
		if strictJSON(body, &request) != nil || !validHash(request.StageID) || request.ActorID < 1 || request.RequestedAt.After(time.Now()) || time.Since(request.RequestedAt) > 5*time.Minute {
			return errors.New("invalid or expired imported update request")
		}
		sourcePath, err := StageDirectory(filepath.Join(paths.Data, "selfupdate", "stages"), request.StageID)
		if err != nil {
			return err
		}
		source, err := safepath.OpenRoot(ctx, sourcePath)
		if err != nil {
			return err
		}
		defer source.Close()
		metadata, err := readChecked(ctx, source, "release.json", MaxManifestBytes)
		if err != nil {
			return err
		}
		sig, err := readChecked(ctx, source, "release.sig", 1024)
		if err != nil {
			return err
		}
		var untrusted Manifest
		if strictJSON(metadata, &untrusted) != nil {
			return errors.New("invalid staged release metadata")
		}
		manifest, err := VerifyManifest(trust, metadata, sig, untrusted.Version, installed.Version, installed.Schema, time.Now())
		if err != nil {
			return err
		}
		artifact, _ := manifest.Artifact("linux", "amd64")
		archive, err := source.Open(artifact.Name)
		if err != nil {
			return err
		}
		defer archive.Close()
		stagesPath := filepath.Join(paths.State, "stages")
		stager := Stager{Dir: stagesPath, Trust: trust, CurrentVersion: installed.Version, Schema: installed.Schema, OS: "linux", Arch: "amd64"}
		staged, err := stager.Stage(ctx, manifest.Version, metadata, sig, archive)
		if err != nil {
			return err
		}
		if staged.ID != request.StageID {
			return errors.New("staged release identity does not match request")
		}
		candidate, err := safepath.OpenRoot(ctx, filepath.Join(stagesPath, staged.ID))
		if err != nil {
			return err
		}
		defer candidate.Close()
		nonce := make([]byte, 32)
		if _, err := rand.Read(nonce); err != nil {
			return err
		}
		journal := activationJournal{Request: request, Nonce: hex.EncodeToString(nonce), Prior: installed, Candidate: Installed{Format: 1, Version: manifest.Version, Schema: manifest.SchemaAfter, SHA256: artifact.BinarySHA256}}
		return worker.activate(ctx, journal, candidate)
	default:
		return errors.New("native updater operation is not allowed")
	}
}
