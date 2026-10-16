package match

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strings"
)

// EntryKind 规则条目类型。
type EntryKind int

const (
	EntryDomain EntryKind = iota
	EntryIP
	EntryGeoIP
)

// String 返回 CanonicalRoutingRule.Type 使用的字符串。
func (k EntryKind) String() string {
	switch k {
	case EntryDomain:
		return "domain"
	case EntryIP:
		return "ip"
	case EntryGeoIP:
		return "geoip"
	default:
		return "unknown"
	}
}

// ClassifyEntry 将原始规则条目分类为 domain/ip 并返回归一化 condition。
// 关键字前缀识别先于逗号切分；尾段 ",proxy" 为 target 噪声（剥离忽略）。
// err 非空 = 条目非法，校验层直接报错。
func ClassifyEntry(raw string) (EntryKind, string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return EntryDomain, "", fmt.Errorf("条目为空")
	}
	lower := strings.ToLower(s)

	// —— 显式 IP 关键字：冒号风格（先于逗号切分）——
	if strings.HasPrefix(lower, "ip-cidr:") {
		return classifyIPEntry(s[len("ip-cidr:"):])
	}
	if strings.HasPrefix(lower, "ip-range:") {
		return classifyIPEntry(s[len("ip-range:"):])
	}
	if strings.HasPrefix(lower, "ip-from:") {
		cond := normalizeFromTo(s)
		if cond == s { // 未解析出 from/to 对
			return EntryIP, "", fmt.Errorf("ip-from 条目需要 ip-from:a,ip-to:b 形式，得到 %q", s)
		}
		return classifyIPEntry(cond)
	}
	if lower == "geoip" {
		return EntryGeoIP, "", fmt.Errorf("裸 geoip 条目无意义；请使用 geoip:CN 或 geoip:CN+HK+US")
	}
	if strings.HasPrefix(lower, "geoip:") {
		return classifyGeoIPColon(s[len("geoip:"):])
	}

	// —— Clash 风格逗号条目 ——
	if strings.Contains(s, ",") {
		parts := strings.Split(s, ",")
		first := strings.ToLower(strings.TrimSpace(parts[0]))
		switch first {
		case "domain", "domains":
			if len(parts) < 2 {
				return EntryDomain, "", fmt.Errorf("%s 前缀条目缺少域名", first)
			}
			return classifyDomainEntry(parts[1])
		case "ip-cidr", "ip-range":
			if len(parts) < 2 {
				return EntryIP, "", fmt.Errorf("%s 前缀条目缺少 IP 条件", first)
			}
			return classifyIPEntry(parts[1])
		case "geoip":
			if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
				return EntryGeoIP, "", fmt.Errorf("geoip 前缀条目缺少国家码")
			}
			code := strings.ToUpper(strings.TrimSpace(parts[1]))
			if !isValidCountryCode(code) {
				return EntryGeoIP, "", fmt.Errorf("非法国家码 %q（ISO 3166-1 两位大写字母）", parts[1])
			}
			for _, extra := range parts[2:] {
				if isValidCountryCode(strings.ToUpper(strings.TrimSpace(extra))) {
					return EntryGeoIP, "", fmt.Errorf("geoip 多国请用 + 分隔（如 geoip:CN+HK），得到 %q", s)
				}
			}
			return EntryGeoIP, code, nil
		}
		// 既有兜底（parseProxyRuleDomain 语义）：取尾段；尾段为 proxy 时取倒数第二段。
		// 完整保留旧解释，保证 DOMAIN-SUFFIX,x,proxy / a.com,b.com,proxy 等存量形态按旧值通过。
		fallback := strings.TrimSpace(parts[len(parts)-1])
		if strings.EqualFold(fallback, "proxy") && len(parts) >= 2 {
			fallback = strings.TrimSpace(parts[len(parts)-2])
		}
		return classifyAny(fallback)
	}

	return classifyAny(s)
}

// classifyAny 裸值分类：先试 IP 形态，再按域名结构校验。
func classifyAny(s string) (EntryKind, string, error) {
	lower := strings.ToLower(strings.TrimSpace(s))
	// 注意：不能把 ':' 排除在外——裸 IPv6 字面量含 ':'，必须能进入 IP 分支
	if lower != "" && !strings.Contains(lower, "*") {
		if isIPCond(lower) {
			return classifyIPEntry(lower)
		}
	}
	return classifyDomainEntry(s)
}

// isIPCond 判断是否为 IP 形态（单 IP / CIDR / 区间）。
func isIPCond(s string) bool {
	if _, err := netip.ParseAddr(s); err == nil {
		return true
	}
	if _, err := netip.ParsePrefix(s); err == nil {
		return true
	}
	_, _, ok := parseIPRange(s)
	return ok
}

// classifyIPEntry 校验 IP 条件并返回其规范形 condition。
// 规范形 = netip String() 序列化（v6 大小写/零压缩统一）+ 4-in-6 Unmap 归一，
// 保证「IP-CIDR:2001:DB8::/32」与「ip-cidr:2001:db8::/32」编译出同一 condition，
// 快照去重键（kind|cond）可剪除语义等价的重复条目。
func classifyIPEntry(cond string) (EntryKind, string, error) {
	norm, err := canonicalIPCond(cond)
	if err != nil {
		return EntryIP, "", err
	}
	return EntryIP, norm, nil
}

// canonicalIPCond 校验 IP 条件合法性并返回规范形（供校验层报具体错误 + 去重键一致性）。
// 4-in-6 归一策略（TUN 目的地址恒纯 v4/v6 形，netip 族序 16 字节排在 4 字节后，
// 4-in-6 形态规则对纯形目的永不命中，属「校验过但死规则」）：
//   - 单 IP / 区间端点：Unmap 归一（::ffff:1.2.3.4 → 1.2.3.4）——4-in-6 即该 v4 地址
//     （RFC 4291），归一无歧义且对用户最友好；
//   - CIDR 前缀含 4-in-6 地址：直接报错——前缀位数落在 128 位空间（如 /120），
//     无法无歧义映射到 32 位空间（bits-96 假设对 bits<96 形态不成立），请写 IPv4 形态。
func canonicalIPCond(cond string) (string, error) {
	cond = strings.TrimSpace(cond)
	if cond == "" {
		return "", fmt.Errorf("IP 条件为空")
	}
	if strings.Contains(cond, "-") {
		i := strings.IndexByte(cond, '-')
		from, errF := netip.ParseAddr(strings.TrimSpace(cond[:i]))
		to, errT := netip.ParseAddr(strings.TrimSpace(cond[i+1:]))
		if errF != nil || errT != nil {
			return "", fmt.Errorf("非法 IP 区间 %q", cond)
		}
		from, to = from.Unmap(), to.Unmap()
		// 【加固】区间端点同样拒绝带 zone 的地址（TUN 目的 IP 无 zone，
		// 带 zone 端点会形成「校验通过但永不命中」的静默失效）
		if from.Zone() != "" || to.Zone() != "" {
			return "", fmt.Errorf("IP 区间端点不支持作用域 %q", cond)
		}
		if from.Is4() != to.Is4() {
			return "", fmt.Errorf("IP 区间两端协议族不一致 %q", cond)
		}
		if from.Compare(to) > 0 {
			return "", fmt.Errorf("IP 区间起点大于终点 %q", cond)
		}
		return from.String() + "-" + to.String(), nil
	}
	if prefix, err := netip.ParsePrefix(cond); err == nil {
		if prefix.Addr().Is4In6() {
			return "", fmt.Errorf("CIDR 条件 %q 为 IPv4-mapped IPv6 形态；请改用 IPv4 形态（如 1.2.3.0/24）", cond)
		}
		return prefix.String(), nil
	}
	addr, err := netip.ParseAddr(cond)
	if err != nil {
		return "", fmt.Errorf("非法 IP 条件 %q（支持 单IP / CIDR / a-b 区间）", cond)
	}
	addr = addr.Unmap()
	if addr.Zone() != "" {
		return "", fmt.Errorf("IP 条件不支持作用域 %q", cond)
	}
	return addr.String(), nil
}

// classifyDomainEntry 域名条目：宽容归一化（URL/host:port 提取）+ 结构校验。
// 结构校验适用于所有域名条目（spec 3.3）：字符集、空标签、首尾点号；
// 不强制完整 DNS 合法性（长度/TLD/连字符规则）。
func classifyDomainEntry(v string) (EntryKind, string, error) {
	s := strings.TrimSpace(v)
	if s == "" {
		return EntryDomain, "", fmt.Errorf("域名为空")
	}
	// URL 形态提取 host（对齐前端与统计侧既有行为）
	if strings.Contains(s, "://") || strings.HasPrefix(s, "//") {
		if parsed, err := url.Parse(s); err == nil && parsed.Host != "" {
			s = parsed.Host
		}
	}
	// host:port 提取
	if host, _, err := net.SplitHostPort(s); err == nil && host != "" {
		s = host
	}
	s = strings.Trim(strings.TrimSpace(s), "[]")
	// 注意：不剥离尾点——首尾点号属结构非法（spec 3.3），由下方空标签检查拒绝；
	// 匹配时的 host 尾点由 MatchDomain 内部剥离，互不影响
	if s == "" {
		return EntryDomain, "", fmt.Errorf("域名为空")
	}
	// 提取后的 host 若为 IP 形态（含 host:port / [v6]:port 提取产物），归类为 IP 条件，
	// 避免编译成永不命中的死规则域名（spec §3.3）
	if lower := strings.ToLower(s); !strings.Contains(lower, "*") && isIPCond(lower) {
		return classifyIPEntry(lower)
	}
	lower := strings.ToLower(s)
	for _, r := range lower {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '-' && r != '*' {
			return EntryDomain, "", fmt.Errorf("域名含非法字符 %q（域名仅允许字母数字、点、连字符与 *；IDN 请使用 punycode）", string(r))
		}
	}
	for _, label := range strings.Split(lower, ".") {
		if label == "" {
			return EntryDomain, "", fmt.Errorf("域名含空标签（如 a..b 或首尾点号）")
		}
	}
	return EntryDomain, lower, nil
}

// classifyGeoIPColon 解析冒号风格 geoip 条目（多国 + 分隔）。
// 多国段去重后按字典序排列归一（输入排列序不进入 condition）——
// 「geoip:HK+CN」与「geoip:CN+HK」产出同一 condition，去重键（kind|cond）一致。
func classifyGeoIPColon(rem string) (EntryKind, string, error) {
	rem = strings.TrimSpace(rem)
	if rem == "" {
		return EntryGeoIP, "", fmt.Errorf("geoip 条件为空")
	}
	if strings.Contains(rem, ",") {
		return EntryGeoIP, "", fmt.Errorf("geoip 多国请用 + 分隔（如 geoip:CN+HK），得到 %q", rem)
	}
	seen := map[string]struct{}{}
	norm := make([]string, 0, 4)
	for _, seg := range strings.Split(rem, "+") {
		c := strings.ToUpper(strings.TrimSpace(seg))
		if c == "" {
			return EntryGeoIP, "", fmt.Errorf("geoip 条目含空国家码 %q", rem)
		}
		if !isValidCountryCode(c) {
			return EntryGeoIP, "", fmt.Errorf("非法国家码 %q（ISO 3166-1 两位大写字母）", seg)
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		norm = append(norm, c)
	}
	sort.Strings(norm)
	return EntryGeoIP, strings.Join(norm, "+"), nil
}

// isValidCountryCode 判断两位大写 ISO 3166-1 alpha-2 国家码。
func isValidCountryCode(c string) bool {
	return len(c) == 2 && c[0] >= 'A' && c[0] <= 'Z' && c[1] >= 'A' && c[1] <= 'Z'
}
