package panelwin

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// panelwin_windows.go 的接线结构断言(M2 评审 M-7;GOOS 无关——只读源码
// 文本,不依赖 Windows 可编译,写法照 panelwin_darwin_struct_test.go 先例,
// 辅助函数就地复制不跨包引用)。
//
// 钉两件事:
//   - SetWindowPos 的坐标实参必须来自 reanchorPhysicalPos 的返回值(Task 15
//     DPI 物理重锚接线)。防未来重构退回 opts.X/opts.Y 原始坐标——那会让
//     150% 缩放下面板底边越过图标顶约 280px 的破损形态回归(重锚纯函数本身
//     的算术已由 panelwin_reanchor_test.go 表驱动钉死,本测试钉接线);
//   - resize 消息链完整(高度自适应,2026-11-28 用户拍板):分派带最低阈值
//     闸 → applyHeight 闭包按 opts.Side 锚定边重排 → SetWindowPos 落窗 →
//     局部跟踪矩形回写。防未来重构把 resize 分派拆散或 applyHeight 绕过
//     SetWindowPos 直接改别的窗口属性(高度自适应链静默断裂)。

func TestPanelwinWindowsSetWindowPosUsesReanchor(t *testing.T) {
	src := panelwinWindowsSource(t)

	// ① 重锚调用在册,且以具名返回值形态接线(reX, reY)。
	reIdx := strings.Index(src, "reX, reY := reanchorPhysicalPos(")
	if reIdx < 0 {
		t.Fatal("panelwin_windows.go 缺 reanchorPhysicalPos 接线(DPI 物理重锚被拆?):\n" + src)
	}

	// ② SetWindowPos 调用块整体在重锚之后(先算后用)。
	posIdx := strings.Index(src, "procSetWindowPos.Call(")
	if posIdx < 0 {
		t.Fatal("panelwin_windows.go 缺 procSetWindowPos.Call(尺寸校正丢失)")
	}
	if posIdx < reIdx {
		t.Fatalf("SetWindowPos(%d) 必须在 reanchorPhysicalPos(%d) 之后(先算后用)", posIdx, reIdx)
	}

	// ③ 调用块(至闭合括号)的坐标实参是重锚返回值,不得退回 opts.X/opts.Y。
	callBlock := src[posIdx:]
	if end := strings.Index(callBlock, "\n)"); end >= 0 {
		callBlock = callBlock[:end]
	}
	for _, want := range []string{"int32(reX)", "int32(reY)"} {
		if !strings.Contains(callBlock, want) {
			t.Fatalf("SetWindowPos 调用块坐标实参未取重锚返回值(缺 %q),退回原始坐标会让 150%% DPI 底边越图标顶回归:\n%s", want, callBlock)
		}
	}
	if strings.Contains(callBlock, "opts.X") || strings.Contains(callBlock, "opts.Y") {
		t.Fatalf("SetWindowPos 调用块出现原始 opts.X/opts.Y 坐标(物理重锚被绕过):\n%s", callBlock)
	}
}

// TestPanelwinWindowsResizeFollowChain — resize 高度自适应链(2026-11-28
// 用户拍板;评审增益批补锚):分派在 MessageCallback 内带受理闸(≥280,
// 与 macOS clamp 下限同口径),applyHeight 闭包在分派之前定义(先定义后
// 引用),闭包内经纯函数 resizeWindowRect 解算(表驱动见
// panelwin_resize_test.go)→ SetWindowPos(swpNoZOrder|swpNoActivate)落窗
// → chromium.Resize 同步 controller bounds → 局部跟踪矩形回写。
// GOOS 无关,只读源码文本(同上)。
func TestPanelwinWindowsResizeFollowChain(t *testing.T) {
	src := panelwinWindowsSource(t)

	// ① MessageCallback 的 resize 分派在册,带 ≥280 受理闸与 applyHeight
	//    调用(阈值闸防畸形/退化 height 把窗压成一条线;口径与 macOS clamp
	//    下限 280 对齐,评审 Minor-7)。
	dispIdx := strings.Index(src, `if typ == "resize" {`)
	if dispIdx < 0 {
		t.Fatal("panelwin_windows.go 缺 resize 分派(高度自适应链断裂):\n" + src)
	}
	for _, want := range []string{"height >= 280", "applyHeight(height)"} {
		if !strings.Contains(src, want) {
			t.Fatalf("panelwin_windows.go resize 分派缺 %q(受理闸/跟随调用丢失)", want)
		}
	}

	// ② applyHeight 闭包在册,且定义在分派之前(先定义后引用)。
	defIdx := strings.Index(src, "applyHeight := func(logicalH float64) {")
	if defIdx < 0 {
		t.Fatal("panelwin_windows.go 缺 applyHeight 闭包(高度跟随不落地)")
	}
	if defIdx > dispIdx {
		t.Fatalf("applyHeight(%d) 必须在 resize 分派(%d) 之前定义(先定义后引用)", defIdx, dispIdx)
	}

	// ③ chromium 变量先声明后赋值,声明在 applyHeight 之前(闭包可见性:
	//    闭包内落窗后要调 chromium.Resize;赋值推迟到 Embed 处)。
	if !strings.Contains(src, "var chromium *edge.Chromium") {
		t.Fatal("panelwin_windows.go 缺 chromium 先声明(var chromium *edge.Chromium;applyHeight 闭包引用不到即编译失败或退化为不跟随)")
	}

	// ④ 闭包跟踪矩形以重锚结果初始化(不退回 opts.X/opts.Y 原始坐标),
	//    且 resize 后回写跟踪矩形(下一次 resize 以最新矩形为基准)。
	if !strings.Contains(src, "curX, curY, curW, curH := reX, reY, int(physW), int(physH)") {
		t.Fatal("applyHeight 跟踪矩形未以重锚结果初始化(退回原始坐标会让 DPI 位移回归)")
	}
	if !strings.Contains(src, "curX, curY, curW, curH = x, y, w, h") {
		t.Fatal("applyHeight 未回写跟踪矩形(连续 resize 会以过期矩形为基准,窗口位置漂移)")
	}

	// ⑤ 几何解算走纯函数(评审必修 3):闭包只做跟踪/落窗/回写,公式变化
	//    由 panelwin_resize_test.go 表驱动拦,不在壳内散写。
	if !strings.Contains(src, "resizeWindowRect(curX, curY, curW, curH, logicalH, scale, opts.Side, workH)") {
		t.Fatal("applyHeight 缺 resizeWindowRect 纯函数接线(side/工作区钳高公式脱离表驱动守卫)")
	}

	// ⑥ 闭包内落窗走 SetWindowPos 且不带激活/Z 序扰动(与初始定位同旗标:
	// resize 只改几何,不抢焦点不重排 Z);落窗之后必须 chromium.Resize
	// (评审必修 1:pkg/edge 的 Chromium 只有显式 Resize GetClientRect→
	// PutBounds 才同步 controller bounds,不随父 HWND 自动跟随——不调则
	// 窗高变了内容停在旧高,裁剪/透明空区)。
	body := src[defIdx:]
	if end := strings.Index(body, "\n\t}"); end >= 0 {
		body = body[:end]
	}
	posIdx := strings.Index(body, "procSetWindowPos.Call(")
	resIdx := strings.Index(body, "chromium.Resize()")
	if posIdx < 0 {
		t.Fatal("applyHeight 闭包内缺 procSetWindowPos.Call(高度跟随不走 SetWindowPos)")
	}
	if !strings.Contains(body, "swpNoZOrder|swpNoActivate") {
		t.Fatal("applyHeight 的 SetWindowPos 缺 swpNoZOrder|swpNoActivate(resize 不得抢焦点/扰动 Z 序)")
	}
	if resIdx < 0 {
		t.Fatal("applyHeight 闭包内缺 chromium.Resize()(WebView2 内容不跟随窗高:裁剪/透明空区)")
	}
	if resIdx < posIdx {
		t.Fatalf("applyHeight 内 chromium.Resize(%d) 必须在 SetWindowPos(%d) 之后(先落窗再同步 bounds)", resIdx, posIdx)
	}

	// ⑦ Embed 后补一次初始 Resize(jchv webview.go L343 同款惯例):
	//    controller bounds 与窗口物理尺寸首帧对齐。
	embedIdx := strings.Index(src, "chromium.Embed(hwnd)")
	if embedIdx < 0 {
		t.Fatal("panelwin_windows.go 缺 chromium.Embed(hwnd)(WebView2 接线被拆)")
	}
	if strings.Index(src[embedIdx:], "chromium.Resize()") < 0 {
		t.Fatal("panelwin_windows.go Embed 之后缺初始 chromium.Resize()(首帧 controller bounds 不对齐窗口尺寸)")
	}
}

// TestPanelwinWindowsCloseOnDeactivate — 撤销方案 B「关闭即退」(2026-11-28)
// 的 Windows 壳结构断言:WM_ACTIVATE 失活(点面板外/宿主切换)→
// PostQuitMessage(0) 失焦即退;close 请求消息(wmApp+1)→ PostQuitMessage(0)
// 请求关闭;两个出口都零驻留——窗口随进程退出彻底消失。
// GOOS 无关,只读源码文本(同上)。
func TestPanelwinWindowsCloseOnDeactivate(t *testing.T) {
	src := panelwinWindowsSource(t)

	// ① close 请求消息常量与接收分支在册(WM_APP+1;与 app/tray 投递侧跨
	//    文件对齐由彼端 TestPanelM2CloseChannelContract 锚定)。常量锚容忍
	//    gofmt 对齐空格差异(正则)。
	for _, want := range []string{
		`wmApp\s+= 0x8000`,
		`wmAppPanelToggle = wmApp \+ 1`,
		"case wmAppPanelToggle:",
	} {
		if !regexp.MustCompile(want).MatchString(src) {
			t.Fatalf("panelwin_windows.go 缺 %q(close 请求接收链断裂)", want)
		}
	}

	// ② wmActivate 失活分支:PostQuitMessage(0) 失焦即退(不得再走任何
	//    隐藏/驻留动作——方案 B 的 SW_HIDE 形态已废除)。
	actIdx := strings.Index(src, "case wmActivate:")
	if actIdx < 0 {
		t.Fatal("panelwin_windows.go 缺 case wmActivate 分支")
	}
	actBody := src[actIdx:]
	// 截到下一个 case 分支(从本分支字面量之后起搜,避免命中自身的 "case ")。
	if next := strings.Index(src[actIdx+len("case wmActivate:"):], "\tcase "); next >= 0 {
		actBody = src[actIdx : actIdx+len("case wmActivate:")+next]
	}
	if !strings.Contains(actBody, "procPostQuitMessage.Call(uintptr(ExitFocusLoss))") {
		t.Fatalf("wmActivate 失活分支未 PostQuitMessage(0)(失焦即退链断裂):\n%s", actBody)
	}
	for _, banned := range []string{"hidePanelWindow", "swHide", "panelHidden", "panelIdle"} {
		if strings.Contains(actBody, banned) {
			t.Fatalf("wmActivate 失活分支仍在隐藏驻留(%q;撤销方案 B:失焦=PostQuitMessage(0)):\n%s", banned, actBody)
		}
	}

	// ③ close 请求分支:PostQuitMessage(0) 请求关闭。
	toggleIdx := strings.Index(src, "case wmAppPanelToggle:")
	if toggleIdx < 0 {
		t.Fatal("panelwin_windows.go 缺 case wmAppPanelToggle 分支")
	}
	toggleBody := src[toggleIdx:]
	if next := strings.Index(src[toggleIdx+len("case wmAppPanelToggle:"):], "\tcase "); next >= 0 {
		toggleBody = src[toggleIdx : toggleIdx+len("case wmAppPanelToggle:")+next]
	}
	if !strings.Contains(toggleBody, "procPostQuitMessage.Call(uintptr(ExitFocusLoss))") {
		t.Fatalf("close 请求分支未 PostQuitMessage(0)(请求关闭链断裂):\n%s", toggleBody)
	}

	// ④ 反向锚:方案 B 常驻态的进程级状态与隐藏/切换动作已删——全文件
	//    不得再引用已废除的符号。
	for _, banned := range []string{"hidePanelWindow", "togglePanelWindow", "panelChromium", "panelHidden", "panelIdle", "swHide"} {
		if strings.Contains(src, banned) {
			t.Fatalf("panelwin_windows.go 仍引用已废除的方案 B 符号 %q(隐藏驻留/空闲回收/toggle 切换均已随撤销批删除)", banned)
		}
	}
}

// TestPanelwinWindowsFirstFrameShow — 首帧尺寸修正(2026-11-28 用户反馈
// 「点击瞬间出现大空白区域再缩到目标尺寸」)的 Windows 壳结构断言:
//
//  1. 主路径【不立即 ShowWindow】——窗口创建后保持隐藏,procShowWindow 全
//     文件只在 showNow 闭包内出现一次(立即上屏会把缺省 560 高的空窗先闪
//     出来,正是本次要消灭的形态);
//  2. 首次 resize 分派 applyHeight 落窗后才经 showNow 上屏(首帧即正确尺寸),
//     并同步 persistLastPanelHeight 落盘(高度持久化 Part 2);
//  3. 1.2s 兜底:time.AfterFunc 经自定消息 WM_APP+2(wmAppPanelShow)投递回
//     消息循环线程,wndProc 分派 panelShowWindow 执行同一 showNow(AfterFunc
//     goroutine 不能直接动窗口,窗口 API 单线程语义)。
//
// GOOS 无关,只读源码文本(同上)。
func TestPanelwinWindowsFirstFrameShow(t *testing.T) {
	src := panelwinWindowsSource(t)

	// ① showNow 闭包在册:唯一上屏入口(SetForegroundWindow/ShowWindow/
	//    chromium.Focus 三件全在闭包内——焦点链不补齐则失焦即退失效)。
	showIdx := strings.Index(src, "showNow := func(reason string) {")
	if showIdx < 0 {
		t.Fatal("panelwin_windows.go 缺 showNow 闭包(首次上屏入口不存在,首帧尺寸闪变回归)")
	}
	showBody := src[showIdx:]
	if end := strings.Index(showBody, "\n\t}\n"); end >= 0 {
		showBody = showBody[:end]
	}
	for _, want := range []string{
		"procSetForegroundWindow.Call(hwnd)",
		"procShowWindow.Call(hwnd, uintptr(swShow))",
		"chromium.Focus()",
	} {
		if !strings.Contains(showBody, want) {
			t.Fatalf("showNow 闭包缺 %q(上屏/焦点链不完整,失焦即退或键盘焦点回归):\n%s", want, showBody)
		}
	}

	// ② 主路径不立即上屏:procShowWindow 全文件恰一次且都在 showNow 内;
	//    上屏前的启动段(创建→Embed→Navigate)不得出现 ShowWindow。
	if got := strings.Count(src, "procShowWindow.Call"); got != 1 {
		t.Fatalf("procShowWindow.Call 出现 %d 次, want 恰 1 次(showNow 唯一入口——立即上屏=首帧闪缺省 560 高空白)", got)
	}

	// ③ 兜底链:WM_APP+2 常量 + AfterFunc 投递 + wndProc 分派 panelShowWindow。
	if !regexp.MustCompile(`wmAppPanelShow\s+= wmApp \+ 2`).MatchString(src) {
		t.Fatal("panelwin_windows.go 缺 wmAppPanelShow = wmApp+2 常量(兜底自定消息不存在)")
	}
	if !strings.Contains(src, "procPostMessageW.Call(hwnd, uintptr(wmAppPanelShow), 0, 0)") {
		t.Fatal("panelwin_windows.go 缺兜底 PostMessageW 投递(AfterFunc goroutine 直接动窗口会跨线程)")
	}
	if !strings.Contains(src, "time.AfterFunc") {
		t.Fatal("panelwin_windows.go 缺 1.2s 兜底 AfterFunc(resize 未到时面板永不出现)")
	}
	caseIdx := strings.Index(src, "case wmAppPanelShow:")
	if caseIdx < 0 {
		t.Fatal("panelwin_windows.go 缺 case wmAppPanelShow 分派(兜底消息无人消费)")
	}
	if !strings.Contains(src[caseIdx:], "panelShowWindow(") {
		t.Fatalf("case wmAppPanelShow 未分派 panelShowWindow(兜底路径绕过唯一上屏入口):\n%s", src[caseIdx:caseIdx+200])
	}

	// ④ resize 分派:applyHeight 落窗之后 showNow 上屏 + persistLastPanelHeight
	//    落盘(顺序锚——先改尺寸再上屏,首帧即正确尺寸)。
	dispIdx := strings.Index(src, `if typ == "resize" {`)
	if dispIdx < 0 {
		t.Fatal("panelwin_windows.go 缺 resize 分派")
	}
	dispBody := src[dispIdx:]
	if next := strings.Index(dispBody, "\n\t\t}"); next >= 0 {
		dispBody = dispBody[:next]
	}
	applyIdx := strings.Index(dispBody, "applyHeight(height)")
	persistIdx := strings.Index(dispBody, "persistLastPanelHeight(height)")
	showCallIdx := strings.Index(dispBody, "showNow(")
	if applyIdx < 0 || persistIdx < 0 || showCallIdx < 0 {
		t.Fatalf("resize 分派缺落窗/落盘/上屏链(applyHeight=%d persistLastPanelHeight=%d showNow=%d):\n%s", applyIdx, persistIdx, showCallIdx, dispBody)
	}
	if showCallIdx < applyIdx {
		t.Fatalf("showNow(%d) 必须在 applyHeight(%d) 之后(先落窗再上屏,首帧即正确尺寸)", showCallIdx, applyIdx)
	}

	// ⑤ panelShowWindow 注入口在册(wndProc 与 runPanelWindow 的接缝)。
	if !strings.Contains(src, "var panelShowWindow func(reason string)") {
		t.Fatal("panelwin_windows.go 缺 panelShowWindow 注入口(wndProc 兜底分派接不到 showNow)")
	}
}

// panelwinWindowsSource 读取本包的 Windows 壳源码。
func panelwinWindowsSource(t *testing.T) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: 无法定位测试文件")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "panelwin_windows.go"))
	if err != nil {
		t.Fatalf("读取 panelwin_windows.go: %v", err)
	}
	return string(src)
}
