# Hosted Windows source behavior gate. No acquired executable is ever invoked.
# Use actual producer outputs, not arbitrary remote downloads. All generated
# validation, hash, ACL, native argv and cleanup behavior remains under test.
# The two explicit test seams replace only download I/O and final native launch.
#Requires -Version 5.1
param(
    [Parameter(Mandatory=$true)][string]$EntryScript,
    [Parameter(Mandatory=$true)][string]$PayloadPath,
    [switch]$Development
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Assert-NativeArgvJson([string]$Json, [string[]]$Expected, [switch]$LegacySemanticsFixture) {
    if ($LegacySemanticsFixture) {
        try { $argv = ConvertFrom-Json51Fixture $Json } catch { throw 'native argv JSON refused' }
    } else {
        try { $argv = ConvertFrom-Json -InputObject $Json } catch { throw 'native argv JSON refused' }
    }
    if ($argv -isnot [System.Array]) { throw 'native argv was not an array' }
    if ($argv.Count -ne $Expected.Count) { throw 'native argv count mismatch' }
    for ([int]$index = 0; $index -lt $Expected.Count; $index++) {
        if ($argv[$index] -isnot [string] -or $argv[$index] -cne $Expected[$index]) { throw 'native argv member mismatch' }
    }
}

# Reproduce Windows PowerShell 5.1's historical single-object array emission
# under either engine, then prove why the old @(...)[6] capture was invalid.
function ConvertFrom-Json51Fixture([string]$Json) {
    $value = ConvertFrom-Json -InputObject $Json
    Write-Output -NoEnumerate $value
}
$argvFixtureJson = '["prepare","--repository","fixture/repo","--trust-file","C:\\fixture\\trust.json","--version","v1.2.3","--output","C:\\fixture\\output"]'
$argvFixtureExpected = @('prepare','--repository','fixture/repo','--trust-file','C:\fixture\trust.json','--version','v1.2.3','--output','C:\fixture\output')
$legacyArgv = @(ConvertFrom-Json51Fixture $argvFixtureJson)
if ($legacyArgv.Count -ne 1 -or $legacyArgv[0] -isnot [System.Array] -or $legacyArgv[0].Count -ne $argvFixtureExpected.Count) { throw 'PowerShell 5.1 argv behavior fixture drift' }
$baselineAssertionFailed = $false
try { $baselineAssertionFailed = $legacyArgv[6] -cne 'v1.2.3' -or $legacyArgv[8] -cne 'C:\fixture\output' }
catch [System.IndexOutOfRangeException] { $baselineAssertionFailed = $true }
if (-not $baselineAssertionFailed) { throw 'PowerShell 5.1 argv baseline no longer reproduces its invalid indexed assertions' }
Assert-NativeArgvJson $argvFixtureJson $argvFixtureExpected
Assert-NativeArgvJson $argvFixtureJson $argvFixtureExpected -LegacySemanticsFixture
foreach ($legacySemantics in @($false,$true)) {
    foreach ($invalidArgvJson in @('{', '["prepare","--repository"]', $argvFixtureJson.Replace('v1.2.3','v1.2.4'))) {
        $refused = $false
        try {
            if ($legacySemantics) { Assert-NativeArgvJson $invalidArgvJson $argvFixtureExpected -LegacySemanticsFixture }
            else { Assert-NativeArgvJson $invalidArgvJson $argvFixtureExpected }
        } catch { $refused = $true }
        if (-not $refused) { throw 'malformed, truncated or changed native argv accepted' }
    }
}

$source = [System.IO.File]::ReadAllText($EntryScript).Replace("`r`n","`n")
$dispositionImport = '        [DllImport("kernel32.dll", SetLastError=true)]' + "`n" + '        static extern bool SetFileInformationByHandle(SafeFileHandle handle, uint kind, ref Disposition information, uint size);'
foreach ($needle in @('public static class PrivateIO {',$dispositionImport,'        [DllImport("kernel32.dll")]','            System.Threading.Thread.Sleep(100);')) {
    if (($source.Split(@($needle),[StringSplitOptions]::None)).Count -ne 2) { throw 'directory retry test seam drift' }
}
$source = $source.Replace('public static class PrivateIO {', @'
public static class PrivateIO {
        public static Action RetryPauseForTest;
        public static int TestDispositionFailures;
        public static int TestDispositionError;
        public static int TestDispositionSkip;
        public static int TestDispositionInjected;
        public static string TestDispositionMarker;
'@)
$nativeDispositionImport = '        [DllImport("kernel32.dll", EntryPoint="SetFileInformationByHandle", SetLastError=true)]' + "`n" + '        static extern bool NativeSetFileInformationByHandle(SafeFileHandle handle, uint kind, ref Disposition information, uint size);'
$source = $source.Replace($dispositionImport,$nativeDispositionImport)
if (($source.Split(@($nativeDispositionImport),[StringSplitOptions]::None)).Count -ne 2) { throw 'native disposition retry test seam lost its Win32 entrypoint' }
$source = $source.Replace('        [DllImport("kernel32.dll")]', @'
        [DllImport("kernel32.dll", SetLastError=true)]
        static extern void SetLastError(uint error);
        static bool SetFileInformationByHandle(SafeFileHandle handle, uint kind, ref Disposition information, uint size) {
            if (TestDispositionSkip > 0) { TestDispositionSkip--; return NativeSetFileInformationByHandle(handle,kind,ref information,size); }
            if (TestDispositionFailures > 0) { TestDispositionFailures--; TestDispositionInjected++; if (TestDispositionMarker != null) System.IO.File.WriteAllText(TestDispositionMarker,TestDispositionInjected.ToString(System.Globalization.CultureInfo.InvariantCulture)); SetLastError((uint)TestDispositionError); return false; }
            return NativeSetFileInformationByHandle(handle,kind,ref information,size);
        }
        [DllImport("kernel32.dll")]
'@)
$source = $source.Replace('            System.Threading.Thread.Sleep(100);','            if (RetryPauseForTest != null) RetryPauseForTest(); else System.Threading.Thread.Sleep(100);')
$tokens = $null; $parseErrors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseInput($source,[ref]$tokens,[ref]$parseErrors)
if ($parseErrors.Count -ne 0) { throw 'generated PowerShell failed parsing' }
# Execute the actual pure redirect policy with adversarial inputs, without HTTP.
$policy = @($ast.FindAll({param($n) $n -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -in @('Refuse','Assert-Redirect')},$true))
if ($policy.Count -ne 2) { throw 'redirect policy seam drift' }
foreach ($function in $policy) { . ([ScriptBlock]::Create($function.Extent.Text)) }
$allowed = 'https://release-assets.githubusercontent.com/fixed?token=private-secret'
if ((Assert-Redirect $allowed) -cne $allowed) { throw 'valid signed redirect changed' }
foreach ($bad in @('https://attacker.invalid/fixed?token=private-secret','http://release-assets.githubusercontent.com/fixed','https://user:password@release-assets.githubusercontent.com/fixed','https://release-assets.githubusercontent.com.attacker.invalid/fixed','https://release-assets.githubusercontent.com:444/fixed')) {
    $rejected = $false
    try { $null = Assert-Redirect $bad } catch { $rejected = $true; if ($_.Exception.Message.Contains('private-secret')) { throw 'redirect exception leak' } }
    if (-not $rejected) { throw 'unsafe redirect accepted' }
}
$creation = @($ast.FindAll({param($n) $n -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq 'New-PrivateScratchFile'},$true))
if ($creation.Count -ne 1) { throw 'private creation seam drift' }
$initialization = @($ast.FindAll({param($n) $n -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq 'Initialize-PrivateIO'},$true))
if ($initialization.Count -ne 1) { throw 'compiler initialization seam drift' }
$observedInitialization = $initialization[0].Extent.Text.Replace('function Initialize-PrivateIO','function Initialize-ShippingPrivateIO') + @'

function Initialize-PrivateIO {
    if ([Environment]::GetEnvironmentVariable('TEMP',[EnvironmentVariableTarget]::Process) -cne $scratch -or [Environment]::GetEnvironmentVariable('TMP',[EnvironmentVariableTarget]::Process) -cne $scratch) { throw 'compiler was not directed into private scratch' }
    Assert-Scratch $scratch $scratchIdentity
    Initialize-ShippingPrivateIO
}
'@
$observedCreation = $creation[0].Extent.Text.Replace('function New-PrivateScratchFile','function New-ShippingPrivateScratchFile') + @'

function New-PrivateScratchFile([string]$Name) {
    $path = New-ShippingPrivateScratchFile $Name
    if ([Environment]::GetEnvironmentVariable('TEMP',[EnvironmentVariableTarget]::Process) -cne $env:OUTER_TEST_ORIGINAL_TEMP -or [Environment]::GetEnvironmentVariable('TMP',[EnvironmentVariableTarget]::Process) -cne $env:OUTER_TEST_ORIGINAL_TMP) { throw 'compiler temporary environment was not restored' }
    # Observe the ACTUAL newly created file before any caller writes to it.
    # Expected ownership/rights are independently derived, not taken from the
    # producer's expected SDDL or its Win32 security-comparison implementation.
    $sidType = [System.Security.Principal.SecurityIdentifier]
    $actual = Get-Acl -LiteralPath $path
    $who = [System.Security.Principal.WindowsIdentity]::GetCurrent()
    $adminToken = [System.Security.Principal.WindowsPrincipal]::new($who).IsInRole([System.Security.Principal.WindowsBuiltInRole]::Administrator)
    $expectedOwner = $who.User.Value
    $allowed = @('S-1-5-32-544','S-1-5-18')
    if ($adminToken) { $expectedOwner = 'S-1-5-32-544' } else { $allowed += $who.User.Value }
    $actualOwner = $actual.GetOwner($sidType).Value
    if ($actualOwner -cne $expectedOwner -or -not $actual.AreAccessRulesProtected) { throw 'atomic scratch child owner/protection mismatch' }
    $rules = @($actual.GetAccessRules($true,$true,$sidType))
    if ($rules.Count -ne $allowed.Count) { throw 'atomic scratch child rule count mismatch' }
    $seen = @{}
    foreach ($rule in $rules) {
        $principal = $rule.IdentityReference.Value
        if ($principal -cnotin $allowed -or $seen.ContainsKey($principal) -or $rule.IsInherited -or $rule.AccessControlType -ne [System.Security.AccessControl.AccessControlType]::Allow -or $rule.FileSystemRights -ne [System.Security.AccessControl.FileSystemRights]::FullControl -or $rule.InheritanceFlags -ne [System.Security.AccessControl.InheritanceFlags]::None -or $rule.PropagationFlags -ne [System.Security.AccessControl.PropagationFlags]::None) { throw 'atomic scratch child has broader or inherited rights' }
        $seen.Add($principal,$true)
    }
    [System.IO.File]::AppendAllText($env:OUTER_TEST_ACLS,($Name+'|'+$actualOwner+'|'+($allowed -join ',')+[Environment]::NewLine))
    return $path
}
'@
$download = @($ast.FindAll({param($n) $n -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq 'Receive-Preparer'},$true))
if ($download.Count -ne 1) { throw 'download seam drift' }
$fakeDownload = @'
function Receive-Preparer([string]$Url, [System.IO.Stream]$Destination, [long]$ExpectedSize) {
    [System.IO.File]::WriteAllText($env:OUTER_TEST_SCRATCH,$scratch)
    if ($Url -cne $env:OUTER_EXPECTED_URL) { throw 'unpinned preparer URL' }
    if ($env:OUTER_TEST_MODE -eq 'error') { throw 'private-secret?token=private-secret' }
    $body = [System.IO.File]::ReadAllBytes($env:OUTER_TEST_PAYLOAD)
    if ($env:OUTER_TEST_MODE -eq 'tamper') { $body[0] = $body[0] -bxor 1 }
    if ($env:OUTER_TEST_MODE -eq 'oversize') { $body = [byte[]]($body + @(0)) }
    if ($env:OUTER_TEST_MODE -eq 'truncate') { $body = [byte[]]($body[0..($body.Length-2)]) }
    $Destination.Write($body,0,$body.Length)
}
'@
$source = $source.Remove($download[0].Extent.StartOffset,$download[0].Extent.EndOffset-$download[0].Extent.StartOffset).Insert($download[0].Extent.StartOffset,$fakeDownload)
$source = $source.Replace($creation[0].Extent.Text,$observedCreation)
$source = $source.Replace($initialization[0].Extent.Text,$observedInitialization)
$native = '& $binary @nativeArgs 1> $resultPath 2> $errorPath'
if (($source.Split(@($native),[StringSplitOptions]::None)).Count -ne 2) { throw 'runner seam drift' }
$fakeRunner = @'
[System.IO.File]::WriteAllText($env:OUTER_TEST_ARGS,(ConvertTo-Json -InputObject $nativeArgs -Compress))
[System.IO.File]::WriteAllText($resultPath,'{"prepared":true}')
if ($env:OUTER_TEST_MODE -eq 'foreign-child') { [System.IO.File]::WriteAllText([System.IO.Path]::Combine($scratch,'unowned.txt'),'preserve foreign child') }
if ($env:OUTER_TEST_MODE -in @('delete-pending-child','parent-substitution','changed-directory-acl')) {
    $transientPath = [System.IO.Path]::Combine($scratch,'transient.txt')
    [System.IO.File]::WriteAllText($transientPath,'transient child')
    $global:outerTransientHandle = [System.IO.File]::Open($transientPath,[System.IO.FileMode]::Open,[System.IO.FileAccess]::ReadWrite,[System.IO.FileShare]::ReadWrite -bor [System.IO.FileShare]::Delete)
    [System.IO.File]::Delete($transientPath)
    if ($global:outerTransientHandle.SafeFileHandle.IsClosed -or (Test-Path -LiteralPath $transientPath)) { throw 'delete-pending child fixture did not retain its shared handle after path deletion' }
    [MinecraftUiOuter.PrivateIO]::RetryPauseForTest = [System.Action]{
        $global:outerTransientHandle.Dispose()
        $global:outerTransientHandle = $null
        $actualScratch = [System.IO.File]::ReadAllText($env:OUTER_TEST_SCRATCH)
        if ($env:OUTER_TEST_MODE -eq 'parent-substitution') {
            [System.IO.Directory]::Move($actualScratch,$actualScratch+'.moved')
            [void][System.IO.Directory]::CreateDirectory($actualScratch)
            [System.IO.File]::WriteAllText([System.IO.Path]::Combine($actualScratch,'replacement.txt'),'preserve replacement')
        } elseif ($env:OUTER_TEST_MODE -eq 'changed-directory-acl') {
            $security = Get-Acl -LiteralPath $actualScratch
            $everyone = [System.Security.Principal.SecurityIdentifier]::new('S-1-1-0')
            $rule = [System.Security.AccessControl.FileSystemAccessRule]::new($everyone,[System.Security.AccessControl.FileSystemRights]::ReadAndExecute,[System.Security.AccessControl.AccessControlType]::Allow)
            $security.AddAccessRule($rule)
            Set-Acl -LiteralPath $actualScratch -AclObject $security
        }
        [System.IO.File]::WriteAllText($env:OUTER_TEST_RETRY_MARKER,'retry reached')
    }
}
if ($env:OUTER_TEST_MODE -in @('delete-pending-child','parent-substitution','changed-directory-acl','non145-file-disposition','non145-directory-disposition')) {
    [MinecraftUiOuter.PrivateIO]::TestDispositionFailures = 1
    [MinecraftUiOuter.PrivateIO]::TestDispositionError = 145
    [MinecraftUiOuter.PrivateIO]::TestDispositionMarker = $env:OUTER_TEST_DISPOSITION_MARKER
    if ($env:OUTER_TEST_MODE -in @('delete-pending-child','parent-substitution','changed-directory-acl','non145-directory-disposition')) { [MinecraftUiOuter.PrivateIO]::TestDispositionSkip = 4 }
}
if ($env:OUTER_TEST_MODE -in @('non145-file-disposition','non145-directory-disposition')) {
    [MinecraftUiOuter.PrivateIO]::TestDispositionError = 5
}
if ($env:OUTER_TEST_MODE -eq 'changed-child-owner') {
    $security = Get-Acl -LiteralPath $trustPath
    $security.SetOwner([System.Security.Principal.WindowsIdentity]::GetCurrent().User)
    Set-Acl -LiteralPath $trustPath -AclObject $security
}
# Every retained child handle must forbid path replacement through invocation.
try { [System.IO.File]::Move($binary,$binary+'.moved'); throw 'binary handle allowed rename' } catch [System.IO.IOException] {}
$LASTEXITCODE = 0
'@
$source = $source.Replace($native,$fakeRunner)
$root = [System.IO.Path]::Combine([System.IO.Path]::GetTempPath(),('outer-behavior-' + [Guid]::NewGuid().ToString('N')))
$null = [System.IO.Directory]::CreateDirectory($root)
try {
    $path = [System.IO.Path]::Combine($root,'entry.ps1')
    [System.IO.File]::WriteAllText($path,$source)
    $env:OUTER_TEST_PAYLOAD = [System.IO.Path]::GetFullPath($PayloadPath)
    $env:OUTER_TEST_ORIGINAL_TEMP = [Environment]::GetEnvironmentVariable('TEMP',[EnvironmentVariableTarget]::Process)
    $env:OUTER_TEST_ORIGINAL_TMP = [Environment]::GetEnvironmentVariable('TMP',[EnvironmentVariableTarget]::Process)
    # Extract only the generated fixed URL, never supply a production URL override.
    $match = [regex]::Match($source,"Receive-Preparer '(https://github.com/([^/]+/[^/]+)/releases/[^']+)' ")
    if (-not $match.Success) { throw 'URL seam drift' }
    $expectedRepository = $match.Groups[2].Value
    $env:OUTER_EXPECTED_URL = $match.Groups[1].Value
    $cases = @('good','tamper','oversize','truncate','error','malicious-version','foreign-child','delete-pending-child','parent-substitution','changed-directory-acl','non145-file-disposition','non145-directory-disposition')
    $who = [System.Security.Principal.WindowsIdentity]::GetCurrent()
    $adminToken = [System.Security.Principal.WindowsPrincipal]::new($who).IsInRole([System.Security.Principal.WindowsBuiltInRole]::Administrator)
    if ($adminToken) { $cases += 'changed-child-owner' }
    if ($Development) { $cases += 'development-without-flag' } else { $cases += 'owner-with-flag' }
    foreach ($mode in $cases) {
        $env:OUTER_TEST_MODE = $mode
        $env:OUTER_TEST_ARGS = [System.IO.Path]::Combine($root,($mode+'.args'))
        $env:OUTER_TEST_SCRATCH = [System.IO.Path]::Combine($root,($mode+'.scratch'))
        $env:OUTER_TEST_ACLS = [System.IO.Path]::Combine($root,($mode+'.acls'))
        $env:OUTER_TEST_RETRY_MARKER = [System.IO.Path]::Combine($root,($mode+'.retry'))
        $env:OUTER_TEST_DISPOSITION_MARKER = [System.IO.Path]::Combine($root,($mode+'.disposition'))
        $output = [System.IO.Path]::Combine($root,'literal $(Write-Output PWNED); output')
        $version = 'latest'
        if ($mode -eq 'malicious-version') { $version = 'v1.2.3;$(Write-Output PWNED)' }
        $call = @('-NoProfile','-NonInteractive','-ExecutionPolicy','Bypass','-File',$path,'-Output',$output,'-Version',$version)
        if (($Development -and $mode -ne 'development-without-flag') -or $mode -eq 'owner-with-flag') { $call += '-AllowDevelopment' }
        $engine = [System.Diagnostics.Process]::GetCurrentProcess().MainModule.FileName
        $priorPreference = $ErrorActionPreference
        try {
            # Windows PowerShell represents redirected native stderr as error
            # records; expected refusal cases must reach our exit assertions.
            $ErrorActionPreference = 'Continue'
            $captured = & $engine @call 2>&1 | Out-String
            $exit = $LASTEXITCODE
        } finally { $ErrorActionPreference = $priorPreference }
        $success = $mode -in @('good','delete-pending-child')
        $runnerReached = $mode -in @('good','foreign-child','changed-child-owner','delete-pending-child','parent-substitution','changed-directory-acl','non145-file-disposition','non145-directory-disposition')
        if (($exit -eq 0) -ne $success) { throw ('unexpected result for '+$mode) }
        if ($captured.Contains('private-secret')) { throw 'raw error leak' }
        if (-not $success -and $captured.Contains('"prepared":true')) { throw 'success JSON emitted on failure' }
        if ((Test-Path -LiteralPath $env:OUTER_TEST_ARGS) -ne $runnerReached) { throw ('unexpected native runner reachability for '+$mode) }
        $retryExpected = $mode -in @('delete-pending-child','parent-substitution','changed-directory-acl')
        if ((Test-Path -LiteralPath $env:OUTER_TEST_RETRY_MARKER) -ne $retryExpected) { throw ('unexpected directory retry seam for '+$mode) }
        $injectedDispositionExpected = $mode -in @('delete-pending-child','parent-substitution','changed-directory-acl','non145-file-disposition','non145-directory-disposition')
        $injectionMarkerExists = Test-Path -LiteralPath $env:OUTER_TEST_DISPOSITION_MARKER
        if ($injectionMarkerExists -ne $injectedDispositionExpected) { throw ('unexpected injected disposition marker for '+$mode) }
        if ($injectedDispositionExpected -and [System.IO.File]::ReadAllText($env:OUTER_TEST_DISPOSITION_MARKER) -cne '1') { throw ('unexpected injected disposition count for '+$mode) }
        if ($runnerReached) {
            $actualScratch = [System.IO.File]::ReadAllText($env:OUTER_TEST_SCRATCH)
            $expectedArgv = @('prepare','--repository',$expectedRepository,'--trust-file',[System.IO.Path]::Combine($actualScratch,'trust.json'),'--version',$version,'--output',$output)
            if (($call -contains '-AllowDevelopment')) { $expectedArgv += '--allow-development' }
            Assert-NativeArgvJson ([System.IO.File]::ReadAllText($env:OUTER_TEST_ARGS)) $expectedArgv
            $facts = @([System.IO.File]::ReadAllLines($env:OUTER_TEST_ACLS))
            $names = @($facts | ForEach-Object { $_.Split('|')[0] } | Sort-Object)
            if (($names -join ',') -cne 'error.txt,minecraft-ui-bootstrap.exe,result.json,trust.json') { throw 'not every actual scratch child had owner/DACL checked' }
            if ($mode -eq 'good') { foreach ($fact in $facts) { [Console]::Out.WriteLine('Atomic scratch ACL verified: '+$fact) } }
        }
        if (Test-Path -LiteralPath $env:OUTER_TEST_SCRATCH) {
            $scratch = [System.IO.File]::ReadAllText($env:OUTER_TEST_SCRATCH)
            if ($mode -eq 'foreign-child') {
                if ([System.IO.File]::ReadAllText([System.IO.Path]::Combine($scratch,'unowned.txt')) -cne 'preserve foreign child') { throw 'cleanup deleted foreign child' }
                if (@([System.IO.Directory]::EnumerateFileSystemEntries($scratch)).Count -ne 1) { throw 'owned children were not cleaned before nonempty directory refusal' }
                if (-not $captured.Contains('scratch cleanup refused')) { throw 'nonempty cleanup refusal was hidden' }
                [System.IO.Directory]::Delete($scratch,$true)
            } elseif ($mode -eq 'changed-child-owner') {
                if (-not (Test-Path -LiteralPath ([System.IO.Path]::Combine($scratch,'trust.json')))) { throw 'cleanup deleted child after ownership change' }
                if (-not $captured.Contains('scratch cleanup refused')) { throw 'changed owner refusal was hidden' }
                # Test-owned fixture cleanup after preservation assertions.
                [System.IO.Directory]::Delete($scratch,$true)
            } elseif ($mode -eq 'parent-substitution') {
                $replacement = [System.IO.Path]::Combine($scratch,'replacement.txt')
                $moved = $scratch+'.moved'
                if (-not (Test-Path -LiteralPath $replacement) -or [System.IO.File]::ReadAllText($replacement) -cne 'preserve replacement') { throw 'replacement parent was not preserved' }
                if (-not (Test-Path -LiteralPath $moved)) { throw 'original directory identity was not preserved after rename' }
                if (-not $captured.Contains('scratch cleanup refused')) { throw 'parent substitution refusal was hidden' }
                [System.IO.Directory]::Delete($scratch,$true)
                [System.IO.Directory]::Delete($moved,$true)
            } elseif ($mode -eq 'changed-directory-acl') {
                if (-not $captured.Contains('scratch cleanup refused')) { throw 'changed directory ACL refusal was hidden' }
                if (@([System.IO.Directory]::EnumerateFileSystemEntries($scratch)).Count -ne 0) { throw 'changed ACL fixture retained unexpected children' }
                [System.IO.Directory]::Delete($scratch,$true)
            } elseif ($mode -eq 'non145-file-disposition') {
                if (-not $captured.Contains('scratch cleanup refused')) { throw 'non-145 disposition refusal was hidden' }
                if (-not (Test-Path -LiteralPath ([System.IO.Path]::Combine($scratch,'trust.json')))) { throw 'non-145 failure retried or removed owned file' }
                [System.IO.Directory]::Delete($scratch,$true)
            } elseif ($mode -eq 'non145-directory-disposition') {
                if (-not $captured.Contains('scratch cleanup refused')) { throw 'non-145 directory disposition refusal was hidden' }
                if (@([System.IO.Directory]::EnumerateFileSystemEntries($scratch)).Count -ne 0) { throw 'directory non-145 fixture retained unexpected children' }
                [System.IO.Directory]::Delete($scratch,$true)
            } elseif (Test-Path -LiteralPath $scratch) { throw 'owned scratch leaked' }
        }
        if (-not (Test-Path -LiteralPath $path)) { throw 'cleanup removed unowned source' }
    }
    [Console]::Out.WriteLine('Windows outer behavioral checks passed (test downloader/runner; no native preparer invocation).')
} finally {
    [System.IO.Directory]::Delete($root,$true)
}
# Refusal cases intentionally leave a child exit code of 1. Only after every
# assertion and cleanup succeeds does this harness itself report success.
exit 0
