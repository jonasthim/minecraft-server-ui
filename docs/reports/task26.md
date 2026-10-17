# Task26: Daily operating shell, instance overview and trends

Implementation evidence for integrator review; package acceptance and ledger updates remain with the integrator. Base: `6ffb25b`. Reference inspected read-only: Valheim `dabec19585e3f401eceb127840c71542fa84dac1` (`v1.21.3`), especially dashboard, overview, sidebar and top bar. Existing Minecraft forest/earth palette is preserved.

## Delivered contract and behavior

`features/instances/index.ts` exports `InstanceLayout`, `InstancePage`, `useInstanceContext`, and `InstanceContext`. The context is `{ detail: InstanceDetail; refresh: (signal?: AbortSignal) => Promise<void> }`. The integrator owns the nested `App.tsx` route composition. The layout supplies one persistent identity/live header, feature navigation and reviewed lifecycle controls above its `Outlet`. Lifecycle controls remain mounted across section navigation; the disclosure is open initially on the overview. Instance/account/role changes remount the scope and clear old dialogs, jobs and data. Detail refresh stays stable, independent of live telemetry and independent of lifecycle terminal feedback.

The shell adds a searchable selected-server control and persisted System/Light/Dark appearance selection using Mantine's existing storage. Canonical route matching avoids an unguarded URI decode. The responsive feature links have explicit current-page state, keyboard focus and 44px mobile targets.

Fleet cards expose observed lifecycle/readiness, native roster count, JVM CPU/resident memory, logical instance bytes, timestamps and gaps. Fleet totals remain unknown unless every roster is recent, known, complete and gap-free. The running count explicitly describes observed running processes. There is no inferred host CPU/memory/disk or healthy-state fallback. Resource cards disclose that 100% CPU means one logical CPU; JVM memory is not heap. A stale or partial roster does not establish zero or a fleet total.

Fleet observation uses two workers, each with three bounded read requests: at most six concurrent telemetry requests, regardless of fleet size. Each completed fleet pass schedules its next pass after 15 s. Requests have 10 s read deadlines, visibility cancellation and generation fencing through the existing polling helper. Detail/sidebar/metrics reads have read deadlines. Existing lifecycle and job interval polling was narrowly changed with explicit integrator authorization to serialize requests; first job observation remains delayed 2 s, status period 5 s. Lifetime cancellation guards mutation continuations. Preview/operation POST requests retain their prior deadline semantics; no new 10 s mutation timeout is imposed.

Inline SVG charts show CPU, resident memory, instance disk and players with timestamps and accessible descriptions. Nulls, source gaps, missing periods and coverage gaps break lines; isolated observations remain visible as dots. Full metrics retains the readable, scrollable exact-value history and all ranges. Overview uses compact trends with a link to full history, followed by the settings heading and settings controls.

## Verification

All tools use repository Playwright/Chromium; no browser automation dependency was added.

- `npm --prefix web run typecheck` — passed after final source changes.
- `npm --prefix web run lint` — passed after final source changes.
- `node --test web/src/features/metrics/poll.test.mjs` — 2 passed: visibility/range replacement and abort-ignoring late results/errors cannot overwrite current data or add another poll.
- `LIFECYCLE_CHECK_URL=http://127.0.0.1:15226 node web/tests/lifecycle-terminal-refresh.mjs` — 3 scenarios passed after final LifecyclePanel changes: success, lifecycle refresh failure and parent detail refresh failure. Terminal result appears before a deferred ancillary response; original failure survives; each terminal completion refreshes parent detail exactly once. Fixture additions permit the new header's list/unknown roster/empty metric reads and preserve the original assertions.
- `node web/tests/operating-parity.mjs` — final intercepted suite passed: incomplete/stale fleet never displays 101 players; process-running remains distinct from starting readiness; gap splits CPU into two lines; held Alpha metrics do not replace Beta after sidebar switching; stale Beta roster 99 is not current; role change clears open admin stop confirmation/actions; malformed client route leaves the shell alive; 390 px has no document overflow. Slow lifecycle read held across 11 s produces exactly 1 poll request, catching the old 5 s overlapping interval. Recorded maximum concurrent metric fixture reads: 3, below the six-read fleet cap. No page errors.
- `TASK26_ACTUAL=1 node web/tests/operating-parity.mjs` — initial complete actual+intercepted run passed. Shipping UI against a fresh SQLite database created the first admin, created `operating-review` Vanilla 1.21.5 with EULA unchecked, navigated overview/metrics, persisted dark theme across reload, and used the 390 px selected-server navigation. No API response interception was used in this actual segment. Subsequent post-review actual capture reauthenticated through the UI and verified the final compact overview at desktop / 390 px. An additional fresh actual segment captured the new console's honest empty/readiness state for Task27. Intermediate fixture-only test failures were resolved (clock installation, disabled install fixture action, Vite rejecting malformed URL before SPA delivery); final intercepted and lifecycle runs above pass.

### Reviewable screenshots

Final screenshots, captured after scrolling to the top:

- `/tmp/task26-screenshots/actual-empty-desktop.png` — actual first administrator's empty database.
- `/tmp/task26-screenshots/actual-overview-desktop.png` — actual final compact overview, stopped/uninstalled, source gaps and grouped settings.
- `/tmp/task26-screenshots/actual-overview-mobile-dark.png` — actual final 390 px dark overview.
- `/tmp/task26-screenshots/intercepted-gap-mobile.png` — explicitly intercepted running/starting state and gap-separated trends.
- `/tmp/task26-screenshots/intercepted-beta-stale-mobile.png` — explicitly intercepted Beta with stale roster withheld.
- `/home/thim/.cache/dilla-tmp/task27-console-actual-empty.png` — actual empty console, no managed JVM identity, no captured output, readiness/control unverified.

Desktop/mobile screenshots were visually inspected. Root also reviewed earlier images and requested the applied compact-overview/settings grouping and top-scroll corrections.

## Preview and remaining limits

Per the integrator's user-preview instruction, the disposable manager remains on `127.0.0.1:8080` with Vite on `127.0.0.1:15226`. Current state is `/tmp/task26-data-final`; this is separate from the host service. A restrictive 0600 credentials file was handed privately to the integrator for authenticated local preview. The final fresh database contains one configured, uninstalled server with EULA unchecked. This task does not install/start a game, accept the EULA, change the host service, or qualify real JVM/player observations.

There is no host aggregate endpoint. Large fleets are bounded but use per-instance reads and wait for a fleet pass before updating cards. The old LifecyclePanel is reused with the approved polling/lifetime fix; all confirmation, EULA, native policy, role and safety copy remains. Root performs combined production build/browser integration review. No native Windows, real game/client, backup/restore or stable compatibility claims follow from these UI checks. No staging, commit, push, new dependency, API change or vault write was performed.
