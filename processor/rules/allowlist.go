package rules

import "strings"

// MatchDomain checks if a domain matches a pattern.
// Supports:
// - Exact match: example.com
// - Plain suffix: example.com matches www.example.com
// - Wildcard subdomain: *.example.com (matches a.b.example.com, not example.com)
func MatchDomain(pattern, domain string) bool {
	if pattern == "" || domain == "" {
		return false
	}

	pattern = strings.ToLower(strings.TrimSpace(pattern))
	domain = strings.ToLower(strings.TrimSpace(domain))

	if pattern == domain {
		return true
	}

	if strings.HasSuffix(domain, "."+pattern) {
		return true
	}

	if strings.HasPrefix(pattern, "*.") {
		suffix := strings.TrimPrefix(pattern, "*.")
		if suffix == "" {
			return false
		}
		return strings.HasSuffix(domain, "."+suffix)
	}

	return false
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
