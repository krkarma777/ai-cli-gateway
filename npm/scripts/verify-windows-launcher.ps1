[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)]
  [string]$InstallPrefix,

  [Parameter(Mandatory = $true)]
  [ValidatePattern('^v(?:0|[1-9][0-9]*)[.](?:0|[1-9][0-9]*)[.](?:0|[1-9][0-9]*)$')]
  [string]$ExpectedTag,

  [Parameter(Mandatory = $true)]
  [ValidatePattern('^[0-9a-f]{40}$')]
  [string]$ExpectedCommit
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$ResolvedPrefix = [IO.Path]::GetFullPath($InstallPrefix)
if (-not [IO.Path]::IsPathFullyQualified($InstallPrefix) -or
    $ResolvedPrefix -cne $InstallPrefix -or
    -not (Test-Path -LiteralPath $ResolvedPrefix -PathType Container)) {
  throw 'windows launcher verification failed'
}

$CmdShim = Join-Path $ResolvedPrefix 'ai-cli-gateway.cmd'
$PowerShellShim = Join-Path $ResolvedPrefix 'ai-cli-gateway.ps1'
$CmdPath = [IO.Path]::GetFullPath($env:ComSpec)
$PowerShellPath = [IO.Path]::GetFullPath((Get-Process -Id $PID).Path)
foreach ($Shim in @($CmdShim, $PowerShellShim)) {
  $Item = Get-Item -LiteralPath $Shim -Force
  if ($Item.PSIsContainer -or
      -not [string]::IsNullOrEmpty([string]$Item.LinkType)) {
    throw 'windows launcher verification failed'
  }
}

function Get-EntrySnapshot {
  $Entries = [Collections.Generic.List[string]]::new()
  foreach ($Item in Get-ChildItem -LiteralPath $ResolvedPrefix -Force -Recurse) {
    $Kind = if ($Item.PSIsContainer) { 'directory' } else { 'file' }
    $RelativePath = [IO.Path]::GetRelativePath($ResolvedPrefix, $Item.FullName)
    $Entries.Add($Kind + ':' + $RelativePath)
  }
  $Entries.Sort([StringComparer]::Ordinal)
  return @($Entries)
}

$ExpectedEntries = @(Get-EntrySnapshot)
function Assert-EntrySnapshot {
  $ActualEntries = @(Get-EntrySnapshot)
  $Differences = @(
    Compare-Object `
      -ReferenceObject $ExpectedEntries `
      -DifferenceObject $ActualEntries `
      -CaseSensitive
  )
  if ($Differences.Count -ne 0) {
    throw 'windows launcher verification failed'
  }
}

function Invoke-Shim(
  [ValidateSet('cmd', 'powershell')]
  [string]$Kind,
  [string]$Shim,
  [string]$Argument
) {
  $StartInfo = [Diagnostics.ProcessStartInfo]::new()
  $StartInfo.UseShellExecute = $false
  $StartInfo.CreateNoWindow = $true
  $StartInfo.RedirectStandardOutput = $true
  $StartInfo.RedirectStandardError = $true
  if ($Kind -ceq 'cmd') {
    $StartInfo.FileName = $CmdPath
    foreach ($Value in @('/d', '/s', '/c', 'call', $Shim, $Argument)) {
      $StartInfo.ArgumentList.Add($Value)
    }
  } else {
    $StartInfo.FileName = $PowerShellPath
    foreach ($Value in @(
      '-NoLogo',
      '-NoProfile',
      '-NonInteractive',
      '-File',
      $Shim,
      $Argument
    )) {
      $StartInfo.ArgumentList.Add($Value)
    }
  }

  $Process = [Diagnostics.Process]::new()
  $Process.StartInfo = $StartInfo
  try {
    if (-not $Process.Start()) {
      throw 'windows launcher verification failed'
    }
    $StdoutTask = $Process.StandardOutput.ReadToEndAsync()
    $StderrTask = $Process.StandardError.ReadToEndAsync()
    $Process.WaitForExit()
    $Stdout = $StdoutTask.GetAwaiter().GetResult().Replace("`r`n", "`n")
    $Stderr = $StderrTask.GetAwaiter().GetResult().Replace("`r`n", "`n")
    return [PSCustomObject]@{
      Status = $Process.ExitCode
      Stdout = $Stdout
      Stderr = $Stderr
    }
  } finally {
    $Process.Dispose()
  }
}

function Assert-VersionResult([PSCustomObject]$Result) {
  if ($Result.Status -ne 0 -or $Result.Stderr -cne '') {
    throw 'windows launcher verification failed'
  }
  $Value = $Result.Stdout.TrimEnd("`r", "`n")
  $Pattern = '^ai-cli-gateway ' +
    [Regex]::Escape($ExpectedTag) +
    ' [(]' +
    [Regex]::Escape($ExpectedCommit) +
    ', [0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z[)]$'
  if ($Value -cnotmatch $Pattern) {
    throw 'windows launcher verification failed'
  }
  return $Value
}

$CmdVersionResult = Invoke-Shim 'cmd' $CmdShim 'version'
Assert-EntrySnapshot
$CmdVersion = Assert-VersionResult $CmdVersionResult
$PowerShellVersionResult = Invoke-Shim 'powershell' $PowerShellShim 'version'
Assert-EntrySnapshot
$PowerShellVersion = Assert-VersionResult $PowerShellVersionResult
if ($CmdVersion -cne $PowerShellVersion) {
  throw 'windows launcher verification failed'
}

function Assert-InvalidResult([PSCustomObject]$Result) {
  $Usage = "usage:`n" +
    "  ai-cli-gateway version`n" +
    "  ai-cli-gateway init [OPTIONS]`n" +
    "  ai-cli-gateway serve [--config PATH]`n" +
    "  ai-cli-gateway doctor [--config PATH] [--json]`n"
  if ($Result.Status -ne 2 -or
      $Result.Stdout -cne '' -or
      $Result.Stderr -cne $Usage) {
    throw 'windows launcher verification failed'
  }
}

$CmdInvalidResult = Invoke-Shim 'cmd' $CmdShim '__launcher_exit_probe__'
Assert-EntrySnapshot
Assert-InvalidResult $CmdInvalidResult
$PowerShellInvalidResult = Invoke-Shim 'powershell' $PowerShellShim '__launcher_exit_probe__'
Assert-EntrySnapshot
Assert-InvalidResult $PowerShellInvalidResult
