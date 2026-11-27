package tray

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/getlantern/systray"
)

// companion M2 面板接线测试(Task 15):spawn 参数组装、锚定判废与兜底、
// 兜底切换(菜单显隐翻转)、launcher 装配正确性、退出闸对接。全部经注入口
// (panelAnchorSampler / panelProcessSpawner / panelExecutable /
// panelOpenItemVisor / panelQuitInvoker)拦截真实系统调用——测试零真实进程、
// 零真实托盘、零真实退出。

// restorePanelM2Seams 记录 M2 接线注入口当前实现并在测试结束时恢复(包级
// var 不恢复会跨测试互染;与 M1 的 restorePanelBridgeSeams 同型)。
func restorePanelM2Seams(t *testing.T) {
	t.Helper()

	origSampler, origSpawner, origExe := panelAnchorSampler, panelProcessSpawner, panelExecutable
	origVisor, origQuit := panelOpenItemVisor, panelQuitInvoker
	origOpener, origNotifier := panelURLOpener, panelNotifier
	origRegister, origMode := panelClickRegister, panelModeSetter
	t.Cleanup(func() {
		panelAnchorSampler, panelProcessSpawner, panelExecutable = origSampler, origSpawner, origExe
		panelOpenItemVisor, panelQuitInvoker = origVisor, origQuit
		panelURLOpener, panelNotifier = origOpener, origNotifier
		panelClickRegister, panelModeSetter = origRegister, origMode
	})
}

// m2Sample 构造锚定采样:全字段可指定,缺省形态为合法底部任务栏矩形 + 光标
// 在(500,1400)。
func m2Sample(ax, ay, w, h, screenW, screenH int, ok bool) panelAnchorSample {
	return panelAnchorSample{
		AX: ax, AY: ay, W: w, H: h,
		ScreenW: screenW, ScreenH: screenH,
		CursorX: 500, CursorY: 1400, HasCursor: true, OK: ok,
	}
}

func TestPanelM2AnchorUsable(t *testing.T) {
	tests := []struct {
		name                 string
		ax, ay, w, h, sw, sh int
		want                 bool
	}{
		{"常规底部任务栏矩形", 900, 1400, 22, 22, 1440, 2560, true},
		{"常规顶部任务栏矩形", 900, 40, 22, 22, 1440, 2560, true},
		{"占位矩形形态(0,-33)", 0, -33, 22, 22, 1440, 2560, false},
		{"x=0但y非负(左上角真实图标可放行)", 0, 0, 22, 22, 1440, 2560, true},
		{"y越过屏高(屏外/溢出区形态)", 900, 2560, 22, 22, 1440, 2560, false},
		{"宽超1.5倍主屏", 0, 40, 4000, 22, 1440, 2560, false},
		{"高超1.5倍主屏", 0, 40, 22, 4000, 1440, 2560, false},
		{"屏幕尺寸未知只做形态判废(macOS直传)", 0, -33, 22, 22, 0, 0, false},
		{"屏幕尺寸未知且形态合法", 900, 0, 22, 22, 0, 0, true},
		{"零宽矩形", 900, 1400, 0, 22, 1440, 2560, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := panelAnchorUsable(tc.ax, tc.ay, tc.w, tc.h, tc.sw, tc.sh); got != tc.want {
				t.Fatalf("panelAnchorUsable(%d,%d,%d,%d,%d,%d) = %v, want %v",
					tc.ax, tc.ay, tc.w, tc.h, tc.sw, tc.sh, got, tc.want)
			}
		})
	}
}

func TestPanelM2SideForTray(t *testing.T) {
	// 图标中心在屏幕下半 → bottom(面板向上弹);上半 → top(面板向下弹);
	// 屏幕尺寸未知(macOS 直传,菜单栏恒顶部)→ 恒 top。
	tests := []struct {
		name           string
		ay, h, screenH int
		want           string
	}{
		{"底部任务栏", 1400, 22, 2560, panelSideBottom},
		{"顶部任务栏", 40, 22, 2560, panelSideTop},
		{"屏幕尺寸未知(macOS)", 40, 22, 0, panelSideTop},
		{"图标压中线偏下", 1271, 22, 2560, panelSideBottom},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := panelSideForTray(tc.ay, tc.h, tc.screenH); got != tc.want {
				t.Fatalf("panelSideForTray(%d,%d,%d) = %q, want %q", tc.ay, tc.h, tc.screenH, got, tc.want)
			}
		})
	}
}

// TestPanelM2SpawnOptsAssembly opts 组装(spawn 参数的现取口):URL=面板页
// (运行时真实端口)、open-dashboard/open-peer=仪表盘根、锚定判废三分支
// (合法直用 / 占位判废走光标兜底 / 无光标走中性顶部锚)、方位随图标半屏。
func TestPanelM2SpawnOptsAssembly(t *testing.T) {
	restorePanelM2Seams(t)

	t.Run("合法矩形直用+底部任务栏", func(t *testing.T) {
		panelAnchorSampler = func() panelAnchorSample {
			return m2Sample(900, 1400, 22, 22, 2560, 1600, true)
		}
		a := &CompanionApp{httpURL: "http://127.0.0.1:56789"}
		opts := a.panelSpawnOpts()
		if opts.URL != "http://127.0.0.1:56789/panel.html" {
			t.Fatalf("URL = %q, want 运行时真实端口的 /panel.html", opts.URL)
		}
		if opts.OpenDashboard != "http://127.0.0.1:56789" || opts.OpenPeer != "http://127.0.0.1:56789" {
			t.Fatalf("open 深链未取仪表盘根:%+v", opts)
		}
		if opts.AnchorX != 900 || opts.AnchorY != 1400 || opts.IconW != 22 || opts.IconH != 22 {
			t.Fatalf("图标矩形未直传:%+v", opts)
		}
		if opts.PanelW != panelW || opts.PanelH != panelH {
			t.Fatalf("面板逻辑尺寸失真:%d×%d, want %d×%d", opts.PanelW, opts.PanelH, panelW, panelH)
		}
		if opts.Side != panelSideBottom {
			t.Fatalf("底部任务栏 side = %q, want bottom", opts.Side)
		}
	})

	t.Run("占位矩形判废走光标兜底", func(t *testing.T) {
		panelAnchorSampler = func() panelAnchorSample {
			return m2Sample(0, -33, 22, 22, 2560, 1600, true) // Task 4 探针实测占位形态
		}
		a := &CompanionApp{httpURL: "http://127.0.0.1:56789"}
		opts := a.panelSpawnOpts()
		if opts.AnchorX != 500-11 || opts.AnchorY != 1400-11 || opts.IconW != 22 || opts.IconH != 22 {
			t.Fatalf("占位矩形未走光标兜底:%+v", opts)
		}
	})

	t.Run("无光标平台判废走中性顶部锚", func(t *testing.T) {
		s := m2Sample(0, -33, 22, 22, 0, 0, true)
		s.HasCursor = false // macOS 形态
		panelAnchorSampler = func() panelAnchorSample { return s }
		a := &CompanionApp{httpURL: "http://127.0.0.1:56789"}
		opts := a.panelSpawnOpts()
		if opts.AnchorX != 0 || opts.AnchorY != panelMenuBarNeutralY {
			t.Fatalf("无光标判废未走中性顶部锚:%+v", opts)
		}
		if opts.Side != panelSideTop {
			t.Fatalf("中性锚 side = %q, want top", opts.Side)
		}
	})

	t.Run("TrayRect失败同样判废兜底", func(t *testing.T) {
		panelAnchorSampler = func() panelAnchorSample {
			return m2Sample(0, 0, 0, 0, 2560, 1600, false) // ok=false(托盘未初始化)
		}
		a := &CompanionApp{httpURL: "http://127.0.0.1:56789"}
		opts := a.panelSpawnOpts()
		if opts.AnchorX != 500-11 || opts.AnchorY != 1400-11 {
			t.Fatalf("ok=false 未走光标兜底:%+v", opts)
		}
	})
}

// TestPanelM2SpawnArgsAssembly panel 子命令参数组装纯函数:与 cmd/panel.go
// 的 flags(--url --ax --ay --w --h --pw --ph --side --open-dashboard
// --open-peer)一一对应,子命令名 panel,可执行文件本体由 exec.Command 首参
// 携带(不在本函数返回值内)。
func TestPanelM2SpawnArgsAssembly(t *testing.T) {
	args := panelSpawnArgs(SpawnOpts{
		URL:           "http://127.0.0.1:56789/panel.html",
		AnchorX:       900,
		AnchorY:       1400,
		IconW:         22,
		IconH:         22,
		PanelW:        340,
		PanelH:        560,
		Side:          panelSideBottom,
		OpenDashboard: "http://127.0.0.1:56789",
		OpenPeer:      "http://127.0.0.1:56789",
	})
	want := []string{
		"panel",
		"--url", "http://127.0.0.1:56789/panel.html",
		"--ax", "900", "--ay", "1400", "--w", "22", "--h", "22",
		"--pw", "340", "--ph", "560", "--side", "bottom",
		"--open-dashboard", "http://127.0.0.1:56789",
		"--open-peer", "http://127.0.0.1:56789",
	}
	if len(args) != len(want) {
		t.Fatalf("参数个数 = %d, want %d:%v", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q(全量:%v)", i, args[i], want[i], args)
		}
	}
}

// TestPanelM2LauncherWiring 装配正确性:installPanelLauncher 注册的托盘点击
// 回调必须是 launcher.Click(未开 spawn / 已开 Kill),spawn 经
// panelProcessSpawner,opts 现取现用(每次 spawn 重新采样)。
func TestPanelM2LauncherWiring(t *testing.T) {
	restorePanelM2Seams(t)

	spawnCalls := 0
	optsSamples := 0
	var lastOpts SpawnOpts
	var liveChild *fakeChild
	panelAnchorSampler = func() panelAnchorSample {
		optsSamples++
		return m2Sample(100*optsSamples, 1400, 22, 22, 2560, 1600, true)
	}
	panelProcessSpawner = func(opts SpawnOpts) (ChildProcess, error) {
		spawnCalls++
		lastOpts = opts
		liveChild = newFakeChild()
		return liveChild, nil
	}
	panelQuitInvoker = func(*CompanionApp) {}
	panelOpenItemVisor = func(*systray.MenuItem, bool) {}

	var registered func()
	panelClickRegister = func(fn func()) { registered = fn }
	modeCalls := make([]bool, 0, 1)
	panelModeSetter = func(on bool) { modeCalls = append(modeCalls, on) }

	a := &CompanionApp{httpURL: "http://127.0.0.1:56789"}
	a.installPanelLauncher()

	if registered == nil {
		t.Fatal("installPanelLauncher 未注册托盘点击回调")
	}
	if len(modeCalls) != 1 || !modeCalls[0] {
		t.Fatalf("installPanelLauncher 面板模式调用 = %v, want [true]", modeCalls)
	}
	if a.panelLauncher == nil {
		t.Fatal("installPanelLauncher 未落 a.panelLauncher")
	}

	registered() // 点击 = 开面板
	if spawnCalls != 1 || optsSamples != 1 {
		t.Fatalf("首次点击未 spawn:calls=%d samples=%d", spawnCalls, optsSamples)
	}
	if lastOpts.AnchorX != 100 || lastOpts.Side != panelSideBottom || lastOpts.URL != "http://127.0.0.1:56789/panel.html" {
		t.Fatalf("spawn 参数组装失真(锚定现取现用):%+v", lastOpts)
	}
	registered() // 再点 = 收起
	if liveChild.kills != 1 {
		t.Fatalf("已开状态点击未 Kill:kills=%d", liveChild.kills)
	}
}

// TestPanelM2FallbackToggle 兜底切换:连崩 3 次(截获重拉调度,零真实定时器)
// → onFallback 恢复原生菜单形态(nil 回调 + 关面板模式 + 「打开面板」项
// Show + 桌面通知);菜单点「打开面板」→ RetryFromFallback + 面板模式恢复
// (回调重挂 + 项 Hide + 点击语义回归 toggle)。
func TestPanelM2FallbackToggle(t *testing.T) {
	restorePanelM2Seams(t)

	panelAnchorSampler = func() panelAnchorSample { return m2Sample(900, 1400, 22, 22, 2560, 1600, true) }
	spawnCalls := 0
	var liveChild *fakeChild
	panelProcessSpawner = func(SpawnOpts) (ChildProcess, error) {
		spawnCalls++
		liveChild = newFakeChild()
		return liveChild, nil
	}
	panelQuitInvoker = func(*CompanionApp) {}
	panelNotifier = func(string, string) {}

	var registered func()
	panelClickRegister = func(fn func()) { registered = fn }
	modeCalls := make([]bool, 0, 4)
	panelModeSetter = func(on bool) { modeCalls = append(modeCalls, on) }
	visorCalls := make([]bool, 0, 4)
	panelOpenItemVisor = func(_ *systray.MenuItem, show bool) { visorCalls = append(visorCalls, show) }

	a := &CompanionApp{httpURL: "http://127.0.0.1:56789"}
	// mPanelOpen 以零值 MenuItem 代真:显隐走 stub 替身,不触碰 AppKit/wndProc。
	a.mPanelOpen = &systray.MenuItem{}
	a.installPanelLauncher()
	retries := make([]func(), 0, 2) // 截获重拉到期动作(零真实定时器)
	a.panelLauncher.scheduleRetry = func(_ time.Duration, fn func()) { retries = append(retries, fn) }
	registered() // 开面板

	// 连崩 3 次:第 1 次退出直接转投;后两次先触发放挂的重拉(重开)再退出
	for i := 0; i < 3; i++ {
		if i > 0 {
			if len(retries) == 0 {
				t.Fatalf("第 %d 崩前无挂起重拉", i+1)
			}
			fn := retries[len(retries)-1]
			retries = retries[:len(retries)-1]
			fn()
		}
		alive, killedByUs := a.panelLauncher.exitReport()
		a.panelLauncher.childExited(1, alive, killedByUs)
	}
	if registered != nil {
		t.Fatal("兜底态点击回调未置 nil(原生菜单未恢复)")
	}
	if len(modeCalls) == 0 || modeCalls[len(modeCalls)-1] {
		t.Fatalf("兜底态未关面板模式:modeCalls=%v", modeCalls)
	}
	if len(visorCalls) != 1 || !visorCalls[0] {
		t.Fatalf("「打开面板」项显隐调用 = %v, want [true(Show)]", visorCalls)
	}

	// 菜单点「打开面板」:退出兜底态 + 立即 spawn + 面板模式恢复
	a.handlePanelOpenClick()
	if spawnCalls != 4 { // 首开 + 两次自动重拉 + Retry 立即 spawn
		t.Fatalf("Retry 未立即 spawn:calls=%d", spawnCalls)
	}
	if registered == nil {
		t.Fatal("恢复后点击回调未重挂(launcher.Click)")
	}
	if len(modeCalls) == 0 || !modeCalls[len(modeCalls)-1] {
		t.Fatalf("恢复后未重开面板模式:modeCalls=%v", modeCalls)
	}
	if len(visorCalls) != 2 || visorCalls[1] {
		t.Fatalf("恢复后「打开面板」项显隐调用 = %v, want 第二次 false(Hide)", visorCalls)
	}
	registered() // 恢复后点击语义回归 toggle
	if liveChild.kills != 1 {
		t.Fatalf("恢复后点击未收起:kills=%d", liveChild.kills)
	}
}

// TestPanelM2QuitGateWiring 面板内退出(code=60)→ onQuitRequested → 既有
// 退出闸(panelQuitInvoker 缺省转投 CompanionApp.quit;Windows 侧 quit 内含
// stopProxyForQuit 确认闸,取消即留在托盘)。
func TestPanelM2QuitGateWiring(t *testing.T) {
	restorePanelM2Seams(t)

	panelAnchorSampler = func() panelAnchorSample { return m2Sample(900, 1400, 22, 22, 2560, 1600, true) }
	panelProcessSpawner = func(SpawnOpts) (ChildProcess, error) {
		return newFakeChild(), nil
	}
	panelOpenItemVisor = func(*systray.MenuItem, bool) {}

	quits := 0
	panelQuitInvoker = func(*CompanionApp) { quits++ }

	var registered func()
	panelClickRegister = func(fn func()) { registered = fn }
	panelModeSetter = func(bool) {}

	a := &CompanionApp{httpURL: "http://127.0.0.1:56789"}
	a.installPanelLauncher()
	registered() // 开面板

	alive, killedByUs := a.panelLauncher.exitReport()
	a.panelLauncher.childExited(60, alive, killedByUs) // quit 退出码
	if quits != 1 {
		t.Fatalf("code=60 未对到退出闸:quits=%d", quits)
	}
}

// TestPanelM2QuitDoubleTriggerGuard 结构:两平台 quit() 的承诺点(close
// done)必须经 quitOnce 护栏——M2 的 panelQuitInvoker 引入首个并发触发点
// (forwardPanelExit goroutine),两次 code=60 退出交叠(如 G1 卡在阻塞式
// MessageBoxW 确认窗期间用户再触发)在旧 select 判废形态下 double close
// panic(M2 评审 M-2)。
func TestPanelM2QuitDoubleTriggerGuard(t *testing.T) {
	for _, tc := range []struct{ name, file string }{
		{"macOS", "companion.go"},
		{"Windows", "companion_windows.go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := goFuncBody(t, trayFileSource(t, tc.file), "quit")
			doIdx := strings.Index(body, "a.quitOnce.Do(")
			if doIdx < 0 {
				t.Fatalf("%s quit() 缺 quitOnce 双触发护栏(退出承诺点未互斥):\n%s", tc.name, body)
			}
			closeIdx := strings.Index(body, "close(a.done)")
			if closeIdx < 0 {
				t.Fatalf("%s quit() 未 close done:\n%s", tc.name, body)
			}
			if closeIdx < doIdx {
				t.Fatalf("%s quit() 的 close(done) 必须在 quitOnce 护栏之内:\n%s", tc.name, body)
			}
		})
	}
}

// TestPanelM2ForwardExitChain 退出转投链:spawn 闭包起的 forwardPanelExit
// goroutine 把 child.Wait() 的退出码经 exitReport(先于 childExited,错序
// alive 恒 0 静默失效)转投 childExited——测试只送码进 Wait 通道,以重拉
// 调度被触发为「真实闭包链路确曾走到状态机」的到达信号(select+超时等待,
// 防双读者竞态挂死)。
func TestPanelM2ForwardExitChain(t *testing.T) {
	restorePanelM2Seams(t)

	panelAnchorSampler = func() panelAnchorSample { return m2Sample(900, 1400, 22, 22, 2560, 1600, true) }
	panelProcessSpawner = func(SpawnOpts) (ChildProcess, error) {
		return newFakeChild(), nil
	}
	panelQuitInvoker = func(*CompanionApp) {}
	panelOpenItemVisor = func(*systray.MenuItem, bool) {}

	var registered func()
	panelClickRegister = func(fn func()) { registered = fn }
	panelModeSetter = func(bool) {}

	a := &CompanionApp{httpURL: "http://127.0.0.1:56789"}
	a.installPanelLauncher()
	// 重拉调度截获走通知通道:到达方是转投 goroutine,主 goroutine 不得
	// 无同步直读计数切片(-race 面),通道通知 + 超时等待替代。
	retryArrived := make(chan struct{}, 1)
	a.panelLauncher.scheduleRetry = func(_ time.Duration, _ func()) {
		select {
		case retryArrived <- struct{}{}:
		default: // 已通知过:幂等
		}
	}
	registered() // 开面板

	child := a.panelLauncher.child
	// fakeChild 的 Wait 通道带缓冲:测试直投退出码(真实形态由
	// osExecChild 的 goroutine 在进程退出后送达)。⚠️ 不再手工调
	// forwardPanelExit——spawn 闭包已起 go a.forwardPanelExit(child),
	// 再手工调一次即双读者竞抢缓冲 1 的通道,竞输方永久阻塞(go test
	// 10 分钟超时挂死面);这里只送码,断言交给真实闭包链路。
	child.(*fakeChild).wait <- 1
	select {
	case <-retryArrived:
	case <-time.After(2 * time.Second):
		t.Fatalf("崩溃退出码未经理线转投到状态机(2s 内重拉调度未触发)")
	}
}

// TestPanelM2OnReadyMenuItemBeforeAIHeader 结构:两平台 onReady 的「打开面板」
// 项都必须排在 AI header 之前创建(库只有 append 语义),创建即隐藏(面板
// 模式用不到,兜底态才 Show);菜单事件循环接「打开面板」点击。
func TestPanelM2OnReadyMenuItemBeforeAIHeader(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
	}{
		{"macOS", "companion.go"},
		{"Windows", "companion_windows.go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := goFuncBody(t, trayFileSource(t, tc.file), "onReady")
			openIdx := strings.Index(body, "a.mPanelOpen = systray.AddMenuItem")
			aiIdx := strings.Index(body, "a.mAIHeader = systray.AddMenuItem")
			if openIdx < 0 || aiIdx < 0 {
				t.Fatalf("onReady 缺「打开面板」项或 AI header:\n%s", body)
			}
			if openIdx > aiIdx {
				t.Fatalf("「打开面板」项必须排在 AI header 之前创建(库只有 append 语义):open=%d ai=%d", openIdx, aiIdx)
			}
			if !strings.Contains(body, "panelOpenItemVisor(a.mPanelOpen, false)") {
				t.Fatalf("「打开面板」项创建后未隐藏:\n%s", body)
			}

			menuBody := goFuncBody(t, trayFileSource(t, tc.file), "handleMenuEvents")
			if !strings.Contains(menuBody, "a.mPanelOpen.ClickedCh") || !strings.Contains(menuBody, "a.handlePanelOpenClick()") {
				t.Fatalf("handleMenuEvents 未接「打开面板」点击:\n%s", menuBody)
			}
		})
	}
}

// TestPanelM2InstallLauncherEndsOnReady 结构:两平台 onReady 收尾动作从 M1
// 的 installPanelBridge 换成 installPanelLauncher(时序语义同 M1:菜单构建
// 与既有 goroutine 启动之后;挂接是最后一句)。
func TestPanelM2InstallLauncherEndsOnReady(t *testing.T) {
	for _, tc := range []struct {
		name       string
		file       string
		goroutineA string
	}{
		{"macOS", "companion.go", "go func() {"},
		{"Windows", "companion_windows.go", "goWithCompanionTrace(\"connectAndStartHTTP\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := goFuncBody(t, trayFileSource(t, tc.file), "onReady")
			idx := strings.Index(body, "a.installPanelLauncher()")
			if idx < 0 {
				t.Fatalf("onReady 缺少 a.installPanelLauncher():\n%s", body)
			}
			quitIdx := strings.Index(body, "a.mQuit = systray.AddMenuItem")
			goIdx := strings.Index(body, tc.goroutineA)
			if idx < quitIdx || idx < goIdx {
				t.Fatalf("installPanelLauncher 必须在菜单构建(%d)与 goroutine 启动(%d)之后,实际 %d", quitIdx, goIdx, idx)
			}
			for _, line := range strings.Split(body[idx+len("a.installPanelLauncher()"):], "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "//") {
					continue
				}
				t.Fatalf("installPanelLauncher 之后 onReady 还有代码(挂接必须收尾): %q", line)
			}
		})
	}
}

// TestPanelM2InstallLauncherRegisterBeforeMode 结构:installPanelLauncher
// 先注册点击回调再开面板模式(M1 时序契约延续:macOS 摘菜单前回调必须就位)。
func TestPanelM2InstallLauncherRegisterBeforeMode(t *testing.T) {
	body := goFuncBody(t, trayFileSource(t, "panel_bridge_m2.go"), "installPanelLauncher")
	regIdx := strings.Index(body, "panelClickRegister(")
	modeIdx := strings.Index(body, "panelModeSetter(true)")
	if regIdx < 0 || modeIdx < 0 {
		t.Fatalf("installPanelLauncher 缺回调注册或面板模式开启:\n%s", body)
	}
	if regIdx > modeIdx {
		t.Fatalf("installPanelLauncher 必须先注册点击回调再开面板模式:\n%s", body)
	}
}

// TestPanelM2FallbackHandlerSemantics 结构:onFallback 回调体必须恢复原生
// 菜单形态(nil 回调 + 关面板模式)+ Show「打开面板」+ 桌面通知;恢复体对称。
func TestPanelM2FallbackHandlerSemantics(t *testing.T) {
	enterBody := goFuncBody(t, trayFileSource(t, "panel_bridge_m2.go"), "enterPanelFallback")
	for _, anchor := range []string{"panelClickRegister(nil)", "panelModeSetter(false)", "panelOpenItemVisor(a.mPanelOpen, true)", "panelNotifier("} {
		if !strings.Contains(enterBody, anchor) {
			t.Fatalf("enterPanelFallback 缺 %q:\n%s", anchor, enterBody)
		}
	}
	restoreBody := goFuncBody(t, trayFileSource(t, "panel_bridge_m2.go"), "restorePanelMode")
	for _, anchor := range []string{"panelClickRegister(", "panelModeSetter(true)", "panelOpenItemVisor(a.mPanelOpen, false)"} {
		if !strings.Contains(restoreBody, anchor) {
			t.Fatalf("restorePanelMode 缺 %q:\n%s", anchor, restoreBody)
		}
	}
}

// TestPanelM2PanelSizesMatchShell 面板逻辑尺寸字面量与 internal/panelwin 的
// DefaultPanelW/H 对齐(不 import,理由见 panel_launcher.go 文件头闭包纪律;
// 结构断言防漂移——漂移即 CSS 视口宽偏离 340 验收线)。
func TestPanelM2PanelSizesMatchShell(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: 无法定位测试文件")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", "internal", "panelwin", "panelwin.go"))
	if err != nil {
		t.Fatalf("读取 panelwin.go: %v", err)
	}
	src := string(raw)
	if !strings.Contains(src, "DefaultPanelW = 340") || !strings.Contains(src, "DefaultPanelH = 560") {
		t.Fatal("internal/panelwin 默认面板尺寸漂移:接线层 panelW/panelH 字面量需同步")
	}
}

// TestPanelM2MacDarwinSpawnNoHarvestComment 结构锚定:macOS spawn shim 必须
// 注明无收割机制、依赖失焦自退(计划评审次#13),防止后人照 Windows 侧
// 误以为有 Job 等价物。
func TestPanelM2MacDarwinSpawnNoHarvestComment(t *testing.T) {
	src := trayFileSource(t, "panel_spawn_darwin.go")
	for _, anchor := range []string{"无收割机制", "失焦自退"} {
		if !strings.Contains(src, anchor) {
			t.Fatalf("panel_spawn_darwin.go 缺关键注释 %q:\n%s", anchor, src)
		}
	}
}
