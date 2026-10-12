package mirror

import "testing"

// TestMatchesAnyDomain 锁定 MatchesAnyDomain 对外语义。
// 入口顺序：MatchesAnyDomain(host, domains)；normalizeHost（去端口+小写）在入口完成，
// hostMatches 的模式语义见其注释。本文件为 mirror 包首个测试。
func TestMatchesAnyDomain(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		domains []string
		want    bool
	}{
		// —— 精确 ——
		{"exact", "example.com", []string{"example.com"}, true},
		// —— 后缀（裸域 pattern 命中子域）——
		{"suffix-sub", "www.example.com", []string{"example.com"}, true},
		{"suffix-deep", "a.b.example.com", []string{"example.com"}, true},
		{"no-cross-boundary", "badexample.com", []string{"example.com"}, false},
		// —— *. 通配（命中子域，显式排除裸域）——
		{"star-prefix-sub", "cdn.epic.com", []string{"*.epic.com"}, true},
		{"star-prefix-deep", "a.b.epic.com", []string{"*.epic.com"}, true},
		{"star-prefix-bare-excluded", "epic.com", []string{"*.epic.com"}, false},
		// —— 大小写与端口（normalizeHost）——
		{"case-insensitive", "CDN.Epic.COM", []string{"*.epic.com"}, true},
		{"port-stripped", "example.com:443", []string{"example.com"}, true},
		{"case-and-port", "CDN.Epic.COM:443", []string{"*.epic.com"}, true},
		// —— 多 pattern 与空值 ——
		{"multi-pattern-second-hit", "example.com", []string{"other.com", "example.com"}, true},
		{"multi-pattern-miss", "example.com", []string{"other.com", "*.other.com"}, false},
		{"empty-domain-list", "example.com", nil, false},
		{"empty-host", "", []string{"example.com"}, false},
		{"empty-pattern-in-list", "example.com", []string{""}, false},

		// —— glob 新能力（委托共享内核 processor/match 后）：
		// '*' 可出现在任意位置，每个 '*' 匹配 ≥1 个任意字符（可跨点号）——
		{"glob-tail-cross-tld", "aliang.co.uk", []string{"aliang.*"}, true},
		{"glob-head", "cdnepic.com", []string{"*epic.com"}, true},
		{"glob-mid", "cdn.epic.com", []string{"cdn*.com"}, true},
		// 共享内核语义：host 侧另去尾点（normalizeHost 不处理，MatchDomain 剥离）
		{"host-trailing-dot", "example.com.", []string{"example.com"}, true},
		// 不对称：pattern 侧尾点运行时不剥离（上游结构校验拒绝），故永不匹配
		{"pattern-trailing-dot-never-matches", "example.com", []string{"example.com."}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchesAnyDomain(tc.host, tc.domains); got != tc.want {
				t.Errorf("MatchesAnyDomain(%q, %v) = %v, want %v", tc.host, tc.domains, got, tc.want)
			}
		})
	}
}
