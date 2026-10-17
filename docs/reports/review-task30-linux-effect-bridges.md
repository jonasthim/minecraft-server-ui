# Task 30 Linux effect bridge review

## Scope

Reviewed the recorded Linux asset publication and effect reconciliation changes against `.superpowers/sdd/2026-10-17-valheim-parity-completion/task30-linux-asset-recovery-brief.md`, plus `LinuxInstaller.RemoveInstalledForRollback` against `docs/reports/task30-linux-receipt-removal.md` and the existing maintenance receipt compare-and-swap behavior.

## Findings

### P2 — Completed publication recovery state is not covered by a fixture

`reconcileLinuxAssetPublication` explicitly accepts `pending` absent with the public file equal to the exact staged state. This is the key post-rename recovery boundary for first install, and the completed exchange boundary after the old inode has been unlinked. The new reconciliation tests cover an unactivated pending candidate and an activated exchange while the prior inode is still pending, but do not construct this completed state. Add fixtures for both first-install and replacement publication with `public=staged` and `pending=absent`, and assert the exact staged identity is returned and no unrelated file is touched. See [assets_recovery_linux.go](/home/thim/Repositories/minecraft-server-ui/internal/install/assets_recovery_linux.go:75) and [assets_recovery_linux_test.go](/home/thim/Repositories/minecraft-server-ui/internal/install/assets_recovery_linux_test.go:35).

## Receipt removal review

No correctness finding. The method requires the live session, held updater operation lock, stopped-manager data lock, stopped service, and revalidated roots/data binding. For a present receipt it matches the session-owned snapshot and expected image, hashes the canonical executable, compares the pinned receipt identity and bytes before removal, and fences the state directory. Captured absence remains a no-op and does not create a receipt. These checks follow the existing `MaintenanceReceipt` snapshot/CAS semantics and report post-unlink fence failure as uncertain.

## Verdict

**Request changes for the missing crash-boundary fixture.** Source review found no other actionable issue in the assigned files. The worker report records package test, race, vet, and diff-check passes; this review did not rerun them because concurrent native and launch work is active.

## Round 2 — completed publication fixture

**PASS.** The added first-install and replacement fixtures cover `public=staged` with the operation pending path absent. Both assert the exact staged state, including inode identity, remains public; each also checks the pending path is absent and unrelated scratch is preserved. The replacement fixture models exchange activation followed by removal of the prior inode. No further findings in the scoped change.
