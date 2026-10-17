# Task30L5 installer transaction coordinator

Implemented the common source-level coordinator against the frozen
`internal/install/contracts.go` contract. The coordinator owns durable phase
ordering, option and journal validation, fixed stage errors, and rollback
decisions. Native adapters, local administrator checks, independent worker,
startup fencing, CLI wiring and native qualification remain root integration
work; this report does not claim a complete installer.

## Coordinator behavior

- `applyWith`, `uninstallWith` and `recoverWith` share a bounded driver protocol.
  Apply validates both clean absolute input paths and the exact stable version
  before the driver is called. The prepared target version must match that
  request. Stable image versions, positive schemas, lower-case SHA-256 values,
  transaction IDs, modes, phases and failure stages are validated at every
  loaded/prepared boundary.
- A prior committed or rolled-back journal is finalized before a fresh
  `Prepare`. Any valid nonterminal or malformed prior journal refuses a new
  operation without preparing inputs or invoking an effect. `recoverWith`
  finalizes terminal journals; every valid nonterminal phase, including
  `manual_repair`, gets at most one rollback attempt per call.
- The effect order is durable and explicit:
  `prepared → quiescing → Quiesce → stopped → replacing → Replace → Publish →
  published → verifying → Verify → committed → Finish`. `record_quiescing` is
  included in the stage allowlist as root requested. Failure to persist the
  initial prepared transaction is treated as uncertain publication: no stop,
  replacement or rollback is attempted.
- Post-prepared failures persist `rolling_back` before rollback under
  `context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)`, retaining
  caller context values while dropping caller cancellation/deadline. Rollback
  runs once. Failure to persist a transition or finish leaves recovery pending
  and never triggers cleanup of preimages. A committed `Finish` failure returns
  the accurate installed/uninstalled outcome with `FailedStage=finish` and does
  not roll back a healthy install.
- Driver transaction and image pointers are defensively copied at every call.
  Errors expose only a fixed authored stage from their `Error()` string while
  retaining causes for `errors.Is`/`errors.As`. No database or world restore
  behavior exists in this coordinator.

## Verification

- `go test ./internal/install -count=1` passed.
- `go test -race ./internal/install -count=1` passed.
- `go vet ./internal/install` passed.
- `GOOS=windows GOARCH=amd64 go test -c -o /tmp/install-coordinator-windows.test.exe ./internal/install` passed. This is a compile check, not native Windows qualification.
- `git diff --check` on the owned source/report paths passed.
- `go test ./...` passed across application, managerstate, installinputs and selfupdate packages, then failed in `release/candidate TestCandidateContract`: its fixture build returned `error obtaining VCS status: exit status 128` (suggesting `-buildvcs=false`). No release candidate source was changed in this packet.

The deterministic stateful fixture proves effect ordering, initial-journal
uncertainty, one-shot rollback, independent cancellation-resistant cleanup,
context-value preservation, fixed error redaction, committed cleanup behavior,
interrupted recovery, malformed journal refusal, version binding, and
defensive copies against a mutating driver. Those tests do not substitute for
native adapter tests or the required installed lifecycle workflow.

## Integrator acceptance

Review findings are closed. Root independently ran package race/vet with the
committed selfupdate hooks overlaid to exclude a concurrent, unfinished packet.
An overlay restoring caller cancellation to rollback fails
TestApplyCancellationUsesIndependentRollbackContextAndReturnsFailure. The new
rollback-record cases cover failures before and after uncertain publication of
rolling_back and rolled_back, preserve both causes, and forbid subsequent
rollback or Finish when their required durable transition is unconfirmed.
Source coordinator acceptance does not qualify any native installer lifecycle.
