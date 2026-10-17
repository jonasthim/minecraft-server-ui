# Task30 Linux systemd service adapter

## Scope

Added the private Linux service primitive in `internal/install/service_linux.go` with deterministic coverage in `service_linux_test.go`. The adapter uses fixed `/usr/bin/systemctl` invocations, a fixed sanitized environment, bounded output and per-command deadlines. Test hooks are immutable values carried in a context, so no global hook state or host service access is used by package tests.

Manager inspection strictly parses the exact requested properties and rejects duplicate, missing, unknown, malformed, transitional, and incoherent states. Stop proves the manager stopped; start targets only the fixed manager unit; enablement is verified and refuses a missing or unsupported unit. Manager effects snapshot managed game-unit states and verify they remain unchanged. Uninstall proof enumerates game units twice, validates each instance name, queries each unit’s exact state, and refuses active or uncertain games.

The manager image proof validates the expected image, checks the fixed executable with `lstat` for root ownership, one link, regular type, mode `0755`, and a 256 MiB limit, then hashes it and compares `/proc/<pid>/exe` identity. Process start ticks and service PID/state are rechecked around the proof. Errors surfaced by the adapter do not include raw command output or process paths.

Review fixes retain load/active/substate values from the second game-unit listing, reject any visible active or transitional state, and repeat exact unit-property proofs before returning success. Stop receives a fixed 155-second command budget, start receives six minutes for the four-minute recovery dependency plus startup, and other systemctl calls retain 30 seconds. Every child deadline remains capped by the caller context.

## Evidence

- `PATH=/home/thim/.local/go/bin:$PATH go test ./internal/install` — passed.
- `PATH=/home/thim/.local/go/bin:$PATH go test -race ./internal/install` — passed.
- `PATH=/home/thim/.local/go/bin:$PATH go vet ./internal/install` — passed.
- `git diff --check -- internal/install/service_linux.go internal/install/service_linux_test.go` — passed.
- Tests exercise strict property parsing, state proofs, command bounds/cancellation/argv and deadline selection/capping, unsafe game-unit names, active games, changing enumerations, activity appearing in the second listing, manager executable mismatch/replacement, and process/PID changes.

## Limitations

This is a bounded adapter, not the native installer or its acceptance run. No host `systemctl`, account, firewall, or game operations were performed. Native Ubuntu 22.04/24.04 systemd acceptance and full installer integration remain pending.
