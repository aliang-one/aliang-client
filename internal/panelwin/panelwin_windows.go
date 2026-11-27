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
// 生命周期:WM_ACTIVATE 失活 → 退出码 0(失焦自退);页面 quit 消息 →
// 退出码 60;open 消息 → ShellExecute 开默认浏览器后驻留。退出码契约见
// panelwin.go(Task 14 launcher:0/60 正常关闭,其余=崩溃)。
//
// 前台交接:显示前必须 SetForegroundWindow——companion spawn 时的
// AllowSetForegroundWindow(pid)(Task 15)只授予资格,本进程不调
// SetForegroundWindow 则 WM_ACTIVATE 激活事件不发,失活(=失焦自退)永不
// 触发,整条失焦即退链失效(二审阻塞 3)。
package panelwin

import (
	"fmt"
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
	wmDestroy  = 0x0002
	// 注:WM_QUIT(0x0012)不再声明常量——退出码协议已收进自建消息循环的
	// WM_QUIT 分支(GetMessageW 返回 0 时直接读 wParam),见下方消息循环注释。

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

var (
	user32  = windows.NewLazySystemDLL("user32.dll")
	shcore  = windows.NewLazySystemDLL("shcore.dll")
	dwmapi  = windows.NewLazySystemDLL("dwmapi.dll")
	shell32 = windows.NewLazySystemDLL("shell32.dll")

	procGetModuleHandleW          = user32.NewProc("GetModuleHandleW")
	procRegisterClassExW          = user32.NewProc("RegisterClassExW")
	procCreateWindowExW           = user32.NewProc("CreateWindowExW")
	procDefWindowProcW            = user32.NewProc("DefWindowProcW")
	procGetMessageW               = user32.NewProc("GetMessageW")
	procTranslateMessage          = user32.NewProc("TranslateMessage")
	procDispatchMessageW          = user32.NewProc("DispatchMessageW")
	procPostQuitMessage           = user32.NewProc("PostQuitMessage")
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

// Run 阻塞展示面板,经消息循环正常返回退出码(0=失焦自退,60=quit)。
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

	// ③ 圆角 + 亚克力(Win11 22621+);低版本 DwmSetWindowAttribute 返回
	// 失败,静默降级为纯半透明底(前端半透明底色仍是完整可用的形态)。
	setDwmAttributeU32(hwnd, dwmwaWindowCornerPreference, dwmwcpRound)
	setDwmAttributeU32(hwnd, dwmwaSystemBackdropType, dwmsbtTransientWindow)

	// ④ WebView2 必须用 pkg/edge 的 Chromium(见文件头),Embed 到自建
	// HWND——Embed 内部自泵消息直到 controller 就绪。
	chromium := edge.NewChromium()
	if !chromium.Embed(hwnd) {
		return fmt.Errorf("WebView2 初始化失败(运行时缺失或加载失败)")
	}

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
	// 后驻留;未知类型默认忽略不崩(协议冻结:quit/open 两类型)。
	chromium.MessageCallback = func(payload string) {
		typ, kind, known := ParseHostMessage(payload)
		if !known {
			return // 未知类型/垃圾 JSON:忽略
		}
		if typ == "quit" {
			procPostQuitMessage.Call(uintptr(ExitQuit))
			return
		}
		if typ == "open" {
			if target := opts.OpenTarget(kind); target != "" {
				openInDefaultBrowser(target)
			}
			// open 后驻留。
		}
	}

	// Navigate 无返回值(edge 内部直调 vtbl,失败静默);白名单闸已保证
	// 非 白 origin 页面加载不进来。
	chromium.Navigate(opts.URL)

	// ⑤ 显示前 SetForegroundWindow(见文件头:AllowSetForegroundWindow 只
	// 授予资格,不调则 WM_ACTIVATE 激活不发、失焦即退整条失效)。
	procSetForegroundWindow.Call(hwnd)
	procShowWindow.Call(hwnd, uintptr(swShow))
	// 首帧键盘焦点:SetForegroundWindow 只把窗口推到前台,WebView2 内部
	// 文档不一定拿到焦点(首帧键盘输入丢失);程序化 MoveFocus 补齐
	// (pkg/edge 的 Focus() 无返回值,失败形态随 Task 16 实机探针收口)。
	chromium.Focus()

	// 自建消息循环:GetMessageW 返回 0 才是收到 WM_QUIT(wParam 即退出码:
	// 60=quit;WM_ACTIVATE 失活与 WM_DESTROY 都 PostQuitMessage(0))。
	var m msgW
	gotQuit := false
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		switch int32(ret) {
		case -1:
			// -1=错误,按失焦自退处置(进程退出码 0,launcher 记正常关闭),
			// 错误本身 logger.Error 留痕。不读 m.WParam:GetMessageW 失败时
			// 结构体内容未定义,读它是残渣面。
			logger.Error("panelwin 消息循环 GetMessageW 返回 -1,按失焦自退收尾(退出码 0)")
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

// panelWndProc — 窗口过程:失焦即退(⑦)与销毁收尾,其余交 DefWindowProc。
func panelWndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmActivate:
		// wParam 低 16 位=激活状态;WA_INACTIVE=失活(点面板外/宿主切换)
		// → 失焦自退,退出码 0(launcher 判据里的正常关闭)。
		if wParam&0xFFFF == waInactive {
			procPostQuitMessage.Call(uintptr(ExitFocusLoss))
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

// openInDefaultBrowser — ShellExecute "open" 开默认浏览器(open 消息分派;
// 面板驻留不退出)。
func openInDefaultBrowser(target string) {
	u16, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return
	}
	openVerb, _ := windows.UTF16PtrFromString("open")
	procShellExecuteW.Call(0, // hwnd:与面板窗口解绑,失败不影响面板
		uintptr(unsafe.Pointer(openVerb)),
		uintptr(unsafe.Pointer(u16)),
		0, 0, uintptr(shellExecuteWSHownormal),
	)
}
