# Task 30 Linux maintenance-aware updater reconciliation

`LinuxInstaller.ReconcileUpdaterForMaintenance` accepts the caller's typed
maintenance challenge and validates it against the snapshot's installed image
and the manager's current control files. The challenge must be current and
valid, its `From` image must match the captured installed image, and its
schema must match. The drain receipt is strictly decoded and freshly validated.

The bridge pins both proof files and their control root, requires the exact
snapshot control set plus the two maintenance entries, and rechecks proof
identities, bytes, and the root before cleanup and after cleanup. It preserves
the maintenance challenge and drain receipt while ordinary terminal updater
files are reconciled. The ordinary `ReconcileUpdater` path retains its prior
behavior and rejects these extra entries. Nonterminal maintenance calls still
validate proof before returning without updater cleanup.

Temporary-root tests cover successful terminal cleanup with proof retention,
malformed challenge, nonce mismatch, wrong source image, foreign and pending
entries, replaced challenge, ordinary-path refusal, a captured-absent control
root transition, and validation on nonterminal snapshots. Package tests,
race tests, and vet are the available evidence; native Linux qualification is
not claimed.
