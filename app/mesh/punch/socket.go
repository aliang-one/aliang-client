// Package punch 是 mesh UDP 打洞编排的家(socket/stun/tls/listener/dialer/
// session 按任务陆续落位);本文件承载打洞 socket 出厂即绑默认物理接口的
// 公共逻辑与平台绑定入口。
package punch

import (
	"fmt"
	"net"

	tunutils "aliang.one/nursorgate/inbound/tun/runner/utils"
)

// InterfaceResolver 解析系统默认物理接口名。默认实现取
// inbound/tun/runner/utils/device.go:65 GetDefaultInterface(纯解析、
// 无缓存无副作用,每次调用跑 route/ip/PowerShell Get-NetRoute 级只读探测);测试注入桩用。
type InterfaceResolver func() (string, error)

// DefaultResolver 默认物理口解析器,导出为变量便于测试整体替换。
var DefaultResolver InterfaceResolver = tunutils.GetDefaultInterface

// NewBoundUDPConn 建一个出厂即绑默认物理接口的 udp4 socket:
// resolver 解析接口名 → bindUDP4 按平台设绑定选项 → OS 分配随机端口。
//
// 语义铁律(评审定谳):绑定失败即返回错误,由上层载体自禁落 piko 中继,
// 绝不静默回落未绑定 socket——TUN-on 下裸 socket 的 STUN 应答描述的是
// 引擎换发 socket 的映射,洞会打错 5-tuple。v1 只做 udp4(IPv6 打洞
// 不在本批范围,与 app/http/services/agent_peer_lan.go:96 的 To4 断言对齐)。
func NewBoundUDPConn(resolve InterfaceResolver) (*net.UDPConn, string, error) {
	if resolve == nil {
		resolve = DefaultResolver
	}
	iface, err := resolve()
	if err != nil {
		return nil, "", fmt.Errorf("punch socket: resolve interface: %w", err)
	}
	conn, err := bindUDP4(iface)
	if err != nil {
		return nil, "", fmt.Errorf("punch socket: bind %s: %w", iface, err)
	}
	return conn, iface, nil
}
