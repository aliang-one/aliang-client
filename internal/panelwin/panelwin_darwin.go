//go:build darwin

// panelwin_darwin.go — 托盘面板纯原生窗口壳,macOS 侧 cgo 包装层
// (实现计划 M2 Task 13)。ObjC 实现在 panelwin_darwin.m:NSPanel(不抢宿主
// 焦点)+ NSVisualEffectView 材质铺底 + WKWebView 透明底。
//
// 2026-11-28 用户拍板方案 B「首次后常驻」:进程常驻,本文件承担三件壳内
// 职责——
//  1. toggle 信号:signal.Notify(SIGUSR1) → handler goroutine → cgo
//     panelwin_toggle()(ObjC 侧 dispatch_async 派发主线程完成可见⇄隐藏,
//     信号 handler goroutine 不直接碰 UI)。SIGUSR1 专用契约见
//     panel_spawn_darwin.go(companion 投递侧)。
//  2. 可见性回传:ObjC 经导出回调 panelwinGoVisibilityChanged 喂
//     panelIdle(panelwin_idle.go),Go 侧凭此管理 10 分钟空闲回收计时
//     (隐藏启动/重置、显示停止;超时 exit 0=正常关闭零惩罚)。
//  3. 孤儿自检:companion 死后面板被 launchd 收养(ppid==1),后台
//     goroutine 每 5s 检查 → exit 0(Windows 侧无此看门狗,Job 收割)。
//
// 退出码契约:0=正常关闭(空闲回收/孤儿自检)、60=quit、其余=崩溃。
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
void panelwin_toggle(void);
extern void panelwinGoVisibilityChanged(int hidden);
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
// 迁走,NSApp run 失效→windowDidResignKey 永不触发→失焦=隐藏整条链死成
// 僵尸(非崩溃,launcher 崩溃计数看不见)。与 third_party/systray/systray.go、
// jchv/go-webview2(pkg/edge,Windows 侧)双先例同构。
func init() { runtime.LockOSThread() }

// panelwinGoVisibilityChanged 可见性回传(ObjC → Go,方案 B):panelSetHidden
// 每次真实改口时经 //export 符号调用(AppKit 主线程,非阻塞喂控制器)。
// hidden 非 0 = 刚隐藏(启动/重置空闲计时),0 = 刚显示(停止计时)。
//
//export panelwinGoVisibilityChanged
func panelwinGoVisibilityChanged(hidden C.int) {
	panelIdle.SetHidden(hidden != 0)
}

// startPanelToggleHandler SIGUSR1 toggle 接收(方案 B):注册信号 handler +
// 后台 goroutine,信号到达 → cgo panelwin_toggle()(ObjC 内部派发主线程,
// 本 goroutine 不碰 UI)。⚠️ 只在面板进程的 Run 路径注册——包级 init 会波及
// 同二进制的 companion(它会静默吞掉 SIGUSR1),故不放 init。注册前的窗口
// 期(fork→Go runtime 启动到 Run 进入,毫秒级)内到达的 SIGUSR1 仍是默认
// 终止行为(信号死亡 exit −1 → launcher 记崩溃),窗口极窄,接受。
func startPanelToggleHandler() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	go func() {
		for range ch {
			logger.Debug("panelwin: SIGUSR1 toggle")
			C.panelwin_toggle()
		}
	}()
}

// startPanelOrphanWatchdog 孤儿自检起跑(方案 B §4,见 panelwin_idle.go):
// companion 死后 ppid==1 → exit 0(正常关闭,launcher 零惩罚)。生产形态
// stop 传 nil(goroutine 与进程同寿)。
func startPanelOrphanWatchdog() {
	go panelOrphanWatchdog(nil, os.Getppid, os.Exit, panelOrphanWatchInterval)
}

type nativeHost struct{}

// NewHost 返回 macOS 壳实现。
func NewHost() Host { return nativeHost{} }

// Run 阻塞展示面板。正常路径不返回——空闲回收 exit(0)、孤儿自检 exit(0)、
// 页面 quit 消息 exit(60) 都直接终止进程;失焦=隐藏不再产生退出(方案 B)。
// 逃逸到这里的返回只可能是 NSApp run 的异常形态,按 0 返回(0=正常关闭,
// launcher 零惩罚)。起跑顺序:toggle handler 与孤儿自检先于 NSApp run 注册
// ——常驻形态下二者都必须在首帧前就位。
func (nativeHost) Run(opts Options) int {
	startPanelToggleHandler()
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
