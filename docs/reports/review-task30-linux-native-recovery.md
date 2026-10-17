# Review: Task 30 Linux native recovery

## Finding closed

The initial review found that terminal operation directories accepted unknown
or pending children when the journal was absent. The native change now checks a
bounded allowlist of terminal operation entries before accepting the retained
result. Pending and unknown entries cause recovery-required/manual-repair and
remain untouched. A focused fixture covers `effects.pending`,
`result.json.pending`, and an unknown file.

## Reviewed areas

- Explicit `RecoverPending` is reached through the recovery entry, and ordinary
  load/check/apply do not promote protected pending files.
- Pending effects are strict-decoded, canonical, operation-bound, checked as a
  single valid transition, and promoted only after actual asset state matches
  the recorded publication/completion.
- Updater reconciliation is marked only after reconciliation succeeds. During
  rollback, the marker prevents a second snapshot after native effects begin;
  before effects begin, recovery validates captured inventory/assets and
  reconciles before restoring or restarting the manager.
- Pending target inventories during rollback are now admitted only when bound
  to the immutable plan and effect evidence. Recovery promotes the exact
  publication when planned assets remain published, or removes that exact
  pending file after rollback has demonstrably started, with destination
  identity/content CAS checks.
- First-install retained failure inventory validation disallows deployment
  assets, validates bootstrap trust provenance, and requires valid account,
  data, service, and receipt evidence before publication.
- Rollback preserves unrecorded replacement assets and does not restore the
  manager database or world data.
- The follow-up recovery tests prove exact terminal result pending promotion,
  preserve foreign result bytes, promote a valid target inventory, and remove
  an exact target inventory pending file only after rollback has started.

## Verification and limits

Passed:

- `/home/thim/.local/go/bin/go test ./internal/install ./internal/installinputs`
- `/home/thim/.local/go/bin/go test -race ./internal/install ./internal/installinputs`
- `/home/thim/.local/go/bin/go vet ./internal/install ./internal/installinputs`

The suite also covers effect-marker pending promotion and the fresh
prepare/load/quiesce/replace composition. It does not directly exercise a
retained-status inventory pending file. This review did not run systemd, create
host accounts, or claim native Ubuntu qualification.

**Verdict: Approve the reviewed native recovery changes, subject to the
integrator's combined review and hosted Ubuntu qualification.**
