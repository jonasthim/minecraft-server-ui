# Paused startup recovery admission

Implemented for integrator review; package acceptance remains with the integrator.

## Files and interface

- `internal/jobs/paused.go`: `Service.WithPausedInstance(ctx context.Context, instanceID string, fn func() error) error`.
- `internal/jobs/service.go`: active recovery count and atomic `Resume` guard.
- `internal/jobs/paused_recovery_test.go`: durable queue, concurrent admission, lifecycle, and cleanup regressions.

The new method admits callbacks only after successful `StartPaused`, while the
service is still paused and live. Retained queued jobs remain queued and retain
their exact input. Running rows and overlapping idle/recovery reservations return
`instance_busy`. The existing target reservation atomically blocks `Enqueue` and
ordinary `WithIdle`, while unrelated instances remain available. Ordinary idle
mutations continue rejecting queued and running jobs.

`Resume` returns `instance_busy` without enabling dispatch while any recovery
callback remains active, including callbacks for different instances. Callback
return never resumes dispatch implicitly. Successful repeated `Resume` calls
remain idempotent; `Start` retains its existing one-shot behavior.

Callbacks hold neither the queue mutex nor a SQL connection. The shared worker
drain retains reservations through deferred callback cleanup after cancellation,
errors, and panic unwinding; errors and panics propagate unchanged. `Stop` closes
admission and cancels the service, waits for callbacks, and honestly reports its
own deadline if cleanup has not finished. Recursive jobs/native guard calls are
documented as forbidden. Callbacks capture their caller context for I/O and must
finish their cleanup before returning; cancellation does not forcibly terminate
them.

## Evidence

Tests first failed because the recovery capability was absent. After the
implementation, all of these commands passed:

```text
go test ./internal/jobs -count=1
go test -race ./internal/jobs -count=1
go vet ./internal/jobs
```

New coverage includes a real queued job admitted through the API, retained after
closing and reopening a file-backed SQLite database, reconciled while paused,
and executed only after recovery returns and explicit resume. Tests also check
target enqueue rejection with unrelated admission, overlapping idle/recovery
rejection in both directions, multiple concurrent recovery callbacks, resume
denial without dispatch, callback cancellation/error/panic cleanup, shutdown
timeout followed by successful drain, input validation, and denial before
startup, after resume, after service cancellation, and during shutdown. Existing
jobs tests pass unchanged.

## Scope and remaining review

No endpoint, schema, instance, composition, or ledger files were changed. No
dependencies were added. Main/instance integration and acceptance review belong
to the integrator and lifecycle owner. These deterministic package tests do not
qualify real Linux/Windows or Minecraft recovery behavior.
