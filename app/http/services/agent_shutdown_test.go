package services

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestAgentServiceShutdownKillsManagedChild 锁死 RUN-03:agent 进程退出前
// 调 Shutdown,在途回合派生的托管子进程必须被杀,不得成为孤儿继续跑。
//
// 此前 StopLocalServer 只做 HTTP shutdown + 停 usage/keepalive,从不调
// closeAll——run 的 ctx 不被 cancel,进程组组杀就不会触发。
func TestAgentServiceShutdownKillsManagedChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX 进程组语义;Windows 由 Job Object 覆盖")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())

	s := NewAgentService()
	// 用与生产完全相同的构造器起一个长活子进程:它带 Setpgid 与
	// Cancel(-pgid SIGKILL),就是 RUN-03 要验证的那条组杀路径。
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(runCtx, "sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	// exec.CommandContext 的 Cancel 回调由 Wait 的 goroutine 驱动:生产里 run
	// goroutine 一直在 Wait,这里必须同样起一个,否则 ctx 取消后 Cancel 不执行。
	waitDone := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(waitDone)
	}()
	childPid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-childPid, syscall.SIGKILL)
		select {
		case <-waitDone:
		case <-time.After(3 * time.Second):
		}
	})
	if !processAlive(childPid) {
		t.Fatal("child died before shutdown; test setup is wrong")
	}

	// 把这个 ctx 登记成一个在途回合的 cancel,正是 Shutdown 要收口的东西。
	s.mu.Lock()
	s.ai.sessions["s-shutdown"] = &agentAISession{
		id:           "s-shutdown",
		cancel:       cancel,
		lastActiveAt: time.Now(),
	}
	s.mu.Unlock()

	s.Shutdown()

	// 组杀是异步送达的,给一个短窗。
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(childPid) {
		if time.Now().After(deadline) {
			t.Fatalf("managed child pid=%d still alive 5s after Shutdown; it would keep writing the workspace", childPid)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// 会话与终端 map 都应被清空。
	s.mu.Lock()
	aiLeft := len(s.ai.sessions)
	s.mu.Unlock()
	if aiLeft != 0 {
		t.Fatalf("ai sessions left after Shutdown: %d", aiLeft)
	}
	// 幂等:重复调用不 panic。
	s.Shutdown()
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	// 僵尸进程在父进程 Wait 前仍占 pid,kill(0) 返回成功;用 ps 区分。
	if err == syscall.ESRCH {
		return false
	}
	out, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	stat := strings.TrimSpace(string(out))
	return !strings.HasPrefix(stat, "Z")
}
