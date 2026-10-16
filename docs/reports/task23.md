# Task 23 — Deaths and advancements provenance

Implementation submitted for integrator review on 2026-10-16 against `8c071ed`. This report does not mark the package accepted. The integrator owns review, staging/commits and the implementation ledger. No Minecraft classes were executed, server started, EULA accepted or host unit installed. No dependencies were added.

## Owned implementation and contract

- `internal/events/game/`: strict native English pattern parser, bounded durable event history and independent transactional source cursor, partial coverage reporting and cancellable observation loop.
- `internal/db/migrations/00013_game_events.sql`: per-instance cursor/coverage and event tables, unique durable source keys, cascade cleanup and history/retention indexes.
- `internal/api/game_event_handlers.go` and tests: viewer-only bounded history, strict queries and shared role/PAT enforcement.
- `docs/api/game-events.yaml`: frozen history endpoint and three DTOs. Root merged this fragment and regenerated frontend types in `0a69bed`; generated types were not edited by this worker.
- `web/src/features/game-events/GameEventsPage.tsx`: generated-type history page and panel, explicit coverage/identity/version/time uncertainty, empty/error/retention/gap states, bounded rows and pagination.

`game.New(db, logs, instances)` accepts the existing console service and instance service through narrow typed interfaces. It exposes `Poll(ctx,id)`, `Run(ctx)` and `History(ctx,id,after,limit)`. `api.MountGameEvents` mounts `GET /instances/{instanceId}/game-events` inside the authenticated API group. `GameEventsPage` and `GameEventsPanel({instanceId})` are exported for composition. After Task20 froze shared wiring at `0ff0089`, root explicitly delegated `cmd/minecraft-ui/main.go`, `web/src/App.tsx` and the instance feature link. These now construct/mount the service, expose the lazy history route and visible link, and run/drain it through `watchObservability`. Observation channel capacity and drain count derive from the actual loop list, preserving startup reconciliation ordering and draining before console/jobs/native/database teardown. No Shell changes were made.

## Source, confidence and limits

Only already-persisted redacted console lines are consumed through `console.History`; there is no additional raw log reader, command transport or assumed add-on. Game events use their own source cursor and unique instance/source-line key. With explicit root delegation, ingestion also reads the frozen `console_cursors.pruned_before` metadata column for the exact instance inside its transaction: the public console history API does not signal preexisting retention for a first zero cursor. An existing floor creates a deduplicated initial-retention gap, conservatively including retention that advanced between source-page read and transaction. No console metadata or underlying source is changed. Inserting all records, recording coverage, advancing that cursor and pruning retention happen in one SQLite transaction. A failed transaction publishes neither records nor the cursor. Replay/restart repeats no retained event, while identical messages at distinct source cursors remain distinct observations.

Each record identifies its instance, console source cursor, manager ingestion observation time, parser revision, source, confidence and best-known name-only identity. It has no guessed UUID. Historical emitting version remains explicitly `unknown`: current configured version is separate capability context and cannot retrospectively prove the version that emitted a retained console line. Server time remains null because a console HH:mm:ss prefix establishes neither date nor timezone. Static pattern review does not establish an authenticated event emitter, account identity or a qualified server/loader combination.

The parser requires the exact native server-thread INFO prefix and a bounded allowlist of English death templates or advancement/task/challenge/goal announcements. Normal player chat, insecure-chat decoration, `/me`, `/say`, plugin-prefixed output, malformed clock/prefix/message text and partial/redacted lines cannot become gameplay observations. A native-looking plugin/data-pack broadcast can imitate a system message; confidence is therefore `native_log_pattern`. Death templates outside the reviewed subset, formatted display names, localization and plugin formats are omitted. No arbitrary unknown log line is re-exposed as a game event.

Exact configured versions 1.12.2, 1.21.9, 1.21.11 and 26.3 have statically reviewed patterns; other versions remain unverified, and unreviewed 26.3 variants expose an unavailable capability. This is a parser capability statement, not release compatibility evidence. Minecraft 26.3's new `System chat: ` log prefix is recognized explicitly. Coverage remains partial even while ingestion is active: death/advancement announcements can be disabled, team death-message visibility can bypass global logging, advancement display can disable announcement, templates/localization may differ, and omitted/rotated/retained output or downtime may hide events. An empty history proves no absence of gameplay events.

Source cursor resets/expiration, retained console gap markers, redacted lines and changes to unavailable source capability create explicit gap records. Console omission reasons are static safe descriptions; raw error details and redacted values are not reconstructed. The response always states partial coverage, and stale/unstarted event ingestion is unavailable even if console capture is fresh. A pristine console with no source cursor remains unavailable until retained console output has been observed; an empty incremental page after a nonzero cursor does not make a previously observed quiet source unavailable. History survives capability changes; a 26.3 or unknown configured version does not erase older observations.

Retention is at most 30 days and 20000 records including gaps per instance; a persistent floor discloses expired event history. UTC storage uses fixed nanosecond precision for correctly ordered text comparisons. Pages have at most 200 records. Foreign/future/expired event cursors reset to retained history with coverage gaps. The UI keeps at most 500 rows and offers explicit refresh and load-more controls.

`Run` starts no durable jobs. One worker consumes at most 500 lines for each of at most 32 instances per five-second pass, advancing a fair instance cursor before wrapping. All source/database I/O carries cancellation. An instance existence check inside the write transaction handles deletion between source read and FK insertion as a benign disappearing instance. Unexpected source/database errors return to the manager for visible failure; cancellation drains the active operation. No raw private source error is presented as coverage text.

## Primary static evidence

Official cached artifacts were inspected using ZIP resource extraction and `javap -c -p`, without running Minecraft:

| Official server artifact | SHA-1 rechecked |
| --- | --- |
| 1.12.2 | `886945bfb2b978778c3a0288fd7fab09d315b25f` |
| 1.21.9 | `11e54c2081420a4d49db3007e66c80a22579ff2a` |
| 1.21.11 | `64bb6d763bed0a9f1d632ec347938594144943ed` |
| 26.3 | `33680f5f2ac32864d6d7cf5e56a705fdb3e05f4c` |

- Bundled `assets/minecraft/lang/en_us.lang` (1.12.2) and `en_us.json` (modern versions) provide the exact task/challenge/goal translations and the selected death templates. Legacy floor-lava and dragon-breath text differs and both exact forms are included.
- 1.12.2 `np` (advancement manager) constructs `chat.type.advancement.<task/challenge/goal>` and broadcasts through `pl.a(hh)`. `MinecraftServer.a(hh)` logs the component's plain text at INFO. 1.21.9 mapped `PlayerAdvancements` (`amr`) invokes the announcement and `PlayerList` (`bbd`) broadcasts to `MinecraftServer.a(xx)`, which logs `Component.getString()` at INFO. 1.21.11 `PlayerList` (`bbz`) and `MinecraftServer` retain this plain system message path.
- 26.3 `PlayerAdvancements` announces through `PlayerList.broadcastSystemMessage`. Its overload calls `MinecraftServer.sendSystemMessage(Component)`, whose body calls `LOGGER.info("System chat: {}", component.getString())`. `AdvancementType.createAnnouncement` supplies the player's display name and bracketed advancement name through the translated message.
- 26.3 `ServerPlayer.die` conditionally broadcasts the combat death message only when death messages are enabled; team visibility may route only to team recipients instead. These branches explain an actual native completeness gap.
- 26.3 `SayCommand`/`EmoteCommands` use `PlayerList.broadcastChatMessage` with `SAY_COMMAND`/`EMOTE_COMMAND`; `MinecraftServer.logChatMessage` decorates those messages before logging. Their wrappers and normal player-chat wrappers do not satisfy the game parser.

Artifacts reside in `/home/thim/.cache/dilla-tmp/minecraft-console-schemas` and the separate legacy JAR path from the handoff. Extracted grammar and disassembly evidence is under `/home/thim/.cache/dilla-tmp/task23-static-evidence` (not repository runtime assets). Official context: [1.21.9 release notes](https://www.minecraft.net/en-us/article/minecraft-java-edition-1-21-9) introduce authenticated management protocol discovery; [26.3 release notes](https://www.minecraft.net/en-us/article/minecraft-java-edition-26-3) explicitly warn that some observable log lines changed or disappeared. This package does not assume undiscovered MSMP death/advancement notifications.

## Verification

Scoped checks passed during implementation:

```text
go test ./internal/events/game -count=1
go test ./internal/api -run TestGameEvents -count=1
go test -race ./internal/events/game -count=1
go test -race ./internal/api -run TestGameEvents -count=1
go test ./cmd/minecraft-ui -count=1
go test -race ./cmd/minecraft-ui -count=1
go vet ./internal/events/game ./internal/api ./cmd/minecraft-ui
cd web && npm run typecheck
cd web && npm run lint
cd web && npm run build
go build -o /home/thim/.cache/dilla-tmp/task23-minecraft-ui ./cmd/minecraft-ui
node /home/thim/.cache/dilla-tmp/task23-browser.mjs
```

Focused tests cover reviewed legacy/modern and 26.3 message forms, ordinary chat/emote/say/plugin spoofing, malformed/partial/redacted messages, version/source uncertainty, name-only identity, gaps and unavailable/unverified and pristine-versus-quiet-source capabilities, cursor-update failure rollback, restart/replay dedupe, repeated distinct events, age/count retention, bounded pagination, foreign/future cursors, source loss and preexisting console retention, deletion between source read and event insertion, active-read cancellation/drain, unexpected loop failure, anonymous/disabled-user/role denial, malformed/duplicate/unknown/out-of-range queries, read-scoped PAT access and immediate revocation denial. Initial parser, service, handler and 26.3 tests were observed failing before their entrypoints/behavior existed; malformed percent encoding separately reproduced an HTTP200 bug before strict parsing fixed it.

## Remaining evidence

Integrator review and ledger update remain required. Shared route/runtime composition and actual shipping browser/mobile/keyboard evidence are recorded below. Parser fixtures/static bytecode and supplemental rendering cannot substitute for actual Minecraft event interoperability. Real server/client qualification, tested loader combinations, native Windows and Linux systemd execution remain unqualified here.


## Delegated shipping composition and browser evidence

The production TypeScript/Vite build passes and includes the `GameEventsPage` chunk. A native manager binary built from the composed shared checkout served those embedded production assets on isolated loopback port18173. No user browser/profile was used: root explicitly authorized standalone Playwright `chromium.launch()` and fresh `newContext()` because the in-app browser runtime had no available browser.

The final390×844 run started with a new, empty manager data directory and passed:

- First administrator through the actual shipping UI, then a Vanilla26.3 instance through the shipping create dialog, with EULA unchecked. The server was never installed/started. No gameplay event/database prerequisites were seeded.
- Keyboard focus/Enter on the visible Deaths & advancements instance link, actual history HTTP200, no fabricated gameplay events, explicit unknown emitting version and no-source coverage. A second refresh after a real five-second observer pass confirmed pristine source availability remains false.
- Direct route reload, viewer and operator sign-in/read through the shipping app; those users were created using the shipping authenticated administrator API.
- No horizontal overflow and no page errors; shipping administrator/viewer/operator screenshots were inspected.

Separately labelled intercepted responses supplied supplemental UI-only death/advancement/gap records, a long custom title, retention floor, keyboard pagination and a failed refresh that retained visible history with an explicit warning. These responses did not change manager state, source logs or world files, and make no real-event qualification claim. Supplemental screenshots were inspected.

Reproducer: `/home/thim/.cache/dilla-tmp/task23-browser.mjs`. Evidence JSON and screenshots: `/home/thim/.cache/dilla-tmp/task23-browser-artifacts/`, including `shipping-empty-390.png`, `shipping-viewer-390.png`, `shipping-operator-390.png`, `supplemental-patterns-gaps-390.png`, `supplemental-refresh-failure-390.png` and `evidence.json`. The temporary manager was interrupted, drained and exited with code0 after validation. Main/App/instance-link ownership and all Task23 owned files are frozen for integrator review; this worker staged/committed nothing.
