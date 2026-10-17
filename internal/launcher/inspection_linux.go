//go:build linux

package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func inspectIdentity(ctx context.Context, id domain.ProcessIdentity) (string, string, string, error) {
	return inspectIdentityUsing(ctx, id, processIdentity, readBrokerIdentity)
}
func inspectIdentityUsing(ctx context.Context, id domain.ProcessIdentity, read func(int) (string, string, string, error), broker func(context.Context, domain.ProcessIdentity) (NativeInspection, error)) (string, string, string, error) {
	start, exe, cwd, err := read(id.PID)
	if start != "" && start != id.StartIdentity || IsMissingProcess(err) {
		return start, exe, cwd, err
	}
	// Even readable metadata is insufficient for a pre-hardening JVM: the loaded
	// template can change while the old process retains its original namespace.
	if id.WorkingDir != "/var/lib/minecraft-ui/instances/"+id.InstanceID+"/server" {
		return start, exe, cwd, err
	}
	if !filepath.IsAbs(id.Executable) || !stringsHasRuntimePrefix(id.Executable) {
		return start, "", "", fmt.Errorf("managed JVM is outside the fixed runtime tree")
	}
	result, e := broker(ctx, id)
	if e != nil {
		if ProcessIdentityFailureStage(e) == "" {
			e = processIdentityStageFailure(processIdentityBrokerInvoke, e)
		}
		return reconcileInspectionFailure(ctx, id, start, exe, cwd, e, read)
	}
	if e = validateBrokerIdentity(id, result); e != nil {
		e = processIdentityStageFailure(processIdentityBrokerIdentity, e)
		return reconcileInspectionFailure(ctx, id, start, exe, cwd, e, read)
	}
	return result.StartIdentity, result.Executable, result.WorkingDir, nil
}

// A privileged inspection can fail after the JVM exits, even when the initial
// unprivileged proc read succeeded. One direct read may conclusively establish
// that the original process ended; every ambiguous result keeps the broker or
// validation error so a live process never bypasses isolation proof.
func reconcileInspectionFailure(ctx context.Context, id domain.ProcessIdentity, start, exe, cwd string, original error, read func(int) (string, string, string, error)) (string, string, string, error) {
	if ctx.Err() != nil || errors.Is(original, context.Canceled) || errors.Is(original, context.DeadlineExceeded) {
		return start, exe, cwd, original
	}
	currentStart, currentExe, currentCwd, err := read(id.PID)
	if ctx.Err() != nil {
		return start, exe, cwd, original
	}
	if currentStart != "" && currentStart != id.StartIdentity {
		return currentStart, currentExe, currentCwd, nil
	}
	if IsMissingProcess(err) {
		return currentStart, currentExe, currentCwd, err
	}
	return start, exe, cwd, original
}

func readBrokerIdentity(ctx context.Context, id domain.ProcessIdentity) (NativeInspection, error) {
	return readBrokerIdentityUsing(ctx, id, safeBrokerPath, func(ctx context.Context, id domain.ProcessIdentity) ([]byte, error) {
		bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(bounded, "/usr/bin/sudo", "-n", "/usr/local/lib/minecraft-ui/unitctl", "inspect", id.InstanceID)
		output := new(boundedInspectionOutput)
		cmd.Stdout = output
		cmd.Stderr = io.Discard
		if e := cmd.Run(); e != nil {
			return nil, fmt.Errorf("privileged instance identity inspection: %w", e)
		}
		return append([]byte(nil), output.Bytes()...), nil
	})
}

func readBrokerIdentityUsing(ctx context.Context, id domain.ProcessIdentity, checkPath func(string) error, invoke func(context.Context, domain.ProcessIdentity) ([]byte, error)) (NativeInspection, error) {
	for _, path := range []string{"/usr/local/lib/minecraft-ui/unitctl", "/usr/local/bin/minecraft-ui"} {
		if e := checkPath(path); e != nil {
			return NativeInspection{}, processIdentityStageFailure(processIdentityBrokerPath, e)
		}
	}
	output, e := invoke(ctx, id)
	if e != nil {
		if ProcessIdentityFailureStage(e) == "" {
			e = processIdentityStageFailure(processIdentityBrokerInvoke, e)
		}
		return NativeInspection{}, e
	}
	var result NativeInspection
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(&result); e != nil {
		return NativeInspection{}, processIdentityStageFailure(processIdentityBrokerDecode, e)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return NativeInspection{}, processIdentityStageFailure(processIdentityBrokerDecode, fmt.Errorf("extra broker output"))
	}
	return result, nil
}
func stringsHasRuntimePrefix(path string) bool {
	rel, err := filepath.Rel("/var/lib/minecraft-ui/runtimes", path)
	return err == nil && filepath.Clean(path) == path && rel != "." && rel != ".." && !filepath.IsAbs(rel) && len(rel) > 3 && rel[:3] != "../"
}
func validateBrokerIdentity(id domain.ProcessIdentity, result NativeInspection) error {
	if result.InstanceID != id.InstanceID || result.PID != id.PID || result.LauncherPID != id.LauncherPID || result.StartIdentity != id.StartIdentity || !equalExecutable(result.Executable, id.Executable) || !sameWorkingDirectory(result.WorkingDir, id) || result.UserNamespace == "" || len(result.InvocationID) != 32 {
		return fmt.Errorf("scoped broker identity does not match the expected JVM")
	}
	return nil
}

type boundedInspectionOutput struct{ bytes.Buffer }

func (b *boundedInspectionOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 16384 {
		return 0, fmt.Errorf("broker output exceeds bound")
	}
	return b.Buffer.Write(p)
}
func safeBrokerPath(path string) error {
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 || p == path && !info.Mode().IsRegular() {
			return fmt.Errorf("privileged broker path is not a root-owned immutable regular installation")
		}
		if filepath.Dir(p) == p {
			return nil
		}
	}
}
