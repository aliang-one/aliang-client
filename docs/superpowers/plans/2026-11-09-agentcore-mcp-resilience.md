# agentcore MCP 健壮性包实施计划(检活重连 + tool 级过滤)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** agentcore MCP 工具调用失败时自动探活重连并单次重试(连续失败判死),并新增 tool 级 include/exclude 过滤(注册表 API 可写、claude 载荷隔离、未知名 fail-loud)。

**Architecture:** 会话持有者 `agentCoreMCPSessionHolder`(同 server 全桥共享,锁内换会话)承接会话生命周期;过滤在 ListTools 之后、128 cap 与命名之前做。字段贯通注册表管理面与消费面,claude 载荷显式剥键。

**Tech Stack:** Go;github.com/modelcontextprotocol/go-sdk/mcp v1.8.0;现有测试注入钩子 `coreMCPDialForTest`。

**Spec:** `docs/superpowers/specs/2026-11-09-agentcore-mcp-resilience-design.md`(已批准,本计划全部细节以 spec 为准)

**提交纪律(全任务适用,CLAUDE.md 协议):** 提交标题全中文(「新增:」「修复:」「重构:」前缀),正文用 HEREDOC,尾部带:

```
🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>
```

下文各 commit 步骤只写标题与要点,footer 照此补齐,不重复。

**测试命令口径:** 单测用 `go test ./app/http/services -run '^<TestName>$' -count=1 -v`;预期失败/通过的输出以 FAIL/PASS 关键字 + ok 判定。注意:不要加 `-race`(仓库基线有预存 race flake,与本工作无关)。

**测试编写两条纪律(全任务适用):**
- dial 闭包(经 `agentCoreMCPDialWithTimeout` 在 goroutine 中执行)**禁用 `t.Fatalf`**——非锁保护 + Goexit 语义;要报错就 `return nil, nil, error`。闭包内调 `startTestMCPSession` 实践安全(helper 的 t.Helper/t.Cleanup 注册在 testing 包内有锁)。
- Task 3 过滤判失败分支的语句序是「先 `closeFn()` 后 `fail()`」,与既有 err 分支(fail 在前)相反——有意为之(先关会话防泄漏,再记账),评审逐行 diff 时勿"纠正"。

---

### Task 0: worktree 准备

**Files:** 无代码改动。

- [ ] **Step 1: 建 worktree 切分支**

```bash
git -C /Users/mac/MyProgram/GoProgram/nursor/alianggate worktree add ../alianggate-mcp-resilience -b feat/mcp-resilience master
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate-mcp-resilience
```

- [ ] **Step 2: 确认构建基线绿**

Run: `go build ./... && go test ./app/http/services -run '^TestAgentCoreMCP' -count=1`
Expected: 全部 PASS(既有 MCP 测试基线)。

---

### Task 1: 注册表写通道——条目字段 + 归一 + 上限校验

**Files:**
- Modify: `app/http/services/agent_mcp_registry.go:51-59`(entry 结构)、`:555-570`(request 结构,**同位同型**追加——结构体转换护栏 `aliangMCPServerEntry(r)` 要求逐一同名同型同序)、`:188-208`(validate)、`:213-237`(toMap)
- Test: `app/http/services/agent_mcp_registry_test.go`

- [ ] **Step 1: 写失败测试(往返归一 + 上限拒绝)**

在 `agent_mcp_registry_test.go` 末尾追加:

```go
func TestAliangMCPEntryToolListRoundTripNormalize(t *testing.T) {
	home := t.TempDir()
	// 5 项归一后应剩 2 项 [a b]:trim/去空串/dedup(保首现顺序)
	inc := []string{"a", " a ", "", "a", "b"}
	if err := upsertAliangMCPServerForTest(home, "srv", aliangMCPServerEntry{
		Command: "echo", IncludeTools: inc, ExcludeTools: []string{"x"},
	}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	root, existed, err := loadAliangMCPRegistryRoot(home)
	if err != nil || !existed {
		t.Fatalf("读回失败: %v existed=%v", err, existed)
	}
	raw := root["servers"].(map[string]interface{})["srv"].(map[string]interface{})
	incGot, _ := raw["include_tools"].([]interface{})
	if len(incGot) != 2 || incGot[0] != "a" || incGot[1] != "b" {
		t.Fatalf("include 归一落盘错误: %v", incGot)
	}
	excGot, _ := raw["exclude_tools"].([]interface{})
	if len(excGot) != 1 || excGot[0] != "x" {
		t.Fatalf("exclude 落盘错误: %v", excGot)
	}
}

func TestAliangMCPEntryToolListCapReject(t *testing.T) {
	home := t.TempDir()
	big := make([]string, agentCoreMCPToolListCap+1) // 65 条,超上限必须拒绝
	for i := range big {
		big[i] = fmt.Sprintf("t%d", i)
	}
	err := upsertAliangMCPServerForTest(home, "srv", aliangMCPServerEntry{Command: "echo", IncludeTools: big})
	if err == nil || !strings.Contains(err.Error(), "include_tools") {
		t.Fatalf("超上限应拒绝并点名字段: %v", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services -run '^TestAliangMCPEntryToolList' -count=1 -v`
Expected: FAIL(字段不存在,编译错误即算失败)。

- [ ] **Step 3: 实现**

3a. `agent_ai_agentcore_mcp.go` 常量区(`:27-44`)加:

```go
// agentCoreMCPToolListCap 单 server 工具名单上限(写通道 fail-closed)。
const agentCoreMCPToolListCap = 64

// normalizeAgentCoreMCPToolList 工具名单归一:trim/去空串/dedup(保首现顺序)。
// 写通道落盘前与读通道过滤前共用(读侧兜住手改文件,沿 type 归一先例)。
func normalizeAgentCoreMCPToolList(list []string) []string {
	seen := make(map[string]bool, len(list))
	out := make([]string, 0, len(list))
	for _, v := range list {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
```

(注:helper 放消费侧文件、常量被注册表文件引用——同包,合法。)

3b. `agent_mcp_registry.go`:`aliangMCPServerEntry`(:51-59)与 `AliangMCPServerRequest`(:555-563)**在完全相同的末尾位置**追加同型字段(护栏依赖同序,任一侧漏加/错位会在 `entry()` 的结构体转换处编译期炸):

```go
	IncludeTools []string `json:"include_tools,omitempty"` // 消费侧(agentcore)白名单;claude 载荷剥除
	ExcludeTools []string `json:"exclude_tools,omitempty"` // 消费侧(agentcore)黑名单;claude 载荷剥除
```

Request 侧 json tag 不带 `,omitempty`(沿该结构体现有 tag 风格)。

3c. `validateAliangMCPEntry`(:188-208)在 type 校验之前加:

```go
	if len(normalizeAgentCoreMCPToolList(e.IncludeTools)) > agentCoreMCPToolListCap {
		return fmt.Errorf("include_tools 归一后超上限 %d", agentCoreMCPToolListCap)
	}
	if len(normalizeAgentCoreMCPToolList(e.ExcludeTools)) > agentCoreMCPToolListCap {
		return fmt.Errorf("exclude_tools 归一后超上限 %d", agentCoreMCPToolListCap)
	}
```

3d. `aliangMCPEntryToMap`(:213-237)在 `Enabled` 分支前加(落盘前归一,防非规范字节冻进注册表):

```go
	if norm := normalizeAgentCoreMCPToolList(e.IncludeTools); len(norm) > 0 {
		out["include_tools"] = norm
	}
	if norm := normalizeAgentCoreMCPToolList(e.ExcludeTools); len(norm) > 0 {
		out["exclude_tools"] = norm
	}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services -run '^TestAliangMCPEntryToolList' -count=1 -v`
Expected: 2 个 PASS。再跑 `go test ./app/http/services -run '^TestAliangMCP' -count=1` 确认无回归。

- [ ] **Step 5: Commit**

```bash
git add app/http/services/agent_mcp_registry.go app/http/services/agent_ai_agentcore_mcp.go app/http/services/agent_mcp_registry_test.go
git commit -m "$(cat <<'EOF'
新增:MCP 注册表条目 include_tools/exclude_tools 字段——写通道归一落盘+64 条上限 fail-closed
EOF
)"
```

---

### Task 2: 消费侧解析——raw 条目携带过滤名单

**Files:**
- Modify: `app/http/services/agent_ai_agentcore_mcp.go:46-65`(RawEntry/RawServer 结构)
- Test: `app/http/services/agent_ai_agentcore_mcp_test.go`

- [ ] **Step 1: 写失败测试**

`agent_ai_agentcore_mcp_test.go` 末尾追加:

```go
func TestAgentCoreMCPRawServersParseToolLists(t *testing.T) {
	home := t.TempDir()
	_ = upsertAliangMCPServerForTest(home, "srv", aliangMCPServerEntry{
		Command: "echo", IncludeTools: []string{"a", "b"}, ExcludeTools: []string{"x"},
	})
	raws := agentCoreMCPRawServersIn(home)
	if len(raws) != 1 {
		t.Fatalf("应采到 1 条: %+v", raws)
	}
	if len(raws[0].IncludeTools) != 2 || raws[0].IncludeTools[0] != "a" {
		t.Fatalf("include 解析错误: %v", raws[0].IncludeTools)
	}
	if len(raws[0].ExcludeTools) != 1 || raws[0].ExcludeTools[0] != "x" {
		t.Fatalf("exclude 解析错误: %v", raws[0].ExcludeTools)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services -run '^TestAgentCoreMCPRawServersParseToolLists$' -count=1 -v`
Expected: FAIL(字段不存在)。

- [ ] **Step 3: 实现**

`agentCoreMCPRawServer`(:47-55)与 `agentCoreMCPRawEntry`(:57-65)末尾各加:

```go
	IncludeTools []string `json:"include_tools"`
	ExcludeTools []string `json:"exclude_tools"`
```

`agentCoreMCPRawServersIn` 的 out append(:139-142)带上两字段:

```go
		out = append(out, agentCoreMCPRawServer{
			Name: key, Command: entry.Command, Args: entry.Args, Env: entry.Env,
			Type: entry.Type, URL: entry.URL, Headers: entry.Headers,
			IncludeTools: entry.IncludeTools, ExcludeTools: entry.ExcludeTools,
		})
```

(注册表→raw 走 json.Marshal/Unmarshal 透传,新字段自动流转;`.claude.json`/插件源无此字段则零值,天然兼容。)

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services -run '^TestAgentCoreMCPRawServers' -count=1 -v`
Expected: PASS(含既有同名前缀测试)。

- [ ] **Step 5: Commit**

```bash
git add app/http/services/agent_ai_agentcore_mcp.go app/http/services/agent_ai_agentcore_mcp_test.go
git commit -m "$(cat <<'EOF'
新增:agentcore 消费侧 raw 条目解析 include/exclude 工具名单
EOF
)"
```

---

### Task 3: 过滤逻辑 + 装配循环接线

**Files:**
- Modify: `app/http/services/agent_ai_agentcore_mcp.go`(`agentCoreMCPServerToolDefs` :372-517:阶段二装配循环)
- Test: `app/http/services/agent_ai_agentcore_mcp_test.go`(filter 单元 + HTTP 全链两条)

- [ ] **Step 1: 写失败测试**

1a. filter 单元测试(覆盖 spec 测试 4/5/10:include 交集+未知名硬失败、exclude 剔除+未知名软警告、读通道归一;测试 6 的 cap 交互回归锁见 1c):

```go
func TestFilterAgentCoreMCPTools(t *testing.T) {
	tools := []*mcp.Tool{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	got, err := filterAgentCoreMCPTools(tools, []string{"a", "c"}, nil, "srv")
	if err != nil || len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Fatalf("include 白名单交集错误: %v %v", got, err)
	}
	_, err = filterAgentCoreMCPTools(tools, []string{"a", "ghost"}, nil, "srv")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("include 未知名应硬失败: %v", err)
	}
	// exclude 未知名仅警告不判失败;剔除生效
	got, err = filterAgentCoreMCPTools(tools, nil, []string{"b", "ghost"}, "srv")
	if err != nil || len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Fatalf("exclude 黑名单剔除错误: %v %v", got, err)
	}
	// 读通道归一(手改文件兜底):trim/去空串/dedup 后照常过滤
	got, err = filterAgentCoreMCPTools(tools, []string{" a ", "", "a", "c"}, nil, "srv")
	if err != nil || len(got) != 2 || got[0].Name != "a" {
		t.Fatalf("读通道归一过滤错误: %v %v", got, err)
	}
	// include 与 exclude 同列同一工具:exclude 胜
	got, err = filterAgentCoreMCPTools(tools, []string{"a", "b"}, []string{"b"}, "srv")
	if err != nil || len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("同列冲突应 exclude 胜: %v %v", got, err)
	}
}
```

1b. HTTP 全链三条(既有 `startHTTPTestMCPServer` 助手支持多工具名,打通 dial→list→filter;第三条锁「过滤先于 cap」实施顺序,spec 测试 6):

```go
func TestAgentCoreMCPServerToolDefsToolLevelExclude(t *testing.T) {
	endpoint := startHTTPTestMCPServer(t, []string{"t1", "t2"}, func(map[string]interface{}) (string, bool) {
		return "ok", false
	})
	m := &agentAIManager{}
	raws := []agentCoreMCPRawServer{{Name: "srv", Type: "http", URL: endpoint, ExcludeTools: []string{"t2"}}}
	tools, closeAll := m.agentCoreMCPServerToolDefs(context.Background(), raws, nil)
	defer closeAll()
	if len(tools) != 1 || tools[0].Name() != "mcp__srv__t1" {
		t.Fatalf("exclude 后应只剩 t1: %+v", tools)
	}
}

func TestAgentCoreMCPServerToolDefsToolLevelIncludeGhostFails(t *testing.T) {
	endpoint := startHTTPTestMCPServer(t, []string{"t1"}, func(map[string]interface{}) (string, bool) {
		return "ok", false
	})
	m := &agentAIManager{}
	raws := []agentCoreMCPRawServer{{Name: "srv", Type: "http", URL: endpoint, IncludeTools: []string{"ghost"}}}
	tools, closeAll := m.agentCoreMCPServerToolDefs(context.Background(), raws, nil)
	defer closeAll()
	if len(tools) != 0 {
		t.Fatalf("include 未知名应整 server 跳过: %+v", tools)
	}
}

// spec 测试 6:过滤必须在 128 cap 计数之前(回归锁)。130 个工具剔 10 个:
// 正确顺序 → 120 桥(cap 128 未触发);实现错序(先截 128 再剔 10)→ 118,红。
func TestAgentCoreMCPServerToolDefsFilterBeforeCap(t *testing.T) {
	names := make([]string, 130)
	for i := range names {
		names[i] = fmt.Sprintf("t%03d", i)
	}
	endpoint := startHTTPTestMCPServer(t, names, func(map[string]interface{}) (string, bool) {
		return "ok", false
	})
	m := &agentAIManager{}
	excl := []string{"t000", "t001", "t002", "t003", "t004", "t005", "t006", "t007", "t008", "t009"}
	raws := []agentCoreMCPRawServer{{Name: "srv", Type: "http", URL: endpoint, ExcludeTools: excl}}
	tools, closeAll := m.agentCoreMCPServerToolDefs(context.Background(), raws, nil)
	defer closeAll()
	if len(tools) != 120 {
		t.Fatalf("过滤后应剩 120(先过滤后 cap): %d", len(tools))
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services -run '^TestFilterAgentCoreMCPTools$|^TestAgentCoreMCPServerToolDefsToolLevel' -count=1 -v`
Expected: FAIL(filter 未定义)。

- [ ] **Step 3: 实现**

3a. filter 函数(`agent_ai_agentcore_mcp.go`,放 `listAgentCoreMCPTools` 之后):

```go
// filterAgentCoreMCPTools include 白名单交集 + exclude 黑名单剔除(spec §5.2)。
// 读通道归一先做(手改注册表文件兜底)。include 引用不存在的工具名 → 整 server
// 判失败(返回 err,调用方走 fail 语义)——把配置错误从「静默丢工具」变成
// 「显式报错」;exclude 未知名仅 Warn(剔除尚不存在的工具可能是有意预留)。
// 调用时序:ListTools 之后、128 cap 与命名之前——被过滤的工具不占预算。
func filterAgentCoreMCPTools(tools []*mcp.Tool, include, exclude []string, serverName string) ([]*mcp.Tool, error) {
	include = normalizeAgentCoreMCPToolList(include)
	exclude = normalizeAgentCoreMCPToolList(exclude)
	names := make(map[string]bool, len(tools))
	for _, t := range tools {
		names[t.Name] = true
	}
	if len(include) > 0 {
		missing := make([]string, 0, len(include))
		for _, n := range include {
			if !names[n] {
				missing = append(missing, n)
			}
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("include_tools 引用不存在的工具: %v", missing)
		}
	}
	excl := make(map[string]bool, len(exclude))
	for _, n := range exclude {
		excl[n] = true
		for _, t := range tools {
			if t.Name == n {
				goto known
			}
		}
		logger.Warn(fmt.Sprintf("agentcore-mcp: server=%s exclude_tools 未匹配任何工具: %q", sanitizeAgentCoreMCPSegment(serverName), n))
	known:
	}
	var out []*mcp.Tool
	for _, t := range tools {
		if len(include) > 0 && !slices.Contains(include, t.Name) {
			continue
		}
		if excl[t.Name] {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}
```

(需要 `"slices"` import;若不想用 goto,可写成小 helper `containsToolName(tools, n) bool`,二选一,以更贴近仓库现有风格为准。)

3b. 阶段二装配循环接线(`:473-486` 一带),在 `connected++` **之前**插过滤(spec §5.2 计数互斥:判失败只进 skipped):

```go
	for _, out := range outcomes {
		if out.err != nil {
			fail(out.raw.Name, out.err)
			if out.closeFn != nil {
				out.closeFn()
			}
			continue
		}
		tools, ferr := filterAgentCoreMCPTools(out.tools, out.raw.IncludeTools, out.raw.ExcludeTools, out.raw.Name)
		if ferr != nil {
			// 计数互斥(spec §5.2):过滤判失败的 server 只进 skipped,
			// 不进 connected——「已连接 N(跳过 K)」不得双计。
			if out.closeFn != nil {
				out.closeFn()
			}
			fail(out.raw.Name, ferr)
			continue
		}
		if len(tools) != len(out.tools) {
			logger.Info(fmt.Sprintf("agentcore-mcp: server=%s 工具过滤 保留 %d/%d(include=%d exclude=%d)",
				sanitizeAgentCoreMCPSegment(out.raw.Name), len(tools), len(out.tools),
				len(out.raw.IncludeTools), len(out.raw.ExcludeTools)))
		}
		if out.closeFn != nil {
			closes = append(closes, out.closeFn)
		}
		connected++
		// ↓ 以下 cap 与装配循环不变,只是把遍历对象从 out.tools 换成 tools
		for _, tool := range tools {
			...(原 :491-512 逻辑逐字保留)
		}
	}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services -run '^TestFilterAgentCoreMCPTools$|^TestAgentCoreMCPServerToolDefs' -count=1 -v`
Expected: 新 4 个 PASS(filter 单元 + exclude + include-ghost + filter-before-cap)+ 既有 tooldefs 测试无回归。

- [ ] **Step 5: Commit**

```bash
git add app/http/services/agent_ai_agentcore_mcp.go app/http/services/agent_ai_agentcore_mcp_test.go
git commit -m "$(cat <<'EOF'
新增:agentcore MCP tool 级 include/exclude 过滤——include 未知名硬失败整 server 跳过,过滤在 128 cap 之前,connected/skipped 计数互斥
EOF
)"
```

---

### Task 4: claude 载荷剥离 + 回归锁

**Files:**
- Modify: `app/http/services/agent_ai_claude_policy.go:219-224`(marshal 之前剥键)
- Test: `app/http/services/agent_ai_claude_policy_mcp_registry_test.go`

- [ ] **Step 1: 写失败测试**

在该测试文件末尾追加(复用 `swapClaudeTierHome`/`claudeTierPayloadServerNames` 既有助手):

```go
func TestClaudeTierMCPArgsStripsAgentcoreToolFilters(t *testing.T) {
	home := t.TempDir()
	swapClaudeTierHome(t, home)
	_ = upsertAliangMCPServerForTest(home, "srv", aliangMCPServerEntry{
		Command: "echo", IncludeTools: []string{"a"}, ExcludeTools: []string{"b"},
	})
	policy := agentAIClaudeRemotePolicy{projectMCPTrusted: true}
	flags, cleanup, err := claudeTierMCPArgs(policy, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	servers := claudeTierPayloadServerNames(t, flags, cleanup)
	entry := servers["srv"]
	if entry["command"] != "echo" {
		t.Fatalf("基础字段必须保留: %v", entry)
	}
	if _, has := entry["include_tools"]; has {
		t.Fatalf("include_tools 不得进 claude 载荷: %v", entry)
	}
	if _, has := entry["exclude_tools"]; has {
		t.Fatalf("exclude_tools 不得进 claude 载荷: %v", entry)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services -run '^TestClaudeTierMCPArgsStripsAgentcoreToolFilters$' -count=1 -v`
Expected: FAIL(键泄漏进载荷)。

- [ ] **Step 3: 实现**

`claudeTierMCPArgs` 在 `payload, err := json.Marshal(...)`(:224)之前加:

```go
	// claude 载荷剥离(spec §5.1 条目6):include/exclude 是 agentcore 消费侧
	// 专属语义,claude CLI 载荷不携带。剥在合并产物上;硬约束:不得剔在
	// readAliangMCPRegistryInto 本体——agentcore 消费侧走同一合并函数,
	// 剔了整个特性失效。
	for _, v := range merged {
		entry, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		delete(entry, "include_tools")
		delete(entry, "exclude_tools")
	}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services -run '^TestClaudeTierMCPArgs' -count=1 -v`
Expected: 新测试 PASS + 既有 claudeTierMCPArgs 族全 PASS。

- [ ] **Step 5: Commit**

```bash
git add app/http/services/agent_ai_claude_policy.go app/http/services/agent_ai_claude_policy_mcp_registry_test.go
git commit -m "$(cat <<'EOF'
修复:claude MCP 载荷剥除 include/exclude 工具过滤键——agentcore 消费侧专属语义不外泄(regression 锁死)
EOF
)"
```

---

### Task 5: holder 重构——桥改会话持有者(纯重构,行为不变)

**Files:**
- Modify: `app/http/services/agent_ai_agentcore_mcp.go`(holder 结构 + 桥字段 + 装配 + closeAll)
- Test: `app/http/services/agent_ai_agentcore_mcp_test.go`(4 处桥构造适配)

- [ ] **Step 1: 实现结构与改造**(此步是纯结构重构,新行为在 Task 6;先改后跑绿)

5a. holder 结构(放 `agentCoreMCPBridge` 定义之前):

```go
// agentCoreMCPSessionHolder 同 server 全部桥共享的会话持有者(spec §4.1)。
// 锁内换会话,closeAll 永远关到当前会话——重连换新后零泄漏。内核每 run 串行
// 执行工具,锁是纵深防御不承担正确性主责。dial 与测试注入同源(coreMCPDialForTest)。
type agentCoreMCPSessionHolder struct {
	mu         sync.Mutex
	session    *mcp.ClientSession
	closeFn    func() // 关当前会话(内含子 ctx cancel)
	raw        agentCoreMCPRawServer
	dial       func(context.Context, agentCoreMCPRawServer) (*mcp.ClientSession, func(), error)
	consecFail int  // 连续「重连后仍失败」计数(spec §4.3)
	dead       bool // run 内判死:后续调用快速失败不再拨号
}

func newAgentCoreMCPSessionHolder(session *mcp.ClientSession, closeFn func(), raw agentCoreMCPRawServer, dial func(context.Context, agentCoreMCPRawServer) (*mcp.ClientSession, func(), error)) *agentCoreMCPSessionHolder {
	return &agentCoreMCPSessionHolder{session: session, closeFn: closeFn, raw: raw, dial: dial}
}

// Close 锁内关当前会话。closeAll 语义锚点:closeFn 一律经此处走,换会话后
// 关到的是新会话。
func (h *agentCoreMCPSessionHolder) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closeFn != nil {
		h.closeFn()
	}
	h.session = nil
	h.closeFn = nil
	h.dead = true // 防关闭后再触发重连(泄漏面)
}

// call 暂为直通(Task 6 换成重连版)。判死检查先行——Close 后 dead=true,
// nil session 不会被解引用。
func (h *agentCoreMCPSessionHolder) call(ctx context.Context, tool string, args map[string]interface{}) (*mcp.CallToolResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dead {
		return nil, fmt.Errorf("MCP server 已在本次运行内判死(连续 %d 次重连失败),本 run 内请勿继续调用该 server 工具", agentCoreMCPReconnectDeadCap)
	}
	return h.session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
}
```

(常量 `agentCoreMCPReconnectDeadCap = 3` 一并加到常量区,spec §6:文案取值拼接不硬编码。)

5b. 桥字段:`session *mcp.ClientSession` → `holder *agentCoreMCPSessionHolder`;`Execute` 里 `b.session.CallTool(ctx, ...)` → `b.holder.call(ctx, b.rawTool, input)`。

5c. 装配:dialOutcome 加 `holder` 字段;goroutine 成功路径构建 `holder: newAgentCoreMCPSessionHolder(session, func() { closeFn(); cancel() }, raw, dial)`(dial=已 resolve 的注入值,在 goroutine 闭包作用域内);阶段二成功路径 `closes = append(closes, ...)` 改为 `holders = append(holders, out.holder)`;closeAll 遍历 holders 调 `Close()`(串行关停代价注释原样保留)。

5d. 既有测试适配(4 处):`:130`、`:153` 的 `session: session` → `holder: newAgentCoreMCPSessionHolder(session, func() { _ = session.Close() }, agentCoreMCPRawServer{}, nil)`;`:429`(TestAgentCoreMCPBridgeTransportError:先 `session.Close()` 再 Execute,CallTool 必回 transport 错误)→ holder 的 dial **必须给立即报错的存根**而非 nil——Task 6 的重连路径会在 goroutine 里调 `h.dial`,nil 函数值是 runtime panic 直接崩测试二进制;存根报错后文案含「已自动重连」,仍满足该测试 `Contains("MCP 调用失败")` 断言:

```go
holder: newAgentCoreMCPSessionHolder(session, func() { _ = session.Close() }, agentCoreMCPRawServer{},
	func(context.Context, agentCoreMCPRawServer) (*mcp.ClientSession, func(), error) {
		return nil, nil, fmt.Errorf("dial 拒绝")
	})
```

`:269`(nil-session panic 路径)→ `holder: newAgentCoreMCPSessionHolder(nil, nil, agentCoreMCPRawServer{}, nil)`——**call 不做 nil 特判**,nil 解引用 panic 仍走外层 recover,该测试预期一字不改(dead 先行检查为 false,nil 走到 CallTool 才 panic;此测试在 Task 6 后 holder 不会走到重连,无 nil-dial 风险)。

- [ ] **Step 2: 全包测试确认绿**

Run: `go build ./... && go test ./app/http/services -run '^TestAgentCoreMCP' -count=1 -v`
Expected: 全部 PASS(行为不变,包括 nil-session panic 测试原样)。

- [ ] **Step 3: Commit**

```bash
git add app/http/services/agent_ai_agentcore_mcp.go app/http/services/agent_ai_agentcore_mcp_test.go
git commit -m "$(cat <<'EOF'
重构:agentcore MCP 桥改会话持有者——同 server 全桥共享 holder,closeAll 经 holder 锁内关当前会话(纯重构,行为不变)
EOF
)"
```

---

### Task 6: 检活重连——call 重连流程 + 判死

**Files:**
- Modify: `app/http/services/agent_ai_agentcore_mcp.go`(替换 Task 5 的占位 `call`)
- Test: `app/http/services/agent_ai_agentcore_mcp_test.go`

- [ ] **Step 1: 写失败测试**(spec 测试 1/2/3/7)

```go
// agentCoreMCPCallError 携带面向模型的最终文案,Execute 原样采用不再二次包装
// (判死文案与「已自动重连」文案都由 holder 定型)。
func TestAgentCoreMCPHolderReconnectSuccess(t *testing.T) {
	good := startTestMCPSession(t, "t", func(map[string]interface{}) (string, bool) { return "ok", false })
	dialCalls := 0
	dial := func(context.Context, agentCoreMCPRawServer) (*mcp.ClientSession, func(), error) {
		dialCalls++
		return good, func() { _ = good.Close() }, nil
	}
	init := startTestMCPSession(t, "t", func(map[string]interface{}) (string, bool) { return "ok", false })
	_ = init.Close() // 初始会话立死,逼出重连
	h := newAgentCoreMCPSessionHolder(init, func() { _ = init.Close() }, agentCoreMCPRawServer{Name: "srv"}, dial)
	b := &agentCoreMCPBridge{holder: h, rawTool: "t", name: "mcp__srv__t"}
	res := b.Execute(context.Background(), map[string]interface{}{}, nil)
	if res.IsError || !strings.Contains(res.Content, "ok") {
		t.Fatalf("重连后应成功: %+v", res)
	}
	if dialCalls != 1 {
		t.Fatalf("应恰好重拨一次: %d", dialCalls)
	}
}

func TestAgentCoreMCPHolderIsErrorNoReconnect(t *testing.T) {
	dialCalls := 0
	dial := func(context.Context, agentCoreMCPRawServer) (*mcp.ClientSession, func(), error) {
		dialCalls++
		return nil, nil, fmt.Errorf("不应拨号")
	}
	session := startTestMCPSession(t, "t", func(map[string]interface{}) (string, bool) { return "工具报错", true })
	h := newAgentCoreMCPSessionHolder(session, func() { _ = session.Close() }, agentCoreMCPRawServer{Name: "srv"}, dial)
	b := &agentCoreMCPBridge{holder: h, rawTool: "t", name: "mcp__srv__t"}
	res := b.Execute(context.Background(), map[string]interface{}{}, nil)
	if !res.IsError || !strings.Contains(res.Content, "工具报错") {
		t.Fatalf("IsError 应原样透传: %+v", res)
	}
	if dialCalls != 0 {
		t.Fatalf("工具逻辑报错不得触发重连: %d", dialCalls)
	}
}

func TestAgentCoreMCPHolderDeadAfterCap(t *testing.T) {
	dialCalls := 0
	dial := func(context.Context, agentCoreMCPRawServer) (*mcp.ClientSession, func(), error) {
		dialCalls++
		s := startTestMCPSession(t, "t", func(map[string]interface{}) (string, bool) { return "ok", false })
		_ = s.Close() // 拨出来的也立死
		return s, func() { _ = s.Close() }, nil
	}
	init := startTestMCPSession(t, "t", func(map[string]interface{}) (string, bool) { return "ok", false })
	_ = init.Close()
	h := newAgentCoreMCPSessionHolder(init, func() { _ = init.Close() }, agentCoreMCPRawServer{Name: "srv"}, dial)
	b := &agentCoreMCPBridge{holder: h, rawTool: "t", name: "mcp__srv__t"}
	for i := 0; i < agentCoreMCPReconnectDeadCap; i++ {
		res := b.Execute(context.Background(), map[string]interface{}{}, nil)
		if !res.IsError || !strings.Contains(res.Content, "已自动重连") {
			t.Fatalf("第 %d 次应失败且注明已重连: %+v", i+1, res)
		}
	}
	res := b.Execute(context.Background(), map[string]interface{}{}, nil)
	if !res.IsError || !strings.Contains(res.Content, "判死") || !strings.Contains(res.Content, "3") {
		t.Fatalf("判死文案错误: %+v", res)
	}
	if dialCalls != agentCoreMCPReconnectDeadCap {
		t.Fatalf("判死后不得再拨号: dialCalls=%d", dialCalls)
	}
}

func TestAgentCoreMCPHolderCloseClosesCurrent(t *testing.T) {
	var closedLog []string
	good := startTestMCPSession(t, "t", func(map[string]interface{}) (string, bool) { return "ok", false })
	dial := func(context.Context, agentCoreMCPRawServer) (*mcp.ClientSession, func(), error) {
		return good, func() { _ = good.Close(); closedLog = append(closedLog, "s2") }, nil
	}
	init := startTestMCPSession(t, "t", func(map[string]interface{}) (string, bool) { return "ok", false })
	_ = init.Close()
	h := newAgentCoreMCPSessionHolder(init, func() { closedLog = append(closedLog, "s1") }, agentCoreMCPRawServer{Name: "srv"}, dial)
	b := &agentCoreMCPBridge{holder: h, rawTool: "t", name: "mcp__srv__t"}
	if res := b.Execute(context.Background(), map[string]interface{}{}, nil); res.IsError {
		t.Fatalf("重连应成功: %+v", res)
	}
	h.Close() // 必须关到换后的新会话
	// 重连路径先关旧会话(记录 s1),Close 再关换上的新会话(记录 s2):
	// 两条都必须证——「旧会话在换时已关」(防泄漏)+「新会话被 Close 关到」。
	if len(closedLog) != 2 || closedLog[0] != "s1" || closedLog[1] != "s2" {
		t.Fatalf("关闭序应为 [s1(换时关旧), s2(Close 关新)]: %v", closedLog)
	}
	// Close 后再调用:判死快速失败,不拨号不解引用 nil
	res := b.Execute(context.Background(), map[string]interface{}{}, nil)
	if !res.IsError || !strings.Contains(res.Content, "判死") {
		t.Fatalf("Close 后应判死快速失败: %+v", res)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services -run '^TestAgentCoreMCPHolder' -count=1 -v`
Expected: FAIL(直通 call 无重连,ReconnectSuccess/DeadAfterCap/CloseClosesCurrent 挂,IsErrorNoReconnect 可能过)。

- [ ] **Step 3: 实现——替换 `call` 为重连版(spec §4.3)**

```go
// call 锁内 CallTool;transport 层失败(err != nil)触发重连:关旧会话 →
// 重拨(子 ctx 带 cancel,deadline 由 DialWithTimeout 外部施加)→ 换新 →
// 单次重试。IsError=true 的结果是工具逻辑报错(协议链路健康),不重连。
// 判死:连续 agentCoreMCPReconnectDeadCap 次「重连后仍失败」→ dead,后续
// 调用快速失败。重拨失败路径 holder.session 保持为已关闭旧会话、不置 nil
// ——判死路径与拨号计数断言依赖此语义(spec §4.3c)。
func (h *agentCoreMCPSessionHolder) call(ctx context.Context, tool string, args map[string]interface{}) (*mcp.CallToolResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dead {
		return nil, &agentCoreMCPCallError{text: fmt.Sprintf("MCP server 已在本次运行内判死(连续 %d 次重连失败),本 run 内请勿继续调用该 server 工具", agentCoreMCPReconnectDeadCap)}
	}
	res, err := h.session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err == nil {
		h.consecFail = 0
		return res, nil
	}
	if h.closeFn != nil {
		h.closeFn()
	}
	dctx, dcancel := context.WithCancel(ctx)
	session, closeFn, derr := agentCoreMCPDialWithTimeout(h.dial, dctx, h.raw, agentCoreMCPInitTimeout)
	if derr != nil {
		dcancel()
		h.noteReconnectFailure(derr)
		return nil, &agentCoreMCPCallError{text: "MCP 调用失败(已自动重连): " + err.Error()}
	}
	h.session = session
	h.closeFn = func() { closeFn(); dcancel() }
	logger.Info(fmt.Sprintf("agentcore-mcp: server=%s 重连成功", sanitizeAgentCoreMCPSegment(h.raw.Name)))
	res2, err2 := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err2 != nil {
		h.noteReconnectFailure(err2)
		return nil, &agentCoreMCPCallError{text: "MCP 调用失败(已自动重连): " + err2.Error()}
	}
	h.consecFail = 0
	return res2, nil
}

// noteReconnectFailure 记账:连续失败达判死阈值则标记,本 run 不再拨号。
func (h *agentCoreMCPSessionHolder) noteReconnectFailure(err error) {
	h.consecFail++
	logger.Warn(fmt.Sprintf("agentcore-mcp: server=%s 重连失败(第 %d 次): %v", sanitizeAgentCoreMCPSegment(h.raw.Name), h.consecFail, err))
	if h.consecFail >= agentCoreMCPReconnectDeadCap {
		h.dead = true
		logger.Warn(fmt.Sprintf("agentcore-mcp: server=%s 已判死,本 run 内不再重连", sanitizeAgentCoreMCPSegment(h.raw.Name)))
	}
}
```

桥的 `Execute` 错误分支改为:

```go
	if err != nil {
		isErr, text = true, err.Error() // agentCoreMCPCallError 携带最终文案
	} else {
		text, isErr = agentCoreMCPResultText(res)
	}
```

(需在文件内定义 `agentCoreMCPCallError`(Error() 返回 text);它是唯一进 Execute 错误分支的 error 类型——nil-session panic 路径走 recover,不经此处。)

- [ ] **Step 4: 跑测试确认通过**

Run: `go build ./... && go test ./app/http/services -run '^TestAgentCoreMCP' -count=1 -v`
Expected: 全部 PASS(Task 5 适配过的既有测试 + 4 个新 holder 测试)。

- [ ] **Step 5: Commit**

```bash
git add app/http/services/agent_ai_agentcore_mcp.go app/http/services/agent_ai_agentcore_mcp_test.go
git commit -m "$(cat <<'EOF'
新增:agentcore MCP 检活重连——transport 层失败自动重拨换会话并单次重试,连续 3 次失败 run 内判死防拨号风暴
EOF
)"
```

---

### Task 7: 全量验证

**Files:** 无新改动(只验证)。

- [ ] **Step 1: 全包测试**

Run: `go build ./... && go vet ./app/http/services && go test ./app/http/services -count=1`
Expected: build/vet 零输出,测试全 PASS。若遇与本次改动无关的预存失败(仓库已知 auth/agent_ai 子进程超时族),单包复跑确认非回归后记录,不顺手修。

- [ ] **Step 2: 关键回归三连**

Run: `go test ./app/http/services -run '^TestAliangMCPEntryToolList|^TestClaudeTierMCPArgs|^TestAgentCoreMCPHolder' -count=1 -v`
Expected: 全 PASS。

- [ ] **Step 3: (可选)真机冒烟口径**

部署前冒烟:配一个 echo 型 stdio MCP server → run 中 kill 该子进程 → 下一次工具调用应看到「重连失败(第 N 次)」日志直至判死;配 include_tools 名单写错 → run 日志应出现「include_tools 引用不存在的工具」且该 server 工具缺席。

---

## 计划外备忘

- `agent_integrations.go:114` 的清单采集走 `agentCoreMCPRawServersIn`,会看到新字段——纯诊断通道,无害(spec 评审备忘,不处理)。
- 仪表盘 UI(字段编辑面)是后续独立批次,不在本计划。
- Task 3 filter 的 goto 写法若与仓库风格不合,用 containsToolName helper 替代(计划已注明二选一)。
