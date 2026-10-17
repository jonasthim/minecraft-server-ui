# Task 32B native run analysis at `92d0acd`

Run [39043683105](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39043683105) tested commit `92d0acd2a332bc1ad57c3e01526364ac458a81de` on 2026-10-17. Build-input jobs passed. The Ubuntu 22.04 installed-updater exercise passed; Ubuntu 24.04 and Windows Server 2022 failed. I downloaded the three installed-updater artifacts read-only to `/home/thim/.cache/dilla-tmp/updater-39043683105-download/`. I inspected only the retained summaries and fixed diagnostic JSON, not raw journals, API assertion text, or native event payloads beyond the scrubbed diagnostic output. No local service, account, firewall, game, or preview instance was operated.

## Ubuntu 24.04: missing `CAP_SETUID` in the updater context

Job `117428451958` failed during installed activation. The initial status reported version `v0.0.1`, schema 20, configured trust, and activation available. The retained journal summary reports one `runuser_uid:eperm` and one `command:exit:1`; the activation snapshot is `aborted`, with no rollback attempted. The updater instance unit is failed with exit status 1, while the separate recovery unit is inactive with success status.

The new context packet identifies the immediate execution constraint that the previous run lacked:

- Both the updater process and the pinned calling thread are classified `euid:root`.
- In both snapshots, `cap_setuid_effective:no` and `cap_setuid_permitted:no`, while `cap_setuid_bounding:yes`.
- Both report `no_new_privs:1`, seccomp mode `2`, and the initial UID map.
- The harmless child classification matched the captured thread context in the local diagnostic test, but that fixture does not reproduce the hosted service.

This makes the observed `runuser` failure consistent with a caller that lacks the capability needed to change to the manager UID. Linux documents that `CAP_SETUID` permits arbitrary changes to a process's UIDs and that an unprivileged UID change can fail with `EPERM` ([setuid(2)](https://man7.org/linux/man-pages/man2/setuid.2.html), [capabilities(7)](https://man7.org/linux/man-pages/man7/capabilities.7.html)). The process's `cap_setuid_bounding:yes` shows the capability remains in its bounding set; the immediate failure is that it is absent from the permitted and effective sets. The updater unit source has no explicit `CapabilityBoundingSet=` or `SystemCallFilter=` directive. The scrubbed runtime property report labels both properties `configured`, but cannot parse their named contents, so it cannot show whether the active service manager adds policy beyond the unit source. `seccomp:2` alone does not establish that the `setuid` syscall was filtered.

The artifact does not retain `security.capability` metadata or a `getcap` result for the installed updater executable, so file-capability presence cannot be determined from this run. The observed `no_new_privs:1` is material: the kernel documents that `execve()` under this flag does not grant privileges through file capabilities or set-ID mode bits ([No New Privileges Flag](https://docs.kernel.org/userspace-api/no_new_privs.html)). Therefore an executable file capability cannot explain gaining `CAP_SETUID` at the updater's exec boundary in this context. The report still does not establish where the permitted capability was lost before the `runuser` child was started.

Ubuntu 22.04 passed genuine activation, negative-health rollback, and interrupted-candidate recovery with the same product unit source. This narrows the observed difference to the hosted execution environment or distribution-specific behavior, but does not identify which. No capability, syscall-filter, or updater-unit policy change is justified by this run alone. Before changing policy, the next useful evidence is a bounded, exact classification of effective capability permitted/effective/bounding membership and whether the active syscall filter names `setuid`, with the unit identity and service context retained as already done.

## Windows Server 2022: `configured_sid` lookup returns 1332

Job `117428452151` passed installation checks and reached the first installed API activation. Its initial updater status was available with trust configured, and the activation journal ended `aborted` without rollback. The new bounded Event Log packet records:

```json
[
  {"event_id":1001,"activation_stage":"schema_probe"},
  {"event_id":1002,"probe_step":"configured_sid","win32_code":1332}
]
```

The error code is `ERROR_NONE_MAPPED`: Windows did not map the supplied account name to a SID ([Microsoft system error codes 1300–1699](https://learn.microsoft.com/en-us/windows/win32/debug/system-error-codes--1300-1699-)). In [native_windows.go](/home/thim/Repositories/minecraft-server-ui/internal/selfupdate/native_windows.go:637), this diagnostic is emitted when `windows.LookupSID("", configuration.ServiceStartName)` returns an error. A SID mismatch after a successful lookup would emit an ordinary validation error with code `unknown`, so the numeric 1332 specifically localizes this failure to account-name resolution.

The harness's separately retained identity summary reports that the configured manager SID and probe-service SID match. Its pre-activation assertion resolves the SCM account name to the expected SID, including explicit handling for the `.`-qualified local-account spelling in [windows-updater-support.ps1](/home/thim/Repositories/minecraft-server-ui/deploy/qualification/windows-updater-support.ps1:62). That establishes the installed service identity is correct through the harness resolver, while the Go probe's direct `LookupSID` input is not resolvable in its current form. The exact `ServiceStartName` value was intentionally not retained, so the run does not prove it uses that spelling. A narrow candidate correction is to apply the same strictly local `.`-qualified to machine-qualified-name normalization before the Go lookup, preserving exact SID comparison and refusing other forms; first confirm the SCM representation using a finite spelling-class diagnostic or a hosted rerun of the normalization path. Do not accept a name-only match or change the expected SID.

The Event Log summary reports one event each for SCM IDs 7000 and 7026, but the retained summary contains only counts; it cannot establish that these correspond to this probe failure. The diagnostic packet retains no raw event message, account name, SID, or exception text. The harness records `cleanup_checked: true` after failure.

## Scope and next step

This run improves diagnosis without qualifying the failed platforms. Ubuntu 24.04 now points to unavailable `CAP_SETUID` in the actual updater/thread capability sets, but the source of that restricted set remains outside the captured policy detail. Windows now proves the schema probe fails while mapping the configured service account name, with 1332 rather than a SID equality failure. Root may assign targeted source work using these boundaries; no implementation or runner-policy change is included in this analysis.
