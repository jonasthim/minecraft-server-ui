# Task 32B Linux capability probe

## Evidence and question

Run [39043683105](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39043683105), Ubuntu 24.04 job `117428451958`, recorded one `runuser_uid:eperm` during updater activation. The retained context in [the run analysis](task32b-native-92d0acd-analysis.md) reports root euid, `CAP_SETUID` permitted/effective absent and bounding present, `NoNewPrivileges=1`, seccomp mode 2, and the initial UID map. The run did not retain the effective capability list, syscall filter contents, or executable file capability metadata. That evidence localizes the failed operation but not the source of the missing capability.

The updater and recovery source units both set `User=root`, `Group=root`, `RestrictSUIDSGID=true`, and `NoNewPrivileges=true`. They do not declare `CapabilityBoundingSet=`, `PrivateUsers=`, or `SystemCallFilter=`. The proposed probe runs only on a fresh hosted Ubuntu 24.04 runner and does not invoke the installed updater.

## Preflight follow-up

Hosted run `39045804905`, job `117434166142`, stopped before creating probe
files. Its log contains only the combined message `Fixed probe unit is
installed/loaded or systemd preflight failed.` The available artifact contains
runner metadata but no `result.json` from the probe script. It therefore does
not distinguish an installed matching unit, a loaded matching unit, or a
failed `systemctl` query.

The official [systemctl reference](https://www.freedesktop.org/software/systemd/man/latest/systemctl.html)
describes `list-unit-files` as listing installed unit files and `list-units`
as listing loaded units. A source check against systemd v255's
[`systemctl-list-unit-files.c`](https://raw.githubusercontent.com/systemd/systemd/v255/src/systemctl/systemctl-list-unit-files.c)
shows the command returns `-ENOENT` when its filtered result count is zero.
That makes an exact-name query unsuitable for proving expected absence. The
hosted log does not identify which query returned nonzero, but on a fresh
runner the absent fixed unit files are expected, so this is a concrete
preflight defect consistent with the failure.

The preflight now supplies both the fixed target name and the known
`systemd-journald.service` sentinel to each systemctl listing. It requires the
sentinel to appear exactly once in each successful query, then counts only the
exact target name. This keeps installed-unit and loaded-transient refusal
checks while allowing an absent target to be distinguished from a failed
query. The exact two-name result strings are rejected above 16 KiB total or
4 KiB per line, parsed into fixed counts, and never retained raw. On refusal, `result.json` contains only
the fixed status, both numeric exit statuses, target counts, and sentinel
counts; the existing workflow already uploads that file. A missing sentinel,
unexpected row, oversize output, or command error still fails closed.

Pure fixtures verify the three distinct outcomes (`absent`, `unit_present`,
`query_failed`), including a simulated `-ENOENT` for an empty exact filter and
successful sentinel-backed empty-target results. They check exit/count
metadata for failures from either query. The hosted run's exact query statuses
remain unavailable, but the source-level no-match behavior that made the
preflight reject expected absence is established.

## Probe design

[linux-updater-context.sh](../../deploy/qualification/linux-updater-context.sh) installs two uniquely named, temporary oneshot units that copy the updater unit's sandbox directives and run a fixed Python probe in both `ExecStartPre` and `ExecStart`. Each marker contains finite enums only. The probe captures the calling thread's effective UID/GID state, capability membership for `CAP_SETUID` and `CAP_SETGID` across the five Linux capability sets, `NoNewPrivileges`, seccomp mode, securebits, bounded UID-map classification, and safe metadata categories for the updater binary, probe, Python interpreter, and `runuser` executable. It also tries two short-lived child transitions to the pre-existing `nobody` account: `setuid` only, then the `setgroups`/`setgid`/`setuid` sequence used by `runuser`. The child is killed and reaped after a fixed deadline; its pipe EOF is not treated as process completion.

The baseline copies the production unit directives exactly. The control adds one `CapabilityBoundingSet=CAP_SETUID CAP_SETGID` assignment. Per systemd v255 documentation, this sets the control's bounding set to those two named capabilities and affects their permitted, effective, and inheritable sets as well; it is not merely an additive grant. This is a deliberately narrow comparison to test whether the two capabilities needed by the child transition are sufficient in this sandbox. Both cases keep `RestrictSUIDSGID=true`, `NoNewPrivileges=true`, and every other copied sandbox directive. This probe does not justify changing production unit policy.

The v255 `CapabilityBoundingSet=` documentation describes named lists, complement lists, and the effects on process capability sets. The same documentation says `NoNewPrivileges=true` prevents privilege gain through `execve()` of set-ID or file-capability executables. The kernel documentation also says this flag does not prevent a suitably privileged process from making a direct `setuid(2)` call. Therefore NNP alone does not explain the observed direct-call `EPERM`; the probe records the live capability state and direct child result instead of inferring a cause from NNP or seccomp mode. [systemd v255 `systemd.exec`](https://github.com/systemd/systemd-stable/blob/v255-stable/man/systemd.exec.xml), [kernel No New Privileges documentation](https://docs.kernel.org/userspace-api/no_new_privs.html), [Linux `capabilities(7)`](https://man7.org/linux/man-pages/man7/capabilities.7.html).

The property summarizer accepts systemd's lowercase or uppercase capability names, applies a leading `~` complement only when its syntax is unambiguous, and emits only fixed unit/property categories. For `SystemCallFilter`, it records the filter mode and whether `setuid`/`@setuid` appears literally. It does not infer syscall access from absence of a literal token because groups such as `@system-service` can include unnamed syscalls. Duplicate, malformed, oversized, or unrecognized values become `unknown`.

The child keeps its close-on-exec error pipe open through `execve`: success closes the descriptor at exec, while failure can still report a fixed errno category and exits nonzero. A pure fixture injects an `execve` failure and verifies both the pipe payload and nonzero result. This fixture does not run the credential transition.

## Guard and cleanup boundary

The script requires a GitHub-hosted Ubuntu 24.04 runner running as root, an ordinary updater binary inside the workspace, an empty report directory, no installed manager executable or state directories, no `minecraft-ui` account/group, and no pre-existing fixed probe paths or units. Before creating files, it checks both installed unit files and loaded units by exact fixed name; failure of either systemctl query fails closed. Helper and unit files are exclusively created, and their device/inode/owner identities are recorded before content is written. Cleanup checks loaded state and stops a probe unit only when systemd confirms its `FragmentPath` is the exact unit file created by this script. It removes only files whose recorded identity still matches. If ownership cannot be confirmed, cleanup leaves unit files in place and reports failure rather than stopping an unrelated transient unit. The script creates only empty, mode-0700 state directories needed to reproduce `ReadWritePaths`; it does not create accounts or touch the firewall or game. Trap cleanup removes directories only when their recorded device/inode/owner identity still matches. Cleanup status is retained in a fixed result object.

No source unit policy or native host was changed for this packet. A hosted run is still required to learn whether the control changes the observed capability state and transition result.

## Local checks

The deterministic local checks exercise the proc-status parser, malformed/duplicate fields, UID-map whitespace/range handling, fixed marker schema, unit-property redaction, capability complement syntax, syscall filter mode, conservative unknown handling, and unit preflight responses for installed units, loaded transient units, and either systemctl query failing. They do not run the credential transition or create local services/accounts.

```text
bash -n deploy/qualification/linux-updater-context.sh
bash deploy/qualification/linux-updater-context.sh --pure-fixture
python3 -m unittest deploy/qualification/summarize_unit_context_test.py
```

Native capability and transition results remain pending a hosted GitHub Actions run.
