# Task 30 Linux native installer driver

## Delivered

Implemented the native Linux driver in `internal/install/native_*.go`. Preparation authenticates an exact candidate against independently supplied bootstrap trust, imports protected members, captures fixed deployment preimages, and records immutable intent and native effect evidence. The child reopens and reauthenticates recorded inputs only when apply quiescence needs them; rollback and other recovery do not depend on still-valid prepared metadata.

The driver provisions only the fixed service account and private data layout. New account home creation is suppressed in `useradd`; the driver creates the fixed home beneath a pinned parent and sets ownership through opened directory handles. Existing directory ownership and modes are checked. Nested manager-owned directories are created relative to held roots after acquiring the stopped-manager data lock. A fresh apply writes its operation challenge before candidate start. Active uninstall checks managed games before stopping the manager and rechecks during removal.

Asset writes and rollback publish through durable staged-identity effect records. Rollback restores captured content while recording each newly published inode, then rebuilds the prior inventory from the actual restored snapshots. Superseded updater receipts can therefore restore their captured manager image even when the older inventory has stale manager metadata. Receipt changes use the reviewed compare-and-swap and first-install receipt-removal bridges.

The optional administrator-controlled update trust file is validated as current policy and preserved transactionally. Its content and inode may legitimately change after installation, so ordinary inventory checks do not treat its drift as deployment corruption. Bootstrap trust remains bound to authenticated immutable intent. A failed first install records a distinct `retained` state only after account, data, deployment assets, receipt absence, and original operation evidence are verified; it does not claim the attempted database schema was initialized.

The read-only check uses `selfupdate.InspectLinuxInstallerState` and verifies the fixed root binding before and after inspection. It does not acquire the updater lock, create installer state, or open the manager database. Coherent unchanged apply requires the complete desired authenticated asset plan to match current content and a bounded `/healthz` response in addition to the protected running image check.

Terminal updater cleanup is restart-safe. Quiesce prepares captured terminal controls before publishing the installer challenge, reconciles them after the manager stops, and persists an `UpdaterReconciled` effect marker before native asset replacement. Rollback takes a fresh updater snapshot only when that marker is absent and all captured asset, inventory, and effect preimages prove replacement has not started. Explicit recovery promotes only validated typed pending evidence, inventory, and terminal-result publications; ordinary load remains read-only. Read-only installation check reports an updater-superseded manager image as coherent only when a strict terminal snapshot accounts for the receipt advance and every other owned deployment asset still matches inventory.

Rollback requires recorded staged identity before replacing or deleting an asset. A same-content replacement inode with no native effect record is preserved and returned as a manual-repair error. Retained trust files do not count as deployment assets when admitting a repeated uninstall.

## Verification

Network-free fixtures cover strict maintenance proof handling and paired cleanup refusal, delayed rollback challenge cleanup ownership, data-directory no-escape provisioning, changed updater content defeating the unchanged shortcut, exact staged effect round trips and pending-marker recovery, prior-image restore with a fresh inode, preservation of unrecorded same-target replacements, terminal manager-only supersession validation, retained inventory relations, and a fresh `Prepare → Record → Close → Load → Quiesce → Replace` composition. The composition fixture asserts the parent's pinned inputs are closed before recorded-input reopening and that reopening precedes account, service, or data-lock effects. The recorded-input authentication primitive separately covers signed import/reopen, tampering, replacement, and read-only behavior.

These fixtures do not establish native Linux acceptance. No local service account, manager service, firewall, game process, or external service was used. Real `useradd`, `runuser`, systemd behavior, a signed release bundle, active updater supersession, and readiness against a disposable Ubuntu host remain for the integrator's hosted qualification workflow.

The hosted CI inode assertions were made deterministic by retaining an open descriptor to the original executor/preimage while replacing the path, then checking the replacement is a distinct file before exercising launch rejection or rollback restoration. This prevents filesystem inode-number reuse from invalidating either fixture. Both focused tests passed 30 consecutive runs; the install package test, race test, and vet checks passed locally.

## Integration dependencies

Active-manager quiescence uses the exact challenge/drain proof and the narrow maintenance updater reconciliation bridge. Launch permit grant/revoke and startup checks are owned by `launch_linux.go`; the native driver calls those APIs for verification and rollback. Linux CLI, executor dispatch, qualification, workflow, and ledger integration remain outside this package report. Do not mark Task 30 accepted until the integrator reviews the code and the approved Ubuntu workflow passes.
