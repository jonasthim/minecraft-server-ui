# Task 08 — Fabric, Forge and NeoForge provider evidence

Date: 2026-10-16. Status: implementation submitted for integrator review. Product acceptance remains pending lifecycle integration, real server/client workflows and native Windows evidence. No ledger checkbox was changed; no staging or commits were performed.

## Ownership and frozen integration APIs

Owned changes are exclusively `internal/server/{fabric,forge,neoforge,loaderutil}/**` and this report. No shared domain, runtime, instance, main, router, schema, generated types, dependency or providerutil implementation was edited.

```go
fabric.New(dataDir string, client *http.Client) *fabric.Provider
forge.New(dataDir string, client *http.Client) *forge.Provider
neoforge.New(dataDir string, client *http.Client) *neoforge.Provider

// Each implements domain.DistributionProvider and also exposes:
Versions(ctx context.Context) ([]string, error)
Loaders(ctx context.Context, minecraftVersion string) ([]string, error)
ManualDownloadURL() string

loaderutil.VerifyLaunch(ctx context.Context, dataDir, instanceID string,
    spec domain.LaunchSpec) error
```

`LoaderVersion` is the exact Fabric loader version, Forge loader build (without its Minecraft prefix), or complete NeoForge loader version. Floating labels are rejected. Runtime policy applies the Minecraft 1.12.2 floor, Fabric 1.14 floor and NeoForge 1.20.2 floor before network or filesystem mutation. Exact availability comes from official metadata and verified artifacts, never from a filename alone. Fabric Meta catalog decoding ignores historical client main-class shapes while the selected server tuple has strict supported launch-class checks.

Catalog methods are discovery, not support claims. Forge Maven carries explicit Minecraft-build coordinates; NeoForge Maven only carries loader versions, so NeoForge Versions/Loaders use naming conventions to narrow discovery candidates. As approved by the integrator, selecting a candidate MUST call Resolve and surface its result before installation confirmation. Resolve downloads the checksum-verified official installer and binds `install_profile.minecraft`, `version.json.inheritsFrom`, exact loader identity and supported installer schema. A plausible version name cannot override different installer metadata. Loaders are ordered numerically where applicable. New release families outside reviewed runtime rules are rejected.

Install re-resolves metadata, compares the executable-affecting canonical plan, revalidates the exact supplied managed runtime with runtime.Manager.Get and checks the runtime record for equality. Paths are derived through instance.Paths; an existing provisioned server directory is mandatory. The coordinator owns database identity, job serialization, stopped-state, backup, EULA and final activation. Install returns a LaunchSpec proposal and does not write active launch state or start a game.

## Immutable executable layout and launch behavior

```
instances/<id>/server/.manager/loaders/<plan-runtime-generation-sha256>/
  manifest.json               # schema 2, canonical plan, exact managed runtime,
                              # metadata source digests/times, every file SHA256,
                              # exact provider argv, verification time
  server.jar                 # Fabric: original hash-verified Mojang artifact
  minecraft/{versions,libraries}/... # Fabric: verified bundled nested JARs
  libraries/...              # exact Fabric or installer libraries
  installer.jar              # Forge/NeoForge: published-hash verified installer
  installer-output.txt       # bounded private diagnostic output
  ...                        # generated installer executable files
```

An unpredictable private sibling stage is populated and validated before rename. Go file operations use os.Root; canonical existing paths and archive entries reject symlinks/traversal, Windows device names, control characters, alternate separators, case collisions and unsafe types. Bounded JAR parsing never blindly extracts arbitrary entries. Ordinary failures/cancellation remove their stage; abrupt process crashes may leave unreferenced .stage directories for coordinator cleanup. Existing releases are verified before reuse and never overwritten when corrupt. All regular files and Linux directories are synced before publication; Windows file sync/cross-build exists but native rename/durability remains unqualified.

Fabric uses exact loader, intermediary, server/common libraries from Fabric Meta and independently verified Maven sidecar hashes. It launches the verified KnotServer class through an explicit classpath. For legacy unbundled servers, fabric.gameJarPath names the immutable Mojang JAR. For modern bundled servers, installation extracts only the nested files listed by the hash-verified Mojang bundle and checks their internal published SHA256; the game property and classpath name the extracted immutable server/library files. The Minecraft bundler is never executed. Linux and Windows use their native classpath separator with absolute paths; the working directory stays the mutable instance server directory.

Forge/NeoForge run only the hash-verified official publisher installer through the exact managed Java executable in private staging, with a ten-minute execution deadline. The process receives an allowlist environment containing managed JAVA_HOME/PATH and private HOME/TEMP; JVM/native injection variables and inherited credentials are absent. Linux cancellation kills the installer process group; Windows uses a suspended child assigned to a kill-on-close Job Object before resume. Output capture is bounded. Publisher shell/batch scripts are removed and never executed. Processor-generated files are independently checked against every published server data/processor hash. Installer/version JSON library SHA1 and Mojang bundled-library SHA256 are checked wherever published. Outputs without published hashes carry explicit provenance from the verified installer plus local SHA256 receipts; those receipts are integrity records, not an independent publisher signature.

Modern Forge/NeoForge argument files are required to match bytes embedded in the pinned installer. A conservative Java argument parser expands them directly into argv; every module path/classpath/library-directory reference becomes an absolute reference to that immutable release. Required referenced files must exist. No shell or .bat/.sh launcher is involved. Legacy Forge uses its executable JAR and validates the manifest Class-Path files, which Java resolves relative to the immutable JAR. The exact game/client/native Windows launch remains unqualified.

Updates publish a new immutable directory; a retained previous LaunchSpec remains a rollback proposal. Publication never rewinds worlds or restores data. The lifecycle/update coordinator must transactionally persist activation and retain all referenced releases. No provider-owned active pointer, supervisor state, world/config/mod/plugin data or EULA file is modified.

## Offline launch verification handoff

The integrator requested an offline gate for later native game launch. VerifyLaunch checks the canonical instance working directory, immutable release identifier, schema-2 receipt and combined canonical plan/runtime generation digest, complete file inventory, each pinned artifact, published installer/output/bundled-library hashes and rebuilt provider argv. It binds the receipt Java executable, runtime ID, major and archive checksum to LaunchSpec. The native launcher separately runs runtime.Manager.Get to verify all runtime files and host identity before using this verifier.

Only the native-owner helper launchargs.StripMemory may extend provider argv: no prefix, or exactly `-Xms<min(512,max)>M -Xmx<max>M`, with canonical decimal max 256..1048576. The rest must equal the rebuilt baseline exactly. Extra environment overrides, arbitrary flags, alternate classes, mixed release paths, changed files or changed arguments are rejected offline. No network or game execution occurs during verification. The coordinator remains responsible for binding the selected memory amount to database configuration at activation.

**Integration dependency:** loaderutil imports the native-owner package `internal/server/launchargs` and calls `StripMemory([]string) ([]string, error)`. That new package must be committed before or together with these providers; committing Task08 alone without the native-owner helper would produce a broken intermediate build. The integrator owns its commit ordering and the static `internal/server/verify.go` dispatch wiring.

## Official primary sources inspected

- [Fabric Meta API](https://github.com/FabricMC/fabric-meta/blob/master/README.md): game/loader catalogs and exact loader tuple launcher metadata.
- [Exact Fabric 1.20.1 / 0.16.14 metadata](https://meta.fabricmc.net/v2/versions/loader/1.20.1/0.16.14): exact intermediary, main class, Java floor and server/common libraries.
- [Fabric loader server launcher](https://github.com/FabricMC/fabric-loader/blob/master/src/main/java/net/fabricmc/loader/impl/launch/server/FabricServerLauncher.java) and [bundler processing](https://github.com/FabricMC/fabric-loader/blob/master/minecraft/src/main/java/net/fabricmc/loader/impl/game/minecraft/BundlerProcessor.java): gameJarPath and modern bundled behavior. These informed the direct launch; no publisher Java source was copied.
- [Forge official Maven catalog](https://maven.minecraftforge.net/net/minecraftforge/forge/maven-metadata.xml), [Forge installer source](https://github.com/MinecraftForge/Installer), and hash-verified installer profile/version/argument files.
- [NeoForge server installation](https://docs.neoforged.net/user/docs/server/), [NeoForge Maven catalog](https://maven.neoforged.net/releases/net/neoforged/neoforge/maven-metadata.xml), and hash-verified installer profile/version/argument files.
- [Mojang release manifest](https://piston-meta.mojang.com/mc/game/version_manifest_v2.json) through reviewed Vanilla Resolve; exact Mojang server provenance and published SHA1 are preserved.

Metadata requests have thirty-second deadlines and eight-MiB limits. Artifact requests have a two-minute client bound, exact size/published checksum and 512-MiB maximum (installer inspection additionally caps at 64 MiB). HTTPS publisher host allowlists reject credentials, custom ports, query strings, unsafe paths, unexpected hosts and redirects. Metadata/errors do not echo raw attacker responses or signed URLs. Maven SHA256 is preferred; SHA1 fallback occurs only when the SHA256 sidecar is genuinely absent (404), not on authorization or transient errors. Cookie jars are removed.

Missing required artifacts/checksums/layouts fail with no successful launch proposal. Official manual download destinations are exposed by ManualDownloadURL: [Fabric server](https://fabricmc.net/use/server/), [Forge downloads](https://files.minecraftforge.net/) and [NeoForge](https://neoforged.net/). A future upload/resume flow belongs to the coordinator/content workflow; this package does not fabricate downloaded files or accept unverified substitutes.

## Automated verification and RED/GREEN evidence

Ordinary tests are deterministic and network-free. HTTP/managed-runtime boundaries are replaced only where needed; real ZIP/JAR files, filesystem staging, hashing, process cancellation and offline argv verification are exercised. Small exact official JSON/Unix/Windows argfile extracts are recorded under loaderutil/testdata with provenance. Real generated temporary JAR fixtures cover malformed and unsafe archives.

Observed failure-first evidence:

- Initial scoped go test: RED, constructors/HTTP/publication/layout APIs absent; implemented to GREEN.
- Bundled Minecraft test: RED, PrepareMinecraft absent; GREEN after nested JAR extraction/hash validation.
- Historical Fabric catalog mainClass string: RED with malformed metadata; GREEN after decoding only catalog identity fields.
- Exact Forge version identity test: RED because substring identity matched; GREEN after exact profile/version ID binding.
- Generated-output publisher checksum test: RED, VerifyGenerated absent; GREEN after server data/processor checksum validation.
- Offline verifier test: RED, verifier and shared Fabric argv reconstruction absent; GREEN after canonical runtime/argv/file checks.
- Numeric loader catalog test: RED with 20.4.9 before 20.4.10; GREEN after numeric ordering and reviewed 26.x candidate discovery.
- Real signed legacy Forge manifest fixture: RED at the 64-KiB cap; GREEN after raising the bounded main-manifest input limit to one MiB and reading launch attributes only from its main section.

Coverage includes all flavor floors/floating versions, malformed/unavailable catalogs, plausible-but-mismatched tuple identity, hash/length errors, checksum downgrade refusal, metadata bounds, redirects/error sanitization, unsafe URLs/paths/JAR entries, bundled content tampering, missing libraries, Linux/Windows official argfile relocation, arbitrary flags/argfiles, missing-instance rejection, runtime mismatch, partial-stage cancellation, real installer process cancellation, injected environment removal, preserved world/config/EULA and absent supervisor launch writes, cache tampering/symlinks/extra executables, offline runtime binding and canonical memory extensions.

Commands and final scoped results:

- `go test ./internal/server/loaderutil ./internal/server/fabric ./internal/server/forge ./internal/server/neoforge` — PASS.
- `go test -race ./internal/server/loaderutil ./internal/server/fabric ./internal/server/forge ./internal/server/neoforge` — PASS on final scoped code.
- `go vet ./internal/server/loaderutil ./internal/server/fabric ./internal/server/forge ./internal/server/neoforge` — PASS.
- `env GOOS=windows GOARCH=amd64 go build ./internal/server/loaderutil ./internal/server/fabric ./internal/server/forge ./internal/server/neoforge` — PASS, cross-compilation only.

The broad suite was intentionally not run while peers owned active shared files, as explicitly required by the integrator. No unrelated package failure is hidden by a claimed full-suite pass.

## Real official installer-only evidence

Opt-in command (ordinary tests skip it):

```sh
env MINECRAFT_UI_LOADER_SMOKE=1 go test -count=1 -run TestOfficialLoaderInstallOnly -v ./internal/server/loaderutil -timeout=30m
```

Optional MINECRAFT_UI_LOADER_TUPLE restricts this to one named tuple. The test uses production Resolve/Install, actual managed Temurin, real publisher downloads and real Forge/NeoForge installer processes. It verifies offline launch proposals, reuses installation through a second Install call and asserts unchanged pre-existing world/config/EULA bytes. No game starts and no EULA is accepted. Disposable files are cleaned afterwards.

| Tuple | Result | Principal pinned artifact |
|---|---|---|
| Fabric 1.17.1 / 0.16.14 (legacy unbundled) | PASS, install/offline verification/reuse/preservation | loader 1,416,842 bytes, SHA256 9ed7b7f4197153ad97d71b4b9cd9148989e8f0c600934b035ce45f3ef003efc0 |
| Fabric 1.20.1 / 0.16.14 (bundled) | PASS, install/offline verification/reuse/preservation | same loader; Mojang server 47,791,053 bytes, SHA1 84194a2f286ef7c14ed7ce0090dba59902951553 |
| Forge 1.20.1 / 47.4.0 | PASS, real installer/offline verification/reuse/preservation and published generated hashes | installer 6,056,587 bytes, SHA256 f3f57465e2cbdc328193f77ba7f0c94cb64405f31ed556753d9275e8bdc06e2c |
| NeoForge 1.20.4 / 20.4.253 | PASS, real installer/offline verification/reuse/preservation | installer 6,540,143 bytes, SHA256 8f1413379067059acc0151444b17536cfad97756d99fc70e45587bf229c986fa |
| Forge 1.12.2 / 14.23.5.2860 | PASS, real installer/offline verification/reuse/preservation after runtime/manifest fixes | installer 4,681,950 bytes, SHA256 ea7c33ba95e3993a98d0e9e38168c0759ec323a18675a71d938e1f3f70e6e8e7 |

Successful Java17 cases used Temurin 17.0.20.1+1 Linux x64, runtime archive SHA256 3808d1d15e3ec6bd5b84057fb5d84c33d8a1536a258146bcea2e603fc726e08e. The earlier full smoke run exited failure because the legacy Forge Java8 prerequisite failed; its four other subtests passed. The runtime owner fixed the exact official 8.0.504+1 archive directory alias and exact-build vendor validation (SHA256 9c70e102f527ac674ac2fe9c7d47b9a04e2d19842ba5ab8e9b33f368bbadfaea), without loader-owned edits to runtime. The first legacy retry then exposed the real Forge executable signed manifest size (198,899 bytes), which exceeded the conservative 64-KiB bound. A signed-manifest fixture was observed RED, the bound was raised to a still-bounded one MiB, and only main-section launch attributes are parsed. Scoped tests returned GREEN. The final exact command `env MINECRAFT_UI_LOADER_SMOKE=1 MINECRAFT_UI_LOADER_TUPLE=forge-legacy go test -count=1 -run TestOfficialLoaderInstallOnly -v ./internal/server/loaderutil -timeout=30m` PASSED in 11.34 seconds, including real Java8 installer execution, offline verification, cache reuse and world/config/EULA preservation. All five table rows have passing evidence; no single post-fix combined network-suite run is claimed.

An additional real NeoForge 1.21.1 / 21.1.172 installer run exited zero but publication correctly failed: its embedded Unix argfile references absent libraries/net/minecraft/server/20240808.144430/server-20240808.144430-extra.jar while its profile declares an MC_EXTRA coordinate with the 1.21.1- prefix. No guessed file, alias or successful launch proposal was produced. This is a failed exact layout check, not a general NeoForge incompatibility claim. Other exact builds require independent resolution/installation evidence.

Early Fabric smoke failures exposed historical catalog decoding (fixed with an observed RED/GREEN regression). Early sandbox DNS failures were retried with tool escalation; they are not publisher unavailability evidence. Official Forge Maven subsequently succeeded using the identifying User-Agent.

## Remaining integration and qualification

The integrator must review this package and update the ledger. Tasks09/18 must wire discovery/Resolve/Install into authorized durable jobs and role-aware UI, serialize the stopped-state/safety-backup/EULA transaction, persist proposals and retain rollback references. Native launch must compose managed-runtime verification with this offline provider verifier. Readiness is Task10. No real game start, client connection, update activation, world recovery or stable support matrix is qualified by installer-only checks. Windows build checks do not substitute for native installer/filesystem/process/game execution. Legacy Forge installer-only qualification also passed after the narrowly reviewed runtime and manifest-bound corrections described above.


## Review fix round 1 — runtime generations and publication durability

BASE 011f99e. Independent review raised two Important findings; both are addressed for integrator re-review, with no shared-file edits, staging or commits by this worker.

1. **Distinct managed runtime generations.** Publication previously keyed the directory solely by PlanID(plan) while requiring exact receipt runtime equality. An unchanged game/loader tuple with a newer verified Temurin build therefore collided with the older receipt. The immutable key is now SHA256 over a typed record containing the canonical plan ID, managed runtime ID and Java archive checksum. Receipt runtime equality is retained. Offline VerifyLaunch checks the same combined key; its public signature and the opaque 64-character path component remain unchanged. The native owner was notified. The focused fixture installs two verified runtime generations of the same Fabric plan, checks that their executable paths differ, independently verifies both through VerifyLaunch after both exist, and confirms reuse of the newer generation.

2. **Durable publication ancestors.** The success fence now syncs the loaders directory, containing .manager directory and canonical instance server directory, in that order. This applies to new publication and verified reuse, including the competing-publisher path. A real filesystem regression moves .manager away while publication holds its rooted handles: the prior implementation incorrectly returned a successful proposal despite the absent canonical ancestor; the corrected fence returns failure. Existing publication tests also exercise the normal newly-created ancestor success path. Linux directory syncing is exercised; native Windows directory durability remains unqualified as previously documented.

Observed RED command:

```sh
go test ./internal/server/loaderutil -run 'TestOfflineVerifierRebuildsArgsAndBindsRuntime|TestPublicationRejectsMissingAncestorAtDurabilityFence' -count=1
```

The runtime-generation regression failed with `publish newer verified runtime: invalid loader publication receipt`; the ancestor regression failed because publication returned success with an unsyncable containing directory. The same command passed after the fixes.

Final scoped verification:

- `go test -race ./internal/server/loaderutil ./internal/server/fabric ./internal/server/forge ./internal/server/neoforge -count=1` — PASS.
- `go vet ./internal/server/loaderutil ./internal/server/fabric ./internal/server/forge ./internal/server/neoforge` — PASS.
- `env GOOS=windows GOARCH=amd64 go build ./internal/server/loaderutil ./internal/server/fabric ./internal/server/forge ./internal/server/neoforge` — PASS; compilation only.

Official network/installer smokes were not repeated: these fixes change local generation identity and fsync fencing, with focused filesystem/offline-verifier evidence covering the reported failures. The earlier exact publisher/installer evidence and its game/client/native Windows limitations remain as recorded above. Two findings addressed, zero known open findings from this round; independent re-review remains with the integrator.
