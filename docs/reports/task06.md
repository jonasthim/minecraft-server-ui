# Task 06 — managed Temurin runtime package

Status: implementation submitted for integrator review. Task 06 product acceptance remains pending integration and real Linux/Windows qualification.

## Exported integration contract

- `runtime.New(dataDir string, client *http.Client) *Manager`
- `(*Manager).Ensure(context.Context, major int, io.Writer) (domain.JavaRuntime, error)`
- `(*Manager).EnsureRequirements(context.Context, domain.LaunchRequirements, io.Writer) (domain.JavaRuntime, error)`
- `(*Manager).Get(context.Context, runtimeID string) (domain.JavaRuntime, error)`
- `(*Manager).List(context.Context) ([]domain.JavaRuntime, error)`
- `runtime.Select(domain.VersionSelection) (domain.LaunchRequirements, error)`

`Ensure` reuses an existing valid runtime for a Java major without fetching a newer release. Persist the returned exact ID on the instance. `Get` revalidates every installed file against its recorded SHA256/size/executable status. `Ensure` also checks the JVM vendor, exact runtime build, Java major and x64 architecture. The returned `Artifact.Checksum` is the published archive SHA256 after independent verification. Private `.runtime.json` records verified digest/time and extracted-file hashes; no domain contract changes were made.

## Implemented behavior

- Official Adoptium v3 latest-assets metadata for Linux/Windows x64 HotSpot normal-heap JDKs, Java 8/11/16/17/21/25. Reject mismatched vendor, major, architecture, OS, image type, JVM, package filename, size, digest or repository before downloading.
- HTTPS-only requests; initial artifacts restricted to the matching `github.com/adoptium/temurin{major}-binaries/releases/download/...` repository. Every redirect is checked; GitHub's release asset hosts are allowed only as redirects. Caller cookie jars are removed and errors do not expose signed redirect URLs.
- Download into a unique private stage, enforce advertised byte length and SHA256, extract and validate the layout/release metadata, run Java with a bounded direct argv invocation and scrub Java/native library injection environment, then atomically rename the complete directory into the immutable ID containing the archive digest.
- No global Java environment, registry, package manager, or arbitrary downloaded script changes. All artifacts live below the configured `runtimes` directory.
- Bounded zip/tar.gz extraction; reject traversal, absolute/drive/UNC/ADS paths, Windows device names, trailing dot/space, invalid characters, duplicate and case-colliding paths, files used as directories, special files, oversized entries, excess entry counts, and unsafe links. Bound gzip decompression including parser-consumed headers/padding and validate the gzip trailer. Internal Linux license symlinks/hardlinks are validated and copied into regular files; filesystem links are never created. Zip symlinks/special files are rejected.
- Context cancellation covers requests, copy/extract/hash operations, lock waits, and Java inspection. Failure/interruption removes its stage and preserves published installations. Hard-crash orphan stages are ignored by cache discovery. Retrying starts from byte zero.
- Managers in one process share an interruptible installation lock. Separate processes may download concurrently, but publication never replaces an existing complete directory; the losing publisher returns only an independently validated exact winner. No mutable "latest" pointer is used.
- Cache reads reject runtime-root/directory/manifest symlinks, malformed identifiers, executable paths outside the exact expected directory, malformed records, modified/missing/extra regular files, and links/special files in an installed tree.
- Runtimes are retained. Reference-safe deletion is intentionally deferred until the integrator provides the complete instance/process reference graph.

## Compatibility provenance

`Select` is a conservative runtime policy, not qualification of a game/loader build. Providers must independently resolve an existing exact build, apply stricter installer requirements and reject unavailable combinations. Rules reject snapshots, versions before 1.12.2, Fabric before 1.14, NeoForge before 1.20.2, unreviewed release families and unknown flavors. Currently reviewed game families are 1.12–1.21 and 26.1–26.3.

| Distribution | Game versions | Selected Java |
|---|---|---|
| Vanilla/Forge | 1.12.2–1.16.x / 1.17.x / 1.18–1.20.4 / 1.20.5–1.21.x / 26.1–26.3 | 8 / 16 / 17 / 21 / 25 |
| Fabric | 1.14–1.16.x / 1.17–1.20.4 / 1.20.5–1.21.x / 26.1–26.3 | 8 / 17 / 21 / 25 |
| NeoForge | 1.20.2–1.20.4 / 1.20.5–1.21.x / 26.1–26.3 | 17 / 21 / 25 |
| Paper | 1.12.2–1.16.4 / 1.16.5 / 1.17–1.19.x / 1.20–1.21.x / 26.1–26.3 | 11 / 16 / 17 / 21 / 25 |

Sources consulted 2026-10-16: [Adoptium API cookbook](https://github.com/adoptium/api.adoptium.net/blob/main/docs/cookbook.adoc), [archive installation/hash verification](https://adoptium.net/installation/archives), [Paper runtime table](https://docs.papermc.io/paper/getting-started/), [Forge historical Java table](https://docs.minecraftforge.net/en/fg-5.x/gettingstarted/), [Forge 1.16 Java 8](https://docs.minecraftforge.net/en/1.16.x/gettingstarted/), [Fabric server requirements](https://wiki.fabricmc.net/install), [NeoForge 1.20.3–1.20.4](https://docs.neoforged.net/docs/1.20.4/gettingstarted/), [Minecraft 1.20.5 Java 21 change](https://www.minecraft.net/en-us/article/minecraft-java-edition-1-20-5), [Minecraft 26.1 Java 25 change](https://www.minecraft.net/en-us/article/minecraft-java-edition-26-1). Historical requirements are conservative policy inputs; exact future patch availability comes from provider metadata.

## Verification

- `go test ./internal/runtime` — passed; deterministic HTTP transport and JVM probe boundary doubles, real filesystem/zip/tar/gzip/hash logic. Includes bad hashes, corrupted archives, traversal and Windows path variants, duplicate/case collisions, escaping/internal links, special/oversized entries, data-bearing directory headers, zip layout, cancellation during transfer and lock wait, unchanged existing runtime after cancellation, shared-manager concurrency, cache tampering/root symlinks, publisher redirects/metadata, Java vendor/architecture/version parsing and environment filtering, compatibility boundaries.
- `go test -race ./internal/runtime` — passed after final package changes.
- `go vet ./internal/runtime` — passed.
- `GOOS=windows GOARCH=amd64 go test -c ./internal/runtime -o /home/thim/.cache/dilla-tmp/minecraft-runtime-test-windows.exe` — passed. Compilation only; no native Windows execution claim.
- `go test ./...` — initially passed before Task 03 files appeared. Final rerun passed runtime and existing foundation/auth packages but failed ongoing Task 03 packages: `internal/events/hub_test.go` references not-yet-created `NewHub`/`Event`; `internal/jobs/service_test.go` references not-yet-created `Service`/`New`/`Job`/`Request`/`Execution`. Those files belong to the active jobs/events worker and were not modified here.

Test-first sequence observed missing implementation failures, then green tests. Additional root-symlink and data-bearing-directory regression tests were each observed failing before their fixes.

## Opt-in real official smoke

Ordinary tests never access the network. The integrator can explicitly download and execute a real official Temurin 17, then validate reuse with a fresh manager:

```sh
MINECRAFT_UI_RUNTIME_SMOKE=1 go test ./internal/runtime -run '^TestOfficialTemurinSmoke$' -v -count=1 -timeout=20m
```

This uses a temporary directory, verifies official bytes before execution, prints the exact version/ID/SHA256, checks reuse, and cleans up afterwards. It has not been run by this worker. A passing smoke verifies only that exact runtime on that host. Real installations/launches across the Java/game/loader matrix, native Windows filesystem/process behavior, instance pinning, job progress/API/UI and reference integration remain required acceptance evidence. No new dependency was added; no shared wiring, staging or commits were performed.
