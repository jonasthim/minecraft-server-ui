# Development

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
