package tray

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"aliang.one/nursorgate/app/agentruntime"
	httpServer "aliang.one/nursorgate/app/http"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/common/version"
	startupRuntime "aliang.one/nursorgate/processor/runtime"
	"github.com/getlantern/systray"
)

// TrayApp manages the system tray application
type TrayApp struct {
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

	isRunning  bool
	runService *services.RunService
	done       chan struct{}
	onStart    func()
	onStop     func()
	onRestart  func()
}

// NewTrayApp creates a new tray application instance
func NewTrayApp() *TrayApp {
	return &TrayApp{
		isRunning:  false,
		runService: services.GetSharedRunService(),
		done:       make(chan struct{}),
	}
}

// SetCallbacks sets the callback functions for menu actions
func (t *TrayApp) SetCallbacks(onStart, onStop, onRestart func()) {
	t.onStart = onStart
	t.onStop = onStop
	t.onRestart = onRestart
}

// onReady is called when the systray is ready
func onReady() {
	logger.Info("System tray initialized")

	// Set tray icon (inactive state initially)
	systray.SetIcon(GetIconDisabled())
	systray.SetTooltip("Aliang - Proxy Stopped")

	// Create menu items
	mOpenDashboard := systray.AddMenuItem("Open Dashboard", "Open web dashboard in browser")
	systray.AddSeparator()

	mProxyStatus := systray.AddMenuItem("Status: syncing status...", "Current proxy listener status")
	mProxyStatus.Disable()

	mModeHTTP := systray.AddMenuItemCheckbox("Regular Mode", "Choose Regular Mode for the next explicit start", false)
	mModeTUN := systray.AddMenuItemCheckbox("Deep Mode", "Choose Deep Mode for the next explicit start", false)

	systray.AddSeparator()

	mStart := systray.AddMenuItem("Start Proxy", "Start the selected Deep Mode or Regular Mode proxy listener")
	mStop := systray.AddMenuItem("Stop Proxy", "Stop the active Deep Mode or Regular Mode proxy listener")
	mStop.Disable()
	mRestart := systray.AddMenuItem("Restart Proxy", "Restart the active Deep Mode or Regular Mode proxy listener")
	mRestart.Disable()

	systray.AddSeparator()

	// Add version info
	versionInfo := fmt.Sprintf("Version: %s", version.String())
	mVersion := systray.AddMenuItem(versionInfo, "Application version")
	mVersion.Disable()

	systray.AddSeparator()

	// AI Acceleration status section (hidden by default)
	mAIHeader := systray.AddMenuItem("AI Acceleration", "AI acceleration status")
	mAIHeader.Disable()
	mAIHeader.Hide()

	var mAIProviders []*systray.MenuItem
	for i := 0; i < maxAIProviders; i++ {
		item := systray.AddMenuItem("", "AI provider status")
		item.Disable()
		item.Hide()
		mAIProviders = append(mAIProviders, item)
	}

	mQuit := systray.AddMenuItem("Quit", "Quit the application")

	app := NewTrayApp()
	app.mProxyStatus = mProxyStatus
	app.mModeHTTP = mModeHTTP
	app.mModeTUN = mModeTUN
	app.mStart = mStart
	app.mStop = mStop
	app.mRestart = mRestart
	app.mOpenDashboard = mOpenDashboard
	app.mQuit = mQuit
	app.mAIHeader = mAIHeader
	app.mAIProviders = mAIProviders

	// Set default callbacks
	app.SetCallbacks(
		func() { app.startProxy() },
		func() { app.stopProxy() },
		func() { app.restartProxy() },
	)

	// Ensure the dashboard/API server is available for local control UI.
	go func() {
		time.Sleep(250 * time.Millisecond)
		if err := agentruntime.EnsureStarted(); err != nil {
			logger.Warn(fmt.Sprintf("Failed to start user agent runtime from tray: %v", err))
		} else if err := services.RequestUserAgentSyncAfterAuth("agent_started"); err != nil {
			logger.Warn(fmt.Sprintf("Failed to sync user session to agent runtime: %v", err))
		}
		app.ensureDashboardServer()
		app.syncProxyState()
	}()

	// Handle menu clicks
	go app.handleMenuEvents()
	go app.syncProxyStateLoop()
}

// onExit is called when the systray is exiting
func onExit() {
	logger.Info("System tray exiting")
}

// handleMenuEvents handles all menu item click events
func (t *TrayApp) handleMenuEvents() {
	for {
		select {
		case <-t.mStart.ClickedCh:
			if t.onStart != nil {
				t.onStart()
			}

		case <-t.mStop.ClickedCh:
			if t.onStop != nil {
				t.onStop()
			}

		case <-t.mModeHTTP.ClickedCh:
			t.selectMode("http")

		case <-t.mModeTUN.ClickedCh:
			t.selectMode("tun")

		case <-t.mRestart.ClickedCh:
			if t.onRestart != nil {
				t.onRestart()
			}

		case <-t.mOpenDashboard.ClickedCh:
			t.openDashboard()

		case <-t.mQuit.ClickedCh:
			if t.quit() {
				return
			}
		}
	}
}

// startProxy starts the currently selected proxy mode.
func (t *TrayApp) startProxy() {
	if t.runService == nil {
		logger.Error("Tray run service is not initialized")
		return
	}

	logger.Debug("Starting proxy from tray...")
	result := t.runService.StartService()
	status := trayResultString(result, "status")
	if status == "failed" {
		logger.Error(fmt.Sprintf("Failed to start proxy from tray: %s", trayResultMessage(result)))
		t.syncProxyState()
		return
	}
	logger.Debug(fmt.Sprintf("Tray proxy start result: %s", trayResultMessage(result)))
	t.syncProxyState()
}

// stopProxy stops the currently active proxy mode.
func (t *TrayApp) stopProxy() {
	if t.runService == nil {
		logger.Error("Tray run service is not initialized")
		return
	}

	logger.Debug("Stopping proxy from tray...")
	result := t.runService.StopService()
	status := trayResultString(result, "status")
	if status == "failed" && trayResultString(result, "error") != "not_running" {
		logger.Error(fmt.Sprintf("Failed to stop proxy from tray: %s", trayResultMessage(result)))
		t.syncProxyState()
		return
	}
	logger.Debug(fmt.Sprintf("Tray proxy stop result: %s", trayResultMessage(result)))
	t.syncProxyState()
}

// restartProxy restarts the currently active proxy mode.
func (t *TrayApp) restartProxy() {
	logger.Debug("Restarting proxy from tray...")
	t.stopProxy()
	time.Sleep(500 * time.Millisecond)
	t.startProxy()
}

func (t *TrayApp) selectMode(mode string) {
	if t.runService == nil {
		logger.Error("Tray run service is not initialized")
		return
	}

	logger.Debug("Selecting " + mode + " mode from tray...")
	result := t.runService.SwitchMode(mode)
	if trayResultString(result, "status") == "failed" {
		logger.Error(fmt.Sprintf("Failed to switch tray mode: %s", trayResultMessage(result)))
		t.syncProxyState()
		return
	}

	logger.Debug(fmt.Sprintf("Tray mode switch result: %s", trayResultMessage(result)))
	t.syncProxyState()
}

func (t *TrayApp) syncModeMenu(mode string) {
	if t.mModeHTTP != nil {
		if mode == "http" {
			t.mModeHTTP.Check()
		} else {
			t.mModeHTTP.Uncheck()
		}
	}
	if t.mModeTUN != nil {
		if mode == "tun" {
			t.mModeTUN.Check()
		} else {
			t.mModeTUN.Uncheck()
		}
	}
}

// openDashboard opens the web dashboard in the default browser
func (t *TrayApp) openDashboard() {
	t.ensureDashboardServer()
	actualPort := t.waitForDashboardPort(3 * time.Second)
	if actualPort == "" {
		logger.Error("Failed to get dashboard port: HTTP server may not be ready")
		return
	}

	dashboardURL := fmt.Sprintf("http://localhost:%s", actualPort)

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
		logger.Error(fmt.Sprintf("Failed to open dashboard: %v", err))
	}
}

// quit exits the application
func (t *TrayApp) quit() bool {
	logger.Debug("Quitting application from tray...")

	if !t.stopProxyForQuit() {
		return false
	}

	select {
	case <-t.done:
	default:
		close(t.done)
	}

	if err := httpServer.StopHttpServer(); err != nil {
		logger.Error(fmt.Sprintf("Failed to stop dashboard server during quit: %v", err))
	}

	systray.Quit()
	os.Exit(0)
	return true
}

func (t *TrayApp) stopProxyForQuit() bool {
	if t.runService == nil {
		return true
	}

	logger.Debug("Ensuring proxy is stopped before quit...")
	result := t.runService.StopService()
	if isAcceptableQuitProxyStopResult(result) {
		logger.Debug(fmt.Sprintf("Tray quit proxy stop result: %s", trayResultMessage(result)))
		t.syncProxyState()
		return true
	}

	logger.Error(fmt.Sprintf("Failed to stop proxy during quit: %s", trayResultMessage(result)))
	t.syncProxyState()
	return false
}

func (t *TrayApp) ensureDashboardServer() {
	if httpServer.IsServerRunning() {
		return
	}

	logger.Debug("Starting dashboard HTTP server for tray...")
	httpServer.StartHttpServer()
}

func (t *TrayApp) waitForDashboardPort(timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if actualPort := httpServer.GetActualPort(); actualPort != "" {
			return actualPort
		}
		time.Sleep(100 * time.Millisecond)
	}
	return httpServer.GetActualPort()
}

func (t *TrayApp) syncProxyStateLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			t.syncProxyState()
		case <-t.done:
			return
		}
	}
}

func (t *TrayApp) syncProxyState() {
	if t.runService == nil {
		return
	}

	status := t.runService.GetStatus()
	running, _ := status["is_running"].(bool)
	mode := strings.ToLower(trayResultString(status, "current_mode"))
	if mode == "" {
		mode = "unknown"
	}
	modeLabel := trayModeDisplayName(mode)
	description := trayResultString(status, "status")

	t.isRunning = running
	t.syncModeMenu(mode)

	if t.mProxyStatus != nil {
		t.mProxyStatus.SetTitle(trayProxyStatusTitle(mode, running, description))
	}

	if t.mStart != nil {
		if running {
			t.mStart.Disable()
		} else {
			t.mStart.Enable()
		}
	}
	if t.mStop != nil {
		if running {
			t.mStop.Enable()
		} else {
			t.mStop.Disable()
		}
	}
	if t.mRestart != nil {
		if running {
			t.mRestart.Enable()
		} else {
			t.mRestart.Disable()
		}
	}

	if running {
		aiStatus := BuildAIStatusFromTracker()
		systray.SetIcon(GetIcon())
		systray.SetTooltip(trayProxyTooltip(mode, true))
		t.updateAIStatusMenu(aiStatus)
		return
	}

	systray.SetIcon(GetIconDisabled())
	t.hideAIStatus()
	startupStatus := startupRuntime.GetStartupState().GetStatus()
	if startupStatus == startupRuntime.READY || startupStatus == startupRuntime.CONFIGURED {
		systray.SetTooltip(trayProxyTooltip(mode, false))
		return
	}
	systray.SetTooltip(fmt.Sprintf("Aliang - %s Unavailable (%s)", modeLabel, startupStatus))
}

func trayResultString(result map[string]interface{}, key string) string {
	if result == nil {
		return ""
	}
	value, _ := result[key].(string)
	return value
}

func trayResultMessage(result map[string]interface{}) string {
	for _, key := range []string{"msg", "message", "details", "status"} {
		if value := trayResultString(result, key); value != "" {
			return value
		}
	}
	return "unknown result"
}

func isAcceptableQuitProxyStopResult(result map[string]interface{}) bool {
	if result == nil {
		return false
	}

	status := trayResultString(result, "status")
	if status == "success" {
		return true
	}

	return status == "failed" && trayResultString(result, "error") == "not_running"
}

func (t *TrayApp) updateAIStatusMenu(providers []AIProviderStatus) {
	if !applyAIStatusMenu(t.mAIHeader, t.mAIProviders, providers) {
		t.hideAIStatus()
	}
}

func (t *TrayApp) hideAIStatus() {
	if t.mAIHeader != nil {
		t.mAIHeader.Hide()
	}
	for _, item := range t.mAIProviders {
		item.Hide()
	}
}

// Run starts the system tray application
// This is the main entry point for the tray application
func Run() {
	logger.Debug("Starting system tray...")
	systray.Run(onReady, onExit)
}
