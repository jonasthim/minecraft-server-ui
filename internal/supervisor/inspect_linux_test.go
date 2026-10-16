//go:build linux

package supervisor

import (
	"context"
	"testing"
)

func TestPrivilegedInspectionRejectsUnsafeIDBeforeHostAccess(t *testing.T) {
	if _, err := NativeInspect(context.Background(), "../manager"); err == nil {
		t.Fatal("unsafe instance ID accepted")
	}
}
func TestInspectionRequiresDirectChildCgroupAndRootCreatedNamespace(t *testing.T) {
	valid := inspectionScope{LauncherPID: 100, JVMPID: 101, JVMParent: 100, UnitGroup: "/system.slice/system-minecraft.slice/minecraft@test.service", LauncherGroup: "/system.slice/system-minecraft.slice/minecraft@test.service", JVMGroup: "/system.slice/system-minecraft.slice/minecraft@test.service", InspectorNamespace: "1:1", LauncherNamespace: "1:2", JVMNamespace: "1:2", NamespaceOwnerUID: 0}
	if err := validateInspectionScope(valid); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*inspectionScope){func(s *inspectionScope) { s.JVMParent = 999 }, func(s *inspectionScope) { s.JVMGroup = "/system.slice/other.service" }, func(s *inspectionScope) { s.LauncherGroup = "/another" }, func(s *inspectionScope) { s.LauncherNamespace = s.InspectorNamespace }, func(s *inspectionScope) { s.JVMNamespace = "1:3" }, func(s *inspectionScope) { s.NamespaceOwnerUID = 1000 }} {
		bad := valid
		mutate(&bad)
		if err := validateInspectionScope(bad); err == nil {
			t.Fatal("unscoped privileged inspection allowed", bad)
		}
	}
}
