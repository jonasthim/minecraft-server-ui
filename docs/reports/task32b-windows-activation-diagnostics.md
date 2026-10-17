# Task 32b: Windows activation failure diagnostics

## Failure boundary

Run [39041683747](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39041683747)
on commit `71b0c66452066f0b5b23416631581dd791edc0e2` passed process-access
qualification. The installed updater's API accepted the first activation
request, then the activation journal reached `aborted` before the acceptance
harness expected `healthy`. The Windows driver previously had no
`ActivationFailure` sink, and the updater discarded the asynchronous worker's
returned error. That left the failure stage and schema probe result unknown.

## Diagnostic changes

The Windows driver now writes bounded informational events through the
existing `golang.org/x/sys/windows/svc/eventlog` package, source
`MinecraftUIUpdater`:

- Event 1001 records only one stage from the existing fixed activation-stage
  allowlist.
- Event 1002 records one fixed schema-probe substep and either a bounded
  numeric Windows error code or `unknown`. It never records the returned error
  text.
- Event 1003 records a stopped probe service's `Win32ExitCode` and
  `ServiceSpecificExitCode` as `uint32` values.

All event writes are best effort: open, write, and close errors are ignored and
do not change the updater result or recovery path. No goroutines are created.
The event writer is injectable in tests; tests do not open the local Event
Log.

The qualification harness queries the Application log by fixed provider name
using a fixed XPath time bound from the test start. This avoids requiring
registered provider metadata for the unregistered source.
[RegisterEventSourceW documentation](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-registereventsourcew)
states that an unregistered source is written to the Application log. The
harness caps retrieval at 1024 events and reads only each event's ID and first
insertion value. It accepts only anchored fixed grammars with strict `uint32`
parsing, then writes counts, allowlisted stage names, and numeric codes to
`native-update-diagnostics.json`. It does not retain raw event messages, XML,
paths, identities, credentials, or arbitrary event data. A query or parse
failure is represented only by `capture_failed: true` and an empty bounded
summary; no exception text is saved.

## Verification

- `GOOS=windows GOARCH=amd64 go test -c -o /tmp/task32b-diagnostics.test.exe ./internal/selfupdate` — passed.
- `GOOS=windows GOARCH=amd64 go vet ./internal/selfupdate` — passed.
- PowerShell 7.6.7 parsed both updated qualification scripts — passed.
- The PowerShell parser fixture passed valid records and rejected unknown
  suffixes, paths, error-code overflow, and a batch over 1024 records.
- `git diff --check` on the owned files — passed.

The Windows Go tests were cross-compiled, not run on this Linux host. No local
service, account, installer, firewall, game, or preview process was operated.
A fresh hosted Windows qualification run is still required to determine the
exact failing activation and schema-probe substeps.
