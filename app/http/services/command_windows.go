//go:build windows

package services

import (
	"context"
	"os/exec"
	"syscall"
	"time"

	"aliang.one/nursorgate/app/agentcore"
)

// backgroundCommandWaitDelay 是 Windows 侧的 Wait 盖帽时长：后台化孙进程继承
// stdout/stderr 管道不放时，cmd.Wait 在直接子进程退出后最长再等这么久就强断
// （ErrWaitDelay）。与 POSIX 侧 command_nonwindows.go 的 3s 对齐。
const backgroundCommandWaitDelay = 3 * time.Second

// newBackgroundCommand spawns a console child process with its window hidden.
// On Windows, exec'ing any console program (git, claude, codex, …) otherwise
// flashes a cmd window for every invocation — and the inventory scan calls
// `git status` / `git ls-files` per project on a periodic tick, which strobes
// the screen continuously after login. HideWindow (CREATE_NO_WINDOW-equivalent)
// suppresses that. Stdio piping is unaffected, so stdio-based tool protocols
// keep working. Use for every short-lived tool probe / background tool launch.
func newBackgroundCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

// newBackgroundCommandContext 在 HideWindow 之外补齐 Windows 侧的进程纪律。
// Windows 没有进程组（Setpgid / kill(-pgid) 均不可用），裸 CommandContext 的
// ctx kill 只 TerminateProcess 直接子进程——后台化孙进程继续持有继承来的
// stdout 管道，cmd.Wait 永久阻塞在管道 EOF（与 POSIX 侧同一形态的串行槽杀手，
// 见 command_nonwindows.go 注释）。两层兜底：
//  1. WaitDelay：孙进程拖住管道时 Wait 最长再等 3s 强断（ErrWaitDelay），调用方
//     的槽位必然释放；
//  2. Cancel：ctx 到期先杀直接子进程（无 Job 时的降级路径；需要组杀的调用方
//     走 newBackgroundCommandTreeContext，Cancel 被 Job 组杀覆盖）。
func newBackgroundCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.WaitDelay = backgroundCommandWaitDelay
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	return cmd
}

// processGroupTree 已下沉到 agentcore（2026-11-28，内核工具与宿主后台命令
// 共用同一 Job 实现）：这里只留转发。
type processGroupTree = agentcore.ProcessGroupTree

func newProcessGroupTree() *processGroupTree { return agentcore.NewProcessGroupTree() }

// newBackgroundCommandTreeContext 是 newBackgroundCommandContext 的组杀增强版：
// Job 可用时把 Cancel 覆盖为 Job 组杀，并把树句柄交还调用方（Attach/Release）。
// 树恒非 nil（Job 创建失败时是 Ok()=false 的空树，行为退回基线封装），调用方
// 无需判空。
func newBackgroundCommandTreeContext(ctx context.Context, name string, args ...string) (*exec.Cmd, *processGroupTree) {
	cmd := newBackgroundCommandContext(ctx, name, args...)
	tree := newProcessGroupTree()
	if tree.Ok() {
		cmd.Cancel = tree.KillTree
	}
	return cmd, tree
}
