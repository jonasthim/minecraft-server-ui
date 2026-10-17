# Task31D2 backend — correction round 1 review

2026-10-17. Independent scoped re-review of `f6336ac..f413f95`, using `.superpowers/sdd/2026-10-17-valheim-parity-completion/review-task31d2-backend-fix1.diff`. Reviewed all four correction files against original Important finding R1 and checked for regressions introduced by this fix. This does not reopen unrelated packet scope.

**SPEC: PASS. QUALITY: APPROVE. Addressed: 1. Open: 0. New findings: 0.**

R1 is closed. `maxPendingSettingsBytes` now supplies the same 2 MiB limit to the stored-draft reader and save path. Save checks the actual marshaled bytes before changing the row, revision or audit, and returns the ordinary validation error. The reader budget remains unchanged. No schema, lifecycle, admission or authorization behavior changes are introduced.

The service regression covers both an absent row and an existing draft, asserting unchanged state and audit counts plus a usable discard after refusal. The shipping HTTP regression proves a compact 502,358-byte request expands into a 3,002,271-byte draft and receives 422, preserves the exact previous draft and audit state, and leaves that draft readable/discardable. A smaller configuration containing `<>&` still completes save, read and discard.

Independent verification, exit 0:

```text
go test -race ./internal/instance ./internal/api -run 'TestPendingSettingsRejectsOversizedSerializedDraftAtomically|TestPendingSettingsHTTPRejectsCompactSerializedExpansionAtomically' -v -count=1 -timeout=60s
  instance 1.673s; api 1.488s

go test ./internal/instance ./internal/api -run '^TestPendingSettings' -count=1 -timeout=60s
  instance 0.233s; api 0.054s

go vet ./internal/instance ./internal/api

git diff --check f6336ac f413f95 -- internal/instance/pending_settings.go internal/instance/pending_settings_test.go internal/api/pending_settings_handlers_test.go docs/reports/task31d2-backend.md
```

Independent negative control, exit 1:

```text
go test -overlay=/home/thim/.cache/dilla-tmp/task31d2-fix1-review-oh3i7gph/overlay.json ./internal/instance ./internal/api -run 'TestPendingSettingsRejectsOversizedSerializedDraftAtomically|TestPendingSettingsHTTPRejectsCompactSerializedExpansionAtomically' -v -count=1 -timeout=60s
```

The external overlay removes only the new save-size conditional. Both service subcases then fail because save returns nil, and the HTTP regression fails with PUT status 200 instead of 422. This independently confirms that the new regressions detect the original defect. Repository implementation and test files were not modified during review.

These checks use migrated SQLite, shipping handlers and labeled fixtures. No real games/services, native platform qualification, user preview, vault writes, staging or commits were involved. Integrator acceptance and ledger ownership remain with the root.
