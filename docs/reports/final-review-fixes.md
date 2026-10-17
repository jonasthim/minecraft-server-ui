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

The owning service mutex serializes its uploads, preparation/staging and expiry across all instances. Content and pack own separate stage areas and quotas. Cleanup additionally holds a SQL transaction from the ownership recheck and candidate selection through filesystem unlink and metadata deletion. It takes a SQLite write reservation before selecting candidates. The production database's single connection serializes durable queue/reference publication across that interval; no newly queued job can appear between selection and unlink. All queries/deletes inside cleanup use the transaction. Contained path/root, unlink and directory-sync helpers perform filesystem-only work, with no nested database acquisition. R1 below supersedes the original caller-context transaction lifetime: cancellation must not release ownership while filesystem effects continue.

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

## R1 follow-up: cancellation retains cleanup ownership through filesystem effects

Assigned follow-up base: `661e2bc` (original correction wave `e515655`). The scoped review accepted I1's retention policy and M1, and found a new Important R1 in cleanup ownership on caller cancellation. This follow-up changes only content/pack cleanup lifetime and focused tests; it does not reopen those accepted policies or UI behavior.

### Correction and lifetime assessment

The original `db.BeginTx(ctx)` lifetime was unsafe: `database/sql.Tx.awaitDone` can roll back and release the pooled connection asynchronously when the caller cancels, while a synchronous filesystem operation in the cleanup goroutine is still admitted. The service mutex does not serialize independent durable queue publication. Inspection of the local Go `database/sql` implementation confirmed this distinction.

Both cleanup implementations now acquire an explicit `db.Conn(ctx)` using the original caller context. Waiting for that admission is cancellable. After admission, `conn.BeginTx(context.WithoutCancel(ctx), nil)` gives the ownership transaction an explicit lifetime. Caller cancellation/deadline cannot start its asynchronous rollback. Existing write reservation, ownership predicates, stage TTL, quotas, history and recovery guards remain intact.

SQL statements and filesystem preparation still receive the original caller context. Cleanup checks cancellation between stages and before commit. Cancellation before a stage is admitted aborts that stage. Cancellation after the final check may allow an already admitted synchronous unlink/sync to finish; the database ownership fence remains held throughout. This is deliberate: an ordinary context check cannot cancel a filesystem syscall or close the check-to-unlink window. It is unsafe to release ownership on a hard deadline while such effects may still run. Cancellation stops further work as soon as control returns to a checked boundary.

Deferred ordering is explicit: filesystem helper/root cleanup finishes first, explicit transaction rollback finishes next, and only then does `Conn.Close` return the reserved connection to the pool. There is no background cleanup goroutine and no database reentry under the transaction. Original-context cancellation can interrupt statement execution, but no subsequent filesystem work starts on its error path. The detached transaction lifetime is not used to continue ordinary queries/uploads after cancellation.

The same partial-failure policy remains: if an admitted unlink finishes and the cancelled metadata DELETE cannot commit, cleanup returns cancellation and retains retryable stage metadata for already absent bytes. A publication can proceed only after filesystem work stops and ownership is released. An abort before unlink instead leaves valid stage bytes available to the subsequent publisher. This preserves both allowed cancellation outcomes without pretending every expired receipt remains usable after completed cleanup.

### Focused red/green and cancellation coverage

New `TestStageCleanupCancellationKeepsPublicationFence` cases exist in both packages. They use real staged bytes, the existing fault-injection mechanism and a real asynchronous queued-job INSERT with its own short deadline. One case cancels before filesystem work; the second cancels after the last pre-unlink check, immediately before the admitted unlink. While cleanup remains at either seam, independent publication must remain blocked rather than acquiring the database after caller cancellation. The producer's deadline prevents the old reproduction's synchronous-insert deadlock on a corrected fence. After cleanup returns, a fresh publication succeeds. The first case verifies the queued receipt remains hash/size readable; the second verifies unlink finished before publication and cancelled metadata remains charged/retryable.

Both packages failed these tests before the lifetime fix with `cancellation released ownership before filesystem effects stopped: <nil>`, demonstrating that the independent INSERT completed inside the cleanup boundary. The final focused race run passed: content 2.268 s and pack 2.266 s.

A check-only mutation restored `db.BeginTx(ctx)` while retaining the new `ctx.Err` checks. The after-unlink-admission case failed in both packages (content 0.014 s, pack 0.017 s) with the same early-publication assertion. The correct connection/transaction ownership code was restored immediately. This demonstrates that cancellation prechecks alone do not satisfy R1.

`TestStageCleanupCancellationWhileWaitingForAdmission` reserves the sole production-style pooled connection, starts actual cleanup asynchronously, observes its admission wait, then cancels the caller while keeping the blocker connection reserved. Cleanup must return `context.Canceled` before the blocker is released, with stage bytes and metadata intact. This guards against detaching the admission wait together with the transaction lifetime.

The tests explicitly exercise manual cancellation, which invokes the same `database/sql` transaction-lifetime cancellation mechanism as a caller deadline. No separate filesystem-hang/deadline stress or native platform execution is claimed. The review's saved overlay snapshots an older test file; it was read as reproduction evidence and was not copied over current tests or used as the final corrected regression.

### Follow-up verification and freeze

Commands executed on this follow-up:

- Red: `go test ./internal/content ./internal/modpack -run '^TestStageCleanupCancellationKeepsPublicationFence$' -count=1 -timeout=30s` failed in both packages before the ownership correction.
- Green: `go test -race ./internal/content ./internal/modpack -run '^TestStageCleanupCancellation' -count=1 -timeout=30s` passed (2.268 s / 2.266 s), including admission cancellation.
- Check-only mutation: `go test ./internal/content ./internal/modpack -run '^TestStageCleanupCancellationKeepsPublicationFence$/cancel_after_unlink_admission$' -count=1 -timeout=30s` failed in both packages; correct production code was restored.
- `go vet ./internal/content ./internal/modpack` passed.
- `git diff --check` passed.

Final complete changed-package check: `go test -race ./internal/content ./internal/modpack -count=1 -timeout=180s` passed (content 37.012 s; pack 20.764 s). Self-review traced defer ordering, original-context SQL/FS error exits and the cancellation boundary; no further code changes were needed. All six follow-up-owned files are frozen for the integrator's targeted R1 review. No shared contracts, UI, queue, schema, dependencies or unrelated Windows files changed. The integrator owns the narrowly scoped R1 review and the next combined hosted CI run. No additional broad review, staging, commit, push, host/game mutation or vault write was performed.

## Native Windows follow-up: remove redundant chmod from stage deletion

Assigned base: `c55b1aa`. Actual hosted native Windows run `39023970811` failed content cleanup and its composed update consumers. The saved log `/home/thim/.cache/dilla-tmp/minecraft-ui-ci-windows-third.log` reports `chmod ...state/content/<receipt>.blob: Access is denied` in stage expiry/quota, queue-publication, partial-failure, admitted-cancellation and update receipt-retention checks. These are actual Windows test failures; local Linux passes did not qualify this path. I1 policy, R1 ownership lifetime and M1 remain closed and unchanged by this follow-up.

### Diagnosis and minimal correction

`openChecked` returns a validated `Root.Open` reader (`O_RDONLY`). Windows gives that handle read access; `File.Chmod` passes its existing handle to `SetFileInformationByHandle`, which requires `FILE_WRITE_ATTRIBUTES` when clearing the read-only attribute. The handle lacks that capability. The error is not a request to relax retention, ignore permission errors or make staging writable.

The local Go 1.27 implementation was inspected at `$GOROOT/src/os/root_windows.go`, `$GOROOT/src/internal/syscall/windows/at_windows.go` and `$GOROOT/src/internal/poll/fd_windows.go`. `Root.Remove` uses contained `Deleteat` with `FILE_DISPOSITION_IGNORE_READONLY_ATTRIBUTE` where available. Its older Windows/filesystem fallback obtains attribute-write capability with `ReOpenFile` on the already held deletion handle and clears the read-only attribute on that same object before deletion. It does not reopen an absolute or separately reconstructed file path for attribute mutation. This behavior also explains why the redundant preparatory chmod is unnecessary on supported release-target Windows APIs. On Unix, unlink of an owned private directory entry does not require making file bytes writable.

`internal/content/stage.go` now retains the existing contained `openChecked` type, identity, size and single-link validation, closes the checked reader with error propagation, and calls contained `Root.Remove` directly. The same correction applies to the failed-publication defer, where stage bytes have already been frozen read-only before metadata publication fails. No attribute change is attempted through the read-only validation handle. Staging remains immutable until removal; removal errors remain errors. There is no new dependency, syscall wrapper, platform helper or unchecked pathname operation.

### Focused tests and evidence boundaries

New `internal/content/stage_cleanup_test.go` tests verify:

- A real staged blob is read-only after staging and remains read-only at the final unlink seam; successful removal deletes both bytes and metadata and remains retryable when already absent.
- A real hardlink causes refusal before mutation. Both linked paths retain their original bytes and read-only state, and the stage receipt remains charged. This does not depend on Windows symlink-creation privileges and introduces no skip.
- An injected SQLite publication failure after byte freezing preserves the primary error and removes the already read-only artifact and its quota reservation through the failed-stage defer.

The readonly-at-unlink test failed against the prior helper on Linux with `immutable stage became writable before unlink`; the three new focused race checks then passed after correction (1.687 s). The supplied native Windows failure log independently provides the original platform-specific red evidence for `File.Chmod` itself. The new tests and existing retention/cancellation/update assertions were not weakened.

Commands executed:

- Red: `go test ./internal/content -run 'TestStageCleanupRemovesReadOnly|TestStageCleanupRefusesHardlinked|TestStagePublicationFailureReclaims' -count=1 -timeout=30s` failed on the immutable-stage assertion before correction.
- Green: `go test -race ./internal/content -run 'TestStageCleanupRemovesReadOnly|TestStageCleanupRefusesHardlinked|TestStagePublicationFailureReclaims' -count=1 -timeout=30s` passed (1.687 s).
- `go vet ./internal/content ./internal/update` passed.
- `git diff --check` passed.

Complete necessary checks: `go test -race ./internal/content ./internal/update -count=1 -timeout=180s` passed (content 28.927 s; update 28.105 s). `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /home/thim/.cache/dilla-tmp/minecraft-ui-content-stage-cleanup.test.exe ./internal/content` passed as a build-only check, not native execution. Self-review confirmed both deletion paths retain validation/error propagation and the accepted R1 fence still spans the unlink seam. All three follow-up-owned files are frozen for targeted review. Only the content stage helper, its new focused tests and this report are owned by this follow-up. Native Windows success awaits the next hosted run; no local cross-compilation or Linux check is represented as Windows execution. No other packages/UI/contracts, accepted ownership policy, dependencies, staging/commit/push, host/game services or vault files were changed.
