# Minecraft Server UI Implementation Plan

> **For agentic workers:** Use the approved parallel work package process in `AGENTS.md`; tasks use checkbox syntax for tracking.

**Goal:** Build and release the approved native Minecraft server manager described in the design spec.

**Architecture:** One Go service and embedded React 19/Mantine client use SQLite for manager state and supervise isolated local Java server instances. Linux uses systemd and Windows uses the supported native manager/process lifecycle. Shared API/domain/interfaces are frozen by the foundation task; packages integrate through those contracts and a central integrator reviews the combined product.

**Tech Stack:** Go 1.27.0; React 19, TypeScript, Vite, Mantine; Node 24.15; SQLite; native Java servers; managed Adoptium Temurin runtimes; systemd on x64 Debian/Ubuntu and native Windows 10/11 or Server 2019+.

**Spec:** `docs/superpowers/specs/2026-10-16-minecraft-server-ui-design.md`

## Global Constraints

- Support x64 Debian/Ubuntu with systemd and Windows 10/11 or Windows Server 2019+.
- Support Minecraft 1.12.2+; Fabric 1.14+; NeoForge 1.20.2+ only in tested combinations.
- Use managed, hash-verified Adoptium Temurin runtimes per instance.
- Roles are viewer, operator, admin; enforce API authorization and redacted audit logging.
- Updates are version-pinned, previewed, backed up, and blocked on backup failure or unresolved conflicts.
- Online backups never deliberately stop the server; use the five-minute save lease/watchdog procedure.
- Keep ten automatic backups for 30 days; protect manual backups; exclude external mod/plugin databases and `manager.db`; include local mod/plugin data in full server backups.
- Restore only with explicit confirmation, safety backup, and stopped server; no automatic world rollback.
- Metrics every 30 s, raw retention 7 days, hourly retention 90 days; active map sampling every 5 s.
- Crash recovery: maximum 3 tries within 10 min, increasing delays; deliberate stops never restart.
- CurseForge requires an operator-provided key; never ship a shared key.
- Exclude Bedrock, Docker, remote hosts, existing-server imports, world uploads, remote backups, mandatory add-ons, and client pack export.
- Keep every work package's file ownership disjoint. Only the integrator changes shared wiring after review.
- Do not claim support or completion without the acceptance evidence named in this plan and the release matrix.

## Review Focus

- Untrusted archives and metadata, including traversal, duplicate paths, oversized entries, and hash mismatch: test rejection before extraction or activation in Tasks 6, 13–14, 15–18, and 24.
- Concurrent world changes and interrupted save leases: test watchdog/save-on recovery, staging validation, and crash reconciliation in Task 13.
- Version and loader compatibility gaps: test both supported and rejected tuples in Tasks 6–8 and 18.
- Role and secret boundaries across HTTP, streams, logs, config editors, and notifications: test denial and redaction in Tasks 2, 3, 9–12, 16, 20, and 23.
- Cross-platform process and filesystem behavior, especially deliberate stop and manager restart: test Linux and Windows workflows in Tasks 4–5 and release qualification in Task 24.

---

## Work package rules

The accepted ledger is `docs/IMPLEMENTATION.md`. All 25 rows start pending. A package owner updates only their owned files and reports evidence; the integrator updates shared contracts/wiring after review. Parallel packages require disjoint ownership and announced frozen interfaces. Task 02 may start auth against frozen foundation interfaces before Task 01 finishes, but integration is not complete until Task 01 review and central wiring.

The task file paths below are package boundaries, not permission to invent incompatible interfaces. Use the names/types in the frozen foundation/API contracts. If a contract is missing, report the exact contract needed and have the integrator resolve it; do not edit another package's files.

### Adjacent interfaces and ownership boundaries

| Handoff | Producer → consumer | Boundary rule |
|---|---|---|
| Shared contracts | 01 → all packages | 01 owns domain types, router composition, OpenAPI, generated types, and migrations baseline. Feature packages own their resource handlers and migration files; integrator wires them. |
| Identity | 02 → all API/UI packages | 02 owns auth middleware, auth/user/settings/audit handlers and token contracts. Feature owners enforce declared roles and call the audit interface; they do not change auth wiring. |
| Jobs/events | 03 → 04–25 | 03 owns queue/event/stream interfaces. Producers publish typed events and jobs through those interfaces; no feature package edits the shared event bus. |
| Instance and process | 09 → 04–08, 10–24 | 09 owns instance paths/config/service and instance handlers. 04/05 own platform supervisor implementations; 07/08 own server installers; consumers use Task 09's frozen instance service. |
| Backups and updates | 13 → 14 → 18/19/24 | 13 owns online lease/staging; 14 owns archive/retention/restore; 18 owns instance version transaction; 24 owns manager self-update. Instance rollback may restore world data only by explicit confirmation; manager executable rollback is automatic on failed activation. |
| Content and catalogs | 15 → 16 → 17/18 | 15 owns provider/catalog clients; 16 owns managed mods/plugins and config editor; 17 owns pack baseline/diff/apply; 18 consumes compatibility and conflict results. |
| Map and game events | 21 → 22; 10/11 → 23 | 21 owns world decoding; 22 owns map API presentation/UI and five-second sampling; 23 owns death/advancement provenance and persistence. |
| Shared presentation | 25 integrates all UI | Feature tasks own their feature folders. 25 owns shared theme/accessibility shell and integration review; it must coordinate instead of editing feature code owned by another task. |


### Task 01: Foundation, contracts, and build

**Files:** scaffold-owned shared files (`go.mod`, `go.sum`, `Makefile`, CI, `internal/domain/**`, `internal/api/router.go`, `docs/openapi.yaml`, migrations baseline, `web/**` shell, `deploy/**`).

**Depends:** none. **Acceptance:** `make build` and `make check` run on Linux; generated API types match OpenAPI; shared service interfaces and error/event shapes are documented and frozen; empty SQLite startup migrates cleanly; native Windows cross-build is recorded without claiming runtime qualification. Review before claiming integration.

- [ ] Confirm Go 1.27.0 and Node 24.15 toolchain declarations and release metadata.
- [ ] Define instance, job, event, user/role, backup, schedule, metrics, and catalog contracts used by downstream packages.
- [ ] Define router composition and per-resource handler registration without placeholder success responses.
- [ ] Establish migrations, SQLite settings, embedded frontend build, and generated API types.
- [ ] Run the scaffold build/check and review contracts; record evidence.

### Task 02: Identity, OIDC, tokens, authorization, audit

**Files:** `internal/auth/**`, `internal/audit/**`, identity and audit migrations, auth/user/settings/audit handler files. **Depends:** frozen Task 01 interfaces; may start before Task 01 review without shared wiring edits.

**Acceptance:** empty-database setup creates the first admin; local and OIDC sessions, scoped API tokens, revocation, expiry, role gates, audit history, and secret redaction have tests and manual flow evidence.

- [ ] Implement setup/login/logout/session lifecycle and OIDC against a controlled test provider.
- [ ] Implement viewer/operator/admin middleware and per-operation API authorization.
- [ ] Implement scoped token issue/list/revoke flow and document storage/expiry/revocation.
- [ ] Record security mutations with secrets and credentials redacted.
- [ ] Test role matrix, token expiry/revocation, OIDC failures, CSRF/origin rules, and audit redaction.

### Task 03: Jobs, events, and event streaming

**Files:** `internal/jobs/**`, `internal/events/**`, job/event handlers and migrations. **Depends:** Task 01.

**Acceptance:** jobs are durable, cancellable where safe, serialized per instance, stream progress to authorized clients, survive manager restart with truthful state, and never expose another user's secrets.

- [ ] Implement job state transitions and per-instance queues.
- [ ] Persist job output and publish typed progress events.
- [ ] Implement authenticated/role-gated event stream with bounded subscribers.
- [ ] Test cancellation, process restart recovery, event authorization, and slow subscribers.

### Task 04: Linux supervision

**Files:** `internal/supervisor/systemd_*.go`, `deploy/linux/**`, Linux-specific command files. **Depends:** Tasks 1, 3, 6. Instance service integration comes from Task 09.

**Acceptance:** real Linux service start/stop/status/autostart works; server stays alive across manager restart; deliberate stop is classified and never crash-restarted.

- [ ] Implement strict unit control and least-privilege install configuration.
- [ ] Implement systemd status and process-exit classification.
- [ ] Verify restart of manager leaves game processes running and reconnects status.
- [ ] Exercise stop timeout, failed unit, autostart, and service upgrade.

### Task 05: Windows supervision

**Files:** `internal/supervisor/windows_*.go`, `deploy/windows/**`. **Depends:** Tasks 1, 3, 6. Instance service integration comes from Task 09.

**Acceptance:** native Windows qualification on Windows 10/11 and Server 2019+ shows graceful stop/wait, startup autostart, manager restart behavior, and correct deliberate-stop classification. Until an execution environment exists, this package remains pending.

- [ ] Implement native process and service lifecycle with bounded graceful stop and forced-stop reporting.
- [ ] Implement manager-start autostart and wait-for-exit behavior.
- [ ] Validate path, quoting, signal, and file replacement behavior on each supported Windows family.
- [ ] Record real host evidence; cross-compilation alone does not pass acceptance.

### Task 06: Temurin runtime manager and compatibility rules

**Files:** `internal/runtime/**`, runtime catalog data and tests. **Depends:** Tasks 1, 3.

**Acceptance:** each instance selects a compatible Temurin runtime, downloads only official artifacts, verifies published hashes, safely resumes/retries, and rejects unsupported tuples.

- [ ] Define supported Java/Minecraft/loader compatibility table with source provenance.
- [ ] Implement official metadata resolution, staged download, hash verification, and atomic activation.
- [ ] Implement per-instance runtime selection and reference-safe cleanup.
- [ ] Test bad hash, interrupted download, traversal metadata, and compatibility rejection.

### Task 07: Vanilla and Paper installation/update

**Files:** `internal/server/vanilla/**`, `internal/server/paper/**`, provider clients. **Depends:** Tasks 1, 3, 6. Instance service integration comes from Task 09.

**Acceptance:** supported Vanilla and Paper combinations install, launch, report readiness, update transactionally, and preserve per-instance worlds/configuration.

- [ ] Implement official metadata/artifact resolver for Vanilla and Paper.
- [ ] Verify artifact hashes/signatures when publisher provides them; record provenance.
- [ ] Install into staged instance paths and atomically activate.
- [ ] Test supported and unsupported versions, interrupted install, and update recovery.

### Task 08: Fabric, Forge, NeoForge installation/update

**Files:** `internal/server/fabric/**`, `internal/server/forge/**`, `internal/server/neoforge/**`. **Depends:** Tasks 1, 3, 6. Instance service integration comes from Task 09.

**Acceptance:** loader installers work only for tested compatible combinations, with pinned artifacts and recoverable activation; unsupported versions fail before mutation.

- [ ] Implement loader metadata and installer flows with hash/provenance records.
- [ ] Apply exact compatibility floors: Fabric 1.14+, NeoForge 1.20.2+, all Minecraft 1.12.2+ where loader supports it.
- [ ] Implement staged update and rollback of executable files only.
- [ ] Test representative supported tuples and rejection matrix.

### Task 09: Instances, directories, ports, config

**Files:** `internal/instance/**`, instance/config handlers, instance UI feature. **Depends:** Tasks 1, 3.

**Acceptance:** create/edit/delete multiple isolated instances; validate slugs, port conflicts, config bounds, safe paths, and role permissions; each instance has separate server, runtime, world/dimension, logs, and backup roots.

- [ ] Implement path helpers rooted in configured data directory and strict instance IDs.
- [ ] Implement create/list/read/update/delete and port reservation validation.
- [ ] Implement typed config validation and atomic config writes.
- [ ] Build forms and audit all mutations; test cross-instance isolation and traversal rejection.

### Task 10: Console, commands, readiness, logs

**Files:** `internal/console/**`, console/command handlers and UI. **Depends:** Tasks 1, 3, 4/5, 7/8. Instance service integration comes from Task 09.

**Acceptance:** persistent bounded logs, safe command execution, readiness distinct from process state, and role-gated live console behavior on supported server types.

- [ ] Implement log capture, rotation, streaming, and bounded history.
- [ ] Implement command validation/transport per server type; reject unsupported commands clearly.
- [ ] Parse startup/readiness and shutdown signals without making unsupported version claims.
- [ ] Test large logs, Unicode, stream reconnect, role denial, and deliberate shutdown.

### Task 11: Players, history, whitelist, bans, ops

**Files:** `internal/players/**`, player handlers and UI. **Depends:** Tasks 1, 3, 9, 10.

**Acceptance:** live player state and join history work; whitelist, bans, kicks, and OP management are version-aware, audited, and role-gated.

- [ ] Track player sessions and history from supported event sources.
- [ ] Implement list operations with preservation of unrelated comments/formatting where applicable.
- [ ] Implement kick/whitelist/ban/operator mutations and audit events.
- [ ] Test duplicate names/IDs, offline mutation, permission matrix, and history retention.

### Task 12: World discovery, dimensions, download

**Files:** `internal/world/**`, world handlers and UI. **Depends:** Tasks 1, 9.

**Acceptance:** discover standard/custom dimensions per supported layout, safely list and download selected world data, and reject path traversal. No world-upload endpoint or existing-server import.

- [ ] Implement version-aware world and dimension discovery.
- [ ] Implement safe streaming download with authorization and size limits.
- [ ] Test legacy and modern layouts including names with spaces and malicious path names.
- [ ] Confirm excluded upload/import routes are absent from the API contract.

### Task 13: Online backup lease and watchdog

**Files:** `internal/backup/lease_*.go`, backup service staging path, lease migrations. **Depends:** Tasks 1, 3, 4/5, 9, 10.

**Acceptance:** online backup never deliberately stops the game; within a maximum five-minute lease it requests save-off, waits for acknowledged `save-all flush`, copies to staging, then immediately requests save-on. Compression and archive validation run after save-on. Watchdog and manager-crash recovery restore saving; imperfect player/mod consistency is documented.

- [ ] Implement per-instance lease manager with five-minute hard deadline and owner/job tracking.
- [ ] Implement save-off, acknowledged `save-all flush`, staged copy, and immediate save-on in cleanup on every path; enforce a five-minute lease deadline.
- [ ] Run compression and archive validation after save-on, outside the lease.
- [ ] Implement watchdog expiry and startup reconciliation of abandoned leases.
- [ ] Inject failures at each lease step and verify save-on; verify invalid archives are not recorded as usable.

### Task 14: Backup archive, retention, restore

**Files:** `internal/backup/archive.go`, restore/retention handlers and UI. **Depends:** Task 13.

**Acceptance:** manual/scheduled world/full archives validate; ten automatic/30-day defaults; manual protected; external mod/plugin databases and `manager.db` excluded, local mod/plugin data included in full backups; restore requires confirmation, safety backup, and stopped server; world rollback never automatic.

- [ ] Implement format-aware archive creation and traversal-safe validation.
- [ ] Implement automatic retention and protected manual retention.
- [ ] Implement explicit restore workflow with pre-restore safety backup and stopped-state gate.
- [ ] Test corrupt/traversal archives, retention edge cases, failed safety backup, and restore interruption.

### Task 15: Catalog clients and safe downloads

**Files:** `internal/catalog/**`, catalog handlers and UI. **Depends:** Tasks 1, 3, 6.

**Acceptance:** Modrinth and CurseForge searches, Hangar and Modrinth plugin catalogs, safe downloads/provenance, and CurseForge disabled without a user key. No shared API credentials in binaries or CI.

- [ ] Implement catalog clients with timeouts, paging, bounded response sizes, and explicit user-agent.
- [ ] Implement CurseForge key configuration and disabled UX when absent.
- [ ] Cache metadata with source and fetched timestamp; validate download URLs and hashes.
- [ ] Test timeout, malformed metadata, key absence, and hash mismatch.

### Task 16: Mods/plugins and text config editor

**Files:** `internal/content/**`, mod/plugin handlers, content UI/config editor. **Depends:** Tasks 1, 3, 9, 15.

**Acceptance:** administrator upload and catalog installs support only update/disable/enable/uninstall controls; preserve config; editor has path/size limits and atomic writes.

- [ ] Implement staged install with source/version/hash/file manifest.
- [ ] Implement catalog browse/install and administrator upload, then the four lifecycle controls (update, disable, enable, uninstall) with safe uninstall based on recorded files.
- [ ] Implement admin uploads and unknown-content compatibility state.
- [ ] Implement text config editor and audit trail; test traversal, oversized files, and preserved local settings.

### Task 17: Modpack three-way application

**Files:** `internal/modpack/**`, pack handlers and UI. **Depends:** Tasks 3, 9, 15, 16.

**Acceptance:** compare old baseline, local state, and new pack; preview conflicts and proposed changes; preserve overrides; retain item selections; show source pack link and concise customization summary; do not export client packs.

- [ ] Store the applied baseline and file hashes.
- [ ] Compute add/update/delete/conflict decisions from three inputs.
- [ ] Implement preview and explicit user choices for server items.
- [ ] Apply transactionally and test local edits, removed upstream files, missing files, and interrupted apply.

### Task 18: Update preview and transaction

**Files:** `internal/update/**`, update handlers and UI. **Depends:** Tasks 6–9, 13–17.

**Acceptance:** automatic updates keep Minecraft version pinned; manual change checks strict compatibility and creates safety backup; preview exposes conflicts; backup failure/conflict blocks automatic update; recovery never rewinds world data.

- [ ] Build update plan from current state, target artifacts, compatibility matrix, and pack/mod baseline.
- [ ] Display changes/conflicts and require confirmation for manual upgrades.
- [ ] Apply executable/content updates transactionally; keep automatic rollback limited to server executable/content state.
- [ ] Provide an explicit, separately confirmed instance rollback path that can restore world data from backup.
- [ ] Test backup failure, conflict, unknown upload, partial activation, version pin behavior, and both rollback paths.

### Task 19: Schedules, announcements, restart, crash recovery

**Files:** `internal/scheduler/**`, scheduler handlers and UI. **Depends:** Tasks 3, 4/5, 10, 13, 18.

**Acceptance:** backup/command/announcement/restart/auto-update schedules work; five-minute configurable countdown; max three crash attempts within ten minutes with increasing delays; explicit stop never restarts.

- [ ] Implement durable schedule definitions, timezone behavior, and next-run reporting.
- [ ] Implement announcements and countdown restart flow.
- [ ] Implement crash retry window/backoff and deliberate-stop classification.
- [ ] Test manager restart, overlapping jobs, time boundaries, and user stop.

### Task 20: JVM metrics and notifications

**Files:** `internal/metrics/**`, `internal/notify/**`, metrics/notification handlers and UI. **Depends:** Tasks 1, 3, 9, 10.

**Acceptance:** JVM sampling every 30 seconds, raw 7-day and hourly 90-day retention, CPU/memory/disk/players/history; Discord/Slack/ntfy/Telegram/webhook/email delivery history with secret redaction.

- [ ] Implement JVM-only metric collection and rollups.
- [ ] Implement retention and authorized queries.
- [ ] Implement notification providers and secret storage/masking.
- [ ] Test provider failure history, redaction, retention boundaries, and role access.

### Task 21: World map data decoding

**Files:** `internal/mapdata/**`, versioned fixtures and tests. **Depends:** Tasks 1, 9, 12.

**Acceptance:** decode applicable world formats for the qualification matrix and clearly return unsupported/stale status; unknown block IDs render through a stable fallback.

- [ ] Implement versioned world/dimension metadata readers.
- [ ] Decode terrain and height slices for supported formats.
- [ ] Add fixtures for 1.12 legacy, 1.16 packing, 1.18 negative Y, 1.20.5 mixed codecs, Minecraft Server Management Protocol (MSMP), introduced in 1.21.9 as authenticated JSON-RPC over WebSocket, 26.1 folders, and 26.3 log changes as applicable.
- [ ] Test malformed/truncated world data and unknown block fallback.

### Task 22: Map UI, height, dimensions, markers

**Files:** `web/src/features/map/**`, map handlers as assigned. **Depends:** Tasks 3, 11, 12, 21.

**Acceptance:** 2D map supports standard/custom dimensions and height slice, stale state is visible, active viewer/player data samples every five seconds, and 1.12.2 limits are explained.

- [ ] Implement map/dimension selection and height controls.
- [ ] Implement marker rendering and refresh keyed by world data version.
- [ ] Implement five-second sampling with authorization and bounded fanout.
- [ ] Test empty world, unsupported dimension, stale cache, and 390 px layout.

### Task 23: Deaths and advancements provenance

**Files:** `internal/events/game/**`, event handlers and UI. **Depends:** Tasks 3, 10, 11.

**Acceptance:** death and advancement history includes source/provenance and explicit gaps; vanilla log limitations are not presented as complete telemetry and no add-on is mandatory.

- [ ] Parse available native events and attach source/version provenance.
- [ ] Persist/query history with retention and authorization.
- [ ] Present gaps and confidence in UI.
- [ ] Test missing log capability, malformed lines, and duplicate event delivery.

### Task 24: Self-update and stable release qualification

**Files:** `internal/selfupdate/**`, release workflows, `docs/RUNBOOK.md`, release metadata. **Depends:** Tasks 1–23.

**Acceptance:** only stable releases are published; signed/hashed artifact verification precedes activation; failed activation returns to prior executable; full real-server/client, Linux/Windows, restore, archive, hash, race, and role workflow has evidence.

- [ ] Implement staged manager self-update, verification, activation, health check, and automatic rollback of the manager executable only.
- [ ] Qualify supported versions/formats and publish limits.
- [ ] Run empty DB → admin → install/start/connect → backup/restore on Linux and Windows.
- [ ] Inject archive traversal, hash, save lease, process, update, and competing-write failures; review role API and audit redaction.
- [ ] Publish stable release only after all required evidence is recorded.

### Task 25: Theme, accessibility, mobile, integration review

**Files:** `web/src/**`, shared UI shell, `docs/**` as assigned to integrator. **Depends:** Tasks 1–24.

**Acceptance:** coherent Minecraft theme, accessible forms/dialogs/focus, role-aware controls, and usable 390 px layout; end-to-end empty-database workflow has no fixture-only prerequisites.

- [ ] Apply shared tokens/components and consistent status/job/empty states.
- [ ] Complete keyboard, labels, focus, contrast, and mobile checks.
- [ ] Run complete real workflow and remove fixture assumptions that the product cannot create.
- [ ] Review all API role gates, safety paths, and support claims against design and ledger.
