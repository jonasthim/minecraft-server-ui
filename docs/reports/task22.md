# Task 22 — saved map presentation and native observations

Status: implementation and fix round1 independently reviewed; composed browser evidence accepted. Actual game/platform qualification remains pending. Base `71b6660` includes the independently reviewed Task21/Task11 containment fixes. No dependency, process/service installation, EULA acceptance, game execution, staging or commit was performed by this worker.

## Owned implementation and contract

- `internal/mapview/**`: finite view admission, presentation cache and active native singleflight. `New(Terrain, Presence)` consumes frozen Task21 Summary/Tile and existing console service plus the explicitly delegated new typed position method.
- `internal/api/map_handlers*.go`: `MountMap` in the authenticated API group; viewer GET summary and fixed 3×3 view. Strict integer, unknown/repeated parameter validation; existing role/token checks run on every finite request. No event stream or background subscriber survives a request.
- `docs/api/map.yaml`: additive fragment; integrator owns merging and generating shared TypeScript. `MapView.native` carries typed snapshot, positions, source/observed time, coverage/gap and reason. Existing PlayerIdentity UUID/name authority is preserved.
- `web/src/features/map/**`: generated API types, Mantine dimension/height controls, fixed-size Canvas, zoom and chunk pan, keyboard arrows/+/- and explicit buttons. Unknown non-air cells remain purple with block labels; missing, stale, unsupported, corrupt and unknown chunks are hatched and separately named in an accessible summary and chunk evidence. The height ceiling chooses highest non-air at/below Y, never an exact plane. Source file mtime, region-header game-clock save time and manager observation time remain separate.
- Root explicitly delegated only new `internal/console/map_positions.go` and its tests for the narrow position capability. Existing console transports/session/grammar and mapdata/player files were not edited.

Root composition: `mapview.New(mapdata.New(worldService, paths), consoleService)` and `api.MountMap`. UI exports `MapPage`/`MapPanel`; root owns route and visible server navigation wiring.

## Work/cache/cancellation bounds

Two global finite summary/view requests, no waiting admission queue and a 15-second overall request deadline; decoder retains its own two-read/10-second bounds. Each view has nine chunks, one exact discovered dimension and one optional inclusive height. Centers leave a one-chunk margin within Task21 coordinate bounds.

Terrain cache: at most32 entries and3MiB serialized tile/key data, LRU, five-second TTL. Native cache: at most32 instance entries and1MiB serialized observation/key data. The combined serialized cache budget is4MiB; this is an encoded-data budget, not a heap measurement. Fingerprinted successful tiles alone are shared; each expired sample calls the decoder again for full source identity/content/policy validation. The request index includes instance, exact dimension, chunk, height and presentation policy, and cached values retain decoder fingerprint/provenance. Failed/stale/unfingerprinted terrain is not cached. Clones prevent callers from mutating shared tile, height, timestamp, identity or marker slices.

Native reads use one per-instance singleflight and a five-second TTL anchored at probe start, inside global admission. There is no durable job, permanent ticker or retained viewer list. Busy/error native reads return unknown/gap, never a fabricated zero count or coordinates, while saved terrain remains usable. Native roster history's separate30-second sampler is unchanged. Identity/position coverage is capped at16, preserving total observed count and declaring incomplete/truncated coverage.

The browser makes one finite request at a time on a five-second cadence, cancels on hidden/unmount/key change, and fences responses even when a transport ignores abort. It does not scan player files or re-read world files separately for each player. Keyboard pan/height/dimension changes cancel the old view; failed refreshes visibly identify displayed data as potentially stale.

## Native positions without an add-on

Official static evidence establishes targeted native `data get entity <observed target> Dimension`, then `... Pos`, then `... Dimension` for exactly1.21.9 and26.3. Known UUIDs target that exact validated server UUID; name-only observations target the exact validated username and remain name-only. The acknowledgement must name the observed exact player and contain only a strict selected triple or namespaced dimension string. No whole-NBT command, private inventory dump, arbitrary selector/path, player-file query or UUID inference exists.

A single original `WithIdle`/session captures the generation, gets a fresh roster and performs up to48 targeted commands for16 players under a four-second batch deadline. Every command and final publication checks the original generation/readiness. A changed dimension before/after Pos rejects that marker. Nonfinite/out-of-world coordinates, unsafe dimension names, numeric/ambiguous dimension IDs, unfamiliar/localized/plugin feedback and missing names produce gaps. Same-dimension motion and round-trip teleports remain possible across sequential commands and are disclosed. A failed final generation/deadline check publishes no positions; failures are not success evidence.

Minecraft1.12.2 has no data-get command; its fresh native roster remains useful, while position capability is unavailable. Other versions are unverified, rather than asserted impossible. Legacy terrain also has numeric mod-registry and0–255 height limits, shown in the UI. This is a conservative static version whitelist, not a release compatibility claim.

Primary artifacts inspected without running Minecraft:

- Official cached1.21.9 mappings and inner JAR: mapped `DataCommands`, `EntityDataAccessor`, `Entity` (`cdv`), `ServerPlayer` (`awy`); bytecode writes `Pos` and namespaced `Dimension`.
- Official cached26.3 inner JAR: `DataCommands` read-only get branch; `EntityDataAccessor` only prohibits player **writes**; `NbtPredicate.getEntityTagToCompare` invokes native save; `Entity.saveWithoutId` writes `Pos` through Vec3's three-double-list codec; `ServerPlayer` writes namespaced `Dimension`; `EntitySelectorParser.parseNameOrUUID` accepts UUID targets. Bundled `assets/minecraft/lang/en_us.json` gives the exact native entity query feedback shape.
- Cache root: `/home/thim/.cache/dilla-tmp/minecraft-console-schemas`. Disassembly evidence: `/home/thim/.cache/dilla-tmp/task22-{entity26.3,serverplayer26.3,entity1.21.9,serverplayer1.21.9,selector26.3,entitydata26.3}.txt`.

These static command/field shapes and deterministic transport fixtures do not qualify runtime interoperability with an actual server/client, loader/plugin changes, Linux systemd or native Windows.

## Verification

Scoped checks passed:

```text
go test ./internal/mapview ./internal/console -count=1
go test -race ./internal/mapview ./internal/console -count=1
go test -race ./internal/api -run TestMap -count=1
go vet ./internal/mapview ./internal/console ./internal/api
node --experimental-strip-types --test web/src/features/map/poll.test.mjs
cd web && npm run typecheck && npm run lint && npm run build
git diff --check -- internal/mapview internal/console/map_positions.go internal/console/map_positions_test.go internal/api/map_handlers.go internal/api/map_handlers_test.go docs/api/map.yaml web/src/features/map
```

Focused regressions cover fixed view bounds, source-cache expiry, request isolation/clone safety, cache count/data budgets, global admission, cancellation release, per-instance native singleflight and observation expiry, strict finite position/dimension projection, exact narrow command sequence, cross-dimension changes, authenticated MSMP roster plus targeted command projection, native UUID provenance, generation changes, capped16-player/48-command coverage, viewer role and strict query rejection, real scoped PAT reads and immediate denial after revocation, hidden/unmount cancellation and out-of-order response fencing. Initial service/parser tests were observed failing with missing production entrypoints before implementation. Node tests use `.mjs` to avoid introducing Node-only types into the DOM application build.

The integrator owns broad combined-suite checks while backup/content/map shared composition is changing. The map UI typechecks before route composition; the then-current production build did not yet include a navigable map route. Actual empty-database390px browser flow and separately labelled synthetic terrain visuals must be recorded after root composes the shipping binary. No screenshot/fixture or real saved-game claim is made yet.


## Integrator composition and browser evidence

Root composed the authenticated API and visible server map route in4cb6e0e. Production TypeScript/Vite build passed and contains the map chunk. Root built the shipping binary from an isolated archive of that commit with its matching production web assets, avoiding concurrently uncommitted feature files.

Isolated Playwright390x844 passed: fresh DB → first administrator → actual official1.12.2 instance through shipping UI with EULA unchecked → Saved map link → real HTTP200 missing-world summary → visible legacy limitation → keyboard pan → direct route reload. A viewer created through the shipping user API logged in and read the same real map endpoint successfully. No page errors or horizontal overflow occurred. No world files/database prerequisites were seeded, game launched or service installed.

Separately labelled supplemental UI-only intercepted responses exercised a custom dimension, inclusive height query, player marker/centering, keyboard zoom, purple unknown blocks, distinct stale chunk and failed-refresh stale notice. They do not constitute saved-world decoding or native position interoperability evidence. Root inspected both shipping and supplemental screenshots. Reproducer `.superpowers/sdd/2026-10-16-minecraft-server-ui/map-browser.mjs`; screenshots in `map-browser-artifacts/`. The disposable8192 manager was stopped and drained after validation. Actual game/native platform qualification remains pending.

## Fix round 1 — automatic dimension changes (2026-10-16)

Independent review approved the package with one minor finding: discovery could replace the selected dimension while retaining the previous dimension's terrain, including when the replacement view request failed. This fix changes only the map feature and this report, against the reviewed `95690b9` implementation; shared API/composition and existing browser evidence remain unchanged.

Discovery, selected dimension and displayed view now update in one pure reducer transition. If discovery removes the selection, it selects the first discovered dimension (or clears selection for an empty list) and clears the previous view atomically. A late response tagged with a different requested dimension is rejected. Ordinary discovery polls preserve a still-discovered user selection and its saved terrain. The summary callback performs no state mutation inside another state updater.

Focused state regressions exercise observed custom terrain followed by discovery replacement/empty discovery, a failed replacement request with no new view, late old-dimension responses, recovery from an empty dimension list, and ordinary polls preserving useful same-dimension terrain. Against the extracted original transition semantics, replacement and empty-discovery tests failed by retaining the Moon terrain object; after the fix all three state regressions and the existing visibility/cancellation test pass.

Verification passed:

```text
node --experimental-strip-types --test web/src/features/map/*.test.mjs
cd web && npm run typecheck && npm run lint && npm run build
git diff --check -- web/src/features/map docs/reports/task22.md
```

No full browser rerun was needed for this pure state consistency correction; the integrator's existing shipping/mobile and supplemental canvas observations above retain their stated limits. Integrator commit and re-review remain pending; this worker did not stage or commit.
