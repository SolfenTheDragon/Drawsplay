param(
  [string]$SourceExe = "release\DrawSplay.exe"
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

if (-not (Test-Path $SourceExe)) {
  throw "Host executable not found: $SourceExe"
}

$installDir = Join-Path $env:LOCALAPPDATA "DrawSplay"
New-Item -ItemType Directory -Force -Path $installDir | Out-Null
$target = Join-Path $installDir "host.exe"
Copy-Item -Force $SourceExe $target

Write-Host "Installed host to $target"
& $target -service install
