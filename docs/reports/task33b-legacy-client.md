# Task33b — legacy-floor game client harness

This packet extends the existing hosted real-game harness to the frozen
Vanilla tuples `1.12.2` with managed Temurin 8 and `1.21.9` with managed Temurin
21. The modern tuple remains enabled with its existing chunk-batch requirement.
This is harness coverage only; no game was launched and no legacy compatibility
claim is qualified by these local tests.

## Protocol behavior

The client CLI now requires exactly `--mode MODE --version VERSION`, with the
version restricted to those two tuples. It still accepts no endpoint argument
and uses the fixed loopback host and port. Its immutable per-version capability
map skips `player_loaded`, chunk-batch acknowledgements, and `tick_end` for
1.12.2. The legacy report marks all three capabilities `false` and omits the
`chunk_batches` count, so the absence is explicit and no packet evidence is
fabricated. It continues to require PLAY entry, a nonempty chunk, a server
position/teleport acknowledgement, and keep-alive observation. The locked
protocol plugin handles the matching keep-alive response. The legacy look packet
uses the 1.12.2 `onGround` layout; the modern flags layout and required batch
acknowledgement remain covered.

The serializer tests use the installed lockfile versions
`minecraft-protocol@1.68.0` and `minecraft-data@3.117.0`. The locked 1.12.2
protocol map identifies protocol 340 and serverbound `teleport_confirm`,
`keep_alive`, and `look`; it has no `player_loaded`, `chunk_batch_received`, or
`tick_end` packet names. The test serializes both legacy and modern packet
shapes without opening a connection.

## Tuple and evidence handling

The Python harness passes a frozen tuple configuration from the selected
Minecraft version through creation, preview, runtime evidence, client startup,
backup verification, and reporting. It checks the selected preview Java major
and requires the installed runtime manifest, runtime ID, Temurin major and
Adoptium release URL to agree with that tuple. The 1.12.2 payload omits
`simulation_distance`, which predates that setting. Summaries and wrapper
`result.json` files identify the selected version and the corresponding
`vanilla-{version}-real-headless-game` scope. The hosted Windows and Ubuntu
guards, EULA fixture, loopback/firewall restrictions, owned cleanup, and time
bounds were preserved; their wrappers accept only the two exact versions.

Runtime provenance also binds the selected ID's 64-hex archive suffix to the
manifest's `verified_sha256` and its matching Adoptium artifact checksum. The
positive Temurin 8 fixture uses one digest for all three; a mismatched ID suffix
is rejected.

The existing flow remains shared for both versions: connect and mark yaw
31.25, reconnect and verify it, create the online full backup while preserving
the process identity, inspect save-off/save-on lease evidence, mark yaw 121.25,
stop deliberately, take and verify the safety restore backup, restore, reconnect
at 31.25, restart the manager, and verify deliberate stop does not restart the
game. No workflow or product source was changed in this packet.

## Local verification

- `npm test --prefix deploy/qualification/game-client` — 7 tests passed,
  including exact tuple CLI parsing, both serializer layouts and legacy
  capability absence.
- `MINECRAFT_UI_TEST_PWSH=/home/thim/.cache/dilla-tmp/task33a-powershell-7.6.7/runtime/pwsh python3 -m unittest game_api_test.py` — 32 tests passed.
- The runtime ID/checksum mismatch regression failed before the binding check
  and passed after it.
- `bash -n deploy/qualification/linux-game.sh` — pass.
- `python3 -m py_compile deploy/qualification/game_api.py deploy/qualification/game_api_test.py` — pass.
- `git diff --check` on owned files — pass.

These are source and fixture tests. They do not establish a real 1.12.2 login,
PLAY/chunk/keep-alive flow, yaw persistence, online backup/save-on, restore,
reconnect, Java 8 server startup, native cleanup, or either host's qualification.
Those assertions remain required in the dispatched Ubuntu and Windows runs.
