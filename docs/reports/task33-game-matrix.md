# Task 33 game matrix — 2026-11-15

Disposable GitHub-hosted Ubuntu 24.04 and Windows Server 2022 runners, the
`Installed Minecraft acceptance` workflow, explicit EULA acceptance for the
disposable servers, and the shipping manager built from the listed commit.
Each job installs the official server and hash-verified Adoptium Temurin
runtime through the manager, starts it, connects a real headless protocol
client (`minecraft-protocol@1.68.0`, `minecraft-data@3.117.0`), sets and
re-reads a player-view yaw marker, takes an acknowledged online backup without
replacing the game process, stops deliberately, restores with a fresh safety
backup, reconnects to the restored marker, restarts the manager, and finally
observes 65 seconds without an automatic restart after a deliberate stop.

## Results

Final matrix at `44c9b2b` (12 runs, 24 jobs), with reruns at `dcef441` for the
three jobs that failed there.

| Tuple | Loader / build | Java | Ubuntu 24.04 | Windows Server 2022 |
|---|---|---|---|---|
| Vanilla 1.12.2 (legacy layout) | — | 8 | Pass | Pass |
| Vanilla 1.16.5 (packing) | — | 8 | Pass | Pass |
| Vanilla 1.18.2 (negative Y) | — | 17 | Pass | Pass |
| Vanilla 1.20.5 (mixed codecs) | — | 21 | Pass | Pass |
| Vanilla 1.21.9 (MSMP) | — | 21 | Pass | Pass |
| Vanilla 26.1 (folders) | — | 25 | Pass | Fail once (readiness >60 s after manager restart), pass on rerun |
| Vanilla 26.3 (log changes), server-only | — | 25 | Pass | Pass |
| Paper 1.21.10 | build 130 (stable) | 21 | Pass (twice) | **Fails**: second stop forced (see open issues) |
| Fabric 1.21.9 | loader 0.20.1 | 21 | Pass | Pass |
| Forge 1.20.1 | 47.4.28 (recommended) | 17 | Fail once (stop probe), pass on rerun | Pass |
| Forge 1.12.2 | 14.23.5.2859 (recommended) | 8 | Pass | Pass |
| NeoForge 1.21.1 | 21.1.266 | 21 | Pass | Pass |

Runs: 44c9b2b — 43719809005 (1.12.2), 43719814445 (1.16.5), 43719820285
(1.18.2), 43719825607 (1.20.5), 43719831119 (1.21.9), 43719836524 (26.1),
43719841768 (26.3), 43719846689 (Paper), 43719852114 (Fabric), 43719857128
(Forge 1.20.1), 43719862725 (Forge 1.12.2), 43719867603 (NeoForge).
dcef441 reruns — 43721109553 (26.1), 43721115678 (Paper), 43721121485
(Forge 1.20.1).

Java majors are Mojang's official `javaVersion.majorVersion`; loader builds
come from official Paper (fill v3), Fabric meta, Forge promotions and NeoForge
Maven metadata. Paper 1.21.9 has no stable build, so Paper uses 1.21.10, which
shares protocol 773 with vanilla 1.21.9.

## Product defects found and fixed by these runs

- Managed Java 8 failed for every later official Temurin update: the JVM vendor
  alias was pinned to 8u504-b01 (now 8u512-b09).
- Legacy (1.12.2) online backups were refused because `level.dat` does not
  exist until the first save; the live layout check now runs after the
  acknowledged flush.
- Fabric 1.21.9 crashed on duplicate ASM (bundled 9.6 beside Fabric's 9.10.1);
  loader-provided libraries now win, as in Fabric's own launcher.
- Forge/NeoForge console lines carry a `[minecraft/<Logger>]` tag, so command
  acknowledgements, readiness, player history and game events never matched.
- 26.3 prefixes console command feedback with `System chat: `; accepted only by
  the exact feedback checks for 26.3+ (player history keeps 26.3 as a source gap).
- Paper's thread-less `[HH:MM:SS INFO]:` layout is accepted for Paper instances
  only.
- Installer launch validation spuriously required recovery when a concurrent
  `/proc/locks` read was torn (found while verifying this work; 4/48 loaded
  test iterations before, 0/48 after).

## Open issues

- **Paper on Windows, second stop**: after a manager restart relaunches Paper,
  a deliberate stop saves every world within about one second
  (`All RegionFile I/O tasks to complete`), but the JVM does not exit and is
  forced at the 90 s deadline. Reproduced twice; the first stop in the same
  run and all Ubuntu stops are clean. Data was saved before termination, but
  the stop is reported unclean. Not yet diagnosed.
- **Intermittent native stop/readiness**: one Forge 1.20.1 Ubuntu stop exited
  cleanly but the manager's process probe failed (`broker_invoke`), and one
  26.1 Windows manager restart took over 60 s to serve `/healthz`. Both passed
  on rerun; these continue the existing Task 33A intermittent-stop item.

## Limits

- 26.3 is server-only: no released protocol client supports it, so gameplay
  markers are replaced by the stopped server's restored `level.dat` matching
  the online archive manifest. Client gameplay on 26.3 is not qualified.
- The client is headless and offline-authenticated on loopback. The Paper tuple
  accepts chunks streamed without vanilla batch packets; NeoForge required the
  client to answer configuration `ping` with `pong`, as the vanilla client does.
- Paper's thread-less console layout is trusted for acknowledgements, history
  and events. Players cannot produce unprefixed INFO lines there, but plugin
  code can; plugins already run with full server authority.
- Hosts are GitHub's Ubuntu 24.04 and Windows Server 2022 images only. Ubuntu
  22.04, Windows 10/11 and Server 2019 are not covered by this matrix.
