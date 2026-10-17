# First complete Linux Minecraft acceptance

[Run39042942229](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39042942229),
Ubuntu24.04 job117425924630, exact source
`3888b0675fee09d37e98a0c76b0698ced895b844`: PASS. Root inspected the retained
game-summary.json and result.json; both report complete success. The Windows
job failed before installation at its firewall rule, so the overall run failed.

## Exact scope

- Shipping manager SHA256
  `cc7826763894434b65127c9887213768928d9e00911e66ea4ff387035a4b3bee`.
- Official Vanilla1.21.9 jar SHA1
  `11e54c2081420a4d49db3007e66c80a22579ff2a`,58638725bytes.
- Official verified Adoptium Temurin21.0.12.1+1-LTS archive SHA256
  `ce79869e1307ed8ee1e2baa86a412b1eb5b75d10a01006d788a6f968bcfaee94`.
- Fresh disposable canonical state, explicitly accepted EULA, creative peaceful
  seed33, offline authentication restricted to loopback by the harness firewall.
  Real headless protocol client `minecraft-protocol@1.68.0`; no graphical or
  Microsoft-authenticated client is implied.

## Reviewed milestones

Six real PLAY connections each received49chunks, teleport and keepalive traffic.
Player view yaw progressed0→31.25, reconnect31.25, then121.25 and reconnect121.25.
The online backup captured82verified files and yaw31.25 while the exact original
JVM identity remained unchanged. Both save-off/save-on were acknowledged and
the lease was released before its deadline. Backup SHA256:
`32b784c68573077df87badf29a8317c0587aaa5e780d016a9c7a43c631e69df0`.

Explicit stopped restore first captured an82file safety backup with the newer
yaw121.25, SHA256
`05930e65852ab90113f28e1e24307d425cd89f548192c5125166135a47db5484`.
The restarted client then observed restored yaw31.25. Manager restart preserved
the running Linux game's identity and restored marker; another connection
observed31.25. Deliberate stop remained stopped for65seconds.

Native cleanup completed; the borrowed hosted prefix mode was0777→0755→0777
on the same inode, with owned application/helper/unit paths absent afterward.
The user's local preview was not used or changed.

## Remaining boundaries

This is one successful execution of the initial tuple. The earlier run's
stopping_process failure has not been reproduced or explained; this success is
not evidence that a deliberate-stop code defect was fixed. Retain the new
bounded diagnostics and investigate any recurrence.

Windows, other versions/flavors/hosts, full map/addon/modpack acceptance, crash
retry injection, installer upgrades and stable publication remain separate
requirements. This milestone does not complete Task33 or the whole product.
