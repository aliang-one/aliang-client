//go:build windows

package services

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
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

// processGroupTree 是 Windows 的进程树组杀句柄（Job Object 实现）：创建
// KILL_ON_JOB_CLOSE 的 Job → 进程启动后 AssignProcessToJobObject 入树 →
// 超时/取消 TerminateJobObject 组杀（覆盖 Job 内全部后台化子孙）→ Close 兜底
// （agent 进程崩溃/被杀时句柄由内核回收，Job 连带终止整棵树）。POSIX 对应物
// 是 Setpgid + 组杀 SIGKILL（command_nonwindows.go），两平台共用同一调用面
// （newBackgroundCommandTreeContext / attach / release）。
type processGroupTree struct {
	job windows.Handle
	ok  bool
}

// newProcessGroupTree 创建带 KILL_ON_JOB_CLOSE 的空 Job。失败（权限/句柄耗尽）
// 回 ok=false 的空树：调用方降级为直接子进程 kill + WaitDelay 盖帽，不阻断执行。
func newProcessGroupTree() *processGroupTree {
	t := &processGroupTree{}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil || job == windows.InvalidHandle {
		return t
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return t
	}
	t.job = job
	t.ok = true
	return t
}

// attach 把已启动的直接子进程挂进 Job，必须在 Start 之后、超时窗口之前调用：
// 之后子进程派生的全部子孙自动入树（TerminateJobObject / KILL_ON_JOB_CLOSE 均
// 覆盖）；attach 之前已存在的孤儿子进程不在树内，靠 WaitDelay 盖帽兜底。
// 句柄获取：Go 1.25 起 os.Process.Handle() 已被移除（handle 改为内部
// *processHandle 私有管理），按 PID 用 OpenProcess 取 PROCESS_SET_QUOTA |
// PROCESS_TERMINATE 权限句柄（AssignProcessToJobObject 的最低要求），用完即
// Close——关闭的是我们自己的句柄，进程与 Job 的关联不受影响。
// 尽力而为：失败只降级为直接子进程 kill，不阻断主流程。
func (t *processGroupTree) attach(cmd *exec.Cmd) error {
	if !t.ok || cmd == nil || cmd.Process == nil {
		return nil
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("open process for job assign: %w", err)
	}
	defer windows.CloseHandle(ph)
	return windows.AssignProcessToJobObject(t.job, ph)
}

// killTree 组杀：TerminateJobObject(job, 1) 覆盖 Job 内全部子孙进程（直接子进程
// 与后台化孙进程一并终止）。作为 cmd.Cancel 安装。
func (t *processGroupTree) killTree() error {
	if !t.ok {
		return errors.New("process group job object unavailable")
	}
	return windows.TerminateJobObject(t.job, 1)
}

// release 收尾关闭 Job 句柄。KILL_ON_JOB_CLOSE 语义：正常路径这里是显式收尾；
// agent 进程异常退出（崩溃/被杀）没走到 release 时，句柄由内核回收，Job 内
// 进程树被连带终止——Close 即兜底。
func (t *processGroupTree) release() {
	if !t.ok {
		return
	}
	_ = windows.CloseHandle(t.job)
	t.ok = false
}

// newBackgroundCommandTreeContext 是 newBackgroundCommandContext 的组杀增强版：
// Job 可用时把 Cancel 覆盖为 Job 组杀，并把树句柄交还调用方（attach/release）。
// 树恒非 nil（Job 创建失败时是 ok=false 的空树，行为退回基线封装），调用方
// 无需判空。
func newBackgroundCommandTreeContext(ctx context.Context, name string, args ...string) (*exec.Cmd, *processGroupTree) {
	cmd := newBackgroundCommandContext(ctx, name, args...)
	tree := newProcessGroupTree()
	if tree.ok {
		cmd.Cancel = tree.killTree
	}
	return cmd, tree
}
