# Task33A Linux process-probe exit race

## Hosted evidence and limits

Game run `39048953623` at `80577e60d68a1741a4b73cd7028d4ac9268c7509` on Ubuntu 24.04 / Vanilla 1.21.9 reached four real client connections and passed the acknowledged online save-lease backup with 82 verified files and unchanged native identity. It then failed at `stopping_process` with `job_terminal_failure` and bounded diagnostic `stop_process_probe_failed`.

The retained final records show matching identity, deliberate operator exit, exit code 0, no forced termination or watchdog error, and the console reports stopped. Those records locate the failure at the stop-process probe boundary but do not contain its original native error and do not prove that the race below caused the hosted failure.

## Source mechanism and correction

On Linux, `inspectIdentityUsing` first reads the target's direct `/proc` identity and then requires a privileged broker inspection for the canonical installed instance layout. Previously, either a broker error or broker-result validation error was returned immediately. If the original process exited between the initial proc read and broker completion, the stop probe could therefore report an inspection failure even though the original process was gone.

After a broker or validation failure, the code now makes one direct identity recheck. A process-missing result or a changed boot/start identity proves the original process has exited and is returned as such. A same-identity result, permission or malformed metadata, other unknown errors, or cancellation preserves the original broker/validation failure. Cancellation skips the recheck; cancellation during the recheck also preserves the original failure. The broker is called once. No retry, timeout, admission, identity comparison, or isolation requirement changes.

## Deterministic regressions

`TestInspectionBrokerFailureRechecksAndConfirmsExitedProcess` starts a harmless child test process, confirms its initial direct proc identity, then terminates and reaps it inside the injected broker call before returning an injected broker failure. The post-broker direct read must classify the original process as missing; the test asserts exactly two direct reads and one broker call.

Negative controls require the original broker error to remain for a live same-identity child, permission-denied or malformed direct rechecks, and cancellation. A changed start identity is accepted as conclusive original-process exit. `TestInspectionValidationFailureRechecksButPreservesLiveProcess` confirms an invalid broker identity still fails while the process remains live.

## Verification and limits

Passed on Linux:

```text
go test -count=1 ./internal/launcher
go test -race -count=1 ./internal/launcher
go vet ./internal/launcher
git diff --check -- internal/launcher/inspection_linux.go internal/launcher/inspection_linux_test.go
```

These tests establish the source behavior with local harmless child processes; they do not establish that this race caused the retained hosted failure or qualify the installed game. A hosted rerun is needed to determine whether the Linux failure changes. Windows probe failures remain outside this Linux-only correction and require their own native error evidence.
