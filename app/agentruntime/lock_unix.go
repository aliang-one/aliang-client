//go:build !windows

package agentruntime

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// tryFlock 用非阻塞独占 flock 仲裁单实例。拿不到锁（已有 user-agent 在跑）立即返回 error。
func tryFlock(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("another user-agent instance holds the lock: %w", err)
	}
	return nil
}

// isAgentPIDAlive 通过 signal 0 探测 pid 是否存活（不发实际信号）。
func isAgentPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// isAgentProcessByComm 校验 pid 对应进程命令行包含 "aliang"。
// pidfile fallback kill 前用此防御：agent 异常退出后 pidfile 残留，OS 把该 pid 复用给
// 无关进程时，避免误杀。macOS / Linux 的 ps -p PID -o comm= 均符合 POSIX。
func isAgentProcessByComm(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), "aliang")
}
