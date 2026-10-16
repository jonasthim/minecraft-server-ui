# Task 06 — Java 8 archive compatibility repair

Submitted for independent integrator review against base `0216110`. The task brief named the earlier base `1d550b3`; root confirmed no intervening runtime edits. This report does not mark Task 06 accepted or qualify Minecraft combinations/native Windows execution.

## Official evidence and reproduced failures

Reconfirmed current [official Adoptium Linux x64 JDK 8 metadata](https://api.adoptium.net/v3/assets/latest/8/hotspot?architecture=x64&heap_size=normal&image_type=jdk&os=linux&vendor=eclipse). Downloaded the exact [official archive](https://github.com/adoptium/temurin8-binaries/releases/download/jdk8u504-b01/OpenJDK8U-jdk_x64_linux_hotspot_8u504b01.tar.gz) into `/home/thim/.cache/dilla-tmp/temurin8-java8-repair.tar.gz` and independently verified its full byte count and SHA-256 before inspection/execution:

| Field | Verified evidence |
|---|---|
| Release | `jdk8u504-b01` / `8.0.504+1` |
| Exact runtime | `1.8.0_504-b01` |
| Archive | `OpenJDK8U-jdk_x64_linux_hotspot_8u504b01.tar.gz` |
| Bytes | `103542511` |
| SHA-256 | `9c70e102f527ac674ac2fe9c7d47b9a04e2d19842ba5ab8e9b33f368bbadfaea` |

The failing tar entry is a symbolic directory alias:

```text
jdk8u504-b01/man/ja -> ja_JP.UTF-8
resolved target: jdk8u504-b01/man/ja_JP.UTF-8
```

The source is an explicit directory containing one subdirectory and 40 regular files (41 copied descendants, 996044 additional file bytes). The archive's only other link, `jdk8u504-b01/jre/lib/amd64/server/libjsig.so -> ../libjsig.so`, targets a regular file and already matched the existing copy rule.

The faithful `TestJava8ManDirectoryAlias` fixture was observed RED before implementation with `archive link target is not a regular file`.

After repairing that alias, the first real official smoke passed archive verification/extraction and failed JVM vendor validation. The [official release JSON](https://github.com/adoptium/temurin8-binaries/releases/download/jdk8u504-b01/OpenJDK8U-jdk_x64_linux_hotspot_8u504b01.tar.gz.json) names vendor `Eclipse Adoptium`, while independent execution of the hash-verified archive produced:

```text
java.runtime.version = 1.8.0_504-b01
java.vendor = Temurin
java.version = 1.8.0_504
os.arch = amd64
```

That diagnostic used the production safe extractor and scrubbed Java environment; its temporary opt-in test was removed after inspection. A permanent focused JVM regression was then observed RED at the official tuple, with `Java executable vendor does not match Temurin build`.

## Approved bounded repairs

Root explicitly approved both compatibility exceptions before implementation:

1. Materialize only `<single validated JDK root>/man/ja -> ja_JP.UTF-8`, with an explicit directory source in that same root. Arbitrary directory aliases and hard-directory links remain rejected. Copy ordinary files/directories; never create filesystem links. Reject any archived link within the source subtree, filesystem links/specials, target/descendant collisions, unrecognized layouts and multiple top roots. Copied descendants pass through the existing portable path, case/duplicate, entry count, Windows path, file size, aggregate byte and cancellation guards. The alias root itself was already counted as an archive entry. Sibling-only copy prevents recursive alias expansion.
2. Accept JVM vendor `Temurin` only for Java major 8 and exact runtime build `1.8.0_504-b01`. This JVM-only rule does not change release-file `IMPLEMENTOR` validation. Official Adoptium metadata/origin/hash verification, x64 validation, exact metadata/probe build matching, immutable publication and complete installed-file integrity verification remain enforced. Public runtime vendor stays normalized to `Eclipse Adoptium`.

Compatibility cost: future official directory alias layouts require separate review. A future Java 8 build reporting this vendor string is safely refused until independently verified and reviewed; this is not a generic vendor relaxation.

## Regression coverage and final verification

Changed files only:

- `internal/runtime/archive.go`: reviewed alias copy.
- `internal/runtime/java.go`: exact-build JVM vendor exception.
- `internal/runtime/archive_java8_test.go`: faithful alias fixture; escape, unrecognized alias, hard-directory link, source link, descendant/case collision, cycle, implicit source and multiple-root rejection; copied entry/byte budgets and cancellation.
- `internal/runtime/java8_test.go`: real JVM property parser/probe acceptance for the observed tuple; nearby build, other major and wrong vendor rejection; release implementor remains strict; metadata/probe build mismatch rejected.
- This report.

Commands/results:

- `go test ./internal/runtime -run '^TestJava8ManDirectoryAlias$' -v -count=1` — expected RED before alias repair.
- `go test ./internal/runtime -run 'TestJava8Man|TestArchive' -v -count=1` — GREEN after alias repair, including prior archive safety tests.
- `go test ./internal/runtime -run '^TestJava8TemurinJVMVendor$' -v -count=1` — expected RED before JVM vendor repair.
- `go test -race ./internal/runtime -count=1` — final PASS (1.170 s), including both permanent regressions and copy-budget/cancellation checks. Ordinary tests remain network-free.
- `go vet ./internal/runtime` — final PASS.
- `env GOOS=windows GOARCH=amd64 go test -c ./internal/runtime -o /home/thim/.cache/dilla-tmp/minecraft-runtime-java8-windows.exe` — final PASS; compilation only.
- `env MINECRAFT_UI_RUNTIME_SMOKE=1 MINECRAFT_UI_RUNTIME_SMOKE_MAJOR=8 go test ./internal/runtime -run '^TestOfficialTemurinSmoke$' -v -count=1 -timeout=20m` — final PASS (6.41 s).

Final real smoke downloaded/verified/executed the official bytes listed above and returned `temurin-8-linux-x64-9c70e102f527ac674ac2fe9c7d47b9a04e2d19842ba5ab8e9b33f368bbadfaea`. It confirmed JVM vendor `Temurin`, runtime `1.8.0_504-b01`, architecture `amd64`, and identical ID/executable from a fresh-manager `Get` and `Ensure` whose transport rejected any network request. The temporary runtime installation was cleaned up by the smoke.

Loader owner `/root/loaders` was notified directly after this verification so the Forge 1.12.2 installer-only check could resume. No Minecraft server launch or EULA acceptance was performed. No shared contracts, wiring, dependencies, staging or commits were changed. Native Windows filesystem/process qualification and Debian/Ubuntu runtime qualification remain pending acceptance work; cross-compilation does not replace them.
