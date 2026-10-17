# Task32B capability experiment: run 39046819697

## Evidence

Run `39046819697` at commit `cdf5dc2f2e35b6f364fdf37d2f3196ba21e6158c` succeeded on GitHub-hosted Ubuntu 24.04 x64 with systemd `255.4-1ubuntu8.17` and Go `1.27.0`. The retained artifacts are under `/home/thim/.cache/dilla-tmp/task32b-capability-39046819697/linux-updater-context-cdf5dc2f2e35b6f364fdf37d2f3196ba21e6158c/`.

Both the baseline probe unit and the control unit started successfully and produced complete `ExecStartPre` and `ExecStart` records. Across all four records, the relevant context was identical:

- Effective UID/GID were root; the UID map was `initial`.
- `CAP_SETGID` was permitted, effective, and in the bounding set. `CAP_SETUID` was in the bounding set but absent from both permitted and effective sets. Neither capability was ambient or inheritable.
- `NoNewPrivileges` was enabled, seccomp was in filter mode, and the reported securebits were all unset.
- The direct UID-only transition and the harmless `setgroups` → `setgid` → `setuid` sequence both ended at `setuid_eperm`.
- Python, its interpreter, and `runuser` were regular root-owned files without setuid/setgid bits or file capabilities. This rules out a file-mode/capability difference for those test executables; it does not model every detail of util-linux's PAM behavior.

The baseline and control unit property summaries also agree: `User=root`, bounding set includes named `CAP_SETUID`, `PrivateUsers=no`, syscall filter configured in denylist mode, no literal `setuid` filter token detected, `RestrictSUIDSGID=yes`, and `NoNewPrivileges=yes`. The control adds `CapabilityBoundingSet=CAP_SETUID CAP_SETGID`; it does not change the observed process capability sets or transition result. A bounding-set allowance is not evidence that the capability is currently permitted/effective; systemd describes that setting as bounding-set control, while `AmbientCapabilities=` is the separate mechanism for adding ambient capabilities. [systemd v255 service execution documentation](https://github.com/systemd/systemd-stable/blob/v255-stable/man/systemd.exec.xml)

The run's `result.json` reports `exit_code: 0` and `cleanup_checked: true`. The probe script checks and removes its fixed helper, unit files, scratch state directories, and units; successful exit is evidence that its cleanup checks passed. The updater binary metadata says it was not root-owned, as expected for the workspace build artifact; the script only inspected it and did not execute it.

## Interpretation and limits

This experiment reproduces the failed credential transition in a disposable systemd unit with the updater unit's visible hardening. The evidence localizes the immediate `setuid(2)` failure to a service process that has no permitted/effective `CAP_SETUID`; the same failure occurs before/after unit start and with/without the explicit bounding-set line. It therefore does not support changing `NoNewPrivileges`, `RestrictSUIDSGID`, or `CapabilityBoundingSet` alone as a fix.

The artifact does **not** prove that missing capability is the only denial. Seccomp is active, and the summarized `setuid_filter_named=no` only means no literal `setuid` or `@setuid` token was found; it cannot rule out a syscall group that includes `setuid`. The raw filter list and systemd manager's own capability state were intentionally not retained. The probe uses direct harmless syscalls rather than starting the Minecraft manager, and does not establish that the installed updater succeeds.

## Follow-up control prepared

The probe script now keeps the baseline unit unchanged and changes only the control unit to add `AmbientCapabilities=CAP_SETUID`; the previous explicit bounding-set control line is removed. Both units retain the same user, sandbox, `NoNewPrivileges`, and `RestrictSUIDSGID` settings. The finite output tags the control as `ambient_cap_setuid`, and the pure fixture requires the control policy to contain exactly that single line while the baseline policy remains empty. This keeps the next comparison focused on whether systemd can provide the missing capability in this hosted service context.

There is a plausible systemd v255 source explanation: `exec-invoke.c` enables `keep_seccomp_privileges` for an explicit root user with seccomp, then deliberately drops `CAP_SETUID` unless `AmbientCapabilities` includes it ([systemd v255 source, lines 4431–4490](https://github.com/systemd/systemd/blob/v255/src/core/exec-invoke.c#L4431-L4490)). This matches the probe's `CAP_SETUID` bounding bit with absent permitted/effective bits. It remains an inference until this native control run shows that adding only the ambient capability restores `CAP_SETUID` and the harmless transition.

If the new control still lacks the capability or still fails at `setuid`, retain the policy and investigate the inherited/default seccomp filter in a separate controlled experiment. Do not change the shipping unit based on this diagnostic alone.
