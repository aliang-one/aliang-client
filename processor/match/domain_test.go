package match

import "testing"

func TestMatchDomain(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		host    string
		want    bool
	}{
		// —— 无 * ：精确 + 后缀（现状语义，回归锁定）——
		{"exact", "example.com", "example.com", true},
		{"suffix-sub", "example.com", "www.example.com", true},
		{"suffix-deep", "example.com", "a.b.example.com", true},
		{"no-cross-boundary", "example.com", "badexample.com", false},
		{"case-insensitive", "EXAMPLE.com", "WWW.Example.COM", true},
		// —— *.< 前缀通配（现状语义，回归锁定）——
		{"star-prefix-sub", "*.epic.com", "cdn.epic.com", true},
		{"star-prefix-deep", "*.epic.com", "a.b.epic.com", true},
		{"star-prefix-bare-excluded", "*.epic.com", "epic.com", false},
		// —— 新 glob 语义：* 跨级、任意位置 ——
		{"tail-cross-labels", "aliang.*", "aliang.xxx", true},
		{"tail-multi-tld", "aliang.*", "aliang.co.uk", true},
		{"tail-bare-host-excluded", "aliang.*", "aliang", false},
		{"head-no-dot", "*epic.com", "cdnepic.com", true},
		{"head-with-dot", "*epic.com", "cdn.epic.com", true},
		{"head-bare-excluded", "*epic.com", "epic.com", false},
		{"mid-label", "cdn*.com", "cdnabc.com", true},
		{"mid-cross-dot", "cdn*.com", "cdn.epic.com", true},
		{"mid-miss", "cdn*.com", "ab.com", false},
		// —— 边界 ——
		{"empty-pattern", "", "example.com", false},
		{"empty-host", "example.com", "", false},
		{"host-trailing-dot", "example.com", "example.com.", true},
		{"star-needs-one-char", "a*", "a", false},
		{"star-consumes-one", "a*", "ab", true},
		{"whole-host-star", "*", "anything.example", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchDomain(tc.pattern, tc.host); got != tc.want {
				t.Fatalf("MatchDomain(%q, %q) = %v, want %v", tc.pattern, tc.host, got, tc.want)
			}
		})
	}
}
