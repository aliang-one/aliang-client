package tls

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/match"
	"golang.org/x/net/http2/hpack"
)

// aliangHTTPHostRewriteTargets is the hardcoded provider -> upstream host map
// used when HTTP requests are forwarded to aliang.
var aliangHTTPHostRewriteTargets = map[string]string{
	"openai":    "api.openai.com",
	"anthropic": "api.anthropic.com",
	"claude":    "api.anthropic.com",
}

func rewriteAliangHTTPRequestHost(req *http.Request) bool {
	if req == nil {
		return false
	}

	originalHost := normalizedAliangHTTPHost(requestHostForRewrite(req))
	if originalHost == "" {
		return false
	}

	provider, rewrittenHost, ok := lookupAliangRewrittenHost(originalHost)
	if !ok || rewrittenHost == "" || rewrittenHost == originalHost {
		return false
	}

	req.Host = rewrittenHost
	if req.URL != nil && req.URL.Host != "" {
		req.URL.Host = rewrittenHost
	}

	logger.Debug("Aliang HTTP host rewritten: provider=", provider, " original=", originalHost, " rewritten=", rewrittenHost)
	return true
}

func rewriteAliangHTTP2HeaderFields(fields []hpack.HeaderField) ([]hpack.HeaderField, bool) {
	if len(fields) == 0 {
		return fields, false
	}

	originalHost, ok := getHTTP2HeaderFieldValue(fields, ":authority")
	if !ok || strings.TrimSpace(originalHost) == "" {
		originalHost, ok = getHTTP2HeaderFieldValue(fields, "host")
	}
	originalHost = normalizedAliangHTTPHost(originalHost)
	if !ok || originalHost == "" {
		return fields, false
	}

	provider, rewrittenHost, matched := lookupAliangRewrittenHost(originalHost)
	if !matched || rewrittenHost == "" || rewrittenHost == originalHost {
		return fields, false
	}

	rewritten := make([]hpack.HeaderField, 0, len(fields))
	for _, field := range fields {
		switch strings.ToLower(strings.TrimSpace(field.Name)) {
		case ":authority", "host":
			field.Value = rewrittenHost
		}
		rewritten = append(rewritten, field)
	}

	logger.Debug("Aliang HTTP/2 host rewritten: provider=", provider, " original=", originalHost, " rewritten=", rewrittenHost)
	return rewritten, true
}

func lookupAliangRewrittenHost(host string) (string, string, bool) {
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil || len(cfg.Customer.AIRules) == 0 {
		return "", "", false
	}

	normalizedHost := normalizedAliangHTTPHost(host)
	if normalizedHost == "" {
		return "", "", false
	}

	for provider, rule := range cfg.Customer.AIRules {
		if rule == nil || rule.Enble == nil || !*rule.Enble {
			continue
		}
		targetHost, ok := aliangHTTPHostRewriteTargets[strings.ToLower(strings.TrimSpace(provider))]
		if !ok {
			continue
		}
		for _, pattern := range rule.Include {
			if aliangHTTPHostMatches(pattern, normalizedHost) {
				return provider, targetHost, true
			}
		}
	}

	return "", "", false
}

func requestHostForRewrite(req *http.Request) string {
	if req == nil {
		return ""
	}
	if host := strings.TrimSpace(req.Host); host != "" {
		return host
	}
	if req.URL != nil {
		return strings.TrimSpace(req.URL.Host)
	}
	return ""
}

// aliangHTTPHostMatches 判定已归一化的 host 是否命中 pattern。
// pattern 侧保留 normalizedAliangHTTPHost 归一化：调用点传入的是 rule.Include
// 原始条目，可能为 URL / host:port 形态。
//
// 已委托共享内核 processor/match（语义唯一定义点：spec 第 3 节；
// 旧语义精确/后缀/"*." 保持不变，'*' 通配扩展为任意位置 glob）。
func aliangHTTPHostMatches(pattern, normalizedHost string) bool {
	return match.MatchDomain(normalizedAliangHTTPHost(pattern), normalizedHost)
}

func normalizedAliangHTTPHost(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	value = strings.TrimSuffix(value, ".")
	if value == "" {
		return ""
	}

	if strings.Contains(value, "://") {
		if parsedURL, err := url.Parse(value); err == nil && parsedURL.Host != "" {
			value = parsedURL.Host
		}
	}

	if host, port, err := net.SplitHostPort(value); err == nil {
		_ = port
		value = host
	} else if host, port, ok := strings.Cut(value, ":"); ok && port != "" && isAllDigits(port) {
		value = host
	}

	value = strings.Trim(value, "[]")
	return strings.TrimSpace(value)
}

func isAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}
