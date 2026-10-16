# Task 03 — jobs, events, and event streaming

Implementation delivered for integration review. Acceptance remains pending the integrator's review and ledger update.

## Delivered behavior

- SQLite-backed queued/running/succeeded/failed/cancelled jobs, private JSON inputs, captured requester, safe-cancellation flag, progress, last phase, cancellation intent, and timestamps. Migration `00003_jobs.sql` extends the foundation table and creates durable log storage.
- FIFO selection per instance and four global execution slots. Instance-less manager operations share a serial queue. An instance remains occupied until its handler actually returns, including cancellation cleanup. A SQL unique index also prevents two running jobs for a non-null instance.
- Handler kinds freeze before startup. Queued jobs retain their exact inputs across restart. In-flight jobs become failed with their last phase preserved and an interruption message; they are never silently replayed. Missing queued worker kinds fail explicitly. Resumable file reconciliation is intentionally reserved for a future explicit recovery hook.
- Stop immediately rejects new work, closes subscriptions, requests cancellation, and waits for actual worker return. Timeout reports a cleanup timeout and leaves slots occupied. Queued work remains durable for the next startup. Handler cleanup can use an independent bounded 30-second context.
- Durable safe application output, capped at 10,000 lines of at most approximately 8 KiB per job. Credential field patterns, bearer values, URL userinfo/query parameters, and sensitive payload values are redacted before persistence or publication. Raw handler errors and panics become opaque public failures; handlers must not pass raw upstream bodies/configuration to Log.
- Bounded live fanout (32 events per subscriber). A slow subscriber is disconnected; it cannot block producers or conceal loss behind silent event drops. Event and log shapes alias the shared domain contract.
- Viewer reads/list/detail/logs/download/SSE; operator cancels only their own cancellable jobs; administrator cancels any cancellable job. Cancellation audit and durable state update are one transaction. Session/CSRF and current role/token scopes are enforced through the shared protected API router and explicit feature role gates.
- SSE sends a fresh progress snapshot, JSON typed progress/log events, 15-second heartbeats, five-second write deadlines, no-store headers, and exits on request cancellation or service shutdown. Each connection lasts at most 30 seconds and then reconnects through authentication: permission revocation/role changes can remain effective on an existing stream for up to 30 seconds.
- Mantine JobsPage uses generated API types, a real list/detail/progress/output flow, safe role-aware cancellation, visible errors/results, and log download. Browser memory keeps the latest 500 log lines. Reconnects query durable output from a separate durable cursor and deduplicate live/replayed lines. The list refreshes every ten seconds. Layout wraps controls and long identifiers for narrow screens.

## Reusable contracts

```go
service := jobs.New(database, logger)
service.Register("kind", func(ctx context.Context, execution *jobs.Execution) error {
    // execution.Job is the public domain.Job snapshot.
    // execution.Payload is a private json.RawMessage.
    // Journal the phase before side effects; pass ctx through all I/O.
    if err := execution.Phase(ctx, "download", 10); err != nil { return err }
    return execution.Log(ctx, "Application-authored safe status")
})
err := service.Start(managerContext)
job, err := service.Enqueue(ctx, jobs.Request{
    InstanceID: instanceID, Kind: "kind", Payload: privateJSON,
    RequestedBy: actingUser.ID, Cancellable: true,
})
// On shutdown, use a separate bounded shutdown context:
err = service.Stop(shutdownContext)
```

Register before Start, on the single manager process. The authorizing feature endpoint captures requester and approves operation/payload; no generic HTTP enqueue endpoint exists. `RequestedBy: 0` represents a system job and requires an administrator for cancellation. Never put credential material in payloads; use secret-store references. For critical cleanup, `execution.CleanupContext()` returns a fresh 30-second context/cancel pair. Release the instance only by returning from the handler after cleanup.

`api.MountJobs(protectedRouter, service)` registers `/jobs`, `/jobs/{jobId}`, `/cancel`, `/logs`, `/logs/download`, and `/events` inside the existing `/api/v1` authenticated + CSRF router. It must not be mounted under a second `/api/v1` prefix. `web/src/features/jobs` exports `JobsPage` for the authenticated `/jobs` shell route; optional `instanceID` filters the list.

The OpenAPI fragment is `docs/api/jobs.yaml`. The integrator merged it into the root schema and regenerated `web/src/api/schema.d.ts`; feature UI imports Job/JobLog/JobEvent from that generated output.

## Evidence

- Observed initial missing-feature failure for the service/event test suite, then implemented it and obtained passing results.
- Observed initial MountJobs missing-feature failure for the API suite, then implemented endpoints and obtained passing results.
- Stream header regression was observed failing when the test asserted sent headers via `ResponseRecorder.Result()`, then passed after headers were set before flushing.
- `go test -race ./internal/jobs ./internal/events ./internal/api`: PASS after cancellation, queue, shutdown, redaction, stream, and role tests were added.
- `go vet ./internal/jobs ./internal/events ./internal/api`: PASS.
- `go test ./...`: initially PASS across auth/runtime/foundation/Task 03. The final run after composition wiring failed due concurrent work outside this package: `internal/launcher/launcher_test.go` references not-yet-implemented Paths/ValidateSpec/CaptureIdentity/VerifyIdentity/newLeaseGuard; runtime `TestHistoricalJava16VendorValidation` (official16 parse/release/probe) and `TestHistoricalJava16InstallsVerifiedAndReuses` fail the historical vendor checks. Task 03 and cmd composition tests remained green in that run.
- `npm run typecheck` in web: PASS after the reconnect cursor correction.
- `npm run build` in web: PASS after JobsPage shell/navigation mounting.
- `go test ./cmd/minecraft-ui -run TestSharedAPIMountBootstrapsThenReadsJobs -count=1`: observed missing mountAPI RED, then PASS with a real emptyDB setup session and shared API routes.
- `npm run lint` in web: PASS.

Specific behavioral tests cover the four-worker maximum; same-instance cancellation cleanup; next queued job execution; queued cancellation; cancel ownership and audit; stopped-service enqueue rejection; cleanup timeout; closed subscribers; restart with exact persisted payload; truthful interrupted phase; opaque source errors and credential redaction; unregistered/unsafe identifiers/input; unsafe cancellation; unauthenticated streams/downloads; role matrix; stream snapshot, sent headers, shutdown; invalid log cursors; and slow-subscriber disconnect with a job filter.

## Remaining integration evidence and limits

- Service lifetime, shared authenticated API mounting, and JobsPage /jobs navigation are now wired with explicit integrator authorization. The integrator must review acceptance evidence and update the ledger.
- Integrated browser emptyDB → first administrator → navigation → Jobs empty state passed using local Playwright against the actual built app at 127.0.0.1:8098. At 390×844, document scrollWidth/clientWidth were both 390. Focused Refresh jobs and pressed Enter; the empty state remained visible and the focused button remained Refresh jobs. Inspected screenshot `.playwright-mcp/task03-jobs-mobile.png` (local untracked artifact, intentionally unstaged). Console errors were expected anonymous `/auth/me` 401 and missing favicon 404; no jobs API/UI error occurred.
- Browser build/start commands: `go build -o /home/thim/.cache/dilla-tmp/minecraft-ui-task03 ./cmd/minecraft-ui`; `env MINECRAFT_UI_ADDR=127.0.0.1:8098 MINECRAFT_UI_DATA_DIR=/home/thim/.cache/dilla-tmp/minecraft-task03-browser /home/thim/.cache/dilla-tmp/minecraft-ui-task03 serve`. Used Playwright browser_run_code_unsafe to set viewport, fill the real setup form, submit, open mobile navigation, click Jobs, and focus/Enter Refresh jobs. The isolated validation process was stopped with SIGTERM after evidence capture.
- Live browser job cancellation/detail interaction awaits a future real enqueue operation. No database seed or test-only production enqueue endpoint was added. Real service/API tests cover running cancellation, streaming progress/logs, and cleanup.
- Actual installation/backup handlers are future packages; no fabricated enqueue operation or fixture-only product prerequisite is provided here.
- This package does not qualify Minecraft versions, real server operation, Linux systemd, or native Windows execution.
- SSE replay is client recovery from durable state/output rather than a persistent transport event-ID log. Downloads cap at 5 MiB with an explicit truncation line; paginated logs remain available for remaining retained output.


## Task 09 integration extension — WithIdle

Added `jobs.Service.WithIdle(ctx context.Context, instanceID string, fn func() error) error` in owned `internal/jobs/idle.go`, with admission/dispatch changes in `service.go` and behavioral tests in `idle_test.go`.

The final integrator-approved contract uses a per-instance admission reservation. WithIdle validates the domain instance ID, callback, caller context, service availability, overlapping reservation, and queued/running job absence under the same mutex used by admission and dispatch. It then releases the global mutex before invoking the callback. Same-instance Enqueue returns `instance_busy`, dispatch skips reserved instances, and unrelated jobs continue admitting, publishing progress/logs, cancelling, and completing. A reservation does not require the instance row to exist, allowing guarded instance creation.

The callback must never call jobs methods. Lock order is **jobs reservation → native supervisor.WithStopped → filesystem/DB mutation**. No SQL transaction, open result rows, or connection remains held during the callback, so its file/DB mutations can use the database's sole connection. The callback captures ctx for its I/O and returns only after cleanup. A deferred release clears the reservation and signals dispatch after normal return, error, cancellation, or panic. Caller cancellation never releases the reservation while callback cleanup is still active.

Callbacks join the existing active worker drain group while admission is protected by the stopping mutex. Stop closes admission before waiting, waits for callbacks as well as workers, and reports its deadline honestly while a callback remains active. This avoids WaitGroup Add/Wait races and prevents shutdown from closing the database before a guarded mutation has returned.

Evidence for this extension:

- Observed `WithIdle` missing-method RED before implementation, including after the integrator refined the reservation contract.
- `TestWithIdleExcludesSameInstanceAndAllowsUnrelatedProgress`: same-instance admission and overlapping mutation rejection, callback DB write with max-one connection, unrelated worker phase/log/completion, dispatch liveness, and later same-instance admission.
- `TestWithIdleRejectsQueuedAndRunningJobs`: running and queued work both block mutation; unrelated instance IDs remain available.
- `TestWithIdleValidationErrorsAndCancellationReleaseLock`: unavailable/stopped service, invalid IDs, pre-cancelled caller, callback errors/cancellation, and successful subsequent mutation.
- `TestWithIdleCancellationRetainsReservationUntilReturn`: cancellation cannot free the reservation before cleanup returns.
- `TestWithIdlePanicReleasesReservation`: panic unwinding releases the reservation.
- `TestWithIdleParticipatesInShutdownDrain`: Stop times out while callback cleanup is active and refuses new callbacks, then drains after return.
- `go test -race ./internal/jobs ./internal/events ./internal/api`: PASS.
- `go vet ./internal/jobs ./internal/events ./internal/api`: PASS.
- `go test ./...`: PASS, including current instance, launcher, runtime, supervisor, auth, and command packages. Earlier concurrent missing-symbol and Java 16 failures recorded above were resolved by their owning workers before this final extension run.

No non-owned implementation files were changed for this extension. Nothing was staged or committed. Independent integration review and ledger updates remain the integrator's responsibility.


## Independent review fix round 1 — absolute stream authorization expiry

Review found that the 30-second stream timer was created after the initial snapshot write/flush, while later writes each received an independent five-second deadline. Slow initial or near-expiry output could therefore extend an already-authenticated stream beyond the documented 30-second limit.

Fixed only the jobs SSE handler: it now establishes an absolute expiry before lookup or output and starts its lifetime timer immediately. Snapshot, progress-event, and heartbeat writes all check expiry immediately before output and use `min(now + 5 seconds, absolute expiry)` for their write/flush deadline. An event selected at expiry is rejected even when the timer and event channels are simultaneously ready. Disconnect, reconnect authentication, and durable state/log replay remain unchanged.

Added handler-private timing arguments rather than mutable global clock state. `TestJobStreamAbsoluteExpiryBoundsSlowAndNearExpiryOutput` runs the actual stream handler through Chi and the viewer role gate, a real SQLite-backed job snapshot, and real service cancellation progress events. A local virtual clock and deadline-aware response writer model a slow initial snapshot, an event selected at expiry, and near-expiry event/heartbeat output without 30-second sleeps. All three cases were observed RED against the prior deadline behavior: near-expiry writes were accepted and an already-expired event was written. After the fix, near-expiry writes time out at the original absolute boundary and an expired event is never written.

Verification: `go test -race ./internal/api` PASS, including the new regression and existing jobs API role/privacy/stream tests. No unrelated package suites were rerun for this fix round. Changed files are `internal/api/jobs_handlers.go`, `internal/api/jobs_handlers_test.go`, and this report only. Nothing staged or committed; independent scoped review remains pending.
