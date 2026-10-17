# Task30 Linux installer launch permit report

## Implemented

Added `internal/install/launch_linux.go` with the three frozen functions:

- `grantLinuxInstallerLaunch` publishes a permit only for the current durable transaction in `verifying` or `rolling_back`, after checking the bound intent, installed manager hash and mode, protected executor hash/mode/inode, current worker PID and start ticks, and the actual updater `manager.lock` owner in `/proc/locks`.
- `revokeLinuxInstallerLaunch` removes only an exact permit for the same operation and executor after rechecking the current journal, caller identity, lock owner, permit inode and bytes. Missing permits are idempotent when the transaction and caller proofs are valid.
- `CheckLinuxInstallerStartup` is read-only. It returns `false, nil` for genuinely absent installer state, `true, nil` only for a permit bound to a live exact worker that still owns the kernel lock, and the fixed recovery-required error for incomplete, stale, orphaned, or ambiguous installer state.

Paths and filenames are fixed in the implementation. The only path, process, authority, and publication overrides are private context fixtures used by tests. The package creates no state directories or locks from startup inspection.

Added `internal/install/launch_linux_test.go` for absent-state read-only behavior, authority-before-I/O, strict permit decoding, recovery blocking for a journal without a permit and for unknown pending state, and a full grant/read/revoke flow. The flow uses a real temporary `flock` and private hooks that pin the test PID/start token to the fixture executor inode. Negative cases cover wrong phase/image, replacement by equal executor bytes on a new inode, changed process start token, wrong lock owner, released lock, and both prepublication cleanup and postpublication uncertainty.

## Verification

Commands run with Go `/home/thim/.local/go/bin/go`:

- Isolated source-list test covering installer contracts, coordinator, Linux model/store/assets, launch implementation, and relevant test files — passed after the final launch changes.
- The same isolated source list with `-race` — passed.
- `go vet` on the same isolated source list — passed.
- `go test ./internal/selfupdate ./cmd/minecraft-ui -count=1` — passed before the final launch hardening changes.

The standard `go test ./internal/install -count=1` passed once during the work, then became blocked by concurrent edits in `native_flow_linux.go` (`io`, `bytes`, and `linuxMaxInventoryBytes` are undefined around lines 675 and 741). The isolated source-list run excludes those incomplete shared native files and verifies this package's launch behavior.

## Native acceptance limits

These tests use private temporary roots and the local process lock. They do not qualify a root-owned installation under `/var/lib/minecraft-ui-update`, a real manager/executor replacement, systemd startup dependency execution, or recovery during a live native install. No installed paths, manager process, game process, or systemd service were changed.

The frozen contract requires the protected executor to be mode `0755`. At implementation time, `native_linux.go` creates a new executor with mode `0700` and accepts only `0755` for an existing copy. The launch validator follows the frozen `0755` requirement; the native staging path must be aligned before integration acceptance.

## Lock identity review follow-up

`launchLinuxOwnsUpdaterLock` now opens and pins `manager.lock` through the held state root, validates the opened file and canonical entry identity before and after reading `/proc/locks`, and confirms the canonical state-root path still names the held root. Kernel ownership is matched against the pinned file's device/inode. Added deterministic temporary-fixture regressions that replace the lock pathname with a new inode or replace the state-root pathname while `/proc/locks` is being read.

Verification is pending because the current assembled `internal/install` package does not parse: concurrent changes in `model_linux.go` report syntax errors starting near line 253. No host services or files were touched.
