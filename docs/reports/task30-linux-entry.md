# Task30 Linux installer entry and worker dispatch

## Scope

Added the `install` command parser and platform entry points in `internal/install`. The Linux command path uses the existing typed installer driver, validates arguments before native access, checks local administrator authority, prepares and records the transaction before dispatch, releases the updater session before starting the independent worker, and reports bounded JSON results. The worker validates its exact transaction and executable identity before continuing a prepared transaction or performing rollback-only recovery.

Recovery dispatch selects `recover-worker` in the fixed systemd-run argv, and the command router test exercises the `install recover` controller route. A valid published terminal worker result remains terminal when systemd dispatch reports an error; the result is marked at the worker stage and returned alongside the error. Check reports allow the retained state only when it is incoherent and not pending.

`install check` calls the existing read-only `checkLinuxInstallation` implementation. The startup API delegates to `CheckLinuxInstallerStartup`. Non-Linux builds expose the same API and return a fixed unsupported error for mutating commands.

## Verification

After the shared native foundation changes compiled, the following checks passed:

- `/home/thim/.local/go/bin/go test ./internal/install`
- `/home/thim/.local/go/bin/go test -race ./internal/install`
- `/home/thim/.local/go/bin/go vet ./internal/install`
- `GOOS=windows GOARCH=amd64 /home/thim/.local/go/bin/go test -c -o /tmp/task30-install.test.exe ./internal/install`
- `gofmt` and `git diff --check` for the entry files

Fixtures cover strict argument rejection, unchanged apply without dispatch, durable prepared ordering before release/dispatch, uncertain record refusal, cancelled and ambiguous dispatch returning pending, fixed systemd-run argv/environment, transaction identity/phase guards, and protected bounded result reads. The temporary Windows test binary is cross-compilation evidence only.

Follow-up regressions cover recovery controller routing and argv selection, preserving a published terminal result on dispatch error, and retained check-report validation. The latest package test attempt is blocked before test execution by a syntax error in the concurrently edited native-owned `internal/install/model_linux.go` near `validateLinuxUninstallIntent`; the entry files were formatted and pass `git diff --check`.

## Acceptance limits

This packet does not establish native Ubuntu installation or upgrade acceptance. No host systemd unit, account, firewall, manager, or game process was changed. `systemd-run` was not executed locally. Native Ubuntu runner qualification and the integrator's CLI/startup wiring remain required.
