param(
  [string]$Configuration = "Release"
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

New-Item -ItemType Directory -Force -Path "release" | Out-Null

Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem

function Resolve-SignToolPath {
  $direct = Get-Command signtool.exe -ErrorAction SilentlyContinue
  if ($direct) {
    return $direct.Source
  }
  $kitsRoot = "C:\Program Files (x86)\Windows Kits\10\bin"
  if (Test-Path $kitsRoot) {
    $candidate = Get-ChildItem -Path $kitsRoot -Filter signtool.exe -Recurse -ErrorAction SilentlyContinue |
      Sort-Object FullName |
      Select-Object -Last 1
    if ($candidate) {
      return $candidate.FullName
    }
  }
  return $null
}

function Sign-WindowsExecutable {
  param(
    [Parameter(Mandatory = $true)][string]$ExePath
  )

  $requireSigning = ($env:DRAWSPLAY_REQUIRE_SIGNING -eq "1" -or $env:DRAWSPLAY_REQUIRE_SIGNING -eq "true")
  $certPath = $env:DRAWSPLAY_WINDOWS_SIGNING_PFX_PATH
  $certPassword = $env:DRAWSPLAY_WINDOWS_SIGNING_PFX_PASSWORD
  $timestampUrl = $env:DRAWSPLAY_WINDOWS_SIGNING_TIMESTAMP_URL
  if ([string]::IsNullOrWhiteSpace($timestampUrl)) {
    $timestampUrl = "http://timestamp.digicert.com"
  }

  if ([string]::IsNullOrWhiteSpace($certPath) -or [string]::IsNullOrWhiteSpace($certPassword)) {
    if ($requireSigning) {
      throw "Windows code signing is required but DRAWSPLAY_WINDOWS_SIGNING_PFX_PATH/PASSWORD are not set."
    }
    Write-Warning "Windows code signing is not configured; continuing with unsigned host binary."
    return
  }
  if (-not (Test-Path $certPath)) {
    throw "Windows signing certificate file not found: $certPath"
  }

  $signTool = Resolve-SignToolPath
  if ([string]::IsNullOrWhiteSpace($signTool)) {
    throw "signtool.exe not found; cannot sign Windows executable."
  }

  Write-Host "Signing host executable..."
  & $signTool sign /fd SHA256 /f $certPath /p $certPassword /tr $timestampUrl /td SHA256 $ExePath
  if (-not $?) {
    throw "signtool failed to sign $ExePath"
  }
  & $signTool verify /pa $ExePath
  if (-not $?) {
    throw "signtool verification failed for $ExePath"
  }
}

function New-SingleFileZip {
  param(
    [Parameter(Mandatory = $true)][string]$SourcePath,
    [Parameter(Mandatory = $true)][string]$DestinationZipPath,
    [Parameter(Mandatory = $true)][string]$EntryName
  )
  if (Test-Path $DestinationZipPath) {
    Remove-Item -Force $DestinationZipPath
  }
  $zip = [System.IO.Compression.ZipFile]::Open($DestinationZipPath, [System.IO.Compression.ZipArchiveMode]::Create)
  try {
    [System.IO.Compression.ZipFileExtensions]::CreateEntryFromFile(
      $zip,
      $SourcePath,
      $EntryName,
      [System.IO.Compression.CompressionLevel]::Optimal
    ) | Out-Null
  }
  finally {
    $zip.Dispose()
  }
}

function Copy-FileWithRetry {
  param(
    [Parameter(Mandatory = $true)][string]$SourcePath,
    [Parameter(Mandatory = $true)][string]$DestinationPath,
    [int]$Attempts = 5,
    [int]$DelayMilliseconds = 400
  )

  for ($attempt = 1; $attempt -le $Attempts; $attempt++) {
    try {
      Copy-Item -Force $SourcePath $DestinationPath
      return $true
    }
    catch {
      if ($attempt -eq $Attempts) {
        return $false
      }
      Start-Sleep -Milliseconds $DelayMilliseconds
    }
  }
  return $false
}

Write-Host "Building host..."
Set-Location "$repoRoot\host"
go build -o host.exe .
if (-not $?) {
  throw "Host build failed"
}
Sign-WindowsExecutable -ExePath "$repoRoot\host\host.exe"

Write-Host "Building Android APK..."
Set-Location "$repoRoot\android"
.\gradlew.bat :app:assembleRelease
if (-not $?) {
  throw "Android release build failed"
}

Set-Location $repoRoot
$hostBuildPath = "host\host.exe"
$hostReleasePath = "release\DrawSplay.exe"
$apkBuildPath = "android\app\build\outputs\apk\release\app-release.apk"
$apkReleasePath = "release\DrawSplay.APK"

if (-not (Copy-FileWithRetry -SourcePath $hostBuildPath -DestinationPath $hostReleasePath)) {
  Write-Warning "Could not overwrite release\DrawSplay.exe because it is in use; packaging host\host.exe instead."
  $hostReleasePath = $hostBuildPath
}

if (-not (Copy-FileWithRetry -SourcePath $apkBuildPath -DestinationPath $apkReleasePath)) {
  Write-Warning "Could not overwrite release\DrawSplay.APK because it is in use; packaging app-release.apk instead."
  $apkReleasePath = $apkBuildPath
}

Write-Host "Creating release archives..."
New-SingleFileZip -SourcePath $hostReleasePath -DestinationZipPath "release\DrawSplay-host-windows.zip" -EntryName "DrawSplay.exe"
New-SingleFileZip -SourcePath $apkReleasePath -DestinationZipPath "release\DrawSplay-android-release.zip" -EntryName "DrawSplay.APK"

$hashTargets = @(
  @{ Path = $hostReleasePath; Label = "release/DrawSplay.exe" },
  @{ Path = $apkReleasePath; Label = "release/DrawSplay.APK" },
  @{ Path = "release\DrawSplay-host-windows.zip"; Label = "release/DrawSplay-host-windows.zip" },
  @{ Path = "release\DrawSplay-android-release.zip"; Label = "release/DrawSplay-android-release.zip" }
)
$hashes = foreach ($target in $hashTargets) {
  $hash = Get-FileHash -Algorithm SHA256 $target.Path
  "{0} *{1}" -f $hash.Hash.ToLower(), $target.Label
}
Set-Content -Path "release\SHA256SUMS.txt" -Value $hashes -Encoding ascii

Write-Host "Release artifacts are ready in .\release"
