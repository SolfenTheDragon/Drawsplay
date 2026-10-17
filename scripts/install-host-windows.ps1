param(
  [string]$SourceExe = "",
  [switch]$NoDriverInstall,
  [switch]$DesktopShortcut,
  [switch]$NoAutostart
)

# Installs DrawSplay with the host's built-in installer: one administrator
# prompt, binaries in %ProgramFiles%\DrawSplay, per-user state and settings in
# %APPDATA%\DrawSplay, tray autostart at login (-NoAutostart to skip). Uninstall with
# "%ProgramFiles%\DrawSplay\DrawSplay-Uninstall.exe" or Apps & Features.

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

if ([string]::IsNullOrWhiteSpace($SourceExe)) {
  $SourceExe = Get-ChildItem -Path "release" -Filter "drawsplay_installer_*.exe" -ErrorAction SilentlyContinue |
    Sort-Object LastWriteTime | Select-Object -Last 1 -ExpandProperty FullName
  if (-not $SourceExe) {
    throw "No release\drawsplay_installer_*.exe found; run scripts\build-release.ps1 or pass -SourceExe"
  }
}
if (-not (Test-Path $SourceExe)) {
  throw "Host executable not found: $SourceExe"
}

$installerArgs = @("-install", "-quiet")
if ($NoDriverInstall) {
  $installerArgs += "-no-driver-install"
}
if ($DesktopShortcut) {
  $installerArgs += "-desktop-shortcut"
}
if ($NoAutostart) {
  $installerArgs += "-no-autostart"
}
$proc = Start-Process -FilePath (Resolve-Path $SourceExe) -ArgumentList $installerArgs -Wait -PassThru
if ($proc.ExitCode -ne 0) {
  throw "DrawSplay install failed (exit code $($proc.ExitCode))"
}
Write-Host "DrawSplay installed to $env:ProgramFiles\DrawSplay (settings: $env:APPDATA\DrawSplay)"
