# Minecraft Server UI operations

The current build is `0.0.0-dev`. No stable release or supported Minecraft/loader/native-host combination has completed release qualification. Installation-only checks and cross-compilation are development evidence. See [Task 24](reports/task24.md), [Linux qualification](../deploy/linux/QUALIFICATION.md), and [Windows qualification](../deploy/windows/QUALIFICATION.md).

## Installation and recovery boundary

Use the reviewed native installers only after designating a test host and reviewing its service configuration. Linux runs the manager as `minecraft-ui`, with a root-owned `/usr/local/bin/minecraft-ui`; the manager must never be granted arbitrary privileged copy or shell access. The existing `unitctl` accepts lifecycle operations on validated instance identifiers only. Windows Server2022 installed-service, ACL and updater checks have hosted evidence; other Windows targets and the full installer lifecycle still need qualification.

Keep `manager.db` and its SQLite WAL/SHM sidecars private. Game backups exclude manager state. Manager snapshots and restore operate only while the manager is stopped; never copy only a live WAL-mode database file. Restoring manager state requires explicit review and does not rewind game worlds.

## Stopped manager-state snapshots

After stopping the manager through its normal service controls, run these
commands as the manager account with `MINECRAFT_UI_DATA_DIR` set to its existing
data directory (Windows also permits an authorized administrator):

```text
minecraft-ui manager-state snapshot
minecraft-ui manager-state verify --snapshot HEX_ID
```

Use the 64-character lowercase `id` from the first command for `HEX_ID`. A
successful command prints one JSON summary. Snapshots are private directories
at `maintenance/HEX_ID` within the data directory. They include `manager.db`
and every present SQLite WAL/SHM/journal sidecar. Verification checks the
manifest, hashes, schema and SQLite integrity using a separate working copy.
Both commands hold the manager lock and refuse absent or unsafe database state.
They do not start the service or migrate the database.

Treat these snapshots as sensitive: they contain manager users, credentials
and configuration. They exclude worlds and other game files. Keep the complete
directory intact. Snapshot and verification tests have run on Linux and Windows
Server 2022; installed recovery qualification remains pending.

## Stopped manager-state restore

Stop the manager through its normal service controls. On Linux, run restore and
resume as the manager account. On Windows, use LocalSystem or an elevated
Administrator with the data directory on the fixed local C: NTFS volume.
Set `MINECRAFT_UI_DATA_DIR` to that existing data directory. Verify the selected
snapshot first, and review its schema and summary. Restore creates and verifies
a safety snapshot of the current manager database before replacing the complete
SQLite file set. Its successful JSON summary includes the selected snapshot ID,
safety snapshot ID, schema transition and the required resume executable hash.
Keep both snapshots intact.

For a snapshot at the schema embedded in the currently running manager, run:

```text
minecraft-ui manager-state restore --snapshot HEX_ID --confirm-manager-state-restore
```

If the selected snapshot has an older schema, supply the exact version and
SHA-256 of the manager build that will perform resume. That build must support
this restore format and have the same embedded schema as the selected snapshot:

```text
minecraft-ui manager-state restore --snapshot HEX_ID --confirm-manager-state-restore --resume-version VERSION --resume-executable-sha256 HEX_SHA256
```

The version and hash must be supplied together and in that order. Use the
executable hash reported by the trusted release process; do not guess it.
Restoring older-schema state without this binding is refused. The restore leaves
a startup hold for review. Inspect restored manager records and separately
reconcile them with the running world state. Then run the recorded matching
manager build and explicitly review the hold:

```text
minecraft-ui manager-state resume --snapshot HEX_ID --confirm-manager-state-review
```

Resume verifies the selected snapshot and current manager executable before it
clears the hold. Restore and resume do not start the manager or game servers,
copy world files, or rewind world progress. Do not delete `hold.json`,
`hold.pending`, or recovery files manually. To continue an interrupted restore,
repeat the same confirmed restore command with the same executing build and
resume-image options. A valid recorded operation continues from its verified
file locations and retains its original safety snapshot. Ambiguous or changed
state is refused and keeps startup blocked for manual recovery. Resume is only
available after restore reaches its completed, held state.

## Stopped-manager schema migration

Run migration only while the manager service is stopped and after verifying a
matching snapshot. On Linux, run both commands as the manager account. On
Windows, run them as LocalSystem or an elevated member of Administrators; the
data directory must be on the fixed local C: NTFS volume. The Windows command
refuses custom volumes and other filesystems.

Use the source schema reported by `verify` for `N`, and the embedded schema of
the manager build you are running for `M`:

```text
minecraft-ui manager-state migrate --snapshot HEX_ID --from-schema N --to-schema M
```

The grammar is fixed: snapshot ID first, then `--from-schema`, then
`--to-schema`; schema numbers are canonical positive decimal integers. The
command prints one JSON summary when migration reaches the migrated state.
That success still leaves a startup hold in place. Review the manager records
and separately assess the running world state, then explicitly resume with the
same snapshot and build:

```text
minecraft-ui manager-state resume --snapshot HEX_ID --confirm-manager-state-review
```

Resume removes the matching hold after rechecking the current manager database,
snapshot, build version and executable. It does not start the manager or game
servers, change world data, or rewind a world. Start the manager through its
normal service controls only after resume succeeds.

A failed or interrupted migration keeps startup blocked for review or recovery.
Do not delete `hold.json` or `hold.pending` manually and do not use resume to
clear a `migrating` hold. Preserve the original verified snapshot unchanged.

## Manager update trust

Production release source and verification public keys are not configured. Self-update activation must remain unavailable until the integrator supplies and reviews a root-owned trust configuration, a bounded native activation worker, shutdown/drain coordination, and authenticated version-specific readiness. A local checksum or a checksum fetched beside an artifact does not establish publisher identity.

The proposed signed release format binds a strict stable `vMAJOR.MINOR.PATCH` tag, repository, validity window, platform, archive and executable sizes/SHA-256, and database schema. A detached Ed25519 signature covers the exact metadata bytes. Signing private keys belong to the release owner; test-generated keys must never become production trust. Downloads use fixed configured HTTPS release paths and bounded bodies; unsafe or duplicate archive members fail before activation.

The initial schema policy permits only an unchanged database schema. After copying and verifying the candidate into the fixed protected executable slot, the helper runs its fixed capability entry point as the manager account. This reports its embedded schema and version without opening the database, serving HTTP or launching jobs/games. The helper compares that result with the signed metadata, current live schema and captured prior capability, then rehashes the candidate before replacement. A manager capable of running migrations must not be launched before compatibility is established. Automatic executable rollback is safe only when the prior manager can read the database left by the candidate. Refuse schema-changing self-updates until a separately reviewed compatibility protocol exists.

## Manager activation and failure handling

Native activation requires integration review and real host qualification. Its implemented sequence is: import an untrusted staged release into private privileged storage; reverify signed metadata and both archive/executable hashes there; acquire a durable activation lock; write and sync recovery intent; quiesce new admissions and drain active operations; verify that saving leases and file transactions are resolved; finish console/player/scheduler/observation drains; wait for the old manager process and manager lock; verify the live schema/current actor and the candidate embedded capability; replace only the fixed manager executable; start the candidate; require exact authenticated candidate/version/hash/schema readiness; then mark activation healthy durably.

Linux Minecraft instances must survive manager restart. Windows must gracefully stop all managed JVMs concurrently and wait for exit before replacing the running executable, then use validated autostart on manager startup. A manager update must not change game versions, content, configuration, worlds, or backups.

If candidate launch/readiness fails, an independent native worker must restore the captured prior manager executable and attempt one bounded restart. Failed activation cleanup has a separate five-minute budget, including caller cancellation or deadline expiry. The Linux update unit allows fifteen minutes for the nine-minute operation budget, cleanup and margin; an unchanged-manager abort after a failed stop uses three minutes. Recovery intent must be durably saved before a restart or rollback is treated as owned/completed. Keep a durable repair marker when automatic recovery is ambiguous or fails; do not retry indefinitely. Recovery must check executable identities and journal state instead of assuming a file named `.previous` is correct. Never restore an old executable onto a database schema it cannot read. Never restore worlds automatically.

Do not manually overwrite a running Windows executable. Do not bypass a failed signature/hash/schema check or weaken Linux service filesystem restrictions. A failed save-on/recovery drain blocks activation and requires administrator repair.

Linux recovery remains a nonpersistent oneshot required before every ordinary manager start, including later in the same boot. During the updater's own synchronous candidate/prior start, a private launch permit bridges that recovery invocation. It binds the exact journal stage/nonce/phase/image and live fixed updater PID/start time; the kernel must still report that PID holding the protected transaction lock for the exact device/inode. A dead worker or released lock cannot authorize startup. A well-formed permit with proven stale process start/image or released ownership denies only that bypass: ordinary recovery acquires the protected native lock, reconciles the valid journal, then removes and fences the stale permit before success. Malformed/unsafe permits, invalid journals and ambiguous inspection still refuse startup. The manager never skips its systemd dependencies. Permits are removed and fenced on every start return. Root-state ownership, process visibility and actual installed-systemd reboot/rollback behavior still require native qualification.

Windows pipe authentication opens only SCM connect and service query-status access, with limited process query access. Service control remains privileged. The same-account demand-start probe uses a protected, independently verified fixed executable copy. A nonce/version/schema/hash-bound capability request selects its no-database branch; the schema/actor branch remains read-only. No service-account credentials are persisted outside normal SCM credential handling.

The update page reloads at most two reverified staged receipts from status. Repeated staging of the same verified immutable release returns its receipt; it does not require another artifact download. Discovery rechecks publisher signature, expiry, version/schema/platform, archive/executable hashes, receipt identity and checked paths. Invalid or expired state is withheld with a bounded reconciliation message. Reload, stage selection and status refresh leave activation confirmation unchecked. Neither discovery nor retry requests activation. Background status refresh preserves rejected staging/activation errors. A transport-ambiguous staging failure is reported as recovered only when verified status contains the expected version, platform, schema and executable hash; confirmation remains unchecked.

## Release evidence collection

Start each actual platform run with an empty dedicated data directory, the exact shipping binary/hash, browser viewport and version, OS/build, native installer/service configuration and official managed Java identity. Create the first administrator and first instance through the shipping UI. Record explicit EULA consent before any real game launch. Install/start each claimed tuple, connect a real client, verify readiness, create an online backup, confirm save-on, stop, explicitly confirm restore with a safety backup, and reconnect.

Record native process/service restart and crash recovery, manager executable activation/failed-health rollback, traversal/hash/download failure, interrupted saving leases and watchdog, competing updates/writes, role denials and redacted audit output. Capture 390 px keyboard first use. Keep secrets, tokens and player private data out of reports/artifacts. Synthetic fixtures, mocks, cross builds and Wine results must carry their actual classification.

`release/qualification.json` lists outstanding release gates. A non-null path alone is not evidence: the independent reviewer must inspect the linked run records, exact versions/platforms, assertions and failures. Publishing remains disabled until all gates and whole-branch review pass and the release owner separately authorizes publication.

Windows privileged durability currently requires the fixed local C: NTFS volume. The LocalSystem updater flushes that verified whole volume at protected publication boundaries; this can be costly. Other volumes, UNC/reparse/mounted layouts, unsupported filesystems and flush errors refuse activation. Service-account staging/control directory publication is nondurable: loss after a crash requires restaging, and privileged import always rereads, re-verifies and fences its copies. Windows Server2022 installed updater activation and rollback have hosted evidence. Physical power-loss durability remains unqualified.
