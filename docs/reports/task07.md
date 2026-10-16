# Task 07 — Vanilla and Paper provider evidence

Date: 2026-10-16. Status: implementation ready for integrator review; **package acceptance is pending lifecycle integration and real server/client qualification**. No ledger checkbox has been marked and no files were staged or committed.

## Ownership and contract clarification

Added `internal/server/vanilla/**`, `internal/server/paper/**`, `internal/server/providerutil/**`, and this report. The integrator explicitly authorized the only shared-file edit: name the parameters and clarify `domain.DistributionProvider.Install` in `internal/domain/contracts.go`. Its string argument is the **instance ID**, never an arbitrary directory. The older Task 01 report's directory interpretation is superseded by this clarification. Signature types are unchanged.

No router, main, schema, generated types, dependency, runtime implementation, instance implementation or supervisor implementation was changed.

## Interfaces and coordinator responsibilities

```go
vanilla.New(dataDir string, client *http.Client) *vanilla.Provider
paper.New(dataDir string, client *http.Client) *paper.Provider

// Both implement domain.DistributionProvider and expose:
Versions(ctx context.Context) ([]string, error)

// Paper additionally exposes:
Builds(ctx context.Context, minecraftVersion string) ([]string, error)
```

`Versions` returns eligible published stable Minecraft versions newest first, within the existing runtime manager's reviewed release rules. It does not assert tested support. Paper's `Builds` returns only downloadable `STABLE` numeric build IDs newest first. Catalog-listed historical versions can lack stable downloadable builds; no fallback or mirror is fabricated. `Resolve` requires an exact published tuple: Vanilla's `LoaderVersion` is empty; Paper uses **`VersionSelection.LoaderVersion` as the exact numeric Paper build ID**, such as `232`. Empty, leading-zero, experimental, unavailable and floating `latest` builds are rejected. The UI must label this field “Paper build”.

`Install(ctx, instanceID, plan, managedJava, progress)` re-resolves publisher metadata, rejects modified executable-affecting plan fields and revalidates the supplied managed runtime with `runtime.Manager.Get`. The runtime must exactly equal the managed record and satisfy the plan's Java requirements. Paths come only from `instance.Paths`; the coordinator must already have provisioned the instance's server directory. The provider cannot create an unregistered instance. The caller must also establish the database identity, job serialization, stopped-state, compatibility, backup and EULA gates. Provider `Install` returns a launch proposal and cannot establish those lifecycle conditions itself.

The returned `LaunchSpec` contains the actual managed Java executable, argv `-jar <absolute immutable server.jar> nogui`, the canonical instance `server` working directory, managed runtime ID and verified Java archive checksum. The caller inserts memory flags and owns persistence/activation. There is no shell launcher, bundled script execution, supervisor state write, `state/launch.json` write or server process execution.

## Executable layout and publication

```
instances/<instanceID>/server/.manager/artifacts/<plan-digest>/
  server.jar
  manifest.json
```

The plan digest is SHA256 over selection, launch requirements and artifact metadata with retrieval timestamps zeroed. Warnings, capability descriptions and fetched timestamps do not affect it. Updates produce a separate immutable release directory, so a previous launch specification remains valid. There is no mutable provider-owned active pointer. The final activation belongs to Tasks 09/18, which persist a selected `LaunchSpec` transactionally.

Download into a random sibling `.stage-*` directory, check exact size/published digest, validate the JAR/launch manifest and reject unsafe archive paths, then sync files and rename the complete directory. Operations below the provisioned instance server tree use `os.Root`; existing symlinks in canonical paths are rejected. Linux syncs publication directories. Windows syncs files but directory flushing and native rename behavior still need qualification. Concurrent publication of an existing directory only reuses it after full verification. Every normal failure/cancellation cleans the current stage; an abrupt process crash can leave an unreferenced `.stage-*` directory for later coordinator cleanup. Old releases are retained because a supervisor or rollback proposal may still reference them.

`manifest.json` records the canonical plan, source metadata URLs and local metadata SHA256/time, Mojang's published version-JSON SHA1 where applicable, local verified artifact SHA256 and verification time. Existing immutable releases are rehashed against the published checksum and local manifest digest before reuse. A corrupt existing release is rejected rather than overwritten. World files, configuration, mods, plugin data, `eula.txt`, and supervisor launch state are untouched. Publication never restores or rewinds world data.

## Official APIs and network policy

Sources inspected on 2026-10-16:

- [Paper Downloads Service](https://docs.papermc.io/misc/downloads-service/) — Fill v3 project/version/build endpoints, identifying User-Agent/contact requirement, `STABLE` channel, artifact URL/size/SHA256.
- [Paper project catalog](https://fill.papermc.io/v3/projects/paper).
- [Paper 1.21.4 builds](https://fill.papermc.io/v3/projects/paper/versions/1.21.4/builds) — live official API inspected directly, including build `232`.
- [Mojang version manifest v2](https://piston-meta.mojang.com/mc/game/version_manifest_v2.json) — exact release identity, per-version metadata URL and published metadata SHA1.
- [Mojang 1.12.2 metadata](https://piston-meta.mojang.com/v1/packages/832d95b9f40699d4961394dcf6cf549e65f15dc5/1.12.2.json) — live official metadata inspected directly, Java 8 and published server size/SHA1.
- [Minecraft Java server download](https://www.minecraft.net/en-us/download/server) — official distribution and user EULA responsibility.

Requests send `minecraft-server-ui/dev (https://github.com/jonasthim/minecraft-server-ui)`. Metadata has a 30-second context deadline and 8 MiB response limit. HTTP clients have at most a two-minute timeout; artifacts have a 512 MiB hard bound and must exactly match their published size. Exact HTTPS hosts/path shapes are allowed for `piston-meta.mojang.com`, `piston-data.mojang.com`, legacy official `launcher.mojang.com`, `fill.papermc.io` and `fill-data.papermc.io`. Artifact object path digests are bound to the declared digest; Paper filenames also match exact version/build. Credentials, query strings, custom ports, URL escapes, fragments, external hosts and all redirects are rejected. Network errors and malformed metadata errors do not echo raw attacker URLs or response text.

Mojang's published server SHA1 is verified as publisher provenance, and a local SHA256 is recorded in addition. Paper's published SHA256 is required. All per-version Mojang metadata is checked against its manifest-published SHA1 before parsing. Runtime policy is sourced from Task 06; observed Mojang `javaVersion` must equal that reviewed requirement. Unknown or changed tuples fail closed.

## Automated verification

Tests use inline official-shaped metadata and real local ZIP/JAR files. Only HTTP and the external managed-runtime verification boundary are replaced in transaction fixtures; filesystem staging, hashing, JAR inspection, publication and cache verification run for real. Ordinary tests are network-free, and the real download smoke is skipped without an explicit environment opt-in.

Coverage includes stable catalog filtering and exact version/build selection, wrong flavor, floors/unavailable releases, numeric build validation, metadata SHA1 mismatch, wrong artifact hashes/size/URL/filename, oversized or trailing metadata JSON, redirect refusal and error sanitization, malformed/unsafe JARs, runtime mismatch, traversal IDs, symlinks, tampered cached releases, and cancellation after download. Publication fixtures verify unchanged world/config/mod/plugin/EULA bytes, actual Java argv/cwd, no supervisor launch-state writes, deterministic cache reuse despite changed fetch time, and no candidate or stage after a canceled update. A missing-instance regression first failed because the provider created directories, then passed after creation was restricted to provisioned server roots.

Commands and observed results:

- Initial `go test ./internal/server/vanilla ./internal/server/paper`: RED, providers/constructors absent.
- Initial transaction `go test ./internal/server/...`: RED, shared install/client functions absent.
- New `Download` regression: RED, verified download API absent; GREEN after implementation.
- `go test -run TestMissingInstanceCannotBeProvisionedByProvider ./internal/server/providerutil`: RED before rooted/provisioned-path fix; covered by final GREEN suite.
- `go test ./internal/server/...`: PASS.
- `go test -race ./internal/server/...`: PASS on final code.
- `go vet ./internal/server/...`: PASS on final code.
- `go test ./...`: PASS on final code, including API, instance, runtime, launcher and supervisor packages.
- `env GOOS=windows GOARCH=amd64 go build ./internal/server/...`: PASS; cross-compilation only.
- `env MINECRAFT_UI_SERVER_SMOKE=1 go test -count=1 -run TestOfficialArtifactSmoke -v ./internal/server/providerutil`: PASS, both exact official artifacts downloaded and hashes verified (1.83 s). No JVM, game server, installer, world generation or EULA consent occurred.

One intermediate run encountered concurrent Task 09 compile errors in `internal/instance/service.go`; the owner resolved them, and the final full Go suite passed. The provider worker did not edit that package.

### Live official download evidence

At 2026-10-16 11:59:48–49 UTC:

| Tuple | Java requirement | Downloaded bytes | Published digest | Independent local SHA256 |
|---|---:|---:|---|---|
| Vanilla 1.12.2 | 8 | 30,222,121 | SHA1 `886945bfb2b978778c3a0288fd7fab09d315b25f` | `fe1f9274e6dad9191bf6e6e8e36ee6ebc737f373603df0946aafcded0d53167e` |
| Paper 1.21.4 build 232 | 21 | 51,437,498 | SHA256 `5ee4f542f628a14c644410b08c94ea42e772ef4d29fe92973636b6813d4eaffc` | `5ee4f542f628a14c644410b08c94ea42e772ef4d29fe92973636b6813d4eaffc` |

URLs are the manifest-pinned Mojang object and Fill-data object URLs returned by the production resolvers. The smoke exercises production metadata resolution and bounded/hash-verified streaming download to a discard writer. It does **not** demonstrate production `Install` with real managed Java or real JAR launch behavior.

## Remaining acceptance and qualification

Integrator review is required before the ledger is updated. Tasks 09/18 must wire providers and catalog methods into authenticated jobs/UI, implement stopped-state and backup transaction gates, persist selected launch specs, and retain/release referenced executable directories safely. Task 10 owns readiness parsing. A later, explicitly EULA-approved lifecycle test must install with real managed Java, start the exact servers, prove readiness, connect real clients, update under coordinator control and verify world/config preservation. Native Windows execution and durability are unqualified. The release version/layout matrix remains unqualified by these package tests. No stable support or complete Task 07 acceptance claim is made.
