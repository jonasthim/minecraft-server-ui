# Task30L4A Windows ACL check

## Finding

Hosted Windows run `39045481814` (commit `326876f`, job `117433228740`) failed manager state tests with both `manager account identity could not be verified` and `directory DACL is not the exact private allowlist`. The retained log is `/home/thim/.cache/dilla-tmp/ci-39045481814-windows.log`.

The existing Windows checks converted security descriptors to SDDL and compared the resulting text or extracted ACE fields from that serialization. The hosted log records the resulting failures, but does not expose the descriptor serialization, so it does not prove which formatting difference caused the run to fail. The correction removes serialization from both decisions and evaluates the actual ACL entries.

## Change

`platformManagerSID` now reads allow ACEs from the Windows ACL and accepts exactly one writable account SID in the existing `S-1-5-21-...` domain family. Malformed ACE or SID data fails closed. `checkExactPrivateDACL` now requires a protected, nondefaulted DACL with exactly three allow ACEs: the manager SID, Administrators, and Local System. Each ACE must grant the same full access mask and have exactly the object/container inheritance flags. ACE ordering and equivalent mask spelling do not affect the decision. No trustees, rights, owner rules, or write access were added.

Windows tests exercise the generated descriptor, reordered ACEs and numeric full-access masks, and rejection of extra trustees, deny ACEs, reduced rights, unprotected DACLs, and inherit-only ACEs. A filesystem fixture also verifies manager SID discovery from reordered binary ACEs.

Follow-up review identified that an `INHERIT_ONLY_ACE` is not effective on the directory whose DACL is being inspected. Manager SID discovery now skips such allow ACEs. Additional Windows fixtures prove an effective manager SID remains selected when another domain SID has inherited-only write rights, and that an inherited-only SID alone is refused.

The write-right predicate also includes `FILE_DELETE_CHILD` (`0x40`), which grants the ability to delete entries within a directory according to [Microsoft's file access rights reference](https://learn.microsoft.com/en-us/windows/win32/fileio/file-access-rights-constants). A direct Windows predicate test verifies this right is classified as writable while read-only and execute-only masks are not.

## Verification and limits

- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/managerstate -o /tmp/managerstate-windows.test.exe` passed.
- `go test ./internal/managerstate -count=1` passed on Linux.
- After the inherit-only and `FILE_DELETE_CHILD` review fixes, `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/managerstate -o /tmp/managerstate-windows-acl.test.exe`, `go test ./internal/managerstate -count=1`, `go vet ./internal/managerstate`, and the owned-file diff check passed.
- `git diff --check` passed for the owned source and test files.

The Windows tests were cross-compiled, not run locally. The change still needs the native Windows Actions test gate to confirm the hosted descriptor behavior and manager-state CLI fixtures.
