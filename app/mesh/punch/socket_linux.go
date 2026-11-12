//go:build linux

package punch

import (
	"context"
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// bindUDP4 在 udp4 socket 的 Control 回调里设 SO_BINDTODEVICE,把出站
// 流量钉在物理口上——TUN 引擎接管默认路由后 socket 仍从指定接口出站。
// 写法参照 inbound/tun/dialer/sockopt_linux.go(那边函数未导出且耦合
// Options,这里独立实现)。
//
// 权限约束与失败语义(必须知晓):SO_BINDTODEVICE 需要 CAP_NET_RAW——
// 生产 core 进程以 root 跑,天然满足;非 root 环境(如开发容器)会得到
// EPERM,按「绑定失败即返回错误→上层载体自禁落 piko」处理,不降级裸奔。
func bindUDP4(iface string) (*net.UDPConn, error) {
	// 先解析接口名:不存在的名字在预检即报清晰错误(内核侧
	// SO_BINDTODEVICE 对无效名只给 EINVAL,不指明原因)。
	if _, err := net.InterfaceByName(iface); err != nil {
		return nil, fmt.Errorf("interface %q: %w", iface, err)
	}
	lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		var sockErr error
		if err := c.Control(func(fd uintptr) {
			sockErr = unix.BindToDevice(int(fd), iface)
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
