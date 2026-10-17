# Task30 installer lifecycle implementation proposal

Status: proposed for integrator review, 2026-10-17. This is an implementation decomposition, not acceptance evidence. Only this report was written; no product files, services, accounts, games, stages or commits were changed by this task.

## Basis and existing authorization

The approved design, original Tasks04/05/24, corrective Task30, `docs/IMPLEMENTATION.md`, and `docs/reports/valheim-delivery-parity.md` already require complete installation, check, rerun/repair, upgrade, uninstall, Windows account/logon bootstrap, helper maintenance, preservation and recovery. The user has authorized continuing implementation and disposable GitHub native tests. These do not require another product-scope permission request. Local machine account/service/game changes remain outside this assignment. Production signing identity and public publication remain separate owner decisions.

Read sources include the design/original plan/ledger/corrective plan, both current native installers and unit definitions, candidate/bootstrap/outer contracts, native updater transaction/driver/permissions/durability/recovery, manager drain/probe/readiness composition, DB/recovery/manager locks, existing native harnesses, and the local Valheim installers. Valheim supplies the user-visible lifecycle precedent; retain Minecraft's stronger signed publisher verification, protected executable directories and unchanged-schema self-update policy. In particular, do not copy Valheim's Windows service-account write permission on its executable directory.

A1 `72ea5bb` plus `f6d2906` and A2 `2739f92` are accepted source scope. Root's candidate workflow/native preservation gate is independent and still being completed. Keep A1/A2 preparation-only. `completion.json` is a completion/provenance receipt, not an authorization to install files at a later privilege crossing.

## Findings that constrain the design

| Current source | Concrete consequence |
| --- | --- |
| `deploy/linux/install.sh` copies the manager, unitctl, units and optionally updater before invoking `bootstrap`; updater rejects existing `installed.json` | A rerun can partially replace a live installation, then fail. The receipt must be maintained through a new reviewed operation. |
| `deploy/windows/install.ps1` rejects an existing `MinecraftUI` service and requires a prepared account | Rerun, repair, upgrade and account bootstrap are absent. |
| Both installers leave start/readiness to the operator | Successful file copy does not prove installation success. |
| `internal/selfupdate/native.go` has a manager-only, unchanged-schema `activationJournal` | It cannot represent helper/unit/service changes or schema migration. Preserve its format and meanings. |
| Linux `recover` and Windows updater service own independent executable recovery; probe/capability operations run as manager account | Reuse this boundary; do not execute candidate manager code as root/LocalSystem. |
| Windows `windowsDriver.probe` replaces the fixed probe image for each verified probe | Probe maintenance must serialize with this operation and wait for actual probe process exit. A probe hash is meaningful only at a quiescent boundary. |
| `db.Open` migrates; `db.OpenExisting` requires the binary's current schema | Neither is a generic inspect/snapshot operation across an upgrade. Introduce explicit no-migration maintenance entry points. |
| Native Task30C snapshots saw only DB after clean shutdown | They do not prove retained-WAL preservation. Keep the existing acceptance and add a separate retained-WAL test. |
| `candidate` verifier code imports `selfupdate`; `bootstrap` also imports both | A selfupdate-to-bootstrap/candidate dependency creates a cycle. Put installer verification/orchestration in a separate package. |

## Recommended architecture

Use the existing updater executable for a **local administrator-only `maintain` command family**, backed by a new `internal/install` package. Keep shell/PowerShell as operator entry points and Windows credential/account provisioning. Keep all existing manager-accessible sudo verbs, unitctl grammar, pipe messages, paths, update manifest and activation journal unchanged. No extra binary, runtime, daemon dependency or Go module is needed.

The common installer engine owns authenticated input import, checked protected storage, transaction inventory, durable progress, independent rollback and receipt publication. Platform adapters own native service/account-independent file operations. Windows PowerShell alone handles `PSCredential`, local account creation and SCM credential assignment; it never serializes a password into the engine's journal or arguments.

An apply runs as a separate administrator process from a private transaction copy of `selfupdate[.exe]`, rather than from the installed helper being replaced. The script verifies the bundle before executing this helper; the helper reimports and verifies all inputs into protected storage before mutation. Linux uses an independent bounded root worker/unit; Windows uses an independent elevated worker, with inherited private handles/explicit process identity, not a child in the manager's Job Object. Parent cancellation/disconnection cannot discard cleanup. Do not add an unattended credential store or an arbitrary command execution interface.

The process is independent of the manager and the invoking script; it is not immune to reboot or a forced kill. A durable interrupted transaction is recovered by a fixed-path administrator `maintain recover` entry point. Normal service startup must reconcile or refuse a pending installer transaction before exposing the manager. This startup fence is part of root integration, not an optional documentation instruction.

Alternatives rejected for this packet: a shell-only transaction with unverified JSON and ad hoc receipt editing duplicates existing security code; expanding the manager activation journal changes an accepted recovery protocol; a permanent new maintenance service/binary expands the bootstrap surface without requiring new capability.

## Contracts the integrator should freeze first

### Command surface

Recommended public wrappers:

- Linux: `install.sh --prepared ABS_DIR --trust-file ABS_FILE [--allow-development]`, `install.sh --check`, `install.sh --repair --prepared ABS_DIR --trust-file ABS_FILE`, `install.sh --uninstall`, `install.sh --recover`.
- Windows: corresponding `-Prepared`, `-TrustFile`, `-Check`, `-Repair`, `-Uninstall`, `-Recover`; first install/recreation additionally accepts `-Credential` and explicit `-CreateServiceAccount`.
- Exact version versus latest stable remains acquisition's `--version`/`-Version`; the prepared receipt records the resolved immutable version. Do not resolve latest again during apply.
- Keep existing positional Linux binary/updater and Windows `-Binary/-UpdaterBinary` temporarily as explicitly labelled local-development qualification modes, with the same lifecycle engine and safety gates. They must never silently stand in for publisher-authenticated installation. Require an explicit development option in new documentation; root updates old harness callers together.

Recommended updater command dispatch, behind a real root/elevated-token check before any I/O:

```
selfupdate maintain check
selfupdate maintain apply --prepared ABS_DIR --trust-file ABS_FILE [--repair] [--allow-development]
selfupdate maintain recover
selfupdate maintain uninstall
```

Only `apply` accepts input paths; they never select output paths, commands, service names, repository endpoints or executable names. Protected journal recovery takes no caller path. No `maintain` verb is added to sudoers or the manager pipe. Check reads only: no mkdir, lock creation, sidecar creation, service start, account/right creation, journal cleanup, network or download. Exit 0 means coherent installed/retained-uninstalled state; exit 1 means drift or pending repair; exit 2 means invalid input or unsupported/unsafe host. Return bounded fixed-field JSON suitable for the native harness.

Freeze these package entry points in root's contract patch before parallel implementation:

```
package install

type ApplyOptions struct {
    Prepared, TrustFile string
    Repair, AllowDevelopment bool
}
func Check(context.Context) (CheckReport, error)
func Apply(context.Context, ApplyOptions) (Result, error)
func Recover(context.Context) (Result, error)
func Uninstall(context.Context) (Result, error)
```

`CheckReport` and `Result` use explicit enums/booleans/version/hash identities, bounded counts and fixed stage labels; they never contain credentials, raw config, paths from external metadata, SQLite rows or arbitrary process errors. Filesystem mutation uses an internal adapter with a fake implementation only in deterministic package tests. Native adapters derive canonical paths; no exported caller-supplied production path override.

Root supplies a narrow bridge in new `internal/selfupdate/maintenance*.go` files for protected-root/file validation, privileged fences, current installed-receipt read/CAS publication, native transaction locking, challenge/drain/readiness and fixed-account probes. The bridge exposes capabilities, not arbitrary file-copy or command primitives. Internal install must not reach into `activationJournal` or fake an `ActivationRequest.ActorID`. Local OS administrator authority is a separate authority source; existing web-admin actor revalidation remains intact for manager-initiated updates.

### Fixed storage and ownership

| Purpose | Linux | Windows |
| --- | --- | --- |
| Existing native update state | `/var/lib/minecraft-ui-update` | `C:\ProgramData\MinecraftUIUpdate` |
| New installation inventory | state `/installation.json` | state `\installation.json` |
| New lifecycle intent | state `/installation-transaction.json` | state `\installation-transaction.json` |
| Transaction preimages/executor/imports | state `/install/<64-lowercase-hex-ID>/` | state `\install\<64-lowercase-hex-ID>\` |
| Private manager-state snapshots | data `/maintenance/<64-lowercase-hex-ID>/` | data `\maintenance\<64-lowercase-hex-ID>\` |
| Existing update lock | state `/manager.lock` | state `\manager.lock` |
| Existing manager lock | data `/manager.lock` | data `\manager.lock` |

Use the existing privileged update lock to exclude self-update and installer changes. Establish lock ordering before adapters: privileged operation lock → stop/wait manager → manager data lock. Release the data lock before starting manager/probe or offline manager-state child commands that acquire it. Keep the privileged operation lock and startup fence across that handoff, then reacquire/revalidate the data lock before further file mutation. Never nest the parent data lock around a child that needs that lock. Do not unlink lock files or create a second lock that lets installed self-update race.

All privileged new files have the same root/Administrators/System ownership and no manager write access as current protected updater state. Use exclusive creation and retained identities, reject links/reparse points/hardlinks/unsafe ancestry, and fence durable publications with the existing platform policy (Linux file/parent sync; fixed C: NTFS volume fence on Windows). Snapshot directories are private and manager-account-owned for offline data commands, with protected inventory only recording bounded identity/digest information. Never recursively chmod/chown the existing instance tree.

`installation.json` format1 records canonical platform, resolved version, signed bootstrap digest, manager/updater/member identities, allowlisted installed asset destinations/modes, service identity/startup/environment facts, Windows manager SID, and installed/uninstalled state. It contains no executable caller-supplied paths or secrets. Existing operator files are recorded as preserved identities rather than copied into reports. The manager member in this inventory records the installer baseline, not a replacement authority for current `installed.json`: successful ordinary self-update changes the current manager without rewriting the installer inventory. Check/next maintenance capture the current protected installed receipt and coherent terminal activation state, verify its actual executable, and carry that identity into the new transaction; they must not flag a legitimate self-update as tampering merely because the bootstrap baseline differs. Updater/unit/helper hashes remain inventory-bound. The Windows probe identity is refreshed only after a quiescent verified probe operation; normal self-update may change it, so check must compare it with permitted installed/current/transaction images rather than a permanently stale bootstrap hash.

`installation-transaction.json` format1 records transaction ID, mode, exact old/new inventories, owned staged/preimage file identities, original startup policy, finite progress, failed stage and recovery disposition. Phases: `prepared`, `quiescing`, `stopped`, `replacing`, `published`, `verifying`, `committed`, `rolling_back`, `rolled_back`, `manual_repair`. Persist intent before each effect; absence and preimage are different states. Keep this journal independent from `activation.json`, `installed.json`, `account.json`, requests and manager writable controls. Unknown fields/phases, duplicate JSON members, overflow and mismatching file identities refuse mutation.

### Existing installations and installed.json

First install may use existing bootstrap once, after capability/hash checks. An existing `installed.json` is never removed to rerun bootstrap. A root-only compare-and-swap maintenance operation checks its captured prior version/schema/hash and actual current executable, then atomically updates the receipt only after publishing the verified replacement. Recovery can restore only the journal-bound old receipt/image. On reinstall after data-preserving uninstall, a protected `uninstalled` inventory authorizes intentional absence of executable/service while retaining the old receipt; maintenance publishes the new receipt through that transition.

A pre-inventory installation needs one bounded adoption path: check exact canonical unit/SCM bindings, actual unprivileged identity, protected files/ancestry, receipt/hash and no pending work, then capture an inventory before changes. A changed/missing helper or unfamiliar unit is reported as drift. `--repair` may replace an allowlisted shipped asset after preserving its preimage; it does not bless unsafe ownership, a different SID, unknown executables, a pending update, or tampered trust. Missing inventory is not proof of a clean machine.

### Recovery and service-start boundary

Root owns the shared integration that runs installer recovery ahead of existing updater recovery. Both protocols use the same privileged lock and must never recursively acquire it. A pending ordinary manager-update journal/request is first reconciled by the existing updater or reported as requiring that recovery; the installer cannot reinterpret it.

A synchronous installer-owned start needs the same quality of proof as the existing Linux launch permit: exact live worker PID/start/image, kernel ownership of the existing update lock, transaction ID/phase/nonce and expected published executable/receipt. Put any installer permit in the transaction directory; do not change the existing update permit/journal wire format or skip systemd dependencies. A stale worker cannot authorize startup. Normal boot/restart without a live permitted worker recovers coherent preimages or stays stopped with `manual_repair`. Windows enforces the same fence before the updater dependency reports readiness. Root may share process/lock identity helpers; workers must not invent a marker that merely says recovery already ran.

## Lifecycle behavior

### Install, same-version rerun and repair

Preflight native host/architecture, systemd or SCM, existing paths/services/accounts, available space, executable/metadata bounds, schema and pending transactions before changing the installation. No EULA acceptance or game launch is part of installer success. Linux documents existing `sudo`, `runuser`, `visudo`, systemd, coreutils and acquisition curl prerequisites; no package-manager invocation is silently added. Windows supports stock Windows PowerShell5.1 and PowerShell7 using native APIs and existing Go dependencies.

Reauthenticate `bootstrap.json`/`bootstrap.sig` against independently supplied public trust and requested receipt identity; verify every exact bundle member on a protected private copy. `completion.json` alone is insufficient. Candidate schema/version capability is probed as the manager account before normal startup. Install account/directories/verified assets/services, bootstrap the native receipt, start updater/recovery ordering and manager, then require the actual setup API/readiness identity. Empty state must lead to the existing first-admin flow.

An identical same-version rerun succeeds without changing config/trust/data or rebooting a healthy coherent installation. If assets require repair, use the same transaction as upgrade. An existing environment file, registry Environment values, listener address, public trust, accounts, DB, runtimes, instances and backups are preserved. Initial trust comes only from the independently reviewed input when no installed trust exists; a different incoming trust file cannot silently rotate installed trust. Do not replace existing private data ACLs recursively or replace unknown service overrides silently.

Canonical native self-update remains bound to the existing data directory and `127.0.0.1:8080`. Existing custom deployments remain custom and report activation unavailable. Scope choice recommended here: lifecycle support first covers canonical native installation; retaining `-DataDir` compatibility must not imply updater support on an arbitrary/UNC volume. Root should reject or explicitly classify custom layouts before delegating adapters.

### Upgrade and failure rollback

1. Stage/reverify all new bytes and capture exact manager/helper/unit/service preimages, receipt and startup policy before quiescing. Refuse pending nonterminal self-update or ambiguous installer state.
2. Publish a fresh challenge; stop the manager through its native graceful path. Require successful drain of HTTP/admissions/jobs/samplers/save leases/file transactions, process exit and data-lock acquisition. For an already stopped manager, verify stopped DB/transaction state without fabricating a drain receipt. A failed stop/drain cannot authorize replacement.
3. Linux never stops/restarts `minecraft@*.service` for manager replacement. Keep game template execution paths stable and replace executable inodes by verified rename. Live launchers/JVMs retain their identities. Windows waits for manager, launcher/JVM descendants, probe and updater process exit before replacing their respective images; no force kill counts as graceful success.
4. Replace manager, updater, unitctl, shipped unit definitions and probe coherently under the separate lifecycle intent. Stop/wait the Windows updater before replacing `selfupdate.exe`; the transaction executor is elsewhere. Account/SID, service environment, administrator config and trust stay preserved. Do not overwrite an existing unrelated drop-in.
5. Publish `installed.json` and installation inventory as journaled durable state; configure/start native recovery ordering, release the data lock and start manager with the installer permit. Check nonce/version/schema/executable readiness, actual API setup/login behavior and updated helper capability. Commit only after all checks pass.
6. On failure, the independent worker uses a fresh bounded cleanup context, stops/waits candidate processes, restores verified binary/helper/service preimages and receipts, then attempts one bounded old-manager restart. Restore only when the prior manager can read the actual current schema. Record and report unsuccessful rollback truthfully. Do not restore `manager.db`, worlds, server configuration, content or backup archives automatically.

Root must set explicit budgets in the contract patch: retain the existing 9-minute ordinary operation plus independent 5-minute rollback budget and native timeout margin unless actual evidence requires another bounded value. Helper rollback covers service definitions/configuration, native helper/probe identities and the manager receipt; merely restoring manager.exe is insufficient. Do not delete preimages or mark recovery complete before native/file/receipt fences succeed.

### Data-preserving uninstall

Stop/wait the manager and privileged/probe workers, exclude active transactions, disable/remove only allowlisted owned native registrations/grants/executables, and mark protected inventory `uninstalled`. Keep data, config, trust, snapshots, runtime/world/content/backup trees, the service account and sufficient protected inventory for a safe reinstall. On Windows `update-trust.json` stays in its protected Program Files directory even after its executables are removed; do not promise that directory is empty. Unknown files remain and are reported.

Recommended operational choice: Linux uninstall refuses while any managed game unit is active or process identity is ambiguous, with exact instructions to stop games intentionally first. It does not strand running games by deleting their helper/template. Ordinary upgrade/rerun continues to preserve running games. Remove autostart links only for validated recorded instances, not arbitrary units matching a loose glob. Preserving accounts avoids orphaning data ownership and does not invent account-deletion scope. Test a second uninstall and reinstall using retained state.

## Windows account and logon details

Retain the dedicated unprivileged `PSCredential` model. First install supports either an existing local account with the supplied credential or explicit creation of a named local account using that credential. Reject LocalSystem, built-in Administrator, administrator-group membership and identity mismatches by resolved SID, not only name regexes. Bind manager and probe to the same SID; updater remains LocalSystem. Keep binary/trust RX versus private data Modify versus privileged state administrators/System-only.

Use a bounded LSA `SeServiceLogonRight` add/query operation; the existing qualification helper demonstrates a dependency-free API path. Query before adding and record whether this transaction added the right. Never use policy-wide export/import, remove someone else's existing rights, or override `SeDenyServiceLogonRight`/domain policy. Logon refusal remains a visible actionable error. Use localized identities by SID.

Create/recreate manager/probe service registrations from PowerShell while the credential remains in memory/SCM, with startup disabled until the engine publishes verified executables. Inventory the preexisting/created registrations before provisioning, so initial failure deletes only newly owned registrations. Upgrades of valid existing services retain SCM credentials and ordinarily need no new credential; recreation of a missing probe/manager explicitly requests `PSCredential`. No account switch or password reset is implicit in rerun. Ordinary upgrades update existing SCM configurations in place and never delete/recreate credential-bearing registrations; otherwise independent rollback could not reconstruct the SCM password. A previously missing service may be newly created with the supplied credential and removed on rollback to its captured absence. Existing account/password recovery instructions update both SCM registrations through in-memory credentials and verify SID/logon/startup again.

Keep account provisioning distinct from executable activation: a failure before an old installation is modified can leave a newly created disabled account/right only when cleanup cannot safely prove ownership; report that partial state. Native tests must cover both create-account and existing-account modes, missing-logon-right repair, existing service credentials, service recreation, and failure without credential artifacts.

## Stopped manager-state snapshot, migration and explicit recovery

Add a small `internal/managerstate` package and no-server CLI dispatched by `cmd/minecraft-ui`. These commands run under the manager OS account (or explicitly authorized Windows administrator), acquire the real manager data lock, validate existing DB/sidecars without creating a new DB, and never start HTTP/jobs/games. Snapshot must support an existing older schema; it cannot use `db.Open` or require `db.OpenExisting`'s current-schema equality.

Recommended frozen CLI:

```
minecraft-ui manager-state snapshot
minecraft-ui manager-state migrate --snapshot HEX_ID --from-schema N --to-schema M
minecraft-ui manager-state restore --snapshot HEX_ID --confirm-manager-state-restore
```

The snapshot command creates a new private `maintenance/HEX_ID` directory exclusively. Hold the manager lock across source-set discovery, copies, identity/size/hash verification and durable snapshot manifest publication. Copy every existing `manager.db`, `-wal`, `-shm` and `-journal`; reject missing DB, links/hardlinks/identity changes/unsafe names, disk exhaustion and concurrent starts. Snapshot file presence must exactly match the captured set. Validate a **working copy** of the snapshot with SQLite WAL recovery enabled, `integrity_check`, current Goose schema and required application schema/row checks. Opening validation must not mutate the preserved raw snapshot. `immutable=1` must not conceal committed WAL content. A nonzero DB or `quick_check=ok` alone is inadequate.

Migration is explicit offline maintenance, never ordinary rerun or manager self-update. Require the validated pre-migration snapshot, expected old schema and candidate embedded target schema; use existing embedded migrations without HTTP/autostart. Keep the installer journal/schema intent durable before calling it and re-read actual schema afterward. No downgrade path is implied. Once schema may have changed, an old executable cannot be automatically restarted solely because its bytes were restored. Leave startup blocked with exact recovery commands if compatibility is not proven.

Restore is a separately explicit manager-state action while stopped. First take another validated snapshot of current manager state, verify the chosen snapshot and version/schema binding, then restore the complete SQLite file set with durable recovery intent. Never pair an old DB with stale WAL/SHM. Preserve failed/current state for inspection; do not delete an unexpected sidecar. The operation changes manager records/credentials/schedules and can diverge from worlds that continued on Linux. Finish behind a manual review/startup hold until the installed matching binary and instance state are reconciled; restoring manager state never copies or rewinds world files. Native tests must exercise exact documented commands. This is the existing manual-schema-maintenance exception, not a relaxation of automatic rollback.

## Disjoint implementation packets and order

Root first adjudicates the recommended canonical-layout, no-op rerun and active-game uninstall choices above, freezes the listed structs/command grammar/journal schemas/path map/startup hook/bridge, and owns all shared wiring. No new user permission is implied by these routine implementation choices. This report deliberately leaves acceptance in the root's ledger.

| Packet | Exclusive owned files | Deliverable and acceptance |
| --- | --- | --- |
| 30L1 transaction core | New `internal/install/{contract.go,verify.go,state.go,transaction.go,recovery.go,check.go}` and focused matching tests; `docs/reports/task30-lifecycle-core.md` | Reverify bootstrap/private inputs; durable exact inventory/CAS receipt transitions; finite recovery; no-op/check/uninstall semantics against deterministic fake native adapter; kill/fault boundary tests prove preimage ownership and no world/DB restoration. |
| 30L2 Linux adapter | `deploy/linux/install.sh`; new `internal/install/native_linux.go`, `native_linux_test.go`; `docs/reports/task30-linux-lifecycle.md` | Native preflight/account/directories, strict helper/units/sudoers maintenance, independent worker, coherent start/drain/rollback, active-game survival and uninstall refusal. Unit/drop-in content changes requested from root if shared; Linux owner does not edit updater/core. |
| 30L3 Windows adapter | `deploy/windows/install.ps1`; new `internal/install/native_windows.go`, `native_windows_test.go`; `docs/reports/task30-windows-lifecycle.md` | Account/right/credential model, existing/recreated SCM services, SID/ACL checks, independent worker, stop/wait replacement, updater/probe maintenance/rollback. Keep all PowerShell helper code in the authenticated existing installer file to avoid changing bundle members. |
| 30L4 manager state | New `internal/managerstate/**`; new `cmd/minecraft-ui/manager_state.go` and CLI tests; `docs/reports/task30-manager-state.md` | Existing-schema locked raw DB+WAL snapshots, working-copy validation, explicit offline migrations/restore and no autostart. Native retained-WAL coverage; no root main edits. |
| Root integration/qualification | `cmd/minecraft-ui-update/main.go`, `cmd/minecraft-ui/main.go` dispatch, new `internal/selfupdate/maintenance*.go` bridge and shared startup hooks, affected unit/drop-in/sudoers wiring only if needed, build/candidate contract references, all workflow/harness wiring and operator docs | Review bridges first; verify both platforms together; run actual install/rerun/upgrade/failure/recovery/uninstall; update ledger after evidence review. |

Recommended internal adapter seam, frozen in L1's `contract.go` by root before either adapter starts:

```
type nativePlatform interface {
    Inspect(context.Context) (NativeState, error)
    StopManager(context.Context) error
    StopAuxiliaries(context.Context) error
    ProbeCandidate(context.Context, Image) (Capability, error)
    Configure(context.Context, ServiceState) error
    StartManager(context.Context, StartPermit) error
    WaitHealthy(context.Context, Image, string) error
    RemoveRegistrations(context.Context, ServiceState) error
}
func newNativePlatform() (nativePlatform, error)
```

`Image` is exactly version/schema/SHA256; `Capability` mirrors the existing fixed no-migration probe; `StartPermit` binds the journal/worker/nonce/image described above. `NativeState` contains typed manager/auxiliary process identities and a `ServiceState`; `ServiceState` contains only the finite canonical services, existence/startup/dependency/environment references and SID/UID facts. Environment bytes, if required for exact rollback, stay in private preimages and never enter public results. Engine state, receipts, per-file intent/preimage/publication/rollback and protected-root operations stay in L1 rather than hidden inside platform `Configure`. The core calls these small native operations between its own durable checkpoints. Windows `Configure` updates existing credential-bearing services in place; credential-only first registration/recreation remains in the wrapper's explicitly journaled provisioning phase. Root must define the concrete fields of these structs in the initial contract patch, rather than permit adapter-local parallel variants.

L2/L3 may start against L1's frozen interfaces; do not edit shared contract files. L1's engine uses build-tagged native constructors so adapters remain disjoint. L4 is independent after data/path/CLI contracts freeze. To limit cost, run at most two implementers plus root, reuse workers for their fix rounds, and defer a broad independent review until each coherent packet is ready. Security-sensitive transaction/Windows/state work needs competent review; cheaper models fit docs/mechanical harness updates after contracts. Root should not spend three workers independently redesigning lifecycle orchestration.

Do not stage/commit without root's explicit assignment. Do not update `docs/IMPLEMENTATION.md` from worker packets. Native and source acceptance remain separate.

## Native harness and shared-path changes root must include

Use a new lifecycle-specific `deploy/qualification/linux-installer.sh`, `windows-installer.ps1`, bounded `installer_api.py` and `installer_paths_test.py`; wire `.github/workflows/installed-installer.yml`. Reuse existing setup/login/CSRF/private report helpers where behavior matches. The dedicated workflow consumes exact retained signed development candidate bundles and records their commit/version/hash; package two versions from the same source for lifecycle transitions, clearly labelled as development tests. This does not prove historical public-version compatibility.

Root must update old manager/updater/game harness assumptions together: installers now may start services, have explicit development modes, maintain inventory/transaction files and leave retained data on uninstall. Existing harnesses that intend to start services themselves should use an explicit supported `--no-start`/`-NoStart` **qualification mode**, or root should remove the redundant starts; do not silently seed files/service state to bypass the shipping path. A no-start mode cannot satisfy ordinary installer readiness acceptance.

Exact extra owned paths for preexisting refusal and cleanup are both platforms' state `/install`, `installation.json`, `installation-transaction.json`, inventory pending files, transaction pending files, and data `/maintenance`; existing `/var/lib/minecraft-ui*` and Windows data/updater-state roots cover their descendants. Also inventory any new exact scratch/publication slot chosen during root implementation. Do not add blanket cleanup of Program Files/ProgramData, `/usr/local/bin` or unknown service/account names. A separate executor stays under the checked existing private state roots, avoiding another untracked temporary namespace.

Harness cleanup is stronger than product uninstall: after asserting uninstall preserved data/config/trust/accounts, the disposable harness removes only its captured owned state/account/right and verifies final absence and zero processes. Keep the Linux borrowed `/usr/local/bin` ownership/inode/mode preparation/restoration and Windows reparse/SID/ACL guards. Preserve already accepted `.new/.next/.previous` refusal and cleanup. Retain only allowlisted booleans/counts/versions/fixed stages/native process identity metadata; no DB/WAL/snapshot/raw journals/credentials/trust bodies/transcripts enter artifacts.

Required matrix on Ubuntu22.04, Ubuntu24.04 and WindowsServer2022:

1. Clean authenticated candidate install through the shipping wrapper → genuine setup API → first admin/instance through existing APIs → persistent configuration. No preseeded DB.
2. Check hashes/mtime/ACL/service/account/data state before and after read-only check, proving zero mutation. Malformed prepared receipt, expired/wrong-key metadata, member change after preparation, unsafe ancestry, unsupported host and unknown preexisting state fail before host mutation.
3. Identical rerun, repair of a known missing/changed shipped helper, successful version upgrade, updated updater/probe/unit identities, valid installed receipt, preserved users/config/trust/world/backups and a functioning subsequent manager self-update.
4. Failures before stop, after stop, between helper/image publications, before/after receipt publication and during candidate health. Kill the invoking script while independent rollback proceeds; separately kill the worker/reboot-test boundary and invoke fixed recovery. Require coherent old assets/receipt/service startup or truthful stopped manual-repair state, never mixed success.
5. Linux independent harmless service identity survives the installer manager upgrade/rollback. The existing dummy unit is only substrate evidence; Task33 must additionally repeat manager replacement against its real shipping-installed game and verify launcher/JVM/client/world continuity. Do not weaken sealed-runtime verification to run a fake Minecraft fixture.
6. Windows real SCM manager/probe/updater stop/wait and old-image handle tests prove no live overwrite; existing/create-account/logon-right modes and credential preservation execute natively. Task33 separately proves graceful actual JVM stop and validated autostart; no fake process result claims game behavior.
7. Snapshot a stopped manager with an actual retained committed WAL on each native OS. A dedicated crashable SQLite fixture can create this state under the real data lock after manager stop; demonstrate row/schema loss without WAL, exact sidecar copy and working-copy integrity while leaving raw snapshot immutable. Label it retained-WAL storage evidence. Also run clean-manager snapshot and actual offline command/migration/restoration with an explicit hold.
8. Uninstall preserves data/config/trust/account, removes owned services/grants/executables, repeats safely, then reinstall recovers first-admin/instance identity from retained data. Linux active-game uninstall refuses without disturbing that game.

Source tests must show useful negative controls: bypassing member revalidation, receipt CAS, helper preimage restore, stopped-process/lock gates, sidecar completeness or owned-cleanup checks makes a covering regression fail. Full native source CI and existing installed updater regression gates run after shared bridge integration. Other Windows/Linux families, production trust distribution, physical power-loss and real game matrix retain their independent release gates.

## Decisions for root, and actual external blockers

Recommended routine scope decisions for root are explicitly marked above: canonical native layout first; healthy identical rerun does not restart; uninstall retains accounts/trust/snapshots and refuses active Linux games; interrupted ambiguous maintenance blocks startup and exposes fixed recovery; schema-changing upgrades require explicit offline maintenance. These fit already approved preservation/recovery rules and should be adjudicated in the integrator contract patch, without another user permission round.

There is no unavoidable new product decision blocking implementation. Production public-key custody/authentication/rotation and stable publication authorization are still external owner inputs; ephemeral signed fixtures permit implementation and native qualification now. Native support for Windows10/11/Server2019 and additional Debian hosts still needs real execution environments before claims. Existing unresolved installed-updater diagnostics remain prerequisites for claiming the combined path works; this proposal does not waive them.

## Integrator review — prerequisites before implementation

The decomposition is retained as a proposal, not a frozen transaction API. Root
found one input-contract gap: accepted A1 emits only authenticated bundle members
and completion.json; it does not preserve bootstrap.json/bootstrap.sig. Therefore
the proposed later privileged reauthentication cannot yet be implemented from
Prepared+TrustFile. Do not trust the unsigned completion receipt as authority.

Recommended first packet30L0: preserve the exact already verified bootstrap.json
and detached bootstrap.sig as additional private preparation outputs, using the
same exclusive identity-bound publication and cleanup as other outputs, before
completion.json. Preserve the existing signed metadata formats and bundle member
layout. The signature remains validated against independently supplied public
trust at each later privilege boundary; no public key is accepted from prepared
inputs. Update exact-output/native preservation checks for these two metadata
files, including their original bytes and bounds. A new install verifier then
reimports metadata and each expected member into protected private storage,
verifies the signature/expiry/repository/version/platform and every member hash,
and only afterward exposes a typed immutable input snapshot to transaction code.
No prepared helper or script is executed merely because completion.json exists.

Root adjudicates canonical native layout, no restart for coherent identical
rerun, data/config/trust/account preservation on uninstall, refusal of active
Linux game removal, and explicit offline schema-changing maintenance as ordinary
implementation choices within the approved design. Startup fencing and receipt
CAS remain required; concrete journal/bridge structs must be frozen separately
before parallel adapter implementation. This review does not authorize bypassing
any outstanding native updater defect, changing production trust or publication.
