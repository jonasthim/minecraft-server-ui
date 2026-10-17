package selfupdate

import "testing"

// Literal access masks are hand-derived from Microsoft's file access rights,
// not constructed using the predicate's masks. The 0x116 fixture is the actual
// effective Users ACE recorded at ProgramData in hosted run39033415308.
func TestWindowsPathAccessPolicy(t *testing.T) {
	tests := []struct {
		name                      string
		mask                      uint32
		ancestor, directory, file bool
	}{
		{"observed_programdata_create_children", 0x116, false, true, true},
		{"add_file_or_write_data", 0x2, false, true, true},
		{"add_subdirectory_or_append_data", 0x4, false, true, true},
		{"read_and_traverse", 0x1200a9, false, false, false},
		{"write_extended_attributes", 0x10, false, true, true},
		{"write_attributes", 0x100, false, true, true},
		{"delete_object", 0x10000, true, true, true},
		{"delete_child", 0x40, true, true, true},
		{"change_dacl", 0x40000, true, true, true},
		{"change_owner", 0x80000, true, true, true},
		{"generic_write", 0x40000000, true, true, true},
		{"generic_all", 0x10000000, true, true, true},
		{"create_and_delete_child", 0x46, true, true, true},
		{"create_and_change_dacl", 0x40116, true, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, target := range []struct {
				name   string
				kind   windowsPathKind
				unsafe bool
			}{
				{"ancestor", windowsExistingAncestor, tt.ancestor},
				{"application_directory", windowsApplicationDirectory, tt.directory},
				{"regular_file", windowsRegularFile, tt.file},
			} {
				t.Run(target.name, func(t *testing.T) {
					if got := untrustedWindowsAccess(tt.mask, target.kind); got != target.unsafe {
						t.Fatalf("unsafe = %v, want %v", got, target.unsafe)
					}
				})
			}
		})
	}
}

func TestWindowsUnknownPathKindRefused(t *testing.T) {
	if !untrustedWindowsAccess(0, windowsPathKind(255)) {
		t.Fatal("unknown path policy accepted")
	}
}
