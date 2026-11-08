//go:build windows

package lanbeacon

import (
	"net"
	"syscall"
)

// beaconListenConfig Windows 变体:无 SO_REUSEPORT,同机多实例双绑不可行
// (双实例测试仅限 unix 系);单实例生产只需广播写权限。刻意不设
// SO_REUSEADDR——Windows 语义允许强抢他进程端口,风险大于收益。
var beaconListenConfig = net.ListenConfig{Control: beaconSocketControl}

func beaconSocketControl(network, address string, c syscall.RawConn) error {
	var sockErr error
	err := c.Control(func(fd uintptr) {
		if e := syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1); e != nil && sockErr == nil {
			sockErr = e
		}
	})
	if err != nil {
		return err
	}
	return sockErr
}
