# Review: Task 30 Linux native driver and model

## Finding

### P1 — rollback after a worker interruption can leave captured updater state unreconciled

`internal/install/native_flow_linux.go:33-37, 134-148` captures the ordinary updater terminal snapshot in memory and reconciles it only at the end of `Quiesce`. A concrete interruption is:

1. The protected updater state contains a valid terminal activation journal and its captured scratch image(s).
2. The installer records `phaseQuiescing`; `Quiesce` captures that state, writes its own maintenance challenge, stops the manager, and acquires the data lock.
3. The worker exits before `ReconcileUpdaterForMaintenance` completes.
4. A fresh `install recover-worker` follows `recoverWith` → `recoverRollback` → `Rollback` (`internal/install/coordinator.go:206-246`). `Rollback` stops/locks the manager, restores the install transaction, and can restart the prior image, but never reconciles the captured updater snapshot. The snapshot was process-local, and a fresh `SnapshotUpdater` cannot reconstruct it while the installer-owned maintenance control files are present.

This can complete installer rollback while leaving the terminal updater journal and scratch files behind, bypassing the exact terminal reconciliation that normal quiescence requires. Recovery needs durable exact proof for that captured terminal state, or a narrow recovery snapshot/reconciliation path that binds the installer challenge to the loaded intent and validates the terminal updater objects before cleanup. Preserve rejection of foreign control entries. Add a fresh-process test that interrupts after challenge publication and confirms recovery reconciles the captured terminal files before restarting or retiring the installer journal.

### P1 — protected inventory/result pending files have no retry reconciliation

`writeLinuxProtectedState` in `internal/install/native_flow_linux.go:480-549` refuses any existing `<name>.pending` file and has no ownership/content-bound recovery path. It is used for both `installation.json` and `result.json`. A process kill after writing or syncing the pending file but before rename makes a subsequent writer fail permanently. In a fresh-install rollback this can prevent publishing the retained failure inventory; during an upgrade rollback `restorePriorInventory` can observe that the old inventory is already correct and return while leaving `installation.json.pending` behind. A `result.json.pending` file similarly prevents `Finish` from retrying, so the terminal journal cannot retire. `checkLinuxInstallation` does not inspect these pending names and can report a coherent layout despite the residue.

Recover only a pending file whose protected metadata and exact expected bytes match the operation being retried, while the live destination still matches the captured CAS preimage (or captured absence). Keep foreign, replaced, malformed, or ambiguous pending files intact and report recovery required. Cover killed-writer retries for both destinations and ensure read-only check classifies pending files.

## Reviewed areas

- Intent and inventory validation bind image, account, asset role/order, preimage identities, updater receipt supersession, and retained failed-install provenance.
- Recorded inputs are reopened from the protected operation tree and reauthenticated before quiescence; the parent-to-worker composition test confirms the parent inputs are closed first.
- The unchanged fast path now compares all planned asset contents. Rollback inventory reconstruction now uses the captured current manager preimage when an ordinary terminal updater had superseded the old inventory.
- Nested account-owned data directories are provisioned through retained roots after manager stop and data-lock acquisition.
- Receipt publication/removal, asset rollback, service policy, game-stop fences, readiness, and read-only check behavior were reviewed. No additional concrete defect was confirmed in those paths.

## Verification and limits

Passed:

- `go test ./internal/install ./internal/installinputs`
- `go test -race ./internal/install ./internal/installinputs`
- `go vet ./internal/install ./internal/installinputs`

`TestFreshNativePrepareCloseLoadQuiesceReplaceComposition` exercises the fresh install boundary through replacement using temporary fixtures. This review did not execute systemd, `useradd`/`runuser`, a signed production release bundle, or a disposable Ubuntu host. It does not establish native Linux acceptance.

## Integrator closure

Both findings are closed in the current source. The installer now records
successful updater reconciliation before asset effects, and explicit recovery
reconciles a captured terminal updater before rollback when that marker is
absent. Typed pending publication recovery validates the immutable operation,
current preimage, native identities and effect evidence before promotion or
exact removal. Unknown and partial files remain untouched.

The independent follow-up review in
[`review-task30-linux-native-recovery.md`](review-task30-linux-native-recovery.md)
approved those changes and their focused recovery fixtures. The integrator's
combined package race checks and vet passed after the final source changes.

**Current verdict: approve the reviewed source; hosted Ubuntu lifecycle
qualification remains outstanding.** The original findings above are retained
as review history.
