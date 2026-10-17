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

This is temporary diagnosis, not a shipping change. YAML parsing, the extracted workflow PowerShell parser, the expanded behavior-harness parser, and the instrumented entry-script parser pass under portable PowerShell 7.6.7. The injected C# compiles there. A bounded helper fixture emits the allowed stage/API/numeric tuple, collapses invalid mode/reason/stage/API/code values to fixed fallback labels, and does not print a synthetic secret or child name. These checks validate the diagnostic plumbing only; hosted Windows PowerShell 5.1 evidence is still needed to identify the failing API and its code.
