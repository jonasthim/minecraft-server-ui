# Run only on a fresh disposable hosted Windows VM as Administrator.
param(
 [Parameter(Mandatory=$true)][string]$Binary,
 [Parameter(Mandatory=$true)][string]$OutputDirectory
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
if ($env:GITHUB_ACTIONS -ne 'true' -or $env:RUNNER_ENVIRONMENT -ne 'github-hosted') { throw 'Disposable hosted runner required.' }
$administrator = [Security.Principal.WindowsPrincipal]::new([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $administrator.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Administrator required.' }
$Binary = (Resolve-Path -LiteralPath $Binary).Path
$OutputDirectory = (Resolve-Path -LiteralPath $OutputDirectory).Path
# Pure shipping-function regressions run before account/service/file mutation.
& (Join-Path $PSScriptRoot 'windows-paths.ps1') | Tee-Object -FilePath (Join-Path $OutputDirectory 'path-assertions.txt')
$target = 'C:\Program Files\MinecraftUI'
$data = 'C:\ProgramData\MinecraftUI'
if ((Get-Service MinecraftUI -ErrorAction SilentlyContinue) -or (Test-Path -LiteralPath $target) -or (Test-Path -LiteralPath $data)) { throw 'Refusing existing installation.' }

# Add/remove one account right through LSA. No secedit export, password file,
# credential-bearing command line, process transcript or policy artifact.
Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Security.Principal;
public static class NativeSmokeLogonRight {
 [StructLayout(LayoutKind.Sequential)] struct Attributes {
  public uint Length; public IntPtr RootDirectory; public IntPtr ObjectName;
  public uint Flags; public IntPtr SecurityDescriptor; public IntPtr SecurityQualityOfService;
 }
 [StructLayout(LayoutKind.Sequential)] struct UnicodeString {
  public ushort Length; public ushort MaximumLength; public IntPtr Buffer;
 }
 [DllImport("advapi32.dll")] static extern uint LsaOpenPolicy(IntPtr name, ref Attributes attributes, uint access, out IntPtr policy);
 [DllImport("advapi32.dll")] static extern uint LsaAddAccountRights(IntPtr policy, IntPtr sid, UnicodeString[] rights, uint count);
 [DllImport("advapi32.dll")] static extern uint LsaRemoveAccountRights(IntPtr policy, IntPtr sid, [MarshalAs(UnmanagedType.U1)] bool all, UnicodeString[] rights, uint count);
 [DllImport("advapi32.dll")] static extern uint LsaClose(IntPtr policy);
 [DllImport("advapi32.dll")] static extern uint LsaNtStatusToWinError(uint status);
 static void Check(uint status) { if (status != 0) throw new Win32Exception((int)LsaNtStatusToWinError(status)); }
 public static void Set(string sidText, bool add) {
  IntPtr policy = IntPtr.Zero, sidMemory = IntPtr.Zero, rightMemory = IntPtr.Zero;
  try {
   var attrs = new Attributes(); attrs.Length = (uint)Marshal.SizeOf(typeof(Attributes));
   Check(LsaOpenPolicy(IntPtr.Zero, ref attrs, 0x00000810, out policy));
   var sid = new SecurityIdentifier(sidText); var bytes = new byte[sid.BinaryLength]; sid.GetBinaryForm(bytes, 0);
   sidMemory = Marshal.AllocHGlobal(bytes.Length); Marshal.Copy(bytes, 0, sidMemory, bytes.Length);
   string right = "SeServiceLogonRight"; rightMemory = Marshal.StringToHGlobalUni(right);
   var value = new UnicodeString { Length = (ushort)(right.Length * 2), MaximumLength = (ushort)((right.Length + 1) * 2), Buffer = rightMemory };
   if (add) Check(LsaAddAccountRights(policy, sidMemory, new [] {value}, 1));
   else Check(LsaRemoveAccountRights(policy, sidMemory, false, new [] {value}, 1));
  } finally {
   if (rightMemory != IntPtr.Zero) Marshal.FreeHGlobal(rightMemory);
   if (sidMemory != IntPtr.Zero) Marshal.FreeHGlobal(sidMemory);
   if (policy != IntPtr.Zero) LsaClose(policy);
  }
 }
}
'@

function Assert-True([bool]$Condition, [string]$Message) {
 if (-not $Condition) { $script:assertionFailure = $Message; throw $Message }
}
function Assert-PrivateAcl([string]$Path, [string]$AccountSid, [Security.AccessControl.FileSystemRights]$Rights, [bool]$RequireProtected = $true) {
 $acl = Get-Acl -LiteralPath $Path
 if ($RequireProtected) { Assert-True $acl.AreAccessRulesProtected 'ACL inheritance remained enabled.' }
 $expected = @('S-1-5-18','S-1-5-32-544',$AccountSid)
 $found = $false
 foreach ($entry in $acl.Access) {
  $sidValue = $entry.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
  Assert-True ($sidValue -in $expected) 'Unexpected principal can access private installation.'
  Assert-True ($entry.AccessControlType -eq 'Allow') 'Unexpected ACL deny rule.'
  if ($sidValue -eq $AccountSid) {
   Assert-True (($entry.FileSystemRights -band $Rights) -eq $Rights) 'Service account lacks required file rights.'
   if ($Rights -eq [Security.AccessControl.FileSystemRights]::ReadAndExecute) {
    Assert-True (($entry.FileSystemRights -band [Security.AccessControl.FileSystemRights]::Write) -eq 0) 'Service account can modify Program Files.'
   }
   $found = $true
  }
 }
 Assert-True $found 'Service-account ACL absent.'
}

$account = 'mcu' + [Guid]::NewGuid().ToString('N').Substring(0,12)
$sid = $null
$accountCreated = $false
$rightGranted = $false
$installStarted = $false
$failed = $false
$script:assertionFailure = $null
$stage = 'account'
try {
 $randomBytes = [byte[]]::new(36)
 [Security.Cryptography.RandomNumberGenerator]::Fill($randomBytes)
 $secret = ConvertTo-SecureString ('aA1!' + [Convert]::ToBase64String($randomBytes)) -AsPlainText -Force
 [Array]::Clear($randomBytes,0,$randomBytes.Length)
 New-LocalUser -Name $account -Password $secret -AccountNeverExpires -PasswordNeverExpires -UserMayNotChangePassword | Out-Null
 $accountCreated = $true
 $sid = (Get-LocalUser -Name $account).SID.Value
 $qualified = $env:COMPUTERNAME + '\' + $account
 $credential = [Management.Automation.PSCredential]::new($qualified,$secret)
 [NativeSmokeLogonRight]::Set($sid,$true)
 $rightGranted = $true
 $admins = Get-LocalGroupMember -SID 'S-1-5-32-544'
 Assert-True (-not ($admins | Where-Object { $_.SID.Value -eq $sid })) 'Service account is an administrator.'
 $stage = 'installer'
 $installStarted = $true
 & (Join-Path $PSScriptRoot '..\windows\install.ps1') -Binary $Binary -Credential $credential
 $stage = 'SCM and ACL'
 $service = Get-CimInstance Win32_Service -Filter "Name='MinecraftUI'"
 Assert-True ($service.StartName -ieq $qualified) 'SCM service identity differs.'
 Assert-True ($service.PathName -ceq '"C:\Program Files\MinecraftUI\minecraft-ui.exe" serve') 'SCM binary path differs.'
 Assert-PrivateAcl $target $sid ([Security.AccessControl.FileSystemRights]::ReadAndExecute)
 Assert-PrivateAcl (Join-Path $target 'minecraft-ui.exe') $sid ([Security.AccessControl.FileSystemRights]::ReadAndExecute) $false
 Assert-PrivateAcl $data $sid ([Security.AccessControl.FileSystemRights]::Modify)
 Get-FileHash -Algorithm SHA256 -LiteralPath $Binary | Select-Object Algorithm,Hash | ConvertTo-Json | Set-Content (Join-Path $OutputDirectory 'manager-sha256.json')
 & $Binary version | Set-Content (Join-Path $OutputDirectory 'manager-version.txt')
 if ($LASTEXITCODE -ne 0) { throw 'Version command failed.' }
 $stage = 'native start'
 Start-Service MinecraftUI
 (Get-Service MinecraftUI).WaitForStatus('Running',[TimeSpan]::FromSeconds(60))
 $process = Get-CimInstance Win32_Process -Filter "Name='minecraft-ui.exe'" | Where-Object { $_.ExecutablePath -eq (Join-Path $target 'minecraft-ui.exe') }
 Assert-True (@($process).Count -eq 1) 'Expected one installed manager process.'
 $owner = Invoke-CimMethod -InputObject $process -MethodName GetOwner
 Assert-True ($owner.ReturnValue -eq 0 -and $owner.User -eq $account) 'Manager process owner differs.'
 $stage = 'API and service lifecycle'
 & python (Join-Path $PSScriptRoot 'manager_api.py') | Tee-Object -FilePath (Join-Path $OutputDirectory 'api-assertions.txt')
 if ($LASTEXITCODE -ne 0) { throw 'Installed API assertions failed.' }
 Assert-True (Test-Path -LiteralPath (Join-Path $data 'manager.db')) 'Canonical manager database absent.'
 Assert-PrivateAcl (Join-Path $data 'manager.db') $sid ([Security.AccessControl.FileSystemRights]::Modify) $false
 Write-Output 'PASS Windows actual SCM identity, ACL, onboarding, persistence and lifecycle'
} catch {
 $failed = $true
 # Fixed stage and type only; never serialize exception objects/credential.
 Write-Output ('FAIL installed Windows smoke at ' + $stage + ' (' + $_.Exception.GetType().Name + ')')
 if ($script:assertionFailure) { Write-Output ('Assertion: ' + $script:assertionFailure) }
} finally {
 try {
  if ($installStarted) {
   Get-CimInstance Win32_Service -Filter "Name='MinecraftUI'" | Select-Object Name,State,StartMode,ExitCode,ServiceSpecificExitCode | ConvertTo-Json | Set-Content (Join-Path $OutputDirectory 'scm-summary.json')
   # Event diagnostics retain fixed IDs/counts, never arbitrary message bodies.
   @(Get-WinEvent -FilterHashtable @{LogName='System';ProviderName='Service Control Manager';StartTime=(Get-Date).AddMinutes(-20)} -ErrorAction SilentlyContinue) | Group-Object Id | Select-Object Name,Count | ConvertTo-Json | Set-Content (Join-Path $OutputDirectory 'event-summary.json')
   $service = Get-Service MinecraftUI -ErrorAction SilentlyContinue
   if ($service) {
    if ($service.Status -ne 'Stopped') { Stop-Service MinecraftUI; $service.WaitForStatus('Stopped',[TimeSpan]::FromSeconds(160)) }
    & sc.exe delete MinecraftUI | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'SCM delete failed.' }
    $service.Dispose()
   }
   $remaining = @(Get-CimInstance Win32_Process -Filter "Name='minecraft-ui.exe'" | Where-Object { $_.ExecutablePath -eq (Join-Path $target 'minecraft-ui.exe') })
   Assert-True ($remaining.Count -eq 0) 'Orphan installed manager at cleanup.'
   foreach ($path in @($target,$data)) { if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path -Recurse -Force } }
  }
 } catch { $failed = $true; Write-Output 'FAIL installed service/file cleanup' }
 try {
  if ($rightGranted) { [NativeSmokeLogonRight]::Set($sid,$false) }
  if ($accountCreated) { Remove-LocalUser -Name $account }
 } catch { $failed = $true; Write-Output 'FAIL ephemeral account/right cleanup' }
 @{scope='installed-manager-only';failed=$failed;last_stage=$stage} | ConvertTo-Json | Set-Content (Join-Path $OutputDirectory 'result.json')
}
if ($failed) { exit 1 }
