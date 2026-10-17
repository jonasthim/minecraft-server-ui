# Task32B: fresh disposable hosted Windows VM only. No Minecraft executes.
param(
 [Parameter(Mandatory=$true)][string]$InitialBinary,
 [Parameter(Mandatory=$true)][string]$SuccessBinary,
 [Parameter(Mandatory=$true)][string]$UpdaterBinary,
 [Parameter(Mandatory=$true)][string]$QualifierBinary,
 [Parameter(Mandatory=$true)][string]$NegativeBinary,
 [Parameter(Mandatory=$true)][string]$PeerSuccessBinary,
 [Parameter(Mandatory=$true)][string]$PeerNegativeBinary,
 [Parameter(Mandatory=$true)][string]$OutputDirectory
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
if ($env:GITHUB_ACTIONS -ne 'true' -or $env:RUNNER_ENVIRONMENT -ne 'github-hosted') { throw 'Disposable hosted runner required.' }
$administrator = [Security.Principal.WindowsPrincipal]::new([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $administrator.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Administrator required.' }
foreach ($name in @('InitialBinary','SuccessBinary','UpdaterBinary','QualifierBinary','NegativeBinary','PeerSuccessBinary','PeerNegativeBinary')) {
 $value = Get-Variable -Name $name -ValueOnly
 if (-not (Test-Path -LiteralPath $value -PathType Leaf) -or (Get-Item -LiteralPath $value).LinkType) { throw 'Regular binary input required.' }
 Set-Variable -Name $name -Value (Resolve-Path -LiteralPath $value).Path
}
$OutputDirectory = (Resolve-Path -LiteralPath $OutputDirectory).Path
if (-not (Test-Path -LiteralPath $OutputDirectory -PathType Container) -or (Get-Item -LiteralPath $OutputDirectory).LinkType) { throw 'Regular report directory required.' }
$target = 'C:\Program Files\MinecraftUI'
$data = 'C:\ProgramData\MinecraftUI'
$updaterState = 'C:\ProgramData\MinecraftUIUpdate'
foreach ($serviceName in @('MinecraftUI','MinecraftUIUpdater','MinecraftUIUpdateProbe')) {
 if (Get-Service -Name $serviceName -ErrorAction SilentlyContinue) { throw 'Refusing existing native installation.' }
}
foreach ($path in @($target,$data,$updaterState)) {
 if (Test-Path -LiteralPath $path) { throw 'Refusing existing canonical state.' }
}
. (Join-Path $PSScriptRoot 'windows-updater-support.ps1')
$account = 'mcu' + [Guid]::NewGuid().ToString('N').Substring(0,12)
if (Get-LocalUser -Name $account -ErrorAction SilentlyContinue) { throw 'Refusing existing service account.' }
$scratch = Join-Path $env:RUNNER_TEMP ('minecraft-updater-' + [Guid]::NewGuid().ToString('N'))
$sid = $null
$accountCreated = $false
$rightGranted = $false
$installStarted = $false
$scratchCreated = $false
$failed = $false
$cleanupChecked = $false
$script:assertionFailure = $null
$stage = 'inputs'
function Wait-NativeService([string]$Name, [string]$State, [int]$Seconds) {
 $service = Get-Service -Name $Name
 try { $service.WaitForStatus($State,[TimeSpan]::FromSeconds($Seconds)) } finally { $service.Dispose() }
}
function Assert-ServiceIdentity([string]$Name, [string]$ExpectedSid, [string]$ExpectedPath) {
 $service = Get-CimInstance Win32_Service -Filter ("Name='" + $Name + "'")
 Assert-True ($null -ne $service -and $service.PathName -ceq $ExpectedPath) 'SCM executable binding differs.'
 Assert-True ((Resolve-ServiceAccountSid $service.StartName) -eq $ExpectedSid) 'SCM account SID differs.'
}
try {
 New-Item -ItemType Directory -Path $scratch | Out-Null
 $scratchCreated = $true
 & icacls.exe $scratch '/inheritance:r' '/grant:r' 'BUILTIN\Administrators:(OI)(CI)F' 'NT AUTHORITY\SYSTEM:(OI)(CI)F' | Out-Null
 Assert-True ($LASTEXITCODE -eq 0) 'Private acquisition scratch ACL failed.'
 $initialVersion = & $InitialBinary version
 Assert-True ($LASTEXITCODE -eq 0 -and $initialVersion -ceq 'minecraft-ui v0.0.1') 'Initial manager version differs.'
 $successVersion = & $SuccessBinary version
 Assert-True ($LASTEXITCODE -eq 0 -and $successVersion -ceq 'minecraft-ui v0.0.2') 'Success manager version differs.'
 $updaterVersion = & $UpdaterBinary version
 Assert-True ($LASTEXITCODE -eq 0) 'Updater version command failed.'
 $inputSummary = @{
  scope = 'fixture acquisition; installed native activation/recovery; no game evidence'
  initial_version = $initialVersion
  success_version = $successVersion
  updater_version = $updaterVersion
 }
 foreach ($name in @('InitialBinary','SuccessBinary','UpdaterBinary','QualifierBinary','NegativeBinary','PeerSuccessBinary','PeerNegativeBinary')) {
  $inputSummary[$name + '_sha256'] = (Get-FileHash -Algorithm SHA256 -LiteralPath (Get-Variable -Name $name -ValueOnly)).Hash.ToLowerInvariant()
 }
 $inputSummary | ConvertTo-Json | Set-Content (Join-Path $OutputDirectory 'input-summary.json')
 $fixtures = Join-Path $scratch 'fixtures'
 $repo = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
 & $QualifierBinary prepare --success $SuccessBinary --negative $NegativeBinary --peer-success $PeerSuccessBinary --peer-negative $PeerNegativeBinary --license (Join-Path $repo 'LICENSE') --notices (Join-Path $repo 'THIRD_PARTY_NOTICES.txt') --out $fixtures
 Assert-True ($LASTEXITCODE -eq 0) 'Fixture acquisition preparation failed.'
 $stage = 'account'
 $randomBytes = [byte[]]::new(36)
 [Security.Cryptography.RandomNumberGenerator]::Fill($randomBytes)
 $secret = ConvertTo-SecureString ('aA1!' + [Convert]::ToBase64String($randomBytes)) -AsPlainText -Force
 [Array]::Clear($randomBytes,0,$randomBytes.Length)
 New-LocalUser -Name $account -Password $secret -AccountNeverExpires -PasswordNeverExpires -UserMayNotChangePassword | Out-Null
 $accountCreated = $true
 $sid = (Get-LocalUser -Name $account).SID.Value
 $qualified = $env:COMPUTERNAME + '\' + $account
 $credential = [Management.Automation.PSCredential]::new($qualified,$secret)
 [NativeUpdaterLogonRight]::Set($sid,$true)
 $rightGranted = $true
 Assert-True (-not (Get-LocalGroupMember -SID 'S-1-5-32-544' | Where-Object { $_.SID.Value -eq $sid })) 'Manager account is an administrator.'
 $stage = 'shipping installer'
 $installStarted = $true
 & (Join-Path $PSScriptRoot '..\windows\install.ps1') -Binary $InitialBinary -Credential $credential -UpdaterBinary $UpdaterBinary
 Assert-ServiceIdentity MinecraftUI $sid '"C:\Program Files\MinecraftUI\minecraft-ui.exe" serve'
 Assert-ServiceIdentity MinecraftUIUpdateProbe $sid '"C:\Program Files\MinecraftUI\minecraft-ui-probe.exe" update-schema-service'
 Assert-ServiceIdentity MinecraftUIUpdater 'S-1-5-18' '"C:\Program Files\MinecraftUI\selfupdate.exe" service'
 Assert-PrivateAcl $target $sid ([Security.AccessControl.FileSystemRights]::ReadAndExecute)
 Assert-PrivateAcl $data $sid ([Security.AccessControl.FileSystemRights]::Modify)
 # Root/admin private state has no manager ACE, including inherited access.
 $stateAcl = Get-Acl -LiteralPath $updaterState
 Assert-True $stateAcl.AreAccessRulesProtected 'Updater state inheritance remained enabled.'
 foreach ($entry in $stateAcl.Access) {
  $principal = $entry.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
  Assert-True ($principal -in @('S-1-5-18','S-1-5-32-544') -and $entry.AccessControlType -eq 'Allow') 'Untrusted access to updater state.'
 }
 $stateOwner = $stateAcl.GetOwner([Security.Principal.SecurityIdentifier]).Value
 Assert-True ($stateOwner -in @('S-1-5-18','S-1-5-32-544')) 'Updater state owner differs.'
 $stage = 'fixture staging under manager SID'
 Copy-Item -LiteralPath (Join-Path $fixtures 'public-trust.json') -Destination (Join-Path $target 'update-trust.json') -Force
 & icacls.exe (Join-Path $target 'update-trust.json') '/setowner' 'BUILTIN\Administrators' | Out-Null
 Assert-True ($LASTEXITCODE -eq 0) 'Public fixture trust owner failed.'
 Assert-PrivateAcl (Join-Path $target 'update-trust.json') $sid ([Security.AccessControl.FileSystemRights]::ReadAndExecute) $false
 $helper = Join-Path $target 'qualification-updater.exe'
 Copy-Item -LiteralPath $QualifierBinary -Destination $helper
 & icacls.exe $helper '/setowner' 'BUILTIN\Administrators' | Out-Null
 Assert-True ($LASTEXITCODE -eq 0) 'Qualification helper owner failed.'
 $bundle = Join-Path $data 'selfupdate\qualification-input'
 Copy-Item -LiteralPath $fixtures -Destination $bundle -Recurse
 # No credential enters process arguments/environment/files. Start-Process
 # hands the in-memory PSCredential to Windows logon for the manager SID.
 foreach ($version in @('v0.0.2','v0.0.3')) {
  $stageFile = Join-Path $data ('selfupdate\' + $version + '-stage.json')
  $arguments = @('stage','--bundle',('"'+$bundle+'"'),'--version',$version,'--current','v0.0.1','--stage-dir',('"'+(Join-Path $data 'selfupdate\stages')+'"'))
  $process = Start-Process -FilePath $helper -ArgumentList $arguments -Credential $credential -RedirectStandardOutput $stageFile -WindowStyle Hidden -PassThru
  try {
   if (-not $process.WaitForExit(90000)) { $process.Kill(); throw 'Manager-account staging timed out.' }
   Assert-True ($process.ExitCode -eq 0) 'Manager-account staging failed.'
  } finally { $process.Dispose() }
 }
 $stage = 'native updater services'
 Start-Service MinecraftUIUpdater
 Wait-NativeService MinecraftUIUpdater Running 180
 Start-Service MinecraftUI
 Wait-NativeService MinecraftUI Running 60
 $service = Get-CimInstance Win32_Service -Filter "Name='MinecraftUIUpdater'"
 $process = Get-CimInstance Win32_Process -Filter ("ProcessId="+$service.ProcessId)
 $owner = Invoke-CimMethod -InputObject $process -MethodName GetOwnerSid
 Assert-True ($owner.ReturnValue -eq 0 -and $owner.Sid -eq 'S-1-5-18' -and $process.ExecutablePath -ceq (Join-Path $target 'selfupdate.exe')) 'Updater actual PID/image/SID differs.'
 @{manager_sid=$sid;probe_sid=$sid;updater_sid=$owner.Sid;updater_pid=$service.ProcessId;state_owner_sid=$stateOwner} | ConvertTo-Json | Set-Content (Join-Path $OutputDirectory 'identity-summary.json')
 $stage = 'installed API activation, negative health and interruption'
 & python (Join-Path $PSScriptRoot 'updater_api.py') --success-stage (Join-Path $data 'selfupdate\v0.0.2-stage.json') --negative-stage (Join-Path $data 'selfupdate\v0.0.3-stage.json') --initial-binary $InitialBinary --success-binary $SuccessBinary --negative-binary $NegativeBinary --expected-sid $sid --output $OutputDirectory | Tee-Object -FilePath (Join-Path $OutputDirectory 'api-assertions.txt')
 Assert-True ($LASTEXITCODE -eq 0) 'Installed API updater acceptance failed.'
 Write-Output 'PASS installed Windows genuine activation, negative health rollback and interrupted candidate recovery'
} catch {
 $failed = $true
 Write-Output ('FAIL installed updater at ' + $stage + ' (' + $_.Exception.GetType().Name + ')')
 if ($script:assertionFailure) { Write-Output ('Assertion: ' + $script:assertionFailure) }
} finally {
 try {
  if ($installStarted) {
   @(Get-CimInstance Win32_Service -Filter "Name='MinecraftUI' OR Name='MinecraftUIUpdater' OR Name='MinecraftUIUpdateProbe'") | Select-Object Name,State,StartMode,ProcessId,ExitCode,ServiceSpecificExitCode | ConvertTo-Json | Set-Content (Join-Path $OutputDirectory 'scm-summary.json')
   @(Get-WinEvent -FilterHashtable @{LogName='System';ProviderName='Service Control Manager';StartTime=(Get-Date).AddMinutes(-25)} -ErrorAction SilentlyContinue) | Group-Object Id | Select-Object Name,Count | ConvertTo-Json | Set-Content (Join-Path $OutputDirectory 'event-summary.json')
   # Terminate an owned interrupted updater first, so no update goroutine can
   # restart the manager while the remaining owned services are removed.
   $updateService = Get-CimInstance Win32_Service -Filter "Name='MinecraftUIUpdater'"
   if ($updateService -and $updateService.ProcessId -gt 0) {
    $updateProcess = Get-CimInstance Win32_Process -Filter ("ProcessId="+$updateService.ProcessId)
    Assert-True ($updateProcess.ExecutablePath -ceq (Join-Path $target 'selfupdate.exe')) 'Cleanup updater image differs.'
    Stop-Process -Id $updateService.ProcessId -Force
    Wait-NativeService MinecraftUIUpdater Stopped 40
   }
   foreach ($name in @('MinecraftUI','MinecraftUIUpdateProbe','MinecraftUIUpdater')) {
    $service = Get-Service -Name $name -ErrorAction SilentlyContinue
    if ($service) {
     try {
      if ($service.Status -ne 'Stopped') { Stop-Service -Name $name; $service.WaitForStatus('Stopped',[TimeSpan]::FromSeconds(180)) }
      & sc.exe delete $name | Out-Null
      Assert-True ($LASTEXITCODE -eq 0) 'Owned service deletion failed.'
     } finally { $service.Dispose() }
    }
   }
   $remaining = @(Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith($target+'\',[StringComparison]::OrdinalIgnoreCase) })
   Assert-True ($remaining.Count -eq 0) 'Orphan installed manager/updater/probe/helper during cleanup.'
   foreach ($path in @($target,$data,$updaterState)) { if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path -Recurse -Force } }
   foreach ($path in @($target,$data,$updaterState)) { Assert-True (-not (Test-Path -LiteralPath $path)) 'Owned state survived cleanup.' }
   foreach ($name in @('MinecraftUI','MinecraftUIUpdateProbe','MinecraftUIUpdater')) { Assert-True (-not (Get-Service -Name $name -ErrorAction SilentlyContinue)) 'Owned service survived cleanup.' }
  }
  $cleanupChecked = $true
 } catch { $failed = $true; Write-Output 'FAIL installed updater service/file cleanup' }
 try {
  if ($rightGranted) { [NativeUpdaterLogonRight]::Set($sid,$false) }
  if ($accountCreated) { Remove-LocalUser -Name $account; Assert-True (-not (Get-LocalUser -Name $account -ErrorAction SilentlyContinue)) 'Owned account survived cleanup.' }
  if ($scratchCreated) { Remove-Item -LiteralPath $scratch -Recurse -Force }
 } catch { $failed = $true; Write-Output 'FAIL ephemeral account/right/scratch cleanup' }
 @{scope='installed-updater-fixture-acquisition';failed=$failed;last_stage=$stage;cleanup_checked=$cleanupChecked} | ConvertTo-Json | Set-Content (Join-Path $OutputDirectory 'result.json')
}
if ($failed) { exit 1 }
