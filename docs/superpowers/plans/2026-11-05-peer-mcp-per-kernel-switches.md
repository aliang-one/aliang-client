# peer MCP 按内核细分开关 Implementation Plan

> **执行状态(2026-11-05):全部任务已执行完毕,改动留在 worktree `feature/peer-mcp-per-kernel` 未提交(等用户指示)。**
> 验证:config/services/handlers 三包 PeerMCP 用例全绿(新增逐内核矩阵/自愈/回显形状/载荷真相/handler 400-405 契约);vitest 269/269;mobile e2e 10/10;vite build 绿。
> 预存失败(基线复现,非回归):services `TestJournalTruncateBeforeKeepsPrefixWithAttachedRows`(master 同挂)、e2e `chat-device-sessions › tool_timeout`(上轮已钉)。
> 执行偏差:①405 分支顺手按 HandleTerminalScrollback 惯例修了直传 HTTP 405 被映射成 500 的老毛病(测试抓到);②基线又前进(04563ae 已把卡收进设备互访子 tab),Task 4 范围按计划基线注记收窄。

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `customer.peer_mcp` 升级为「总开关+三目标子键」(agentcore 工具面/claudecode 注册/codex 注册),设备互访卡内可独立点击,逐内核门控/自愈/回显。

**Architecture:** 三态指针(nil=跟随总开关)扩展 PeerMCPConfig;三读点各换各的有效值;apply 原语下沉单内核,自愈与 POST 端点按目标应用;POST 载荷从布尔改为完整 peer_mcp 配置对象(保两段式立即性,规避 runtime 配置同步竞态);状态回显 `registration[]`→`targets[]{kernel,expected,state,error}`。前端卡内加三行细分开关。

**Tech Stack:** Go(config/services/handlers 三态指针 + encoding/json)、Vue 3 Options API、vitest、go test。

**Spec:** `docs/superpowers/specs/2026-11-05-peer-mcp-per-kernel-switches-design.md`

**Worktree:** `.worktrees/peer-mcp-per-kernel`(branch `feature/peer-mcp-per-kernel`,基线 master@9ffe702)。**基线注意:** 04563ae 已把 PeerMCPSettings 卡收进 AgentPage 第四子 tab「设备互访」(peer-access,经 PeerAccessSettings 包装)——本计划不动卡位置,只升级卡内粒度。

**提交纪律:** 全程不自动 commit;完成后报告等指示。

---

### Task 1: config 三子键 + 有效读点(TDD)

**Files:**
- Modify: `processor/config/types.go`(PeerMCPConfig ~行 680、EffectivePeerMCPEnabled ~行 687)
- Test: `processor/config/peer_mcp_test.go`

- [ ] **Step 1: 写失败测试**(追加到 peer_mcp_test.go)

```go
func TestEffectivePeerMCPSubTargets(t *testing.T) {
	b := func(v bool) *bool { return &v }
	cases := []struct {
		name                     string
		master, sub              *bool
		wantAgentcore            bool
		wantClaude, wantCodex    bool
	}{
		{"全缺省", nil, nil, true, true, true},
		{"master关_子true被压制", b(false), b(true), false, false, false},
		{"master开_子nil跟随", b(true), nil, true, true, true},
		{"master开_子false", b(true), b(false), false, false, false},
		{"master缺省_子false", nil, b(false), false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{Customer: &CustomerConfig{PeerMCP: &PeerMCPConfig{
				Enable: tc.master, AgentcoreEnabled: tc.sub, ClaudeEnabled: tc.sub, CodexEnabled: tc.sub,
			}}}
			if got := c.EffectivePeerMCPAgentCoreEnabled(); got != tc.wantAgentcore {
				t.Fatalf("agentcore = %v, want %v", got, tc.wantAgentcore)
			}
			if got := c.EffectivePeerMCPClaudeEnabled(); got != tc.wantClaude {
				t.Fatalf("claude = %v, want %v", got, tc.wantClaude)
			}
			if got := c.EffectivePeerMCPCodexEnabled(); got != tc.wantCodex {
				t.Fatalf("codex = %v, want %v", got, tc.wantCodex)
			}
		})
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./processor/config/ -run TestEffectivePeerMCPSubTargets`
Expected: FAIL(字段/函数不存在)

- [ ] **Step 3: 实现 types.go**

PeerMCPConfig 加三字段(注释:三态 nil=跟随总开关):

```go
type PeerMCPConfig struct {
	Enable           *bool `json:"enable,omitempty"`
	AgentcoreEnabled *bool `json:"agentcore_enabled,omitempty"`
	ClaudeEnabled    *bool `json:"claude_enabled,omitempty"`
	CodexEnabled     *bool `json:"codex_enabled,omitempty"`
}
```

EffectivePeerMCPEnabled 之后加私有 helper + 三读点:

```go
// effectivePeerMCPSub 总开关与子键的合取:master(默认真) && (sub nil=跟随→真)。
func (c *Config) effectivePeerMCPSub(sub *bool) bool {
	if !c.EffectivePeerMCPEnabled() {
		return false
	}
	if sub == nil {
		return true
	}
	return *sub
}

func (c *Config) EffectivePeerMCPAgentCoreEnabled() bool {
	if c == nil || c.Customer == nil {
		return true
	}
	return c.effectivePeerMCPSub(c.Customer.PeerMCP.GetAgentcoreEnabled())
}
```

注:`c.Customer.PeerMCP` 可能为 nil——三个读点都要先判 `c.Customer.PeerMCP == nil` 返回 true(与 EffectivePeerMCPEnabled 同款防御)。子指针直接传字段即可,不需要 Get 方法(上面 GetAgentcoreEnabled 示意改为直接字段引用)。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./processor/config/ -run TestEffectivePeerMCP`
Expected: PASS(新旧全部)

---

### Task 2: 单内核 apply 原语 + 自愈/端点/回显逐内核化(TDD)

**Files:**
- Modify: `app/http/services/agent_peer_mcp.go`
- Test: `app/http/services/agent_peer_mcp_test.go`(现有用例改+新用例)

- [ ] **Step 1: 写失败测试**(要点,具体断言按现有用例形态续写)

1. `TestApplyPeerMCPRegistrationPerKernel`:home 注入 fixture,claude=false/codex=true → claude 注销、codex 保持注册,互不影响。
2. `TestEnsurePeerMCPMatchesConfigPerKernel`:全局配置注入(GLOBAL config setter,按现有测试注入模式)master=true+claude_enabled=false+codex 缺省 → 自愈后 claude missing、codex registered。
3. `TestPeerMCPStatusTargets`:状态回显含 `targets` 数组,每项含 kernel/expected/state,expected 与有效读点一致;`registration` 键不存在(一次性切换)。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run 'PeerMCP'`
Expected: 新用例 FAIL(函数不存在/形状不符)

- [ ] **Step 3: 实现 agent_peer_mcp.go**

a) 循环体抽单内核原语,旧签名保留为包装:

```go
// applyPeerMCPKernel 单内核注册对齐原语(幂等,写盘失败返回应用前状态+Error)。
func applyPeerMCPKernel(home, kernel string, register bool) PeerMCPApplyResult {
	// …现 ApplyPeerMCPRegistration 循环体内逻辑原样搬入…
}

func ApplyPeerMCPRegistration(home string, register bool) []PeerMCPApplyResult {
	return []PeerMCPApplyResult{
		applyPeerMCPKernel(home, "claude", register),
		applyPeerMCPKernel(home, "codex", register),
	}
}
```

b) `PeerMCPApplyResult` 加字段:

```go
Expected bool `json:"expected"` // 该内核当前有效值(期望态)
```

c) 新增按配置应用(读全局配置逐内核有效值):

```go
// ApplyPeerMCPRegistrationFromConfig 按当前内存配置逐内核对齐(claude/codex 各自有效值)。
func ApplyPeerMCPRegistrationFromConfig(home string) []PeerMCPApplyResult {
	claude := config.GetGlobalConfig().EffectivePeerMCPClaudeEnabled()
	codex := config.GetGlobalConfig().EffectivePeerMCPCodexEnabled()
	return []PeerMCPApplyResult{
		applyPeerMCPKernel(home, "claude", claude),
		applyPeerMCPKernel(home, "codex", codex),
	}
}
```

d) `EnsurePeerMCPMatchesConfigForTest` → 改调 `ApplyPeerMCPRegistrationFromConfig(home)`。

e) `syncPeerMCPConfigInMemory` 从 `func(bool)` 扩为收配置对象(读其现实现后改:内存 GlobalConfig.Customer.PeerMCP 整体替换为新 cfg,tri-state 保真)。

f) `SetPeerMCPRegistration(cfg *config.PeerMCPConfig)` 签名改为收配置:内存同步用 cfg,应用走 `applyPeerMCPKernel(home,"claude",cfgEffectiveClaude)`(有效值按传入 cfg 现算,不读全局——与现实现「载荷即真相」语义一致),home 缺失分支同款逐内核报错。

g) `PeerMCPStatus` → `targets`:

```go
func peerMCPExpected(kernel string) bool // 按内核调对应 Effective 读点
// Status 返回:{enabled(master), targets[]{kernel,expected,state,error}, status_error}
```

(现 `registration` 键一次性改 `targets`;契约测试与前端同步,无跨端兼容负担。)

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services/ -run 'PeerMCP'`
Expected: PASS(新旧全部;旧双内核布尔用例走包装路径仍绿)

---

### Task 3: agentcore 注入闸换读点 + handler 载荷改造

**Files:**
- Modify: `app/http/services/agent_peer_tools.go`(~行 971 门控)
- Modify: `app/http/handlers/agent_handler.go`(HandlePeerMcp ~行 579-598)

- [ ] **Step 1: 注入闸**

`agentCorePeerTools` 内 `EffectivePeerMCPEnabled()` → `EffectivePeerMCPAgentCoreEnabled()`(注释同步:关=仅 agentcore 工具面关,claude/codex 注册面各自独立)。

- [ ] **Step 2: handler POST**

POST 载荷从 `{enable}` 改为 peer_mcp 配置对象(显式键才有效的三态契约由前端归一层保证,后端指针天然三态):

```go
case http.MethodPost:
	var req struct {
		PeerMCP *config.PeerMCPConfig `json:"peer_mcp"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.PeerMCP == nil {
		common.ErrorBadRequest(w, "Invalid request body: peer_mcp object required", nil)
		return
	}
	common.Success(w, h.service.SetPeerMCPRegistration(req.PeerMCP))
```

(文件若未 import processor/config 则补。GET 不变。)

- [ ] **Step 3: 相关 Go 包全绿**

Run: `go build ./... && go test ./processor/config/ ./app/http/services/ -run 'PeerMCP'`
Expected: PASS(若 services 包有预存 flake 按 repo 惯例单包复跑,勿当回归)

---

### Task 4: 前端——normalize/表单/三行开关/i18n

**Files:**
- Modify: `app/website/src/utils/agentCoreSettings.js`(normalizePeerMCPConfig)
- Modify: `app/website/src/components/settings/PeerMCPSettings.vue`
- Modify: `app/website/src/i18n/zh.js`、`en.js`
- Test: `app/website/src/utils/agentCoreSettings.test.js`(peer_mcp 归一用例扩展;若无则新建最小用例)

- [ ] **Step 1: normalize 扩展**(三态契约:显式布尔才产键)

```js
export function normalizePeerMCPConfig(source) {
  if (!source || typeof source !== 'object' || Array.isArray(source)) {
    return undefined;
  }
  const out = {};
  for (const key of ['enable', 'agentcore_enabled', 'claude_enabled', 'codex_enabled']) {
    if (typeof source[key] === 'boolean') out[key] = source[key];
  }
  return Object.keys(out).length ? out : undefined;
}
```

(注意:现有实现是「有 enable 才返回对象」;新契约四键同权,空对象不落键。SettingsPage 的 `...(peerMcp ? { peer_mcp: peerMcp } : {})` 用法不变。)

- [ ] **Step 2: PeerMCPSettings 表单与模板**

- data:`form.enabled` 旁加 `form.agentcoreEnabled/form.claudeEnabled/form.codexEnabled`,加载 status 时以 `enabled`(master)+targets[].expected 初始化;master 切换时未显式动过的子开关视觉跟随 master(简单策略:子开关无「跟随」态展示,默认值=master 当前值,详见下)。
- **表单策略(简化三态)**:子开关在 UI 上是普通布尔,初始值=对应 target 的 expected(缺省即 master);保存时**总是显式产出四键**。后端 nil=跟随 仅服务旧数据/程序化写入路径,UI 可达状态全部显式——行为等价,免去 touched 跟踪。
- 模板:总开关行下加三行细分(同款 toggle 样式),每行右侧放该内核状态徽章(从 targets[] 取);`expected !== state` 时徽章追加「漂移」小标(`peerMcp_drift` 键)。
- save():`this.$emit('save', { ...(this.config||{}), peer_mcp: { enable, agentcore_enabled, claude_enabled, codex_enabled } })`;`setPeerMCPRegistration(enable)` 调用改为 `setPeerMCPRegistration(payload)`(services/agentApi.js 同步签名),absorbStatus 适配 targets。
- registration→targets:现有 `registration` data/computed/模板引用全部改名 targets(行 43-59 区域)。

- [ ] **Step 3: i18n(zh/en)**

```js
// zh
peerMcp_subAgentcore: 'aliangcode 工具面',
peerMcp_subAgentcoreDesc: 'aliangcode 内核的跨设备工具注入(设备互访五件套)。',
peerMcp_subClaude: '注册到 Claude Code',
peerMcp_subClaudeDesc: '把 aliang-peer MCP 服务端写入 ~/.claude.json(平台托管)。',
peerMcp_subCodex: '注册到 Codex',
peerMcp_subCodexDesc: '把 aliang-peer MCP 服务端写入 ~/.codex/config.toml(平台托管)。',
peerMcp_drift: '漂移',
// en 对应
```

- [ ] **Step 4: 前端全绿**

Run: `npx vitest run && npx vite build 2>&1 | tail -1`
Expected: 全 PASS + 构建成功

---

### Task 5: 全量验证与收尾

- [ ] **Step 1: Go 相关包**

Run: `go build ./... && go test ./processor/config/ ./app/http/services/ ./app/http/handlers/ 2>&1 | tail -5`
Expected: PASS(services/handlers 预存 flake 基线见 repo 惯例;TestSessionAuthorityProxy… 基线 5/10 勿洗白)

- [ ] **Step 2: 前端全套**

Run: `cd app/website && npx vitest run && npx playwright test tests/e2e/mobile-responsive.spec.js 2>&1 | tail -2`
Expected: 全 PASS(起测前 `lsof -iTCP:4174` 清场防旧 server 复用)

- [ ] **Step 3: 冒烟清单**(报告给用户)

1. 设备互访子 tab:总开关关→三细分全灰且保存后 claude/codex 注册被注销
2. 总开关开+claude 细分关→claude 注销、codex 保持;agentcore 工具面对话不出现 peer 五件套
3. 状态徽章漂移提示:手工删掉 ~/.claude.json 的 aliang-peer 条目后刷新,claude 行显示 missing+漂移
4. 旧数据兼容:手写 `{peer_mcp:{enable:false}}` 进 customer 配置→三面全关

- [ ] **Step 4: 报告用户**(不 commit)
