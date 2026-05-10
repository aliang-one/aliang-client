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
VERSION="${VERSION:-1.0.0}"
ARCH="${ARCH:-}"

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
	if [ "$(uname)" = "Darwin" ]; then
		go build -ldflags="-s -w -X aliang.one/nursorgate/common/version.BuildMode=prod" -o "$SCRIPT_DIR/aliang" ./cmd/aliang/main.go
	else
		CGO_ENABLED=0 go build -ldflags="-s -w -X aliang.one/nursorgate/common/version.BuildMode=prod" -o "$SCRIPT_DIR/aliang" ./cmd/aliang/main.go
	fi
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
if [ -f "$SCRIPT_DIR/Aliang.icns" ]; then
    cp "$SCRIPT_DIR/Aliang.icns" "$APP_DIR/Contents/Resources/Aliang.icns"
    echo "=== App icon copied ==="
else
    echo "=== Warning: Aliang.icns not found, skipping icon ==="
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

# Stop and remove old core LaunchDaemon if exists (system-wide)
echo "Preinstall: Stopping old core LaunchDaemon..."
launchctl bootout "system/one.aliang.aliang.core" 2>&1 || true
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

# Bootstrap as system LaunchDaemon
echo "Postinstall: Bootstrapping Core service..."
if launchctl bootstrap "system" "$PLIST_PATH" 2>&1; then
    echo "Postinstall: Core service registered successfully"
else
    echo "Postinstall: WARNING - bootstrap returned non-zero (service may already be registered)"
fi

echo "Postinstall: Core service setup complete"
POSTINSTALL_SCRIPT
chmod +x "$SCRIPTS_DIR/postinstall"

# Step 7: No need to put plist in app bundle - postinstall creates it directly
echo "=== App bundle and Core binary ready ==="

# Step 8: Build component package with pkgbuild
echo "=== Building component package ==="
pkgbuild --identifier one.aliang.aliang \
    --version "$VERSION" \
    --root "$PAYLOAD_DIR" \
    --scripts "$SCRIPTS_DIR" \
    --install-location "/" \
    "$BUILD_DIR/Aliang.pkg"

# Step 9: Create distribution package with productbuild
echo "=== Building distribution package ==="
productbuild --identifier one.aliang.aliang \
    --version "$VERSION" \
    --package "$BUILD_DIR/Aliang.pkg" \
    "$PKG_PATH"

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
