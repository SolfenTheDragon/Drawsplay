<#
.SYNOPSIS
  Builds drawsplay-media.exe, the Windows native capture/encode helper.

.DESCRIPTION
  Compiles main.cpp with the MSVC toolchain from the newest Visual Studio
  (Build Tools are enough) and the Windows SDK. No WDK is needed: the helper
  is a normal user-mode program that talks to the DrawSplay display driver
  through its public IOCTL header.

  The output is named drawsplay-media.exe because the host looks for the
  helper under that name next to its own executable (or in
  DRAWSPLAY_MEDIA_HELPER).

.EXAMPLE
  ./build.ps1 -OutDir ..\..\host
#>
[CmdletBinding()]
param(
    [string] $OutDir = (Join-Path $PSScriptRoot 'out'),
    [ValidateSet('x64', 'arm64')] [string] $Arch = 'x64'
)
$ErrorActionPreference = 'Stop'

$vswhere = Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio\Installer\vswhere.exe'
if (-not (Test-Path $vswhere)) { throw 'vswhere.exe not found; install Visual Studio or the C++ Build Tools.' }
$vs = & $vswhere -latest -products * -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath
if (-not $vs) { throw 'No Visual Studio installation with the C++ tools was found.' }
$vcvars = Join-Path $vs 'VC\Auxiliary\Build\vcvarsall.bat'
$hostArch = if ($Arch -eq 'arm64') { 'x64_arm64' } else { 'x64' }

New-Item -ItemType Directory -Force $OutDir | Out-Null
$obj = Join-Path $OutDir 'obj'
New-Item -ItemType Directory -Force $obj | Out-Null
$src = Join-Path $PSScriptRoot 'main.cpp'
$exe = Join-Path $OutDir 'drawsplay-media.exe'

$libs = 'd3d11.lib dxgi.lib mfplat.lib mfuuid.lib mf.lib strmiids.lib cfgmgr32.lib ole32.lib oleaut32.lib user32.lib winmm.lib'
$cmd = "`"$vcvars`" $hostArch >nul && cl.exe /nologo /std:c++17 /EHsc /O2 /W4 /MT /DUNICODE /D_UNICODE " +
       "/Fo`"$obj\\`" `"$src`" /Fe`"$exe`" /link $libs"
cmd.exe /c $cmd
if ($LASTEXITCODE -ne 0) { throw "cl.exe failed ($LASTEXITCODE)" }
Write-Host "Built: $exe"
