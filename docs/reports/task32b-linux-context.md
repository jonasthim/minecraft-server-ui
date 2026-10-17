# Task 32B Linux context diagnostics

The Ubuntu 24.04 run at commit `71b0c66452066f0b5b23416631581dd791edc0e2` ended at `runuser_uid:eperm`; the artifact did not retain the effective credential, namespace, or seccomp state. That observation still does not identify the source of `EPERM`. This change adds a bounded context packet for a later hosted rerun without changing the updater service policy.

## Packet and privacy boundary

Immediately before the `exec.Cmd.Start` for the fixed `runuser -u minecraft-ui -- <manager> update-schema` command, the Go goroutine pins to its current OS thread. It reads bounded `/proc/self/status`, `/proc/thread-self/status`, and `/proc/thread-self/uid_map`, then starts the child while still pinned. Linux keeps credentials per thread, and Go documents that a child started while the goroutine is locked to an OS thread inherits that thread's state ([credentials(7)](https://www.man7.org/linux/man-pages/man7/credentials.7.html), [os/exec](https://pkg.go.dev/os/exec)). The test also checks a harmless `/usr/bin/id -u` subprocess against the captured thread's root/nonroot classification.

The emitted `native-update-schema-context:` markers contain only fixed enums: process and calling-thread effective UID class; `CAP_SETUID` effective, permitted, and bounding membership; `NoNewPrivs`; seccomp mode; and initial/other/unknown UID map. A missing, oversized, duplicate, or malformed proc field maps to `unknown`; all four `Uid` values and all UID-map ranges are validated before classification. Raw proc contents, numeric capability masks, UID values, file paths, and LSM profile text are not emitted. The journal summarizer accepts only the finite marker grammar and counts those values. The harness queries loaded updater instances and the recovery unit. A bounded scrubber reduces the allowlisted `User`, `CapabilityBoundingSet`, `PrivateUsers`, `SystemCallFilter`, `RestrictSUIDSGID`, and `NoNewPrivileges` properties to fixed metadata; it omits profile/path values and raw capability/syscall lists. The unit file is unchanged.

## Source and verification

- [native_linux.go](/home/thim/Repositories/minecraft-server-ui/internal/selfupdate/native_linux.go:135) takes the context immediately before starting the schema child and emits it only when launch, child execution, output bounds, or JSON validation fails.
- [schema_context_linux.go](/home/thim/Repositories/minecraft-server-ui/internal/selfupdate/schema_context_linux.go:31) pins the launching goroutine, performs only the bounded proc reads, parses selected fields, and constructs fixed markers.
- [summarize-journal.py](/home/thim/Repositories/minecraft-server-ui/deploy/qualification/summarize-journal.py:88) accepts only the finite context marker grammar; [summarize_journal_test.py](/home/thim/Repositories/minecraft-server-ui/deploy/qualification/summarize_journal_test.py) exercises accepted and rejected values.
- [linux-updater.sh](/home/thim/Repositories/minecraft-server-ui/deploy/qualification/linux-updater.sh:80) queries loaded updater instances and the recovery unit, then retains `updater-context-properties.json` with the scrubbed journal summary.
- [summarize-updater-unit.py](/home/thim/Repositories/minecraft-server-ui/deploy/qualification/summarize-updater-unit.py) bounds input to 16 KiB and emits only fixed metadata; [summarize_unit_context_test.py](/home/thim/Repositories/minecraft-server-ui/deploy/qualification/summarize_unit_context_test.py) checks unit classification, redaction, duplicates, and input bounds.

Checks run locally:

- `go test ./internal/selfupdate -count=1` passed, including malformed/duplicate proc fields, bounded reads, initial-map parsing, the harmless child check, and existing schema-probe diagnostics.
- `python3 -m unittest deploy.qualification.summarize_journal_test` passed, including context marker allowlist tests.
- `python3 -m unittest deploy.qualification.summarize_unit_context_test` passed, including bounded raw-property input and finite metadata output.
- `bash -n deploy/qualification/linux-updater.sh` passed.
- `git diff --check` passed.

These local checks do not reproduce the hosted updater service sandbox. Native Linux qualification is still pending; retain `updater-context-properties.json` and `native_schema_context_counts` from the next failed probe. Interpret the packet together with its existing `runuser_uid` and child-entry markers. Do not change `NoNewPrivileges`, capabilities, UID mapping, system-call filters, or other unit policy until the same hosted execution context supplies evidence that identifies a specific cause.
