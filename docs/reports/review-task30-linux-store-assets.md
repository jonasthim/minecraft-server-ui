# Review: Task30 Linux store and assets

## Verdict: Request changes

The fixed asset primitives match the reviewed role/path/mode contract, retain file and parent identities, bound and hash file reads, and use exclusive same-directory staging for publication and removal. The journal store has one concrete recovery blocker on pre-publication errors.

## Findings

### Medium — pre-publication failures leave a pending marker that permanently blocks journal loading

**File:** `internal/install/store_linux.go:202`

`Record` creates and syncs `installation-transaction.pending` before the context check and `beforePublish` seam. If either check fails, or a later pre-rename recheck fails, the function returns without removing its own pending file. `Load` treats any pending entry as requiring recovery and returns an error at lines 87–90; it does not distinguish this unrenamed temporary file from an uncertain post-rename publication. For example, a deterministic `beforePublish` fence error leaves the old journal unchanged (or absent), but a subsequent `Load` cannot recover it and all later `Record` calls fail because the slot remains occupied.

Keep the pending file on uncertain post-rename outcomes, but on known pre-rename failures remove it only after verifying it is still the exact file created by this call, then sync the parent. Preserve any replaced or foreign pending entry. Add a regression that injects a pre-publication failure and proves the old journal remains loadable and the foreign-pending case remains untouched.

## Checks

- Read the repository instructions, approved design/plan/implementation ledger, both frozen Task30 briefs, and both worker reports.
- `git diff --check -- internal/install/store_linux.go internal/install/store_linux_test.go internal/install/assets_linux.go internal/install/assets_linux_test.go` — passed.
- `PATH=/home/thim/.local/go/bin:$PATH go test -race ./internal/install` — passed (`ok`, cached).

No code, ledger, service, account, firewall, or game state was changed. This review does not qualify the native Linux installer lifecycle.

## Scoped re-review: journal pending cleanup

**Verdict: Pass.** The round one finding is resolved. `Record` now removes its own pending inode on known pre-rename exits using a detached, bounded cleanup context. Cleanup revalidates the state root and pending inode, owner, and link count, and checks contents when the full expected journal was written. It preserves a pending entry that has been replaced. The `published` flag is set immediately after successful rename, so post-rename errors bypass cleanup and leave the live journal in place while the store remains uncertain.

The new tests cover cleanup after a prepublication failure with caller cancellation, subsequent loading and recording from the prior journal, and preservation of a replaced foreign pending entry. The existing uncertain-publication test checks that post-rename failure retains the published journal, removes no pending marker, and blocks further guessing.

Verification: the documented source-list race run passed with `-count=1`:

```text
PATH=/home/thim/.local/go/bin:$PATH go test -race -overlay=/tmp/task30-store-overlay/overlay.json internal/install/contracts.go internal/install/coordinator.go internal/install/store_linux.go internal/install/store_linux_test.go internal/install/coordinator_test.go -count=1
ok command-line-arguments 1.068s
```

No new breakage found in the scoped store fix. Assets were not re-reviewed.
