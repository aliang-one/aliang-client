//go:build !windows

package tray

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/common/version"
	"aliang.one/nursorgate/internal/ipc"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/setup"
	"github.com/getlantern/systray"
)

const (
	companionHTTPURL = "http://" + config.DefaultManagementAddr
)

// CompanionApp is the macOS tray companion that communicates with Core via IPC.
type CompanionApp struct {
	mProxyStatus   *systray.MenuItem
	mModeHTTP      *systray.MenuItem
	mModeTUN       *systray.MenuItem
	mStart         *systray.MenuItem
	mStop          *systray.MenuItem
	mRestart       *systray.MenuItem
	mOpenDashboard *systray.MenuItem
	mQuit          *systray.MenuItem

	mAIHeader    *systray.MenuItem
	mAIProviders []*systray.MenuItem

	ipcClient    *ipc.Client
	httpURL      string
	isRunning    bool
	coreReady    bool
	reconnectSeq int
	done         chan struct{}
}

// NewCompanionApp creates a new CompanionApp with IPC client.
func NewCompanionApp() *CompanionApp {
	return &CompanionApp{
		ipcClient: ipc.NewClient(),
		httpURL:   companionHTTPURL,
		done:      make(chan struct{}),
	}
}

func (a *CompanionApp) onReady() {
	logger.Debug("macOS tray companion initialized")

	systray.SetIcon(GetIconDisabled())
	systray.SetTooltip("Aliang - Starting...")

	a.mOpenDashboard = systray.AddMenuItem("Open Dashboard", "Open the service dashboard in browser")
	systray.AddSeparator()

	a.mProxyStatus = systray.AddMenuItem("Status: starting core...", "Current proxy listener status")
	a.mProxyStatus.Disable()

	a.mModeHTTP = systray.AddMenuItemCheckbox("Regular Mode", "Choose Regular Mode for the next explicit start", false)
	a.mModeTUN = systray.AddMenuItemCheckbox("Deep Mode", "Choose Deep Mode for the next explicit start", false)

	systray.AddSeparator()

	a.mStart = systray.AddMenuItem("Start", "Start the active proxy listener in the background service")
	a.mStop = systray.AddMenuItem("Stop", "Stop the active proxy listener in the background service")
	a.mStop.Disable()
	a.mRestart = systray.AddMenuItem("Restart", "Restart the active proxy listener in the background service")
	a.mRestart.Disable()

	systray.AddSeparator()
	versionInfo := fmt.Sprintf("Version: %s", version.String())
	mVersion := systray.AddMenuItem(versionInfo, "Application version")
	mVersion.Disable()

	systray.AddSeparator()

	// AI Acceleration status section (hidden by default)
	a.mAIHeader = systray.AddMenuItem("AI Acceleration", "AI acceleration status")
	a.mAIHeader.Disable()
	a.mAIHeader.Hide()

	for i := 0; i < maxAIProviders; i++ {
		item := systray.AddMenuItem("", "AI provider status")
		item.Disable()
		item.Hide()
		a.mAIProviders = append(a.mAIProviders, item)
	}

	a.mQuit = systray.AddMenuItem("Quit Aliang", "Quit the menu bar companion (Core keeps running)")

	// Connect to Core and start HTTP dashboard
	go func() {
		a.connectAndStartHTTP()
		go a.handleMenuEvents()
		go a.syncStateLoop()
	}()
}

func (a *CompanionApp) onExit() {
	logger.Debug("macOS tray companion exiting")
}

// connectAndStartHTTP connects to Core via IPC and starts the HTTP dashboard.
func (a *CompanionApp) connectAndStartHTTP() {
	// Check if core service plist is installed
	if !setup.IsCoreServiceInstalled() {
		logger.Error("Core service is not installed")
		a.applyUnavailableState("core service not installed")
		return
	}

	// Check if core is already running via launchd
	if !setup.IsCoreServiceRunning() {
		logger.Debug("Core service not running, starting via kickstart...")
		if err := setup.KickstartCoreService(); err != nil {
			logger.Error(fmt.Sprintf("Failed to kickstart core service: %v", err))
			a.applyUnavailableState("core service start failed")
			return
		}
	}

	// Wait for IPC to become available (max 10 seconds)
	if !a.waitForIPC(10 * time.Second) {
		logger.Error("Core service did not become ready within 10 seconds")
		a.applyUnavailableState("core service startup timed out")
		return
	}

	// Start HTTP dashboard via IPC
	resp, err := a.ipcClient.Send(ipc.ActionStartHTTP, nil)
	if err != nil {
		logger.Error(fmt.Sprintf("Failed to start HTTP dashboard via IPC: %v", err))
		a.applyUnavailableState("failed to start HTTP dashboard")
		return
	}

	if !resp.OK {
		logger.Error(fmt.Sprintf("Core rejected start_http: %s", resp.Error))
		a.applyUnavailableState("core rejected start_http")
		return
	}

	// Extract port from response
	if data, ok := resp.Data.(map[string]interface{}); ok {
		if port, ok := data["port"].(string); ok && port != "" {
			a.httpURL = "http://127.0.0.1:" + port
		} else if portNum, ok := data["port"].(float64); ok {
			a.httpURL = fmt.Sprintf("http://127.0.0.1:%d", int(portNum))
		}
	}

	a.coreReady = true
	logger.Debug(fmt.Sprintf("Core connected, HTTP dashboard available at %s", a.httpURL))
}

// waitForIPC waits for the IPC socket to become available.
func (a *CompanionApp) waitForIPC(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := a.ipcClient.Connect(); err == nil {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func (a *CompanionApp) handleMenuEvents() {
	for {
		select {
		case <-a.mStart.ClickedCh:
			a.startProxy()
		case <-a.mStop.ClickedCh:
			a.stopProxy()
		case <-a.mModeHTTP.ClickedCh:
			a.selectMode("http")
		case <-a.mModeTUN.ClickedCh:
			a.selectMode("tun")
		case <-a.mRestart.ClickedCh:
			a.restartProxy()
		case <-a.mOpenDashboard.ClickedCh:
			a.openDashboard()
		case <-a.mQuit.ClickedCh:
			a.quit()
			return
		case <-a.done:
			return
		}
	}
}

func (a *CompanionApp) startProxy() {
	logger.Debug("Starting proxy from tray companion...")
	result, err := a.ipcClient.Send(ipc.ActionStartProxy, nil)
	if err != nil {
		logger.Error(fmt.Sprintf("Failed to start proxy from tray companion: %v", err))
		a.applyUnavailableState(fmt.Sprintf("service unavailable (%v)", err))
		return
	}

	if !result.OK {
		logger.Error(fmt.Sprintf("Background service rejected tray start request: %s", result.Error))
	}
	a.syncState()
}

func (a *CompanionApp) stopProxy() {
	logger.Debug("Stopping proxy from tray companion...")
	result, err := a.ipcClient.Send(ipc.ActionStopProxy, nil)
	if err != nil {
		logger.Error(fmt.Sprintf("Failed to stop proxy from tray companion: %v", err))
		a.applyUnavailableState(fmt.Sprintf("service unavailable (%v)", err))
		return
	}

	if !result.OK && !strings.Contains(result.Error, "not_running") {
		logger.Error(fmt.Sprintf("Background service rejected tray stop request: %s", result.Error))
	}
	a.syncState()
}

func (a *CompanionApp) selectMode(mode string) {
	logger.Debug(fmt.Sprintf("Selecting %s mode from tray companion...", mode))
	result, err := a.ipcClient.Send(ipc.ActionSwitchMode, ipc.SwitchModeArgs{Mode: mode})
	if err != nil {
		logger.Error(fmt.Sprintf("Failed to switch mode from tray companion: %v", err))
		a.applyUnavailableState(fmt.Sprintf("service unavailable (%v)", err))
		return
	}

	if !result.OK {
		logger.Error(fmt.Sprintf("Background service rejected tray mode switch: %s", result.Error))
	}
	a.syncState()
}

func (a *CompanionApp) restartProxy() {
	a.stopProxy()
	time.Sleep(400 * time.Millisecond)
	a.startProxy()
}

func (a *CompanionApp) syncModeMenu(mode string) {
	if a.mModeHTTP != nil {
		if mode == "http" {
			a.mModeHTTP.Check()
		} else {
			a.mModeHTTP.Uncheck()
		}
	}
	if a.mModeTUN != nil {
		if mode == "tun" {
			a.mModeTUN.Check()
		} else {
			a.mModeTUN.Uncheck()
		}
	}
}

func (a *CompanionApp) stopProxyIfNeeded() {
	if !a.isRunning {
		return
	}
	logger.Debug("Stopping proxy before quit...")
	a.ipcClient.Send(ipc.ActionStopProxy, nil)
}

func (a *CompanionApp) syncStateLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			a.syncState()
		case <-a.done:
			return
		}
	}
}

func (a *CompanionApp) syncState() {
	result, err := a.ipcClient.Send(ipc.ActionGetStatus, nil)
	if err != nil {
		a.reconnectSeq++
		a.handleCoreUnavailable()
		return
	}

	// Core is reachable — reset reconnect counter
	a.reconnectSeq = 0
	a.coreReady = true

	if !result.OK {
		a.handleCoreError(result.Error)
		return
	}

	data, ok := result.Data.(map[string]interface{})
	if !ok {
		a.handleCoreError("invalid status response")
		return
	}

	running, _ := data["is_running"].(bool)
	mode := strings.ToLower(trayResultString(data, "current_mode"))
	if mode == "" {
		mode = "unknown"
	}

	description := trayResultString(data, "status")

	a.isRunning = running
	a.syncModeMenu(mode)
	if a.mProxyStatus != nil {
		a.mProxyStatus.SetTitle(trayProxyStatusTitle(mode, running, description))
	}
	if a.mModeHTTP != nil {
		a.mModeHTTP.Enable()
	}
	if a.mModeTUN != nil {
		a.mModeTUN.Enable()
	}
	if a.mStart != nil {
		if running {
			a.mStart.Disable()
		} else {
			a.mStart.Enable()
		}
	}
	if a.mStop != nil {
		if running {
			a.mStop.Enable()
		} else {
			a.mStop.Disable()
		}
	}
	if a.mRestart != nil {
		if running {
			a.mRestart.Enable()
		} else {
			a.mRestart.Disable()
		}
	}

	if running {
		systray.SetIcon(GetIcon())
		systray.SetTooltip(trayProxyTooltip(mode, true))
		aiStatus := BuildAIStatusFromIPCData(data)
		a.updateAIStatusMenu(aiStatus)
		return
	}

	systray.SetIcon(GetIconDisabled())
	systray.SetTooltip(trayProxyTooltip(mode, false))
	a.hideAIStatus()
}

// handleCoreUnavailable is called when the core IPC is unreachable.
func (a *CompanionApp) handleCoreUnavailable() {
	if !setup.IsCoreServiceInstalled() {
		a.applyUnavailableState("core service not installed")
		return
	}

	// Check if core is still registered with launchd
	if setup.IsCoreServiceRunning() {
		a.applyUnavailableState("core service starting...")
		return
	}

	// Core has stopped — attempt to restart it
	if a.reconnectSeq <= 3 {
		logger.Debug(fmt.Sprintf("Core service stopped, attempting restart (attempt %d)...", a.reconnectSeq))
		if err := setup.KickstartCoreService(); err != nil {
			logger.Error(fmt.Sprintf("Failed to restart core service: %v", err))
		}
		a.applyUnavailableState("core service restarting...")
		return
	}

	a.applyUnavailableState("core service unavailable")
}

// handleCoreError handles errors from core responses.
func (a *CompanionApp) handleCoreError(errMsg string) {
	if strings.Contains(errMsg, "not_running") {
		a.applyUnavailableState("proxy not running")
		return
	}
	a.applyUnavailableState(fmt.Sprintf("core error: %s", errMsg))
}

func (a *CompanionApp) applyUnavailableState(reason string) {
	a.isRunning = false
	if a.mProxyStatus != nil {
		a.mProxyStatus.SetTitle(fmt.Sprintf("Status: %s", reason))
	}
	if a.mStart != nil {
		a.mStart.Disable()
	}
	if a.mModeHTTP != nil {
		a.mModeHTTP.Disable()
	}
	if a.mModeTUN != nil {
		a.mModeTUN.Disable()
	}
	if a.mStop != nil {
		a.mStop.Disable()
	}
	if a.mRestart != nil {
		a.mRestart.Disable()
	}
	a.hideAIStatus()

	systray.SetIcon(GetIconDisabled())
	systray.SetTooltip(fmt.Sprintf("Aliang - %s", reason))
}

func (a *CompanionApp) openDashboard() {
	dashboardURL := a.httpURL
	if dashboardURL == "" {
		dashboardURL = companionHTTPURL
	}

	var cmdName string
	var args []string
	switch runtime.GOOS {
	case "linux":
		cmdName = "xdg-open"
		args = []string{dashboardURL}
	case "windows":
		cmdName = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", dashboardURL}
	case "darwin":
		cmdName = "open"
		args = []string{dashboardURL}
	default:
		logger.Error(fmt.Sprintf("Unsupported platform: %s", runtime.GOOS))
		return
	}

	cmd := newBackgroundCommand(cmdName, args...)
	if err := cmd.Start(); err != nil {
		logger.Error(fmt.Sprintf("Failed to open dashboard from tray companion: %v", err))
	}
}

func (a *CompanionApp) quit() {
	select {
	case <-a.done:
	default:
		close(a.done)
	}

	// 1. Stop proxy if running
	a.stopProxyIfNeeded()

	// 2. Stop HTTP dashboard via IPC (but keep Core running)
	logger.Debug("Stopping HTTP dashboard via IPC...")
	a.ipcClient.Send(ipc.ActionStopHTTP, nil)

	// 3. Close IPC connection
	a.ipcClient.Close()

	// 4. Exit tray
	systray.Quit()
	os.Exit(0)
}

func (a *CompanionApp) updateAIStatusMenu(providers []AIProviderStatus) {
	if !applyAIStatusMenu(a.mAIHeader, a.mAIProviders, providers) {
		a.hideAIStatus()
	}
}

func (a *CompanionApp) hideAIStatus() {
	if a.mAIHeader != nil {
		a.mAIHeader.Hide()
	}
	for _, item := range a.mAIProviders {
		item.Hide()
	}
}

func RunCompanion() {
	app := NewCompanionApp()
	logger.Debug("Starting macOS tray companion...")
	systray.Run(app.onReady, app.onExit)
}
