# Native Windows qualification (pending)

No native Windows execution environment has been established or provided for
this session. The code and
installer are unqualified on Windows 10, Windows 11 and Server 2019+. Cross builds
only check compilation. Do not mark Task 05 accepted until each supported family
has a recorded native execution report.

The command now dispatches `launch` to `runLaunch` and wraps serving with
`runNativeService`. Startup reconciles/waits prior JVMs with `Autostart(nil)`;
providers do not yet supply validated installed launch specs, so actual automatic
starts remain pending. Shutdown drains HTTP, then stops jobs and JVM launchers
concurrently with an independent 110-second context before DB close and SCM
return. A service StopPending response does not itself prove JVM exit. Before
provider jobs integrate, jobs must support paused startup admission so dispatch
cannot race native/instance reconciliation. A future autostart resolver must
verify installation, canonical DB config and runtime; stale launch.json alone
is insufficient.

Install with elevated Windows PowerShell 5.1 or PowerShell 7, a dedicated account
with Log on as a service, and `install.ps1 -Binary <absolute-exe> -Credential
(Get-Credential)`. Use local encrypted storage and restrict the entire data tree;
Go 0600 modes do not establish Windows ACLs. Administrator memberships and service
logon rights must be checked on the host before starting. Bind the HTTP listener
to loopback or configure the approved authenticated origin/reverse proxy.

For each Windows family, record OS build, architecture, binary SHA-256, Java
version/hash, Minecraft tuple, paths including spaces/Unicode, and results of:

1. Install as the dedicated account. Start two distinct instances and verify
   identity.json PID is Java, LauncherPID is the launcher, console output persists,
   stdout/stderr stay bounded, commands reach stdin and readiness stays separate.
2. Deliberate stop: server receives stop, saves, exits; launcher records deliberate
   exit. Confirm no scheduled retry. Server ignoring stop: confirm bounded force,
   process Wait, forced flag and surfaced error.
3. Stop-Service: confirm StopPending while graceful save/exit happens, then STOPPED
   only after both JVM and launcher have exited. Confirm former ports can bind.
4. Restart-Service: verify old JVM creation identity ceased before autostart of
   enabled instances. Disabled instances stay stopped. Kill the manager process:
   parent pipe EOF must produce the same graceful stop and Wait behavior.
5. Kill launcher during an active lease: suspended-before-containment job setup
   must prevent a JVM escaping, and kill-on-close must clean up descendants.
   Manager-only crash must keep launcher watchdog alive until save-on delivery.
6. Simulate stale PID/old boot identity, inaccessible process image and unavailable
   boot GUID query. No foreign PID may be signalled; unverifiable state must block
   autostart. Probe boot GUID availability under the actual service account.
7. Fill JVM stdin without reading: writes must fail promptly and save-on retries
   remain bounded/visible. Verify PIPE_NOWAIT setup on this Windows family.
8. Upgrade: Stop-Service and WAIT for STOPPED plus old JVM/launcher exit before
   replacing the executable. Verify replacement/rollback under ACLs. Start service
   and verify autostart. Never overwrite a running Windows executable.
9. Test concurrent stopped edits/start, token rejection, private ACL access denial,
   long/Unicode paths and argv quoting without shell interpolation.

The Windows process identity binds the boot GUID, creation FILETIME and image
path. Win32 has no stable documented remote cwd API; the private launcher record
and authenticated identity-bound control carry the working directory. This limit
must stay explicit in the review and host evidence.

## Current security boundary

The JVM currently runs as the same Windows account as the manager. Job Objects
provide lifetime containment; they do not provide a credential or filesystem
security boundary. No restricted token or per-instance ACL isolation has been
implemented or qualified. A JVM/mod/plugin can access manager.db and other
instance files wherever that service account has permission. Restricting the
data tree from other accounts does not isolate games from their manager or each
other. This limitation must be resolved/reviewed before claiming instance
security isolation on Windows; the current cross build cannot establish it.

Production launch now verifies managed runtime provenance/inventory and immutable
provider artifacts/argv offline. All LaunchSpec.Env overrides are rejected until
a typed policy is approved. These checks do not change the same-account access
limitation. EOF/SCM recovery failures now yield a nonzero launcher result even if
the JVM exits zero; native tests must confirm SCM shutdown reports such failures
and waits for final bookkeeping.
