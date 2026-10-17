# Task33A Windows process exit probe race

## Hosted evidence

Candidate run `39051450784` includes a Windows game summary with phase `deliberate_stop_restore`, failure `job_terminal_failure` at `stopping_process`, and diagnostics `native_stop_unclean`, `stop_native_os_error_5`, `stop_process_api_query_image`, and `stop_process_probe_failed`. Its bounded launcher summary reports `identity_match=true`, `exit_present=true`, `exit_code=0`, deliberate operator exit, and no forced termination; the console summary reports stopped. This shows that QueryFullProcessImageName returned access denied in a probe associated with a later clean exit. It does not establish whether the process was already exiting when the API failed, so AccessDenied itself is not treated as exit evidence.

The hosted evidence does not prove the process-exit race caused this failure.

## Source mechanism and correction

`processIdentity` opens a process handle with query and synchronize rights, checks that handle once with a zero-time wait, then calls `GetProcessTimes` and `QueryFullProcessImageName` using the same handle. The handle pins the process object, so the PID cannot be reused underneath these calls. The process can still exit just after the initial `WAIT_TIMEOUT`, including while the metadata API is returning an error. A second zero-time wait can still observe `WAIT_TIMEOUT` if the process has not yet reached the signaled state.

After either process metadata query fails, the implementation now waits at most 1000 ms on that same held handle. Only `WAIT_OBJECT_0` makes the metadata failure conclusive evidence of exit and returns `os.ErrNotExist`. `WAIT_TIMEOUT` preserves the original metadata error; failed, abandoned, or unknown wait outcomes preserve both the metadata and wait causes. The stage remains attributed to the metadata API that failed, and process-missing classification stays false unless the same handle is signaled. Healthy probes still perform only the initial zero-time wait. This adds no metadata retries, process reopen, permission change, or identity fallback.

## Verification and limits

The Windows-only tests start a harmless copy of the test executable and keep it alive. Existing cases cover errors after the handle has already become signaled. The added coordinated case injects QueryFullProcessImageName access denied and gates child termination on entry to the bounded native wait. Thus the child remains live through the old zero-time recheck, while the bounded wait observes the later signal on the same handle. Timeout, failed, abandoned, and unknown wait controls preserve the metadata error and its API stage; failed waits also preserve their wait error. The tests assert that healthy initial waits remain zero-time and the metadata wait never exceeds 1000 ms. Live-child controls continue to reject metadata errors as exit evidence.

Cross-compilation and Linux tests do not execute the Windows child-process scheduling test. Native Windows CI is still required to verify the coordinated fixture and to determine whether this bounded same-handle wait resolves the hosted failure. No hosted cause is claimed until that rerun.
