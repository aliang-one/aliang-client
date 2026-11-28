package tray

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// 内嵌补丁版 systray 的 macOS 后端补丁形态锚定 2026-11-26 macOS companion
// 内存泄漏:third_party/systray 以「逐字节忠实于上游 + 仅功能补丁」为纪律,
// 补丁只靠 PATCH_NOTES 里的人工流程重放。这里把「每个可被 goroutine 直接
// 调到的 C 入口都必须在调用线程上排空 autorelease」钉成测试,同步上游时
// 漏重放即红。
//
// 泄漏机理:这些入口在调用者 goroutine 所在的线程上创建 NSData/NSImage/
// NSString/NSNumber 及图片解码的中间对象(大量是 autoreleased),然后
// runInMainThread 才切到主线程;Go 运行时的线程上没有 autorelease pool,
// 这些对象永不释放。macOS companion 每 2s 一轮同步,实测约 2GB/天
// (RSS 很小是因为泄漏页全被系统压缩,要看 footprint)。上游 #135 自
// 2020 年开着至今。

// goroutineCallableDarwinEntries 是 systray.h 声明、Go 侧经 cgo 直接调用的
// 全部入口:它们都跑在任意 goroutine 所在的线程上,而不是主线程。
var goroutineCallableDarwinEntries = []string{
	"setIcon",
	"setMenuItemIcon",
	"setTitle",
	"setTooltip",
	"add_or_update_menu_item",
	"add_separator",
	"hide_menu_item",
	"show_menu_item",
	"quit",
	// 本仓补丁:面板模式胶水(2026-11-26 托盘面板 M1 Task 4)——同样会被
	// goroutine 直接调用,包 pool 纪律自动生效(上面的排空测试逐个校验)。
	"set_panel_mode",
	"tray_rect",
}

// noOwnPoolNeededDarwinFunctions 是不需要自己那层 pool 的 C 函数:只在主线程上
// 执行,或只被已包 pool 的入口调用,且自身不创建 autoreleased 对象。
var noOwnPoolNeededDarwinFunctions = []string{
	"registerSystray",
	"nativeLoop",
	"runInMainThread",
	"find_menu_item",
}

func TestVendoredSystrayDarwinEntriesDrainAutoreleaseOnCallingThread(t *testing.T) {
	src := vendoredSystrayDarwinSource(t)

	for _, fn := range goroutineCallableDarwinEntries {
		t.Run(fn, func(t *testing.T) {
			body := strings.TrimSpace(cFunctionBody(t, src, fn))

			// 函数体必须「整体」是一层 pool:入口处新建的 autoreleased 对象
			// 只要有一个落在 pool 外,就会随同步循环永久泄漏。
			const poolOpen = "@autoreleasepool {"
			if !strings.HasPrefix(body, poolOpen) {
				t.Fatalf("%s 的函数体没有包在 @autoreleasepool 里:\n%s", fn, body)
			}
			if close := matchingBrace(body, strings.Index(body, "{")); close != len(body)-1 {
				t.Fatalf("%s 的 @autoreleasepool 只包住了函数体的一部分(pool 闭合于 %d,函数体长 %d):\n%s", fn, close, len(body), body)
			}
			if !strings.Contains(body, "本仓补丁") {
				t.Fatalf("%s 的补丁缺少「本仓补丁」注释锚点(升级时靠 grep 此词重放补丁):\n%s", fn, body)
			}
		})
	}
}

func TestVendoredSystrayDarwinEntryListIsComplete(t *testing.T) {
	src := vendoredSystrayDarwinSource(t)
	defs := cFunctionDefinitions(src)
	if len(defs) == 0 {
		t.Fatal("未匹配到任何 C 函数定义,正则已失效")
	}

	// 上游新增入口时强制归类:会被 goroutine 直接调用的必须进
	// goroutineCallableDarwinEntries(并包 pool),否则进 noOwnPoolNeeded。
	for _, fn := range defs {
		if slices.Contains(goroutineCallableDarwinEntries, fn) || slices.Contains(noOwnPoolNeededDarwinFunctions, fn) {
			continue
		}
		t.Errorf("systray_darwin.m 出现未归类的 C 函数 %q:请判断它是否会被 goroutine 直接调用", fn)
	}
}

func TestCFunctionDefinitionsKeepsNestedParenthesesInParameters(t *testing.T) {
	// 参数表里的函数指针/块自带右括号。正则若把第一个右括号当成参数表结束,
	// 这种入口会从清单里静默消失,完整性检查就成了假通过。
	src := "void withFnPtr(void (*cb)(int)) {\n  cb(1);\n}\n\n" +
		"void withBlock(void (^blk)(int, int)) {\n  blk(1, 2);\n}\n"

	got := cFunctionDefinitions(src)
	want := []string{"withFnPtr", "withBlock"}
	if !slices.Equal(got, want) {
		t.Fatalf("cFunctionDefinitions() = %v, want %v", got, want)
	}
}

func vendoredSystrayDarwinSource(t *testing.T) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: 无法定位测试文件")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", "third_party", "systray", "systray_darwin.m"))
	if err != nil {
		t.Fatalf("读取内嵌 systray 的 macOS 后端: %v", err)
	}
	return string(src)
}

// cParameterList 匹配 C 函数定义的参数表。参数表内允许出现嵌套括号(块类型、
// 函数指针),这类入口才不会从清单里静默消失;定义与声明靠结尾的 `{` 区分。
const cParameterList = `\([^;{}]*\)`

// cFunctionDefinitions 列出顶格书写的 C 函数定义名(不含 ObjC 方法)。
func cFunctionDefinitions(src string) []string {
	def := regexp.MustCompile(`(?m)^[A-Za-z_][A-Za-z0-9_ *]*?\b([A-Za-z_][A-Za-z0-9_]*)\s*` + cParameterList + `\s*\{`)

	var names []string
	for _, m := range def.FindAllStringSubmatch(src, -1) {
		names = append(names, m[1])
	}
	return names
}

// cFunctionBody 返回 name 对应 C 函数最外层花括号之间的正文。
func cFunctionBody(t *testing.T, src, name string) string {
	t.Helper()

	idx := slices.Index(cFunctionDefinitions(src), name)
	if idx < 0 {
		t.Fatalf("systray_darwin.m 里找不到 C 函数 %s 的定义", name)
	}
	def := regexp.MustCompile(`(?m)^[A-Za-z_][A-Za-z0-9_ *]*?\b` + regexp.QuoteMeta(name) + `\s*` + cParameterList + `\s*\{`)
	loc := def.FindStringIndex(src)
	if loc == nil {
		t.Fatalf("systray_darwin.m 里找不到 C 函数 %s 的定义", name)
	}
	close := matchingBrace(src, loc[1]-1)
	if close < 0 {
		t.Fatalf("systray_darwin.m 里 C 函数 %s 的花括号不闭合", name)
	}
	return src[loc[1]:close]
}

// matchingBrace 返回 s[open] 这个 '{' 对应的 '}' 的下标。
func matchingBrace(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// TestVendoredSystrayDarwinPanelPatch 钉住 macOS 托盘底座面板胶水的补丁形态
// (2026-11-26 托盘面板 M1 Task 4,对齐调研 §6.5):panelMode=true 时 AppDelegate
// 摘掉原生菜单、给按钮挂 trayClicked:(sendActionOn 掩码含左右键按下,点击 →
// systray_tray_clicked → Go 侧 panelClickFn);panelMode=false 时摘掉 action、
// 接回原生菜单(原生菜单兜底)。菜单对象与刷新循环完全不动,切换无损。
// third_party/systray 以「逐字节忠实于上游 + 仅功能补丁」为纪律,补丁只靠
// PATCH_NOTES 里 grep「本仓补丁」的人工流程重放——这里钉成测试,同步上游漏
// 重放即红。新 C 入口的 @autoreleasepool 纪律由上面的清单类测试自动覆盖。
//
// export 三处分工按仓内惯例(与既有 systray_menu_item_selected 同款):
//   - Go 侧 systray_nonwindows.go 持 //export systray_tray_clicked(实现体);
//   - systray.h 持 extern 声明;
//   - systray_darwin.m 持调用点(trayClicked: → systray_tray_clicked())。
//
// darwin 侧 TrayRect 是整数四元组、Windows 侧是导出 Rect 类型——两种形态
// 并存是既定设计(systray_windows.go 的 TrayRect 见 systray_windows_patch_test.go)。
func TestVendoredSystrayDarwinPanelPatch(t *testing.T) {
	mSrc := vendoredSystrayDarwinSource(t)
	goSrc := vendoredSystrayDarwinGoSource(t)
	nonwindowsSrc := vendoredSystrayNonwindowsSource(t)
	headerSrc := vendoredSystrayHeaderSource(t)

	t.Run("面板模式开关", func(t *testing.T) {
		// C 入口照 setIcon 等既有入口写法,经 runInMainThread 封送主线程。
		body := cFunctionBody(t, mSrc, "set_panel_mode")
		if !strings.Contains(body, "runInMainThread(@selector(setPanelMode:)") {
			t.Fatalf("set_panel_mode 必须经 runInMainThread 封送主线程(窗口/状态项只能在主线程动):\n%s", body)
		}

		// AppDelegate 方法:摘菜单挂 action(左/右键按下都触发)与摘 action 接回菜单。
		for _, want := range []string{
			"statusItem.menu = nil",  // detach:摘菜单,面板模式
			"statusItem.menu = menu", // attach:接回原生菜单(兜底)
			"statusItem.button.action = @selector(trayClicked:)",
			"sendActionOn:",             // 按下即触发(而非默认抬起)
			"NSEventMaskLeftMouseDown",  // 左键
			"NSEventMaskRightMouseDown", // 右键:左右键都触发,→ panelClickFn
			// false 分支必须把掩码恢复成按钮默认的抬起触发:上游同步重放时漏
			// 这行,兜底模式下按钮仍按下即发 action(与 menu 接管相冲)。
			"sendActionOn:NSEventMaskLeftMouseUp",
		} {
			if !strings.Contains(mSrc, want) {
				t.Errorf("systray_darwin.m 缺少 %q(面板模式切换形态不完整)", want)
			}
		}
		assertAnchorAbove(t, mSrc, "- (void)setPanelMode:(NSNumber *)panel {", 8)
	})

	t.Run("点击转发链路", func(t *testing.T) {
		if !strings.Contains(mSrc, "systray_tray_clicked();") {
			t.Error("systray_darwin.m 缺少对 systray_tray_clicked 的调用(AppDelegate trayClicked: 应转发到 Go 侧出口)")
		}

		// Go 侧出口与 Windows 侧 wndProc 点击分支对齐:非阻塞丢 goroutine,
		// 绝不在 AppKit 主线程做耗时工作;hook 为 nil 则忽略。2026-11-28
		// 原子化:读侧须经 loadPanelClickHook(AppKit 主线程 cgo 进来读,
		// M2 运行期写,裸读是数据竞争)。
		body := goFuncBody(t, nonwindowsSrc, "systray_tray_clicked")
		for _, want := range []string{
			"if fn := loadPanelClickHook(); fn != nil {",
			"go fn()",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("systray_tray_clicked 实现缺少 %q(必须与 Windows 侧同款:非阻塞起 goroutine):\n%s", want, body)
			}
		}
		if strings.Contains(body, "panelClickFn") {
			t.Errorf("systray_tray_clicked 直接引用 panelClickFn(须经 loadPanelClickHook 读——AppKit 主线程 cgo 进来读,M2 运行期写):\n%s", body)
		}
		assertAnchorAbove(t, nonwindowsSrc, "//export systray_tray_clicked", 8)
	})

	t.Run("托盘图标矩形", func(t *testing.T) {
		body := cFunctionBody(t, mSrc, "tray_rect")
		if !strings.Contains(body, "runInMainThread(@selector(fillTrayRect:)") {
			t.Fatalf("tray_rect 必须经 runInMainThread 封送主线程(窗口 frame 只能主线程读):\n%s", body)
		}

		// NSScreen 全局坐标系左下原点 → 全局左上原点(与 Windows 同基准):
		// 主屏 = [NSScreen screens] 首个(带菜单栏的那块,全局坐标零点在其左下角)。
		for _, want := range []string{
			"[NSScreen screens]",
			"frame.origin.y - frame.size.height", // screenHeight - (bottomY + h) 的换算形态
		} {
			if !strings.Contains(mSrc, want) {
				t.Errorf("systray_darwin.m 缺少 %q(缺左下原点→全局左上原点换算,面板锚定会偏一个屏高)", want)
			}
		}
		assertAnchorAbove(t, mSrc, "- (void)fillTrayRect:(NSMutableArray *)box {", 8)

		// Go 包装在 systray_darwin.go(TrayRect 签名是整数四元组,与 Windows 侧
		// 返回导出 Rect 的形态并存是既定设计)。
		for _, want := range []string{
			"func SetPanelMode(panel bool) {",
			"func TrayRect() (x, y, w, h int, ok bool) {",
		} {
			if !strings.Contains(goSrc, want) {
				t.Errorf("systray_darwin.go 缺少包装 %q", want)
			}
			assertAnchorAbove(t, goSrc, want, 8)
		}
	})

	t.Run("export三处分工", func(t *testing.T) {
		// 与既有 systray_menu_item_selected 的三处分工同款:Go 持 //export 实现,
		// systray.h 持 extern 声明,.m 持调用点。
		if !strings.Contains(nonwindowsSrc, "//export systray_tray_clicked") {
			t.Error("systray_nonwindows.go 缺少 //export systray_tray_clicked(既有 systray_menu_item_selected 的 //export 就在此文件,同款位置)")
		}
		if !strings.Contains(headerSrc, "extern void systray_tray_clicked") {
			t.Error("systray.h 缺少 systray_tray_clicked 的 extern 声明")
		}
		for _, want := range []string{
			"void set_panel_mode(bool panel)",
			"void tray_rect(int* x, int* y, int* w, int* h, bool* ok)",
		} {
			if !strings.Contains(headerSrc, want) {
				t.Errorf("systray.h 缺少声明 %q", want)
			}
		}
	})
}

// vendoredSystrayDarwinGoSource 读取内嵌 systray 的 darwin Go 包装层。
func vendoredSystrayDarwinGoSource(t *testing.T) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: 无法定位测试文件")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", "third_party", "systray", "systray_darwin.go"))
	if err != nil {
		t.Fatalf("读取内嵌 systray 的 darwin Go 包装层: %v", err)
	}
	return string(src)
}

// vendoredSystrayNonwindowsSource 读取内嵌 systray 的非 Windows cgo 桥接层
// (既有 systray_menu_item_selected 的 //export 在此文件)。
func vendoredSystrayNonwindowsSource(t *testing.T) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: 无法定位测试文件")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", "third_party", "systray", "systray_nonwindows.go"))
	if err != nil {
		t.Fatalf("读取内嵌 systray 的非 Windows 桥接层: %v", err)
	}
	return string(src)
}

// vendoredSystrayHeaderSource 读取内嵌 systray 的 C 头文件。
func vendoredSystrayHeaderSource(t *testing.T) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: 无法定位测试文件")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", "third_party", "systray", "systray.h"))
	if err != nil {
		t.Fatalf("读取内嵌 systray 的 C 头文件: %v", err)
	}
	return string(src)
}
