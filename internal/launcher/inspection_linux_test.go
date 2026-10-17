//go:build linux

package launcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

const inspectionExitChildEnv = "MINECRAFT_UI_INSPECTION_EXIT_CHILD"

func TestInspectionExitRaceChild(t *testing.T) {
	if os.Getenv(inspectionExitChildEnv) != "1" {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}

func startInspectionExitRaceChild(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestInspectionExitRaceChild$")
	cmd.Env = append(os.Environ(), inspectionExitChildEnv+"=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState != nil {
			return
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

func TestBrokerResultMustMatchCompleteExpectedJVM(t *testing.T) {
	expected := domain.ProcessIdentity{InstanceID: "test", PID: 101, LauncherPID: 100, StartIdentity: "boot:start", Executable: "/var/lib/minecraft-ui/runtimes/java/bin/java", WorkingDir: "/var/lib/minecraft-ui/instances/test/server"}
	valid := NativeInspection{InstanceID: expected.InstanceID, PID: expected.PID, LauncherPID: expected.LauncherPID, StartIdentity: expected.StartIdentity, Executable: expected.Executable, WorkingDir: expected.WorkingDir, UserNamespace: "1:2", InvocationID: "12345678901234567890123456789012"}
	if err := validateBrokerIdentity(expected, valid); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*NativeInspection){func(i *NativeInspection) { i.PID++ }, func(i *NativeInspection) { i.LauncherPID++ }, func(i *NativeInspection) { i.StartIdentity = "reused" }, func(i *NativeInspection) { i.InstanceID = "other" }, func(i *NativeInspection) { i.Executable = "/tmp/foreign" }, func(i *NativeInspection) { i.WorkingDir = "/tmp" }, func(i *NativeInspection) { i.UserNamespace = "" }, func(i *NativeInspection) { i.InvocationID = "" }} {
		bad := valid
		mutate(&bad)
		if err := validateBrokerIdentity(expected, bad); err == nil {
			t.Fatalf("unscoped identity accepted: %+v", bad)
		}
	}
}
func TestProbeHonorsCanceledContextBeforeProcessOrBrokerAccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ProbeIdentityContext(ctx, domain.ProcessIdentity{}); err != context.Canceled {
		t.Fatalf("error=%v", err)
	}
}

func TestReadablePreHardeningJVMStillRequiresActiveIsolationProof(t *testing.T) {
	expected := domain.ProcessIdentity{InstanceID: "test", PID: 101, LauncherPID: 100, StartIdentity: "boot:start", Executable: "/var/lib/minecraft-ui/runtimes/java/bin/java", WorkingDir: "/var/lib/minecraft-ui/instances/test/server"}
	inspected := false
	_, _, _, err := inspectIdentityUsing(context.Background(), expected, func(int) (string, string, string, error) {
		return expected.StartIdentity, expected.Executable, expected.WorkingDir, nil
	}, func(context.Context, domain.ProcessIdentity) (NativeInspection, error) {
		inspected = true
		return NativeInspection{}, fmt.Errorf("loaded template changed but actual private namespace is absent")
	})
	if !inspected || err == nil {
		t.Fatal("readable legacy process bypassed actual isolation proof")
	}
}

func TestInspectionBrokerFailureRechecksAndConfirmsExitedProcess(t *testing.T) {
	cmd := startInspectionExitRaceChild(t)
	initialStart, _, _, err := processIdentity(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	id := domain.ProcessIdentity{
		InstanceID: "exit-probe", PID: cmd.Process.Pid, StartIdentity: initialStart,
		Executable: "/var/lib/minecraft-ui/runtimes/test/bin/java",
		WorkingDir: "/var/lib/minecraft-ui/instances/exit-probe/server",
	}
	var reads, brokerCalls atomic.Int32
	brokerErr := errors.New("injected privileged broker failure")
	read := func(pid int) (string, string, string, error) {
		reads.Add(1)
		return processIdentity(pid)
	}
	broker := func(context.Context, domain.ProcessIdentity) (NativeInspection, error) {
		brokerCalls.Add(1)
		if err := cmd.Process.Kill(); err != nil {
			return NativeInspection{}, err
		}
		_ = cmd.Wait()
		return NativeInspection{}, brokerErr
	}
	_, _, _, err = inspectIdentityUsing(context.Background(), id, read, broker)
	if !errors.Is(err, os.ErrNotExist) || !IsMissingProcess(err) {
		t.Fatalf("direct post-broker identity read did not prove exit: %v", err)
	}
	if ProcessIdentityFailureStage(err) != "" {
		t.Fatalf("authoritative missing-process recheck received failure stage %q", ProcessIdentityFailureStage(err))
	}
	if reads.Load() != 2 || brokerCalls.Load() != 1 {
		t.Fatalf("unexpected probe counts: direct_reads=%d broker_calls=%d", reads.Load(), brokerCalls.Load())
	}
}

func TestInspectionBrokerFailurePreservesLiveOrUnknownIdentityFailure(t *testing.T) {
	t.Run("missing broker path is not process exit", func(t *testing.T) {
		id := domain.ProcessIdentity{
			InstanceID: "exit-probe", PID: 123, StartIdentity: "boot:start",
			Executable: "/var/lib/minecraft-ui/runtimes/test/bin/java",
			WorkingDir: "/var/lib/minecraft-ui/instances/exit-probe/server",
		}
		pathErr := &os.PathError{Op: "open", Path: "/usr/local/lib/minecraft-ui/unitctl", Err: syscall.ENOENT}
		brokerErr := processIdentityStageFailure(processIdentityBrokerPath, pathErr)
		reads := 0
		_, _, _, err := inspectIdentityUsing(context.Background(), id, func(int) (string, string, string, error) {
			reads++
			return id.StartIdentity, id.Executable, id.WorkingDir, nil
		}, func(context.Context, domain.ProcessIdentity) (NativeInspection, error) {
			return NativeInspection{}, brokerErr
		})
		if err == nil || !errors.Is(err, syscall.ENOENT) || IsMissingProcess(err) || reads != 2 || ProcessIdentityFailureStage(err) != "broker_path" {
			t.Fatalf("broker path error was misclassified after live recheck: stage=%q missing=%v reads=%d err=%v", ProcessIdentityFailureStage(err), IsMissingProcess(err), reads, err)
		}
	})

	t.Run("same live process", func(t *testing.T) {
		cmd := startInspectionExitRaceChild(t)
		start, _, _, err := processIdentity(cmd.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		id := domain.ProcessIdentity{
			InstanceID: "exit-probe", PID: cmd.Process.Pid, StartIdentity: start,
			Executable: "/var/lib/minecraft-ui/runtimes/test/bin/java",
			WorkingDir: "/var/lib/minecraft-ui/instances/exit-probe/server",
		}
		var reads, brokerCalls atomic.Int32
		brokerErr := errors.New("injected broker failure")
		_, _, _, err = inspectIdentityUsing(context.Background(), id, func(pid int) (string, string, string, error) {
			reads.Add(1)
			return processIdentity(pid)
		}, func(context.Context, domain.ProcessIdentity) (NativeInspection, error) {
			brokerCalls.Add(1)
			return NativeInspection{}, brokerErr
		})
		if !errors.Is(err, brokerErr) || IsMissingProcess(err) || ProcessIdentityFailureStage(err) != "broker_invoke" {
			t.Fatalf("live process broker failure was ignored: %v", err)
		}
		if reads.Load() != 2 || brokerCalls.Load() != 1 {
			t.Fatalf("unexpected probe counts: direct_reads=%d broker_calls=%d", reads.Load(), brokerCalls.Load())
		}
	})

	t.Run("malformed direct recheck", func(t *testing.T) {
		id := domain.ProcessIdentity{
			InstanceID: "exit-probe", PID: 123, StartIdentity: "boot:start",
			Executable: "/var/lib/minecraft-ui/runtimes/test/bin/java",
			WorkingDir: "/var/lib/minecraft-ui/instances/exit-probe/server",
		}
		calls := 0
		brokerErr := errors.New("injected broker failure")
		_, _, _, err := inspectIdentityUsing(context.Background(), id, func(int) (string, string, string, error) {
			calls++
			if calls == 1 {
				return id.StartIdentity, id.Executable, id.WorkingDir, nil
			}
			return "", "", "", errors.New("malformed proc identity")
		}, func(context.Context, domain.ProcessIdentity) (NativeInspection, error) {
			return NativeInspection{}, brokerErr
		})
		if !errors.Is(err, brokerErr) || IsMissingProcess(err) || calls != 2 || ProcessIdentityFailureStage(err) != "broker_invoke" {
			t.Fatalf("unknown direct recheck replaced broker failure: calls=%d error=%v", calls, err)
		}
	})

	t.Run("permission denied direct recheck", func(t *testing.T) {
		id := domain.ProcessIdentity{
			InstanceID: "exit-probe", PID: 123, StartIdentity: "boot:start",
			Executable: "/var/lib/minecraft-ui/runtimes/test/bin/java",
			WorkingDir: "/var/lib/minecraft-ui/instances/exit-probe/server",
		}
		calls := 0
		brokerErr := errors.New("injected broker failure")
		_, _, _, err := inspectIdentityUsing(context.Background(), id, func(int) (string, string, string, error) {
			calls++
			if calls == 1 {
				return id.StartIdentity, id.Executable, id.WorkingDir, nil
			}
			return "", "", "", os.ErrPermission
		}, func(context.Context, domain.ProcessIdentity) (NativeInspection, error) {
			return NativeInspection{}, brokerErr
		})
		if !errors.Is(err, brokerErr) || IsMissingProcess(err) || calls != 2 || ProcessIdentityFailureStage(err) != "broker_invoke" {
			t.Fatalf("permission failure at direct recheck replaced broker failure: calls=%d error=%v", calls, err)
		}
	})

	t.Run("changed start identity proves original exited", func(t *testing.T) {
		id := domain.ProcessIdentity{
			InstanceID: "exit-probe", PID: 123, StartIdentity: "boot:original",
			Executable: "/var/lib/minecraft-ui/runtimes/test/bin/java",
			WorkingDir: "/var/lib/minecraft-ui/instances/exit-probe/server",
		}
		calls := 0
		brokerErr := errors.New("injected broker failure")
		start, _, _, err := inspectIdentityUsing(context.Background(), id, func(int) (string, string, string, error) {
			calls++
			if calls == 1 {
				return id.StartIdentity, id.Executable, id.WorkingDir, nil
			}
			return "boot:replacement", id.Executable, id.WorkingDir, nil
		}, func(context.Context, domain.ProcessIdentity) (NativeInspection, error) {
			return NativeInspection{}, brokerErr
		})
		if err != nil || start != "boot:replacement" || calls != 2 {
			t.Fatalf("changed start identity did not prove original exit: start=%q calls=%d error=%v", start, calls, err)
		}
	})

	t.Run("canceled broker is not replaced", func(t *testing.T) {
		id := domain.ProcessIdentity{
			InstanceID: "exit-probe", PID: 123, StartIdentity: "boot:start",
			Executable: "/var/lib/minecraft-ui/runtimes/test/bin/java",
			WorkingDir: "/var/lib/minecraft-ui/instances/exit-probe/server",
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		_, _, _, err := inspectIdentityUsing(ctx, id, func(int) (string, string, string, error) {
			calls++
			return id.StartIdentity, id.Executable, id.WorkingDir, nil
		}, func(context.Context, domain.ProcessIdentity) (NativeInspection, error) {
			cancel()
			return NativeInspection{}, context.Canceled
		})
		if !errors.Is(err, context.Canceled) || calls != 1 || ProcessIdentityFailureStage(err) != "broker_invoke" {
			t.Fatalf("cancellation was replaced by a new identity read: calls=%d error=%v", calls, err)
		}
	})

	t.Run("cancellation during recheck is preserved", func(t *testing.T) {
		id := domain.ProcessIdentity{
			InstanceID: "exit-probe", PID: 123, StartIdentity: "boot:start",
			Executable: "/var/lib/minecraft-ui/runtimes/test/bin/java",
			WorkingDir: "/var/lib/minecraft-ui/instances/exit-probe/server",
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		brokerErr := errors.New("injected broker failure")
		_, _, _, err := inspectIdentityUsing(ctx, id, func(int) (string, string, string, error) {
			calls++
			if calls == 2 {
				cancel()
				return "", "", "", os.ErrNotExist
			}
			return id.StartIdentity, id.Executable, id.WorkingDir, nil
		}, func(context.Context, domain.ProcessIdentity) (NativeInspection, error) {
			return NativeInspection{}, brokerErr
		})
		if !errors.Is(err, brokerErr) || IsMissingProcess(err) || calls != 2 || ProcessIdentityFailureStage(err) != "broker_invoke" {
			t.Fatalf("canceled direct recheck replaced broker failure: calls=%d error=%v", calls, err)
		}
	})
}

func TestInspectionValidationFailureRechecksButPreservesLiveProcess(t *testing.T) {
	cmd := startInspectionExitRaceChild(t)
	start, _, _, err := processIdentity(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	id := domain.ProcessIdentity{
		InstanceID: "exit-probe", PID: cmd.Process.Pid, StartIdentity: start,
		Executable: "/var/lib/minecraft-ui/runtimes/test/bin/java",
		WorkingDir: "/var/lib/minecraft-ui/instances/exit-probe/server",
	}
	var reads atomic.Int32
	_, _, _, err = inspectIdentityUsing(context.Background(), id, func(pid int) (string, string, string, error) {
		reads.Add(1)
		return processIdentity(pid)
	}, func(context.Context, domain.ProcessIdentity) (NativeInspection, error) {
		return NativeInspection{InstanceID: id.InstanceID, PID: id.PID + 1, LauncherPID: id.LauncherPID,
			StartIdentity: id.StartIdentity, Executable: id.Executable, WorkingDir: id.WorkingDir,
			UserNamespace: "1:2", InvocationID: "12345678901234567890123456789012"}, nil
	})
	if err == nil || IsMissingProcess(err) || reads.Load() != 2 || ProcessIdentityFailureStage(err) != "broker_identity" {
		t.Fatalf("invalid isolation proof was ignored for live process: reads=%d error=%v", reads.Load(), err)
	}
}
