# aliang-peer 跨设备 MCP 专门入口与开关 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把已存在的 `aliang mcp` stdio 服务端平台托管注册进 claude/codex 的原生配置,新增 `customer.peer_mcp.enable` 总开关门控三内核(agentcore 注入 + claude/codex 注册),仪表盘「能力接入」分区新增平台托管卡带状态回显。

**Architecture:** 单一新文件 `agent_peer_mcp.go` 承载注册管理器(claude.json 全量 map 往返 + config.toml 文本级段拼接 + 状态读取 + Apply/Ensure);配置键走 customer 白名单三件套(与 `agentcore.mcp_enabled` 同族);HTTP 端点 GET/POST `/api/agent/peer/mcp` 双路由表注册(runtime 侧执行写入,core 侧透传);agentcore 门控在 `agentCorePeerTools` 函数头。spec 见 `docs/superpowers/specs/2026-11-03-peer-mcp-entry-switch-design.md`。

**Tech Stack:** Go 1.25(module `aliang.one/nursorgate`)、github.com/BurntSushi/toml v1.4.0(已在 go.mod:8,只用 decode 侧)、Vue 3 options API + 原生 checkbox 卡片模式、vitest。

---

## 执行守则(每个 executor 必读)

1. **提交授权**:各任务末尾的 commit 步骤按仓库守则须用户授权;用户批准执行本计划即视为逐任务提交授权。提交标题全中文「新增:/修复:」,HEREDOC 多行,结尾两行:
   ```
   🤖 Generated with [Claude Code](https://claude.com/claude-code)

   Co-Authored-By: Claude Code <noreply@anthropic.com>
   ```
2. **worktree 强制**:所有改动在 worktree 进行(Task 0),不碰 master 工作区。
3. **测试红线**:
   - 新测试文件名避开 GOOS 尾段(不用 `_darwin/_windows` 结尾,防静默跳过);新测试先 `-list` 确认真实执行。
   - `app/http/services` 包测试**不带 `-race`**(预存竞争已修但基线有两枚已知 flake:`TestStartProxyWhenSessionReady`、`TestHandleAgentAuthRejectedNotificationRules`;`processor/auth` 有一枚 `TestSessionAuthorityProxyAdmissionLinearizesWithDemotion`)。名单内失败勿当回归。
   - services 包测试默认自动隔离出站请求(`localUserAgentBaseURL`→`http://127.0.0.1:1`,useragent_test_isolation.go)——**绝不删除该护栏**;需要断言出站内容时显式覆盖并 `t.Cleanup` 恢复。
   - 本计划测试涉及用户家目录文件(`.claude.json`/`config.toml`):**一律走 `peerMCPHome` 包级注入 + `t.TempDir()`,严禁读写真实家目录**;换桩后必须 `t.Cleanup` 恢复;同包测试禁 `t.Parallel`(与 `mcpInventoryFor` 换桩同规)。
4. **禁入目录**:`gemini-cli-main/`、`aliang/`、`alianggate-transfer/`。

---

### Task 0: worktree 准备

**Files:** 无新文件(worktree 副本)。

- [ ] **Step 1: 建 worktree**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
git worktree add ../alianggate-peer-mcp -b feature/peer-mcp-switch master
cd ../alianggate-peer-mcp
```

- [ ] **Step 2: 复制 dist(embed 占位坑)**

新 worktree 首次 `go build` 会因 embed 占位失败,从主仓复制:

```bash
cp -R /Users/mac/MyProgram/GoProgram/nursor/alianggate/app/website/dist app/website/dist
```

- [ ] **Step 3: 验证构建基线**

```bash
go build ./... 2>&1 | head -20
```

Expected: 无错误(或仅与本 feature 无关的既有告警)。

---

### Task 1: 配置键 `customer.peer_mcp`(结构体 + 白名单三件套 + 读取点)

**Files:**
- Modify: `processor/config/types.go`(CustomerConfig 字段 :519-534、PeerMCPConfig 新结构体、EffectivePeerMCPEnabled、extractCustomerUnknownFields case :1085-1095、customerUnknownKeyErrors 文案 :1033-1038)
- Modify: `app/http/services/customer_config_service.go`(validateEditableCustomerKeys case :407-418、错误文案 :383/:413 附近——以 grep 命中为准,勿盲信行号)
- Test: `processor/config/peer_mcp_test.go`(新建)

- [ ] **Step 1: 写失败测试**

新建 `processor/config/peer_mcp_test.go`(package config):

```go
package config

import (
	"encoding/json"
	"testing"
)

// peer_mcp 三态:nil=默认开(对齐 agentcore peer 注入现状,升级不回退)、
// true/false 显式。加载/保存白名单两处都必须放行该键,否则保存 400。
func TestEffectivePeerMCPEnabledDefaults(t *testing.T) {
	var c Config
	if !c.EffectivePeerMCPEnabled() {
		t.Fatal("nil Config 应默认开启")
	}
	c.Customer = &CustomerConfig{}
	if !c.EffectivePeerMCPEnabled() {
		t.Fatal("PeerMCP 未配置应默认开启")
	}
	on := true
	c.Customer.PeerMCP = &PeerMCPConfig{Enable: &on}
	if !c.EffectivePeerMCPEnabled() {
		t.Fatal("显式 true 应开启")
	}
	off := false
	c.Customer.PeerMCP = &PeerMCPConfig{Enable: &off}
	if c.EffectivePeerMCPEnabled() {
		t.Fatal("显式 false 应关闭")
	}
}

func TestPeerMCPKeyAcceptedOnLoad(t *testing.T) {
	// fixture 必须带合法 core 段:Config.Validate 过完 customer 白名单后会
	// 无条件校验 core.api_server 必填(types.go:910-918),缺 core 段会以
	// 「core.api_server is required」无关错误失败,误导排查方向。
	raw := `{"core":{"api_server":"https://api.example.com"},"customer":{"peer_mcp":{"enable":false}}}`
	var c Config
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("peer_mcp 应被加载白名单放行, got: %v", err)
	}
	if c.EffectivePeerMCPEnabled() {
		t.Fatal("显式 false 应关闭")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./processor/config/ -run 'TestEffectivePeerMCPEnabled|TestPeerMCPKeyAcceptedOnLoad' -v
```

Expected: FAIL(编译错误 `EffectivePeerMCPEnabled`/`PeerMCPConfig` undefined)。

- [ ] **Step 3: 实现 types.go**

在 `processor/config/types.go` 的 `CustomerConfig`(:519-534)末尾(`AgentCore` 字段后)追加字段:

```go
	// PeerMCP 是 aliang-peer 跨设备 MCP 工具面的总开关(customer.peer_mcp)。
	// nil 时 Enable 视为 true——对齐 agentcore peer 注入的既有现状(默认开),
	// 升级不产生行为回退。它门控三处:agentcore 注入、claude/codex 注册。
	PeerMCP *PeerMCPConfig `json:"peer_mcp,omitempty"`
```

在同文件(`AgentCoreTuningConfig` 定义之后合适位置)追加:

```go
// PeerMCPConfig 控制 aliang-peer 跨设备工具面(customer.peer_mcp)。三态指针
// 语义与 agentcore.mcp_enabled 同族:nil=未配置(默认开)、true/false=显式。
type PeerMCPConfig struct {
	Enable *bool `json:"enable,omitempty"`
}
```

在 `EffectiveAgentCoreSkillsEnabled`(:663-674 区域)之后追加:

```go
// EffectivePeerMCPEnabled 返回 aliang-peer 跨设备工具面是否开启。默认开启:
// peer 工具是既有能力(agentcore 已默认注入),加开关不能把没配过的人的功能
// 关掉。只有显式写成 false 才关闭。
func (c *Config) EffectivePeerMCPEnabled() bool {
	if c == nil || c.Customer == nil || c.Customer.PeerMCP == nil || c.Customer.PeerMCP.Enable == nil {
		return true
	}
	return *c.Customer.PeerMCP.Enable
}
```

`extractCustomerUnknownFields` 的 case 列表(:1091-1095)加键:

```go
		case "proxy", "ai_rules", "proxy_rules", "traffic_mirror", "http1_drop", "model_mapping", "custom_env_vars", "geoip", "agentcore_llm", "agentcore", "peer_mcp":
```

`customerUnknownKeyErrors` 错误文案(:1035-1037)的方括号清单同步加 `peer_mcp`:

```go
		return fmt.Errorf("customer.%s is forbidden: editable customer fields are [proxy ai_rules proxy_rules traffic_mirror http1_drop model_mapping custom_env_vars geoip agentcore_llm agentcore peer_mcp]", sorted[0])
```

- [ ] **Step 4: 同步 customer_config_service.go 白名单**

```bash
grep -n "agentcore_llm" app/http/services/customer_config_service.go
```

在 `validateEditableCustomerKeys`(:407-418)的键 case 列表加 `"peer_mcp"`,并同步同文件错误文案(:394/:410-417)中的 `[... agentcore]` 清单——**改一处必改三处**(两 switch + 文案),这是该家族注释明言的规则。用 grep 确认无遗漏:

```bash
grep -rn "editable customer fields are" processor/config/ app/http/services/
```

所有命中行的方括号清单都补 ` peer_mcp`。

- [ ] **Step 5: 跑测试确认通过**

```bash
go test ./processor/config/ -run 'TestEffectivePeerMCPEnabled|TestPeerMCPKeyAcceptedOnLoad' -v
go test ./processor/config/ ./app/http/services/ -run 'TestValidateEditableCustomer|TestCustomer' -v 2>&1 | tail -5
```

Expected: PASS(services 侧客户配置白名单既有测试不回归)。

- [ ] **Step 6: Commit**

```bash
git add processor/config/types.go processor/config/peer_mcp_test.go app/http/services/customer_config_service.go
git commit -m "$(cat <<'EOF'
新增:customer.peer_mcp 配置键(aliang-peer 跨设备工具面总开关,三态默认开,白名单三处同步)

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: 注册管理器 `agent_peer_mcp.go`

**Files:**
- Create: `app/http/services/agent_peer_mcp.go`
- Create: `app/http/services/agent_peer_mcp_test.go`

- [ ] **Step 1: 写失败测试**

新建 `app/http/services/agent_peer_mcp_test.go`(package services):

```go
package services

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 换桩纪律:peerMCPHome 指向 t.TempDir(),严禁读写真实家目录;t.Cleanup 恢复;
// 同包换桩禁 t.Parallel。
func swapPeerMCPHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	orig := peerMCPHome
	peerMCPHome = func() string { return home }
	t.Cleanup(func() { peerMCPHome = orig })
	return home
}

func TestApplyPeerMCPClaudeRegistrationPreservesUserContent(t *testing.T) {
	home := swapPeerMCPHome(t)
	path := filepath.Join(home, ".claude.json")
	existing := `{"num":1700000000000,"mcpServers":{"shared":{"command":"user-cmd"}},"projects":{"p":{"history":true}}}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyPeerMCPClaudeRegistration(home, true); err != nil {
		t.Fatalf("register: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// 其他顶层键与既有 server 必须保留
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	if _, ok := root["projects"]; !ok {
		t.Fatal("其他顶层键丢失")
	}
	if !strings.Contains(string(raw), `"shared"`) {
		t.Fatal("既有 server 条目丢失")
	}
	// UseNumber 保证数字字面量原样回写
	if !strings.Contains(string(raw), "1700000000000") {
		t.Fatal("数字字面量被改写")
	}
	// aliang-peer 条目形状
	var servers struct {
		MCPServers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &servers); err != nil {
		t.Fatal(err)
	}
	entry := servers.MCPServers["aliang-peer"]
	if entry.Command == "" || len(entry.Args) != 1 || entry.Args[0] != "mcp" {
		t.Fatalf("aliang-peer 条目形状不符: %+v", entry)
	}
	// 幂等:再应用一次字节不变
	first, _ := os.ReadFile(path)
	if err := applyPeerMCPClaudeRegistration(home, true); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if !bytes.Equal(first, second) {
		t.Fatal("重复注册不幂等")
	}
	// 注销:只删本键
	if err := applyPeerMCPClaudeRegistration(home, false); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "aliang-peer") {
		t.Fatal("注销后仍残留 aliang-peer")
	}
	if !strings.Contains(string(raw), `"shared"`) || !strings.Contains(string(raw), `"projects"`) {
		t.Fatal("注销误伤用户内容")
	}
}

func TestApplyPeerMCPCodexRegistrationPreservesUserContent(t *testing.T) {
	home := swapPeerMCPHome(t)
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := "# my gateway\nmodel = \"gpt\"\n\n[model_providers.aliang]\nname = \"Aliang Gateway\"\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyPeerMCPCodexRegistration(home, true); err != nil {
		t.Fatalf("register: %v", err)
	}
	raw, _ := os.ReadFile(path)
	text := string(raw)
	for _, keep := range []string{"# my gateway", "model = \"gpt\"", "[model_providers.aliang]", "Aliang Gateway"} {
		if !strings.Contains(text, keep) {
			t.Fatalf("用户内容丢失: %q", keep)
		}
	}
	if !strings.Contains(text, "[mcp_servers.aliang-peer]") {
		t.Fatal("托管块未写入")
	}
	if !strings.Contains(text, "command = \"") || !strings.Contains(text, `args = ["mcp"]`) {
		t.Fatalf("托管块形状不符:\n%s", text)
	}
	// 幂等
	first, _ := os.ReadFile(path)
	if err := applyPeerMCPCodexRegistration(home, true); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if !bytes.Equal(first, second) {
		t.Fatalf("重复注册不幂等:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	// 注销:只删托管块
	if err := applyPeerMCPCodexRegistration(home, false); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	text = string(raw)
	if strings.Contains(text, "aliang-peer") {
		t.Fatal("注销后仍残留托管块")
	}
	for _, keep := range []string{"# my gateway", "model = \"gpt\"", "[model_providers.aliang]"} {
		if !strings.Contains(text, keep) {
			t.Fatalf("注销误伤用户内容: %q", keep)
		}
	}
}

func TestPeerMCPKernelStateTransitions(t *testing.T) {
	home := swapPeerMCPHome(t)
	// 初始:missing
	if st, err := peerMCPRegistrationStates(home); err != nil {
		t.Fatal(err)
	} else {
		for _, r := range st {
			if r.State != PeerMCPStateMissing || r.Error != "" {
				t.Fatalf("初始态应为 missing 无错: %+v", r)
			}
		}
	}
	if results := ApplyPeerMCPRegistration(home, true); len(results) != 2 {
		t.Fatalf("应返回两内核结果: %+v", results)
	} else {
		for _, r := range results {
			if r.State != PeerMCPStateRegistered || r.Error != "" {
				t.Fatalf("注册后应 registered: %+v", r)
			}
		}
	}
	// 已 registered 再 Apply=幂等跳过(不重写文件);注销后应 missing 无错
	for _, r := range ApplyPeerMCPRegistration(home, false) {
		if r.State != PeerMCPStateMissing || r.Error != "" {
			t.Fatalf("注销后应 missing 无错: %+v", r)
		}
	}
}

func TestPeerMCPStaleDetection(t *testing.T) {
	home := swapPeerMCPHome(t)
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// 手写的旧 command(模拟用户按文档手工注册过)
	if err := os.WriteFile(path, []byte("[mcp_servers.aliang-peer]\ncommand = \"/old/aliang\"\nargs = [\"mcp\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	results := ApplyPeerMCPRegistration(home, true)
	for _, r := range results {
		if r.Kernel == "codex" && (r.State != PeerMCPStateRegistered || r.Error != "") {
			t.Fatalf("stale 应被自愈为 registered: %+v", r)
		}
	}
}

func TestEnsurePeerMCPMatchesConfig(t *testing.T) {
	home := swapPeerMCPHome(t)
	off := false
	configSetGlobalForTest(t, &off)
	EnsurePeerMCPMatchesConfigForTest(home)
	// 关:两内核注册态应为 missing(没注册过,注销幂等)
	st, err := peerMCPRegistrationStates(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range st {
		if r.State != PeerMCPStateMissing {
			t.Fatalf("关闭态自愈后应 missing: %+v", r)
		}
	}
	on := true
	configSetGlobalForTest(t, &on)
	EnsurePeerMCPMatchesConfigForTest(home)
	st, err = peerMCPRegistrationStates(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range st {
		if r.State != PeerMCPStateRegistered {
			t.Fatalf("开启态自愈后应 registered: %+v", r)
		}
	}
}
```

配套测试助手(同一测试文件内;`configSetGlobalForTest` 若包内已有等价助手则复用):

```go
func configSetGlobalForTest(t *testing.T, enable *bool) {
	t.Helper()
	// 与包内约定一致(agent_ai_agentcore_test.go 等):save-old/restore-old,
	// 不用 ResetGlobalConfigForTest 清零。
	old := config.GetGlobalConfig()
	config.SetGlobalConfig(&config.Config{Customer: &config.CustomerConfig{PeerMCP: &config.PeerMCPConfig{Enable: enable}}})
	t.Cleanup(func() { config.SetGlobalConfig(old) })
}
```

测试文件顶部 import 需补 `"aliang.one/nursorgate/processor/config"`(按 Step 3 最终代码调整 import)。

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./app/http/services/ -run 'TestApplyPeerMCP|TestPeerMCPKernel|TestPeerMCPStale|TestEnsurePeerMCP' -v 2>&1 | tail -5
```

Expected: FAIL(编译错误,符号未定义)。

- [ ] **Step 3: 实现 `agent_peer_mcp.go`**

新建 `app/http/services/agent_peer_mcp.go`(完整文件):

```go
package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
)

// aliang-peer 跨设备 MCP 工具面的平台托管注册(spec 2026-11-03 §6)。
//
// 职责:把已存在的 `aliang mcp` stdio 服务端(cmd/mcp.go,server 名
// aliang-peer)注册进 claude(<home>/.claude.json 顶层 mcpServers)与 codex
// (<home>/.codex/config.toml [mcp_servers.aliang-peer])的原生配置,并按
// customer.peer_mcp 总开关做启动自愈对齐。总开关的持久化归 customer 配置
// 通道(仪表盘 → core 落盘/DB),本文件只管「内核配置文件的注册状态」;
// 两态允许短暂分歧,由状态回显如实呈现 + 启动自愈收敛。
//
// 硬约束:
//   - 内核配置是用户文件,合并写绝不语义重写:claude 侧全量 map 往返
//     (json.Decoder.UseNumber 保数字字面量),codex 侧文本级段拼接
//     (保注释/空行/键序,刻意不用 toml re-encode)。
//   - 家目录一律走 peerMCPHome(生产=agentHome(),root agent 下解析登录
//     用户家目录),不写字面 ~。
//   - 写失败只返回错误/记日志,绝不 panic、绝不阻断内核 run。

const peerMCPManagedServerName = "aliang-peer"

// peerMCPHome 与 mcpInventoryHome 同款的测试注入点:生产走 agentHome()。
var peerMCPHome = agentHome

// PeerMCPKernelState 单内核注册状态(诚实回显的取值域)。
type PeerMCPKernelState string

const (
	PeerMCPStateRegistered PeerMCPKernelState = "registered" // 条目在且 command 指向当前二进制
	PeerMCPStateStale      PeerMCPKernelState = "stale"      // 条目在但 command 与当前二进制不一致(含手工注册形态)
	PeerMCPStateMissing    PeerMCPKernelState = "missing"    // 条目不在
)

// PeerMCPApplyResult 单内核注册结果(HTTP 状态回显的数据形状)。
type PeerMCPApplyResult struct {
	Kernel string             `json:"kernel"`          // "claude" | "codex"
	State  PeerMCPKernelState `json:"state"`           // 应用后的状态(失败时为应用前状态)
	Error  string             `json:"error,omitempty"` // 非空=该内核应用失败,其余内核不受影响
}

// peerMCPCommand 返回注册进内核配置的 stdio 命令:当前二进制绝对路径 +
// mcp 子命令(与 cmd/mcp.go 头注释给出的手工接入形态一致)。
func peerMCPCommand() (string, []string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", nil, fmt.Errorf("resolve executable: %w", err)
	}
	abs, err := filepath.Abs(exe)
	if err != nil {
		return "", nil, fmt.Errorf("abs executable path: %w", err)
	}
	return abs, []string{"mcp"}, nil
}

func peerMCPCommandMatches(actual string) bool {
	if strings.TrimSpace(actual) == "" {
		return false
	}
	command, _, err := peerMCPCommand()
	return err == nil && actual == command
}

// ---------------------------------------------------------------------------
// 状态读取(只读,不写)
// ---------------------------------------------------------------------------

// readPeerMCPClaudeEntry 读 .claude.json 顶层 mcpServers["aliang-peer"] 的
// command。文件缺失/键缺失 → ("" , false, nil)。
func readPeerMCPClaudeEntry(home string) (string, bool, error) {
	path := filepath.Join(home, ".claude.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read %s: %w", path, err)
	}
	var root struct {
		MCPServers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return "", false, fmt.Errorf("parse %s: %w", path, err)
	}
	entry, ok := root.MCPServers[peerMCPManagedServerName]
	if !ok || strings.TrimSpace(entry.Command) == "" {
		return "", false, nil
	}
	return entry.Command, true, nil
}

// readPeerMCPCodexEntry 读 config.toml [mcp_servers.aliang-peer].command。
func readPeerMCPCodexEntry(home string) (string, bool, error) {
	path := filepath.Join(home, ".codex", "config.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg codexConfigFile
	if _, err := toml.Decode(bytes.NewReader(raw), &cfg); err != nil {
		return "", false, fmt.Errorf("parse %s: %w", path, err)
	}
	entry, ok := cfg.MCPServers[peerMCPManagedServerName]
	if !ok || strings.TrimSpace(entry.Command) == "" {
		return "", false, nil
	}
	return entry.Command, true, nil
}

func peerMCPKernelState(home, kernel string) (PeerMCPKernelState, error) {
	var (
		command string
		found   bool
		err     error
	)
	switch kernel {
	case "claude":
		command, found, err = readPeerMCPClaudeEntry(home)
	case "codex":
		command, found, err = readPeerMCPCodexEntry(home)
	default:
		return "", fmt.Errorf("unknown kernel %q", kernel)
	}
	if err != nil {
		return "", err
	}
	if !found {
		return PeerMCPStateMissing, nil
	}
	if peerMCPCommandMatches(command) {
		return PeerMCPStateRegistered, nil
	}
	return PeerMCPStateStale, nil
}

// peerMCPRegistrationStates 读两内核当前注册状态(不写)。
func peerMCPRegistrationStates(home string) ([]PeerMCPApplyResult, error) {
	out := make([]PeerMCPApplyResult, 0, 2)
	for _, kernel := range []string{"claude", "codex"} {
		state, err := peerMCPKernelState(home, kernel)
		if err != nil {
			return nil, err
		}
		out = append(out, PeerMCPApplyResult{Kernel: kernel, State: state})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 应用(写,逐内核 best-effort)
// ---------------------------------------------------------------------------

// ApplyPeerMCPRegistration 按 register 对齐两内核注册状态。已满足的内核
// 跳过写盘(幂等);单内核失败不影响另一内核;返回应用后的逐内核状态。
func ApplyPeerMCPRegistration(home string, register bool) []PeerMCPApplyResult {
	out := make([]PeerMCPApplyResult, 0, 2)
	for _, kernel := range []string{"claude", "codex"} {
		state, err := peerMCPKernelState(home, kernel)
		if err != nil {
			out = append(out, PeerMCPApplyResult{Kernel: kernel, State: PeerMCPStateMissing, Error: err.Error()})
			continue
		}
		if (register && state == PeerMCPStateRegistered) || (!register && state == PeerMCPStateMissing) {
			out = append(out, PeerMCPApplyResult{Kernel: kernel, State: state})
			continue
		}
		var applyErr error
		switch kernel {
		case "claude":
			applyErr = applyPeerMCPClaudeRegistration(home, register)
		case "codex":
			applyErr = applyPeerMCPCodexRegistration(home, register)
		}
		if applyErr != nil {
			out = append(out, PeerMCPApplyResult{Kernel: kernel, State: state, Error: applyErr.Error()})
			continue
		}
		newState, err := peerMCPKernelState(home, kernel)
		if err != nil {
			out = append(out, PeerMCPApplyResult{Kernel: kernel, State: state, Error: err.Error()})
			continue
		}
		out = append(out, PeerMCPApplyResult{Kernel: kernel, State: newState})
	}
	return out
}

// EnsurePeerMCPMatchesConfig 启动自愈(runtime 进程专用):按总开关对齐
// 注册状态——开=确保注册且 command 指向当前二进制;关=确保注销。逐内核
// best-effort,失败只 Warn 不阻断启动。
func EnsurePeerMCPMatchesConfig() {
	EnsurePeerMCPMatchesConfigForTest(peerMCPHome())
}

// EnsurePeerMCPMatchesConfigForTest 是 home 可注入的自愈本体。
func EnsurePeerMCPMatchesConfigForTest(home string) {
	if home == "" {
		logger.Warn("peer-mcp: startup heal skipped, agent home unavailable")
		return
	}
	register := config.GetGlobalConfig().EffectivePeerMCPEnabled()
	for _, r := range ApplyPeerMCPRegistration(home, register) {
		if r.Error != "" {
			logger.Warn(fmt.Sprintf("peer-mcp: startup heal kernel=%s register=%v err=%s", r.Kernel, register, r.Error))
		}
	}
}

// ---------------------------------------------------------------------------
// claude 侧:~/.claude.json 全量 map 往返
// ---------------------------------------------------------------------------

// applyPeerMCPClaudeRegistration 维护 .claude.json 顶层 mcpServers 的
// aliang-peer 条目。全量 map 往返保留其余顶层键与 server;UseNumber 让数字
// 字面量原样回写(时间戳不漂移)。键序经 encoding/json 确定性重排——
// claude CLI 每次自写同样会重排,无语义影响。
func applyPeerMCPClaudeRegistration(home string, register bool) error {
	path := filepath.Join(home, ".claude.json")
	root := map[string]interface{}{}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&root); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	case os.IsNotExist(err):
		if !register {
			return nil // 没文件=没注册,幂等
		}
	default:
		return fmt.Errorf("read %s: %w", path, err)
	}

	servers, _ := root["mcpServers"].(map[string]interface{})
	if servers == nil {
		if !register {
			return nil
		}
		servers = map[string]interface{}{}
		root["mcpServers"] = servers
	}

	if register {
		command, args, err := peerMCPCommand()
		if err != nil {
			return err
		}
		servers[peerMCPManagedServerName] = map[string]interface{}{
			"type":    "stdio",
			"command": command,
			"args":    args,
		}
	} else {
		if _, ok := servers[peerMCPManagedServerName]; !ok {
			return nil // 幂等
		}
		delete(servers, peerMCPManagedServerName)
	}

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return writePeerMCPFileAtomic(path, out)
}

// ---------------------------------------------------------------------------
// codex 侧:config.toml 文本级段拼接
// ---------------------------------------------------------------------------

// applyPeerMCPCodexRegistration 文本级维护 config.toml 的
// [mcp_servers.aliang-peer] 块:register=true 替换既有块(或文件尾追加),
// false 整块删除。既有块 = 表头行起到下一个 '[' 表头行/文件尾。只认官方
// 表头形态;文件其余内容(注释/空行/键序)原样保留——刻意不走 toml
// re-encode,整文件重编码会毁掉用户注释与排版。
func applyPeerMCPCodexRegistration(home string, register bool) error {
	path := filepath.Join(home, ".codex", "config.toml")
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	command, args, err := peerMCPCommand()
	if err != nil {
		return err
	}
	header := "[mcp_servers." + peerMCPManagedServerName + "]"
	lines := strings.Split(string(raw), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil // 空文件归一成零行
	}
	start, end := -1, len(lines)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if start < 0 {
			if trimmed == header {
				start = i
			}
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			end = i
			break
		}
	}

	trimTrailingBlank := func(in []string) []string {
		for len(in) > 0 && strings.TrimSpace(in[len(in)-1]) == "" {
			in = in[:len(in)-1]
		}
		return in
	}

	var out []string
	switch {
	case register && start < 0:
		out = trimTrailingBlank(lines)
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, buildPeerMCPTOMLBlock(command, args)...)
	case register: // 替换既有托管块(含块内旧注释)
		out = append(append(append([]string{}, lines[:start]...), buildPeerMCPTOMLBlock(command, args)...), lines[end:]...)
	case start < 0: // 注销但本来就没有:幂等
		return nil
	default: // 注销:整块移除
		out = trimTrailingBlank(append(append([]string{}, lines[:start]...), lines[end:]...))
		out = append(out, "")
	}
	return writePeerMCPFileAtomic(path, []byte(strings.Join(out, "\n")))
}

// buildPeerMCPTOMLBlock 生成托管块;末尾空行元素用于 join 后收尾换行。
func buildPeerMCPTOMLBlock(command string, args []string) []string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = quickSetupTOMLQuote(a)
	}
	return []string{
		"[mcp_servers." + peerMCPManagedServerName + "]",
		"command = " + quickSetupTOMLQuote(command),
		"args = [" + strings.Join(quoted, ", ") + "]",
		"",
	}
}

// ---------------------------------------------------------------------------
// 原子写(保留原文件权限)
// ---------------------------------------------------------------------------

func writePeerMCPFileAtomic(path string, data []byte) error {
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".aliang-peer-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename into %s: %w", path, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// AgentService 门面(HTTP handler 用)
// ---------------------------------------------------------------------------

// PeerMCPStatus 返回总开关与两内核注册状态(诚实回显,不写任何文件)。
func (s *AgentService) PeerMCPStatus() map[string]interface{} {
	states, err := peerMCPRegistrationStates(peerMCPHome())
	registration := states
	if err != nil {
		registration = nil
	}
	return map[string]interface{}{
		"enabled":      config.GetGlobalConfig().EffectivePeerMCPEnabled(),
		"registration": registration,
		"status_error": errorStringOrNil(err),
	}
}

// SetPeerMCPRegistration 应用注册(逐内核 best-effort)并同步本进程内存
// 配置,返回应用后的状态。总开关本身的持久化走 customer.peer_mcp(仪表盘
// 两段式:先存配置再调本端点),本方法不写配置文件。
func (s *AgentService) SetPeerMCPRegistration(enable bool) map[string]interface{} {
	syncPeerMCPConfigInMemory(enable)
	home := peerMCPHome()
	results := ApplyPeerMCPRegistration(home, enable)
	for _, r := range results {
		if r.Error != "" {
			logger.Warn(fmt.Sprintf("peer-mcp: apply kernel=%s register=%v err=%s", r.Kernel, enable, r.Error))
		}
	}
	return map[string]interface{}{"enabled": enable, "registration": results}
}

// syncPeerMCPConfigInMemory 把开关同步进本进程内存全局配置:runtime 的配置
// 快照只在启动时加载(core 负责落盘与 DB),不同步的话 agentcore 注入门控
// 要等 agent 重启才生效。只动内存,磁盘持久化仍归 customer 配置通道。
func syncPeerMCPConfigInMemory(enable bool) {
	cfg := config.GetGlobalConfig()
	if cfg == nil {
		return
	}
	if cfg.Customer == nil {
		cfg.Customer = &config.CustomerConfig{}
	}
	on := enable
	cfg.Customer.PeerMCP = &config.PeerMCPConfig{Enable: &on}
}

func errorStringOrNil(err error) interface{} {
	if err == nil {
		return nil
	}
	return err.Error()
}
```

注意:若 `processor/config` 尚未被本文件外的同包符号(agentHome 等)依赖冲突,import 按编译器提示调整;`codexConfigFile`/`quickSetupTOMLQuote`/`agentHome` 均为同包既有符号(`agent_mcp_inventory.go:331`、`quick_setup_catalog.go:130`、`agent_home.go:17`)。

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./app/http/services/ -run 'TestApplyPeerMCP|TestPeerMCPKernel|TestPeerMCPStale|TestEnsurePeerMCP' -v
```

Expected: PASS。若有编译错误按提示修 import(不改变函数语义)。

- [ ] **Step 5: 修测试文件 helper(import 补齐)**

确认 `agent_peer_mcp_test.go` 顶部 import 含 `"aliang.one/nursorgate/processor/config"`;`configSetGlobalForTest` 若与包内既有助手重名则改名。

- [ ] **Step 6: 复跑测试**

```bash
go test ./app/http/services/ -run 'TestApplyPeerMCP|TestPeerMCPKernel|TestPeerMCPStale|TestEnsurePeerMCP|TestPeerMCP' -v
```

Expected: 全 PASS。

- [ ] **Step 7: Commit**

```bash
git add app/http/services/agent_peer_mcp.go app/http/services/agent_peer_mcp_test.go
git commit -m "$(cat <<'EOF'
新增:aliang-peer 平台托管注册管理器(claude.json 全量往返保数字字面量 + config.toml 文本级段拼接保注释排版 + 启动自愈)

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: agentcore 注入门控

**Files:**
- Modify: `app/http/services/agent_peer_tools.go`(agentCorePeerTools 函数头,:967 附近)
- Test: `app/http/services/agent_peer_mcp_test.go`(追加)

- [ ] **Step 1: 写失败测试(追加到 agent_peer_mcp_test.go)**

```go
func TestAgentCorePeerToolsGatedByPeerMCPEnabled(t *testing.T) {
	swapPeerMCPHome(t) // 防 agentHome 意外落真实家目录(本测试不写文件,防御性)
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)
	m := newAgentAIManager()
	run := agentAIRun{sessionID: "gate"}
	if len(m.agentCorePeerTools(run)) == 0 {
		t.Fatal("默认(未配置)应注入 peer 工具")
	}
	off := false
	config.SetGlobalConfig(&config.Config{Customer: &config.CustomerConfig{PeerMCP: &config.PeerMCPConfig{Enable: &off}}})
	if got := m.agentCorePeerTools(run); len(got) != 0 {
		t.Fatalf("显式关闭后不应注入 peer 工具, got %d", len(got))
	}
}
```

注意:`newAgentAIManager()` 是既有测试构造器(**禁用 `&agentAIManager{}` 字面量**,nil map 会 panic);`agentAIRun{sessionID: ...}` 同包可直设。

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./app/http/services/ -run 'TestAgentCorePeerToolsGatedByPeerMCPEnabled' -v
```

Expected: FAIL——「显式关闭后不应注入 peer 工具, got 5」。

- [ ] **Step 3: 实现门控**

在 `agent_peer_tools.go` 的 `agentCorePeerTools` 函数体最前面(journal 闭包定义之前)插入:

```go
	// peer_mcp 总开关(customer.peer_mcp,仪表盘「设备互访」卡):关 =
	// 三内核工具面同关。读点与 MCP 全局开关(EffectiveAgentCoreMCPEnabled)
	// 同族同语义:默认开,nil 与 true 等价,每 run 现场读取。
	if !config.GetGlobalConfig().EffectivePeerMCPEnabled() {
		return nil
	}
```

检查 import:若 `agent_peer_tools.go` 尚未 import `aliang.one/nursorgate/processor/config` 则补上(grep 确认):

```bash
grep -n "processor/config" app/http/services/agent_peer_tools.go
```

- [ ] **Step 4: 覆盖子 agent 路径的门控核实**

子 agent 的 peer 工具工厂在 `agent_ai_agentcore_subagent.go:383-391`(自建 peerFactory)。核实它是否经由 `agentCorePeerTools`:

```bash
grep -n "agentCorePeerTools\|peerFactory\|AgentPeerToolEvent" app/http/services/agent_ai_agentcore_subagent.go
```

- 若工厂内部调用了 `m.agentCorePeerTools(...)`:Task 3 的门控天然覆盖,无需改动;
- 若工厂绕开 `agentCorePeerTools` 直接构建 bridge:在该工厂构建前插入同款三行门控(引用同一 `EffectivePeerMCPEnabled`),并把上面测试补一条断言(经子 agent 工厂取工具验证为空)。

- [ ] **Step 5: 跑测试确认通过 + agentcore 既有测试不回归**

```bash
go test ./app/http/services/ -run 'TestAgentCorePeerToolsGatedByPeerMCPEnabled' -v
go test ./app/http/services/ -run 'TestAgentCore' -v 2>&1 | tail -3
```

Expected: PASS(agentcore 既有用例默认态不受影响)。

- [ ] **Step 6: Commit**

```bash
git add app/http/services/agent_peer_tools.go app/http/services/agent_peer_mcp_test.go
git commit -m "$(cat <<'EOF'
新增:agentcore peer 注入受 customer.peer_mcp 总开关门控(默认开,与 MCP 全局开关同族语义)

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: HTTP 端点 GET/POST `/api/agent/peer/mcp`(双路由表)

**Files:**
- Modify: `app/http/handlers/agent_handler.go`(追加 HandlePeerMcp,仿 :546-574 HandleScanDirectories)
- Modify: `app/http/routes/routes.go`(:272-278 peer 块旁追加)
- Modify: `app/agentruntime/server.go`(registerAgentRoutes 内追加)

- [ ] **Step 1: 实现 handler**

在 `app/http/handlers/agent_handler.go` 的 `HandleScanDirectories` 之后追加(确认文件已 import `encoding/json`、`io`、`net/http`、`aliang.one/nursorgate/common`——HandleScanDirectories 已全部用到,无须新增):

```go
// HandlePeerMcp GET 读取 / POST 应用 aliang-peer 跨设备 MCP 注册(经
// proxyIfNeeded 代理到 runtime:注册写入的是用户家目录的内核配置文件,
// 必须发生在用户态进程;core 侧同名注册只做透传,双注册教训同 peer 家族)。
func (h *AgentHandler) HandlePeerMcp(w http.ResponseWriter, r *http.Request) {
	if h.proxyIfNeeded(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		common.Success(w, h.service.PeerMCPStatus())
	case http.MethodPost:
		var req struct {
			Enable *bool `json:"enable"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.Enable == nil {
			common.ErrorBadRequest(w, "Invalid request body: enable(bool) required", nil)
			return
		}
		common.Success(w, h.service.SetPeerMCPRegistration(*req.Enable))
	default:
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
	}
}
```

- [ ] **Step 2: 双路由表注册**

`app/http/routes/routes.go` 在 peer 块(:274-278)的 `register("/api/agent/peer/note", ...)` 之后追加:

```go
	// aliang-peer 跨设备 MCP 平台托管注册/状态(GET 读状态,POST 应用)。
	register("/api/agent/peer/mcp", h.Agent.HandlePeerMcp, http.MethodGet, http.MethodPost)
```

`app/agentruntime/server.go` 的 `registerAgentRoutes` 内 peer 块之后追加:

```go
	mux.HandleFunc("/api/agent/peer/mcp", agentHandler.HandlePeerMcp)
```

- [ ] **Step 3: 路由 parity 测试**

```bash
go test ./app/agentruntime/ -run 'Parity' -v 2>&1 | tail -3
```

Expected: PASS(双表都注册后 parity 测试自然绿;漏一侧会红)。

- [ ] **Step 4: 构建验证**

```bash
go build ./...
```

Expected: 无错误。

- [ ] **Step 5: Commit**

```bash
git add app/http/handlers/agent_handler.go app/http/routes/routes.go app/agentruntime/server.go
git commit -m "$(cat <<'EOF'
新增:/api/agent/peer/mcp 端点(GET 状态/POST 应用注册,双路由表注册,core 透传 runtime 执行写入)

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: 启动自愈接线(runtime)

**Files:**
- Modify: `app/agentruntime/server.go`(startup 处调用 EnsurePeerMCPMatchesConfig)

- [ ] **Step 1: 定位接线点**

```bash
grep -n "registerAgentRoutes(" app/agentruntime/server.go
```

- [ ] **Step 2: 接线**

在 `registerAgentRoutes(mux)` 调用点之后(server 构造完成、开始监听前)插入:

```go
	// aliang-peer 注册自愈:按 customer.peer_mcp 对齐内核配置文件(开=确保
	// 注册且 command 最新;关=确保注销)。只 Warn 不阻断启动;应用细节见
	// services.EnsurePeerMCPMatchesConfig。
	services.EnsurePeerMCPMatchesConfig()
```

(`server.go` 已 import services——`services.GetSharedAgentService()` 在同文件。)

- [ ] **Step 3: 构建验证**

```bash
go build ./...
go vet ./app/agentruntime/ ./app/http/services/ ./processor/config/ 2>&1 | tail -3
```

Expected: 无错误。

- [ ] **Step 4: Commit**

```bash
git add app/agentruntime/server.go
git commit -m "$(cat <<'EOF'
新增:runtime 启动时按 peer_mcp 开关自愈 aliang-peer 注册状态(漂移修复+开关对齐,失败只 Warn)

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: 语义等价契约测试(双包)

**Files:**
- Modify: `app/http/services/agent_peer_mcp.go`(追加 PeerMCPContractSpecs 数据)
- Create: `app/http/services/agent_peer_mcp_contract_test.go`
- Create: `cmd/mcp_contract_test.go`

- [ ] **Step 1: 在 agent_peer_mcp.go 追加契约数据**

```go
// ---------------------------------------------------------------------------
// 语义等价契约(spec 2026-11-03 §8.4)
// ---------------------------------------------------------------------------

// PeerMCPContractSpec 钉住 stdio 面(cmd/mcp.go peerMCPTools)与 agentcore 面
// (agent_peer_tools.go agentCorePeerTools)五工具的语义等价契约。两处描述符
// 是手维护近似副本:三个工具名不同(agentcore 点名 vs stdio 无前缀)、描述
// 措辞有差,仅执行后端同源。本契约只钉语义(参数面 + 关键行为约束双侧俱在),
// 刻意不做统一改名。
type PeerMCPContractSpec struct {
	CoreName  string   // agentcore 面名(点名)
	StdioName string   // stdio 面名(claude/codex 共用)
	Required  []string // 双侧 required 必须一致
	Optional  []string // 双侧可选参数名集必须一致
	Keywords  []string // 双侧描述都必须出现的关键约束词
}

func PeerMCPContractSpecs() []PeerMCPContractSpec {
	return []PeerMCPContractSpec{
		{
			CoreName: "peer.exec", StdioName: "peer_exec",
			Required: []string{"device_id", "command"},
			Optional: []string{"timeout_ms", "workdir"},
			Keywords: []string{"peer exec enabled", "fail_reason=rate_limited", "Serial"},
		},
		{
			CoreName: "peer.fetch_file", StdioName: "peer_fetch_file",
			Required: []string{"device_id", "remote_path"},
			Keywords: []string{"peer fetch enabled", "transfer_id"},
		},
		{
			CoreName: "peer.device_list", StdioName: "device_list",
			Keywords: []string{"peer exec is enabled"},
		},
		{
			CoreName: "peer.device_info", StdioName: "device_info",
			Optional: []string{"device_id"},
			Keywords: []string{"load summary"},
		},
		{
			CoreName: "peer.device_note", StdioName: "device_note",
			Required: []string{"key", "value"},
			Optional: []string{"device_id"},
			Keywords: []string{"max 64 bytes", "max 4096 bytes"},
		},
	}
}
```

- [ ] **Step 2: services 侧契约测试**

新建 `app/http/services/agent_peer_mcp_contract_test.go`:

```go
package services

import (
	"sort"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/agentcore"
)

// 契约测试(services 侧):agentcore 面五工具的参数面与关键行为约束必须与
// PeerMCPContractSpecs 一致。漂移即红——修法是把漂移一侧的描述/schema 对齐,
// 不是放宽契约。
func TestPeerMCPContractAgentCoreSide(t *testing.T) {
	m := newAgentAIManager()
	tools := m.agentCorePeerTools(agentAIRun{sessionID: "contract"})
	byName := map[string]agentcore.Tool{}
	for _, tool := range tools {
		byName[tool.Name()] = tool
	}
	for _, spec := range PeerMCPContractSpecs() {
		tool, ok := byName[spec.CoreName]
		if !ok {
			t.Fatalf("agentcore 面缺少工具 %s", spec.CoreName)
		}
		assertPeerMCPSchemaShape(t, spec.CoreName, tool.InputSchema(), spec)
		for _, kw := range spec.Keywords {
			if !strings.Contains(tool.Description(), kw) {
				t.Errorf("%s 描述缺少关键约束 %q(两侧描述须语义等价)", spec.CoreName, kw)
			}
		}
	}
}

func assertPeerMCPSchemaShape(t *testing.T, label string, schema map[string]interface{}, spec PeerMCPContractSpec) {
	t.Helper()
	props, _ := schema["properties"].(map[string]interface{})
	if props == nil {
		t.Fatalf("%s schema 缺 properties", label)
	}
	gotProps := make([]string, 0, len(props))
	for k := range props {
		gotProps = append(gotProps, k)
	}
	sort.Strings(gotProps)
	want := append(append([]string{}, spec.Required...), spec.Optional...)
	sort.Strings(want)
	if strings.Join(gotProps, ",") != strings.Join(want, ",") {
		t.Errorf("%s 参数面漂移: got [%s] want [%s]", label, gotProps, want)
	}
	var gotReq []string
	if rawReq, ok := schema["required"].([]interface{}); ok {
		for _, r := range rawReq {
			if s, ok := r.(string); ok {
				gotReq = append(gotReq, s)
			}
		}
	}
	sort.Strings(gotReq)
	wantReq := append([]string{}, spec.Required...)
	sort.Strings(wantReq)
	if strings.Join(gotReq, ",") != strings.Join(wantReq, ",") {
		t.Errorf("%s required 漂移: got [%s] want [%s]", label, gotReq, wantReq)
	}
}
```

注意:`aliang.one/nursorgate/app/agentcore` 的 import 路径以仓库实际为准(`agent_peer_tools.go` 头部 import 同路径,写前 grep 确认);`tool.InputSchema()` 返回类型若非 `map[string]interface{}` 按实际调整断言取值方式。

- [ ] **Step 3: cmd 侧契约测试**

新建 `cmd/mcp_contract_test.go`:

```go
package cmd

// 契约测试(cmd 侧):stdio 面五工具与 services.PeerMCPContractSpecs 对照。
// 两侧描述是手维护近似副本,本测试钉参数面与关键行为约束的语义等价。
// 形状断言 helper 与 services 侧重复是有意的:两包各自独立红/绿,不引入
// 跨包测试耦合。

import (
	"sort"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/services"
)

func TestPeerMCPContractStdioSide(t *testing.T) {
	byName := map[string]peerMCPTool{}
	for _, tool := range peerMCPTools {
		byName[tool.name] = tool
	}
	for _, spec := range services.PeerMCPContractSpecs() {
		tool, ok := byName[spec.StdioName]
		if !ok {
			t.Fatalf("stdio 面缺少工具 %s", spec.StdioName)
		}
		assertPeerMCPStdioSchemaShape(t, spec.StdioName, tool.schema, spec)
		for _, kw := range spec.Keywords {
			if !strings.Contains(tool.description, kw) {
				t.Errorf("%s 描述缺少关键约束 %q", spec.StdioName, kw)
			}
		}
	}
}

func assertPeerMCPStdioSchemaShape(t *testing.T, label string, schema map[string]interface{}, spec services.PeerMCPContractSpec) {
	t.Helper()
	props, _ := schema["properties"].(map[string]interface{})
	if props == nil {
		t.Fatalf("%s schema 缺 properties", label)
	}
	gotProps := make([]string, 0, len(props))
	for k := range props {
		gotProps = append(gotProps, k)
	}
	sort.Strings(gotProps)
	want := append(append([]string{}, spec.Required...), spec.Optional...)
	sort.Strings(want)
	if strings.Join(gotProps, ",") != strings.Join(want, ",") {
		t.Errorf("%s 参数面漂移: got [%s] want [%s]", label, gotProps, want)
	}
	var gotReq []string
	if rawReq, ok := schema["required"].([]string); ok {
		gotReq = rawReq
	}
	sort.Strings(gotReq)
	wantReq := append([]string{}, spec.Required...)
	sort.Strings(wantReq)
	if strings.Join(gotReq, ",") != strings.Join(wantReq, ",") {
		t.Errorf("%s required 漂移: got [%s] want [%s]", label, gotReq, wantReq)
	}
}
```

注意:cmd 侧 `schema["required"]` 是 `[]string` 字面量(见 cmd/mcp.go:243),与 services 侧 bridge 经 `peerToolSchema` 生成的 `[]interface{}` 形态不同——两个 helper 已按各自形态写好。

- [ ] **Step 4: 跑契约测试**

```bash
go test ./app/http/services/ -run 'TestPeerMCPContractAgentCoreSide' -v
go test ./cmd/ -run 'TestPeerMCPContractStdioSide' -v
```

Expected: PASS。若有 keyword 缺失红:说明该侧描述真的缺这条约束措辞——把缺失的约束短语补进该侧描述(最小措辞对齐),**不许删契约关键词**。

- [ ] **Step 5: Commit**

```bash
git add app/http/services/agent_peer_mcp.go app/http/services/agent_peer_mcp_contract_test.go cmd/mcp_contract_test.go
git commit -m "$(cat <<'EOF'
新增:peer 五件套双面语义等价契约测试(参数面+关键行为约束,钉漂移不统一命名)

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: 前端(归一层 + 平台托管卡 + i18n + API 客户端)

**Files:**
- Modify: `app/website/src/utils/agentCoreSettings.js`(新增 normalizePeerMCPConfig)
- Modify: `app/website/src/utils/agentCoreSettings.test.js`(归一层测试)
- Modify: `app/website/src/components/SettingsPage.vue`(normalizeCustomerConfig 挂键 :425、capability 分区挂卡 :194-211)
- Create: `app/website/src/components/settings/PeerMCPSettings.vue`
- Modify: `app/website/src/services/agentApi.js`(两个函数)
- Modify: `app/website/src/i18n/zh.js`、`app/website/src/i18n/en.js`(mcpGlobal 键组旁,zh :576-581)

⚠️ **最大的坑(2026-10-31 验收缺陷①)**:布尔键必须在归一层透传——`normalizeCustomerConfig` 显式枚举键,**漏加 `peer_mcp` = 保存 HTTP 200 但永不落盘、刷新恒显默认开**。归一层测试先写。

- [ ] **Step 1: 写归一层失败测试**

在 `app/website/src/utils/agentCoreSettings.test.js` 追加(先看 :155 附近既有用例的 import/描述风格,保持一致):

```js
// peer_mcp 归一:布尔透传为线格式 enable;缺键/非对象/非布尔 → undefined
// (不进 patch 与回显,后端 nil 视为开启——三态与 mcp_enabled 同族)。
describe('normalizePeerMCPConfig', () => {
  it('布尔透传为线格式 enable', async () => {
    const { normalizePeerMCPConfig } = await import('./agentCoreSettings');
    expect(normalizePeerMCPConfig({ enable: false })).toEqual({ enable: false });
    expect(normalizePeerMCPConfig({ enable: true })).toEqual({ enable: true });
  });
  it('缺键/非对象/非布尔 → undefined', async () => {
    const { normalizePeerMCPConfig } = await import('./agentCoreSettings');
    expect(normalizePeerMCPConfig(undefined)).toBeUndefined();
    expect(normalizePeerMCPConfig('x')).toBeUndefined();
    expect(normalizePeerMCPConfig({})).toBeUndefined();
    expect(normalizePeerMCPConfig({ enable: 'yes' })).toBeUndefined();
  });
});
```

import 风格按该文件既有顶部 import 调整(若已静态 import 则直接用)。

- [ ] **Step 2: 跑测试确认失败**

```bash
cd app/website && npx vitest run src/utils/agentCoreSettings.test.js 2>&1 | tail -5
```

Expected: FAIL(normalizePeerMCPConfig is not exported)。

- [ ] **Step 3: 实现 normalizePeerMCPConfig**

在 `app/website/src/utils/agentCoreSettings.js` 的 `normalizeAgentCoreTuning` 之后追加:

```js
// ---- customer.peer_mcp(aliang-peer 跨设备工具面总开关) ----
// 三态与 mcp_enabled 同族:布尔显式落盘;缺键/非布尔 → undefined(不进
// patch,后端 nil 视为开启)。⚠️ 必须在 SettingsPage.normalizeCustomerConfig
// 挂键,否则「保存 HTTP 200 但永不落盘」(2026-10-31 验收缺陷①同族)。
export function normalizePeerMCPConfig(source) {
  if (source && typeof source === 'object' && !Array.isArray(source)
    && typeof source.enable === 'boolean') {
    return { enable: source.enable };
  }
  return undefined;
}
```

- [ ] **Step 4: SettingsPage 挂键 + 挂卡**

`SettingsPage.vue` 顶部确认已从 `../utils/agentCoreSettings` import(既有 normalizeAgentCoreTuning 同源),把 `normalizePeerMCPConfig` 加进 import;`normalizeCustomerConfig`(:425)改为:

```js
function normalizeCustomerConfig(payload = {}) {
  const defaults = createDefaultCustomerConfig();
  const peerMcp = normalizePeerMCPConfig(payload?.peer_mcp);
  return {
    proxy: { /* ……原样不动…… */
    },
    ai_rules: normalizeAiRules(payload?.ai_rules),
    proxy_rules: normalizeProxyRules(payload?.proxy_rules),
    model_mapping: normalizeModelMappingConfig(payload?.model_mapping),
    custom_env_vars: normalizeCustomEnvVarsConfig(payload?.custom_env_vars),
    agentcore_llm: normalizeAgentCoreLLMConfig(payload?.agentcore_llm),
    agentcore: normalizeAgentCoreTuning(payload?.agentcore),
    ...(peerMcp ? { peer_mcp: peerMcp } : {})
  };
}
```

capability 分区(:194-211 的 grid 内,`AgentSkillsGlobalSettings` 之后)挂卡:

```html
              <PeerMCPSettings
                :config="customerConfig"
                :loading="isLoadingCustomerConfig"
                :saving="isSavingCustomerConfig"
                :error="customerConfigError"
                :success-message="customerConfigSuccess"
                @save="saveCustomerConfig"
              />
```

并在 `<script>` 里注册组件(按该文件既有组件 import + components 注册模式)。

- [ ] **Step 5: 实现 PeerMCPSettings.vue**

新建 `app/website/src/components/settings/PeerMCPSettings.vue`:

```vue
<template>
  <div class="flex h-full flex-col rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
    <div class="flex items-start gap-3">
      <div class="min-w-0 flex-1">
        <div class="flex items-center gap-2">
          <h2 class="text-base font-bold text-slate-900 dark:text-white">{{ t('peerMcp_title') }}</h2>
          <span class="rounded bg-primary/10 px-1.5 py-0.5 text-[10px] font-bold text-primary">{{ t('peerMcp_managed') }}</span>
        </div>
        <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">{{ t('peerMcp_description') }}</p>
      </div>
    </div>

    <label class="mt-5 flex min-h-11 cursor-pointer items-center justify-between gap-3 border-y border-slate-100 py-3 text-sm text-slate-700 dark:border-slate-800 dark:text-slate-200">
      <span>
        <span class="block text-xs font-semibold">{{ t('peerMcp_enable') }}</span>
        <span class="mt-0.5 block text-[11px] leading-4 text-slate-500 dark:text-slate-400">{{ t('peerMcp_hint') }}</span>
      </span>
      <input
        type="checkbox"
        data-testid="peer-mcp-enable"
        class="h-4 w-4 shrink-0 rounded border-slate-300 text-primary focus:ring-primary/20 dark:border-slate-600 dark:bg-slate-900"
        :checked="form.enabled"
        :disabled="loading || saving"
        @change="form.enabled = $event.target.checked"
      />
    </label>

    <ul class="mt-3 flex flex-wrap gap-1.5">
      <li
        v-for="tool in tools"
        :key="tool"
        class="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-[10px] text-slate-600 dark:bg-slate-800 dark:text-slate-300"
      >{{ tool }}</li>
    </ul>

    <div class="mt-3 flex flex-wrap items-center gap-2 text-[11px]" data-testid="peer-mcp-registration">
      <template v-if="statusAvailable">
        <span
          v-for="row in registration"
          :key="row.kernel"
          class="rounded px-1.5 py-0.5 font-bold"
          :class="stateClass(row.state)"
        >{{ row.kernel }}:{{ stateLabel(row.state) }}</span>
        <!-- spec §7 诚实回显:逐内核失败原因短文案,不做静默假绿 -->
        <span
          v-for="row in failedRows"
          :key="'err-' + row.kernel"
          class="w-full text-red-600 dark:text-red-400"
        >{{ row.kernel }}:{{ row.error }}</span>
      </template>
      <span v-else class="text-slate-400">{{ t('peerMcp_statusUnavailable') }}</span>
    </div>

    <div v-if="error" class="mt-3 text-xs text-red-600 dark:text-red-400" role="status">{{ error }}</div>
    <div v-else-if="successMessage" class="mt-3 text-xs text-emerald-600 dark:text-emerald-400" role="status">{{ successMessage }}</div>

    <button
      type="button"
      class="mt-auto inline-flex h-11 self-end items-center gap-1.5 rounded-lg bg-primary px-4 text-xs font-bold text-white transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30 disabled:opacity-50"
      :disabled="loading || saving"
      @click="save"
    >
      {{ saving ? t('peerMcp_saving') : t('peerMcp_save') }}
    </button>
  </div>
</template>

<script>
import { useI18n } from '../../i18n';
import { getPeerMCPStatus, setPeerMCPRegistration } from '../../services/agentApi';

// 工具名按 stdio 面展示(本卡是 claude/codex 的入口,以它们实际可见的名字为准)。
const PEER_TOOLS = ['peer_exec', 'peer_fetch_file', 'device_list', 'device_info', 'device_note'];

export default {
  name: 'PeerMCPSettings',
  setup() {
    const { t } = useI18n();
    return { t };
  },
  props: {
    config: { type: Object, default: () => ({}) },
    loading: { type: Boolean, default: false },
    saving: { type: Boolean, default: false },
    error: { type: String, default: '' },
    successMessage: { type: String, default: '' }
  },
  emits: ['save'],
  computed: {
    failedRows() {
      return this.registration.filter((row) => row.error);
    }
  },
  data() {
    return {
      form: { enabled: this.formFromConfig(this.config) },
      tools: PEER_TOOLS,
      registration: [],
      statusAvailable: false
    };
  },
  watch: {
    config: {
      deep: true,
      handler(nextConfig) {
        this.form.enabled = this.formFromConfig(nextConfig);
      }
    }
  },
  mounted() {
    this.loadRegistration();
  },
  methods: {
    formFromConfig(config) {
      const raw = config?.peer_mcp?.enable;
      return typeof raw === 'boolean' ? raw : true;
    },
    async loadRegistration() {
      try {
        const data = await getPeerMCPStatus();
        this.registration = Array.isArray(data?.registration) ? data.registration : [];
        this.statusAvailable = true;
      } catch (_) {
        this.statusAvailable = false;
      }
    },
    stateLabel(state) {
      const key = `peerMcp_state_${state}`;
      const label = this.t(key);
      // 缺键回落键名本身(i18n 行为),此时展示原始值更可读
      return label === key ? String(state) : label;
    },
    stateClass(state) {
      if (state === 'registered') return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300';
      if (state === 'stale') return 'bg-amber-50 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300';
      if (state === 'missing') return 'bg-slate-100 text-slate-500 dark:bg-slate-800 dark:text-slate-400';
      return 'bg-red-50 text-red-600 dark:bg-red-900/30 dark:text-red-300';
    },
    async save() {
      const enable = !!this.form.enabled;
      // 两段式:①emit 让父页持久化 customer.peer_mcp(core 落盘/DB);
      // ②本端点让 runtime 立即应用注册(写内核配置 + 内存同步)。
      this.$emit('save', { ...(this.config || {}), peer_mcp: { enable } });
      try {
        const data = await setPeerMCPRegistration(enable);
        this.registration = Array.isArray(data?.registration) ? data.registration : [];
        this.statusAvailable = true;
      } catch (_) {
        this.statusAvailable = false;
      }
    }
  }
};
</script>
```

- [ ] **Step 6: agentApi.js 加函数**

在 `app/website/src/services/agentApi.js` 按 `setAgentScanDirectories`(:86-91)的既有形态追加:

```js
// aliang-peer 跨设备 MCP:GET 注册状态 / POST 应用注册(runtime 执行)。
export function getPeerMCPStatus() {
  return request('/api/agent/peer/mcp', { method: 'GET' });
}

export function setPeerMCPRegistration(enable) {
  return request('/api/agent/peer/mcp', {
    method: 'POST',
    body: JSON.stringify({ enable })
  });
}
```

(若该文件的 request 封装要求显式 `headers: { 'Content-Type': 'application/json' }`,照 `setAgentScanDirectories` 的写法补齐。)

- [ ] **Step 7: i18n 键组**

`app/website/src/i18n/zh.js` 在 `mcpGlobal_` 键组(:576-581)之后加:

```js
  // -- PeerMCPSettings --
  peerMcp_title: '设备互访 (Aliang Peer)',
  peerMcp_description: '让 claude/codex 内核获得跨设备工具(列出设备、拉取文件、远程执行)。目标机的访问仍受对方设备开关与访问控制约束。',
  peerMcp_managed: '平台托管',
  peerMcp_enable: '启用跨设备工具面',
  peerMcp_hint: '关闭后三内核(claude/codex/agentcore)都不再暴露这组工具,已注册条目会被移除。',
  peerMcp_state_registered: '已注册',
  peerMcp_state_missing: '未注册',
  peerMcp_state_stale: '路径漂移',
  peerMcp_state_error: '失败',
  peerMcp_statusUnavailable: '注册状态不可用(agent 未响应)',
  peerMcp_save: '保存',
  peerMcp_saving: '保存中',
```

`en.js` 同位置加英文对应(title: 'Device Mesh (Aliang Peer)' 等,风格对照 en.js:577-582 的 mcpGlobal 组)。

- [ ] **Step 8: 跑前端测试 + 构建**

```bash
cd app/website && npx vitest run src/utils/agentCoreSettings.test.js 2>&1 | tail -5
npx vitest run 2>&1 | tail -5
npm run build 2>&1 | tail -3
```

Expected: 归一层测试 PASS;vitest 全量无新红;build 成功。同时跑移动端哨兵(e2e 若本地可跑):

```bash
npx playwright test tests/e2e/mobile-responsive.spec.js 2>&1 | tail -3
```

(新卡让 capability 分区变高,哨兵若红需按 md: 还原桌面模式修模板 class,守则见仓库 mobile-responsive 约定。)

- [ ] **Step 9: Commit**

```bash
git add app/website/src/utils/agentCoreSettings.js app/website/src/utils/agentCoreSettings.test.js app/website/src/components/SettingsPage.vue app/website/src/components/settings/PeerMCPSettings.vue app/website/src/services/agentApi.js app/website/src/i18n/zh.js app/website/src/i18n/en.js
git commit -m "$(cat <<'EOF'
新增:设置页「设备互访 (Aliang Peer)」平台托管卡(总开关+注册状态回显+归一层 peer_mcp 透传+i18n)

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: 全量回归

- [ ] **Step 1: Go 全测(目标包)**

```bash
go test ./processor/config/ ./cmd/ ./app/agentruntime/ 2>&1 | tail -5
go test ./app/http/services/ 2>&1 | tail -5
```

Expected: PASS(排除执行守则第 3 条的已知预存红名单;`app/http/services` 全包跑较慢属正常)。

- [ ] **Step 2: 前端全量**

```bash
cd app/website && npx vitest run 2>&1 | tail -3 && npm run build 2>&1 | tail -2
```

- [ ] **Step 3: 汇总**

如有红:逐个判断是否名单内预存;非名单内新红必须修完才算完。

---

### Task 9: 真机验收清单(手动,部署 liang-dev 后)

按 spec §9 执行,部署走标准管线(内存记忆「liang-dev 双单元部署」:本地 tar 精确清单→scp→换树→CGO 构建+版本戳→stop agent/core→备份→换装→enable→先 agent 后 core):

- [ ] 开关默认开:升级后 claude 会话问「列出我的设备」→ AI 调 `device_list` 返回清单;codex 同。
- [ ] 「从 XX 设备拉 ~/a.txt」→ `peer_fetch_file` → AliangInbox 落地,手机收 transfer.progress。
- [ ] 目标机 `peer_exec_enabled` 关闭时 `peer_exec` 返回可读门禁拒绝。
- [ ] 仪表盘关开关:claude/codex 新会话工具消失、agentcore peer 工具消失;卡片状态如实(含 agent 重启后自愈对齐)。
- [ ] 开关开 + 手工删除 `~/.claude.json` 里 aliang-peer 条目 → 重启 agent 自动补回。
- [ ] sanitized 档 + 项目不可信(默认):claude 会话仍可见 aliang-peer 工具(user 作用域默认加载)。
- [ ] ask_user/审批卡/trust tier 零回归(跑一轮既有冒烟)。
- [ ] `cat ~/.codex/config.toml`:用户注释/`[model_providers.aliang]` 完好,托管块在尾部。

---

## 完成定义

- Task 1-8 全部 commit,Go 契约/注册/门控测试绿,前端 vitest+build 绿,路由 parity 绿。
- Task 9 真机清单全勾。
- 更新记忆(内核感知通道全景定案的「CLI provider 无 peer 工具」遗留项闭环)。
