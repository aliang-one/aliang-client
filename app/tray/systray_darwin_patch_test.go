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
