//go:build darwin

package panelwin

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// panelwin_darwin.m 的结构断言(实现计划 Task 13;写法照
// app/tray/systray_darwin_patch_test.go 先例——本包不依赖那个包,辅助函数
// 就地复制)。钉住的是「壳的物理形态」四件事:
//
//  1. 失焦即退链路:windowDidResignKey → exit(0)(不激活则永不触发,所以
//     激活面 makeKeyAndOrderFront + NSApp activate 也一并钉住);
//  2. 毛玻璃归系统材质层:NSVisualEffectView(behindWindow)铺底 +
//     WKWebView drawsBackground=false(KVC 私有面)+ NSPanel opaque=false
//     ——前端 hosted 态只画半透明底(Task 7 评审固化),壳不透明底色会
//     盖住材质、验收必挂;
//  3. WebMessage 通道:addScriptMessageHandler 的 handler 名必须为
//     "panel"(与前端 messageHandlers.panel 对齐);quit→exit(60)、
//     open→NSWorkspace openURL、resize→顶边固定跟随改窗高、未知类型
//     默认忽略;
//  4. 内存纪律:每个可被 Go goroutine 直接调到的 C 入口,函数体整体包一层
//     @autoreleasepool(cgo 调用跑在调用者线程上,Go 运行时线程没有
//     autorelease pool——同款泄漏先例见 third_party/systray/systray_darwin.m
//     与 PATCH_NOTES);全文件零裸 autorelease 消息(ARC 下对象生命周期
//     确定性,面板进程存活即对象存活,不需要手工 autorelease);
//  5. 主线程钉定:panelwin_darwin.go 必须 runtime.LockOSThread——AppKit
//     UI 事件循环必须在主线程,不钉则 NSApp run 失效、失焦即退链死成僵尸;
//  6. 畸形 payload 守卫:type/kind 提取先过 isKindOfClass:[NSString class]
//     闸({"type":123} 这类形态走「未知忽略」,不得 unrecognized selector 崩)。

func TestPanelwinDarwinShellStructure(t *testing.T) {
	src := panelwinDarwinSource(t)

	t.Run("失焦即退与激活面", func(t *testing.T) {
		for _, want := range []string{
			"windowDidResignKey",                     // NSWindowDelegate 失焦回调
			"exit(60)",                               // quit 消息退出码 60
			"makeKeyAndOrderFront",                   // 不取焦点则失焦即退永不触发
			"activateIgnoringOtherApps:YES",          // 同上:程序化激活(12.0 可用,14 起弃用仍有效)
			"NSWindowStyleMaskNonactivatingPanel",    // 不抢宿主焦点
			"NSApplicationActivationPolicyAccessory", // 无 Dock 图标
		} {
			if !strings.Contains(src, want) {
				t.Errorf("panelwin_darwin.m 缺少 %q(失焦即退/激活链路不完整)", want)
			}
		}
		// 失焦自退 exit(0) 用正则锚:精确字面量对空白变体(exit ( 0 ))会
		// 误报缺失,钉的是「零参 exit 调用」这个形态本身。
		if !regexp.MustCompile(`exit\s*\(\s*0\s*\)`).MatchString(src) {
			t.Errorf("panelwin_darwin.m 缺少 exit(0)(失焦自退,退出码契约 0)")
		}
	})

	t.Run("毛玻璃材质层与透明底", func(t *testing.T) {
		for _, want := range []string{
			"NSVisualEffectView",                     // 系统材质层铺底
			"NSVisualEffectBlendingModeBehindWindow", // behindWindow 混合
			"drawsBackground",                        // WKWebView 私有 KVC 面
			"@NO",                                    // drawsBackground=false 的赋值形态
		} {
			if !strings.Contains(src, want) {
				t.Errorf("panelwin_darwin.m 缺少 %q(材质层/透明底形态不完整,毛玻璃验收必挂)", want)
			}
		}
		// NSPanel 不透明底关掉:正则锚容忍空白变体(opaque=NO / opaque = NO)。
		if !regexp.MustCompile(`opaque\s*=\s*NO`).MatchString(src) {
			t.Errorf("panelwin_darwin.m 缺少 opaque=NO(NSPanel 不透明底会盖住材质层,毛玻璃验收必挂)")
		}
		// drawsBackground 必须是 setValue:forKey: 的 KVC 面(plan 口径:
		// 私有面,公证探针随 Task 16 同批)——不许写成属性点语法误装公有。
		if !strings.Contains(src, `forKey:@"drawsBackground"`) {
			t.Errorf("panelwin_darwin.m 的 drawsBackground 必须经 setValue:forKey:@NO 的 KVC 形态(私有 API 口径)")
		}
		// 材质层 frame 必须是 contentView 本地坐标(2026-11-28 实机走查坐实:
		// 用 contentRectForFrameRect:panel.frame 的屏幕全局坐标 addSubview,
		// 材质层落到可视区外=全透明空壳窗口)。锚死本地坐标形态,拦回退;
		// 禁用形态按括号调用面匹配(注释里引述旧写法不算)。
		if strings.Contains(src, "[panel contentRectForFrameRect:panel.frame]") {
			t.Errorf("panelwin_darwin.m 材质层 frame 不得用屏幕全局坐标 [panel contentRectForFrameRect:panel.frame](addSubview 需 contentView 本地坐标,全局坐标=全透明空壳)")
		}
		if !strings.Contains(src, "NSMakeRect(0, 0, w, h)") {
			t.Errorf("panelwin_darwin.m 材质层 frame 缺少 contentView 本地坐标形态 NSMakeRect(0, 0, w, h)")
		}
	})

	t.Run("WebMessage 通道与协议冻结", func(t *testing.T) {
		// handler 名必须为 "panel":前端 messageHandlers.panel 逐字对齐,
		// 改名即静默断裂(postMessageHost 落回无宿主降级)。
		if !strings.Contains(src, `addScriptMessageHandler`) || !strings.Contains(src, `name:@"panel"`) {
			t.Errorf("panelwin_darwin.m 缺少 addScriptMessageHandler name:@\"panel\"(与前端 messageHandlers.panel 对齐)")
		}
		// open 分派:kind → 对应 URL,经 NSWorkspace 开默认浏览器后驻留。
		for _, want := range []string{
			`@"dashboard"`, // kind=dashboard → 仪表盘 URL
			`@"peer"`,      // kind=peer → 设备互访 URL
			"openURL",      // NSWorkspace openURL
		} {
			if !strings.Contains(src, want) {
				t.Errorf("panelwin_darwin.m 缺少 %q(open 分派不完整)", want)
			}
		}
		// 未知类型默认忽略:类型白名单(quit/open/resize,新增需双侧同批),
		// handler 必须有显式忽略分支(注释锚点),不得崩。
		if !strings.Contains(src, "未知类型") {
			t.Errorf("panelwin_darwin.m 缺少「未知类型」忽略分支(类型白名单:未知 type 必须忽略不崩)")
		}
	})

	t.Run("畸形 payload 类守卫", func(t *testing.T) {
		// {"type":123}/{"type":"open","kind":[]} 这类畸形 payload 会让
		// body[@"type"]/body[@"kind"] 拿到 NSNumber/NSArray,后续
		// isEqualToString: 直接 unrecognized selector 崩——协议注释是
		// 「未知形态忽略不崩」。type/kind 的每处提取(NSDictionary 直取与
		// JSON 字符串兜底解析,共两段×两键)都必须先过
		// isKindOfClass:[NSString class] 闸,非 NSString 一律走「未知忽略」。
		// 计数基线 = 6:4 处真守卫(两段×两键)+ 1 处预存的 body 整体判型
		// + 1 处协议注释里的字面量。锚 6 而非 4,是为了拆掉 1~2 个守卫
		// 仍会红——锚 4 时拆一个守卫恰好还绿,守卫会静默流失。
		if got := strings.Count(src, "isKindOfClass:[NSString class]"); got < 6 {
			t.Errorf("panelwin_darwin.m 的 type/kind 提取缺 isKindOfClass:[NSString class] 守卫(需 ≥6 处=4 真守卫+1 预存 body 判型+1 注释,现 %d 处):畸形 payload {\"type\":123} 会 unrecognized selector 崩",
				got)
		}
	})

	t.Run("导航白名单", func(t *testing.T) {
		// decidePolicyFor 仅放行回环白名单 origin(调研 §6.3 不变量)。
		for _, want := range []string{
			"decidePolicyForNavigationAction",
			"WKNavigationActionPolicyCancel",
		} {
			if !strings.Contains(src, want) {
				t.Errorf("panelwin_darwin.m 缺少 %q(导航白名单不完整)", want)
			}
		}
	})

	t.Run("autoreleasepool 纪律", func(t *testing.T) {
		defs := cFunctionDefs(src)
		if len(defs) == 0 {
			t.Fatal("未匹配到任何 C 函数定义,正则已失效")
		}
		for _, fn := range poolWrappedEntries {
			body := strings.TrimSpace(panelwinCFunctionBody(t, src, fn))
			const poolOpen = "@autoreleasepool {"
			if !strings.HasPrefix(body, poolOpen) {
				t.Errorf("%s 的函数体没有包在 @autoreleasepool 里(cgo 入口跑在 Go goroutine 线程上,无 pool 即泄漏):\n%s", fn, body)
				continue
			}
			if close := matchingBraceIdx(body, strings.Index(body, "{")); close != len(body)-1 {
				t.Errorf("%s 的 @autoreleasepool 只包住了函数体的一部分(pool 闭合于 %d,函数体长 %d):\n%s", fn, close, len(body), body)
			}
		}
		// 无裸 autorelease 创建点:ARC 下全文件不允许手写 autorelease 消息
		// ——手工 autorelease 只会引入对 pool 的隐性依赖;对象生命周期=
		// 进程存活(面板是叶子进程),显式管理无意义。对剥注释后的代码断言:
		// 注释里提到 autorelease(纪律说明本身)不算实现里有,否则误报。
		code := stripObjcComments(src)
		if idx := strings.Index(code, "autorelease]"); idx >= 0 {
			t.Errorf("panelwin_darwin.m 出现裸 autorelease 消息(ARC 纪律:不手写 autorelease),位置 %d 附近:\n%s",
				idx, lineAround(code, idx))
		}
	})

	t.Run("主线程钉定", func(t *testing.T) {
		// AppKit 强约束:UI 事件循环必须在主线程。包 init 必然在主 goroutine
		// 执行,runtime.LockOSThread 钉住后 cgo 的 C.panelwin_run 才跑在主
		// 线程——不钉则调度器可能把主 goroutine 迁走,NSApp run 失效,
		// 失焦即退整条链死成僵尸(非崩溃,launcher 崩溃计数看不见)。
		goSrc := panelwinDarwinGoSource(t)
		if !strings.Contains(goSrc, "LockOSThread") {
			t.Errorf("panelwin_darwin.go 缺少 runtime.LockOSThread(主 goroutine 未钉主线程,NSApp run 失效→失焦即退链死成僵尸)")
		}
	})

	t.Run("C 入口清单完备", func(t *testing.T) {
		// 每个顶格 C 函数定义都必须归类:会被 Go goroutine 直接调到的进
		// poolWrappedEntries(函数体整体包 @autoreleasepool);只被
		// pool 包住的入口内部调用的静态辅助进 noOwnPoolNeeded(同
		// systray_darwin_patch_test.go 的两清单归类思路)。
		got := cFunctionDefs(src)
		if !slices.Contains(got, "panelwin_run") {
			t.Errorf("panelwin_darwin.m 缺少 C 入口 panelwin_run")
		}
		for _, fn := range got {
			if slices.Contains(poolWrappedEntries, fn) || slices.Contains(noOwnPoolNeeded, fn) {
				continue
			}
			t.Errorf("panelwin_darwin.m 出现未归类的 C 函数 %q:请判断它是否会被 Go goroutine 直接调用并登记到对应清单", fn)
		}
	})
}

// TestPanelwinDarwinResizeFollow — resize 高度自适应链(2026-11-28 用户
// 拍板)的 .m 结构断言:height 过 NSNumber 类闸(畸形 payload 不得崩)、
// clamp [280, 顶边-8]、顶边固定向下生长的 setFrame 形态(topY - newH)。
// macOS 托盘恒菜单栏,resize 恒顶边固定;Windows 侧 side-aware 在
// panelwin_windows_struct_test.go 锚。
func TestPanelwinDarwinResizeFollow(t *testing.T) {
	src := panelwinDarwinSource(t)

	// ① resize 分派在册(类型白名单第三类型)。
	dispIdx := strings.Index(src, `@"resize"`)
	if dispIdx < 0 {
		t.Fatal("panelwin_darwin.m 缺 resize 分派(高度自适应链断裂):\n" + src)
	}

	// ② height 提取必须过 NSNumber 类闸(两段提取:NSDictionary 直取与
	//    JSON 字符串兜底解析;{"height":"abc"} 这类形态不得 unrecognized
	//    selector 崩)。
	if got := strings.Count(src, "isKindOfClass:[NSNumber class]"); got < 2 {
		t.Errorf("panelwin_darwin.m 的 height 提取缺 isKindOfClass:[NSNumber class] 守卫(需 ≥2 处=两段提取,现 %d 处):畸形 payload {\"height\":\"abc\"} 会崩", got)
	}

	// ③ 顶边固定 setFrame 形态:origin.y = 顶边 - 新高(全局左下原点),
	//    display:YES animate:NO(高度跟随即时生效,不做动画)。
	for _, want := range []string{
		"topY - newH",                // 顶边固定的 origin.y 形态
		"topY - 8",                   // 屏幕下缘留 8 的钳高
		"setFrame:NSMakeRect(f.origin.x, topY - newH, f.size.width, newH)",
		"animate:NO",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("panelwin_darwin.m 缺少 %q(resize 顶边固定跟随形态不完整)", want)
		}
	}

	// ④ 最低可用高 280 在册(防 height 畸形小值把窗压成一条线)。
	if !strings.Contains(src, "newH < 280") {
		t.Error("panelwin_darwin.m 缺少 newH < 280 最低可用高钳制(畸形 height 会把窗压成一条线)")
	}
}

// poolWrappedEntries — 会被 Go goroutine 所在线程直接调到的 C 入口:
// 函数体必须整体包一层 @autoreleasepool(结构测试逐个校验)。
var poolWrappedEntries = []string{"panelwin_run"}

// noOwnPoolNeeded — 不需要自持 pool 的静态辅助函数:只被已包 pool 的
// 入口内部调用,其 autoreleased 结果在调用方的 pool 里排空。
var noOwnPoolNeeded = []string{"originOfURL"}

// panelwinDarwinSource 读取本包的 ObjC 壳源码。
func panelwinDarwinSource(t *testing.T) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: 无法定位测试文件")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "panelwin_darwin.m"))
	if err != nil {
		t.Fatalf("读取 panelwin_darwin.m: %v", err)
	}
	return string(src)
}

// panelwinDarwinGoSource 读取本包的 Go cgo 包装层源码(主线程钉定断言用)。
func panelwinDarwinGoSource(t *testing.T) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: 无法定位测试文件")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "panelwin_darwin.go"))
	if err != nil {
		t.Fatalf("读取 panelwin_darwin.go: %v", err)
	}
	return string(src)
}

// stripObjcComments 剥掉 ObjC 源码的 // 行注释与 /* */ 块注释(字符串字面量
// 原样保留)——「实现里不得出现 X」类断言必须对剥注释后的代码做,否则注释里
// 提一句 X 就会误报。
func stripObjcComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	inLine, inBlock, inStr := false, false, false
	for i := 0; i < len(src); i++ {
		c := src[i]
		var next byte
		if i+1 < len(src) {
			next = src[i+1]
		}
		switch {
		case inLine:
			if c == '\n' {
				inLine = false
				b.WriteByte(c)
			}
		case inBlock:
			if c == '*' && next == '/' {
				inBlock = false
				i++
			}
		case inStr:
			b.WriteByte(c)
			switch {
			case c == '\\' && next != 0:
				b.WriteByte(next)
				i++
			case c == '"':
				inStr = false
			}
		case c == '/' && next == '/':
			inLine = true
			i++
		case c == '/' && next == '*':
			inBlock = true
			i++
		default:
			if c == '"' {
				inStr = true
			}
			b.WriteByte(c)
		}
	}
	return b.String()
}

// cFunctionDefs 列出顶格书写的 C 函数定义名(不含 ObjC 方法)。
func cFunctionDefs(src string) []string {
	def := regexp.MustCompile(`(?m)^[A-Za-z_][A-Za-z0-9_ *]*?\b([A-Za-z_][A-Za-z0-9_]*)\s*\([^;{}]*\)\s*\{`)

	var names []string
	for _, m := range def.FindAllStringSubmatch(src, -1) {
		names = append(names, m[1])
	}
	return names
}

// panelwinCFunctionBody 返回 name 对应 C 函数最外层花括号之间的正文。
func panelwinCFunctionBody(t *testing.T, src, name string) string {
	t.Helper()

	def := regexp.MustCompile(`(?m)^[A-Za-z_][A-Za-z0-9_ *]*?\b` + regexp.QuoteMeta(name) + `\s*\([^;{}]*\)\s*\{`)
	loc := def.FindStringIndex(src)
	if loc == nil {
		t.Fatalf("panelwin_darwin.m 里找不到 C 函数 %s 的定义", name)
	}
	close := matchingBraceIdx(src, loc[1]-1)
	if close < 0 {
		t.Fatalf("panelwin_darwin.m 里 C 函数 %s 的花括号不闭合", name)
	}
	return src[loc[1]:close]
}

// matchingBraceIdx 返回 s[open] 这个 '{' 对应的 '}' 的下标。
func matchingBraceIdx(s string, open int) int {
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

// lineAround 返回位置 idx 附近的行,用于失败信息定位。
func lineAround(src string, idx int) string {
	start := strings.LastIndexByte(src[:idx], '\n')
	if start < 0 {
		start = 0
	}
	end := strings.IndexByte(src[idx:], '\n')
	if end < 0 {
		end = len(src)
	} else {
		end += idx
	}
	return src[start:end]
}
