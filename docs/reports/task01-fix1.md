# Task 01 review fixes, round 1

Status: implemented; integrator review pending. No package acceptance or release qualification is claimed.

## Review findings addressed

1. Added the missing shared event/backup/schedule/metrics/catalog contracts in `internal/domain/feature_contracts.go`. These are DTOs and narrow interfaces with documented boundaries, not feature implementations. Backup service methods enqueue work and return durable job IDs; save lease ownership is separate from archive/restore. Restore confirmation/safety/stopped requirements and lease reconciliation are documented. Metrics represent missing observations as null and carry provenance/gap reasons. Catalog compatibility declarations remain distinct from tested support; no credential is part of a public DTO.
2. Coordinated directly with Task 03: `events.Event` and `events.Log` now alias `domain.Event` and `domain.EventLog`. Task 03 owns the implementation/aliases. `domain.Job` is the shared base record; `jobs.Job` extends it with public ownership/progress/cancellation fields. The single complete OpenAPI `Job` projection comes from the jobs fragment; no incompatible second API identity was introduced.
3. Fixed documented development: `make dev-backend` explicitly sets backend listen `127.0.0.1:8080` and browser origin `http://127.0.0.1:5173`; Vite explicitly binds `127.0.0.1:5173` with strictPort. No production CSRF implementation was edited or relaxed. Environment example and `docs/development.md` explain standalone/development origins and equivalent PowerShell commands.
4. Merged the six Task 03 operations and Job/JobLog/JobEvent schemas from owner-provided `docs/api/jobs.yaml` into shared OpenAPI; added schemas for the new feature DTOs; ran the documented generator. Jobs service/router startup was not mounted in main by this change.
5. Updated `docs/IMPLEMENTATION.md` under explicit integrator delegation and mirrored the ignored SDD progress file byte for byte. Tasks 01/02 are reviewed with fixes pending, 03 is in progress, 06 is implemented pending review. Recorded root-provided official Temurin smoke evidence and commit abacb69 without changing runtime files or broadening support claims.

## TDD evidence

RED: `go test ./internal/config -run TestDevelopmentCommandAcceptsViteOrigin -v` failed with `dev origin = "http://127.0.0.1:8080"; Vite uses http://127.0.0.1:5173`. The test obtains the actual Makefile command via `make -n`, applies its environment to real config loading, then exercises the real CSRF guard with the browser origin.

GREEN: the same command passed after the Make/Vite change. Final tests also have a platform-independent config/CSRF case. Only the extra Makefile dry-run contract case skips if make is absent; native Windows config/CSRF behavior is still tested. An unrelated browser origin is rejected with 403.

## Verification

- `go test -race ./internal/config ./internal/domain` PASS.
- `go vet ./internal/config ./internal/domain` PASS.
- `make gen` PASS; generated schema includes the merged complete job resource projection and future feature DTOs.
- `npm run typecheck` and `npm run lint` PASS.
- `git diff --check` PASS.
- `cmp docs/IMPLEMENTATION.md .superpowers/sdd/2026-10-16-minecraft-server-ui/progress.md` PASS.
- Initial expanded `go test -race ./internal/config ./internal/domain ./internal/api` hit Task 03's in-progress `jobs_handlers_test.go:17 undefined MountJobs`; config/domain passed. After that owner supplied the handler, `go test -race ./internal/config ./internal/domain ./internal/api` PASS for all three packages.

## Files owned by this fix

- `.env.example`, `Makefile`, `web/vite.config.ts`.
- `internal/config/dev_test.go` (new), `internal/domain/feature_contracts.go` (new).
- `docs/development.md` (new), `docs/IMPLEMENTATION.md`, ignored `.superpowers/sdd/2026-10-16-minecraft-server-ui/progress.md`.
- `docs/openapi.yaml`, generated `web/src/api/schema.d.ts`.
- `docs/reports/task01-fix1.md`.

Identity fixes, Task 03 handlers/service/event aliases/API fragment and Task 06 runtime code are owned by their respective workers and were not edited here. No files staged or committed.
