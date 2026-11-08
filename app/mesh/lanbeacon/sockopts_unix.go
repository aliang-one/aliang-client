//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package lanbeacon

import (
	"net"
	"syscall"
)

// beaconListenConfig 广播与监听 conn 共用的 ListenConfig;Control 统一置位:
//   - SO_BROADCAST:全局广播 255.255.255.255 的写权限;
//   - SO_REUSEADDR/SO_REUSEPORT:同机多实例可同绑端口(多实例仅测试场景;
//     广播报文本机回环会投递到同端口全部 listener——2026-11-08 macOS 实测,
//     linux 对 bcast 同为全投递),生产单实例下两者无害。
var beaconListenConfig = net.ListenConfig{Control: beaconSocketControl}

func beaconSocketControl(network, address string, c syscall.RawConn) error {
	var sockErr error
	err := c.Control(func(fd uintptr) {
		for _, opt := range []int{syscall.SO_REUSEADDR, syscall.SO_REUSEPORT, syscall.SO_BROADCAST} {
			if e := syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, opt, 1); e != nil && sockErr == nil {
				sockErr = e
			}
		}
	})
	if err != nil {
		return err
	}
	return sockErr
}
