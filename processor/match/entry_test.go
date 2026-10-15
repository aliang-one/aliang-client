package match

import "testing"

func TestClassifyEntry(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		kind    EntryKind
		cond    string
		wantErr bool
	}{
		// 域名
		{"plain", "Example.COM", EntryDomain, "example.com", false},
		{"legacy-domains-prefix", "domains,google.com,proxy", EntryDomain, "google.com", false},
		{"legacy-domain-prefix", "domain,youtube.com", EntryDomain, "youtube.com", false},
		{"glob", "aliang.*", EntryDomain, "aliang.*", false},
		{"url-form", "//cdn.Epic.com/path", EntryDomain, "cdn.epic.com", false},
		{"scheme-form", "https://a.com/x", EntryDomain, "a.com", false},
		{"host-port", "example.com:8443", EntryDomain, "example.com", false},
		// 旧版兜底语义（parseProxyRuleDomain 复刻）
		{"legacy-clash-suffix", "DOMAIN-SUFFIX,example.com,proxy", EntryDomain, "example.com", false},
		{"legacy-multi-segment", "a.com,b.com,proxy", EntryDomain, "b.com", false},
		{"legacy-no-proxy-tail", "foo,bar", EntryDomain, "bar", false},
		// IP
		{"bare-ip", "1.2.3.4", EntryIP, "1.2.3.4", false},
		{"bare-cidr", "121.32.12.0/24", EntryIP, "121.32.12.0/24", false},
		{"bare-range", "1.2.3.1-1.2.3.254", EntryIP, "1.2.3.1-1.2.3.254", false},
		{"clash-cidr", "ip-cidr,121.32.12.0/24,proxy", EntryIP, "121.32.12.0/24", false},
		{"clash-range", "ip-range,1.2.3.1-1.2.3.254", EntryIP, "1.2.3.1-1.2.3.254", false},
		{"colon-cidr", "IP-CIDR:10.0.0.0/8", EntryIP, "10.0.0.0/8", false},
		{"colon-range", "ip-range:1.2.3.1-1.2.3.2", EntryIP, "1.2.3.1-1.2.3.2", false},
		{"from-to", "ip-from:1.2.3.1,IP-TO:1.2.3.254", EntryIP, "1.2.3.1-1.2.3.254", false},
		{"from-to-target", "ip-from:1.2.3.1,ip-to:1.2.3.254,proxy", EntryIP, "1.2.3.1-1.2.3.254", false},
		{"v6-cidr", "2001:db8::/32", EntryIP, "2001:db8::/32", false},
		{"ip-port-entry", "1.2.3.4:99", EntryIP, "1.2.3.4", false},
		{"bracketed-v6-port", "[2001:db8::1]:443", EntryIP, "2001:db8::1", false},
		// 非法
		{"empty", "   ", EntryDomain, "", true},
		{"bad-ip-cidr", "ip-cidr:abc", EntryIP, "", true},
		{"inverted-range", "ip-range:1.2.3.254-1.2.3.1", EntryIP, "", true},
		{"cross-family-range", "ip-range:1.2.3.1-::ff", EntryIP, "", true},
		{"empty-label", "a..b", EntryDomain, "", true},
		{"leading-dot", ".example.com", EntryDomain, "", true},
		{"trailing-dot-pattern", "example.com.", EntryDomain, "", true},
		{"bad-char", "my_server.com", EntryDomain, "", true},
		{"bad-char-regex", ".*\\.example\\.com", EntryDomain, "", true},
		{"keyword-missing-value", "ip-cidr:", EntryIP, "", true},
		{"from-missing-to", "ip-from:1.2.3.1", EntryIP, "", true},
		// 【加固】zone 端点（本行是计划外新增，来自 Task 2 质量审查的发现）
		{"zoned-range", "ip-range:fe80::1%eth0-fe80::ff", EntryIP, "", true},
		// 边界路径锁定（复审补充）
		{"domains-ip-value", "domains,1.2.3.4", EntryIP, "1.2.3.4", false},
		{"domain-v6-value", "domain,2001:db8::1", EntryIP, "2001:db8::1", false},
		{"empty-value-after-prefix", "domains,,proxy", EntryDomain, "", true},
		{"hostile-query-url", "http://a.com/x?y=<script>", EntryDomain, "a.com", false},
		{"uppercase-prefix", "DOMAINS,Google.COM", EntryDomain, "google.com", false},
		// —— geoip（Task 1）——
		{"geoip-colon-single", "geoip:CN", EntryGeoIP, "CN", false},
		{"geoip-colon-multi", "geoip:CN+HK+US", EntryGeoIP, "CN+HK+US", false},
		{"geoip-colon-case", "GEOIP:cn", EntryGeoIP, "CN", false},
		{"geoip-colon-spaces", " geoip: CN + HK ", EntryGeoIP, "CN+HK", false},
		{"geoip-colon-dedupe", "geoip:CN+CN", EntryGeoIP, "CN", false},
		{"geoip-clash-single", "GEOIP,CN,proxy", EntryGeoIP, "CN", false},
		{"geoip-clash-no-tail", "geoip,JP", EntryGeoIP, "JP", false},
		{"geoip-clash-noise-tail", "geoip,CN,proxy,extra", EntryGeoIP, "CN", false},
		{"geoip-colon-comma-multi-rejected", "geoip:CN,HK", EntryGeoIP, "", true},
		{"geoip-clash-comma-multi-rejected", "geoip,CN,HK", EntryGeoIP, "", true},
		{"geoip-bare-keyword-rejected", "geoip", EntryGeoIP, "", true},
		{"geoip-empty", "geoip:", EntryGeoIP, "", true},
		{"geoip-empty-plus", "geoip:CN++HK", EntryGeoIP, "", true},
		{"geoip-colon-trailing-plus", "geoip:CN+", EntryGeoIP, "", true},
		{"geoip-bad-code", "geoip:CHN", EntryGeoIP, "", true},
		{"geoip-colon-comma-rejected-mixed-case", "geoip:cn,HK", EntryGeoIP, "", true},
		{"geoip-clash-empty", "geoip,", EntryGeoIP, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, cond, err := ClassifyEntry(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ClassifyEntry(%q) expected error, got kind=%v cond=%q", tc.raw, kind, cond)
				}
				return
			}
			if err != nil {
				t.Fatalf("ClassifyEntry(%q) unexpected error: %v", tc.raw, err)
			}
			if kind != tc.kind || cond != tc.cond {
				t.Fatalf("ClassifyEntry(%q) = (%v, %q), want (%v, %q)", tc.raw, kind, cond, tc.kind, tc.cond)
			}
		})
	}
}
