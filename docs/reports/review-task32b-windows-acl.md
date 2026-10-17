# Task32B Windows ACL correction — independent review

Reviewed 2026-10-17: `c39cf1e..20b548c` (`20b548c3d901bad167bcc95bd5dc7fadc5be4cd3`).

**SPEC: PASS. QUALITY: APPROVE.** No Critical, Important, or Minor findings in
the five-file packet. This approves the source correction for native testing;
it does not complete Task32B or qualify installed Windows updating.

## Scope

Reviewed the exact supplied
`.superpowers/sdd/2026-10-17-valheim-parity-completion/review-task32b-windows-acl.diff`,
confirmed byte-for-byte equal to `git diff c39cf1e 20b548c`, and inspected all
five changed files: `native_windows.go`, `native_acl.go`, both new ACL test
files, and `task32b-windows-acl.md`. Context included AGENTS.md, the original
design/plan, the corrective completion plan, implementation ledger, observed
Task32B failure evidence, and the existing `safepath.OpenRoot` contract.

## Specification and security assessment

- The observed effective Users `0x116` ACE is accepted only for an existing
  ancestor. Its `0x2`/`0x4` rights create children on directories; they do not
  grant write/append to an existing protected child. Application directories
  and regular files instead reject all four `0x116` bits. The strict policy
  also closes the former append/subdirectory-creation omission.
- The replacement mask `0x500d0040` retains GENERIC_ALL, GENERIC_WRITE,
  DELETE, WRITE_DAC, WRITE_OWNER, and FILE_DELETE_CHILD refusal at every
  inspected level. Owners remain limited to SYSTEM, Administrators and the
  resolved TrustedInstaller SID. Null/absent DACLs and effective deny or
  unsupported ACEs remain refused; inherit-only ACEs are skipped only where
  ineffective, while effective inherited grants are inspected normally.
- `openProtectedRoot` checks the existing application directory under the
  strict policy before using the ancestor exception and continues to the
  local drive root. Missing/wrong-type/reparse paths fail; every reparse tag
  is refused by its attribute, independently of its identity. Each accepted
  ancestor has the already-existing checked child required by the nonempty
  directory argument. Retaining both child DELETE and parent DELETE_CHILD
  refusal prevents an untrusted principal from emptying or replacing this
  protected chain. I found no new ancestor replacement or reparse race
  introduced by the permission exception under this existing trust model.
- The separate attribute and named-security-descriptor reads do not create a
  new handle-based absolute-name guarantee. The final existing safepath
  traversal and binding checks remain, and the report correctly identifies
  ACL ancestry as the trust basis. Trusted administrators and arbitrary
  filesystem/filter behavior are not newly claimed as contained.
- Regular-file/link-count checks remain intact. No installer ACL, credential,
  service, trust default, API contract, acquisition path, or release gate is
  changed by this packet.

The rights interpretation matches Microsoft's
[file access rights constants](https://learn.microsoft.com/en-us/windows/win32/fileio/file-access-rights-constants).
The existing-child argument matches Microsoft's
[child security descriptor and inheritance rules](https://learn.microsoft.com/en-us/windows/win32/fileio/file-security-and-access-rights)
and [delete/rename access requirements](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-movefileexw).
Attribute writes require care: Microsoft's
[FSCTL_SET_REPARSE_POINT specification](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-fsa/4aeefef8-92c3-4abc-af7a-a610caf8a165)
allows write-data or write-attributes access but rejects nonempty directories;
the [Win32 reparse restrictions](https://learn.microsoft.com/en-us/windows/win32/fileio/reparse-points)
also require an empty directory. These support the stated source reasoning,
without replacing native filesystem evidence.

## Test quality and independent verification

The portable table contains 14 literal rights scenarios across three policies,
including the observed `0x116`, each replacement right, append and attributes/EA;
it also checks an unknown policy fails closed. Expectations do not reuse the
production mask. Windows tests go beyond the predicate: 28 fixtures pass through
Windows' real SDDL parser into the shipping descriptor validator, with separate
unsupported-effective-ACE and nine path-type/reparse attribute cases. The
synthetic TrustedInstaller SID is supplied only to the extracted validator;
production still resolves the real identity. These are in-memory descriptor
tests, not actual host ACL/access or concurrent replacement experiments.

Repeated on Linux against the reviewed commit:

| Check | Result |
| --- | --- |
| `go test -race ./internal/selfupdate -count=1` | PASS, 10.780s |
| `go test ./internal/selfupdate -run '^TestWindows(PathAccessPolicy\|UnknownPathKindRefused)$' -count=1 -v` | PASS, all 42 policy combinations and unknown-policy case |
| `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /home/thim/.cache/dilla-tmp/review-task32b-windows-acl.test.exe ./internal/selfupdate` | PASS, compilation only |
| `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet ./internal/selfupdate` | PASS |
| `git diff --check c39cf1e 20b548c --` with the five packet paths | PASS |

Windows-only tests were not executed here. Next evidence remains native
WindowsServer2022 execution of those tests and the actual shipping installer,
then genuine installed activation, failed-health rollback and observed
interruption/recovery. Other Windows families and stable support remain
unqualified. No host service/account/ACL operations, implementation edits,
staging or commits were performed during review. No out-of-scope findings were
identified; unrelated worktree files were left untouched.
