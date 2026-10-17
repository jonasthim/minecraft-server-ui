# Task32B native run analysis at 71b0c66

Run [39041683747](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39041683747) tested commit `71b0c66452066f0b5b23416631581dd791edc0e2` on 2026-10-17. The Ubuntu 22.04 exercise passed. Ubuntu 24.04 job `117422813122` and Windows 2022 job `117422813108` failed; their build-input jobs passed. The run was downloaded read-only to `/home/thim/.cache/dilla-tmp/updater-39041683747-download/`.

Only retained fixed summaries and JSON diagnostics were read. The Windows job log was processed in memory through an exact source-authored message allowlist; raw lines were not displayed. Raw journal records and `api-assertions.txt` were not read from disk. No local service, account, firewall, game, or preview instance was operated. Preview port `12826` was left untouched.

## Ubuntu 24.04: observed failure boundary

Job `117422813122` failed in the installed activation step. Its `initial-update-status.json` reports current `v0.0.1`, schema 20, configured trust, and activation available. `journal-summary.json` contains:

```json
"native_update_failure_counts": {"schema_probe": 1},
"native_schema_probe_counts": {"runuser_uid:eperm": 1, "command:exit:1": 1},
"native_pam_failure_counts": {}
```

`activation-state-summary.json` says phase `aborted`, `rollback_attempted: false`, prior `v0.0.1` and candidate `v0.0.2`, both schema 20. The updater and recovery units were still activating when the artifact captured their state, so this artifact does not establish completed recovery.

This is more specific than the previous run39038823522 report: fixed classification now identifies the exact failed boundary as `runuser` being unable to set the user ID, with `EPERM`, followed by exit 1. There is no child-entry marker, so `/usr/local/bin/minecraft-ui update-schema` did not reach its authored probe boundary. The result does not identify which kernel or service policy returned `EPERM`.

Source linkage:

- [native_linux.go](/home/thim/Repositories/minecraft-server-ui/internal/selfupdate/native_linux.go:135) constructs the fixed `/usr/sbin/runuser -u minecraft-ui -- <manager> update-schema` call; lines 197–227 classify only exact known runuser text into `runuser_uid:eperm`.
- [native.go](/home/thim/Repositories/minecraft-server-ui/internal/selfupdate/native.go:329) labels a returned probe error `schema_probe` and aborts before candidate copying or executable replacement.
- The installed updater unit source [minecraft-ui-update@.service](/home/thim/Repositories/minecraft-server-ui/deploy/linux/minecraft-ui-update@.service:6) runs as root and sets `RestrictSUIDSGID=true` and `NoNewPrivileges=true`. These settings are present in the source unit used by this commit; the artifact does not retain effective capabilities or the active seccomp state, so it cannot attribute this `EPERM` to a particular directive. The [Linux kernel's `no_new_privs` documentation](https://docs.kernel.org/userspace-api/no_new_privs.html) says an appropriately privileged process can still call `setuid(2)`. [systemd's `RestrictSUIDSGID` documentation](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html#RestrictSUIDSGID=) describes blocking creation of SUID/SGID file modes. Neither source alone explains this observed UID-transition denial.
- The artifact's fixed `input-summary.txt` records systemd `255.4-1ubuntu8.17` and util-linux `2.39.3-9ubuntu6.6`, tying the observation to those package versions.

### Follow-up source review and next step for Ubuntu

The observed `runuser_uid:eperm` is specifically util-linux `setuid(pw_uid)` failing: [util-linux v2.39.3 `su-common.c`](https://github.com/util-linux/util-linux/blob/v2.39.3/login-utils/su-common.c) calls `setgid()` and then `setuid()` in `change_identity()`, reporting `cannot set user id` for the latter. This is the package version recorded by the artifact.

The checked-in updater unit has no `CapabilityBoundingSet`, `PrivateUsers`, `SystemCallFilter`, or `AppArmorProfile` directive; it explicitly has root `User=` and the `NoNewPrivileges` and `RestrictSUIDSGID` settings shown above. The upstream [Linux kernel `no_new_privs` documentation](https://docs.kernel.org/userspace-api/no_new_privs.html) states that an appropriately privileged task can still call `setuid(2)`. The [systemd v255 `RestrictSUIDSGID` documentation](https://github.com/systemd/systemd-stable/blob/v255-stable/man/systemd.exec.xml) defines it as denying creation of SUID/SGID mode bits on files; [systemd v255 seccomp source](https://github.com/systemd/systemd-stable/blob/v255-stable/src/shared/seccomp-util.c) implements that by inspecting file-mode arguments. That directive does not explain a direct `setuid()` denial. The distinct `@setuid` system-call filter group does include `setuid()`, but no `SystemCallFilter=` appears in the checked-in unit. The artifact does not include the effective loaded properties, current capability set, UID map, or LSM state, so a runner-level or effective-policy cause remains unidentified.

The project call is `exec.CommandContext` in [native_linux.go](/home/thim/Repositories/minecraft-server-ui/internal/selfupdate/native_linux.go:140), with no `SysProcAttr.Credential`; there is no `setuid`, `setgroups`, or credential-changing Go call in that file. The UID transition belongs to the separate runuser process. A harmless local Go child fixture returned `go_child_inherited_euid=true`. The local account runs as UID/GID 1000 and has no root-native updater sandbox, so this fixture does not reproduce the hosted denial and cannot qualify the native service.

The bounded packet is implemented in [schema_context_linux.go](/home/thim/Repositories/minecraft-server-ui/internal/selfupdate/schema_context_linux.go:31) and the schema probe emits it only on failure. It records process-leader and calling-thread effective UID class, `CAP_SETUID` effective/permitted/bounding bit booleans, `NoNewPrivs`, seccomp mode, and whether the calling thread's UID map exactly matches the initial map. Reads are limited to `/proc/self/status`, `/proc/thread-self/status`, and `/proc/thread-self/uid_map`; every value is reduced to a fixed enum before diagnostics. The launch goroutine is pinned to one OS thread through snapshot and `exec.Cmd.Start`, because Linux credentials are per-thread and Go `os/exec` inherits the locked thread's state ([Linux credentials(7)](https://www.man7.org/linux/man-pages/man7/credentials.7.html), [Go os/exec](https://pkg.go.dev/os/exec)). The qualification harness now queries loaded updater instances and the recovery unit, then retains only fixed metadata in `updater-context-properties.json`; raw systemd property values pass through the bounded scrubber, which omits `AppArmorProfile` and path-bearing values. Nothing changes updater-unit policy. A harmless local `id -u` child fixture verifies the captured calling-thread effective-UID class against the actual child; this is not native service evidence. See [task32b-linux-context.md](/home/thim/Repositories/minecraft-server-ui/docs/reports/task32b-linux-context.md) for verification and hosted follow-up. No native rerun has been dispatched, so the hosted cause remains undetermined.

## Windows 2022: observed failure boundary

Job `117422813108` passed native installation and the no-argument process-access qualifier. Its `process-access-summary.json` records all ten expected booleans as true: limited query access and image binding succeeded, while terminate, VM read/write/operation, duplicate handle, create thread, write DACL, and write owner were denied.

The fixed `result.json` says `failed: true`, `cleanup_checked: true`, and `last_stage: "installed API activation, negative health and interruption"`. The safe activation snapshot says `aborted`, `rollback_attempted: false`, prior `v0.0.1`, candidate `v0.0.2`. At capture, SCM summaries show manager and updater running and the probe stopped; the identity summary shows LocalSystem for the updater and the dedicated account SID for the manager. The initial update status was available and trust configured.

The downloadable Windows artifact does not contain `api-assertions.txt`. The failed GitHub job log was therefore filtered in memory using the exact authored `RuntimeError` messages extracted from `updater_api.py` and `manager_api.py`; no raw log line was emitted. The sole matching failure was `unexpected native updater terminal state`. This proves the first apply request passed its expected HTTP 202 check and `wait_terminal` observed the matching journal reach terminal `aborted`, where the script expected `healthy`. It rules out a failed apply HTTP request as the stopping point. It still does not identify the internal worker stage that wrote `aborted`.

Source linkage:

- [windows-updater.ps1](/home/thim/Repositories/minecraft-server-ui/deploy/qualification/windows-updater.ps1:177) records service identity and process-access evidence before setting the coarse stage at line 198 and invoking the API acceptance script.
- [updater_api.py](/home/thim/Repositories/minecraft-server-ui/deploy/qualification/updater_api.py:308) applies the genuine update, then waits for terminal phase `healthy`; the API returned before that wait, and the matching journal was instead `aborted`. It writes `acceptance-summary.json` only after successful preservation assertions at lines 298–304. No such summary was retained, so the run did not complete the first success checkpoint.
- The common worker calls optional `ActivationFailure(stage)` only when its driver implements it in [native.go](/home/thim/Repositories/minecraft-server-ui/internal/selfupdate/native.go:148). Linux implements this sink, but `windowsDriver` in [native_windows.go](/home/thim/Repositories/minecraft-server-ui/internal/selfupdate/native_windows.go:553) does not. The Windows updater starts activation asynchronously and discards its returned error at [native_windows.go](/home/thim/Repositories/minecraft-server-ui/internal/selfupdate/native_windows.go:1205). This leaves a source-backed diagnostics gap for the exact internal stage.

### Next step for Windows

Keep the process DACL grant: its dedicated qualification passed in this run. Add a small fixed Windows activation-stage diagnostic using the common worker's existing allowlisted `stage` value, exposed through a protected fixed record or Windows service diagnostic channel. Have the qualifier retain only that stage and the terminal phase/rollback flag. This locates the failing operation without API bodies, credentials, SID values, or raw error text. Make a behavior change only after that stage identifies the failed native operation.

## Conclusion

The Ubuntu failure is localized to util-linux `setuid()` returning `EPERM`; the checked-in unit's visible `NoNewPrivileges` and `RestrictSUIDSGID` settings do not explain it, and effective runner policy is not retained. The Windows failure is localized to the first activation: the request is accepted, then the journal aborts before success; a Windows activation-stage sink is missing. No behavior fix is justified yet. The next work is to add the bounded diagnostics above and rerun native qualification on hosted disposable runners.
