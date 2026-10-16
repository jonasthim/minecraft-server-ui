# Task15 — Catalog clients and safe downloads

Implementation submitted for integrator review. This report does not accept the work package or change the implementation ledger.

## Delivered boundary

Modrinth mods/modpacks/plugins, CurseForge mods/modpacks and Hangar Paper plugins use their real official metadata APIs. Discovery preserves exact project/version/file identifiers, publisher links, published integrity and sizes, version environment/loader/Minecraft declarations, required/optional dependency relationships, metadata source and fetch timestamp. Hangar version resolution uses its version name in `id`, while `upstream_id` preserves its distinct numeric version ID. Minecraft/game-platform data is kept distinct from tested release support; every compatibility record has `tested: false`. Legacy missing Modrinth environment remains unknown; client-only and singleplayer-only versions are explicitly server-unsupported. Hangar's Paper metadata never unions proxy platform versions into Minecraft versions.

`catalog.New(Config{DB, ArtifactDir, UserAgent, Transport})` constructs the service. Composition owns `ArtifactDir`, derived as `<data root>/catalog/artifacts`; it is never an API parameter. Optional `Transport` permits deterministic tests without adding dependencies. Internal consumers use:

- `Providers(ctx)` for masked availability.
- `Search(ctx, Query{Provider, Kind, Text, Minecraft, Loader, Offset, Limit})`.
- `Project(ctx, provider, projectID)`; returned canonical IDs should be used thereafter.
- `Versions(ctx, provider, projectID, Query)` and `Version(ctx, provider, projectID, versionID)`.
- `Download(ctx, Selection{Provider, ProjectID, VersionID, FileID})` resolves publisher metadata again, selects an exact file and returns `VerifiedArtifact` with provenance, published file record, compatibility evidence, local SHA256 and private `Path` (`json:"-"`). There is no arbitrary URL fetch or host path API.
- `ConfigureCurseForge(ctx, actor domain.User, key)` checks administrator role, replaces/clears the private DB key and atomically audits the action without key value or length.

Task16 owns activation and all content lifecycle/config edits. Task17 owns archive validation, server-item selection and three-way pack application. No content installation, archive extraction, add-on execution, game start, EULA consent or version qualification is claimed by this package.

## Credentials and limits

Migration00007 adds a private singleton `catalog_settings` row. No environment secret is introduced or inherited by JVMs. The key is never returned in API responses or passed to download requests. Configuration returns only availability; configured means stored, not that the remote provider has accepted the key. Upstream access failures remain structured/redacted. Administrator replace/clear checks exist both at API and service; credential input is at most512bytes and request body1024bytes. Existing protected SQLite file permissions are the storage boundary; there is no encryption-at-rest claim.

Metadata requests carry a descriptive product/contact user-agent, context deadline15seconds, response limit8MiB, page size1..50 (Hangar max25), offset0..10000, bounded identifier/filter lengths and a five-minute in-memory cache. Cache caps are256entries and32MiB aggregate raw metadata bytes. Credential changes invalidate cached metadata using a generation fence, so an old in-flight request cannot repopulate the current generation. Credential failure bodies are never cached. Modrinth has no version-list paging on the documented endpoint: its bounded response is capped at10000versions and then paged locally. Oversized lists fail explicitly.

Artifacts require exact positive published size up to2GiB and valid published SHA1/SHA256/SHA512. MD5-only files require manual resolution. All available supported published hashes are checked and local SHA256 is recorded. SHA1 provenance is preserved rather than upgraded into a stronger publisher assertion. Downloads use two shared slots, a ten-minute context deadline, exact HTTPS publisher CDN hosts, no userinfo/query/fragment/ports, traversal refusal, bounded streaming, private random staging and atomic content-addressed hard-link publication. Redirects are refused for both metadata and files, preventing credential forwarding or cross-provider fetches; an unapproved or redirect-only publisher artifact requires manual resolution. Existing digest files are reverified and never overwritten, including corruption. Linux synchronizes the publication directory. No native Windows filesystem qualification is claimed.

Approved direct artifact hosts are `cdn.modrinth.com`, `edge.forgecdn.net`, `mediafilez.forgecdn.net` and `hangarcdn.papermc.io`. Other links remain official-page/manual-resolution paths; no arbitrary external hosts are downloaded. Cancellation removes only the operation's staging file; a cleanup failure returns an explicit review-required error, preserving the original cause. The writable staging alias is removed before the published inode becomes read-only, avoiding Windows readonly alias deletion semantics. Publisher filenames are screened for traversal and Windows unsafe forms even though storage names use only a generated digest.

CurseForge nullable/restricted URLs remain manual with the official release link. Attached `serverPackFileId` and `isServerPack` evidence are preserved; browsing a client pack does not claim that it runs as a server pack. Hangar external required dependencies remain explicit manual links and required status, never silently satisfied or downloaded.

## API/schema/UI integration

`MountCatalog` is composed inside the shared authenticated and CSRF-protected API group. Viewer browsing routes are:

- `GET /api/v1/catalog/providers`.
- `GET /api/v1/catalog/{provider}/search`.
- `GET /api/v1/catalog/{provider}/projects/{projectId}`.
- `GET /api/v1/catalog/{provider}/projects/{projectId}/versions`.
- `GET /api/v1/catalog/{provider}/projects/{projectId}/versions/{versionId}`.

`PUT /api/v1/catalog/curseforge-key` is administrator-only and accepts `{ "key": "..." }`; an empty value clears it. Existing token scope admission applies. No browser artifact download mutation exists in this task.

`docs/api/catalog.yaml` is merged into the root schema and types regenerated with `npm --prefix web run gen:api`. The preexisting dormant foundation `CatalogProject`/`CatalogVersion` contracts remain untouched: owned richer contracts use `CatalogProjectMetadata`/`CatalogVersionMetadata`. Other added schema names are `CatalogAvailability`, `CatalogProviders`, `CatalogKeyInput`, `CatalogCompatibility`, `CatalogDependency`, `CatalogFile`, `CatalogSearchResult`, and `CatalogVersionPage`. Removing the new owned schemas/paths from the merged document produces the same preexisting structure as HEAD. No hand-edited generated types. The merged document has incidental existing schema text reordering; structural comparison confirms only owned additions.

The lazy `/catalog` Mantine page and navigation show real results, page controls, publisher evidence/hashes/dependencies, credential availability and administrator replace/clear forms. Viewers have no credential controls. It reports missing key, errors, manual steps, unknown compatibility and unqualified tested support. Exact `/catalog` and existing `/jobs` were added to the embedded SPA route allowlist after a real deep-link reload404; focused tests preserve unknown health/API/assets404s and refuse POST fallback.

## Evidence

Commands below were run on the Linux development host with deterministic fixtures and temporary databases. The default Go package suite stays network-free; official smoke is explicit opt-in.

- `go test -race ./internal/catalog ./internal/api ./internal/db ./cmd/minecraft-ui` — passed.
- `go vet ./internal/catalog ./internal/api ./internal/db ./cmd/minecraft-ui` — passed.
- `npm --prefix web run gen:api`, `typecheck`, `lint`, `build` — passed.
- `go build -o /tmp/minecraft-catalog-smoke ./cmd/minecraft-ui` — passed.
- `CATALOG_OFFICIAL_SMOKE=1 go test ./internal/catalog -run TestOfficialSmoke -count=1 -v` — passed against official read-only APIs/CDN. Modrinth mod search47results, exact FabricAPI version`Mys3P7lK`, environment`client_or_server_prefers_both`; plugin search11results (selected version environment remained`unknown`); modpack search1584results, selected version`Q89Og6Ep` explicitly`client_only`; Hangar search34results, version`5.12.2-SNAPSHOT+1104`, upstream numeric version31715, Paper artifact only. Counts are time-specific observations, not fixture expectations.
- The official smoke downloaded one inert FabricAPI artifact:2452735bytes, localSHA256`79ac44b40780acbd884b34c50be1e39af682847e5f5cb3b1fddeeaa768dce800`, after checking official published hashes. Temporary test storage was cleaned. No key was supplied and no live CurseForge credentials were used.
- `CATALOG_SMOKE_URL=http://127.0.0.1:18085 node internal/catalog/testdata/ui-smoke.cjs` against a fresh disposable, built embedded application — passed Chromium390x844: real Modrinth search/release inspection, keyboard Enter submit, no horizontal overflow, disabled CurseForge disclosure, viewer credential control absence and API403, no install buttons/claims or browser page errors. Screenshot:`/tmp/catalog-mobile-smoke.png`.

Independent, hand-authored provider-shape fixtures cover unavailable key/no outgoing request, exact filters/paging/cache, new Modrinth client-only environment, dependency requirements, malformed/null/oversized JSON, request cancellation/deadline, traversal/unsafe identifiers, nullable CurseForge/manual URLs and attached server-pack evidence, Hangar external required dependencies and Paper/proxy separation, exact upstream IDs and canonical official links, cache byte bound, credential and artifact redirects, hash mismatch, unsupported integrity, cancellation cleanup, immutable publication/reuse, refusal to replace existing corrupt bytes, Windows unsafe names and a real Linux filesystem permission failure proving retained staging is reported. API tests cover anonymous/viewer/operator/admin admission, key clear/replace, input limits, audit redaction and missing-key503. Focused SPA tests first reproduced the actual reload defect and then passed after the bounded fix.

## Primary provider references and Task17 handoff

Consulted current official documentation and actual read-only official API responses on2026-10-16:

- [Modrinth search](https://docs.modrinth.com/api/operations/searchprojects/), [project](https://docs.modrinth.com/api/operations/getproject/) and [version](https://docs.modrinth.com/api/operations/getversion/). Actual v2 responses include new version`environment`, project environment arrays and exact file IDs. Project-wide side declarations are not inherited as version qualification.
- [Modrinth environment explanation](https://modrinth.com/news/article/new-environments/) distinguishes the nine declared environments. Client-only and singleplayer-only are excluded from server compatibility; missing/unknown declarations remain unknown.
- [Official mrpack specification](https://support.modrinth.com/en/articles/8802351-modrinth-modpack-format-mrpack): ZIP root`modrinth.index.json`, formatVersion1/gameMinecraft; file entries include path, mandatory SHA1/SHA512, downloads and fileSize. Optional `env.server` is a distinct physical-side enum(required/optional/unsupported), not the newer API version environment enum. Unsupported server files must be excluded, optional files need user selection, and absent evidence needs conservative review. Apply common overrides then server overrides; never client overrides. Loader dependency IDs are explicit and unknown IDs must remain unresolved. This task does not parse/apply packs. The narrower Task15 CDN policy sends non-CDN/GitHub/GitLab pack entries to later manual resolution instead of expanding arbitrary fetch permissions.
- [CurseForge official REST API](https://docs.curseforge.com/rest-api/) documents API-key authentication, game432, mod class6/modpack class4471, file hashes(SHA1/MD5), dependency relation codes, nullable alternate/server pack IDs and direct download APIs. No redistributed key or inferred download URL is used.
- [Hangar official API docs](https://hangar.papermc.io/api-docs) and [publisher OpenAPI](https://hangar.papermc.io/v3/api-docs) specify anonymous public metadata, page cap25, Paper platform downloads/fileInfo/sha256Hash, externalUrl and platform-specific plugin dependencies. Live publisher CDN responses establish the exact Hangar CDN host.

## Remaining qualification and dependent work

Integrator review and ledger acceptance are pending. Native Windows execution remains unqualified. No live CurseForge smoke is claimed without an operator's private key. No content activation, pack extraction/application, dependency installation, trial uploads, automatic update qualification or full release matrix is claimed. Task16/17 must gate side/loader/game compatibility, required/manual dependencies, strict archive/path layout and transaction/backup requirements before changing any instance. Unknown evidence blocks automatic content/update qualification; a hash-verified artifact alone does not establish content compatibility.

## Independent review — fix round1

Addressed both Important and all four Minor findings from review of `fd825a7`. Re-review and integrator acceptance remain pending; no package acceptance or release qualification is asserted here.

1. Credential configuration now requires a present, nonnull JSON string. `{}`, `null`, `{ "key": null }` and values of the wrong JSON type return422 without changing the stored key or adding an audit mutation. An explicit empty string still clears the key. The pointer used for request decoding does not change the existing OpenAPI contract, which already requires a string field.
2. Every exact CurseForge version/list/download flow validates its canonical project with the existing game432 and mod/modpack class6/4471 checks before processing files. Each file also requires its own `gameId` to be432; missing/foreign game IDs are rejected. Download inherits these checks before any CDN request. Deterministic positive cases cover both mods and modpacks; negative cases exercise foreign project games, unsupported Minecraft classes, and missing/foreign file games across all three entry points.
3. Modrinth search `versions` is mapped to Minecraft versions only inside the search adapter. A project response uses only `game_versions`; its `versions` release IDs can never become compatibility claims when game-version metadata is missing.
4. Modrinth nullable slugs remain usable through their canonical IDs. Nonempty slugs accept the publisher-documented punctuation and length rules. Publisher links continue to use the validated canonical project ID, so nullable or punctuation-bearing vanity slugs cannot drop an otherwise valid search page.
5. Hangar's numeric dependency fallback page is rebound to the canonical owner/project/version page when project metadata becomes available. Explicit external dependency links remain intact.
6. Changing catalog kind aborts the current discovery request and resets results, releases, selected detail, loading/error/paging state and incompatible loader selection. Existing per-request abort guards prevent late results from being applied, including a fetch implementation that completes despite cancellation. Provider changes share the same reset boundary.

All five backend defects were reproduced with new focused regressions before implementation, including the credential mutation/audit effects and the exact CurseForge entry-point bypasses. The UI regression reproduced the old stale-project result before the fix. The new self-contained `internal/catalog/testdata/ui-stale-request.cjs` serves the built application on an ephemeral loopback port, supplies deterministic API fixtures and holds search/release completion independently of AbortSignal. After a content-kind change, both delayed response cases remain discarded and loading state is cleared at390px.

Final round1 verification:

- `go test ./internal/catalog ./internal/api -count=1` — passed.
- `go test -race ./internal/catalog ./internal/api` — passed.
- `go vet ./internal/catalog ./internal/api` — passed.
- `npm --prefix web run typecheck`, `lint`, `build` — passed.
- `node internal/catalog/testdata/ui-stale-request.cjs` — passed both delayed search and delayed release cases at390px, without browser errors or external network requests.
- `CATALOG_OFFICIAL_SMOKE=1 go test ./internal/catalog -run TestOfficialSmoke -count=1 -v` — passed again against the official public Modrinth/Hangar metadata APIs and inert verified FabricAPI CDN artifact. No CurseForge key or live CurseForge verification is claimed.

Rechecked [official Modrinth search metadata](https://docs.modrinth.com/api/operations/searchprojects/), [project metadata](https://docs.modrinth.com/api/operations/getproject/) and [CurseForge file/project schemas](https://docs.curseforge.com/rest-api/) for the distinct version fields, nullable/punctuation-bearing slug contract, `gameId` and content-class fields. No exported DTO, API schema, generated types or shared composition change was required for this round.
