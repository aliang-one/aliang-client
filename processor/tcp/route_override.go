package tcp

import (
	"net/netip"
	"strings"

	M "aliang.one/nursorgate/inbound/tun/metadata"
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

// (旧 AI 离线改道判定已随出口链路改造移除,2026-11-12:改道由开关驱动、
// 常驻语义,落点见 mesh_delegate.go meshDelegateEligible/dialViaMeshDelegate
// 与 handler.go 的两处消费点。)
