# Task32B Windows pipe correction — independent review

Reviewed 2026-10-17: `a363451..d733005`, limited to
`native_windows.go`, `native_pipe_windows_test.go` and
`task32b-windows-pipe.md`. The frozen `review-task32b-pipe-fix1.diff` exactly
matches that commit diff; all three working files match `d733005`. The approved
design/plan, ledger and prior diagnostic review remain the review boundary.

**SPEC: PASS. QUALITY: APPROVE.** Zero Critical, Important or Minor findings.
Approval covers this source correction and its native test assertions. Updated
Windows execution and installed-updater acceptance remain pending.

## Behavior and security evidence

- `internal/selfupdate/native_windows.go:750`: only `ERROR_NO_DATA` gains a reset
  path. `DisconnectNamedPipe` is checked; its failure is returned with its native
  cause intact. Other unexpected connection errors still return immediately.
  The existing caller closes the handle on failure and produces fixed stage-06
  SCM diagnostics; no raw error, client command or sensitive value is newly
  logged or reported.
- The reset uses the same held server handle. It neither closes/recreates the
  pipe nor opens a gap in first-instance name ownership. Creation flags, private
  DACL, remote-client refusal, account receipt, manager SCM PID/image validation
  and fixed command authorization are unchanged. A stale connection is reset
  before it can reach command handling. Microsoft documents both the need to
  disconnect before reconnecting and the pipe's continued existence while its
  handle remains open: [DisconnectNamedPipe](https://learn.microsoft.com/en-us/windows/win32/api/namedpipeapi/nf-namedpipeapi-disconnectnamedpipe).
- Cancellation is checked before every connect attempt and during the unchanged
  50 ms poll wait. The helper owns no handle close, so production still closes
  exactly through its existing caller. The nil-success branch is unchanged;
  the reported native idle test passed, and this patch does not infer another
  runtime defect from general API possibilities.

## Native assertion review

- `internal/selfupdate/native_pipe_windows_test.go:112` preserves the direct
  native closed-client assertion and exact `ERROR_NO_DATA` diagnostic. It is
  distinct from the recovery test, so the original failure state remains visible.
- At line 132, the recovery regression creates/closes an early client, invokes
  the shipping helper, admits a fresh client and requires exact `status` bytes
  through the same server handle. It retries only `ERROR_PIPE_BUSY`, uses a
  two-second context and rejects a helper error or lack of progress. Returning
  the old `ERROR_NO_DATA` cannot satisfy the test. The buffered result channel
  avoids a blocked completion send; cancellation and existing handle cleanup
  remain in place. Local unique names/current-identity DACL avoid production
  service, account and pipe state.
- Lines 190 and 199 require pre-cancelled acceptance to return cancellation and
  an invalid handle to return `ERROR_INVALID_HANDLE`, respectively. They cannot
  become green by accepting arbitrary transport errors or waiting out a deadline.
- At line 207, collision accepts only `ERROR_ACCESS_DENIED` or the actually
  observed `ERROR_PIPE_BUSY`, requires `INVALID_HANDLE_VALUE`, checks the exact
  diagnostic for either result, then transfers a fixed command through the
  original server. This accommodates the observed single-instance refusal
  without accepting a second server or damage to the original instance.

## Verification and limits

Independent Linux-host checks:

- Frozen diff and reviewed-file identity checks: PASS.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o
  /home/thim/.cache/dilla-tmp/review-task32b-pipe-fix1.test.exe
  ./internal/selfupdate`: PASS, compilation only.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet ./internal/selfupdate`: PASS.
- `go test -race -count=1 ./internal/selfupdate`: PASS, 10.656 s;
  Linux-applicable tests only.
- Scoped `git diff --check a363451 d733005`: PASS.

Package commands ran in the composed workspace, which contains parallel Linux
diagnostic/source changes outside this review. Their implementation is not
approved by this report. The three reviewed files were independently verified
against the frozen commit.

The supplied installed-run evidence identifies stage 06 / `ERROR_NO_DATA` as the
Windows failure. The source report distinguishes that actual observation and the
prior collision expectation failure from the correction's still-pending native
execution. No green updated Windows suite, successful installed recovery or
release qualification is inferred from Linux execution or cross-compilation.

Only this review report was authored; no implementation, ledger, service,
account, firewall, preview or game state was changed. Nothing was staged or
committed and no Actions run was dispatched. The integrator owns acceptance and
dispatch.
