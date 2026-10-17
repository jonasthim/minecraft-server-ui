# Task 30 Linux maintenance-control bridge

`LinuxInstaller.PrepareMaintenanceControl(ctx, snapshot)` is the bridge for a terminal updater operation that still owns manager control files when installer maintenance begins. The caller keeps the existing updater lock. The method does not require the manager to be stopped.

For a terminal snapshot, it revalidates the captured activation journal, privileged updater request, scratch images, installed receipt inode and bytes, executable hash, protected state and binary roots, manager control root, and exact captured control set. It removes only captured `request.json`, `drained.json`, and `challenge.json` entries under the manager data control directory. It fences the directory and checks that ordinary updater state and the directory binding remain stable. It then returns a fresh strict terminal snapshot for the caller to reconcile after stopping the manager and publishing its own maintenance proof.

A nonterminal clean snapshot and a terminal snapshot with no captured manager controls are returned unchanged without filesystem mutation. A replacement, pending entry, foreign control entry, changed root, or changed ordinary updater state is refused before deletion. If a cleanup is interrupted partway, the activation journal remains and a fresh snapshot can safely validate the remaining exact controls and retry.

## Verification

Passed:

- `go test ./internal/selfupdate`
- `go test -race ./internal/selfupdate`
- `go vet ./internal/selfupdate`

The temporary-fixture tests cover an active service state, exact control cleanup, retained privileged updater files and executable/scratch inode identities, fresh snapshot reconciliation, foreign and pending controls, same-bytes replacement, journal replacement, partial-cleanup retry, a concurrent foreign entry, and nonterminal no-op behavior. These tests do not execute systemd or qualify a production Linux host.
