# SDD ledger — plan: docs/superpowers/plans/2026-10-16-minecraft-server-ui.md

# Implementation Ledger

No package has final acceptance yet. Scaffolding, plans, mocks, and package-local tests are not evidence that a product workflow is complete. The integrator marks a row complete only after review of its acceptance evidence and required cross-package integration.

| ID | Work package | Depends on | Owner / boundaries | Acceptance evidence | Status |
|---|---|---|---|---|---|
| 01 | Foundation, contracts, build | — | Scaffold owner; shared contracts/build/router | Empty DB migration, Linux build/check, generated API types, frozen reviewed interfaces | Reviewed; fixes pending |
| 02 | Identity, OIDC, tokens, authorization, audit | 01 interfaces; can start before review without shared wiring | `internal/auth`, `internal/audit`, identity handlers/migrations | First-admin flow, local/OIDC auth, scoped token revoke/expiry, role matrix, redacted audit | Reviewed; fixes pending |
| 03 | Jobs, events, streaming | 01 | `internal/jobs`, `internal/events`, job/event handlers | Durable states, authorized streaming, cancellation/recovery/slow subscriber evidence | In Progress |
| 04 | Linux supervision | 01, 03, 06 | Linux supervisor and deploy files | Real systemd lifecycle, survives manager restart, deliberate stop never retries | Pending |
| 05 | Windows supervision | 01, 03, 06 | Windows supervisor and deploy files | Real qualification on Windows 10/11 and Server 2019+; graceful stop/wait/autostart | Pending |
| 06 | Temurin runtime manager | 01, 03 | `internal/runtime` | Official hash-verified runtime, atomic activation, compatibility matrix and failure tests | Implemented; review pending |
| 07 | Vanilla and Paper | 01, 03, 06 | `internal/server/vanilla`, `paper` | Real tested install/launch/update and recovery for supported tuples | Pending |
| 08 | Fabric, Forge, NeoForge | 01, 03, 06 | `internal/server/fabric`, `forge`, `neoforge` | Tested tuple matrix, version floors, pre-mutation rejection, recoverable activation | Pending |
| 09 | Instances, ports, config | 01, 03 | `internal/instance`, instance handlers/UI | Multiple isolated instances, validation, safe paths, role gates | Pending |
| 10 | Console, commands, readiness | 01, 03, 04/05, 07/08, 09 | `internal/console`, console handlers/UI | Persistent logs, safe commands, readiness distinction, reconnect/role evidence | Pending |
| 11 | Players and lists | 01, 03, 09, 10 | `internal/players`, player handlers/UI | Online/history and whitelist/ban/kick/OP workflows, audit and role evidence | Pending |
| 12 | World discovery and download | 01, 09 | `internal/world`, world handlers/UI | Versioned dimensions, safe download; no upload/import route | Pending |
| 13 | Online backup lease/watchdog | 01, 03, 04/05, 09, 10 | `internal/backup` lease and staging only | Five-minute save-off/acknowledged flush/copy lease; save-on immediately after copy; archive work after save-on | Pending |
| 14 | Backup archive, retention, restore | 13 | `internal/backup` archive/restore handlers/UI | 10 auto/30 days, protected manual, external mod/plugin DBs and manager.db excluded, local mod/plugin data in full backup, confirmed stopped restore + safety backup | Pending |
| 15 | Catalogs and safe downloads | 01, 03, 06 | `internal/catalog`, catalog handlers/UI | Modrinth/CurseForge/Hangar coverage, no shared key, malformed/hash failure tests | Pending |
| 16 | Mods/plugins and config editor | 01, 03, 09, 15 | `internal/content`, handlers/editor UI | Catalog browse/install and admin upload; four lifecycle controls; safe editor and preserved config | Pending |
| 17 | Modpack three-way application | 03, 09, 15, 16 | `internal/modpack`, handlers/UI | Baseline/local/new preview, conflicts, override preservation, retained selections | Pending |
| 18 | Update preview and transaction | 06–09, 13–17 | `internal/update`, handlers/UI | Pinned autos, strict manual upgrade, backup/conflict gates, confirmed world-restoring instance rollback | Pending |
| 19 | Schedules, announcements, crash recovery | 03, 04/05, 10, 13, 18 | `internal/scheduler`, handlers/UI | Configurable countdown (5 min default), 3 retries/10 min, increasing delay, stop respected | Pending |
| 20 | JVM metrics and notifications | 01, 03, 09, 10 | `internal/metrics`, `internal/notify`, handlers/UI | 30 s sampling, 7 d raw/90 d hourly, six providers, redacted delivery history | Pending |
| 21 | Map decoding | 01, 09, 12 | `internal/mapdata`, fixtures | Listed legacy/modern formats, unknown-block fallback, malformed-data handling | Pending |
| 22 | Map UI, dimensions, height, markers | 03, 11, 12, 21 | `web/src/features/map`, assigned map handlers | 2D standard/custom dimensions, height slice, stale label, 5 s samples, mobile | Pending |
| 23 | Deaths and advancements | 03, 10, 11 | `internal/events/game`, handlers/UI | Provenance and gaps, native telemetry limitations explicit, dedupe/authorization | Pending |
| 24 | Self-update and stable release | 01–23 | `internal/selfupdate`, release workflows, runbook | Verified activation/rollback and complete Linux/Windows real workflow/failure evidence | Pending |
| 25 | Theme, accessibility, integration review | 01–24 | Shared web shell and integrator-owned docs | Minecraft theme, accessible 390 px UI, no fixture-only first-use state, full review | Pending |

## Execution notes

- Parallel package work is approved. Keep file ownership disjoint, use frozen interfaces, and reserve shared wiring and integration edits for the integrator.
- Task 02 can begin against Task 01's announced frozen interfaces, but no integration claim is valid until Task 01 has been reviewed.
- The repository itself is the isolated feature branch; no extra worktree is required at this stage.
- Windows runtime qualification is currently unestablished. Task 05 remains pending until real Windows evidence exists.
- World uploads, remote backups, and existing-server imports are excluded even if a reference project implements them.
- Each package must name actual commands and results in its completion report. A green mock or build does not replace real workflow evidence required by the design.

## Integrator review and evidence — 2026-10-16

- Root reviewed foundation/identity commit `9a4ff7e`; Tasks 01 and 02 require review fixes before acceptance. Task 01 fixes add missing shared contracts and align Vite/backend development origins; identity fixes remain with Task 02's owner. Applying a fix does not itself close review.
- Task 03 is in progress. Jobs/events own their durable service and richer public job projection. Shared `domain.Event`/`EventLog` are aliased by the event package, and the jobs OpenAPI fragment is merged centrally. No jobs runtime wiring is claimed before its owner reports ready.
- Task 06 implementation is commit `abacb69`, awaiting independent review. Root ran `env MINECRAFT_UI_RUNTIME_SMOKE=1 go test ./internal/runtime -run '^TestOfficialTemurinSmoke$' -v -count=1 -timeout=20m`: PASS in 9.10 seconds. Official Temurin 17.0.20.1+1 linux/x64 archive: 193252603 bytes, SHA-256 `3808d1d15e3ec6bd5b84057fb5d84c33d8a1536a258146bcea2e603fc726e08e`. Vendor/version/architecture execution and fresh-manager verified reuse passed. This verifies that runtime tuple; it does not qualify Minecraft combinations or Windows execution.
- Development uses the exact browser origin `http://127.0.0.1:5173`, fixed Vite port and loopback binding. Production origin/CSRF rules remain strict. Environment examples distinguish standalone embedded serving from the two-process development command.
- Future backup/schedule/metrics/catalog service boundaries are DTO/interface contracts only. No unimplemented endpoints or successful placeholder operations are exposed by adding these contracts.
