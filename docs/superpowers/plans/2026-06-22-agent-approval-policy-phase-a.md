# Agent AI 审批策略模板 — Phase A（agent 端）实现计划

> **STATUS: Phase A COMPLETE (2026-06-22).** Tasks 1-9 done via TDD. vet + build clean;
> 19 policy/approval tests green; full services suite green (only a known pre-existing
> terminal PTY flake `TestAgentTerminalManagerRejectsUnsafeRemoteExecution`, unrelated).
> Not committed (per CLAUDE.md, awaits explicit user request).


> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:test-driven-development. Steps use checkbox (`- [ ]`) syntax.
> 本计划只覆盖 **Phase A（agent，本仓库 Go）**。Phase B（server）/ C（mobile）在另两个仓库，独立计划另写。
> **提交策略**：遵 `CLAUDE.md`，本计划不自动 `git commit`；每个任务实现+测试通过后留给用户提交。

**Goal:** 在 agent 端实现审批策略求值引擎 + 缓存 + hash 同步，使只读/无害工具本地秒放行、不上报服务端，只有文件改写/危险命令/兜底才上报；内置 balanced 默认模板，失联时优雅退化。

**Architecture:** 新增 `agent_approval_policy.go`（类型 + 纯函数求值引擎 + 缓存 + HTTP 同步 + 内置默认）；在两处审批入口（`handleClaudeApprovalHook` / `codexAppServerApprovalResult`）调用 `requestApproval` 之前插求值；在 `runUserMessage` spawn 前做 hash 校验；`applyRemoteDeviceSettings` 识别 policy hash 变化触发重拉。求值是纯函数，TDD 友好。

**Tech Stack:** Go（`aliang.one/nursorgate`），既有测试隔离套路（`ALIANG_DATA_DIR` + `cache.ResetCacheDirForTest` + `auth.ResetAuthPersistenceForTest` + `config.ResetGlobalConfigForTest`），`encoding/json`，`net/http`（复用 agent→server 既有鉴权 HTTP 调用），SHA-256。

**Spec:** `docs/superpowers/specs/2026-06-22-agent-approval-policy-template.md`

---

## 文件结构

| 文件 | 责任 | 动作 |
|---|---|---|
| `app/http/services/agent_approval_policy.go` | 类型（`ApprovalPolicy`/`ApprovalRule`/`policyDecision`）+ 内置 balanced/allow-all 默认 + `evaluateApprovalPolicy` 纯函数 + 缓存 load/save + HTTP hash/full 同步 + `ensurePolicyBeforeRun` + AgentService 持有/读取生效策略 | 新增 |
| `app/http/services/agent_approval_policy_test.go` | 引擎各分支 + 排序 + fail-safe + allow-all + 同步(命中/不命中/失联退化) + 缓存重启幸存 | 新增 |
| `app/http/services/agent_ai.go` | `handleClaudeApprovalHook:1010` + `codexAppServerApprovalResult:1958` 插求值短路；`runUserMessage:524` 插 `ensurePolicyBeforeRun` | 改 |
| `app/http/services/agent_service.go` | `applyRemoteDeviceSettings` 识别 `approval_policy.hash` 变化 → 标记需重拉 | 改 |
| `app/http/models/agent_protocol.go` | 审批请求契约说明增 `policy_version`/`matched_rule_id`（可选） | 改 |
| `app/http/services/agent_ai.go`（`agentAIApprovalRequest`/`agentAIApprovalRequestPayload`） | 请求结构 + payload 带上下文字段 | 改 |

---

## Task 1：类型 + 内置默认模板

**Files:**
- Create: `app/http/services/agent_approval_policy.go`
- Test: `app/http/services/agent_approval_policy_test.go`

- [ ] **Step 1: 写失败测试（内置 balanced 默认的存在与形状）**

```go
func TestBuiltinBalancedDefaultPolicy(t *testing.T) {
	p := builtinBalancedPolicy()
	if p.Scheme != "balanced" {
		t.Fatalf("scheme = %q, want balanced", p.Scheme)
	}
	if p.DefaultDecision != decisionRequireApproval {
		t.Fatalf("default = %q, want require_approval (fail-safe)", p.DefaultDecision)
	}
	ids := ruleIDs(p.Rules)
	if ids[0] != "dangerous-bash" {
		t.Fatalf("first rule must be dangerous-bash (defense-in-depth), got %v", ids)
	}
	if len(p.Rules) < 4 {
		t.Fatalf("expected >=4 rules, got %d", len(p.Rules))
	}
}
```

- [ ] **Step 2: 运行，确认 FAIL**（`undefined: builtinBalancedPolicy`）

Run: `go test ./app/http/services/ -run TestBuiltinBalancedDefaultPolicy -v`
Expected: FAIL — 符号未定义。

- [ ] **Step 3: 最小实现** — 定义类型 + `decisionXxx` 常量 + `builtinBalancedPolicy()`（按 spec 默认模板 4 条规则，dangerous-first）+ `builtinAllowAllPolicy()` + 辅助 `ruleIDs`（放测试文件或 export）。

```go
package services

type policyDecision string
const (
	decisionAutoApprove     policyDecision = "auto_approve"
	decisionRequireApproval policyDecision = "require_approval"
	decisionAutoDeny        policyDecision = "auto_deny"
)

type approvalRuleMatch struct {
	Tool         []string `json:"tool,omitempty"`
	CommandRegex string   `json:"command_regex,omitempty"`
}

type approvalRule struct {
	ID       string             `json:"id"`
	Match    approvalRuleMatch  `json:"match"`
	Decision policyDecision     `json:"decision"`
	Reason   string             `json:"reason,omitempty"`
	// compiled at load time, not serialized
	cmdRe *regexp.Regexp `json:"-"`
}

type ApprovalPolicy struct {
	Version         int            `json:"version"`
	Hash            string         `json:"hash,omitempty"`
	Scheme          string         `json:"scheme"`
	DeviceID        string         `json:"device_id,omitempty"`
	Rules           []approvalRule `json:"rules"`
	DefaultDecision policyDecision `json:"default_decision"`
}
```

- [ ] **Step 4: 运行，确认 PASS**

Run: `go test ./app/http/services/ -run TestBuiltinBalancedDefaultPolicy -v`
Expected: PASS。

- [ ] **Step 5: 留给用户提交**（不自动 commit）

---

## Task 2：求值引擎（核心纯函数，TDD）

**Files:**
- Modify: `app/http/services/agent_approval_policy.go`
- Test: `app/http/services/agent_approval_policy_test.go`

- [ ] **Step 1: 写一组失败测试（一个行为一个测试）**

```go
func TestEvaluateReadonlyToolAutoApproves(t *testing.T) {
	d, _ := evaluateApprovalPolicy(builtinBalancedPolicy(), "Read", nil)
	if d != decisionAutoApprove { t.Fatal("Read should auto-approve") }
}
func TestEvaluateFileMutationEscalates(t *testing.T) {
	d, _ := evaluateApprovalPolicy(builtinBalancedPolicy(), "Edit", nil)
	if d != decisionRequireApproval { t.Fatal("Edit should escalate") }
}
func TestEvaluateDangerousBashEscalates(t *testing.T) {
	d, _ := evaluateApprovalPolicy(builtinBalancedPolicy(), "Bash", json.RawMessage(`{"command":"rm -rf /tmp/x"}`))
	if d != decisionRequireApproval { t.Fatal("rm -rf should escalate") }
}
func TestEvaluateSafeBashApproves(t *testing.T) {
	d, _ := evaluateApprovalPolicy(builtinBalancedPolicy(), "Bash", json.RawMessage(`{"command":"grep foo bar.txt"}`))
	if d != decisionAutoApprove { t.Fatal("grep should approve") }
}
func TestEvaluateUnknownToolFailSafe(t *testing.T) {
	d, _ := evaluateApprovalPolicy(builtinBalancedPolicy(), "SomeNewTool", nil)
	if d != decisionRequireApproval { t.Fatal("unknown must fail-safe escalate") }
}
func TestEvaluateAllowAllApprovesEverything(t *testing.T) {
	for _, tool := range []string{"Bash","Edit","Read"} {
		d, _ := evaluateApprovalPolicy(builtinAllowAllPolicy(), tool, json.RawMessage(`{"command":"rm -rf /"}`))
		if d != decisionAutoApprove { t.Fatalf("allow-all must approve %s", tool) }
	}
}
func TestEvaluateDangerousFirstBeatsBroadSafeRule(t *testing.T) {
	// 即便存在宽泛 `git -> approve` 安全规则，`git push` 仍须被前置危险规则命中
	p := ApprovalPolicy{
		Rules: []approvalRule{
			{idMatch("Bash","git\\s+push"), decisionRequireApproval},
			{idMatch("Bash","^git\\b"), decisionAutoApprove}, // 宽泛安全规则
		},
		DefaultDecision: decisionRequireApproval,
	}
	d, _ := evaluateApprovalPolicy(p, "Bash", json.RawMessage(`{"command":"git push origin main"}`))
	if d != decisionRequireApproval { t.Fatal("dangerous-first must win") }
}
```
（`idMatch` 为测试辅助：构造一条 rule。）

- [ ] **Step 2: 运行，确认 FAIL**（`undefined: evaluateApprovalPolicy`）

Run: `go test ./app/http/services/ -run 'TestEvaluate' -v`

- [ ] **Step 3: 实现 `evaluateApprovalPolicy`**

```go
// 首条命中生效；都不命中 -> DefaultDecision。返回 (decision, matchedRuleID)。
func evaluateApprovalPolicy(p ApprovalPolicy, toolName string, toolInput json.RawMessage) (policyDecision, string) {
	toolName = strings.TrimSpace(toolName)
	for _, r := range p.Rules {
		if ruleMatches(r, toolName, toolInput) {
			return r.Decision, r.ID
		}
	}
	return p.DefaultDecision, ""
}

// ruleMatches：tool 列表 OR；command_regex 仅对命中 tool 时校验（若有）。
func ruleMatches(r approvalRule, toolName string, toolInput json.RawMessage) bool {
	if len(r.Match.Tool) > 0 && !containsString(r.Match.Tool, toolName) {
		return false
	}
	if strings.TrimSpace(r.Match.CommandRegex) == "" {
		return true
	}
	re := r.compiled() // lazy compile + cache
	cmd := extractBashCommand(toolInput) // 从 tool_input.command 取；空则不命中 regex
	return cmd != "" && re.MatchString(cmd)
}
```
（`compiled()` 懒编译并缓存到 `r.cmdRe`；`extractBashCommand` 从 `{"command":"..."}` 取命令字符串。）

- [ ] **Step 4: 运行，确认 PASS**

Run: `go test ./app/http/services/ -run 'TestEvaluate' -v`
Expected: 全 PASS。

- [ ] **Step 5: 留给用户提交**

---

## Task 3：缓存 load/save（`approval_policy.json`，同 device_identity 套路）

**Files:**
- Modify: `app/http/services/agent_approval_policy.go`
- Test: `app/http/services/agent_approval_policy_test.go`

- [ ] **Step 1: 写失败测试（重启幸存 + 损坏容忍）**

```go
func TestPolicyCacheSurvivesRestart(t *testing.T) {
	setupAgentPolicyTestEnv(t) // ALIANG_DATA_DIR + resets
	p := builtinBalancedPolicy(); p.Version = 42; p.Hash = "sha256:abc"
	if err := savePolicyCache(p); err != nil { t.Fatal(err) }
	got, ok := loadPolicyCache()
	if !ok || got.Version != 42 || got.Hash != "sha256:abc" {
		t.Fatalf("cache not recovered: ok=%v v=%d", ok, got.Version)
	}
}
func TestPolicyCacheCorruptReturnsNone(t *testing.T) {
	setupAgentPolicyTestEnv(t)
	path, _ := policyCachePath()
	os.WriteFile(path, []byte("{not json"), 0o600)
	if _, ok := loadPolicyCache(); ok { t.Fatal("corrupt cache must yield ok=false") }
}
```

- [ ] **Step 2: 运行，确认 FAIL**

Run: `go test ./app/http/services/ -run 'TestPolicyCache' -v`

- [ ] **Step 3: 实现 `policyCachePath` / `loadPolicyCache` / `savePolicyCache`**

参考既有 `agentIdentityPath()`/`loadAgentDeviceIdentity()`/`saveAgentDeviceIdentity()`（`agent_service.go`）：同目录（`filepath.Dir(agentStatePath())`）下 `approval_policy.json`，原子写（tmp 0o600 + `os.Rename`），损坏/缺失返回 `ok=false`。

- [ ] **Step 4: 运行，确认 PASS**

Run: `go test ./app/http/services/ -run 'TestPolicyCache' -v`

- [ ] **Step 5: 留给用户提交**

---

## Task 4：生效策略持有 + `effectiveApprovalPolicy()`（带兜底链）

**Files:**
- Modify: `app/http/services/agent_approval_policy.go`、`agent_service.go`

- [ ] **Step 1: 写失败测试（兜底优先级：内存 > 缓存 > 内置 balanced）**

```go
func TestEffectivePolicyFallbackChain(t *testing.T) {
	setupAgentPolicyTestEnv(t)
	svc := NewAgentService()
	// 无内存、无缓存 -> 内置 balanced
	p := svc.effectiveApprovalPolicy()
	if p.Scheme != "balanced" { t.Fatal("must fall back to builtin balanced") }
}
```

- [ ] **Step 2: 运行 FAIL**（`undefined: (*AgentService).effectiveApprovalPolicy`）

- [ ] **Step 3: 实现**
  - AgentService 增字段 `policy *ApprovalPolicy` + `policyMu sync.Mutex`（或复用既有锁）。
  - `effectiveApprovalPolicy()`：内存有→用；否则 load 缓存；缓存无→`builtinBalancedPolicy()`。**永不返回 allow-all 作为兜底。**

- [ ] **Step 4: 运行 PASS**

Run: `go test ./app/http/services/ -run TestEffectivePolicyFallbackChain -v`

- [ ] **Step 5: 留给用户提交**

---

## Task 5：两处审批入口插求值短路

**Files:**
- Modify: `app/http/services/agent_ai.go`（`handleClaudeApprovalHook:1010`、`codexAppServerApprovalResult:1958`）
- Test: `app/http/services/agent_approval_policy_test.go`

- [ ] **Step 1: 写失败测试（行为级：Read 触发 hook 不产生 `ai.approval.request`）**
  用现有 `agentAISession` + 假 hook payload 驱动 `handleClaudeApprovalHook`，断言：tool=Read 时返回 `permissionDecision=allow` 且 `requestApproval` **未被调用**（可用计数 writer / spy）。tool=Edit 时走 escalate。

- [ ] **Step 2: 运行 FAIL**

- [ ] **Step 3: 集成**
  在 `handleClaudeApprovalHook` 取 `run`/`writeJSON` 之后、`buildClaudeApprovalRequest` 之前插入：

```go
toolName := firstNonEmpty(remoteString(raw, "tool_name"), remoteString(raw, "toolName"))
toolInput := marshalAgentAIRaw(raw["tool_input"])
decision, matchedID := m.service.effectiveApprovalPolicy().evaluate(toolName, toolInput) // 或包级 evaluateApprovalPolicy
switch decision {
case decisionAutoApprove:
	logger.Info(fmt.Sprintf("approval-hook: AUTO-APPROVE by policy rule=%s tool=%s", matchedID, toolName))
	return claudeApprovalHookDecision(hookEventName, true, "auto-approved by policy: "+matchedID), nil
case decisionAutoDeny:
	return claudeApprovalHookDecision(hookEventName, false, "denied by policy: "+matchedID), nil
case decisionRequireApproval:
	// 既有路径；req 附 MatchedRuleID/PolicyVersion
}
```
  对 `codexAppServerApprovalResult` 做对称处理（Codex 路径在调 `requestApproval` 前用同一求值）。

  > AgentService 与 agentAIManager 的引用关系：`agentAIManager` 已能拿到必要上下文；若 `m.service` 不存在，走既有 `GetSharedAgentService()` 或在 manager 构造时注入 service。

- [ ] **Step 4: 运行 PASS**（新测试 + 既有审批测试不回归）

Run: `go test ./app/http/services/ -run 'AgentAI|Approval' -v`

- [ ] **Step 5: 留给用户提交**

---

## Task 6：HTTP 同步（hash/full）+ `ensurePolicyBeforeRun`

**Files:**
- Modify: `app/http/services/agent_approval_policy.go`、`agent_ai.go`（`runUserMessage:524`）

- [ ] **Step 1: 写失败测试（httptest server：hash 命中不重拉；不命中重拉并落盘；fetch 失败退化缓存/内置）**

```go
func TestEnsurePolicyHashMatchNoRefetch(t *testing.T) { /* server 只应被 hit 一次 hash */ }
func TestEnsurePolicyHashMismatchRefetchesAndCaches(t *testing.T) { /* full 被拉取，缓存更新 */ }
func TestEnsurePolicyFetchFailsFallsBackGracefully(t *testing.T) { /* server 500 -> 用缓存/内置，不 error */ }
```

- [ ] **Step 2: 运行 FAIL**

- [ ] **Step 3: 实现**
  - 定位 agent→server 既有鉴权 HTTP 调用（`registerAndSyncLocked` 用的 client / `device_token` Bearer 头）。复用之。
  - `fetchPolicyHash(ctx) (version int, hash string, err error)`：`GET {AgentServer}/api/agent/approval-policy/hash`，device_token 鉴权，超时 3s。
  - `fetchPolicy(ctx) (ApprovalPolicy, error)`：`GET {AgentServer}/api/agent/approval-policy`。
  - `ensurePolicyBeforeRun(ctx)`：取内存策略 hash → 拉远端 hash → 相等则返回；不等→拉 full→校验 hash→落盘+内存；任一失败→记 warn，用 `effectiveApprovalPolicy()` 兜底，**不返回 error**（绝不阻塞 AI）。
  - `runUserMessage`（`agent_ai.go:524`）在 `go m.runCLI(...)` 前 `m.service.ensurePolicyBeforeRun(ctx)`（best-effort，可 `go` 异步但需在 runCLI 前完成或容忍并发；保守用同步短超时）。

- [ ] **Step 4: 运行 PASS**

Run: `go test ./app/http/services/ -run 'TestEnsurePolicy' -v`

- [ ] **Step 5: 留给用户提交**

---

## Task 7：`applyRemoteDeviceSettings` 识别 hash 变化触发重拉

**Files:**
- Modify: `app/http/services/agent_service.go`（`applyRemoteDeviceSettings`）

- [ ] **Step 1: 写失败测试（收到 device.settings.updated 带 approval_policy.hash 与本地不同 → 触发 ensurePolicy）**

- [ ] **Step 2: 运行 FAIL**

- [ ] **Step 3: 实现**
  在 `applyRemoteDeviceSettings` 解析 `approval_policy.hash`（若存在），与 `effectiveApprovalPolicy().Hash` 比对；不同则异步 `ensurePolicyBeforeRun(ctx)`（best-effort）。

- [ ] **Step 4: 运行 PASS**

Run: `go test ./app/http/services/ -run 'RemoteDeviceSettings|Policy' -v`

- [ ] **Step 5: 留给用户提交**

---

## Task 8：审批请求附 policy 上下文 + 契约说明

**Files:**
- Modify: `app/http/services/agent_ai.go`（`agentAIApprovalRequest`、`agentAIApprovalRequestPayload`）、`app/http/models/agent_protocol.go`

- [ ] **Step 1: 写失败测试（escalate 路径 payload 含 `policy_version` + `matched_rule_id`）**

- [ ] **Step 2: 运行 FAIL**

- [ ] **Step 3: 实现**
  - `agentAIApprovalRequest` 增 `MatchedRuleID string` / `PolicyVersion int`（`buildClaudeApprovalRequest` 填入，来自 Task 5 的 `matchedID`/`policy.Version`）。
  - `agentAIApprovalRequestPayload` 序列化这俩字段（`matched_rule_id`/`policy_version`，`omitempty`）。
  - `agent_protocol.go` 契约注释增这两个可选字段（供服务端/手机端展示"为何上报"）。

- [ ] **Step 4: 运行 PASS**（既有 `agent_ai_approval_proto_test.go` 不回归）

Run: `go test ./app/http/services/ -run 'AgentAI' -v`

- [ ] **Step 5: 留给用户提交**

---

## Task 9：全量验证

- [ ] `go vet ./app/http/services/... ./app/http/models/...`
- [ ] `go build ./app/... ./processor/... ./cmd/...`
- [ ] `go test ./app/http/services/`（除既有无关 flake 外全绿）
- [ ] 人工核对：`handleClaudeApprovalHook` 对 Read/Grep 现在返回 allow 且不产生 `ai.approval.request`（本地日志 `AUTO-APPROVE by policy`）

---

## 完成定义（Phase A）

- 引擎 + 缓存 + hash 同步 + 两入口集成 + settings 触发重拉 + 上下文字段，全部 TDD 通过。
- **失联/无服务端时退化到内置 balanced**，立即消除 read/grep/TaskUpdate 审批洪流。
- 不改既有 `ai.approval.*` 送达协议；未升级服务端时优雅退化，零回归。
- 代码未自动提交，留给用户。

## 后续（独立计划）

- **Phase B（server，`AliangPhoneServer`）**：2 表 + device settings 扩 `approval_policy` + resolve/hash + agent-facing API + 契约 `docs/agent-cloud-contract` 更新 + `npm run contract:agent`。
- **Phase C（mobile，`AliangVibeCodingPhone`）**：`DeviceDetailScreen` scheme 选择 + custom 勾选；`ApprovalCenterScreen` 展示 policy 上下文。
