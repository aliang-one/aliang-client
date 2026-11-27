//go:build darwin

// panelwin_darwin.go — 托盘面板纯原生窗口壳,macOS 侧 cgo 包装层
// (实现计划 M2 Task 13)。ObjC 实现在 panelwin_darwin.m:NSPanel(不抢宿主
// 焦点)+ NSVisualEffectView 材质铺底 + WKWebView 透明底;失焦即退与
// quit 消息在 .m 里经 exit() 直接终止进程(叶子进程,无需返回路径)。
//
// 本包只允许进入 darwin/windows 构建与 ./cmd/panel 的依赖闭包;
// aliang 主命令在 Linux 的闭包由 cmd/linux_closure_test.go 把守。
package panelwin

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework WebKit

#include <stdlib.h>

void panelwin_run(const char* url, int x, int y, int w, int h,
                  const char* dashboardURL, const char* peerURL);
*/
import "C"

import (
	"runtime"
	"unsafe"
)

// AppKit 强约束:UI 事件循环必须在主线程。包 init 必然在主 goroutine 执行,
// 钉住后 cgo 的 C.panelwin_run 跑在主线程——不钉则调度器可能把主 goroutine
// 迁走,NSApp run 失效→windowDidResignKey 永不触发→失焦即退整条链死成僵尸
// (非崩溃,launcher 崩溃计数看不见)。与 third_party/systray/systray.go、
// jchv/go-webview2(pkg/edge,Windows 侧)双先例同构。
func init() { runtime.LockOSThread() }

type nativeHost struct{}

// NewHost 返回 macOS 壳实现。
func NewHost() Host { return nativeHost{} }

// Run 阻塞展示面板。正常路径不返回——失焦自退 exit(0)、页面 quit 消息
// exit(60) 都在 .m 内直接终止进程;逃逸到这里的返回只可能是 NSApp run 的
// 异常形态,按失焦退出码返回(0=正常关闭,launcher 零惩罚)。
func (nativeHost) Run(opts Options) int {
	url := C.CString(opts.URL)
	defer C.free(unsafe.Pointer(url))
	dash := C.CString(opts.OpenDashboard)
	defer C.free(unsafe.Pointer(dash))
	peer := C.CString(opts.OpenPeer)
	defer C.free(unsafe.Pointer(peer))

	C.panelwin_run(url, C.int(opts.X), C.int(opts.Y),
		C.int(opts.PanelW), C.int(opts.PanelH), dash, peer)
	return ExitFocusLoss
}
