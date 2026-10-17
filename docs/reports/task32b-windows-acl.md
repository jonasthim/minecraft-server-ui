# Task32B — Windows existing-ancestor ACL correction

Status: source correction prepared for integrator review. Native Windows ACL
tests and installed bootstrap/update qualification remain pending. No service,
account or canonical application path was changed on the development host.

## Observed failure and scope

[Hosted run39033415308](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39033415308)
first rejects `C:\ProgramData` during the shipping installer's updater bootstrap.
The preceding application state directory and the bootstrap file are private to
SYSTEM/Administrators. The rejected ancestor is SYSTEM-owned, has no reparse
attribute, and grants Users an effective allow ACE `0x116`, with container
inheritance and no inherit-only propagation. This intersects the old forbidden
mask `0x500d0042` only at `0x2`. The scrubbed evidence is in
[task32b.md](task32b.md); no raw DACL/credential dump was added here.

The old predicate treats `0x2` uniformly as file data-write. On a directory it
means permission to create a new file. The correction distinguishes an existing
ancestor containing a protected child, an application directory, and a regular
application file. It also closes the original omission of `0x4`: file append or
directory creation. The integrator explicitly approved refusing attribute/EA
writes in strict application modes within this correction.

## Rights and reparse reasoning

Microsoft defines `0x2` and `0x4` according to object type: child creation on
directories, data write/append on files. `0x10` and `0x100` concern extended
attributes and attributes. The observed `0x116` has no delete, delete-child,
DACL-change, owner-change or generic write/all grant.
[File Access Rights Constants](https://learn.microsoft.com/en-us/windows/win32/fileio/file-access-rights-constants)

Windows checks the child's own security descriptor for access to that child;
parent ACEs do not directly grant access to an existing child. Effective
inherited grants are checked on each inspected child. Inherit-only ACEs are
ignored only on the object where they are not effective.
[File security and access rights](https://learn.microsoft.com/en-us/windows/win32/fileio/file-security-and-access-rights)

Creation rights alone do not authorize removing an existing protected child.
Microsoft documents delete-on-object or delete-child-on-parent as necessary
for deletion/renaming. Both remain forbidden for untrusted principals at every
checked level, together with DACL/owner changes and generic write/all.
[MoveFileEx](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-movefileexw)

Attribute writes must not be described as universally harmless: Microsoft's
object-store specification permits setting a reparse point with data-write or
attribute-write access, and rejects doing so on a nonempty directory. The
Win32 documentation independently requires directory emptiness. Therefore the
ancestor exception relies on its already-existing protected child, plus the
retained refusal of every removal/replacement grant. Strict application modes
also refuse attribute/EA writes, including on an empty application directory.
[FSCTL_SET_REPARSE_POINT](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-fsa/4aeefef8-92c3-4abc-af7a-a610caf8a165),
[Reparse points](https://learn.microsoft.com/en-us/windows/win32/fileio/reparse-points)

## Implementation and preserved boundaries

- `native_windows.go` checks the requested application directory first, then
  walks outward. A missing, wrong-type or reparse directory fails before the
  ancestor exception is reached. Each step checks context cancellation.
- Every inspected path checks `FILE_ATTRIBUTE_REPARSE_POINT`, independently of
  tag, so junctions/custom tags receive the same refusal as symbolic links.
- The final `safepath.OpenRoot` traversal and its checked directory binding
  remain. No new claim is made that an `os.Root` handle prevents absolute-path
  replacement; the trust argument relies on the full ACL ancestry checks.
- Existing ancestors permit create-new-child/attribute/EA grants. All modes
  retain the original delete/delete-child/WRITE_DAC/WRITE_OWNER/generic-write/
  generic-all refusal. Strict directories/files additionally refuse `0x116`.
- Trusted owners remain SYSTEM, Administrators or the resolved TrustedInstaller
  SID. Null/absent DACLs, untrusted/missing owners, effective unsupported ACEs,
  unsafe regular-file/link counts and path/type failures remain rejected.
  Effective deny ACEs still require explicit review rather than being used to
  simplify or subtract unsafe allow grants.
- The pure `native_acl.go` predicate supplies actual portable regression
  coverage of these Windows masks without adding a dependency or a test-only
  production hook. Windows descriptor parsing stays in the shipping validator.

ProgramData permissions, shipping installers, trust defaults, services,
acquisition, API contracts and release gates are unchanged by this packet.

## Verification and limitations

Executed locally on Linux on 2026-10-17:

- `go test ./internal/selfupdate -run '^TestWindowsPathAccessPolicy$' -count=1`:
  failed against the original, unchanged `0x500d0042` predicate extracted into
  the portable helper. The observed ACE and add-file ancestor cases were
  incorrectly rejected; application-directory creation, file append and
  attribute/EA mutation cases were incorrectly allowed. The corrected predicate
  passes all 14 rights scenarios across three modes; unknown modes also fail
  closed. Expectations use hand-derived literal fixtures.
- `go test -race ./internal/selfupdate`: PASS (`10.608s`), all package tests
  applicable to Linux. The Windows-only descriptor tests are not executed.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o
  /home/thim/.cache/dilla-tmp/selfupdate-windows-acl.test.exe
  ./internal/selfupdate`: PASS, compilation only. The compile first identified
  a missing explicit conversion from Windows' typed `ACCESS_MASK`; corrected
  before the passing rerun.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet ./internal/selfupdate`: PASS.
- `git diff --check -- internal/selfupdate/native_windows.go`: PASS.

`native_acl_windows_test.go` contains 28 descriptor cases through Windows'
actual SDDL parser and the shipping validator, plus an unknown effective ACE
case and nine path-attribute/type cases. These cover the actual effective
container-inherit `0x116` ACE, strict private state/file descriptors, trusted
installer, null/absent/empty DACLs, owner refusal, inherited versus inherit-only
grants, unsupported/deny ACEs, append, root child creation and replacement
rights. They are cross-compiled, **not yet run** on Windows. They inspect
in-memory descriptors and do not change host ACLs or require accounts/services.

The Microsoft documentation supports the stated access-right/nonempty-directory
argument; it is not native proof of the concrete filesystem or installer. The
next required evidence is Windows2022 execution of these tests and the shipping
installer on the same disposable hosted layout, followed by the genuine
activation/rollback/interruption cases. Other Windows families, arbitrary
filesystem/filter configurations and stable release support remain unqualified.
The integrator owns review, hosted dispatch and ledger updates.
