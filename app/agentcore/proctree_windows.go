//go:build windows

package agentcore

import (
	"errors"
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ProcessGroupTree 是 Windows 的进程树组杀句柄（Job Object 实现）：创建
// KILL_ON_JOB_CLOSE 的 Job → 进程启动后 Attach 入树 → 超时/取消 KillTree
// 组杀（覆盖 Job 内全部后台化子孙）→ Release 兜底（agent 进程崩溃/被杀时
// 句柄由内核回收，Job 连带终止整棵树）。POSIX 对应物是 Setpgid + 组杀
// SIGKILL（exec_unix.go newToolCommandContext），两平台共用同一调用面。
//
// 并发契约（给 Task 2 调用方的 happens-before 事实）：KillTree 仅应经
// cmd.Cancel（由 exec 在 ctx 到期时编排，先于 Wait 返回）或在 Wait 返回前
// 显式调用；Release 仅应在 Wait 返回之后调用——Wait 返回即代表管道拷贝
// goroutine 已收尾，此时关 Job 句柄才不会有与组杀的窗口交错。
//
// 为什么放在 agentcore 而不是 common：内核工具执行必须自带进程纪律
// （组杀），而 agentcore 禁止 import services——services 的同款实现
// （command_windows.go）够不着。services→agentcore 依赖已存在，故实现
// 沉到本包，services 反向委托。
type ProcessGroupTree struct {
	job windows.Handle
	ok  bool
}

// NewProcessGroupTree 创建带 KILL_ON_JOB_CLOSE 的空 Job。失败（权限/句柄耗尽）
// 回 ok=false 的空树：调用方降级为直接子进程 kill + WaitDelay 盖帽，不阻断执行。
func NewProcessGroupTree() *ProcessGroupTree {
	t := &ProcessGroupTree{}
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

// Attach 把已启动的直接子进程挂进 Job，必须在 Start 之后、超时窗口之前调用：
// 之后子进程派生的全部子孙自动入树（TerminateJobObject / KILL_ON_JOB_CLOSE 均
// 覆盖）；Attach 之前已存在的孤儿子进程不在树内，靠 WaitDelay 盖帽兜底。
// 句柄获取：Go 1.25 起 os.Process.Handle() 已被移除（handle 改为内部
// *processHandle 私有管理），按 PID 用 OpenProcess 取 PROCESS_SET_QUOTA |
// PROCESS_TERMINATE 权限句柄（AssignProcessToJobObject 的最低要求），用完即
// Close——关闭的是我们自己的句柄，进程与 Job 的关联不受影响。
// 尽力而为：失败只降级为直接子进程 kill，不阻断主流程。
func (t *ProcessGroupTree) Attach(cmd *exec.Cmd) error {
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

// KillTree 组杀：TerminateJobObject(job, 1) 覆盖 Job 内全部子孙进程（直接子进程
// 与后台化孙进程一并终止）。作为 cmd.Cancel 安装。
func (t *ProcessGroupTree) KillTree() error {
	if !t.ok {
		return errors.New("process group job object unavailable")
	}
	return windows.TerminateJobObject(t.job, 1)
}

// Release 收尾关闭 Job 句柄。KILL_ON_JOB_CLOSE 语义：正常路径这里是显式收尾；
// agent 进程异常退出（崩溃/被杀）没走到 Release 时，句柄由内核回收，Job 内
// 进程树被连带终止——Close 即兜底。
func (t *ProcessGroupTree) Release() {
	if !t.ok {
		return
	}
	_ = windows.CloseHandle(t.job)
	t.ok = false
}

// Ok 报告 Job 句柄是否可用：调用方（如 services 的
// newBackgroundCommandTreeContext）用它决定是否把 cmd.Cancel 覆盖为组杀。
func (t *ProcessGroupTree) Ok() bool { return t.ok }
