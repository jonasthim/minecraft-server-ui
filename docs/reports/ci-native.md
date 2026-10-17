# Native CI checks

Date: 2026-10-17. Scope: `.github/workflows/ci.yml`; no release qualification or publication change.

## Workflow

Pushes, pull requests and manual dispatch run two independent x64 jobs: `ubuntu-24.04` and `windows-2022` (Windows Server 2022). Matrix failure does not cancel the other operating system. Each job has a 35-minute limit; the Go race step has a 15-minute limit and each package test binary has a five-minute timeout.

Both jobs install the Go version declared by `go.mod`, currently 1.27.0, and Node 24 through the existing official setup actions with explicit x64 architecture. They log runner OS/architecture, image identity and installed tool versions, and assert native Go host/target OS and amd64 target architecture. Runner images are updated by GitHub; the run's setup and toolchain logs identify the actual image and tools used.

Each job installs frontend dependencies and builds the embedded web bundle once before Go vet, native `go test -race -v -count=1 -timeout=5m ./...`, and native manager/updater builds. Race testing executes the unit tests and avoids a redundant ordinary full-suite run. Tests use cgo for the race detector; manager/updater builds use `CGO_ENABLED=0`, matching the existing build targets, with native OS/architecture rather than cross-compilation. Verbose test output preserves existing test skip reasons in the Actions log.

Linux additionally regenerates API types through the documented npm script and checks the generated file for drift, runs frontend typecheck/lint, provisions all locked Go modules with `go mod download`, verifies `go.mod`/`go.sum` have no changes, and runs `make check-notices`. Provisioning includes dependencies only linked on Windows because notices inventory both targets; the notice checker itself remains offline. No new application dependency is introduced. The workflow has only `contents: read` permission and checkout does not persist credentials. It does not upload artifacts, publish, deploy, supply secrets, accept a Minecraft EULA or enable opt-in official-download/service qualification tests.

## Windows compiler and shell

The Windows image manifest lists GCC and Git for Windows Bash. Windows compiler preflight selects the first installed `gcc` application in PATH order, records its version, requires the `x86_64-w64-mingw32` target, and verifies the compiler can locate an existing `libsynchronization.a`. This follows the Go race detector's documented MinGW-w64 v8+ requirement; an unavailable or unsuitable compiler fails the job. The resolved native compiler path becomes `CC`. [Windows image manifest](https://github.com/actions/runner-images/blob/ee1ff4569e381a9649b400338bdc8ceb7e18a80e/images/windows/Windows2022-Readme.md), [Go race requirements](https://go.dev/doc/articles/race_detector#Requirements).

The compiler preflight uses PowerShell and checks `$LASTEXITCODE` immediately after every native compiler command. Remaining run steps explicitly use Bash on both platforms. On Windows GitHub selects Git for Windows Bash, which executes the installed native `go.exe` and test executables. GitHub's explicit Bash invocation includes `-e -o pipefail`; no result-masking pipeline, `continue-on-error`, package filter or Windows-specific test exclusion is added. [GitHub shell behavior](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idstepsshell).

The official Ubuntu manifest includes Bash, GCC, Python and make for the Linux checks. GitHub documents both selected runner labels as x64. The image manifests are environment assumptions, not evidence that this checkout has passed on those hosts. [Ubuntu image manifest](https://github.com/actions/runner-images/blob/ee1ff4569e381a9649b400338bdc8ceb7e18a80e/images/ubuntu/Ubuntu2404-Readme.md), [GitHub runner labels](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idruns-on).

## Validation and remaining gate

- `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -color .github/workflows/ci.yml`: passed. The validation tool is outside the application's module dependencies.
- Parsed YAML structure, asserted triggers/permissions/matrix/timeouts, and ran `bash -n` against every Bash run script: passed.
- Exercised GitHub's documented Bash invocation with deliberately failing standalone and pipeline commands; both returned 1 and prevented a subsequent success command from running: passed on the local Linux shell.
- `git diff --check -- .github/workflows/ci.yml docs/reports/ci-native.md`: passed.
- No full local Go/frontend suites were repeated for this workflow-only change. Local full-suite/cross-build evidence previously recorded by the integrator is separate from hosted execution.

The [first GitHub Actions run](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39021877786), commit `7993055`, reached Windows compiler preflight and failed before native tests. The host was Windows Server 2022 build 10.0.20348, image `windows-2022` version `20261012.333.1`, with Go `1.27.0 windows/amd64`, Node `v24.21.0` and npm `11.19.0`. [Windows job 117365151785](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39021877786/job/117365151785) reported both `C:\mingw64\bin\gcc.exe` and `C:\Strawberry\c\bin\gcc.exe` from `Get-Command`. Reading `.Source` on that result produced an array; the call operator attempted to execute the combined paths and exited 1. This was a workflow selection defect, not a Go test failure.

Execution fix round 1 makes discovery explicit with `-All`, wraps its result in `@(...)`, and takes `[0].Source`. Microsoft documents that `Get-Command -All` returns commands in execution precedence order and that indexing an array selects one element. The selected path remains subject to the same x64 target, synchronization library and immediate native return-code checks. No assertion or package is excluded. [Microsoft Get-Command documentation](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/get-command#example-12-get-all-instances-of-the-notepad-command), [PowerShell array indexing](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_arrays#reading-an-array).

The corrected workflow passed actionlint, YAML/source checks and whitespace validation again. Local `pwsh` is unavailable, so the PowerShell change was checked against the actual failure and Microsoft's documented collection/indexing semantics; it has not been executed locally. The [second hosted run](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39022162348), commit `98540a2`, subsequently passed Windows compiler preflight and reached native vet, as observed by the integrator while the notice fix below was prepared. Its final native suite outcome was still pending.

## Offline notice provisioning fix

In the first run, [Ubuntu job 117365151662](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39021877786/job/117365151662) on image `ubuntu-24.04` version `20261012.334.1`, Go `1.27.0 linux/amd64`, passed native vet, race tests and both native builds. All nine notice unit tests also passed. The final offline notice generation check failed with `go list failed; provision locked dependencies/toolchain locally`, leaving the job failed. These are partial check results, not an overall CI pass.

The exact inventory command is `go list -mod=readonly -deps -json ./cmd/minecraft-ui ./cmd/minecraft-ui-update`, run for both `linux` and `windows` with cgo disabled. A focused reproduction used a fresh isolated `GOMODCACHE` and the existing local download cache as a file-based module proxy, without builds, tests or remote downloads:

1. Provision the Linux inventory: exit 0.
2. Repeat the Linux inventory with `GOPROXY=off`: exit 0.
3. Run the Windows inventory with `GOPROXY=off`: exit 1. `modernc.org/libc` needed locked modules `github.com/mattn/go-isatty v0.0.24` and `github.com/ncruces/go-strftime v1.0.0`, which the native Linux inventory had not downloaded.
4. Run `go mod download` into that same cache: exit 0.
5. Repeat the offline Windows inventory: exit 0.
6. Run `python3 release/generate_notices.py --check` against the provisioned cache with networking disabled by the checker: exit 0. `go.mod` and `go.sum` byte hashes were unchanged throughout.

Execution fix round 2 adds a Linux provisioning step immediately before `make check-notices` and fails on any lockfile diff. Go's installed `go help mod download` confirms that, with this module's Go version, the command provisions the modules explicitly required in `go.mod`; it does not change dependency selection. The offline checker, inventory union, notice assertions and application code are unchanged. Full Go/frontend suites were not repeated. Actionlint, YAML/source checks, Bash syntax and whitespace validation passed again. A hosted rerun including this provisioning fix remains required for an overall CI pass.

Record each final run's commit, URL, exact runner image/toolchain, job conclusions and failed/skipped tests before claiming native CI success. Any actual Windows failure must be diagnosed and fixed without weakening assertions or excluding failing packages.

A green run establishes execution of the existing native package/race tests and builds on Ubuntu 24.04 and Windows Server 2022. Existing platform-specific build constraints, privilege-dependent skips, and opt-in service/download tests still limit coverage. It does not qualify Windows 10/11 or Server 2019, installed Windows services or real systemd lifecycle, manager/game restart and autostart, real Minecraft/client operation, or real-world backup/restore. Those product acceptance and stable release gates remain in `docs/IMPLEMENTATION.md`; no package status is closed by this report.

## Combined correction run

[Run 39023970811](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39023970811) tested commit `c55b1aadc0c6ca782fbdabc418a03e784feaa419` after independent review of the CI provisioning, Windows implementation/fixtures and final integration corrections.

- [Ubuntu job 117371190298](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39023970811/job/117371190298): **success**. Image `ubuntu24 20261012.334.1`, Go `1.27.0 linux/amd64`. All 38 Go packages with tests passed under the race detector, with zero failed test entries. Native vet and both native executable builds passed. Generated API drift, TypeScript, lint, frontend build, locked-module provisioning and dependency notices (nine tests plus exact offline inventory) passed.
- [Windows job 117371190484](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39023970811/job/117371190484): **failure**. Image `win22 20261012.333.1`, Go `1.27.0 windows/amd64`. Compiler preflight, frontend build and native vet passed. The race suite passed 36 packages and failed only `internal/content` and `internal/update`, with 14 failed entries including nested subtests. Native executable builds were not reached. All remaining failures trace to content cleanup changing attributes through a read-only file handle. Database, launcher, supervisor, updater, map/player/world and loader/provider filesystem corrections passed their actual native package tests.

The Linux log records nine existing opt-in/environment skips: backup browser harness, official catalog/pack/Temurin/artifact/loader/historical-NeoForge smokes, isolated insufficient-space setup, and installed systemd lifecycle. Windows records eleven skips: the seven applicable opt-in browser/official-download checks, Linux directory permission enforcement, Unix filename behavior, Unix ancestor-rename durability fixture, and the existing runtime symlink-root test's Windows privilege guard. No new skip or failing-package exclusion was introduced. Tests excluded by platform build constraints are not counted as executed.

The remaining native content correction is recorded in `docs/reports/final-review-fixes.md`. This run is an overall failure despite the Linux success and narrower Windows failure set; a subsequent combined hosted run is required.
