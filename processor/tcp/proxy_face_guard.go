// proxy_face_guard.go — 代理面目标守卫(2026-11-24 审计):统一入口的代理面
// (HTTP CONNECT/明文、socks5)只以「会话租约」为门,没有目标 ACL;回环/私网
// 目标被特权网关进程直接代拨,56431/56433 看到的来源恒为回环——管理面所有
// 「回环即信任」的判定对局域网客户端全部成立(跨机实证:经 socks5 让 liang-dev
// 代连 127.0.0.1:56431,200 带数据)。
//
// 守卫:非回环来源的代理面连接,不得访问本机回环/内网目标,也不得使用
// 「AI base-URL」形态(目标是回环 56432——那会带着本机登录态进平台隧道,
// 等于冒用本机账号的 AI 额度)。回环来源不受限(本机应用/CLI 的既有用法);
// TUN、SSH 入口等其它入站不带 IngressProxyFace 标记,同样不受限。
package tcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/processor/config"
)

// ErrProxyFaceLocalDestination 非回环来源的代理面连接试图访问本机/内网目标。
// 哨兵导出:入站面(socks5/HTTP)在成功应答之前用它回拒,日志/排障按
// errors.Is 识别。
var ErrProxyFaceLocalDestination = errors.New("proxy face: non-loopback client may not reach loopback/private destinations")

// cgnatRange 是 RFC6598 运营商级 NAT 段(100.64.0.0/10):netip 不把它算进
// IsPrivate,但它同样是「只在本机/内网有意义」的寻址范围。
var cgnatRange = netip.MustParsePrefix("100.64.0.0/10")

// CheckProxyFaceDestination 代理面目标守卫。返回 nil 表示放行。
func CheckProxyFaceDestination(md *M.Metadata) error {
	if md == nil || !md.IngressProxyFace {
		return nil
	}
	// 回环来源(含 IPv4-mapped 形态)不受限;来源未知(非网络连接)按本机对待。
	if !md.SrcIP.IsValid() || md.SrcIP.WithZone("").Unmap().IsLoopback() {
		return nil
	}
	if md.DstIP.IsValid() && isLocalOnlyDestination(md.DstIP) {
		return fmt.Errorf("%w (dst=%s:%d)", ErrProxyFaceLocalDestination, md.DstIP, md.DstPort)
	}
	// 目标以域名给出且尚未解析成 IP:只认 AI base-URL 形态的回环主机名。
	if md.DstPort == config.DefaultHTTPProxyPort && isLoopbackHostName(md.HostName) {
		return fmt.Errorf("%w (dst=%s:%d)", ErrProxyFaceLocalDestination, md.HostName, md.DstPort)
	}
	return nil
}

// isLocalOnlyDestination 报告目标地址是否「只在本机/内网有意义」:回环、
// RFC1918/ULA 私网、链路本地(含 169.254.169.254 这类元数据服务)、组播、
// 未指定地址、CGNAT 段。
func isLocalOnlyDestination(ip netip.Addr) bool {
	ip = ip.WithZone("").Unmap()
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified() ||
		cgnatRange.Contains(ip)
}

// isLoopbackHostName 报告主机名是否是回环字面量或 "localhost"。
func isLoopbackHostName(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "localhost" {
		return true
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap().IsLoopback()
	}
	return false
}

// ---- mesh-exit 注入流的目标校验 ----
//
// 转发端(forward_enable=true)此前对已准入对端开放任意目标,含本机回环服务:
// 56431 管理 API 的信任边界是「来源为回环」,而代拨发生在网关进程里,对端经
// mesh-exit 让转发端代连 127.0.0.1:56431 即可改写它的配置。校验分两层:
// 字面量/localhost 在入站口(inbound/ssh)Accept 前判;域名在这里解析后判。

// ErrInjectedLocalDestination mesh-exit 注入流的目标指向/解析到本机回环
// (非 AI base-URL 形态)或链路本地等「只对本机有意义」的地址。
var ErrInjectedLocalDestination = errors.New("mesh-injected: target is a loopback/link-local address")

// InjectedTargetAllowed 报告 mesh-exit 注入流的目标地址是否允许:
//   - 回环:仅放行「AI base-URL 形态」(目标=回环 56432)——出口链路委托的
//     合法载荷,链的末端设备据此在自己的管线上走 aliang 分支出网;
//   - 链路本地、未指定、组播:拒绝;
//   - 其余(公网、转发端局域网):放行——节点转发的本意。
func InjectedTargetAllowed(ip netip.Addr, port uint16) bool {
	ip = ip.WithZone("").Unmap()
	switch {
	case ip.IsLoopback():
		return port == config.DefaultHTTPProxyPort
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(), ip.IsUnspecified(), ip.IsMulticast():
		return false
	}
	return true
}

// vetInjectedHost 解析 mesh-exit 注入流的域名目标并逐地址校验;全部通过则
// 返回钉住的地址(调用方写进 DstIP,拨号不再二次解析——杜绝 DNS 重绑定:
// 校验一次解析、拨号用的就是校验过的那个地址;与 CONNECT 面取 ips[0] 同款)。
// 解析失败原样返回错误(拨号本来也会失败)。
func vetInjectedHost(ctx context.Context, host string, port uint16) (netip.Addr, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return netip.Addr{}, err
	}
	var pinned netip.Addr
	for _, a := range addrs {
		ip, ok := netip.AddrFromSlice(a.IP)
		if !ok {
			continue
		}
		ip = ip.WithZone("").Unmap()
		if !InjectedTargetAllowed(ip, port) {
			return netip.Addr{}, fmt.Errorf("%w (injected target %s resolves to %s)", ErrInjectedLocalDestination, host, ip)
		}
		if !pinned.IsValid() {
			pinned = ip
		}
	}
	if !pinned.IsValid() {
		return netip.Addr{}, fmt.Errorf("resolve injected target %s: no usable addresses", host)
	}
	return pinned, nil
}
