# Task30A PowerShell 5.1 directory cleanup failure

## Hosted evidence

Candidate run `39048442475`, Windows job log `/home/thim/.cache/dilla-tmp/candidate-39048442475-windows.log`, reports the PowerShell 5.1 case as `good`, with outer entry-script exit 1. Its bounded assertion diagnostic says `native_runner_reached` and `argv=match`: the test's fake runner was reached and recorded the expected argument array. That establishes the template reached its pre-invocation checks and fake-runner seam; it does not establish a real native preparer invocation.

The four `Atomic scratch ACL verified` lines at 16:18:25 are from the earlier PowerShell 7 step, not the PowerShell 5.1 case. The PowerShell 5.1 failure at 16:18:45 occurs at the test's unexpected-exit assertion before it emits those four summary lines. Its later cleanup diagnostic at 16:18:46 observed an existing scratch directory, readable protected ACL, and zero entries after the failure. `bound=true` records that the template's scratch handle passed `AssertBound` earlier, before the cleanup code disposed that handle; it does not mean a handle remained open during `DeleteOwned`. The old `owner_matches=false` diagnostic compared the owner only to the current token user, so it did not rule out the Administrators owner used by elevated setup. The cleanup call had an outer `MethodInvocationException` and inner `IOException`.

The post-failure empty-directory observation and stage `delete_directory` narrow the refusal to the directory cleanup path, but they do not identify which native operation failed or prove a PowerShell 5.1 runtime defect. The PowerShell 7 harness passes, but no directory API error code was captured.

## Source path

`DeleteOwned` opens the target with `DELETE | READ_CONTROL | FILE_READ_ATTRIBUTES`, `OPEN_EXISTING`, backup semantics for a directory, and read/write/delete sharing. It then checks the opened handle's identity and exact owner/protected DACL with `AssertBound`. Finally, it marks the handle for deletion with `SetFileInformationByHandle` and `FileDispositionInfo` (information class 4). The zero-entry count was collected by the parent diagnostic after failure, not before the call.

Each of those stages currently collapses failures into the same generic `IOException` through `Failure()`. The diagnostic therefore cannot distinguish opening the DELETE handle, handle-bound validation, or the disposition call. The exact native error is discarded.

## Diagnosis and next evidence

I could not reproduce the Windows API behavior on this Linux host. The local PowerShell fixtures cannot execute `kernel32.dll` calls, so the actual refusal point remains unknown. The evidence does not support a shipping-template correction. Identity, ACL, empty-directory, and unknown-child guards remain unchanged.

The candidate workflow now temporarily instruments only the generated PowerShell 5.1 test source. Its output adds fixed `dir_open`, `dir_validate`, and `dir_disposition` stages, plus a bounded API enum for `CreateFileW`, `GetFileInformationByHandle`, `GetSecurityInfo`, `SetFileInformationByHandle`, logical validation, or a managed failure outside those APIs. BOOL API failures capture `GetLastWin32Error()` before any further call; `GetSecurityInfo` uses its returned status code. Logical identity/security mismatches use `-1`, never a possibly stale last-error value. The parent assertion emits the tuple only when the cleanup stage is `delete_directory` and every enum/code is in range. The owner diagnostic boolean accepts either the current user or the built-in Administrators SID, matching the two owner choices in the existing template; this does not change the cleanup check.

This was temporary diagnosis, not a shipping change. YAML parsing, the extracted workflow PowerShell parser, the expanded behavior-harness parser, and the instrumented entry-script parser passed under portable PowerShell 7.6.7. The injected C# compiled there. A bounded helper fixture emits the allowed stage/API/numeric tuple, collapses invalid mode/reason/stage/API/code values to fixed fallback labels, and does not print a synthetic secret or child name. Those checks validated diagnostic plumbing; the next hosted Windows PowerShell 5.1 run was still needed to identify the failing API and code.

## Diagnostic newline correction

Candidate run `39050048566` reached both verified preparations and passed PowerShell 7, but the new PowerShell 5.1 instrumentation stopped at `directory diagnostic API seam drift` before running its cases. The multiline source needle used the host's `Environment.NewLine`; the generated entry script uses LF, so a CRLF needle did not match on Windows. The original cleanup failure was not exercised by this run.

Root reproduced the exact seam failure locally by executing the instrumentation with Windows CRLF joins against the LF template. The diagnostic now normalizes the test source to LF and constructs that multiline needle with LF. All four combinations of LF/CRLF source and Linux/Windows runtime joins pass the exact-count seam guards; actionlint passes. This changes only temporary diagnostic construction. Actual Windows cleanup evidence still requires the next native run.

## Candidate 39050798063 — disposition failure

The subsequent hosted run at `06a4a3c` passes the PowerShell 5.1 `good` case
and reaches the tamper case's expected refusal. Its bounded tamper diagnostic
reports: scratch exists and is readable; the directory ACL is readable and
protected; the recorded scratch identity was bound before cleanup; the parent
enumeration after failure found zero residual entries; cleanup failed at
`delete_directory`; and the instrumented `SetFileInformationByHandle` call
returned Win32 code 145 (`ERROR_DIR_NOT_EMPTY`). The child-level tamper check
was otherwise the expected failure. No raw exception message, path, or child
name is needed to state this result.

This narrows the failed native operation: the directory handle passed identity
and ACL validation, and `FileDispositionInfo` was rejected as not empty. It
does not establish a general PowerShell 5.1 limitation because the good case
uses the same cleanup implementation successfully. The later zero-entry
enumeration is a separate observation after the failed call; it does not prove
the directory was empty at the instant NTFS processed that call. Source review
also found no obvious retained child handle at that point: child streams are
disposed before child deletion, each child deletion uses a scoped handle, and
the retained scratch-directory handle is disposed before directory deletion.
The remaining evidence is consistent with a short-lived child/delete-pending
entry or concurrent writer, but it does not identify which one. Do not label a
PowerShell handle leak as the established cause.

Microsoft documents that `FILE_DISPOSITION_INFO` marks the object for deletion
when the handle closes, and the file-system protocol maps
`STATUS_DIRECTORY_NOT_EMPTY` to a directory that is not empty at deletion time.
The `RemoveDirectory` contract likewise requires an empty directory. These
contracts support the native error classification, not a claim about the
transient entry's origin. See [SetFileInformationByHandle](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-setfileinformationbyhandle),
[MS-FSCC FileDispositionInformation](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-fscc/12c3dd1c-14f6-4229-9d29-75fb2cb392f6),
and [RemoveDirectory](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-removedirectory).

### Bounded directory retry

Root accepted a narrow resilience change without attributing the transient
entry to a specific process. `PrivateIO.DeleteOwned` now captures the
disposition error immediately and retries only when the target is a directory
and the first disposition returns 145. It closes the failed handle, waits once
for 100 ms, then opens the recorded path with a DELETE handle that denies
directory rename while the retry runs. On that held handle it rechecks the
recorded volume/file identity and exact protected ACL, then uses a disposed,
single-entry-bounded enumeration to require zero children before the second
handle-based disposition. The second disposition is the final attempt.

Files and all other error codes retain the original one-attempt failure path.
An identity mismatch, ACL change, enumeration error, or any child refuses
cleanup. There is no recursive removal or path-based deletion fallback. The
existing unknown-child fixture now traverses the retry path and verifies the
foreign child is retained. The expanded Windows behavior fixture adds a real
delete-pending child with a second shared-delete handle and a deterministic
retry seam that releases the handle between attempts. It also checks parent
replacement, changed directory ACL, and a non-145 disposition failure for
both a file and a directory for preservation and no-retry behavior.

The fixture and embedded Win32 adapter still require native hosted execution
under Windows PowerShell 5.1 and PowerShell 7. Portable PowerShell parsing and
source checks do not establish the Windows handle-sharing or delete-pending
behavior; report native results separately before claiming those gates pass.
