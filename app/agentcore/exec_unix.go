//go:build !windows

package agentcore

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

// toolCommandWaitDelay 是「组杀之后仍等管道关闭」的兜底时长。
//
// 与 services/command_nonwindows.go 同款取值：组杀已发出，若还有孙进程赖着
// 不关管道，3 秒后 Wait 强断并返回 exec.ErrWaitDelay，绝不无限等。
const toolCommandWaitDelay = 3 * time.Second

// newToolCommandContext 构造工具执行用的命令。
//
// 为什么不直接用 exec.CommandContext：它默认的 Cancel 只 kill 直接子进程
// （cmd.Process.Kill() → kill(pid, SIGKILL)），**不碰进程组**。而 shell 工具
// 执行的往往不是单条命令——`ls …; find … | head` 这种复合/管道形态会让 shell
// fork 出 find/head，这些孙进程继承 Go 侧创建的 stdout 管道写端。shell 被
// SIGKILL 之后孙进程变孤儿仍在跑，写端一直不关，于是：
//
//   - os/exec 的拷贝 goroutine 永远等不到 EOF；
//   - cmd.Wait() 在 awaitGoroutines 里无超时地 <-c.goroutineErr（WaitDelay=0）；
//   - 30 秒超时明明已经触发，工具却永不返回，整轮 AI run 被拖到 1 小时看门狗。
//
// 2026-11-01 线上实锤：一条 `ls -d ~/*aliang*; find ~ … | head` 卡了 1 小时 58
// 分，期间 macOS TCC 弹窗无人应答。同类事故在 services/command_nonwindows.go
// 已修过一次（2026-09-23），agentcore 因为禁止 import services 而漏掉了。
//
// 三件套：
//  1. Setpgid：让 shell 成为新进程组组长，组内所有后代共享一个 pgid；
//  2. Cancel 用 kill(-pgid)：一次干掉整组，含孙进程；
//  3. WaitDelay：万一仍有漏网进程握着管道，3 秒后强断 Wait。
func newToolCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		pid := cmd.Process.Pid
		// 顺序很关键：**先** kill 直接子进程，成功（说明还没被 waitpid 回收）
		// 才组杀。反过来的话，在「子进程已正常退出、Wait 却还在等孙进程关管道」
		// 这段窗口里（WaitDelay 让这段窗口长达 3 秒），pid 可能已被内核复用：
		// kill(-pid) 就会误杀一个毫不相干的进程组；若复用出的 pid 恰好是 agent
		// 自己（launchd/systemd 拉起的进程通常 pgid==pid），agent 会把自己整组
		// SIGKILL 掉。Process.Kill() 对已回收进程返回 os.ErrProcessDone，正是
		// 我们需要的「还活着吗」判据。
		if err := cmd.Process.Kill(); err != nil {
			return err
		}
		// 组长已死，但组内孙进程还在：pgid 在成员存活期间依旧有效。
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		return nil
	}
	cmd.WaitDelay = toolCommandWaitDelay
	return cmd
}
