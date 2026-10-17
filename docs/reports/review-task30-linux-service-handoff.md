# Task 30 Linux service and coordinator handoff review

## Scope

Reviewed `internal/install/service_linux.go`, `internal/install/service_linux_test.go`, the coordinator extraction in `internal/install/coordinator.go`, and `internal/install/coordinator_handoff_test.go` against `.superpowers/sdd/2026-10-17-valheim-parity-completion/task30-linux-service-brief.md` and `docs/reports/task30-linux-service.md`.

## Findings

### 1. Stop and start systemctl calls can time out before their service jobs finish — high

`internal/install/service_linux.go:27` gives every systemctl command a 30-second deadline. The fixed manager unit allows 150 seconds for shutdown (`deploy/linux/minecraft-ui.service:16`), and the existing Linux updater uses a 155-second stop deadline (`internal/selfupdate/native_linux.go:119-128`). A stop that is still draining save leases or other manager work can therefore be reported as failed while systemd continues the stop job. The coordinator may then begin rollback without proof that the manager has stopped.

The same global limit applies to `start`. The manager start is ordered after the recovery service, whose configured start timeout is four minutes (`deploy/linux/minecraft-ui-update-recovery.service:15`). The fixed `systemctl start` can fail at 30 seconds while that required recovery is still running.

Use operation-specific bounds for stop and start, capped by the caller’s overall operation deadline. The existing service and updater budgets support at least 155 seconds for stop and up to the recovery dependency’s four-minute budget for start.

### 2. The second game-unit enumeration ignores active states — high

`requireLinuxGamesStopped` in `internal/install/service_linux.go:297-320` checks each exact unit state after the first enumeration, then calls `enumerateLinuxGameUnits` again but compares only the returned names. The parser validates the listed state tokens but discards them. If a game becomes active, activating, deactivating, or reloading after its exact `show` and before this second listing, the second listing visibly reports that state and the function still returns success. During uninstall, that can authorize removal while a managed game is running.

Retain and validate the state fields from the final enumeration, or query every discovered unit again and require the exact stopped proof before returning success. Keep the set-change comparison.

## Coordinator handoff

No finding in the requested coordinator extraction. `continuePreparedWith` validates the supplied ID, loads the journal once, accepts only that matching transaction in `prepared`, and enters the existing execution path without calling `Prepare` or recording `prepared` again. Recovery remains rollback-only for nonterminal transactions. The new handoff tests exercise both operation modes, refusal of wrong or advanced transactions, and rollback after a worker failure.

## Verification

- Isolated service and coordinator source-list `go test` — passed.
- Isolated service and coordinator source-list `go test -race` — passed.
- Isolated service and coordinator source-list `go vet` — passed.
- Whole `go test ./internal/install` and `go test -race ./internal/install` — fail in the concurrently present `model_linux_test.go` fixtures (`asset plan is invalid`; retained inventory validation). These failures are outside the reviewed file scope.
- Whole `go vet ./internal/install` — passed.
- No host `systemctl`, account, firewall, or game operations were performed. Native Ubuntu acceptance remains pending.

## Verdict

Request changes for the two service findings before treating this adapter as ready for integration. The coordinator handoff source review passes.

## Round 2 service re-review

This follow-up supersedes the service portion of the verdict above.

Both prior service findings are fixed in the reviewed source:

- Stop and start select exact 155-second and six-minute budgets by matching their fixed argv. Other systemctl calls remain at 30 seconds, and `context.WithTimeout` caps each child deadline at the caller deadline.
- The final game-unit enumeration now retains and rejects active or transitional states, compares unit sets, and repeats exact unit-property proofs for every listed unit.

No new breakage found in this scoped re-review. The updated Task 30 service report records its full package test, race, and vet evidence. Independently, the two regression tests covering the fixes passed normally and under `-race`; `go vet ./internal/install` passed. The prior coordinator handoff approval is unchanged.
