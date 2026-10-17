# Task32B ambient capability result and native updater correction

[Run39047356269](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39047356269)
at e9616802be16c369d1324050317884ae4809431c completed the controlled
Ubuntu24.04 experiment and verified owned cleanup. The two fixed units differ
only by `AmbientCapabilities=CAP_SETUID`; the common sandbox is unchanged.

| Native observation, both ExecStartPre and ExecStart | Baseline | Control |
| --- | --- | --- |
| SETUID bounding | present | present |
| SETUID inheritable/permitted/effective/ambient | absent | present |
| SETGID permitted/effective | present | present |
| Direct setuid transition | EPERM | success |
| setgroups/setgid/setuid transition | EPERM at setuid | success |

Root inspected the retained bounded JSON. Both units started, both markers were
complete, and final cleanup_checked=true / exit_code=0. The older explicit
bounding-set experiment39046819697 did not restore the permitted/effective bit.

This result agrees with systemd v255's implementation: its seccomp preparation
keeps temporary privileges and subsequently drops SETUID unless it appears in
the ambient policy. The explicit root User setting participates in that path.
[systemd source](https://github.com/systemd/systemd/blob/v255/src/core/exec-invoke.c#L4431-L4490).

The shipped updater and recovery units now retain that one ambient capability
for their fixed manager-account probe. NoNewPrivileges, RestrictSUIDSGID and all
other sandbox settings remain enabled. The manager and JVM units are untouched.
No broad capability list or credential-transition retry is introduced.

The native experiment proves the direct transition under the copied sandbox;
it does not by itself prove util-linux/PAM, the full updater, recovery, or any
other operating-system tuple. The existing installed-updater matrix must pass
genuine activation, failed-health rollback and interrupted recovery after this
correction before Ubuntu24 updater qualification can be accepted.
