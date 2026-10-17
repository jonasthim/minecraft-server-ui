//go:build windows

package managerstate

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestPrivateDirectoryOwnerUsesExactSIDAndWellKnownOwners(t *testing.T) {
	managerSID := "S-1-5-21-111-222-333-1000"
	for _, test := range []struct {
		name string
		sid  string
		want bool
	}{
		{"manager", managerSID, true},
		{"numeric prefix collision", managerSID + "0", false},
		{"administrators alias target", "S-1-5-32-544", true},
		{"system alias target", "S-1-5-18", true},
		{"builtin users", "S-1-5-32-545", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner, err := windows.StringToSid(test.sid)
			if err != nil {
				t.Fatal("test SID could not be parsed")
			}
			if got := privateDirectoryOwnerAllowed(owner, managerSID); got != test.want {
				t.Fatalf("owner decision = %v, want %v", got, test.want)
			}
		})
	}
}

func testManagerStateSID(t *testing.T) string {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		t.Fatal("current test account SID is unavailable")
	}
	return user.User.Sid.String()
}

func prepareManagerStateTestDirectory(t *testing.T, path string) {
	t.Helper()
	managerSID := testManagerStateSID(t)
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
