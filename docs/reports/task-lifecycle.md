# Lifecycle coordinator — operational integration

Submitted for integrator review, 2026-10-16. This continues the preserved implementation against `600b502` and resumed at `3601c2e` after the full-access transition. Package acceptance belongs to the integrator. No Minecraft process was run, no EULA was accepted, and no host service was installed or changed. Native game/platform qualification remains pending.

## Owned implementation and composition

- `internal/lifecycle/` and migration `00005_lifecycle.sql`: typed durable install/start/stop/restart/autostart/uninstall jobs; persisted exact install previews; immutable generation references; activation intent/recovery; configured native autostart state; private generation-bound startup rule verification. Providers, runtime manager, native supervisor and console remain the authoritative implementations.
- `internal/api/lifecycle_handlers.go` and tests; `docs/api/lifecycle.yaml`: viewer state/catalog reads, operator previews and normal operations, administrator autostart/uninstall/emergency stop. Unknown operations/private job kinds and additional executable/path fields are refused. Request auditing excludes selection payloads, paths and credentials; workers recheck current actor role/disabled state.
- `web/src/features/lifecycle/LifecyclePanel.tsx`: generated API types, exact resolved artifact/checksum preview and confirmation, durable job progress/output link, separate JVM/readiness state, normal countdown and explicit emergency confirmation, native autostart uncertainty, uninstall retention disclosure.
- Delegated composition in `cmd/minecraft-ui/main.go` and narrow integration tests: current-job stopped guard; all five providers and managed Java; lifecycle/console/world API mounting; paused queue recovery; Windows prior-process stop/wait and validated autostart; Linux process preservation; private readiness jobs; world orphan export reconciliation before HTTP; queue resume. Background console exits trigger bounded HTTP shutdown with a safe error; cancellation drains it before database closure. Queue and Windows native shutdown retain existing concurrent bounded drain.
- Delegated `InstancesPage.tsx`/`App.tsx`: server operation panel; console/world links and console route; creation uses official version catalog candidates and no fabricated version default, while existing selections remain pinned.
- Console owner alone merged `docs/api/{lifecycle,console,worlds}.yaml` into the root schema and regenerated `web/src/api/schema.d.ts`. Generated types were not hand-edited by lifecycle.

## Safety and recovery behavior

Install previews bind instance revision and original exact tuple for thirty minutes. Resolution precedes confirmation. Initial loader/build selection can fill a blank only before committed installation/generation history and while not installed; commit conditionally checks revision, Java major and the blank pin. Reinstall cannot change existing pins. Minecraft/flavor changes still require the separate compatibility/safety-backup workflow.

The stopped session covers recovery, managed runtime/provider installation, materialization and seal. Provider bytes remain immutable proposals. An activation intent precedes offline-verified `launch.json`, and the installation/runtime/provisioning pointer commit determines recovery. An interrupted uncommitted proposal reseals the previous DB generation; a committed proposal reseals the new one. Generation references remain retained. Worlds are never rewound.

Manual start and enabling autostart on a stopped server prepare the same current persisted memory and private management configuration under stopped admission. Native start happens after releasing stopped admission while the queue still serializes the instance. Explicit EULA acceptance is required. Native start independently verifies the managed Java/artifacts again. Its returned exact JVM identity binds the subsequent readiness session. Ten-minute active readiness probes apply/read back required native rules; readiness failure does not stop or blindly retry a running game. Durable authored job output reports the current process result even after cancellation closes mutation admission.

Normal stop/restart defaults to 300 seconds, with a 0..3600 range. Fresh typed player observations and acknowledged announcements bracket the countdown. Cancellation before native stop leaves the game running. Administrator emergency stop requires exact instance-ID confirmation and may skip the countdown. Native stop still reports unclean/forced outcomes honestly; failed stop prevents restart. Deliberate native stop intent is owned by the supervisor; no autonomous crash retries were added.

Uninstall disables native autostart first and requires a stopped instance plus exact ID confirmation. It removes only the active launch and installation pointer. Worlds, configuration, local content, backups, Java caches and immutable executable generations remain. Permanent instance deletion remains separate.

Configured autostart records desired/applied separately and retains `applied=null` plus a safe error on native failure. Windows startup skips instances with retained queued work and verifies/rebinds current memory before boot. Linux manager restart preserves existing games. Private `lifecycle_verify` captures the exact healthy running identity after reconciliation, writes a system audit with NULL actor, queues requester 0 work, and rejects changed generation within a live console session. It never calls Start/Stop. Console `Refresh` remains read-only.

## Evidence

- Intentional initial red test `TestStartupVerificationOnlyQueuesFreshRunningGames` failed compilation until `VerifyRunningAfterReconcile` was implemented.
- `go test ./internal/lifecycle ./internal/api ./cmd/minecraft-ui`: PASS after adding role/input tests, provider-failure preservation, initial pin/history/stale preview checks, fresh acknowledgement versus stale JVM generation, no-readiness running-process diagnostics, verified countdown cancellation, emergency native failure, autostart memory/queued conflicts, retained uninstall data, and pre/post-seal commit interruption recovery.
- Shared startup regression uses actual SQLite/queue/services and a retained lifecycle job: paused dispatch preserves its private payload, world orphan cleanup completes, Resume dispatches it, and the missing EULA fails before any native Start. It would fail if export cleanup reacquired `WithIdle` over retained queued work.
- `go test -race ./internal/lifecycle ./internal/api ./cmd/minecraft-ui`: PASS for the initial integrated lifecycle/API/main changes; final hook/startup verification is recorded below.
- `npm --prefix web run typecheck`, `npm --prefix web run lint`, `npm --prefix web run build`: PASS for the integrated generated-type UI. Root owns the actual composite 390px/keyboard browser review.
- `go build -o /home/thim/.cache/dilla-tmp/minecraft-ui-lifecycle-preview ./cmd/minecraft-ui`: PASS. This binary is a local review artifact only.

## Remaining integration review

Linux next-boot memory correctness is implemented through the delegated instance/activation hook and protected boot interruption guard described below. Integrator review of that cross-package boundary remains required. Existing real systemd/native Windows, real Minecraft/client readiness, MSMP wire behavior and supported-combination matrix qualification remain pending. Cross-compilation/package fixtures do not establish these claims.

No subagents, staging or commits were performed by this worker. No new dependency was added by lifecycle. Vault changes remain outside this worker's scope; a focused session note can be preserved after integration review through the required Obsidian transaction flow.

### Memory-edit integration extension (approved by integrator)

The integrator delegated `internal/instance/service.go`, new instance activation hook/tests, `internal/launcher/run.go` plus protected marker helpers/tests, and exactly the pre-start pending check in `internal/supervisor/supervisor.go`. `WithActivationCoordinator` supplies a prepared `ConfigActivation` with `Apply(ctx,*sql.Tx)` and `Finish(ctx)`. Installed memory edits fail explicitly if the coordinator is missing. Uninstalled edits and installed edits that retain the same memory keep their prior behavior.

A lifecycle SQL intent is durable before publishing `server/.manager/activation.pending.json`. The protected marker has a fixed name and canonical instance-bound content; unreadable, malformed, oversized, linked/replaced marker/parent state fails closed. It is a blocker, never an independent memory-authority file. Marker file and parent entries are synced on Linux. The existing `.manager` native protected mount covers it.

The instance SQL transaction changes persisted memory/config and the lifecycle generation/installation together. `Apply` does offline seal and writes only through that existing transaction; it never reacquires queue/native locks or queries the service DB while the transaction owns its connection. Production seal syncs the launch parent after the verified file replacement. Successful commit clears the instance config journal before bounded independent `Finish` reseals the committed generation and removes the blocker, then clears the lifecycle SQL intent. Failed rollback leaves the blocker when config recovery cannot be verified. Every ordinary lifecycle activation and uninstall also uses the same blocker protocol. Native.Start checks it before sealing/platform start; independent launcher Run checks again under lifetime admission, including systemd boot before manager startup. Offline Seal and stopped admission remain available for recovery.

Additional deterministic evidence passes: successful 1024→4096 MiB edit updates DB+installation+next-boot bytes without changing desired/applied autostart; before-seal/after-seal/cancel failures restore all three to 1024; crash after marker but before config journal restores the old generation; crash after DB commit before Finish retains the new 4096 generation and recovers it; seal cleanup failure and unsafe config restoration retain blocker/journals; independent boot refuses pending state before even loading/verifying a launch; malformed/duplicate/unknown-field/trailing JSON or linked protected parent refuses launch and refuses silent blocker deletion.


### Startup recovery and browser follow-up

The integrator separately assigned `jobs.WithPausedInstance(ctx,id,fn)` to the jobs worker. Lifecycle consumes it through startup-only `instance.WithRecoveryMutationGuard` and `stoppedMutationGuard.WithStoppedForRecovery`; the sole `instance/reconcile.go` change routes startup handling through that guard. Ordinary CRUD/materialization keeps `WithIdle`, and current-job mutations retain their existing execution admission. A recovery callback holds native stopped admission once. Retained queued jobs do not defer config restoration; live Linux instances still defer without a stop.

The shared production-startup regression now injects a real retained queued lifecycle job, a valid uncommitted config snapshot/journal, a protected activation marker and orphan export ZIP. Config recovery restores old files first; lifecycle then removes the obsolete launch/marker; world export cleanup completes; Resume dispatches the unchanged queued job, which fails its missing-EULA check before native Start. A separate test confirms a live JVM leaves its pending config journal untouched and never receives Start/Stop.

Root's actual 390px browser review found and corrected the HTML `pattern` hyphen escape for Chrome Unicode Sets (`v`) mode. A Node `RegExp(...,'v')` check accepts `my-server` and rejects traversal. Root independently observed first-admin → official searchable 1.12.2 selection using ArrowDown/Enter → first server with EULA unchecked, with no horizontal overflow. The complete browser report remains root-owned.

The browser's real install job correctly failed native admission on this host without installed service units. A follow-up ensures that pre-callback native admission failure produces the actionable safe message: “Native service is unavailable or does not match this manager configuration. Install or repair the approved native service before retrying.” The guard tracks callback entry, so download/config failures are never mislabeled as host service failures. Raw causes/paths never enter durable output. Lifecycle state now preserves a known-live `ProcessRunning=true` observation even when the native control channel errors; process status stays unknown and administrator emergency stop remains reachable. A regression covers this distinction.

Normal console cancellation errors are ignored only when the owning context was cancelled; unexpected console-loop exit still stops HTTP within its bounded shutdown. Console background work drains before DB closure.

Final scoped evidence: `go test ./internal/lifecycle ./internal/instance ./internal/launcher ./internal/supervisor ./internal/api ./cmd/minecraft-ui` passed after all startup/hook/diagnostic changes. Final `go test -race` and `go vet` over the same six packages passed. `GOOS=windows GOARCH=amd64 go build -o /home/thim/.cache/dilla-tmp/minecraft-ui-lifecycle-windows.exe ./cmd/minecraft-ui` passed; this is compilation only. Final UI `npm --prefix web run typecheck`, `npm --prefix web run lint`, and `npm --prefix web run build` passed after the pattern fix. The final embedded Linux preview binary was rebuilt at `/home/thim/.cache/dilla-tmp/minecraft-ui-lifecycle-preview`; actual browser follow-up and package acceptance remain integrator-owned.

Final lifecycle-owned files: `internal/lifecycle/{service,operations,verify,config_activation,sync_linux,sync_other}.go`, associated journal/operation/config-activation tests; `internal/api/lifecycle_handlers{,_test}.go`; migration `00005_lifecycle.sql`; `docs/api/lifecycle.yaml`; `web/src/features/lifecycle/LifecyclePanel.tsx`; this report. Delegated cross-package files: `cmd/minecraft-ui/main.go`, `main_lifecycle_test.go`, `web/src/App.tsx`, `web/src/features/instances/InstancesPage.tsx`, `internal/instance/service.go`, `reconcile.go` (single startup guard route), new `activation.go`/`activation_test.go`, `internal/launcher/run.go`, new `activation.go`/`activation_test.go`, and the three-line pending check in `internal/supervisor/supervisor.go`. Jobs files and shared schema generation belong to their assigned owners.

## Lifecycle review fix round 1 — SQLite commit durability

BASE `a108fac`; one Important review finding addressed. The on-disk database used WAL with `synchronous=NORMAL`. SQLite explicitly permits a committed WAL transaction to be lost on power failure in this mode. That undermines the ordering between the SQL activation/config journal and the separately synced launch/blocker files. The change uses `synchronous=FULL`; existing lifecycle/config file ordering is unchanged. Primary reference: [SQLite synchronous pragma](https://www.sqlite.org/pragma.html#pragma_synchronous).

Only `internal/db/db.go`, `internal/db/db_test.go`, and this report changed in this fix pass. No migration, dependency, API/UI, lifecycle state-machine, native service, or catalog changes were made by this worker. Integrator review remains required; no staging or commits were performed.

`Open` now retains WAL, foreign keys ON and the 5000ms busy timeout, and requests FULL. A private connector wraps the existing installed SQLite connector and reads back actual `journal_mode=wal`, `synchronous=2`, `foreign_keys=1` and `busy_timeout=5000` before each physical disk connection enters the pool. Missing/unexpected pragma results or mismatched values close and reject that connection. Driver DSN configuration and verification both run on replacement connections. The one-open-connection contract remains intact. `OpenMemory` retains its original direct SQLite opening and isolated memory-journal behavior.

Evidence:

- RED: `go test ./internal/db -run '^TestOnDiskDurabilityAcrossConnectionReplacementAndReopen$' -count=1` failed against the old production setting with `synchronous=1 want2`.
- GREEN: `go test ./internal/db -count=1` passed. The actual disk-file regression verifies WAL/FULL/foreign-key/busy-timeout and one connection; commits a row; forces `driver.ErrBadConn` and verifies the physical connection is replaced; verifies the settings again; closes/reopens and checks settings plus the committed row. Negative tests use actual disk files to refuse DELETE journaling, NORMAL synchronization, disabled foreign keys and an incorrect busy timeout. Memory-journal/single-connection/isolation regressions pass.
- `go test -race ./internal/db -count=1`: PASS.
- `go vet ./internal/db`: PASS.
- `git diff --check`: PASS.

This verifies SQLite's configured policy, rejection behavior, physical connection replacement and ordinary reopen persistence. It does not simulate or qualify power failure, filesystem/hardware flush guarantees, native Windows execution or real Minecraft behavior. UI and broad suites were not repeated for this database-only fix.
