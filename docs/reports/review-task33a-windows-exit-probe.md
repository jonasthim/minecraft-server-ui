# Independent review: Task33A Windows process exit probe

## Verdict

The patch implements the requested bounded exit proof. Only `WAIT_OBJECT_0` from a zero-time wait on the same held process handle turns a process metadata failure into conclusive exit. The initial `OpenProcess` classification, process identity comparisons, and deadlines/retry behavior remain unchanged. I found no source blocker in this patch.

## Review findings

`processIdentityWithOps` holds the process handle across the initial wait and metadata queries. If `GetProcessTimes` or `QueryFullProcessImageName` fails, `processMetadataError` checks that same handle once with a zero-time wait. Only a successful `WAIT_OBJECT_0` yields `os.ErrNotExist`; timeout, wait error, abandoned, and unknown results preserve the original metadata error through `Unwrap`. Initial wait failures are wrapped distinctly so `ERROR_INVALID_PARAMETER` or `os.ErrNotExist` returned by the wait cannot be mistaken for a missing process. `IsMissingProcess` explicitly excludes both typed wrappers, while the direct `OpenProcess` error path remains unchanged.

The Windows-only tests cover both metadata-query branches. They use a harmless test-executable child, deterministically terminate it after the initial `WAIT_TIMEOUT`, inject failure in `GetProcessTimes` or `QueryFullProcessImageName`, and require the held native handle to prove exit. A native timing test keeps the metadata calls real. Live-child controls and recheck outcomes for wait error, timeout, abandoned, and unknown results verify that ambiguous results retain the original error and are not classified as exit. Initial wait signaled, abandoned, unknown, and error outcomes are separately covered.

## Verification and limits

Passed:

```text
go test -count=1 ./internal/launcher
go test -race -count=1 ./internal/launcher
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/launcher-task33a-exit-probe-review.test.exe ./internal/launcher
GOOS=windows GOARCH=amd64 go vet ./internal/launcher
git diff --check -- internal/launcher/identity_windows.go internal/launcher/identity_windows_test.go
```

Linux tests do not execute the Windows-only child-process tests. Windows cross-compilation and vet do not qualify native behavior. The native Windows run is still pending; no hosted failure can yet be attributed to this race. The retained run only supports the bounded category `stop_process_probe_failed`.
