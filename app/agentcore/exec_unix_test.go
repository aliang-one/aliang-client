//go:build !windows

package agentcore

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestRunShellGroupKillBoundsWallTime 是 2026-11-01 线上卡死的回归测试。
//
// 事故形态：`ls -d ~/*aliang*; find ~ … | head` 这类**复合命令**让 shell fork 出
// 孙进程，孙进程继承了 os/exec 为 cappedWriter 创建的 stdout 管道。裸
// exec.CommandContext 的 Cancel 只 SIGKILL 直接子进程（shell），孙进程变孤儿
// 继续持有管道写端 → 拷贝 goroutine 等不到 EOF → cmd.Run() 无限等待。
// 结果：30 秒超时形同虚设，整轮 run 卡了 1 小时 58 分。
//
// 修复（exec_unix.go 的 Setpgid + kill(-pgid) + WaitDelay）之后，超时必须真正
// 生效：命令在 300ms 超时后必须很快返回，而不是陪孙进程一起睡。
func TestRunShellGroupKillBoundsWallTime(t *testing.T) {
	env := &ToolEnv{ShellPolicy: shPolicy()}
	input := map[string]interface{}{
		// 复合命令是关键：单条 `sleep 30` 会被 shell exec 优化成直接子进程，
		// 反而测不出这个缺陷。
		"command": "sleep 30; echo done",
		"timeout": 300,
	}

	start := time.Now()
	res := (&bashTool{}).Execute(context.Background(), input, env)
	elapsed := time.Since(start)

	// 孙进程睡眠 30s。修复前这里会等满 30s（或直到 run 看门狗），修复后
	// 应该 300ms + WaitDelay(3s) 量级返回。10s 是宽松上界，避免 CI 抖动。
	if elapsed > 10*time.Second {
		t.Fatalf("超时未生效：300ms 上限的命令占用了 %s，说明孙进程仍在持有管道", elapsed)
	}
	if !res.IsError {
		t.Fatalf("超时的命令必须返回 IsError=true，实际: %+v", res)
	}
}

// TestRunShellGrandchildPipeDoesNotHangWait 直接验证 WaitDelay 兜底：即使
// 孙进程没被组杀干净（模拟），Wait 也不允许超过 WaitDelay 太多。
func TestRunShellGrandchildPipeDoesNotHangWait(t *testing.T) {
	env := &ToolEnv{ShellPolicy: shPolicy()}
	input := map[string]interface{}{
		"command": "sh -c 'sleep 30' ; echo done",
		"timeout": 300,
	}

	start := time.Now()
	(&bashTool{}).Execute(context.Background(), input, env)
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("嵌套 shell 场景仍然挂死：%s", elapsed)
	}
}

// TestRunShellBackgroundChildKeepsOutput 覆盖 WaitDelay 带来的新错误类别。
//
// `cmd &` 是极常见写法（nohup ./server &、npm run dev &）。shell 立刻以 0 退出
// 并打印输出，但后台子进程仍握着 stdout 管道，于是 WaitDelay 到期、cmd.Run()
// 返回 exec.ErrWaitDelay。**这不是失败**：必须把已采集的输出回填并说明可能不
// 完整。修复前这里会落到通用失败分支，输出被整段丢弃、成功被报成「执行失败」，
// 模型于是重试，每重试一次再漏一个后台孤儿进程。
func TestRunShellBackgroundChildKeepsOutput(t *testing.T) {
	env := &ToolEnv{ShellPolicy: shPolicy()}
	input := map[string]interface{}{
		// 超时给足 60s，确保命中的是 WaitDelay 而不是 DeadlineExceeded。
		"command": "sleep 20 & echo STARTED",
		"timeout": 60000,
	}

	start := time.Now()
	res := (&bashTool{}).Execute(context.Background(), input, env)
	elapsed := time.Since(start)

	if !strings.Contains(res.Content, "STARTED") {
		t.Fatalf("命令明明打印了 STARTED，输出却被丢弃: %q", res.Content)
	}
	if res.IsError {
		t.Fatalf("退出码 0 的命令被判为失败: %q", res.Content)
	}
	// 不能被 20 秒的后台 sleep 拖住；WaitDelay=3s 是上界，10s 留抖动余量。
	if elapsed > 10*time.Second {
		t.Fatalf("后台子进程把工具拖住了: %s", elapsed)
	}
}
