# Task31C — saved map, retained events and schedule conveniences

2026-10-17. Source packet based on `097c743`; pending independent integrator review and ledger acceptance.

## Changes

- Saved map: pointer capture supports direct dragging with a visual preview; release snaps to whole saved chunks. CSS pointer distances are converted using the displayed canvas rectangle and active zoom (`3 × CSS distance / displayed width or height / zoom` chunks). Center requests retain the existing bounds of −1,874,999 through 1,874,998. Wheel zoom uses the existing 1×/2×/4× levels when the map is focused or Ctrl is held; the page otherwise scrolls normally. Native canvas resolution stays 480×480 while its displayed square responds to available width. Existing keyboard, zoom select and directional buttons remain available.
- Drag cancellation clears the visual offset on cancelled/lost pointers, blur, other navigation and zoom changes. Dimension changes reset center and zoom; intentional native-player centering preserves its observed target. Instance route changes remount the panel and retain existing request cancellation and stale-response fences. Touch dragging requires an explicit checkbox; ordinary touch allows vertical page scrolling.
- Events: case-insensitive name and message filters, event type, inclusive manager-observation UTC bounds, and reset. Counts identify the filtered and loaded scope; filtering never claims a complete retained-server search. Existing oldest-first 100-observation cursor pages and 500-row screen limit remain. Eviction is disclosed. Source-gap and retention warnings remain outside the filters, and earlier gap knowledge survives pagination and eviction. Error refresh retains disclosed stale data. Server timestamps/UUIDs are still unknown where the native evidence cannot establish them.
- Schedules: daily, hourly, elapsed six-hour, Sunday and weekday timing presets plus editable custom expressions. Recognized cron/descriptors and simple hour/minute intervals receive human-readable timing previews with explicit stored timezone. Other expressions remain custom and server validation/next-run reporting stays authoritative. Restart countdown remains 300 seconds. New definitions remain disabled; operator/administrator ownership and available-action gates remain. Exact `jobHref` links are preserved.

## Verification

All browser evidence below is intercepted shipping-client behavior, not a native game, map decoder, scheduler execution, real retained log or installed-manager qualification. The fixture uses a separate Vite instance at `127.0.0.1:15531`, never the live user preview on12826 or a presumed backend on8080.

Commands passed:

```text
npm --prefix web run typecheck
npm --prefix web run lint
node --experimental-strip-types --test web/src/features/map/poll.test.mjs web/src/features/map/selection.test.mjs
node web/tests/map-history-schedules.mjs
```

Existing map polling/selection tests: 4 passed. Focused Chromium browser checks cover:

1. 390px CSS-scaled horizontal and vertical pointer dragging; zoom-dependent chunk movement; wheel, keyboard and dimension reset.
2. Desktop displayed scaling; cancelled pointer; extreme coordinates clamp to world bounds; dimension/instance delayed responses; touch scroll opt-in and mobile width.
3. Name/text/type and both UTC time bounds; reset; cursor pagination; persistent source/retention disclosures; failed-refresh stale state.
4. Six 100-observation pages enforce the 500-row bound, disclose discarded rows and retain a gap warning after the gap row is evicted.
5. An instance switch rejects delayed old event history and resets local filters.
6. Preset/custom editing and timezone preview; five-minute restart default; exact disabled POST body; operator creation with no controls for another operator's definition; viewer cannot create.

Review screenshots (intercepted client fixtures) are available at:

- `/home/thim/.cache/dilla-tmp/task31c-map-mobile.png`
- `/home/thim/.cache/dilla-tmp/task31c-map-desktop.png`
- `/home/thim/.cache/dilla-tmp/task31c-events-filters.png`
- `/home/thim/.cache/dilla-tmp/task31c-schedule-mobile.png`

## Boundaries

No API/backend/schema, dependencies, shell/router or ledger edits. No services, games, EULA acceptance, vault writes, staging or commits. Direct map navigation consumes only the existing bounded saved-view and independently aged native-position contract. Custom schedule preview is descriptive rather than a client cron validator; the scheduler remains authoritative. Task31 and real game/version/platform acceptance remain the integrator's responsibility.

## Independent review fixes — round 1

Reviewed against freeze `06561bc` on 2026-10-17; C1 and C2 addressed, awaiting integrator acceptance.

- C1: descriptor expansion now checks `Object.hasOwn` before reading the alias object. Inherited names cannot become non-string values passed to `.split`. Custom text remains editable and only the server validates whether it is a legal schedule.
- C2: only weekday `0` receives the Sunday preview. Weekday `7` remains custom text because the shipping robfig cron parser accepts weekdays0–6.
- Added browser regressions entering the exact values `constructor`, `toString`, `__proto__`, and `0 3 * * 7`. Each must render a custom preview without a page error and leave the input usable. An intercepted server validation rejection preserves the editor and creates no successful definition; changing back to valid timing then saves the existing exact disabled restart payload. Existing preset, timezone, ownership and viewer checks run in the same focused path.

Regression was observed failing on `constructor` before the source fix. Verification after the fix passed:

```text
TASK31C_SCHEDULES_ONLY=1 node web/tests/map-history-schedules.mjs
npm --prefix web run typecheck
npm --prefix web run lint
```

Schedule-only mode avoids rerunning unrelated map/history browser work for this helper correction. These remain intercepted client checks, with a separate fixture Vite process on15531; the live user preview on12826 was untouched. No backend, shared file, dependency, staging or commit changes.
