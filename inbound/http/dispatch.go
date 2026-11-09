// dispatch.go — 统一入口协议分派(spec §4/§6.2):56432 accept 后先嗅探协议族,
// 再移交对应处理器。本文件只做「交对人」:
//
//	KindSSH   → inbound/ssh 注入缝(入口未配置则由缝自身拒收断连)
//	KindSocks → inbound/socks5 入站(no-auth+CONNECT-only,会话租约门禁)
//	KindMesh  → 装配层接线的 mesh 帧处理器(默认未装配=断连)
//	其余       → 既有 HTTP 路径(ReadRequest 失败即断连,语义零变化)
//
// 门禁语义逐协议保持不变:嗅探层不做任何准入判断,准入归各协议处理器。
package http

import (
	"fmt"
	"net"

	"aliang.one/nursorgate/common/logger"
	sniff "aliang.one/nursorgate/inbound/sniff"
	"aliang.one/nursorgate/inbound/socks5"
	sshin "aliang.one/nursorgate/inbound/ssh"
)

// MeshConnHandler 是统一口 mesh 帧分派钩子(spec §6.2):装配层(core boot)经
// tunnel.WireUnifiedIngressMeshDispatch 接线为 HandleMeshConn(carrier="lan")。
// nil=未装配(纯 HTTP 客户端/未开组网),连接断开——inbound 层不反向 import
// app 层,接线方向恒为 app→inbound。
var MeshConnHandler func(conn net.Conn)

// dispatchConn 处理一条来自统一口的连接。conn 的关闭归本函数(defer);各
// 处理器内部的关闭为幂等双关(与合并前 server.go 同模式)。
func dispatchConn(conn net.Conn) {
	defer conn.Close()

	kind, downstream, err := sniff.Sniff(conn)
	if err != nil {
		logger.Debug(fmt.Sprintf("[DISPATCH] sniff failed from %s: %v", conn.RemoteAddr(), err))
		return
	}

	switch kind {
	case sniff.KindSSH:
		// ssh_ingress 未配置时由 HandleIngressConn 拒收断连并回 false,
		// 不回显任何协议字节(spec §4 钉死的未定义分支)。
		if !sshin.HandleIngressConn(downstream) {
			logger.Debug(fmt.Sprintf("[DISPATCH] ssh rejected (ingress unconfigured) from %s", conn.RemoteAddr()))
		}
	case sniff.KindMesh:
		if MeshConnHandler == nil {
			logger.Debug(fmt.Sprintf("[DISPATCH] mesh frame rejected (handler not wired) from %s", conn.RemoteAddr()))
			return
		}
		// 拒绝行/防环/门禁/注入全部归 HandleMeshConn,返回即终。
		MeshConnHandler(downstream)
	case sniff.KindSocks:
		// 直接调 socks5.Handler(其内建 TCPHandler 测试缝 + 惰性默认管线)。
		// 刻意不在 dispatch 侧赋值任何缝:在 dispatch 包做装配会重新引入
		// import 期单例装配(评审定案),缝归 socks5 包所有。
		socks5.Handler(downstream)
	default:
		handleRawConnection(downstream)
	}
}
