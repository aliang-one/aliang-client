# System Tray Module

This module provides a cross-platform system tray for Nonelane.

## Features

- ✅ Cross-platform support (Linux, macOS, Windows)
- ✅ Right-click menu with common actions
- ✅ Start/Stop/Restart server
- ✅ Open web dashboard in browser
- ✅ Version information
- ✅ Graceful quit

## Usage

### Start with System Tray

```bash
# Start with tray icon
./nonelane tray

# Start with config file and tray
./nonelane tray --config ./config.json

# Start with tray and token
./nonelane tray --token your-token
```

### Menu Options

- **Open Dashboard**: Opens the web interface in your default browser
- **Start Server**: Starts the HTTP server
- **Stop Server**: Stops the HTTP server
- **Restart Server**: Restarts the HTTP server
- **Version**: Shows application version
- **Quit**: Exits the application

## Icon Setup

The tray icon is embedded at build time from platform-specific files in this directory:

- `icon-active.png` / `icon-inactive.png` for Linux and macOS
- `icon-active.ico` / `icon-inactive.ico` for Windows

To customize:

1. Prepare active and inactive variants of the icon
2. Replace the matching files for your target platform
3. Rebuild the application

### Platform-Specific Icon Guidelines

- **Windows**: Use ICO files for tray icons
- **macOS**: Use PNG with transparency
- **Linux**: Use PNG with transparency

## Architecture

```
app/tray/
├── tray.go             # Main tray application logic
├── icon_nonwindows.go  # PNG embedding for Linux/macOS
├── icon_windows.go     # ICO embedding for Windows
├── icon-active.png     # Active tray icon for Linux/macOS
├── icon-inactive.png   # Inactive tray icon for Linux/macOS
├── icon-active.ico     # Active tray icon for Windows
├── icon-inactive.ico   # Inactive tray icon for Windows
└── README.md           # This file
```

## How It Works

1. When `nonelane tray` is executed, the `runTray` function in `cmd/tray.go` is called
2. It loads configuration (if provided)
3. It initializes the system tray using `systray.Run()`
4. The tray icon appears in the system tray area
5. Users can right-click to access the menu
6. Server starts automatically when tray is ready

## Dependencies

- `github.com/getlantern/systray`: Cross-platform system tray library

## Troubleshooting

### Linux

On Linux, you may need to install `libappindicator3-dev`:

```bash
# Ubuntu/Debian
sudo apt-get install libappindicator3-dev

# Fedora
sudo dnf install libappindicator-gtk3-devel

# Arch Linux
sudo pacman -S libappindicator
```

### macOS

No additional dependencies required.

### Windows

No additional dependencies required.

## Future Enhancements

- [ ] Add status indicator in the icon (running/stopped)
- [ ] Add configuration dialog
- [ ] Add log viewer
- [ ] Support for multiple themes
- [ ] Notifications for important events
