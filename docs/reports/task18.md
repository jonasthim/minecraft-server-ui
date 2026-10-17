# Task 18: normal update coordination and shared participants

Date: 2026-10-17. Worker submission for integrator review, **not package
acceptance**. Root owns the implementation ledger, final combined verification,
commits, API/router/type integration and independent review. Explicit world
rollback is separately reported in `task18-rollback.md`; shared composition is
in `task18-integration.md`; Task25 owns the final Updates UI continuation.

## Product behavior

- Queued `instance_update_prepare` stages exact target distributions/runtime and
  publisher artifacts, preserving the observed OLD selection/content/pack tuple.
  It never stops/starts Minecraft, captures backups, changes a live pointer, or
  enters the final coordinator commit. Uninstalled instances receive an
  actionable typed refusal before queue admission. Public execution fields are
  diagnostic; durable requester/kind/payload establish authority.
- Typed actor-owned previews expose exact selection/runtime/checksums, changes,
  compatibility/conflict blockers, actionable target pack changes/selections and
  required official archive/member downloads, and standalone required files.
  Unknown/trial uploads never become known compatibility evidence. Pinned
  incompatible items, missing/unknown dependencies, unavailable required files,
  unmanaged/changed bytes and unresolved choices block activation.
- Automatic admission is exactly
  `EnqueueAutomaticOnce(ctx,key,instanceID,actor)(jobs.Job,error)`. Canonical
  target-free input uses `jobs.EnqueueIdempotent`; the job derives its target
  while retaining exact Minecraft/flavor pinning. Manual changes require an
  unchanged confirmed preview and strict target proof.
- Apply constructs fresh job-bound capabilities and rechecks original revisions,
  provider/runtime identity, current publisher metadata, hashes and current
  authority. A verified protected **full safety backup succeeds before stop or
  activation**. Stop intent and acknowledged warning countdown precede stop.
- One retained original stopped scope contributes instance selection/private
  management, lifecycle generation, complete content manifest, pack baseline and
  coordinator state in one privately bound SQL transaction. No ordinary
  lifecycle/content/pack journals are minted. Server content publication uses
  contained paths, staged pre/postimages, checksums and CAS; no world path can
  be represented by this normal journal.
- Preboot compensation restores executable/content state only. After boot intent
  the NEW selection is retained; no older executable or world rewind is applied
  automatically. The separately confirmed administrator rollback coordinator
  owns every possible world move, new current full safety capture and immutable
  consent/archive/history binding.

## Authority and lock contracts

`StoppedSession.WithCoordinatorScope` retains one original parent session and
privately binds its original instance service/database. The unforgeable owner
pointer rejects copies, fabricated and expired scopes. `WithUse` and
`WithCommitUse` supply linked operation contexts, close admissions and drain
outstanding operations/errors before authority expires. Capability locks are
short and never retained across callbacks into another participant.

`WithCommit` permits exactly one coordinator commit attempt, exposes only its
own SQL transaction, drains detached participant work and preserves its error.
No participant reenters parent Session methods or the service DB inside that
transaction. Service lock order is instance scope, pack, then content; no
`WithUse` spans a callback that calls `WithCommit`. Selection participation
prepares private management protocol/secret/loopback reservation outside SQL,
then commits selection/runtime/protocol/reservation together. Credentials do not
enter previews/history/audit or rollback responses.

`Execution.CheckCurrent` checks exact original execution/service identity and
immutable dispatched job/requester/kind under the short queue lock. It is
read-only and acquires no slot or SQL transaction. Scheduler
`BeginUpdateStop` returns the exact committed stop epoch and original one-use
start closure. Fresh requester/authority/epoch/lifetime are checked; the existing
recovery mutex spans only native Start. Later deliberate stop/configuration
prevents restart; readiness waits release that mutex.

Ordinary backup/content/pack/config/delete/activation ownership remains strict.
Only an exact original prepared normal update with ready protected archive,
confirmed native suspension, unchanged durable request/preview/before pointers
and canonical marker can continue its own stopped admission. Explicit rollback
has its distinct exact original administrator consent/history exception. Neither
exception can be borrowed by an ordinary edit or unrelated/copied job.

## Durable normal journal and recovery

Frozen migration00018 and supersession contracts were separately checkpointed by
root (`003b083`, `c192c7a`) for Task24; those commits did not accept this service.
Normal private `Journal` version1 records plan/request, before/after lifecycle,
content before/after, pack before/after, contained content operations/preimages,
stop epoch and prior running state. Optional later receipts are:

- `autostart`: `prior_present`, `prior_desired`, `prior_applied` (nullable),
  `prior_error`, `disable_confirmed`, `restore_confirmed`.
- `started_identity`: the exact native-returned process identity, when available.
- `recovery_observation`: safe authored current-process/administration wording.

States are prepared → publishing → committed → boot_attempted → complete;
preboot restored state is rolled_back. Failures retain blocked ownership.
Superseded is reserved for the exact separately consented one-hop instance
rollback handoff; it does not claim worlds were restored.

The independent update and world-risk markers plus the protected activation
marker block launch without SQL. SQL boot intent is durable **before** marker
release/native Start. Linux units have no manager dependency, so autostart is
explicitly suspended through the existing native method before releasing boot
blockers; desired administrator policy is preserved. Its preimage is durable
before the OS operation. SetAutostart runs outside the native stopped lock.
Successful finalization/readiness or safe preboot compensation restores prior
known policy; absent prior policy remains disabled. Native policy errors record
`applied=NULL`, never fabricate success, and remain visible. Failure to disable
retains boot blockers; policy-only restoration failure reports a partial result.

Preboot recovery checks SQL-selected generations/revisions before materializing
managed configuration, uses CAS content replay/compensation, verifies/seals the
SQL-selected generation, and finishes policy/marker cleanup. Postboot recovery
never republishes content/configuration or reseals against a potentially running
game. It reconstructs exact markers, reasserts suspension and retains NEW
pointers/current bytes. Only an exact persisted returned native identity can be
stopped automatically; matching runtime/cwd alone is insufficient. Missing or
uncertain proof leaves an honest running/unknown administrative hold without a
signal or file/config write.

`StartupBlocked` is read-only: it distinguishes that exact reconciled hold from
foreign/corrupt/unresolved ownership. Root injects it into instance materialization
and lifecycle autostart/readiness. The manager can serve recovery controls and
resume the queue while ordinary edits/start stay blocked. True ownership
corruption remains an error. Startup root orders ordinary backup/content/pack
repair before update, and update before ordinary instance materialization.

## Required official file workflows

The public fragment is `docs/api/updates.yaml`; root merges it and regenerates
central TypeScript with `make gen`. `MountUpdates` belongs in authenticated,
CSRF-protected routing and uses existing read/operate/admin PAT scopes.

- `Plan.pack` uses bounded existing ModpackPreview diagnostics. Pack conflict
  choices, optional/excluded/pinned/disabled selections and manual receipts are
  resubmitted through typed ModpackRequest in a fresh preview job.
- Admin exact archive acquisition reuses the existing modpack archive endpoint.
  Admin raw member upload uses
  `updates/previews/{outerPreviewId}/pack/members?path=...`, returns only
  `{path,receipt_id,sha256,size}`, and cannot supply hashes/paths outside the
  original unresolved receipt. It freshly checks official metadata and archive,
  original target/source/old revisions, member size/hash/text/descriptor, and
  current administrator in the same private stage publication transaction.
- Private `pack_member_receipts[path]` acquisition metadata is CAS-appended in
  that same transaction, preserving Plan/Request/original pack proof. It protects
  interrupted upload→reprepare from ordinary stage cleanup. It conveys no
  capability, launch pointer or world consent.
- Standalone `Request.content_manual[installedID]=ContentUploadID` requires an
  exact ContentSelection (including file_id), manual mode and current admin.
  `Plan.content_manual_required` supplies selection/official URL/filename/hashes/
  reason. Existing inert admin JAR upload is reused; fresh target preparation
  verifies exact publisher size/digests and descriptor/dependency proof. Final
  content commit checks current admin inside the one supplied SQL transaction.
- Confirmation digest excludes only diagnostic pack.id and source provenance
  fetch time; meaningful target/source/hash/revision/choice/required-file fields
  remain bound. Persisted previews contain immutable evidence, never a reusable
  originating job capability.

Serialized preview envelopes are refused above 16 MiB before insertion; at most 32
unexpired previews per instance can be prepared, with 30-minute expiry. Member
acquisition metadata has at most 8192 paths. Responses list at most 32 previews and
64 history rows. Historical previews, consent and referenced stages are
conservatively retained; no history pruning was introduced. SQL/files can grow
with retained history. Safe future pruning must cover jobs, all update rows
(including terminal/superseded), rollback receipts/consent and private acquisition
references. Existing stage byte/count quotas remain in force; ordinary content
and pack cleanup now preserve update previews/jobs/transactions.

## Verification evidence

All tests are deterministic local fixtures, with no network, game launch, EULA
acceptance or host installation. Native sealing success is explicitly injected
through approved `lifecycle.Config.VerifyAndSeal`; production root leaves it nil,
retaining the existing full immutable runtime/distribution verifier.

Observed passing checks during this submission:

- `go test -race ./internal/update ./internal/instance ./internal/lifecycle
  ./internal/backup ./internal/content ./internal/modpack ./internal/jobs
  ./internal/scheduler ./internal/launcher -count=1 -timeout=90s` passed at the
  postboot/policy foundation checkpoint (update 22.899s; all nine packages passed).
- `go vet` on those packages plus `internal/api` passed at that checkpoint;
  intentional copied mutex-bearing capability fixtures use reflection.
- After required-file extensions, `go test ./internal/update ./internal/api
  ./internal/content ./internal/modpack ./internal/lifecycle ./internal/instance
  -count=1 -timeout=45s` passed, followed by focused standalone/member/digest/
  coordinator/boot-risk/native-policy/direct-start/target-proof regressions.
- Scoped `git diff --check` passed before final report. Root performs the final
  combined frozen suite once; this report does not substitute earlier evidence
  for that gate. Task25 reports final TypeScript/lint/build/browser evidence.

Concrete regressions cover copied/forged/expired scope/execution/participants,
linked cancellation and callback drain/overlap, foreign supplied SQL transaction,
one commit boundary and detached failure preservation; legacy→MSMP/protocol
upgrade/downgrade/port collision/rollback; scheduler later stop/configuration and
guard-first serialization; stage-only preview and exact old/target tuple split;
changed provider proof, expired cross-job capability, pinned incompatibility and
unknown language/content; full backup failure; preboot native verification failure;
postcommit crash before ordinary configuration materialization; boot intent after
publication marker removal; safe surviving-JVM administration and explicit
rollback after queue resume; native suspension/restore failures and ordering;
manual file verified resume, wrong digest/foreign/expired receipt/changed metadata,
admin revocation during stage publication and at live SQL commit; immediate
receipt retention and meaningful confirmation digest changes. Supersession has
an explicit corrupted terminal-cycle refusal regression in addition to its frozen
helper checks. API tests exercise roles, existing PAT scopes and strict bounded
request decoding.

## Owned scope for review

New normal feature files: `internal/update/{plan,service,prepare,apply,journal,
ordinary_ready,normal_readiness,readiness,recovery,startup,status,manual}.go` and
associated plan/readiness/journal/coordinator/content_manual tests; API
`update_handlers.go`/tests; API fragment; initial UI feature ownership was handed
to Task25. The frozen supersession helper is unchanged; its cycle test was added.

New shared participants: instance coordinator_scope/update/update_reconcile and
tests; jobs current and tests; launcher update_transaction and tests; lifecycle
update/update_autostart/update_recovery and tests; scheduler update_stop and tests;
content update and tests; modpack update/update_manual and tests.

Narrow existing edits: instance service/reconcile hold field/check and
backup_properties shared extraction; jobs immutable dispatch binding; launcher
single update marker hook (preserves accepted Task17 hook); lifecycle Config
sealing/defer hooks and early autostart/readiness checks; scheduler existing
cancelRecoveryLocked extraction; content/modpack target-aware private preview
extractions (ordinary wrapper semantics preserved), content cleanup/fresh upload
admin check, pack stageChecked publication seam/reference cleanup. Ordinary
backup restore primitive extraction was initially authored here; subsequent
backup rollback ownership/refinement is reported by the rollback worker.

No staging/commits, new dependencies, subagents, deployment, game process, EULA,
native Windows/systemd qualification or vault writes were performed. Real
Linux/Windows/game/client and power-loss release qualification remains absent;
mocks, builds and inert artifact checks do not establish stable support.

## Normal review fix round 1 (reviewed HEAD f58d921)

All three Important findings in
`.superpowers/sdd/2026-10-16-minecraft-server-ui/task18-normal-review.md` were
addressed for the same reviewer's rereview. This is a worker repair submission,
not an acceptance claim. Separately approved rollback source files and UI files
were not edited in this round.

- **I1 — exact attempted process ownership.** Every in-flight readiness/receipt
  failure now passes the identity returned by `native.Start` to cleanup. Stop
  requires the original instance, positive PID, nonempty start identity and full
  equality with the fresh current identity. A replacement or uncertain identity
  remains an observation-only administrator hold. The direct-start regression
  now covers A returned/B current, missing proof, and receipt-persistence failure
  with valid A, as well as original durable intent and generation checks.
- **I2 — terminal completion and postboot recovery.** After durable `complete`,
  the final diagnostic log is best effort and the error defer is explicitly
  disabled. An exact surviving/recreated update or world-risk marker on a booted
  complete row enters the existing byte-preserving administrative hold; SQL
  completion cannot authorize file replay. Recovery checks exact marker ownership
  before reconstruction, handles the risk-only publication edge, reasserts native
  policy suspension, and preserves NEW pointers. `restoreNormalFiles` rejects
  every boot-attempted journal as a second guard. Clean terminal policy-only
  repair retains its read-only path. Regressions cover terminal log SQL failure,
  running and stopped game-modified configuration, exact risk-only marker,
  no reseal/materialization/start or uncertain stop, ordinary admission blocking,
  instance/lifecycle startup deferral, actual queue resume and separately confirmed
  rollback from both boot-attempted and complete-marker restart states. No game
  readiness is inferred from terminal SQL or marker possession.
- **I3 — verify before publication.** The narrow private staging seam validates
  exact staged member bytes and destination rules before opening the fresh-admin
  and preview-CAS publication transaction. Failed validation removes the blob and
  reservation without publishing `pack_member_receipts`. The wrong-digest test
  asserts zero retained stages/quota/blobs and no receipt reference, then verifies
  successful acquisition and retained receipt on a valid retry. Ordinary staging
  continues to use the same wrapper with no additional validator.

### RED → GREEN evidence

Before fixes, focused regressions failed with replacement/uncertain JVM stopped
(I1), completed update job reported failed after final diagnostic rejection and
complete-marker startup either refused running native state or left an orphan
hold after replay (I2), and one retained three-byte stage after wrong digest (I3).
The same focused regressions passed after the repairs.

Final bounded checks (no repository suite repeated):

```sh
go test ./internal/update ./internal/lifecycle ./internal/modpack -run 'TestCoordinator|TestCompleteBootMarkers|TestBootRiskStartup|TestAutostartSuspension|TestDirectUpdateStart|TestTargetManualReceipt|TestJournalPublication' -count=1 -timeout=90s
go test -race ./internal/update ./internal/lifecycle ./internal/modpack -run 'TestCoordinator|TestCompleteBootMarkers|TestBootRiskStartup|TestAutostartSuspension|TestDirectUpdateStart|TestTargetManualReceipt|TestJournalPublication' -count=1 -timeout=90s
```

Both passed. Race timings: update 8.695s, lifecycle 2.158s, modpack 2.378s.
Scoped `git diff --check` also passed. These are deterministic local fixtures with
injected native behavior, not Linux/Windows/game/runtime qualification.

Frozen repair files: `internal/lifecycle/update.go`, `update_test.go`;
`internal/update/apply.go`, `recovery.go`, `coordinator_test.go`;
`internal/modpack/stage.go`, `update_manual.go`, `update_manual_test.go`; this
report. No dependencies, migration/API contracts, staging, commits, host/native
installation, EULA acceptance or game execution changed.
