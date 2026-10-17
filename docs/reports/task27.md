# Task27 — retained console

Candidate: uncommitted Task27 files on the approved feature workspace, based on `6ffb25b`. Integrator review and ledger acceptance remain required.

Implemented in `web/src/features/console/**`; focused browser regression in `web/tests/console-parity.mjs`. No dependencies, API/schema changes, raw filesystem routes, credentials, or shared shell changes.

## Delivered behavior

- Case insensitive literal text filtering of the current live buffer or retained page, preserving ascending cursor order and displaying matched/total counts. The filter does not silently filter downloads.
- Follow to tail starts enabled. Scrolling more than40px above the tail turns it off; live collection continues. The switch restores following. Pause closes the stream, suspends follow and resumes from the cursor on request. Retained browsing does not jump when live output arrives. Instance changes reset the entire console scope and cancel pending requests/streams/exports.
- Escaped React text lines with timestamps, explicit gap/error/warning/output labels and color; server text never becomes HTML. Both history and streamed pages validate line size/count, ascending IDs, cursor and timestamp shape. Live memory remains500 lines; retained browsing holds one500-line page and at most64 page cursors.
- Oldest-first retained browser with next/previous/start-again access, visible cursor and page scope. Uses only the existing authorized redacted `/instances/{id}/console/logs?after=…&limit=500` API. Live and history/export cursors are separate.
- Bounded unfiltered plain text download through the live cursor observed when requested: at most8 pages /4MiB of line data, plus a small scope header. Header and UI disclose limits, truncation, reported gaps, non-atomic reads, redaction, exclusion of older/raw logs, and absence of a completeness guarantee. Downloads are cancelled on server changes, with no late download under the next server.
- Console history/status reads and each export page have a15s deadline combined with instance/request cancellation. Timeout errors release loading state and remain visible. Disconnect/connecting/pause marks readiness stale/unverified rather than continuing to show a green ready claim.
- Existing operator/admin command allowlist, role gates, acknowledgement/job feedback and safe command API remain intact.

## Contract and scope

The existing backend retains up to2,000 redacted lines and permits500 per page, ascending by ID. This is a retained console browser/export, not Valheim's arbitrary stored-file picker or cross-file raw-log search. No additional raw-file access is introduced. The backend does not disclose a pruned-before marker when starting at cursor0, so “no gap reported” cannot establish completeness. This limitation is explicit in the shipping UI and every export header. Concurrent retention may change between pages; reported gap markers are retained and disclosed.

## Verification

- `npm --prefix web run typecheck` — PASS.
- `npm --prefix web run lint` — PASS.
- `node web/tests/console-parity.mjs` with owned Vite on `127.0.0.1:15175` — PASS. Test first failed against the original UI because the filter was missing. The focused browser script asserts filtering/order, duplicate resumed snapshots after a simulated disconnect,500-line bound, safe text rendering, scroll stopping follow, explicit follow, pause, page navigation, unfiltered ordered redacted download/scope, gaps, wrong-instance in-flight export cancellation,4MiB truncation, malformed-page refusal,390px keyboard and width, viewer command absence/operator acknowledgement, stale ready observation after disconnect, bounded request timeout handling, and empty/failure output. Timeout tests explicitly accelerate `AbortSignal.timeout` to50ms; they exercise the production cancellation/error behavior without waiting15s.
- The deterministic script intercepts console history/API and substitutes EventSource. It is client behavior evidence, not real redaction correctness, live native reconnection, real-game output, service lifetime or release qualification. It ran successfully with the integrated Task26 `InstanceLayout` routing.
- Genuine manager evidence, supplied by Task26 worker from its disposable manager on `:8080` and Vite `:15226`, with no request interception:390px empty console showed `Console stream connected`,0 buffered lines/cursor0,0of0, and `No captured output yet`. Readiness: Not ready; JVM Not running; Control/required settings Unverified; no managed JVM identity recorded; gameplay overrides unverified. Administrator approved-command selector/button present. Parent header showed stopped process/readiness and unknown players/metric gaps. No game operation or EULA mutation was performed.
- Screenshots: `/home/thim/.cache/dilla-tmp/task27-console-mobile.png` (intercepted filter/keyboard state) and `/home/thim/.cache/dilla-tmp/task27-console-actual-empty.png` (genuine manager empty view).

No Go/full suite run: this packet changes console frontend behavior only. No stage/commit/push was performed. Real game logs/readiness/command acknowledgement, native stream lifetime and supported-server qualification remain pending separately.
