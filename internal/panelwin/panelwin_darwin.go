//go:build darwin

// panelwin_darwin.go — 托盘面板纯原生窗口壳,macOS 侧 cgo 包装层
// (实现计划 M2 Task 13)。ObjC 实现在 panelwin_darwin.m:NSPanel(不抢宿主
// 焦点)+ NSVisualEffectView 材质铺底 + WKWebView 透明底。
//
// 生命周期(2026-11-28 撤销方案 B「首次后常驻」,回归「点击才拉起+关闭
// 即退」——常驻的隐藏 WKWebView 窗口在该 macOS 上三连败:orderOut 后
// makeKey/orderFrontRegardless 被防抢焦点策略静默忽略、重显后 WebKit 内容
// 不回复绘制、makeKey 抢用户焦点被夺回后 resignKey 自动再隐藏;残留边框随
// 进程退出彻底消失)。本文件承担两件壳内职责——
//  1. close 请求信号:companion 点图标(有子在册)发 SIGUSR1 → handler
//     goroutine → exit(0) 正常关闭(SIGUSR1 专用契约见 panel_spawn_darwin.go
//     投递侧;壳内不注册 handler 则 SIGUSR1 默认终止=信号死亡 exit −1,
//     launcher 误计崩溃,故 handler 是契约承载点,不可删)。
//  2. 孤儿自检:companion 死后面板被 launchd 收养(ppid==1),后台
//     goroutine 每 5s 检查 → exit 0(Windows 侧无此看门狗,Job 收割)。
//
// 退出码契约:0=正常关闭(点面板外/失焦/close 请求/孤儿自检)、60=quit、
// 其余=崩溃。
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
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"unsafe"

	"aliang.one/nursorgate/common/logger"
)

// AppKit 强约束:UI 事件循环必须在主线程。包 init 必然在主 goroutine 执行,
// 钉住后 cgo 的 C.panelwin_run 跑在主线程——不钉则调度器可能把主 goroutine
// 迁走,NSApp run 失效→windowDidResignKey 永不触发→失焦即退链死成僵尸
// (非崩溃,launcher 崩溃计数看不见)。与 third_party/systray/systray.go、
// jchv/go-webview2(pkg/edge,Windows 侧)双先例同构。
func init() { runtime.LockOSThread() }

// startPanelCloseHandler SIGUSR1 关闭请求接收:companion 点图标(有子在册)
// 发 SIGUSR1(app/tray panelToggleSender 投递侧),壳内收到即 exit(0) 正常
// 关闭——窗口随进程退出消失(「关闭即退」),companion 记正常关闭零惩罚。
// ⚠️ 只在面板进程的 Run 路径注册——包级 init 会波及同二进制的 companion
// (它会静默吞掉 SIGUSR1),故不放 init。不注册则 SIGUSR1 默认终止=信号死亡
// exit −1,launcher 误计崩溃——本 handler 是「close 请求=exit(0)」契约的
// 承载点。注册前的窗口期(fork→Go runtime 启动到 Run 进入,毫秒级)内到达
// 的 SIGUSR1 仍是默认终止行为(信号死亡 exit −1 → launcher 记崩溃),窗口
// 极窄,接受。
func startPanelCloseHandler() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	go func() {
		for range ch {
			logger.Info("panelwin: 收到 SIGUSR1 关闭请求,正常退出(退出码 0)")
			os.Exit(ExitFocusLoss)
		}
	}()
}

// startPanelOrphanWatchdog 孤儿自检起跑:companion 死后 ppid==1 → exit 0
// (正常关闭,launcher 零惩罚)。生产形态 stop 传 nil(goroutine 与进程
// 同寿)。
func startPanelOrphanWatchdog() {
	go panelOrphanWatchdog(nil, os.Getppid, os.Exit, panelOrphanWatchInterval)
}

type nativeHost struct{}

// NewHost 返回 macOS 壳实现。
func NewHost() Host { return nativeHost{} }

// Run 阻塞展示面板。正常路径不返回——点面板外/失焦 exit(0)(.m 内直接
// 终止)、close 请求 exit(0)、孤儿自检 exit(0)、页面 quit 消息 exit(60)
// 都直接终止进程。逃逸到这里的返回只可能是 NSApp run 的异常形态,按 0
// 返回(0=正常关闭,launcher 零惩罚)。起跑顺序:close 请求 handler 与孤儿
// 自检先于 NSApp run 注册——点击后的 close 请求与孤儿收割都必须在首帧前
// 就位。
func (nativeHost) Run(opts Options) int {
	startPanelCloseHandler()
	startPanelOrphanWatchdog()

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
