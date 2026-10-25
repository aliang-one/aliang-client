//go:build windows

package services

// Windows 侧：Win32 错误码。syscall 包不导出这些常量，用字面量并注释出处
// （WinError.h）：112 = ERROR_DISK_FULL，395 = ERROR_DISK_QUOTA_EXCEEDED。

import (
	"errors"
	"syscall"
)

func isAgentTransferDiskFullErrno(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == 112 || errno == 395
}
