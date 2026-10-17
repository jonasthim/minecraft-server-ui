# Task 30L4A: stopped manager-state snapshots

## Implemented

Added `internal/managerstate.Snapshot` and `Verify`, plus the no-server adapter
`runManagerState`. Snapshot and verify require an existing absolute data
directory and ordinary `manager.db`, acquire the same `launcher.AcquireManager`
OS lock used by the manager, and retain it through validation/publication. They
copy only `manager.db` and present SQLite WAL/SHM/journal sidecars. Other manager
data in the data directory is ignored and never copied.

The snapshot root is created exclusively under `maintenance`, mode 0700 on Unix
and with a protected Windows DACL for the manager account, Administrators, and
System. The format-1 private manifest is bounded, strictly decoded (duplicate
and unknown fields rejected), sorted, and written last. Copies stream through a
fixed buffer with context checks, SHA-256, source identity/set/content
revalidation, and synced files. Created files retain their open creation
handles through cleanup, preventing inode/file-ID reuse from making an
unlink-and-recreate replacement appear owned. Failed cleanup checks the
retained handle and current path identity, and preserves unknown or replaced
objects. Mid-copy cancellation returns all partial ownership records; fixed
authored errors preserve `errors.Is` identity for cancellation without exposing
filesystem, SQL, or driver error text.

SQLite checks run on a separate private copy, with the original raw snapshot
left unchanged. SQLite opens the already-created working database in `mode=rw`
so a replaced or missing validation path cannot create a new database. Before
the open, after SQLite's first connection, and after validation closes, checks
bind the data, maintenance, validation directory, and database path back to
their retained roots and creation handle. Normal SQLite WAL recovery is enabled;
no immutable mode or migration opener is used. Validation requires exactly one `integrity_check`
row equal to `ok`, an empty `foreign_key_check`, a coherent Goose migration
ledger, a supported schema number, and the foundation tables/columns present
for that schema. The validator accepts an older schema without migrating it.

The adapter accepts exactly `snapshot` or `verify --snapshot HEX_ID`, uses
`config.FromEnvironment`, and emits one JSON `Summary` only after independent
verification. Root integrated dispatch from `main.go`; the command test exercises both
snapshot and verification through that dispatcher.

## Evidence

- `go test ./internal/managerstate ./cmd/minecraft-ui` — passed.
- `go test -race ./internal/managerstate ./cmd/minecraft-ui` — passed.
- `go vet ./internal/managerstate ./cmd/minecraft-ui` — passed.
- `GOOS=windows GOARCH=amd64 go test -c -o /tmp/managerstate-task30l4a.test.exe ./internal/managerstate` — passed.
- `GOOS=windows GOARCH=amd64 go test -c -o /tmp/managerstate-cmd-task30l4a.test.exe ./cmd/minecraft-ui` — passed.
- `GOOS=windows GOARCH=amd64 go vet ./internal/managerstate ./cmd/minecraft-ui` — passed.
- `git diff --check` over the owned paths — passed.

The temporary child-process WAL fixture commits a marker, explicitly checkpoints
the schema, then exits without closing SQLite. The resulting nonempty WAL
contains the committed marker while the main database alone does not. Snapshot
preserves the exact raw sidecar set and hashes; an independently opened copy
with the WAL recovers the marker, and the main-file-only negative control loses
it. Verify also passes without changing the source files. A version-1 fixture
passes and remains version 1 after snapshot.

Other covered refusals include absent DB without creation, lock contention,
cancellation between copied files with cleanup and preserved cancellation
identity, cancellation during validation copy with retained partial ownership,
sidecar-set change after capture, hardlinks and symlinked DB,
duplicate/unknown manifest fields, tampered DB bytes, extra files, invalid IDs,
replacement/unknown-object cleanup, and a deterministic validation-directory
replacement at SQLite open that leaves no database in the substitute path.
Windows owner checks now compare the exact manager SID and use well-known SID
checks for Administrators and System; Windows tests prepare an explicit private
DACL and reject numeric SID-prefix collisions.

## Limits

Windows behavior was cross-compiled and vetted but not executed natively here;
the hosted Windows test must run the account-DACL and retained-WAL cases. Parent
directory flush behavior and platform power-loss durability were not qualified.
This packet adds no migration, restore, installer/startup fence, native workflow,
service/account manipulation, or release claim. Root reviewed the source and
CLI dispatch for integration; native Windows execution is still pending.
