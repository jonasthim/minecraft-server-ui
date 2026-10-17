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

$source = [System.IO.File]::ReadAllText($EntryScript)
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
    $cases = @('good','tamper','oversize','truncate','error','malicious-version','foreign-child')
    $who = [System.Security.Principal.WindowsIdentity]::GetCurrent()
    $adminToken = [System.Security.Principal.WindowsPrincipal]::new($who).IsInRole([System.Security.Principal.WindowsBuiltInRole]::Administrator)
    if ($adminToken) { $cases += 'changed-child-owner' }
    if ($Development) { $cases += 'development-without-flag' } else { $cases += 'owner-with-flag' }
    foreach ($mode in $cases) {
        $env:OUTER_TEST_MODE = $mode
        $env:OUTER_TEST_ARGS = [System.IO.Path]::Combine($root,($mode+'.args'))
        $env:OUTER_TEST_SCRATCH = [System.IO.Path]::Combine($root,($mode+'.scratch'))
        $env:OUTER_TEST_ACLS = [System.IO.Path]::Combine($root,($mode+'.acls'))
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
        $success = $mode -eq 'good'
        $runnerReached = $mode -in @('good','foreign-child','changed-child-owner')
        if (($exit -eq 0) -ne $success) { throw ('unexpected result for '+$mode) }
        if ($captured.Contains('private-secret')) { throw 'raw error leak' }
        if (-not $success -and $captured.Contains('"prepared":true')) { throw 'success JSON emitted on failure' }
        if ((Test-Path -LiteralPath $env:OUTER_TEST_ARGS) -ne $runnerReached) { throw ('unexpected native runner reachability for '+$mode) }
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
