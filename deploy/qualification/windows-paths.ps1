# Deterministic regression for the shipping installer's path predicate.
# Extract and define only that function through the parser: never dot-source
# the installer, invoke it, touch services/accounts/files, or fabricate a copy
# of the production implementation in the test.
$ErrorActionPreference = 'Stop'
$installer = Join-Path $PSScriptRoot '..\windows\install.ps1'
$tokens = $null
$parseErrors = $null
$tree = [Management.Automation.Language.Parser]::ParseFile($installer,[ref]$tokens,[ref]$parseErrors)
if ($parseErrors.Count -ne 0) { throw 'Shipping installer has PowerShell parse errors.' }
$functions = @($tree.FindAll({ param($node)
 $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Test-AbsolutePath'
},$false))
if ($functions.Count -ne 1) { throw 'Expected one shipping Test-AbsolutePath function.' }
. ([scriptblock]::Create($functions[0].Extent.Text))

$accepted = @(
 'C:\ProgramData\MinecraftUI',
 'D:\a\minecraft-server-ui\minecraft-server-ui\bin\minecraft-ui.exe',
 'C:\Program Files\MinecraftUI\minecraft-ui.exe',
 'C:\',
 'C:/ProgramData/MinecraftUI',
 '\\server\share',
 '\\server\share\MinecraftUI',
 '\\server\share\',
 '\\server/share/MinecraftUI'
)
$rejected = @(
 '', ' ', 'minecraft-ui.exe', '.\minecraft-ui.exe', '..\minecraft-ui.exe',
 'C:minecraft-ui.exe', 'C:', '\ProgramData\MinecraftUI', '/ProgramData/MinecraftUI',
 '\\', '\\server', '\\server\', '\\\share', '\\server\\share',
 '\\server\share:stream', '\\.\pipe\manager', '\\?\C:\MinecraftUI',
 'http://server/share', '1:\MinecraftUI', '//server/share',
 "C:\MinecraftUI$([char]0)", "C:\MinecraftUI`n", '\\server\share\bad*file',
 '\\..\share', '\\server\.', '\\server\..\MinecraftUI'
)
foreach ($path in $accepted) {
 if (-not (Test-AbsolutePath $path)) { throw 'Shipping absolute-path predicate rejected a supported native path.' }
}
foreach ($path in $rejected) {
 if (Test-AbsolutePath $path) { throw 'Shipping absolute-path predicate accepted an unsafe or relative path.' }
}
Write-Output ('PASS shipping absolute-path predicate: ' + $accepted.Count + ' accepted, ' + $rejected.Count + ' rejected cases; no installer effects')
