//go:build windows

package agentcore

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

// toolCommandWaitDelay 见 exec_unix.go 的同名常量。
const toolCommandWaitDelay = 3 * time.Second

// newToolCommandContext 构造工具执行用的命令，返回（cmd, tree）。
//
// 2026-11-28 起接入 Job Object 树杀（proctree_windows.go）：此前 Windows 只有
// WaitDelay 兜底——shell 被杀后孙进程仍握着继承来的 stdout 管道，复合命令
// （`a; b | c`）与后台化后代（`cmd &`）成为孤儿，与 POSIX 侧 2026-11-01 事故
// 同形态。三层纪律：
//  1. HideWindow：exec 任意 console 程序不再闪 cmd 窗口（services 同款）；
//  2. Cancel：Job 可用时组杀整树（KillTree），否则降级直接子进程 kill；
//  3. WaitDelay：仍有漏网进程握管道时 3s 强断 Wait。
//
// 返回的 tree 由调用方在 cmd.Start() 成功后 Attach、Wait 返回后 Release
// （并发契约见 proctree_windows.go 类型注释）。
func newToolCommandContext(ctx context.Context, name string, args ...string) (*exec.Cmd, *ProcessGroupTree) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.WaitDelay = toolCommandWaitDelay
	tree := NewProcessGroupTree()
	if tree.Ok() {
		cmd.Cancel = tree.KillTree
	} else {
		// Job 创建失败：降级直接子进程 kill（见 tools_shell.go 的
		// fallbackCancelDirectKill），WaitDelay 兜底孙进程。
		cmd.Cancel = fallbackCancelDirectKill(cmd)
	}
	return cmd, tree
}

// newProcessGroupKiller 返回直接树杀闭包（TerminateJobObject，Release 前有效，
// 正好覆盖后台路径「armed 保持到 bgCtx.Done 才 Release」的时序）；cmd 形参
// 为两平台统一签名，Windows 忽略。
func newProcessGroupKiller(cmd *exec.Cmd, tree *ProcessGroupTree) func() {
	return func() { _ = tree.KillTree() }
}
