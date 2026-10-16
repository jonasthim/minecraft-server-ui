# Task 06 — review fix round 1

Submitted for integrator review. This fix does not mark Task 06 accepted or qualify Minecraft/loader combinations or native Windows execution.

## Finding and change

The strict `Eclipse Adoptium` checks rejected the official final Temurin 16 release, `16.0.2+7`, whose vendor is `Eclipse Foundation`. Runtime selection already chooses Java 16 for Vanilla/Forge 1.17 and Paper 1.16.5, so their managed-runtime installation was blocked.

`internal/runtime/java.go` now accepts the historical vendor only when the major is exactly 16 and the runtime build is exactly `16.0.2+7`. Release validation reads the historical `FULL_VERSION` field when `JAVA_RUNTIME_VERSION` is absent and the implementor is `Eclipse Foundation`. No fallback is applied to other vendors. The parsed JVM keeps its actual vendor; the public/manifest runtime vendor remains normalized to `Eclipse Adoptium`. Exact probe-to-metadata build/major matching, x64 checks, official metadata/vendor/repository checks, archive byte count/hash verification, staged activation, and installed file verification remain in place.

Official evidence inspected on 2026-10-16:

- [Official Temurin 16 Linux archive metadata](https://github.com/adoptium/temurin16-binaries/releases/download/jdk-16.0.2%2B7/OpenJDK16U-jdk_x64_linux_hotspot_16.0.2_7.tar.gz.json): vendor `Eclipse Foundation`, version `16.0.2+7`, SHA-256 below, configure vendor name `Eclipse Foundation`.
- The actual archive release file contains `IMPLEMENTOR="Eclipse Foundation"`, `IMPLEMENTOR_VERSION="Temurin-16.0.2+7"`, `JAVA_VERSION="16.0.2"`, `FULL_VERSION="16.0.2+7"`, and `OS_ARCH="x86_64"`. It does not contain `JAVA_RUNTIME_VERSION`.

## Regression evidence

Added `internal/runtime/java16_test.go`:

- Each real validation gate (JVM property parser, release-file validation, probe comparison) accepts the historical tuple and rejects Oracle, a different Java 16 build, a different major, mismatched major/build, and aarch64.
- Missing/wrong release build and a probe build different from verified metadata remain rejected.
- Deterministic install exercises official-shaped metadata, a hash-verified fixture archive with the historical release layout, canonical runtime vendor, and fresh-manager `Get`/offline `Ensure` reuse. Only the external HTTP/probe boundaries are doubled; extraction, hashing, release checks, publication and cache verification are real.

Observed RED before the vendor fix: `go test ./internal/runtime -run '^TestHistoricalJava16' -v -count=1` failed at all three `official16` validation subtests and installation (`Temurin release vendor or architecture mismatch`). Wrong vendor/build/major/architecture cases passed by rejecting the input.

The first real smoke after the initial vendor-only change verified the archive bytes but failed release validation. Inspection showed the initial fixture incorrectly assumed a `JAVA_RUNTIME_VERSION` field. The fixture was corrected to the actual `FULL_VERSION` layout and observed RED again before adding its bounded fallback. The final package race run is GREEN, including those regressions.

## Final verification

Host: `Linux 7.2.9-2-cachyos x86_64 GNU/Linux`; `go version go1.27.0 linux/amd64`.

- `go test -race ./internal/runtime` — PASS after the final production change (1.092 s).
- `go vet ./internal/runtime` — PASS after the final production change.
- `env GOOS=windows GOARCH=amd64 go test -c ./internal/runtime -o /home/thim/.cache/dilla-tmp/minecraft-runtime-fix1-windows.exe` — PASS after the final production change. Compilation only.
- `git diff --check -- internal/runtime` — PASS.
- `env MINECRAFT_UI_RUNTIME_SMOKE=1 MINECRAFT_UI_RUNTIME_SMOKE_MAJOR=16 go test ./internal/runtime -run '^TestOfficialTemurinSmoke$' -v -count=1 -timeout=20m` — PASS (11.56 s).

Real smoke verified and executed:

| Field | Evidence |
|---|---|
| Archive | `OpenJDK16U-jdk_x64_linux_hotspot_16.0.2_7.tar.gz` |
| Official repository | `adoptium/temurin16-binaries`, release `jdk-16.0.2+7` |
| Downloaded and verified bytes | `205463525` |
| SHA-256 | `323d6d7474a359a28eff7ddd0df8e65bd61554a8ed12ef42fd9365349e573c2c` |
| Runtime ID | `temurin-16-linux-x64-323d6d7474a359a28eff7ddd0df8e65bd61554a8ed12ef42fd9365349e573c2c` |
| Executed JVM | vendor `Eclipse Foundation`, runtime `16.0.2+7`, architecture `amd64` |
| Fresh-manager reuse | `Get` and `Ensure` returned identical ID/executable; transport rejected any network request |

The opt-in smoke still defaults to Java 17; `MINECRAFT_UI_RUNTIME_SMOKE_MAJOR` explicitly selects a supported major only after `MINECRAFT_UI_RUNTIME_SMOKE=1`. Ordinary package tests remain network-free and skip the smoke. The smoke uses a temporary installation and cleans it up.

Only `internal/runtime/**` and this report were changed. No dependency, shared contract/wiring, staging, or commit changes were made. Verification was scoped to the assigned runtime package. Native Debian/Ubuntu and Windows qualification and actual game launches remain integrator acceptance work.
