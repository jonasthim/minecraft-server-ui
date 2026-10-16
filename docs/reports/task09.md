# Task 09 — instance CRUD, paths, ports and configuration

Delivered for integration review. The integrator must review acceptance evidence and update the implementation ledger before marking this package complete.

## Delivered behavior

- An empty database can create, list, read, edit and permanently delete multiple independent instance records and directories. The shipping Servers inventory has first-use create actions; no fixture or manual SQL is required. Detail/settings and typed destructive confirmation are reachable from the inventory. Administrator mutations and viewer/operator reads have explicit endpoint gates; mounting belongs inside the existing authenticated + CSRF API group.
- Canonical directories are `dataDir/instances/<validatedID>/{server,logs,backups,state}`. Shared immutable runtimes remain `dataDir/runtimes` and are never removed by instance deletion. Strict ids, portable Windows device names and invalid characters, relative traversal, symlink components, and unsafe path areas are rejected. Actual owned file mutations use `os.Root`; directory deletion does not follow nested symlinks.
- Unique SQLite game-port reservation is checked transactionally and backed by the existing unique constraint. Two concurrent creates for one port yield one success. All instances bind their selected game port; per-instance custom bind/RCON/query/MSMP configuration is excluded from user extras.
- Typed config enforces difficulty/mode enums, max-player and view/simulation bounds, one portable world directory, bounded seed/MOTD and extra-property sizes. Java properties encoding safely escapes key/value separators, continuation characters, Unicode and line breaks. Existing unknown file properties are decoded and preserved, including escaped separators, Unicode and continued lines; manager-owned dangerous controls and credential fields are stripped. Explicitly removing a known extra from the form removes it.
- The manager owns RCON/query/MSMP control settings, disabling those interfaces until their reviewed integrations are available. Server properties and EULA files are staged with private modes, fsynced and atomically renamed. Ordinary failed updates compensate files with their prior canonical settings.
- EULA defaults false. Explicit acceptance records the authenticated administrator and UTC time alongside instance settings, writes `eula=true` only for that explicit persisted acceptance, and creates a separate audit event. Ordinary saves cannot implicitly accept or revoke it.
- Java requirements are derived by `runtime.Select` from the requested Minecraft/flavor selection. The UI presents automatic managed Adoptium Temurin selection. These are reviewed structural rules, not a qualification claim; exact published build/provider compatibility is still an installation gate. Minecraft/flavor/loader/Java changes are blocked pending the provider compatibility and safety-backup workflow. The world directory is immutable after creation.
- Every create/update/delete and explicit EULA acceptance is audited in the same SQL transaction, with no raw config or credentials in audit details.
- Lifecycle exclusion is injected and fails closed if absent. Mutations must hold jobs admission/dispatch exclusion outside native supervisor stopped/lifecycle/OS exclusion for their full filesystem and SQL work. An additional in-transaction check rejects queued/running jobs. No default stopped status or success-only install/start endpoint exists. Newly created records truthfully report `not_installed`.
- The Mantine interface follows the existing forest palette and shell; settings use one column at narrow sizes, wrapping controls and text, labeled inputs, focus-trapped dialogs, explicit success/failure feedback and role-aware actions. Only generated API DTOs are used.

## Frozen handoff

```go
paths, err := instance.NewPaths(dataDir)
service := instance.New(database, paths, guard)
api.MountInstances(protectedRouter, service)
```

`Paths.For(id) (Directories,error)` returns Root, Server, Logs, Backups, State and RuntimeCache. `Paths.Resolve(id, area, relative) (string,error)` accepts server/logs/backups/state and portable relative components. Returned strings alone are not race-safe handles; consumers performing I/O should use contained `os.Root` operations as this package does.

`MutationGuard.WithStopped(context.Context, string, func()error) error` must compose jobs `WithIdle` outside native supervisor `WithStopped`. Unknown verification returns unavailable, active processes return instance_running, queued/running jobs return instance_busy. The callback must not call jobs methods. `nil` guard blocks all existing-instance updates and deletes.

Service methods are `List(ctx) ([]Detail,error)`, `Get(ctx,id) (Detail,error)`, `Create(ctx,actor,draft) (Detail,error)`, `Update(ctx,actor,id,draft) (Detail,error)`, and `Delete(ctx,actor,id,confirmation) error`. `Draft` contains instance, config and accept_eula. `Detail` contains instance, config, eula and truthful persisted provisioning.

`MountInstances` exposes GET/POST `/instances`, GET/PUT/DELETE `/instances/{instanceId}` under protected `/api/v1`. List responds `{instances: Detail[]}`; create/read/update respond `{detail: Detail}`; delete requires `{confirmation: exactID}` and returns 204. Admin create returns 201. The OpenAPI fragment is `docs/api/instances.yaml`; its paths and schemas were merged centrally with explicit integrator authorization and regenerated using the documented command.

Frontend exports `InstancesPage` and `InstancePage` from `web/src/features/instances`. They are mounted at `/` and `/instances/:instanceId` while preserving existing auth/jobs routes. Main service composition remains integrator-owned.

`Reconcile(ctx) (ReconcileResult,error)` runs after queue startup and before HTTP/autostart. The result has `Recovered []string` and `Deferred []ReconcileDeferred` with InstanceID/Code/Reason. Live, busy and unverifiable instances are deferred without file mutation; ownership conflicts are errors. Each created instance has a private ownership marker and persisted matching token. A durable deletion intent survives instance-row cascade, so interrupted precommit deletion restores only the proven owned staged directory, while committed deletion finishes private cleanup. Unknown directories are never adopted, and pending deletion IDs cannot be reused. The native admission lock is retained outside the deletable instance tree at `dataDir/locks/<id>.lock`.

`Materialize(ctx,id) error` recreates server.properties and eula.txt from canonical database settings under the same stopped/idle guard. It must precede a future launch after a live instance was deferred. It cannot run inside an already-running instance job or an already-held native guard; future provider orchestration needs an explicit current-job admission contract. No bypass is supplied here.

## Evidence

- `go test ./internal/instance`: observed RED because the Task09 service/path/config APIs were missing, then PASS after implementation.
- Unknown-existing-properties regression: observed RED (`map[]`), then PASS after bounded Java properties decoding/preservation.
- `go test -race ./internal/instance ./internal/api`: PASS after CRUD, EULA, unique concurrent port, guards, file preservation, path, role and deletion-isolation tests.
- `go vet ./internal/instance ./internal/api`: PASS.
- Required crash-boundary tests pass: precommit staged deletion restores the owned world sentinel; committed deletion completes cleanup; repeated replay is idempotent; pending IDs cannot be reused; foreign ownership prevents recovery without touching either tree; live instances defer canonical rewrite; stopped materialization restores DB properties and false EULA after file disagreement.
- Paper input accepts an optional positive numeric pinned build in loader_version; nonnumeric builds are rejected. Vanilla still requires empty. The form labels this Paper build.
- Shared API client now decodes canonical details.fields (legacy fields fallback retained), and instance forms display structured validation messages.
- In `web`, `npm run gen:api`: PASS after merging schema and after making Java requirement server-owned.
- In `web`, `npm run typecheck`: PASS.
- In `web`, `npm run lint`: PASS.
- In `web`, `npm run build`: PASS; shipping inventory/detail/form chunk produced.

Tests cover two isolated instances and survival of the other after deletion; EULA default/actor/time; viewer/operator denial of mutations and all-role read access; anonymous denial; audit records; port collision including concurrent creates; Java derivation and Fabric version floor; strict config bounds; RCON credential rejection; line-break/separator escaping; unknown existing Java properties; nil/running guards; traversal/device-name/symlink rejection; explicit delete confirmation; and retention of shared runtimes and external symlink targets.

## Remaining integration evidence and limits

- The integrator must compose/mount the real jobs/native lifecycle guard and API service before integrated CRUD acceptance. A status-only check is insufficient. Browser emptyDB → first admin → create two servers → read/edit/delete and 390 px keyboard evidence is still pending that composition.
- No provider installation, JVM launch, Linux systemd qualification, native Windows execution or real Minecraft support tuple is claimed by these metadata/config operations.
- Filesystem renames and SQLite commit are not one cross-resource atomic transaction. Normal failures compensate configuration; startup reconciliation safely materializes canonical DB configuration when stopped and defers surviving live games. Future provider launch orchestration must call guarded Materialize after a deferral before starting again. Durable owned deletion replay covers both sides of the SQL commit boundary; cleanup failures retain their journal and report a partial operation. World content is never rewound. An orphan created before its initial SQL commit is deliberately not adopted or overwritten.
- Port reservation is within this manager database; external applications can still occupy ports. Actual server startup must report real bind failures.
- Property preservation uses bounded text parsing and intentionally excludes manager-owned control/credential properties. Exact distribution/version compatibility remains the provider's responsibility.
- No extra dependencies, database fixtures, uploads, imports, runtime-cache deletion, staging or commits were introduced by this worker.

## Review fix round 1 — base 4092144

Both Important findings and the related startup-preservation issue were reproduced before correction. The focused regression command was:

`go test ./internal/instance -run 'TestReplayPartialCleanup|TestFailedSavePreserves|TestMaterializePreserves' -count=1`

It failed for all three expected reasons: a removed inner ownership marker blocked deletion replay; a failed EULA activation stripped a newly discovered unknown property from compensation; and startup materialization erased a file-only unknown property.

### Changes

- Cleanup now writes an authenticated sibling proof, `<journaled-staged-name>.owner`, outside the recursively removed directory before deleting any subtree. The exact persisted journal token authenticates that proof. It remains until the staged directory is completely removed, so partial `RemoveAll` and process interruption can resume even after `state/instance-owner` disappears. Invalid proof files, symlinks, conflicting original roots and unrelated directories still fail closed. Linux directory metadata is synced before advancing durable cleanup boundaries.
- Update preflights and snapshots the exact original server.properties and eula.txt bytes. A nonregular managed path fails before any activation. A private, ownership-checked `state/config.pending.json` records those bounded snapshots plus the before/after DB revision before activating updated files. Ordinary compensation restores exact original bytes, including comments and file-only extras. An incomplete rollback retains its recovery intent.
- Guarded startup/materialization first resolves any pending config intent against the persisted DB revision. An uncommitted update restores its before snapshot; a committed update retains the committed settings. It then materializes known settings and DB-tracked extras canonically while preserving unrelated, safe file-only properties. This prevents failed proposed extra properties from being silently accepted during startup.
- File-only edits made after a pending transaction's snapshot cannot be distinguished from that transaction's proposed bytes. Recovery intentionally restores the recorded before snapshot for an uncommitted transaction. With no pending transaction, unrelated file-only extras are preserved. World files are never part of these snapshots or rewound by this flow.
- No API, frontend, main wiring or lifecycle-session interface was changed in this fix round. The proposed explicit current-job admission/session interface remains separate future integration work.

### Verification

`go test ./internal/instance -run 'TestPartialCleanupPermission|TestInterruptedConfigIntent|TestReplayPartialCleanup|TestFailedSavePreserves|TestMaterializePreserves' -count=1 -v`: PASS. On this Linux user session the real permission-failure case ran without skipping: RemoveAll left the blocked server subtree after deleting its inner owner marker, then replay completed after permissions were repaired. Before-commit and after-commit config intents both recovered with the expected extras and canonical false EULA.

`go test -race ./internal/instance ./internal/api`: PASS (`internal/instance` 2.473s, `internal/api` 1.815s in the final scoped run).

Changed files for this fix: internal/instance/service.go, reconcile.go, service_test.go; new config_recovery.go, sync_linux.go and sync_other.go; this report. The non-Linux directory-sync helper retains portable file fsync/rename behavior; native Windows power-loss durability is still unqualified. No frontend checks were repeated because no frontend files changed. Independent scoped re-review and integrator acceptance remain pending.
