# Review: Windows private scratch directory cleanup retry

## Result

No material correctness or safety finding in the reviewed retry and workflow changes.

## Source review

`DeleteOwned` retries only a directory disposition that fails with Win32 error 145 (`ERROR_DIR_NOT_EMPTY`). It captures that code immediately, closes the first handle, waits once for 100 ms, then reopens the same path without delete sharing. Before the second disposition it rechecks the exact recorded file identity, owner, protected DACL, exact ACL entries, and an empty directory listing. The second disposition is attempted once; every other initial error and every retry error refuses cleanup. File dispositions never retry.

The reopened handle denies delete sharing, so the checked directory cannot be renamed while the retry's identity/security checks and disposition are performed. The final deletion is handle-bound. A path swap before reopening is rejected by the recorded identity check. Concurrent child creation after the enumeration still causes the native directory disposition to fail rather than receiving another retry.

The test harness preserves the original Win32 entry point explicitly as `NativeSetFileInformationByHandle`; its injected wrapper uses `SetLastError` to exercise controlled error paths. The workflow now runs the same checked-in behavior harness in both PowerShell 7 and Windows PowerShell 5.1, with no temporary source instrumentation.

## Fixture coverage

The Windows harness covers a successful retry after a delete-pending child handle is released; a foreign child that remains and blocks cleanup; parent path substitution; changed scratch-directory ACL; and non-145 file and directory disposition errors that must not retry. It checks owned-child cleanup and preserves replacement/foreign entries before test-owned cleanup. The `non145-directory-disposition` fixture skips exactly the four known child dispositions before injecting error 5 at the directory disposition.

The behavior harness parsed successfully with PowerShell 7.6.7 on Linux. The native filesystem and P/Invoke cases were not executed locally; hosted Windows PowerShell 7 and 5.1 runs remain the execution evidence for those cases.
