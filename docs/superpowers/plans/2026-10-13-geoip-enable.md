# GeoIP 接通实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** geoip 条目语法（`geoip:CN`/`geoip:CN+HK+US`/`GEOIP,CN,proxy`）全链路可用：分类→编译 Type=geoip→mmdb 查找真实命中；数据从用户 COS 异步分发；start/tray/core 三条启动路径接入；修掉 geoip 条目误编译为域名规则的全类问题。

**Architecture:** `processor/match` 新增 `EntryGeoIP` 分类；快照编译三分支；`decision_engine` 的 `matchGeoIP` 换真实现（`geoipLookup` 可注入 seam + 大小写适配）；`processor/geoip` 拆 `LoadLocal`/`EnsureDownloaded`（下载移出锁）+ COS 默认 URL；`cmd` 共享编排函数接入三路径；镜像校验拒绝 geoip、统计守卫扩展、死路径 checkGeoIP 删除、UI+文档同步。

**Tech Stack:** Go 1.25（oschwald/geoip2-golang v1.9.0 已在 go.mod）、标准库 testing（processor 包）、Vue Options API（前端）。

**语义权威：** spec `docs/superpowers/specs/2026-10-13-geoip-enable-design.md`（v3）——实现前通读 §3（语法）与 §4（架构）。易错三件套：多国分隔符是 **`+`** 不是逗号；快照层 condition 会被**小写化**（匹配侧必须适配）；**下载绝不持锁**。

**约定：** 全中文提交「新增：/修复：/重构：」+ `Co-Authored-By: Claude Code <noreply@anthropic.com>` 页脚；processor 包标准库 testing 表驱动；`go test ./...` 预存 flake（services 包 agent_ai 子进程超时）失败先单包复跑。

---

### Task 0: worktree 环境准备

**Files:** 无代码改动

- [ ] **Step 1: 创建 worktree 分支**（主工作区 `/Users/mac/MyProgram/GoProgram/nursor/alianggate` 执行）

```bash
git stash -u   # ⚠️ 确认无并行会话同时 stash
git worktree add ../alianggate-geoip -b feat/geoip-enable
git stash pop
```

- [ ] **Step 2: 拷贝 spec 与本计划进 worktree 并提交**

```bash
mkdir -p ../alianggate-geoip/docs/superpowers/specs ../alianggate-geoip/docs/superpowers/plans
cp docs/superpowers/specs/2026-10-13-geoip-enable-design.md ../alianggate-geoip/docs/superpowers/specs/
cp docs/superpowers/plans/2026-10-13-geoip-enable.md ../alianggate-geoip/docs/superpowers/plans/
cd ../alianggate-geoip
git add docs/superpowers/
git commit -m "$(cat <<'EOF'
新增：GeoIP 接通设计文档与实现计划

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 1: `processor/match` 条目分类——`EntryGeoIP`

**Files:**
- Modify: `processor/match/entry.go`
- Test: `processor/match/entry_test.go`

- [ ] **Step 1: 写失败测试**（追加到 TestClassifyEntry 矩阵；勿动既有行）

```go
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
		{"geoip-bad-code", "geoip:CHN", EntryGeoIP, "", true},
		{"geoip-lowercase-code-rejected", "geoip:cn,HK", EntryGeoIP, "", true},
		{"geoip-clash-empty", "geoip,", EntryGeoIP, "", true},
```

注意 `geoip-clash-comma-multi-rejected`（`geoip,CN,HK`）：第二段后出现任一 `^[A-Z]{2}$` 段 = 逗号多国尝试 → 拒绝；而 `geoip,CN,proxy,extra` 的 `proxy`/`extra` 都不是国家码 → 噪声忽略（spec §3.1 边界定义）。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./processor/match/ -run TestClassifyEntry -v`
Expected: 新 geoip 行 FAIL（`EntryGeoIP` 未定义/走到域名兜底）

- [ ] **Step 3: 实现**（entry.go 四处改动）

(a) 常量与 String：
```go
const (
	EntryDomain EntryKind = iota
	EntryIP
	EntryGeoIP
)

func (k EntryKind) String() string {
	switch k {
	case EntryIP:
		return "ip"
	case EntryGeoIP:
		return "geoip"
	}
	return "unknown" // 保持既有防御默认值（勿改为 "domain"）
}
```

(b) `ClassifyEntry` 内、`ip-from:` 冒号分支之后、逗号切分之前，加：
```go
	if lower == "geoip" {
		return EntryGeoIP, "", fmt.Errorf("裸 geoip 条目无意义；请使用 geoip:CN 或 geoip:CN+HK+US")
	}
	if strings.HasPrefix(lower, "geoip:") {
		return classifyGeoIPColon(s[len("geoip:"):])
	}
```

(c) 逗号切分的 switch 加 case（与 domain/ip-cidr 等 case 并列）：
```go
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
```

(d) 文件尾新增两个 helper（`isValidCountryCode` 用字符判断，不引 regexp）：
```go
// classifyGeoIPColon 解析冒号风格 geoip 条目（多国 + 分隔）。
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
	for _, c := range strings.Split(rem, "+") {
		c = strings.ToUpper(strings.TrimSpace(c))
		if c == "" {
			return EntryGeoIP, "", fmt.Errorf("geoip 条目含空国家码 %q", rem)
		}
		if !isValidCountryCode(c) {
			return EntryGeoIP, "", fmt.Errorf("非法国家码 %q（ISO 3166-1 两位大写字母）", c)
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		norm = append(norm, c)
	}
	return EntryGeoIP, strings.Join(norm, "+"), nil
}

// isValidCountryCode 判断两位大写 ISO 3166-1 alpha-2 国家码。
func isValidCountryCode(c string) bool {
	return len(c) == 2 && c[0] >= 'A' && c[0] <= 'Z' && c[1] >= 'A' && c[1] <= 'Z'
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./processor/match/ -v -count=1`
Expected: 全绿（既有 93+ 全部不回归 + geoip 新行）

- [ ] **Step 5: 提交**

```bash
git add processor/match/
git commit -m "$(cat <<'EOF'
新增：条目分类器 geoip 语法（冒号 + 多国 / Clash 单国）并修复误编译归类

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: `matchGeoIP` 真实现（可注入 seam + 大小写适配）

**Files:**
- Modify: `processor/routing/decision_engine.go:235-243`（matchGeoIP stub）
- Test: `processor/routing/decision_engine_test.go`（新建，若无）

- [ ] **Step 1: 写失败测试**

```go
package routing

import "testing"

func TestMatchGeoIP(t *testing.T) {
	orig := geoipLookup
	t.Cleanup(func() { geoipLookup = orig })

	t.Run("multi-any-hit", func(t *testing.T) {
		geoipLookup = func(ip net.IP) (string, error) { return "JP", nil }
		if !matchGeoIP("CN+HK+JP", "1.2.3.4") {
			t.Fatal("expected hit for JP in CN+HK+JP")
		}
	})
	t.Run("case-adaptation-lowercase-condition", func(t *testing.T) {
		geoipLookup = func(ip net.IP) (string, error) { return "CN", nil }
		// 快照层小写化产物 + 结构化逗号串归一
		for _, cond := range []string{"cn", "cn+hk", "cn,hk"} {
			if !matchGeoIP(cond, "1.2.3.4") {
				t.Fatalf("expected hit for condition %q", cond)
			}
		}
	})
	t.Run("miss", func(t *testing.T) {
		geoipLookup = func(ip net.IP) (string, error) { return "US", nil }
		if matchGeoIP("CN+HK", "1.2.3.4") {
			t.Fatal("expected miss")
		}
	})
	t.Run("unlisted-ip", func(t *testing.T) {
		geoipLookup = func(ip net.IP) (string, error) { return "", nil }
		if matchGeoIP("CN", "1.2.3.4") {
			t.Fatal("expected miss for empty code")
		}
	})
	t.Run("lookup-error", func(t *testing.T) {
		geoipLookup = func(ip net.IP) (string, error) { return "", errors.New("not loaded") }
		if matchGeoIP("CN", "1.2.3.4") {
			t.Fatal("expected miss on error")
		}
	})
	t.Run("empty-inputs", func(t *testing.T) {
		geoipLookup = func(ip net.IP) (string, error) { return "CN", nil }
		if matchGeoIP("", "1.2.3.4") || matchGeoIP("CN", "") || matchGeoIP("CN", "not-an-ip") {
			t.Fatal("expected miss on empty/invalid inputs")
		}
	})
}
```

（import 按需补 `errors`/`net`；文件已存在则追加。）

- [ ] **Step 2: 确认失败**

Run: `go test ./processor/routing/ -run TestMatchGeoIP -v`
Expected: 编译失败或全 FAIL（stub 恒 false）

- [ ] **Step 3: 实现**（替换 decision_engine.go:235-243 的 stub；import 补 `net`/`strings`/`processor/geoip`——注意 strings 在此前委托时被删过，需加回）

```go
// geoipLookup 可被测试替换；默认委托 processor/geoip 单例
// （服务未启用/未加载时返回错误 → 不命中，热路径零日志）。
var geoipLookup = func(ip net.IP) (string, error) {
	info, err := geoip.GetService().LookupCountry(ip)
	if err != nil || info == nil {
		return "", err
	}
	return info.ISOCode, nil
}

// matchGeoIP 判定 IP 归属国是否命中 condition（多国 `+`/`,` 分隔任一命中）。
// 快照层 condition 已小写化（snapshot.go:185），统一大写后 EqualFold 适配；
// `,` 分隔仅来自结构化注入路径，归一为 `+` 后统一处理。
func matchGeoIP(condition, ip string) bool {
	if condition == "" || ip == "" {
		return false
	}
	addr := net.ParseIP(ip)
	if addr == nil {
		return false
	}
	code, err := geoipLookup(addr)
	if err != nil || code == "" {
		return false
	}
	norm := strings.ToUpper(strings.ReplaceAll(condition, ",", "+"))
	for _, c := range strings.Split(norm, "+") {
		if c != "" && strings.EqualFold(c, code) {
			return true
		}
	}
	return false
}
```

（删原 stub 的 TODO 注释与 Debug 日志；`matchGeoIP` 签名与两处调用点 `:174/:198` 不变。）

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./processor/routing/ -v -count=1 && go build ./...`
Expected: 全绿（既有 snapshot 测试不回归——它们不涉及 geoip 规则；`geoipLookup` 默认实现在服务未加载时静默不命中）

- [ ] **Step 5: 提交**

```bash
git add processor/routing/decision_engine.go processor/routing/decision_engine_test.go
git commit -m "$(cat <<'EOF'
新增：matchGeoIP 真实现（可注入查找 + 快照小写化适配 + 多国任一命中）

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: 快照编译 geoip 分支 + 编译后匹配集成测试

**Files:**
- Modify: `processor/routing/snapshot.go:329-332`（appendRules ruleType 判定）
- Test: `processor/routing/snapshot_test.go`（追加）

- [ ] **Step 1: 写失败测试**

```go
func TestRuntimeSnapshot_CompileGeoIPRuleAsGeoIPType(t *testing.T) {
	// setup 模式跟随既有 TestRuntimeSnapshot_*：构造 cfg.Customer.ProxyRules = ["geoip:CN+HK+US"]
	// 断言：恰产出 1 条规则 {Type: "geoip", Condition: "CN+HK+US"}；既有 ai/proxy 规则编号不受影响
}

func TestRuntimeSnapshot_CompileGeoIPClashStyleRegression(t *testing.T) {
	// ProxyRules = ["GEOIP,CN,proxy"]（socks 关闭 → target direct）
	// 断言：产出 {Type: "geoip", Condition: "CN"} —— 回归锁定：不再产出 domain "cn"
}

func TestDecideRouteFromSnapshot_GeoIPConditionMatchesCaseInsensitively(t *testing.T) {
	// ⚠️ geoip 条目必须放 AI include 面（AliangEnabled=true → target=toAliang ≠ 默认 direct），
	// 否则命中/不命中都产生 RouteDirect，断言恒真、测试空洞
	// 编译含 include ["geoip:CN+HK"] 的配置 → 快照 condition 小写化为 "cn+hk"
	// override geoipLookup 返回 "HK"，ctx{Domain:"", IP:"1.2.3.4"} → RouteToALiang（命中实证）
	// override 返回 "US" → RouteDirect（不命中走默认）
	// （geoipLookup 是本包未导出变量，测试可直接替换，t.Cleanup 恢复）
}
```

（setup 细节照抄既有测试的 cfg 构造与 CompileRuntimeSnapshotFromRuntimeInputs 调用方式。）

- [ ] **Step 2: 确认失败**

Run: `go test ./processor/routing/ -run 'GeoIP' -v`
Expected: FAIL（Type 是 domain 或规则缺失）

- [ ] **Step 3: 实现**（snapshot.go appendRules 的 ruleType 判定改三分支）

```go
		ruleType := model.RuleTypeDomain
		switch kind {
		case match.EntryIP:
			ruleType = model.RuleTypeIP
		case match.EntryGeoIP:
			ruleType = model.RuleTypeGeoIP
		}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./processor/routing/ -v -count=1 && go test ./processor/config/ ./app/http/services/ -count=1 2>&1 | tail -3`
Expected: 全绿（依赖包回归；services 慢属正常）

- [ ] **Step 5: 提交**

```bash
git add processor/routing/
git commit -m "$(cat <<'EOF'
新增：快照编译 geoip 三分支与编译后匹配集成测试（修复 GEOIP,CN,proxy 误编译）

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: `processor/geoip` 重构——锁外下载 + COS 源

**Files:**
- Modify: `processor/geoip/service.go`
- Test: `processor/geoip/service_test.go`（新建）

- [ ] **Step 1: 写失败测试**

```go
package geoip

import (
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLoadLocal(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.mmdb")
	s := &Service{}
	if err := s.LoadLocal(missing); err == nil {
		t.Fatal("expected error for missing file")
	}
	// 有效库场景：用环境里已验证的 mmdb（若 CI 无网，跳过该子测——
	// 用 t.Skip 保护：os.Getenv("GEOIP_TEST_MMDB") 提供路径时才跑）
	if p := os.Getenv("GEOIP_TEST_MMDB"); p != "" {
		s2 := &Service{}
		if err := s2.LoadLocal(p); err != nil {
			t.Fatalf("LoadLocal: %v", err)
		}
		if !s2.IsEnabled() {
			t.Fatal("expected enabled after load")
		}
		if info, err := s2.LookupCountry(net.ParseIP("114.114.114.114")); err != nil || info == nil || info.ISOCode != "CN" {
			t.Fatalf("lookup CN: %v %+v", err, info)
		}
	} else {
		t.Skip("GEOIP_TEST_MMDB not set")
	}
}

func TestEnsureDownloadedUsesOverriddenURLAndSkipsExisting(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "db.mmdb")
	os.WriteFile(existing, []byte("placeholder"), 0o644)
	origURL := os.Getenv("ALIANG_GEOIP_DOWNLOAD_URL")
	t.Setenv("ALIANG_GEOIP_DOWNLOAD_URL", "http://127.0.0.1:1/never.mmdb") // 必失败地址
	if err := EnsureDownloaded(existing); err != nil {
		t.Fatalf("existing file must skip download: %v", err)
	}
	if origURL != "" {
		t.Setenv("ALIANG_GEOIP_DOWNLOAD_URL", origURL)
	}
}

func TestLoadLocalCorruptFile(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "corrupt.mmdb")
	os.WriteFile(corrupt, []byte("not-a-mmdb"), 0o644)
	s := &Service{}
	if err := s.LoadLocal(corrupt); err == nil {
		t.Fatal("expected error for corrupt file")
	}
	if s.IsEnabled() {
		t.Fatal("must not be enabled after failed load")
	}
}

func TestLookupDuringDownloadMissesImmediately(t *testing.T) {
	// seam：downloadFunc 替换为慢写入器；并发 Lookup 必须立即不命中而非阻塞
	s := &Service{}
	dir := t.TempDir()
	target := filepath.Join(dir, "db.mmdb")
	orig := downloadFunc
	t.Cleanup(func() { downloadFunc = orig })
	var release chan struct{}
	downloadFunc = func(url, dest string) error {
		os.WriteFile(dest, []byte("pending"), 0o644) // 非法内容
		<-release
		os.WriteFile(dest, []byte("still-invalid"), 0o644)
		return nil
	}
	release = make(chan struct{})
	go func() { _ = EnsureDownloaded(target); _ = s.LoadLocal(target) }() // LoadLocal 会因内容非法报错——测试只断言 lookup 不阻塞
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		done := make(chan struct{})
		go func() { _, _ = s.LookupCountry(net.ParseIP("1.2.3.4")); close(done) }()
		select {
		case <-done:
		case <-time.After(100 * time.Millisecond):
			t.Fatal("LookupCountry blocked while download in progress")
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(release)
}
```

（`downloadFunc` seam 见 Step 3；`Service{}` 零值可用前提：确认现有构造不依赖私有字段初始化——GetService 单例外的零值 Service 仅用于测试。）

- [ ] **Step 2: 确认失败**

Run: `go test ./processor/geoip/ -v`
Expected: 编译失败（`LoadLocal`/`EnsureDownloaded`/`downloadFunc` 未定义）

- [ ] **Step 3: 实现**（service.go）

(a) 常量与 URL 覆盖：
```go
const (
	// DefaultGeoIPDownloadURL 用户自有 COS 分发（git.io 短链已失效）。
	DefaultGeoIPDownloadURL = "https://aliang-1305838434.cos.ap-nanjing.myqcloud.com/static/GeoLite2-Country.mmdb"
	DefaultGeoIPDatabaseFile = "GeoLite2-Country.mmdb"
)

// geoipDownloadURL 返回生效下载地址（env 可覆盖）。
func geoipDownloadURL() string {
	if u := os.Getenv("ALIANG_GEOIP_DOWNLOAD_URL"); u != "" {
		return u
	}
	return DefaultGeoIPDownloadURL
}
```

(b) 下载 seam：`var downloadFunc = downloadDatabase`；`EnsureDownloaded` 内调用 `downloadFunc(geoipDownloadURL(), expandedPath)`。原 `LoadDatabase` 内的下载调用点删除。

(c) 拆分（替换原 `LoadDatabase`，grep 确认其唯一调用方 `cmd/start.go:242` 由 Task 5 一并改造——本 Task 先同步把 `initializeGeoIPDatabase` 改为 `EnsureDownloaded+LoadLocal` 串行以保编译）：
```go
// EnsureDownloaded 确保本地存在数据库文件；缺失则下载（无锁——下载绝不持 s.mu）。
func EnsureDownloaded(path string) error { /* 原 stat/MkdirAll/download 逻辑，URL 用 geoipDownloadURL() */ }

// LoadLocal 仅打开本地数据库并原子替换 reader；缺失即错、不下载。
func (s *Service) LoadLocal(path string) error { /* 持 s.mu：expand→stat(缺失报错)→open→swap→enabled=true */ }
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./processor/geoip/ -v -count=1 && go build ./...`
Expected: 全绿（GEOIP_TEST_MMDB 未设时该子测 SKIP 属预期）

- [ ] **Step 5: 提交**

```bash
git add processor/geoip/ cmd/start.go
git commit -m "$(cat <<'EOF'
重构：GeoIP 下载移出锁外（LoadLocal/EnsureDownloaded 拆分）并切换 COS 分发源

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: 启动编排——三路径接入

**Files:**
- Modify: `processor/geoip/service.go`（新增 `EnsureLoadedAsync`）
- Modify: `cmd/start.go`（InitializeGlobalRuleEngine :220-222 替换 Disable；:112 注释改真）、`cmd/core.go:173`（initializeCoreRuleEngine 追加调用）
- Test: `processor/geoip/service_test.go`（追加）+ `cmd`（如可行）

- [ ] **Step 1: 写失败测试**（service_test.go 追加）

```go
func TestEnsureLoadedAsync_LocalFileSyncLoad(t *testing.T) {
	p := os.Getenv("GEOIP_TEST_MMDB")
	if p == "" { t.Skip("GEOIP_TEST_MMDB not set") }
	// ⚠️ EnsureLoadedAsync 解析的是 state 目录路径（非 GEOIP_TEST_MMDB）——
	// 必须把 fixture 放到隔离 state 目录的 geoip 子目录下，否则走后台下载分支导致测试失败/假绿
	t.Setenv("ALIANG_DATA_DIR", t.TempDir()) // 隔离机制以现场为准（testisolate 或等价 env）
	dest, err := cache.GetCacheSubdir("geoip")
	if err != nil { t.Fatal(err) }
	data, err := os.ReadFile(p)
	if err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(dest, DefaultGeoIPDatabaseFile), data, 0o644); err != nil { t.Fatal(err) }
	s := GetService() // 单例——测试里先 Close/Disable 复位
	defer s.Disable()
	if err := s.EnsureLoadedAsync(); err != nil { t.Fatalf("EnsureLoadedAsync: %v", err) }
	// 本地文件存在 → 返回时应已同步加载并 Enable
	if !s.IsEnabled() { t.Fatal("expected enabled (local file)") }
}

func TestEnsureLoadedAsync_MissingFileReturnsImmediately(t *testing.T) {
	s := GetService()
	defer s.Disable()
	t.Setenv("ALIANG_GEOIP_DOWNLOAD_URL", "http://127.0.0.1:1/never.mmdb")
	// DefaultDatabasePath 指向隔离目录且文件不存在 → 应立即返回（异步下载在后台失败仅日志）
	// （需 testisolate 风格隔离 state 目录，或给 Service 增加可注入路径参数——以现场最小改为准）
	start := time.Now()
	_ = s.EnsureLoadedAsync()
	if time.Since(start) > 2*time.Second {
		t.Fatal("EnsureLoadedAsync blocked on missing file")
	}
}
```

- [ ] **Step 2: 确认失败** → **Step 3: 实现**

`service.go` 新增：
```go
// EnsureLoadedAsync 本地已有数据库则同步装载；缺失则后台下载后装载（不阻塞调用方）。
// 三条启动路径（start/tray/core）共用。返回 nil 仅代表编排已启动。
func (s *Service) EnsureLoadedAsync() error {
	path, err := DefaultDatabasePath()
	if err != nil {
		return err
	}
	expanded, err := cache.ExpandHomePath(path)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(expandedPath); statErr == nil {
		return s.LoadLocal(path) // 本地 IO，毫秒级，同步完成
	}
	logger.Info("GeoIP database not found, downloading in background: " + geoipDownloadURL())
	go func() {
		if err := EnsureDownloaded(path); err != nil {
			logger.Warn(fmt.Sprintf("GeoIP database background download failed (will retry next startup): %v", err))
			return
		}
		if err := s.LoadLocal(path); err != nil {
			logger.Warn(fmt.Sprintf("GeoIP database load failed after download: %v", err))
			return
		}
		logger.Info("GeoIP database ready; geoip rules now active")
	}()
	return nil
}
```

`cmd/start.go`：`InitializeGlobalRuleEngine` 的 `:220-222`（Step 2 Disable 段）替换为 `geoip.GetService().EnsureLoadedAsync()` + Info 日志；`:110-112` 注释改为与代码一致（"GeoIP database loading (local-first, background download if missing)"）。`cmd/core.go:173` `initializeCoreRuleEngine` 末尾追加同一调用（失败仅 writeStartupTrace 警告，不阻断 daemon 启动）。`initializeGeoIPDatabase` 旧函数删除（其职责被编排取代）。

- [ ] **Step 4: 跑测试确认通过**

Run: `go build ./... && go test ./processor/geoip/ ./cmd/ -count=1 2>&1 | tail -3`
Expected: 全绿

- [ ] **Step 5: 提交**

```bash
git add processor/geoip/ cmd/
git commit -m "$(cat <<'EOF'
新增：GeoIP 启动编排（本地优先/缺失后台下载）接入 start/tray/core 三路径

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: 死路径清理——`checkGeoIP`/`chinaDirect` 删除

**Files:**
- Modify: `processor/rules/engine.go:22`（chinaDirect 字段）、`:109`（调用点）、`:205-~230`（checkGeoIP 函数）

- [ ] **Step 1: 删除**

- `:109` 的 `if result := e.checkGeoIP(ctx); result != nil { ... }` 调用块
- `checkGeoIP` 函数整体
- `chinaDirect` 字段（grep 确认无赋值点——审阅已证实只读）
- 若 `Initialize` 参数/结构体里有 chinaDirect 相关透传（grep `chinaDirect` 全仓），一并最小清理
- 删除后 `net` import 若成为孤儿（其唯一使用点是 checkGeoIP 内 :215）一并移除，`go build` 兜底

- [ ] **Step 2: 验证**

Run: `go build ./... && go vet ./processor/rules/ && go test ./processor/rules/ -count=1`
Expected: 全绿（该路径无测试依赖——死路径）

- [ ] **Step 3: 提交**

```bash
git add processor/rules/engine.go
git commit -m "$(cat <<'EOF'
删除：旧 GeoIP 全局路由死路径（checkGeoIP/chinaDirect）防止复活语义冲突

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: 校验面——customer 自动生效确认 + mirror 拒绝 geoip

**Files:**
- Modify: `processor/config/types.go`（TrafficMirrorConfig.Validate，:311 一带）
- Test: `processor/config/types_test.go`（追加）

- [ ] **Step 1: 写失败测试**

- customer 正例：`ai_rules.x.include` 含 `geoip:JP`、`proxy_rules` 含 `geoip:CN+HK+US` 与 `GEOIP,CN,proxy` → Validate 通过（Task 1 落地后应自动通过——此用例锁定）
- customer 反例：`geoip:CHN` → 拒绝且错误含原文与「非法国家码」
- mirror 反例：`traffic_mirror.domains` 含 `geoip:CN` → 拒绝且错误注明「镜像列表不支持 geoip 条目」
- mirror 正例回归：既有 glob/IP 条目用例仍通过

- [ ] **Step 2: 确认失败**（mirror geoip 用例红；customer 用例应已绿）

- [ ] **Step 3: 实现**（mirror validate 的既有 ClassifyEntry 循环里加 kind 判断）

```go
		if kind, _, err := match.ClassifyEntry(d); err != nil {
			return fmt.Errorf("traffic_mirror.domains[%d] invalid %q: %v", i, d, err)
		} else if kind == match.EntryGeoIP {
			return fmt.Errorf("traffic_mirror.domains[%d] invalid %q: 镜像列表不支持 geoip 条目", i, d)
		}
```

（以现场循环结构为准适配。）

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./processor/config/ -v -count=1`
Expected: 全绿

- [ ] **Step 5: 提交**

```bash
git add processor/config/
git commit -m "$(cat <<'EOF'
新增：镜像列表拒绝 geoip 条目（域名语义零命中可能）并锁定客户面 geoip 校验

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: 统计守卫扩展

**Files:**
- Modify: `processor/statistic/ai_domains.go`（normalize 的 EntryIP 守卫，:246-250 一带）
- Test: `processor/statistic/ai_domains_test.go`（追加）

- [ ] **Step 1: 写失败测试**

- normalize(`geoip:CN+HK`) → `CN+HK`（现状：落入域名路径变形/拒绝）
- normalize(`GEOIP,CN,proxy`) → `CN`（现状截断变形）
- 幂等：两次 normalize 相等

- [ ] **Step 2: 确认失败** → **Step 3: 实现**（守卫改 `kind == match.EntryIP || kind == match.EntryGeoIP`）

- [ ] **Step 4: 跑测试** `go test ./processor/statistic/ -count=1` → 全绿

- [ ] **Step 5: 提交**

```bash
git add processor/statistic/
git commit -m "$(cat <<'EOF'
新增：统计归一化守卫扩展 geoip 条目（展示形态与编译侧一致）

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: `GeoIPEnabled` 保留停读处置

**Files:**
- Modify: `common/model/routing_config.go`（GeoIPEnabled 字段注释）、`processor/routing/switch_manager.go`（geoIPEnabled 字段注释）

- [ ] **Step 1: grep 盘点**

Run: `grep -rn "GeoIPEnabled\|geoIPEnabled\|IsGeoIPEnabled\|SetGeoIPEnabled" --include="*.go" . | grep -v _test`
Expected: 与 spec §4.5 清单一致（rules_handler 回显 + switch_manager 字段；无行为分支）

- [ ] **Step 2: 注释标注**（两个字段各加一行说明，不改任何逻辑）

```go
	// GeoIPEnabled 为兼容保留的内存态字段：geoip 规则生效与否由「规则存在 + 数据库已加载」
	// 决定，本字段不参与任何行为分支（历史遗留，勿新增读取）。
```

- [ ] **Step 3: 提交**

```bash
git add common/model/routing_config.go processor/routing/switch_manager.go
git commit -m "$(cat <<'EOF'
重构：标注 GeoIPEnabled 为兼容保留字段（geoip 生效改由规则与数据库状态决定）

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: 前端校验 + i18n

**Files:**
- Modify: `app/website/src/components/settings/RulesSettings.vue`、`app/website/src/i18n/zh.js`、`app/website/src/i18n/en.js`

- [ ] **Step 1: i18n 键**（zh/en 同步，`i18n.test.js` 护航）

```js
rules_geoipInvalid: 'geoip 条目无效（国家码为两位大写字母 ISO 3166-1，多国用 + 分隔，如 geoip:CN+HK）',
rules_geoipCommaMulti: 'geoip 多国请用 + 分隔（如 geoip:CN+HK）；逗号写法在当前输入框会被拆坏',
```
en 对应英文。

- [ ] **Step 2: 校验分支**（validateRuleEntry 加 geoip 识别；`ENTRY_CHARS_RE` 不含 `+`/`:`——geoip 分支须在其之前短路）

```js
  // geoip：冒号风格（多国 + 分隔）
  if (lower.startsWith('geoip:')) {
    const rem = s.slice(6).trim();
    if (!rem) return t('rules_geoipInvalid');
    if (rem.includes(',')) return t('rules_geoipCommaMulti');
    const codes = rem.split('+').map((c) => c.trim().toUpperCase()).filter(Boolean);
    if (!codes.length || !codes.every((c) => /^[A-Z]{2}$/.test(c))) return t('rules_geoipInvalid');
    return '';
  }
```

- [ ] **Step 3: include 面的逗号多国专用检测（tokenizer 拆分前的原始草稿扫描）**

在 include 输入的校验入口（providerEditorError 对应的草稿原文，及 include 相关 computed）加一个前置函数：
```js
// 拆分前扫描：include 面任何 geoip 逗号形态都会被逗号 tokenizer 拆坏
// （无例外——Clash 形态 GEOIP,CN,proxy 属 proxy_rules 专属，include 面同样非法）
function detectGeoipCommaIssue(rawText) {
  const lines = String(rawText || '').split('\n');
  for (const line of lines) {
    if (/geoip[:,][^\n]*,[^\n]*/i.test(line)) {
      return t('rules_geoipCommaMulti');
    }
  }
  return '';
}
```
（proxy_rules 文本域按行拆分，`GEOIP,CN,proxy` 合法——该检测**只挂在 include 输入**，不挂 proxy_rules。）

- [ ] **Step 4: 测试与构建**

Run: `cd app/website && npm test && npm run build`
Expected: 全绿（含 i18n 键集一致）、构建成功（dist 哈希按惯例随 `git add app/website/` 入库）
**payload 断言（vitest，落进测试文件而非手跑）**：`sanitizeList('geoip:CN+HK+US')` 输出为**单条目** `['geoip:CN+HK+US']`（锁定 `+` 形态穿过分词器，对应 spec §7.8 提交 payload 断言）
逻辑自查探针（提取函数跑）：`geoip:CN+HK+US` 过 / `geoip:CN,HK` 拒（专用提示）/ `geoip:CHN` 拒 / `geoip:cn` 过（大写归一）/ `GEOIP,CN,proxy` 在 proxy_rules 上下文过、在 include 上下文拒（提示）

- [ ] **Step 5: 提交**

```bash
git add app/website/
git commit -m "$(cat <<'EOF'
新增：前端 geoip 条目校验（+ 多国支持与逗号多国专用提示）及中英文案

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: 文档

**Files:**
- Modify: `README.md`、`README.zh-CN.md`（Rule Syntax 小节加 geoip 行）
- Modify: `docs/tutorials/zh_CN/usage-guide.md`（§12.1 加 geoip 段）、`docs/tutorials/en/usage-guide.md`（对应段）、`docs/tutorials/zh_CN/usage-guide.import.json` + `usage-guide.mdx`（脚本重生成）
- Modify: `docs/api-reference.md`（`:11` 依赖表行与 `:396` 附近 geoip 段）、`processor/geoip/download_example.md`（重写：COS 地址 + env 覆盖 + 新编排日志；6 处 git.io 清零）

**内容要点**（与 spec §3 一致，别写出矛盾句）：
- 写法：`geoip:CN`、`geoip:CN+HK+US`（**多国用 + 分隔**）、`GEOIP,CN,proxy`（仅 proxy_rules/config 面，AI include 输入不支持逗号形态）
- 生效范围：与 IP 条件一致（规则引擎被咨询的 TCP 路径；无 SNI TLS/UDP/本地 HTTP 代理入站三盲区不生效）
- 数据获取：启动自动从 COS 下载到本地数据目录（缺失时一次）；`ALIANG_GEOIP_DOWNLOAD_URL` 可覆盖；COS 副本建议每月手动刷新
- 无库时 geoip 规则不生效（其余功能不受影响）；保存时非法国家码显式拒绝

- [ ] **Step 1: 更新五处文档**（嵌入既有结构；zh 改后跑 `python3 docs/tutorials/scripts/md_to_mdx.py` 重生成）
- [ ] **Step 2: 提交**

```bash
git add README.md README.zh-CN.md docs/
git commit -m "$(cat <<'EOF'
新增：geoip 规则语法文档（写法/生效范围/数据获取）并清理旧下载地址残留

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 12: 全量验证与验收核对

- [ ] **Step 1: 全仓**

```bash
go build ./... && go vet ./... && go test ./... 2>&1 | grep -E "FAIL|panic" | head -5
```
（services 偶发超时属预存 flake——单包复跑判定；panic_recovered test.panic 日志是既有的 panic 恢复测试输出，非失败）

- [ ] **Step 2: 前端** `cd app/website && npm test && npm run build`

- [ ] **Step 3: 验收清单核对**（spec §8 七条，逐条给 test:line/commit 证据）

- [ ] **Step 4: 分支盘点** `git log --oneline master..HEAD` + `git diff master..HEAD --stat | tail -3`

- [ ] **Step 5: 汇总报告**（Status / 各步输出摘录 / 验收核对表 / 遗留事项；若 cmd 三路径未写自动化测试仅靠单元级覆盖，须在本报告**显式声明该偏差**）
