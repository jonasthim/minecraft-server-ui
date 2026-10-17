# Task31D2 backend and root integration review

2026-10-17. Independent review of `7b10b8a..6227cb2`, using the frozen packet `.superpowers/sdd/2026-10-17-valheim-parity-completion/review-task31d2-backend.diff` and backend brief. Current checkout packet source files match `6227cb2`; later unrelated work was excluded.

**SPEC: FAIL. QUALITY: REQUEST CHANGES.** One Important finding remains.

## R1 — Important: a successful save can create an unreadable, undiscardable draft

Location: `internal/instance/pending_settings.go:215–220` (serialization and persistence), together with the read limit at lines 108–109.

`SavePendingSettings` validates the typed configuration and marshals it with `json.Marshal`, but persists the result without enforcing the reader's 2 MiB maximum. Go's default JSON encoding expands literal `<`, `>` and `&` into six-byte HTML escapes. A valid HTTP request below the endpoint's 1 MiB body limit can therefore create a stored draft larger than 2 MiB.

Reproduced through the shipping `MountInstances` PUT/GET/DELETE handlers and migrated SQLite, with an authorized administrator and ordinary guard fixture: add 100 valid `extra` properties, each containing 5,000 literal `<` characters. The 501,834-byte PUT returns 200 and revision 1; stored JSON is 3,001,753 bytes. Subsequent GET and DELETE both return 503, `stored pending settings are invalid; administrator repair required`. The service-level reproduction independently confirms successful persistence followed by failed read/discard. Resaving and ordinary managed start also call the same reader, so the accepted request leaves no supported discard/rebase path and prevents a managed start until direct database repair. Saving itself does not affect the running process.

Make the saved representation and read budget agree before committing. For example, reject an oversized serialized draft with a validation error before its INSERT/audit, or adopt an encoding/budget that covers every accepted typed draft. Add a regression using a compact HTTP request with HTML-sensitive values that verifies either a usable save→GET→discard round trip or an atomic validation refusal preserving the previous draft, revision and audit state. Do not merely raise the read limit without accounting for the maximum possible serialization expansion.

## Reviewed scope and safeguards

All 17 packet files were reviewed: application composition and its race test; implementation ledger and corrective plan adjudication; OpenAPI and generated TypeScript contract; route registration, strict pending handlers and HTTP tests; migration 20; pending service and tests; stopped-update refactor and session binding; lifecycle preparation and tests; worker report. UI behavior and unrelated game harness/Windows diagnostic work were outside this packet.

The reviewed implementation otherwise keeps save/discard SQL-only under queue/recovery admission, validates fresh enabled administrators inside transactions, rejects omitted/null config booleans and unsafe revision inputs, and preserves world/version/runtime/port identity. Apply binds the original stopped session/execution, checks private current identity before SQL and durable job kind/payload/requester inside SQL, and consumes the exact draft revision atomically with active configuration and audits. The existing configuration intent, snapshot compensation and memory activation sequence remains shared. Ordinary preparation reloads actual detail; autostart policy preparation and strict update-start do not consume drafts. No additional reachable authority, queue-lock inversion, transaction or isolation defect was identified in this scoped review.

## Independent verification

Passed, exit 0:

```text
go test -race ./internal/instance ./internal/lifecycle ./internal/api ./cmd/minecraft-ui -run 'TestPendingSettings|TestOrdinaryStart|TestAutostartPreparationPreservesPendingSettings|TestUpdateStartPreservesPendingSettings' -count=1 -timeout=120s
  instance 7.368s; lifecycle 3.794s; api 1.376s; main 2.020s

go test ./internal/instance ./internal/lifecycle ./internal/api ./cmd/minecraft-ui -count=1 -timeout=120s
  instance 0.588s; lifecycle 0.502s; api 0.597s; main 0.347s

go vet ./internal/instance ./internal/lifecycle ./internal/api ./cmd/minecraft-ui
git diff --check 7b10b8a 6227cb2 -- <all 17 packet files>
```

Reviewer regression, expected failure, exit 1:

```text
go test -overlay=/home/thim/.cache/dilla-tmp/task31d2-review-ecwt89ez/http-overlay.json ./internal/api -run '^TestReviewHTTPPendingSerializedSizeRoundTrip$' -v -count=1 -timeout=60s
  request bytes=501834 save status=200
  GET after successful save 503: unavailable, administrator repair required
  DELETE after successful save 503: unavailable, administrator repair required
```

The HTTP and service regressions use temporary external Go overlays; repository implementation/tests were not modified. The existing focused tests are meaningful for SQL, queue and explicitly labeled native fixtures, but omit the serialization-budget round trip exposed above. These checks do not qualify real games, client connectivity, installed native services, Windows execution, supported release combinations or user preview. No services, accounts, EULA consent, vault content, staging or commits were changed. The integrator retains ledger and acceptance ownership.
