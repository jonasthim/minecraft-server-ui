# Task30 Linux installer acquisition packet

This packet adds the Linux-only acquisition bridge used by the future native
installer. It does not implement install, update, check, or uninstall flows and
does not qualify a native Linux host.

`AcquireLinuxInstaller` checks root authority before opening paths, accepts a
missing protected updater state root on the fixed fresh-host path, allows the
manager executable to be absent, and takes the existing updater operation
lock. It retains protected state and binary roots for the session. `OpenState`
returns a separately opened root pinned to the same directory identity.

`SnapshotUpdater` reads strict installed-receipt and activation state. It
accepts only `healthy`, `rolled_back`, or `aborted` journals whose receipt and
current manager image match the phase. It binds supported request, scratch,
challenge, and drain files to the journal and refuses pending, unknown, unsafe,
or mismatched state. Snapshots are opaque and owned by their live session.

`LockStoppedManager` checks the fixed systemd service state, existing data
directory ownership and mode, and the manager-owned `manager.lock`. If that
lock is absent, it creates it exclusively, changes ownership through the
retained descriptor, syncs it and the data directory, then acquires the same
launcher lock used by the manager. It rechecks the data root, lock identity,
and service state after acquisition. `ReconcileUpdater` requires this lock and
removes only the captured terminal files after revalidating their identities
and bytes; it syncs each affected root. Receipt reads and compare-and-swap
continue through the existing strict `Maintenance` implementation after
reconciliation.

Focused temporary-root tests cover authority ordering; missing state and
binary; unsafe executable links; transient protected-root replacement during
`OpenState`; session-owned and cross-session snapshots; stopped-service
gating; user-owned lock preservation and contention; malformed and unknown
journal schemas; pending updater markers; healthy, rolled-back, and aborted
phase/image relations; producer and delayed-recovery challenge expiries,
including rejection of far-future or nonmatching challenges;
replacement of a captured terminal source; injected deletion, cancellation,
and sync-fence failures; and successful matching terminal cleanup. The
partial-cleanup tests verify the journal remains when a cleanup step fails,
and that already completed deletions are reported as partial progress.
`go test ./internal/selfupdate` passes. These fixtures are source evidence
only; native systemd, account, ownership, and filesystem qualification remains
outstanding.
