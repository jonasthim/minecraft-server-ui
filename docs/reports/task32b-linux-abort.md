# Task32B Ubuntu 24 activation abort — bounded diagnosis

2026-10-17. Actual evidence: [installed updater run 39035609290](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39035609290)
at `02b554d797e543452fb99ff7ffc66c340caae73c`, Ubuntu 24 image
`20261012.334.1`, systemd `255.4-1ubuntu8.17`. The existing scrubbed artifact
copy under `/home/thim/.cache/dilla-tmp/updater-39035609290-ubuntu24` was read
without changing host installation state or dispatching another run.

The actual cause remains **unknown within the checks before executable
replacement**. This packet improves diagnosis; it does not fix or accept the
observed activation failure.

## Established observations

- Initial update status reports fixture `v0.0.1`, schema20, configured trust and
  available native activation.
- The retained journal reports `aborted`, prior `v0.0.1` and candidate `v0.0.2`
  at schema20, and `rollback_attempted: false`.
- The manager unit is `inactive/dead`, MainPID0, Result `success`, exit0.
  The update worker (PID2527) and recovery unit (PID2544) are both still
  `activating/start` when the harness captures the failure snapshot.
- The journal summary has one manager-listening event and priority counts,
  but no originating updater failure stage. It cannot distinguish a stop error
  from the drain, live schema/authorization, scratch, copy, fence or candidate
  probe checks that can abort while the prior image remains installed.

Source review identifies two owners that persist `aborted`: stop-error
reconciliation, and deferred cleanup when a check fails with the prior image
still present and the in-memory phase `prepared` or `stopped`. Both then attempt
to start the prior manager. The active recovery process is compatible with that
start dependency; its presence does not establish the original failing check.
Ordinary `recover` can also publish an abort for an unpublished journal, so the
terminal phase alone must not be treated as a complete error trace.

The harness captures journal/unit summaries before killing the still-running
worker. The command's existing final error print happens only after its cleanup
returns. A failure stage can therefore be lost even when an abort is already
durable. The older run39034369430 timeout and Ubuntu22's successful cases do
not establish the Ubuntu24 failure cause.

No new service installation/restart, account/firewall change, preview or game
execution was used to reproduce this hosted failure. There is no native local
reproduction of its exact cause in this packet.

## Narrow diagnostic change

`activationWorker.failActivation` accepts an allowlisted fixed stage and returns
the original error. Calls sit directly at failure returns before replacement;
the stop-error branch calls it before starting reconciliation. Go evaluates the
return expression before deferred cleanup. The stage is therefore emitted
before a cleanup hash, save, start or recovery dependency can block.

The Linux driver writes exactly `native-update-failure:<fixed-stage>` to stderr,
which the installed systemd unit collects. No error text, path, nonce, actor,
request, credential or journal body is passed to the sink. Unknown stages and
nil errors produce no diagnostic. Windows has no diagnostic sink in this packet.

The three combined checks for drain, current schema and candidate capability
now distinguish an operation error (`drain_read`, `schema_probe`,
`candidate_probe`) from rejected returned values (`drain_validate`,
`schema_validate`, `candidate_validate`). They return the same existing fixed
error messages and use the same cleanup paths. The other stages distinguish
stop, lock, journal, scratch, prior/candidate copy, hash and fence operations.
No guard, timeout, retry, activation phase, rollback condition or terminal
assertion was relaxed.

The shared journal scrubber preserves its two existing count fields and adds
`native_update_failure_counts`. It accepts only exact messages for known stages;
unknown stages and any leading/trailing payload are omitted. The existing Linux
harness already runs this scrubber before killing the worker, so its control
flow and cleanup require no changes. The updater API helper is unchanged.

## Verification

- New Go regression failed against the original implementation in all ten
  injected failure cases because no stage was available before cleanup. It
  exercises actual worker activation with temporary file roots and a held
  cleanup Start callback. The candidate reports exactly one expected fixed stage
  before the callback resumes for stop, drain operation/value, schema
  operation/value, stopped-journal fence, scratch, candidate copy, executable
  fence and candidate capability rejection. Every case still returns an error,
  retains the prior executable and persists `aborted` without rollback.
- Success emits no failure marker. Separate checks verify unknown-stage and
  nil-error omission, original-error identity and absence of private cause text.
- The three new subprocess tests for the actual Python scrubber failed before
  implementation and pass afterwards. They verify count retention, exact known
  markers and omission of private decoys, unknown values and appended payloads.
- `go test -race ./internal/selfupdate ./cmd/minecraft-ui-update -count=1`:
  PASS. `go vet ./internal/selfupdate ./cmd/minecraft-ui-update`: PASS.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o
  /home/thim/.cache/dilla-tmp/linux-abort-selfupdate.test.exe
  ./internal/selfupdate`: PASS, compilation only. It does not qualify Windows
  native behavior; another worker owns the concurrent Windows correction.
- Python3.10 grammar checks and scoped whitespace checks: PASS.

Independent source review and a subsequent exact-commit hosted run remain
required. The next failed run must be interpreted using its actual fixed stage,
then reproduced or traced further before a behavior fix is proposed. Task32B
acceptance and implementation-ledger changes remain with the integrator.
