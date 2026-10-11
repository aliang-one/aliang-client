# 规则匹配语法扩展（glob 通配符 + IP 条件）实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 域名规则支持 `*` 任意位置 glob 通配（可跨点号），路由层支持 IP 条件（单 IP / CIDR / 区间 / 关键字 / Clash 前缀），四套匹配实现收敛到共享内核，新语法非法时显式报错。

**Architecture:** 新建 `processor/match` 共享匹配内核（纯函数、无 regexp），四套既有匹配实现全部委托；`snapshot.go` 编译器按 `ClassifyEntry` 分类产出 `Type=domain`/`Type=ip` 规则打通 IP 通道；配置校验面接入条目校验终结静默失效。

**Tech Stack:** Go 1.25（netip，标准库测试，无 testify in processor 包）、Vue2 风格 Options API（前端校验）、语义权威定义见 spec：`docs/superpowers/specs/2026-10-11-rule-syntax-glob-ip-design.md`（**实现前先通读 spec 第 3、4 节**）。

**约定：**
- 所有提交信息全中文「新增：/修复：/重构：」风格，不带 feat:/fix: 前缀。
- 每个 commit 页脚附：`Co-Authored-By: Claude Code <noreply@anthropic.com>`。
- 测试风格：processor 包用标准库 `testing` + 表驱动 + `t.Run`（范例 `processor/watcher/aliang_http_host_rewrite_test.go:99`）。
- 全仓 `go test ./...` 有预存 flake（agent_ai 子进程超时类，高并行偶败）——失败时先单包复跑再判断。

---

### Task 0: worktree 环境准备

**Files:** 无代码改动（创建 worktree、拷贝文档）

- [ ] **Step 1: 创建 worktree 分支**（在主工作区 `/Users/mac/MyProgram/GoProgram/nursor/alianggate` 执行）

```bash
git stash -u   # ⚠️ 若有并行会话在跑，确认其不会同时 stash
git worktree add ../alianggate-rule-syntax -b feat/rule-syntax-glob-ip
git stash pop
```

- [ ] **Step 2: 拷贝 spec 与本计划进 worktree**

```bash
mkdir -p ../alianggate-rule-syntax/docs/superpowers/specs ../alianggate-rule-syntax/docs/superpowers/plans
cp docs/superpowers/specs/2026-10-11-rule-syntax-glob-ip-design.md ../alianggate-rule-syntax/docs/superpowers/specs/
cp docs/superpowers/plans/2026-10-11-rule-syntax-glob-ip.md ../alianggate-rule-syntax/docs/superpowers/plans/
```

- [ ] **Step 3: 提交文档**

```bash
cd ../alianggate-rule-syntax
git add docs/superpowers/
git commit -m "$(cat <<'EOF'
新增：规则语法扩展（glob 通配符 + IP 条件）设计文档与实现计划

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 1: 共享内核——域名匹配 `MatchDomain`

**Files:**
- Create: `processor/match/domain.go`
- Test: `processor/match/domain_test.go`

- [ ] **Step 1: 写失败测试**（矩阵覆盖 spec 3.1 全部行 + 边界）

```go
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
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./processor/match/ -run TestMatchDomain -v`
Expected: 编译失败（`MatchDomain` 未定义）

- [ ] **Step 3: 实现**

```go
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
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./processor/match/ -v`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add processor/match/
git commit -m "$(cat <<'EOF'
新增：共享域名匹配内核（精确/后缀/*.<通配回归兼容 + 任意位置 glob）

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: 共享内核——IP 条件 `MatchIPCond`

**Files:**
- Create: `processor/match/ipcond.go`
- Test: `processor/match/ipcond_test.go`

- [ ] **Step 1: 写失败测试**

```go
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
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./processor/match/ -run TestMatchIPCond -v`
Expected: 编译失败（`MatchIPCond` 未定义）

- [ ] **Step 3: 实现**

```go
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
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./processor/match/ -v`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add processor/match/
git commit -m "$(cat <<'EOF'
新增：共享 IP 条件匹配（单 IP/CIDR/区间 + ip-cidr/ip-range/ip-from 关键字）

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: 共享内核——条目分类器 `ClassifyEntry`

**Files:**
- Create: `processor/match/entry.go`
- Test: `processor/match/entry_test.go`

关键约定（spec 3.2/3.3 + 评审澄清）：**关键字前缀识别先于逗号切分**；尾段 `,proxy` 视为 target 噪声剥离（与 `parseProxyRuleDomain` 既有约定一致）；域名条目统一做结构校验（字符集/空标签/首尾点号，**适用于纯域名**）；URL 形态（`://`、`//` 前缀）与 `host:port` 宽容提取（对齐 `processor/statistic/ai_domains.go:225-259` 既有 normalizer 行为，保障存量兼容）。

- [ ] **Step 1: 写失败测试**

```go
package match

import "testing"

func TestClassifyEntry(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		kind      EntryKind
		cond      string
		wantErr   bool
	}{
		// 域名
		{"plain", "Example.COM", EntryDomain, "example.com", false},
		{"legacy-domains-prefix", "domains,google.com,proxy", EntryDomain, "google.com", false},
		{"legacy-domain-prefix", "domain,youtube.com", EntryDomain, "youtube.com", false},
		{"glob", "aliang.*", EntryDomain, "aliang.*", false},
		{"url-form", "//cdn.Epic.com/path", EntryDomain, "cdn.epic.com", false},
		{"scheme-form", "https://a.com/x", EntryDomain, "a.com", false},
		{"host-port", "example.com:8443", EntryDomain, "example.com", false},
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
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./processor/match/ -run TestClassifyEntry -v`
Expected: 编译失败（`ClassifyEntry`/`EntryKind` 未定义）

- [ ] **Step 3: 实现**

```go
package match

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// EntryKind 规则条目类型。
type EntryKind int

const (
	EntryDomain EntryKind = iota
	EntryIP
)

// String 返回 CanonicalRoutingRule.Type 使用的字符串。
func (k EntryKind) String() string {
	if k == EntryIP {
		return "ip"
	}
	return "domain"
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
		cond := strings.TrimSpace(s[len("ip-cidr:"):])
		return EntryIP, cond, validateIPCond(cond)
	}
	if strings.HasPrefix(lower, "ip-range:") {
		cond := strings.TrimSpace(s[len("ip-range:"):])
		return EntryIP, cond, validateIPCond(cond)
	}
	if strings.HasPrefix(lower, "ip-from:") {
		cond := normalizeFromTo(s)
		if cond == s { // 未解析出 from/to 对
			return EntryIP, "", fmt.Errorf("ip-from 条目需要 ip-from:a,ip-to:b 形式，得到 %q", s)
		}
		return EntryIP, cond, validateIPCond(cond)
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
			cond := strings.TrimSpace(parts[1])
			return EntryIP, cond, validateIPCond(cond)
		}
		// 既有兜底：尾段 proxy → 取倒数第二段
		last := strings.ToLower(strings.TrimSpace(parts[len(parts)-1]))
		if last == "proxy" && len(parts) >= 2 {
			return classifyAny(strings.Join(parts[:len(parts)-1], ","))
		}
	}

	return classifyAny(s)
}

// classifyAny 裸值分类：先试 IP 形态，再按域名结构校验。
func classifyAny(s string) (EntryKind, string, error) {
	lower := strings.ToLower(strings.TrimSpace(s))
	// 注意：不能把 ':' 排除在外——裸 IPv6 字面量含 ':'，必须能进入 IP 分支
	if lower != "" && !strings.Contains(lower, "*") {
		if isIPCond(lower) {
			return EntryIP, lower, validateIPCond(lower)
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

// validateIPCond 校验 IP 条件合法性（供校验层报具体错误）。
func validateIPCond(cond string) error {
	cond = strings.TrimSpace(cond)
	if cond == "" {
		return fmt.Errorf("IP 条件为空")
	}
	if strings.Contains(cond, "-") {
		i := strings.IndexByte(cond, '-')
		from, errF := netip.ParseAddr(strings.TrimSpace(cond[:i]))
		to, errT := netip.ParseAddr(strings.TrimSpace(cond[i+1:]))
		if errF != nil || errT != nil {
			return fmt.Errorf("非法 IP 区间 %q", cond)
		}
		if from.Is4() != to.Is4() {
			return fmt.Errorf("IP 区间两端协议族不一致 %q", cond)
		}
		if from.Compare(to) > 0 {
			return fmt.Errorf("IP 区间起点大于终点 %q", cond)
		}
		return nil
	}
	if _, err := netip.ParsePrefix(cond); err == nil {
		return nil
	}
	addr, err := netip.ParseAddr(cond)
	if err != nil {
		return fmt.Errorf("非法 IP 条件 %q（支持 单IP / CIDR / a-b 区间）", cond)
	}
	if addr.Zone() != "" {
		return fmt.Errorf("IP 条件不支持作用域 %q", cond)
	}
	return nil
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
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./processor/match/ -v`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add processor/match/
git commit -m "$(cat <<'EOF'
新增：规则条目分类器（domain/ip 识别 + 结构校验 + 宽容归一化）

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: 路由引擎委托共享内核

**Files:**
- Modify: `processor/routing/decision_engine.go:207-266`（matchDomain、matchIP）

- [ ] **Step 1: 先跑既有测试建立基线**

Run: `go test ./processor/routing/ -v`
Expected: PASS（记录基线）

- [ ] **Step 2: 委托改造**（保持函数名与签名，调用点零改动）

```go
// T030: matchDomain 委托共享内核（语义：精确 + 后缀 + glob 通配，见 processor/match）。
func matchDomain(pattern, domain string) bool {
	return match.MatchDomain(pattern, domain)
}

// T031: matchIP 委托共享内核（语义：CIDR / 单 IP / 区间，见 processor/match）。
func matchIP(cidr, ip string) bool {
	return match.MatchIPCond(cidr, ip)
}
```

保留原注释块中的示例文档，import 增加 `"aliang.one/nursorgate/processor/match"`（go.mod module 为 `aliang.one/nursorgate`，已核实）。删除原函数体（`decision_engine.go:213-239` 与 `:245-266`）。

- [ ] **Step 3: 跑测试确认通过**

Run: `go test ./processor/routing/ ./processor/tcp/ -v`
Expected: PASS（旧语义回归不变；`geoip` stub 不动）

- [ ] **Step 4: 提交**

```bash
git add processor/routing/decision_engine.go
git commit -m "$(cat <<'EOF'
重构：路由引擎域名/IP 匹配委托共享内核 processor/match

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: snapshot 编译通道——产出 Type=ip 规则

**Files:**
- Modify: `processor/routing/snapshot.go:283-462`（compileCanonicalRoutingFromRuntimeInputs、collectAIDomains、collectProxyRuleDomains、parseProxyRuleDomain）
- Test: `processor/routing/snapshot_test.go`（追加）

- [ ] **Step 1: 跑既有编译测试建立基线**

Run: `go test ./processor/routing/ -v`
Expected: PASS（既有 4 个 `TestRuntimeSnapshot_*` 测试即基线；`-run TestCompile` 匹配不到任何现有测试，勿用）

- [ ] **Step 2: 重构编译入口**——按 `match.ClassifyEntry` 分类产规则，域/IP 去重按 `(kind, cond)`：

```go
// compileCanonicalRoutingFromRuntimeInputs 内（伪结构，具体以现函数为准改造）：
// 1) aiEntries := 原始 AI 条目（collectAIDomains 改为 collectAIEntries，返回原始 []string，
//    去掉内部 dedupeNormalizedDomains——去重下沉到 appendRules 按 kind+cond 进行）
// 2) proxyEntries := cfg.Customer.ProxyRules 原始条目
// 3) appendRules(entries, idPrefix, target)：
for _, raw := range entries {
    kind, cond, err := match.ClassifyEntry(raw)
    if err != nil || cond == "" {
        continue // 校验层已拦截非法条目；编译期兜底跳过（与现状宽松一致）
    }
    key := kind.String() + "|" + cond
    if _, dup := seen[key]; dup {
        continue
    }
    seen[key] = struct{}{}
    ruleType := model.RuleTypeDomain
    if kind == match.EntryIP {
        ruleType = model.RuleTypeIP
    }
    rules = append(rules, config.CanonicalRoutingRule{
        ID:        fmt.Sprintf("%s_%d", idPrefix, len(rules)),
        Type:      string(ruleType),
        Condition: cond,
        Enabled:   true,
        Target:    string(target),
    })
}
```

4) `parseProxyRuleDomain` 保留为薄包装（仅供既有测试与可能的调用方）：`kind, cond, err := match.ClassifyEntry(raw); return cond, err == nil && kind == match.EntryDomain`。若 grep 确认无其他调用方且既有测试改写为直接测 ClassifyEntry，可删除该函数（二选一，执行时决定，优先保留包装减小 diff）。
5) `dedupeNormalizedDomains` 若无其他调用方则删除（先 `grep -rn dedupeNormalizedDomains`）。

- [ ] **Step 3: 追加编译测试**（跟随 `snapshot_test.go` 既有 setup 模式）

新增用例：
- AI include 含 `aliang.*` → 产出 `Type=domain, Condition=aliang.*` 规则
- proxy_rules 含 `ip-cidr,121.32.12.0/24,proxy` → 产出 `Type=ip, Condition=121.32.12.0/24` 规则（**回归锁定：不再误编译为 domain**）
- 混合条目 → ID 前缀保持 `ai_allowlist_N` / `proxy_rule_N` 连续编号
- 端到端：`CompileRuntimeSnapshotFromRuntimeInputs` → `DecideRouteFromSnapshot(ctx{Domain:"", IP:"121.32.12.5"})` 命中该 IP 规则的 target；`IP:"121.32.13.1"` 不命中
- 存量回归：纯 `domains,x` 配置产出与改造前完全一致（既有 4 个测试即回归）

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./processor/routing/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add processor/routing/
git commit -m "$(cat <<'EOF'
新增：snapshot 编译通道支持 IP 规则（修复 ip-cidr 条目被误编译为域名）

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: mirror 委托 + 首个测试

**Files:**
- Modify: `processor/mirror/matcher.go:46-78`（hostMatches）
- Test: `processor/mirror/matcher_test.go`（新建，mirror 包首个测试）

- [ ] **Step 1: 先写行为基线测试**（按**当前实现**语义，先于改造跑绿）

覆盖：精确、后缀、`*.` 命中子域、`*.` 不命中裸域、大小写、normalizeHost 去端口（`example.com:443`）。表驱动同 Task 1 风格，测 `MatchesAnyDomain` 入口。

- [ ] **Step 2: 跑基线测试确认通过**

Run: `go test ./processor/mirror/ -v`
Expected: PASS

- [ ] **Step 3: 委托改造**——`hostMatches` 函数体替换为：

```go
func hostMatches(pattern, host string) bool {
	return match.MatchDomain(pattern, host)
}
```

`normalizeHost`（去端口+小写）保留不动。`MatchesAnyDomain` 不变。

- [ ] **Step 4: 跑测试确认基线仍绿**（glob `*`≥1 字符性质使 `*.` 不命中裸域语义自动保持）

Run: `go test ./processor/mirror/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add processor/mirror/
git commit -m "$(cat <<'EOF'
重构：mirror 域名匹配委托共享内核并补首个测试基线

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Host 重写委托

**Files:**
- Modify: `processor/watcher/aliang_http_host_rewrite.go:120-136`（aliangHTTPHostMatches）

- [ ] **Step 1: 跑既有测试基线**

Run: `go test ./processor/watcher/ -run TestAliangHTTPHostMatches -v`
Expected: PASS

- [ ] **Step 2: 委托改造**

```go
func aliangHTTPHostMatches(pattern, normalizedHost string) bool {
	return match.MatchDomain(normalizedAliangHTTPHost(pattern), normalizedHost)
}
```

（**pattern 侧必须保留 `normalizedAliangHTTPHost` 归一化**：调用点 `:98` 传入的是 `rule.Include` 原始条目，可能为 URL / host:port 形态——今天能匹配，裸委托会静默丢失该能力，与 ClassifyEntry 接受 URL 形态相矛盾。host 侧调用点已归一化，MatchDomain 内部再归一化无害。函数内原 `*.` 分支与 exact-or-suffix 分支整体删除。）

- [ ] **Step 3: 跑测试确认通过**（既有 glob/suffix 用例回归）

Run: `go test ./processor/watcher/ -v`
Expected: PASS

- [ ] **Step 4: 提交**

```bash
git add processor/watcher/aliang_http_host_rewrite.go
git commit -m "$(cat <<'EOF'
重构：AI 加速 Host 重写匹配委托共享内核

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: 统计侧委托 + 修 CIDR 截断变形

**Files:**
- Modify: `processor/statistic/ai_domains.go:225-276`（normalize 函数与 aiDomainMatches）

- [ ] **Step 1: 写失败测试**（新建 `processor/statistic/ai_domains_test.go`，注意该包其他测试的 TestMain 约定——先看同包既有测试文件有没有）

用例：
- `aiDomainMatches("aliang.*", "aliang.co.uk") == true`（glob 生效）
- `aiDomainMatches("121.32.12.0/24", "example.com") == false`（IP 条目不命中域名 host）
- normalize 函数对 `121.32.12.0/24` **保持原样**（修复：现状截断成 `121.32.12.0`）
- normalize 对 `*.epic.com`、`https://a.com/x`、`host:8443` 行为与现状一致（回归）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./processor/statistic/ -run TestAIDomain -v`
Expected: FAIL（截断变形用例失败）

- [ ] **Step 3: 改造**

- normalize 函数（`:225-259`）：在 `strings.IndexRune(value, '/')` 截断（`:240`）与 `SplitHostPort`（`:244`）之前，先用 `match.ClassifyEntry(value)` 判断：若为 `EntryIP` 且 err==nil，直接返回归一化 condition（跳过 `/` 截断与端口剥离）。
- `aiDomainMatches`（`:262-276`）函数体替换为 `return match.MatchDomain(pattern, host)`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./processor/statistic/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add processor/statistic/
git commit -m "$(cat <<'EOF'
修复：统计侧 IP 条目被斜杠截断变形 + 域名匹配委托共享内核

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: 死路径最小同步（allowlist）

**Files:**
- Modify: `processor/rules/allowlist.go:10-35`（MatchDomain）

- [ ] **Step 1: 改造**

```go
// MatchDomain 委托共享内核（该包当前无生产调用者，同步语义防止将来复活时漂移）。
func MatchDomain(pattern, domain string) bool {
	return match.MatchDomain(pattern, domain)
}
```

- [ ] **Step 2: 编译验证**（该函数无测试；`go vet` 兜底）

Run: `go build ./processor/rules/ && go vet ./processor/rules/`
Expected: 无输出（成功）

**范围说明（有意不做）**：spec §5 第 9 行原列的 `common/model/routing_config.go` ip 校验分支扩展**不实施**——该路径（`NewRoutingRulesConfigFromJSON`）零生产调用者，扩展死校验只增维护面；将来复活时应直接改用 `processor/match.MatchIPCond`。（spec §5 第 9 行已同步改为「不改动」。）

- [ ] **Step 3: 提交**

```bash
git add processor/rules/allowlist.go
git commit -m "$(cat <<'EOF'
重构：allowlist 死路径匹配同步共享内核语义

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: 配置校验面——终结静默失效

**Files:**
- Modify: `processor/config/types.go:800-813`（validateCustomerEditableSurface 的两个循环）
- Test: 该校验的既有测试所在文件（`grep -rn "validateCustomerEditableSurface\|cannot be empty" processor/config/*_test.go` 定位），追加用例

- [ ] **Step 1: 写失败测试**

- `ai_rules.x.include` 含 `ip-cidr:abc` → Validate 返回错误且信息含条目下标
- `proxy_rules` 含 `a..b` → 返回错误
- `proxy_rules` 含 `ip-from:1.2.3.254,ip-to:1.2.3.1`（倒置）→ 返回错误
- `traffic_mirror.domains` 含 `a..b` → `TrafficMirrorConfig.Validate` 返回错误；合法 glob/IP 条目通过
- 合法混合（`domains,google.com` / `aliang.*` / `ip-cidr,121.32.12.0/24,proxy`）→ 通过
- 存量回归：既有合法用例全部仍通过

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./processor/config/ -v`
Expected: 新用例 FAIL

- [ ] **Step 3: 改造**（两个循环各加一行校验）

```go
for i := range rule.Include {
    rule.Include[i] = strings.TrimSpace(rule.Include[i])
    if rule.Include[i] == "" {
        return fmt.Errorf("customer.ai_rules.%s.include[%d] cannot be empty", provider, i)
    }
    if _, _, err := match.ClassifyEntry(rule.Include[i]); err != nil {
        return fmt.Errorf("customer.ai_rules.%s.include[%d] invalid %q: %v", provider, i, rule.Include[i], err)
    }
}

for i := range c.Customer.ProxyRules {
    c.Customer.ProxyRules[i] = strings.TrimSpace(c.Customer.ProxyRules[i])
    if c.Customer.ProxyRules[i] == "" {
        return fmt.Errorf("customer.proxy_rules[%d] cannot be empty", i)
    }
    if _, _, err := match.ClassifyEntry(c.Customer.ProxyRules[i]); err != nil {
        return fmt.Errorf("customer.proxy_rules[%d] invalid %q: %v", i, c.Customer.ProxyRules[i], err)
    }
}
```

**traffic_mirror.domains 同样接入**（spec 4.4，缺了它 mirror 输入仍是静默失效路径）：在 `TrafficMirrorConfig.Validate`（`types.go:299` 附近）现有 domains 非空校验旁，对每个 domain 调用 `match.ClassifyEntry`，err 非空返回 `traffic_mirror.domains[%d] invalid %q: %v`（含条目原文；结构体/字段名以现场代码为准）。

import 增加 `"aliang.one/nursorgate/processor/match"`（go.mod module 为 `aliang.one/nursorgate`，已核实）。

- [ ] **Step 4: 跑测试确认通过**（注意：既有测试数据里若有今天「侥幸通过」的畸形条目，此时会暴露——逐个判断是修测试数据还是属预期拒绝，属预期拒绝的更新为期望错误）

Run: `go test ./processor/config/ ./app/http/... -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add processor/config/
git commit -m "$(cat <<'EOF'
新增：客户可编辑配置的规则条目校验（非法 IP/glob 显式报错）

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: 前端实时校验 + i18n

**Files:**
- Modify: `app/website/src/components/settings/RulesSettings.vue`（proxy_rules 文本域与 AI include 输入的校验）
- Modify: `app/website/src/i18n/zh.js`、`app/website/src/i18n/en.js`

- [ ] **Step 1: i18n 键**（zh/en 键集必须一致，`i18n.test.js` 会强制）

zh.js（放在 `rules_serverError` 附近，`:637` 一带）：
```js
rules_entryInvalidChars: '条目含非法字符（域名仅允许字母数字、点、连字符与 *；IP 条件请检查格式）',
rules_entryEmptyLabel: '域名不能包含空标签（如 a..b、首尾点号）',
rules_ipInvalid: 'IP 条件无效（支持 单IP / CIDR(如 121.32.12.0/24) / 区间(如 1.2.3.1-1.2.3.254)）',
```
en.js 对应英文翻译（键名相同）。

- [ ] **Step 2: 校验函数**（加在 RulesSettings.vue `<script>` 的工具函数区，`normalizeIncludeEntry` 附近）

```js
// 与后端 processor/match ClassifyEntry 对齐的前端校验（区间数值顺序由后端强校验）。
const ENTRY_CHARS_RE = /^[a-z0-9.*-]+$/;
const IPV4_RE = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/;

function parseIPFamily(s) {
  const m = String(s).match(IPV4_RE);
  if (m && m.slice(1).every((o) => Number(o) <= 255)) return 4;
  if (String(s).includes(':') && /^[0-9a-fA-F:.]+$/.test(s)) return 6;
  return 0;
}

function isValidIPCond(cond) {
  if (cond.includes('/')) {
    const slash = cond.indexOf('/');
    const fam = parseIPFamily(cond.slice(0, slash));
    const bits = cond.slice(slash + 1);
    if (!/^\d{1,3}$/.test(bits)) return false;
    const max = fam === 4 ? 32 : 128;
    return fam > 0 && Number(bits) <= max;
  }
  if (cond.includes('-')) {
    const dash = cond.indexOf('-');
    const f = parseIPFamily(cond.slice(0, dash));
    return f > 0 && f === parseIPFamily(cond.slice(dash + 1));
  }
  return parseIPFamily(cond) > 0;
}

// 返回 '' 表示合法，否则返回 i18n 错误文案。
function validateRuleEntry(raw) {
  let s = String(raw || '').trim();
  if (!s) return '';
  const lower = s.toLowerCase();
  if (lower.startsWith('ip-cidr:')) return isValidIPCond(s.slice(8).trim()) ? '' : t('rules_ipInvalid');
  if (lower.startsWith('ip-range:')) return isValidIPCond(s.slice(9).trim()) ? '' : t('rules_ipInvalid');
  if (lower.startsWith('ip-from:')) {
    const from = /ip-from:\s*([0-9a-fA-F:.]+)/.exec(s);
    const to = /ip-to:\s*([0-9a-fA-F:.]+)/.exec(s);
    return from && to ? '' : t('rules_ipInvalid');
  }
  if (lower.startsWith('ip-cidr,') || lower.startsWith('ip-range,')) {
    const second = s.split(',')[1];
    return second && isValidIPCond(second.trim()) ? '' : t('rules_ipInvalid');
  }
  if (parseIPFamily(lower) > 0 || isValidIPCond(lower)) return '';
  if (lower.includes(',')) {
    const parts = lower.split(',');
    const first = parts[0].trim();
    if (first === 'domain' || first === 'domains') s = parts[1] || '';
    else if (parts[parts.length - 1].trim() === 'proxy') s = parts[parts.length - 2] || '';
    else s = parts[parts.length - 1];
  }
  if (s.includes('://') || s.startsWith('//')) {
    try { s = new URL(s.includes('://') ? s : `https:${s}`).hostname || s; } catch { /* 交给字符校验报错 */ }
  }
  s = String(s).trim(); // 不剥离尾点——与后端一致：尾点属结构非法，交由空标签检查拒绝
  if (!ENTRY_CHARS_RE.test(s)) return t('rules_entryInvalidChars');
  if (s.split('.').some((label) => !label)) return t('rules_entryEmptyLabel');
  return '';
}
```

- [ ] **Step 3: 接入 UI**——先读组件现状（proxy_rules 文本域、AI include 输入的保存链路），在保存前对每条调用 `validateRuleEntry`，任一非法则显示错误并阻止提交；输入时实时提示（debounce 可选）。错误展示复用组件现有错误文案渲染机制（`isValidServer` 同款模式）。

- [ ] **Step 4: 前端测试与构建**

Run: 在 `app/website/` 下执行项目既有测试命令（`i18n.test.js` 覆盖键集一致性）与构建命令（先 `cat package.json` 确认 scripts）
Expected: 测试 PASS、构建成功

- [ ] **Step 5: 提交**

```bash
git add app/website/
git commit -m "$(cat <<'EOF'
新增：规则条目前端实时校验与中英文错误提示

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 12: 文档更新

**Files:**
- Modify: `README.md:168` 附近、`README.zh-CN.md:163` 附近（domains 匹配说明段）
- Modify: `docs/tutorials/zh_CN/usage-guide.md:648` 附近（12.1 规则格式）、`docs/tutorials/en/usage-guide.md` 对应段
- Modify: `docs/tutorials/zh_CN/usage-guide.import.json`（**仅 zh_CN 有 import.json**，由 `docs/tutorials/scripts/md_to_mdx.py` 消费；en 侧只有 usage-guide.md，无 import.json）

- [ ] **Step 1: 统一语法说明文本**（中英各一份，以下为中文版要点）

- 域名：`example.com`（精确+子域）、`*.epic.com`（子域通配，不含裸域）、`aliang.*`（任意 TLD，含 `aliang.co.uk`）、`*epic.com`（前缀通配，含 `cdnepic.com`）；`*` 匹配 ≥1 个任意字符、可跨点号
- IP：`1.2.3.4`、`121.32.12.0/24`、`1.2.3.1-1.2.3.254`、`ip-cidr:`、`ip-range:`、`ip-from:…,ip-to:…`、Clash 风格 `ip-cidr,x/24,proxy`；支持 IPv6
- **生效范围**：IP 条件在 TUN TCP 路径按真实目的 IP 匹配；无 SNI 的 TLS、UDP 流量与本地 HTTP 代理入站不经过规则引擎，IP 条件不生效
- 非法条目（坏 IP、空标签等）保存时会被明确拒绝

- [ ] **Step 2: 提交**

```bash
git add README.md README.zh-CN.md docs/tutorials/
git commit -m "$(cat <<'EOF'
新增：规则语法文档（glob 通配符与 IP 条件写法及生效范围）

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 13: 全量验证与验收核对

- [ ] **Step 1: 全仓构建 + vet + 测试**

```bash
go build ./... && go vet ./...
go test ./... 2>&1 | tail -40
```
Expected: 构建成功；测试全绿（agent_ai 相关偶发超时属预存 flake——单包复跑 `go test ./app/http/services/ -run <名>` 判断）

- [ ] **Step 2: 验收清单逐条核对**（spec 第 8 节）

- [ ] `aliang.*` 命中 `aliang.co.uk`；`*.epic.com` 不命中裸域；`*epic.com` 同命中 `cdnepic.com` 与 `cdn.epic.com`（Task 1 测试）
- [ ] `ip-cidr,121.32.12.0/24,proxy` 编译为 Type=ip 且端到端命中（Task 5 测试）
- [ ] `ip-from:…,ip-to:…` 校验与区间判定；倒置区间被拒（Task 3/10 测试）
- [ ] 非法条目显式报错、不再静默（Task 10 测试）
- [ ] 四套调用点同一内核（Task 4/6/7/8/9 diff 均为委托）
- [ ] 存量默认配置零改动通过校验与编译（Task 5/10 回归用例）

- [ ] **Step 3: 手工冒烟（可选但推荐）**

启动本机 agent，通过设置页写入一条 `ip-cidr,121.32.12.0/24,proxy` 与 `aliang.*`，观察 `curl 127.0.0.1:56433/api/agent/status` 路由快照与实际分流。

- [ ] **Step 4: 汇总报告**——向用户报告：提交列表、测试结果、验收核对表、遗留事项（UDP/无 SNI 盲区、geoip、跨仓协同为非目标）。
