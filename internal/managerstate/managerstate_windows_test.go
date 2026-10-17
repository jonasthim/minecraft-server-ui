//go:build windows

package managerstate

import (
	"os"
	"path/filepath"
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

func TestExactPrivateDACLUsesBinaryAllowlist(t *testing.T) {
	managerSID := "S-1-5-21-111-222-333-1000"
	for _, test := range []struct {
		name string
		dacl string
		want bool
	}{
		{
			name: "alternate ACE order and numeric access mask",
			dacl: "D:P(A;OICI;0x001f01ff;;;SY)(A;OICI;FA;;;" + managerSID + ")(A;OICI;0x001f01ff;;;BA)",
			want: true,
		},
		{
			name: "exact generated descriptor",
			dacl: "D:P(A;OICI;FA;;;" + managerSID + ")(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)",
			want: true,
		},
		{
			name: "extra principal",
			dacl: "D:P(A;OICI;FA;;;" + managerSID + ")(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)(A;OICI;FA;;;WD)",
		},
		{
			name: "deny ACE",
			dacl: "D:P(D;OICI;FA;;;WD)(A;OICI;FA;;;" + managerSID + ")(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)",
		},
		{
			name: "reduced rights",
			dacl: "D:P(A;OICI;GW;;;" + managerSID + ")(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)",
		},
		{
			name: "unprotected DACL",
			dacl: "D:(A;OICI;FA;;;" + managerSID + ")(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)",
		},
		{
			name: "inherit only ACE",
			dacl: "D:P(A;IO;FA;;;" + managerSID + ")(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(test.dacl)
			if err != nil {
				t.Fatal("test security descriptor could not be built")
			}
			err = checkExactPrivateDACL(sd, managerSID)
			if (err == nil) != test.want {
				t.Fatalf("private DACL accepted = %v, want %v", err == nil, test.want)
			}
		})
	}
}

func TestGenericRootMkdirChildDoesNotMeetPrivateDirectoryContract(t *testing.T) {
	parentPath := t.TempDir()
	prepareManagerStateTestDirectory(t, parentPath)
	parent, err := os.OpenRoot(filepath.Clean(parentPath))
	if err != nil {
		t.Fatal("test parent directory could not be opened")
	}
	defer parent.Close()
	managerSID := testManagerStateSID(t)
	operation, _, err := createPrivateDirectory(parent, "operation", managerSID)
	if err != nil {
		t.Fatal("private operation directory could not be created")
	}
	defer operation.Close()

	if err := operation.Mkdir("inherited", 0700); err != nil {
		t.Fatal("generic child directory could not be created")
	}
	inheritedSD, err := windows.GetNamedSecurityInfo(filepath.Join(operation.Name(), "inherited"), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal("generic child DACL could not be read")
	}
	if err := checkExactPrivateDACL(inheritedSD, managerSID); err == nil {
		t.Fatal("generic inherited child unexpectedly has the protected private DACL")
	}
	if _, err := openCheckedDirectory(operation, "inherited", managerSID); err == nil {
		t.Fatal("generic inherited child unexpectedly met the protected private DACL contract")
	}
	if err := operation.Remove("inherited"); err != nil {
		t.Fatal("generic child fixture could not be removed")
	}

	private, _, err := createPrivateDirectory(operation, "explicit", managerSID)
	if err != nil {
		t.Fatal("explicit private child did not meet the protected private DACL contract")
	}
	defer private.Close()
}

func TestPlatformManagerSIDUsesBinaryWritableACEs(t *testing.T) {
	managerSID := testManagerStateSID(t)
	inheritedOnlySID := "S-1-5-21-998-997-996-1000"
	if inheritedOnlySID == managerSID {
		inheritedOnlySID = "S-1-5-21-998-997-996-1001"
	}
	for _, test := range []struct {
		name    string
		account string
		want    string
	}{
		{
			name:    "effective manager wins over inherited-only writable SID",
			account: "(A;OICI;0x001f01ff;;;" + managerSID + ")(A;OICIIO;0x001f01ff;;;" + inheritedOnlySID + ")",
			want:    managerSID,
		},
		{
			name:    "inherited-only SID is not an effective manager",
			account: "(A;OICIIO;0x001f01ff;;;" + inheritedOnlySID + ")",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := t.TempDir()
			setManagerStateTestDACL(t, path, "D:P"+test.account+"(A;OICI;0x001f01ff;;;BA)(A;OICI;0x001f01ff;;;SY)")
			root, err := os.OpenRoot(filepath.Clean(path))
			if err != nil {
				t.Fatal("test data directory could not be opened")
			}
			defer root.Close()
			got, err := platformManagerSID(root)
			if test.want == "" {
				if err == nil || got != "" {
					t.Fatal("inherited-only SID was accepted as an effective manager")
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("manager SID lookup returned expected=%v", err == nil && got == test.want)
			}
		})
	}
}

func TestMaskGrantsWriteIncludesDirectoryDeleteChild(t *testing.T) {
	const fileDeleteChild = uint32(0x00000040)
	for _, test := range []struct {
		name string
		mask uint32
		want bool
	}{
		{name: "delete children", mask: fileDeleteChild, want: true},
		{name: "write file data", mask: windows.FILE_WRITE_DATA, want: true},
		{name: "read only", mask: windows.FILE_READ_DATA, want: false},
		{name: "execute only", mask: windows.FILE_EXECUTE, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := maskGrantsWrite(test.mask); got != test.want {
				t.Fatalf("write classification = %v, want %v", got, test.want)
			}
		})
	}
}

func setManagerStateTestDACL(t *testing.T, path, sddl string) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
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
