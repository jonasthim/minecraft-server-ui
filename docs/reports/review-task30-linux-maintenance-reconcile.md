# Review: Task30 Linux maintenance reconciliation bridge

## Verdict

**Approve. No Critical, Important, or Minor correctness findings.** The bridge meets the requested source-level invariants in the reviewed diff. This approval covers the reconciliation bridge and package fixtures; it does not establish native Ubuntu installer acceptance.

## Review notes

- `ReconcileUpdater` still calls the shared reconciler without a maintenance challenge. The new proof validation path activates only through `ReconcileUpdaterForMaintenance`.
- The maintenance call binds `From` to the captured `Snapshot.Installed` value, enforces the matching schema, and validates the challenge lifetime and drain receipt freshness/identity.
- Maintenance reconciliation requires the held installer updater lock, stopped manager state, the actual manager data lock and canonical data-root binding before accepting proof.
- It pins the control root and captures both manager-owned proof files by identity, bytes, strict 0600 mode and single-link validation. The exact expected control-file set is checked before cleanup and again after cleanup. Foreign, pending, malformed, mismatched, and replaced files fail before updater cleanup begins.
- Both maintenance proof files remain in place after terminal updater cleanup. Nonterminal maintenance reconciliation validates the proof before its no-op return.
- Tests exercise proof retention, invalid proof refusal, replacement detection before cleanup, ordinary reconciliation refusal, absent-to-present root transition, and nonterminal proof validation.

## Verification

- `/home/thim/.local/go/bin/go test ./internal/selfupdate` — PASS
- `/home/thim/.local/go/bin/go test -race ./internal/selfupdate` — PASS
- `/home/thim/.local/go/bin/go vet ./internal/selfupdate` — PASS

All proof and filesystem cases use disposable test fixtures. No host services, accounts, firewall rules, manager processes, or game processes were touched.
