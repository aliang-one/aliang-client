package rules

import "aliang.one/nursorgate/processor/match"

// MatchDomain 委托共享内核（该包当前无生产调用者，同步语义防止将来复活时漂移）。
func MatchDomain(pattern, domain string) bool {
	return match.MatchDomain(pattern, domain)
}

// IsDomainAllowed returns true if domain matches any pattern in allowlist.
func IsDomainAllowed(domain string, allowlist []string) bool {
	if domain == "" || len(allowlist) == 0 {
		return false
	}

	for _, pattern := range allowlist {
		if MatchDomain(pattern, domain) {
			return true
		}
	}
	return false
}
