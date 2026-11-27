//go:build windows

package tray

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"aliang.one/nursorgate/app/agentruntime"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/common/version"
	"aliang.one/nursorgate/internal/ipc"
	"aliang.one/nursorgate/internal/singleinstance"
	"aliang.one/nursorgate/processor/setup"
	"github.com/getlantern/systray"
	"golang.org/x/sys/windows"
)

const maxCompanionTraceSize = 128 * 1024

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

	// mPanelOpen「打开面板」恢复项(M2,见 panel_bridge_m2.go):面板模式
	// 隐藏,兜底态(连崩 3 次)Show,点击 → RetryFromFallback。
	mPanelOpen *systray.MenuItem
	// panelLauncher M2 面板 launcher(installPanelLauncher 装配,nil=未装配)。
	panelLauncher *PanelLauncher

	ipcClient *ipc.Client
	httpURL   string
	isRunning bool
	// autostart 标记本次 companion 由登录自启拉起(MSI HKCU Run 键传
	// --autostart,2026-11-27 P0)。与手动双击的行为差异只有两点:不自动
	// 弹仪表盘浏览器、启动失败不弹模态 MessageBox(无人值守,只落 trace);
	// 托盘菜单、agent 拉起与看护完全一致。
	autostart bool
	// lastViewKey 记录上次实际落到 GDI 面的可见面签名(签名构造见
	// state_gate.go):周期轮询撞到相同签名即整轮跳过,止血 systray
	// MenuItem.SetIcon 每次新建 HBITMAP 不销毁的泄漏(~83min 耗尽 10k 配额)。
	lastViewKey string
	done        chan struct{}
	// quitOnce 退出承诺点护栏(M2 评审 M-2):panelQuitInvoker 引入首个
	// 并发触发点(forwardPanelExit goroutine 的 code=60 转投可与托盘菜单
	// quit 并发),护 close(done) 恰一次,防两次退出交叠 double close panic。
	quitOnce sync.Once
}

func NewCompanionApp() *CompanionApp {
	a := &CompanionApp{
		ipcClient: ipc.NewClient(),
		httpURL:   panelDefaultBaseURL, // 基址字面拷贝三处归一,见 panel_bridge.go
		done:      make(chan struct{}),
	}
	// M2 面板 launcher 在这里(而非 onReady)装配:字段写在任何菜单/转投
	// goroutine 启动之前,handlePanelOpenClick/forwardPanelExit 的读天然
	// happens-before,无 race 面(见 panel_bridge_m2.go newPanelLauncher)。
	a.newPanelLauncher()
	return a
}

func (a *CompanionApp) onReady() {
	logger.Debug("Windows tray companion initialized")

	systray.SetIcon(GetIconDisabled())
	systray.SetTooltip("Aliang - Starting background service...")

	a.mOpenDashboard = systray.AddMenuItem("Open Dashboard", "Open the background service dashboard in browser")
	systray.AddSeparator()

	a.mProxyStatus = systray.AddMenuItem("Status: starting service...", "Current proxy listener status")
	a.mProxyStatus.Disable()

	a.mModeHTTP = systray.AddMenuItemCheckbox("Regular Mode", "Choose Regular Mode for the next explicit start", false)
	a.mModeTUN = systray.AddMenuItemCheckbox("Deep Mode", "Choose Deep Mode for the next explicit start", false)

	systray.AddSeparator()

	a.mStart = systray.AddMenuItem("Start Proxy", "Start the active proxy listener in the background service")
	a.mStop = systray.AddMenuItem("Stop Proxy", "Stop the active proxy listener in the background service")
	a.mStop.Disable()
	a.mRestart = systray.AddMenuItem("Restart Proxy", "Restart the active proxy listener in the background service")
	a.mRestart.Disable()

	systray.AddSeparator()
	versionInfo := fmt.Sprintf("Version: %s", version.String())
	mVersion := systray.AddMenuItem(versionInfo, "Application version")
	mVersion.Disable()

	systray.AddSeparator()

	// M2 托盘面板:「打开面板」恢复项——库只有 append 语义,必须排在 AI
	// header 之前创建;面板模式隐藏,兜底态(连崩 3 次)Show,点击走
	// handlePanelOpenClick(见 panel_bridge_m2.go)。
	a.mPanelOpen = systray.AddMenuItem("Open Panel", "Reopen the tray panel after repeated crashes")
	panelOpenItemVisor(a.mPanelOpen, false)
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

	a.mQuit = systray.AddMenuItem("Quit Aliang", "Stop the background proxy and quit the companion")

	goWithCompanionTrace("connectAndStartHTTP", func() {
		a.connectAndStartHTTP()
		goWithCompanionTrace("handleMenuEvents", a.handleMenuEvents)
		goWithCompanionTrace("syncStateLoop", a.syncStateLoop)
		goWithCompanionTrace("agentWatchdogLoop", a.agentWatchdogLoop)
	})

	// M2 托盘面板接线(收尾动作):左右键点击 → launcher.Click(面板进程
	// 开合/崩溃兜底,见 panel_bridge_m2.go),取代 M1 的浏览器形态。Windows
	// 侧 panelClickFn 非 nil 即接管左右键(wndProc 分支),无摘菜单动作;
	// 挂接仍放在菜单构建完成之后,与 macOS 侧时序语义保持一致。
	a.installPanelLauncher()
}

func (a *CompanionApp) onExit() {
	logger.Debug("Windows tray companion exiting")
}

func (a *CompanionApp) connectAndStartHTTP() {
	serviceName := setup.GetServiceName()
	writeWindowsCompanionTrace("connectAndStartHTTP service=%s", serviceName)

	if a.waitForIPC(1500 * time.Millisecond) {
		logger.Debug("Connected to existing Windows service via IPC")
	} else {
		status, err := setup.GetServiceStatus(serviceName, true)
		if err != nil {
			logger.Warn("Failed to query Windows service status before startup", "error", err)
		}

		serviceRunning := err == nil && status != nil && (status.IsRunning || status.Status == "start_pending")
		if !serviceRunning {
			logger.Debug("Windows service not running, starting it...")
			if err := startServiceWithElevation(serviceName); err != nil {
				logger.Error("Failed to start Windows service", "error", err)
				a.failStartup("background service start failed", fmt.Sprintf("后台服务启动失败：%v", err))
				return
			}
		} else {
			logger.Debug("Windows service already running, waiting for IPC...")
		}

		if !a.waitForIPC(15 * time.Second) {
			a.failStartup("background service startup timed out", "后台服务启动超时，未能建立 IPC 连接。")
			return
		}
	}

	resp, err := a.ipcClient.Send(ipc.ActionStartHTTP, nil)
	if err != nil {
		logger.Error("Failed to start dashboard via IPC", "error", err)
		a.failStartup("failed to start dashboard", fmt.Sprintf("启动控制面板失败：%v", err))
		return
	}
	if !resp.OK {
		a.failStartup("background service rejected dashboard start", "后台服务拒绝启动控制面板。")
		return
	}

	if data, ok := resp.Data.(map[string]interface{}); ok {
		if port, ok := data["port"].(string); ok && port != "" {
			a.httpURL = "http://127.0.0.1:" + port
		} else if portNum, ok := data["port"].(float64); ok {
			a.httpURL = fmt.Sprintf("http://127.0.0.1:%d", int(portNum))
		}
	}

	if err := agentruntime.EnsureStarted(); err != nil {
		logger.Warn("Failed to start user agent runtime from Windows companion", "error", err)
	} else if err := a.syncAgentAuthFromCore("companion_agent_started"); err != nil {
		logger.Warn("Failed to request agent auth sync from core", "error", err)
	}

	a.syncState()
	if !a.autostart {
		// 自动弹面板只属于「用户主动双击启动」语义;登录自启(Run 键)
		// 形态弹浏览器等于每次登录必开一个窗口(2026-11-27 对抗评审
		// blocker)。macOS companion 本就不自动弹,此处对齐。
		go func() {
			time.Sleep(300 * time.Millisecond)
			a.openDashboard()
		}()
	}
}

func (a *CompanionApp) waitForIPC(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := a.ipcClient.Connect(); err == nil {
			return true
		}
		time.Sleep(400 * time.Millisecond)
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
		case <-a.mPanelOpen.ClickedCh:
			a.handlePanelOpenClick()
		case <-a.mQuit.ClickedCh:
			if a.quit() {
				return
			}
		case <-a.done:
			return
		}
	}
}

func (a *CompanionApp) startProxy() {
	result, err := a.ipcClient.Send(ipc.ActionStartProxy, nil)
	if err != nil {
		logger.Error("Failed to start proxy from Windows companion", "error", err)
		a.applyUnavailableState("background service unavailable")
		return
	}
	if !result.OK {
		logger.Error("Background service rejected proxy start", "error", result.Error)
	}
	a.syncState()
}

func (a *CompanionApp) stopProxy() {
	result, err := a.ipcClient.Send(ipc.ActionStopProxy, nil)
	if err != nil {
		logger.Error("Failed to stop proxy from Windows companion", "error", err)
		a.applyUnavailableState("background service unavailable")
		return
	}
	if !result.OK && result.Error != "not_running" {
		logger.Error("Background service rejected proxy stop", "error", result.Error)
	}
	a.syncState()
}

func (a *CompanionApp) selectMode(mode string) {
	result, err := a.ipcClient.Send(ipc.ActionSwitchMode, ipc.SwitchModeArgs{Mode: mode})
	if err != nil {
		logger.Error("Failed to switch mode from Windows companion", "error", err)
		a.applyUnavailableState("background service unavailable")
		return
	}
	if !result.OK {
		logger.Error("Background service rejected mode switch", "error", result.Error)
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
		a.syncUnavailableGated("background service unavailable")
		return
	}
	if !result.OK {
		a.syncUnavailableGated("background service error")
		return
	}

	data, ok := result.Data.(map[string]interface{})
	if !ok {
		a.syncUnavailableGated("invalid service state")
		return
	}

	mode := strings.ToLower(trayResultString(data, "current_mode"))
	if mode == "" {
		mode = "unknown"
	}
	running, _ := data["is_running"].(bool)
	description := trayResultString(data, "status")

	// 登录门槛：未运行时向 core 查询启动拦截原因（与点击 Start 后 StartService
	// 的强制校验同源），未登录/恢复中提前置灰 Start，而不是等用户点击后失败。
	startBlockCode, startBlockMsg := "", ""
	if !running {
		startBlockCode, startBlockMsg = a.proxyStartBlockedReason()
	}

	// 可见面变更检测:签名不变即整轮跳过,不再触碰 GDI 面(state_gate.go
	// 头注释记录了 0.196 GDI 泄漏事故的完整机理)。isRunning 仍须记账,
	// quit 闸与菜单点击路径读它。
	providers := BuildAIStatusFromIPCData(data)
	viewKey := buildTrayViewKey(mode, running, startBlockCode, providers)
	if viewKey == a.lastViewKey {
		a.isRunning = running
		return
	}
	a.lastViewKey = viewKey

	a.isRunning = running
	a.syncModeMenu(mode)
	if running {
		systray.SetIcon(GetIcon())
		systray.SetTooltip(trayProxyTooltip(mode, true))
	} else {
		systray.SetIcon(GetIconDisabled())
		systray.SetTooltip(trayProxyTooltip(mode, false))
	}

	statusTitle := trayProxyStatusTitle(mode, running, description)
	if blockedTitle := trayStartBlockedStatusTitle(startBlockCode); blockedTitle != "" {
		statusTitle = blockedTitle
	}
	a.mProxyStatus.SetTitle(statusTitle)
	a.mModeHTTP.Enable()
	a.mModeTUN.Enable()

	startDisabled, startTooltip := startMenuItemGate(running, startBlockMsg)
	if startDisabled {
		a.mStart.Disable()
	} else {
		a.mStart.Enable()
	}
	if startTooltip != "" {
		a.mStart.SetTooltip(startTooltip)
	}

	if running {
		a.mStop.Enable()
		a.mRestart.Enable()
		a.updateAIStatusMenu(providers)
	} else {
		a.mStop.Disable()
		a.mRestart.Disable()
		a.hideAIStatus()
	}
}

// syncUnavailableGated 对 IPC 不可达态做变更检测后再落 applyUnavailableState:
// 周期轮询每 2s 撞一次错误,同一 reason 反复出现时不再触碰 GDI 面。点击路径
// (startProxy 等)仍直走 applyUnavailableState,立即反馈不受此门控。
func (a *CompanionApp) syncUnavailableGated(reason string) {
	key := buildTrayUnavailableViewKey(reason)
	if key == a.lastViewKey {
		return
	}
	a.lastViewKey = key
	a.applyUnavailableState(reason)
}

// goWithCompanionTrace 以带取证落盘的方式启动 goroutine:panic 时先写
// companion trace 再原样 re-panic(崩溃语义不变,只补证据)。companion 是
// windowsgui 构建,panic 栈默认写进无处可去的 stderr,默认 GOTRACEBACK 下
// 也不产 WER 事件——进程会无痕消失(2026-11-20 0.196 今晨实例即此形态),
// 本函数就是给这条死亡路径补的取证口。
func goWithCompanionTrace(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				writeWindowsCompanionTrace("panic in %s: %v", name, r)
				logger.Error("Windows companion goroutine panicked", "where", name, "panic", r)
				panic(r)
			}
		}()
		fn()
	}()
}

// proxyStartBlockedReason queries the core's login gate for starting the proxy.
// Returns empty strings when the query fails (old core, IPC error) so the menu
// falls back to the enabled default — the click path still enforces the gate.
func (a *CompanionApp) proxyStartBlockedReason() (string, string) {
	result, err := a.ipcClient.Send(ipc.ActionProxyStartBlockedReason, nil)
	if err != nil || result == nil || !result.OK {
		return "", ""
	}
	data, ok := result.Data.(map[string]interface{})
	if !ok {
		return "", ""
	}
	return trayResultString(data, "code"), trayResultString(data, "msg")
}

func (a *CompanionApp) applyUnavailableState(reason string) {
	systray.SetIcon(GetIconDisabled())
	systray.SetTooltip("Aliang - Service Unavailable")
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
}

func (a *CompanionApp) openDashboard() {
	cmd := newBackgroundCommand("rundll32", "url.dll,FileProtocolHandler", a.httpURL)
	if err := cmd.Start(); err != nil {
		logger.Error("Failed to open dashboard from Windows companion", "error", err)
	}
}

func (a *CompanionApp) quit() bool {
	logger.Debug("Quitting Windows tray companion...")

	if !a.stopProxyForQuit() {
		showWindowsCompanionMessage("Aliang", "无法确认代理已经停止，已取消退出。请稍后重试。")
		return false
	}

	// 双触发护栏(M2 评审 M-2):panelQuitInvoker(M2)引入首个并发触发点
	// ——forwardPanelExit goroutine 的 code=60 转投可与托盘菜单 quit 并发
	// (G1 卡在阻塞式 MessageBoxW 确认窗期间用户再触发),旧 select 判废
	// 形态下两次触发可同时走到 default 分支 → double close(done) panic。
	// Once 护承诺点:仅首次过闸的触发 close done 并走完整退出序列,并发
	// 迟到者直接返回 false 让位(收尾归首次触发);确认窗取消路径在 Once
	// 之前返回、不消费护栏,下次触发照常重试——首次触发语义不变。
	committed := false
	a.quitOnce.Do(func() {
		committed = true
		close(a.done)
	})
	if !committed {
		return false
	}

	_, _ = a.ipcClient.Send(ipc.ActionStopHTTP, nil)
	_ = a.ipcClient.Close()

	systray.Quit()
	os.Exit(0)
	return true
}

func (a *CompanionApp) stopProxyForQuit() bool {
	logger.Debug("Ensuring background proxy is stopped before quit...")

	result, err := a.ipcClient.Send(ipc.ActionStopProxy, nil)
	if err != nil {
		logger.Error("Failed to stop proxy during Windows companion quit", "error", err)
		return false
	}

	if !result.OK {
		if strings.Contains(strings.ToLower(result.Error), "not_running") {
			logger.Debug("Background proxy was already stopped before quit")
			return true
		}

		logger.Error("Background service rejected proxy stop during quit", "error", result.Error)
		return false
	}

	data, _ := result.Data.(map[string]interface{})
	if isAcceptableQuitProxyStopResult(data) {
		logger.Debug(fmt.Sprintf("Windows companion quit proxy stop result: %s", trayResultMessage(data)))
		return true
	}

	logger.Error("Background proxy stop returned an unexpected result during quit", "result", result.Data)
	return false
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
	runCompanion(false)
}

// RunCompanionAutostart 是登录自启形态入口(Run 键 `companion --autostart`):
// 无人值守启动,见 CompanionApp.autostart 注释。
func RunCompanionAutostart() {
	runCompanion(true)
}

func runCompanion(autostart bool) {
	// 必须在 systray 创建任何窗口/菜单之前声明 DPI 感知：
	// Win11 非 100% 缩放下，DPI-unaware 进程的托盘菜单会渲染成空白方块。
	enableWindowsDPIAwareness()

	guard, acquired, err := singleinstance.Acquire()
	if err != nil {
		logger.Error("Failed to acquire Windows companion single-instance guard", "error", err)
		return
	}
	if !acquired {
		logger.Debug("Aliang companion is already running, exiting duplicate launch request.")
		return
	}
	defer guard.Close()

	app := NewCompanionApp()
	app.autostart = autostart
	systray.Run(app.onReady, app.onExit)
}

func (a *CompanionApp) failStartup(reason, message string) {
	a.applyUnavailableState(reason)
	writeWindowsCompanionTrace("failStartup reason=%s", reason)
	if a.autostart {
		// 登录自启形态无人值守:模态 MessageBox 会拦在登录现场等一个
		// 不存在的点击。只落 trace,静默退出等下轮登录/手动启动重试
		// (2026-11-27 对抗评审 blocker 的次级问题)。
		logger.Warn(fmt.Sprintf("companion autostart failed: reason=%s", reason))
		go func() {
			time.Sleep(200 * time.Millisecond)
			systray.Quit()
			os.Exit(1)
		}()
		return
	}
	showWindowsCompanionMessage("Aliang", message)
	go func() {
		time.Sleep(200 * time.Millisecond)
		systray.Quit()
		os.Exit(1)
	}()
}

func showWindowsCompanionMessage(title, body string) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	messageBox := user32.NewProc("MessageBoxW")

	titlePtr, _ := windows.UTF16PtrFromString(title)
	bodyPtr, _ := windows.UTF16PtrFromString(body)

	const mbOK = 0x00000000
	const mbIconError = 0x00000010

	messageBox.Call(0, uintptr(unsafe.Pointer(bodyPtr)), uintptr(unsafe.Pointer(titlePtr)), mbOK|mbIconError)
}

func writeWindowsCompanionTrace(format string, args ...interface{}) {
	path := filepath.Join(os.TempDir(), "aliang-companion.log")
	if info, err := os.Stat(path); err == nil && info.Size() >= maxCompanionTraceSize {
		_ = os.Remove(path)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	line := fmt.Sprintf(format, args...)
	_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), line)
}
