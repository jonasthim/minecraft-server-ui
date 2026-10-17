# Task32B diagnostic packet — independent review

Reviewed 2026-10-17: `1aad4cd..28047a8` against the approved design, implementation
plan, ledger, contributor instructions and `task32b-windows-pipe.md`. The frozen
`review-task32b-diagnostics.diff` was verified byte-for-byte against that commit
diff. Scope is its six files only; unrelated pending-settings/game work was not
reviewed or changed.

**SPEC: PASS. QUALITY: APPROVE.** No Critical, Important or Minor findings in this
diagnostic source packet. This approves the bounded diagnostic instrumentation
and harness correction, not a Windows runtime fix or installed-update acceptance.

## Evidence supporting the verdict

- `internal/selfupdate/native_windows.go:542`: SCM encoding uses fixed operation
  values and only a fitting 16-bit native error. Unknown errors use a fixed stage;
  oversized native statuses are not truncated. Error text is fixed while
  `Unwrap` preserves the original cause for `errors.Is`/`errors.As`. Recovery,
  recovery deadline, account loading, descriptor, creation and connection failures
  receive distinguishable numeric codes without copying paths, SIDs, requests,
  tokens or payloads.
- `internal/selfupdate/native_windows.go:679`: the production account-receipt
  validation, DACL, pipe name, remote-client refusal, first-instance requirement,
  sizes, wait mode, verified SCM PID/image checks and command authorization are
  unchanged. Creation/connection helpers contain the original operations and
  branches. Connection errors still close the handle and fail the service;
  cancellation still closes the handle and returns normally. No native error
  gains a new retry/ignore path.
- `internal/selfupdate/native_pipe_windows_test.go:44`: native tests call the
  shipping helpers with unique local pipe names and a DACL limited to the test
  identity. They use actual Win32 calls, verify fixed-command bytes, disconnected
  client and duplicate-server errors, and validate nested error encoding. They do
  not open the production pipe or install a service/account. The idle test at
  line 84 explicitly rejects premature success; it can expose an existing bug
  instead of treating any native result as success.
- The named-pipe assertions agree with Microsoft's documented connected/closed
  client states and first-instance collision behavior. Nonblocking availability
  is distinct from an established connection; retaining the existing nil-result
  branch therefore leaves a meaningful native failure possibility. This is not
  evidence that it caused run 39034369430. Sources checked during review:
  [ConnectNamedPipe](https://learn.microsoft.com/en-us/windows/win32/api/namedpipeapi/nf-namedpipeapi-connectnamedpipe)
  and [CreateNamedPipe](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-createnamedpipea).
- `deploy/qualification/updater_api.py:106`: initial status retains only fixture
  versions, bounded schema, strict booleans and fixed reason categories.
  `write_journal_evidence` at line 143 bounds input to a 16 KiB journal plus a
  one-byte oversize probe and emits only allowlisted phases, validated hashes,
  fixture identities, bounded schemas and a strict rollback flag. Unknown values,
  malformed/oversized state and I/O failures become fixed classifications. Nonce,
  actor, timestamps, arbitrary errors and extra fields are not retained. The new
  chmod entries expose only these scrubbed reports, not private activation state.
- `deploy/qualification/updater_api.py:195`: the terminal phase assertion now runs
  outside the transient retry loop. A same-stage unexpected terminal fails
  immediately; partial JSON remains retryable and the 310-second bound and exact
  expected phase remain enforced. Evidence is retained in `finally`. Reporting
  failure still fails the harness with fixed text. Existing CSRF, identity,
  executable hash, rollback and data-preservation checks are unchanged.

## Independent verification

- `python3 -B -m unittest discover -s deploy/qualification -p updater_api_test.py
  -v`: PASS, 10 tests. These include real temporary journals, controlled time,
  partial JSON recovery, immediate unexpected-terminal failure, timeout evidence,
  input bounds, primitive typing and omission of private fields.
- Executed the new unexpected-terminal regression against the baseline module
  loaded from `1aad4cd`: it fails because the terminal rejection becomes
  `native updater terminal state timed out`. The candidate regression passes.
- `go test -race -count=1 ./internal/selfupdate`: PASS (Linux-applicable tests).
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o
  /home/thim/.cache/dilla-tmp/review-task32b-diagnostics.test.exe
  ./internal/selfupdate`: PASS, compilation only.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet ./internal/selfupdate`: PASS.
- `bash -n deploy/qualification/linux-updater.sh` and scoped commit-diff
  `git diff --check`: PASS.

## Remaining native gate

Windows pipe tests were not executed on this Linux host. In particular, the
unchanged connection logic may fail the idle test; approval must not be recorded
as a green Windows suite. Native execution must establish the actual failing
operation/Win32 code and Ubuntu 24.04 terminal or last journal phase before a
runtime cause or correction is claimed. The source report preserves these
limits and does not claim activation, rollback, interruption recovery or release
qualification from cross-compilation.

No local services, accounts, firewall, preview or game state was changed; no
Actions run was dispatched. Only this review report was authored. Integration
acceptance and ledger updates remain with the integrator.
