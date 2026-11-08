//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package lanbeacon

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// beaconListenConfig 广播与监听 conn 共用的 ListenConfig;Control 统一置位
// (socket 选项常量取 golang.org/x/sys/unix——stdlib syscall 的
// SO_REUSEPORT 常量按 GOOS/arch 可缺,跨平台编译面收敛到 x/sys):
//   - SO_BROADCAST:全局广播 255.255.255.255 的写权限;
//   - SO_REUSEADDR/SO_REUSEPORT:同机多实例可同绑端口(多实例仅测试场景;
//     广播报文本机回环会投递到同端口全部 listener——darwin 2026-11-08 实测;
//     linux 投递语义按 SO_REUSEPORT 标准语义推断,Task 13 集成在 Linux 真机
//     复核),生产单实例下两者无害。
var beaconListenConfig = net.ListenConfig{Control: beaconSocketControl}

func beaconSocketControl(network, address string, c syscall.RawConn) error {
	var sockErr error
	err := c.Control(func(fd uintptr) {
		for _, opt := range []int{unix.SO_REUSEADDR, unix.SO_REUSEPORT, unix.SO_BROADCAST} {
			if e := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, opt, 1); e != nil && sockErr == nil {
				sockErr = e
			}
		}
	})
	if err != nil {
		return err
	}
	return sockErr
}
