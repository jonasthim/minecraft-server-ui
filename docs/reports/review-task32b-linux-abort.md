# Task32B Linux activation diagnostics — independent review

2026-10-17. Specification **PASS**. Quality **APPROVE**. Zero open or new
findings. This approves the narrow diagnostic packet for an exact-commit hosted
rerun; it does not accept the Ubuntu24 activation failure or Task32B native
qualification.

## Reviewed scope

Reviewed `d733005..134be704bdd144c87325320d818206ef93797e5e` against the frozen
`.superpowers/sdd/2026-10-17-valheim-parity-completion/review-task32b-linux-abort.diff`.
The frozen diff is byte-identical to that commit range, and all six reviewed
working files match the submitted commit: `native.go`, `native_linux.go`,
`native_diagnostics_test.go`, the journal scrubber and its subprocess tests, and
`task32b-linux-abort.md`. Concurrent Windows and bootstrap work is outside this
review. The approved design, implementation plan, ledger, contributor rules,
and diagnostic report supplied the acceptance boundary.

## Specification and source findings

The failure markers are emitted synchronously by the worker's failure return
expressions, before deferred cleanup runs. The separate manager-stop error
branch emits its marker before creating its reconciliation context or acquiring
the manager lock. An eventual blocked manager Start or recovery dependency
therefore cannot suppress an already emitted stage. Existing final command
error reporting remains after worker cleanup.

All existing guards, fixed rejection errors, underlying returned errors,
activation phases, cleanup ownership, lock handling, rollback rules and timeout
budgets are preserved. Splitting the three combined operation/value guards
retains their original short-circuit behavior and checks. Executable publication
and subsequent recovery logic are unchanged. This is additive diagnosis of
existing failures, with no inferred behavior correction.

The diagnostic helper permits exactly 19 constant stage strings, omits nil
errors and unknown stages, and returns the original error unchanged. Only the
stage reaches the optional driver sink. The Linux sink emits the fixed prefix
and stage; no cause, path, request, nonce, actor, credential or journal body is
passed into that message. Windows has no sink added by this packet.

The scrubber's exact-message lookup has the same 19-stage allowlist. It adds
only `native_update_failure_counts`, preserving both existing count fields and
their behavior. Arbitrary message suffixes/prefixes, unknown stages, non-string
messages and other journal fields cannot become diagnostic keys or values.
The unchanged Linux harness collects the summary before killing the worker.

## Independent evidence

- `go test -race ./internal/selfupdate ./cmd/minecraft-ui-update -count=1`:
  PASS (`internal/selfupdate` 10.773s; updater command has no test files).
- `go vet ./internal/selfupdate ./cmd/minecraft-ui-update`: PASS.
- `python3 deploy/qualification/summarize_journal_test.py`: all three PASS.
- Python3.10 grammar parsing, equality of both stage allowlists, exact frozen
  diff/file checks, and scoped `git diff --check d733005 134be70`: PASS.
- Independently ran the ten blocked-cleanup regression cases against the
  original `d733005` activation implementation using a Go overlay. The unused
  new diagnostic helper was appended solely because other candidate tests
  reference it and otherwise would not compile; the original activation body
  was unchanged. Every case failed specifically because its expected marker
  was absent while actual cleanup Start was blocked. Thus these tests detect
  the original lost diagnostic, rather than merely testing the helper in
  isolation. The candidate race run passes the same cases and checks one
  marker, an error return, the unchanged prior executable, durable `aborted`,
  and no rollback attempt.
- Independently ran the new subprocess tests against the original scrubber:
  all three fail because the new count field is absent. Candidate subprocess
  tests also cover exact counts, retained existing fields and private decoys.
  Baseline experiment files are isolated under
  `/home/thim/.cache/dilla-tmp/task32b-review-7ake9s93`.

The retained scrubbed Ubuntu24 artifacts for actual run39035609290 confirm
the report's observation: `aborted`, rollback false, prior/candidate schema20,
manager inactive/dead with MainPID0 and exit0, and worker/recovery still
activating with PIDs2527/2544. The summary contains counts but no originating
stage. Neither those artifacts nor this package review establish which original
check failed. A recovery process can also publish an unpublished abort; the
terminal phase alone is insufficient to infer its cause.

## Acceptance limits

The next hosted run must use the integrated exact commit, retain the new fixed
stage on failure, and preserve all existing activation/recovery/data assertions.
Its result must be reviewed before assigning a behavior fix or claiming native
acceptance. No local native service, game, preview, account, firewall, database
operation or Actions dispatch was performed for this review. Only this assigned
review report was written in the repository; no implementation, staging,
commit or ledger mutation was made.
