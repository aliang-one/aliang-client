package statistic

import "testing"

func TestAIDomainMatches(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		host    string
		want    bool
	}{
		{"glob pattern hits host", "aliang.*", "aliang.co.uk", true},
		{"ip cidr entry never hits domain host", "121.32.12.0/24", "example.com", false},
		{"exact host", "openai.com", "openai.com", true},
		{"suffix host", "openai.com", "api.openai.com", true},
		{"unrelated host", "openai.com", "example.com", false},
		{"wildcard subdomain hit", "*.epic.com", "www.epic.com", true},
		{"wildcard deep subdomain hit", "*.epic.com", "sub.www.epic.com", true},
		{"wildcard bare domain miss", "*.epic.com", "epic.com", false},
		{"wildcard unrelated miss", "*.epic.com", "www.example.com", false},
		{"empty pattern", "", "example.com", false},
		{"empty host", "example.com", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := aiDomainMatches(tt.pattern, tt.host); got != tt.want {
				t.Fatalf("aiDomainMatches(%q, %q) = %v, want %v", tt.pattern, tt.host, got, tt.want)
			}
		})
	}
}

func TestAIDomainPatternNormalization(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		// IP 条目不得被首个 '/' 截断或剥端口（修复点）
		{"cidr kept intact", "121.32.12.0/24", "121.32.12.0/24"},
		{"cidr with surrounding space", " 121.32.12.0/24 ", "121.32.12.0/24"},
		{"ip range kept intact", "1.2.3.1-1.2.3.254", "1.2.3.1-1.2.3.254"},
		{"ip from/to collapsed to range", "ip-from:1.2.3.1,ip-to:1.2.3.2", "1.2.3.1-1.2.3.2"},
		{"ip cidr keyword prefix stripped", "ip-cidr:10.0.0.0/8", "10.0.0.0/8"},
		// 大小写关键字：依赖 ai_domains.go:224 先小写化的顺序，锁住
		{"uppercase-cidr-keyword", "IP-CIDR:10.0.0.0/8", "10.0.0.0/8"},
		{"clash ip cidr entry", "ip-cidr,121.32.12.0/24,proxy", "121.32.12.0/24"},
		{"bare ip unchanged", "1.2.3.4", "1.2.3.4"},
		{"bare v6 ip unchanged", "2001:db8::1", "2001:db8::1"},
		{"v6 cidr kept intact", "2001:db8::/32", "2001:db8::/32"},
		// 域名 / wildcard / URL / 端口回归（行为与现状一致）
		{"wildcard preserved", "*.epic.com", "*.epic.com"},
		{"wildcard ip-ish suffix untouched", "*.1.2.3.4", "*.1.2.3.4"},
		{"url host extracted", "https://a.com/x", "a.com"},
		{"host port stripped", "host:8443", "host"},
		{"domain host port stripped", "a.com:8443", "a.com"},
		{"trailing dot trimmed", "example.com.", "example.com"},
		{"plain domain unchanged", "example.com", "example.com"},
		{"whitespace and case normalized", "  EXAMPLE.com  ", "example.com"},
		{"empty collapses", "   ", ""},
		// 非法 IP 条目：ClassifyEntry 报错，保持既有截断路径
		{"invalid ip entry keeps legacy path", "ip-cidr:abc", "ip-cidr"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeAIDomainPattern(tt.raw); got != tt.want {
				t.Fatalf("normalizeAIDomainPattern(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}

	// 幂等性：ai_activity.go:234 生产路径会对已归一化 pattern 再归一化一次，属性必须锁定
	idempotent := []struct {
		name string
		raw  string
	}{
		{"ip-entry-idempotent", "121.32.12.0/24"},           // normalize 两次结果一致
		{"clash-ip-idempotent", "ip-cidr,10.0.0.0/8,proxy"}, // 同上
	}
	for _, tt := range idempotent {
		t.Run(tt.name, func(t *testing.T) {
			first := normalizeAIDomainPattern(tt.raw)
			second := normalizeAIDomainPattern(first)
			if first == "" {
				t.Fatalf("normalizeAIDomainPattern(%q) 首次归一化结果为空", tt.raw)
			}
			if second != first {
				t.Fatalf("normalizeAIDomainPattern 二次归一化不幂等：%q → %q → %q", tt.raw, first, second)
			}
		})
	}
}
