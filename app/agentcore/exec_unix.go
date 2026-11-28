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

// newToolCommandContext 构造工具执行用的命令，返回（cmd, tree）二元组：
// unix 的组杀在 cmd.Cancel 内完成，tree 是空壳占位（proctree_other.go），
// 与 Windows 侧共用同一调用面，调用方无需按平台分叉。
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
func newToolCommandContext(ctx context.Context, name string, args ...string) (*exec.Cmd, *ProcessGroupTree) {
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
	return cmd, &ProcessGroupTree{}
}

// newProcessGroupKiller 返回直接组杀闭包，供后台路径在 watchCtx 退场（主
// 进程被 reap）后仍能终止孙进程：不走 os/exec 的 Cancel 编排，直接
// Process.Kill + kill(-pgid)。tree 形参为两平台统一签名，POSIX 忽略。
//
// pgid==pid：Start 后调用，子进程是组长（Setpgid）。组长存活期 pgid 有效；
// 主命令已 reap 后 pgid 仍被存活孙进程占住，组杀照常命中。闭包 armed 窗
// 最长到 bgCtx.Done（自然死亡后最长 24h），期间 pid 可能被回收复用——
// 组杀前先 kill(-pid,0) 探测组存活（ESRCH=组已散，跳过；EPERM=组在但异
// 主，跳过不损），组活着时行为与无条件组杀一致。探测与组杀两条相邻系统
// 调用之间仍有 µs 级 TOCTOU 残窗（量级与上方前台 Cancel 闭包自接受的暴露
// 同级）。Windows 侧按 Job 身份杀（TerminateJobObject），无 pid 复用险，
// 不需要同款探测。
func newProcessGroupKiller(cmd *exec.Cmd, tree *ProcessGroupTree) func() {
	pid := cmd.Process.Pid // Start 后调用；子进程是组长（Setpgid），pgid==pid
	return func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill() // 已 reap 时 ErrProcessDone，放行组杀
		}
		// 组存活探测：armed 窗最长到 bgCtx.Done（自然死亡后最长 24h），期间
		// pid 可能被回收复用为新进程组长——kill(-pid,0) 探测（ESRCH=组已散，
		// 跳过；EPERM=组在但异主，跳过不损），组活着时行为与无条件组杀一致。
		if syscall.Kill(-pid, 0) == nil {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	}
}
