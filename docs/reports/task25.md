# Task 25 — theme, accessibility and integration evidence

Status: original checkpoint fix I1 and final Updates UI submission ready for integrator review. Owned files are frozen; full Task25 acceptance remains pending independent review, Tasks18/24 composition and native/release evidence. No ledger completion is claimed.

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

At this original checkpoint, final update/rollback navigation and confirmations, Task24 composition, the long publisher-filename interaction and real native workflows remained pending. The continuation below records the subsequent UI checks; successful native game/client/content/backup/restore and release qualification remain unestablished. No EULA was accepted, game started, host unit installed, deployment performed, change staged/committed, vault modified or external message sent. Root owns ledger acceptance.

## Final integration continuation — Updates UI plan (before code)

Task25 now owns `web/src/features/updates/**`; root retains routes, schema/build wiring and all backend files. Keep the existing workbench tokens and Mantine components. Present target content and pack source inputs before preparation; show per-path choices next to affected paths, and official links/hashes next to required-file acquisition. Uploads create verified receipts and prepare a new typed preview; they never apply an update. Use full-width wrapping paths/file labels, normal sentence case, explicit pending-choice state, and existing separate update/world-rollback confirmations. Preserve cumulative draft choices/selections/receipts in-session. Saved immutable previews remain reviewable; editing restored previews discloses required file reacquisition because the API intentionally omits the private original request.

Pre-build critique: a plain blocker list would leave manual downloads and conflicts unresolved. Replace it with the corresponding typed choice/upload controls while preserving each backend blocker and safety gate. No new visual metaphor or generic JSON editor is needed.

## Updates UI submission — 2026-10-17

Ownership transferred from Task18 to this worker: `web/src/features/updates/**`. Implementation files are `UpdatesPage.tsx`, `PackReview.tsx`, `ContentManualReview.tsx`, `draft.ts` and `draft.test.mjs`; the existing index export remains. Root owns App/instance navigation, OpenAPI/generated types, builds and backend changes. No backend or shared schema/build file was edited in this continuation.

The page now uses the generated instance GET `{detail}` response. Optional content loading no longer prevents the instance/status screen from rendering. Terminal job status is set after final preview/log reads so effect cleanup cannot abort its own result refresh. A new preview is selected automatically only when exactly one previously unseen preview exists; ambiguous concurrent results require explicit selection.

Exact content replacement controls preserve the publisher/project and accept an explicit release ID and file ID. Exact pack selection includes publisher/project/release/file ID. `Plan.pack` displays source, target tuple, per-path hashes/reasons, typed keep/upstream decisions (including upstream deletion), cumulative optional/disabled/pinned/excluded selections, required official archive/member links and hashes, and all backend blockers. Changing any draft field marks the displayed preview stale and disables Apply until a fresh verified preview is prepared. Source changes clear only receipts bound to that exact source while retaining path decisions. In-session full requests are retained for prepared previews. Restored public plans recover their path decisions/selections; the UI explains that omitted private receipts and standalone replacement selections may require reacquisition.

Administrator archive uploads use the existing `/modpack/archives` endpoint with `archive_manual.selection` including exact file ID. Administrator member uploads use the OUTER update preview ID in `/updates/previews/{id}/pack/members?path=...`; the diagnostic inner pack ID is never used for actions. Returned receipts accumulate in `Request.pack.archive_receipt_id` and `Request.pack.manual[path]`, followed by a new typed asynchronous prepare job. The standalone required-file continuation uses generated `content_manual_required`, the existing administrator `/content/uploads?filename=...` endpoint and cumulative `Request.content_manual[id]`, copying the diagnostic exact selection into `content_selections[id]`. Standalone upload digests are labelled staged until the new preview checks publisher hashes and target descriptors. Upload/reload/preparation never invokes Apply.

Update confirmation shows the exact target, pack release/file, countdown and safety-backup/start policy; its checkbox is initially false and receives keyboard focus. World rollback remains a separate administrator dialog requiring both the exact instance ID and explicit world-data rewind authorization. Backend blockers, expiry, pending edits, required files and absent changes prevent update activation.

### Final browser/build evidence

No first-use prerequisites were mocked or rerun. The actual route used the earlier shipping-UI-created `data-final`, accounts and instances on loopback port 12825. The original empty-DB evidence above remains the primary onboarding evidence.

| Embedded binary | SHA-256 | Evidence boundary |
|---|---|---|
| `/home/thim/.cache/dilla-tmp/minecraft-ui-task25-updates` | `68418627ffa0487985a1b5675a71708649a88ce2a9085c9b921865b6cb63cbce` | Pack controls and first actual route check |
| `/home/thim/.cache/dilla-tmp/minecraft-ui-task25-final` | `04b64b51e918dc7e1a9c16287edc2ed0fc6ebd011e086c0de18ed6cda071e339` | Complete content/pack receipt and confirmation fixture |
| `/home/thim/.cache/dilla-tmp/minecraft-ui-task25-contrast` | `b863ecf6fddb0e5910266d88482c0d027ee046bf1d96054613320baa22e7cdbe` | Final shipping route/role/typed refusal plus review-I1 computed colors and mobile screenshots |

Root ran successful production npm builds and CGO-disabled manager builds for these binaries. The latter two contain identical Updates UI; the last adds the separate contrast fix below. All are development evidence, not native platform qualification.

Actual shipping checks: server feature navigation reached **Updates and rollback** by keyboard; current selection displayed 1.21.9/Vanilla; the screen fitted 390 px and desktop; viewer lacked Prepare and operator could see it; no page errors or Apply requests occurred. The initial backend checkpoint admitted a prepare job then returned a generic safe failure. This was reported to the backend owner. The final backend now refuses the uninstalled instance before admission with `instance_not_installed` and the visible action **Install the selected server files before preparing an update.** Successful preview/install/update is not claimed. No host service was installed and no game/EULA action occurred.

The supplemental fixture uses actual signed-in accounts and intercepts only feature metadata/actions. It verifies exact content/pack file IDs, archive acquisition, two cumulative target-pack member receipts, a wrong-hash error and retry, keep/delete conflict choices, cumulative optional/disabled/pin/exclusion flags, a standalone content receipt without losing pack decisions, and countdown edits preserving every receipt while disabling stale Apply. Intercepted Apply calls require explicit update confirmation or exact-ID-plus-world-rewind rollback confirmation; none reaches the backend. Operator rendering has zero upload controls, retains conflict choices, keeps Apply blocked and sends no POST. These are UI contract/interaction tests, not successful native transactions or publisher verification evidence.

The deferred modpack long-filename check uses intercepted publisher metadata only. The full accessible Preview label/title is retained, wraps normally to 56 px of text inside a 74 px button, and the page remains 390 px wide. It sends no preview/apply request and changes no pack semantics.

Commands: `npm --prefix web run typecheck` PASS; `npm --prefix web run lint` PASS; `node --experimental-strip-types --test web/src/features/updates/draft.test.mjs` PASS (3 tests covering cumulative path flags/choices/receipts, public-plan restoration, source-bound receipt invalidation); scoped `git diff --check` PASS. No broad Go suite was run for this continuation. Browser commands are `node /home/thim/.cache/dilla-tmp/task25-updates/<script>.mjs` using the existing repository Playwright installation and isolated contexts.

Artifacts under `/home/thim/.cache/dilla-tmp/task25-updates/`:

- `shipping.mjs`, `shipping-results.json`: final actual route/nav/role/refusal; `evidence/shipping-updates390.png`, `shipping-preview-refusal390.png`, `shipping-updates-desktop.png`. `shipping-pack-checkpoint-results.json` retains the earlier generic-failure comparison.
- `supplemental.mjs`, `supplemental-results.json`: pack-only checkpoint contract test.
- `supplemental-content.mjs`, `supplemental-content-results.json`: final complete content+pack workflow; `evidence/supplemental-content-required-archive390.png`, `supplemental-content-pack-conflicts390.png`, `supplemental-content-update-confirm390.png`, `supplemental-content-update-confirm-desktop.png`, `supplemental-content-world-rollback-confirm390.png`.
- `operator-gates.mjs`, `operator-gate-results.json`, `evidence/supplemental-operator-manual-gates390.png`: operator controls and blocked manual-acquisition state.
- `modpack-long-label.mjs`, `modpack-long-label-results.json`, `evidence/supplemental-modpack-long-filename390.png`: deferred filename wrapping check.

One initial fixture measured 406 px immediately after changing an open desktop dialog to a mobile viewport. Waiting for the breakpoint/scrollbar state to settle produced 390 px; no product CSS change was needed. Primary tests wait for settled page/dialog state. The full-page confirmation screenshots show fixed chrome at the capture's scroll position; the earlier actual viewport header check remains authoritative.

## Checkpoint review fix round 1 — I1 contrast (separate submission)

Independent review `.superpowers/sdd/2026-10-16-minecraft-server-ui/task25-review.md` found one Important issue and no other checkpoint findings: white-on-yellow warning badges and yellow-on-white source-gap text were 2.13:1. Verified the affected badge/text color choices and Mantine contrast behavior before changing them.

Fix files are ONLY `web/src/theme.ts` and `web/src/features/game-events/GameEventsPage.tsx`, separate from the new Updates feature files. Filled badges now use Mantine `autoContrast` with luminance threshold 0.179; this chooses the readable black/white foreground for normal-size labels. Source-gap text uses `light-dark(#7a4b00, #ffd43b)`. All wording, event provenance and coverage semantics remain intact.

On the exact `b863ecf6…22e7cdbe` binary, isolated 390 px browser contexts measured rendered computed colors on actual retained shipping event/coverage records:

| Element | Light foreground/background | Light ratio | Dark foreground/background | Dark ratio |
|---|---|---:|---|---:|
| Partial-observation badge | `#000000` / `#f59f00` | 9.84:1 | `#000000` / `#ffd43b` | 14.73:1 |
| Source-gap badge | `#000000` / `#f59f00` | 9.84:1 | `#000000` / `#ffd43b` | 14.73:1 |
| Source-gap text | `#7a4b00` / `#ffffff` | 7.41:1 | `#ffd43b` / `#242424` | 10.89:1 |

`contrast.mjs` / `contrast-results.json` records computed colors, relative-luminance calculations and assertions at or above 4.5:1. Screenshots: `evidence/contrast-actual-events-light390.png` and `contrast-actual-events-dark390.png`. Early supplemental source-gap screenshots are retained separately; final measured records existed in the shipping DB and needed no interception. Focused typecheck/lint passed after the fix. Independent acceptance of I1 remains with root/reviewer.

## Final handback boundary

Owned code/report is frozen for integrator review; the isolated 12825 manager is stopped. The UI-only deferred navigation, conflict/manual-file, long-label and confirmation checks are now recorded above. Full Task25 acceptance still requires independent review of this Updates submission, final Task18/24 backend composition/safety review, successful real content/config/backup/restore/game/client workflows, actual Linux service/Windows execution and the release version/layout matrix. The report makes no native or stable-release qualification claim. No commits, staging, deployments, host units, game launches, EULA acceptance, vault writes or external messages were performed by this worker.
