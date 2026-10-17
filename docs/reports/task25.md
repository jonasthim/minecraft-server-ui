# Task 25 — theme, accessibility and integration evidence

Status: independent implementation checkpoint ready for integrator review. Files frozen; full Task 25 acceptance remains pending final Tasks 18/24 composition and whole-branch review. No ledger completion is claimed.

## Design plan, recorded before implementation

The visual reference is a server keeper's workbench: light stone work surfaces, a spruce navigation rail and a small original grass-and-earth block mark. This is an operational tool, so normal readable text and explicit actions carry all important meaning.

Tokens: stone `#f3f5f1` (canvas), chalk `#ffffff` (forms), spruce `#253b32` (navigation), leaf `#356c45` (primary action), earth `#795438` (subtle block edge), ink `#223029` (body). Status colors retain Mantine's semantic red/amber/blue families; text accompanies state colors. Type: local Segoe UI/Noto Sans for body and headings, local Cascadia/DejaVu monospace only for actual logs. Heading scale stays compact (24–30 px) and body stays 16 px. No fetched font, game artwork or dependency.

Layout option A uses the current header/rail/content model with less mobile padding, a skip link, visible location and a compact role footer. Option B would put all navigation above content; rejected because administrator routes wrap into several rows at 390 px. Alignment remains left aligned; content max-width 1280 px.

```text
Desktop                    Mobile
┌ block / product ──────┐   ┌ menu / block / product ┐
│ spruce │ title/actions│   │ title/actions         │
│ routes │ stone work   │   │ stone work surface    │
│ account│ surface      │   │ stacked forms         │
└────────┴──────────────┘   └───────────────────────┘
```

Pre-build critique: a dark background plus bright green accent would resemble a generic developer dashboard. Revised to a light mineral canvas and restrained spruce rail; spend the distinctive detail on the original block mark and small terrain edge in chrome. Square-ish panels reflect block construction, while inputs keep familiar focus and spacing. Avoid textured text backgrounds, pixel fonts, ornamental inventory slots and animated decoration. Keep coverage limitations concise with expandable source detail.

## Ownership

Assigned: shared theme/CSS/shell/index/favicon; JobsPage; narrow accessibility, layout and wording in existing feature pages except self-update and updates. Backend: events hub/tests, jobs handlers/tests, and Subscribe method/tests only in jobs service; retain Task18's concurrent immutable bindExecution change. Root owns routes/API schema/build wiring and ledger. No staging, commits, game execution, EULA acceptance or vault writes.

## Evidence

The checks below verify the assigned development checkpoint. Native Linux game/client, Windows execution and stable release qualification are not established by this work.

Additional root delegation: create the repository README, extend development prerequisites/commands, add native/Linux/Windows updater build targets and include cross-builds in CI. The updater command is `cmd/minecraft-ui-update` and artifacts use the same `minecraft-ui-update` name expected by deployment scripts. A first dry run accidentally used `updater` as the command-directory suffix; actual build exposed this immediately and it was corrected before final verification.

Initial verification: `go test ./internal/events -count=1` failed as expected before the new bounded constructor existed. After implementation, `go test ./internal/events ./internal/jobs ./internal/api -count=1` and `go test -race ./internal/events ./internal/jobs ./internal/api -count=1` passed. Tests exercise global/concurrent quota, typed unavailable error, slow-reader release, idempotent close, lookup-failure release, client cancellation and the existing absolute authorization lifetime. `npm --prefix web run typecheck` and `npm --prefix web run lint` passed. `make -n build-updater build-updater-linux build-updater-windows` and all three actual updater builds passed after the directory-name correction.

First-use run started from a newly created private data directory at `/home/thim/.cache/dilla-tmp/task25-browser/data`, using root's production binary `/home/thim/.cache/dilla-tmp/minecraft-ui-task25`, serving only `127.0.0.1:12825`. An isolated repository Playwright Chromium context created the administrator and then `spruce-meadow` through actual shipping UI. Official Vanilla catalog discovery supplied Minecraft 1.21.9. No database/API seeding was used; EULA stayed unchecked and no Minecraft process launched. Initial script selectors incorrectly assumed required labels lacked the asterisk and that Select used textbox; corrected to accessible role/name matching. This was test-harness correction, not product fallback.


## Implemented changes

- Shared visual tokens use the approved Minecraft-inspired palette, readable local type and modest corner radii. Added an original SVG block mark/favicon, 16 px mobile shell padding, stronger dimmed text, visible focus, reduced-motion treatment and an accessible skip link. Header/nav now expose useful navigation labels, expanded state, current route and the signed-in role; the product name is no longer a competing page heading.
- Server feature links retain every existing route and use concise labels in a two-column mobile grid. Final screenshot review found a narrow name column even though the page had no overflow; the corrected mobile server summary puts its information across the full width with status/action below. Desktop keeps the existing shell and interaction model.
- Users, Activity and token tables have labelled, focusable native horizontal scroll regions. User edit/revoke actions have specific accessible names; user creation/edit focus is explicit. Long modpack preview filenames wrap and retain their full accessible text/title. Acquisition, choices and pack application semantics are unchanged.
- Event coverage and individual source detail use native keyboard disclosures. Important gap/partial-observation statements stay visible. Metrics use readable source/field labels without task numbers and retain sampling, retention, unknown-value and provenance disclosures.
- Jobs fetch durable state/output before deciding whether streaming is needed. Terminal snapshots open no stream. A terminal progress event closes the stream immediately and fetches final durable output. Reconnection catches up from durable cursors; live log IDs cannot move that cursor past missing saved lines. Final output failures and retry are visible, and log output can receive keyboard focus.
- The events hub admits at most 128 total subscribers with bounded per-subscriber buffers. Closed/saturated admission returns typed `unavailable` (HTTP 503); the API rejects it before SSE headers. Close, lookup failure, client cancellation and slow-consumer eviction release admission. The absolute 30-second authorization deadline remains unchanged. Root merged the 503 OpenAPI response and regenerated types centrally.
- Added README/development entry points and matching local/Linux/Windows updater build targets. CI includes both updater cross-builds. No dependencies or credentials were added.

### Exact owned files

`web/src/theme.ts`, `web/src/index.css`, `web/src/layout/Shell.tsx`, `web/index.html`, `web/public/favicon.svg`, `web/src/features/jobs/JobsPage.tsx`, narrow changes in `web/src/features/auth/{AuthProvider,UsersPage,AccountPage,AuditPage}.tsx`, `web/src/features/instances/InstancesPage.tsx`, `web/src/features/game-events/GameEventsPage.tsx`, `web/src/features/metrics/MetricsPanel.tsx`, and `web/src/features/modpacks/ModpacksPage.tsx`.

Backend: `internal/events/{hub.go,hub_test.go}`, `internal/api/{jobs_handlers.go,jobs_handlers_test.go}`, and only the Subscribe method/comment/signature-dependent test changes in `internal/jobs/{service.go,service_test.go}`. Task18's concurrent `bindExecution(ctx, id, j, payload)` line remains intact.

Additional assigned docs/build files: `README.md`, `docs/development.md`, `Makefile`, `.github/workflows/ci.yml`, this report. API schema/generated types, ledger, routes, updates/self-update UI and other concurrent backend files belong to root/other workers and were not edited by this worker.

## Final shipping browser checkpoint

Primary evidence was captured on 2026-10-16 with the production embedded binary `/home/thim/.cache/dilla-tmp/minecraft-ui-task25-tables`, SHA-256 `f65c57cdd25967ced89f1b6ff0f5fd599337070ef95216348844aed284c9b4fd`. It served `http://127.0.0.1:12825` using the NEW private directory `/home/thim/.cache/dilla-tmp/task25-browser/data-final`. Isolated Chromium contexts came from the repository's existing Playwright installation; no user profile was used.

The primary run used no seeded DB, intercepted endpoint, fake native helper or precreated account/instance. The UI created the first administrator with keyboard submission and two Vanilla 1.21.9 instances (`spruce-meadow`, port 25565; `birch-valley`, port 25566) using official version discovery. The create dialog initially focused Server name; Escape closed it and returned focus to its trigger. EULA remained unchecked for both. This proves configuration onboarding, not game installation or compatibility.

Through the actual UI the same administrator created viewer/operator accounts, a disabled Europe/Stockholm world-backup schedule and a read token. The token's one-time secret was dismissed before screenshots. Saving desired instance settings returned an explicit native-service-unavailable error and kept the dialog open. That operation is not claimed successful: the approved host service is absent here, and its safety check was not bypassed.

A manual world-backup request created an actual durable job that failed safely because the approved native service was unavailable. The Backups page showed the failure; Job details loaded its durable error line and reported saved output loaded. Instrumentation observed zero EventSource constructions for this terminal job. No backup/restore success or native lifecycle success is inferred from that failure.

Twenty settled shipping routes were checked at 390 x 844: server list/detail and players/events/console/world/map/metrics/backups/content/modpack/schedules, plus users/account/activity/authentication/notifications/manager-updates/catalog/jobs. All had page width 390 and no unlabelled visible input/textarea in the scanned main content; no page errors were recorded. This is a scoped DOM/layout check, not a full screen-reader or accessibility certification. Current empty/uninstalled states were inspected honestly; no installed content or saved worlds were fabricated.

Mobile keyboard navigation opened the menu and activated Jobs, then closed the menu. The coverage disclosure opened with Enter. At 1440 px, the first Tab reached Skip to content and Enter focused main content. Scrolling a real viewport kept the fixed header at top 0; the earlier mid-page full-page-capture concern was not reproduced as a viewport defect. Activity history retained focus and scrolled 40 px on ArrowRight (324 px viewport, 731 px content).

### Corrections with before/after evidence

- Settled mobile Users was 415 px wide and Activity 540 px wide before table changes; both are 390 px after. Initial scan code sampled lazy loading too early, so it was corrected to wait for page headings before recording widths. Only the settled scan is treated as evidence.
- A screenshot revealed server names squeezed into about 66 px by status/action siblings despite no horizontal page overflow. The grid correction gives both names the full 316 px card content width. Visual review confirms each name and its version/port/memory are readable.
- The stream fixture demonstrated saved gap/final-log recovery, one stream closed once on terminal state, a visible final-log service failure and successful retry. See the separate supplemental classification below.

The final card-only covering check on 2026-10-17 used `/home/thim/.cache/dilla-tmp/minecraft-ui-task25-cards`, SHA-256 `191fb474ebe9a3f655c5fa1c0e56b7ec3932ffcea8d8785ac07a695d7c9f2651`, against the already UI-created `data-final`. It checked corrected mobile/desktop cards, HTTP 200 SVG favicon, and the existing terminal job after manager restart (still zero EventSource constructions). This binary also contains another worker's in-progress self-update changes; no new self-update acceptance is attributed to it. Fresh onboarding was not repeated for this layout-only correction.

## Artifacts and reproducible commands

All browser scripts/results/screenshots are in `/home/thim/.cache/dilla-tmp/task25-browser/` (private local evidence, not committed product data). Authentication state files stay private and are not deliverables.

| Evidence | Script / result | Selected screenshots under `evidence/` |
|---|---|---|
| Primary final empty DB → administrator → two instances | `final-first-use.mjs`, `final-first-use-results.json` | `final-01-fresh-setup390.png`, `final-02-empty-servers390.png`, `final-03-create-dialog390.png`, `final-04-two-servers390.png` (pre-card-fix comparison) |
| Actual accounts/schedule/token/failure/job, 20 mobile routes, roles and keyboard | `features.mjs`, `final-feature-results.json` | `final-users390.png`, `final-audit390.png`, `final-account390.png`, `final-schedules390.png`, `final-settings-host-limit390.png`, `final-backup-failure390.png`, `final-job-failure390.png`, `final-events390.png`, `final-server390.png`, `final-server-desktop.png` |
| Actual native table keyboard scroll | `table-keyboard.mjs`, `table-keyboard-results.json` | Focus/scroll dimensions recorded in JSON |
| Final corrected cards/favicon/terminal job after restart | `card-check.mjs`, `card-results.json` | `final-corrected-two-servers390.png`, `final-corrected-two-servers-desktop.png` |
| Supplemental intercepted job stream behavior | `synthetic-job-stream.mjs`, `synthetic-stream-results.json` | `supplemental-job-final-error390.png` |

Browser commands were `node /home/thim/.cache/dilla-tmp/task25-browser/<script>.mjs` for the named scripts. The feature script's first settings-save assumption failed because native-service verification correctly refused the operation; the script was adjusted to assert the visible refusal, with no product safety change. Its original table-scroll measurement inspected the inner sizing element; a separate focused check measured the actual native scroll region and passed.

Verification commands/results: `go test ./internal/events ./internal/jobs ./internal/api -count=1` PASS; `go test -race ./internal/events ./internal/jobs ./internal/api -count=1` PASS; `npm --prefix web run typecheck` PASS; `npm --prefix web run lint` PASS; `make -n build-updater build-updater-linux build-updater-windows` PASS; `make build-updater build-updater-linux build-updater-windows` PASS; scoped `git diff --check` PASS. Root reported successful `npm` production builds and `CGO_ENABLED=0 go build` for the exact binaries above. No optional broad retesting was performed after those specific risks were checked.

Supplemental stream classification: Playwright intercepted only job endpoints and replaced EventSource in an isolated context. It exercised a running job, missed durable log recovery, terminal progress, final output failure and retry without writing fixtures to the manager. This does not qualify a real successful game operation, reconnect network transport or native platform. Primary terminal-job evidence comes from the real failed-backup job separately.

## Review and remaining work

Visual self-critique: the initial cards technically fitted the viewport while wasting their most important text column. The corrected grid resolves this; screenshots now show clear server names and a compact calm control surface. The original block mark provides the distinctive Minecraft cue; no added textures, pixel font or decorative animation compete with operational content.

This checkpoint is ready for integrator review only. Still required: final composed instance update/rollback navigation and safety-confirmation checks after Task18, Task24 final UI integration/review, long publisher-filename interaction in the composed modpack flow, successful real content/config/backup/restore/native lifecycle flows on authorized hosts, actual Windows execution, game/client/version matrix evidence and whole-branch review. No EULA was accepted, game started, host unit installed, deployment performed, change staged/committed, vault modified or external message sent. Root owns ledger acceptance.
