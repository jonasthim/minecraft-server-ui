# Task 30 Linux asset primitives — worker report

## Scope delivered

Implemented the fixed Linux asset table and filesystem primitives in `internal/install/assets_linux.go`, with deterministic package regressions in `internal/install/assets_linux_test.go`.

The implementation uses only the fourteen fixed role/path/member/mode records. Trust files have no prepared archive member. Inspection is read only, checks protected directory ancestry and exact target mode/owner/type/link count/size, hashes through a held descriptor, and retains the original file and directory identities. Snapshot checks reject path, parent, inode, metadata, or content changes.

Preimage capture is exclusive, bounded, hash checked, mode `0600`, and syncs the file and containing directories. Publication stages to an exclusive fixed-name file, verifies the source digest and size, syncs it, then publishes with Linux `renameat2` semantics. Existing captured targets use exchange and identity/content verification; initially absent targets use no-replace publication. Removal pins the target under a fixed quarantine name, verifies the captured identity and state, then removes only that file. Post-publication errors return the state observed when it can be read and report an uncertain outcome.

Tests map fixed paths beneath a private `t.TempDir` root through an unexported in-process seam. No test writes to host asset paths, and no environment or command-line override was added.

## Evidence

- `go test ./internal/install` — passed.
- `go test -race ./internal/install` — passed.
- `git diff --check -- internal/install/assets_linux.go internal/install/assets_linux_test.go` — passed.

Regression coverage includes fixed role/path contracts and copied specs, read-only inspection, identity capture, exclusive preimage capture, verified publication, source digest mismatch, before/after rename failures, foreign pending files, byte-identical inode replacement, symlink and hardlink refusal, read-only missing-parent inspection, parent provisioning with explicit reinspection, cancellation, repeated removal, and replacement preservation.

## Limits and integration status

This packet supplies filesystem primitives only. It does not provide privileged updater locking, parent-directory provisioning, trust/config validation, manager/service/game evidence, transaction recovery, CLI wiring, or an independent executor. The root integrator must review these primitives in the composed driver and update the implementation ledger before claiming the package complete. No files outside the assigned ownership were edited, and nothing was staged or committed.
