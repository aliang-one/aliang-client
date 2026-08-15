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
