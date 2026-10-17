# Development

Use Go 1.27.0 (see `go.mod`), Node.js 24 and npm. GNU Make is needed for the repository targets; Windows PowerShell can run their Go/npm commands directly. Install locked dependencies with `make deps` (`go mod download` and `npm ci` in `web`).

For the embedded application, run `make build` and `./bin/minecraft-ui serve`, then open `http://127.0.0.1:8080`. This creates private local state under `./data` by default. An empty database presents administrator setup, followed by the server list and **Create your first server**. Creating settings does not install or start Minecraft. See [identity.md](identity.md) for access controls.

`make check` runs Go vet, race tests, strict TypeScript checking and frontend lint. `make test` runs Go tests. After an API schema change, run `make gen` to regenerate `web/src/api/schema.d.ts`; do not edit generated types. `make web` checks TypeScript and builds the frontend bundle.

Manager cross-builds: `make build-linux build-windows`. Optional privileged updater builds: `make build-updater` for the local host, or `make build-updater-linux build-updater-windows` for Linux/Windows x64 artifacts. The latter produce `bin/minecraft-ui-update-linux-amd64` and `bin/minecraft-ui-update-windows-amd64.exe`, matching the manager artifact platform suffixes. Builds do not install services. Cross-build success does not qualify native host behavior; see [RUNBOOK.md](RUNBOOK.md) and [IMPLEMENTATION.md](IMPLEMENTATION.md).

The backend API listens at `127.0.0.1:8080`. Vite serves the browser at the exact public origin `http://127.0.0.1:5173` and proxies `/api` requests to the backend. Vite binds to `127.0.0.1` and fails if port 5173 is occupied. Open that address explicitly so browser mutations match the configured origin.

On Linux, run `make dev-backend` and `make dev-web` in separate terminals. The backend target sets the public origin to Vite and uses `./devdata`.

On Windows PowerShell, run the backend in one terminal:

```powershell
$env:MINECRAFT_UI_ADDR = '127.0.0.1:8080'
$env:MINECRAFT_UI_BASE_URL = 'http://127.0.0.1:5173'
$env:MINECRAFT_UI_DATA_DIR = './devdata'
go run ./cmd/minecraft-ui serve
```

In another terminal:

```powershell
npm --prefix web run dev
```

For a standalone binary serving its embedded UI, use the backend's own browser origin (default `http://127.0.0.1:8080`). Production deployments set `MINECRAFT_UI_BASE_URL` to the externally visible HTTPS origin. The development commands configure a precise origin; they do not permit arbitrary cross-origin requests.

Configuration and CSRF tests run on all platforms. The additional Makefile dry-run contract test skips only when `make` is unavailable. Windows build/test execution does not replace the real host and Minecraft qualification required for release.
