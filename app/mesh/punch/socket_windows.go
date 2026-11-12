//go:build windows

package punch

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// IP_UNICAST_IF 的 IPPROTO_IP 套接字选项值;x/sys/windows 未导出该常量,
// 与 inbound/tun/dialer/sockopt_windows.go 同源自定义。
const ipUnicastIf = 31

// bindUDP4 在 udp4 socket 的 Control 回调里设 IP_UNICAST_IF,把出站
// 流量钉在物理口上。IPv4 的选项值要求接口索引以网络字节序传入
// (big-endian uint32 再 reinterpret),写法参照
// inbound/tun/dialer/sockopt_windows.go(那边函数未导出且耦合 Options,
// 这里独立实现)。v1 只做 v4。
func bindUDP4(iface string) (*net.UDPConn, error) {
	ifc, err := net.InterfaceByName(iface)
	if err != nil {
		return nil, fmt.Errorf("interface %q: %w", iface, err)
	}
	lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		var sockErr error
		if err := c.Control(func(fd uintptr) {
			// For IPv4, this parameter must be an interface index in
			// network byte order.
			// Ref: https://learn.microsoft.com/en-us/windows/win32/winsock/ipproto-ip-socket-options
			var bytes [4]byte
			binary.BigEndian.PutUint32(bytes[:], uint32(ifc.Index))
			index := *(*uint32)(unsafe.Pointer(&bytes[0]))
			sockErr = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, ipUnicastIf, int(index))
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
