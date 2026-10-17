# Task33A Windows process exit probe race

## Hosted evidence

Candidate run `39047941319` includes a Windows game summary with phase `deliberate_stop_restore`, failure `job_terminal_failure` at `stopping_process`, and diagnostics `native_stop_unclean` / `stop_process_probe_failed`. Its bounded launcher summary reports `identity_match=true`, `exit_present=true`, `exit_code=0`, and deliberate operator exit; the console summary reports stopped. The Linux job in that run passed.

The retained Windows summary does not include the failing native API, error code, or raw error. It does not prove that the process-exit race caused this hosted failure.

## Source mechanism and correction

`processIdentity` opens a process handle with query and synchronize rights, checks that handle once with a zero-time wait, then calls `GetProcessTimes` and `QueryFullProcessImageName` using the same handle. The handle pins the process object, so the PID cannot be reused underneath these calls. The process can still exit after the initial `WAIT_TIMEOUT`. Before this change, any subsequent metadata-query error was returned directly, even if the held handle had become signaled in the meantime.

The implementation now rechecks the same handle after either process metadata query fails. Only `WAIT_OBJECT_0` makes that metadata failure conclusive evidence of exit and returns `os.ErrNotExist`. A failed wait, `WAIT_TIMEOUT`, `WAIT_ABANDONED`, or any unknown result preserves the original metadata error through `Unwrap`; a typed wrapper prevents `os.ErrNotExist` or `ERROR_INVALID_PARAMETER` from being misclassified as an exited process before the wait proves it. Errors from the initial wait are likewise preserved without using the missing-PID shortcut. An initial `WAIT_OBJECT_0` remains conclusive exit evidence, while abandoned and unknown results are errors. The code does not retry metadata queries or change identity comparisons.

## Verification and limits

The Windows-only tests start a harmless copy of the test executable and keep it alive. Deterministic cases terminate it after the initial `WAIT_TIMEOUT`: one injects a `GetProcessTimes` failure; another keeps `GetProcessTimes` native and injects a `QueryFullProcessImageName` failure, asserting the image query was reached. Both then require the real held handle to prove exit. A separate native-timing case leaves both metadata calls native after termination and accepts either a complete identity or a conclusive missing-process result, never an ambiguous error. Live-child controls inject `ERROR_INVALID_PARAMETER` at each metadata call, and recheck controls inject wait failure, timeout, abandoned, and unknown results with generic, `ERROR_INVALID_PARAMETER`, and `os.ErrNotExist` errors. Those ambiguous cases must retain the original error through `errors.Is` and must not be classified as exit. Initial wait error, abandoned, and unknown cases are covered separately.

`GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/launcher` and Windows-targeted `go vet ./internal/launcher` passed. `go test -race ./internal/launcher` passed on Linux. Cross-compilation does not execute the Windows child-process tests. A native Windows run is still required, and the hosted failure remains unattributed until that run or further bounded diagnostics exposes the native error.
