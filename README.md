# Minecraft Server UI

A native Go application with an embedded React 19/Mantine UI and SQLite state for managing local Minecraft Java servers. It provides separate server instances, managed Java runtimes, console output, backups, content management and observation tools.

This branch is in development. [GitHub Actions](https://github.com/jonasthim/minecraft-server-ui/actions) runs native Go race tests and builds on Ubuntu 24.04 and Windows Server 2022, plus frontend, API and dependency-notice checks. The six [Linux installer scenarios](docs/reports/task30-linux-qualification.md#accepted-ubuntu-installer-milestone) pass on Ubuntu 22.04 and 24.04. Real Minecraft/client workflows, the compatibility matrix and broader native-host release qualification remain open. Passing CI does not establish stable release support. See the [implementation ledger](docs/IMPLEMENTATION.md) for current evidence and remaining work.

## Try the local UI

Install Go 1.27.0, Node.js 24 and npm, plus GNU Make for these commands. From this checkout:

```sh
make deps
make build
./bin/minecraft-ui serve
```

Open **http://127.0.0.1:8080**. On an empty database, create your administrator account, then choose **Create your first server**. Select server software and an official Minecraft version, a unique server ID and a free game port. Creating a server saves settings; installing server files and accepting the Minecraft EULA are separate steps. No game starts during account or server creation.

The default data directory is `./data`, resolved relative to your working directory; keep it private and persistent. The default listen address is `127.0.0.1:8080`. Set `MINECRAFT_UI_DATA_DIR`, `MINECRAFT_UI_ADDR` and `MINECRAFT_UI_BASE_URL` to use a different data directory, address or browser origin. The browser origin must match the configured base URL for authenticated mutations.

## Develop and operate

- [Development](docs/development.md): prerequisites, frontend/backend development, generated API types and checks.
- [Identity and access](docs/identity.md): administrator setup, local/OIDC accounts, roles and tokens.
- [Runbook](docs/RUNBOOK.md): host operation, safety boundaries, updates and qualification requirements.
- [Linux installer](docs/linux-installer.md): authenticated installation, check, upgrade, recovery and retained-data uninstall; tested with native Ubuntu 22.04/24.04 lifecycle fixtures.
- [Approved design](docs/superpowers/specs/2026-10-16-minecraft-server-ui-design.md): product scope and release requirements.

The optional privileged updater is built separately with `make build-updater`; building it does not install or enable a host service. See the runbook before any host installation.

Licensed under the [MIT license](LICENSE).
