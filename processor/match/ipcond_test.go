package match

import "testing"

func TestMatchIPCond(t *testing.T) {
	cases := []struct {
		name string
		cond string
		ip   string
		want bool
	}{
		{"single-v4", "1.2.3.4", "1.2.3.4", true},
		{"single-v4-miss", "1.2.3.4", "1.2.3.5", false},
		{"cidr-hit", "121.32.12.0/24", "121.32.12.199", true},
		{"cidr-miss", "121.32.12.0/24", "121.32.13.1", false},
		{"cidr-keyword", "ip-cidr:121.32.12.0/24", "121.32.12.1", true},
		{"cidr-keyword-upper", "IP-CIDR:121.32.12.0/24", "121.32.12.1", true},
		{"range-hit-low", "1.2.3.1-1.2.3.254", "1.2.3.1", true},
		{"range-hit-mid", "1.2.3.1-1.2.3.254", "1.2.3.100", true},
		{"range-hit-high", "1.2.3.1-1.2.3.254", "1.2.3.254", true},
		{"range-miss-above", "1.2.3.1-1.2.3.254", "1.2.3.255", false},
		{"range-keyword", "ip-range:1.2.3.1-1.2.3.254", "1.2.3.2", true},
		{"from-to-keyword", "ip-from:1.2.3.1,ip-to:1.2.3.254", "1.2.3.9", true},
		{"range-inverted-no-match", "1.2.3.254-1.2.3.1", "1.2.3.9", false},
		{"range-cross-family-no-match", "1.2.3.1-::ff", "::2", false},
		{"single-v6", "2001:db8::1", "2001:db8::1", true},
		{"cidr-v6", "2001:db8::/32", "2001:db8::dead", true},
		{"range-v6", "2001:db8::1-2001:db8::ff", "2001:db8::80", true},
		{"range-single-point", "1.2.3.4-1.2.3.4", "1.2.3.4", true},
		{"cidr-zero", "0.0.0.0/0", "8.8.8.8", true},
		{"empty-cond", "", "1.2.3.4", false},
		{"empty-ip", "1.2.3.4", "", false},
		{"bad-ip", "1.2.3.4", "not-an-ip", false},
		{"bad-cond", "abc", "1.2.3.4", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchIPCond(tc.cond, tc.ip); got != tc.want {
				t.Fatalf("MatchIPCond(%q, %q) = %v, want %v", tc.cond, tc.ip, got, tc.want)
			}
		})
	}
}
