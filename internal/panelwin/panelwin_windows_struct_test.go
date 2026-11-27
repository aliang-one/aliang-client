package panelwin

import (
	"os"
	"path/filepath"
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
// 用户拍板):分派在 MessageCallback 内带最低阈值闸,applyHeight 闭包在
// 分派之前定义(先定义后引用),闭包内按 opts.Side 锚定边重排并以
// SetWindowPos(swpNoZOrder|swpNoActivate)落窗、局部跟踪矩形回写。
// GOOS 无关,只读源码文本(同上)。
func TestPanelwinWindowsResizeFollowChain(t *testing.T) {
	src := panelwinWindowsSource(t)

	// ① MessageCallback 的 resize 分派在册,带 >120 最低阈值与 applyHeight
	//    调用(阈值闸防畸形/退化 height 把窗压成一条线)。
	dispIdx := strings.Index(src, `if typ == "resize" {`)
	if dispIdx < 0 {
		t.Fatal("panelwin_windows.go 缺 resize 分派(高度自适应链断裂):\n" + src)
	}
	for _, want := range []string{"height > 120", "applyHeight(height)"} {
		if !strings.Contains(src, want) {
			t.Fatalf("panelwin_windows.go resize 分派缺 %q(最低阈值/跟随调用丢失)", want)
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

	// ③ 闭包跟踪矩形以重锚结果初始化(不退回 opts.X/opts.Y 原始坐标),
	//    且 resize 后回写跟踪矩形(下一次 resize 以最新矩形为基准)。
	if !strings.Contains(src, "curX, curY, curW, curH := reX, reY, int(physW), int(physH)") {
		t.Fatal("applyHeight 跟踪矩形未以重锚结果初始化(退回原始坐标会让 DPI 位移回归)")
	}
	if !strings.Contains(src, "curY, curH = newY, newH") {
		t.Fatal("applyHeight 未回写跟踪矩形(连续 resize 会以过期矩形为基准,窗口位置漂移)")
	}

	// ④ 闭包按 opts.Side 锚定边重排(top=顶边固定/bottom=底边固定)。
	if !strings.Contains(src, "switch opts.Side {") {
		t.Fatal("applyHeight 缺 opts.Side 锚定边判别(side-aware 跟随退化为单边)")
	}

	// ⑤ 闭包内落窗走 SetWindowPos 且不带激活/Z 序扰动(与初始定位同旗标:
	// resize 只改几何,不抢焦点不重排 Z)。
	body := src[defIdx:]
	if end := strings.Index(body, "\n\t}"); end >= 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "procSetWindowPos.Call(") {
		t.Fatal("applyHeight 闭包内缺 procSetWindowPos.Call(高度跟随不走 SetWindowPos)")
	}
	if !strings.Contains(body, "swpNoZOrder|swpNoActivate") {
		t.Fatal("applyHeight 的 SetWindowPos 缺 swpNoZOrder|swpNoActivate(resize 不得抢焦点/扰动 Z 序)")
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
