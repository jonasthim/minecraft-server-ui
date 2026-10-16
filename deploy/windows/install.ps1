# Run as Administrator. A dedicated unprivileged service account is required.
# Supply a PSCredential created interactively with Get-Credential; no passwords
# are embedded in command lines, application config, or generated script files.
param(
 [Parameter(Mandatory=$true)][string]$Binary,
 [Parameter(Mandatory=$true)][System.Management.Automation.PSCredential]$Credential,
 [string]$DataDir = 'C:\ProgramData\MinecraftUI'
)
$ErrorActionPreference = 'Stop'
$target = 'C:\Program Files\MinecraftUI'
function Test-AbsolutePath([string]$Value) { return ($Value -match '^[A-Za-z]:[\/]' -or $Value -match '^\\[^\/]+[\/][^\/]+(?:[\/]|$)') }
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
