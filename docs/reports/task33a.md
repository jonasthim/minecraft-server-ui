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
3. Start via lifecycle API and require actual native JVM image, creation identity and working-directory evidence alongside acknowledged control/game/settings readiness. Linux evidence independently binds `/proc` image/cwd/boot/start ticks; Windows queries actual image, PID, liveness and the original100ns creation FILETIME from one native process handle. Windows working directory is corroborated by the private launch identity, as Windows has no supported remote-cwd API.
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

## Fix round1/5 — review of7b10b8a

The independent review in `docs/reports/review-task33a.md` requested changes for two Important findings. Both are corrected in this working-tree packet; independent re-review and hosted dispatch remain pending. No production files changed.

- **G1/R1, Linux installer scratch ownership:** `/usr/local/bin/minecraft-ui.new` now appears in the actual pre-ownership refusal loop, fixed owned cleanup operands and final absence loop. Existing regular files and dangling symlinks are refused without modification. Owned partial installer output is removed, and a leftover path makes cleanup fail. Three deterministic source regressions extract only those static fragments, validate/map every absolute operand under `TemporaryDirectory`, and execute no installer/service/account/firewall commands.
- **G2/R2, Windows timestamp precision:** removed `Win32_Process.CreationDate` conversion. A small C# helper opens the target with `PROCESS_QUERY_LIMITED_INFORMATION | SYNCHRONIZE`, binds `GetProcessId` and liveness to that handle, obtains original high/low FILETIME words through `GetProcessTimes`, queries the image through `QueryFullProcessImageName`, rechecks liveness, and closes the handle in `finally`. Concatenated eight-digit unsigned high/low hexadecimal words are compared exactly to the shipping launch identity; there is no rounding/tolerance. Python independently requires returned PID equality and exact image file binding. Existing wrapper SCM service-account SID/ACL checks are retained. The source abstraction regression uses FILETIME134100000000000007, accepts the exact value and rejects the value truncated to microseconds; additional cases reject wrong PID, wrong image and a failed native query.
- **Packet hygiene:** include the previously untracked `deploy/qualification/game-client/.gitignore` containing only `node_modules/`. Dependency installation output remains outside the source packet.

Red/green evidence:

```text
python3 -m unittest discover -s deploy/qualification -p game_api_test.py -v
Before implementation: Ran13 tests; FAILED (failures=4, errors=4).
  The actual original Linux guard accepted both .new fixtures (0 instead of2),
  the actual cleanup left owned .new bytes, and final absence coverage was missing.
  Windows regression errors identified the not-yet-extracted native helper.
After implementation and final source checks: Ran13 tests; OK.

python3 -m py_compile deploy/qualification/game_api.py deploy/qualification/game_api_test.py
PASS (exit0)
bash -n deploy/qualification/linux-game.sh
PASS (exit0)
git diff --check -- deploy/qualification/linux-game.sh deploy/qualification/game_api.py deploy/qualification/game_api_test.py docs/reports/task33a.md
PASS (exit0)
```

The installed .NET SDK10.0.112 also compiled the **actual** C# text extracted from `WINDOWS_SNAPSHOT_SCRIPT` in a temporary net10.0 library project. Command: `dotnet build <temporary>/Snapshot.csproj --nologo --verbosity quiet --ignore-failed-sources`; output: `Build succeeded. 0 Warning(s), 0 Error(s)` (exit0,2.98s). The SDK additionally printed a workload-verification notice; no workload changes were performed. This checks C# compilation only and does not execute Windows APIs. The temporary project and outputs were removed. `pwsh` remains unavailable locally, so hosted PowerShell parsing, actual Windows native-handle behavior and real game evidence are still required.

No local privileged operations, Windows calls, services or games were executed. Nothing was staged or committed by this worker.

## First hosted attempt — run39035607511

[Installed Minecraft acceptance39035607511](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39035607511) ran the exact source `02b554d797e543452fb99ff7ffc66c340caae73c`, with explicit EULA acceptance and Vanilla1.21.9, on Ubuntu24.04 and WindowsServer2022. Both jobs finished **failure**. This worker monitored the existing root dispatch and did not dispatch another run.

The artifact contents were read directly from the GitHub artifact ZIPs in memory, using only the workflow's allowlisted report files. Completed job logs were filtered to their fixed `PHASE`/`FAIL` messages and exit codes. No private runtime state or raw service logs were downloaded.

| Host | Scrubbed artifact | Manager SHA256 | Observed result |
|---|---|---|---|
| Ubuntu24.04 |12101883478|`f39ae7c1e10bb9cb6a7c803de235b89407e4a263527413b806f6d0e4456a86a3`|`official_resolve_install`; job failed at `installing`; wrapper exit1|
| WindowsServer2022 |12102186851|`e63bda82a280e6d01fb0b7ea64d69606f76290b84052ffdcf97b5f08e39b8d32`|Wrapper failed at `dependencies`; fixed catch type `ParameterBindingValidationException`; no API summary|

Ubuntu evidence establishes actual native manager startup, empty-state first-admin setup, instance creation/EULA acceptance and official Vanilla resolution. The resolved server artifact was58638725bytes with published SHA1 `11e54c2081420a4d49db3007e66c80a22579ff2a`. Installation failed before phase `managed_java`, so this attempt does not establish runtime installation, server launch, client play/chunks, backup, restore or reconnect.

The old Linux report retained only generic terminal state/phase. `jobs.Service` deliberately writes a generic public `job.error`; lifecycle logging also omits wrapped causes. Therefore the exact installation failure cause is **unknown** from this attempt. Source inspection places the failure before managed-runtime work, among stopped-job admission, activation recovery, preview validation and managed-service availability. A writable hosted-image `/usr/local/bin` prefix would violate the shipping supervisor's root-owned-ancestry guard, as seen in prior updater qualification, but this artifact does not record its mode. It is a plausible precondition, **not a confirmed explanation for this run**.

Windows failed before firewall/account/installer/API work. The original `dependencies` stage covers Node validation and npm installation, and its catch omitted source location, so the exact throwing statement is **unknown**. No game or client qualification is implied by either failed job.

## First-run diagnostic and disposable-host preparation patch

Root authorized only the following harness changes; production guards and shipping code remain unchanged:

- Failed API jobs now read one bounded public `/jobs/{id}/logs?after=0&limit=500` page and map **exact** known authored messages to fixed diagnostic enums. Generic lifecycle failures remain `lifecycle_failure_unspecified`; unknown text stays `unclassified`. Retrieval/bounds errors have fixed diagnostic codes and never replace the original failed job state/phase. No log text, wrapped error cause, path, URL, secret, request payload or arbitrary error body is emitted.
- Linux records fixed filesystem permission metadata for the executable/helper/unit and their fixed ancestors in the existing `result.json`: UID/GID, mode, inode/device, existence and file type only. Snapshots bracket prefix preparation and cleanup. There are no new artifact filenames.
- Linux ports the already reviewed updater harness's explicit disposable-image preparation: validate root ownership, directory type, no symlinks and safe ancestor modes; record the original `/usr/local/bin` mode and device/inode/UID/GID; clear only group/other write bits for the test; preserve every unrelated mode bit; then restore the exact original mode only if the original directory binding remains intact. This prepares a trusted native installation without bypassing supervisor checks. It may remove a runner precondition but does not prove the old failure's cause. Actual pre-test mode and restoration will be visible on the next reviewed hosted attempt.
- Windows now separates `dependency_node` and `dependency_npm` and records only a fixed exception classification and bounded numeric source line. This adds diagnosis, not a speculative fix for the unknown parameter-validation cause.

Deterministic source evidence:

```text
python3 -B -m unittest discover -s deploy/qualification -p game_api_test.py -v
Diagnostic tests before implementation: Ran14; FAILED (failures=1, errors=8).
Prefix/metadata tests before implementation: Ran17; FAILED (failures=2, errors=1).
After both changes: Ran17; OK (0.202s).
bash -n deploy/qualification/linux-game.sh
PASS (exit0)
python3 -m py_compile deploy/qualification/game_api.py deploy/qualification/game_api_test.py
PASS (exit0)
git diff --check -- deploy/qualification/game_api.py deploy/qualification/game_api_test.py deploy/qualification/linux-game.sh deploy/qualification/windows-game.ps1 docs/reports/task33a.md
PASS (exit0)
```

New source fixtures verify exact classification and rejection of malicious/near-match messages, unavailable/oversized diagnostic pages, metadata-only output, mode preservation/restoration for0777/02777/01777/0755, and refusal to restore through a replaced directory binding. All prefix paths are remapped under temporary fixture directories; no privileged host prefix, native service or game was touched locally. Existing `.new` refusal/removal/final-absence tests continue to pass. `pwsh` remains unavailable locally, so the new Windows source diagnostics still require hosted PowerShell execution.

This patch requires independent source review and a later integrator-owned exact-source dispatch. The failed attempt remains failed; Task33A real gameplay acceptance and the broader Task33 matrix remain open.

## Second hosted attempt — run39036770835

[Installed Minecraft acceptance39036770835](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39036770835) ran exact source `d0994d106c77421eb36983a79e4601aeb69d065d` with the same explicit EULA/Vanilla1.21.9 inputs. Both hosts finished **failure**. This worker monitored the integrator's dispatch, read only the scrubbed artifacts and filtered failure signals, and performed no further dispatch.

Ubuntu artifact **12102357708**, manager SHA256 `8da8fa2f7937f8c1d0ac4d63f07a94b16d7cd8badc9087c13c2176033b2958aa`, establishes progress beyond the first attempt: shipping lifecycle installation completed, and the actual managed official Adoptium runtime manifest reports Temurin21.0.12.1+1-LTS, archive SHA256 `ce79869e1307ed8ee1e2baa86a412b1eb5b75d10a01006d788a6f968bcfaee94`. Native start then failed at **`verifying_game_readiness`**, reported under harness phase `native_start_readiness`; the bounded public-job classifier returned `unclassified`. This is a failed product lifecycle readiness job, not a passed game-start assertion. Its specific underlying readiness cause remains unknown from the retained evidence; no client, online backup, restore or reconnect assertions were reached. The integrator was notified for root-owned production diagnosis.

The new Linux metadata confirms this runner's original `/usr/local/bin` was UID0/GID0, nonsymlink directory, mode0777, device2049/inode50791. Test preparation changed only the mode to0755. Before cleanup it remained0755, and restoration returned it to0777 on the exact same device/inode. The previously suspected unsafe-prefix precondition is therefore **observed and corrected for this run**. This does not retroactively prove the earlier run's exact failure cause.

Windows artifact **12102616083**, manager SHA256 `21a2640061cd4a0c71785785fa0a21a2c7bb9a7191070ab6fbae90bbd699e2ff`, narrows the initial failure to **`dependency_npm`**, before firewall/account/installer/API work. Its `failure_diagnostic` remained null. The completed job log supplies a concrete harness defect: **`Unable to find type [Management.Automation.ParameterBindingValidationException]`**. The newly added catch classifier attempted to resolve an unavailable PowerShell type before storing its numeric line and classification, masking the original npm-stage exception. Therefore the npm operation's original error/source line is still unknown.

The demonstrated catch defect is corrected by capturing the existing exception's runtime `GetType().Name` as text and comparing it to the fixed string `ParameterBindingValidationException`; it no longer tries to resolve that type. A safe default diagnostic is stored before further classification, and the fixed catch output reuses the captured type name. No speculative change to npm invocation, dependency versions, authentication, native installers or production guards is included.

Source regression and verification:

```text
python3 -B -m unittest discover -s deploy/qualification -p game_api_test.py -v
Before correction: Ran18 tests; FAILED (failures=1).
After correction: Ran18 tests; OK (0.194s).
```

The added source guard rejects the exact unavailable type literal that this hosted job proved unsafe. It does not substitute for native PowerShell execution; `pwsh` remains unavailable locally. This correction needs source review and a later root-owned hosted run to expose the original npm-stage failure. Both hosted attempts remain failed and Task33 gameplay acceptance remains open. No source was staged or committed by this worker, and no local native operation or game was launched.

## Third hosted attempt — run39038021693

[Installed Minecraft acceptance39038021693](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39038021693) ran exact source `c41b2d654ccde2a7be9edbd6a1c7b40c39a9801c`. Both hosts still finished **failure**. The integrator provided the scrubbed artifact trees at `/home/thim/.cache/dilla-tmp/game-39038021693`; this worker reviewed them and the fixed Windows failure signals. No worker dispatch was performed.

The Ubuntu manager SHA256 was `27d1ffe8cb1802283c79c28cf0949d6c5bbd8063d8d846edb57c848f483146fd`. This attempt establishes a real initial milestone for **Vanilla1.21.9 on Ubuntu24.04**: installed native process/readiness and required settings verification passed after the reviewed discovery-array correction. The native JVM was PID3000, with boot/start identity `e4546f71-b5c6-4088-b984-9ef7efe538bc:15368`. Two actual offline loopback `minecraft-protocol@1.68.0` PLAY connections each received49world chunks, one acknowledged teleport and one keepalive. The first sent gameplay yaw31.25; the second's server-supplied initial yaw was31.25, proving the saved player-view marker survived disconnect/reconnect. Their completed chunk-batch counts were12and6. This is real headless gameplay evidence for the stated disposable tuple, not Microsoft-authenticated/graphical-client qualification.

The harness then failed at `online_backup` with HTTP409/`instance_busy`, **before an online backup job could be admitted**. There is no successful lease/archive/restore/restart assertion in this attempt. Exact-source inspection narrows the throwing request to `POST /instances/game-acceptance/backups`: the preceding backup-list and console-history GET implementations have no `instance_busy` path. Backup enqueue reaches `jobs.Enqueue`, which rejects an existing `s.idle` reservation. Background read-only console/player/map observation currently uses that same exclusive reservation. This establishes a job-admission race with read-only observation, but the artifacts do not identify the specific concurrent holder. It would be inaccurate to name one sampler as the observed holder.

Root assigned the product admission correction separately. This worker did not change jobs, console, backup admission or retry/wait behavior. The owned harness now adds only a **fixed `api_operation` enum** to failed HTTP diagnostics. Exact allowlisted route/method shapes distinguish backup-create/list, console logs, lifecycle and other harness calls; unknown paths become `unclassified`. It retains no raw endpoint, identifier, request payload, URL or body and preserves the failed response. This makes a future409 directly attributable without source-path elimination.

Windows manager SHA256 was `968b66f53d487505301f05bb1950d72df28b82f67cb362a2658c6be1e72c2f8f`. Its working catch now reports `dependency_npm`, `parameter_binding_validation`, source line47, covering the invocation containing an inline `Join-Path $PSScriptRoot` argument. Toolchain setup recorded Node24.21.0/npm11.19.0. Firewall/account/installer/game API work was not reached.

## Windows npm scope reproduction and correction

Root explicitly approved an official **portable PowerShell development tool in disposable task cache**, solely for harmless argument/scope fixtures. No system package was installed, no persistent PATH/profile change was made and no product dependency was added. The downloaded [official PowerShell7.6.7 release](https://github.com/PowerShell/PowerShell/releases/tag/v7.6.7) Linux-x64 archive SHA256 was verified as `6896f9e3a6b0a354bf470835a96da76d2f801cd50dcf047ad7c4563bf93bf431` against both the separate official `hashes.sha256` file and official release metadata. The checksums file itself matched its independently published SHA256 `6c2d5f481cc20c83df45d5c29e15f0d7e9e1db82eb5b8f7f2fe33b107f308325`. Both checks passed before extraction/execution. The tool is confined to `/home/thim/.cache/dilla-tmp/task33a-powershell-7.6.7`.

The exact [upstream npm11.19.0 PowerShell shim](https://raw.githubusercontent.com/npm/cli/v11.19.0/bin/npm.ps1), SHA256 `3bf7c5ef339b71d6f8bc4ef4a7b7adb90727788674f0a3cef2ede8f5969ca327`, reparses the original invocation's argument expressions and executes them through `Invoke-Expression`. The inline `$PSScriptRoot` expression is re-evaluated there with an empty script root. With that actual shim, caller/dot-source fixtures and harmless local JS stubs standing in for npm-prefix/npm-cli, the original harness command reproduced `ParameterBindingValidationException`, parameter `Path`, and `ParameterArgumentValidationErrorEmptyStringNotAllowed,Microsoft.PowerShell.Commands.InvokeExpressionCommand`. A simple non-reparsing shim did not fail, isolating the re-evaluation behavior.

The focused correction resolves `$gameClientDirectory` and `$npmLog` **before** invoking npm and passes those scalar variables. The actual upstream shim then passed the exact `ci`, `--ignore-scripts`, `--prefix`, and caller-relative directory arguments, including a directory containing spaces. It invokes no real npm installation in the reproduction. There is no npm.cmd workaround, shell-evaluation change, dependency/version change or increased wait.

The permanent network-free regression extracts the **actual current dependency block** from `windows-game.ps1` and runs it in a temporary PowerShell caller against a purpose-written parser/re-evaluation stand-in. It copies no substantial upstream shim source into the repository, executes no package install and tests the space-containing directory and exact arguments. It runs with an explicitly supplied development tool or an available CI `pwsh`; it skips explicitly if neither exists.

Verification:

```text
python3 -B -m unittest discover -s deploy/qualification -p game_api_test.py -v
HTTP operation diagnostic before implementation: Ran20; FAILED (failures=6).
After implementation: Ran20; OK (0.202s).

MINECRAFT_UI_TEST_PWSH=/home/thim/.cache/dilla-tmp/task33a-powershell-7.6.7/runtime/pwsh python3 -B -m unittest discover -s deploy/qualification -p game_api_test.py -v
Original npm dependency block: Ran21; FAILED (failures=1), empty Path in Invoke-Expression.
Precomputed argument block: Ran21; OK (0.484s).

Portable PowerShell Parser.ParseFile on windows-game.ps1 and windows-updater-support.ps1
PASS: 0 parse errors for each (files parsed, not executed).
python3 -m py_compile deploy/qualification/game_api.py deploy/qualification/game_api_test.py
PASS (exit0)
git diff --check -- deploy/qualification/game_api.py deploy/qualification/game_api_test.py deploy/qualification/windows-game.ps1 docs/reports/task33a.md
PASS (exit0)
```

The portable Linux PowerShell fixture is language/argument evidence, **not Windows OS qualification**. Temporary fixture callers, outputs and JS stand-ins were removed. No service/account/firewall/LSA/native Windows function, Minecraft process or user preview was used locally. The native Windows hosted workflow must still execute the corrected path and all remaining game assertions. Source review and the independently owned observation-admission fix are required before root dispatches the next exact-source game run. Online backup/restore/restart and Task33 completion remain open.
