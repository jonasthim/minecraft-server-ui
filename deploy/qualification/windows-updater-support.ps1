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
