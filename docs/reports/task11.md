# Task 11 — Players, history, whitelist, bans and operators

Implementation submitted for independent integration review on 2026-10-16. This report does not mark the package accepted. The integrator owns review, staging/commit and the implementation ledger. Started against `648891e`; shared composition was explicitly delegated after catalog freeze `fd825a7`. No Minecraft was executed, no EULA accepted, and no native host service installed or modified.

## Files and integration

Owned implementation:

- `internal/players/service.go`, `history.go`, `files.go`, `players_test.go`.
- `internal/db/migrations/00008_players.sql`.
- `internal/api/player_handlers.go`, `player_handlers_test.go`.
- Narrow console extensions `internal/console/players.go`, `players_test.go`; existing console RPC/session files are unchanged.
- `docs/api/players.yaml` and `web/src/features/players/PlayersPage.tsx`.

Delegated integration:

- `cmd/minecraft-ui/main.go`: construct/register players before paused startup recovery; mount inside the authenticated API; start the bounded sampling loop after recovery; cancel and drain it before console, jobs/native and database teardown. Unexpected loop exit shuts down HTTP and returns a safe failure. Existing startup reconciliation ordering is unchanged. `main_players_test.go` covers safe unexpected failure and normal cancellation.
- `web/src/App.tsx`: lazy `/instances/:instanceId/players` route. `InstancesPage.tsx`: visible instance link.
- `docs/openapi.yaml`: add only the new player paths/schema keys from the fragment. `web/src/api/schema.d.ts`: regenerated with `cd web && npm run gen:api`. Structural comparison against HEAD verified every prior path/component unchanged. No hand-edited generated types or dependencies.

## Frozen interfaces and identity limits

`players.New(db, paths, *instance.Service, *console.Service, *jobs.Service, Native)` exposes `Register`, `Run(ctx)`, `Poll(ctx,id)`, `Snapshot(ctx,id)`, `Lists(ctx,id,kind)`, `History(ctx,id,after,limit)`, `HistoryWithEvents(ctx,id,after,eventAfter,limit)`, and `Enqueue(ctx,id,Mutation,actor)`. `Native` is the narrow native identity/inspection/stopped capability. Runtime mutations continue through the existing console transport; no second raw command transport exists.

Console extensions are `Service.PlayerSnapshot`, `Service.NativePlayerList`, and callback-only `Session.PlayerSnapshot`, `Session.PlayerList`, `Session.MutatePlayer`. Read-only service probes use existing idle admission and session lifetime/generation checks. Job mutations use exactly one current-job console session or stopped instance session. `PlayerMutationResult` separates `Received`, `Acknowledged`, and `ReadbackVerified`; legacy acknowledgements do not claim independent readback.

- `PlayerIdentity`: optional `uuid`, optional `name`, `authority`, `source`. A server UUID is instance-local evidence and does not prove online Mojang account authentication. Name-only evidence never generates a UUID. Missing both fields, unsafe names/UUIDs, duplicate UUIDs, and ambiguous duplicate names are rejected. UUID-only native evidence is retained.
- `PlayerSnapshot`: instance ID, captured generation, observation time, known/complete flags, observed count/players, gap and reason. No position/dimension fields are exposed. Task20/22/23 must use proven evidence; names/counts never imply UUIDs or coordinates.
- `PlayerSession`: first/last seen, optional ended observation boundary, generation, identity provenance and gap/end reason. These are sampled observations, not fabricated exact join/leave times. Restarts, stale intervals and failed probes close an interval with `observation_gap`.
- `PlayerHistoryEvent`: independent event ID/source console cursor, ingestion observation time, exact recognized join/leave message and name-only provenance. Chat/plugin-prefixed lookalikes are ignored. Events are not joined to UUIDs from other lines. Session and event cursors are independent and typed; foreign/future/expired cursors replay with gaps.
- Polling: 30 seconds, two fixed workers, bounded pending work, 45-second snapshot freshness. Viewer refresh reads the cache and creates no durable jobs. History retains at most 30 days AND 10000 sessions per instance; independently, 30 days AND 20000 log events. Console cursors, floors and observed sessions survive service reconstruction.

Native log availability is not completeness. History explicitly discloses unavailable ingestion and known 26.3 join/leave omissions; absent events never prove no joins or leaves. Exact recognition is intentionally conservative and may miss loader/plugin-specific lines. Short sessions between polls, manager downtime, rotations, and retained-log loss remain visible limitations.

## Lists, mutations and authorization

Viewer reads; operator kick/whitelist/ban changes; administrator OP grants/revocations and explicit operator permission levels. HTTP gates include required token scopes before enqueue. Every job rechecks the current user role/disabled state before admission. Request/result audit records target UUID/name, action, explicit mode and outcome; it excludes arbitrary reasons, credentials and raw native output. Failed/partial jobs retain a safe authored reason in job output.

Mutation mode is explicitly `online` or `offline`. Unknown process/control state never falls back to filesystem writes. Online modern operations require a UUID and discovered native methods/results, reject missing readback capabilities before mutation, validate the fresh returned list/identity, then independently read back membership/permission or roster absence. Every request and response uses Task10's captured-generation guard. Delivery alone cannot establish success. Readback failure is a failed/possibly partial operation, with guidance to inspect before retrying.

Legacy online commands accept only validated exact usernames and fixed native default messages: kick, whitelist add/remove, ban/pardon, op/deop. Untrusted reasons and arbitrary console syntax are absent from the request contract. Explicit UUID targeting and explicit OP levels are rejected before delivery on legacy transports; the server's default OP level is read back. Commands require fresh exact native feedback, then stable saved JSON membership/permission readback; kicking requires a fresh roster absence check. Legacy list calls retain names only. 1.12.2's two-line header/roster pair is parsed with count agreement; modern legacy list syntax is separately recognized. Unsupported or altered plugin feedback fails visibly.

List reads remain available while running: authenticated native management when available, otherwise a bounded saved-native-JSON snapshot with source/time and `runtime_verified=false`. Saved files do not establish plugin runtime state. Only typed identity/permission summaries are returned; ban source/reason/private fields are not exposed.

Offline writes execute only under `instance.WithStoppedJob`. UUID is explicit; additions also require a username, conflicting saved identities fail, and kick is unavailable offline. Known list filenames are derived from contained instance paths. JSON is bounded to 4 MiB/10000 entries, opened through safe root handles with file identity and repeated byte checks; symlinks, malformed/duplicate fields, invalid identities and malformed OP fields fail before staging. Missing lists mean an empty saved native list. Atomic stage/fsync/rename and readback are used; staging cleanup and post-activation durability failures propagate honestly. Native JSON has no comments; commented/invalid files are preserved unchanged. Unrelated entry object bytes, whitespace and unknown fields survive; array separators/outer whitespace can normalize after a real edit. Native Windows durability still needs qualification.

## Primary static input evidence

Official cached artifacts were inspected without running Minecraft; SHA-1 was rechecked:

| Artifact | SHA-1 |
| --- | --- |
| 1.12.2 server | `886945bfb2b978778c3a0288fd7fab09d315b25f` |
| 1.21.9 server | `11e54c2081420a4d49db3007e66c80a22579ff2a` |
| 26.3 server | `33680f5f2ac32864d6d7cf5e56a705fdb3e05f4c` |

Sources are the approved cache `/home/thim/.cache/dilla-tmp/minecraft-console-schemas` (official mappings/inner JARs) and `/home/thim/.cache/dilla-tmp/minecraft-instance-evidence-1.12.2.jar`. `javap` and bundled language assets establish 1.12.2 `cq`/`pl` two-line list grammar and mutation acknowledgements; 1.21.9 mapped `IncomingRpcMethods`/`PlayerDto`/operator/ban/kick DTOs; 26.3 named equivalents, allowlist/ban/operator whole-list results and independent getters. Both modern protocol versions use numeric `permissionLevel`: 26.3 uses `PermissionLevel.INT_CODEC`, despite its internal enum. PlayerDto contains optional ID/name, with no positions. See official [1.21.9 release notes](https://www.minecraft.net/en-us/article/minecraft-java-edition-1-21-9) and [26.3 release notes](https://www.minecraft.net/en-us/article/minecraft-java-edition-26-3). Static shapes/acknowledgements are protocol inputs, not runtime interoperability qualification.

## Verification

Scoped commands passed:

```text
go test ./internal/players ./internal/console -count=1
go test -race ./internal/players ./internal/console -count=1
go test -race ./internal/api -run TestPlayers -count=1
go test ./cmd/minecraft-ui -run 'TestPlayersFailure|TestConsoleFailure|TestStartup' -count=1
go vet ./internal/players ./internal/console ./internal/api ./cmd/minecraft-ui
cd web && npm run gen:api
cd web && npm run typecheck
cd web && npm run lint
cd web && npm run build
```

Tests cover partial/invalid/ambiguous identities; modern protocol1.0.0/3.1.0 fresh acknowledgements, missing discovered readback before effects, malformed acknowledgements, mismatch and stale generation; expired sessions; fresh legacy names and 1.12.2 paired roster; stopped mutation single admission; actual paused durable queue plus authority revocation; running process rejection; malformed JSON/OP fields, unknown-field/whitespace preservation, duplicate JSON keys and symlink rejection; explicit role matrix and token-scope denial; stale snapshots; generation/dedupe/retention/count limits; independent log cursors and foreign-instance cursor gaps; safe failed-job output and unexpected sampler shutdown. Regressions were observed failing before fixes. Tests use local SQLite/files and loopback native protocol fixtures; no actual server/client is claimed. The broad suite was deliberately left to the integrator while peer packages were in progress.

## Actual browser evidence

The in-app runtime initialized but reported no available browser; documented discovery returned an empty list. The integrator explicitly delegated isolated standalone Playwright as fallback. A clean Chromium context at 390×844 used the real embedded Linux binary and fresh task-owned manager database on loopback18093. No user profile or seeded server/player/world rows were used.

- Actual first administrator form → empty Servers → Create server → searchable official Vanilla1.12.2 selection → `players-mobile`, with EULA unchecked.
- Opened the visible Players/native lists link with keyboard Enter; keyboard Refresh worked. The page showed unknown presence and an explicit gap, empty saved native list with runtime-unverified provenance, and empty observed history. No fake online players were injected.
- Created viewer/operator accounts through the actual Users UI. Viewer could read native lists and had no mutation form; operator had ordinary mutations and no OP grant/revoke options. Administrator retained those options.
- Real queued online kick against the uninstalled/nonrunning instance transitioned to failed, with linked Jobs output. Browser review exposed missing safe job output; a failing backend regression was added, the journal fix applied and the binary rebuilt. The repeated real job now shows `managed JVM identity is unavailable` in Jobs/Details. No native process was started.
- No horizontal document overflow at390px on administrator/viewer/operator player screens; full screenshots were inspected. No page JavaScript errors in the completed review.

Artifacts: `/home/thim/.cache/dilla-tmp/task11-players-mobile390.png`, `task11-players-failure390.png`, `task11-viewer390.png`, `task11-operator390.png`. The review manager was shut down after validation; task-owned scripts/data remain under that cache directory for reproducibility. These observations cover real manager UI flows, not live game/player or OS service qualification.

## Remaining review/qualification

Independent integrator review and ledger acceptance are required. Real Minecraft/client evidence remains pending across the full required version/layout/flavor matrix, including actual successful player events/mutations, offline-mode UUID behavior and loader/plugin overrides. Native Linux service/systemd and Windows execution/durability qualification remain pending. Legacy parsers intentionally reject unfamiliar/localized/plugin feedback rather than reporting success. No positions are supplied to maps/telemetry, and no release support combination is claimed from fixtures or cross-compilation.

## Fix round 1 — independent review findings (2026-10-16)

Scope: two Important findings from independent review of `5a664de`. This round changes only `internal/players/**` and this report. Existing console/session/RPC files, API/generated types, shared composition, and peer world/map/backup work remain untouched. No staging, commit, subagents, Minecraft/EULA actions or host service changes occurred. These fixes are submitted for independent re-review, not package acceptance.

1. **Ancestor replacement before player list root acquisition.** The three saved-list paths previously called `os.OpenRoot(dirs.Server)` after path validation. An instance ancestor replaced with a symlink in that gap could redirect viewer reads, legacy acknowledgement readback, or offline writes outside the instance. Regressions against that absolute-opening behavior reproduced exposure of an outside saved identity and a changed outside whitelist. `root.go` now follows the reviewed mapdata `42c5114` policy: anchor at the filesystem root/local Windows drive, traverse one pinned component at a time, compare pre-open/current/opened identities, retain ancestor handles and verify the full chain before returning the root. Symlink/reparse directories and UNC/device/extended volumes are refused. All three service paths use this boundary. Read results require verification before and after reading; offline mutation verifies before reading/staging, immediately before activation, and after mutation/readback. A swapped ancestor cannot retarget the pinned handles. If a change is observed, the operation fails honestly rather than exposing outside bytes or claiming a successful mutation.

   Deterministic evidence covers substitution after `Paths.For` for both reads and offline writes; substitution after root opening (pinned reads still reach the original tree and final verification fails); substitution after staging before rename (activation is refused, inside/outside files stay byte-identical, staging is removed); and the actual `Service.Lists` boundary, using its existing native identity dependency to replace the ancestor after path validation and before opening. The initial regressions failed with `OutsideSecret` exposure, outside whitelist mutation, and missing final ancestor rejection; all now pass. Native Windows execution remains pending; the Windows directory policy checks reparse attributes and was cross-compiled only.

2. **Null OP fields accepted as zero/false.** Separate regressions for `level:null` and `bypassesPlayerLimit:null` reproduced successful saved reads and activation of another OP entry while preserving the malformed original entry. Saved OP scalar decoding now uses presence/null-aware pointers and requires non-null, correctly typed values before staging. Each regression asserts read rejection, mutation rejection, unchanged original bytes and absence of staging artifacts. Both failed before the fix and pass afterward. Static inspection of the approved official 26.3 `ServerOpListEntry` confirmed that present native fields invoke `getAsInt`/`getAsBoolean`, rather than treating JSON null as a valid zero/false permission value. No game was executed.

Files: `internal/players/root.go`, `root_nonwindows.go`, `root_windows.go`, `root_test.go`, `files.go`, `service.go`, `players_test.go`, and this report. The public API/DTO contract is unchanged.

Scoped verification after both fixes:

```text
go test ./internal/players -run 'TestListsServiceRejectsSwap|TestSavedListsRejectAncestor|TestPlayerServerRootPins|TestOfflineActivation|TestNullOperator' -count=1
go test ./internal/players -count=1
go test -race ./internal/players -count=1
go vet ./internal/players
GOOS=windows GOARCH=amd64 go test -c -o /home/thim/.cache/dilla-tmp/task11-players-fix1-windows.test.exe ./internal/players
git diff --check -- internal/players docs/reports/task11.md
```

No broad suite or repeat browser run was needed for these internal filesystem/decoder fixes; the existing browser evidence does not substitute for the new deterministic regressions. Integrator re-review and all existing real game/native platform qualification gates remain required.
