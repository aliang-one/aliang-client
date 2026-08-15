param(
    [Parameter(Mandatory = $true)]
    [string]$ArchivePath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Get-WindowsDirectory {
    $windowsDir = [Environment]::GetFolderPath("Windows")
    if ([string]::IsNullOrWhiteSpace($windowsDir)) {
        $windowsDir = $env:WINDIR
    }
    if ([string]::IsNullOrWhiteSpace($windowsDir)) {
        $windowsDir = $env:SystemRoot
    }
    if ([string]::IsNullOrWhiteSpace($windowsDir)) {
        throw "Unable to determine the Windows directory."
    }
    return $windowsDir
}

function Get-NativeSystem32Path {
    param(
        [Parameter(Mandatory = $true)]
        [string]$WindowsDir
    )

    $system32 = Join-Path $WindowsDir "System32"
    if ([Environment]::Is64BitOperatingSystem -and -not [Environment]::Is64BitProcess) {
        $sysnative = Join-Path $WindowsDir "Sysnative"
        if (Test-Path -LiteralPath $sysnative -PathType Container) {
            return $sysnative
        }
    }
    return $system32
}

function Resolve-WintunInstallPlan {
    param(
        [Parameter(Mandatory = $true)]
        [string]$WindowsDir
    )

    $nativeSystem32 = Get-NativeSystem32Path -WindowsDir $WindowsDir
    $system32Display = Join-Path $WindowsDir "System32"
    $syswow64 = Join-Path $WindowsDir "SysWOW64"
    if (-not (Test-Path -LiteralPath $syswow64 -PathType Container)) {
        $syswow64 = $null
    }

    $architecture = $env:PROCESSOR_ARCHITEW6432
    if ([string]::IsNullOrWhiteSpace($architecture)) {
        $architecture = $env:PROCESSOR_ARCHITECTURE
    }
    if ([string]::IsNullOrWhiteSpace($architecture)) {
        if ([Environment]::Is64BitOperatingSystem) {
            $architecture = "AMD64"
        } else {
            $architecture = "X86"
        }
    }

    switch ($architecture.ToUpperInvariant()) {
        "AMD64" {
            return @{
                NativeSystem32  = $nativeSystem32
                SysWOW64        = $syswow64
                SourceSubdir    = "amd64"
                TargetDir       = $nativeSystem32
                TargetDisplayDir = $system32Display
            }
        }
        "ARM64" {
            return @{
                NativeSystem32  = $nativeSystem32
                SysWOW64        = $syswow64
                SourceSubdir    = "arm64"
                TargetDir       = $nativeSystem32
                TargetDisplayDir = $system32Display
            }
        }
        "ARM" {
            return @{
                NativeSystem32  = $nativeSystem32
                SysWOW64        = $syswow64
                SourceSubdir    = "arm"
                TargetDir       = $nativeSystem32
                TargetDisplayDir = $system32Display
            }
        }
        "X86" {
            $targetDir = $nativeSystem32
            $targetDisplayDir = $system32Display
            if ([Environment]::Is64BitOperatingSystem -and $syswow64) {
                $targetDir = $syswow64
                $targetDisplayDir = $syswow64
            }

            return @{
                NativeSystem32  = $nativeSystem32
                SysWOW64        = $syswow64
                SourceSubdir    = "x86"
                TargetDir       = $targetDir
                TargetDisplayDir = $targetDisplayDir
            }
        }
        default {
            throw "Unsupported Windows architecture for Wintun installation: $architecture"
        }
    }
}

function Get-TargetWintunDllPath {
    param(
        [Parameter(Mandatory = $true)]
        [hashtable]$Plan
    )

    return Join-Path $Plan.TargetDir "wintun.dll"
}

$resolvedArchivePath = [System.IO.Path]::GetFullPath($ArchivePath)
if (-not (Test-Path -LiteralPath $resolvedArchivePath -PathType Leaf)) {
    throw "Embedded Wintun archive not found: $resolvedArchivePath"
}

$windowsDir = Get-WindowsDirectory
$installPlan = Resolve-WintunInstallPlan -WindowsDir $windowsDir
$targetPath = Get-TargetWintunDllPath -Plan $installPlan
$targetDisplayPath = Join-Path $installPlan.TargetDisplayDir "wintun.dll"
if (Test-Path -LiteralPath $targetPath -PathType Leaf) {
    Write-Host "Wintun already exists at $targetDisplayPath. Skipping embedded installation."
    exit 0
}

$tempRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("aliang-wintun-" + [System.Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $tempRoot -Force | Out-Null

try {
    Write-Host "Extracting embedded Wintun package from $resolvedArchivePath"
    Expand-Archive -LiteralPath $resolvedArchivePath -DestinationPath $tempRoot -Force

    $sourcePath = Join-Path $tempRoot ("wintun\bin\" + $installPlan.SourceSubdir + "\wintun.dll")
    if (-not (Test-Path -LiteralPath $sourcePath -PathType Leaf)) {
        throw "Unable to locate wintun.dll for architecture '$($installPlan.SourceSubdir)' inside the embedded archive."
    }

    Write-Host "Installing Wintun to $targetDisplayPath"
    Copy-Item -LiteralPath $sourcePath -Destination $targetPath -Force

    if (-not (Test-Path -LiteralPath $targetPath -PathType Leaf)) {
        throw "Wintun installation completed but the DLL could not be verified at $targetDisplayPath."
    }

    Write-Host "Wintun installed successfully at $targetDisplayPath"
}
finally {
    Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
}
