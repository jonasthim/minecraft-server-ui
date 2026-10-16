# Task 19 — schedules and crash recovery groundwork

Task status: implementation and scoped evidence reported for integrator review. This is not acceptance of all Task 19: the Task 18 version-pinned automatic update transaction has not been implemented or composed. Actual Minecraft, native systemd/Windows, countdown delivery and supported-version qualification remain separate.

## Implemented

- Durable per-instance schedule definitions and occurrence history for backups, approved commands, announcements, reviewed lifecycle restarts, and the typed future automatic update producer.
- Definitions default disabled. Each has a stored creator account, explicit effective IANA timezone, next occurrence, enabled/disabled reason and latest occurrence/job state. Only the owner operator or an administrator may edit/delete; another actor editing takes current ownership explicitly. Dispatch reloads account state and current selection, then feature workers reauthorize again.
- Existing five-field robfig cron grammar and descriptors remain available. An empty timezone resolves the host IANA name through configured name, TZ/time.Local or Linux zoneinfo information. Unresolvable Windows labels require an explicit IANA selection; no mutable `Local` alias or guessed timezone is stored. Embedded Go timezone data serves native Windows. `TZ=` and `CRON_TZ=` expressions are rejected in favor of the single timezone field.
- DST missing times are skipped; repeated wall-clock occurrences have separate UTC identities. `@every` is elapsed time, bounded to whole seconds between one minute and 365 days. Impossible expressions are rejected. Restart defaults to 300 seconds, with explicit 0 through 3600 preserved.
- Startup reconciliation records one concrete `skipped_downtime` report and moves unclaimed triggers into the future. There is no downtime flood. During live processing, overdue triggers at least one minute old also skip. Only one outstanding occurrence per definition is retained; subsequent triggers record `skipped_overlap`. Different definitions use the jobs queue's per-instance serialization.
- Schedule claiming, immutable action snapshot and occurrence identity commit before feature admission. The private `schedule:<occurrenceID>` key is passed to typed feature adapters. If admission acknowledgement is lost, the same durable job is recovered. Claimed intent survives restart even though unclaimed missed triggers are skipped.
- Jobs-owned idempotent admission atomically stores a canonical request fingerprint, private key and job row. A mismatching request conflicts; terminal replay returns the original job. Admission tombstones intentionally survive job/history/instance retention and cannot permit an old key to execute again. History removed after admission yields a conflict rather than a new job.
- Typed adapters preserve reviewed authorization, audit, validation and workers: console `EnqueueCommandOnce`, backup `EnqueueAutomaticOnce` (fixed automatic trigger), lifecycle `EnqueueRestartOnce` (no emergency skip). No arbitrary shell or generic job route was added. No nested feature enqueue occurs inside scheduler jobs.
- Automatic update capability is `EnqueueAutomaticOnce(context.Context, key string, instanceID string, actor domain.User) (jobs.Job, error)`. Task 18 must derive the current pinned Minecraft selection and own preview/compatibility/conflict/backup gates. Missing capability is explicit: update definitions may be saved disabled; enabling is refused and no successful update job is fabricated.
- Crash recovery defaults disabled and requires explicit administrator configuration per instance. It observes exact native identity/exit proof; missing/uncertain proof, a newer process generation, a deliberate exit, or a running but unready JVM never qualifies. Persisted attempts and deliberate-stop epochs survive manager restart.
- Retry delays are 10, 30 and 60 seconds, with at most three native start admissions in an inclusive rolling ten-minute window. Each admission is durable before native I/O; a crash in that boundary conservatively consumes an attempt. Preparatory failure or manager interruption before native admission requires review of that generation rather than an endless preparation loop.
- Lifecycle recovery reuses the original jobs execution, installation/activation/EULA checks, native start and readiness path. Original durable requester/kind/instance binding rejects forged or copied execution authority. The scheduler lock encloses only the stop epoch/exact exit check, durable attempt reservation and native start; readiness waits happen after lock release.
- A deliberate stop/uninstall hook persists a newer epoch before queue admission, cancels pending attempts and running recovery waits, and suppresses retries of the exact stopped generation. A later manual start can create a new generation. If stop wins the guard, native start never runs; if native start wins, the ordinary queued stop handles that generation.
- Crash notification intent is a separate durable scheduler outbox, one event per hash of exact native identity. A lost publisher acknowledgement retries the same key. `notify.EnqueueServerCrash` accepts only a bounded opaque ID, instance and occurrence time. The finite `server_crashed` kind binds only `crash_recovery`; it never includes PIDs, arguments, raw exit reasons or native errors. Existing success/failure operation allowlists, terminal trigger and transport behavior are unchanged. Channels remain disabled by default and validate all three event choices without duplicates.
- Mantine schedule/recovery UI uses generated API types and role-aware controls, explicit success/failure messages, next/last run, reasons and safe job/history links. New definitions are saved disabled; unavailable update capability is visible.

## Owned and explicitly delegated files

Original ownership: `internal/scheduler/**`, migration `00015_schedules.sql`, new `internal/api/schedule_handlers*.go`, `docs/api/schedules.yaml`, `web/src/features/schedules/**`, this report.

Integrator explicitly delegated these narrow shared changes:

- `internal/jobs/idempotent.go`, tests, `00016_job_admissions.sql`, and minimal `jobs/service.go` enqueue extraction.
- New console/backup/lifecycle `scheduled.go` adapters/tests, with minimal extraction of existing enqueue methods into private helpers.
- Lifecycle `Config.StopIntent`/service field and pre-admission stop hook; `startGuarded` extraction around only native start; `StartForRecovery` original-execution authority check.
- New notify crash producer/tests; finite enum/config/service validation changes; notification UI choice; `providers.go` adds only the fixed unexpected-exit message branch.

The integrator owns central OpenAPI merge/type generation, main/router/App wiring and acceptance ledger. No dependencies, subagents, worktrees, stage or commit actions were performed by this worker. No actual game, EULA acceptance, native unit installation, host deployment or external messages occurred.

## Integration contract

`New(scheduler.Config{DB, Log, HostTimezone, Commands: controls, Backups: backups, Restarts: lifecycleService, Updates: nil, Native: native, Lifecycle: lifecycleService, Jobs: queue, OnCrash: publisher})`.

Bind lifecycle `StopIntent` through a captured scheduler pointer closure. Register `scheduler.RegisterJobs()` before queue registration freezes. After paused queue recovery, lifecycle/backup/content recovery, call mandatory `scheduler.Reconcile(ctx)` before autostart/readiness and queue Resume. Run `scheduler.Run(ctx)` in the manager's cancellation/draining loop. `Run` retains intent and reports safe logging on transient tick errors. Bind publisher to `notify.EnqueueServerCrash(ctx,event.ID,event.InstanceID,event.OccurredAt)`.

Mount `api.MountSchedules` inside authenticated/CSRF protected routing. The fragment uses the existing `sessionCookie`/`personalToken` security schemes after integrator normalization. Root merged the fragment and the four notification enum locations and regenerated types; `countdown_seconds` is optional with a descriptive default because the generator otherwise marks OpenAPI default properties required.

## Scoped verification

Observed commands:

- `go test ./internal/scheduler ./internal/jobs ./internal/lifecycle ./internal/console ./internal/backup ./internal/notify ./internal/api` — PASS.
- `go test -race ./internal/scheduler ./internal/jobs ./internal/lifecycle ./internal/console ./internal/backup ./internal/notify` — PASS.
- `go test -race ./internal/scheduler` after adding the queue/window/publisher tests — PASS.
- `cd web && npm run typecheck && npm run lint` — PASS against centrally regenerated schema.

Tests cover Stockholm DST gap/fold, elapsed interval semantics, expression/timezone/action bounds, default/zero countdown, unsafe commands and announcement text, role changes, lost enqueue acknowledgement, restart reconciliation, concurrent schedulers, overlap reports, missing update producer, idempotent admission transaction rollback and tombstones, typed automatic-backup trigger, approved-command and restart reauthorization, HTTP roles/strict input, exact crash proof, deliberate stop across a fresh service, native generation replacement, durable admission before native I/O, rejection of repeated consumed admission, three actual queued retry attempts with increasing delay and refusal of a fourth, inclusive ten-minute boundary, forged recovery execution rejection, and crash publisher acknowledgement recovery.

## Remaining evidence and limits

- Task 18 composition and its real backup/conflict/pinned-update evidence remain required for full Task 19 acceptance.
- Real online backups, acknowledged restart/announcement countdowns and actual game/client behavior remain unqualified by these deterministic package tests.
- Native systemd manager restart with live games and real Windows intentional shutdown/autostart must be qualified on their target operating systems. Cross-compilation is not native Windows qualification.
- The native recovery guard assumes the manager's established single-process ownership lock. SQLite admits recurring work safely across concurrent service instances, but process-control locking is per running manager.
- `schedule_runs`, crash evidence and admission tombstones currently retain durable history without pruning. Future retention must preserve admission tombstones and must not turn missing history into permission to reexecute.
- UI browser evidence is recorded below after the isolated local composition check; no production profile or existing user data is used.

## Composed startup and 390 px browser evidence

The integrator composed main/router/UI and generated contracts. Added delegated `cmd/minecraft-ui/main_schedules_test.go`; `go test ./cmd/minecraft-ui -run '^TestStartupSchedulesReconcileBeforeAutostartAndDispatch$'` PASS. The test proves the scheduler reconciliation capability is mandatory, runs after interrupted world export cleanup while retained jobs remain paused, and precedes Windows autostart and Resume. Injected failure and missing capability prevent later autostart/dispatch and retain the paused queue. This is deterministic startup ordering evidence, not native Windows qualification.

Browser plugin runtime discovery reported no available browsers. The integrator explicitly approved isolated repository Playwright fallback and production asset/binary rebuild after an actual accessibility finding. A fresh temporary database on loopback port 18178 was used. The shipping UI created the first administrator and the server definition, selecting real official 1.21.9 metadata. The server remained uninstalled and EULA-unaccepted throughout. No DB fixtures, actual game start, unit installation or external messaging occurred.

Observed at 390 × 844:

- Disabled announcement creation with explicit Europe/Stockholm timezone; empty history; edit to a restart with explicit zero countdown; enable and disable; next-run reporting; saving unavailable pinned-update definitions disabled and disabled Enable control.
- Recovery starts disabled and presents explicit administrator enable. No actual crash recovery was enabled in this browser check.
- All three schedule dialogs now give their close buttons accessible names. An initial browser check found the missing names; the fix was rebuilt and verified. History close works with focused Enter, editor close with Escape, and deletion confirmation close with focused Enter. Escape cancellation preserves definitions.
- Viewer/operator accounts were created through the shipping administration form. In separate clean browser contexts, viewers have no schedule mutation or recovery controls; operators cannot edit the administrator's schedules, can create their own approved-command schedule, see exactly that schedule's Edit control, and have no administrator recovery button.
- All checked schedule pages had document width 390 equal to viewport 390 and no page errors. The admin screenshot was visually inspected after scrolling to the top.

Final browser output: `PASS`, zero countdown preserved, unavailable update disabled, all three named keyboard closes verified. Separate viewer and operator outputs: `PASS`, width 390, no page errors.

Artifacts (temporary local evidence):

- `/home/thim/.cache/dilla-tmp/task19-schedules390.png`
- `/home/thim/.cache/dilla-tmp/task19-schedules-viewer390.png`
- `/home/thim/.cache/dilla-tmp/task19-schedules-operator390.png`
- Reproduction scripts: `task19-browser.mjs`, `task19-roles-browser.mjs`, `task19-final-browser.mjs` in the same temporary directory. The long workflow script stops after user creation because its direct mobile Sign out target is in the closed navigation; role checks were completed using separate clean browser contexts. This was test navigation, not a scheduler failure.

Post-fix verification: `npm run build`, temporary `go build`, `npm run typecheck`, `npm run lint`, `go test -race ./internal/scheduler`, and the targeted startup test all PASS. The isolated test manager was shut down after evidence collection. The broader touched-package tests/race checks above passed before the UI-only label correction; unaffected suites were not repeated.

## Independent review fix round 1 — base `37df25b`

The independent groundwork review requested changes for two Important findings. Both were reproduced by deterministic regressions before implementation. Only owned scheduler files and this report were changed; there are no schema, UI, lifecycle, native, notification or main composition changes in this round.

1. **Deliberate stop with uncertain native identity:** `CancelRecovery` previously preserved enabled recovery while recording an empty suppression generation after identity lookup failure. It now persists `enabled=false` in the same transaction as the newer stop epoch and cancellation whenever the native service is absent, identity lookup fails, or returned identity is incomplete/mismatched. A persisted safe reason explicitly requires administrator review and re-enablement. Restored observation or a fresh manager service cannot silently lift that suspension; even a later observed generation stays suspended until the existing administrator configuration API explicitly enables recovery. A successfully identified stop still suppresses its exact generation while preserving the existing configured policy.

   Regression: `internal/scheduler/recovery_test.go`, `TestDeliberateStopWithUncertainIdentitySuspendsRecovery`. Three cases (lookup failure, absent native capability, incomplete identity) each reproduced the original unsafe new pending attempt. The fixed test checks cancelled prior intent, persisted disabled state/reason through a fresh service, no retry after restored/successor identity observation, refusal of operator re-enablement, and successful explicit administrator re-enablement. Existing public `enabled` and `reason` fields expose the suspension; no new API field is needed.

2. **Startup batches above 100 schedules:** startup reconciliation now captures one fixed startup cutoff and drains each bounded due batch before returning. Every definition that was due at that cutoff records a downtime skip and advances beyond the same cutoff before ordinary timer processing or startup queue Resume can follow. Advancing wall clock between batches cannot cause future live occurrences to be consumed by startup. Cancellation is checked between batches and SQL operations keep their existing context cancellation.

   Regressions: new `internal/scheduler/dispatch_test.go`. `TestStartupReconcileDrainsAllDueSchedulesAtFixedCutoff` creates 101 enabled definitions across two instances (respecting the 100-per-instance bound), restarts ten seconds after their due time, and first reproduced only 100 skips. It now verifies all 101 downtime records, no due definition left behind, all next triggers at 12:02 using one captured cutoff despite an advancing injected clock, and no producer admission on the first normal tick. `TestStartupReconcileCancellationReturnsBeforeAdmission` verifies a cancelled startup returns cancellation without admission and a successful reconciliation retry still skips the missed trigger before the first timer tick.

Observed scoped commands/results:

- `go test ./internal/scheduler -run '^TestDeliberateStopWithUncertainIdentitySuspendsRecovery$'` — FAIL before the fix in all three cases; PASS after the fix.
- `go test ./internal/scheduler -run '^TestStartupReconcile'` — FAIL before the fix (`startup skipped 100 schedules; want all 101`); PASS after the fix.
- `go test -race ./internal/scheduler` — PASS after both fixes, including existing policy, admission, recovery and dispatch tests.
- `git diff --check -- internal/scheduler docs/reports/task19.md` — PASS.

Unchanged broader suites and browser checks were not rerun. The integrator owns review acceptance and commit; this worker performed no stage/commit action. Full Task 19 still requires Task 18 composition and the previously recorded real game/native qualification.
