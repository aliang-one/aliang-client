#!/bin/bash
set -e

# Build PKG installer for macOS
# Architecture: Tray(.app/Shell) + Core(LaunchDaemon/system-wide)

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
BUILD_DIR="$SCRIPT_DIR/build-pkg"
PAYLOAD_DIR="$BUILD_DIR/payload"
SCRIPTS_DIR="$BUILD_DIR/scripts"
APP_DIR="$PAYLOAD_DIR/Applications/Aliang.app"
CORE_DIR="$PAYLOAD_DIR/Library/Application Support/one.aliang.aliang"
VERSION="${VERSION:-$(git -C "$PROJECT_DIR" tag --points-at HEAD --sort=-version:refname 2>/dev/null | head -1)}"
if [ -z "$VERSION" ]; then
	echo "ERROR: VERSION is required when HEAD has no release tag" >&2
	exit 1
fi
BUNDLE_VERSION="${VERSION#v}"
ARCH="${ARCH:-}"
MAC_APPLICATION_IDENTITY="${MAC_APPLICATION_IDENTITY:-}"
MAC_INSTALLER_IDENTITY="${MAC_INSTALLER_IDENTITY:-}"
REQUIRE_SIGNED_RELEASE="${REQUIRE_SIGNED_RELEASE:-0}"

if [ "$REQUIRE_SIGNED_RELEASE" = "1" ] && { [ -z "$MAC_APPLICATION_IDENTITY" ] || [ -z "$MAC_INSTALLER_IDENTITY" ]; }; then
	echo "ERROR: signed release requires MAC_APPLICATION_IDENTITY and MAC_INSTALLER_IDENTITY" >&2
	exit 1
fi

if [ -z "$ARCH" ]; then
    case "$(uname -m)" in
        x86_64)
            ARCH="amd64"
            ;;
        arm64|aarch64)
            ARCH="arm64"
            ;;
        *)
            ARCH="$(uname -m)"
            ;;
    esac
fi

PKG_BASENAME="Aliang-${VERSION}-${ARCH}"
PKG_PATH="$SCRIPT_DIR/${PKG_BASENAME}.pkg"

echo "=== Building Aliang PKG Installer ==="
echo "Project dir: $PROJECT_DIR"
echo "Version: $VERSION"
echo "Architecture: $ARCH"

# Clean previous build
rm -rf "$BUILD_DIR"
mkdir -p "$APP_DIR/Contents/MacOS"
mkdir -p "$APP_DIR/Contents/Resources"
mkdir -p "$CORE_DIR"
mkdir -p "$SCRIPTS_DIR"

# Step 1: Build the binary (skip if pre-built binary already exists, e.g. from CI)
if [ -x "$SCRIPT_DIR/aliang" ]; then
	echo "=== Pre-built binary found, skipping compile ==="
else
	echo "=== Building aliang binary ==="
	cd "$PROJECT_DIR"

	# On macOS, CGO is required for systray (Cocoa/Objective-C)
	COMMIT="$(git rev-parse --short HEAD)"
	LDFLAGS="-s -w -X aliang.one/nursorgate/common/version.Version=$VERSION -X aliang.one/nursorgate/common/version.GitCommit=$COMMIT -X aliang.one/nursorgate/common/version.BuildMode=prod"
	if [ "$(uname)" = "Darwin" ]; then
		go build -ldflags="$LDFLAGS" -o "$SCRIPT_DIR/aliang" ./cmd/aliang/main.go
	else
		CGO_ENABLED=0 go build -ldflags="$LDFLAGS" -o "$SCRIPT_DIR/aliang" ./cmd/aliang/main.go
	fi
fi

BINARY_VERSION="$("$SCRIPT_DIR/aliang" version 2>/dev/null | awk 'NR == 1 { print $NF }')"
if [ -z "$BINARY_VERSION" ] || [ "$BINARY_VERSION" = "unknown" ] || [ "${BINARY_VERSION#v}" != "$BUNDLE_VERSION" ]; then
	echo "ERROR: binary version ${BINARY_VERSION:-unknown} does not match package version $VERSION" >&2
	exit 1
fi

# Step 2: Copy binary to app bundle (Shell entry point)
echo "=== Copying binary to app bundle ==="
cp "$SCRIPT_DIR/aliang" "$APP_DIR/Contents/MacOS/aliang"
chmod +x "$APP_DIR/Contents/MacOS/aliang"

# Step 3: Copy binary to Core location (for LaunchDaemon)
echo "=== Copying binary to Core location ==="
cp "$SCRIPT_DIR/aliang" "$CORE_DIR/aliang"
chmod +x "$CORE_DIR/aliang"

# Step 4: Copy Info.plist and icon
echo "=== Copying Info.plist and icon ==="
cp "$SCRIPT_DIR/Info.plist" "$APP_DIR/Contents/Info.plist"
plutil -replace CFBundleShortVersionString -string "$BUNDLE_VERSION" "$APP_DIR/Contents/Info.plist"
plutil -replace CFBundleVersion -string "$BUNDLE_VERSION" "$APP_DIR/Contents/Info.plist"
if [ -f "$SCRIPT_DIR/Aliang.icns" ]; then
    cp "$SCRIPT_DIR/Aliang.icns" "$APP_DIR/Contents/Resources/Aliang.icns"
    echo "=== App icon copied ==="
else
    echo "=== Warning: Aliang.icns not found, skipping icon ==="
fi

# Sign both installed executables before packaging. The app bundle owns its own
# copy while the LaunchDaemon installs the Core copy separately.
if [ -n "$MAC_APPLICATION_IDENTITY" ]; then
	echo "=== Signing app and Core binaries ==="
	codesign --force --options runtime --timestamp \
		--sign "$MAC_APPLICATION_IDENTITY" "$CORE_DIR/aliang"
	codesign --force --options runtime --timestamp \
		--sign "$MAC_APPLICATION_IDENTITY" "$APP_DIR"
else
	echo "=== App signing skipped (development package only) ==="
fi

# Step 5: Create preinstall script
echo "=== Creating preinstall script ==="
cat > "$SCRIPTS_DIR/preinstall" << 'PREINSTALL_SCRIPT'
#!/bin/bash

echo "Preinstall: Stopping old services..."

# Get current user info
CURRENT_USER=$(whoami)
USER_ID=$(id -u "$CURRENT_USER")
echo "Preinstall: Running as user: $CURRENT_USER (UID: $USER_ID)"

# Stop and remove old tray agent if exists (LaunchAgent style)
echo "Preinstall: Stopping old tray agent..."
launchctl bootout "gui/${USER_ID}/one.aliang.tray" 2>&1 || true
rm -f "$HOME/Library/LaunchAgents/one.aliang.tray.plist" 2>&1 || true

# Stop and remove old core LaunchAgent if exists
echo "Preinstall: Stopping old core LaunchAgent..."
launchctl bootout "gui/${USER_ID}/one.aliang.core" 2>&1 || true
rm -f "$HOME/Library/LaunchAgents/one.aliang.core.plist" 2>&1 || true

# Stop the old Core completely. Continuing while launchd still owns the old
# process makes Installer report success while macOS keeps executing old code.
echo "Preinstall: Stopping old core LaunchDaemon..."
if launchctl print "system/one.aliang.aliang.core" >/dev/null 2>&1; then
    if ! launchctl bootout "system/one.aliang.aliang.core" 2>&1; then
        echo "Preinstall: ERROR - failed to stop the existing Core service" >&2
        exit 1
    fi
    for _ in $(seq 1 30); do
        if ! launchctl print "system/one.aliang.aliang.core" >/dev/null 2>&1; then
            break
        fi
        sleep 1
    done
    if launchctl print "system/one.aliang.aliang.core" >/dev/null 2>&1; then
        echo "Preinstall: ERROR - old Core service did not exit within 30 seconds" >&2
        exit 1
    fi
fi
rm -f "/Library/LaunchDaemons/one.aliang.aliang.core.plist" 2>&1 || true

echo "Preinstall: Old services cleaned up"
PREINSTALL_SCRIPT
chmod +x "$SCRIPTS_DIR/preinstall"

# Step 6: Create postinstall script
echo "=== Creating postinstall script ==="
cat > "$SCRIPTS_DIR/postinstall" << 'POSTINSTALL_SCRIPT'
#!/bin/bash

echo "Postinstall: Setting up Core service..."

# Create system-level directories
echo "Postinstall: Creating system directories..."

# Socket directory
mkdir -p "/var/run/"
chmod 755 "/var/run/"

# Log directory
LOG_DIR="/Library/Logs/Aliang"
mkdir -p "$LOG_DIR"
chmod 755 "$LOG_DIR"

# Data directory
DATA_DIR="/Library/Application Support/one.aliang.aliang"
mkdir -p "$DATA_DIR"
chmod 755 "$DATA_DIR"

echo "Postinstall: System directories ready"

# Migrate old user data if exists
OLD_DATA_DIR="$HOME/.aliang"
if [ -d "$OLD_DATA_DIR" ] && [ ! -f "$DATA_DIR/config.json" ]; then
    echo "Postinstall: Migrating old user data from $OLD_DATA_DIR..."
    cp -r "$OLD_DATA_DIR/"* "$DATA_DIR/" 2>/dev/null || true
    echo "Postinstall: Data migration complete"
fi

# Create LaunchDaemon plist
echo "Postinstall: Creating LaunchDaemon plist..."
PLIST_PATH="/Library/LaunchDaemons/one.aliang.aliang.core.plist"

cat > "$PLIST_PATH" << 'PLIST_EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>one.aliang.aliang.core</string>
	<key>ProgramArguments</key>
	<array>
		<string>/Library/Application Support/one.aliang.aliang/aliang</string>
		<string>core</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>WorkingDirectory</key>
	<string>/Library/Application Support/one.aliang.aliang</string>
	<key>StandardOutPath</key>
	<string>/Library/Logs/Aliang/core.log</string>
	<key>StandardErrorPath</key>
	<string>/Library/Logs/Aliang/core.error.log</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>ALIANG_DATA_DIR</key>
		<string>/Library/Application Support/one.aliang.aliang</string>
		<key>ALIANG_LOG_DIR</key>
		<string>/Library/Logs/Aliang</string>
		<key>ALIANG_SOCKET_PATH</key>
		<string>/var/run/aliang-core.sock</string>
	</dict>
</dict>
</plist>
PLIST_EOF

chmod 644 "$PLIST_PATH"
echo "Postinstall: LaunchDaemon plist created at $PLIST_PATH"

# Bootstrap as system LaunchDaemon. A non-zero result is an installation
# failure: otherwise the files update while the live service remains stale.
echo "Postinstall: Bootstrapping Core service..."
if ! launchctl bootstrap "system" "$PLIST_PATH" 2>&1; then
    echo "Postinstall: ERROR - failed to register Core service" >&2
    exit 1
fi
launchctl kickstart -k "system/one.aliang.aliang.core"
for _ in $(seq 1 30); do
    if launchctl print "system/one.aliang.aliang.core" 2>/dev/null | grep -q "state = running"; then
        echo "Postinstall: Core service is running"
        break
    fi
    sleep 1
done
if ! launchctl print "system/one.aliang.aliang.core" 2>/dev/null | grep -q "state = running"; then
    echo "Postinstall: ERROR - Core service did not reach running state" >&2
    exit 1
fi

echo "Postinstall: Core service setup complete"
POSTINSTALL_SCRIPT
chmod +x "$SCRIPTS_DIR/postinstall"

# Step 7: No need to put plist in app bundle - postinstall creates it directly
echo "=== App bundle and Core binary ready ==="

# Step 8: Build component package with pkgbuild
echo "=== Building component package ==="
PKGBUILD_ARGS=(
	--identifier one.aliang.aliang
	--version "$VERSION"
	--root "$PAYLOAD_DIR"
	--scripts "$SCRIPTS_DIR"
	--install-location "/"
)
if [ -n "$MAC_INSTALLER_IDENTITY" ]; then
	PKGBUILD_ARGS+=(--sign "$MAC_INSTALLER_IDENTITY")
fi
pkgbuild "${PKGBUILD_ARGS[@]}" "$BUILD_DIR/Aliang.pkg"

# Step 9: Create distribution package with productbuild
echo "=== Building distribution package ==="
PRODUCTBUILD_ARGS=(
	--identifier one.aliang.aliang
	--version "$VERSION"
	--package "$BUILD_DIR/Aliang.pkg"
)
if [ -n "$MAC_INSTALLER_IDENTITY" ]; then
	PRODUCTBUILD_ARGS+=(--sign "$MAC_INSTALLER_IDENTITY")
fi
productbuild "${PRODUCTBUILD_ARGS[@]}" "$PKG_PATH"

# Fail the release before upload if the app metadata drifted from the binary.
PACKAGED_BUNDLE_VERSION="$(plutil -extract CFBundleShortVersionString raw "$APP_DIR/Contents/Info.plist")"
if [ "$PACKAGED_BUNDLE_VERSION" != "$BUNDLE_VERSION" ]; then
	echo "ERROR: packaged bundle version $PACKAGED_BUNDLE_VERSION does not match $BUNDLE_VERSION" >&2
	exit 1
fi

if [ -n "$MAC_INSTALLER_IDENTITY" ]; then
	pkgutil --check-signature "$PKG_PATH"
fi

echo ""
echo "=== Build Complete ==="
echo "PKG Installer: $PKG_PATH"
echo ""
echo "Installation:"
echo "  - Aliang.app will be installed to /Applications/Aliang.app"
echo "  - Core binary installed to /Library/Application Support/one.aliang.aliang/aliang"
echo "  - Core service (LaunchDaemon) registered with system-wide scope"
echo "  - Core starts automatically at system boot"
echo "  - Opening Aliang.app starts the Shell which connects to Core via IPC"
