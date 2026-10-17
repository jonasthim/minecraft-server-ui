# Task32B Windows availability diagnostics — independent review

Reviewed 2026-10-17: `c41b2d6..9969bb7`, all seven files in the frozen
`task32b-windows-availability-review.diff`. The frozen diff was verified against
the commit diff, and all seven reviewed working files match `9969bb7` exactly.
The approved design, plan, ledger and preceding Task32B reviews remain the
product/security boundary. Unrelated bootstrap/workflow work is excluded.

**SPEC: PASS. QUALITY: APPROVE.** Zero Critical, Important or Minor findings.
This approves diagnostic source and manager composition. It does not establish
the remaining native cause or a runtime correction.

## Evidence

- `internal/selfupdate/service.go:71`: unconfigured trust and development builds
  still return before any native probe. The optional adapter at line 94 obtains
  availability and reason through exactly one detailed observation with the
  caller's context. It preserves nil/legacy fallback, supplies the old generic
  fallback when the reason is empty, and clears reasons on success. Status never
  requests activation. No public API shape or authorization rule changes.
- `internal/selfupdate/native_windows.go:368`: detailed availability is stateless
  and retains canonical-installation and native-trust prerequisites. It then
  performs the existing status call once. `ready` is the only successful reply;
  identity/I/O errors take precedence over even a ready-looking response.
- `internal/selfupdate/native_windows.go:159`: SCM access remains CONNECT and
  QUERY_STATUS only, process access remains QUERY_LIMITED_INFORMATION, and
  running state/nonzero PID, exact expected path, actual file identity and pipe
  server PID binding remain required. Extraction into process/pipe helpers
  preserves their checks and handle ownership. Successful handles retain their
  existing deferred closes; failed service opens disconnect SCM.
- `internal/selfupdate/native_windows.go:239`: pipe names, access flags, fixed
  commands, sizes, message mode, three-second context and 50 ms polling remain
  unchanged. Error wrappers retain causes through `Unwrap`; deadline wrappers
  preserve cancellation and the native error together. A complete write is still
  required. Activation still requires the exact `accepted` reply. The updater's
  DACL, first-instance/remote refusal, pre-command client PID/image check and
  activation authorization remain unchanged.
- `internal/selfupdate/native_windows.go:339`: diagnostic text contains only the
  fixed stage and a fitting 16-bit Win32 code. Invalid stages become internal,
  oversized native statuses are omitted, and raw errors, paths, identities,
  requests and payloads are not formatted. At line 414, wire parsing requires
  exact length, prefix, lowercase round-trip encoding, code family and one of
  the four server-only stages. Unknown strings/stages become the fixed
  unexpected-reply category, never availability or activation permission.
- `internal/selfupdate/native_windows.go:933`: only the authorized status
  command's existing four failure checks gain fixed replies. Each still rejects
  readiness. A rejected pre-command identity still gets `refused`, without
  reading a command or revealing the failed identity subcheck.
- `deploy/qualification/updater_api.py:107`: the artifact parser full-matches
  only the authored diagnostic format and an explicit 28-stage allowlist. It
  retains a fixed category/stage and numeric code, preserving existing primitive
  and fixture identity filtering. Arbitrary reason text, unknown stages, another
  family, altered case, suffixes and newlines are not retained. Artifact
  permissions and private-journal handling do not change.

## Independent verification

- `python3 -B -m unittest discover -s deploy/qualification -p updater_api_test.py
  -v`: PASS, 12 tests.
- Loaded the baseline sanitizer from `c41b2d6` in memory and ran the new authored
  diagnostic regression: it fails with `unrecognized`, as expected. The same
  regression passes on the candidate.
- Ran the actual new single-observation regression with only `service.go`
  overlaid from `c41b2d6` using Go's temporary overlay: it fails with the expected
  observation-count assertion. Candidate tests pass. No implementation file was
  edited for this negative control.
- `go test -race -count=1 ./internal/selfupdate`: PASS, 14.271 s, Linux-applicable
  tests, including adapter/fallback/configuration-gate regressions.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o
  /home/thim/.cache/dilla-tmp/review-task32b-windows-availability.test.exe
  ./internal/selfupdate`: PASS, compilation only.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet ./internal/selfupdate`: PASS.
- Scoped commit `git diff --check` and frozen-file checks: PASS.

Package commands used the composed workspace; the seven reviewed files were
separately matched to the frozen candidate. Other composed implementation is
not approved by this report. Windows-only tests have not executed on this Linux
host. Their assertions cover fixed diagnostics/privacy, error precedence,
strict reply parsing, cancellation/canonical guards and actual read-only
process/invalid-handle routing; they do not install services/accounts or open
the production pipe.

The supplied native evidence establishes a running updater with unavailable
manager activation, and the prior pipe tests' native success. It does not select
this remaining failure's cause. The worker report preserves that distinction
and labels its process-open JSON as an example. A reviewed native run must
execute these new tests and retain the authenticated diagnostic before a cause,
fix or installed Windows acceptance is claimed.

Only this review report was authored. No local native services, accounts,
firewall, preview or game state was changed; no Actions run was dispatched and
nothing was staged or committed. Ledger/integration acceptance remains with the
integrator.
