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
// 钉一件事:SetWindowPos 的坐标实参必须来自 reanchorPhysicalPos 的返回值
// (Task 15 DPI 物理重锚接线)。防未来重构退回 opts.X/opts.Y 原始坐标——
// 那会让 150% 缩放下面板底边越过图标顶约 280px 的破损形态回归(重锚纯函数
// 本身的算术已由 panelwin_reanchor_test.go 表驱动钉死,本测试钉接线)。

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
