<#
.SYNOPSIS
  Builds, catalogs, signs and stages DrawSplay's IddCx virtual display driver
  into host/driver/idd so `go build` can embed it (go:embed).

.DESCRIPTION
  The host binary embeds DrawSplayIdd.dll/.inf/.cat (idd_install_windows.go).
  For a release those files are rebuilt from drivers/windows/idd. Build-time
  catalog signing is optional unless required below; the elevated installer
  signs an unsigned catalog locally with a per-PC trusted certificate.

  Steps:
    1. Resolve the WDK root (Microsoft.Windows.WDK.x64 NuGet, or -WdkRoot).
    2. Compile DrawSplayIdd.dll (+ copy the INF) via the driver's build.ps1.
    3. Stamp the INF (stampinf) with the amd64 architecture and a DriverVer.
    4. Generate the catalog (Inf2Cat).
    5. Sign the catalog with the release certificate (signtool), when one is
       configured. Required when DRAWSPLAY_REQUIRE_SIGNING=1.
    6. Copy DrawSplayIdd.dll/.inf/.cat into host/driver/idd.

  When no WDK and no signing certificate are available (local dev), the script
  warns and leaves the already-committed host/driver/idd artifacts in place so
  local host builds still compile. The installer handles unsigned catalogs.

.PARAMETER DriverVersion
  DriverVer version stamped into the INF (e.g. 1.0.0.0). Defaults to 1.0.0.0.
#>
[CmdletBinding()]
param(
    [string] $WdkRoot,
    [ValidateSet('x64')] [string] $Arch = 'x64',
    [string] $DriverVersion = '1.0.0.0',
    [string] $WdkNuGetVersion = ''
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
$driverSrc = Join-Path $repoRoot 'drivers\windows\idd'
$embedDir = Join-Path $repoRoot 'host\driver\idd'
$requireSigning = ($env:DRAWSPLAY_REQUIRE_SIGNING -in @('1', 'true', 'yes'))

function Find-UnderKits {
    param([string] $Leaf)
    $roots = @()
    # The NuGet WDK ships stampinf/Inf2Cat under <root>\bin; prefer it.
    if ($script:resolvedWdk) { $roots += (Join-Path $script:resolvedWdk 'bin') }
    $roots += @(
        "${env:ProgramFiles(x86)}\Windows Kits\10\bin",
        "${env:ProgramFiles}\Windows Kits\10\bin"
    )
    foreach ($root in $roots) {
        if (-not (Test-Path $root)) { continue }
        $hit = Get-ChildItem -Path $root -Filter $Leaf -Recurse -ErrorAction SilentlyContinue |
            Where-Object { $_.FullName -match '\\x64\\' } |
            Sort-Object FullName | Select-Object -Last 1
        if (-not $hit) {
            $hit = Get-ChildItem -Path $root -Filter $Leaf -Recurse -ErrorAction SilentlyContinue |
                Sort-Object FullName | Select-Object -Last 1
        }
        if ($hit) { return $hit.FullName }
    }
    return $null
}

function Resolve-WdkRoot {
    if ($WdkRoot -and (Test-Path (Join-Path $WdkRoot 'Include'))) { return $WdkRoot }
    $nuget = Get-Command nuget.exe -ErrorAction SilentlyContinue
    if (-not $nuget) {
        throw "WDK not found and nuget.exe is unavailable; pass -WdkRoot <folder containing Include/ and Lib/>."
    }
    $pkgDir = Join-Path $env:RUNNER_TEMP 'wdk-nuget'
    if ([string]::IsNullOrWhiteSpace($env:RUNNER_TEMP)) { $pkgDir = Join-Path $env:TEMP 'ds-wdk-nuget' }
    New-Item -ItemType Directory -Force -Path $pkgDir | Out-Null
    $nugetArgs = @('install', 'Microsoft.Windows.WDK.x64', '-OutputDirectory', $pkgDir, '-ExcludeVersion', '-NonInteractive')
    if (-not [string]::IsNullOrWhiteSpace($WdkNuGetVersion)) {
        $nugetArgs += @('-Version', $WdkNuGetVersion)
        Write-Host "Restoring Microsoft.Windows.WDK.x64 $WdkNuGetVersion ..."
    }
    else {
        Write-Host "Restoring Microsoft.Windows.WDK.x64 (latest) ..."
    }
    & $nuget.Source @nugetArgs | Out-Host
    if ($LASTEXITCODE -ne 0) { throw "nuget install Microsoft.Windows.WDK.x64 failed ($LASTEXITCODE)" }
    $root = Join-Path $pkgDir 'Microsoft.Windows.WDK.x64\c'
    if (-not (Test-Path (Join-Path $root 'Include'))) {
        throw "Restored WDK package has no Include/ at $root"
    }
    return $root
}

$canBuildDriver = $true
try {
    $resolvedWdk = Resolve-WdkRoot
}
catch {
    if ($requireSigning) { throw }
    Write-Warning "Cannot build the IddCx driver ($($_.Exception.Message)); keeping committed host/driver/idd artifacts."
    $canBuildDriver = $false
}

if ($canBuildDriver) {
    $staging = Join-Path $env:TEMP ("ds-idd-" + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Force -Path $staging | Out-Null
    try {
        Write-Host "Building DrawSplayIdd.dll ..."
        & (Join-Path $driverSrc 'build.ps1') -WdkRoot $resolvedWdk -Arch $Arch -OutDir $staging
        if ($LASTEXITCODE -ne 0) { throw "driver build.ps1 failed ($LASTEXITCODE)" }

        $inf = Join-Path $staging 'DrawSplayIdd.inf'
        $cat = Join-Path $staging 'DrawSplayIdd.cat'

        $stampinf = Find-UnderKits 'stampinf.exe'
        if (-not $stampinf) { throw "stampinf.exe not found under Windows Kits." }
        $stampDate = (Get-Date).ToString('MM/dd/yyyy')
        Write-Host "Stamping INF (amd64, DriverVer $stampDate,$DriverVersion) ..."
        & $stampinf -f $inf -a 'amd64' -d $stampDate -v $DriverVersion
        if ($LASTEXITCODE -ne 0) { throw "stampinf failed ($LASTEXITCODE)" }

        $inf2cat = Find-UnderKits 'Inf2Cat.exe'
        if (-not $inf2cat) { throw "Inf2Cat.exe not found under Windows Kits." }
        Write-Host "Generating catalog ..."
        & $inf2cat /driver:$staging /os:10_X64 /uselocaltime
        if ($LASTEXITCODE -ne 0) { throw "Inf2Cat failed ($LASTEXITCODE)" }
        if (-not (Test-Path $cat)) { throw "Inf2Cat produced no catalog at $cat" }

        $certPath = $env:DRAWSPLAY_WINDOWS_SIGNING_PFX_PATH
        $certPassword = $env:DRAWSPLAY_WINDOWS_SIGNING_PFX_PASSWORD
        if (-not [string]::IsNullOrWhiteSpace($certPath) -and -not [string]::IsNullOrWhiteSpace($certPassword)) {
            $signtool = Find-UnderKits 'signtool.exe'
            if (-not $signtool) { throw "signtool.exe not found; cannot sign the driver catalog." }
            $timestampUrl = $env:DRAWSPLAY_WINDOWS_SIGNING_TIMESTAMP_URL
            if ([string]::IsNullOrWhiteSpace($timestampUrl)) { $timestampUrl = 'http://timestamp.digicert.com' }
            Write-Host "Signing driver catalog ..."
            & $signtool sign /fd SHA256 /f $certPath /p $certPassword /tr $timestampUrl /td SHA256 $cat
            if ($LASTEXITCODE -ne 0) { throw "signtool failed to sign the catalog" }
            & $signtool verify /pa $cat
            if ($LASTEXITCODE -ne 0) { throw "catalog signature verification failed" }
        }
        elseif ($requireSigning) {
            throw "Driver signing is required but DRAWSPLAY_WINDOWS_SIGNING_PFX_PATH/PASSWORD are not set."
        }
        else {
            Write-Warning "No signing certificate; staging an unsigned catalog for per-PC signing by the elevated installer."
        }

        New-Item -ItemType Directory -Force -Path $embedDir | Out-Null
        foreach ($name in 'DrawSplayIdd.dll', 'DrawSplayIdd.inf', 'DrawSplayIdd.cat') {
            Copy-Item -Force (Join-Path $staging $name) (Join-Path $embedDir $name)
        }
        Write-Host "Staged driver into $embedDir"
    }
    finally {
        Remove-Item -Recurse -Force $staging -ErrorAction SilentlyContinue
    }
}

# Fail fast if the embed dir is incomplete (go:embed would fail to compile).
foreach ($name in 'DrawSplayIdd.dll', 'DrawSplayIdd.inf', 'DrawSplayIdd.cat') {
    $p = Join-Path $embedDir $name
    if (-not (Test-Path $p) -or ((Get-Item $p).Length -eq 0)) {
        throw "Embedded driver file missing or empty: $p"
    }
}
