//go:build windows

package selfupdate

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsProtectedSecurityDescriptor(t *testing.T) {
	const installer = "S-1-5-80-1-2-3-4-5"
	tests := []struct {
		name, sddl string
		kind       windowsPathKind
		allow      bool
	}{
		{"observed_programdata_effective_container_inherit", "O:SYD:(A;;FA;;;SY)(A;;FA;;;BA)(A;CI;0x116;;;BU)", windowsExistingAncestor, true},
		{"programdata_ace_is_not_private_application_directory", "O:SYD:(A;CI;0x116;;;BU)", windowsApplicationDirectory, false},
		{"programdata_ace_is_not_private_file", "O:SYD:(A;CI;0x116;;;BU)", windowsRegularFile, false},
		{"private_application_directory", "O:BAD:(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", windowsApplicationDirectory, true},
		{"private_bootstrap_file", "O:BAD:(A;ID;FA;;;SY)(A;ID;FA;;;BA)", windowsRegularFile, true},
		{"trusted_installer_owner_and_ace", "O:" + installer + "D:(A;;FA;;;" + installer + ")", windowsRegularFile, true},
		{"untrusted_owner_without_write_ace", "O:BUD:(A;;FR;;;BU)", windowsExistingAncestor, false},
		{"missing_owner", "D:(A;;FR;;;BU)", windowsExistingAncestor, false},
		{"null_dacl", "O:SYD:NO_ACCESS_CONTROL", windowsExistingAncestor, false},
		{"absent_dacl", "O:SY", windowsExistingAncestor, false},
		{"empty_dacl", "O:SYD:", windowsRegularFile, true},
		{"read_only_untrusted", "O:SYD:(A;;FRFX;;;BU)", windowsApplicationDirectory, true},
		{"ancestor_delete_child", "O:SYD:(A;;0x40;;;BU)", windowsExistingAncestor, false},
		{"ancestor_delete_self", "O:SYD:(A;;SD;;;BU)", windowsExistingAncestor, false},
		{"ancestor_change_dacl", "O:SYD:(A;;WD;;;BU)", windowsExistingAncestor, false},
		{"ancestor_change_owner", "O:SYD:(A;;WO;;;BU)", windowsExistingAncestor, false},
		{"ancestor_generic_write", "O:SYD:(A;;GW;;;BU)", windowsExistingAncestor, false},
		{"ancestor_generic_all", "O:SYD:(A;;GA;;;BU)", windowsExistingAncestor, false},
		{"append_only_file", "O:SYD:(A;;0x4;;;BU)", windowsRegularFile, false},
		{"add_subdirectory_application_root", "O:SYD:(A;;0x4;;;BU)", windowsApplicationDirectory, false},
		{"write_attributes_application_root", "O:SYD:(A;;0x100;;;BU)", windowsApplicationDirectory, false},
		{"write_ea_file", "O:SYD:(A;;0x10;;;BU)", windowsRegularFile, false},
		{"effective_inherited_write", "O:SYD:(A;CIID;GW;;;BU)", windowsExistingAncestor, false},
		{"inherit_only_write_does_not_apply_here", "O:SYD:(A;CIIO;GW;;;BU)", windowsExistingAncestor, true},
		{"inherit_only_write_must_be_checked_on_child", "O:SYD:(A;CIID;GW;;;BU)", windowsApplicationDirectory, false},
		{"effective_deny_requires_review", "O:SYD:(D;;GW;;;BU)(A;;FR;;;BU)", windowsExistingAncestor, false},
		{"trusted_effective_deny_requires_review", "O:SYD:(D;;GW;;;SY)", windowsExistingAncestor, false},
		{"inherit_only_deny_is_not_effective", "O:SYD:(D;CIIO;GW;;;BU)", windowsExistingAncestor, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(tt.sddl)
			if err != nil {
				t.Fatalf("parse fixed security descriptor: %v", err)
			}
			err = validateWindowsSecurityDescriptor(sd, tt.kind, installer)
			if (err == nil) != tt.allow {
				t.Fatalf("accepted = %v, want %v", err == nil, tt.allow)
			}
		})
	}
}

func TestWindowsProtectedPathAttributes(t *testing.T) {
	for _, tt := range []struct {
		name       string
		attributes uint32
		kind       windowsPathKind
		allow      bool
	}{
		{"existing_ancestor_directory", 0x10, windowsExistingAncestor, true},
		{"application_directory", 0x10, windowsApplicationDirectory, true},
		{"regular_file", 0x80, windowsRegularFile, true},
		{"file_cannot_use_ancestor_policy", 0x80, windowsExistingAncestor, false},
		{"directory_cannot_use_file_policy", 0x10, windowsRegularFile, false},
		// The attribute is checked without interpreting the reparse tag, so
		// junctions and custom tags have the same refusal as symlinks.
		{"any_reparse_directory_tag", 0x410, windowsExistingAncestor, false},
		{"any_reparse_application_tag", 0x410, windowsApplicationDirectory, false},
		{"any_reparse_file_tag", 0x480, windowsRegularFile, false},
		{"unknown_kind", 0x10, windowsPathKind(255), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := safeWindowsPathAttributes(tt.attributes, tt.kind); got != tt.allow {
				t.Fatalf("accepted = %v, want %v", got, tt.allow)
			}
		})
	}
}

func TestWindowsUnsupportedACERefused(t *testing.T) {
	sd, err := windows.SecurityDescriptorFromString("O:SYD:(A;;FR;;;BU)")
	if err != nil {
		t.Fatal("parse descriptor fixture:", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal("read descriptor fixture:", err)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		t.Fatal("read fixture ACE:", err)
	}
	ace.Header.AceType = 0xff
	if validateWindowsSecurityDescriptor(sd, windowsExistingAncestor, "") == nil {
		t.Fatal("unsupported effective ACE accepted")
	}
}
