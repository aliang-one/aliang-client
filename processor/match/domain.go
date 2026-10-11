// Package match 提供规则条目的共享匹配内核。
// 语义唯一定义点：docs/superpowers/specs/2026-10-11-rule-syntax-glob-ip-design.md 第 3 节。
// 热路径（每连接调用），禁止使用 regexp。
package match

import "strings"

// MatchDomain 判定 host 是否命中 pattern。
// 均先小写、去空白、去尾点后比较。
//   - pattern 不含 '*'：精确匹配，或后缀匹配（"example.com" 命中 "www.example.com"）
//   - pattern 含 '*'：glob 匹配，每个 '*' 匹配 ≥1 个任意字符（可跨点号）
func MatchDomain(pattern, host string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if pattern == "" || host == "" {
		return false
	}
	if !strings.Contains(pattern, "*") {
		if pattern == host {
			return true
		}
		return strings.HasSuffix(host, "."+pattern)
	}
	return globMatch(pattern, host)
}

// globMatch 含 '*' 的通配匹配：'*' 匹配 ≥1 个任意字符（含点号）。
// 贪心回溯；遇 '*' 立即消费 1 字符以保证「至少 1 字符」语义
// （因此裸域不被 "*.epic.com" 命中、尾 '*' 不能匹配空串）。
func globMatch(pattern, host string) bool {
	p, h := 0, 0
	starP := -1
	for h < len(host) {
		switch {
		case p < len(pattern) && pattern[p] == host[h]:
			p++
			h++
		case p < len(pattern) && pattern[p] == '*':
			starP = p
			p++
			h++ // '*' 至少消费 1 字符
		case starP != -1:
			h++ // 回溯：让最近的 '*' 多吞一个字符
			p = starP + 1
		default:
			return false
		}
	}
	// host 已耗尽：pattern 必须恰好走完（剩余 '*' 也需要 ≥1 字符，不能吞空）
	return p == len(pattern)
}
