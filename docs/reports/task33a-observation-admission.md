# Task33A observation admission correction

Status: implementation and focused verification submitted for integrator review.
No stable, native gameplay, or exact hosted rerun acceptance is claimed.

## Finding and bounded correction

Hosted game run39038021693 atc41b2d6 reached an official Vanilla1.21.9 server
and actual client connection/reconnection before backup POST returned409
`instance_busy`. The run did not retain the identity of the competing observer.
Source investigation establishes the structural conflict: five read-only paths
used `WithIdle`, whose reservation intentionally refuses new queued mutations.
Deterministic delayed reads reproduce that refusal without launching a game.

`jobs.Service.WithObservation` now uses a distinct per-instance reservation,
initialized by `New` and protected by the existing admission mutex. It requires
a valid context, instance ID and callback, a live resumed service, no existing
mutation/observation reservation, and no queued/running same-instance job.
Enqueue remains unchanged and persists requests during observations. Dispatch
defers that instance until the observation's deferred release signals the queue.
Queued jobs therefore take priority over later observers. Unrelated instances
continue admission, execution and progress.

The callback runs without the admission mutex or a held SQLite connection and
must neither call queue methods nor mutate controlled server state. It captures
its existing bounded context. The reservation and worker drain membership last
through callback return, including cancellation and deferred panic cleanup.
Stop closes admission and honestly waits for existing callbacks. `WithIdle` and
`WithPausedInstance` reject observation overlap. Existing mutation admission,
save/archive reservations, job authorization and backup guards are unchanged.

## Read-only call audit

| Caller | Audited behavior preserved |
|---|---|
| `console.Refresh` | `session.verify(ctx, false)` reads native readiness and gamerules; the false apply flag prevents gamerule updates. Legacy `list` is observational. Readiness cache writes are manager observations. |
| `console.PlayerSnapshot` | Authenticated players RPC or legacy `list`, bounded command context, session scope, generation checks, identity/provenance and coverage remain. |
| `console.NativePlayerList` | Discovered read-only allowlist/bans/operators RPC via `Session.PlayerList`; no mutation RPC or saved-file mutation. Existing session and RPC guards remain. |
| `console.PlayerPositions` | Roster plus targeted `data get entity` Dimension/Pos/Dimension reads; four-second bound, 16-player cap, original captured generation and gap disclosures remain. |
| `players.Lists` saved fallback | Validated paths, pinned server root, stable JSON read, and native identity comparison before/after; no file mutation. Saved provenance and unverified-runtime status remain. |

Only these five reservation calls migrate. Lifecycle sessions continue using
the existing job mutation capability. No API schema, role gate, save/restore,
server configuration or release qualification boundary changes.

## Regression and verification evidence

- Before implementation, `go test ./internal/jobs ./internal/console ./internal/players`
  passed. The new `TestReadOnlyProbeAcceptsQueuedCommandDuringDelayedRPC` then
  failed against original production source in all four subtests (`refresh`,
  `snapshot`, `list`, `positions`) with `instance_busy: instance is being changed`.
- A temporary Go overlay restoring the original four caller source files from
  Git HEAD reproduces the same four failures plus
  `TestSavedListObservationAcceptsQueuedWork`, which fails with the same
  `instance_busy` admission refusal. The overlay does not modify working files.
- `go test ./internal/jobs ./internal/console ./internal/players -count=1`
  passed: jobs0.477s, console0.503s, players0.220s.
- `go test -race ./internal/jobs ./internal/console ./internal/players -count=1`
  passed: jobs15.039s, console18.590s, players11.205s.
- `go vet ./internal/jobs ./internal/console ./internal/players` passed.
- `git diff --check -- internal/jobs internal/console internal/players` passed.
- `go test ./...` passed every other package but failed
  `release/candidate.TestCandidateContract`: its unrelated temporary fixture's
  `go build` reports `error obtaining VCS status: exit status 128`. The same
  failure reproduced with
  `go test ./release/candidate -run TestCandidateContract -count=1`.
  No out-of-scope source or environment change was made to bypass it.

New jobs tests cover durable payload admission while dispatch is deferred,
same-instance mutation/observation exclusion, queued/running priority,
unrelated progress, lifecycle/argument validation, preserved callback errors,
caller cancellation and panic cleanup, shutdown timeout/drain, and deferred
release waking dispatch. Synchronous dispatch plus persisted state assertions
establish ordering without arbitrary sleeps. Existing idle and paused-recovery
tests also passed, retaining mutation admission refusal and recovery semantics.
Console tests use real authenticated local WebSocket RPC and the real queue,
delaying discovery while an authorized command is durably queued. Saved-list
tests use a real pinned filesystem read delayed at native generation checking.
Both verify queued work executes after the read completes.

## Frozen changed paths

- `internal/jobs/service.go`
- `internal/jobs/idle.go`
- `internal/jobs/paused.go`
- `internal/jobs/observation.go` (new)
- `internal/jobs/observation_test.go` (new)
- `internal/console/service.go`
- `internal/console/players.go`
- `internal/console/map_positions.go`
- `internal/console/observation_admission_test.go` (new)
- `internal/players/service.go`
- `internal/players/observation_admission_test.go` (new)
- `docs/reports/task33a-observation-admission.md` (new)

No staging, commit, dependency, live game or host service changes. Native
Windows execution and the exact authorized hosted game rerun remain separate
acceptance requirements for the integrator. No retry or weakened backup guard
has been introduced. The original run's specific competing observer remains
unknown; these regressions establish and correct the admission conflict common
to all five audited read paths.
