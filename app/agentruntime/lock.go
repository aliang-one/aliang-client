package agentruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/internal/runtimepath"
)

// agentLockPath 返回 user-agent 的 pidfile / 锁文件路径（与 agent.log 同目录）。
func agentLockPath() (string, error) {
	stateDir, err := runtimepath.UserStateDir()
	if err != nil {
		return "", fmt.Errorf("resolve user state dir: %w", err)
	}
	return filepath.Join(stateDir, "agent", "agent.pid"), nil
}

// acquireAgentLock 获取 user-agent 的进程级独占锁（flock / LockFileEx），并把自身 pid
// 写入 pidfile。返回的 *os.File 必须由调用方保持打开直到进程退出——锁的生命周期绑定到
// 该 fd，关闭即释放（即便进程 crash，OS 也会回收 fd 释放锁）。
//
// 拿不到锁说明已有 user-agent 在跑，返回非 nil error，调用方应干净退出、不 listen。
// 这是跨进程唯一性的内核级仲裁，配合 56433 端口监听构成双兜底。
func acquireAgentLock() (*os.File, error) {
	lockPath, err := agentLockPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("create agent dir: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open pidfile %s: %w", lockPath, err)
	}
	if err := tryFlock(f); err != nil {
		f.Close()
		return nil, err
	}
	// 持锁成功：覆写为当前 pid（截断旧内容，避免陈旧字节残留）。写入失败非致命——
	// 锁已持有，pidfile 仅用于 stop-stale 兜底与诊断。
	if err := f.Truncate(0); err != nil {
		logger.Warn(fmt.Sprintf("acquireAgentLock: truncate pidfile failed: %v", err))
	}
	if _, err := f.Seek(0, 0); err != nil {
		logger.Warn(fmt.Sprintf("acquireAgentLock: seek pidfile failed: %v", err))
	}
	if _, err := fmt.Fprintf(f, "%d\n", os.Getpid()); err != nil {
		logger.Warn(fmt.Sprintf("acquireAgentLock: write pid failed: %v", err))
	}
	return f, nil
}

// readAgentPIDFromFile 从 pidfile 读出 pid。用于 stop-stale 在 HTTP status 取不到时兜底
// （agent 半死、不响应 /api/agent/status，但 pidfile 里有它启动时写入的 pid）。
// 文件不存在 / 格式无效返回 0。
func readAgentPIDFromFile() int {
	lockPath, err := agentLockPath()
	if err != nil {
		return 0
	}
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return 0
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err != nil {
		return 0
	}
	return pid
}
