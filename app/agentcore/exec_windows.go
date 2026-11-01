//go:build windows

package agentcore

import (
	"context"
	"os/exec"
	"time"
)

// toolCommandWaitDelay 见 exec_unix.go 的同名常量。
const toolCommandWaitDelay = 3 * time.Second

// newToolCommandContext 构造工具执行用的命令。
//
// Windows 没有进程组语义（Setpgid/kill(-pgid) 是 unix 专有），这里退而求其次
// 只补 WaitDelay：shell（cmd/powershell）被杀之后，若孙进程仍握着继承来的
// stdout 管道，Wait 最多再等 3 秒就返回 exec.ErrWaitDelay，不会无限挂死。
//
// 彻底的做法是 CREATE_NEW_PROCESS_GROUP + taskkill /T /F 整树终止；那需要额外
// 的进程树枚举与错误容忍，留作后续（本分支先解决 macOS/Linux 的实锤事故）。
func newToolCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = toolCommandWaitDelay
	return cmd
}
