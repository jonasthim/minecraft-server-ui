# Task33A Linux stop probe diagnostics

## Candidate evidence

Run `39052371244`, source `329622eb04201b4f56fbed9fb506adde02ebb4a0`, completed
real Ubuntu 24.04 / Vanilla 1.21.9 client connections and online backup, then
failed the deliberate stop job with `stop_process_probe_failed` and
`stop_native_os_error_unavailable`. The bounded summary retains a matching
launcher identity and a deliberate, non-forced exit with code 0. It does not
retain the failed probe API or its error category.

Source tracing shows that the initial stop probe passed; otherwise the fixed
category would be `initial_probe_failed`. After the supervisor sends `/stop`,
`waitExit` immediately classifies any later `probe` error as
`process_probe_failed`. The later clean launcher exit record does not identify
which probe step failed. The unavailable OS number means the preserved cause
chain did not contain a bounded `syscall.Errno`; it does not imply an OS error
was lost from a known syscall.

The retained artifact cannot establish the cause of this run. The added stage
labels do not change identity checks, broker requirements, recheck decisions
for process identity, stop retries, or deadlines. One classification boundary
is intentionally safer: a broker installation-path `ENOENT` is now wrapped as
`broker_path`, so `os.IsNotExist` cannot mistake it for proof that the JVM
exited after an ambiguous live-identity recheck.

## Fixed stage coverage

Linux process identity failures now carry one of the existing finite stages:
`boot_identity`, `proc_stat`, `proc_executable`, `proc_working_directory`,
`broker_path`, `broker_invoke`, `broker_decode`, `broker_identity`, or
`process_metadata`. The existing lifecycle diagnostic path emits the stage via
its fixed `Native stop process API` field. It emits a native number only when
the actual cause unwraps to a nonzero `syscall.Errno` no greater than 65535.
No raw error text is added.

Direct `/proc` errors that mean the process is missing remain unstaged and retain
their existing classification. In particular, a missing `/proc` boot identity
remains an unavailable `boot_identity` failure; it is not treated as proof that
the JVM exited. Broker errors keep their original stage when a post-broker
identity recheck is ambiguous. A direct recheck that conclusively observes the
process missing continues to report exit without replacing that result with
the broker error. Stage wrappers preserve `errors.Is` causes and context
cancellation.

## Deterministic tests

Fixtures inject failures at each direct `/proc` boundary and at broker path,
invocation, decoding, and identity-validation boundaries. They assert the fixed
stage and preserved cause. Existing broker-recheck cases now assert that a
same-identity, permission, malformed-read, or cancellation outcome retains the
original stage; a conclusively missing process remains an unstaged exit result.
A broker-path `ENOENT` with a same-identity recheck retains `broker_path` and is
not treated as process exit. A live metadata mismatch reports
`process_metadata`.

These local tests verify finite source diagnostics only. They do not reproduce
the hosted stop failure or qualify Linux native lifecycle behavior. A hosted
run is still needed to see which fixed stage, if any, appears on another
failure.

## Verification

- `go test ./internal/launcher -count=1` — passed.
- `go test -race ./internal/launcher -count=1` — passed.
- `go vet ./internal/launcher` — passed.
- `go test ./internal/supervisor -count=1` and `go vet ./internal/supervisor` — passed.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/launcher-linux-stage.test.exe ./internal/launcher` — passed (compile check only).
- `git diff --check` on the owned launcher and report paths — passed.
