//go:build windows

package selfupdate

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

const processQueryFixtureSID = "S-1-5-21-1-2-3-1001"
const privateProcessFixture = "O:SYG:SYD:P(A;;0x1fffff;;;SY)(A;;0x1fffff;;;BA)"

type processQueryTestACE struct {
	sid           string
	mask          uint32
	typeID, flags byte
}

func processQueryTestEntries(t *testing.T, dacl *windows.ACL) []processQueryTestACE {
	t.Helper()
	var entries []processQueryTestACE
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatal("read fixture ACE:", err)
		}
		entries = append(entries, processQueryTestACE{(*windows.SID)(unsafe.Pointer(&ace.SidStart)).String(), uint32(ace.Mask), ace.Header.AceType, ace.Header.AceFlags})
	}
	return entries
}

func processQueryTestDescriptor(t *testing.T, text string) *windows.SECURITY_DESCRIPTOR {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(text)
	if err != nil {
		t.Fatal("parse fixed process descriptor:", err)
	}
	return sd
}

func processQueryTestMergedDescriptor(t *testing.T, original *windows.SECURITY_DESCRIPTOR, acl *windows.ACL) *windows.SECURITY_DESCRIPTOR {
	t.Helper()
	owner, _, _ := original.Owner()
	group, _, _ := original.Group()
	sd, err := windows.NewSecurityDescriptor()
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.SetOwner(owner, false); err != nil {
		t.Fatal(err)
	}
	if err := sd.SetGroup(group, false); err != nil {
		t.Fatal(err)
	}
	if err := sd.SetDACL(acl, true, false); err != nil {
		t.Fatal(err)
	}
	return sd
}

func TestUpdaterProcessQueryMergeAddsOnlyExactManagerGrant(t *testing.T) {
	manager, err := managerProcessQuerySID(processQueryFixtureSID)
	if err != nil {
		t.Fatal("valid fixture SID rejected")
	}
	for _, fixture := range []string{
		privateProcessFixture,
		"O:BAG:SYD:P(D;;0x1;;;WD)(A;;0x1fffff;;;SY)(A;;0x1fffff;;;BA)S:(ML;;NW;;;HI)",
	} {
		sd := processQueryTestDescriptor(t, fixture)
		beforeText := sd.String()
		original, _, _ := sd.DACL()
		before := processQueryTestEntries(t, original)
		merged, err := mergeUpdaterProcessQuery(sd, manager)
		if err != nil {
			t.Fatal("merge rejected valid private process fixture:", err)
		}
		if sd.String() != beforeText {
			t.Fatal("merge changed original owner/group/control/SACL/DACL")
		}
		var preserved []processQueryTestACE
		count := 0
		for _, entry := range processQueryTestEntries(t, merged) {
			if entry.sid == processQueryFixtureSID {
				count++
				if entry.mask != 0x1000 || entry.typeID != 0 || entry.flags != 0 {
					t.Fatal("manager received more than explicit query-limited access")
				}
			} else {
				preserved = append(preserved, entry)
			}
		}
		if count != 1 || !reflect.DeepEqual(preserved, before) {
			t.Fatal("merge changed existing principals, masks, flags, ACE order or denies")
		}
		candidate := processQueryTestMergedDescriptor(t, sd, merged)
		if _, _, err := processQueryACL(candidate, manager, true); err != nil {
			t.Fatal("merged grant failed strict policy")
		}
		again, err := mergeUpdaterProcessQuery(candidate, manager)
		if err != nil || !reflect.DeepEqual(processQueryTestEntries(t, merged), processQueryTestEntries(t, again)) {
			t.Fatal("repeat grant changed process permissions")
		}
	}
}

func TestUpdaterProcessQueryRejectsUnsafeDescriptors(t *testing.T) {
	manager, _ := managerProcessQuerySID(processQueryFixtureSID)
	for _, tt := range []struct{ name, descriptor string }{
		{"absent_dacl", "O:SYG:SY"},
		{"null_dacl", "O:SYG:SYD:NO_ACCESS_CONTROL"},
		{"empty_dacl", "O:SYG:SYD:"},
		{"manager_owner_has_implicit_dacl_control", "O:" + processQueryFixtureSID + "G:SYD:(A;;0x1fffff;;;SY)(A;;0x1fffff;;;BA)"},
		{"missing_group", "O:SYD:(A;;0x1fffff;;;SY)(A;;0x1fffff;;;BA)"},
		{"missing_system", "O:SYG:SYD:(A;;0x1fffff;;;BA)"},
		{"missing_administrators", "O:SYG:SYD:(A;;0x1fffff;;;SY)"},
		{"broad_everyone_allow", privateProcessFixture + "(A;;0x1000;;;WD)"},
		{"broad_users_allow", privateProcessFixture + "(A;;0x1fffff;;;BU)"},
		{"authenticated_users_allow", privateProcessFixture + "(A;;0x1000;;;AU)"},
		{"unexpected_logon_allow", privateProcessFixture + "(A;;0x1000;;;S-1-5-5-1-2)"},
		{"manager_terminate", privateProcessFixture + "(A;;0x1;;;" + processQueryFixtureSID + ")"},
		{"manager_vm_read", privateProcessFixture + "(A;;0x10;;;" + processQueryFixtureSID + ")"},
		{"manager_duplicate_handle", privateProcessFixture + "(A;;0x40;;;" + processQueryFixtureSID + ")"},
		{"manager_change_dacl", privateProcessFixture + "(A;;0x40000;;;" + processQueryFixtureSID + ")"},
		{"manager_generic_read", privateProcessFixture + "(A;;GR;;;" + processQueryFixtureSID + ")"},
		{"manager_inherit_only", privateProcessFixture + "(A;IOCI;0x1000;;;" + processQueryFixtureSID + ")"},
		{"manager_duplicate_grants", privateProcessFixture + "(A;;0x1000;;;" + processQueryFixtureSID + ")(A;;0x1000;;;" + processQueryFixtureSID + ")"},
		{"manager_deny", "O:SYG:SYD:(D;;0x1;;;" + processQueryFixtureSID + ")(A;;0x1fffff;;;SY)(A;;0x1fffff;;;BA)"},
		{"group_deny_query", "O:SYG:SYD:(D;;0x1000;;;BU)(A;;0x1fffff;;;SY)(A;;0x1fffff;;;BA)"},
		{"late_deny", privateProcessFixture + "(D;;0x1;;;WD)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sd := processQueryTestDescriptor(t, tt.descriptor)
			if _, err := mergeUpdaterProcessQuery(sd, manager); err == nil {
				t.Fatal("unsafe process ACL accepted")
			}
		})
	}
}

func TestUpdaterProcessQueryRejectsNonAccountSIDs(t *testing.T) {
	for _, value := range []string{
		"", "not-a-sid", "S-1-1-0", "S-1-5-11", "S-1-5-18", "S-1-5-19",
		"S-1-5-32-544", "S-1-5-32-545", "S-1-5-80-1-2-3-4-5",
		"S-1-5-21-1-2-3-500", "S-1-5-21-1-2-3-512", "S-1-5-21-1-2-3-999",
		"S-1-5-21-1-2-1001", " S-1-5-21-1-2-3-1001", "S-1-5-21-01-2-3-1001",
		"S-1-5-21-1-2-3-01001", "S-1-5-21-1-2-3-4294967296",
		"S-1-5-21-4294967296-2-3-1001", "S-1-5-21-+1-2-3-1001",
		"S-1-5-21--2-3-1001", "s-1-5-21-1-2-3-1001",
	} {
		if _, err := managerProcessQuerySID(value); err == nil {
			t.Fatal("nonconventional or privileged SID accepted")
		}
	}
	for _, value := range []string{"S-1-5-21-0-0-0-1000", "S-1-5-21-4294967295-2-3-4294967295"} {
		if _, err := managerProcessQuerySID(value); err != nil {
			t.Fatalf("canonical unprivileged account SID %q rejected: %v", value, err)
		}
	}
	if _, err := mergeUpdaterProcessQuery(processQueryTestDescriptor(t, privateProcessFixture), nil); err == nil {
		t.Fatal("nil manager SID accepted")
	}
}

func TestUpdaterProcessQueryDoesNotRunWithMissingProtectedReceipt(t *testing.T) {
	s := &windowsUpdater{ctx: context.Background(), paths: nativePaths{State: filepath.Join(t.TempDir(), "absent")}}
	statuses := make(chan svc.Status, 4)
	failure, code := s.Execute(nil, make(chan svc.ChangeRequest), statuses)
	if !failure || code&0xffff0000 != 0x4d070000 {
		t.Fatal("missing receipt did not fail at process-security startup stage")
	}
	for len(statuses) > 0 {
		if (<-statuses).State == svc.Running {
			t.Fatal("Running published before validated process-query grant")
		}
	}
}

func TestUpdaterProcessQueryCancellationAndInvalidHandle(t *testing.T) {
	manager, _ := managerProcessQuerySID(processQueryFixtureSID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := grantUpdaterProcessQuery(ctx, windows.CurrentProcess(), manager); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled grant reached process security operations")
	}
	if err := grantUpdaterProcessQuery(context.Background(), windows.Handle(0), manager); !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		t.Fatal("invalid process handle was ignored")
	}
	if windowsUpdaterExitCode(&windowsUpdaterFailure{windowsUpdaterProcessSecurity, windows.ERROR_ACCESS_DENIED}) != 0x4d070005 {
		t.Fatal("process security startup failure lost fixed SCM code")
	}
}
