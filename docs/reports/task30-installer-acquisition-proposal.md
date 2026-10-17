# Task30 installer acquisition and updater reconciliation proposal

Status: source-grounded interface proposal for root contract freeze. No
production code or existing contracts were changed. This is a prerequisite
design, not Task30 implementation or native qualification.

## Recommendation

Keep `selfupdate.AcquireMaintenance` strict as frozen by Task30L1B. Add a
separate installer acquisition entry point that understands first install,
managed upgrade, retained-state reinstall, repair, and uninstall. It must
create only missing canonical coordination roots with native protected
permissions, acquire the existing updater operation lock, and then revalidate
root identities. It must not create a second operation lock or accept
caller-selected output paths.

The new session should expose a read-only classification of updater state.
It may reconcile a *strictly validated terminal* manager-update journal only
after the manager is stopped and the real manager data lock is held. A
nonterminal, malformed, pending, or ambiguous updater state remains a refusal
and must be handled by the existing updater recovery path before a fresh
installer acquisition. Do not call updater recovery while holding the updater
operation lock: both `recoverLinuxAt` and Windows updater recovery acquire that
same lock internally.

## Source facts that shape the boundary

- `platformPaths()` fixes Linux roots to `/var/lib/minecraft-ui`,
  `/var/lib/minecraft-ui-update`, `/usr/local/bin`,
  `/usr/local/lib/minecraft-ui/selfupdate`, and
  `/etc/minecraft-ui/update-trust.json`; Windows uses the corresponding fixed
  `C:\ProgramData\MinecraftUI*` and `C:\Program Files\MinecraftUI` roots.
  These paths must remain internal constants, never CLI destinations.
- `AcquireMaintenance` checks administrator authority before path I/O, opens
  existing protected state and binary roots, requires the canonical manager
  executable, rechecks root bindings, then takes `launcher.AcquireManager` on
  the updater state directory. Its conflict check refuses `activation.json`
  regardless of phase, along with pending/request/launch state. This is the
  correct receipt-only behavior but makes it unusable for first install or a
  data-preserving uninstall/reinstall where the manager executable is absent.
- The updater lock is `manager.lock` under protected updater state. The
  separate manager data lock is acquired only after manager stop. Preserve the
  order: updater operation lock → manager stop and drain → manager data lock.
  Release the data lock before an authorized manager/probe child start, while
  retaining the updater lock and startup fence; reacquire and revalidate before
  more file effects.
- `activationWorker.recover` handles nonterminal phases, including restoring a
  prior executable; it is not installer reconciliation. For terminal phases
  it returns without removing the journal. `cleanupTerminal` removes fixed
  scratch/request/journal paths after checking the current binary hash against
  `installed.json`, but does not bind each cleanup object to a captured
  identity or verify the journal's phase-to-receipt image relation. Do not
  expose it directly as the installer cleanup primitive.
- Linux recovery runs through `recoverLinuxAt`; Windows updater startup runs
  `windowsUpdater.recover`. Both serialize through the same updater lock.
  Normal update activation later invokes `cleanupTerminal`, which explains why
  completed terminal journals can remain for the installer to encounter.
- `candidate.VerifyBootstrap` authenticates the exact signed metadata only
  against independently configured public keys. `candidate.ParsePublicTrust`
  explicitly does not make a supplied trust file a production trust anchor.
  `installinputs.Import` correctly reauthenticates the prepared metadata and
  members into a private transaction child, but the trust passed to it must
  already be rooted independently.
- `internal/install` currently has only the deterministic coordinator and a
  private `driver`; `Prepare` is the no-service/no-asset-effect preflight and
  captures immutable intent. `Quiesce` is the first operation phase where the
  manager can be stopped and the terminal updater journal can safely be
  cleaned. `Publish` must use the maintenance receipt CAS rather than the
  updater's unconditional `recordInstalled` helper.

## Proposed bridge types

Keep the existing `Maintenance` type and receipt methods for installed-image
CAS. Add a separate acquisition mode and an opaque updater-state snapshot:

```go
type InstallerMode string

const (
    InstallerFreshInstall InstallerMode = "fresh_install"
    InstallerUpgrade      InstallerMode = "upgrade"
    InstallerReinstall    InstallerMode = "reinstall"
    InstallerRepair       InstallerMode = "repair"
    InstallerUninstall    InstallerMode = "uninstall"
)

type InstallationState string

const (
    StateFresh       InstallationState = "fresh"
    StateInstalled   InstallationState = "installed"
    StateUninstalled InstallationState = "uninstalled"
    StateAdoption    InstallationState = "adoption_required"
)

type UpdaterState string

const (
    UpdaterAbsent       UpdaterState = "absent"
    UpdaterTerminal     UpdaterState = "terminal_pending_cleanup"
    UpdaterNeedsRecovery UpdaterState = "recovery_required"
)

type InstallerSnapshot struct {
    State   InstallationState
    Receipt *MaintenanceReceipt // opaque snapshot; nil only when verified absent
    Updater UpdaterState
    // private inventory/journal/activation digests and retained handles
}

func AcquireInstallerMaintenance(ctx context.Context, mode InstallerMode) (*Maintenance, error)
func (m *Maintenance) InstallerSnapshot(ctx context.Context, mode InstallerMode) (InstallerSnapshot, error)
func (m *Maintenance) ReconcileTerminalUpdater(ctx context.Context, snapshot InstallerSnapshot) error
```

Names are proposed for root freeze. `InstallerSnapshot` must be session-bound
internally; the public scalar fields are descriptive only and cannot authorize
effects. The private captured state binds exact file handles/identities and
bytes/digests. `ReconcileTerminalUpdater` accepts a snapshot from the same live
session, revalidates it under the still-held updater lock, and does nothing for
`UpdaterAbsent`. It is called by the native driver in `Quiesce`, after it has
stopped the manager and acquired the actual data lock. It never invokes a
manager-update actor probe, invents `ActorID`, or changes the activation wire
format.

The installer core continues to own trust import, inventory and journal
schemas. `AcquireInstallerMaintenance` owns only platform authority, canonical
root creation/validation, the existing updater lock, root/file identity pins,
installed receipt snapshot and updater-state classification. It returns the
existing opaque `MaintenanceReceipt` or captured absence; `CompareAndSwapInstalled`
remains valid only after the transaction has placed the new canonical manager
binary and independently verifies its hash.

## Acquisition and state rules

Acquisition checks actual OS administrator authority before path I/O. It checks
every fixed ancestor for ordinary directory type, ownership/ACL and no symlink
or reparse point. For fresh install only, it may create missing fixed roots
exclusively under already-verified parents using the production root ownership
and ACL policy. The new directories are opened and pinned, then all path
bindings are rechecked after taking the existing updater lock. No arbitrary
parent, root, executable name or service name comes from an option.

The updater state root and lock are coordination metadata; their exclusive
creation is the only pre-journal filesystem setup allowed. A crash that leaves
an otherwise empty protected state root/lock is recognizable as fresh only
when every other fixed state slot is absent. Never delete a lock path. On
Linux, ensure first-install setup does not leave `data/manager.lock` root-owned
mode 0600 and unusable by the later `minecraft-ui` service: after the service
account/data root exists, establish the canonical lock file with that account's
required access while root holds the descriptor/operation boundary. On Windows,
create the data lock beneath the exact service-SID DACL and verify inheritance.
In either case, first install must use the same `launcher.AcquireManager` lock
semantics that the manager and manager-state commands use after installation.

After acquisition, classify only these supported states:

| Mode | Required state | Refusal conditions |
| --- | --- | --- |
| Fresh install | No inventory, installed receipt, canonical service, manager binary, prior account record, or unknown app-owned asset. Coordination roots may be absent or contain only the known operation lock. | Any preexisting DB/data tree or untracked/partial install goes to explicit adoption/repair review; missing receipt alone does not prove a clean host. |
| Upgrade | Valid installed inventory, protected `installed.json`, canonical manager binary matching that receipt, exact service/account identity, and coherent allowlisted helper/native state. | Nonterminal updater state, unknown service overrides, unsafe ownership/ACL, different manager identity, wrong schema, or modified trust. |
| Reinstall | Protected inventory state is `uninstalled`; retained native receipt, trust and account are valid; manager/service binaries are absent exactly where the inventory says they were removed. | Uninventoryed absence, changed account/SID, or foreign executable/service. Reinstallation preserves retained DB/config/instances and does not recreate admin state. |
| Repair | Managed installed state with a narrowly identified missing/changed allowlisted asset and all roots/identity/trust still safe. | Unknown or unsafe asset, changed identity, pending transaction, or any repair that would imply adopting arbitrary host state. |
| Uninstall | Valid installed inventory and current receipt/image; no ambiguous native transaction. | Linux refuses while any owned game unit is active or process identity is uncertain. It removes only inventoried app registrations/assets and retains data, trust, service account, installed receipt and an `uninstalled` inventory for reinstall. |

An existing installation with no inventory is `StateAdoption`, never fresh. A
separate bounded adoption operation can later validate exact canonical service
bindings, real service principal, protected ancestry/assets, receipt/image and
absence of pending work before writing its first inventory. Do not silently
fold adoption into fresh install or `--repair`.

For an existing managed installation, the receipt describes the current manager
image, not the bootstrap-era manager hash in inventory: ordinary self-update
may have changed it. Validate manager receipt against the actual canonical
binary and activation terminal state. Inventory continues to bind updater,
unit and other shipped assets, service identity/configuration and the signed
bootstrap identity. Windows probe image comparison must account for the
quiescent updater generation rather than a stale bootstrap hash.

## Terminal updater reconciliation

The lifecycle `Prepare` phase may capture a terminal journal but must not remove
it. The immutable installer intent records its exact journal digest, native
identity, terminal phase and expected image. In `Quiesce`, after manager stop
and data-lock acquisition, reconciliation reopens and compares that exact
snapshot before deletion. No paths are deleted based only on a phase string.

Accept only strict format-1 activation journals whose phase is exactly
`healthy`, `rolled_back`, or `aborted`. Require stable `Prior`/`Candidate`
versions, equal schema, valid digests, valid exact request/nonce fields, and
strict JSON with no unknown, duplicate or trailing fields. Bind the current
`installed.json` and actual manager executable to `Candidate` for `healthy`,
and to `Prior` for `rolled_back` or `aborted`. Validate every present fixed
`.next`/`.previous` scratch file against its allowed prior/candidate digest,
regular-file type, protected owner/DACL, single link and captured identity.
Inspect any present imported request and control challenge/drain/request only
as cleanup candidates when their strict contents match the exact journal's
StageID, nonce, actor value and prior/candidate tuple; matching `ActorID` is
comparison data, never local authorization. Any other fixed control/pending
file, changed identity, mismatching bytes, or failed fence leaves state intact
and blocks installation. Delete only exact validated objects and fence each
affected root before proceeding.

Refuse, without mutation, on `prepared`, `stopped`, `replacing`, `candidate`,
`rollback`, unknown/malformed phases, `activation.pending`, unrecognized
request/control state, or ambiguous executable/receipt identity. Those states
belong to normal updater recovery or explicit manual repair. Do not release and
reacquire the operation lock to call recovery mid-transaction; if root chooses
that handoff later, it must become a distinct freeze with process/lock
revalidation across the release window.

This clean terminal reconciliation is a narrow maintenance-state prerequisite;
it is not a generic `activationWorker.cleanupTerminal` call because that helper
does not give the installer the required captured cleanup identity and
phase-to-image binding. The updater's accepted journal and actor semantics stay
unchanged.

## Trust and authenticated inputs

`ApplyOptions.TrustFile` plus a successful `ParsePublicTrust` call is
insufficient by itself: the caller could supply a new trust file and a bundle
signed by its matching key. On first install, the bootstrap trust must be
anchored outside the prepared directory and outside arbitrary CLI arguments.
Recommended source is the release-specific authenticated outer installer,
which pins the exact trust digest and repository (or embeds the public key
set); the installer helper reopens the selected trust file, compares it to
that pinned identity, then passes the cloned trust to
`installinputs.Import`. The outer script's exact bytes remain authenticated
through its independently pinned release-specific digest.

For managed upgrades and repairs, require the incoming trust identity to match
the protected canonical installed trust exactly. Trust rotation is a separate
reviewed operation, not an implicit side effect of reinstall. Keep bootstrap
publisher trust distinct from the native self-update trust file if their
lifetimes differ. Do not use `update-trust.example.json` as trust; the current
Linux installer copies it as a placeholder and the existing updater then has
no configured trust. `DEVELOPMENT ONLY` inputs require explicit development
mode and must never be persisted as production trust. No public key or trust
bytes from `completion.json` are authoritative.

After validating the trust anchor, import original `bootstrap.json`,
`bootstrap.sig`, `completion.json`, and exact platform members with
`installinputs.Import` into `state/install/<operation-id>/inputs`. Keep its
handles through snapshotting/preimage capture; only the authenticated imported
copy is eligible for later native probing or publication. Do not execute any
prepared installer script or helper before import completes. Candidate schema
must equal the existing manager schema for normal apply; schema changes use
the separate offline migration/restore protocol and matching image hold.

## Durable inventory and journal storage

Use only the protected canonical updater state root:

- `installation.json`: strict format-1 durable inventory, fixed destination
  roles (never arbitrary serialized target paths), platform and canonical
  service identity, installed/uninstalled state, exact signed bootstrap
  manifest/trust identity, manager/updater/helper member hashes and modes,
  service startup/environment identity, Linux service UID/GID or Windows
  manager SID, and preserved-versus-owned asset classification. No
  credentials, private signing keys, process output or raw environment values.
- `installation-transaction.json`: the single active transaction CAS record
   already described by `internal/install/contracts.go`: format, random
  operation ID, mode, finite phase, immutable intent SHA256, prior/target
  manager image, and fixed failure stage. Unknown/duplicate fields or an
  inconsistent prior/target relation refuse all mutation.
- `install/<64-lowercase-hex-operation-id>/intent.json`: immutable strict
  intent bound to the journal digest. Store the exact prior/target inventory
  digests, trust/bootstrap identity, file identity/hash/size/mode preimages,
  absent-versus-present slots, native service/account/startup preimages and
  per-effect state required for rollback. Use fixed role enums and derive
  destinations from `platformPaths`; do not store caller-selected paths.
  Any environment bytes required for exact rollback stay in a private
  transaction preimage, not logs, results or the public journal. SCM
  credentials/passwords are never captured or serialized.
- `install/<id>/inputs/` is owned by `installinputs.Import`. Separate
  `preimage/`, `stage/`, and independent executor image only if the frozen
  intent contract needs them; each gets an exact set manifest, retained native
  identities, private ACL/mode and bounded bytes. Do not add a second lock.

Publish intent and journal through exclusive private pending files, retained
creation handles, exact CAS against the prior file, same-root atomic rename and
file plus parent durability fences. Linux requires syncing each changed
directory level. Windows uses the established fixed-local-C: volume fence,
not directory `Sync`. A fence failure after namespace change is an uncertain
pending transaction, never rollback-by-guess. Keep the coordinator journal
independent of `activation.json`, `installed.json`, `account.json` and manager
control files. `CompareAndSwapInstalled` publishes only after the new manager
binary has been placed and verified; it must be journaled with inventory
publication so recovery can prove which receipt won. Do not remove preimages
or mark terminal until receipts, native registrations, final inventory and
all fences have been re-read and validated.

## Suggested packet sequence and acceptance

1. **L1C acquisition bridge**: add the separate typed acquisition mode, safe
   missing-root creation for fresh/reinstall, state classification, strict
   terminal updater snapshot and data-lock-gated terminal reconciliation. Keep
   `AcquireMaintenance` unchanged. Test Linux/Windows policies via private
   injected roots; native hosted tests prove Windows DACL/owner and lock file
   access. Cover absent, managed-installed, retained-uninstalled, adoption,
   missing binary, invalid roots, parent substitution, concurrent operation
   lock and cancellation cleanup.
2. **L1D install storage/core**: freeze exact inventory, immutable intent and
   CAS publication structs in `internal/install` (root-owned `contracts.go`).
   Reauthenticate and pin the external trust anchor; import with
   `installinputs.Import`; test fresh creation, identical no-op, managed
   upgrade, retained reinstall, repair refusal and data-preserving uninstall
   against a deterministic adapter. Fault every before/after publication
   boundary and prove foreign replacements/preimages survive.
3. **Updater reconciliation regression**: create real temporary signed-format
   activation records in each terminal phase with matching/mismatching
   receipts, manager binaries and scratch; ensure only exact terminal state is
   cleaned after manager/data-lock proof. Every nonterminal/malformed/pending
   state must remain byte-identical and block apply. Include a negative control
   that removes the image-to-phase comparison and demonstrates test failure.
4. **Native adapter qualification**: run actual fresh install, repeated
   install, upgrade, failed rollback, explicit recovery, uninstall and retained
   reinstall on disposable Ubuntu 22.04/24.04 and Windows Server 2022, with
   exact manager identity, no game-service changes, preserved data/config,
   setup/readiness and cleanup assertions. Cross-compilation is not native
   evidence.

Do not start packet 2 adapters until root freezes the L1C/root authority,
lock/data-lock, trust anchor, inventory and journal contracts. This proposal
does not supply production signing keys or authorize trust rotation, legacy
adoption, database/world restoration, actor fabrication, service mutation by
the manager, or installer execution from the prepared directory.
