package tcp

import (
	"net/netip"
	"strings"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/meshproto"
	"aliang.one/nursorgate/processor/config"
)

func shouldForceAliangRoute(metadata *M.Metadata) bool {
	if metadata == nil || metadata.DstPort != uint16(config.DefaultHTTPProxyPort) {
		return false
	}

	if metadata.DstIP.IsValid() && metadata.DstIP.IsLoopback() {
		return true
	}

	host := strings.ToLower(strings.TrimSpace(metadata.HostName))
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}

	if ip, err := netip.ParseAddr(host); err == nil && ip.IsLoopback() {
		return true
	}

	return false
}

// meshOfflineRedirectForAI AI 流量离线改道判定(计划 Task 12,design §2
// 行11/§9.4;信号缝见 mesh_offline.go):shouldForceAliangRoute 命中的连接
// (56432 回环,类别推断恒为 ai)在「离线信号真 + AI 类离线兜底勾选」时,
// 从 RouteToALiang 改判 mesh 兜底节点(经 mesh 转发到兜底节点,B 出其
// 隧道),返回兜底地址。三态:
//   - 在线(信号 false/未接线)→ 不改道,在线行为零变化;
//   - 离线但 AI 兜底未勾选/总开关关/Addr 空 → 不改道,保持
//     RouteToALiang(离线下隧道拨号必失败,错误如实上抛由 AI 客户端重试
//     策略收口);
//   - 离线且兜底门控通过(共享助手 config.MeshFallbackActiveForClass:
//     总开关+Enable+ai∈Classes+Addr 非空)→ 改道。
//
// 类别不另行推断:本判定只在 shouldForceAliangRoute 命中后消费,56432
// 回环判据(looksLikeAITraffic 同源口径)恒推断 ai,直接以 ClassAI 查门控。
func meshOfflineRedirectForAI() (addr string, ok bool) {
	if !meshOfflineActive() {
		return "", false
	}
	return config.MeshFallbackActiveForClass(config.GetGlobalConfig(), meshproto.ClassAI)
}
