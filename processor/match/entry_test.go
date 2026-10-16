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
		// —— 【加固】IPv4-mapped IPv6 归一（minor#11）——
		// 4-in-6 单 IP / 区间端点 Unmap 归一为 v4（TUN 目的地址恒纯形，
		// 16 字节 4-in-6 形态因 netip 族序永不命中纯 v4 目的，属「校验过但死规则」）；
		// CIDR 前缀含 4-in-6 直接报错（前缀位数落在 128 位空间，如 /120，
		// 无法无歧义映射到 32 位空间，请写 IPv4 形态）
		{"m4in6-single-unmapped", "::ffff:1.2.3.4", EntryIP, "1.2.3.4", false},
		{"m4in6-cidr-rejected", "ip-cidr:::ffff:1.2.3.0/120", EntryIP, "", true},
		{"m4in6-cidr-bare-rejected", "::ffff:1.2.3.0/120", EntryIP, "", true},
		{"m4in6-range-normalized", "ip-range:::ffff:1.2.3.1-::ffff:1.2.3.254", EntryIP, "1.2.3.1-1.2.3.254", false},
		{"m4in6-range-mixed-normalized", "ip-from:::ffff:1.2.3.1,ip-to:1.2.3.254", EntryIP, "1.2.3.1-1.2.3.254", false},
		// —— 【加固】condition 规范形（minor#12）：快照去重键（kind|cond）——
		// 对语义等价的条目（大小写/零压缩写法不同）必须产出同一 condition
		{"uppercase-v6-canonicalized", "IP-CIDR:2001:DB8::/32", EntryIP, "2001:db8::/32", false},
		{"v6-zero-compression-canonicalized", "2001:0db8:0000::1", EntryIP, "2001:db8::1", false},
		// —— 【加固】geoip 多国排序归一（minor#12 伴随）——
		{"geoip-colon-multi-sorted", "geoip:HK+CN", EntryGeoIP, "CN+HK", false},
		// —— 尾段动作词噪声剥离（HIGH#1）：集合 {proxy, direct, reject, no-resolve}，大小写不敏感 ——
		// 尾段动作词不再落成匹配字面主机 direct/reject/no-resolve 的死域名规则；
		// 前端 RulesSettings.vue 逗号分支保持同一集合（红线：两边集合必须一致）。
		{"action-tail-direct", "example.com,direct", EntryDomain, "example.com", false},
		{"action-tail-direct-clash-form", "DOMAIN-SUFFIX,example.com,DIRECT", EntryDomain, "example.com", false},
		{"action-tail-reject", "example.com,REJECT", EntryDomain, "example.com", false},
		{"action-tail-no-resolve", "example.com,no-resolve", EntryDomain, "example.com", false},
		{"action-tail-mixed-case", "example.com,Direct", EntryDomain, "example.com", false},
		// 剥离后对目标段做完整校验（非「先剥离后免检」）：剥离出的段非法照样报错
		{"action-tail-stripped-segment-validated", "example..com,direct", EntryDomain, "", true},
		// 单次剥离语义锁定：尾段非动作词 → 取尾段（中间段动作词不参与，与 a.com,b.com,proxy 旧语义同构）
		{"action-tail-middle-ignored", "example.com,direct,extra", EntryDomain, "extra", false},
		// geoip 分支互涉锁定：direct/no-resolve 非两位国家码 → 噪声段 → 单国合法（行为与修复前一致）
		{"geoip-clash-direct-tail", "geoip,CN,direct", EntryGeoIP, "CN", false},
		{"geoip-clash-action-noise-multi", "geoip,cn,no-resolve,extra", EntryGeoIP, "CN", false},
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
