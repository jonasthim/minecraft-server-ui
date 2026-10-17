# Recover a local administrator password

Use the installed, matching-version `minecraft-ui admin-reset --username NAME --password-stdin` command on the manager host. It requires an existing enabled administrator with a local password, no linked OIDC identity, and local login enabled. Usernames follow normal login normalization. The password must be 10–128 bytes; stdin accepts a single password with optional LF/CRLF termination and then EOF.

A successful reset revokes **every session and personal access token of that administrator**, including automation tokens. Recreate required personal tokens after login and update their consumers. Other users, roles, authentication policy, worlds, jobs, and instance data stay intact. An audit event identifies the local offline operation without storing credentials. This command cannot create or enable an account, elevate a role, reopen setup, enable local login, or reset an SSO account. Recover SSO credentials at the identity provider; changes to authentication policy require a separate reviewed procedure.

The command takes the same exclusive manager lock as `serve`, before opening SQLite, and keeps it until SQLite closes. Missing or unsafe paths and schema mismatches are refused without creating or migrating the database. Use the manager's configured canonical data directory and an operating-system identity already authorized to access its private state. The private-directory and lock checks retain the launcher's trust in that account; another process controlled by that account can modify its files. Keep the manager stopped throughout backup and reset, including any separate scheduled deployment/update automation.

**Qualification:** deterministic disposable-database tests cover this implementation. The following installed-host recovery procedures still require disposable native Linux and Windows runner evidence. Existing native manager installation tests do not qualify password recovery.

## Linux (systemd installation)

These examples use the installer defaults: unit `minecraft-ui`, binary `/usr/local/bin/minecraft-ui`, service account `minecraft-ui`, data `/var/lib/minecraft-ui`. If `/etc/minecraft-ui/environment` overrides the data directory or other process configuration, use those same values below. Do not source an unreviewed environment file as shell code. Use a private terminal without shell tracing/session recording for password entry.

1. Stop the manager and verify the unit is inactive. Linux game launchers can continue running independently; password recovery does not change their files.

   ```bash
   sudo systemctl stop minecraft-ui.service
   sudo systemctl is-active minecraft-ui.service
   # Expected: inactive (is-active returns nonzero for inactive).
   ```

2. Make a private, consistent backup of the stopped manager's SQLite files. This Bash block requires the standard `flock` utility and holds the exact Linux manager lock during copying. It includes every existing WAL/shared-memory/rollback-journal sidecar. Do not copy only `manager.db` from an active manager; committed changes can still be in `manager.db-wal`.

   ```bash
   sudo bash -s <<'BACKUP'
   set -euo pipefail
   data=/var/lib/minecraft-ui
   test "$(systemctl is-active minecraft-ui.service)" = inactive
   test -d "$data" && test ! -L "$data"
   test -f "$data/manager.db" && test ! -L "$data/manager.db"
   test -f "$data/manager.lock" && test ! -L "$data/manager.lock"
   umask 077
   snapshot=$(mktemp -d /var/backups/minecraft-ui-recovery.XXXXXXXX)
   flock --exclusive --nonblock "$data/manager.lock" bash -s -- "$data" "$snapshot" <<'COPY'
   set -euo pipefail
   data=$1
   snapshot=$2
   for name in manager.db manager.db-wal manager.db-shm manager.db-journal; do
     if test -e "$data/$name" || test -L "$data/$name"; then
       test -f "$data/$name" && test ! -L "$data/$name"
       cp -p -- "$data/$name" "$snapshot/$name"
     fi
   done
   test -s "$snapshot/manager.db"
   COPY
   printf 'Stopped-manager backup: %s\n' "$snapshot"
   BACKUP
   ```

   Keep the entire backup directory together, protected as credential-bearing state. If copying fails, leave the manager stopped and resolve it before resetting. Do not restore database files over a running manager or merge a snapshot with current sidecars. Database rollback would reinstate old credentials/tokens and rewind other manager records; it is a separately reviewed recovery operation.

3. Enter the new password privately and pipe it through stdin. Run this in **Bash**. The password is read without echo, never appears in the command line or environment, and `printf` is a shell builtin. Do not replace the prompt with a literal password, `echo PASSWORD`, an environment variable, `--password`, or a here-document containing the secret.

   ```bash
   sudo -v
   (
     set +x
     trap 'unset recovery_password' EXIT
     IFS= read -r -s -p 'New administrator password: ' recovery_password || exit 1
     builtin printf '\n' >&2
     builtin printf '%s\n' "$recovery_password" |
       sudo -n -u minecraft-ui env MINECRAFT_UI_DATA_DIR=/var/lib/minecraft-ui \
       /usr/local/bin/minecraft-ui admin-reset --username admin --password-stdin
   )
   ```

   Continue only after exit status zero and the confirmation that account sessions and personal access tokens were revoked. A refusal does not reset the password. A database-close, confirmation-output, or commit error requires verifying account state before retrying; the command's error distinguishes a committed reset whose confirmation could not be written.

4. Start the service, verify it is active, and sign in through the configured HTTPS origin using the administrator username and new password. Previously open tabs must sign in again; verify the old password fails. Create replacement personal tokens only as required.

   ```bash
   sudo systemctl start minecraft-ui.service
   sudo systemctl is-active minecraft-ui.service
   # Expected: active.
   ```

## Windows (native service installation)

Use an elevated **Windows PowerShell 5.1 or later** session already authorized by the installer's ACLs. Defaults are service `MinecraftUI`, executable `C:\Program Files\MinecraftUI\minecraft-ui.exe`, and data `C:\ProgramData\MinecraftUI`. Read the existing service `Environment` registry property if the data directory was customized; set `$data` below to that exact existing directory. The password entered here is an application password, independent of the Windows service account credential. Do not create or change Windows accounts to recover it.

1. Stop and wait for the service. Native Windows shutdown waits for managed game processes; allow the documented shutdown period.

   ```powershell
   $ErrorActionPreference = 'Stop'
   $data = 'C:\ProgramData\MinecraftUI'
   $binary = 'C:\Program Files\MinecraftUI\minecraft-ui.exe'
   $service = Get-Service -Name MinecraftUI
   Stop-Service -Name MinecraftUI
   $service.WaitForStatus([System.ServiceProcess.ServiceControllerStatus]::Stopped,
                          [TimeSpan]::FromMinutes(3))
   ```

2. Copy the stopped SQLite files while holding the manager's first-byte lock. `FileStream.Lock(0,1)` conflicts with the Windows launcher's exclusive first-byte lock. The backup sits inside the existing private data directory and inherits its restricted ACLs. This block fails on reparse points rather than following them.

   ```powershell
   if ((Get-Service MinecraftUI).Status -ne 'Stopped') { throw 'Manager must be stopped.' }
   $directory = Get-Item -LiteralPath $data
   if (-not $directory.PSIsContainer -or
       ($directory.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'Unsafe data directory.' }
   foreach ($name in @('manager.db', 'manager.lock')) {
     $item = Get-Item -LiteralPath (Join-Path $data $name)
     if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
       throw 'Unsafe manager state file.'
     }
   }
   $lock = [IO.File]::Open((Join-Path $data 'manager.lock'), [IO.FileMode]::Open,
                         [IO.FileAccess]::ReadWrite, [IO.FileShare]::ReadWrite)
   $locked = $false
   try {
     $lock.Lock(0, 1)
     $locked = $true
     $snapshot = Join-Path $data ('recovery-backup-' + [Guid]::NewGuid().ToString('N'))
     New-Item -ItemType Directory -Path $snapshot | Out-Null
     foreach ($name in @('manager.db', 'manager.db-wal', 'manager.db-shm', 'manager.db-journal')) {
       $source = Join-Path $data $name
       try { $item = Get-Item -LiteralPath $source -Force -ErrorAction Stop }
       catch [System.Management.Automation.ItemNotFoundException] { continue }
       if ($null -ne $item) {
         if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
           throw 'Unsafe SQLite state file.'
         }
         Copy-Item -LiteralPath $source -Destination (Join-Path $snapshot $name) -ErrorAction Stop
       }
     }
     if ((Get-Item -LiteralPath (Join-Path $snapshot 'manager.db')).Length -eq 0) {
       throw 'Empty database backup.'
     }
     Write-Output "Stopped-manager backup: $snapshot"
   } finally {
     if ($locked) { $lock.Unlock(0, 1) }
     $lock.Dispose()
   }
   ```

   Preserve the whole snapshot and its private ACLs. Do not mix saved/current sidecars or restore into an active service. A database rollback reinstates prior credentials and rewinds other manager records, so review it separately.

3. Prompt with `Read-Host -AsSecureString` and send the password directly to the command's redirected stdin. Run without PowerShell transcription, debugger capture, or screen/session recording. The temporary plaintext exists only in process memory for transfer; the `finally` block releases it. No password enters process arguments, environment, shell history, or a temporary file.

   ```powershell
   $secret = Read-Host 'New administrator password' -AsSecureString
   $pointer = [IntPtr]::Zero
   $process = $null
   $writer = $null
   try {
     $info = [Diagnostics.ProcessStartInfo]::new()
     $info.FileName = $binary
     $info.Arguments = 'admin-reset --username admin --password-stdin'
     $info.UseShellExecute = $false
     $info.RedirectStandardInput = $true
     $info.EnvironmentVariables['MINECRAFT_UI_DATA_DIR'] = $data
     $process = [Diagnostics.Process]::new()
     $process.StartInfo = $info
     if (-not $process.Start()) { throw 'Cannot start administrator recovery.' }
     $pointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secret)
     $plain = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($pointer)
     $writer = [IO.StreamWriter]::new($process.StandardInput.BaseStream,
                                    [Text.UTF8Encoding]::new($false))
     $writer.WriteLine($plain)
     $writer.Dispose()
     $writer = $null
     $process.WaitForExit()
     if ($process.ExitCode -ne 0) { throw 'Administrator recovery failed; manager remains stopped.' }
   } finally {
     if ($null -ne $writer) { $writer.Dispose() }
     if ($pointer -ne [IntPtr]::Zero) { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($pointer) }
     $plain = $null
     $secret.Dispose()
     if ($null -ne $process) { $process.Dispose() }
   }
   ```

   Continue only after exit zero and the revocation confirmation. Account refusal, schema/path/lock failures, and transaction failures leave the manager stopped. Investigate rather than changing policy or removing lock files. Keep `manager.lock` in place permanently; deleting a locked file can defeat exclusion.

4. Start the service, wait for running state, then sign in through the configured HTTPS origin with the new password. Verify the old password fails and replace any revoked automation tokens that are still needed.

   ```powershell
   Start-Service -Name MinecraftUI
   (Get-Service MinecraftUI).WaitForStatus(
     [System.ServiceProcess.ServiceControllerStatus]::Running, [TimeSpan]::FromMinutes(1))
   ```
