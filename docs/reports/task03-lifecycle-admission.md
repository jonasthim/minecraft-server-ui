# Task 03 lifecycle admission integration

Status: **DONE** for the bounded jobs prerequisite. Independent review and composed lifecycle wiring remain with the integrator. This does not claim a complete install/start product workflow.

## Exact API semantics

### Paused startup

`Service.StartPaused(ctx context.Context) error` performs the existing single-manager recovery: previously running jobs become failed with their last phase retained; queued jobs keep their exact private payloads and ordering. It freezes registered handler kinds and opens normal durable enqueue/WithIdle/Stop admission, while preventing every queued handler from dispatching. WithIdle still rejects instances with queued/running jobs. Startup is single-use; a stopped service cannot be restarted.

`Service.Resume() error` enables dispatch on a live started service. Repeated Resume calls are idempotent while it remains live. Resume before startup, after parent-context cancellation, or after stopping begins returns `unavailable` and cannot reopen dispatch. `Service.Start(ctx)` composes StartPaused and Resume to preserve ordinary startup behavior. Paused enqueues remain durable; activation preserves per-instance serialization and the four-worker global bound.

### Private execution admission

`Execution.WithInstanceMutation(ctx context.Context, fn func(instanceID string) error) error` accepts only the original execution pointer created for the currently running job, while that handler's mutation admission remains open. Authority comes from an unexported identity bound to the private dispatcher job/instance, not writable public `Execution.Job.ID` or `InstanceID`. Nil, forged, copied, stale, or handler-returned execution objects return `forbidden`. Instance-less or invalid private instance IDs and nil callbacks return `validation_failed`. Overlap for the same execution returns `instance_busy`. A cancelled caller/current execution or stopping service cannot begin new mutation.

The callback receives the authoritative instance ID and runs without a held global queue mutex or SQL connection. It retains the current job's execution slot and per-instance serialization. Other instances can enqueue, journal progress/logs, complete, and cancel normally. Phase and Log now also use the private immutable job identity, so writable public metadata cannot redirect the journal; both remain usable inside an admitted mutation.

Only one callback per execution is active at a time. Return/error/cancellation/panic unwinding releases callback ownership; callback panics propagate after release and are caught by the ordinary handler panic boundary when on that handler's goroutine. Handler return closes further mutation admission and drains an already admitted callback before persisting terminal state or freeing the queue slot. This also covers an improperly detached child goroutine: an active callback keeps the job running and the next job queued. Stop reports its deadline while that callback remains active, then drains once it returns. If a detached callback fails after handler return, its error prevents the job from being reported as successful. Raw callback errors still receive the existing opaque public failure treatment.

This boundary is not a native lock. Compose **validated execution admission → supervisor.WithStopped exactly once → stopped instance session → materialization/artifact activation**. Do not recursively acquire queue/native guards inside the callback. Capture ctx for I/O and complete cleanup before return. Phase/Log may be called when the composed native operation does not create another lock cycle. Normal Service.WithIdle remains the idle-only admission boundary and does not bypass a running job.

## Files

- `internal/jobs/service.go`: paused startup/resume, dispatch gate, execution binding, handler-return close/drain.
- `internal/jobs/execution.go`: private identity and canonical journal job ID.
- `internal/jobs/mutation.go`: exact-pointer private authority, callback exclusivity, error/panic ownership release, detached drain.
- `internal/jobs/lifecycle_admission_test.go`: focused SQLite/concurrency tests.
- This report. No main, instance, native supervisor, API, or shared domain files were changed.

## RED / GREEN evidence

Before implementing the boundaries, `go test ./internal/jobs ./internal/events` failed with the expected missing-feature errors: StartPaused, Resume, and WithInstanceMutation were undefined. The same new tests passed after implementation.

Meaningful tests exercise:

- Paused recovery of interrupted work and unchanged queued payload/order, including new durable paused enqueue; no handler before activation; frozen handler registration; WithIdle while paused; repeated Resume.
- Cancelled/stopped Resume and Stop while paused never execute retained queued jobs. Existing ordinary Start tests continue to pass.
- Exact original execution authority, nil/forged/copied/stale rejection, instance-less denial, public metadata tampering, and canonical Phase/Log job identity.
- Same-execution overlap denial, other-instance phase/log/completion during a held callback, a real DB mutation using the single available connection, and callback error/panic/cancellation release.
- Handler-return admission closure, denial of late detached calls, retained running/queued state while an admitted detached callback remains active, next-job execution after callback return, honest Stop timeout/drain, and failure of a detached callback after handler return.

Final commands/results:

```text
go test -race ./internal/jobs ./internal/events ./internal/api
PASS: jobs 2.956s; events/API cached passing results

go vet ./internal/jobs
PASS
```

No unrelated full repository builds or suites were rerun while other workers were editing. No subagents, staging, or commits.

## Composition example

```go
queue := jobs.New(database, logger)
if err := queue.Register("instance-install", func(ctx context.Context, execution *jobs.Execution) error {
    return execution.WithInstanceMutation(ctx, func(instanceID string) error {
        // Exactly one native stopped guard. The composed stopped session then
        // performs materialization/activation without reacquiring queue/native.
        return native.WithStopped(ctx, instanceID, func() error {
            return materializeStoppedInstance(ctx, instanceID, execution)
        })
    })
}); err != nil { return err }
if err := queue.StartPaused(managerContext); err != nil { return err }
if err := reconcileNativeAndInstanceState(managerContext); err != nil { return err }
if err := queue.Resume(); err != nil { return err }
```

The stopped-session functions above are illustrative composition responsibilities, not APIs added by this package. The integrator owns their exact signatures and wiring. A shutdown failure path must still call Stop with an independent bounded cleanup context.

## Remaining limits

No native lock, stopped-session implementation, startup reconciliation, provider materialization, API producer, or browser lifecycle flow is added here. Handlers remain responsible for context-aware I/O and joining their goroutines; draining is defensive protection, not permission to detach work. A callback that never returns keeps its queue slot and produces an honest Stop timeout. Unrecovered panics in arbitrary user-created goroutines retain normal Go process semantics. These files are frozen for independent review.
