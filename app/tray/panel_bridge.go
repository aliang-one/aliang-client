package tray

// M1 托盘面板接线(2026-11-26 托盘面板 M1 Task 12):托盘图标左右键点击 →
// 组装 panel.html 地址 → 打开系统默认浏览器,失败降级桌面通知。
//
// 底座胶水(third_party/systray 本仓补丁)在 Task 3/4 就位:
//   - SetPanelClickHook(fn):非 nil 时左右键都走回调(每次点击起新 goroutine),
//     nil 恢复原生菜单兜底;Windows 走 wndProc 分支,macOS 走 trayClicked:。
//   - SetPanelMode(bool):仅 macOS——true 摘掉原生菜单挂上点击回调,菜单对象
//     仍被刷新循环照常更新(不可见),false 随时可接回;Windows 无此动作
//     (panelClickFn 非 nil 即接管,见 panel_mode_windows.go)。
//
// M1 形态 = 单形态面板:两平台 onReady 末尾都挂回调,macOS 额外开面板模式。
// 范围外(M1 不做):launcher 状态机、面板进程、崩溃兜底(浏览器不崩)、
// Windows Job 对象、TrayRect 锚定(浏览器自己定位)。
//
// 纯 Go、无构建标签,两平台(darwin/windows)编译;平台差异收敛在
// panel_mode_darwin.go / panel_mode_windows.go 两个 shim 里。包内测试通过
// 下面的注入口拦截真实系统调用,不真开浏览器/不真弹通知/不碰 AppKit。
import (
	"fmt"
	"runtime"
	"strings"

	"aliang.one/nursorgate/common/desktop"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
	"github.com/getlantern/systray"
)

// panelClickRegister 托盘点击回调注册口(测试替身),默认 systray.SetPanelClickHook。
var panelClickRegister = systray.SetPanelClickHook

// panelModeSetter 面板模式开关注入口(测试替身),默认平台 shim setTrayPanelMode
// (darwin 转发 systray.SetPanelMode,Windows 空实现)。真实 shim 走 cgo/AppKit,
// 单测必须经此替身拦截。
var panelModeSetter = setTrayPanelMode

// panelURLOpener 「打开 URL」系统调用注入口(测试替身),默认 openPanelURLInBrowser。
// 抽象与既有 openDashboard 同款(rundll32 url.dll,FileProtocolHandler / open /
// xdg-open),只是多包一层错误返回供降级判断。
var panelURLOpener = openPanelURLInBrowser

// panelNotifier 桌面通知注入口(测试替身),默认 desktop.Notify(beeep 封装,
// best-effort;同 companion_watchdog.go 的 registrationStuckNotifier 先例)。
var panelNotifier = desktop.Notify

// panelDefaultBaseURL 面板基址缺省值:core 管理面板默认地址。本文件无构建
// 标签(两平台都能引用),两平台 companion 的 httpURL 初值与面板回退统一
// 引用此常量,三处字面拷贝归一。
const panelDefaultBaseURL = "http://" + config.DefaultManagementAddr

// panelBaseURL 返回面板基址:companion 的 httpURL 由 start_http 响应刷新
// (applyDashboardStartResponse;端口被占时 core 回退随机口),必须用运行时
// 真实值而非写死默认口。空值仅是防御(两平台 NewCompanionApp 都有初值)。
//
// httpURL 并发面(2026-11-28 评审修):写侧 applyDashboardStartResponse 在
// connectAndStartHTTP/补发 goroutine 运行期写;读侧除本函数(M2 launcher 每
// 次 spawn 经 panelSpawnOpts 调)、openDashboard(companion.go:512 /
// companion_windows.go:478)、dashboard_revive 日志外,还有 openDashboard
// 自身。裸 string 字段是数据竞争——改 atomic.Value 存 string,读写一律经
// 下面两个 accessor,禁止直接访问字段。
func (a *CompanionApp) panelBaseURL() string {
	if u := a.getHTTPURL(); u != "" {
		return u
	}
	return panelDefaultBaseURL
}

// getHTTPURL 读仪表盘基址(atomic;零值/未设返回空串)。
func (a *CompanionApp) getHTTPURL() string {
	u, _ := a.httpURL.Load().(string)
	return u
}

// setHTTPURL 写仪表盘基址(atomic;start_http 响应刷新与 NewCompanionApp
// 初值都走这里)。
func (a *CompanionApp) setHTTPURL(u string) {
	a.httpURL.Store(u)
}

// panelURL 拼接面板页地址(/panel.html 由 core 仪表盘伺服,回环免 cookie)。
func panelURL(base string) string {
	return strings.TrimRight(base, "/") + "/panel.html"
}

// panelClickCallback 构造托盘点击回调:组装 URL → 开浏览器,失败降级桌面通知。
//
// SetPanelClickHook 的三条契约在此逐一落实:
//   - 快速返回:回调体只做一次「打开浏览器」系统调用,不轮询不等待;
//   - 可能并发(双击=两个并发回调):回调体无共享可变状态,最坏情形是连点
//     开出两个面板标签页,无害,不做串行化;
//   - panic 终止进程(回调跑在无 recover 的 goroutine 上):回调体自带
//     recover 打日志兜底。
func (a *CompanionApp) panelClickCallback() func() {
	return func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error(fmt.Sprintf("tray panel click callback panicked (recovered): %v", r))
			}
		}()

		url := panelURL(a.panelBaseURL())
		if err := panelURLOpener(url); err != nil {
			logger.Error(fmt.Sprintf("Failed to open panel in browser from tray: %v", err))
			// 桌面通知兜底:URL 一并给出,用户可手动访问。
			panelNotifier("Aliang", fmt.Sprintf("打开面板失败,可手动访问:%s", url))
		}
	}
}

// installPanelBridge 挂接面板模式(onReady 末尾调用,两平台):
// 先注册回调再开面板模式;macOS 侧「SetPanelMode(true) 必须在菜单构建完成
// 之后」的时序由调用点位于 onReady 末尾保证——提前摘菜单会把空菜单钉上
// 状态项(菜单对象此时还没填充)。
func (a *CompanionApp) installPanelBridge() {
	panelClickRegister(a.panelClickCallback())
	panelModeSetter(true)
}

// uninstallPanelBridge 恢复原生菜单兜底(nil 回调 + 关面板模式接回菜单)。
// M1 不调用(无兜底需求),但恢复语义的接线函数存在且可测——面板模式随时
// 可逆,菜单对象在面板期间一直被刷新循环照常更新。
func uninstallPanelBridge() {
	panelClickRegister(nil)
	panelModeSetter(false)
}

// openPanelURLInBrowser 用系统默认浏览器打开 URL:系统调用同 openDashboard
// 的平台分工(Windows=rundll32 url.dll,FileProtocolHandler 隐藏窗口;
// macOS=open;linux=xdg-open,本包虽不在 linux 构建,分支照 companion.go
// 原样保留)。错误返回给调用方降级判断;cmd.Start 失败即报,不等浏览器。
func openPanelURLInBrowser(url string) error {
	var cmdName string
	var args []string
	switch runtime.GOOS {
	case "windows":
		cmdName = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	case "darwin":
		cmdName = "open"
		args = []string{url}
	default:
		cmdName = "xdg-open"
		args = []string{url}
	}
	return newBackgroundCommand(cmdName, args...).Start()
}
