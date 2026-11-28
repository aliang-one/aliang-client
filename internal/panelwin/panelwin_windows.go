//go:build windows

// panelwin_windows.go — 托盘面板纯原生窗口壳,Windows 侧(实现计划 M2
// Task 13)。纯 Go(无 cgo):自建 WS_POPUP 窗口 + pkg/edge.Chromium
// (Embed 到自建 HWND + MessageCallback + 自建消息循环)。
//
// 为什么不用顶层 webview.WebView 包装:其 Bind/JSON-RPC 会拦截 postMessage、
// 无 WebMessageReceived 钩子,quit/open 消息链会静默断裂(实现计划二审
// 重要 4)。为什么不用 app/tray 的 DPI 声明:同名函数未导出,且 internal
// 不得反向依赖 app 包(二审阻塞 2)——下方自含一份十几行的 DPI 声明。
//
// 生命周期(2026-11-28 撤销方案 B「首次后常驻」,回归「点击才拉起+关闭
// 即退」——常驻的隐藏 WebView2 窗口不可靠的教训同 macOS 三连败):
// WM_ACTIVATE 失活(点面板外/宿主切换)→ PostQuitMessage(0) 失焦即退;
// companion 的 close 请求消息(WM_APP+1,panel_spawn_windows.go
// FindWindowW+PostMessageW 投递)→ PostQuitMessage(0) 请求关闭;窗口随
// 进程退出彻底消失,不存在任何驻留形态。页面 quit 消息 → 退出码 60;open
// 消息 → ShellExecute 开默认浏览器后 PostQuitMessage(0) 正常关闭(2026-11-28:
// 此前驻留,指望浏览器抢前台走 WM_ACTIVATE 失活链,但冷启动前台切换延迟
// 期间面板悬在浏览器前——用户走查坐实;ShellExecute 失败 ≤32 才驻留);
// resize 消息 → applyHeight 跟随
// 改窗高(高度自适应),且【首次】resize 落窗后才上屏(首帧尺寸修正
// Part 1,见 runPanelWindow 注释)。退出码契约见 panelwin.go(Task 14 launcher:
// 0=正常关闭(点外/失焦/close 请求)、60=quit,其余=崩溃)。
//
// 前台交接:显示前必须 SetForegroundWindow——companion spawn 时的
// AllowSetForegroundWindow(pid)(Task 15)只授予资格,本进程不调
// SetForegroundWindow 则 WM_ACTIVATE 激活事件不发,失活(=失焦即退)永不
// 触发,整条失焦即退链失效(二审阻塞 3)。
package panelwin

import (
	"fmt"
	"time"
	"unsafe"

	"aliang.one/nursorgate/common/logger"
	"github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

type nativeHost struct{}

// NewHost 返回 Windows 壳实现。
func NewHost() Host { return nativeHost{} }

// —— Win32 常量(仅本文件使用,自含声明避免 internal 反向依赖 app 包) —— //

const (
	wsPopup        = 0x80000000
	wsExToolWindow = 0x00000080
	wsExTopmost    = 0x00000008

	wmActivate = 0x0006
	waInactive = 0
	waActive   = 1
	// waClickActive = 2:鼠标点击激活(WM_ACTIVATE wParam 低字)。
	waClickActive = 2
	wmDestroy     = 0x0002
	// 注:WM_QUIT(0x0012)不再声明常量——退出码协议已收进自建消息循环的
	// WM_QUIT 分支(GetMessageW 返回 0 时直接读 wParam),见下方消息循环注释。

	// wmApp/wmAppPanelToggle:companion → 面板壳的 close 请求消息(语义=
	// 请求关闭,panel_spawn_windows.go FindWindowW+PostMessageW 投递;历史名
	// toggle,2026-11-28 撤销方案 B 后不再做可见⇄隐藏切换)。WM_APP+1 =
	// 0x8001;与投递侧常量跨文件对齐(app/tray 不能 import 本包,漂移由
	// app/tray TestPanelM2CloseChannelContract 源码锚定把守)。
	// wmAppPanelShow(WM_APP+2):壳内部自用——首帧尺寸修正 Part 1 的 1.2s
	// 兜底上屏消息(AfterFunc goroutine 经它把「按当前尺寸显示」投递回消息
	// 循环线程,wndProc 分派 panelShowWindow 执行;见 runPanelWindow 注释)。
	// 不出本进程,无跨文件对齐面。
	wmApp            = 0x8000
	wmAppPanelToggle = wmApp + 1
	wmAppPanelShow   = wmApp + 2

	swShow        = 5
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010

	// DwmSetWindowAttribute 属性:圆角(Win11 22000+)与亚克力材质
	// (DWM 系统背景,Win11 22621+);低版本调用失败即静默降级为纯半透明。
	dwmwaWindowCornerPreference = 33
	dwmwcpRound                 = 2
	dwmwaSystemBackdropType     = 38
	dwmsbtTransientWindow       = 3

	shellExecuteWSHownormal = 1
)

// —— Win32 API(经 NewLazySystemDLL;跨编译纯检查无 cgo) —— //
//
// ⚠️ 每个 proc 必须挂在【导出它的】DLL 上(2026-11-28 Windows 实机走查:
// GetModuleHandleW 曾误挂 user32.dll,启动即 panic「Failed to find
// GetModuleHandleW procedure in user32.dll」——NewLazySystemDLL 只在 Call
// 时查导出表,交叉编译与 go vet 都抓不到,只有真机能炸)。DLL 归属表由
// TestPanelwinWindowsProcDLLOwnership 逐条钉死(对照 Win32 文档),改动
// proc 时同步改测试。

var (
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	user32   = windows.NewLazySystemDLL("user32.dll")
	shcore   = windows.NewLazySystemDLL("shcore.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")

	procGetModuleHandleW          = kernel32.NewProc("GetModuleHandleW")
	procRegisterClassExW          = user32.NewProc("RegisterClassExW")
	procCreateWindowExW           = user32.NewProc("CreateWindowExW")
	procDefWindowProcW            = user32.NewProc("DefWindowProcW")
	procGetMessageW               = user32.NewProc("GetMessageW")
	procTranslateMessage          = user32.NewProc("TranslateMessage")
	procDispatchMessageW          = user32.NewProc("DispatchMessageW")
	procPostQuitMessage           = user32.NewProc("PostQuitMessage")
	procPostMessageW              = user32.NewProc("PostMessageW")
	procShowWindow                = user32.NewProc("ShowWindow")
	procSetWindowPos              = user32.NewProc("SetWindowPos")
	procSetForegroundWindow       = user32.NewProc("SetForegroundWindow")
	procGetDpiForWindow           = user32.NewProc("GetDpiForWindow")
	procSetProcessDpiAwarenessCtx = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware        = user32.NewProc("SetProcessDPIAware")
	procSetProcessDpiAwareness    = shcore.NewProc("SetProcessDpiAwareness")
	procGetSystemMetrics          = user32.NewProc("GetSystemMetrics")
	procDwmSetWindowAttribute     = dwmapi.NewProc("DwmSetWindowAttribute")
	procShellExecuteW             = shell32.NewProc("ShellExecuteW")
)

// smCyFullscreen = SM_CYFULLSCREEN:主屏工作区高度(屏高减任务栏,物理 px),
// 物理重锚的 side 判别入参(reanchorPhysicalPos;见 panelwin_reanchor.go)。
const smCyFullscreen = 17

// dpiAwarenessContextPerMonitorV2 = DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2
// 即 (DPI_AWARENESS_CONTEXT)-4;processPerMonitorDpiAware = PROCESS_PER_MONITOR_DPI_AWARE。
const (
	dpiAwarenessContextPerMonitorV2 = ^uintptr(3)
	processPerMonitorDpiAware       = 2
)

type wndClassExW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     windows.Handle
	HIcon         windows.Handle
	HCursor       windows.Handle
	HbrBackground windows.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       windows.Handle
}

type msgW struct {
	Hwnd    windows.HWND
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      pointW
}

type pointW struct {
	X, Y int32
}

// Run 阻塞展示面板,经消息循环正常返回退出码(0=正常关闭(点面板外/失焦/
// close 请求/销毁收尾,「关闭即退」),60=quit)。
func (nativeHost) Run(opts Options) int {
	// ① 自含 DPI 声明,必须在任何窗口创建之前:非 DPI-aware 进程的窗口
	// 在非 100% 缩放下会被 DPI 虚拟化,SetWindowPos 的物理坐标语义失真。
	// 按能力从新到旧降级;已声明过(清单/先前调用)时 API 报失败,静默容忍。
	declareProcessDpiAwareness()

	if err := runPanelWindow(opts); err != nil {
		// 初始化失败(WebView2 运行时缺失等):非 0 非 60 退出码=崩溃,
		// launcher 据此记失败重拉,连崩转原生菜单兜底(Task 14/15)。
		logger.Error(fmt.Sprintf("panelwin 初始化失败: %v", err))
		return 1
	}
	return lastExitCode
}

// lastExitCode 由消息循环落定(GetMessageW 返回 0 时读 WM_QUIT 的 wParam)。
var lastExitCode int

// panelShowWindow 首帧尺寸修正 Part 1 的上屏动作注入口(runPanelWindow 装配
// 为 showNow 闭包;wndProc 的 WM_APP+2 兜底分派消费)。进程内单窗口、消息
// 循环单线程驱动,两处消费同线程无并发面;nil 守卫兜住「兜底消息早于装配」
// 的极端窗口期。
var panelShowWindow func(reason string)

// panelEverActivated 曾成为前台窗口(WM_ACTIVATE 的 WA_ACTIVE/WA_CLICKACTIVE
// 置位;2026-11-28 评审修,镜像 macOS 侧 everKeyed 守卫):失活即退链只在
// 置位后才武装。未置位时收到 WA_INACTIVE 一律忽略——WebView2 controller
// 初始化期间的内部焦点转移、SetForegroundWindow 被拒后的 ShowWindow 不激活
// 形态,都可能让顶层 HWND 在「从未激活过」的状态下收到一次 WA_INACTIVE,
// 无守卫会 PostQuitMessage(0)→退出码 0→launcher 记正常关闭→不计崩溃→
// 用户感知「点了没反应,再点一次才出来」且永不触发兜底。首次真实激活后
// 链即武装,点面板外/切窗口照常即退。消息循环单线程读写,无并发面。
var panelEverActivated bool

// panelFirstShowFallback 首帧兜底超时:页面加载失败/resize 未到时按当前
// 尺寸上屏(面板不能永远不出现)。与 macOS 侧 1.2s 兜底同值(双侧同批)。
const panelFirstShowFallback = 1200 * time.Millisecond

func runPanelWindow(opts Options) error {
	className, err := windows.UTF16PtrFromString("AliangPanelWindow")
	if err != nil {
		return fmt.Errorf("窗口类名构造失败: %w", err)
	}
	hInstance, _, _ := procGetModuleHandleW.Call(0)

	// 回调句柄必须常驻(wndProc 由 Win32 异步调用);包级 init 只跑一次。
	wndProcHandle := windows.NewCallback(panelWndProc)

	var wc wndClassExW
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	wc.LpfnWndProc = wndProcHandle
	wc.HInstance = windows.Handle(hInstance)
	wc.HbrBackground = windows.Handle(0)
	wc.LpszClassName = className
	if r, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("RegisterClassExW 失败")
	}

	title, _ := windows.UTF16FromString("aliang panel")
	// ② 自建 WS_POPUP(无边框)+ TOOLWINDOW(不进任务栏/Alt-Tab)+
	// TOPMOST(悬浮)。创建为隐藏窗口,WebView2 就绪后再显示。
	hwnd, _, callErr := procCreateWindowExW.Call(
		uintptr(wsExToolWindow|wsExTopmost), // dwExStyle
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(&title[0])),
		uintptr(wsPopup),
		uintptr(int32(opts.X)), uintptr(int32(opts.Y)), // 锚定坐标物理直通
		uintptr(int32(opts.PanelW)), uintptr(int32(opts.PanelH)), // 临时逻辑尺寸,下方按 DPI 校正
		0, 0, hInstance, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowExW 失败: %v", callErr)
	}

	// ⑥ 尺寸 DPI 缩放:创建后取窗口 DPI,scale 物理尺寸=逻辑×scale,
	// CSS 视口维持逻辑宽(锚定坐标物理直通、尺寸逻辑×scale,二审重要 6)。
	scale := 1.0
	if procGetDpiForWindow.Find() == nil {
		if dpi, _, _ := procGetDpiForWindow.Call(hwnd); dpi > 0 {
			scale = float64(dpi) / 96.0
		}
	}
	physW := int32(float64(opts.PanelW) * scale)
	physH := int32(float64(opts.PanelH) * scale)

	// Task 15 硬验收收口:逻辑锚定→物理重锚,消除 150% DPI 底边越顶。
	// 锚定坐标(cmd/panel.go 按逻辑面板宽高算出)在非 100% 缩放下错位——
	// 高度差 (physH-panelH) 全压在锚定边,150% 下面板底边越过图标顶约
	// 280px。现拿到窗口 scale 后按物理面板尺寸重锚(公式与 side 判别见
	// panelwin_reanchor.go;接线层仍传逻辑 pw/ph,与 cmd/panel.go 语义一致)。
	workH := 0
	if procGetSystemMetrics.Find() == nil {
		if r, _, _ := procGetSystemMetrics.Call(smCyFullscreen); r > 0 {
			workH = int(int32(r))
		}
	}
	reX, reY := reanchorPhysicalPos(
		int(int32(opts.X)), int(int32(opts.Y)),
		opts.PanelW, opts.PanelH,
		int(physW), int(physH),
		scale, workH,
	)

	procSetWindowPos.Call(
		hwnd, 0,
		uintptr(int32(reX)), uintptr(int32(reY)),
		uintptr(physW), uintptr(physH),
		uintptr(swpNoZOrder|swpNoActivate),
	)

	// 面板高度自适应(2026-11-28 用户拍板):前端量内容高后发 resize 消息,
	// 壳 clamp 后跟随改窗口高度,超过屏幕可用高才页内滚动。几何解算抽纯函数
	// resizeWindowRect(panelwin_resize.go,表驱动钉死),applyHeight 只是薄壳:
	// 跟踪矩形 → 解算 → SetWindowPos 落窗 → chromium.Resize 同步 bounds →
	// 回写。当前物理矩形用局部变量跟踪(初始化=重锚结果,resize 后更新)——
	// 消息循环单线程驱动无并发,不回读 GetWindowRect,局部量即权威。
	//
	// chromium 先声明后赋值:applyHeight 定义早于 Embed,但落窗后要调
	// chromium.Resize()(pkg/edge 的 Chromium 只有显式 Resize
	// GetClientRect→PutBounds 才同步 controller bounds,不随父 HWND 自动
	// 跟随,评审必修 1);applyHeight 只在 MessageCallback 里被调,彼时
	// Embed 必已完成,非 nil。
	var chromium *edge.Chromium
	curX, curY, curW, curH := reX, reY, int(physW), int(physH)
	applyHeight := func(logicalH float64) {
		x, y, w, h := resizeWindowRect(curX, curY, curW, curH, logicalH, scale, opts.Side, workH)
		if h <= 0 || (x == curX && y == curY && w == curW && h == curH) {
			return // 退化值/无变化:零操作
		}
		procSetWindowPos.Call(
			hwnd, 0,
			uintptr(int32(x)), uintptr(int32(y)),
			uintptr(w), uintptr(h),
			uintptr(swpNoZOrder|swpNoActivate),
		)
		// 内容跟随窗高:SetWindowPos 不传导到 WebView2 controller bounds,
		// 不显式 Resize 则内容停在旧高(裁剪/透明空区)。
		chromium.Resize()
		curX, curY, curW, curH = x, y, w, h
	}

	// ⑤ 首帧尺寸修正 Part 1(2026-11-28 用户反馈「点击瞬间出现大空白区域
	// 再缩到目标尺寸」):窗口创建后【保持隐藏】,首次 resize 分派落窗后才
	// 经 showNow 上屏——此前立即 ShowWindow 会把缺省 560 高的空窗先闪出来,
	// 页面量高 resize 后才缩到实际高度,正是用户反馈的闪变形态。高度持久化
	// (Part 2)已把上次稳态高度读回作出生尺寸,正常路径首帧即正确尺寸;
	// 跨次内容高度变化的残余闪变由「先落窗再上屏」兜住。
	//
	// showNow 是唯一上屏入口(SetForegroundWindow/ShowWindow/chromium.Focus
	// 三件全收在内——SetForegroundWindow 必须保留:AllowSetForegroundWindow
	// 只授予资格,不调则 WM_ACTIVATE 激活不发、失焦即退整条失效;Focus 是
	// 首帧键盘焦点补齐,pkg/edge 的 Focus() 无返回值,失败形态随 Task 16
	// 实机探针收口)。两处触发:首次 resize 到位(MessageCallback)、1.2s
	// 兜底;shown 位保证幂等(重复触发零操作)。定义先于 MessageCallback
	// (闭包引用序)。
	//
	// ⚠️ 装配与兜底计时【必须在 Embed 之后】(2026-11-28 评审修):chromium.Embed
	// 内部自泵消息(GetMessageW→DispatchMessageW)直到 controller 就绪,泵期间
	// 本线程收到的任何消息都会被分派到 wndProc——若 1.2s 兜底 AfterFunc 在
	// Embed 之前起跑,WebView2 冷启动(慢盘/首次运行)超 1.2s 时 WM_APP+2 会在
	// Embed 的泵里被分派→showNow→ShowWindow 把一个 WebView2 尚未挂上的空
	// HWND 显示出来,shown 位已置,后续真正的首帧 resize 再到也只是 applyHeight
	// 落窗(showNow 幂等 return)——用户看到一段空白面板。兜底计时的语义本就
	// 是「页面加载超时」,从 Navigate 起算才对,不是「进程启动超时」。
	// panelShowWindow 的装配与 AfterFunc 起跑一并挪到 Navigate 之后(见下)。
	shown := false
	showNow := func(reason string) {
		if shown || chromium == nil {
			// chromium 守卫:WM_APP+2 兜底消息理论上只在 Navigate 之后才会
			// 被投递(装配已挪后),这里是对「Embed 失败返回前残留消息」的
			// belt-and-suspenders——nil 时不上屏,等进程按初始化失败退出。
			return
		}
		shown = true
		procSetForegroundWindow.Call(hwnd)
		procShowWindow.Call(hwnd, uintptr(swShow))
		chromium.Focus()
		logger.Info("panelwin: 面板上屏(" + reason + ")")
	}

	// ③ 圆角 + 亚克力(Win11 22621+);低版本 DwmSetWindowAttribute 返回
	// 失败,静默降级为纯半透明底(前端半透明底色仍是完整可用的形态)。
	setDwmAttributeU32(hwnd, dwmwaWindowCornerPreference, dwmwcpRound)
	setDwmAttributeU32(hwnd, dwmwaSystemBackdropType, dwmsbtTransientWindow)

	// ④ WebView2 必须用 pkg/edge 的 Chromium(见文件头),Embed 到自建
	// HWND——Embed 内部自泵消息直到 controller 就绪。
	chromium = edge.NewChromium()
	if !chromium.Embed(hwnd) {
		return fmt.Errorf("WebView2 初始化失败(运行时缺失或加载失败)")
	}
	// Embed 后补一次初始 Resize(jchv webview.go L343 同款惯例):controller
	// bounds 与窗口物理尺寸首帧对齐,不调则首帧内容停在 CreateWindowExW 的
	// 临时尺寸上;高度自适应链的后续跟随同走 Resize。
	chromium.Resize()

	// 透明背景:COREWEBVIEW2_COLOR{0,0,0,0}——前端 hosted 态底色透明,
	// 材质层(亚克力)透出来;失败仅降级为白底,不阻断。
	if c2 := chromium.GetController().GetICoreWebView2Controller2(); c2 != nil {
		_ = c2.PutDefaultBackgroundColor(edge.COREWEBVIEW2_COLOR{A: 0, R: 0, G: 0, B: 0})
	}

	// 对象消息转字符串 shim:前端 postMessageHost 发的是 JS 对象,而
	// WebView2 的 WebMessageReceived 对对象消息走 TryGetWebMessageAsJson,
	// pkg/edge 的 MessageCallback 只接 TryGetWebMessageAsString(对象消息
	// 到手是空串)。协议冻结不许改前端,这里在文档创建时把 postMessage 包
	// 一层 JSON.stringify(宿主侧适配,页面无感知)。
	chromium.Init(`(function () {
  if (!window.chrome || !window.chrome.webview) return;
  var post = window.chrome.webview.postMessage.bind(window.chrome.webview);
  window.chrome.webview.postMessage = function (msg) {
    if (msg && typeof msg === 'object') { return post(JSON.stringify(msg)); }
    return post(msg);
  };
})();`)

	// ⑧ 导航白名单:仅放行 --url 的回环 origin。⚠️ 钉死的 pkg/edge 没有
	// NavigationStarting 钩子(vtbl 未导出),白名单经 WebResourceRequested
	// 的 DOCUMENT 过滤器落地:每次主文档导航(含重定向后的请求)都过闸,
	// 不放行的直接给 403 空响应——不变量等价(非白 origin 主文档永不加载)。
	chromium.AddWebResourceRequestedFilter("*", edge.COREWEBVIEW2_WEB_RESOURCE_CONTEXT_DOCUMENT)
	allowOrigin, originErr := LoopbackOrigin(opts.URL)
	if originErr != nil {
		return fmt.Errorf("面板页 URL 非法: %v", originErr)
	}
	chromium.WebResourceRequestedCallback = func(req *edge.ICoreWebView2WebResourceRequest, args *edge.ICoreWebView2WebResourceRequestedEventArgs) {
		uri, err := req.GetUri()
		if err == nil && AllowDocument(uri, allowOrigin) {
			return // 白名单内:放行
		}
		// 拒绝构造/挂载失败=安全闸无法闭合,宁可整面板退出(退出码 1 进
		// launcher 崩溃判据,崩溃计数可见)不可静默放行(fail-closed):
		// 原 fail-open(出错就什么都不做)会让请求落回 WebView2 默认处理,
		// 非 白 origin 内容可能直接加载。
		resp, respErr := chromium.Environment().CreateWebResourceResponse(nil, 403, "Forbidden", "")
		if respErr != nil {
			logger.Error(fmt.Sprintf("panelwin 403 拒绝响应构造失败,fail-closed 退出面板: %v", respErr))
			procPostQuitMessage.Call(1)
			return
		}
		if putErr := args.PutResponse(resp); putErr != nil {
			logger.Error(fmt.Sprintf("panelwin 403 拒绝响应挂载失败,fail-closed 退出面板: %v", putErr))
			procPostQuitMessage.Call(1)
		}
	}

	// ⑨ 消息 handler:quit → 退出码 60;open → ShellExecute 开默认浏览器
	// 成功即 PostQuitMessage(0) 正常关闭(失败驻留);resize → applyHeight
	// 跟随改窗高;未知类型默认忽略不崩(类型白名单:quit/open/resize,新增
	// 需双侧同批)。
	chromium.MessageCallback = func(payload string) {
		typ, kind, height, known := ParseHostMessage(payload)
		if !known {
			return // 未知类型/垃圾 JSON:忽略
		}
		if typ == "quit" {
			procPostQuitMessage.Call(uintptr(ExitQuit))
			return
		}
		if typ == "open" {
			if target := opts.OpenTarget(kind); target != "" {
				if openInDefaultBrowser(target) {
					// open 已分派 → 面板正常关闭(exit 0,2026-11-28 用户
					// 反馈「点仪表盘开了浏览器,面板不消失」):此前刻意驻留,
					// 指望浏览器抢前台→WM_ACTIVATE(WA_INACTIVE) 走失焦即
					// 退链——但浏览器冷启动时前台切换可能延迟数百毫秒,
					// 期间面板悬在浏览器前面(wsExTopmost);或者浏览器已在
					// 跑且以新标签打开时前台切换行为因浏览器而异。外部
					// 动作已完成,确定性退出,不押焦点副效应。退出码 0 走
					// launcher 正常关闭路径,零惩罚零重拉;与 macOS 侧
					// openURL 后 exit(0) 同语义。
					logger.Info("panelwin: open 已分派(" + kind + "),面板正常关闭(exit 0)")
					procPostQuitMessage.Call(uintptr(ExitFocusLoss))
					return
				}
			}
			// ShellExecute 失败(≤32)或未知 kind:没有外部动作发生,面板
			// 驻留(用户还可能换一个键再点),静默不动。
		}
		if typ == "resize" {
			// height 缺失/非数值时 ParseHostMessage 返回 0,过不了受理闸;
			// ≥280 才跟随(评审 Minor-7:与 macOS clamp 下限 280 同口径,
			// 见 panelwin_darwin.m resize 分支的 newH < 280;正常内容高恒在
			// 其上,这里是壳侧对畸形/退化值的兜底,塌缩成一条线的窗高不予
			// 跟随;解算层另有 240×scale 的几何兜底,panelwin_resize.go)。
			// 「applyHeight+落盘」与「上屏」解耦:height 闸只收窄落窗/落盘。
			if height >= 280 {
				applyHeight(height)
				// 高度持久化(首帧尺寸修正 Part 2):稳态逻辑高度落盘,
				// 下次启动 cmd/panel.go 读回作出生尺寸;失败静默降级。
				persistLastPanelHeight(height)
			}
			// 首帧尺寸修正 Part 1(P2-1 对齐 macOS):任何 known resize 消息
			// 都触发上屏,height 闸不收窄——收到 resize 即页面已活,「畸形
			// height 先按出生尺寸出现」优于盲等 1.2s 兜底;真实高度随后续
			// resize 到位再跟随(同 panelwin_darwin.m:showIfAwaitingFirstResize
			// 在 height 闸之外)。height 合法时已先落窗(上方 applyHeight),
			// 首帧即正确尺寸;后续 resize 时位已清,零操作。
			showNow("首次 resize 到位")
			// resize 后驻留。
		}
	}

	// Navigate 无返回值(edge 内部直调 vtbl,失败静默);白名单闸已保证
	// 非 白 origin 页面加载不进来。
	chromium.Navigate(opts.URL)

	// 首帧兜底装配(Navigate 之后——见 showNow 定义处的时序理由):1.2s 内
	// 页面未发 resize(加载失败/脚本异常)则按当前尺寸上屏,面板不能永远不
	// 出现。AfterFunc goroutine 不能直接动窗口(Win32 窗口 API 单线程语义),
	// 经自定消息 WM_APP+2(wmAppPanelShow)投递回消息循环线程,wndProc 分派
	// panelShowWindow 执行同一 showNow(位已清时零操作);正常路径 resize 先
	// 到,兜底空转。计时从 Navigate 起算:Embed 已完成、controller 已就绪,
	// 此刻起 1.2s 是纯粹的「页面加载超时」,不再混入 WebView2 冷启动耗时。
	panelShowWindow = showNow
	time.AfterFunc(panelFirstShowFallback, func() {
		procPostMessageW.Call(hwnd, uintptr(wmAppPanelShow), 0, 0)
	})

	// 自建消息循环:GetMessageW 返回 0 才是收到 WM_QUIT(wParam 即退出码:
	// 60=quit;WM_ACTIVATE 失活/WM_DESTROY/close 请求都 PostQuitMessage(0))。
	var m msgW
	gotQuit := false
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		switch int32(ret) {
		case -1:
			// -1=错误,按正常关闭处置(进程退出码 0,launcher 记正常关闭),
			// 错误本身 logger.Error 留痕。不读 m.WParam:GetMessageW 失败时
			// 结构体内容未定义,读它是残渣面。
			logger.Error("panelwin 消息循环 GetMessageW 返回 -1,按正常关闭收尾(退出码 0)")
			lastExitCode = ExitFocusLoss
			gotQuit = true
		case 0:
			// WM_QUIT:wParam 即退出码(0=失焦/销毁,60=quit)。
			lastExitCode = int(int32(m.WParam))
			gotQuit = true
		default:
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
		if gotQuit {
			break
		}
	}
	return nil
}

// panelWndProc — 窗口过程:失焦即退/close 请求分派与销毁收尾,其余交
// DefWindowProc。三个出口都 PostQuitMessage(0)=正常关闭(退出码 0,launcher
// 零惩罚)——窗口随进程退出彻底消失(「关闭即退」,2026-11-28 撤销方案 B)。
func panelWndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmActivate:
		// wParam 低 16 位=激活状态。
		//   WA_ACTIVE(1)/WA_CLICKACTIVE(2):首次激活 → 武装失焦即退链
		//     (panelEverActivated 置位;镜像 macOS everKeyed 守卫,2026-11-28
		//     评审修——未武装前收到 WA_INACTIVE 一律忽略,见该变量注释);
		//   WA_INACTIVE(0):点面板外/宿主切换 → 失焦即退:PostQuitMessage(0)
		//     正常关闭(撤销方案 B,2026-11-28 恢复原出口;方案 B 期间的
		//     SW_HIDE 驻留形态已废除)。
		switch wParam & 0xFFFF {
		case waActive, waClickActive:
			panelEverActivated = true
		case waInactive:
			if !panelEverActivated {
				logger.Debug("panelwin: WM_ACTIVATE(WA_INACTIVE) 忽略(从未激活过,初始化期焦点抖动防误伤)")
				return 0
			}
			procPostQuitMessage.Call(uintptr(ExitFocusLoss))
		}
		return 0
	case wmAppPanelToggle:
		// companion 托盘点击的 close 请求投递(变量名保留历史名 toggle,
		// 语义=请求关闭):PostQuitMessage(0) 正常关闭,窗口随进程退出
		// 消失(launcher 记 code=0 正常关闭零惩罚)。
		procPostQuitMessage.Call(uintptr(ExitFocusLoss))
		return 0
	case wmAppPanelShow:
		// 1.2s 兜底上屏(首帧尺寸修正 Part 1):AfterFunc goroutine 经自定
		// 消息投递到本线程执行同一 showNow(见 runPanelWindow 注释);正常
		// 路径 resize 先到、位已清,showNow 内零操作。
		if panelShowWindow != nil {
			panelShowWindow("1.2s 兜底(resize 未到)")
		}
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(uintptr(ExitFocusLoss))
		return 0
	default:
		r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
		return r
	}
}

// declareProcessDpiAwareness — ① 自含 DPI 声明(app/tray 同名函数未导出且
// internal 不得反向依赖 app 包,故就地自含):PerMonitorV2 → shcore →
// 经典 SetProcessDPIAware 逐级降级;已声明时的失败静默容忍。
func declareProcessDpiAwareness() {
	if procSetProcessDpiAwarenessCtx.Find() == nil {
		if r, _, _ := procSetProcessDpiAwarenessCtx.Call(dpiAwarenessContextPerMonitorV2); r != 0 {
			return
		}
	}
	if procSetProcessDpiAwareness.Find() == nil {
		if r, _, _ := procSetProcessDpiAwareness.Call(processPerMonitorDpiAware); r == 0 {
			return
		}
	}
	if procSetProcessDPIAware.Find() == nil {
		procSetProcessDPIAware.Call()
	}
}

// setDwmAttributeU32 — DWM 属性写入(圆角/亚克力);低版本失败静默降级。
func setDwmAttributeU32(hwnd uintptr, attr uint32, value uint32) {
	if procDwmSetWindowAttribute.Find() != nil {
		return
	}
	procDwmSetWindowAttribute.Call(hwnd, uintptr(attr), uintptr(unsafe.Pointer(&value)), unsafe.Sizeof(value))
}

// openInDefaultBrowser — ShellExecute "open" 开默认浏览器(open 消息分派)。
// 返回 shell 是否接受了请求(HINSTANCE > 32 为成功;≤32 是 Win32 错误码,
// 如 SE_ERR_NOASSOC 31=无关联程序、SE_ERR_FILE_NOT_FOUND 2)。失败时调用方
// 保持面板驻留,用户可换键重试或手动打开。
func openInDefaultBrowser(target string) bool {
	u16, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return false
	}
	openVerb, _ := windows.UTF16PtrFromString("open")
	ret, _, _ := procShellExecuteW.Call(0, // hwnd:与面板窗口解绑,失败不影响面板
		uintptr(unsafe.Pointer(openVerb)),
		uintptr(unsafe.Pointer(u16)),
		0, 0, uintptr(shellExecuteWSHownormal),
	)
	// ShellExecuteW 返回 HINSTANCE(64 位下高位有符号扩展;错误码 ≤32 的
	// 形态下低 32 位即错误码)。只判「>32」不看具体错误码:失败形态统一
	// 驻留,细分无额外动作可做。
	return int32(ret) > 32
}
