# Task 30 Linux rollback receipt removal bridge

`LinuxInstaller.RemoveInstalledForRollback` is a narrow bridge for the native
first-install rollback path. It requires the live installer session, held
updater operation lock, stopped manager data lock, stopped service, and
revalidated state, binary, and data roots. It accepts only a valid expected
image and a receipt snapshot owned by that session.

For a present captured receipt, the snapshot must name the expected image and
the canonical executable must hash to that image. Immediately before the
fixed `installed.json` unlink, the method rechecks strict updater conflicts
and compares captured receipt identity and bytes. A captured absence is an
idempotent no-op only if absence still holds. The method never creates a
receipt or mutates updater journal state. It syncs the state directory after
removal and reports sync failure as an uncertain durability outcome.

Temporary-root tests cover lock requirements, foreign snapshots, expected
image mismatch, cancellation, replaced and symlink receipts, successful
removal, sync uncertainty, and idempotent captured absence. These tests are
package evidence only and do not qualify native Linux behavior.
