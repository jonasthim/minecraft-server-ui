//go:build linux

package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func TestProcessIdentityLinuxStagesIdentifyProcReadBoundaries(t *testing.T) {
	cause := errors.New("private proc detail")
	statFields := []string{"S"}
	for i := 1; i < 20; i++ {
		statFields = append(statFields, "1")
	}
	validStat := []byte("123 (test process) " + strings.Join(statFields, " "))

	tests := []struct {
		name      string
		failAt    string
		wantStage string
	}{
		{name: "boot identity", failAt: "boot", wantStage: "boot_identity"},
		{name: "process stat", failAt: "stat", wantStage: "proc_stat"},
		{name: "process executable", failAt: "exe", wantStage: "proc_executable"},
		{name: "process working directory", failAt: "cwd", wantStage: "proc_working_directory"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			readFile := func(path string) ([]byte, error) {
				switch path {
				case "/proc/sys/kernel/random/boot_id":
					if tc.failAt == "boot" {
						return nil, cause
					}
					return []byte("12345678-1234-1234-1234-123456789abc"), nil
				case "/proc/4321/stat":
					if tc.failAt == "stat" {
						return nil, cause
					}
					return validStat, nil
				default:
					t.Fatalf("unexpected proc read path %q", path)
					return nil, nil
				}
			}
			readlink := func(path string) (string, error) {
				if path == "/proc/4321/exe" {
					if tc.failAt == "exe" {
						return "", cause
					}
					return "/usr/bin/java", nil
				}
				if path == "/proc/4321/cwd" {
					if tc.failAt == "cwd" {
						return "", cause
					}
					return "/var/lib/minecraft-ui/instances/test/server", nil
				}
				t.Fatalf("unexpected proc link path %q", path)
				return "", nil
			}
			_, _, _, err := processIdentityUsing(4321, readFile, readlink)
			if err == nil || ProcessIdentityFailureStage(err) != tc.wantStage || !errors.Is(err, cause) {
				t.Fatalf("stage=%q err=%v, want stage=%q and original cause", ProcessIdentityFailureStage(err), err, tc.wantStage)
			}
		})
	}
}

func TestProcessIdentityLinuxRejectsLiveMetadataWithFixedStage(t *testing.T) {
	start, executable, workingDir, err := processIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	id := domain.ProcessIdentity{
		InstanceID:    "stage-check",
		PID:           os.Getpid(),
		StartIdentity: start,
		Executable:    filepath.Clean(executable) + ".unexpected",
		WorkingDir:    workingDir,
	}
	_, err = ProbeIdentityContext(context.Background(), id)
	if err == nil || ProcessIdentityFailureStage(err) != "process_metadata" {
		t.Fatalf("stage=%q err=%v, want process_metadata", ProcessIdentityFailureStage(err), err)
	}
}

func TestMissingProcessErrorsRemainUnstaged(t *testing.T) {
	statMissing := &os.PathError{Op: "open", Path: "/proc/4321/stat", Err: syscall.ENOENT}
	got := linuxProcessIdentityFailure(processIdentityProcStat, statMissing)
	if !IsMissingProcess(got) || ProcessIdentityFailureStage(got) != "" {
		t.Fatalf("process exit should remain conclusive and un-staged: stage=%q err=%v", ProcessIdentityFailureStage(got), got)
	}

	bootMissing := &os.PathError{Op: "open", Path: "/proc/sys/kernel/random/boot_id", Err: syscall.ENOENT}
	_, _, _, err := processIdentityUsing(4321, func(string) ([]byte, error) { return nil, bootMissing }, func(string) (string, error) { return "", nil })
	if err == nil || IsMissingProcess(err) || ProcessIdentityFailureStage(err) != "boot_identity" || !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("missing boot identity was misclassified as process exit: stage=%q err=%v", ProcessIdentityFailureStage(err), err)
	}
}

func TestReadBrokerIdentityUsingClassifiesBoundedBoundaries(t *testing.T) {
	id := domain.ProcessIdentity{InstanceID: "broker-test"}
	cause := errors.New("private broker cause")
	good, err := json.Marshal(NativeInspection{InstanceID: id.InstanceID})
	if err != nil {
		t.Fatal(err)
	}
	check := func(string) error { return nil }
	t.Run("unsafe broker path", func(t *testing.T) {
		invoked := false
		_, err := readBrokerIdentityUsing(context.Background(), id, func(string) error { return cause }, func(context.Context, domain.ProcessIdentity) ([]byte, error) {
			invoked = true
			return good, nil
		})
		if ProcessIdentityFailureStage(err) != "broker_path" || !errors.Is(err, cause) || invoked {
			t.Fatalf("stage=%q invoked=%v err=%v", ProcessIdentityFailureStage(err), invoked, err)
		}
	})
	t.Run("broker invocation", func(t *testing.T) {
		_, err := readBrokerIdentityUsing(context.Background(), id, check, func(context.Context, domain.ProcessIdentity) ([]byte, error) { return nil, cause })
		if ProcessIdentityFailureStage(err) != "broker_invoke" || !errors.Is(err, cause) {
			t.Fatalf("stage=%q err=%v", ProcessIdentityFailureStage(err), err)
		}
	})
	t.Run("broker decode", func(t *testing.T) {
		_, err := readBrokerIdentityUsing(context.Background(), id, check, func(context.Context, domain.ProcessIdentity) ([]byte, error) { return []byte("{"), nil })
		if ProcessIdentityFailureStage(err) != "broker_decode" || err == nil {
			t.Fatalf("stage=%q err=%v", ProcessIdentityFailureStage(err), err)
		}
	})
	t.Run("valid broker output", func(t *testing.T) {
		got, err := readBrokerIdentityUsing(context.Background(), id, check, func(context.Context, domain.ProcessIdentity) ([]byte, error) { return good, nil })
		if err != nil || got.InstanceID != id.InstanceID {
			t.Fatalf("result=%+v err=%v", got, err)
		}
	})
}
