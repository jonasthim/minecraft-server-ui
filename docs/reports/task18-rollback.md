# Task 18: explicit consented world rollback worker

Date: 2026-10-17. Integration baseline: `305b5cc` plus the intentionally shared,
uncommitted Task 18 foundation. This is a worker review submission; the integrator
owns package acceptance, shared composition and the implementation ledger.

## Ownership

Authored only these assigned implementation/test files and this report:

- `internal/update/rollback.go`, `rollback_recovery.go`, `rollback_test.go`.
- `internal/lifecycle/update_rollback.go`, `update_rollback_test.go`.
- `internal/content/update_rollback.go`, `update_rollback_test.go`.
- `internal/modpack/update_rollback.go`, `update_rollback_test.go`.
- Refined the delegated, already uncommitted
  `internal/backup/update_rollback.go`, `update_rollback_test.go`.

Normal update preparation/application/recovery, shared lifecycle verification,
ordinary restore engines, schema/router/generated types, startup wiring, instance
participants and readiness composition remain with their assigned owners. No
staging, commits, dependencies, host installation or vault writes were performed.

## Architecture and durable ownership

`NewRollbackCoordinator(*Service)` supplies the frozen `RollbackCoordinator`
adapter. Preview reads only exact boot-attempted failed update history and binds
the current revision/generation/content/baseline, retained pre-update archive/hash,
historical generation/selection, administrator and expiry. Enqueue requires the
separate exact instance confirmation and world rewind acknowledgement. Execution
reads the original durable job and `CheckCurrent`, never exported `e.Job` or
`e.Payload` as authority.

One original stopped backup/session capture produces a **new full CURRENT safety
archive**, preserving the currently committed failed update selection/runtime.
Unsafe sources, mixed layouts and failed safety validation refuse rollback before
old selection or world changes. Historical archive metadata/runtime/generation
must agree. The separate supplied admission transaction rechecks the complete
preview/current/retained identities, persists backup-owned explicit consent and
move intent, supersedes the parent and inserts its exact reciprocal one-hop
successor atomically. It changes no world or selection pointers.

The launcher CAS transfers only the update transaction marker. The independent
world-risk marker retains the old parent identity. An admission committed with
the old marker is recoverable through the exact reciprocal persisted consent.
All malformed, orphan, cyclic, competing or mismatched ownership fails before
world replacement. The independent activation marker is retained through final
restoration and sealing.

The final transaction has one original coordinator scope. Factories take pack
admission before content admission; no `WithUse` is retained across a pointer
commit. Lifecycle, instance selection/private management, pack baseline, content
manifest and backup receipt contribute synchronously through **one supplied SQL
transaction**, privately bound to the original instance database. No participant
opens an ordinary lifecycle/content/pack journal. Copies, expired participants,
overlapping operations and foreign transactions are refused; admitted historical
proof work drains before callback authority closes.

The lifecycle participant loads the exact retained historical row, rechecks the
managed runtime, verifies/seals the historical immutable launch through the
production default verifier and supplies fresh historical target loader language
proof. It creates a new lifecycle generation, binds current manager memory and
never overwrites historical rows. Instance selection participation owns private
protocol/credentials/port transitions; credentials never enter rollback history.

Content rechecks the complete restored JAR set, descriptors, hashes and supported
publisher digests through the existing byte verifier. It refreshes historical
language evidence only and preserves unknown provenance/trial metadata. Pack
restoration CAS-checks the observed baseline and restores the exact prior
baseline or its absence, preserving local override semantics; customized local
files need not equal original upstream baseline hashes. Backup move checkpoints
reuse the reviewed stage/CAS/rename/sync engine. Explicit consent additionally
binds immutable move/manifest identities with private `moves_sha256`; mutable
checkpoint phases are excluded from this digest. Recovery rehashes both target
and new safety archives and compares the selected target manifest before moves.

After pointer commit, restored ordinary properties are materialized while current
typed manager settings/private control credentials are preserved. Verified
historical seal, byte verification and backup receipt completion precede durable
coordinator completion and exact activation/risk/update marker cleanup. Startup
recovery resumes only the persisted original consent, including partial world
moves, committed pointers, completed receipts and interrupted terminal marker
cleanup. Neither rollback nor its recovery starts a game or automatically rewinds
data in response to an ordinary failed update.

## Deterministic evidence

The composed fixture uses real SQLite migrations, durable jobs, stopped scopes,
backup archives, world and local addon bytes, content descriptors/hashes, baseline
CAS, selection and pointer commits, readiness and launcher blockers. Its native
seal/runtime boundary is explicitly labelled fixture data through the approved
`VerifyAndSeal` injection; production wiring retains the full verifier.

Focused coverage includes:

- Historical world/region/config/JAR restoration, exact changed content metadata
  and pack baseline restoration, a nil prior baseline, retained local overrides
  and unknown disabled JAR provenance.
- Fresh administrator, revoked/disabled role, separate false/missing
  acknowledgement, wrong instance confirmation, expired preview and changed
  original durable job consent.
- New current safety metadata, inherited activation blocker, unsafe addon source,
  mixed world layout and no temporary old selection before safety/pointer commit.
- Missing/modified consent, changed archive bytes/hash, changed backup manifest,
  changed parent history, foreign marker and copied private recovery journal.
- Copied/expired lifecycle/content/pack capabilities, foreign transactions and
  historical proof drain blocking paused queue resume.
- Current memory retained while historical generation rows remain immutable;
  historical runtime verification failure leaves the current world untouched.
- Historical Forge candidate conversion replaces cached language evidence with
  fresh historical proof, preserves unknown flags, does not mutate history and
  refuses missing fresh evidence instead of falling back to cached evidence.
- Whole supplied transaction abort after world replacement: current selection,
  generation/content/baseline pointers remain committed as before, receipt remains
  `data_replaced` and native boot remains independently blocked.
- Interruption before world moves, durable admission with old marker, after
  pointer commit, after backup receipt completion and before terminal marker
  cleanup. Recovery never restarts the game and exact terminal replay is safe.
- Backup engine faults immediately after old/new rename, followed by fresh
  paused startup admission and persisted move checkpoint recovery.
- Administrator revocation after world moves prevents pointer commit and retains
  the boot blockers, reporting the partial operation honestly.

Red/green evidence: initial participant/coordinator tests failed on missing
production APIs. More significantly, the new changed-archive-bytes and
changed-backup-manifest regressions both first failed with "unsafe ownership
recovery accepted"; rehash/manifest/move-identity hardening made them pass. The
changed-original-job-consent regression first produced the same failure and now
refuses recovery. Terminal marker cleanup first failed with `sql: no rows in
result set`; exact completed history revalidation now permits cleanup safely.

Verification:

- `go test ./internal/lifecycle ./internal/content ./internal/modpack ./internal/backup ./internal/update -count=1 -timeout=120s`: PASS.
- `go test -race ./internal/update ./internal/backup ./internal/lifecycle ./internal/content ./internal/modpack -count=1 -timeout=180s`: PASS on frozen implementation (update 19.881s; backup 7.197s; lifecycle 9.568s; content 14.082s; modpack 11.900s).
- `go vet ./...`: PASS. Intentional copied-capability tests use explicit reflection
  copies of quiescent objects so the tests still exercise original-owner refusal
  without reporting ordinary unsafe Go lock copies.
- `go test ./... -count=1 -timeout=180s`: ran twice; both runs passed every
  package except the unrelated
  `notify.TestSMTPResponseReadCancellationClosesPeer`,
  `internal/notify/smtp_limits_test.go:201`, which returned `connection_failed`.
  The isolated exact test retry passed. This suite/order-sensitive failure was
  reported to the integrator. After rollback freeze, the integrator separately
  assigned only that cancellation test; its independent follow-up evidence is in
  `docs/reports/task20-cancellation-followup.md`. No repository-wide green claim
  is made; the integrator owns the final complete suite run.

## Limits and integration responsibilities

This evidence establishes deterministic coordinator behavior. It does not qualify
real game versions, game/client interoperability, native launcher/runtime
execution, real MSMP transitions, Windows execution, filesystem power-loss
behavior or stable support combinations. Existing instance participant tests own
private management transition/collision coverage. Forge/NeoForge proof uses the
existing historical immutable loader verifier with no fallback to the current
loader; real loader/game qualification remains outstanding.

The integrator must attach this adapter between update construction and job
registration, review normal/current admission composition and startup terminal
rollback dispatch, and accept evidence/update the ledger. The normal update
worker owns API/UI and terminal/risk-only recovery discovery. No package
completion or release qualification is claimed here.
