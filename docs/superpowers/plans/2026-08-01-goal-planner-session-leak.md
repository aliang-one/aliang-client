# Goal 规划 session 泄漏修复 — 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 Goal 规划产生的 Codex/Claude 内部 CLI session 不再被库存扫描器上报，从而不再在手机 VibeCoding 列表里生成陌生 `ai_import_` 对话。

**Architecture:** 在已有的 `plannerInternal` 闸口捕获规划运行的 native session id，存入一个持久化（复用 `persistIdentityState`）的「内部 SID 集合」；库存扫描器 `collectAgentVibeSessions` 构建候选后在 200 截断前剔除集合内的 id。覆盖 Codex（`thread.started`/`thread.create` 的 threadID，不进 `onNativeSessionID`）与 Claude/OpenCode（`onNativeSessionID` 的 `plannerInternal` 分支）两条 provider 路径。

**Tech Stack:** Go 1.25（`alianggate` 仓库），标准库 `encoding/json`/`os`/`sync`/`time`，表测 `testing`。

**Spec:** `docs/superpowers/specs/2026-08-01-goal-planner-session-leak-design.md`
**Worktree:** `.worktrees/fix-goal-planner-session-leak`（分支 `fix/goal-planner-session-leak`）

---

## 文件结构

| 文件 | 职责 | 动作 |
|------|------|------|
| `app/http/services/agent_ai_identity.go` | 内部 SID 数据模型、canonical id 归一化、remember/snapshot、持久化 load/save | 修改 |
| `app/http/services/agent_ai.go` | `agentAIManager` 增字段；`onNativeSessionID` 捕获点 A；Codex threadID 捕获点 B | 修改 |
| `app/http/services/agent_inventory.go` | `collectAgentVibeSessions` / `collectAgentSyncSnapshot` 收 snapshot 参数，过滤后截断 | 修改 |
| `app/http/services/agent_service.go` | `VibeSessions()`、`collectAgentSyncSnapshotWithActiveRuns` 传入 snapshot | 修改 |
| `app/http/services/agent_ai_identity_test.go`（或现有测试文件） | 数据模型 / remember / 持久化 / 归一化测试 | 新增/追加 |
| `app/http/services/agent_inventory_test.go`（或现有） | 过滤位置、Projects/授权不受影响、两调用方测试 | 新增/追加 |

> 落地时遵循仓库现有测试文件命名约定；若 `agent_ai_identity_test.go` / `agent_inventory_test.go` 已存在则追加，否则新建。

## 设计要点（与 spec 一致，实现时不得偏离）

- **canonical id**：`claude`/`claudecode` → `claude_<id>`；`codex` → `codex_<id>`。内存集合与过滤都用 canonical id 作 key。
- **剪枝**：按 provider 分桶，每桶 ≤ 512 条，**无 TTL**。512 远大于扫描器 `agentVibeSessionFileScanLimit = 120`。
- **持久化**：复用 `persistIdentityState()`（已是 tmp+rename 原子写）。仅首次插入新条目时落盘；锁内判定+拷贝、锁外写盘；失败只 `log` warn，不阻断 planner。
- **过滤位置（分流）**：`collectAgentSyncSnapshot` 在 `agent_inventory.go:76` 用 `collectAgentProjects(vibeSessions)` **从 vibe 候选派生 Projects**（非独立扫描）。故拆 `collectRawAgentVibeSessions`（不截断不过滤）+ 纯函数 `partitionAgentVibeSessions(full, internal)` 分流：`projectsSource`（未过滤前 200、含 internal）→ Projects/AuthorizedDirectories；`vibe`（过滤后前 200、不含 internal）→ VibeSessions 输出。覆盖两个调用方（`VibeSessions()` :231 用 `collectAgentVibeSessions`；`collectAgentSyncSnapshot` :73 用 partition）。
- **snapshot 传递**：`(m *agentAIManager) internalNativeSessionSnapshot() map[string]bool` 返回拷贝；`AgentService` 经 `s.ai` 取 snapshot 传入。**禁止 package global。**

---

## Task 1: canonical id 归一化 helper（纯函数，TDD）

**Files:**
- Modify: `app/http/services/agent_ai_identity.go`
- Test: `app/http/services/agent_ai_identity_test.go`

- [ ] **Step 1: 写失败测试**

```go
package services

import "testing"

func TestCanonicalInternalInventoryID(t *testing.T) {
	cases := []struct{ provider, native, want string }{
		{"claude", "abc-123", "claude_abc-123"},
		{"claudecode", "abc-123", "claude_abc-123"},   // tool.id 是 claudecode，但扫描器发 claude_
		{"CLAUDE", "x", "claude_x"},                    // 大小写不敏感
		{"codex", "thread-9", "codex_thread-9"},
		{"codex", "", ""},                              // 空 native → 空 key（不记）
		{"unknown", "y", ""},                           // 未知 provider → 空 key
	}
	for _, c := range cases {
		if got := canonicalInternalInventoryID(c.provider, c.native); got != c.want {
			t.Errorf("canonicalInternalInventoryID(%q,%q)=%q want %q", c.provider, c.native, got, c.want)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd .worktrees/fix-goal-planner-session-leak && go test ./app/http/services/ -run TestCanonicalInternalInventoryID -count=1`
Expected: FAIL（函数未定义）

- [ ] **Step 3: 最小实现**

在 `agent_ai_identity.go` 加（紧跟 `agentAIIdentityState` 结构体之后）：

```go
// canonicalInternalInventoryID 把 provider + 原始 native session id 归一化为库存
// 扫描器输出的 id 形式（codex_<id> / claude_<id>）。注意 claudecode 的 tool.id
// 是 "claudecode"，但扫描器 emit 的是 "claude_<id>"——必须归一，否则过滤失效。
// 未知 provider 或空 native 返回 ""（调用方据此跳过记录）。
func canonicalInternalInventoryID(provider, nativeSessionID string) string {
	nativeSessionID = strings.TrimSpace(nativeSessionID)
	if nativeSessionID == "" {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude", "claudecode":
		return "claude_" + nativeSessionID
	case "codex":
		return "codex_" + nativeSessionID
	default:
		return ""
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services/ -run TestCanonicalInternalInventoryID -count=1 -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai_identity.go app/http/services/agent_ai_identity_test.go
git commit -m "feat(agent): 内部 SID canonical id 归一化 helper"
```

---

## Task 2: 内部 SID 数据模型 + manager 内存集合 + remember/snapshot（TDD）

**Files:**
- Modify: `app/http/services/agent_ai_identity.go`（数据模型、remember、snapshot、剪枝）
- Modify: `app/http/services/agent_ai.go`（`agentAIManager` 增字段、`newAgentAIManager` 初始化）
- Test: `app/http/services/agent_ai_identity_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestRememberPlannerNativeSession(t *testing.T) {
	m := newAgentAIManager()

	// 首次插入返回 true（新增），重复插入返回 false（幂等）。
	if !m.rememberPlannerNativeSession("claude", "sid-1") {
		t.Fatal("首次插入应为新增")
	}
	if m.rememberPlannerNativeSession("claude", "sid-1") {
		t.Fatal("重复插入不应算新增")
	}
	// claudecode 与 claude 归一到同一 key。
	m.rememberPlannerNativeSession("claudecode", "sid-1")

	// 未知 provider / 空 native 不记，且不算新增。
	if m.rememberPlannerNativeSession("foo", "x") {
		t.Fatal("未知 provider 不应记")
	}
	if m.rememberPlannerNativeSession("claude", "") {
		t.Fatal("空 native 不应记")
	}

	snap := m.internalNativeSessionSnapshot()
	if !snap["claude_sid-1"] {
		t.Errorf("snapshot 缺 claude_sid-1: %v", snap)
	}
}

func TestPruneInternalSessionsPerProviderBucket(t *testing.T) {
	m := newAgentAIManager()
	// 直接构造确定性时间戳（避免 time.Now() 亚微秒同值导致排序不稳）。Codex cap+10 条，
	// c-0 最旧；Claude 1 条。
	base := time.Now().UTC()
	for i := 0; i < agentInternalSessionBucketCap+10; i++ {
		m.internalNativeSessions[fmt.Sprintf("codex_c-%d", i)] = rememberedInternalSession{
			Provider:        "codex",
			NativeSessionID: fmt.Sprintf("c-%d", i),
			CapturedAt:      base.Add(time.Duration(i) * time.Second), // i 越大越新
		}
	}
	m.internalNativeSessions["claude_keep"] = rememberedInternalSession{
		Provider: "claude", NativeSessionID: "keep", CapturedAt: base,
	}

	m.mu.Lock()
	pruneInternalSessionsLocked(m.internalNativeSessions)
	m.mu.Unlock()

	snap := m.internalNativeSessionSnapshot()
	gotCodex := 0
	for k := range snap {
		if strings.HasPrefix(k, "codex_") {
			gotCodex++
		}
	}
	if gotCodex != agentInternalSessionBucketCap {
		t.Errorf("codex 桶 = %d, want %d（应淘汰最旧至 cap）", gotCodex, agentInternalSessionBucketCap)
	}
	if !snap["claude_keep"] {
		t.Error("Claude 标记被 Codex 挤掉——必须按 provider 分桶")
	}
	if snap["codex_c-0"] {
		t.Error("最旧的 codex_c-0 应被淘汰")
	}
	if !snap[fmt.Sprintf("codex_c-%d", agentInternalSessionBucketCap+9)] {
		t.Error("最新的 codex 条目应保留")
	}
}

func TestInternalNativeSessionSnapshotIsolation(t *testing.T) {
	m := newAgentAIManager()
	m.rememberPlannerNativeSession("claude", "sid")
	snap := m.internalNativeSessionSnapshot()
	snap["claude_injected"] = true // 篡改返回的快照不应影响 manager 内部
	if m.internalNativeSessionSnapshot()["claude_injected"] {
		t.Error("snapshot 必须是拷贝，外部篡改不应泄漏回 manager")
	}
}
```

（imports：测试文件需 `import ("fmt"; "strings"; "testing")`。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run 'TestRememberPlannerNativeSession|TestPruneInternalSessionsPerProviderBucket|TestInternalNativeSessionSnapshot' -count=1`
Expected: FAIL（方法/常量未定义）

- [ ] **Step 3: 实现**

3a. 在 `agent_ai_identity.go` 加常量与数据模型：

```go
const agentInternalSessionBucketCap = 512 // 每 provider 最多记住多少条；远大于扫描器 120 文件窗口

// rememberedInternalSession 是一条「已知为内部（goal planner）」的 native CLI session。
// 用于让库存扫描器跳过它，避免被导入为 ai_import_ 对话。
type rememberedInternalSession struct {
	Provider        string    `json:"provider"`         // canonical: "claude" | "codex"
	NativeSessionID string    `json:"native_session_id"`
	CapturedAt      time.Time `json:"captured_at"`
}
```

3b. 在 `agent_ai.go` 的 `agentAIManager` 结构体（:29）加字段（与 `bindings`/`processedRuns` 同区）：

```go
	internalNativeSessions map[string]rememberedInternalSession // key = canonicalInternalInventoryID
```

3c. 在 `newAgentAIManager`（:701）初始化：

```go
	internalNativeSessions: make(map[string]rememberedInternalSession),
```

3d. 在 `agent_ai_identity.go` 加方法：

```go
// rememberPlannerNativeSession 记录一条 goal planner 内部 CLI 运行的 native session id。
// 返回是否为新增（用于决定是否触发落盘）。幂等；未知 provider/空 native 返回 false。
// 按 provider 分桶，每桶 ≤ agentInternalSessionBucketCap，淘汰最旧——避免大量 Codex
// 记录挤掉仍在 Claude 扫描窗口内的标记。
func (m *agentAIManager) rememberPlannerNativeSession(provider, nativeSessionID string) bool {
	key := canonicalInternalInventoryID(provider, nativeSessionID)
	if key == "" || m == nil {
		return false
	}
	now := time.Now().UTC()
	canonProvider := "codex"
	if strings.HasPrefix(key, "claude_") {
		canonProvider = "claude"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.internalNativeSessions[key]; exists {
		return false
	}
	m.internalNativeSessions[key] = rememberedInternalSession{
		Provider:        canonProvider,
		NativeSessionID: strings.TrimSpace(nativeSessionID),
		CapturedAt:      now,
	}
	pruneInternalSessionsLocked(m.internalNativeSessions)
	return true
}

// pruneInternalSessionsLocked 按 provider 分桶淘汰最旧，使每桶 ≤ cap。调用方持 m.mu。
func pruneInternalSessionsLocked(m map[string]rememberedInternalSession) {
	for provider, over := range bucketOverage(m) {
		// 收集该 provider 的所有 (key, capturedAt)，按时间升序淘汰。
		type entry struct{ key string; at time.Time }
		var es []entry
		for k, v := range m {
			if v.Provider == provider {
				es = append(es, entry{k, v.CapturedAt})
			}
		}
		sort.Slice(es, func(i, j int) bool { return es[i].at.Before(es[j].at) })
		for i := 0; i < over && i < len(es); i++ {
			delete(m, es[i].key)
		}
	}
}

// bucketOverage 返回每个超 cap 的 provider 需淘汰多少条。调用方持 m.mu。
func bucketOverage(m map[string]rememberedInternalSession) map[string]int {
	counts := map[string]int{}
	for _, v := range m {
		counts[v.Provider]++
	}
	over := map[string]int{}
	for p, c := range counts {
		if c > agentInternalSessionBucketCap {
			over[p] = c - agentInternalSessionBucketCap
		}
	}
	return over
}

// internalNativeSessionSnapshot 返回当前所有 canonical id 的拷贝，供库存扫描器过滤。
func (m *agentAIManager) internalNativeSessionSnapshot() map[string]bool {
	out := make(map[string]bool)
	if m == nil {
		return out
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.internalNativeSessions {
		out[k] = true
	}
	return out
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services/ -run 'TestRememberPlannerNativeSession|TestPruneInternalSessionsPerProviderBucket|TestInternalNativeSessionSnapshot' -count=1 -v -race`
Expected: PASS（含 -race）

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai_identity.go app/http/services/agent_ai.go app/http/services/agent_ai_identity_test.go
git commit -m "feat(agent): 内部 SID 内存集合 + remember/snapshot（按 provider 分桶剪枝）"
```

---

## Task 3: 持久化（load + save + 旧 JSON 兼容 + 重启恢复，TDD）

**Files:**
- Modify: `app/http/services/agent_ai_identity.go`（`agentAIIdentityState` 增字段；`loadIdentityState` 载入；`persistIdentityState` 写出）
- Test: `app/http/services/agent_ai_identity_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestInternalSessionPersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	// 用一个绑定了 state 路径的 manager：直接调用 persistIdentityState 需要可写的
	// agentAIIdentityPath()。通过把 state 写到 TempDir 再加载来验证。
	// 见 Step 3 说明：测试用 helper setIdentityPathForTest(dir)（或在测试里直接构造 JSON）。

	// 1) 写一份含 InternalNativeSessions 的 state 文件
	raw := `{
	  "bindings": [],
	  "processed_runs": [],
	  "internal_native_sessions": [
	    {"provider":"codex","native_session_id":"t-1","captured_at":"2026-08-01T00:00:00Z"},
	    {"provider":"claude","native_session_id":"s-1","captured_at":"2026-08-01T00:00:01Z"}
	  ]
	}`
	path := filepath.Join(dir, agentAIIdentityFilename)
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	m := newAgentAIManager()
	// 让 agentAIIdentityPath() 指向 dir（见 Step 3 的可测性改造）。
	withIdentityPath(dir, func() {
		m.loadIdentityState()
	})
	snap := m.internalNativeSessionSnapshot()
	if !snap["codex_t-1"] || !snap["claude_s-1"] {
		t.Fatalf("加载后 snapshot 缺失: %v", snap)
	}
}

func TestInternalSessionOldJSONCompat(t *testing.T) {
	dir := t.TempDir()
	// 旧格式：没有 internal_native_sessions 字段。
	raw := `{"bindings":[{"conversation_id":"c1","native_session_id":"n1","state":"confirmed","binding_version":1,"updated_at":"x"}],"processed_runs":[]}`
	path := filepath.Join(dir, agentAIIdentityFilename)
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newAgentAIManager()
	withIdentityPath(dir, func() {
		m.loadIdentityState()
	})
	// 既有 binding 不丢，internal 集合为空不报错。
	if m.bindings["c1"].NativeSessionID != "n1" {
		t.Error("旧 JSON 的 binding 应保留")
	}
	if len(m.internalNativeSessions) != 0 {
		t.Errorf("旧 JSON 应加载为空集合，got %d", len(m.internalNativeSessions))
	}
}
```

> **可测性说明**：`agentAIIdentityPath()` 当前依赖 `agentStatePath()`（全局/进程级）。为可测，**最小改造**：把路径解析抽成 `var identityPathResolver = agentAIIdentityPath`（包级变量，测试可临时替换），或加 `withIdentityPath(dir, fn)` helper（保存/恢复该 resolver）。Step 3 落地此改造。改造必须不影响生产路径（默认 resolver 行为不变）。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run 'TestInternalSessionPersist|TestInternalSessionOldJSON' -count=1`
Expected: FAIL（`withIdentityPath` 未定义 / 字段不存在）

- [ ] **Step 3: 实现**

3a. `agentAIIdentityState`（:40）增字段：

```go
type agentAIIdentityState struct {
	Bindings              []agentAIBindingRecord       `json:"bindings"`
	ProcessedRuns         []agentAIProcessedRun        `json:"processed_runs"`
	InternalNativeSessions []rememberedInternalSession  `json:"internal_native_sessions,omitempty"`
}
```

3b. 路径可测改造（紧跟 `agentAIIdentityPath`）：

```go
// identityPathResolver 可在测试中替换，使 load/persist 指向 TempDir。生产路径不变。
var identityPathResolver = func() (string, error) {
	statePath, err := agentStatePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(statePath), agentAIIdentityFilename), nil
}

func agentAIIdentityPath() (string, error) { return identityPathResolver() }

func withIdentityPath(dir string, fn func()) {
	prev := identityPathResolver
	identityPathResolver = func() (string, error) {
		return filepath.Join(dir, agentAIIdentityFilename), nil
	}
	defer func() { identityPathResolver = prev }()
	fn()
}
```

3c. `loadIdentityState`（:53）：在载入 processedRuns 的循环后追加：

```go
	for _, s := range state.InternalNativeSessions {
		key := canonicalInternalInventoryID(s.Provider, s.NativeSessionID)
		if key == "" {
			continue
		}
		rec := s
		if rec.CapturedAt.IsZero() {
			rec.CapturedAt = time.Now().UTC()
		}
		// canon provider
		if strings.HasPrefix(key, "claude_") {
			rec.Provider = "claude"
		} else if strings.HasPrefix(key, "codex_") {
			rec.Provider = "codex"
		}
		m.internalNativeSessions[key] = rec
	}
	pruneInternalSessionsLocked(m.internalNativeSessions)
```

3d. `persistIdentityState`（:85 起）：在构造 state 时加：

```go
	state.InternalNativeSessions = make([]rememberedInternalSession, 0, len(m.internalNativeSessions))
	for _, s := range m.internalNativeSessions {
		state.InternalNativeSessions = append(state.InternalNativeSessions, s)
	}
```
（该段在 `m.mu.Lock()` 区内拷贝；写盘前对 InternalNativeSessions 排序：按 CapturedAt 降序，并 cap 到 `agentInternalSessionBucketCap * 2`（两 provider）以防文件膨胀——与现有 `agentAIProcessedRunCap` 截断同构。）

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services/ -run 'TestInternalSessionPersist|TestInternalSessionOldJSON' -count=1 -v -race`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai_identity.go app/http/services/agent_ai_identity_test.go
git commit -m "feat(agent): 内部 SID 持久化（load/save + 旧 JSON 兼容 + 路径可测改造）"
```

---

## Task 4: 捕获点 A — Claude/OpenCode（`onNativeSessionID` 的 plannerInternal 分支）

**Files:**
- Modify: `app/http/services/agent_ai.go:4663-4670`（`onNativeSessionID` 闭包的 plannerInternal 分支）
- Test: `app/http/services/agent_ai_identity_test.go`（remember 已覆盖；此处补「plannerInternal 走 remember、不走 confirmBinding」的单元测试，若 `onNativeSessionID` 可直接构造调用则直接测；否则见 Step 3b 的薄封装测试）

- [ ] **Step 1: 写失败测试**

```go
// observePlannerNativeSessionID 是 onNativeSessionID 里 plannerInternal 分支的薄封装，
// 便于单测：plannerInternal 时记内部 SID 且不绑定；非 planner 时交给 binding（这里只验证不记内部）。
func TestObservePlannerNativeSessionID(t *testing.T) {
	m := newAgentAIManager()
	// plannerInternal 路径：记内部、不算 binding。
	m.observePlannerNativeSessionID(true, "claudecode", "abc")
	if !m.internalNativeSessionSnapshot()["claude_abc"] {
		t.Error("plannerInternal 应记内部 SID claude_abc")
	}
	// 非 planner：不应记内部（binding 由 confirmBinding 负责，另有测试）。
	m2 := newAgentAIManager()
	m2.observePlannerNativeSessionID(false, "claudecode", "xyz")
	if len(m2.internalNativeSessionSnapshot()) != 0 {
		t.Error("非 planner 不应进内部 SID 集合")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestObservePlannerNativeSessionID -count=1`
Expected: FAIL（`observePlannerNativeSessionID` 未定义）

- [ ] **Step 3: 实现**

3a. 在 `agent_ai_identity.go` 加薄封装：

```go
// observePlannerNativeSessionID 在拿到 native session id 时调用。plannerInternal=true
// 表示这是一次 goal 规划内部 CLI 运行——记进内部 SID 集合（供扫描器排除），且不绑定会话。
// plannerInternal=false 时是普通运行：不进内部集合，绑定由 confirmBinding 负责。
func (m *agentAIManager) observePlannerNativeSessionID(plannerInternal bool, provider, nativeSessionID string) {
	if !plannerInternal {
		return
	}
	m.rememberPlannerNativeSession(provider, nativeSessionID)
}
```

3b. 在 `agent_ai.go` 的 `onNativeSessionID`（:4663）plannerInternal 分支改为：

```go
	onNativeSessionID := func(nativeSessionID string) {
		if run.plannerInternal {
			// Planner passes are ephemeral; do not bind as a user conversation, but
			// remember the native id so the inventory scanner can exclude it (else it
			// gets imported as a phantom ai_import_ conversation on the phone).
			manager.observePlannerNativeSessionID(true, tool.id, nativeSessionID)
			return
		}
		// ...（既有 confirmBinding 逻辑不动）
```

> 注意 `tool.id` 对 Claude 是 `"claudecode"`、OpenCode 是 `"opencode"`（未知 provider → canonical 返回 ""，不记——可接受，OpenCode 不产生本机 session_index；如需支持再扩 helper）。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services/ -run TestObservePlannerNativeSessionID -count=1 -v -race`
Expected: PASS

- [ ] **Step 5: 编译验证 + 提交**

Run: `go build ./app/... && go vet ./app/http/services/`
Expected: 通过

```bash
git add app/http/services/agent_ai.go app/http/services/agent_ai_identity.go app/http/services/agent_ai_identity_test.go
git commit -m "feat(agent): 捕获 Claude/OpenCode 规划 native SID 到内部排除集合"
```

---

## Task 5: 捕获点 B — Codex（threadID 首次非空，覆盖两个赋值点）

**Files:**
- Modify: `app/http/services/agent_ai.go`（Codex threadID 捕获区，约 :3622-3925；在 threadID 首次非空的汇聚点调用）
- Test: `app/http/services/agent_ai_identity_test.go`（threadID→SID 提取单测；plannerInternal 时 remember 的接线由 build + 真机验证）

- [ ] **Step 1: 写失败测试（threadID 提取纯函数化）**

把「从 Codex 事件拿到 threadID 后是否应 remember」抽成可测：

```go
// rememberCodexPlannerThread 在 Codex planner 运行拿到非空 threadID 时调用一次。
func TestRememberCodexPlannerThread(t *testing.T) {
	m := newAgentAIManager()
	if !m.rememberPlannerNativeSession("codex", "thread-7") {
		t.Fatal("应记新增")
	}
	if !m.internalNativeSessionSnapshot()["codex_thread-7"] {
		t.Fatal("缺 codex_thread-7")
	}
}
```

- [ ] **Step 2: 跑测试确认失败/通过**

Run: `go test ./app/http/services/ -run TestRememberCodexPlannerThread -count=1`
Expected: 这条复用 Task 2 的 remember，应直接 PASS（确认 codex 路径归一正确）。失败则说明 `codex_` 归一有问题。

- [ ] **Step 3: 实现（接线）**

在 `agent_ai.go` Codex threadID 处理区，找到 threadID 首次变为非空后将用于 resume/steer 的汇聚点（:3635 `setAgentAIResumeSessionIDIfEmpty` 附近是天然候选——它在两个赋值点（`turn/started` ~3742、`thread.create` ~3925）之后都会触达）。在该处加：

```go
	if strings.TrimSpace(threadID) != "" {
		setAgentAIResumeSessionIDIfEmpty(...) // 既有逻辑
		if run.plannerInternal {
			manager.rememberPlannerNativeSession("codex", threadID)
		}
	}
```

> **关键**：不要只埋在 `turn/started` 的 case 分支——`thread.create` 响应路径（~3925，`threadID == ""` 时赋值）也要覆盖。放在 `setAgentAIResumeSessionIDIfEmpty` 这一汇聚点即天然覆盖两者。实现时复核该行确实在两条路径之后都会执行（若不在，则在两个赋值点之后各加一处 guard，条件同为 `run.plannerInternal && threadID != ""`，幂等保护由 remember 提供）。

- [ ] **Step 4: 编译验证**

Run: `go build ./app/... && go vet ./app/http/services/`
Expected: 通过

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai.go app/http/services/agent_ai_identity_test.go
git commit -m "feat(agent): 捕获 Codex 规划 threadID 到内部排除集合（覆盖两个赋值点）"
```

---

## Task 6: 库存扫描器分流过滤（projects 未过滤 / vibe 过滤后截断；两调用方都覆盖）

> **关键纠偏（spec §5.4）**：`collectAgentSyncSnapshot` 在 `agent_inventory.go:76` 用 `collectAgentProjects(vibeSessions)` **从 vibe 候选列表派生 Projects**（不是独立扫描）。所以不能在 `collectAgentProjects` 消费候选前就过滤掉 planner session，否则撤授权。解法：拆出 raw 构建 + 纯函数 `partitionAgentVibeSessions` 分流——projects 取未过滤前 200，vibe 取过滤后前 200。

**Files:**
- Modify: `app/http/services/agent_inventory.go`（拆 `collectRawAgentVibeSessions`；新增 `excludeInternalVibeSessions` + `partitionAgentVibeSessions`；`collectAgentSyncSnapshot` 分流；`collectAgentVibeSessions` 加 internal 参数）
- Modify: `app/http/services/agent_service.go`（`VibeSessions` :230 用 raw+filter+truncate；`collectAgentSyncSnapshotWithActiveRuns` :1999 传 snapshot）
- Test: `app/http/services/agent_inventory_test.go`

- [ ] **Step 1: 写失败测试（纯函数，文件系统无关）**

```go
package services

import (
	"fmt"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

// mkVibe 构造测试用 session，按 idx 决定 UpdatedAt（idx 越小越新，排在前）。
func mkVibe(id string, idx int) models.AgentVibeSession {
	return models.AgentVibeSession{ID: id, UpdatedAt: fmt.Sprintf("2026-08-01T00:00:%02dZ", idx)}
}

func TestExcludeInternalVibeSessions(t *testing.T) {
	internal := map[string]bool{"codex_internal-1": true, "claude_internal-2": true}
	in := []models.AgentVibeSession{
		mkVibe("codex_internal-1", 1),
		mkVibe("claude_internal-2", 2),
		mkVibe("codex_real-3", 3),
		mkVibe("claude_real-4", 4),
	}
	got := excludeInternalVibeSessions(in, internal)
	gotIDs := map[string]bool{}
	for _, s := range got {
		gotIDs[s.ID] = true
	}
	if gotIDs["codex_internal-1"] || gotIDs["claude_internal-2"] {
		t.Errorf("internal 未被过滤: %v", gotIDs)
	}
	if !gotIDs["codex_real-3"] || !gotIDs["claude_real-4"] {
		t.Errorf("真实 session 被误删: %v", gotIDs)
	}
}

// 验证：vibe 先过滤后截断（internal 不占名额）；projectsSource 仍含 internal（授权不丢）。
func TestPartitionAgentVibeSnapshotFilterBeforeTruncateAndProjectsUnfiltered(t *testing.T) {
	internal := map[string]bool{"codex_planner-A": true, "claude_planner-B": true}
	// full 已按 UpdatedAt desc 排序：两条 internal 在最前，其余 200 条真实。
	full := make([]models.AgentVibeSession, 0, 202)
	full = append(full, mkVibe("codex_planner-A", 0), mkVibe("claude_planner-B", 1))
	for i := 0; i < 200; i++ {
		full = append(full, mkVibe(fmt.Sprintf("real-%d", i), 2+i))
	}

	projectsSource, vibe := partitionAgentVibeSessions(full, internal)

	// vibe：不含 internal，且仍是 200 条（internal 没占名额，被真实填满）。
	if len(vibe) != agentVibeSummaryMaxSessions {
		t.Fatalf("vibe len = %d, want %d", len(vibe), agentVibeSummaryMaxSessions)
	}
	vibeIDs := map[string]bool{}
	for _, s := range vibe {
		vibeIDs[s.ID] = true
	}
	if vibeIDs["codex_planner-A"] || vibeIDs["claude_planner-B"] {
		t.Errorf("vibe 不应含 internal: %+v", vibeIDs)
	}
	if !vibeIDs["real-0"] || !vibeIDs["real-199"] {
		t.Errorf("vibe 应含全部 200 真实: 缺 real-0/real-199")
	}

	// projectsSource：前 200，仍含 internal（授权不丢）。
	if len(projectsSource) != agentVibeSummaryMaxSessions {
		t.Fatalf("projectsSource len = %d, want %d", len(projectsSource), agentVibeSummaryMaxSessions)
	}
	psIDs := map[string]bool{}
	for _, s := range projectsSource {
		psIDs[s.ID] = true
	}
	if !psIDs["codex_planner-A"] || !psIDs["claude_planner-B"] {
		t.Errorf("projectsSource 应含 internal（项目授权从未过滤派生）: %+v", psIDs)
	}
}
```

（测试文件需 `import ("fmt"; "testing"; "aliang.one/nursorgate/app/http/models")`。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run 'TestExcludeInternalVibeSessions|TestPartitionAgentVibeSnapshot' -count=1`
Expected: FAIL（`excludeInternalVibeSessions` / `partitionAgentVibeSessions` 未定义）

- [ ] **Step 3: 实现**

3a. 在 `agent_inventory.go` 加两个纯函数：

```go
// excludeInternalVibeSessions 从候选里剔除 canonical id ∈ internal 的 planner 内部 session。
func excludeInternalVibeSessions(in []models.AgentVibeSession, internal map[string]bool) []models.AgentVibeSession {
	if len(internal) == 0 {
		return in
	}
	out := make([]models.AgentVibeSession, 0, len(in))
	for _, s := range in {
		if internal[s.ID] {
			continue
		}
		out = append(out, s)
	}
	return out
}

// partitionAgentVibeSessions 把「已按 UpdatedAt desc 排序」的全量候选分流为：
//   - projectsSource：前 agentVibeSummaryMaxSessions 条（**含** internal）——供
//     collectAgentProjects/agentProjectPaths 派生项目与授权目录（隐藏 planner 不撤授权）。
//   - vibe：先 excludeInternalVibeSessions 再取前 200（**不含** internal）——作 VibeSessions 输出
//     （internal 不占名额，先过滤后截断）。
func partitionAgentVibeSessions(full []models.AgentVibeSession, internal map[string]bool) (projectsSource, vibe []models.AgentVibeSession) {
	projectsSource = full
	if len(projectsSource) > agentVibeSummaryMaxSessions {
		projectsSource = projectsSource[:agentVibeSummaryMaxSessions]
	}
	vibe = excludeInternalVibeSessions(full, internal)
	if len(vibe) > agentVibeSummaryMaxSessions {
		vibe = vibe[:agentVibeSummaryMaxSessions]
	}
	return projectsSource, vibe
}
```

3b. 把现有 `collectAgentVibeSessions`（:329，目前 build+sort+truncate）拆成：

```go
// collectRawAgentVibeSessions 构建 codex+claude 全量候选，按 UpdatedAt desc 排序，不截断不过滤。
func collectRawAgentVibeSessions(scanDirs []string) []models.AgentVibeSession {
	sessions := append(collectCodexVibeSessions(scanDirs), collectClaudeVibeSessions(scanDirs)...)
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt > sessions[j].UpdatedAt
	})
	return sessions
}

// collectAgentVibeSessions 返回「对外可见」列表：过滤 internal 后截断 200。供只要列表、
// 不要 projects 的调用方（AgentService.VibeSessions）使用。
func collectAgentVibeSessions(scanDirs []string, internal map[string]bool) []models.AgentVibeSession {
	vibe := excludeInternalVibeSessions(collectRawAgentVibeSessions(scanDirs), internal)
	if len(vibe) > agentVibeSummaryMaxSessions {
		vibe = vibe[:agentVibeSummaryMaxSessions]
	}
	return vibe
}
```

3c. `collectAgentSyncSnapshot`（:73）加 internal 参数并用 partition 分流：

```go
func collectAgentSyncSnapshot(scanDirs []string, internal map[string]bool) agentSyncSnapshot {
	history := collectAgentHistoryRoots()
	projectsSource, vibe := partitionAgentVibeSessions(collectRawAgentVibeSessions(scanDirs), internal)
	projects := collectAgentProjects(projectsSource) // 未过滤派生 → 授权不丢
	authorizedDirectories := agentProjectPaths(projects)
	setAgentAuthorizedExecutionDirectoriesCache(authorizedDirectories)

	return agentSyncSnapshot{
		// ...（DeviceName/Platform/AgentVersion/Host/Capabilities/Tools/History 不变）
		Projects:              projects,
		VibeSessions:          summarizeAgentVibeSessions(vibe), // 过滤后截断
		AuthorizedDirectories: authorizedDirectories,
		CollectedAt:           time.Now().UTC().Format(time.RFC3339),
	}
}
```

> `overlayActiveAgentVibeSessions`（:95）不变：它在已过滤的 `snapshot.VibeSessions` 之上叠加 active runs，其内部 :129 的二次 cap 200 保持原样。

3d. `agent_service.go` 两处调用：

```go
// :230 VibeSessions —— 只要可见列表，用 collectAgentVibeSessions（内部已过滤+截断）。
func (s *AgentService) VibeSessions() []models.AgentVibeSession {
	snapshot := agentSyncSnapshot{
		VibeSessions: summarizeAgentVibeSessions(
			collectAgentVibeSessions(nil, s.ai.internalNativeSessionSnapshot()),
		),
	}
	// ...(既有后续不变)
}

// :1999 collectAgentSyncSnapshotWithActiveRuns —— 传 snapshot 给 collectAgentSyncSnapshot。
func (s *AgentService) collectAgentSyncSnapshotWithActiveRuns(scanDirs []string) agentSyncSnapshot {
	snapshot := collectAgentSyncSnapshot(scanDirs, s.ai.internalNativeSessionSnapshot())
	// ...(既有后续不变)
}
```

> 复核 `s.ai` 在这两处非 nil（`AgentService` 生命周期内 `ai` 已 `newAgentAIManager`）。若可能 nil，加守卫 `internalNativeSessionSnapshot()` 本身对 nil receiver 返回空 map（Task 2 已保证），故无需额外判断。

- [ ] **Step 4: 跑测试确认通过 + 全包编译**

Run: `go test ./app/http/services/ -run 'TestExcludeInternalVibeSessions|TestPartitionAgentVibeSnapshot' -count=1 -v -race && go build ./app/... && go vet ./app/http/services/`
Expected: PASS + 编译通过

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_inventory.go app/http/services/agent_service.go app/http/services/agent_inventory_test.go
git commit -m "feat(agent): 库存扫描器分流过滤（projects 未过滤、vibe 过滤后截断，覆盖两调用方）"
```

---

## Task 7: 全量验证（构建 / vet / 测试 / -race）

**Files:** 无（验证步骤）

- [ ] **Step 1: 全包构建 + vet**

Run: `go build ./app/... && go vet ./app/http/services/`
Expected: 通过（根包 `aliang.one/nursorgate` 的「no main」是预存问题，与本次无关，忽略）

- [ ] **Step 2: services 包全量测试 + race**

Run: `go test ./app/http/services/ -count=1 -timeout 420s -race`
Expected: 全绿；若出现与本次无关的已知 flaky（terminal manager 顺序 / ChatHandler / UserCenter 环境性），记录基线、确认非本次引入

- [ ] **Step 3: 针对本次新增测试单独 -race**

Run: `go test ./app/http/services/ -run 'Internal|Planner|InternalSession|ExcludeInternal|Canonical' -count=1 -race -v`
Expected: PASS

- [ ] **Step 4: 不提交（纯验证）；若中途有未提交修复则补提交**

---

## Task 8: 真机验证（部署后；覆盖已知竞态窗口）

> 此任务在 agent 重新编译部署后执行，不在本会话代码范围内。

- [ ] **Step 1**：`go build ./cmd/aliang`（或仓库主二进制构建命令）产出新 agent 二进制，部署到设备。
- [ ] **Step 2**：触发一次 Goal 规划（含至少一次 replan / 重试），覆盖 Codex 与 Claude 两种 provider。
- [ ] **Step 3**：**强制 agent 重连**，等待至少一次 1 分钟周期 inventory 上报（覆盖「CLI 先落盘、后回报 SID」的竞态窗口）。
- [ ] **Step 4**：确认手机 VibeCoding 列表**不新增**陌生 `ai_import_` 对话。
- [ ] **Step 5**：确认 Goal 主会话（`purpose: 'goal'`）仍正常显示；相关项目授权目录未受影响。
- [ ] **Step 6**：历史已泄漏的 `ai_import_` 由用户手动删除（本设计不自动清理）。

---

## 风险（实现时复查）

- **已知竞态**：CLI 可能先在磁盘创建 session、再经 stdout 回报 SID；几毫秒内若恰逢 inventory sync 仍可能泄漏。单仓止血可接受，真机验证靠强制重连 + 等一次周期上报覆盖（Task 8 Step 3）。
- **OpenCode provider**：`tool.id="opencode"` → canonical 返回 "" 不记。OpenCode 不产生本机 session_index，当前无泄漏面；若将来需要再扩 helper，勿默认放行。
- **provider 偶发不回报 native id**：无 id 可记，该 session 可能仍泄漏；可接受（罕见且失败常不留持久 session）。
- **agent 重装 / state 丢失**：集合清空，扫描窗口内残留旧 planner session 可能泄漏一次；YAGNI，不做种子恢复。
