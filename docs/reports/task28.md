# Task 28 — installed manager smoke

Status: hosted Ubuntu installed-manager checks passed; Windows harness identity
assertion correction awaiting review and another hosted run.
Base: `6ffb25b`. Scope: source-built installed manager on disposable GitHub
Ubuntu 22.04, Ubuntu 24.04 and Windows Server 2022 hosts. No game or Java
process is launched and no EULA, client, production signing or release action
is included. This packet does not qualify Debian, other Windows families,
game isolation/lifetime, backup/restore, updater activation or stable release.

## Delivered

- `deploy/qualification/linux-manager.sh` uses the shipping installer with an
  absolute locally built manager, canonical directories and a fresh service
  account. It refuses existing state. It tests actual systemd identity, private
  data/database modes, root-owned binaries/helper/sudoers and process cleanup.
- `unit-probe.sh` executes as `ExecStartPost` inside the actual installed manager
  unit, without privilege prefixes or alternate sandbox settings. It invokes
  the exact shipping `sudo -n unitctl enable/disable delivery-smoke` path,
  asserts exit codes, tests invalid verb/ID/reserved ID/argument count and
  records its `/proc/self/status` NoNewPrivs. It starts no game. The temporary
  drop-in is removed before manager lifecycle checks.
- The optional fixed-base observation first installs the pre-fix manager unit
  from `6ffb25b`, records actual manager/probe NoNewPrivs and helper statuses,
  then replaces only the unit with the candidate. Baseline output is explicitly
  observational; candidate helper or NoNewPrivs failures always fail the job.
- `windows-manager.ps1` creates a random unprivileged local account, grants and
  later revokes only its service-logon right through LSA, and supplies a
  memory-only PSCredential to the shipping installer. It checks real SCM
  configuration/process ownership, Program Files RX without write, private data
  Modify ACLs and inherited database ACLs. It removes its service, files and
  account in `finally`. Its fixed assertion messages can be emitted safely;
  external error records are never serialized.
- `manager_api.py` checks JSON health and the real `needs_setup` endpoint,
  creates an admin through the public setup API, verifies authentication,
  stop/start and restart, retained admin/session/password login, changed PID,
  listener closure and absence of orphan installed manager processes. Random
  application credentials and cookies stay in memory.
- The integrated workflow is `.github/workflows/installed-manager.yml`, with
  its original draft retained under the completion plan's scratch directory.
  Integrator owns workflow wiring and hosted dispatch. It is
  manual, has three native runners, exact source SHA/hash/version reports,
  bounded commands and jobs, and always uploads scrubbed allowlisted reports.
  Raw journals/events, database, application state, environments and credentials
  are excluded. Journal/event diagnostics contain only fixed names/IDs/counts.

The workflow builds the embedded frontend and native manager directly from the
checked out commit. It does not run the opt-in `TestSystemdHostLifecycle` and
does not install updater/probe services in this first packet. Full installer
lifecycle and update bootstrap/activation remain Tasks 30/32.

## Manager unit correction

The systemd 249 implementation of `context_has_no_new_privileges` enables NNP
when an unprivileged service uses seccomp-backed kernel protection or
RestrictSUIDSGID. This makes the former manager unit's promise that omitting
explicit NoNewPrivileges preserves setuid sudo incorrect.
Primary evidence: [systemd v249 execute.c, lines 1425–1449](https://github.com/systemd/systemd/blob/v249/src/core/execute.c#L1425-L1449)
and [Ubuntu Jammy systemd.exec documentation](https://manpages.ubuntu.com/manpages/jammy/man5/systemd.exec.5.html).

The candidate removes only `ProtectKernelTunables`, `ProtectKernelModules`,
`ProtectKernelLogs` and `RestrictSUIDSGID` from the manager unit. It retains its
unprivileged UID, strict filesystem protection, private home/tmp, protected
cgroups and private data mode. Mount-only read-only `/proc/sys` and `/sys` plus
inaccessible kernel log paths preserve relevant filesystem restrictions without
requiring seccomp NNP. This trades those manager-only syscall filters for the
existing bounded setuid helper. It adds no capability and changes no helper,
sudo grant or game unit; game namespace/seccomp/NNP restrictions are intact.
Hosted baseline/candidate evidence is required before accepting the fix as
native tested behavior.

## Independent review correction I1 — Windows absolute paths

Independent review of `b82a867` found the shipping installer's separator regex
rejected native `C:\...` and UNC paths before service installation. Root
authorized narrow ownership of `Test-AbsolutePath` in `deploy/windows/install.ps1`.
The function now accepts rooted drive paths and complete UNC server/share
paths with slash or backslash separators. It refuses relative and drive-relative
paths, incomplete/malformed UNC, device namespaces, invalid Windows filename
characters and control characters. Strict end anchors reject trailing newlines.
The installer lifecycle and service-credential model are unchanged.

`deploy/qualification/windows-paths.ps1` parses the actual shipping installer,
extracts and defines only its path predicate, and runs 9 accepted and 26 rejected
cases without invoking the installer or changing accounts/services/files. The
Windows native harness runs it before any installation mutation and captures
its fixed assertion count in the scrubbed reports. This requires no additional
workflow step or dependency.

Before correction, local extracted-regex reproduction rejected canonical native
drive and UNC examples. After correction, the exact regex source passes all 35
cases in a Python regex cross-check. This is not native .NET/PowerShell evidence;
the pure PowerShell regression and actual canonical installer/SCM flow must pass
on the hosted Windows runner. Local `pwsh` remains unavailable.

## Local evidence

- `bash -n deploy/qualification/linux-manager.sh`: passed.
- `sh -n deploy/qualification/unit-probe.sh`: passed.
- Python `ast.parse` on both Python sources: passed.
- PyYAML parse of draft plus three-target assertion: passed.
- `git diff --check`: passed.
- `systemd-analyze verify deploy/linux/minecraft-ui.service`: exit 1 solely
  because `/usr/local/bin/minecraft-ui` is absent on the development host. No
  manager service was installed locally and this is not a passing host check.
- PowerShell execution/parser unavailable locally (`pwsh` absent). Native
  Windows behavior requires the hosted workflow.

## Hosted evidence

[Run 39027205172](https://github.com/jonasthim/minecraft-server-ui/actions/runs/39027205172)
tested source `4b5a5df281c56f021e34ae93e29d92e7a77e3e16`. Runner images were
`ubuntu22 20261012.322.1`, `ubuntu24 20261012.334.1` and
`win22 20261012.333.1`. Both Ubuntu targets passed installed-manager assertions
and cleanup. Windows passed all 35 shipping path-predicate regressions and the
shipping installer created the actual service, then failed the harness's textual
SCM account-name comparison before starting the service.

| Host | Baseline manager/probe NNP and helper | Candidate | Installed API/lifecycle/cleanup |
|---|---|---|---|
| Ubuntu 22.04, systemd 249.11-0ubuntu3.22 | NNP 1; enable/disable return 1 (refused) | NNP 0; enable/disable 0; invalid inputs 2 | Setup/admin/session/login, stop/start/restart, zero orphan process and cleanup passed |
| Ubuntu 24.04, systemd 255.4-1ubuntu8.17 | NNP 0; enable/disable 0; invalid inputs 2 | NNP 0; enable/disable 0; invalid inputs 2 | Setup/admin/session/login, stop/start/restart, zero orphan process and cleanup passed |
| Windows Server 2022 | Not applicable | Path regressions and installer reached SCM | Failed harness account-name assertion before service start; no service-lifecycle qualification |

The Linux artifact results concretely reproduce the older-systemd problem and
show the scoped candidate correction passing the same real-unit helper checks.

### Windows harness identity comparison correction

The first Windows run compared `Win32_Service.StartName` with the input account
string. That tests a display representation, while the intended assertion is
that SCM uses the exact newly created unprivileged account. Microsoft documents
the [local service-account .\\UserName form](https://learn.microsoft.com/en-us/windows/win32/ad/local-user-accounts).
The first run's scrubbed diagnostics omitted both StartName and its resolved SID,
so they cannot establish the precise alias SCM returned; normalization is a
plausible cause, not a proven observation from that run.

The revised harness resolves SCM's configured account through Windows to a SID,
normalizing only the documented `.\` local shorthand, and requires equality with
the SID returned by `Get-LocalUser` for the freshly created account. It verifies
both local account spellings resolve and a privileged system identity differs.
The process check uses the documented
[GetOwnerSid method](https://learn.microsoft.com/en-us/windows/win32/cimwin32prov/getownersid-method-in-class-win32-process)
and requires both successful query and the same exact SID. No unresolved or
different identity can pass; existing nonadministrator and ACL checks remain.

Fixed-field `identity-summary.json` captures expected/SCM/process SIDs, query
status and booleans indicating SCM name equality and local-alias use. Credentials,
raw service objects and arbitrary errors remain excluded. This adds evidence for
the next run without claiming the first failure was already resolved natively.
Local whitespace/static source review passed; PowerShell/Windows execution of the
correction is pending integrator dispatch. The integrator alone updates package
and qualification acceptance in the ledger.
