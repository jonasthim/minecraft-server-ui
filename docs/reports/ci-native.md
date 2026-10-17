# Native CI checks

Date: 2026-10-17. Scope: `.github/workflows/ci.yml`; no release qualification or publication change.

## Workflow

Pushes, pull requests and manual dispatch run two independent x64 jobs: `ubuntu-24.04` and `windows-2022` (Windows Server 2022). Matrix failure does not cancel the other operating system. Each job has a 35-minute limit; the Go race step has a 15-minute limit and each package test binary has a five-minute timeout.

Both jobs install the Go version declared by `go.mod`, currently 1.27.0, and Node 24 through the existing official setup actions with explicit x64 architecture. They log runner OS/architecture, image identity and installed tool versions, and assert native Go host/target OS and amd64 target architecture. Runner images are updated by GitHub; the run's setup and toolchain logs identify the actual image and tools used.

Each job installs frontend dependencies and builds the embedded web bundle once before Go vet, native `go test -race -v -count=1 -timeout=5m ./...`, and native manager/updater builds. Race testing executes the unit tests and avoids a redundant ordinary full-suite run. Tests use cgo for the race detector; manager/updater builds use `CGO_ENABLED=0`, matching the existing build targets, with native OS/architecture rather than cross-compilation. Verbose test output preserves existing test skip reasons in the Actions log.

Linux additionally regenerates API types through the documented npm script and checks the generated file for drift, runs frontend typecheck/lint, and runs `make check-notices`. No new application dependency is introduced. The workflow has only `contents: read` permission and checkout does not persist credentials. It does not upload artifacts, publish, deploy, supply secrets, accept a Minecraft EULA or enable opt-in official-download/service qualification tests.

## Windows compiler and shell

The Windows image manifest lists GCC and Git for Windows Bash. Windows compiler preflight resolves installed `gcc`, records its version, requires the `x86_64-w64-mingw32` target, and verifies the compiler can locate an existing `libsynchronization.a`. This follows the Go race detector's documented MinGW-w64 v8+ requirement; an unavailable or unsuitable compiler fails the job. The resolved native compiler path becomes `CC`. [Windows image manifest](https://github.com/actions/runner-images/blob/ee1ff4569e381a9649b400338bdc8ceb7e18a80e/images/windows/Windows2022-Readme.md), [Go race requirements](https://go.dev/doc/articles/race_detector#Requirements).

The compiler preflight uses PowerShell and checks `$LASTEXITCODE` immediately after every native compiler command. Remaining run steps explicitly use Bash on both platforms. On Windows GitHub selects Git for Windows Bash, which executes the installed native `go.exe` and test executables. GitHub's explicit Bash invocation includes `-e -o pipefail`; no result-masking pipeline, `continue-on-error`, package filter or Windows-specific test exclusion is added. [GitHub shell behavior](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idstepsshell).

The official Ubuntu manifest includes Bash, GCC, Python and make for the Linux checks. GitHub documents both selected runner labels as x64. The image manifests are environment assumptions, not evidence that this checkout has passed on those hosts. [Ubuntu image manifest](https://github.com/actions/runner-images/blob/ee1ff4569e381a9649b400338bdc8ceb7e18a80e/images/ubuntu/Ubuntu2404-Readme.md), [GitHub runner labels](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idruns-on).

## Validation and remaining gate

- `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -color .github/workflows/ci.yml`: passed. The validation tool is outside the application's module dependencies.
- Parsed YAML structure, asserted triggers/permissions/matrix/timeouts, and ran `bash -n` against every Bash run script: passed.
- Exercised GitHub's documented Bash invocation with deliberately failing standalone and pipeline commands; both returned 1 and prevented a subsequent success command from running: passed on the local Linux shell.
- `git diff --check -- .github/workflows/ci.yml docs/reports/ci-native.md`: passed.
- No full local Go/frontend suites were repeated for this workflow-only change. Local full-suite/cross-build evidence previously recorded by the integrator is separate from hosted execution.

The first GitHub Actions run remains required. Record its commit, run URL, exact runner image/toolchain, job conclusions and failed/skipped tests before claiming native CI success. Local YAML/shell lint does not execute PowerShell or Windows binaries. Any actual Windows failure must be diagnosed and fixed without weakening assertions or excluding failing packages.

A green run establishes execution of the existing native package/race tests and builds on Ubuntu 24.04 and Windows Server 2022. Existing platform-specific build constraints, privilege-dependent skips, and opt-in service/download tests still limit coverage. It does not qualify Windows 10/11 or Server 2019, installed Windows services or real systemd lifecycle, manager/game restart and autostart, real Minecraft/client operation, or real-world backup/restore. Those product acceptance and stable release gates remain in `docs/IMPLEMENTATION.md`; no package status is closed by this report.
