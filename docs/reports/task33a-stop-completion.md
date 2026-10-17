# Task33A stop completion investigation

The candidate run `39045064246` at `2bce091c738ddffb21cb2bce6904de0d3a748c0f`
failed both hosted game jobs in `deliberate_stop_restore`. Its scrubbed summary
identified `stopping_process` / `native_stop_unclean`. Linux post-failure state
showed the exact process identity stopped with a final exit record, deliberate
operator reason, exit code 0, `forced=false`, and no watchdog error. The hosted
artifact did not retain the failing `Supervisor.Stop` substage. Windows also
reported a stopped process, but its launcher evidence was rejected by the
qualification parser before exit metadata could be inspected.

## Reproduction and correction

I added a deterministic test using a real loopback HTTP server and a harmless
test child process. Its response writer buffers the handler body and pauses the
outer HTTP flush only after `session.handle` returns and releases `session.mu`.
The test consumer takes that same mutex before restoring the lease and writing
`stop`, matching the production run loop's stop closure. Against the original
handler ordering, the child exits while the acknowledgement flush is blocked.
With the correction, the handler flushes before queueing the stop signal and
the child remains alive until the acknowledgement is released. This reproduces
the response/exit ambiguity without running Minecraft or a service.

The handler now writes and flushes the success response before queuing the
operator stop signal. The faithful fixture fails against the original ordering
and passes with the correction. This removes the reproduced source-level race,
but the hosted artifact does not prove that this race caused run `39045064246`.

The supervisor also handles a failed `/stop` control exchange by waiting for
the existing completion checks. It returns success only after the same process
identity is confirmed dead, launcher admission is released, and matching final
metadata confirms a deliberate, unforced exit with code zero and no watchdog
error. Tests reject missing, forced, or wrong-identity metadata. If those checks
do not pass, the stop operation remains an error.

## Diagnostic parser

Windows native start identities use `{GUID}:<16 hexadecimal FILETIME digits>`.
The qualification parser accepted the Linux `GUID:<decimal start ticks>` form
but rejected this exact braced Windows form, yielding `identity_malformed` and
hiding launcher exit details. It now accepts both exact formats and retains
malformed-input rejection for missing braces, wrong tick lengths, nonhex ticks,
and trailing characters. This repairs evidence collection; it does not explain
the Windows stop failure.

## Verification and limits

- `go test -race ./internal/launcher ./internal/supervisor -count=1` passed.
- `go vet ./internal/launcher ./internal/supervisor` passed.
- `python3 -m unittest game_api_test` passed: 28 tests, 2 environment-dependent
  skips.
- Windows amd64 test binaries for `internal/launcher` and
  `internal/supervisor` cross-compiled successfully. This is compile evidence,
  not native Windows qualification.

The failed hosted run remains a failure and is not qualification evidence. A
new hosted game run is needed to see whether the response race correction
resolves the native stop failure and whether Windows launcher diagnostics are
now available.

## Integrator review

Root reviewed the production stop path and required the regression to take
the same launcher mutex as the real stop consumer. The corrected test blocks
the outer flush after the old handler releases that mutex, so the original
ordering fails and the new ordering passes. The supervisor fixture exercises
the actual Stop method and injects a lost acknowledgement only after the real
loopback exchange. The existing identity/admission/final-exit checks remain
the authority for success. Root independently reran both packages with race
detection and vet; both passed. No retry, forced termination, or restart was
added. Source acceptance does not establish the cause of the hosted failure;
the next installed-game run must still pass stop and restore on each host.

## Run 39046816923 and bounded stop-failure evidence

Run `39046816923` at `cdf5dc2f2e35b6f364fdf37d2f3196ba21e6158c` again failed
both hosted game jobs in `deliberate_stop_restore`, with phase
`stopping_process` and public job diagnostic `native_stop_unclean`. The retained
Linux and Windows summaries now both contain available launcher diagnostics:
the recorded identity matches, exit metadata is present with a nonzero exit
time, the stop is deliberate, `forced=false`, watchdog recovery is absent, and
the exit code is zero with operator reason. The process was stopped on both
hosts. These observations still do not identify which internal `Stop` check
returned the failure, and they do not establish a new source-level cause.

To make the next run useful, `Supervisor.Stop` now preserves an internal typed
failure category across its existing error paths. Categories cover initial
lock/probe failures, process-wait and admission-wait timeouts, probe/inspection
errors, missing final metadata, forced termination, watchdog failure, and
unclean exit. A lost control response has a finite explicit category for its
completion failure. The accessor returns only allowlisted codes and unknown
errors map to `unknown`; wrapped causes remain available to Go callers through
`errors.Is`/`errors.As` but are never included in the new journal message.
Lifecycle logs one fixed authored category message under its bounded cleanup
context and retains the prior generic public failure. The qualification client
classifies only exact authored messages; suffixes and arbitrary messages remain
unclassified. No retry, deadline, completion gate, or success condition changed.

Focused tests cover actual timeout and final-metadata/forced branches, lost
control response category retention and cause preservation, unknown or malformed
category rejection, and exact-message qualification parsing with negative
suffix cases. Already-expired and canceled contexts are classified consistently
even when the probe sees them before the wait loop's select. This source
instrumentation is intended to identify a later failure stage; it does not
convert run `39046816923` into a pass or make any claim about its underlying
cause.
