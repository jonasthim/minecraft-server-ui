# Task 01 foundation implementation report

Status: DONE_WITH_CONCERNS (foundation complete; full product remains in progress).

## Implemented

- Go module `github.com/jonasthim/minecraft-server-ui`, Go 1.27.0, native `minecraft-ui serve|version`, embedded React 19/Mantine 9 frontend, graceful shutdown and configurable listen/data/public-origin settings.
- SQLite modernc connection, escaped filesystem URI, WAL/busy timeout/foreign keys, owner-only database permissions, isolated memory databases, per-database goose migration provider without process-global mutable migration configuration.
- Migration 00001 creates instances/config storage, persisted process identities, artifact records and durable jobs with one **running** job per instance (multiple queued rows remain allowed). Auth migration 00002 is owned separately.
- Typed shared instance/config, flavor, runtime, distribution plan, artifact, installed verified artifact, argv launch, JVM process identity, capability, content plan, jobs, structured errors, repository/provider/supervisor and acknowledged command contracts.
- Stable legacy 1.12.2+ and modern 26.x release validation; validation is structural, provider resolution must prove exact version support.
- Distinct real `/healthz` JSON endpoint with database readiness; unknown API, misspelled health and missing asset paths return JSON 404 rather than SPA HTML.
- Strict bounded JSON decoding rejects unknown fields, extra JSON values and garbage suffixes; domain errors preserve cause internally and sanitize client output.
- Query/Mantine/notifications providers, responsive sidebar, account/admin routes, role-aware links and lazy administration pages. Real auth setup/login/account/users/OIDC/audit pages provided by identity worker are integrated. Empty servers page has no disconnected controls.
- OpenAPI health contract and shared schema definitions, merged all 22 identity operations from identity-owned docs/api/auth.yaml, and generated TypeScript. Future resource contracts remain follow-up work.
- Make dependency/build/dev/test/check/gen targets, embedded frontend build, native Linux and Windows amd64 targets, CI build/check configuration, ignore rules and environment example.

## Ownership and exact interfaces

Foundation-owned files:

- `.env.example`, `.gitignore`, `Makefile`, `.github/workflows/ci.yml`, `go.mod`, `go.sum`.
- `cmd/minecraft-ui/main.go`, `cmd/minecraft-ui/main_test.go`.
- `internal/config/config.go`, `internal/config/config_test.go`.
- `internal/domain/instance.go`, `instance_test.go`, `contracts.go`, `errors.go`, `errors_test.go`. Identity owns `domain/auth.go`.
- `internal/db/db.go`, `db_test.go`, `migrations/00001_foundation.sql`. Identity owns settings/users/session/token/audit storage and migration 00002.
- `internal/api/router.go`, `router_test.go`, `respond.go`, `respond_test.go`, `spa.go`. Identity owns `auth_handlers.go`.
- `web/embed.go`, `web/dist/.gitkeep`, `web/package.json`, `web/package-lock.json`, `web/index.html`, `web/vite.config.ts`, `web/postcss.config.cjs`, `web/tsconfig*.json`, `web/src/main.tsx`, `App.tsx`, `theme.ts`, `index.css`, `layout/Shell.tsx`, `api/schema.d.ts`. Identity owns `web/src/api/client.ts` and `web/src/features/auth/**`.
- `docs/openapi.yaml`, this report.

Public integration points:

- `db.Open(ctx,path) (*sql.DB,error)`, `db.OpenMemory(ctx) (*sql.DB,error)`, `db.Migrate(ctx,*sql.DB) error`; goose discovers embedded `internal/db/migrations/*.sql`.
- `api.NewRouter(api.Dependencies{DB *sql.DB,Logger *slog.Logger,Web fs.FS,Mount func(chi.Router)}) http.Handler`; Mount receives root router once. Identity also exposes an API-router mount for later shared `/api/v1` wiring.
- `api.WriteJSON(w,status,value)`, `api.WriteError(w,error)`, `api.WriteValidation(w,...domain.FieldError)`, `api.DecodeJSON(r,dst)`.
- `config.FromEnvironment() (Config,error)` with Address/DataDir/BaseURL/InsecureCookies. Non-Secure cookies default only on HTTP loopback; remote HTTP requires explicit override. Production origin supplied via MINECRAFT_UI_BASE_URL.
- `domain.DistributionProvider.Install(ctx,directory,InstallPlan,JavaRuntime,writer) (LaunchSpec,error)` requires explicit Java runtime.
- `domain.ProcessIdentity.PID` is the actual JVM; LauncherPID is separate; StartIdentity and JavaRuntimeID/JavaChecksum persist identity information beyond PID reuse.
- `domain.CommandExecutor.Execute(ctx,instanceID,command) (CommandResult,error)` reports output/received/verified; stdin ProcessSupervisor.Command remains lower-level.

## TDD evidence

RED: `go test ./internal/domain ./internal/db ./internal/api ./internal/config` failed before implementation with missing Instance/Flavor/Open/NewRouter/FromEnvironment symbols. Added structured error and decoder tests failed with missing ErrorCode/E/AsError/DecodeJSON. Added 26.1/26.3 acceptance case failed because old validation only allowed 1.x. Added CLI and origin/cookie tests failed with missing run and Config.InsecureCookies.

GREEN: the same foundation package command passed after implementation. Native CLI/config tests passed. Database tests verify reopen persistence, foreign keys and memory isolation; API tests exercise real SQLite-backed health and differentiate API/assets/health misses from SPA paths.

## Verification

- `go test -race ./cmd/minecraft-ui ./internal/api ./internal/config ./internal/db ./internal/domain ./internal/auth ./internal/audit` PASS (audit has no test files). Foundation and completed identity included; runtime intentionally excluded while its owner implements.
- `go vet ./cmd/minecraft-ui ./internal/api ./internal/config ./internal/db ./internal/domain ./internal/auth ./internal/audit` PASS.
- `npm run typecheck`, `npm run lint`, `npm run gen:api` PASS.
- `make build-linux build-windows` PASS with embedded frontend and CGO disabled; Windows binary cross-compiles, not executed on Windows. Browser bundle after route splitting 414.98 kB main JS, no chunk warning.
- Offline dependency synchronization/installation audited 87 packages, 0 vulnerabilities.
- Native built Linux binary with temporary data directory: real HTTP `/healthz` returns `{status:ok}`, `/api/v1/auth/status` needs_setup true, `/account` returns embedded HTML, `/health`, `/api/v1/missing`, missing asset all 404, SIGTERM exits 0. PASS.
- Earlier `go test ./...` failed solely runtime worker's in-progress test-first undefined Manager/New/jvmInfo/extractArchive. This is not a final whole-project green claim. Whole-project check and integrated browser acceptance belong to final integration after all tasks.

## Self-review and limitations

- Corrected initial modern-version rejection, settings migration overlap, queued-job uniqueness, missing explicit Java installer dependency and unverified catalog checksum wording during review.
- Removed untrusted forwarded-IP middleware; trusted proxy behavior is not implicitly enabled.
- Main frontend offers identity flows and an empty servers shell; instance creation/lifecycle/content/backups/maps/metrics are not implemented by this foundation and are not presented as working.
- Identity OpenAPI fragment with all 22 operations is merged into docs/openapi.yaml and generated types; future resource routes must be documented and regenerated with those implementations.
- No live Minecraft process/provider network operation, browser visual acceptance or Windows execution has been performed here.
- No files staged or committed; shared workspace integration owner handles commits.
