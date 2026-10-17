# Task 30 Linux asset effect recovery — worker report

## Scope delivered

Extended the accepted fixed asset primitives with `publishLinuxAssetRecorded` and `reconcileLinuxAssetEffect` in [assets_linux.go](/home/thim/Repositories/minecraft-server-ui/internal/install/assets_linux.go) and [assets_recovery_linux.go](/home/thim/Repositories/minecraft-server-ui/internal/install/assets_recovery_linux.go).

Recorded publication calls the supplied durable effect recorder once after staging, hashing, syncing, and validating the captured target. The recorder receives the exact staged state, including device and inode, without a destination path. The helper rechecks the public target, parent binding, staged identity, mode, size, and hash after the callback and before rename. The original publish API remains a wrapper without a recorder. Recorded publication rejects a nil recorder.

Reconciliation validates the role, operation ID, before state, and staged state. It recognizes only the fixed pending or quarantine name for that operation and compares full file state, including inode identity. It removes only a recognized scratch file after rechecking held identities, syncs the parent, and verifies that the public binding stayed unchanged. Ambiguous states, foreign scratch files, missing required parents, and cancellation fail closed. Unrelated scratch siblings are left untouched.

## Evidence

- `go test internal/install/assets_linux.go internal/install/assets_recovery_linux.go internal/install/assets_linux_test.go internal/install/assets_recovery_linux_test.go` — passed.
- `go test -race` with the same asset source list — passed.
- `go vet` with the same asset source list — passed.
- `git diff --check` on the edited tracked asset files — passed.

The full package command currently encounters concurrent incomplete native/launch edits in the shared workspace, so these checks compile and exercise the assigned asset files directly.

Regressions cover callback ordering, exact staged identity, callback failure, target and stage mutation during callback, existing post-rename uncertainty, publication pending/exchanged/first-install combinations, completed first-install and replacement states with no pending file, absent-side ambiguity, foreign same-bytes inode preservation, unrelated scratch preservation, removal quarantine/public/already-removed combinations, ambiguous quarantine preservation, cancellation, and absent no-effect recovery with an unprovisioned parent.

## Integration status

This packet provides the asset effect bridge and scratch reconciliation only. The native driver must validate the immutable intent, effect log, preimage, updater lock, data lock, and stopped-manager evidence before calling reconciliation. No log implementation, native qualification, host asset writes, other files, commits, or staging were performed.
