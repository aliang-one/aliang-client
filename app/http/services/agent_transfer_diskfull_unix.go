//go:build !windows

package services

// Unix 侧：syscall.ENOSPC。darwin/linux 的文件系统错误最终都落到这个 errno。

import (
	"errors"
	"syscall"
)

func isAgentTransferDiskFullErrno(err error) bool {
	return errors.Is(err, syscall.ENOSPC)
}
