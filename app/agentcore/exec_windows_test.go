//go:build windows

package agentcore

import (
	"context"
	"testing"
	"time"
)

// TestNewToolCommandContextWiring 验证 newToolCommandContext 的接线本体
// （Ok() 门控、HideWindow、WaitDelay、Cancel 已装）——proctree 原语由
// TestProcessGroupTreeKillTree 覆盖，本测防「接线层回归无人知」：将来有人
// 删掉 Ok() 分支或漏装 SysProcAttr，必须有一条测试变红。
func TestNewToolCommandContextWiring(t *testing.T) {
	cmd, tree := newToolCommandContext(context.Background(), "cmd", "/C", "echo wiring")
	if cmd == nil || tree == nil {
		t.Fatal("cmd/tree 不得为 nil")
	}
	if !tree.Ok() {
		t.Fatalf("Job Object 应可用（不可用属装机发现，如实报告）")
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow {
		t.Fatal("HideWindow 未接线（三层纪律 ①）")
	}
	if cmd.WaitDelay != toolCommandWaitDelay {
		t.Fatalf("WaitDelay 未接线：got %v want %v（三层纪律 ③）", cmd.WaitDelay, toolCommandWaitDelay)
	}
	if cmd.Cancel == nil {
		t.Fatal("Cancel 未安装")
	}
}

// TestNewToolCommandContextCancelKillsProcess 真走一遍 Cancel 路径：50ms 后
// ctx 到期 → exec 调 Cancel → Wait 必须快速收敛。2s 上界刻意压在 WaitDelay
// （3s）之下：若 Cancel 失效（比如绑到空 Job 又没降级），进程活着要等
// WaitDelay 兜底到 ~3.05s 才返回——本测必须变红，而不是被兜底掩盖成假绿。
// ping 是 Windows 上对重定向 stdin 安全的经典 sleep 写法（timeout /T 在
// stdin 非 console 时直接报错退出）。
func TestNewToolCommandContextCancelKillsProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	cmd, tree := newToolCommandContext(ctx, "cmd", "/C", "ping -n 11 127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := tree.Attach(cmd); err != nil {
		// 与 runShell 生产路径一致：入树失败降级覆盖 Cancel 为直接 kill。
		t.Logf("Attach 失败，按生产路径降级 Cancel: %v", err)
		cmd.Cancel = func() error {
			if cmd.Process != nil {
				return cmd.Process.Kill()
			}
			return nil
		}
	}
	start := time.Now()
	waitErr := cmd.Wait()
	elapsed := time.Since(start)
	tree.Release()

	if elapsed > 2*time.Second {
		t.Fatalf("ctx 到期后 Wait 未在 2s 内收敛（耗时 %s，waitErr=%v）：Cancel 疑似没杀到进程", elapsed, waitErr)
	}
}
