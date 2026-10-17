# Task31D2 backend — desired settings for the next managed start

2026-10-17. Submitted for integrator and independent review. This worker does not accept the package or update the implementation ledger.

Read the approved Minecraft design, original implementation plan, implementation ledger, corrective parity Task31/brief, frozen Task31D2 backend brief, and session-start wiki context. The integrator owns migration20, HTTP/schema/generated types, application guards and wiring, and UI. No dependencies, services, accounts, game instances, EULA consent, user preview, vault content, staging or commits were changed.

## Delivered behavior and authority

- `PendingSettingsDraft`, `PendingSettings`, `SavePendingSettings`, guard option, and GET/save/discard service methods implement the frozen interface. Saving is DB-only even when the native stopped fixture reports running. Active instance data, managed files and configuration recovery intent remain unchanged. The guard is mandatory and injected by the integrator; tests also exercise actual queue `WithIdle` admission.
- Saves validate the entire desired instance/configuration synthesized from active identity, retain the exact stored active revision as the base, require a fresh enabled administrator inside SQL, and compare pending and active revisions. The safe JSON integer limit is 9007199254740991; operations refuse overflow. Same-author/same-desired retries return the existing revision. A deliberate save by a different fresh administrator creates a new revision and originator, allowing reauthorization after revocation. Discards and applies retain revisioned NULL tombstones.
- Strict stored draft validation rejects unknown fields, missing/null desired/configuration fields, invalid values, secrets/manager-owned properties, changed world directory, malformed metadata and oversized JSON. Version, port, runtime and EULA are absent from the pending type. Audits use authored action names and empty details, never configuration JSON.
- Ordinary stopped `Update` refuses an existing draft with explicit discard/rebase guidance. Its original configuration transaction was factored into private `updateStopped`; file snapshots, ownership/recovery checks, file-only extras, config intent, memory activation coordination, SQL/audit sequencing, rollback and cleanup remain shared.
- `StoppedSession.ApplyPendingSettings` binds the exact original session and execution, session lifetime/overlap checks, `CheckCurrent`, authoritative instance and durable running job ID/kind/requester/payload. Public `Execution.Job` and `Payload` fields grant no authority. Only durable lifecycle start/restart and `crash_recovery` jobs qualify. `CheckCurrent` runs before SQL; inside the transaction only SQL checks run, avoiding queue mutex/sole-connection inversion. The original mutation admission remains held, with no queue or native lock reentry.
- Pending consumption and the active settings update share the same SQL transaction, including fresh originator authorization and base/revision CAS. Own running job and serialized queued successors are permitted; other running jobs are refused (the DB also has its unique running-instance constraint). Stale or revoked-author drafts refuse start and remain visible for deliberate save/discard.
- Ordinary `prepareLaunch` applies after stopped admission and activation recovery, then reloads actual detail and installation before materialization and sealing. Memory activation commits with active configuration. A later readiness failure leaves the honestly committed active settings and consumed tombstone intact. Autostart policy preparation uses the prior active configuration. Existing version update/rollback participants and `StartForUpdate` do not consume drafts; version selection commits leave them stale.

## Verification and observed red cases

Initial API tests failed to compile because the frozen pending API did not exist. Before ordinary start integration, `go test ./internal/instance ./internal/lifecycle -run 'TestPendingSettings|TestOrdinaryStart' -count=1` failed: successful preparation left the active name/memory unchanged, and stale/revoked/audit/seal scenarios reached native fixture start. It passed after integration. Stored missing/null boolean/extra regressions failed before strict required-field checks and passed afterward. `TestPendingSettingsAnotherAdminCanReauthorizeSameDesiredDraft` failed with old author1/revision1 before the same-author idempotency restriction, then passed with author2/revision2.

An intermediate capability test attempted to insert a second running job for an instance; the existing unique DB constraint correctly rejected that unreachable state. Its blocked fixture then hung in cleanup. The impossible corruption case was removed and waiting callbacks made cancellation-aware; this was a test fixture failure, not product deadlock evidence.

Final commands, all exit0:

```text
go test ./internal/instance ./internal/lifecycle -count=1 -timeout=60s
  instance 0.565s; lifecycle 0.451s

go test -race ./internal/instance ./internal/lifecycle ./internal/jobs -count=1 -timeout=120s
  instance 19.452s; lifecycle 14.318s; jobs 9.463s

go test ./...
  all Go packages passed, including composed API/update/recovery/scheduler tests

go test -race ./internal/instance -run '^TestPendingSettings' -count=1 -timeout=60s
  instance 7.330s (after final author-idempotency and public-field forgery regression changes)

go vet ./internal/instance ./internal/lifecycle ./internal/jobs
  no findings

git diff --check -- internal/instance/service.go internal/instance/session.go internal/lifecycle/operations.go internal/lifecycle/operations_test.go
  clean
```

New tests use actual migrated SQLite, actual queue admissions and explicitly labeled native fixtures. Coverage includes DB-only running save, viewer/revoked author, pending/active CAS, idempotency, tombstone revision cycles, stale selection/rebase, level-name and secret rejection, mandatory guard/overflow, incomplete stored JSON, competing saves, actual same-instance jobs and queued successors, forged/copied/unbound/expired capabilities, memory activation, SQL/audit/deferred-commit/seal failures, unsafe managed-path refusal, ordinary no-pending compatibility through the existing package suite, autostart isolation and the actual update-start path. Deferred-commit injection verifies active memory and seal remain old and draft remains pending when SQL commit fails. The unsafe managed path case refuses during snapshot validation; it is not evidence of failure after file writing has begun. Existing shared transaction recovery tests continue to cover write/recovery behavior.

The ordinary-start fixture deliberately has no readiness transport: it asserts actual fixture native start, committed settings, memory seal and tombstone, followed by an honestly failed readiness job. The current-job instance tests separately assert successful atomic apply under real queue authority. This does not qualify real games, client connectivity, native Linux/Windows services, distribution/version combinations, direct OS autostart behavior or stable release support. Independent review, composed HTTP/UI acceptance and ledger updates remain with the integrator.
