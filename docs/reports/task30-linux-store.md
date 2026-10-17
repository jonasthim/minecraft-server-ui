# Task30 Linux installer durable store

Implemented the Linux-only durable transaction store in `internal/install/store_linux.go` with deterministic tests in `internal/install/store_linux_test.go`.

The store borrows the caller's state root and validates root ownership and permissions, binds reads to file and directory identities, rejects symlinks and multiply-linked files, strictly decodes bounded canonical journal JSON, and verifies the immutable intent digest. `Record` uses an exclusive pending slot, file and directory syncs, a compare-and-swap against the exact previously loaded bytes and identity, and a final publication check. A post-rename error marks the store uncertain and blocks further mutation. Intent creation is exclusive, bounded, private, and leaves created directories in place when later steps fail.

The private test seam supports temporary fixture roots and deterministic pre- and post-publication failures. Tests cover absent versus malformed journals, all transaction phases, immutable fields, restart loading, stale same-byte replacement, foreign pending and competing journal preservation, intent absence/truncation/digest/link failures, cancellation, uncertain publication, and close behavior.

Round one review found that a known pre-rename failure could leave this call's pending marker and block later loads. `Record` now makes a best-effort cleanup with a fresh three-second context detached from caller cancellation. It rechecks the root and pending inode, owner, and link count before removal, then syncs the parent. When the full expected journal was written, cleanup also verifies its bytes. A replaced or otherwise foreign pending entry is preserved. Successful rename marks publication before any subsequent fallible work, so uncertain post-rename outcomes retain the live journal and are never cleaned up.

## Verification

The store source-list test and race run passed after the review fix:

```text
PATH=/home/thim/.local/go/bin:$PATH go test -race -overlay=/tmp/task30-store-overlay/overlay.json internal/install/contracts.go internal/install/coordinator.go internal/install/store_linux.go internal/install/store_linux_test.go internal/install/coordinator_test.go -count=1
ok command-line-arguments 1.065s
```

Scoped `go vet` passed with the same source list and overlay. The overlay substitutes an empty package for the concurrently edited untracked `internal/selfupdate/installer_linux.go`; the selected source list excludes other in-progress package tests. The full `go test -race ./internal/install` was attempted but could not compile while `internal/install/service_linux_test.go` referenced unfinished `parseLinuxManagerStatus`, `linuxStatus*`, and `linuxService*` symbols. The integrator owns the full package race/vet gate once that work compiles. `git diff --check` passed for the owned files.

No interface conflict was found with the frozen `transaction`, `validateTransaction`, or store signatures. This is storage implementation evidence only; it does not qualify a native installer lifecycle or Linux service/game behavior.
