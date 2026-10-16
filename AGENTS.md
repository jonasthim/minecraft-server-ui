# Minecraft Server UI — contributor instructions

Read `docs/superpowers/specs/2026-10-16-minecraft-server-ui-design.md`, `docs/superpowers/plans/2026-10-16-minecraft-server-ui.md`, and `docs/IMPLEMENTATION.md` before implementation. The design is the approved product boundary; the plan defines work packages and the ledger records status. Do not claim a package complete until its acceptance evidence has been reviewed and the ledger updated by the integrator.

## Work ownership and integration

- Parallel subagent implementation is explicitly approved. Keep each worker inside its package's disjoint file ownership and use frozen interfaces.
- The root/integrator owns shared contracts, API schema/router/build wiring, cross-package changes, and integration review. Ask the integrator to resolve missing or conflicting interfaces; do not edit another package's files.
- Task 02 may start against announced frozen foundation interfaces before Task 01 review, but must not edit shared wiring and cannot claim integration until Task 01 is reviewed.
- This initialized feature branch is already the isolated workspace. Do not make another worktree without a concrete need.
- Do not stage or commit unless the integrator explicitly assigns that action.

## Product requirements

- Native Go + React 19/Mantine + SQLite embedded application. Linux x64 Debian/Ubuntu systemd and Windows 10/11 or Server 2019+ are release targets. Native Windows execution environment is not yet established; cross-compilation is not qualification.
- Minecraft Java only: Vanilla, Paper, Fabric, Forge, NeoForge; Minecraft 1.12.2+, Fabric 1.14+, NeoForge 1.20.2+ in tested combinations. Managed Adoptium Temurin downloads must use official metadata and verified hashes.
- Multiple local instances have isolated directories, ports, runtime, worlds/dimensions, logs, and backups.
- Global roles: viewer/operator/admin. Enforce API and stream role checks. Audit sensitive mutations and redact secrets, tokens, credentials, and sensitive config.
- Online backups must not deliberately stop servers. Use the five-minute save lease, watchdog, crash recovery, staged validation, and always attempt save-on. Restore requires explicit confirmation, a safety backup, and stopped server; never rewind world data automatically.
- Automatic backups default to ten retained for 30 days; manual backups are retention-protected; external mod/plugin databases and `manager.db` are excluded; local mod/plugin data is included in full backups.
- Automatic updates remain version-pinned. Manual version changes require strict compatibility and backup. Backup failure or unresolved conflict blocks automatic updates.
- Crash recovery allows at most three retries in ten minutes with increasing delays. A deliberate operator stop never restarts.
- CurseForge is unavailable unless the operator supplies a key; no shared key may be distributed. Mods/plugins support catalog browse/install and administrator upload, plus update, disable, enable, and uninstall controls.
- No Bedrock, Docker, remote hosts/backups, existing-server import, world uploads, mandatory add-ons, or client-pack export. World downloads are allowed.
- Metrics sample every 30 seconds (raw 7 days, hourly 90 days); active map sampling every five seconds. Preserve provenance and disclose telemetry gaps.
- Stable release support claims require tested version/layout evidence. Required matrix: 1.12 legacy, 1.16 packing, 1.18 negative Y, 1.20.5 mixed codecs, 1.21.9 Minecraft Server Management Protocol (MSMP), authenticated JSON-RPC over WebSocket, 26.1 folders, 26.3 log changes.

## Engineering rules

- Contract first: update the API schema with owned endpoints and regenerate types through the repository's documented command. Do not hand-edit generated types.
- Validate every external identifier/path. Derive paths through instance path helpers. Reject traversal and unsafe archive entries before extraction. Stage and verify downloads before activation.
- Pass `context.Context` through I/O; return wrapped errors; use structured logging. Never log credentials or raw secrets.
- Keep config and API responses typed and validated. Mutations must be atomic where possible and safe to retry. Report partial operations honestly.
- Tests should be deterministic and network-free for package logic. Use real server/client and real Windows/Linux evidence for release claims; mocks are not substitutes. Do not claim supported combinations that have not been tested.
- UI uses Mantine, strict TypeScript, generated API types, role-aware mutations, visible success/failure feedback, keyboard accessibility, and a 390 px mobile layout.
- Preserve LICENSE notices in substantial copied portions. Do not add dependencies or shared credentials without explicit review.
