# Task33A independent specification and quality review

Reviewed candidate: `7b10b8a` against `574c505` (the 12-file packet in `.superpowers/sdd/2026-10-17-valheim-parity-completion/review-task33a.diff`, including the root-owned installed-game workflow).

**SPEC: FAIL — QUALITY: REQUEST CHANGES.** Two Important findings require correction before hosted dispatch. No production code changes are requested by this review.

The review follows `AGENTS.md`, the Task33A brief, the approved design/plan/implementation ledger, and the integrator's amendments approving gameplay yaw markers and the isolated exact protocol/uuid dependencies. The existing local preview and concurrent pending-settings changes were outside scope. No local services, installers, firewall changes, Minecraft connections, or user-preview interaction were performed.

## Findings

### R1 — Important: Linux fresh-state refusal and cleanup omit the installer's temporary executable

Locations: `deploy/qualification/linux-game.sh:18` and `deploy/qualification/linux-game.sh:56`; shipping operation: `deploy/linux/install.sh:17`.

The harness checks `/usr/local/bin/minecraft-ui` before claiming installation ownership, but omits `/usr/local/bin/minecraft-ui.new`. The shipping installer writes the supplied binary to that exact `.new` path before renaming it. A preexisting regular file or dangling symlink at this path therefore passes the fresh-install guard and can be replaced by the installer. If installation fails between writing and renaming, cleanup also omits the newly owned `.new` file and leaves it behind. This violates the explicit fresh-state/no-existing-damage and failure-cleanup requirements.

Reviewer reproduction evaluated only the actual static refusal loop and cleanup operands, with every absolute path remapped under `TemporaryDirectory`; no installer or native service commands were executed. With a preexisting `.new` fixture, the refusal loop exited **0**. With an owned `.new` fixture, the actual cleanup operand list left the file present. The repository's existing `updater_paths_test.py` already demonstrates the relevant regular-file/dangling-symlink and owned-cleanup regression pattern for the updater harness.

Required correction: include the exact installer scratch path in pre-ownership refusal and owned cleanup, verify its final absence, and add isolated regression coverage proving refusal preserves preexisting regular files and dangling symlinks and cleanup removes owned partial output.

### R2 — Important: Windows native identity compares timestamps with incompatible precision

Location: `deploy/qualification/game_api.py:378` through the comparison at line384; shipping identity: `internal/launcher/identity_windows.go:34` and its FILETIME formatting at line57.

The harness obtains `Win32_Process.CreationDate` through CIM, converts it back with `ToFileTimeUtc()`, and requires exact hexadecimal equality with the launcher's creation identity. The launcher obtains the original native `GetProcessTimes` FILETIME. CIM datetime represents only microseconds, while native FILETIME uses 100 ns units. Conversion back to FILETIME cannot recover the final fractional digit. Consequently a valid JVM whose native creation time is not exactly representable in CIM can fail `native_start_mismatch` before any client acceptance begins. Formatting the converted value as 16 hexadecimal digits does not restore that precision.

This is a source-level inference from the two documented representations, not a claimed native Windows reproduction: Microsoft documents the six-digit fractional field in [CIM_DATETIME](https://learn.microsoft.com/en-us/windows/win32/wmisdk/cim-datetime), the datetime type of [Win32_Process.CreationDate](https://learn.microsoft.com/en-us/windows/win32/cimwin32prov/win32-process), and the 100 ns FILETIME returned by [GetProcessTimes](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-getprocesstimes).

Required correction: obtain the process creation timestamp from a native process handle without the CIM datetime conversion, preserving the exact identity check. Add a deterministic regression with nonzero sub-microsecond ticks and retain the required native Windows hosted verification.

## Reviewed behavior and evidence

- First-user setup uses the actual shipping setup/session endpoints, in-memory cookies, `/auth/me` CSRF, fixed same-origin mutation headers, no proxies and redirect refusal. Instance creation, lifecycle preview/install/start/autostart/explicit emergency stop, backup download and confirmed restore match shipping handlers. No DB/config mutation manufactures product state.
- Firewall setup precedes the offline instance configuration. Linux rules cover IPv4/IPv6 ingress outside loopback; Windows blocks non-loopback IPv4 and all IPv6 for the fixed game/API ports while the client uses IPv4 loopback. Native installer/account/SCM checks retain product guards.
- The locked client's PLAY event comes from the protocol library's configuration transition. Its actual handlers require teleport, nonempty chunk data, chunk-batch completion and keepalive; explicit teleport/chunk/tick acknowledgements accompany the library's keepalive replies. The protocol's actual 1.21.9 inbound packet shapes and outgoing packet serialization were inspected. Source tests remain synthetic, as disclosed.
- Reconnect checks distinguish sent yaw from server-read yaw. Online backup checks include unchanged native identity, post-cursor save acknowledgements, the exact released lease, downloaded archive/record/header SHA256, every manifest member hash/size and actual bounded gzip NBT player rotation. Restore requires a fresh protected safety archive containing marker B and reconnect marker A. The report accurately limits the marker to persisted player view state.
- Artifacts use an explicit filename allowlist. Reports retain fixed phase/error codes, hashes, provenance and bounded assertions; raw HTTP/logs, credentials, DB and archives are excluded. Broader matrix, authenticated graphical clients and stable support remain explicitly unqualified.
- The isolated lockfile has75 installed packages with registry integrity pins and no declared install scripts. Reviewer recomputed the reported license counts (65MIT,4BSD-3-Clause,2Apache-2.0,2BSD-2-Clause,1ISC,1CC0-1.0), inspected the retained protocol notice, and confirmed the exact protocol and uuid override checks pass. Product/frontend dependency files are outside the packet. The worker's audit result was read; this review did not repeat an online audit.

Reviewer verification:

- `python3 -m unittest discover -s deploy/qualification -p game_api_test.py -v`: **8 passed**.
- `npm test --prefix deploy/qualification/game-client`: **6 passed**, including exact packet serialization and parent-context uuid imports/integrities, without a server connection.
- Python AST parsing of both new Python files, `bash -n` for the Linux wrapper, and `node --check` for both client files: **passed**.
- `git diff --check 574c505 7b10b8a`: **passed**.
- Isolated evaluation of the Linux refusal/cleanup fragments: reproduced **R1** above.
- `pwsh` and `actionlint` are unavailable locally. No native Windows parsing/execution or hosted game acceptance is claimed.

After both findings are corrected and independently re-reviewed, exact-source GitHub dispatch and evidence review remain required. This report does not close Task33 or qualify any game/host tuple.
