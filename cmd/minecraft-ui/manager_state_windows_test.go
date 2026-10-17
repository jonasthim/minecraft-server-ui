//go:build windows

package main

import (
	"testing"

	"golang.org/x/sys/windows"
)

func prepareManagerStateCommandTestDirectory(t *testing.T, path string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		t.Fatal("current test account SID is unavailable")
	}
	managerSID := user.User.Sid.String()
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + managerSID + ")(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)")
	if err != nil {
		t.Fatal("test data DACL could not be built")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal("test data DACL could not be read")
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal("test data DACL could not be applied")
	}
}
