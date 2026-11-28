//go:build darwin

package tray

import (
	"fmt"
	"os/exec"
	"syscall"

	"aliang.one/nursorgate/common/logger"
	"github.com/getlantern/systray"
)

// spawnPanelProcess darwin 面板 spawn shim:exec 起自身可执行文件的 panel
// 子命令(os.Executable 保证同二进制,cobra 子命令见 cmd/panel.go)。
//
// ⚠️ macOS 无收割机制(计划评审次#13):不持 Job 等价物,companion 先退时
// 面板进程成为孤儿、继续存活——依赖壳内孤儿自检退出(panelwin 包每 5s 查
// os.Getppid()==1(被 launchd 收养)→ exit 0,launcher 记正常关闭零惩罚)。
// 这是计划拍板的可接受形态,不是缺陷;面板窗口无 Dock 图标(NSPanel + 非
// regular 激活形态),孤儿态对用户不可见。Windows 侧的 KILL_ON_JOB_CLOSE
// 在本平台无等价物,不要照抄。
//
// 现取现用契约:opts 由 launcher 每次 spawn 前经 opts 口现取(panelSpawnOpts
// → readPanelTrayAnchor),本 shim 不缓存任何锚定状态。
func spawnPanelProcess(opts SpawnOpts) (ChildProcess, error) {
	exe, err := panelExecutable()
	if err != nil {
		return nil, fmt.Errorf("os.Executable: %w", err)
	}
	cmd := exec.Command(exe, panelSpawnArgs(opts)...)
	child, err := startPanelChild(cmd)
	if err != nil {
		return nil, err
	}
	logger.Debug(fmt.Sprintf("panel process spawned (pid=%d, side=%s, anchor=(%d,%d))",
		cmd.Process.Pid, opts.Side, opts.AnchorX, opts.AnchorY))
	return child, nil
}

// panelToggleSender darwin toggle 投递(方案 B「首次后常驻」):对存活面板
// 子进程发 SIGUSR1,壳内(signal.Notify→handler goroutine→cgo toggle)完成
// 可见⇄隐藏。发送失败(进程已死,kill 返回 ESRCH)→ launcher 按无子处理
// 回退 spawn。
//
// ⚠️ SIGUSR1 专用于面板 toggle,其他组件不得占用(壳内 signal.Notify 接管
// 后默认终止行为被替换;companion 自身进程不注册本信号,不影响托盘侧)。
// 已知边界:标准信号不排队,µs 级合并窗口内连续两次点击可能丢一次 toggle
// (用户感知=点一次没反应,再点即恢复;launcher 不跟踪可见性,零状态损坏,
// 架构上刻意容忍,2026-11-28 架构评审定谳)。
var panelToggleSender = func(pid int) error {
	return syscall.Kill(pid, syscall.SIGUSR1)
}

// readPanelTrayAnchor darwin 锚定采样:AppKit 点(逻辑)坐标、全局左上原点,
// 直传(无 DPI 换算——Retina 由 AppKit 处理,两侧 TrayRect 语义对应、单位
// 随平台,见 systray_darwin.go)。macOS 无 GetCursorPos 等价 Go 可达 API
// (systray 底座未暴露),HasCursor 恒 false——判废失败退中性顶部锚;主屏
// 尺寸不可得,ScreenW/H 恒 0(跳过尺寸判废,形态判废仍生效)。
func readPanelTrayAnchor() panelAnchorSample {
	x, y, w, h, ok := systray.TrayRect()
	return panelAnchorSample{AX: x, AY: y, W: w, H: h, OK: ok}
}
