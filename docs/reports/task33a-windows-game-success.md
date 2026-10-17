# Windows real Minecraft workflow acceptance

Source: `329622eb04201b4f56fbed9fb506adde02ebb4a0`.
[GitHub Actions Windows job](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39052371244/job/117453372442).

The integrator reviewed `game-summary.json`, `result.json` and exact-source
metadata from the retained Windows artifact. The native Windows Server 2022
job and its shipping-service workflow pass. The manager SHA256 is
`069b5aae137c4da25218b3cacf74bc171d22f74f0a6a09ad613d82688e1c47d7`.

## Accepted scope

- Empty disposable state creates the first administrator and installs Vanilla
  1.21.9 using official hash-verified Adoptium Temurin 21.
- Six real headless protocol connections receive chunks, teleport, keepalive
  and chunk-batch traffic. The player yaw marker survives reconnects.
- The online backup verifies 82 files, preserves the JVM identity and captures
  marker 31.25. Save-off/save-on acknowledgments and a released lease before its
  deadline are retained.
- Explicit stopped restore creates a fresh 82-file safety backup containing
  later marker 121.25. Restoring the original backup brings marker 31.25 back.
- Restarting the Windows manager gracefully replaces the game process through
  configured autostart, with a new native identity and the restored marker.
- Deliberate stop remains stopped for the required 65-second observation.
- The workflow completes cleanup and retains a successful result.

## Limits and remaining failures

This qualifies this disposable Vanilla/version/host workflow. It does not
qualify Microsoft-authenticated or graphical clients, other loaders or host
families, the complete map, crash-retry injection, or the full installer.

The Ubuntu 24.04 job in this same run fails at deliberate stop after four
connections and a successful online backup. Its bounded evidence records
`stop_process_probe_failed`, no classified native errno, and a later matching
clean deliberate exit. Earlier complete Ubuntu runs remain valid evidence,
but the intermittent stop failure is unresolved. The combined run therefore
remains failed; it must not be reported as an all-platform pass.
