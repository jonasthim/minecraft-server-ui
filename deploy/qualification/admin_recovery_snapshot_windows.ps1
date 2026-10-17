# Private stopped-manager snapshot; called only by the disposable native harness.
param([Parameter(Mandatory=$true)][string]$Snapshot)
$ErrorActionPreference = 'Stop'
if ($env:GITHUB_ACTIONS -ne 'true' -or $env:RUNNER_ENVIRONMENT -ne 'github-hosted') { exit 2 }
$admin = [Security.Principal.WindowsPrincipal]::new([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $admin.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { exit 2 }
$data = 'C:\ProgramData\MinecraftUI'
$Snapshot = [IO.Path]::GetFullPath($Snapshot)
if ([IO.Path]::GetDirectoryName($Snapshot) -ine $data -or
    [IO.Path]::GetFileName($Snapshot) -cnotmatch '^admin-recovery-[a-z0-9_]+$') { exit 2 }
function Assert-Regular([string]$Path, [bool]$Directory = $false) {
 $item = Get-Item -LiteralPath $Path -Force
 if ($item.PSIsContainer -ne $Directory -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
  throw 'Unsafe snapshot path.'
 }
}
Assert-Regular $data $true
Assert-Regular $Snapshot $true
if (@(Get-ChildItem -LiteralPath $Snapshot -Force).Count -ne 0) { exit 2 }
if ((Get-Service MinecraftUI).Status -ne 'Stopped') { exit 2 }
foreach ($name in @('manager.db', 'manager.lock')) { Assert-Regular (Join-Path $data $name) }
$lockPath = Join-Path $data 'manager.lock'
$lock = [IO.File]::Open($lockPath, [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::ReadWrite)
$locked = $false
try {
 $lock.Lock(0, 1)
 $locked = $true
 $second = [IO.File]::Open($lockPath, [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::ReadWrite)
 $refused = $false
 try {
  try { $second.Lock(0, 1); $second.Unlock(0, 1) }
  catch [IO.IOException] { $refused = $true }
 } finally { $second.Dispose() }
 if (-not $refused) { throw 'Snapshot lock contention was not refused.' }
 $count = 0
 foreach ($name in @('manager.db', 'manager.db-wal', 'manager.db-shm', 'manager.db-journal')) {
  $source = Join-Path $data $name
  try { $null = Get-Item -LiteralPath $source -Force -ErrorAction Stop }
  catch [System.Management.Automation.ItemNotFoundException] { continue }
  Assert-Regular $source
  $destination = Join-Path $Snapshot $name
  Copy-Item -LiteralPath $source -Destination $destination -ErrorAction Stop
  if ((Get-FileHash -Algorithm SHA256 -LiteralPath $source).Hash -cne
      (Get-FileHash -Algorithm SHA256 -LiteralPath $destination).Hash) { throw 'Snapshot hash mismatch.' }
  $count++
 }
 if ((Get-Item -LiteralPath (Join-Path $Snapshot 'manager.db')).Length -eq 0) { throw 'Empty snapshot.' }
 @{snapshot_files=$count;complete_sidecars=$true;hash_match=$true;lock_contention_refused=$true} | ConvertTo-Json -Compress
} finally {
 if ($locked) { $lock.Unlock(0, 1) }
 $lock.Dispose()
}
