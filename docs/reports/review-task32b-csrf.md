# Task32b CSRF correction review

SPEC: **PASS**

QUALITY: **APPROVE**

Reviewed `f5668b6..10e594b` using the frozen
`.superpowers/sdd/2026-10-17-valheim-parity-completion/review-task32b-csrf.diff`.
Scope is the owned `updater_api.py` change, new `updater_api_test.py`, and the
CSRF correction addition to `task32b.md`. The review used contributor rules,
corrective-plan Task32, and the recorded run39033415308 failure. Earlier harness
work and unrelated working-tree changes are outside this review.

## Findings

No blocking correctness, security, or maintainability findings in this packet.

All three installed activation requests now call `apply_update`. Each call
fetches `/api/v1/auth/me` through the same authenticated urllib client before
sending the returned token as `X-CSRF-Token`. The shipping handler returns the
token derived from that session cookie and compares it for authenticated cookie
mutations. No fabricated token or authorization bypass is introduced. Missing
tokens fail before mutation; stage ID, explicit confirmation, expected HTTP202,
and subsequent installed activation/recovery assertions are preserved.

The helper removes stale CSRF headers case-insensitively from its temporary
header list and restores the original list in `finally`, including HTTP failure.
It does not log or persist the token. The shared request helper reports only a
fixed path/status diagnostic for rejected HTTP responses; the new helper adds a
fixed missing-token diagnostic. No token, credentials, cookie, or response body
is added to reports. Shared request code and product enforcement are unchanged.

The fixtures exercise the real urllib request/header construction and shared
request helper with a socket-free transport. They cover missing and wrong-session
tokens, two distinct sessions, stale mixed-case header replacement, exact
activation payload, success/failure header restoration, absence of output, and
missing-token refusal before POST. Their fake authorization rule is appropriately
identified as client-construction evidence, not proof of product enforcement or
native activation. The existing shipping auth test supplies separate enforcement
evidence.

## Verification

- `python3 -B deploy/qualification/updater_api_test.py -v`: all four tests pass.
- `go test ./internal/auth -run '^TestHTTPRequiresCSRFAndRejectsCrossOrigin$' -count=1`:
  passes; retains missing-token and cross-origin rejection and valid-token access.
- Python AST parsing with `feature_version=(3,10)` for both owned Python files:
  passes.
- `git diff --check f5668b6 10e594b -- deploy/qualification/updater_api.py deploy/qualification/updater_api_test.py docs/reports/task32b.md`:
  passes.

No local native operations were performed. The report correctly retains pending
installed activation qualification on all three targets. Approval permits this
scoped harness correction to proceed to the integrator's combined hosted rerun;
it does not establish successful executable activation, rollback, or recovery.
