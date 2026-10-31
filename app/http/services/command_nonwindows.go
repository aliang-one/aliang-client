//go:build !windows

package services

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

// newBackgroundCommand is the non-Windows counterpart: no window to hide, so it
// is a plain exec. Kept as an indirection so call sites are identical across
// platforms (see command_windows.go for why Windows needs to hide the console).
func newBackgroundCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

func newBackgroundCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	// Own process group + group kill on cancel: a plain CommandContext kill only
	// reaps the direct child. CLIs that spawn background children (or re-exec)
	// keep running afterwards AND hold the inherited stdout pipe open, so
	// CombinedOutput hangs well past the deadline while the orphan keeps doing
	// real work (2026-09-23: a 5s CLI effort probe kept a claude model turn
	// alive for 2m13s because the orphaned process closed stdout only when the
	// turn finished).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return cmd.Process.Kill()
	}
	// If some grandchild still holds the pipes after the group kill, cap Wait.
	cmd.WaitDelay = 3 * time.Second
	return cmd
}

// processGroupTree 是组杀抽象的 POSIX 侧 no-op 实现：组杀已由
// newBackgroundCommandContext 的 Setpgid + Cancel(-pgid SIGKILL) 钉死，无需额外
// 句柄。与 Windows 的 Job Object 实现（command_windows.go）共用同一调用面
// （newBackgroundCommandTreeContext / attach / release），调用方代码平台无关。
type processGroupTree struct{}

func newProcessGroupTree() *processGroupTree { return &processGroupTree{} }

// attach 在 POSIX 是 no-op：进程一启动即在 Setpgid 建立的进程组内，组杀由
// Cancel 直接打 -pgid。
func (t *processGroupTree) attach(*exec.Cmd) error { return nil }

// release 在 POSIX 是 no-op（无额外句柄可回收）。
func (t *processGroupTree) release() {}

// newBackgroundCommandTreeContext 与 Windows 同名工厂对齐：返回构造好的命令与
// 组杀树句柄。POSIX 侧树是无状态 no-op，组杀仍走构造器里的 Cancel。
func newBackgroundCommandTreeContext(ctx context.Context, name string, args ...string) (*exec.Cmd, *processGroupTree) {
	return newBackgroundCommandContext(ctx, name, args...), &processGroupTree{}
}
