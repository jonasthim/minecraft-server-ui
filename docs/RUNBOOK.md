# Minecraft Server UI operations

The current build is `0.0.0-dev`. No stable release or supported Minecraft/loader/native-host combination has completed release qualification. Installation-only checks and cross-compilation are development evidence. See [Task 24](reports/task24.md), [Linux qualification](../deploy/linux/QUALIFICATION.md), and [Windows qualification](../deploy/windows/QUALIFICATION.md).

## Installation and recovery boundary

Use the reviewed native installers only after designating a test host and reviewing its service configuration. Linux runs the manager as `minecraft-ui`, with a root-owned `/usr/local/bin/minecraft-ui`; the manager must never be granted arbitrary privileged copy or shell access. The existing `unitctl` accepts lifecycle operations on validated instance identifiers only. Windows needs a real native service/ACL/process qualification host.

Keep `manager.db` and its SQLite WAL/SHM sidecars private. Game backups exclude manager state. Before an administrative manager migration, separately arrange a consistent manager-state backup while the manager is stopped; never copy only a live WAL-mode database file. Restoring manager state requires a reviewed maintenance procedure and must not silently rewind game worlds.

## Manager update trust

Production release source and verification public keys are not configured. Self-update activation must remain unavailable until the integrator supplies and reviews a root-owned trust configuration, a bounded native activation worker, shutdown/drain coordination, and authenticated version-specific readiness. A local checksum or a checksum fetched beside an artifact does not establish publisher identity.

The proposed signed release format binds a strict stable `vMAJOR.MINOR.PATCH` tag, repository, validity window, platform, archive and executable sizes/SHA-256, and database schema. A detached Ed25519 signature covers the exact metadata bytes. Signing private keys belong to the release owner; test-generated keys must never become production trust. Downloads use fixed configured HTTPS release paths and bounded bodies; unsafe or duplicate archive members fail before activation.

The initial schema policy permits only an unchanged database schema. A manager capable of running migrations must not be launched before compatibility is established. Automatic executable rollback is safe only when the prior manager can read the database left by the candidate. Refuse schema-changing self-updates until a separately reviewed compatibility protocol exists.

## Manager activation and failure handling

Native activation requires integration review and real host qualification. Its implemented sequence is: import an untrusted staged release into private privileged storage; reverify signed metadata and both archive/executable hashes there; acquire a durable activation lock; write and sync recovery intent; quiesce new admissions and drain active operations; release all outstanding saving leases; finish console/player/scheduler/observation drains; wait for the old manager process and manager lock; replace only the fixed manager executable; start the candidate; require exact authenticated candidate/version/hash/schema readiness; then mark activation healthy durably.

Linux Minecraft instances must survive manager restart. Windows must gracefully stop all managed JVMs concurrently and wait for exit before replacing the running executable, then use validated autostart on manager startup. A manager update must not change game versions, content, configuration, worlds, or backups.

If candidate launch/readiness fails, an independent native worker must restore the captured prior manager executable and attempt one bounded restart. Keep a durable repair marker when automatic recovery is ambiguous or fails; do not retry indefinitely. Recovery must check executable identities and journal state instead of assuming a file named `.previous` is correct. Never restore an old executable onto a database schema it cannot read. Never restore worlds automatically.

Do not manually overwrite a running Windows executable. Do not bypass a failed signature/hash/schema check or weaken Linux service filesystem restrictions. A failed save-on/recovery drain blocks activation and requires administrator repair.

## Release evidence collection

Start each actual platform run with an empty dedicated data directory, the exact shipping binary/hash, browser viewport and version, OS/build, native installer/service configuration and official managed Java identity. Create the first administrator and first instance through the shipping UI. Record explicit EULA consent before any real game launch. Install/start each claimed tuple, connect a real client, verify readiness, create an online backup, confirm save-on, stop, explicitly confirm restore with a safety backup, and reconnect.

Record native process/service restart and crash recovery, manager executable activation/failed-health rollback, traversal/hash/download failure, interrupted saving leases and watchdog, competing updates/writes, role denials and redacted audit output. Capture 390 px keyboard first use. Keep secrets, tokens and player private data out of reports/artifacts. Synthetic fixtures, mocks, cross builds and Wine results must carry their actual classification.

`release/qualification.json` lists outstanding release gates. A non-null path alone is not evidence: the independent reviewer must inspect the linked run records, exact versions/platforms, assertions and failures. Publishing remains disabled until all gates and whole-branch review pass and the release owner separately authorizes publication.

Windows privileged durability currently requires the fixed local C: NTFS volume. The LocalSystem updater flushes that verified whole volume at protected publication boundaries; this can be costly. Other volumes, UNC/reparse/mounted layouts, unsupported filesystems and flush errors refuse activation. Service-account staging/control directory publication is nondurable: loss after a crash requires restaging, and privileged import always rereads, re-verifies and fences its copies. No native Windows or physical power-loss qualification is recorded yet.
