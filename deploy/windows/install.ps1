# Run as Administrator. A dedicated unprivileged service account is required.
# Supply a PSCredential created interactively with Get-Credential; no passwords
# are embedded in command lines, application config, or generated script files.
param(
 [Parameter(Mandatory=$true)][string]$Binary,
 [Parameter(Mandatory=$true)][System.Management.Automation.PSCredential]$Credential,
 [string]$DataDir = 'C:\ProgramData\MinecraftUI',
 [string]$UpdaterBinary = ''
)
$ErrorActionPreference = 'Stop'
$target = 'C:\Program Files\MinecraftUI'
function Test-AbsolutePath([string]$Value) {
 # PowerShell single quotes preserve regex backslashes. A separator class
 # needs \\ to represent a literal backslash; \/ alone matches only slash.
 $drive = '^[A-Za-z]:[\\/][^<>:"|?*\x00-\x1F]*\z'
 # Require both UNC server/share components; reject device namespaces and
 # invalid Windows filename characters rather than accepting a root prefix.
 $unc = '^\\\\(?!\.{1,2}[\\/])[^\\/:*?"<>|\x00-\x1F]+[\\/](?!\.{1,2}(?:[\\/]|\z))[^\\/:*?"<>|\x00-\x1F]+(?:[\\/][^<>:"|?*\x00-\x1F]*)?\z'
 return ($Value -match $drive -or $Value -match $unc)
}
if (-not (Test-AbsolutePath $Binary) -or -not (Test-AbsolutePath $DataDir)) { throw 'Absolute paths required.' }
if ($Credential.UserName -match '^(LocalSystem|NT AUTHORITY\\SYSTEM|Administrator)$') { throw 'Use a dedicated unprivileged account.' }
if (Get-Service -Name MinecraftUI -ErrorAction SilentlyContinue) { throw 'Service already exists. Use the documented stop/wait upgrade procedure.' }
New-Item -ItemType Directory -Force -Path $target,$DataDir | Out-Null
Copy-Item -LiteralPath $Binary -Destination (Join-Path $target 'minecraft-ui.exe')
# Remove inherited access to private state; administrators retain ownership for
# recovery. The service account can modify only data, never the executable.
& icacls.exe $target '/inheritance:r' '/grant:r' 'BUILTIN\Administrators:(OI)(CI)F' 'NT AUTHORITY\SYSTEM:(OI)(CI)F' ($Credential.UserName + ':(OI)(CI)RX')
if ($LASTEXITCODE -ne 0) { throw 'Executable ACL configuration failed.' }
& icacls.exe $DataDir '/inheritance:r' '/grant:r' 'BUILTIN\Administrators:(OI)(CI)F' 'NT AUTHORITY\SYSTEM:(OI)(CI)F' ($Credential.UserName + ':(OI)(CI)M')
if ($LASTEXITCODE -ne 0) { throw 'Data ACL configuration failed.' }
$path = '"' + (Join-Path $target 'minecraft-ui.exe') + '" serve'
New-Service -Name MinecraftUI -DisplayName 'Minecraft Server UI' -BinaryPathName $path -Credential $Credential -StartupType Automatic
# Environment is scoped to this service. User must have 'Log on as a service'.
New-ItemProperty -Path 'HKLM:\SYSTEM\CurrentControlSet\Services\MinecraftUI' -Name Environment -PropertyType MultiString -Value @("MINECRAFT_UI_DATA_DIR=$DataDir",'MINECRAFT_UI_ADDR=127.0.0.1:8080') -Force | Out-Null
Write-Output 'Installed without starting. Verify service logon rights and local policy, then Start-Service MinecraftUI.'

# Optional bounded LocalSystem update service. The manager keeps RX on binaries;
# neither JVMs nor the manager receive Program Files write or SCM privileges.
if ($UpdaterBinary) {
 if ($DataDir -cne 'C:\ProgramData\MinecraftUI') { throw 'Self-update requires the canonical native data directory.' }
 if (-not (Test-AbsolutePath $UpdaterBinary)) { throw 'Absolute updater binary path required.' }
 if (Get-Service -Name MinecraftUIUpdater,MinecraftUIUpdateProbe -ErrorAction SilentlyContinue) { throw 'Updater/probe service already exists; use reviewed maintenance.' }
 $updaterState = 'C:\ProgramData\MinecraftUIUpdate'
 $control = Join-Path $DataDir 'selfupdate\control'
 New-Item -ItemType Directory -Force -Path $updaterState,(Join-Path $updaterState 'stages'),$control,(Join-Path $DataDir 'selfupdate\stages') | Out-Null
 & icacls.exe $updaterState '/inheritance:r' '/grant:r' 'BUILTIN\Administrators:(OI)(CI)F' 'NT AUTHORITY\SYSTEM:(OI)(CI)F'
 if ($LASTEXITCODE -ne 0) { throw 'Privileged update state ACL failed.' }
 & icacls.exe $updaterState '/setowner' 'BUILTIN\Administrators' '/T'
 if ($LASTEXITCODE -ne 0) { throw 'Updater state ownership failed.' }
 Copy-Item -LiteralPath $UpdaterBinary -Destination (Join-Path $target 'selfupdate.exe')
 Copy-Item -LiteralPath (Join-Path $target 'minecraft-ui.exe') -Destination (Join-Path $target 'minecraft-ui-probe.exe')
 & icacls.exe $target '/setowner' 'BUILTIN\Administrators' '/T'
 if ($LASTEXITCODE -ne 0) { throw 'Updater executable ownership failed.' }
 $capabilityFile = Join-Path $control 'install-capability.json'
 # The already supplied credential stays in memory/SCM. It is never serialized
 # into command lines, environment, reports or a password file.
 $probeProcess = Start-Process -FilePath (Join-Path $target 'minecraft-ui.exe') -ArgumentList 'update-capability' -Credential $Credential -RedirectStandardOutput $capabilityFile -WindowStyle Hidden -PassThru
 if (-not $probeProcess.WaitForExit(10000)) { $probeProcess.Kill(); throw 'Unprivileged capability probe timed out.' }
 if ($probeProcess.ExitCode -ne 0) { throw 'Unprivileged capability probe failed.' }
 if ((Get-Item -LiteralPath $capabilityFile).Length -gt 2048) { throw 'Capability result exceeds bound.' }
 $capability = Get-Content -LiteralPath $capabilityFile -Raw
 $managerSID = ([System.Security.Principal.NTAccount]::new($Credential.UserName)).Translate([System.Security.Principal.SecurityIdentifier]).Value
 Set-Content -LiteralPath (Join-Path $updaterState 'bootstrap-capability.json') -Value $capability -Encoding ASCII
 & icacls.exe (Join-Path $updaterState 'bootstrap-capability.json') '/setowner' 'BUILTIN\Administrators'
 if ($LASTEXITCODE -ne 0) { throw 'Bootstrap capability ownership failed.' }
 & (Join-Path $target 'selfupdate.exe') 'bootstrap' $managerSID
 if ($LASTEXITCODE -ne 0) { throw 'Native updater bootstrap failed.' }
 Remove-Item -LiteralPath $capabilityFile
 $trustFile = Join-Path $target 'update-trust.json'
 if (-not (Test-Path -LiteralPath $trustFile)) { Set-Content -LiteralPath $trustFile -Value '{"format":1,"repository":"","public_keys":{}}' -Encoding ASCII }
 & icacls.exe $trustFile '/setowner' 'BUILTIN\Administrators'
 if ($LASTEXITCODE -ne 0) { throw 'Trust configuration ownership failed.' }
 $probePath = '"' + (Join-Path $target 'minecraft-ui-probe.exe') + '" update-schema-service'
 New-Service -Name MinecraftUIUpdateProbe -DisplayName 'Minecraft UI read-only schema probe' -BinaryPathName $probePath -Credential $Credential -StartupType Manual
 $updatePath = '"' + (Join-Path $target 'selfupdate.exe') + '" service'
 New-Service -Name MinecraftUIUpdater -DisplayName 'Minecraft UI verified executable updater' -BinaryPathName $updatePath -StartupType Automatic
 # The independent updater completes interrupted-executable recovery before
 # allowing the normal manager service to start. The probe has no dependency.
 & sc.exe config MinecraftUI depend= MinecraftUIUpdater
 if ($LASTEXITCODE -ne 0) { throw 'Updater service ordering failed.' }
 Write-Output 'Updater installed with empty trust. Configure reviewed public keys/repository before enabling updates; native Windows qualification is pending.'
}
