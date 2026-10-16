# Task 10 — Console, commands, and readiness

Implementation submitted for integration review, 2026-10-16. This report does not mark Task 10 accepted or claim a tested Minecraft/platform combination. Original implementation began against `011f99e`; the full-access continuation resumed at `3601c2e` and preserved the existing work. No game was executed and no Minecraft EULA was accepted.

## Owned changes

- `internal/console/`: command grammar/roles; durable redacted log ingestion/history; fresh legacy acknowledgement cursors; authenticated, bounded MSMP discovery/client; distinct process/control/game/settings readiness; typed callback-lifetime job sessions; fixed-worker background ingestion and read-only probes.
- `internal/api/console_handlers.go` and tests: viewer history/status/SSE, operator command enqueue, administrator and administrator-token scope for `seed`, bounded cursors, 64 total SSE handlers, absolute 30-second stream reauthentication and write deadlines.
- `internal/db/migrations/00006_console.sql`: console lines/cursors, canonical TCP reservations, existing game-port backfill and insert/update triggers, private management configuration with database constraints.
- `internal/instance/management.go` and tests, plus delegated hooks in `service.go`, `reconcile.go`, and `settings.go`: stopped-session private loopback control provisioning, stable 40-character random alphanumeric secret, unique TCP port reservation, recoverable materialization, ordinary create/edit collision checks, and native server rule scope. Private controls never enter public config/DTOs.
- `go.mod`/`go.sum`: only the explicitly reviewed `github.com/coder/websocket v1.8.15` dependency and its hashes.
- `web/src/features/console/ConsolePage.tsx`: generated-type page/panel with readiness details, bounded/deduplicated output, reconnect and pause/resume, gap/ingestion failure notices, keyboard-focusable wrapped console, role-aware list/announcement/seed controls, and queued versus acknowledged job feedback.
- `docs/api/console.yaml`; sole delegated schema integration merged ready console/world/lifecycle fragments into JSON-formatted `docs/openapi.yaml` and ran `npm run gen:api`. Root additionally delegated the exact `all_discovered_worlds` → `native_server_rules` enum correction in `docs/api/instances.yaml` and the root schema. `web/src/api/schema.d.ts` is generated, never hand-edited. Lifecycle fragment is included solely as the required source of the merged lifecycle types.

Shared `main.go`, `App.tsx`, and instance-page composition belong to lifecycle; they were not edited by this worker. Migration 00005 belongs to lifecycle and must accompany 00006 in the integrated tree. The integrator owns staging/commits and ledger acceptance.

## Behavior and frozen interfaces

`console.New(db, paths, native, instances, queue)` exposes `Register`, `Run`, `WithJobSession`, `Refresh`, `Status`, `Capture`, `History`, and `EnqueueCommand`. Main must cancel and drain `Run` before closing the database. `Refresh` is read-only and does not apply desired native settings.

`WithJobSession` invokes `Execution.WithInstanceMutation` exactly once; it does not nest a stopped-session guard. The original callback argument alone is valid, overlapping method calls are rejected, and callback return drains an active method. `Identity(ctx)` checks the current generation and returns the original identity by value. `ObservePlayers(ctx)` returns `{Count, Known, Source, ObservedAt}` from a fresh acknowledged probe; errors never become a known zero. No persistent player identity model was introduced.

`VerifyReady` applies/readbacks desired native rules within the current job admission. `Players`, `Announce`, `Flush`, and `ReleaseSaving` provide typed internal operations. `ReleaseSaving` accepts an independent cleanup context after original job cancellation while the callback remains live; native save-on release is always attempted even if its acknowledgement cursor cannot be established. Delivery alone leaves `Verified=false`. MSMP `isAutoSave` is not used to prove save-on: fresh exact native feedback is required.

Public grammar is exact lowercase `list` and `say <plain text>` for operators; administrators additionally have `seed`. Unknown plugin commands, aliases, namespace/case/slash variants, lifecycle/save/config/privilege wrappers, multi-line/control/format characters, invalid UTF-8, oversized input and announcement selectors are refused. Request audit contains the verb without announcement contents. Jobs recheck current user role/disabled state before execution.

Readiness separates `process_running`, `control_connected`, `game_ready`, `required_settings_verified`, and `ready_for_use`. Cached observations expire after 15 seconds and are bound to exact JVM identity. Legacy probes establish a cursor immediately before the command; historical Done/list lines do not count. MSMP checks authenticated discovery, the configured protocol version, discovered method/result names, exact response IDs, native `status.started`, required boolean rule update acknowledgement and independent readback. Heartbeats never establish world readiness. Native rule scope is `native_server_rules`; plugin/mod world gameplay overrides remain unverified.

Log ingestion persists only redacted complete lines and newline-boundary checkpoints. Incomplete lines stay private; lines over 8192 bytes are omitted whole. At most three launcher files of 4 MiB each are read per capture, at most 2000 redacted lines remain per instance, and history pages have at most 500 lines. Truncation, lost/ambiguous rotation checkpoints, omitted oversized lines and retention loss disclose gaps. A future/wrong-instance cursor resets with a gap and replays retained data. UI deduplicates by line ID and retains 500 lines. Ingestion errors/staleness are explicit through `logs_available`, `logs_reason`, and `logs_observed_at`, independently of stream connectivity and game readiness.

## Verification performed

All commands below passed in the full-access continuation:

```text
go test ./internal/console ./internal/instance -count=1
go test ./internal/api -run TestConsole -count=1
go test -race ./internal/console ./internal/instance -count=1
go test -race ./internal/api -run TestConsole -count=1
go vet ./internal/console ./internal/instance ./internal/api
cd web && npm run gen:api
cd web && npm run typecheck
cd web && npm run build
cd web && npm run lint
```

Tests cover split Unicode/secret lines, bounded large lines/history, manager cursor reuse, rotation/truncation, retention/future cursors, symlink rejection, credential redaction, generation changes, deliberate stopped state, copied/expired typed sessions, fresh known/unknown player counts, command role/grammar denial, exact native rule readback, pre-world heartbeat rejection, protocol mismatch/missing rules, malformed/oversized/mismatched/mixed/null/error responses, notification flood, deadlines, stream role/token-scope denial, absolute expiry/quota, independent cleanup after cancellation with a missing cursor, visible ingestion failure/recovery, private materialization retry, game/management collision triggers, concurrent canonical port reservation and reservation release after instance deletion. Fixtures remain network-free except loopback test servers; none substitute for real game qualification.

The production web build includes the console route supplied by lifecycle. Root owns the integrated real browser review, including 390 px layout and keyboard behavior; no browser evidence is claimed in this scoped report.

## Primary static artifact evidence

The existing official artifact cache was inspected without running Minecraft. SHA-1 was rechecked in the continuation:

| Official server artifact | SHA-1 |
| --- | --- |
| 1.21.9 | `11e54c2081420a4d49db3007e66c80a22579ff2a` |
| 1.21.11 | `64bb6d763bed0a9f1d632ec347938594144943ed` |
| 26.3 | `33680f5f2ac32864d6d7cf5e56a705fdb3e05f4c` |

Cached files are under `/home/thim/.cache/dilla-tmp/minecraft-console-schemas`; 1.21.9/1.21.11 include official mappings and inner jars. Static class inspection supplied actual discovery/method/codec/result shapes, `status.started`, the native gamerule scope and legacy acknowledgement text, including the separate 1.12.2 fixture. Sources referenced in the task handoff are the official [1.21.9](https://www.minecraft.net/en-us/article/minecraft-java-edition-1-21-9), [1.21.11](https://www.minecraft.net/en-us/article/minecraft-java-edition-1-21-11), [26.2](https://www.minecraft.net/en-us/article/minecraft-java-edition-26-2), and [26.3](https://www.minecraft.net/en-us/article/minecraft-java-edition-26-3) release notes. Static inspection is protocol input evidence, not game interoperability evidence.

## Remaining integration and qualification

- Integrator review, composed browser/mobile/keyboard evidence, and ledger update remain required before Task 10 acceptance.
- Real Minecraft server/client qualification across approved versions and Vanilla/Paper/Fabric/Forge/NeoForge combinations remains pending. Unsupported/missing native capabilities fail explicitly; no plugin/add-on is assumed.
- Native Windows execution and Linux systemd/namespace/process-restart qualification remain pending. Cross-compilation is not Windows qualification.
- Backups/players/schedules must use the typed session within their own existing job admission. Task 13 still owns lease acquisition/watchdog/recovery orchestration; this package provides acknowledged flush/save-on cleanup capabilities only.
- Raw launcher output is private and retains its launcher-owned rotation policy; only redacted database history is served. Log observations disclose gaps and do not guarantee completeness across arbitrary plugin log formats or manager downtime.
