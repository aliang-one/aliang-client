# Goal allowed_scope 执行边界强制 — 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 goal task 的 allowed_scope（allowedRoots + allowedCommands）从提示词里的"画地围栏"变成 PreToolUse hook 的运行时硬校验——越界工具调用在 claude 动手前被挡掉。

**Architecture:** 服务端已在 `goal_context` 信封里下发 allowedRoots/allowedCommands。设备 agent 解析后，经 `run → emitter → session` 三跳存到 `agentAISession`（approval hook 从 session 重建 run）。hook 的 goal-run 分支在现有 dangerous-command 检查之前，调纯函数 `goalScopeAllowsToolCall(toolName, raw, projectPath, roots, commands)` 做路径/命令校验，越界返回 deny + 原因（claude 收到自纠）。本次只动 claude 路径、只对 goal run 生效。

**Tech Stack:** Go 1.25（alianggate agent），复用 `pathUnderAnyScanDir`、`splitShellCommandSegments`、`claudeApprovalCommand`、`github.com/google/shlex`、`filepath.Abs/Clean/EvalSymlinks`。

**Spec:** `docs/superpowers/specs/2026-08-08-goal-allowed-scope-enforcement-design.md`

**仓库注意:** `master` 上有预存脏文件（`outbound/proxy/aliang/*`、`core_config_service.go`）和一条 worktree（`fix/goal-planner-session-leak`）。**所有 commit 只 `git add` 本计划列出的 goal-scope 文件**，勿用 `git add -A`。commit message 用中文。

---

## File Structure

| 文件 | 责任 | 动作 |
|---|---|---|
| `app/http/services/agent_goal.go` | `goalAllowedRootsFromContext` / `goalAllowedCommandsFromContext` 解析 helper；`goalScopeAllowsToolCall` 纯函数 + `resolveScopedPath`/`commandAllowed` 辅助 | 新增函数 |
| `app/http/services/agent_ai.go` | `agentAIRun` + `agentAIRunEmitter` + `agentAISession` 三结构体加 `goalAllowedRoots`/`goalAllowedCommands`；`newAgentAIRunEmitter` 镜像；`messageRun` 注入；session 两处 set 点（`:1870`/`:1905`）；`handleClaudeApprovalHook` goal-run 分支调校验 | 改结构体 + hook |
| `app/http/services/agent_goal_scope_test.go` | `goalScopeAllowsToolCall` 矩阵测 + 两个 parser helper 测 | 新建 |
| `app/http/services/agent_ai_approval_*_test.go` 或 `agent_goal_test.go` | hook 集成测（goal run 越界 deny / 围栏内放行 / vibecoding 不触发） | 加测 |

---

## Task 1: goal_context 解析 helpers

**Files:**
- Modify: `app/http/services/agent_goal.go`（在 `goalRequiredCheckCountFromContext` 旁）
- Test: `app/http/services/agent_goal_scope_test.go`（新建）

- [ ] **Step 1: 写失败测试**

新建 `agent_goal_scope_test.go`：
```go
package services

import "testing"

func TestGoalAllowedScopeFromContext(t *testing.T) {
	envelope := map[string]interface{}{
		"task": map[string]interface{}{
			"allowedRoots":    []interface{}{"/ws/src/foo", " /ws/x "},
			"allowedCommands": []interface{}{"git", "npm", "  "},
		},
	}
	roots := goalAllowedRootsFromContext(envelope)
	wantRoots := []string{"/ws/src/foo", "/ws/x"}
	if len(roots) != len(wantRoots) {
		t.Fatalf("roots: got %v want %v", roots, wantRoots)
	}
	cmds := goalAllowedCommandsFromContext(envelope)
	wantCmds := []string{"git", "npm"}
	if len(cmds) != len(wantCmds) {
		t.Fatalf("cmds: got %v want %v", cmds, wantCmds)
	}
	// 畸形/缺失 → nil（不强制）
	if r := goalAllowedRootsFromContext(nil); r != nil {
		t.Errorf("nil envelope should give nil roots, got %v", r)
	}
	if c := goalAllowedCommandsFromContext("not a map"); c != nil {
		t.Errorf("wrong type should give nil cmds, got %v", c)
	}
}
```

- [ ] **Step 2: 跑测试看失败**

Run: `go test ./app/http/services/ -run TestGoalAllowedScopeFromContext -v`
Expected: FAIL — `goalAllowedRootsFromContext: undefined`。

- [ ] **Step 3: 实现 helpers**

在 `agent_goal.go` 的 `goalRequiredCheckCountFromContext` 后加：
```go
// goalAllowedRootsFromContext reads task.allowedRoots out of the goal_context
// envelope. Absent/malformed → nil (no root enforcement).
func goalAllowedRootsFromContext(raw interface{}) []string {
	return goalStringSliceFromContext(raw, "allowedRoots")
}

// goalAllowedCommandsFromContext reads task.allowedCommands (prefix allowlist).
func goalAllowedCommandsFromContext(raw interface{}) []string {
	return goalStringSliceFromContext(raw, "allowedCommands")
}

func goalStringSliceFromContext(raw interface{}, field string) []string {
	gc, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}
	task, ok := gc["task"].(map[string]interface{})
	if !ok {
		return nil
	}
	items, ok := task[field].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}
```

- [ ] **Step 4: 跑测试看通过**

Run: `go test ./app/http/services/ -run TestGoalAllowedScopeFromContext -v`
Expected: PASS。

- [ ] **Step 5: commit**

```bash
git add app/http/services/agent_goal.go app/http/services/agent_goal_scope_test.go
git commit -m "feat(goal): 解析 goal_context 的 allowedRoots/allowedCommands helper"
```

---

## Task 2: `goalScopeAllowsToolCall` 纯函数（写路径 + Bash）

**Files:**
- Modify: `app/http/services/agent_goal.go`
- Test: `app/http/services/agent_goal_scope_test.go`

- [ ] **Step 1: 写失败测试（覆盖矩阵）**

追加到 `agent_goal_scope_test.go`：
```go
func TestGoalScopeAllowsToolCallWritePaths(t *testing.T) {
	roots := []string{"/ws/src/foo"}
	raw := func(filePath string) map[string]interface{} {
		return map[string]interface{}{"tool_input": map[string]interface{}{"file_path": filePath}}
	}
	cases := []struct {
		name   string
		tool   string
		path   string
		allow  bool
	}{
		{"in root abs", "Edit", "/ws/src/foo/a.ts", true},
		{"outside root", "Edit", "/ws/src/bar/b.ts", false},
		{"parent escape", "Write", "/ws/src/foo/../../etc/passwd", false},
		{"relative resolved under projectPath", "Edit", "src/foo/c.ts", true},
		{"relative resolved outside", "Edit", "src/bar/d.ts", false},
		{"new file (EvalSymlinks fails) under root", "Write", "/ws/src/foo/new.ts", true},
		{"empty roots → no enforcement", "Edit", "/anywhere", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// empty-roots case: pass nil roots
			rs := roots
			if tc.name == "empty roots → no enforcement" {
				rs = nil
			}
			ok, reason := goalScopeAllowsToolCall(tc.tool, raw(tc.path), "/ws", rs, nil)
			if ok != tc.allow {
				t.Errorf("got allow=%v want %v (reason=%q)", ok, tc.allow, reason)
			}
		})
	}
}

func TestGoalScopeAllowsToolCallBash(t *testing.T) {
	cmds := []string{"git", "npm", "npx"}
	raw := func(cmd string) map[string]interface{} {
		return map[string]interface{}{"tool_input": map[string]interface{}{"command": cmd}}
	}
	cases := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"single allowed", "npm test", true},
		{"single not allowed", "rm -rf /", false},
		{"compound all allowed", "npm test && npm run lint", true},
		{"compound with bad segment", "npm test; rm -rf x", false},
		{"pipe both allowed", "npm test | npm run lint", true},
		{"empty command", "", true},
		{"empty allowedCommands → no enforcement", "anything goes", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := cmds
			if tc.name == "empty allowedCommands → no enforcement" {
				cs = nil
			}
			ok, reason := goalScopeAllowsToolCall("Bash", raw(tc.cmd), "/ws", nil, cs)
			if ok != tc.allow {
				t.Errorf("got allow=%v want %v (reason=%q)", ok, tc.allow, reason)
			}
		})
	}
}

func TestGoalScopeAllowsToolCallReadonlyPassthrough(t *testing.T) {
	// 只读工具 + 非空 scope → 放行
	ok, _ := goalScopeAllowsToolCall("Read", map[string]interface{}{}, "/ws", []string{"/ws/src"}, []string{"git"})
	if !ok {
		t.Error("Read tool should pass even with scope set")
	}
	// 空总 scope → 一律放行（零检查审计 task）
	ok, _ = goalScopeAllowsToolCall("Edit", map[string]interface{}{"tool_input": map[string]interface{}{"file_path": "/anywhere"}}, "/ws", nil, nil)
	if !ok {
		t.Error("empty total scope must not enforce")
	}
}
```

- [ ] **Step 2: 跑测试看失败**

Run: `go test ./app/http/services/ -run 'TestGoalScopeAllowsToolCall' -v`
Expected: FAIL — `goalScopeAllowsToolCall: undefined`。

- [ ] **Step 3: 实现纯函数 + 辅助**

在 `agent_goal.go` 加：
```go
// goalScopeAllowsToolCall 判断一次 claude 工具调用是否落在 goal task 的
// allowed_scope 内。raw = handleClaudeApprovalHook 顶层 payload（含 tool_input）。
// projectPath = claude cwd，用于解析相对路径。roots/commands 均空 = 不强制。
//
// agent 级强制（非 OS 级）：防模型漂移出已批准 scope；不防对抗性越狱
// （git -c alias / env 注入 / 解释器 -c 逃逸）。详见
// docs/superpowers/specs/2026-08-08-goal-allowed-scope-enforcement-design.md §6。
func goalScopeAllowsToolCall(
	toolName string,
	raw map[string]interface{},
	projectPath string,
	allowedRoots []string,
	allowedCommands []string,
) (bool, string) {
	if len(allowedRoots) == 0 && len(allowedCommands) == 0 {
		return true, "" // 零检查审计 task：无 scope 可强制
	}
	lower := strings.ToLower(toolName)
	switch {
	case isWriteTool(lower):
		if len(allowedRoots) == 0 {
			return true, "" // 写工具但未配 roots：不挡（交给其它闸）
		}
		fp := scopeFilePath(raw)
		if strings.TrimSpace(fp) == "" {
			return true, "" // 取不到目标：不挡
		}
		resolved := resolveScopedPath(fp, projectPath)
		if !pathUnderAnyScanDir(resolved, allowedRoots) {
			return false, "path outside allowed roots: " + fp
		}
		return true, ""
	case lower == "bash":
		if len(allowedCommands) == 0 {
			return true, ""
		}
		command := claudeApprovalCommand(raw)
		if strings.TrimSpace(command) == "" {
			return true, ""
		}
		segments, ok := splitShellCommandSegments(command)
		if !ok {
			segments = []string{command}
		}
		for _, seg := range segments {
			toks, err := shlex.Split(seg)
			if err != nil || len(toks) == 0 {
				return false, "command not in allowed set (unparseable): " + seg
			}
			if !commandAllowed(toks[0], allowedCommands) {
				return false, "command not in allowed set: " + seg
			}
		}
		return true, ""
	default:
		return true, "" // 只读/其它工具：放行
	}
}

func isWriteTool(lowerTool string) bool {
	return strings.Contains(lowerTool, "edit") ||
		strings.Contains(lowerTool, "write") ||
		strings.Contains(lowerTool, "patch") ||
		strings.Contains(lowerTool, "notebook")
}

// scopeFilePath 从 raw 顶层或 tool_input 抽 file_path/path。
func scopeFilePath(raw map[string]interface{}) string {
	if fp := firstNonEmpty(remoteString(raw, "file_path"), remoteString(raw, "path")); fp != "" {
		return fp
	}
	for _, key := range []string{"tool_input", "toolInput"} {
		if ti, ok := raw[key].(map[string]interface{}); ok {
			if fp := firstNonEmpty(remoteString(ti, "file_path"), remoteString(ti, "path")); fp != "" {
				return fp
			}
		}
	}
	return ""
}

// resolveScopedPath 规范化目标路径：相对按 projectPath 解析 → Clean → EvalSymlinks
// （仅 err==nil 才用；新建文件 Write 目标尚不存在，EvalSymlinks 必失败，回退 Clean-abs）。
// 范式照搬 agent_approval_policy.go 的 Abs/EvalSymlinks 防御写法。
func resolveScopedPath(path, projectPath string) string {
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(projectPath, abs)
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// commandAllowed 首 token 前缀命中 allowedCommands（小写比对）。
func commandAllowed(firstToken string, allowed []string) bool {
	first := strings.ToLower(strings.TrimSpace(firstToken))
	for _, a := range allowed {
		if strings.HasPrefix(first, strings.ToLower(strings.TrimSpace(a))) {
			return true
		}
	}
	return false
}
```

确认 `agent_goal.go` 顶部 import 含 `path/filepath`（应已有；无则加）。

- [ ] **Step 4: 跑测试看通过**

Run: `go test ./app/http/services/ -run 'TestGoalScopeAllowsToolCall' -v`
Expected: 全 PASS。

- [ ] **Step 5: commit**

```bash
git add app/http/services/agent_goal.go app/http/services/agent_goal_scope_test.go
git commit -m "feat(goal): goalScopeAllowsToolCall 纯函数 — 路径/命令围栏校验"
```

---

## Task 3: 数据 plumbing（run → emitter → session）

**Files:**
- Modify: `app/http/services/agent_ai.go`（三个结构体 + `newAgentAIRunEmitter` + `messageRun` + session 两处 set 点）

- [ ] **Step 1: 写失败测试（emitter/session 镜像字段）**

追加到 `agent_goal_scope_test.go`：
```go
func TestGoalScopePlumbingRunToSession(t *testing.T) {
	// messageRun 携带 → emitter 镜像 → session 拷贝（goalIdentity 同款三跳）
	emitter := newAgentAIRunEmitter(agentAIRun{
		runID:                  "r1",
		goalIdentity:           testGoalIdentity(),
		goalAllowedRoots:       []string{"/ws/src/foo"},
		goalAllowedCommands:    []string{"npm"},
	}, func(interface{}) error { return nil })
	if len(emitter.goalAllowedRoots) != 1 || emitter.goalAllowedRoots[0] != "/ws/src/foo" {
		t.Errorf("emitter roots not mirrored: %v", emitter.goalAllowedRoots)
	}
	if len(emitter.goalAllowedCommands) != 1 || emitter.goalAllowedCommands[0] != "npm" {
		t.Errorf("emitter commands not mirrored: %v", emitter.goalAllowedCommands)
	}
}
```

- [ ] **Step 2: 跑测试看失败**

Run: `go test ./app/http/services/ -run TestGoalScopePlumbingRunToSession -v`
Expected: FAIL — `unknown fields goalAllowedRoots/Commands in agentAIRun`。

- [ ] **Step 3: 加字段 + 拷贝点**

(a) `agentAIRun` 结构体（`agent_ai.go`，`goalRequiredCheckCount` 字段旁）加：
```go
	// goalAllowedRoots/Commands 来自 goal_context.task，approval hook 围栏校验用。
	goalAllowedRoots    []string
	goalAllowedCommands []string
```

(b) `agentAIRunEmitter` 结构体（`goalRequiredCheckCount` 旁）加同两字段。

(c) `newAgentAIRunEmitter` 返回值加：
```go
		goalAllowedRoots:    run.goalAllowedRoots,
		goalAllowedCommands: run.goalAllowedCommands,
```

(d) `messageRun := agentAIRun{...}`（`agent_ai.go:1122`）加：
```go
		goalAllowedRoots:    goalAllowedRootsFromContext(msg["goal_context"]),
		goalAllowedCommands: goalAllowedCommandsFromContext(msg["goal_context"]),
```

(e) `agentAISession` 结构体（`goalIdentity` 旁）加：
```go
	goalAllowedRoots    []string
	goalAllowedCommands []string
```

(f) 两处 session set 点——`agent_ai.go:1879`（`session.goalIdentity = cloneGoalIdentity(emitter.goalIdentity)` 旁）和 `:1914`（session 构造字面量 `goalIdentity: cloneGoalIdentity(emitter.goalIdentity)` 旁），各加：
```go
		goalAllowedRoots:    emitter.goalAllowedRoots   // 构造字面量用 ":" 形式
		goalAllowedCommands: emitter.goalAllowedCommands
```
（:1879 是赋值语句用 `session.goalAllowedRoots = emitter.goalAllowedRoots`；:1914 是字面量用 `goalAllowedRoots: emitter.goalAllowedRoots,`。两处用 `grep -n "cloneGoalIdentity(emitter.goalIdentity)" app/http/services/agent_ai.go` 精确定位，恰好 2 处。）

(g) **【关键 4th hop，缺则 hook 永远看到空 scope】** approval hook 在 `:2648-2660` 从 session 重建 `run := agentAIRun{...}`（`m.mu.Unlock()` 在 `:2662`，故 hook 后续只能读 `run`）。该字面量在 `goalIdentity: cloneGoalIdentity(session.goalIdentity),` 旁加：
```go
		goalAllowedRoots:    session.goalAllowedRoots,
		goalAllowedCommands: session.goalAllowedCommands,
```
**不加这行 → Task 4 的 deny 永不触发（false GREEN）。** 这是 review 抓到的核心 plumbing 漏洞。

- [ ] **Step 4: 跑测试看通过 + 编译 + 手核 plumbing**

Run: `go test ./app/http/services/ -run TestGoalScopePlumbingRunToSession -v && go vet ./app/http/services/`
Expected: PASS + vet 干净。

> Step 1 的测只 pin 了 emitter 镜像；session 两处 set 点 (f) 和 4th hop (g) **手核**：跑 `grep -n "goalAllowedRoots" app/http/services/agent_ai.go` 应见 **6 处**——agentAIRun 结构体、agentAIRunEmitter 结构体、newAgentAIRunEmitter、agentAISession 结构体、:1879/:1914 两处之一（实际 5 处在 agent_ai.go + 1 在 agent_goal.go helper，视具体分布以 grep 为准）。少于预期 = 有 set 点漏改。

- [ ] **Step 4: 跑测试看通过 + 编译**

Run: `go test ./app/http/services/ -run TestGoalScopePlumbingRunToSession -v && go vet ./app/http/services/`
Expected: PASS + vet 干净。

- [ ] **Step 5: commit**

```bash
git add app/http/services/agent_ai.go app/http/services/agent_goal_scope_test.go
git commit -m "feat(goal): allowed_scope 经 run→emitter→session 三跳 plumbing"
```

---

## Task 4: approval hook 集成（goal-run 分支调校验）

**Files:**
- Modify: `app/http/services/agent_ai.go`（`handleClaudeApprovalHook` goal-run 分支 `:2677`）
- Test: `app/http/services/agent_goal_test.go`

- [ ] **Step 1: 写失败测试（hook 行为，照搬 `agent_service_test.go:3423` 模板）**

新建 `app/http/services/agent_goal_scope_hook_test.go`：
```go
package services

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

// goalScopeHookSession 照搬 TestGoalApprovalHookUsesProtocolIdentityInsteadOfSessionPrefix
// (agent_service_test.go:3423) 的 setup，加 goalAllowedRoots/Commands 种子。
func goalScopeHookSession(t *testing.T, manager *agentAIManager, projectPath string,
	goalIdentity map[string]interface{}, roots, commands []string) {
	t.Helper()
	mu, events, writer := captureAIWriter(t)
	_ = mu
	_ = events
	_, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	manager.sessions["ai_goal_scope"] = &agentAISession{
		id:            "ai_goal_scope",
		projectPath:   projectPath,
		provider:      "claudecode",
		cancel:        cancel,
		activeWriter:  writer,
		approvalToken: "scope-token",
		runSeq:        1,
		activeRunID:   "goal-run-scope",
		goalIdentity:  goalIdentity,
		goalAllowedRoots:    roots,
		goalAllowedCommands: commands,
	}
}

func TestApprovalHookGoalScopeEnforcement(t *testing.T) {
	setupAgentPolicyTestEnv(t)
	projectPath := setupAgentExecutionProjectForTest(t)
	manager := newAgentAIManager()
	svc := &AgentService{ai: manager}
	manager.service = svc
	defer manager.closeAll()

	goalIdent := map[string]interface{}{"goal_id": "g1", "goal_run_id": "goal-run-scope"}
	call := func(tool string, toolInput map[string]interface{}) map[string]interface{} {
		// 每个用例前重置 session（cancel/writer 一次性），简化：用例各自再 seed
		goalScopeHookSession(t, manager, projectPath, goalIdent, []string{projectPath + "/src"}, []string{"npm"})
		resp, err := manager.handleClaudeApprovalHook(
			context.Background(), "ai_goal_scope", "assistant_goal:goal-run-scope", "scope-token",
			map[string]interface{}{"hook_event_name": "PreToolUse", "tool_name": tool, "tool_input": toolInput},
		)
		if err != nil {
			t.Fatalf("hook error: %v", err)
		}
		return resp
	}

	// 1) Bash 命令不在 allowedCommands(npm) → scope deny
	r := call("Bash", map[string]interface{}{"command": "cat /etc/passwd"})
	if perm := permDecision(r); perm == "allow" {
		t.Errorf("out-of-scope Bash: got allow, want deny (cat not in [npm])")
	}

	// 2) Edit 路径在 allowedRoots(src) 外 → scope deny
	r = call("Edit", map[string]interface{}{"file_path": "/etc/passwd", "old_string": "a", "new_string": "b"})
	if perm := permDecision(r); perm == "allow" {
		t.Errorf("out-of-scope Edit: got allow, want deny (/etc/passwd outside src)")
	}

	// 3) Edit 路径在 src 内 → 不被 scope 挡（permissionDecision 非 scope-deny）
	r = call("Edit", map[string]interface{}{"file_path": projectPath + "/src/foo.ts", "old_string": "a", "new_string": "b"})
	if perm := permDecision(r); perm == "deny" && strings.Contains(fmt.Sprint(r), "outside allowed roots") {
		t.Errorf("in-scope Edit wrongly scope-denied: %#v", r)
	}
}

// vibecoming 回归保护：goalIdentity 空 → scope 不触发（即便 session 上挂了 roots）。
func TestApprovalHookGoalScopeVibeNotTriggered(t *testing.T) {
	setupAgentPolicyTestEnv(t)
	projectPath := setupAgentExecutionProjectForTest(t)
	manager := newAgentAIManager()
	svc := &AgentService{ai: manager}
	manager.service = svc
	defer manager.closeAll()
	// vibe session：goalIdentity 空，但故意挂上 roots（证明 goal 分支整体跳过）
	goalScopeHookSession(t, manager, projectPath, nil, []string{projectPath + "/src"}, []string{"npm"})
	resp, err := manager.handleClaudeApprovalHook(
		context.Background(), "ai_goal_scope", "assistant_goal:goal-run-scope", "scope-token",
		map[string]interface{}{"hook_event_name": "PreToolUse", "tool_name": "Edit",
			"tool_input": map[string]interface{}{"file_path": "/etc/passwd", "old_string": "a", "new_string": "b"}},
	)
	if err != nil {
		t.Fatalf("hook error: %v", err)
	}
	// 关键：vibe 路径不应出现 scope-deny 原因（goal 分支被 len(goalIdentity)==0 跳过）
	if strings.Contains(fmt.Sprint(resp), "outside allowed roots") {
		t.Errorf("vibe session triggered scope enforcement (should be skipped): %#v", resp)
	}
}

func permDecision(resp map[string]interface{}) string {
	if h, ok := resp["hookSpecificOutput"].(map[string]interface{}); ok {
		return fmt.Sprint(h["permissionDecision"])
	}
	return ""
}
```
> 模板依据：`TestGoalApprovalHookUsesProtocolIdentityInsteadOfSessionPrefix`（agent_service_test.go:3423）证明 `cat /etc/passwd` 这类 Bash 在 goal session 现行**允许**——所以用例 1 改成 scope deny 是干净的 RED→GREEN。`models` import 若未用可删。

- [ ] **Step 2: 跑测试看失败**

Run: `go test ./app/http/services/ -run 'TestApprovalHookGoalScope' -v`
Expected: FAIL — 用例 1/2 拿到 `allow`（hook 还没接围栏，越界照放行）。

- [ ] **Step 3: hook 接围栏（读 `run`，不是 `session`——`:2662` 已 Unlock）**

`handleClaudeApprovalHook` goal-run 分支（`agent_ai.go:2677` `if len(run.goalIdentity) > 0 {`），在现有 `decisionAutoDeny` 检查**之前**插入：
```go
	if len(run.goalIdentity) > 0 {
		// Gap B: allowed_scope 执行边界。越界（路径/命令）直接 deny，claude 收到自纠。
		// agent 级强制，非 OS 级——见 docs/superpowers/specs/2026-08-08-goal-allowed-scope-enforcement-design.md §6。
		// 注意：m.mu.Unlock() 已在 :2662 执行，此处只能读局部 run（run 在 :2648-2660 从 session 拷贝）。
		if ok, reason := goalScopeAllowsToolCall(toolName, raw, run.projectPath, run.goalAllowedRoots, run.goalAllowedCommands); !ok {
			logger.Info(fmt.Sprintf("approval-hook: SCOPE-DENY (goal run) tool=%s reason=%s session=%s", toolName, reason, sessionID))
			return claudeApprovalHookDecision(hookEventName, false, "blocked: "+reason), nil
		}
		if svc := m.approvalService(); svc != nil {
			// ... 既有 decisionAutoDeny 分支不动 ...
```
（`run` 在 `:2648-2660` 已从 session 拷好含新字段——依赖 Task 3 (g) 的 4th hop；`toolName`/`raw` 在 `:2672-2674` 已取。）

- [ ] **Step 4: 跑测试看通过**

Run: `go test ./app/http/services/ -run 'TestApprovalHookGoalScope' -v`
Expected: 三个全 PASS（越界 deny、围栏内不挡、vibe 不触发）。

- [ ] **Step 5: commit**

```bash
git add app/http/services/agent_ai.go app/http/services/agent_goal_test.go
git commit -m "feat(goal): approval hook 接入 allowed_scope 围栏（claude goal run）"
```

---

## Task 5: 全量回归 + 限制注释

**Files:** 无新改（仅跑测 + 抽查注释已在 Task 2/4 落地）

- [ ] **Step 1: 全服务包测**

Run: `go test ./app/http/services/ -count=1`
Expected: 全 PASS（含既有 Goal + approval 套件零回归）。约 ~90s。

- [ ] **Step 2: vet + build**

Run: `go vet ./app/http/services/ && go build ./app/http/services/`
Expected: 干净。（`go build ./...` 的 root `main undeclared` 是仓库预存，忽略。）

- [ ] **Step 3: 抽查 §6 限制注释落地**

确认 `goalScopeAllowsToolCall` doc 注释明示"agent 级非 OS 级、防漂移不防对抗性越狱"（Task 2 已写）。若有遗漏补上。

- [ ] **Step 4: 汇总 + 留待部署**

改动全 UNCOMMITTED→ 实际按上述各 Task 已 commit。汇总：本批纯 agent 侧，生效需重编 Go 二进制 + 重启；server/phone 零改。真机 smoke（goal task 越界改文件/跑命令被挡）部署后验。

---

## 验收标准（对齐 spec §7）
- [ ] `goalScopeAllowsToolCall` 矩阵测全绿（Task 2）。
- [ ] hook 集成测：越界 deny / 围栏内放行 / vibecoding 不触发（Task 4）。
- [ ] 全 Goal + approval 套件零回归（Task 5）。
- [ ] vet/build 干净。
- [ ] §6 限制写进代码注释。
