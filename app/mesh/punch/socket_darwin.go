//go:build darwin

package punch

import (
	"context"
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// bindUDP4 在 udp4 socket 的 Control 回调里设 IP_BOUND_IF(IPPROTO_IP),
// 把出站流量钉在物理口上——即使本机路由表被 TUN 引擎接管,socket 仍从
// 指定接口出站。写法参照 inbound/tun/dialer/sockopt_darwin.go(那边函数
// 未导出且耦合 Options,这里独立实现;常量取 x/sys/unix,与
// app/mesh/lanbeacon/sockopts_unix.go 的口径一致)。v1 只做 v4,IPV6_BOUND_IF
// 留待 IPv6 打洞立项。
func bindUDP4(iface string) (*net.UDPConn, error) {
	ifc, err := net.InterfaceByName(iface)
	if err != nil {
		return nil, fmt.Errorf("interface %q: %w", iface, err)
	}
	lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		var sockErr error
		if err := c.Control(func(fd uintptr) {
			sockErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, ifc.Index)
		}); err != nil {
			return err
		}
		return sockErr
	}}
	packet, err := lc.ListenPacket(context.Background(), "udp4", ":0")
	if err != nil {
		return nil, err
	}
	conn, ok := packet.(*net.UDPConn)
	if !ok {
		packet.Close()
		return nil, fmt.Errorf("listen udp4 returned %T, want *net.UDPConn", packet)
	}
	return conn, nil
}
