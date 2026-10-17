# Linux installer completion

Status: the six-scenario Ubuntu installer milestone is accepted following integrator evidence review. The approved design and Task30 preservation/recovery rules remain binding. Broader Task30 and release qualification remain open; Windows and the wider Minecraft matrix are outside this Linux milestone.

## Deliverable

A shipping Linux entry point that installs authenticated prepared inputs, starts and verifies the manager, checks without mutation, reruns without restarting an unchanged healthy installation, upgrades or repairs owned assets, recovers interrupted work, and uninstalls while preserving user data, configuration, trust and the service account. Native acceptance runs on disposable Ubuntu22.04/24.04 GitHub runners. No development-host service/account/firewall/game changes.

## Sequence and ownership

1. **Updater/session bridge** — protected fixed-root acquisition including first installation, the actual existing updater lock, strict terminal updater reconciliation, stopped-manager/data-lock proof and receipt CAS. Worker owns new `internal/selfupdate/installer_*linux*` files. Existing maintenance and web updater contracts remain intact.
2. **Durable storage** — bounded immutable intent and strict coordinator journal CAS in the fixed protected state root, with identity checks and Linux file/parent fsync. Worker owns `internal/install/store_linux*`.
3. **Asset publication** — fixed Linux asset destinations, exact preimage capture, verified atomic publication, identity-bound removal and uncertainty reporting. Worker owns `internal/install/assets_linux*`.
4. **Native driver and entry point** — integrator freezes the inventory/intent/native-effect contracts, then delegates the Linux driver and service control. Integrator owns CLI, startup fence, independent worker dispatch and shell wiring. No caller-selected output paths or manager web actor fabrication.
5. **Native acceptance** — shipping install, unchanged rerun, read-only check, upgrade, failure recovery, uninstall and retained-data reinstall; subsequent ordinary manager update remains functional. Run actual systemd/account/file operations only on disposable runners. Package fixtures are prerequisites, not acceptance substitutes.

Each packet requires independent source review and integrator verification before its ledger status changes. Do not claim the Linux installer complete after the first three primitives.

## Integration decisions

- Fixed canonical Linux installation first. The new privileged state directory and existing updater operation lock are coordination metadata; their creation may precede a transaction. Shared directories and user data are never recursively repaired or removed.
- `check` is read-only and does not create directories, lock files, accounts or SQLite sidecars. Missing, drifted and pending states are explicit.
- Upgrades stop only the manager. Existing independent game units continue running. Uninstall refuses while any managed game is active or uncertain.
- First installation accepts an explicitly supplied, independently trusted public-key file. Prepared metadata does not supply its own authority. Managed upgrades bind to the protected installed bootstrap trust; trust rotation is separate. Development trust requires the explicit development mode.
- Receipt-only `AcquireMaintenance` stays strict. The Linux installer bridge may inspect a valid terminal updater transaction, then reconcile only its captured owned objects after manager stop and actual data-lock acquisition. Nonterminal/ambiguous updater state requires its existing recovery path.
- Normal apply requires unchanged manager DB schema. Explicit offline migration/restore remains separate. Neither automatic installer rollback nor manager rollback rewinds worlds or manager.db.
- Launch permission during an installer transaction must bind the exact immutable intent, expected image, live worker start identity and existing kernel updater lock. A stale permit never bypasses recovery. The installed updater recovery callback must recognize this independently of the existing web-update permit.
- The worker must survive the invoking shell exiting. It runs from a verified private transaction copy and keeps bounded rollback independent of caller cancellation; a reboot/worker kill leaves durable recovery state and blocks normal startup until reconciled.
- On rollback/uninstall, preserve the account, data, configuration, trust and snapshots. Unknown replacements are left intact with a truthful recovery-required result.
- Explicit recovery may complete a fully written pending publication only after validating its typed operation, immutable intent, permitted transition, exact current preimage and native file identities. Read-only check and ordinary apply do not reconcile pending files. Partial, corrupt or unrelated pending files remain untouched.
- Before publishing a maintenance challenge, the installer may remove only the exact captured controls of a verified terminal ordinary updater operation under the actual updater lock. Privileged updater records and executable scratch remain until the manager has drained and the data lock is held. The manager never receives both challenge protocols at once.
- A durable native evidence marker records that ordinary updater reconciliation finished before asset replacement. Recovery must not require the current executable to match the prior receipt while an installer replacement is between its asset and receipt publications.
- Read-only check may report the current verified terminal updater receipt when it supersedes the installer inventory's manager image. The database schema and every other owned deployment asset must still match; operator-managed optional updater trust is separately validated.
- Asset copies must remain anonymous until their verified inode and content are durably recorded. Use Linux `O_TMPFILE` in the destination filesystem, record before linking the fixed staging name, then perform the existing exact publication checks. This closes the reproduced worker-kill window before ownership recording. Unsupported filesystems fail safely without a named-copy fallback; unknown existing scratch remains untouched and prevents a terminal success claim.

- Failed reinstall rollback from a validated `uninstalled` inventory restores an intentionally absent manager executable. A separate Linux installer receipt operation may then restore its retained receipt under the actual updater/data locks, stopped-service proof, exact session-owned current receipt and protected-root/binary-absence checks. Ordinary receipt CAS continues to require a matching executable. The driver selects this operation only from the validated immutable prior inventory; foreign replacements and uncertain publication still require repair.

## Current acceptance

The common coordinator, manager-side drain/readiness hooks, authenticated input importer, receipt CAS and offline manager-state commands already have reviewed evidence in `docs/IMPLEMENTATION.md`. This plan adds Linux integration; it does not reset or reimplement accepted packages.

[Run 39069774439](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39069774439) at `3f22bb431d3b5bc95f92cc8e23319d28a1931b12` passes lifecycle, retained failure/retry and ordinary updater coexistence on both Ubuntu 22.04 and 24.04. The integrator reviewed source/runner bindings, acceptance reports and successful cleanup for all six scenarios; an independent reviewer also inspected retained-retry and coexistence artifacts. The [qualification report](../../reports/task30-linux-qualification.md#accepted-ubuntu-installer-milestone) records the cases and limits. This accepts those native scenarios, without closing Debian, Windows, actual Minecraft compatibility, reboot/power-loss coverage or stable publication gates.
