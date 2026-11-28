//go:build !windows

package services

import (
	"context"
	"os/exec"
	"syscall"
	"time"

	"aliang.one/nursorgate/app/agentcore"
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

// processGroupTree 已下沉到 agentcore（与 Windows 同一面）：POSIX 上是空壳，
// 组杀语义在 exec_unix.go 的 cmd.Cancel 内（Setpgid + 组杀 SIGKILL）。
type processGroupTree = agentcore.ProcessGroupTree

func newProcessGroupTree() *processGroupTree { return agentcore.NewProcessGroupTree() }

// newBackgroundCommandTreeContext 与 Windows 同名工厂对齐：返回构造好的命令与
// 组杀树句柄。POSIX 侧树是无状态 no-op，组杀仍走构造器里的 Cancel。
func newBackgroundCommandTreeContext(ctx context.Context, name string, args ...string) (*exec.Cmd, *processGroupTree) {
	return newBackgroundCommandContext(ctx, name, args...), newProcessGroupTree()
}
