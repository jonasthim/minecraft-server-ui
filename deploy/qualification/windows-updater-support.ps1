# Shared identity assertions copied from the reviewed Task28 acceptance harness.
# Dot-sourcing defines helpers only; it does not create accounts or services.
# Add/remove one account right through LSA. No secedit export, password file,
# credential-bearing command line, process transcript or policy artifact.
Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Security.Principal;
public static class NativeUpdaterLogonRight {
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
function Convert-ProcessAccessEvidence([string]$Body) {
 if ($Body.Length -lt 1 -or $Body.Length -gt 4096) { throw 'Process-access evidence exceeds bounds.' }
 $value = ConvertFrom-Json -InputObject $Body
 $checks = @('query_limited','image_bound','terminate_denied','vm_read_denied','vm_write_denied','vm_operation_denied','duplicate_handle_denied','create_thread_denied','write_dac_denied','write_owner_denied')
 $expected = @('format') + $checks
 $actual = @($value.PSObject.Properties.Name)
 if ($actual.Count -ne $expected.Count -or (Compare-Object ($expected | Sort-Object) ($actual | Sort-Object))) { throw 'Unexpected process-access evidence fields.' }
 if (($value.format -isnot [int] -and $value.format -isnot [long]) -or $value.format -ne 1) { throw 'Unexpected process-access evidence format.' }
 $result = [ordered]@{format=1}
 foreach ($name in $checks) {
  if ($value.$name -isnot [bool] -or -not $value.$name) { throw 'Process-access assertion failed.' }
  $result[$name] = $true
 }
 return $result
}
function Resolve-ServiceAccountSid([string]$AccountName) {
 if ([string]::IsNullOrWhiteSpace($AccountName)) { return $null }
 # SCM's well-known LocalSystem alias resolves through the actual built-in
 # principal, even where NTAccount does not accept that display alias.
 if ($AccountName -ieq 'LocalSystem') {
  $AccountName = ([Security.Principal.SecurityIdentifier]::new('S-1-5-18')).Translate([Security.Principal.NTAccount]).Value
 }
 # SCM may represent a local account as .\user instead of MACHINE\user.
 # Resolve identity through Windows; never accept a name-only match.
 if ($AccountName.StartsWith('.\', [StringComparison]::Ordinal)) {
  $AccountName = $env:COMPUTERNAME + '\' + $AccountName.Substring(2)
 }
 try {
  return ([Security.Principal.NTAccount]::new($AccountName)).Translate([Security.Principal.SecurityIdentifier]).Value
 } catch {
  return $null
 }
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

function Convert-NativeUpdateDiagnostics([object[]]$EventRecords) {
 if ($EventRecords.Count -gt 1024) { throw 'Native update diagnostic event bound exceeded.' }
 $activationStages = 'candidate_fence|journal_absence|prior_hash|prepared_journal|challenge_publish|manager_stop|manager_lock|drain_read|drain_validate|schema_probe|schema_validate|stopped_journal|scratch_check|previous_copy|candidate_copy|executable_fence|candidate_probe|candidate_validate|candidate_hash'
 $probeSteps = 'bin_open|probe_remove|probe_copy|bin_fence|control_open|control_remove|challenge_read|capability_encode|capability_write|service_open|service_config|service_binding|account_sid|configured_sid|recovery_actions|recovery_action_check|service_start|service_query|service_timeout|result_read|result_validate'
 $records = [Collections.Generic.List[object]]::new()
 $counts = [ordered]@{activation_failure=0;probe_failure=0;probe_stopped=0}
 foreach ($record in $EventRecords) {
  $id = [int]$record.Id
  if ($id -notin @(1001,1002,1003)) { continue }
  $properties = @($record.Properties)
  if ($properties.Count -lt 1) { throw 'Native update diagnostic event has no insertion value.' }
  $value = $properties[0].Value
  if ($value -isnot [string] -or $value.Length -gt 256) { throw 'Native update diagnostic insertion value is invalid.' }
  switch ($id) {
   1001 {
    if ($value -cnotmatch ('\Aactivation_stage=(' + $activationStages + ')\z')) { throw 'Native update activation event grammar refused.' }
    $counts.activation_failure++
    $records.Add([ordered]@{event_id=1001;activation_stage=$Matches[1]})
   }
   1002 {
    if ($value -cnotmatch ('\Aprobe_step=(' + $probeSteps + ');win32_code=(unknown|0|[1-9][0-9]{0,9})\z')) { throw 'Native update probe event grammar refused.' }
    $code = $null
    if ($Matches[2] -cne 'unknown') {
     [uint32]$parsedCode = 0
     if (-not [uint32]::TryParse($Matches[2],[ref]$parsedCode)) { throw 'Native update probe error code overflow.' }
     $code = $parsedCode
    }
    $counts.probe_failure++
    $records.Add([ordered]@{event_id=1002;probe_step=$Matches[1];win32_code=$code})
   }
   1003 {
    if ($value -cnotmatch '\Aprobe_state=stopped;win32_exit_code=(0|[1-9][0-9]{0,9});service_specific_exit_code=(0|[1-9][0-9]{0,9})\z') { throw 'Native update stopped event grammar refused.' }
    [uint32]$win32Exit = 0; [uint32]$specificExit = 0
    if (-not [uint32]::TryParse($Matches[1],[ref]$win32Exit) -or -not [uint32]::TryParse($Matches[2],[ref]$specificExit)) { throw 'Native update SCM code overflow.' }
    $counts.probe_stopped++
    $records.Add([ordered]@{event_id=1003;win32_exit_code=$win32Exit;service_specific_exit_code=$specificExit})
   }
  }
 }
 return [ordered]@{format=1;event_counts=$counts;records=$records.ToArray()}
}

function Test-NativeUpdateDiagnosticsParser {
 $good = @(
  [pscustomobject]@{Id=1001;Properties=@([pscustomobject]@{Value='activation_stage=schema_probe'})},
  [pscustomobject]@{Id=1002;Properties=@([pscustomobject]@{Value='probe_step=service_start;win32_code=5'})},
  [pscustomobject]@{Id=1002;Properties=@([pscustomobject]@{Value='probe_step=result_read;win32_code=unknown'})},
  [pscustomobject]@{Id=1003;Properties=@([pscustomobject]@{Value='probe_state=stopped;win32_exit_code=0;service_specific_exit_code=1291845632'})}
 )
 $parsed = Convert-NativeUpdateDiagnostics $good
 if ($parsed.event_counts.activation_failure -ne 1 -or $parsed.event_counts.probe_failure -ne 2 -or $parsed.event_counts.probe_stopped -ne 1 -or $parsed.records.Count -ne 4) { throw 'Native update diagnostic fixture counts differ.' }
 if ($parsed.records[0].activation_stage -cne 'schema_probe' -or $parsed.records[1].win32_code -ne 5 -or $null -ne $parsed.records[2].win32_code -or $parsed.records[3].service_specific_exit_code -ne [uint32]1291845632) { throw 'Native update diagnostic fixture values differ.' }
 foreach ($invalid in @(
  'activation_stage=schema_probe;path=C:\private',
  'probe_step=service_start;win32_code=5;secret=private',
  'probe_step=service_start;win32_code=4294967296',
  'probe_state=stopped;win32_exit_code=0;service_specific_exit_code=4294967296'
 )) {
  $id = 1002
  if ($invalid.StartsWith('activation_stage=',[StringComparison]::Ordinal)) { $id=1001 }
  if ($invalid.StartsWith('probe_state=',[StringComparison]::Ordinal)) { $id=1003 }
  $refused = $false
  try { $null = Convert-NativeUpdateDiagnostics @([pscustomobject]@{Id=$id;Properties=@([pscustomobject]@{Value=$invalid})}) } catch { $refused = $true }
  if (-not $refused) { throw 'Native update diagnostic parser accepted malformed or unbounded evidence.' }
 }
 $tooMany = [object[]]::new(1025)
 $refusedCount = $false
 try { $null = Convert-NativeUpdateDiagnostics $tooMany } catch { $refusedCount = $true }
 if (-not $refusedCount) { throw 'Native update diagnostic parser accepted an unbounded event batch.' }
 return $true
}
