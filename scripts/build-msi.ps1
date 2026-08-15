# Build MSI installer for Windows using WiX Toolset
# This script should be run on Windows with WiX installed

param(
    [string]$Version = "1.1.10",
    [string]$OutputDir = ".",
    [string]$WiXPath = "",
    [string]$CandleExe = "",
    [string]$LightExe = "",
    [string]$WintunZipPath = ""
)

$ErrorActionPreference = "Stop"

$BINARY_NAME = "aliang.exe"
# Keep in sync with processor/setup.GetServiceName()
$SERVICE_NAME = "alianggate"
$SERVICE_DISPLAY_NAME = "Aliang Gateway System Service"
$SERVICE_DESCRIPTION = "Aliang background proxy service with automatic startup"
$MANUFACTURER = "Aliang"
$UPGRADE_CODE = "A1B2C3D4-E5F6-7890-ABCD-EF1234567890"  # Should be generated once per product
$ICON_FILE = "desktop-logo.ico"
$WINTUN_DLL_NAME = "wintun.dll"
$WINTUN_ARCHIVE_NAME = "wintun-0.14.1.zip"
$WINTUN_DOWNLOAD_URL = "https://www.wintun.net/builds/wintun-0.14.1.zip"
$ENV_COMPONENT_GUID = "64E4CB9B-2509-4EFA-8A58-5DFAF5DD17E8"

Write-Host "=== Building Aliang MSI Installer ===" -ForegroundColor Cyan
Write-Host "Version: $Version"

function Resolve-WintunSourceSubdir {
    param(
        [Parameter(Mandatory = $true)]
        [string]$GoArch
    )

    switch ($GoArch.Trim().ToLowerInvariant()) {
        "amd64" { return "amd64" }
        "386" { return "x86" }
        "arm64" { return "arm64" }
        "arm" { return "arm" }
        default { throw "Unsupported GOARCH for Wintun packaging: $GoArch" }
    }
}

$targetGoArch = if ($env:GOARCH) { $env:GOARCH } else { "amd64" }
$wintunSourceSubdir = Resolve-WintunSourceSubdir -GoArch $targetGoArch
if ($targetGoArch -ne "amd64") {
    throw "The current MSI packaging flow supports only GOARCH=amd64. Received: $targetGoArch"
}

# Resolve output directory to absolute path before changing working directory
$currentDir = (Get-Location).Path
if ([System.IO.Path]::IsPathRooted($OutputDir)) {
    $resolvedOutputDir = $OutputDir
} else {
    $resolvedOutputDir = Join-Path $currentDir $OutputDir
}
New-Item -ItemType Directory -Force -Path $resolvedOutputDir | Out-Null
Write-Host "Output: $resolvedOutputDir"

# Check if WiX is installed
if (-not $WiXPath) {
    $wixPath = $null
    if (Test-Path "C:\Program Files (x86)\WiX Toolset v3.11\bin\candle.exe") {
        $wixPath = "C:\Program Files (x86)\WiX Toolset v3.11\bin"
    } elseif (Test-Path "C:\Program Files (x86)\WiX Toolset v3\bin\candle.exe") {
        $wixPath = "C:\Program Files (x86)\WiX Toolset v3\bin"
    } elseif (Get-Command candle.exe -ErrorAction SilentlyContinue) {
        $wixPath = Split-Path (Get-Command candle.exe).Source
    }

    if (-not $wixPath) {
        Write-Host "WiX Toolset not found. Installing via NuGet..." -ForegroundColor Yellow

        # Download nuget.exe if not present
        $nugetExe = "$env:TEMP\nuget.exe"
        if (!(Test-Path $nugetExe)) {
            Write-Host "Downloading nuget.exe..." -ForegroundColor Yellow
            Invoke-WebRequest -Uri "https://dist.nuget.org/win-x86-commandline/latest/nuget.exe" -OutFile $nugetExe
        }

        $wixPath = "$env:TEMP\wix"
        New-Item -ItemType Directory -Force -Path $wixPath | Out-Null
        & $nugetExe install WiX -Version 3.11.2 -OutputDirectory $wixPath -NoHttpCache

        # Find candle.exe dynamically instead of guessing the folder structure
        $wixBin = Get-ChildItem -Path "$wixPath" -Recurse -Filter "candle.exe" -ErrorAction SilentlyContinue | Select-Object -First 1
        if (-not $wixBin) {
            Write-Host "ERROR: Could not find candle.exe after WiX installation" -ForegroundColor Red
            exit 1
        }
        $wixPath = $wixBin.DirectoryName
    }
} else {
    $wixPath = $WiXPath
}

Write-Host "Using WiX from: $wixPath" -ForegroundColor Green

# Create build directory
$buildDir = "$env:TEMP\aliang-msi-build"
$sourceDir = "$buildDir\source"
$payloadDir = "$buildDir\payload"
$wintunExtractDir = "$buildDir\wintun"

# Clean build directory
Remove-Item -Path $buildDir -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $sourceDir | Out-Null
New-Item -ItemType Directory -Force -Path $payloadDir | Out-Null

# Copy binary and icon
Write-Host "Copying binary and icon..." -ForegroundColor Cyan
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
Copy-Item ".\dist\$BINARY_NAME" -Destination "$payloadDir\" -Force
$iconPath = Join-Path $scriptDir $ICON_FILE
if (Test-Path $iconPath) {
    Copy-Item $iconPath -Destination "$payloadDir\" -Force
} else {
    Write-Host "Warning: Icon file $iconPath not found, shortcuts will use default icon" -ForegroundColor Yellow
}

$wintunArchiveDestination = "$buildDir\$WINTUN_ARCHIVE_NAME"
if ($WintunZipPath) {
    if ([System.IO.Path]::IsPathRooted($WintunZipPath)) {
        $resolvedWintunZipPath = $WintunZipPath
    } else {
        $resolvedWintunZipPath = Join-Path $currentDir $WintunZipPath
    }
    if (-not (Test-Path $resolvedWintunZipPath)) {
        throw "Specified Wintun archive not found: $resolvedWintunZipPath"
    }
    Write-Host "Using local Wintun archive: $resolvedWintunZipPath" -ForegroundColor Cyan
    Copy-Item $resolvedWintunZipPath -Destination $wintunArchiveDestination -Force
} else {
    Write-Host "Downloading Wintun archive from $WINTUN_DOWNLOAD_URL" -ForegroundColor Cyan
    Invoke-WebRequest -Uri $WINTUN_DOWNLOAD_URL -OutFile $wintunArchiveDestination
}

Write-Host "Extracting Wintun payload for $targetGoArch..." -ForegroundColor Cyan
Expand-Archive -LiteralPath $wintunArchiveDestination -DestinationPath $wintunExtractDir -Force

$wintunSourcePath = Join-Path $wintunExtractDir "wintun\bin\$wintunSourceSubdir\$WINTUN_DLL_NAME"
if (-not (Test-Path $wintunSourcePath)) {
    throw "Required Wintun DLL not found in archive: $wintunSourcePath"
}
Copy-Item $wintunSourcePath -Destination "$payloadDir\$WINTUN_DLL_NAME" -Force

# Build icon-related XML fragments (conditioned on $iconAvailable)
$iconAvailable = Test-Path $iconPath
$iconDefXml = ""
$shortcutIconAttr = ""
if ($iconAvailable) {
    $iconDefXml = @"

        <!-- Icon Definition -->
        <Icon Id="AliangIcon" SourceFile="$payloadDir\$ICON_FILE"/>
        <Property Id="ARPPRODUCTICON" Value="AliangIcon"/>
"@
    $shortcutIconAttr = 'Icon="AliangIcon"'
}

# Create WiX source file
Write-Host "Creating WiX source..." -ForegroundColor Cyan

$wxsContent = @"
<?xml version="1.0" encoding="UTF-8"?>
<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
    <Product Id="*" Name="Aliang" Language="1033" Version="$Version" Manufacturer="$MANUFACTURER" UpgradeCode="$UPGRADE_CODE">
        <Package InstallerVersion="500" Compressed="yes" InstallScope="perMachine" Platform="x64" Description="Aliang Gateway Proxy Client" Manufacturer="$MANUFACTURER"/>

        <MajorUpgrade DowngradeErrorMessage="A newer version of [ProductName] is already installed."/>
        <MediaTemplate EmbedCab="yes"/>
$iconDefXml
        <!-- Directory Structure -->
        <Directory Id="TARGETDIR" Name="SourceDir">
            <Directory Id="System64Folder">
                <Component Id="WintunDriverComponent" Guid="*" Win64="yes" Permanent="yes" NeverOverwrite="yes">
                    <File Id="WintunDll" Source="$payloadDir\$WINTUN_DLL_NAME" KeyPath="yes"/>
                </Component>
            </Directory>
            <Directory Id="ProgramFiles64Folder">
                <Directory Id="INSTALLFOLDER" Name="Aliang">
                    <Component Id="MainBinary" Guid="*" Win64="yes">
                        <File Id="Aliangexe" Source="$payloadDir\$BINARY_NAME" KeyPath="yes"/>
                        <ServiceInstall Id="AliangServiceInstall"
                                        Type="ownProcess"
                                        Name="$SERVICE_NAME"
                                        DisplayName="$SERVICE_DISPLAY_NAME"
                                        Description="$SERVICE_DESCRIPTION"
                                        Start="auto"
                                        ErrorControl="normal"
                                        Vital="yes"
                                        Account="LocalSystem"
                                        Arguments="core"/>
                        <ServiceControl Id="AliangServiceControl"
                                        Name="$SERVICE_NAME"
                                        Stop="both"
                                        Remove="uninstall"
                                        Wait="yes"/>
                        <RegistryValue Root="HKLM" Key="Software\Aliang" Name="InstallDir" Type="string" Value="[INSTALLFOLDER]"/>
                    </Component>
                </Directory>
            </Directory>
            <Directory Id="CommonAppDataFolder" Name="CommonAppData">
                <Directory Id="AliangData" Name="Aliang">
                    <Component Id="DataDirectory" Guid="*" Win64="yes">
                        <CreateFolder/>
                        <RegistryValue Root="HKLM" Key="Software\Aliang" Name="DataDir" Type="string" Value="[AliangData]" KeyPath="yes"/>
                    </Component>
                </Directory>
            </Directory>
            <Directory Id="ProgramMenuFolder">
                <Directory Id="ApplicationProgramsFolder" Name="Aliang">
                    <Component Id="StartMenuShortcut" Guid="*" Win64="yes">
                        <Shortcut Id="ApplicationStartMenuShortcut" Name="Aliang" Description="Aliang Gateway Proxy Client" Target="[INSTALLFOLDER]$BINARY_NAME" Arguments="companion" WorkingDirectory="INSTALLFOLDER" $shortcutIconAttr/>
                        <RemoveFolder Id="CleanUpShortCut" On="uninstall"/>
                        <RegistryValue Root="HKCU" Key="Software\Aliang" Name="StartMenuInstalled" Type="integer" Value="1" KeyPath="yes"/>
                    </Component>
                </Directory>
            </Directory>
            <Directory Id="DesktopFolder" Name="Desktop">
                <Component Id="DesktopShortcut" Guid="*" Win64="yes">
                    <Shortcut Id="ApplicationDesktopShortcut" Name="Aliang" Description="Aliang Gateway Proxy Client" Target="[INSTALLFOLDER]$BINARY_NAME" Arguments="companion" WorkingDirectory="INSTALLFOLDER" $shortcutIconAttr/>
                    <RegistryValue Root="HKCU" Key="Software\Aliang" Name="DesktopShortcutInstalled" Type="integer" Value="1" KeyPath="yes"/>
                </Component>
            </Directory>
        </Directory>

        <!-- Features -->
        <Feature Id="ProductFeature" Title="Aliang" Level="1">
            <ComponentRef Id="MainBinary"/>
            <ComponentRef Id="WintunDriverComponent"/>
            <ComponentRef Id="DataDirectory"/>
            <ComponentRef Id="EnvironmentComponent"/>
            <ComponentRef Id="StartMenuShortcut"/>
            <ComponentRef Id="DesktopShortcut"/>
        </Feature>

        <!-- Environment Variables (must be inside a Component) -->
        <DirectoryRef Id="INSTALLFOLDER">
            <Component Id="EnvironmentComponent" Guid="$ENV_COMPONENT_GUID" Win64="yes">
                <RegistryValue Root="HKLM" Key="Software\Aliang" Name="EnvironmentInstalled" Type="integer" Value="1" KeyPath="yes"/>
                <Environment Id="ALIANG_DATA_DIR" Name="ALIANG_DATA_DIR" Value="[AliangData]" Permanent="yes" Part="last" Action="set" System="yes"/>
                <Environment Id="ALIANG_LOG_DIR" Name="ALIANG_LOG_DIR" Value="[AliangData]\logs" Permanent="yes" Part="last" Action="set" System="yes"/>
                <Environment Id="ALIANG_SOCKET_PATH" Name="ALIANG_SOCKET_PATH" Value="\\.\pipe\aliang-core" Permanent="yes" Part="last" Action="set" System="yes"/>
            </Component>
        </DirectoryRef>
    </Product>
</Wix>
"@

$wxsPath = "$sourceDir\aliang.wxs"
$wxsContent | Out-File -FilePath $wxsPath -Encoding UTF8

# Build MSI using WiX
Write-Host "Building MSI..." -ForegroundColor Cyan
Push-Location $sourceDir

try {
    # Determine WiX executables to use
    if ($CandleExe -and (Test-Path $CandleExe)) {
        $useCandleExe = $CandleExe
    } else {
        $useCandleExe = "$wixPath\candle.exe"
    }
    if ($LightExe -and (Test-Path $LightExe)) {
        $useLightExe = $LightExe
    } else {
        $useLightExe = "$wixPath\light.exe"
    }

    # Compile WiX source
    Write-Host "Compiling WiX source with: $useCandleExe" -ForegroundColor Yellow
    & $useCandleExe -nologo -ext WixUIExtension -out "$sourceDir\aliang.wixobj" "$wxsPath"
    if ($LASTEXITCODE -ne 0) {
        throw "candle.exe failed with exit code $LASTEXITCODE"
    }

    # Link/Combine into MSI
    Write-Host "Linking into MSI with: $useLightExe" -ForegroundColor Yellow
    & $useLightExe -nologo -ext WixUIExtension -o "$resolvedOutputDir\aliang-$Version.msi" "$sourceDir\aliang.wixobj"
    if ($LASTEXITCODE -ne 0) {
        throw "light.exe failed with exit code $LASTEXITCODE"
    }

    Write-Host "MSI created successfully!" -ForegroundColor Green
    Write-Host "Output: $resolvedOutputDir\aliang-$Version.msi" -ForegroundColor Cyan
}
catch {
    Write-Host "Error building MSI: $_" -ForegroundColor Red
    throw
}
finally {
    Pop-Location
}

# Cleanup
Remove-Item -Path "$buildDir" -Recurse -Force -ErrorAction SilentlyContinue

Write-Host ""
Write-Host "=== Build Complete ===" -ForegroundColor Cyan
Write-Host "MSI Installer: $resolvedOutputDir\aliang-$Version.msi"
Write-Host ""
Write-Host "Note: To install, run: msiexec /i aliang-$Version.msi"
