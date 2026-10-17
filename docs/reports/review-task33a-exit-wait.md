# Independent review — Task33A Windows process exit wait

## Scope

Reviewed the bounded metadata-failure wait in `internal/launcher/identity_windows.go`,
its Windows-only real-child and injected-operation fixtures, and the updated
diagnostic report. The review checked the held-handle contract, wait result
classification, error unwrapping, and stage attribution.

## Findings

No correctness or safety findings.

The initial process probe still waits with timeout zero and uses the same
process handle for metadata. After either metadata API fails, the implementation
waits once for at most 1000 ms on that held handle. It returns missing-process
only for `WAIT_OBJECT_0` with no wait error. Timeout preserves the metadata
failure; failed or unexpected waits preserve the metadata and wait causes.
Because the metadata-stage error is the first member of the joined error,
`ProcessIdentityFailureStage` continues to attribute the failure to
`get_process_times` or `query_image`. No OpenProcess failure is routed through
the new wait, and no PID reopen or permission fallback was introduced.

The coordinated child fixture keeps a harmless child alive after the injected
image-query failure, then terminates it while the bounded wait is pending. The
fake wait fixtures cover timeout, failed, abandoned, and unknown results, check
the 1000 ms ceiling, and assert that metadata and wait errors remain discoverable
through `errors.Is`. Existing live-child controls ensure metadata errors do not
become exit evidence.

## Verification

- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/launcher-exit-wait.test.exe ./internal/launcher` — passed.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet ./internal/launcher` — passed.

These cross-target checks do not execute the Windows child scheduling fixture.
Native Windows CI remains required to verify that the coordinated timing case
passes on the supported runner.

## Verdict

**Approve.** The change follows the same-handle bounded wait contract and keeps
ambiguous metadata failures out of missing-process classification. Native
Windows execution remains pending.
