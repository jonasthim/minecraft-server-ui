# Review: Task 32B Windows activation diagnostics

## Summary

The Go packet is additive and source review found no changed updater error or recovery return conditions. Activation stages, probe steps, and emitted event payloads are finite; native errors reduce to a positive `uint32` code or `unknown`. The harness limits the Application-log query to the fixed provider and start time, caps results at 1024 events, and only serializes fixed grammar matches plus numeric fields. The portable PowerShell fixture and Windows cross-compilation pass.

## Finding

**Medium — diagnostic artifact write can interrupt installed-service cleanup.** In [windows-updater.ps1](/home/thim/Repositories/minecraft-server-ui/deploy/qualification/windows-updater.ps1:225), writing `native-update-diagnostics.json` occurs inside the same outer `try` as updater stop and service deletion. Query and parse exceptions are converted to `capture_failed`, but a failure from `ConvertTo-Json` or `Set-Content` escapes that inner catch and reaches the outer cleanup catch. The remaining installed-service cleanup statements in that block are skipped. This is a new failure point before teardown, so the capture artifact write is not fully best effort. Keep artifact-write failure isolated from cleanup, or perform the write after owned-service teardown.

## Verified boundaries

- [native_diagnostics_windows.go](/home/thim/Repositories/minecraft-server-ui/internal/selfupdate/native_diagnostics_windows.go:31) discards event-log open/write/close errors. It emits only event IDs 1001–1003 and fixed stage/step strings; probe `syscall.Errno` becomes a decimal value only when positive and within `uint32`, otherwise `unknown`. No raw error text enters the event message.
- [native_windows.go](/home/thim/Repositories/minecraft-server-ui/internal/selfupdate/native_windows.go:563) adds diagnostic calls to existing failure branches. The configuration and SID checks were split to attach a label, but preserve their prior fixed errors and return locations. Timeout, service start/query, probe output validation, and activation failure still return through their original error path. The common worker continues to discard the async return while its existing `ActivationFailure(stage)` hook emits the fixed stage.
- The Go allowlists match the PowerShell parser's fixed stage and probe-step alternatives. The parser rejects payload suffixes, invalid numeric forms, and values beyond `uint32`; event insertion data is capped at 256 characters and records at 1024. The stopped-service fixture uses `1291845632` (`0x4d000000`), verifying an unsigned value above `MaxInt32`.
- [windows-updater.ps1](/home/thim/Repositories/minecraft-server-ui/deploy/qualification/windows-updater.ps1:213) constructs a fixed provider/time XPath query against `Application`. The exact `NoMatchingEventsFound,*` error is treated as an empty result; other query errors and parser errors become `capture_failed: true` with an empty summary. The fixed source `MinecraftUIUpdater` falls back to Application when unregistered, as documented by Microsoft's [`RegisterEventSourceW`](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-registereventsourcew) reference. The query uses `Get-WinEvent`'s documented XPath and `MaxEvents` options ([Microsoft PowerShell reference](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.diagnostics/get-winevent?view=powershell-7.5)).
- The test fixture runs the pure parser only; it does not open the local Event Log or operate a service. Native Windows execution remains unverified on this Linux host.

## Checks run

- `GOOS=windows GOARCH=amd64 go test -c -o /tmp/task32b-diagnostics-review.test.exe ./internal/selfupdate` — passed.
- `GOOS=windows GOARCH=amd64 go vet ./internal/selfupdate` — passed.
- Portable PowerShell 7.6.7 parsed both updated scripts and `Test-NativeUpdateDiagnosticsParser` passed.
- `git diff --check` on the reviewed files — passed.

## Verdict

The diagnostic payloads and Event Log filtering are appropriately bounded. Address the cleanup interruption finding before relying on this as a best-effort harness addition. A new hosted Windows qualification run is still needed to establish whether the events are emitted and identify the failed activation stage.

## Integrator correction and acceptance

Root isolated artifact serialization/writing in its own catch. A write failure
marks the qualification failed and emits one fixed message; execution continues
into the existing owned-service teardown. Root exercised the exact new block
under StrictMode Latest with a throwing Set-Content fixture: failure retained,
cleanup continuation reached, exit0 for the fixture. The original unguarded
statement exits1 before reaching that continuation. No local native service or
Event Log operation was invoked. Root Windows compile/vet also passed.

Finding closed. Integrator accepts source for the hosted rerun, with native
event capture and the actual activation failure still unqualified.
