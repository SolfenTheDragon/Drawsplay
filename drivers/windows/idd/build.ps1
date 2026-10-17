<#
.SYNOPSIS
  Builds the DrawSplay IddCx virtual display driver (DrawSplayIdd.dll).

.DESCRIPTION
  Compiles and links the UMDF/IddCx driver with the MSVC toolchain plus the
  Windows SDK and the Windows Driver Kit. The WDK is normally obtained from
  the Microsoft.Windows.WDK.x64 NuGet package (no full WDK install needed):
  pass its extracted root (the folder containing Include/ and Lib/) via
  -WdkRoot. The result is the driver DLL and INF. The catalog is generated
  and signed with the release code-signing certificate by
  scripts/build-idd-driver.ps1, which also stages the dll/inf/cat into
  host/driver/idd for go:embed; this build itself needs no certificate.

.EXAMPLE
  ./build.ps1 -WdkRoot C:\wdk -Arch x64 -OutDir .\out\x64
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)] [string] $WdkRoot,
    [ValidateSet('x64', 'arm64')] [string] $Arch = 'x64',
    [string] $SdkRoot = "${env:ProgramFiles(x86)}\Windows Kits\10",
    [string] $SdkVersion,
    [string] $UmdfVersion = '2.25',
    [string] $IddCxVersion = '1.4',
    [switch] $Symbols,
    [string] $OutDir
)

$ErrorActionPreference = 'Stop'
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
if (-not $OutDir) { $OutDir = Join-Path $scriptDir "out\$Arch" }
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

function Die($msg) { Write-Error $msg; exit 1 }

# --- Resolve the Windows SDK version -------------------------------------
if (-not $SdkVersion) {
    $incRoot = Join-Path $SdkRoot 'Include'
    if (-not (Test-Path $incRoot)) { Die "Windows SDK not found at $SdkRoot" }
    $SdkVersion = (Get-ChildItem $incRoot -Directory |
        Where-Object { $_.Name -match '^10\.' } |
        Sort-Object Name | Select-Object -Last 1).Name
}
Write-Host "SDK version: $SdkVersion"

# --- Locate the MSVC toolchain via vswhere -------------------------------
$vswhere = "${env:ProgramFiles(x86)}\Microsoft Visual Studio\Installer\vswhere.exe"
if (-not (Test-Path $vswhere)) { Die "vswhere.exe not found; install Visual Studio 2022." }
$vsRoot = & $vswhere -latest -products * -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath
if (-not $vsRoot) { Die "No Visual Studio with the MSVC C++ toolset was found." }
$msvcVerFile = Join-Path $vsRoot 'VC\Auxiliary\Build\Microsoft.VCToolsVersion.default.txt'
$msvcVer = (Get-Content $msvcVerFile).Trim()
$vcTools = Join-Path $vsRoot "VC\Tools\MSVC\$msvcVer"
Write-Host "MSVC: $msvcVer"

$hostArch = if ([Environment]::Is64BitOperatingSystem) { 'x64' } else { 'x86' }
$clDir = Join-Path $vcTools "bin\Host$hostArch\$Arch"
$cl = Join-Path $clDir 'cl.exe'
$link = Join-Path $clDir 'link.exe'
if (-not (Test-Path $cl)) { Die "cl.exe not found at $cl" }

# --- Include directories -------------------------------------------------
$sdkInc = Join-Path $SdkRoot "Include\$SdkVersion"
$incs = @(
    (Join-Path $vcTools 'include'),
    (Join-Path $sdkInc 'ucrt'),
    (Join-Path $sdkInc 'um'),
    (Join-Path $sdkInc 'shared'),
    (Join-Path $sdkInc 'winrt'),
    (Join-Path $WdkRoot "Include\wdf\umdf\$UmdfVersion"),
    (Join-Path $WdkRoot "Include\$SdkVersion\um\iddcx\$IddCxVersion")
)
foreach ($i in $incs) { if (-not (Test-Path $i)) { Die "Missing include dir: $i" } }

# --- Library directories -------------------------------------------------
$sdkLib = Join-Path $SdkRoot "Lib\$SdkVersion"
$libs = @(
    (Join-Path $vcTools "lib\$Arch"),
    (Join-Path $sdkLib "ucrt\$Arch"),
    (Join-Path $sdkLib "um\$Arch"),
    (Join-Path $WdkRoot "Lib\wdf\umdf\$Arch\$UmdfVersion"),
    (Join-Path $WdkRoot "Lib\$SdkVersion\um\$Arch\iddcx\$IddCxVersion")
)
foreach ($l in $libs) { if (-not (Test-Path $l)) { Die "Missing lib dir: $l" } }

$archDefs = if ($Arch -eq 'x64') { @('/D_WIN64', '/D_AMD64_', '/DAMD64') }
            else { @('/D_WIN64', '/D_ARM64_', '/DARM64') }

$clArgs = @(
    '/nologo', '/c', '/EHsc', '/MD', '/O2', '/W3', '/GS', '/std:c++17',
    '/DNDEBUG', '/DUMDF_DRIVER',
    "/DUMDF_VERSION_MAJOR=$($UmdfVersion.Split('.')[0])",
    "/DUMDF_VERSION_MINOR=$($UmdfVersion.Split('.')[1])",
    "/DIDDCX_VERSION_MAJOR=$($IddCxVersion.Split('.')[0])",
    "/DIDDCX_VERSION_MINOR=$($IddCxVersion.Split('.')[1])",
    '/DIDDCX_MINIMUM_VERSION_REQUIRED=4'
) + $archDefs
if ($Symbols) { $clArgs += @('/Zi', "/Fd$OutDir\DrawSplayIdd.pdb") }
foreach ($i in $incs) { $clArgs += "/I$i" }
$clArgs += "/Fo$OutDir\"
$clArgs += (Join-Path $scriptDir 'Driver.cpp')

Write-Host "Compiling Driver.cpp ($Arch)..."
& $cl @clArgs
if ($LASTEXITCODE -ne 0) { Die "cl.exe failed ($LASTEXITCODE)" }

$linkArgs = @(
    '/nologo', '/DLL', "/MACHINE:$Arch",
    "/OUT:$OutDir\DrawSplayIdd.dll"
)
if ($Symbols) { $linkArgs += @('/DEBUG', "/PDB:$OutDir\DrawSplayIdd.pdb") }
foreach ($l in $libs) { $linkArgs += "/LIBPATH:$l" }
$linkArgs += (Join-Path $OutDir 'Driver.obj')
$linkArgs += @('WdfDriverStubUm.lib', 'iddcxstub.lib', 'OneCoreUAP.lib', 'avrt.lib', 'ole32.lib', 'ntdll.lib')

Write-Host "Linking DrawSplayIdd.dll ($Arch)..."
& $link @linkArgs
if ($LASTEXITCODE -ne 0) { Die "link.exe failed ($LASTEXITCODE)" }

Copy-Item (Join-Path $scriptDir 'DrawSplayIdd.inf') (Join-Path $OutDir 'DrawSplayIdd.inf') -Force
Write-Host "Built: $OutDir\DrawSplayIdd.dll"
