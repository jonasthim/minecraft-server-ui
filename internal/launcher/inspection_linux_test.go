//go:build linux

package launcher

import (
	"context"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"testing"
)

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
