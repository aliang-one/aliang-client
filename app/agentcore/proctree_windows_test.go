//go:build windows

package agentcore

import (
	"bytes"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestProcessGroupTreeKillTree 验证 Job 组杀覆盖孙进程，并对「组杀退化成单杀」
// 具备辨别力（回归即红）：
//   - 组杀正确：cmd.exe 与孙进程 ping 同属 Job，KillTree 整树终止 → ping 持有的
//     stdout 管道写端全部关闭 → 管道 EOF → cmd.Wait（等 stdio 拷贝 goroutine
//     收尾）立即返回；
//   - 实现退化成只杀直接子进程：ping 存活并握着继承来的写端 → 管道无 EOF →
//     Wait 永久阻塞 → 5s 超时 t.Fatal。
//
// 辨别力依赖三个构造，缺一即对退化形态失明：
//  1. 命令不带「> nul」重定向——重定向在 cmd.exe 内部完成会把管道形态吞掉，
//     ping 就不再持有任何写端；
//  2. cmd.Stdout 给真 buffer——os/exec 才会建真管道、ping 才有写端可继承、
//     Wait 才会等管道 EOF（nil Stdout 时无管道无拷贝 goroutine，cmd.exe 一死
//     Wait 即返回，退化形态照样秒绿）；
//  3. 不设 WaitDelay——3s 强断会把退化形态「放行」成 ErrWaitDelay，正是本测
//     要抓的回归。
func TestProcessGroupTreeKillTree(t *testing.T) {
	tree := NewProcessGroupTree()
	if !tree.Ok() {
		t.Fatalf("Job Object 创建失败（权限/句柄）：装机会话必须报告该降级，而非静默跳过")
	}
	cmd := exec.Command("cmd", "/C", "ping -n 30 127.0.0.1")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Stdout = &bytes.Buffer{}
	cmd.Cancel = tree.KillTree
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := tree.Attach(cmd); err != nil {
		t.Fatalf("attach: %v", err)
	}
	_ = tree.KillTree()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("KillTree 后 Wait 未在 5s 内返回：疑似组杀退化成单杀（ping 存活握住管道）")
	}
	// 双保险：ping 已不在进程表（容忍 Job 终止落账的短暂延迟，最多重试约 2s）。
	deadline := time.Now().Add(2 * time.Second)
	for {
		out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq ping.exe").Output()
		if err == nil && !strings.Contains(strings.ToLower(string(out)), "ping.exe") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("KillTree 后 ping.exe 仍在进程表：组杀未覆盖孙进程（tasklist=%q err=%v）", string(out), err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	tree.Release()
}
