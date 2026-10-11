package mirror

import (
	"strings"

	"aliang.one/nursorgate/processor/match"
)

// normalizeHost strips the port and lowercases the host.
func normalizeHost(raw string) string {
	h := strings.ToLower(strings.TrimSpace(raw))
	if h == "" {
		return ""
	}
	if host, _, err := splitHostPort(h); err == nil {
		return host
	}
	return h
}

// splitHostPort splits host:port. Returns the host part.
func splitHostPort(hostPort string) (string, string, error) {
	h, p, err := parseHostPort(hostPort)
	if err != nil {
		return hostPort, "", err
	}
	return h, p, nil
}

// parseHostPort is a simple host:port parser.
func parseHostPort(hostPort string) (string, string, error) {
	lastColon := strings.LastIndex(hostPort, ":")
	if lastColon < 0 {
		return hostPort, "", nil
	}
	host := hostPort[:lastColon]
	port := hostPort[lastColon+1:]
	// Handle IPv6 brackets
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	return host, port, nil
}

// hostMatches checks if a host matches a domain pattern.
// Patterns:
//   - "exact.domain.com" — exact match
//   - "*.domain.com" — matches any subdomain of domain.com (not domain.com itself)
//   - "suffix.domain.com" — suffix match (matches foo.suffix.domain.com and suffix.domain.com)
//
// 已委托共享内核 processor/match（语义唯一定义点：spec 第 3 节；
// 旧语义精确/后缀/"*." 保持不变，'*' 通配扩展为任意位置 glob）。
func hostMatches(pattern, host string) bool {
	return match.MatchDomain(pattern, host)
}

// MatchesAnyDomain checks whether host matches any of the provided domain patterns.
func MatchesAnyDomain(host string, domains []string) bool {
	normalized := normalizeHost(host)
	if normalized == "" {
		return false
	}
	for _, pattern := range domains {
		if hostMatches(pattern, normalized) {
			return true
		}
	}
	return false
}
