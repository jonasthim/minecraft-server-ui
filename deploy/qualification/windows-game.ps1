# Fresh disposable GitHub-hosted WindowsServer2022 Administrator only.
param(
 [Parameter(Mandatory=$true)][string]$Binary,
 [Parameter(Mandatory=$true)][string]$OutputDirectory,
 [Parameter(Mandatory=$true)][ValidateSet('1.21.9')][string]$MinecraftVersion
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
if ($env:GITHUB_ACTIONS -ne 'true' -or $env:RUNNER_ENVIRONMENT -ne 'github-hosted') { throw 'hosted_only' }
$administrator = [Security.Principal.WindowsPrincipal]::new([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $administrator.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'administrator_required' }
$os = Get-CimInstance Win32_OperatingSystem
if ($os.Caption -notmatch 'Windows Server 2022' -or [Environment]::Is64BitOperatingSystem -ne $true) { throw 'host_not_allowlisted' }
foreach ($path in @($Binary,$OutputDirectory)) {
 $item = Get-Item -LiteralPath $path
 if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'unsafe_input_path' }
}
$Binary = (Resolve-Path -LiteralPath $Binary).Path
$OutputDirectory = (Resolve-Path -LiteralPath $OutputDirectory).Path
if (-not (Test-Path -LiteralPath $OutputDirectory -PathType Container)) { throw 'report_directory_absent' }
foreach ($entry in @(Get-ChildItem -LiteralPath $OutputDirectory -Force)) {
 if ($entry.Name -notin @('commit.txt','go-version.txt','runner.txt') -or $entry.PSIsContainer -or $entry.Length -gt 4096 -or ($entry.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'unexpected_report_file' }
}
if ($OutputDirectory.StartsWith('C:\ProgramData\MinecraftUI',[StringComparison]::OrdinalIgnoreCase) -or $OutputDirectory.StartsWith('C:\Program Files\MinecraftUI',[StringComparison]::OrdinalIgnoreCase)) { throw 'unsafe_output_path' }
$target = 'C:\Program Files\MinecraftUI'
$data = 'C:\ProgramData\MinecraftUI'
if ((Get-Service MinecraftUI -ErrorAction SilentlyContinue) -or (Test-Path -LiteralPath $target) -or (Test-Path -LiteralPath $data)) { throw 'existing_installation' }
# Existing reviewed helper defines LSA/SID/ACL functions only; no updater install.
. (Join-Path $PSScriptRoot 'windows-updater-support.ps1')
$account = 'mcg' + [Guid]::NewGuid().ToString('N').Substring(0,12)
$sid = $null
$accountCreated = $false
$rightGranted = $false
$installStarted = $false
$firewallCreated = $false
$failed = $false
$stage = 'dependencies'
$binaryHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $Binary).Hash.ToLowerInvariant()
$scratch = Join-Path ([IO.Path]::GetTempPath()) ('minecraft-game-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $scratch | Out-Null
try {
 & node -e 'if(Number(process.versions.node.split(".")[0])<22)process.exit(1)'
 if ($LASTEXITCODE -ne 0) { throw 'node_version' }
 & npm ci --ignore-scripts --prefix (Join-Path $PSScriptRoot 'game-client') *> (Join-Path $scratch 'npm')
 if ($LASTEXITCODE -ne 0) { throw 'npm_ci' }
 $stage = 'firewall'
 if (@(Get-NetFirewallProfile | Where-Object { -not $_.Enabled }).Count -ne 0) { throw 'firewall_disabled' }
 if (Get-NetFirewallRule -Name MinecraftGameAcceptance -ErrorAction SilentlyContinue) { throw 'existing_firewall_rule' }
 # Exclude only IPv4 loopback; all other IPv4 and all IPv6 game/API ingress is
 # blocked before offline authentication is enabled. Private MSMP binds loopback.
 New-NetFirewallRule -Name MinecraftGameAcceptance -DisplayName MinecraftGameAcceptance -Direction Inbound -Action Block -Protocol TCP -LocalPort 25565,8080 -RemoteAddress @('0.0.0.0-126.255.255.255','128.0.0.0-255.255.255.255','::/0') -Profile Any | Out-Null
 $firewallCreated = $true
 Assert-True ((Get-NetFirewallRule -Name MinecraftGameAcceptance).Enabled -eq 'True') 'firewall_rule_disabled'
 $stage = 'account'
 $bytes = [byte[]]::new(36)
 [Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
 $secret = ConvertTo-SecureString ('aA1!' + [Convert]::ToBase64String($bytes)) -AsPlainText -Force
 [Array]::Clear($bytes,0,$bytes.Length)
 New-LocalUser -Name $account -Password $secret -AccountNeverExpires -PasswordNeverExpires -UserMayNotChangePassword | Out-Null
 $accountCreated = $true
 $sid = (Get-LocalUser -Name $account).SID.Value
 $qualified = $env:COMPUTERNAME + '\' + $account
 $credential = [Management.Automation.PSCredential]::new($qualified,$secret)
 [NativeUpdaterLogonRight]::Set($sid,$true)
 $rightGranted = $true
 Assert-True (-not (Get-LocalGroupMember -SID 'S-1-5-32-544' | Where-Object { $_.SID.Value -eq $sid })) 'service_account_privileged'
 $stage = 'installer'
 $installStarted = $true
 & (Join-Path $PSScriptRoot '..\windows\install.ps1') -Binary $Binary -Credential $credential *> (Join-Path $scratch 'installer')
 $service = Get-CimInstance Win32_Service -Filter "Name='MinecraftUI'"
 Assert-True ((Resolve-ServiceAccountSid $service.StartName) -eq $sid) 'scm_identity_mismatch'
 Assert-PrivateAcl $target $sid ([Security.AccessControl.FileSystemRights]::ReadAndExecute)
 Assert-PrivateAcl $data $sid ([Security.AccessControl.FileSystemRights]::Modify)
 $stage = 'native_start'
 Start-Service MinecraftUI
 (Get-Service MinecraftUI).WaitForStatus('Running',[TimeSpan]::FromSeconds(60))
 $stage = 'game_api'
 & python (Join-Path $PSScriptRoot 'game_api.py') --minecraft-version $MinecraftVersion --output $OutputDirectory
 if ($LASTEXITCODE -ne 0) { throw 'game_acceptance_failed' }
 $stage = 'complete'
} catch {
 $failed = $true
 # Fixed stage/type only: exception messages may contain arbitrary native data.
 Write-Output ('FAIL game acceptance stage=' + $stage + ' type=' + $_.Exception.GetType().Name)
} finally {
 try {
  if ($installStarted) {
   $service = Get-Service MinecraftUI -ErrorAction SilentlyContinue
   if ($service) {
    if ($service.Status -ne 'Stopped') { Stop-Service MinecraftUI; $service.WaitForStatus('Stopped',[TimeSpan]::FromSeconds(160)) }
    & sc.exe delete MinecraftUI *> (Join-Path $scratch 'delete')
    if ($LASTEXITCODE -ne 0) { throw 'service_delete_failed' }
    $service.Dispose()
   }
   $remaining = @(Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -eq (Join-Path $target 'minecraft-ui.exe') -or ($_.ExecutablePath -and $_.ExecutablePath.StartsWith(($data + '\runtimes\'),[StringComparison]::OrdinalIgnoreCase)) })
   if ($remaining.Count -ne 0) {
    # Only processes executing within this newly owned canonical installation.
    $remaining | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }
    $failed = $true
    Write-Output 'FAIL orphan_game_or_manager_at_cleanup'
   }
   foreach ($path in @($target,$data)) { if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path -Recurse -Force } }
  }
 } catch { $failed = $true; Write-Output 'FAIL service_or_private_state_cleanup' }
 try {
  if ($rightGranted) { [NativeUpdaterLogonRight]::Set($sid,$false) }
  if ($accountCreated) { Remove-LocalUser -Name $account }
 } catch { $failed = $true; Write-Output 'FAIL account_right_cleanup' }
 try { if ($firewallCreated) { Remove-NetFirewallRule -Name MinecraftGameAcceptance } } catch { $failed = $true; Write-Output 'FAIL firewall_cleanup' }
 try { Remove-Item -LiteralPath $scratch -Recurse -Force } catch { $failed = $true; Write-Output 'FAIL private_scratch_cleanup' }
 @{scope='vanilla-1.21.9-real-headless-game';failed=$failed;last_stage=$stage;manager_sha256=$binaryHash} | ConvertTo-Json | Set-Content (Join-Path $OutputDirectory 'result.json')
}
if ($failed) { exit 1 }
