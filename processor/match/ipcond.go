package match

import (
	"net/netip"
	"strings"
)

// MatchIPCond 判定 ip 是否命中 IP 条件 cond（spec 3.2）。
// 支持：单 IP（/32|/128）、CIDR（v4/v6）、闭区间 "a-b"、
// 显式关键字前缀 ip-cidr:/ip-range:（大小写不敏感）、ip-from:a,ip-to:b。
// 校验层负责拒绝非法条目；本函数对非法输入一律不命中（运行时兜底）。
func MatchIPCond(cond, ip string) bool {
	cond = NormalizeIPCond(cond)
	if cond == "" {
		return false
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return false
	}
	if from, to, ok := parseIPRange(cond); ok {
		return from.Compare(addr) <= 0 && addr.Compare(to) <= 0
	}
	if prefix, err := netip.ParsePrefix(cond); err == nil {
		return prefix.Contains(addr)
	}
	if single, err := netip.ParseAddr(cond); err == nil {
		return single.Compare(addr) == 0
	}
	return false
}

// NormalizeIPCond 归一化 IP 条件：去空白、剥关键字前缀、
// "ip-from:a,ip-to:b" → "a-b"。无法识别时原样返回（交由校验/匹配失败）。
func NormalizeIPCond(cond string) string {
	s := strings.TrimSpace(cond)
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "ip-cidr:"):
		return strings.TrimSpace(s[len("ip-cidr:"):])
	case strings.HasPrefix(lower, "ip-range:"):
		return strings.TrimSpace(s[len("ip-range:"):])
	case strings.HasPrefix(lower, "ip-from:"):
		return normalizeFromTo(s)
	}
	return s
}

// normalizeFromTo 解析 "ip-from:A,ip-to:B[,<尾段噪声>]"（大小写不敏感）为 "A-B"。
// 未知逗号段（如尾段 target）忽略。
func normalizeFromTo(s string) string {
	var from, to string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		lower := strings.ToLower(part)
		switch {
		case strings.HasPrefix(lower, "ip-from:") && from == "":
			from = strings.TrimSpace(part[len("ip-from:"):])
		case strings.HasPrefix(lower, "ip-to:") && to == "":
			to = strings.TrimSpace(part[len("ip-to:"):])
		}
	}
	if from == "" || to == "" {
		return s
	}
	return from + "-" + to
}

// parseIPRange 解析 "A-B" 闭区间。要求两端均为合法 IP、同协议族、from ≤ to。
func parseIPRange(s string) (from, to netip.Addr, ok bool) {
	i := strings.IndexByte(s, '-')
	if i <= 0 || i == len(s)-1 {
		return from, to, false
	}
	f, err1 := netip.ParseAddr(strings.TrimSpace(s[:i]))
	t, err2 := netip.ParseAddr(strings.TrimSpace(s[i+1:]))
	if err1 != nil || err2 != nil {
		return from, to, false
	}
	if f.Is4() != t.Is4() {
		return from, to, false // 跨协议族
	}
	if f.Compare(t) > 0 {
		return from, to, false // from > to（校验层提前报错，这里兜底不命中）
	}
	return f, t, true
}
