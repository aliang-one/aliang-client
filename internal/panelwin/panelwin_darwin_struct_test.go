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
//  1. 生命周期链路(2026-11-28 撤销方案 B「首次后常驻」,回归「点击才拉起
//     +关闭即退」):点面板外(全局鼠标监听)/失焦(windowDidResignKey)
//     → exit(0) 正常关闭;resignKey 带 everKeyed 守卫(spawn 期 key 抖动
//     防误伤);初始上屏 orderFrontRegardless 且【不 makeKey】(失败模式③:
//     makeKey 抢用户焦点被夺回后自动再隐藏);
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
//     闸({"type":123} 这类形态走「未知忽略」,不得 unrecognized selector 崩);
//  7. 原生圆角(2026-11-28 用户反馈):contentView layer wantsLayer/
//     cornerRadius/masksToBounds 三件套——CSS 圆角只作用内层 .panel div,
//     被窗口边界裁成方角;半径 16 与前端 --panel-radius 同步
//     (app/website/src/panel/theme.css),两处改动需同批。

func TestPanelwinDarwinShellStructure(t *testing.T) {
	src := panelwinDarwinSource(t)

	t.Run("关闭即退生命周期与激活面", func(t *testing.T) {
		for _, want := range []string{
			"windowDidResignKey",                     // NSWindowDelegate 失焦回调
			"exit(60)",                               // quit 消息退出码 60
			"orderFrontRegardless",                   // 初始上屏(新窗口首显实证可用;不 makeKey)
			"NSWindowStyleMaskNonactivatingPanel",    // 不抢宿主焦点
			"NSApplicationActivationPolicyAccessory", // 无 Dock 图标
		} {
			if !strings.Contains(src, want) {
				t.Errorf("panelwin_darwin.m 缺少 %q(关闭即退生命周期/激活链路不完整)", want)
			}
		}
		// 反向锚:初始上屏【不 makeKey】——失败模式③(撤销方案 B 实机三连败
		// 之一):显示路径 makeKey 抢用户焦点→被夺回→0.12s 内 resignKey 自动
		// 再隐藏。全文件(剥注释后)不得出现 makeKeyAndOrderFront。
		code := stripObjcComments(src)
		if strings.Contains(code, "makeKeyAndOrderFront") {
			t.Errorf("panelwin_darwin.m 出现 makeKeyAndOrderFront(初始上屏不得 makeKey:抢用户焦点会被夺回,resignKey 自动再隐藏=失败模式③)")
		}
	})

	t.Run("关闭即退:点外/失焦 exit(0)+everKeyed 守卫", func(t *testing.T) {
		// resignKey → exit(0)(失焦即退),带 everKeyed 守卫(从未成为 key
		// 不退出——spawn 期 key 抖动防误伤)。方法体截到下一个 ObjC 方法
		// 声明为止,对剥注释后的代码断言(注释里的历史引述不算)。
		resignBody := objcMethodBody(src, "windowDidResignKey:")
		resignCode := stripObjcComments(resignBody)
		if !strings.Contains(resignCode, "everKeyed") {
			t.Errorf("panelwin_darwin.m 的 resignKey 缺 everKeyed 守卫(spawn 期 key 抖动会误伤:面板一出生就退)\n%s", resignBody)
		}
		if !regexp.MustCompile(`exit\s*\(\s*0\s*\)`).MatchString(resignCode) {
			t.Errorf("panelwin_darwin.m 的 resignKey 未 exit(0)(失焦即退链断裂):\n%s", resignBody)
		}
		// 反向锚:隐藏驻留已废除(撤销方案 B)——resignKey 不得再走任何
		// 隐藏/驻留动作。
		for _, banned := range []string{"panelSetHidden", "orderOut", "alphaValue", "ignoresMouseEvents"} {
			if strings.Contains(resignCode, banned) {
				t.Errorf("panelwin_darwin.m 的 resignKey 仍在隐藏驻留(%q;撤销方案 B:失焦=exit 0 正常关闭):\n%s", banned, resignBody)
			}
		}

		// 点面板外即收:全局鼠标监听三件套(鼠标事件无需 TCC;菜单栏带排除
		// ——托盘图标自身的点击是 companion 的手势,不能被本监听抢先关闭),
		// 且监听体内 exit(0) 正常关闭。
		// ⚠️ 菜单栏带方向(2026-11-28 评审修):macOS 全局坐标左下原点,菜单栏
		// 在顶部,判据必须是 loc.y > 主屏高-40;此前锚的 `loc.y < 40` 排除的
		// 是屏幕底部(方向反了),反向锚不得回归。
		for _, want := range []string{
			"addGlobalMonitorForEventsMatchingMask",
			"NSEventMaskLeftMouseDown",
			"loc.y > primaryH - 40",
		} {
			if !strings.Contains(src, want) {
				t.Errorf("panelwin_darwin.m 缺少 %q(点外部关闭链不完整)", want)
			}
		}
		if strings.Contains(stripObjcComments(src), "loc.y < 40") {
			t.Error("panelwin_darwin.m 菜单栏带判据退回 `loc.y < 40`(macOS 全局坐标左下原点,这排除的是屏幕底部,方向反了)")
		}
		monIdx := strings.Index(src, "addGlobalMonitorForEventsMatchingMask")
		if monIdx < 0 {
			t.Fatal("panelwin_darwin.m 缺全局鼠标监听")
		}
		monBody := src[monIdx:]
		if end := strings.Index(monBody, "\n  }];"); end >= 0 {
			monBody = monBody[:end]
		}
		if !regexp.MustCompile(`exit\s*\(\s*0\s*\)`).MatchString(stripObjcComments(monBody)) {
			t.Errorf("panelwin_darwin.m 的点面板外监听未 exit(0)(点外即退链断裂):\n%s", monBody)
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

	t.Run("原生圆角(窗口级)", func(t *testing.T) {
		// 2026-11-28 用户反馈「外边框始终方方正正」:圆角必须在壳层生效——
		// contentView 打 layer 圆角+裁边,而不是只靠前端 .panel 的 CSS
		// border-radius(它贴满窗口后被窗口边界裁成方角)。三件套缺一即回退:
		// 缺 wantsLayer → layer 不落地;缺 cornerRadius → 无圆角;
		// 缺 masksToBounds → 子视图(材质层/WebView)溢出方角。
		for _, want := range []string{
			"panel.contentView.wantsLayer = YES",
			// 半径 16 与前端 theme.css --panel-radius 同步(app/website/src/
			// panel/theme.css),改动需两处同批——锚字面量拦单侧漂移。
			"panel.contentView.layer.cornerRadius = 16",
			"panel.contentView.layer.masksToBounds = YES",
		} {
			if !strings.Contains(src, want) {
				t.Errorf("panelwin_darwin.m 缺少 %q(原生圆角三件套不完整,窗口外边框回退方角)", want)
			}
		}
	})

	t.Run("WebMessage 通道与协议冻结", func(t *testing.T) {
		// handler 名必须为 "panel":前端 messageHandlers.panel 逐字对齐,
		// 改名即静默断裂(postMessageHost 落回无宿主降级)。
		if !strings.Contains(src, `addScriptMessageHandler`) || !strings.Contains(src, `name:@"panel"`) {
			t.Errorf("panelwin_darwin.m 缺少 addScriptMessageHandler name:@\"panel\"(与前端 messageHandlers.panel 对齐)")
		}
		// open 分派:kind → 对应 URL,经 NSWorkspace 开默认浏览器。
		for _, want := range []string{
			`@"dashboard"`, // kind=dashboard → 仪表盘 URL
			`@"peer"`,      // kind=peer → 设备互访 URL
			"openURL",      // NSWorkspace openURL
		} {
			if !strings.Contains(src, want) {
				t.Errorf("panelwin_darwin.m 缺少 %q(open 分派不完整)", want)
			}
		}
		// open 后即退(2026-11-28 用户走查「点仪表盘开了浏览器,面板不消失」):
		// 此前刻意驻留,指望浏览器激活→面板 resignKey 走失焦即退链——但
		// Accessory 进程 + NonactivatingPanel 点击内部不成为 active app,
		// openURL 拉浏览器到前台时面板不是 key,resignKey 永不发,面板悬着。
		// 外部动作已完成,openURL 之后必须 exit(0)(正常关闭,launcher 零惩罚);
		// 「open 后驻留」不得回归。openURL 与 exit(0) 必须同在 open 分支内
		// (exit 不得提到 openURL 之前——那会在浏览器还没拉起时就把面板关掉,
		// 虽然对用户感知无差,但 openURL 从已 exit 的进程发出去属竞态)。
		openBody := objcMethodBody(src, "didReceiveScriptMessage:")
		openIdx := strings.Index(openBody, `isEqualToString:@"open"`)
		if openIdx < 0 {
			t.Fatalf("panelwin_darwin.m 缺 open 分支")
		}
		openBranch := openBody[openIdx:]
		if next := strings.Index(openBranch, `isEqualToString:@"resize"`); next >= 0 {
			openBranch = openBranch[:next]
		}
		urlIdx := strings.Index(openBranch, "openURL")
		exitIdx := strings.Index(openBranch, "exit(0)")
		if urlIdx < 0 {
			t.Fatalf("panelwin_darwin.m open 分支缺 openURL(分派链断裂):\n%s", openBranch)
		}
		if exitIdx < 0 {
			t.Fatalf("panelwin_darwin.m open 分支缺 exit(0)(open 后驻留回归——面板不会消失):\n%s", openBranch)
		}
		if exitIdx < urlIdx {
			t.Fatalf("panelwin_darwin.m open 分支 exit(0) 在 openURL 之前(浏览器还没拉起就关面板):\n%s", openBranch)
		}
		// 失败路径(目标 URL 解析为 nil / 未知 kind)必须【不】exit:没有
		// 外部动作发生,面板驻留让用户换键重试。锚定:openURL 调用包在
		// if (url) 闸内,exit(0) 也在同一闸内(if (target) 块)。
		if !strings.Contains(openBranch, "if (url)") {
			t.Errorf("panelwin_darwin.m open 分支缺 if (url) 闸(解析失败不应 openURL/exit):\n%s", openBranch)
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
			t.Errorf("panelwin_darwin.go 缺少 runtime.LockOSThread(主 goroutine 未钉主线程,NSApp run 失效→失焦链死成僵尸)")
		}
	})

	t.Run("Go 侧接线:close 请求信号+孤儿自检", func(t *testing.T) {
		goSrc := panelwinDarwinGoSource(t)
		for _, want := range []string{
			"signal.Notify(ch, syscall.SIGUSR1)",           // close 请求接收(SIGUSR1 专用契约见 app/tray 投递侧)
			"os.Exit(ExitFocusLoss)",                       // 收到请求即 exit(0) 正常关闭(handler 是「close 请求=exit(0)」契约承载点)
			"panelOrphanWatchdog(nil, os.Getppid, os.Exit", // 孤儿自检起跑(ppid==1 → exit 0)
			"startPanelCloseHandler()",                     // Run 路径起跑(close 请求首帧前就位)
			"startPanelOrphanWatchdog()",
		} {
			if !strings.Contains(goSrc, want) {
				t.Errorf("panelwin_darwin.go 缺少 %q(关闭即退 Go 侧接线断裂)", want)
			}
		}
		// 反向锚:toggle 空闲回收链已删——Go 侧不得再引用已废除的符号。
		for _, banned := range []string{"panelwin_toggle", "panelwinGoVisibilityChanged", "panelIdle", "ALIANG_PANEL_NO_ORPHAN"} {
			if strings.Contains(goSrc, banned) {
				t.Errorf("panelwin_darwin.go 仍引用已废除的方案 B 符号 %q(空闲回收/可见性回传/调试开关均已随撤销批删除)", banned)
			}
		}
		// 信号 handler 只能在 Run(面板进程路径)注册:包级 init 会波及同
		// 二进制的 companion(静默吞 SIGUSR1)。只扫 init 函数体(从首个
		// '{' 到配对 '}',防止单行 init 之后的下一个函数体被误并进来),
		// 不波及文件尾部 Run 路径的合法注册。
		if idx := strings.Index(goSrc, "func init()"); idx >= 0 {
			open := strings.Index(goSrc[idx:], "{")
			if open < 0 {
				t.Fatal("panelwin_darwin.go 的 init() 缺函数体")
			}
			close := matchingGoBrace(goSrc[idx+open:])
			initBody := goSrc[idx : idx+open+close+1]
			if strings.Contains(initBody, "signal.Notify") {
				t.Errorf("panelwin_darwin.go 的 init() 里注册了 SIGUSR1 handler(会波及同二进制 companion,须移到 Run 路径)")
			}
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
	//    display:YES animate:NO(高度跟随即时生效,不做动画);钳高上限
	//    非正(maxH=topY-8 ≤ 0,托盘在负坐标屏)时保持原矩形不动(Minor-5)。
	for _, want := range []string{
		"topY - newH", // 顶边固定的 origin.y 形态
		"topY - 8",    // 屏幕下缘留 8 的钳高
		"setFrame:NSMakeRect(f.origin.x, topY - newH, f.size.width, newH)",
		"animate:NO",
		"maxH <= 0", // newH 钳后非正守卫(负坐标屏形态不压窗,NSLog 留痕)
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

// TestPanelwinDarwinFirstFrameShow — 首帧尺寸修正 Part 1(2026-11-28 用户
// 反馈「点击瞬间出现大空白区域再缩到目标尺寸」)的 .m 结构断言,三件套:
//
//  1. awaitingFirstResize 在册:窗口创建后【不上屏】,置位等待;
//  2. 首次 resize 到位才真正上屏(高度应用之后,首帧即正确尺寸);
//  3. 1.2s 兜底:页面加载失败/resize 未到时按当前尺寸上屏(面板不能永远
//     不出现)。
//
// 反向锚:launch 方法体不得再保留 150ms 早屏 dispatch_after(orderFrontRegardless)
// ——它把缺省 560 高的空窗先闪出来,正是本次要消灭的形态(verifyOnscreenRetry
// 内的 0.15 重试间隔是另一码事,不受本锚约束)。
func TestPanelwinDarwinFirstFrameShow(t *testing.T) {
	src := panelwinDarwinSource(t)

	// ① awaitingFirstResize 在册:属性声明 + 启动置位。
	if !strings.Contains(src, "@property (assign) BOOL awaitingFirstResize;") {
		t.Fatal("panelwin_darwin.m 缺 awaitingFirstResize 属性(首次上屏等待位不存在,首帧尺寸闪变回归)")
	}
	launchBody := objcMethodBody(src, "- (void)applicationDidFinishLaunching:")
	if !strings.Contains(launchBody, "awaitingFirstResize = YES") {
		t.Fatalf("panelwin_darwin.m 启动未置 awaitingFirstResize(窗口会带着缺省 560 高直接上屏):\n%s", launchBody)
	}

	// ② 首次 resize 到位才上屏:resize 分派内调 showIfAwaitingFirstResize,
	//    且在高度应用(applyFirstResize 锚)之后;helper 是唯一上屏入口,
	//    内含 orderFrontRegardless + 合成自检重试。
	resizeBody := objcMethodBody(src, `@"resize"`)
	if !strings.Contains(resizeBody, "showIfAwaitingFirstResize") {
		t.Fatalf("panelwin_darwin.m 的 resize 分派缺首次上屏调用(高度应用后才上屏的链路断裂):\n%s", resizeBody)
	}
	if !strings.Contains(resizeBody, "panelwinGoHeightChanged") {
		t.Fatalf("panelwin_darwin.m 的 resize 分派缺 panelwinGoHeightChanged 落盘回调(高度持久化链断裂):\n%s", resizeBody)
	}
	// 实现体锚用带花括号的签名形态(@interface 声明与文件头注释里都有
	// 裸名出现,objcMethodBody 从首个命中截断,裸名会截错位置)。
	showBody := objcMethodBody(src, "- (void)showIfAwaitingFirstResize {")
	for _, want := range []string{
		"awaitingFirstResize = NO", // 清位:后续 resize/兜底零操作
		"orderFrontRegardless",     // 上屏(不 makeKey)
		"verifyOnscreenRetry:1",    // 合成自检重试挂到真正上屏之后
	} {
		if !strings.Contains(showBody, want) {
			t.Fatalf("panelwin_darwin.m 的 showIfAwaitingFirstResize 缺 %q(首次上屏入口不完整):\n%s", want, showBody)
		}
	}

	// ③ 1.2s 兜底:dispatch_after 1.2s 检查等待位后兜底上屏(页面加载失败/
	//    resize 未到形态下面板不能永远不出现)。
	if !strings.Contains(launchBody, "1.2 * NSEC_PER_SEC") {
		t.Fatalf("panelwin_darwin.m 缺 1.2s 兜底 dispatch_after(resize 未到时面板永不出现):\n%s", launchBody)
	}
	if !strings.Contains(launchBody, "showIfAwaitingFirstResize") {
		t.Fatalf("panelwin_darwin.m 的 1.2s 兜底未走 showIfAwaitingFirstResize(兜底路径绕过唯一上屏入口):\n%s", launchBody)
	}

	// ④ 反向锚:launch 方法体不得再保留 150ms 早屏——窗口带着缺省高直接
	//    上屏,正是「点击瞬间大空白闪一下」的根源。对剥注释后的方法体断言
	//    (注释里的机制说明不算实现)。
	launchCode := stripObjcComments(launchBody)
	if strings.Contains(launchCode, "0.15") {
		t.Fatalf("panelwin_darwin.m 启动路径仍有 150ms 早屏(0.15)——首帧会先闪缺省 560 高的空白:\n%s", launchCode)
	}
	// 上屏动作必须只发生在 showIfAwaitingFirstResize:launch 方法体(剥注释)
	// 不得直接 orderFrontRegardless。
	if strings.Contains(launchCode, "orderFrontRegardless") {
		t.Fatalf("panelwin_darwin.m 启动路径直接 orderFrontRegardless(首帧尺寸闪变回归,上屏须等首次 resize 到位):\n%s", launchCode)
	}
}

// poolWrappedEntries — 会被 Go goroutine 所在线程直接调到的 C 入口:
// 函数体必须整体包一层 @autoreleasepool(结构测试逐个校验)。panelwin_run
// 是唯一 C 入口(close 请求不再经 cgo——handler goroutine 直接 exit(0),
// 无 UI 操作面)。
var poolWrappedEntries = []string{"panelwin_run"}

// noOwnPoolNeeded — 不需要自持 pool 的静态辅助函数:只被已包 pool 的
// 入口内部调用,其 autoreleased 结果在调用方的 pool 里排空。
var noOwnPoolNeeded = []string{"originOfURL"}

// objcMethodBody 返回 name 对应 ObjC 方法的正文(从方法声明起截到下一个
// ObjC 方法声明为止;文件尾无下一方法时截到末尾)。
func objcMethodBody(src, name string) string {
	idx := strings.Index(src, name)
	if idx < 0 {
		return ""
	}
	body := src[idx:]
	if next := strings.Index(src[idx:], "\n- ("); next >= 0 {
		body = src[idx : idx+next]
	}
	return body
}

// matchingGoBrace 返回 s 开头 '{' 对配对 '}' 的偏移(花括号不闭合时返回 -1;
// Go 源码断言用,与 ObjC 版 matchingBraceIdx 同构但下标语义不同)。
func matchingGoBrace(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
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
