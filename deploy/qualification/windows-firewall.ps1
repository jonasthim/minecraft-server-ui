# Task 33a diagnostic only. This script captures read-only firewall provider
# state, then creates/removes a small fixed set of inbound block rules on a
# fresh hosted Windows Server 2022 runner. It does not run a server, create a
# listener, or change service, account, firewall profile, or firewall policy.
param(
 [string]$OutputDirectory,
 [switch]$PureFixtureOnly
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function ConvertTo-UnsignedInteger([byte[]]$Bytes) {
 $littleEndian = [byte[]]::new($Bytes.Length + 1)
 for ($index = 0; $index -lt $Bytes.Length; $index++) { $littleEndian[$index] = $Bytes[$Bytes.Length - 1 - $index] }
 return [Numerics.BigInteger]::new($littleEndian)
}

function Get-AddressPrefix([string]$Text, [System.Net.Sockets.AddressFamily]$Family) {
 $maximum = 0
 if ($Family -eq [System.Net.Sockets.AddressFamily]::InterNetwork) { $maximum = 32 }
 elseif ($Family -eq [System.Net.Sockets.AddressFamily]::InterNetworkV6) { $maximum = 128 }
 else { throw 'address_family_refused' }
 if ($Text -match '\A[0-9]+\z') {
  $prefix = 0
  if (-not [int]::TryParse($Text,[Globalization.NumberStyles]::None,[Globalization.CultureInfo]::InvariantCulture,[ref]$prefix) -or $prefix -lt 0 -or $prefix -gt $maximum) { throw 'prefix_refused' }
  return $prefix
 }
 if ($Family -ne [System.Net.Sockets.AddressFamily]::InterNetwork) { throw 'mask_family_refused' }
 $mask = $null
 try { $mask = [Net.IPAddress]::Parse($Text) } catch { throw 'mask_refused' }
 if ($mask.AddressFamily -ne [System.Net.Sockets.AddressFamily]::InterNetwork) { throw 'mask_family_refused' }
 $prefix = 0; $zeroSeen = $false
 foreach ($octet in $mask.GetAddressBytes()) {
  for ($bit = 7; $bit -ge 0; $bit--) {
   $set = (($octet -band (1 -shl $bit)) -ne 0)
   if ($set -and $zeroSeen) { throw 'noncontiguous_mask_refused' }
   if ($set) { $prefix++ } else { $zeroSeen = $true }
  }
 }
 return $prefix
}

function ConvertTo-AddressInterval([string]$Text) {
 if ([string]::IsNullOrEmpty($Text) -or $Text.Length -gt 128 -or $Text.Contains('%') -or $Text.Contains('[') -or $Text.Contains(']')) { throw 'address_refused' }
 $startText = $Text; $endText = $Text; $prefixText = $null
 $slash = $Text.IndexOf('/')
 if ($slash -ge 0) {
  if ($slash -eq 0 -or $slash -eq ($Text.Length - 1) -or $Text.IndexOf('/', $slash + 1) -ge 0) { throw 'prefix_refused' }
  $startText = $Text.Substring(0,$slash)
  $prefixText = $Text.Substring($slash + 1)
 }
 $startAddress = $null
 if ($prefixText -ne $null) {
  try { $startAddress = [Net.IPAddress]::Parse($startText) } catch { throw 'address_refused' }
  $prefix = Get-AddressPrefix $prefixText $startAddress.AddressFamily
  $bytes = $startAddress.GetAddressBytes()
  $bits = $bytes.Length * 8
  $value = ConvertTo-UnsignedInteger $bytes
  $hostBits = $bits - $prefix
  $blockSize = [Numerics.BigInteger]::Pow([Numerics.BigInteger]2,$hostBits)
  $start = ($value / $blockSize) * $blockSize
  $end = $start + $blockSize - [Numerics.BigInteger]::One
 } else {
  $dash = $Text.IndexOf('-')
  if ($dash -ge 0) {
   if ($dash -eq 0 -or $dash -eq ($Text.Length - 1) -or $Text.IndexOf('-', $dash + 1) -ge 0) { throw 'range_refused' }
   $startText = $Text.Substring(0,$dash); $endText = $Text.Substring($dash + 1)
   try { $startAddress = [Net.IPAddress]::Parse($startText); $endAddress = [Net.IPAddress]::Parse($endText) } catch { throw 'range_refused' }
   if ($startAddress.AddressFamily -ne $endAddress.AddressFamily) { throw 'range_family_refused' }
   $start = ConvertTo-UnsignedInteger $startAddress.GetAddressBytes()
   $end = ConvertTo-UnsignedInteger $endAddress.GetAddressBytes()
   if ($start -gt $end) { throw 'range_order_refused' }
  } else {
   try { $startAddress = [Net.IPAddress]::Parse($startText) } catch { throw 'address_refused' }
   $start = ConvertTo-UnsignedInteger $startAddress.GetAddressBytes()
   $end = $start
  }
 }
 $family = 0
 if ($startAddress.AddressFamily -eq [System.Net.Sockets.AddressFamily]::InterNetwork) { $family = 4 }
 elseif ($startAddress.AddressFamily -eq [System.Net.Sockets.AddressFamily]::InterNetworkV6) { $family = 6 }
 else { throw 'address_family_refused' }
 return [pscustomobject]@{Family=$family;Start=$start;End=$end}
}

function ConvertTo-NormalizedAddressSet([object[]]$Addresses) {
 $intervals = [Collections.Generic.List[object]]::new()
 foreach ($address in $Addresses) { $intervals.Add((ConvertTo-AddressInterval ([string]$address))) }
 $sorted = @($intervals.ToArray() | Sort-Object Family,Start,End)
 $merged = [Collections.Generic.List[object]]::new()
 foreach ($interval in $sorted) {
  if ($merged.Count -eq 0) { $merged.Add($interval); continue }
  $last = $merged[$merged.Count - 1]
  if ($last.Family -eq $interval.Family -and $interval.Start -le ($last.End + [Numerics.BigInteger]::One)) {
   if ($interval.End -gt $last.End) { $last.End = $interval.End }
  } else { $merged.Add($interval) }
 }
 $result = [Collections.Generic.List[string]]::new()
 $culture = [Globalization.CultureInfo]::InvariantCulture
 foreach ($interval in $merged) { $result.Add(($interval.Family.ToString($culture)+':'+$interval.Start.ToString($culture)+':'+$interval.End.ToString($culture))) }
 return $result.ToArray()
}

function Test-FirewallAddressNormalization {
 $original = @('0.0.0.0-126.255.255.255','128.0.0.0-255.255.255.255','::/0')
 $cidr = @('128.0.0.0/1','0.0.0.0/2','64.0.0.0/3','96.0.0.0/4','112.0.0.0/5','120.0.0.0/6','124.0.0.0/7','126.0.0.0/8','::/0')
 $ipv6Split = @('::/1','8000::/1')
 $combinedIPv6Split = @('0.0.0.0-126.255.255.255','128.0.0.0-255.255.255.255','::/1','8000::/1')
 $expected = @('4:0:2130706431','4:2147483648:4294967295','6:0:340282366920938463463374607431768211455')
 $expectedIPv6 = @('6:0:340282366920938463463374607431768211455')
 $left = @(ConvertTo-NormalizedAddressSet $original)
 $right = @(ConvertTo-NormalizedAddressSet $cidr)
 $split = @(ConvertTo-NormalizedAddressSet $ipv6Split)
 $combined = @(ConvertTo-NormalizedAddressSet $combinedIPv6Split)
 if (($left -join '|') -cne ($expected -join '|') -or ($right -join '|') -cne ($expected -join '|') -or ($split -join '|') -cne ($expectedIPv6 -join '|') -or ($combined -join '|') -cne ($expected -join '|')) { throw 'firewall address equivalence fixture failed' }
 foreach ($invalid in @('C:\private\path','10.0.0.0/255.0.255.0','::/129','1.2.3.4-1.2.3.3','127.0.0.1%7')) {
  $refused = $false
  try { $null = ConvertTo-NormalizedAddressSet @($invalid) } catch { $refused = $true }
  if (-not $refused) { throw 'firewall address normalizer accepted malformed fixture' }
 }
 return $true
}

function Test-NetSecurityCommandAvailable([object[]]$Commands, [string]$ExpectedName) {
 foreach ($command in $Commands) {
  if ($command.Name -cne $ExpectedName -or $command.CommandType -notin @('Cmdlet','Function')) { continue }
  if ($command.ModuleName -ceq 'NetSecurity' -or $command.Source -ceq 'NetSecurity') { return $true }
 }
 return $false
}

function Test-FirewallCommandAvailabilityFixture {
 $cdxmlFunction = [pscustomobject]@{Name='Get-NetFirewallRule';CommandType='Function';ModuleName='NetSecurity';Source='NetSecurity'}
 $providerCmdlet = [pscustomobject]@{Name='New-NetFirewallRule';CommandType='Cmdlet';ModuleName='NetSecurity';Source='NetSecurity'}
 $unrelated = [pscustomobject]@{Name='Get-NetFirewallRule';CommandType='Function';ModuleName='PrivateModule';Source='PrivateModule'}
 if (-not (Test-NetSecurityCommandAvailable @($cdxmlFunction) 'Get-NetFirewallRule')) { throw 'NetSecurity function fixture failed' }
 if (-not (Test-NetSecurityCommandAvailable @($providerCmdlet) 'New-NetFirewallRule')) { throw 'NetSecurity cmdlet fixture failed' }
 if (Test-NetSecurityCommandAvailable @($unrelated) 'Get-NetFirewallRule') { throw 'unrelated firewall command fixture failed' }
 if (Test-NetSecurityCommandAvailable @($cdxmlFunction) 'New-NetFirewallRule') { throw 'mismatched firewall command fixture failed' }
 return $true
}

if (-not (Test-FirewallAddressNormalization)) { throw 'firewall address normalization fixture failed' }
if (-not (Test-FirewallCommandAvailabilityFixture)) { throw 'firewall command availability fixture failed' }
if ($PureFixtureOnly) { [Console]::Out.WriteLine('firewall pure fixtures passed'); exit 0 }

if ($env:GITHUB_ACTIONS -ne 'true' -or $env:RUNNER_ENVIRONMENT -ne 'github-hosted') { throw 'fresh hosted runner required' }
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { throw 'Windows Server 2022 runner required' }
$operatingSystem = Get-CimInstance Win32_OperatingSystem -ErrorAction Stop
if ($operatingSystem.Caption -notmatch '\AMicrosoft Windows Server 2022\b') { throw 'Windows Server 2022 runner required' }
$principal = [Security.Principal.WindowsPrincipal]::new([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'administrator required' }
if ([string]::IsNullOrWhiteSpace($OutputDirectory) -or [string]::IsNullOrWhiteSpace($env:GITHUB_WORKSPACE)) { throw 'private output directory required' }
$workspace = [IO.Path]::GetFullPath($env:GITHUB_WORKSPACE).TrimEnd('\')
$outputPath = [IO.Path]::GetFullPath($OutputDirectory).TrimEnd('\')
if (-not $outputPath.StartsWith(($workspace+'\'),[StringComparison]::OrdinalIgnoreCase)) { throw 'private output directory must be under workspace' }
$pathCursor = $outputPath
while ($pathCursor.StartsWith(($workspace+'\'),[StringComparison]::OrdinalIgnoreCase) -or $pathCursor -ieq $workspace) {
 $pathItem = Get-Item -LiteralPath $pathCursor -Force -ErrorAction Stop
 if (($pathItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'ordinary output path required' }
 if ($pathCursor -ieq $workspace) { break }
 $pathCursor = [IO.Path]::GetDirectoryName($pathCursor).TrimEnd('\')
}
if ($pathCursor -ine $workspace -or -not $pathItem.PSIsContainer) { throw 'ordinary output directory required' }
if (@(Get-ChildItem -LiteralPath $outputPath -Force -ErrorAction Stop).Count -ne 0) { throw 'firewall output directory must be empty' }
$reportPath = Join-Path $outputPath 'firewall-summary.json'

$script:diagnosticErrors = [Collections.Generic.List[string]]::new()
function Get-FixedFailureCategory([System.Management.Automation.ErrorRecord]$Record) {
 $type = $Record.Exception.GetType().Name
 if ($type -eq 'ParameterBindingValidationException' -or $type -eq 'ParameterBindingException') { return 'parameter_binding' }
 if ($type -eq 'CimException') {
  $native = $Record.Exception.PSObject.Properties['NativeErrorCode']
  if ($null -ne $native -and $null -ne $native.Value) {
   try {
    switch ([Convert]::ToInt64($native.Value,[Globalization.CultureInfo]::InvariantCulture)) {
     5 { return 'cim_access_denied' }
     87 { return 'cim_invalid_parameter' }
     1168 { return 'cim_not_found' }
    }
   } catch { }
  }
  return 'cim_other'
 }
 $id = [string]$Record.FullyQualifiedErrorId
 if ($id -match '(?i)ProviderNotFound|InvalidNamespace|ClassNotFound') { return 'provider_unavailable' }
 if ($id -match '(?i)AccessDenied|UnauthorizedAccess') { return 'access_denied' }
 return 'other'
}

function Get-ServiceAvailability([string]$Name) {
 try {
  $service = Get-Service -Name $Name -ErrorAction Stop
  switch ([string]$service.Status) {
   'Running' { return 'running' }
   'Stopped' { return 'stopped' }
   'StartPending' { return 'start_pending' }
   'StopPending' { return 'stop_pending' }
   'Paused' { return 'paused' }
   default { return 'other' }
  }
 } catch {
  $script:diagnosticErrors.Add((Get-FixedFailureCategory $_))
  if ($_.Exception.GetType().Name -eq 'ServiceCommandException') { return 'not_found' }
  return 'query_failed'
 }
}

$providerAvailable = $false
try {
 $providerClass = @(Get-CimClass -Namespace 'root/standardcimv2' -ClassName 'MSFT_NetFirewallRule' -ErrorAction Stop)
 $providerAvailable = ($providerClass.Count -eq 1)
} catch { $script:diagnosticErrors.Add((Get-FixedFailureCategory $_)) }
$providerCmdlets = [ordered]@{}
foreach ($commandName in @('Get-NetFirewallRule','New-NetFirewallRule','Get-NetFirewallAddressFilter')) {
 $commands = @(Get-Command -Name $commandName -All -CommandType Cmdlet,Function -ErrorAction SilentlyContinue)
 $providerCmdlets[$commandName] = Test-NetSecurityCommandAvailable $commands $commandName
}
$availability = [ordered]@{
 bfe=(Get-ServiceAvailability 'BFE')
 mpssvc=(Get-ServiceAvailability 'MpsSvc')
 provider_class_available=$providerAvailable
 provider_cmdlets=$providerCmdlets
 diagnostic_error_categories=@($script:diagnosticErrors.ToArray() | Select-Object -Unique)
}

$caseDefinitions = @(
 [pscustomobject]@{Label='minimal_block';Name='MinecraftUI-Task33a-Firewall-Probe-Minimal';IncludeRemoteAddress=$false;Addresses=@()},
 [pscustomobject]@{Label='full_without_remote';Name='MinecraftUI-Task33a-Firewall-Probe-NoRemote';IncludeRemoteAddress=$false;Addresses=@()},
 [pscustomobject]@{Label='ipv4_ranges';Name='MinecraftUI-Task33a-Firewall-Probe-IPv4';IncludeRemoteAddress=$true;Addresses=@('0.0.0.0-126.255.255.255','128.0.0.0-255.255.255.255')},
 [pscustomobject]@{Label='ipv6_any';Name='MinecraftUI-Task33a-Firewall-Probe-IPv6';IncludeRemoteAddress=$true;Addresses=@('::/0')},
 [pscustomobject]@{Label='original';Name='MinecraftUI-Task33a-Firewall-Probe-Original';IncludeRemoteAddress=$true;Addresses=@('0.0.0.0-126.255.255.255','128.0.0.0-255.255.255.255','::/0')},
 [pscustomobject]@{Label='cidr_equivalent';Name='MinecraftUI-Task33a-Firewall-Probe-CIDR';IncludeRemoteAddress=$true;Addresses=@('128.0.0.0/1','0.0.0.0/2','64.0.0.0/3','96.0.0.0/4','112.0.0.0/5','120.0.0.0/6','124.0.0.0/7','126.0.0.0/8','::/0')},
 [pscustomobject]@{Label='ipv6_split';Name='MinecraftUI-Task33a-Firewall-Probe-IPv6Split';IncludeRemoteAddress=$true;Addresses=@('::/1','8000::/1')},
 [pscustomobject]@{Label='ipv4_ranges_ipv6_split';Name='MinecraftUI-Task33a-Firewall-Probe-CombinedIPv6Split';IncludeRemoteAddress=$true;Addresses=@('0.0.0.0-126.255.255.255','128.0.0.0-255.255.255.255','::/1','8000::/1')}
)
$precheckPassed = $true
$precheckErrorCategory = $null
$preexistingName = $false
try {
 foreach ($definition in $caseDefinitions) {
  $existing = @(Get-NetFirewallRule -ErrorAction Stop | Where-Object { $_.Name -ceq $definition.Name })
  if ($existing.Count -ne 0) { $preexistingName = $true; break }
 }
} catch {
 $precheckPassed = $false
 $precheckErrorCategory = Get-FixedFailureCategory $_
}
if ($preexistingName) { throw 'fixed firewall probe name already exists' }
$availability.name_prechecks_passed = $precheckPassed
$availability.precheck_error_category = $precheckErrorCategory

function New-NumericCimCodes {
 return [ordered]@{hresult=[Collections.Generic.List[long]]::new();native_error_code=[Collections.Generic.List[long]]::new();status_code=[Collections.Generic.List[long]]::new()}
}
function Add-NumericCimCodes([System.Collections.IDictionary]$Target, [System.Exception]$Exception) {
 $current = $Exception
 for ($depth = 0; $null -ne $current -and $depth -lt 4; $depth++) {
  $null = $Target.hresult.Add([long]$current.HResult)
  foreach ($name in @('NativeErrorCode','StatusCode')) {
   $property = $current.PSObject.Properties[$name]
   if ($null -eq $property -or $null -eq $property.Value) { continue }
   try { $code = [Convert]::ToInt64($property.Value,[Globalization.CultureInfo]::InvariantCulture) } catch { continue }
   if ($name -ceq 'NativeErrorCode') { $null = $Target.native_error_code.Add($code) }
   else { $null = $Target.status_code.Add($code) }
  }
  $current = $current.InnerException
 }
}

$caseResults = [Collections.Generic.List[object]]::new()
$safetyFailure = -not $precheckPassed
$cleanupHalted = $false
foreach ($definition in $caseDefinitions) {
 $attemptCase = $precheckPassed -and -not $cleanupHalted
 $result = [ordered]@{case=$definition.Label;attempted=$attemptCase;created=$false;cim_codes=(New-NumericCimCodes);failure_categories=[Collections.Generic.List[string]]::new();exact_match_normalized_address_set=$null;cleanup_verified=$null}
 if (-not $attemptCase) {
  if (-not $precheckPassed) { $result.failure_categories.Add($precheckErrorCategory) }
  else { $result.failure_categories.Add('prior_cleanup_unverified') }
  $result.failure_categories = @($result.failure_categories.ToArray())
  $caseResults.Add($result)
  continue
 }
 $result.cleanup_verified = $false
 try {
  try {
   if ($definition.Label -eq 'minimal_block') {
    New-NetFirewallRule -Name $definition.Name -DisplayName $definition.Name -Direction Inbound -Action Block -Protocol TCP -LocalPort 25565 -ErrorAction Stop | Out-Null
   } elseif ($definition.IncludeRemoteAddress) {
    New-NetFirewallRule -Name $definition.Name -DisplayName $definition.Name -Direction Inbound -Action Block -Protocol TCP -LocalPort 25565,8080 -RemoteAddress $definition.Addresses -Profile Any -Enabled True -ErrorAction Stop | Out-Null
   } else {
    New-NetFirewallRule -Name $definition.Name -DisplayName $definition.Name -Direction Inbound -Action Block -Protocol TCP -LocalPort 25565,8080 -Profile Any -Enabled True -ErrorAction Stop | Out-Null
   }
   $result.created = $true
  } catch {
   Add-NumericCimCodes $result.cim_codes $_.Exception
   $result.failure_categories.Add((Get-FixedFailureCategory $_))
  }
  try {
   $rules = @(Get-NetFirewallRule -ErrorAction Stop | Where-Object { $_.Name -ceq $definition.Name })
   if ($rules.Count -gt 0) { $result.created = $true }
   if ($rules.Count -eq 1) {
    $filters = @($rules | Get-NetFirewallAddressFilter -ErrorAction Stop)
    if ($filters.Count -eq 1 -and $definition.IncludeRemoteAddress) {
     $actual = @(ConvertTo-NormalizedAddressSet @($filters[0].RemoteAddress))
     $expected = @(ConvertTo-NormalizedAddressSet $definition.Addresses)
     $result.exact_match_normalized_address_set = (($actual -join '|') -ceq ($expected -join '|'))
    }
   }
  } catch {
   Add-NumericCimCodes $result.cim_codes $_.Exception
   $result.failure_categories.Add((Get-FixedFailureCategory $_))
  }
 } finally {
  # The name was absent immediately before this case. Any matching object now
  # belongs to this attempt, including a cmdlet failure after partial creation.
  try {
   $owned = @(Get-NetFirewallRule -ErrorAction Stop | Where-Object { $_.Name -ceq $definition.Name })
   if ($owned.Count -gt 0) {
    $result.created = $true
    Remove-NetFirewallRule -InputObject $owned -ErrorAction Stop
   }
   $remaining = @(Get-NetFirewallRule -ErrorAction Stop | Where-Object { $_.Name -ceq $definition.Name })
   $result.cleanup_verified = ($remaining.Count -eq 0)
  } catch {
   Add-NumericCimCodes $result.cim_codes $_.Exception
   $result.failure_categories.Add((Get-FixedFailureCategory $_))
   $result.cleanup_verified = $false
  }
 }
 foreach ($key in @('hresult','native_error_code','status_code')) { $result.cim_codes[$key] = @($result.cim_codes[$key].ToArray() | Select-Object -Unique) }
 $result.failure_categories = @($result.failure_categories.ToArray() | Select-Object -Unique)
 if (-not $result.cleanup_verified) { $safetyFailure = $true; $cleanupHalted = $true }
 $caseResults.Add($result)
}

$json = ConvertTo-Json -Depth 6 -InputObject ([ordered]@{availability=$availability;cases=$caseResults.ToArray()})
$encoding = [Text.UTF8Encoding]::new($false)
$bytes = $encoding.GetBytes($json)
$stream = [IO.File]::Open($reportPath,[IO.FileMode]::CreateNew,[IO.FileAccess]::Write,[IO.FileShare]::None)
try { $stream.Write($bytes,0,$bytes.Length) } finally { $stream.Dispose() }
if ($safetyFailure) { exit 1 }
exit 0
