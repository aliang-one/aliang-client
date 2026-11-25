// dispatch.go — 统一入口协议分派(spec §4/§6.2):56432 accept 后先嗅探协议族,
// 再移交对应处理器。本文件只做「交对人」与「来源闸」:
//
//	KindSSH   → inbound/ssh 注入缝(入口未配置则由缝自身拒收断连)
//	KindSocks → inbound/socks5 入站(no-auth+CONNECT-only,会话租约门禁)
//	其余       → 既有 HTTP 路径(ReadRequest 失败即断连,语义零变化)
//
// 各协议内部的准入语义保持不变,归各协议处理器;本层只多一道来源闸——
// 代理面(HTTP/socks5)只对回环来源开放,见 proxyFaceSourceAllowed。
package http

import (
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"

	"aliang.one/nursorgate/common/logger"
	sniff "aliang.one/nursorgate/inbound/sniff"
	"aliang.one/nursorgate/inbound/socks5"
	sshin "aliang.one/nursorgate/inbound/ssh"
	"aliang.one/nursorgate/processor/config"
)

// proxyFaceSourceAllowed 是统一入口代理面(HTTP/socks5)的来源闸。var 仅供
// 测试注入。
//
// 为什么需要:ssh_ingress 一开,56432 就从回环改绑 0.0.0.0(为了让 mesh 对端
// 拨入),而代理面唯一的门「会话租约」是全机一个标志——不看来源、没有逐请求
// 凭据。没有这道闸时,局域网/隧道网里任何设备都能把网关当无鉴权代理,借它
// 冒用本机登录态发 AI 请求,并经代理回环打进只绑回环的管理 API(56431/56433
// 的信任边界就是「来源是回环」,而代拨发生在网关进程里)。
//
// 判定:
//   - 回环来源:放行(本机应用/CLI 的既有用法)。
//   - 非回环来源:仅当运维以 --host 显式把服务绑到非回环地址时放行(显式的
//     「给局域网用」意图);ssh_ingress 把 56432 放宽到 0.0.0.0 不算。
//   - SSH 族不经此闸:它是统一入口唯一面向局域网的协议面,mesh 只骑在它
//     上面,准入归公钥。
var proxyFaceSourceAllowed = defaultProxyFaceSourceAllowed

func defaultProxyFaceSourceAllowed(remote net.Addr) bool {
	tcpAddr, ok := remote.(*net.TCPAddr)
	if !ok {
		// 非 TCP 网络连接(进程内管道等)没有来源概念,按本机对待。
		return true
	}
	if ip, err := netip.ParseAddr(tcpAddr.IP.String()); err == nil && ip.Unmap().IsLoopback() {
		return true
	}
	return serviceBoundToLAN()
}

// serviceBoundToLAN 报告运维是否以 --host 显式把服务绑到了非回环地址
// (0.0.0.0/::/具体局域网地址)。默认 127.0.0.1 恒为 false。
func serviceBoundToLAN() bool {
	ip, err := netip.ParseAddr(strings.TrimSpace(config.ServiceBindHost()))
	if err != nil {
		return false
	}
	return !ip.Unmap().IsLoopback()
}

// rejectProxyFaceConn 对被来源闸拒绝的连接回最小应答后断连:socks5 回
// 「无可接受方法」(0xFF),其余按 HTTP 回 403。不泄露任何本机信息。
func rejectProxyFaceConn(conn net.Conn, kind sniff.Kind) {
	_ = conn.SetWriteDeadline(deadlineAfter(proxyFaceRejectWriteTimeout))
	switch kind {
	case sniff.KindSocks:
		_, _ = conn.Write([]byte{0x05, 0xFF})
	default:
		_, _ = io.WriteString(conn, "HTTP/1.1 403 Forbidden\r\nConnection: close\r\nContent-Length: 0\r\n\r\n")
	}
}

// dispatchConn 处理一条来自统一口的连接。conn 的关闭归本函数(defer);各
// 处理器内部的关闭为幂等双关(与合并前 server.go 同模式)。
func dispatchConn(conn net.Conn) {
	defer conn.Close()

	kind, downstream, err := sniff.Sniff(conn)
	if err != nil {
		logger.Debug(fmt.Sprintf("[DISPATCH] sniff failed from %s: %v", conn.RemoteAddr(), err))
		return
	}

	// 来源闸:代理面(HTTP/socks5)只对回环来源开放;SSH 族放行。
	if kind != sniff.KindSSH && !proxyFaceSourceAllowed(conn.RemoteAddr()) {
		logger.Info(fmt.Sprintf("[DISPATCH] 拒绝非回环来源访问代理面 from=%s kind=%s", conn.RemoteAddr(), kind))
		rejectProxyFaceConn(conn, kind)
		return
	}

	switch kind {
	case sniff.KindSSH:
		// ssh_ingress 未配置时由 HandleIngressConn 拒收断连并回 false,
		// 不回显任何协议字节(spec §4 钉死的未定义分支)。
		if !sshin.HandleIngressConn(downstream) {
			logger.Debug(fmt.Sprintf("[DISPATCH] ssh rejected (ingress unconfigured) from %s", conn.RemoteAddr()))
		}
	case sniff.KindSocks:
		// 直接调 socks5.Handler(其内建 TCPHandler 测试缝 + 惰性默认管线)。
		// 刻意不在 dispatch 侧赋值任何缝:在 dispatch 包做装配会重新引入
		// import 期单例装配(评审定案),缝归 socks5 包所有。
		socks5.Handler(downstream)
	default:
		handleRawConnection(downstream)
	}
}
