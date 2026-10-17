# Task30 read-only Linux updater inspection bridge

## Added API

`selfupdate.InspectLinuxInstallerState(ctx)` returns the installed receipt, whether it is present, whether updater state is pending or ambiguous, and an error. It checks root authority before opening canonical roots, constructs a private read-only `LinuxInstaller` session, and reuses `SnapshotUpdater` for strict receipt, terminal journal, scratch image, and control-state validation. The session never acquires or creates a lock, creates a directory, opens a database, or starts a service. All snapshots, descriptors, and roots are closed before return.

For an absent state root, inspection checks the canonical manager directory for an orphan manager image or updater scratch and checks the manager control directory for unexpected entries, then rechecks that updater state stayed absent. Any uncertainty returns pending or an error instead of claiming absence.

## Verification

Temporary-tree tests snapshot directory names, modes, and file contents before and after absent, installed, terminal healthy, nonterminal activation, and orphan-control cases. The unauthorized case also verifies the root opener was not called. No production paths or host services are used.

Commands run:

- `go test ./internal/selfupdate -run TestInspectLinuxInstallerState -count=1` — passed.
- `go test ./internal/selfupdate -count=1` — passed.
- `go vet ./internal/selfupdate` — passed.
- `go test ./internal/install -run 'TestLinuxInstallerUpdaterLockRejectsCanonicalIdentityReplacementDuringProcRead|TestLinuxInstallerLaunchLockRequiresActualHeldKernelFlock' -count=1` — passed.
- The same focused launch tests with `-race` — passed.
- `go vet ./internal/install` — passed.
- `go test ./internal/install -count=1` — passed after the concurrent native-effect fixture was corrected.

`go test -race ./internal/selfupdate -count=1` and `go test -race ./internal/install -count=1` both passed after the last edits.

No native Ubuntu service qualification or host modification was performed.
