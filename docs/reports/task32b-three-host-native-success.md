# Task32B three-host installed updater run

[GitHub Actions run 39047676665](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39047676665)
completed at `e29221673d9523e7fd99f4fbf3bcc1aab313eecb`. The retained
scrubbed artifact summaries show the installed updater scenarios passing on
Ubuntu 22.04, Ubuntu 24.04, and Windows Server 2022. This report uses only the
retained result, assertion, activation, process, permission, and journal
summaries; it does not inspect build binaries.

## Scenarios and assertions

Each host's `api-assertions.txt` records:

```text
PASS installed updater genuine-success
PASS installed updater negative-health-rollback
PASS installed updater interrupted-candidate-recovery
PASS final native stop: no orphan manager
```

Across all three `acceptance-summary.json` files:

- `genuine-success` ends `healthy` at version `v0.0.2`, schema 20; the running
  process has `binding=true`, its image hash matches the expected prior/candidate
  hash, and the administrator session data is preserved.
- `negative-health-rollback` ends `rolled_back` at `v0.0.2`; rollback was
  attempted, the prior `v0.0.2` image hash is running and bound, and the
  administrator session data is preserved.
- `interrupted-candidate-recovery` records the interruption while phase was
  `candidate` and `rollback_attempted_before_recovery=false`; the post-recovery
  acceptance summary ends `rolled_back` at `v0.0.2`, with the prior image hash
  running and bound. Administrator session data remains preserved.
- Candidate `v0.0.3` uses a distinct hash from prior `v0.0.2`; activation state
  records a journal and `phase=rolled_back` after the final case.

Linux summaries report `cleanup_checked=true`, exit code 0, and the fixture
acquisition scope. The final permission summaries show the temporary
`/usr/local/lib/minecraft-ui`, `/usr/local/bin/minecraft-ui`,
`/var/lib/minecraft-ui-update`, `/var/lib/minecraft-ui-update/stages`, and
`/etc/minecraft-ui` paths absent after prefix restoration. Manager units are
inactive/dead; the recovery oneshot is inactive/dead with successful result.
The interrupted updater instance unit is failed with signal status 9, matching
the intentionally interrupted case. The API assertion independently reports no
orphan manager.

Windows reports `failed=false`, exit code 0, and `cleanup_checked=true`.
`native-update-diagnostics.json` has `capture_failed=false`, zero activation
failures, zero probe failures, and eight stopped-probe observations. Its process
summary binds the verified image and denies terminate, VM read/write/operation,
thread creation, duplicate-handle, DACL-write, and owner-write access. The
identity summary shows the manager and probe SIDs match and the updater runs as
LocalSystem. The final API assertion reports no orphan manager. The SCM summary
shows `MinecraftUI` and the manual probe service stopped; the installed
`MinecraftUIUpdater` service remains running as configured.

## Ubuntu 24.04 credential-transition follow-up

Ubuntu 24.04 now completes the installed-updater flow with systemd
`255.4-1ubuntu8.17`, util-linux `2.39.3-9ubuntu6.6`, and libpam-modules/libpam0g
`1.5.3-5ubuntu5.7`. The genuine-success process is bound to the expected image
and reports UID 999 (the installed manager service account in this fixture).
The bounded journal summary reports `native_pam_failure_counts={}` and no native
update failures. Together with the passing genuine activation, rollback,
interrupted recovery, and final-stop assertions, this closes the previous
Ubuntu 24.04 runuser credential-transition failure for this installed scenario.
The retained summaries do not contain raw PAM messages or credential data, and
they do not claim broader PAM or operating-system qualification.

## Limits

These are native hosted installed-updater fixture results, not Minecraft game,
world, backup, restore, or general-purpose manager qualification. They cover the
three named host images and the exercised update/recovery scenarios only. The
Linux permission summaries show owned prefix restoration; the Windows artifact
shows installed program and updater-service state, not a full machine reset.
No implementation ledger change or completion claim is made here.

Artifacts retained under
`/home/thim/.cache/dilla-tmp/task32b-updater-39047676665/`:

- `installed-updater-ubuntu-22.04-e29221673d9523e7fd99f4fbf3bcc1aab313eecb/`
- `installed-updater-ubuntu-24.04-e29221673d9523e7fd99f4fbf3bcc1aab313eecb/`
- `installed-updater-windows-2022-e29221673d9523e7fd99f4fbf3bcc1aab313eecb/`
