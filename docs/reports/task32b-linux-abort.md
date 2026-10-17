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

## Hosted diagnostic run39036772847 — schema probe failure established

[Run39036772847](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39036772847)
at `d0994d106c77421eb36983a79e4601aeb69d065d` again passed Ubuntu22 and failed
Ubuntu24. Job117408497554 failed in about four seconds after the installed
exercise began. Its scrubbed artifact was downloaded read-only to
`/home/thim/.cache/dilla-tmp/updater-39036772847-ubuntu24`.

The fixed diagnostic is now present:

```json
"native_update_failure_counts": {"schema_probe": 1}
```

This identifies `linuxDriver.ProbeSchema(ctx, false)` returning an error during
the genuine initial update. The preceding stop, manager-lock acquisition and
complete drain-receipt validation passed. It is distinct from returned
capability/schema/administrator rejection, which would emit `schema_validate`.
Candidate copying, probing and executable replacement were not reached.

The retained journal is `aborted`, schema20, prior `v0.0.1`, candidate `v0.0.2`,
and `rollback_attempted: false`. This time cleanup completed before the snapshot:
the old manager is active/running with MainPID2760 and Result `success`; the
worker has failed with exit1 and MainPID0, and the recovery unit is inactive/dead
with Result `success`. There are two manager-listening events. Prefix mode
restoration and owned-state cleanup are recorded, with `cleanup_checked: true`.
This proves successful recovery to the prior manager for this observed failed
activation; it does not qualify a successful Ubuntu24 update.

The precise underlying schema-probe error is **still not retained**. The current
adapter runs fixed `/usr/sbin/runuser -u minecraft-ui --
/usr/local/bin/minecraft-ui update-schema` with the fixed data-directory
environment. It discards stderr and reduces command failure/response overflow
to one error, or rejects malformed JSON separately. The outer diagnostic
intentionally emits only `schema_probe`. The artifact cannot establish whether
the launcher, manager probe execution, read-only database/schema/challenge/
authorization operation, output bound or JSON decoding failed.

No behavior correction was made from this evidence. A further diagnostic-only
scope should distinguish command launch/exit/signal, overflow and JSON failure;
if the manager probe exits, fixed child-operation markers can identify its
config, schema-read, challenge-read or administrator-read boundary. Error text,
paths, credentials and challenge/request values must remain omitted. A product
fix requires that evidence or a faithful failing reproduction; changing
permissions, replacing the privilege boundary or increasing waits now would be
speculation.

## Diagnostic-only follow-up after run39036772847

The integrator assigned a further bounded diagnostic packet, including the
root-owned command probe wiring as an explicit ownership exception. No
behavior correction or alternate privilege mechanism is included.

- `cmd/minecraft-ui/selfupdate.go` now marks a failure at config loading, schema
  read, challenge read, challenge version, actor authorization read or JSON
  encoding. It returns the same original error. Categories come only from
  `errors.Is` for permission, missing-file and context errors, a bounded numeric
  `(*modernc.org/sqlite.Error).Code()` from the existing direct dependency, or
  fixed `other`. No underlying error text is copied.
- `linuxDriver.ProbeSchema` retains its fixed runuser command, account, current/
  previous executable selection and environment. Its helper captures at most
  8KiB of stderr. On failure it forwards at most one exact authored child marker;
  all other stderr is discarded. Oversized stderr is never parsed. It emits
  fixed command launch, exit1–255, signal1–64, context, stdout/stderr overflow or
  invalid-JSON diagnostics before returning the original fixed adapter error.
  Successful probes emit no failure diagnostics and return the same capability.
- The journal scrubber adds `native_schema_probe_counts`, using exact stage/
  reason allowlists and canonical bounded decimal codes. Previous fields retain
  their meanings. Numeric SQLite codes must be1–65535; unknown, signed, padded,
  Unicode, oversized or appended payloads are rejected. No raw stderr, JSON
  body, path, request, nonce or credential is retained.

The markers use `native-update-schema-diagnostic:<stage>:<reason>` with a
numeric final field only for SQLite, process exit or process signal categories.
For example, an actual child `schema_read:sqlite:14` together with
`command:exit:1` would establish a typed SQLite failure in the read-only schema
operation. This is an illustration of the grammar, **not an observation from
the failed hosted run**. A nonzero command without a child marker would locate
failure below or before the authored probe boundaries; its exact lower-level
cause would still require further evidence.

Focused verification of this follow-up:

- Seven real subprocess probe fixtures failed before instrumentation and pass
  afterwards: config, missing DB, actual SQLite schema error, invalid challenge,
  mismatched challenge version, actual SQLite actor error and encoding failure.
  Wrapped permission/missing/context errors retain only their fixed category;
  unknown stages and nil errors emit nothing.
- Linux helper tests use real temporary child processes, without account or
  service operations. They establish fixed exit37, SIGTERM15, stdout/stderr
  bounds, missing/nonexecutable launch, canceled/deadline contexts, invalid JSON
  and successful typed output. Private stderr and malformed marker decoys are
  omitted, and the original fixed error messages are asserted.
- Temporary Go build overlays, without changing shared workspace files,
  suppress each diagnostic layer. The child-boundary regression then fails all
  seven cases; removing adapter outcome reporting fails the corresponding exit,
  signal, overflow, launch and context cases. The unmodified candidate passes.
- `go test -race ./internal/selfupdate ./cmd/minecraft-ui -count=1`: PASS
  (`13.946s` and `9.826s`). `go vet ./internal/selfupdate ./cmd/minecraft-ui`:
  PASS.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o
  /home/thim/.cache/dilla-tmp/schema-diagnostics-minecraft-ui.test.exe
  ./cmd/minecraft-ui`: PASS, compilation only; no Windows runtime claim.
- Four actual scrubber subprocess tests: PASS. The new schema-marker regression
  failed before implementation. Python3.10 grammar and scoped whitespace checks:
  PASS.

No local services, accounts, firewalls, games, preview or canonical installation
paths were changed, and no hosted job was dispatched. Independent review and a
subsequent exact-commit Ubuntu24 run remain required; the schema-probe root cause
and a concrete behavior fix are still pending that evidence.
