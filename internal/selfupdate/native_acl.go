package selfupdate

// windowsPathKind distinguishes a directory containing an already existing
// protected child from application-owned directories and regular files.
type windowsPathKind uint8

const (
	windowsApplicationDirectory windowsPathKind = iota
	windowsExistingAncestor
	windowsRegularFile
)

func untrustedWindowsAccess(mask uint32, kind windowsPathKind) bool {
	// GENERIC_ALL, GENERIC_WRITE, DELETE, WRITE_DAC, WRITE_OWNER and
	// FILE_DELETE_CHILD always allow mutation/replacement of a protected path.
	const replacement = 0x500d0040
	switch kind {
	case windowsExistingAncestor:
		// FILE_ADD_FILE/FILE_ADD_SUBDIRECTORY only create new children. The
		// protected child must already exist and be checked before this mode is
		// used. An ancestor cannot become a reparse point while it is nonempty.
		return mask&replacement != 0
	case windowsApplicationDirectory, windowsRegularFile:
		// 0x2 and 0x4 mean add-file/add-directory on directories, and
		// write/append on files. Also refuse WRITE_EA and WRITE_ATTRIBUTES;
		// attribute-write access can set a reparse point on an empty directory.
		return mask&(replacement|0x116) != 0
	default:
		return true
	}
}
