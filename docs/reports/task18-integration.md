# Task 18: integrator composition evidence

Date: 2026-10-17. This report covers root-owned composition. Normal update,
participant, rollback and UI evidence is recorded by their respective owners.
Implementation review and real platform qualification are separate gates.

## Composition

- `cmd/minecraft-ui/main.go` constructs the update service with existing jobs,
  instances, lifecycle, backups, content and pack services, attaches the explicit
  rollback coordinator before registration, and supplies the scheduler's
  automatic-update producer. Late-bound scheduler callbacks fail if unavailable.
- Ordinary stopped mutations retain strict backup/content/pack/update readiness.
  Original job mutations first pass ordinary recovery checks, then the update
  service's exact original-execution admission. An ordinary edit cannot borrow
  a rollback or prepared-update continuation.
- Startup requires update recovery after backup leases/restores and ordinary
  content/pack recovery, before instance materialization, lifecycle verification,
  exports, schedules, Windows autostart and queue resume. Missing or failed repair
  prevents subsequent startup work. A verified administrative hold is resolved
  through the update service's read-only `StartupBlocked` callback.
- The same hold callback is supplied to instance materialization and lifecycle
  autostart/readiness. Held postboot files and generations are preserved; a
  safely reconciled hold keeps administrator recovery available. Genuine
  ownership corruption remains an error. Production `VerifyAndSeal` remains
  unset and therefore uses the full production verifier.
- Feature routes are mounted in the existing authenticated and CSRF-protected
  group. Handler/service role checks remain mandatory. The central API schema
  includes the owned update fragment; generated TypeScript is produced with
  `make gen`, never edited manually.
- The shared app route and instance navigation expose Updates and rollback.
  The feature UI belongs to Task25's integration continuation. Update/rollback
  confirmation and required-file acquisition checks are reported there.

## Focused checks

`go test ./cmd/minecraft-ui -run 'TestStartup|TestUpdateJobGate|TestCombinedReadiness' -count=1 -timeout=90s`
passed in 0.155 seconds on the composed startup hold hooks.

The startup table verifies lease/restore/content/pack/update/instance ordering.
Missing and failing content, pack and update recovery prevent instance writes,
exports, autostart and job dispatch. Original-execution guard checks use the real
SQLite queue to distinguish ordinary pending repair, allowed original update
continuation, denied continuation and strict fallback. Ordinary mutation
admission remains blocked even when a job-specific exception is available.

The normal update owner's
`TestBootRiskStartupKeepsRunningBytesAndAllowsExplicitRollbackJob` composes the
actual instance/lifecycle/update/rollback services and replacement queue. It
covers an uncertain surviving JVM, unchanged postboot configuration and NEW
generation, deferred materialization/readiness/autostart, available confirmed
rollback and queue resume. Native outcomes and sealing are explicit fixtures;
this report does not treat them as native execution evidence.

Central type generation passes after pack-member and standalone-content manual
receipt contracts were merged. Production frontend and Linux manager builds
pass for the current pack-controls checkpoint. The browser worker records exact
binary hashes and distinguishes shipping UI evidence from intercepted scenarios
in `docs/reports/task25.md`.

## Combined frozen verification

After the backend and final UI code froze, `make check` passed: repository-wide
`go vet ./...`, `go test -race ./...`, TypeScript type checking and frontend lint.
The update race package passed in 26.276 seconds and notifications in 7.129
seconds; the earlier SMTP fixture failure did not recur in this combined run.
`make check-notices` passed its nine focused tests and verified the unchanged
offline notice inventory against the final imported package sets.

All four release-target executables built with `CGO_ENABLED=0`, `GOARCH=amd64`,
`-trimpath` and development version metadata: Linux and Windows manager and
Linux and Windows updater. These are build checks, not execution qualification.
The final UI build used `npm --prefix web run build`; browser evidence is tied
to the exact embedded binary hashes in the Task25 report.

After normal-update fix5c5a4e7 and UI fix25598bd, the same combined checks passed
again. `make gen` produced no generated-type diff, production web build passed,
and all four binaries rebuilt successfully. The affected full race runs were
update27.027s, lifecycle10.558s, modpack13.230s; unchanged packages reused the
Go test cache. Normal/shared fix review closed all three Important findings and
the separate rollback review has no findings. Final UI review still has its
separately reported stale-response edit-loss correction pending.

## Remaining gates

Independent review covers these root-owned changes together with Task18's
normal coordination; rollback and final UI receive their scoped reviews. A
whole-branch review follows the frozen combined implementation and passing
repository verification. No real game/client, installed systemd, native Windows,
power-loss, successful real backup/restore or stable support claim follows from
this report. No EULA was accepted or host service installed by these checks.
