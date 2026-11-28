package tray

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// M1 托盘面板接线测试(2026-11-26 托盘面板 M1 Task 12):托盘图标点击 →
// 组装 panel.html 地址(运行时真实端口,不写死默认口)→ 打开系统默认浏览器,
// 失败降级桌面通知。行为测试通过接线层四个注入口(panelClickRegister /
// panelModeSetter / panelURLOpener / panelNotifier)拦截真实系统调用——
// 测试不真开浏览器、不真弹通知、不真碰 AppKit。

// restorePanelBridgeSeams 记录接线层注入口的当前实现并在测试结束时恢复:
// 这些是包级 var,不恢复会跨测试互染。
func restorePanelBridgeSeams(t *testing.T) {
	t.Helper()

	origOpener, origNotifier := panelURLOpener, panelNotifier
	origRegister, origMode := panelClickRegister, panelModeSetter
	t.Cleanup(func() {
		panelURLOpener, panelNotifier = origOpener, origNotifier
		panelClickRegister, panelModeSetter = origRegister, origMode
	})
}

func TestPanelBridgeURLAssembly(t *testing.T) {
	restorePanelBridgeSeams(t)

	t.Run("拼接panel页", func(t *testing.T) {
		if got := panelURL("http://127.0.0.1:56789"); got != "http://127.0.0.1:56789/panel.html" {
			t.Fatalf("panelURL() = %q, want http://127.0.0.1:56789/panel.html", got)
		}
		// 尾斜杠容忍:地址刷新逻辑不会产生尾斜杠,但拼接处不重排防线。
		if got := panelURL("http://127.0.0.1:56789/"); got != "http://127.0.0.1:56789/panel.html" {
			t.Fatalf("panelURL(尾斜杠) = %q, want 不重复斜杠", got)
		}
	})

	t.Run("基址缺省回退管理地址", func(t *testing.T) {
		a := &CompanionApp{}
		if got := a.panelBaseURL(); !strings.HasPrefix(got, "http://127.0.0.1:") {
			t.Fatalf("panelBaseURL() 空基址未回退默认管理地址: %q", got)
		}
		a.setHTTPURL("http://127.0.0.1:56789")
		if got := a.panelBaseURL(); got != "http://127.0.0.1:56789" {
			t.Fatalf("panelBaseURL() = %q, want 运行时真实地址 http://127.0.0.1:56789", got)
		}
	})
}

// TestPanelBridgeClickOpensRuntimePortURL 挂接后触发点击回调,浏览器拿到的
// 必须是运行时真实端口的 panel.html:端口可能因占用回退随机口(刷新逻辑见
// applyDashboardStartResponse),写死 56431 会开到错误端口。
func TestPanelBridgeClickOpensRuntimePortURL(t *testing.T) {
	restorePanelBridgeSeams(t)

	var openedURL string
	panelURLOpener = func(url string) error {
		openedURL = url
		return nil
	}
	var registered func()
	panelClickRegister = func(fn func()) { registered = fn }

	modeCalls := make([]bool, 0, 1)
	panelModeSetter = func(on bool) { modeCalls = append(modeCalls, on) }

	a := &CompanionApp{}
	a.setHTTPURL("http://127.0.0.1:56789")
	a.installPanelBridge()

	if registered == nil {
		t.Fatal("installPanelBridge 未注册托盘点击回调")
	}
	if len(modeCalls) != 1 || !modeCalls[0] {
		t.Fatalf("installPanelBridge 面板模式调用 = %v, want [true]", modeCalls)
	}

	registered()
	if openedURL != "http://127.0.0.1:56789/panel.html" {
		t.Fatalf("点击回调打开 %q, want http://127.0.0.1:56789/panel.html(运行时真实端口)", openedURL)
	}
}

// TestPanelBridgeUninstallRestoresMenuPath 恢复语义的接线函数存在且可测:
// nil 回调(原生菜单兜底)+ macOS 摘面板模式接回菜单。M1 不调用,兜底能力自证。
func TestPanelBridgeUninstallRestoresMenuPath(t *testing.T) {
	restorePanelBridgeSeams(t)

	var registeredFn func()
	var gotRegisterNil bool
	panelClickRegister = func(fn func()) {
		registeredFn = fn
		gotRegisterNil = fn == nil
	}
	modeCalls := make([]bool, 0, 1)
	panelModeSetter = func(on bool) { modeCalls = append(modeCalls, on) }

	uninstallPanelBridge()

	if !gotRegisterNil {
		t.Fatal("uninstallPanelBridge 未把点击回调置 nil(应恢复原生菜单兜底)")
	}
	if registeredFn != nil {
		t.Fatal("uninstallPanelBridge 后回调仍非 nil")
	}
	if len(modeCalls) != 1 || modeCalls[0] {
		t.Fatalf("uninstallPanelBridge 面板模式调用 = %v, want [false]", modeCalls)
	}
}

func TestPanelBridgeOpenFailureFallsBackToDesktopNotify(t *testing.T) {
	restorePanelBridgeSeams(t)

	panelURLOpener = func(string) error { return errors.New("exec: not found") }
	var notified string
	panelNotifier = func(title, message string) { notified = title + "|" + message }
	panelModeSetter = func(bool) {}

	a := &CompanionApp{}
	a.setHTTPURL("http://127.0.0.1:56789")
	hook := a.panelClickCallback()
	hook() // 不得 panic

	if !strings.Contains(notified, "http://127.0.0.1:56789/panel.html") {
		t.Fatalf("打开失败未降级桌面通知(或通知缺 URL): %q", notified)
	}
}

// TestPanelBridgeCallbackRecoversPanic SetPanelClickHook 契约第三条:回调
// panic 发生在无 recover 的 goroutine 上会终止整个进程——回调体必须自己
// recover 打日志,防止打开浏览器的系统调用 panic 带走 companion。
func TestPanelBridgeCallbackRecoversPanic(t *testing.T) {
	restorePanelBridgeSeams(t)

	panelURLOpener = func(string) error { panic("boom") }
	panelNotifier = func(string, string) {}
	panelModeSetter = func(bool) {}

	a := &CompanionApp{}
	a.setHTTPURL("http://127.0.0.1:56789")
	hook := a.panelClickCallback()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("回调 panic 未被回调体 recover 兜住(会带走 companion): %v", r)
		}
	}()
	hook()
}

// TestPanelBridgeOnReadyWiringBothPlatforms 钉接线时序与平台分工:两平台
// onReady 末尾(菜单构建完成、既有 goroutine 启动之后)都挂面板接线。M2
// 起 onReady 挂 installPanelLauncher(launcher.Click 取代 M1 的浏览器回调,
// 见 panel_bridge_m2_test.go);M1 的 installPanelBridge 函数与其测试保留
// (M1 行为不变,浏览器打开能力仍可被其它路径复用)。bridge 内部先注册回调
// 再开面板模式;macOS 面板模式=摘菜单挂回调(SetPanelMode(true)),时序靠
// 「位于菜单构建之后」保证,提前摘菜单会把空菜单钉上状态项。
func TestPanelBridgeOnReadyWiringBothPlatforms(t *testing.T) {
	bridgeSrc := trayFileSource(t, "panel_bridge.go")
	darwinShimSrc := trayFileSource(t, "panel_mode_darwin.go")
	winShimSrc := trayFileSource(t, "panel_mode_windows.go")

	t.Run("onReady末尾挂接两平台", func(t *testing.T) {
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
					t.Fatalf("onReady 缺少 a.installPanelLauncher() 挂接(M2 接线取代 M1):\n%s", body)
				}
				// 时序一:菜单构建完成之后(否则 macOS 摘菜单接回的是空菜单)。
				quitIdx := strings.Index(body, "a.mQuit = systray.AddMenuItem")
				if quitIdx < 0 {
					t.Fatalf("onReady 找不到菜单构建锚点 a.mQuit:\n%s", body)
				}
				// 时序二:既有 goroutine 启动之后(挂接是 onReady 的收尾动作)。
				goIdx := strings.Index(body, tc.goroutineA)
				if goIdx < 0 {
					t.Fatalf("onReady 找不到 goroutine 启动锚点 %q:\n%s", tc.goroutineA, body)
				}
				if idx < quitIdx || idx < goIdx {
					t.Fatalf("installPanelLauncher 必须在菜单构建(%d)与 goroutine 启动(%d)之后,实际 %d", quitIdx, goIdx, idx)
				}
				// 挂接是 onReady 最后一句:其后仅允许空白与整行 // 注释
				// (按行剥离尾注释,宽容未来在挂接后补的说明性注释)。
				for _, line := range strings.Split(body[idx+len("a.installPanelLauncher()"):], "\n") {
					line = strings.TrimSpace(line)
					if line == "" || strings.HasPrefix(line, "//") {
						continue
					}
					t.Fatalf("installPanelLauncher 之后 onReady 还有代码(挂接必须收尾): %q", line)
				}
			})
		}
	})

	t.Run("bridge先挂回调再开面板模式", func(t *testing.T) {
		installBody := goFuncBody(t, bridgeSrc, "installPanelBridge")
		regIdx := strings.Index(installBody, "panelClickRegister(")
		modeIdx := strings.Index(installBody, "panelModeSetter(true)")
		if regIdx < 0 || modeIdx < 0 {
			t.Fatalf("installPanelBridge 缺回调注册或面板模式开启:\n%s", installBody)
		}
		if regIdx > modeIdx {
			t.Fatalf("installPanelBridge 必须先注册点击回调再开面板模式:\n%s", installBody)
		}

		uninstallBody := goFuncBody(t, bridgeSrc, "uninstallPanelBridge")
		if !strings.Contains(uninstallBody, "panelClickRegister(nil)") || !strings.Contains(uninstallBody, "panelModeSetter(false)") {
			t.Fatalf("uninstallPanelBridge 恢复语义不完整(nil 回调+关面板模式):\n%s", uninstallBody)
		}
	})

	t.Run("回调契约recover", func(t *testing.T) {
		body := goFuncBody(t, bridgeSrc, "panelClickCallback")
		if !strings.Contains(body, "recover()") {
			t.Fatalf("panelClickCallback 缺 recover(SetPanelClickHook 契约第三条:回调 panic 终止进程):\n%s", body)
		}
	})

	t.Run("shim平台分工", func(t *testing.T) {
		if !strings.Contains(darwinShimSrc, "//go:build darwin") || !strings.Contains(darwinShimSrc, "systray.SetPanelMode(") {
			t.Fatalf("panel_mode_darwin.go 必须以 //go:build darwin 转发 systray.SetPanelMode:\n%s", darwinShimSrc)
		}
		if !strings.Contains(winShimSrc, "//go:build windows") || strings.Contains(winShimSrc, "systray.SetPanelMode(") {
			t.Fatalf("panel_mode_windows.go 必须以 //go:build windows 提供空实现(Windows 无摘菜单动作):\n%s", winShimSrc)
		}
	})

	t.Run("URL不写死默认端口", func(t *testing.T) {
		if strings.Contains(bridgeSrc, "56431") {
			t.Fatal("panel_bridge.go 出现字面端口 56431:URL 必须用运行时真实端口(端口占用会回退随机口)")
		}
	})
}

// TestPanelBridgeSeamDefaultsNonNil 四个注入口的默认实现冒烟:它们是包级
// var,未来重构写丢初始化器即静默变 nil——行为测试全走替身拦不住这种断链,
// 要到生产首次点击才踩到(register/modeSetter 的 nil 调用发生在 onReady,
// 直接带走 companion;opener/notifier 被 recover 吞掉表现为点击无响应)。
func TestPanelBridgeSeamDefaultsNonNil(t *testing.T) {
	if panelClickRegister == nil {
		t.Fatal("panelClickRegister 默认实现为 nil(应指向 systray.SetPanelClickHook)")
	}
	if panelModeSetter == nil {
		t.Fatal("panelModeSetter 默认实现为 nil(应指向平台 shim setTrayPanelMode)")
	}
	if panelURLOpener == nil {
		t.Fatal("panelURLOpener 默认实现为 nil(应指向 openPanelURLInBrowser)")
	}
	if panelNotifier == nil {
		t.Fatal("panelNotifier 默认实现为 nil(应指向 desktop.Notify)")
	}
}

// trayFileSource 读取 app/tray 包内指定源文件(结构测试同款,路径相对本包)。
func trayFileSource(t *testing.T, name string) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: 无法定位测试文件")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), name))
	if err != nil {
		t.Fatalf("读取 %s: %v", name, err)
	}
	return string(src)
}
