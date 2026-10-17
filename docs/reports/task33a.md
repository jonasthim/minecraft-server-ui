# Task33A — first real server/client acceptance harness

Status: implementation and deterministic source verification prepared for independent review. No hosted game run has been dispatched by this worker. This report does not qualify a Minecraft tuple or close Task33. The integrator owns hosted dispatch, shipping fixes, ledger status and acceptance.

## Scope and command interface

Only Vanilla **1.21.9**, x64 Ubuntu **24.04** and Windows **Server 2022** are allowlisted. Node22+ and the actual shipping native installer/manager are required. Both wrappers refuse non-hosted environments, existing canonical installations, and unknown version inputs. The user explicitly accepted the EULA for disposable GitHub game tests; no local game/service was launched.

Linux command:

```sh
sudo --preserve-env=GITHUB_ACTIONS,RUNNER_ENVIRONMENT,PATH bash deploy/qualification/linux-game.sh /absolute/minecraft-ui /absolute/report-directory 1.21.9
```

Windows command:

```powershell
pwsh -NoProfile -File deploy/qualification/windows-game.ps1 -Binary C:\absolute\minecraft-ui.exe -OutputDirectory C:\absolute\report-directory -MinecraftVersion 1.21.9
```

The report directory must exist. Only the workflow's `commit.txt`, `go-version.txt` and `runner.txt` may preexist; they must be regular files no larger than4KiB. Wrappers generate only `game-summary.json` and `result.json`. Root owns `.github/workflows/installed-game.yml` and its artifact upload allowlist.

## Intended real assertions

1. Start the installed unprivileged native manager against empty canonical state. Create the actual first admin via HTTP setup, retain the cookie jar in memory, fetch the session CSRF token from `/auth/me`, and attach it and the exact origin to mutations. Reject redirects and proxies.
2. Create `game-acceptance` with explicit EULA acceptance via the instance API. Resolve the exact Vanilla selection and install it through lifecycle preview/install, preserving all production guards. Read only the official managed Temurin21 runtime manifest and report its verified publisher hash/provenance/version.
3. Start via lifecycle API and require actual native JVM image, creation identity and working-directory evidence alongside acknowledged control/game/settings readiness. Linux evidence independently binds `/proc` image/cwd/boot/start ticks; Windows CIM binds actual image and process creation FILETIME. Windows working directory is corroborated by the private launch identity, as Windows has no supported remote-cwd API.
4. Connect `minecraft-protocol@1.68.0` as a real headless gameplay client to fixed `127.0.0.1:25565`. Require PLAY, a server teleport, nonempty world chunk data, completed chunk batch, and a keepalive. The library owns login/configuration/keepalive replies; the harness sends teleport acknowledgements, player-loaded, chunk batch acknowledgements and tick-end packets. It is neither a status ping nor HTTP fixture.
5. Send gameplay rotation yaw31.25 (marker A); disconnect/reconnect and require the server's initial teleport to read back that persisted yaw. Keep the observed client connected during the online full backup. Require the native game PID/start/runtime identity to remain unchanged.
6. Require post-cursor save-off/save-on console acknowledgements, a released lease journal before its deadline, a successful shipping job, and an actual archive manifest with `acknowledged_native_save_lease`. The1.21.9 flush uses shipping authenticated MSMP; evidence is the successful guarded backup path plus that manifest, rather than invented legacy `save-all` text. The narrow read-only SQLite query reads only the lease's state/expiry/release timestamp for that exact instance/job. No management credentials or raw RPC messages are read or reported.
7. Stream the private online archive through the shipping download API into a temporary private directory. Verify the HTTP/record SHA256, every manifest file size/hash, entry containment/type/count/size, real `world/level.dat`, and actual gzip NBT `world/playerdata/<offline-uuid>.dat` Rotation marker A. Do not extract archive entries.
8. Change yaw to121.25 (marker B), disconnect/reconnect and require server readback. Deliberately stop through the explicit administrator emergency API with typed instance confirmation. Restore via shipping API with `confirmed:true`; require a newly created protected safety backup, download it, and verify its real playerdata contains B. Start and reconnect, requiring restored A.
9. Enable autostart through lifecycle API and restart the manager. Linux must retain the exact game process; Windows must stop and start a new process. Require persisted admin session and reconnect marker A. Deliberately stop again and observe stopped state for65seconds, covering the first possible crash retry interval.

The original brief asked for world mutations through HTTP, but the shipping console deliberately allows only `list`, `seed`, `say`. The integrator amended the brief to authorize real gameplay protocol markers while preserving that guard. The marker is saved **player view state**, not terrain/block placement or full world semantic comparison. The archive assertions compare real saved playerdata in the world tree, and reconnect verifies server acceptance.

## Disposable fixtures, exposure and cleanup

The instance explicitly uses offline authentication for this credential-free client, creative/peaceful mode, fixed seed33, two players, and view/simulation distance2. These are visible instance API settings, not direct DB/config writes or changed product defaults. No RCON bypass, injected manager transport, fake runtime, fake server process or fabricated game data is used.

Before enabling offline authentication, Linux inserts verified IPv4/IPv6 native firewall INPUT rules blocking game/API ingress on all non-loopback interfaces. Windows requires enabled firewall profiles and inserts a blocking game/API rule for all non-loopback IPv4 addresses and all IPv6 addresses; its client targets only IPv4 loopback. Private management binds loopback through shipping configuration. No endpoint or port argument can redirect the client elsewhere.

Linux installs/stops/deletes only the fresh canonical unit/account/files and the fixed `minecraft@game-acceptance` unit. Linux manager shutdown intentionally preserves games, so cleanup separately stops the actual game unit and checks for processes under the owned UID. Windows creates an ephemeral non-admin account and LSA service-logon right using the existing reviewed `windows-updater-support.ps1` definitions, then uses the shipping installer; no updater is installed. Cleanup stops/deletes SCM service and removes account/right/rule/files. Orphan processes in the newly owned canonical install fail cleanup and receive bounded termination. Raw subprocess/installer/npm output remains temporary and is never uploaded. Failures emit only fixed phase/type/error codes and allowlisted API/job enums; no response messages, raw logs, passwords, cookies, secrets, DB or archives are artifact inputs.

## Test dependency review

The integrator approved isolated exact `minecraft-protocol@1.68.0`, BSD-3-Clause, Node>=22. Registry integrity:

```text
sha512-atAGg/fhIVqyK6voWvED5QA+4ah65PNwpoDnkHawWhVzQbwFii77KcF6OpsU/Xlbi0o9GpihHiPdORSxXEA+Ug==
```

The complete lockfile fixes75 installed packages (76 including the local root). No package declares an install script; all installs use `npm ci --ignore-scripts`. License inventory:65MIT,4BSD-3-Clause,2Apache-2.0,2BSD-2-Clause,1ISC,1CC0-1.0; every exact package/version/license is listed in `deploy/qualification/game-client/DEPENDENCIES.md`. The full protocol package notice is retained as `LICENSE.minecraft-protocol`; installed npm packages retain their own notices. No product/frontend dependencies changed. The tiny bounded NBT reader uses Python's standard library.

Initial audit found five moderate package findings from **one** underlying uuid advisory: [GHSA-w5hq-g745-h8pq / CVE-2026-41907](https://github.com/advisories/GHSA-w5hq-g745-h8pq). Original paths were protocol1.68.0 → prismarine-auth3.1.1 → @azure/msal-node2.16.3 → uuid8.3.2, and protocol1.68.0 → yggdrasil1.8.0 → uuid10.0.0. These authentication features are optional at execution, although their dependencies are installed normally. The advisory affects uuid<11.1.1 (also12.0.0 and13.0.0) and buffer bounds in v3/v5/v6. Both inspected auth callsites use `uuid.v4()` with no output buffer; offline UUID generation uses separate `uuid-1345`. Offline loopback execution limits reachability; it is not authentication qualification.

Root explicitly reviewed and approved test-only exact override **uuid11.1.1**. There is no patched version inside the parents' declared ^8.3.0/^10.0.0 ranges. The override crosses those ranges, while11.1.1 retains CommonJS and the v4 API used by both callsites. Source tests resolve uuid from each parent, generate/validate a v4 UUID, load the offline client imports, serialize all used1.21.9 gameplay packet shapes without connecting, and verify both package integrity strings. uuid11.1.1 locked integrity:

```text
sha512-vIYxrBCC/N/K+Js3qSN88go7kIfNPssr/hHCesKCQNAjmgvYS2oqr69kIufEG+O4+PfezOH4EbIeHCfFov8ZgQ==
```

Final `npm audit --json --prefix deploy/qualification/game-client`: **0 vulnerabilities**. No blanket audit fix was run.

Primary client references: [upstream1.68.0 API](https://github.com/PrismarineJS/node-minecraft-protocol/blob/1.68.0/docs/API.md), [configuration/play plugin](https://github.com/PrismarineJS/node-minecraft-protocol/blob/1.68.0/src/client/play.js), [keepalive plugin](https://github.com/PrismarineJS/node-minecraft-protocol/blob/1.68.0/src/client/keepalive.js). Installed exact locked protocol data was also inspected for1.21.9 packet shapes. No substantial upstream source was copied into the client.

## Verification performed locally

- `python3 -m unittest discover -s deploy/qualification -p game_api_test.py -v`:8 tests pass. Source checks cover hosted gates, paths/metadata/identifiers, CSRF/origin, redirect refusal, error redaction, terminal-job failure diagnostics, bounded gzip/NBT, real-byte synthetic archive hashing/unsafe entries, yaw comparison, and refusal of local CLI execution/unsafe versions.
- `npm test --prefix deploy/qualification/game-client`:6 tests pass. Simulated EventEmitter packets prove required evidence gates, marker reporting, failure codes, argument rejection, exact protocol packet serialization and dependency override resolution/integrity. They do not represent a joined game server.
- `python3 -m py_compile deploy/qualification/game_api.py deploy/qualification/game_api_test.py`:pass.
- `bash -n deploy/qualification/linux-game.sh`:pass.
- `node --check deploy/qualification/game-client/client.cjs` and `client.test.cjs`:pass.
- Clean exact `npm ci --ignore-scripts --prefix deploy/qualification/game-client`:pass; `npm audit --json ...`:zero findings after reviewed override.
- `pwsh` is unavailable on this Linux workstation. Native Windows parsing/execution remains a required hosted gate; no cross-compilation is substituted.

All synthetic ZIP/NBT and simulated protocol events above are deterministic source tests only. This worker did not run service installers, systemd/SCM/firewall changes, Minecraft, or any user's preview locally.

## Remaining acceptance

Independent source review, exact-source GitHub Ubuntu24.04/WindowsServer2022 dispatch, actual failure triage and final evidence/ledger integration remain with the integrator. A failed shipping guard or operation must remain a failed phase until root applies a reviewed production fix. This harness does not authorize broadening console grammar or fabricating state.

The full required version/layout matrix, other host families, Paper/Fabric/Forge/NeoForge, real addons/maps, update compatibility, crash/lease failure injection, and Microsoft-authenticated graphical clients remain explicitly unqualified. Passing the initial tuple cannot close all Task33 or support stable publication by itself.
