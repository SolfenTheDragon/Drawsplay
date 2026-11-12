# Builds a release zip: dist\DrawSplayDesktop-windows-ARCH-vVERSION.zip (ARCH = x86_64 or arm64)
# Usage: package-windows.ps1 [-Version 1.2.3]   (default: the version in Cargo.toml; a leading "v" is ignored)
# Usage: package-windows.ps1 [-Version 1.2.3] [-Target aarch64-pc-windows-msvc]
param([string]$Version = '', [string]$Target = '')
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')
if ($Version) {
    $version = $Version.TrimStart('v')
    # Built into dd-host, which reports it in its status file (unset: "dev").
    $env:DRAWSPLAY_VERSION = "v$version"
} else {
    $version = (Select-String -Path Cargo.toml -Pattern '^version = "(.*)"' | Select-Object -First 1).Matches.Groups[1].Value
}
if ($Target) {
    cargo build --release --locked --target $Target
    $bin = "target\$Target\release"
    $arch = if ($Target -like 'aarch64-*') { 'arm64' } else { 'x86_64' }
} else {
    cargo build --release --locked
    $bin = 'target\release'
    $arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'x86_64' }
}
if ($LASTEXITCODE -ne 0) { throw 'cargo build failed' }
$name = "DrawSplayDesktop-windows-$arch-v$version"
$dir = Join-Path 'dist' $name
if (Test-Path $dir) { Remove-Item -Recurse -Force $dir }
New-Item -ItemType Directory -Force $dir | Out-Null
Copy-Item "$bin\dd-host.exe", "$bin\dd-client.exe" $dir
# Documentation is optional (it may not be in the checkout).
Copy-Item README.md, GUIDE.md, ..\LICENSE, ..\THIRD-PARTY-NOTICES.md $dir
$driverLicenseDir = Join-Path $dir 'drivers\windows\idd'
New-Item -ItemType Directory -Force $driverLicenseDir | Out-Null
Copy-Item ..\drivers\windows\idd\LICENSE $driverLicenseDir
if (Test-Path docs) {
    New-Item -ItemType Directory -Force (Join-Path $dir 'docs') | Out-Null
    Copy-Item docs\*.md (Join-Path $dir 'docs')
}
$zip = "dist\$name.zip"
if (Test-Path $zip) { Remove-Item -Force $zip }
Compress-Archive -Path $dir -DestinationPath $zip
Write-Output $zip
