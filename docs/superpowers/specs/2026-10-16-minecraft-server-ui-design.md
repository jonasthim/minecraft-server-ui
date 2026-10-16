# Minecraft Server UI — Approved Design

Status: approved product design, 2026-10-16. Source of truth for implementation. If code disagrees, update code or this document in the same reviewed change.

## Goal and product boundary

A public, self-hosted native Go and React 19/Mantine application for installing, configuring, supervising, updating, backing up, and observing multiple Minecraft Java Edition servers on one host. It embeds the web UI in the Go executable and uses SQLite. The product is Minecraft-themed and uses the MIT license and notice in the repository `LICENSE`.

Supported hosts are x64 Debian/Ubuntu under systemd and Windows 10/11 or Windows Server 2019 and later. Native Windows execution is a release requirement; the development environment for exercising it is not yet established. Linux game processes must survive a manager restart. On Windows, the manager gracefully stops servers, waits for exit, and starts those configured for autostart when the manager starts.

Each instance owns its server files, runtime, configuration, ports, worlds, dimensions, logs, and backups. Server types are Vanilla, Paper, Fabric, Forge, and NeoForge, only for tested compatible combinations. Support starts at Minecraft 1.12.2; Fabric starts at 1.14; NeoForge starts at 1.20.2. Do not claim a version or loader combination supported until its installation, launch, update, and recovery paths have been tested.

The product excludes Bedrock, Docker, remote hosts, importing an existing server, world uploads, remote backups, mandatory add-ons, and client-pack export. World downloads are supported. Do not add excluded scope under another name.

## Runtime and installation

Use the approved native Go/React 19/Mantine architecture and reviewed Valheim infrastructure/layout patterns where they fit. Store manager state in embedded SQLite. Use managed Adoptium Temurin runtimes, downloaded from official sources, verified by published hashes, and selected per instance according to Minecraft/loader compatibility. Downloads must be safe to retry and must never replace a runtime or server artifact until verification succeeds.

Keep instances isolated and paths derived from validated instance IDs. Treat archives and catalog metadata as untrusted. Prevent archive traversal, validate hashes and expected layout, and make install/update transactions recoverable. Instance ports must be unique where protocols overlap. Use systemd for Linux process lifetime; implement and document Windows service/process behavior explicitly.

## Identity, authorization, and API

Authentication is global and supports local setup plus OIDC. Roles are `viewer`, `operator`, and `admin`. Enforce authorization on every API operation, including streamed events and file/config endpoints. Sessions and API tokens must be revocable and scoped; document token creation, storage, expiry, and revocation. Record security-relevant mutations in an audit log with credentials and secrets redacted. Never log OIDC secrets, passwords, session tokens, API tokens, or unredacted sensitive configuration.

Configuration forms cover ordinary server settings. A text editor handles mod, plugin, and pack configuration files, with path allowlists, size limits, atomic writes, and permission checks. Mutations return stable structured errors. API contracts are documented and generated frontend types come from the API schema.

## Instance operations and people

Provide instance create/edit/delete, install, start, stop, restart, enable/disable autostart, and uninstall controls. Show job progress and persistent console logs; allow safe server commands. Expose readiness separately from process-running state. Support online player state, join history, whitelist, bans, kicks, and operator management with server-version-aware behavior.

## Server types, mods, plugins, and packs

Provide vanilla server installation and supported Paper, Fabric, Forge, and NeoForge combinations. Catalog providers are Modrinth and CurseForge for mods and modpacks, and Hangar and Modrinth for plugins. CurseForge integration is disabled unless the operator supplies an approved key; never distribute a shared key. Allow administrator uploads for plugins and mods.

Managed content supports browsing and installation from catalogs or administrator uploads, plus four lifecycle controls: update, disable, enable, and uninstall. Preserve source, version, hashes, installed files, and user configuration. Installing a mod loader is distinct from installing a mod.

Modpacks are applied by a three-way comparison of the previous pack baseline, current local files, and newly selected pack contents. Preserve local overrides, identify conflicts, preview proposed changes, and let users choose server-item selections. Do not silently delete user files. Pack customization summaries show the original pack link and a concise list of local changes; no client pack export is provided.

If required files are unavailable, pause the operation with verified upload and official download-link options, then allow safe resume. An administrator may trial an unknown upload against the current server version. Unknown content blocks automatic updates and version upgrades until reviewed. Do not assert compatibility from a filename or catalog listing alone.

## Version changes, jobs, schedules, and crashes

Pin the selected Minecraft version for automatic updates. Manual version changes require strict compatibility checks and a successful safety backup first. Update previews show affected components and conflicts before the transaction. Backup failure or unresolved conflicts block automatic updates. Make update steps atomic where possible and record enough state to recover or explain partial failure.

Schedules cover backups, commands, announcements, restarts, and auto-updates. Restart announcements use a configurable countdown, with a five-minute default. Automatic crash recovery retries at most three times within ten minutes, with increasing delays. An explicit operator stop is deliberate and must never trigger crash restart. Track why a process exited before scheduling recovery.

Self-update artifacts must be verified before activation and support rollback to the prior executable when activation fails. Publish stable releases only. Do not label a build stable unless the release matrix and recovery path passed.

## Backups and consistency

Support manual and scheduled world/full local backups with retention. Backups must work while a server is online and must never deliberately stop it. Acquire a save lease for at most five minutes: request save-off, wait for an acknowledged `save-all flush`, copy the world/server data to staging, then immediately request save-on. Compression and archive validation happen after save-on, outside the lease. A lease manager watchdog must restore saving on timeout, manager crash recovery must reconcile outstanding leases, and every failure path must attempt save-on. Validate archive structure and hashes before recording a backup as usable. Explain that player/mod consistency can be imperfect while a live server changes.

Keep up to ten automatic backups and retain them for 30 days by default. Manual backups are protected from automatic retention. Exclude external mod/plugin databases and the manager database (`manager.db`) from backups. Include local mod/plugin data in full server backups, with the consistency caveat for live copies. Restore requires explicit confirmation, creates a safety backup, and runs only with the server stopped. Restore is an explicit operation; never rewind a world automatically as part of an update rollback. World download is supported.

## Metrics, map, and events

Collect JVM metrics (not launcher metrics) every 30 seconds. Retain raw samples for seven days and hourly aggregates for 90 days. Include CPU, memory, disk, players, and player history. Do not expose private JVM or host data to viewers beyond authorized summaries.

Render a saved-world 2D map with standard and custom dimensions, height slicing, and a fallback for unknown block types. Show stale-data state clearly. Sample active viewers/players every five seconds. Document that Minecraft 1.12.2 has map limitations. Track deaths and advancements with provenance; identify gaps when vanilla server logs cannot provide the event and do not imply an add-on is installed.

## Notifications and UX

Support Discord, Slack, ntfy, Telegram, generic webhooks, and email. Show delivery history and redacted failure detail. Keep notification secrets masked and role-gated.

The application must work at a 390 px mobile viewport and support keyboard navigation, visible focus, labels, and accessible dialogs. Use Mantine components and the Minecraft visual theme consistently. Empty database onboarding must lead to the first usable administrator and first server without fixture-seeded state.

## Release compatibility matrix

Stable release qualification includes known archive and world formats: 1.12 legacy layout; 1.16 packing; 1.18 negative Y; 1.20.5 mixed codecs; the Minecraft Server Management Protocol (MSMP), introduced in 1.21.9 as an authenticated JSON-RPC protocol over WebSocket; 26.1 new folders; and 26.3 log changes. Test only applicable versioned behavior and state the known limits. Keep the displayed product version and compatibility claims tied to tested release evidence.

## Required end-to-end and failure evidence

The release test flow starts from an empty database, creates the admin, installs and starts supported real servers, connects a client, and exercises restore. Cover Linux and Windows. Inject failures in downloads, hash verification, archive traversal, backup save-off/save-on, process stop/start, update transactions, and competing file changes. Exercise API roles and audit redaction. A fake server is useful for package tests but does not replace real-server and client connection evidence.

Ordinary implementation model guidance: GPT-6.1-Sol medium for implementation, high for backup/auth/supervision/compatibility and independent reviews; GPT-6-Luna for documentation and mechanical work. Maximum three workers plus root. Work packages have disjoint file ownership and a central integration owner.

## Explicit implementation rulings

- Parallel subagents were explicitly requested for implementation; this overrides any default sequential-dispatch recommendation. Keep ownership disjoint and centralize integration.
- The initialized feature repository is itself the isolated work area. Do not create another worktree unless execution later establishes a need.
- World uploads and remote backups remain excluded even if a reference implementation supports them.
- Authentication may begin against the announced frozen foundation interfaces without editing shared router/build wiring. Do not claim integrated behavior until the foundation task is reviewed.
- Implementation tasks remain pending until their acceptance evidence exists. Planning, scaffolding, and mocks do not count as feature completion.
