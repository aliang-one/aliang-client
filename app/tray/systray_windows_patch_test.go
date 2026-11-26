package tray

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestSystrayWindowsPanelPatch 钉住 Windows 托盘底座面板胶水的补丁形态
// (2026-11-26 托盘面板 M1 Task 3):公共层点击回调 hook、wndProc 左右键
// 分支、托盘图标屏幕矩形 TrayRect、showMenu 的 TPM_RIGHTBUTTON/WM_NULL
// 小修。third_party/systray 以「逐字节忠实于上游 + 仅功能补丁」为纪律,
// 补丁只靠 PATCH_NOTES 里 grep「本仓补丁」的人工流程重放——这里把六件事
// 钉成测试,同步上游漏重放即红。
//
// 同时把「照做必错」的点钉死(评审阻塞项):
//   - TrayRect 的 cbSize 必须用 uint32(unsafe.Sizeof(notifyIconIdentifier{}))
//     表达式:该结构 x64 sizeof=40,照抄上游 notifyIconData 的字面常量写法
//     会写 24,Shell_NotifyIconGetRect 会恒 E_INVALIDARG,而错误又被兜底
//     静默掩盖,现场只剩「拿不到矩形」。
//   - Call 必须是两参形态(&identifier, &rect):真实 Win32 签名第二参是
//     RECT*(调用方分配好的矩形指针,MSDN 语法块的 LPRECT* 是文档笔误);
//     且 NOTIFYICONIDENTIFIER 与库内 notifyIconData(NOTIFYICONDATAW)是
//     两个不同结构体,不得混用。
//   - 成功只认 ret==0(S_OK);S_FALSE(1)表示图标在溢出区(Win10 给溢出
//     箭头矩形 / Win11 给屏外坐标),连同 rect 原样透传给调用方判废。
//
// 本机是 macOS 跑不了 Windows GUI,验证手段只有编译 + 本结构测试。
func TestSystrayWindowsPanelPatch(t *testing.T) {
	publicSrc := vendoredSystrayPublicSource(t)
	winSrc := vendoredSystrayWindowsSource(t)

	t.Run("公共层点击回调hook", func(t *testing.T) {
		for _, want := range []string{
			"var panelClickFn func()",
			"func SetPanelClickHook(fn func()) {",
			"panelClickFn = fn",
		} {
			if !strings.Contains(publicSrc, want) {
				t.Errorf("systray.go 缺少 %q", want)
			}
		}
		assertAnchorAbove(t, publicSrc, "var panelClickFn func()", 6)
	})

	t.Run("wndProc左右键分支", func(t *testing.T) {
		body := goFuncBody(t, winSrc, "wndProc")
		const segOpen = "switch lParam {"
		idx := strings.Index(body, segOpen)
		if idx < 0 {
			t.Fatalf("wndProc 里找不到 lParam 分发开关(%q):%s", segOpen, body)
		}
		open := idx + len(segOpen) - 1
		close := matchingBrace(body, open)
		if close < 0 {
			t.Fatal("wndProc 的 lParam 开关花括号不闭合")
		}
		seg := body[idx : close+1]

		for _, want := range []string{
			"case WM_RBUTTONUP, WM_LBUTTONUP:",
			"if panelClickFn != nil {",
			"go panelClickFn()",
			"} else {",
			"t.showMenu()",
		} {
			if !strings.Contains(seg, want) {
				t.Errorf("wndProc 的 lParam 分支缺少 %q", want)
			}
		}
		// 顺序钉住 if/else 语义:hook 已设置走点击回调(不再 showMenu),
		// 未设置才落回原生菜单兜底。
		const anchor = "if panelClickFn != nil {"
		if start := strings.Index(seg, anchor); start >= 0 {
			tail := seg[start:]
			order := []string{anchor, "go panelClickFn()", "} else {", "t.showMenu()"}
			pos := 0
			for _, want := range order {
				at := strings.Index(tail[pos:], want)
				if at < 0 {
					t.Errorf("wndProc 点击分支顺序不对,期望依次出现 %v", order)
					break
				}
				pos += at + len(want)
			}
		}
		if !strings.Contains(seg, "本仓补丁") {
			t.Error("wndProc 点击分支缺少「本仓补丁」注释锚点")
		}
	})

	t.Run("identifier结构体与proc声明", func(t *testing.T) {
		const procLine = `pShell_NotifyIconGetRect = s32.NewProc("Shell_NotifyIconGetRect")`
		if !strings.Contains(winSrc, procLine) {
			t.Errorf("systray_windows.go 缺少 proc 声明 %q", procLine)
		}
		assertAnchorAbove(t, winSrc, procLine, 2)

		body := goStructBody(t, winSrc, "notifyIconIdentifier")
		for _, field := range []struct{ name, typ string }{
			{"cbSize", "uint32"},
			{"hWnd", "windows.Handle"},
			{"uID", "uint32"},
			{"guidItem", "windows.GUID"},
		} {
			if !regexp.MustCompile(regexp.QuoteMeta(field.name) + `\s+` + regexp.QuoteMeta(field.typ)).
				MatchString(body) {
				t.Errorf("notifyIconIdentifier 缺少字段 %s %s(Win32 NOTIFYICONIDENTIFIER 四字段必须齐全):%s",
					field.name, field.typ, body)
			}
		}
		assertAnchorAbove(t, winSrc, "type notifyIconIdentifier struct {", 4)
	})

	t.Run("TrayRect_cbSize用Sizeof表达式", func(t *testing.T) {
		body := goFuncBody(t, winSrc, "TrayRect")
		const expr = "cbSize: uint32(unsafe.Sizeof(notifyIconIdentifier{}))"
		if !strings.Contains(body, expr) {
			t.Fatalf("TrayRect 的 cbSize 必须用 %s 表达式(该结构 x64 sizeof=40,字面常量 24 会让 API 恒 E_INVALIDARG 且被兜底静默掩盖):\n%s", expr, body)
		}
		if regexp.MustCompile(`cbSize:\s*[0-9]`).MatchString(body) {
			t.Fatalf("TrayRect 的 cbSize 不允许写字面常量:\n%s", body)
		}
		assertAnchorAbove(t, winSrc, "func TrayRect() (rect, error) {", 6)
	})

	t.Run("TrayRect_两参Call且只认S_OK", func(t *testing.T) {
		body := goFuncBody(t, winSrc, "TrayRect")
		const callHead = "pShell_NotifyIconGetRect.Call("
		callIdx := strings.Index(body, callHead)
		if callIdx < 0 {
			t.Fatalf("TrayRect 里找不到 %s:%s", callHead, body)
		}
		open := callIdx + len(callHead) - 1
		close := matchingParen(body, open)
		if close < 0 {
			t.Fatal("TrayRect 的 Call 括号不闭合")
		}
		args := body[open+1 : close]

		// 两参形态:恰好两个指针实参(&identifier, &rect)。
		if got := strings.Count(args, "uintptr(unsafe.Pointer(&"); got != 2 {
			t.Errorf("Shell_NotifyIconGetRect 必须恰好两参(&identifier, &rect),实为 %d 个指针实参:\n%s", got, args)
		}
		for _, want := range []string{
			"uintptr(unsafe.Pointer(&idf))",
			"uintptr(unsafe.Pointer(&r))",
			"const S_OK = 0",
			"if ret != S_OK {",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("TrayRect 缺少 %q(成功只认 ret==0 即 S_OK)", want)
			}
		}
		if strings.Contains(body, "notifyIconData") {
			t.Error("TrayRect 混用了 notifyIconData(NOTIFYICONDATAW)——它与 NOTIFYICONIDENTIFIER(notifyIconIdentifier)是两个不同结构体,不得混用")
		}
	})

	t.Run("showMenu弹菜单小修", func(t *testing.T) {
		body := goFuncBody(t, winSrc, "showMenu")
		const post = "pPostMessage.Call(uintptr(t.window), WM_NULL, 0, 0)"
		if !strings.Contains(body, post) {
			t.Errorf("showMenu 缺少 %q(TrackPopupMenu 返回后必须喂窗口一条无害消息,否则菜单不收起、再次点击图标弹不出)", post)
		}
		if !regexp.MustCompile(`WM_NULL\s*=\s*0x0000`).MatchString(body) {
			t.Errorf("showMenu 缺少 WM_NULL = 0x0000 常量定义")
		}
		const trackHead = "pTrackPopupMenu.Call("
		trackIdx := strings.Index(body, trackHead)
		if trackIdx < 0 {
			t.Fatalf("showMenu 里找不到 %s:%s", trackHead, body)
		}
		open := trackIdx + len(trackHead) - 1
		close := matchingParen(body, open)
		if close < 0 {
			t.Fatal("showMenu 的 TrackPopupMenu 括号不闭合")
		}
		if args := body[open+1 : close]; !strings.Contains(args, "TPM_RIGHTBUTTON") {
			t.Errorf("TrackPopupMenu flags 未包含 TPM_RIGHTBUTTON:\n%s", args)
		}
		if !strings.Contains(body, "本仓补丁") {
			t.Error("showMenu 小修缺少「本仓补丁」注释锚点")
		}
	})
}

func vendoredSystrayPublicSource(t *testing.T) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: 无法定位测试文件")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", "third_party", "systray", "systray.go"))
	if err != nil {
		t.Fatalf("读取内嵌 systray 的公共层: %v", err)
	}
	return string(src)
}

func vendoredSystrayWindowsSource(t *testing.T) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: 无法定位测试文件")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", "third_party", "systray", "systray_windows.go"))
	if err != nil {
		t.Fatalf("读取内嵌 systray 的 Windows 后端: %v", err)
	}
	return string(src)
}

// goFuncBody 返回 Go 源码里名为 name 的顶层函数(含方法)最外层花括号之间
// 的正文。
func goFuncBody(t *testing.T, src, name string) string {
	t.Helper()

	def := regexp.MustCompile(`(?m)^func\s+(?:\([^)]*\)\s*)?` + regexp.QuoteMeta(name) + `\s*\(`)
	loc := def.FindStringIndex(src)
	if loc == nil {
		t.Fatalf("源码里找不到函数 %s 的定义", name)
	}
	open := strings.Index(src[loc[1]:], "{")
	if open < 0 {
		t.Fatalf("函数 %s 没有函数体", name)
	}
	open += loc[1]
	close := matchingBrace(src, open)
	if close < 0 {
		t.Fatalf("函数 %s 的花括号不闭合", name)
	}
	return src[open+1 : close]
}

// goStructBody 返回名为 name 的结构体定义的花括号内正文。
func goStructBody(t *testing.T, src, name string) string {
	t.Helper()

	decl := "type " + name + " struct {"
	idx := strings.Index(src, decl)
	if idx < 0 {
		t.Fatalf("源码里找不到结构体 %s 的定义", name)
	}
	open := idx + len(decl) - 1
	close := matchingBrace(src, open)
	if close < 0 {
		t.Fatalf("结构体 %s 的花括号不闭合", name)
	}
	return src[open+1 : close]
}

// matchingParen 返回 s[open] 这个 '(' 对应的 ')' 的下标。
func matchingParen(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// assertAnchorAbove 断言 src 里首个包含 want 的行上方 back 行内出现
// 「本仓补丁」注释锚点(升级时靠 grep 此词重放补丁)。
func assertAnchorAbove(t *testing.T, src, want string, back int) {
	t.Helper()

	lines := strings.Split(src, "\n")
	idx := -1
	for i, line := range lines {
		if strings.Contains(line, want) {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Errorf("源码里找不到 %q,无法检查补丁锚点", want)
		return
	}
	lo := max(idx-back, 0)
	above := strings.Join(lines[lo:idx], "\n")
	if !strings.Contains(above, "本仓补丁") {
		t.Errorf("%q 上方 %d 行内没有「本仓补丁」注释锚点(升级重放时 grep 此词找不到该补丁):\n%s", want, back, above)
	}
}
