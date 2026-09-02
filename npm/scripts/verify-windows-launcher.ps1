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
    $ResolvedPrefix -cne $InstallPrefix) {
  throw 'windows launcher verification failed'
}

$RootItem = Get-Item -LiteralPath $ResolvedPrefix -Force
if (-not $RootItem.PSIsContainer -or
    ($RootItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or
    -not [string]::IsNullOrEmpty([string]$RootItem.LinkType)) {
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
  $Pending = [Collections.Generic.Stack[string]]::new()
  $Pending.Push($ResolvedPrefix)
  while ($Pending.Count -ne 0) {
    $CurrentPath = $Pending.Pop()
    $Item = Get-Item -LiteralPath $CurrentPath -Force
    if (($Item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or
        -not [string]::IsNullOrEmpty([string]$Item.LinkType)) {
      throw 'windows launcher verification failed'
    }

    if ($Item.PSIsContainer) {
      $Kind = 'directory'
      foreach ($Child in Get-ChildItem -LiteralPath $Item.FullName -Force) {
        $Pending.Push($Child.FullName)
      }
    } else {
      $Kind = 'file'
    }

    $RelativePath = [IO.Path]::GetRelativePath($ResolvedPrefix, $Item.FullName)
    $RelativePathLength = ([int]$RelativePath.Length).ToString(
      [Globalization.CultureInfo]::InvariantCulture
    )
    $Attributes = ([uint32]$Item.Attributes).ToString(
      [Globalization.CultureInfo]::InvariantCulture
    )
    $CreationTimeUtcTicks = ([int64]$Item.CreationTimeUtc.Ticks).ToString(
      [Globalization.CultureInfo]::InvariantCulture
    )
    $LastWriteTimeUtcTicks = ([int64]$Item.LastWriteTimeUtc.Ticks).ToString(
      [Globalization.CultureInfo]::InvariantCulture
    )
    $Entry = @(
      'kind=' + $Kind
      'pathLength=' + $RelativePathLength
      'path=' + $RelativePath
      'attributes=' + $Attributes
      'creationTimeUtcTicks=' + $CreationTimeUtcTicks
      'lastWriteTimeUtcTicks=' + $LastWriteTimeUtcTicks
    ) -join '|'

    if ($Kind -ceq 'file') {
      $SHA256 = [Security.Cryptography.SHA256]::Create()
      $Stream = $null
      try {
        $Stream = [IO.File]::Open(
          $Item.FullName,
          [IO.FileMode]::Open,
          [IO.FileAccess]::Read,
          [IO.FileShare]::Read
        )
        $Length = ([int64]$Stream.Length).ToString(
          [Globalization.CultureInfo]::InvariantCulture
        )
        $HashBytes = $SHA256.ComputeHash($Stream)
        $Hash = [Convert]::ToHexString($HashBytes).ToLowerInvariant()
      } finally {
        if ($null -ne $Stream) {
          $Stream.Dispose()
        }
        $SHA256.Dispose()
      }
      $Entry += '|length=' + $Length + '|sha256=' + $Hash
    }
    $Entries.Add($Entry)
  }
  $Entries.Sort([StringComparer]::Ordinal)
  return @($Entries)
}

$ExpectedEntries = @(Get-EntrySnapshot)
function Assert-EntrySnapshot {
  $ActualEntries = @(Get-EntrySnapshot)
  if ($ActualEntries.Count -ne $ExpectedEntries.Count) {
    throw 'windows launcher verification failed'
  }
  for ($Index = 0; $Index -lt $ExpectedEntries.Count; $Index++) {
    if (-not [string]::Equals(
      $ExpectedEntries[$Index],
      $ActualEntries[$Index],
      [StringComparison]::Ordinal
    )) {
      throw 'windows launcher verification failed'
    }
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
