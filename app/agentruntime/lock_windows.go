//go:build windows

package agentruntime

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// tryFlock 用 Windows LockFileEx 对整个 pidfile 加非阻塞独占锁，仲裁单实例。
// 进程退出（含崩溃）时 OS 自动释放锁，与 unix flock 语义一致。
func tryFlock(f *os.File) error {
	const (
		lockfileExclusiveLock   = 0x00000002
		lockfileFailImmediately = 0x00000001
	)
	var ol windows.Overlapped // 零值 → 偏移 0
	// 锁 [0, 0xFFFFFFFF] 字节区间，足以覆盖 pidfile 整体。
	if err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		lockfileExclusiveLock|lockfileFailImmediately,
		0,
		0xFFFFFFFF, 0,
		&ol,
	); err != nil {
		return fmt.Errorf("another user-agent instance holds the lock: %w", err)
	}
	return nil
}

// isAgentPIDAlive 用 OpenProcess 探活。
func isAgentPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	const processQueryLimitedInformation = 0x1000
	h, err := windows.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	_ = windows.CloseHandle(h)
	return true
}

// isAgentProcessByComm 在 Windows 上跳过命令行校验（缺 POSIX ps）。
// pidfile fallback 仅在 HTTP status 拿不到时启用，且 Windows 上进程退出即释放锁、
// pidfile 陈旧风险更低；如需更严，可改用 QueryFullProcessImageName 校验镜像名。
func isAgentProcessByComm(pid int) bool {
	return true
}
