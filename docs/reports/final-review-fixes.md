# Final whole-branch review corrections

Date: 2026-10-17. Assigned base: `98540a2`; application reviewed by the whole-branch review: `7993055`. Scope: Important I1 staging reclamation and Minor M1 lifecycle terminal refresh from `.superpowers/sdd/2026-10-16-minecraft-server-ui/whole-branch-review.md`.

Application changes and covering tests are frozen for independent scoped review. The integrator owns acceptance and the implementation ledger. This report does not accept the wave or promote release qualification. Concurrent Windows/CI work belongs to other workers and is not included in this report's claims.

## I1: reclaim temporary stages without deleting update evidence

Owned changes: `internal/content/{service,stage,stage_retention}.go`, `internal/modpack/{manual,stage,stage_retention}.go`, their new `stage_retention_test.go` files, and `internal/update/stage_retention_test.go`. No update production API, schema, queue contract, quota or dependency changed.

The ordinary 24-hour stage expiry now distinguishes historical evidence from live artifact ownership. Expired update previews and completed/rolled-back/superseded update transactions do not permanently pin acquisition/staging blobs. The preview and transaction rows themselves, their immutable JSON, consent, supersession references, audit entries, selected files, update recovery directories and protected backups are retained.

The exact retention boundaries are:

- Any nonterminal update transaction, noncomplete backup-owned update rollback, content journal or pack journal retains **all** stages of its instance. Terminal states are `complete`, `rolled_back`, `superseded` for update transactions, and `complete` for backup-owned rollback.
- Any independent `content-transaction.json`, `modpack-transaction.json`, `update-transaction.json` or `update-world-risk.json` marker retains **all** stages of its instance. Presence is decisive, including malformed, orphan or terminal-SQL markers. Unexpected filesystem inspection errors stop cleanup.
- An update preview retains referenced stages while unexpired, while its expiry is malformed/unparseable, or while a queued/running prepare/update/rollback job payload references that preview. Existing conservative `instr` reference matching is retained; false positives retain artifacts.
- A queued/running prepare/update/rollback job directly referencing a stage retains it. This protects manual upload/member receipts between upload and reprepare, including after the original preview expires. A terminal transaction whose original job is still queued/running retains its referenced stages too.
- Existing ordinary content/pack preview, pack journal and pack archive references remain guards. Existing ordinary preview expiry still checks durable live jobs. Automatic reclamation only selects stages older than the existing 24-hour cutoff; explicit discard retains its existing actor/resource restrictions.

Both quota admission paths retry their existing unchanged count/byte checks after running existing expiry across instances that own stages. Thus abandoned receipts in another instance can release the shared 16-GiB capacity. Content still allows 256 stages and packs 20,000; each retains the existing 8-GiB instance and 16-GiB global byte limits. A legitimate live/recovery owner or malformed hold may still exhaust quota until that work is resolved. Cleanup is conservative during that hold.

The owning service mutex serializes its uploads, preparation/staging and expiry across all instances. Content and pack own separate stage areas and quotas. Cleanup additionally holds a SQL transaction from the ownership recheck and candidate selection through filesystem unlink and metadata deletion. It takes a SQLite write reservation before selecting candidates. The production database's single connection serializes durable queue/reference publication across that interval; no newly queued job can appear between selection and unlink. All queries/deletes inside cleanup use the transaction. Contained path/root, unlink and directory-sync helpers perform filesystem-only work, with no nested database acquisition. Context cancellation rolls the transaction back.

Filesystem deletion precedes metadata removal. A later filesystem, SQL or commit failure is returned; rollback may leave an unowned reservation whose bytes are already absent. A subsequent cleanup tolerates missing files and releases that reservation. It does not report partial cleanup as success.

### Recovery and rollback consumer trace

`internal/update/journal.go:stageJournal` copies verified content/pack participant preimages/postimages into independent `state/updates/<transaction>/before-N` and `after-N` blobs before admitting the durable update transaction. `applyJournalFiles` and normal recovery read those copied blobs and hashes. Participant `WithStage` is an initial preparation/copy consumer; it is not a recovery dependency on historical temporary receipts. Existing active transaction and job guards remain conservative throughout preparation and recovery.

`internal/update/rollback.go` reads immutable selected update history through `decodeRollbackSource`; world rewind uses the backup-owned participant, protected full backup and separately confirmed rollback journal. Temporary stages are not its backup archive. Supersession/consent/history rows and all independent recovery/rollback material remain untouched. Marker presence separately protects a terminal transaction whose cleanup or failed-boot hold is unresolved; terminal SQL is not treated as sufficient release authority.

### Deterministic evidence

The initial new focused tests failed against the original implementation in both content and pack packages: expired abandoned previews, completed history and rolled-back history retained real staged blobs, and repeated-use quota admission failed on cycle 0. This is an executable service/SQLite reproduction of I1, not a query-text assertion.

New retention tests create real staged bytes and verify retained stages still open with their original digest/size. They cover unexpired upload-to-reprepare receipts, queued/running expired-preview ownership, queued reprepare payloads, terminal transactions with a queued original job, prepared recovery, failed-boot `blocked` recovery, succeeded jobs with complete transactions but unresolved update/world-risk markers, orphan malformed markers, and malformed expiry. Reclaimed stages lose both file bytes and quota metadata; historical preview/transaction rows remain.

Quota tests run three cycles of expiry plus fresh successful stage admission. They cover content's 256-stage count limit, both packages' 8-GiB instance bounds, and shared 16-GiB capacity held by two other instances. Byte fixtures use durable quota reservations rather than allocating GiB; separate stage-read tests validate real artifact bytes. Pack's 20,000-record count limit was not separately filled.

`TestStageCleanupSerializesQueuedReferencePublication` in both packages attempts a real queued-job INSERT during the cleanup boundary using the existing fault-injection mechanism. It verifies the INSERT waits while candidate bytes exist, then succeeds after cleanup releases the transaction and the unowned expired receipt is gone. A mutation releasing the transaction immediately before unlink made both tests fail with `queued reference published before receipt unlink completed: <nil>`; restoring the transaction makes them pass. Already queued/running ownership is independently covered by the retention table.

`TestStageCleanupRetriesAfterMetadataFailure` injects an SQLite delete failure after unlink. Cleanup returns an error and retains the reservation; a retry with the trigger removed succeeds despite the already absent file and frees the quota row.

`TestCompletedUpdateReclaimsReceiptsAndKeepsRecoveryMaterial` composes the actual update/content/backup/lifecycle services with native/provider fixtures. A successful manual-content update is followed by temporary-stage/preview expiry and a fresh upload. Original receipts disappear; immutable transaction JSON stays identical; every independent update pre/postimage digest is checked; protected backup metadata is identical and `Backups.Download` validates its real archive. Actual committed preboot reconciliation then succeeds with the old temporary receipts absent and preserves the selected NEW content. This is deterministic composition evidence, not native/game execution.

## M1: terminal reconciliation survives polling cleanup

`web/src/features/lifecycle/LifecyclePanel.tsx` now publishes the authoritative terminal job immediately and runs final reconciliation in a separate effect. The active polling effect's cleanup cannot abort this effect's status request. Final lifecycle status and parent `onChanged` reads run with `Promise.allSettled`; each is attempted even when the other fails, and rejected ancillary reads produce visible, labeled errors. Existing terminal job errors remain displayed immediately.

`web/tests/lifecycle-terminal-refresh.mjs` runs the shipping instance/lifecycle UI with intercepted APIs at 390 x 844. It freezes the independent five-second status poll, advances only the job poll, and deliberately holds the terminal lifecycle response after the terminal render. The original code failed waiting for the parent's persisted pinned-loader display. After correction, all three scenarios pass: delayed success, delayed status failure, and detail failure. Each performs exactly one additional parent detail read (three total because React development StrictMode makes two initial reads), preserves immediate terminal/error feedback, and reports the appropriate ancillary failure. Successful detail refresh shows the persisted `Pinned build/loader: 0.16.0` on reopening installation. A successful final lifecycle read also renders `Installed` before any independent status poll can run.

## Commands and observed results

- Initial red: `go test ./internal/content ./internal/modpack -run 'TestUpdateStage(Retention|Quota)' -count=1 -timeout=90s` failed on the I1 assertions above; all corresponding final checks pass.
- Final covering package suites after transactional cleanup: `go test -race ./internal/content ./internal/modpack ./internal/update -count=1 -timeout=180s` passed: content 61.605 s, modpack 40.240 s, update 66.132 s. Other workers were running checks concurrently on the host.
- Added final retention/partial-failure cases: `go test -race ./internal/content ./internal/modpack -run 'Test(UpdateStage|StageCleanup)' -count=1 -timeout=90s` passed: content 30.487 s, modpack 14.161 s.
- Composed expiry/recovery test also passed separately under race: `go test -race ./internal/update -run TestCompletedUpdateReclaimsReceiptsAndKeepsRecoveryMaterial -count=1 -timeout=90s` (1.605 s).
- `go vet ./internal/content ./internal/modpack ./internal/update` passed.
- `npm --prefix web run typecheck` and `npm --prefix web run lint` passed after the UI fix.
- Start local check server with `npm --prefix web run dev -- --port 15173`; run `node web/tests/lifecycle-terminal-refresh.mjs`. All three scenarios passed; no browser page errors, one final detail read per scenario, no independent five-second poll. The task-owned Vite server is stopped after checking.
- `git diff --check` passed on this wave. No generated types changed.

No full repository suite, production-embed rebuild or hosted CI was rerun for this scoped wave. The integrator coordinates combined verification after concurrent shared Windows fixes. No game/client, EULA acceptance, installed host service, native Windows runtime, real restore or stable version/layout support qualification is claimed. No staging/commit/push, unrelated host mutation or vault write was performed.
