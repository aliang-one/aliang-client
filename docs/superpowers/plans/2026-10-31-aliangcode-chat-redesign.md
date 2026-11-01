# AliangCode 聊天页大重构实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 按 spec(`docs/superpowers/specs/2026-10-31-aliangcode-chat-redesign-design.md` v3)实现聊天页五件套:转录级真撤回重发(Go 两处小改+前端全链)、markdown 渲染、steer 融合/停止/审批硬拦、可交互 option 审批、品牌绿视觉现代化 + 组件化拆分。

**Architecture:** 内核侧新增 journal 截断(文件位置语义)+ create 消费 `truncate_before_message_id`(不带 transcript,宿主全文重建);前端先做纯移动的组件化拆分(既有测试全绿),再在新结构上逐个叠加功能。每个 Task 独立可测、独立提交。

**Tech Stack:** Go(宿主 services 层)/ Vue 3.5 + Vite 5 + Tailwind 3.4 / vitest + Playwright / 新增 markdown-it + dompurify + highlight.js。

---

## 全局约束(每个 Task 的执行者都必须遵守)

1. **开发必须在 worktree**:`feature/chat-redesign` 分支(Task 0 创建)。不直接改 master 工作区。
2. **提交规约**(CLAUDE.md):标题全中文 `新增:xxx` / `修复:xxx`,HEREDOC 多行,结尾:
   ```
   🤖 Generated with [Claude Code](https://claude.com/claude-code)

   Co-Authored-By: Claude Code <noreply@anthropic.com>
   ```
3. **data-testid 硬约束**:重构全程保留 spec §G#1 枚举的 14 个(标签形态不变;`chat-settings-directory` 锁定态必须是 `span`)。
4. **帧契约**:`sessionCreatePayload` 常规路径载荷逐字节同构(truncate 键只在撤回路径出现);`__CHAT_IDLE_TIMEOUT_MS__` 的 `globalThis` 读取点保留;resume 仅首次 create;steer 三分流不变。
5. **Go 测试基线**(memory):`app/http/services` 包高并行偶发子进程超时 flake——失败先单包复跑再判断;`-race` 下 TestStartProxyWhenSessionReady / TestHandleAgentAuthRejectedNotificationRules 是基线 flake 勿当回归;auth 包 TestSessionAuthorityProxyAdmissionLinearizesWithDemotion 基线 5/10 与本重构无关。
6. **新帧语义只加 agent 进程** `DispatchLocalAI` 一侧(core 56431 是 TCP 裸泵,零改动)。
7. 前端测试命令一律在 `app/website/` 下执行;Go 测试在仓库根。
8. 聊天内新增浮层 z 序 ≤999(Dashboard 弹窗层 z-1000 及以上不可越过,spec §G#6)。
9. Task 8-14 各自提交时,把本 Task 引入的 i18n 键以 zh/en 成对写入(`src/i18n/zh.js`/`en.js`),不留到 Task 15(平权单测守卫;i18n.test.js 只查两语言平权,查不出「引用了不存在的键」)。
10. **组件/DOM 测试环境**:仓内 vitest 默认 **node 环境**(vite.config test 块无 environment,jsdom 未装),`main.test.js` 是源码正则断言、**没有 DOM 先例可抄**——Task 3 统一装 `jsdom`(devDep)并在 vite.config test 块加 `environment: 'jsdom'`(一处配置解锁 chatMarkdown/ChatMarkdown/ChatOptionCard 全部 DOM 断言;e2e 不受影响);组件测试用 `createApp` + `document.body` 挂载断言 innerHTML。DOMPurify 在 jsdom 下才是完整形态(node 裸环境 `.sanitize` 为 undefined)。

---

### Task 0: worktree 与构建健全性

**Files:** 无新文件(环境准备)

- [ ] **Step 1: 创建 worktree**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
git worktree add ../alianggate-chat-redesign -b feature/chat-redesign
cd ../alianggate-chat-redesign
```

- [ ] **Step 2: 前端依赖就位**(新 worktree 首次构建坑,memory 存档)

```bash
cp -R /Users/mac/MyProgram/GoProgram/nursor/alianggate/app/website/dist app/website/dist 2>/dev/null || true
cd app/website && npm ci && cd ../..
go build ./...
```

Expected: go build 无错误(嵌入占位已就位)。

- [ ] **Step 3: 基线确认**

```bash
cd app/website && npx vitest run 2>&1 | tail -5 && npx playwright test tests/e2e/chat-settings.spec.js 2>&1 | tail -3
```

Expected: 全绿(基线坏了先停下排查,不带病开工)。

---

### Task 1: Go — journal 截断函数 `truncateBefore`

**Files:**
- Modify: `app/http/services/agent_ai_agentcore_journal.go`(文件末尾追加)
- Test: `app/http/services/agent_ai_agentcore_journal_test.go`(已存在则追加用例)

- [ ] **Step 1: 写失败测试**

在 `agent_ai_agentcore_journal_test.go` 追加(复用该文件既有的临时目录构造方式;若文件不存在,新建,测试内直接 `newAgentCoreJournal(t.TempDir())`):

```go
// 辅助:写一份标准 journal(头行 + msg(u1) + turn + msg(a1) + end + msg(u2-目标) + turn + msg(a2) + end)
func writeTruncateFixture(t *testing.T, j *agentCoreJournal, sessionID string) {
	t.Helper()
	j.ensureSessionHeader(sessionID, "/proj", "glm-5.3", "2026-10-31T00:00:00Z", "dashboard")
	j.appendUser(sessionID, "u1", "第一问", "2026-10-31T00:01:00Z")
	j.appendAssistantTurn(sessionID, 1, "中间叙述", "", "end_turn", nil)
	j.appendAssistant(sessionID, "a1", "第一答", "2026-10-31T00:02:00Z")
	j.appendEnd(sessionID, "success", "")
	j.appendUser(sessionID, "u2", "第二问", "2026-10-31T00:03:00Z")
	j.appendAssistantTurn(sessionID, 2, "叙述二", "", "end_turn", nil)
	j.appendAssistant(sessionID, "a2", "第二答", "2026-10-31T00:04:00Z")
	j.appendEnd(sessionID, "success", "")
}

func TestJournalTruncateBeforeKeepsPrefixWithAttachedRows(t *testing.T) {
	j := newAgentCoreJournal(t.TempDir())
	const sid = "s-trunc-1"
	writeTruncateFixture(t, j, sid)

	applied, err := j.truncateBefore(sid, "u2")
	if err != nil || !applied {
		t.Fatalf("truncate: applied=%v err=%v", applied, err)
	}
	hist, _, err := readAgentCoreJournalHistoryFrom(j.dir, sid)
	if err != nil {
		t.Fatal(err)
	}
	// 保留区间 = u1 + a1(a1 是 u1 区间宿主行);u2 之后全部消失。
	if len(hist) != 2 || hist[0].MessageID != "u1" || hist[1].MessageID != "a1" || hist[1].Content != "第一答" {
		t.Fatalf("unexpected history: %+v", hist)
	}
}

func TestJournalTruncateBeforeIsIdempotent(t *testing.T) {
	j := newAgentCoreJournal(t.TempDir())
	const sid = "s-trunc-2"
	writeTruncateFixture(t, j, sid)
	if _, err := j.truncateBefore(sid, "u2"); err != nil {
		t.Fatal(err)
	}
	applied, err := j.truncateBefore(sid, "u2") // 已截过:目标行不在,幂等成功
	if err != nil || applied {
		t.Fatalf("second truncate should be no-op: applied=%v err=%v", applied, err)
	}
}

func TestJournalTruncateBeforeMissingTargetAndFile(t *testing.T) {
	j := newAgentCoreJournal(t.TempDir())
	const sid = "s-trunc-3"
	writeTruncateFixture(t, j, sid)
	// 不匹配的 id:无操作不报错
	if applied, err := j.truncateBefore(sid, "nope"); err != nil || applied {
		t.Fatalf("missing target: applied=%v err=%v", applied, err)
	}
	// 文件缺失:幂等成功
	if applied, err := j.truncateBefore("ghost", "u1"); err != nil || applied {
		t.Fatalf("missing file: applied=%v err=%v", applied, err)
	}
}

func TestJournalTruncateBeforeKeepsHeadWhenFirstMsgIsTarget(t *testing.T) {
	j := newAgentCoreJournal(t.TempDir())
	const sid = "s-trunc-4"
	writeTruncateFixture(t, j, sid)
	if _, err := j.truncateBefore(sid, "u1"); err != nil {
		t.Fatal(err)
	}
	hist, head, err := readAgentCoreJournalHistoryFrom(j.dir, sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 0 {
		t.Fatalf("expect empty history, got %+v", hist)
	}
	if head.projectPath != "/proj" {
		t.Fatalf("head lost: %+v", head)
	}
}

func TestJournalTruncateBeforeSteerRowFollowsPosition(t *testing.T) {
	j := newAgentCoreJournal(t.TempDir())
	const sid = "s-trunc-5"
	// 手工排布(fixture 的行序固定,无法把 steer 行插进 u1 区间):steer 是
	// 带 message_id 的 kind=msg 行,物理位置在 u1 区间内(a1 之后、u2 之前)。
	j.ensureSessionHeader(sid, "/proj", "glm-5.3", "2026-10-31T00:00:00Z", "dashboard")
	j.appendUser(sid, "u1", "第一问", "ts")
	j.appendAssistant(sid, "a1", "第一答", "ts")
	j.appendUser(sid, "steer-1", "插话", "ts")
	j.appendUser(sid, "u2", "第二问", "ts")
	j.appendAssistant(sid, "a2", "第二答", "ts")
	if _, err := j.truncateBefore(sid, "u2"); err != nil {
		t.Fatal(err)
	}
	hist, _, _ := readAgentCoreJournalHistoryFrom(j.dir, sid)
	// 保留侧断言(关键):u1/a1/steer 三行按位置归属 u1 区间,全保。
	if len(hist) != 3 || hist[2].MessageID != "steer-1" {
		t.Fatalf("steer row inside kept interval must survive: %+v", hist)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./app/http/services/ -run TestJournalTruncateBefore -v`
Expected: FAIL(`j.truncateBefore undefined`)。

- [ ] **Step 3: 实现**

在 `agent_ai_agentcore_journal.go` 文件末尾(`agentCoreJournalOrigin` 之后)追加:

```go
// truncateBefore 按「文件位置语义」截断 journal(spec §A.1):保留头行与
// 位置先于 beforeMessageID 所在 msg 行的全部行;end/turn/tool/approval 等
// 非 msg 行归属其前最近的 msg 行所在区间(随宿主保留/丢弃);目标前没有
// 任何 msg 行时只留头行(孤儿非 msg 行丢弃)。目标 msg 行不存在或文件缺失
// 幂等成功(仅日志;「已截断过」与「id 不匹配」两成因不可区分,接受模糊,
// 终态展示以 journal 刷新为准)。同一 beforeMessageID 重复调用幂等,支持
// 撤回重试。锁内全读→tmp→rename,prependHeaderLocked 同款套路。
// 返回 applied=是否实际发生截断。
func (j *agentCoreJournal) truncateBefore(sessionID, beforeMessageID string) (bool, error) {
	if j == nil || j.dir == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(beforeMessageID) == "" {
		return false, nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	path := filepath.Join(j.dir, agentCoreJournalFileName(sessionID))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	lines := splitJSONLLines(data)
	if len(lines) == 0 {
		return false, nil
	}
	// 头行:首行是合法 session 头则原样保留,否则不造头(读侧容忍无头)。
	var buf bytes.Buffer
	var first map[string]interface{}
	start := 0
	if json.Unmarshal(lines[0], &first) == nil && first["kind"] == "session" {
		buf.Write(lines[0])
		buf.WriteByte('\n')
		start = 1
	}
	// 线性扫描:定位目标 msg 行;msg 行携带 message_id,非 msg 行归属区间。
	isMsg := make([]bool, len(lines))
	target := -1
	for i := start; i < len(lines); i++ {
		var row map[string]interface{}
		if json.Unmarshal(lines[i], &row) != nil || row["kind"] != "msg" {
			continue
		}
		isMsg[i] = true
		if target < 0 {
			if id, _ := row["message_id"].(string); id == beforeMessageID {
				target = i
			}
		}
	}
	if target < 0 {
		logger.Info(fmt.Sprintf("agentcore-journal: truncate no-op (target absent) session=%s before=%s", sessionID, beforeMessageID))
		return false, nil
	}
	firstKeep := -1
	for i := start; i < target; i++ {
		if isMsg[i] {
			firstKeep = i
			break
		}
	}
	if firstKeep >= 0 {
		for i := firstKeep; i < target; i++ {
			buf.Write(lines[i])
			buf.WriteByte('\n')
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	logger.Info(fmt.Sprintf("agentcore-journal: truncated session=%s before=%s kept=%d/%d lines", sessionID, beforeMessageID, target-start, len(lines)-start))
	return true, nil
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./app/http/services/ -run TestJournalTruncateBefore -v`
Expected: 5 个用例全 PASS。

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai_agentcore_journal.go app/http/services/agent_ai_agentcore_journal_test.go
git commit -m "$(cat <<'EOF'
新增:agentcore journal 按消息位置截断函数(撤回重发内核侧第一步)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Go — create() 消费 `truncate_before_message_id`

**Files:**
- Modify: `app/http/models/agent_protocol.go`(≈:311 `ai.session.create` 的 Optional **struct 字面量切片**;≈:240 `AgentEventAISessionCreated` 的 ServerSends Optional)
- Modify: `app/http/services/agent_ai.go`(create() 双分支)
- Test: `app/http/services/agent_ai_test.go`(追加;复用既有 manager 构造惯例)

- [ ] **Step 1: 契约清单更新**

两处,均为**切片元素**(不是注释块,插错位置无法编译):

① `ai.session.create` Optional 切片(≈:311),在 `"transcript"` 元素后追加 `"truncate_before_message_id"`:

```go
		Optional: []string{ ..., "transcript",
			// 撤回重发专用:截断 journal 到该 msg 行之前,宿主从截断后 journal
			// 全文重建 history(帧不携带 transcript);仅 agentcore 生效。
			"truncate_before_message_id", "claude_remote_policy"},
```

② `AgentEventAISessionCreated` 的 ServerSends Optional(≈:240,现为 `{"state=idle"}`)补 `"truncate_applied"`(契约表经 `DefaultAgentProtocolContract` 对外下发;`agent_protocol_contract_test.go` 有抽查先例,同步补断言)。

- [ ] **Step 2: 写失败测试**

⚠️ **测试隔离铁律**(memory `go-test-hits-live-agent-guard` 同类事故预防):manager 的 journal 字段实名是 **`m.coreJournal`**(`agent_ai.go:52`,`m.journal` 不存在)。测试必须 `m.coreJournal = newAgentCoreJournal(t.TempDir())` 覆盖(先例 `agent_ai_slash_journal_test.go:19-24`),实现读取走 `m.coreJournal.dir`——**禁止**任何路径触到全局 `agentCoreJournalDir()`(真机 `~/.aliang/ai-sessions/`)。

```go
func TestCreateTruncateRebuildsHistoryFromJournal(t *testing.T) {
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(t.TempDir()) // 隔离:绝不碰真机 journal
	const sid = "s-create-trunc"
	m.coreJournal.ensureSessionHeader(sid, "/proj", "glm-5.3", "2026-10-31T00:00:00Z", "dashboard")
	m.coreJournal.appendUser(sid, "u1", "第一问", "ts")
	m.coreJournal.appendAssistant(sid, "a1", "第一答", "ts")
	m.coreJournal.appendUser(sid, "u2", "第二问", "ts")

	var created map[string]interface{}
	m.create(map[string]interface{}{
		"session_id":                 sid,
		"provider":                   "agentcore",
		"model":                      "glm-5.3",
		"truncate_before_message_id": "u2",
	}, func(p map[string]interface{}) error { created = p; return nil })

	if created["truncate_applied"] != true {
		t.Fatalf("truncate_applied echo missing: %+v", created)
	}
	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess == nil || len(sess.history) != 2 ||
		sess.history[0].MessageID != "u1" || sess.history[1].Content != "第一答" {
		t.Fatalf("history not rebuilt from truncated journal: %+v", sess)
	}
}

func TestCreateTruncateRejectsWhileRunActive(t *testing.T) {
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(t.TempDir())
	const sid = "s-create-trunc-busy"
	m.coreJournal.ensureSessionHeader(sid, "/proj", "glm-5.3", "ts", "dashboard")
	m.coreJournal.appendUser(sid, "u1", "第一问", "ts")
	m.mu.Lock()
	m.sessions[sid] = &agentAISession{id: sid, provider: "agentcore", cancel: func() {}}
	m.mu.Unlock()

	var resp map[string]interface{}
	m.create(map[string]interface{}{
		"session_id":                 sid,
		"provider":                   "agentcore",
		"truncate_before_message_id": "u1",
	}, func(p map[string]interface{}) error { resp = p; return nil })

	// 时序铁律:拒绝必须发生在任何 journal 写之前——被拒时 journal 必须原封不动。
	if resp["type"] != models.AgentEventAIError {
		t.Fatalf("expect ai.error, got %+v", resp)
	}
	hist, _, _ := readAgentCoreJournalHistoryFrom(m.coreJournal.dir, sid)
	if len(hist) != 1 {
		t.Fatalf("journal must be untouched on reject: %+v", hist)
	}
}

func TestCreateTruncateAllowsEmptyHistory(t *testing.T) {
	// journal 仅 u1 一条,truncate=u1 → history 空 + truncate_applied=true
	// (「撤回到空」语义,允许空覆盖)
}

func TestCreateTruncateIgnoredForNonAgentcore(t *testing.T) {
	// provider=claudecode + truncate 字段 → truncate_applied=false 回显,
	// history 走既有逻辑,journal 不动
}

func TestCreateTruncateJournalUnavailableRejects(t *testing.T) {
	// m.coreJournal = nil(journal 禁用)→ ai.error:撤回锚点不存在,拒绝
}

func TestCreateTruncateIOErrorRejects(t *testing.T) {
	// 读回失败注入:用 agent_ai_agentcore_run_test.go:865 的「文件冒充目录」
	// blocker 手法把 coreJournal.dir 顶成普通文件 → ai.error,
	// 不注册会话/不改 history(不静默空 history)
}
```

注:`agentAISession.cancel` 字段实名(`agent_ai.go:315`,clearRunning 清位 `agent_ai.go:4241`);:322 注释明言单测/TUI 注册的会话无 cancel——谓词对正常 create/run 路径充分,测试装配用 `cancel: func(){}` 置位。

- [ ] **Step 3: 运行确认失败**

Run: `go test ./app/http/services/ -run TestCreateTruncate -v`
Expected: FAIL(字段未消费/truncate_applied 缺失)。

- [ ] **Step 4: 实现 create() 双分支**

**时序铁律(评审 Issue 1):活跃拒绝必须先于任何 journal 写。** 先截断后拒绝 = rename 后僵尸 run 终态行追加进新文件、被撤行复活(spec §6 禁止场景)。在 `create()` 的 `history := remoteAgentAIHistory(msg)` 与 initialContext 前插之后、`m.mu.Lock()` 之前插入:

```go
	// 撤回重发(spec §A.2):截断 journal + history 全文重建(帧不带 transcript)。
	// 时序:①活跃拒绝先于任何 journal 写;②不在内存 ⇒ 无活跃 run,前置检查
	// 覆盖 create() 双分支;③读写失败绝不静默空 history,幂等重试即收敛。
	truncateBeforeID := strings.TrimSpace(remoteString(msg, "truncate_before_message_id"))
	truncateApplied := false
	if truncateBeforeID != "" && provider == "agentcore" {
		m.mu.Lock()
		existingPeek := m.sessions[sessionID]
		busy := existingPeek != nil && existingPeek.cancel != nil
		m.mu.Unlock()
		if busy {
			_ = writeJSON(agentAIErrorPayload(sessionID, "", errors.New("ai.session.create truncate rejected: run active")))
			return
		}
		if m.coreJournal == nil {
			_ = writeJSON(agentAIErrorPayload(sessionID, "", errors.New("ai.session.create truncate unavailable: journal disabled")))
			return
		}
		applied, err := m.coreJournal.truncateBefore(sessionID, truncateBeforeID)
		if err != nil {
			_ = writeJSON(agentAIErrorPayload(sessionID, "", fmt.Errorf("ai.session.create truncate failed: %w", err)))
			return
		}
		truncateApplied = applied
		hist, _, rerr := readAgentCoreJournalHistoryFrom(m.coreJournal.dir, sessionID)
		if rerr != nil {
			// ErrNotExist 理论不可达(截断成功=文件在);其余 IO 错一律拒绝。
			_ = writeJSON(agentAIErrorPayload(sessionID, "", fmt.Errorf("ai.session.create rebuild failed: %w", rerr)))
			return
		}
		history = hist // 空 journal=空 history:允许空覆盖(撤回到空)
	}
```

在 **existing 分支**(`if existing := m.sessions[sessionID]; existing != nil {`)内——活跃拒绝已前置,**不再重复拒绝**——把 history 覆盖条件改为:

```go
		if truncateBeforeID != "" && provider == "agentcore" {
			// 撤回路径:允许空覆盖(重建结果哪怕是空也必须生效)
			existing.history = trimAgentAIHistoryForSession(existing, history)
		} else if len(history) > 0 {
			existing.history = trimAgentAIHistoryForSession(existing, history)
		}
```

**新建分支**零改动:session 构造的 `history: trimAgentAIHistoryWithLimits(history, ...)` 中 history 已被重建值替换(≈:1770,确认生效即可)。

(`existing.cancel` 实名 `agent_ai.go:315`,clearRunning 清位 `:4241`;defer LIFO 顺序 cancel→end行→markRunGuardDone→clearRunning 保证 cancel==nil ⇒ end 行已落,**禁止重排**——spec §A.2。)

两个分支的回显处,`agentAISessionCreatedPayload(...)` 返回的 map 统一补:

```go
		payload := agentAISessionCreatedPayload(existing) // 或 session
		if truncateBeforeID != "" {
			payload["truncate_applied"] = truncateApplied
		}
		_ = writeJSON(payload)
```

- [ ] **Step 5: 运行确认通过 + 回归**

Run: `go test ./app/http/services/ -run 'TestCreateTruncate|TestCreate' -v`
Expected: 全 PASS。再跑 `go test ./app/http/services/ 2>&1 | tail -3` 确认包绿(偶发子进程 flake 单包复跑)。

- [ ] **Step 6: 提交**

```bash
git add app/http/models/agent_protocol.go app/http/services/agent_ai.go app/http/services/agent_ai_test.go
git commit -m "$(cat <<'EOF'
新增:ai.session.create 消费 truncate_before_message_id(journal 截断+全文重建 history)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: 前端依赖安装

**Files:**
- Modify: `app/website/package.json` / `package-lock.json`
- Modify: `app/website/vite.config.js`(test 块加 environment)

- [ ] **Step 1: 安装**

```bash
cd app/website
npm install markdown-it@^14 dompurify@^3 highlight.js@^11
npm install -D jsdom
```

- [ ] **Step 2: vitest DOM 环境**(评审 Issue A:node 裸环境下 DOMPurify 无 `.sanitize`、`document` 不存在)

`vite.config.js` test 块加一行:

```js
  test: {
    include: ['src/**/*.test.js'],
    environment: 'jsdom',
  },
```

- [ ] **Step 3: 冒烟**

```bash
node -e "const M=require('markdown-it');console.log(typeof M)"
npx vitest run src/main.test.js 2>&1 | tail -2
```

Expected: markdown-it 为 function;main.test.js 在 jsdom 环境下保持绿(环境切换无副作用)。

- [ ] **Step 4: 提交**

```bash
git add package.json package-lock.json vite.config.js
git commit -m "新增:聊天 markdown 渲染依赖与 vitest jsdom 环境(markdown-it/dompurify/highlight.js/jsdom)"
```

---

### Task 4: 前端 — markdown 管线纯逻辑 `chatMarkdown.js`

**Files:**
- Create: `app/website/src/utils/chatMarkdown.js`
- Test: `app/website/src/utils/chatMarkdown.test.js`

- [ ] **Step 1: 写失败测试**

```js
import { describe, it, expect } from 'vitest';
import { renderMarkdownSafe, normalizeCjkMarkdown, extractAliangOptions } from './chatMarkdown';

describe('normalizeCjkMarkdown', () => {
  it('补齐 #/​- 后缺空格的标题与列表', () => {
    expect(normalizeCjkMarkdown('#标题')).toBe('# 标题');
    expect(normalizeCjkMarkdown('-项目')).toBe('- 项目');
    expect(normalizeCjkMarkdown('##二级')).toBe('## 二级');
  });
  it('不动代码围栏内的内容', () => {
    const src = '```\n#not-a-heading\n```';
    expect(normalizeCjkMarkdown(src)).toBe(src);
  });
  it('不动正常 markdown', () => {
    expect(normalizeCjkMarkdown('# 已有空格')).toBe('# 已有空格');
  });
});

describe('renderMarkdownSafe', () => {
  it('渲染标题/列表/代码块并保留语言类名', () => {
    const html = renderMarkdownSafe('## 标题\n\n```go\nfmt.Println(1)\n```');
    expect(html).toContain('<h2>');
    expect(html).toContain('language-go');
  });
  it('消毒脚本注入', () => {
    const html = renderMarkdownSafe('<script>alert(1)</script>**bold**');
    expect(html).not.toContain('<script');
    expect(html).toContain('<strong>');
  });
  it('链接带 target=_blank rel=noopener', () => {
    const html = renderMarkdownSafe('[x](https://a.b)');
    expect(html).toContain('target="_blank"');
    expect(html).toContain('rel="noopener"');
  });
  it('未闭合围栏按代码处理不抛错', () => {
    expect(() => renderMarkdownSafe('```js\nconst a = 1;')).not.toThrow();
  });
});

describe('extractAliangOptions', () => {
  it('提取围栏 JSON 并剔除正文', () => {
    const src = '前言\n```aliang-options\n{"option_id":"op1","options":[{"id":"a","label":"A"}]}\n```\n后记';
    const blocks = extractAliangOptions(src);
    expect(blocks).toHaveLength(1);
    expect(blocks[0].option_id).toBe('op1');
  });
  it('坏 JSON 块安全跳过', () => {
    expect(extractAliangOptions('```aliang-options\nnot-json\n```')).toHaveLength(0);
  });
});
```

- [ ] **Step 2: 运行确认失败** — `npx vitest run src/utils/chatMarkdown.test.js` → FAIL(模块不存在)。

- [ ] **Step 3: 实现 `chatMarkdown.js`**

```js
// 聊天气泡 markdown 管线(spec §E):markdown-it 渲染 → highlight.js 子集
// 高亮 → DOMPurify 消毒。GLM 中文容错(行首 #/- 后缺空格)在渲染前 normalize,
// 代码围栏内容不受影响。本文件零 Vue 依赖,纯函数,vitest 直测。
import MarkdownIt from 'markdown-it';
import DOMPurify from 'dompurify';
import hljs from 'highlight.js/lib/core';
import javascript from 'highlight.js/lib/languages/javascript';
import typescript from 'highlight.js/lib/languages/typescript';
import go from 'highlight.js/lib/languages/go';
import json from 'highlight.js/lib/languages/json';
import bash from 'highlight.js/lib/languages/bash';
import yaml from 'highlight.js/lib/languages/yaml';
import python from 'highlight.js/lib/languages/python';
import sql from 'highlight.js/lib/languages/sql';
import xml from 'highlight.js/lib/languages/xml';
import css from 'highlight.js/lib/languages/css';
import diff from 'highlight.js/lib/languages/diff';

hljs.registerLanguage('javascript', javascript);
hljs.registerLanguage('typescript', typescript);
hljs.registerLanguage('js', javascript);
hljs.registerLanguage('ts', typescript);
hljs.registerLanguage('go', go);
hljs.registerLanguage('json', json);
hljs.registerLanguage('bash', bash);
hljs.registerLanguage('shell', bash);
hljs.registerLanguage('yaml', yaml);
hljs.registerLanguage('python', python);
hljs.registerLanguage('sql', sql);
hljs.registerLanguage('html', xml);
hljs.registerLanguage('xml', xml);
hljs.registerLanguage('css', css);
hljs.registerLanguage('diff', diff);

const md = new MarkdownIt({ html: false, linkify: true, highlight(code, lang) {
  if (lang && hljs.getLanguage(lang)) {
    try { return hljs.highlight(code, { language: lang }).value; } catch (_) { /* fallthrough */ }
  }
  return '';
} });

// 默认链接规则加 target/_rel(消毒白名单同步放行这两个属性)
const defaultRender = md.renderer.rules.link_open || ((tokens, idx, opts, _, self) => self.renderToken(tokens, idx, opts));
md.renderer.rules.link_open = (tokens, idx, opts, env, self) => {
  tokens[idx].attrSet('target', '_blank');
  tokens[idx].attrSet('rel', 'noopener');
  return defaultRender(tokens, idx, opts, env, self);
};

// GLM 中文容错第一批(spec §E):行首 #/​-/+/* 后紧跟非空白非换行字符 → 补空格。
// 逐行扫描并跟踪 ``` 围栏开闭,围栏内不动。
export function normalizeCjkMarkdown(src) {
  if (typeof src !== 'string' || !src) return src || '';
  let fenced = false;
  return src.split('\n').map((line) => {
    if (/^\s*(```|~~~)/.test(line)) { fenced = !fenced; return line; }
    if (fenced) return line;
    return line.replace(/^(\s*)(#{1,6}|[-+*])(?=[^\s#*+-])/y, (m, ind, mark) => `${ind}${mark} `);
  }).join('\n');
}

const aliangOptionsRe = /```aliang-options\s*\n([\s\S]*?)\n```/g;

// 提取 ```aliang-options 围栏(供静态卡渲染/防双份对账);坏 JSON 块跳过。
export function extractAliangOptions(src) {
  const out = [];
  if (typeof src !== 'string') return out;
  for (const m of src.matchAll(aliangOptionsRe)) {
    try {
      const parsed = JSON.parse(m[1]);
      if (parsed && typeof parsed === 'object' && Array.isArray(parsed.options) && parsed.options.length) {
        out.push(parsed);
      }
    } catch (_) { /* 坏块跳过 */ }
  }
  return out;
}

// 从原文剔除 aliang-options 围栏(交互卡在场时正文不再重复渲染)。
export function stripAliangOptions(src) {
  if (typeof src !== 'string') return '';
  return src.replace(aliangOptionsRe, '').trim();
}

// 渲染+消毒一步到位;任何异常回退为转义纯文本(流式半截内容永不白屏)。
export function renderMarkdownSafe(src) {
  const input = normalizeCjkMarkdown(String(src ?? ''));
  let html;
  try {
    html = md.render(input);
  } catch (_) {
    html = md.render('');
  }
  return DOMPurify.sanitize(html, {
    ADD_ATTR: ['target', 'rel'],
    FORBID_TAGS: ['style', 'iframe', 'form'],
  });
}
```

- [ ] **Step 4: 运行确认通过** — `npx vitest run src/utils/chatMarkdown.test.js` → PASS。
- [ ] **Step 5: 提交**

```bash
git add src/utils/chatMarkdown.js src/utils/chatMarkdown.test.js
git commit -m "新增:聊天气泡 markdown 安全渲染管线(GLM 中文容错+消毒+aliang-options 提取)"
```

---

### Task 5: 前端 — 时间线归并与撤回切片纯逻辑

**Files:**
- Create: `app/website/src/utils/chatTimeline.js`(归并)
- Create: `app/website/src/utils/chatRecall.js`(切片+载荷)
- Test: `app/website/src/utils/chatTimeline.test.js` / `chatRecall.test.js`(chatSettings.test.js 追加回归用例)

注:`chatSettings.js` 本体**不动**——`sessionCreatePayload` 行为被既有测试钉死;truncate 载荷由 `chatRecall.recallCreatePayload` 独立组装(评审 Issue 8:Files 与 Step 3 口径以此为准)。

- [ ] **Step 1: 写失败测试**

`chatTimeline.test.js`:

```js
import { describe, it, expect } from 'vitest';
import { mergeTimeline } from './chatTimeline';

describe('mergeTimeline', () => {
  it('按 ts 升序归并 messages 与 steerItems 并打 type 标', () => {
    const out = mergeTimeline(
      [{ role: 'user', content: 'a', ts: 200 }, { role: 'assistant', content: 'b', ts: 300 }],
      [{ messageId: 's1', content: 'x', ts: 250 }],
    );
    expect(out.map(i => i.type)).toEqual(['message', 'steer', 'message']);
  });
  it('缺 ts 的项排最后(防御)', () => {
    const out = mergeTimeline([{ role: 'user', content: 'a' }], []);
    expect(out).toHaveLength(1);
  });
  it('脏输入安全返回空数组', () => {
    expect(mergeTimeline(null, undefined)).toEqual([]);
  });
});
```

`chatRecall.test.js`:

```js
import { describe, it, expect } from 'vitest';
import { sliceMessagesBefore, canRecall, recallCreatePayload } from './chatRecall';
import { sessionCreatePayload } from './chatSettings';

describe('sliceMessagesBefore', () => {
  it('返回目标消息之前的切片(不含目标)', () => {
    const msgs = [
      { role: 'user', content: '1', messageId: 'm1' },
      { role: 'assistant', content: '2', messageId: 'm2' },
      { role: 'user', content: '3', messageId: 'm3' },
    ];
    expect(sliceMessagesBefore(msgs, 'm3').map(m => m.messageId)).toEqual(['m1', 'm2']);
  });
  it('目标是首条 → 空数组(撤回到空)', () => {
    expect(sliceMessagesBefore([{ role: 'user', messageId: 'm1' }], 'm1')).toEqual([]);
  });
  it('目标不存在 → null(调用方禁用入口)', () => {
    expect(sliceMessagesBefore([{ role: 'user', messageId: 'm1' }], 'nope')).toBeNull();
  });
});

describe('canRecall', () => {
  it('agentcore + 有 messageId + 有 role=user 才可撤回', () => {
    expect(canRecall({ provider: 'agentcore', role: 'user', messageId: 'x' })).toBe(true);
    expect(canRecall({ provider: 'claudecode', role: 'user', messageId: 'x' })).toBe(false);
    expect(canRecall({ provider: 'agentcore', role: 'user' })).toBe(false);
    expect(canRecall({ provider: 'agentcore', role: 'assistant', messageId: 'x' })).toBe(false);
    expect(canRecall(null)).toBe(false);
  });
});

describe('recallCreatePayload', () => {
  it('基于常规载荷但绝不携带 transcript,且带 truncate_before_message_id', () => {
    const conv = {
      aiSessionId: 'sid', projectPath: '/p', provider: 'agentcore',
      model: 'glm-5.3', effort: 'high',
      deviceSession: { id: 'sid' }, deviceTranscript: [{ role: 'user', content: 'x' }],
    };
    const p = recallCreatePayload(conv, 'm9');
    expect(p.truncate_before_message_id).toBe('m9');
    expect(p.transcript).toBeUndefined();          // 撤回路径绝不喂 transcript
    expect(p.resume_session_id).toBeUndefined();   // resume 仅首次,撤回不带
    expect(p.effort).toBe('high');
    expect(p.session_id).toBe('sid');
  });
});
```

`chatSettings.test.js` 追加:

```js
describe('sessionCreatePayload 回归(truncate 键不常驻)', () => {
  it('常规载荷不含 truncate_before_message_id 键', () => {
    const p = sessionCreatePayload({ aiSessionId: 's', provider: 'agentcore' });
    expect('truncate_before_message_id' in p).toBe(false);
  });
});
```

- [ ] **Step 2: 运行确认失败** — `npx vitest run src/utils/chatTimeline.test.js src/utils/chatRecall.test.js src/utils/chatSettings.test.js` → FAIL。

- [ ] **Step 3: 实现**

`chatTimeline.js`:

```js
// 消息流归并(spec §C):messages(权威流)与 steerItems(瞬态插话)按 ts
// 升序合并为统一时间线,打 type 标供渲染层分流。纯函数,脏输入安全。
export function mergeTimeline(messages, steerItems) {
  const a = Array.isArray(messages) ? messages.map(m => ({ type: 'message', ...m })) : [];
  const b = Array.isArray(steerItems) ? steerItems.map(s => ({ type: 'steer', ...s })) : [];
  return [...a, ...b]
    .map((item, idx) => ({ ...item, _idx: idx }))
    .sort((x, y) => (Number.isFinite(x.ts) ? x.ts : Infinity) - (Number.isFinite(y.ts) ? y.ts : Infinity))
    .map(({ _idx, ...rest }) => rest);
}
```

`chatRecall.js`:

```js
// 撤回重发纯逻辑(spec §A):切片、入口判定、撤回 create 载荷。
// 铁律:撤回帧绝不携带 transcript(宿主从 journal 全文重建),也绝不携带
// resume_session_id(resume 仅首次 create 的既有契约)。
import { sessionCreatePayload } from './chatSettings';

// 返回目标消息之前的切片;目标不存在返回 null(禁用撤回入口)。
export function sliceMessagesBefore(messages, messageId) {
  if (!Array.isArray(messages) || typeof messageId !== 'string' || !messageId) return null;
  const idx = messages.findIndex(m => m && m.messageId === messageId);
  if (idx < 0) return null;
  return messages.slice(0, idx);
}

// 撤回入口判定(spec §A provider 门控):仅 agentcore 会话的 user 消息,
// 且该消息有 messageId(存量旧消息无 id 只能复制)。
export function canRecall(msg) {
  return !!msg
    && msg.role === 'user'
    && typeof msg.messageId === 'string' && !!msg.messageId
    && msg.provider === 'agentcore';
}

// 撤回 create 载荷:常规载荷打底,删 transcript/resume,加 truncate 键。
export function recallCreatePayload(conv, beforeMessageId) {
  const payload = sessionCreatePayload(conv);
  delete payload.transcript;
  delete payload.resume_session_id;
  payload.truncate_before_message_id = String(beforeMessageId);
  return payload;
}
```

注:导出面保持最小(切片/判定/载荷三件),不预留未用导出(YAGNI)。

- [ ] **Step 4: 运行确认通过** — 同 Step 2 命令 → PASS。
- [ ] **Step 5: 提交**

```bash
git add src/utils/chatTimeline.js src/utils/chatRecall.js src/utils/chatTimeline.test.js src/utils/chatRecall.test.js src/utils/chatSettings.test.js
git commit -m "新增:聊天时间线归并与撤回切片纯逻辑(撤回帧绝不携带 transcript)"
```

---

### Task 6: 前端 — 组件化拆分(纯移动,零行为变化)

**Files:**
- Create: `app/website/src/components/chat/ChatSidebar.vue` / `ChatHeader.vue` / `ChatSettingsBar.vue` / `ChatMessageList.vue` / `ChatMessageBubble.vue` / `ChatApprovalCard.vue` / `ChatComposer.vue`
- Create: `app/website/src/composables/useAliangChat.js`
- Modify: `app/website/src/components/ChatPage.vue`(缩为编排壳)

**拆分规则(零行为变化是验收标准):**

| 新文件 | 迁移源(ChatPage.vue 现行号) | 接口约定 |
|---|---|---|
| ChatSidebar.vue | 模板 :3-90 + 分组/重命名相关 computed 与方法 | props: `groups, mergedCount, activeId, showRename(id), renameDraft, renamingId, renameSurface, deviceAvailable`; emits: `item-click, create, rename-start, rename-commit, rename-cancel, rename-draft`。内部保留 13 testid 中的 session-source-badge / device-session-running-dot |
| ChatHeader.vue | :94-150 + header 重命名 | props: `conversation, activeIsDevice, activeRunning, renaming`; emits: `rename-start, rename-commit, rename-cancel, delete, close, open-sidebar` |
| ChatSettingsBar.vue | :152-251 + 设置条全部状态/方法(:510-517, :710-843) | props: `conversation, activeRunning, createError`; emits: `apply-settings(patch), resend-create`。**锁定态目录必须是 span(testid 双形态 160/170 都保)** |
| ChatMessageList.vue | :253-405 + scrollToBottom/activeSteerItems/approvals 渲染 | props: `conversation, activeId, activeMessages, steerItems, approvals, nowTick`; emits: `approval-respond, scroll-state`。本任务**保持纯文本气泡渲染**(markdown 是 Task 7);steer 仍渲染为流外轻量条(融合是 Task 11) |
| ChatMessageBubble.vue | :270-304 单消息块 | props: `msg, nowTick`; emits: 无(操作入口 Task 13 加) |
| ChatApprovalCard.vue | :326-403 + approval 渲染辅助(:1585-1628) | props: `approval, nowTick`; emits: `respond(decision)`。approval-expires testid 原样保留 |
| ChatComposer.vue | :407-460 + slash 状态/方法(:871-884, :1356-1438) + steerBlocked/sendDisabled/sendButtonLabel/inputPlaceholder | props: `modelValue, disabled, label, placeholder, steerBlocked, slashOpen, slashMatches, slashIndex`; emits: `update:modelValue, send, slash-apply, slash-index, slash-close`。**input 保留单行形态与全部事件绑定时序**(textarea 是 Task 8) |
| useAliangChat.js | 脚本其余全部:WS 生命周期、watchdog、事件路由 handleAIEvent、approve/steer/create 确认门、三源清单合并、持久化 | 导出 `useAliangChat()` 返回 ChatPage 现用的一切 ref/computed/方法;**逻辑零改动,只搬家**。`__CHAT_IDLE_TIMEOUT_MS__` 的 globalThis 读取保持在模块顶层(ChatPage.vue:522 行为等价迁移) |

- [ ] **Step 1: 按 `useAliangChat.js → ChatSidebar → ChatHeader → ChatSettingsBar → ChatMessageBubble → ChatMessageList → ChatApprovalCard → ChatComposer → ChatPage 壳` 顺序逐个抽取**(每抽一个跑一次单测,全部抽完跑 e2e)
- [ ] **Step 2: 全量验证**

```bash
npx vitest run
npx playwright test tests/e2e/chat-settings.spec.js tests/e2e/chat-unify.spec.js tests/e2e/chat-device-sessions.spec.js tests/e2e/mobile-responsive.spec.js
```

Expected: 全绿(任何红 = 拆分引入了行为变化,回退修复)。
- [ ] **Step 3: 行数 sanity** — `wc -l src/components/ChatPage.vue src/components/chat/*.vue src/composables/useAliangChat.js`:ChatPage 壳 <300 行。
- [ ] **Step 4: 提交**

```bash
git add -A src/
git commit -m "新增:聊天页组件化拆分(chat/ 组件树+useAliangChat,纯移动零行为变化)"
```

---

### Task 7: 前端 — ChatMarkdown 接入(assistant 气泡)

**Files:**
- Create: `app/website/src/components/chat/ChatMarkdown.vue`
- Modify: `app/website/src/components/chat/ChatMessageBubble.vue`
- Test: `app/website/src/components/chat/ChatMarkdown.test.js`

- [ ] **Step 1: 写失败测试**(`ChatMarkdown.test.js`;**仓内无 @vue/test-utils**,按全局约束 10 用 `createApp` + `document.body` 挂载断言 innerHTML)

```js
import { describe, it, expect } from 'vitest';
import { createApp, h } from 'vue';
import ChatMarkdown from './ChatMarkdown.vue';

function mountHtml(content) {
  const el = document.createElement('div');
  createApp({ render: () => h(ChatMarkdown, { content }) }).mount(el);
  return el.innerHTML;
}

describe('ChatMarkdown', () => {
  it('渲染 strong 与代码语言类名', () => {
    const html = mountHtml('**bold**\n\n```go\nx := 1\n```');
    expect(html).toContain('<strong>');
    expect(html).toContain('language-go');
  });
  it('脚本注入被消毒', () => {
    expect(mountHtml('<script>alert(1)</script>ok')).not.toContain('<script');
  });
  it('代码块带复制按钮与语言标签', () => {
    const html = mountHtml('```js\nconst a=1;\n```');
    expect(html).toContain('chat-md-copy');
    expect(html).toContain('lang-label');
  });
  it('>20 行代码块折叠(折叠类与展开按钮存在)', () => {
    const long = '```txt\n' + Array.from({ length: 25 }, (_, i) => 'l' + i).join('\n') + '\n```';
    const html = mountHtml(long);
    expect(html).toContain('chat-md-fold');
  });
});
```

- [ ] **Step 2: 实现**

```vue
<template>
  <div class="chat-md text-sm leading-relaxed break-words" v-html="html"></div>
</template>
<script setup>
import { ref, watch, onUnmounted } from 'vue';
import { renderMarkdownSafe } from '../../utils/chatMarkdown';

const props = defineProps({ content: { type: String, default: '' } });
const html = ref(renderMarkdownSafe(props.content));
let timer = null;
// 流式容忍(spec §E):delta 高频追加,80ms 防抖重渲染;未闭合围栏由
// markdown-it 天然按代码块处理,半截内容不闪错。
watch(() => props.content, (v) => {
  if (timer) clearTimeout(timer);
  timer = setTimeout(() => { html.value = renderMarkdownSafe(v); }, 80);
});
onUnmounted(() => timer && clearTimeout(timer));
</script>
```

样式(组件内 scoped 或 chat 专用样式层):`.chat-md pre` 包装条(语言标签 class `lang-label` + 复制按钮 class `chat-md-copy`,复制按钮用事件委托读 `pre code` 文本,`navigator.clipboard.writeText`),代码块默认 `max-h-96 overflow-auto`,>20 行加折叠类 `chat-md-fold`(折叠态 `max-h-24`)+ 展开按钮。

- [ ] **Step 3: 接入 ChatMessageBubble** — assistant 分支气泡内容 `{{ msg.content }}` 替换为 `<ChatMarkdown :content="msg.content" />`(status==='running' 且 content 空时仍走 thinking 三点);**user 气泡保持纯文本**(spec §E 防注入);error 气泡保持纯文本。
- [ ] **Step 4: 验证** — vitest 全绿 + `npx playwright test tests/e2e/chat-device-sessions.spec.js`(转录渲染路径不破)。
- [ ] **Step 5: 提交** — `git commit -m "新增:AI 气泡 markdown 渲染(流式防抖+代码块复制/折叠,用户气泡保持纯文本)"`

---

### Task 8: 前端 — 视觉现代化(品牌绿)

**Files:** Modify `chat/` 全部组件 + `ChatPage.vue`;不动全局 tailwind token。

按块实现(每块独立小提交或合并为一个提交,类串如下):

- [ ] **Step 1: 布局与气泡** — 消息列 `max-w-3xl mx-auto w-full`;user 气泡 `rounded-2xl bg-primary text-white px-4 py-2.5 text-sm shadow-sm ml-auto`(右对齐);assistant 气泡容器 `flex gap-2.5` + 24px 头像 `size-6 shrink-0 rounded-full bg-gradient-to-br from-primary to-emerald-500 text-white flex items-center justify-center`(内嵌 `auto_awesome` 12px 图标)+ 卡片 `rounded-2xl border border-slate-200 bg-white px-4 py-2.5 text-sm shadow-sm dark:border-slate-700 dark:bg-slate-800`(左对齐);eyebrow 行 `mb-1 flex items-center gap-2 text-[11px] text-slate-400`(角色名 + 相对时间 + 状态徽);系统条改居中弱化胶囊 `mx-auto w-fit rounded-full border border-slate-200 bg-white/80 px-3 py-1 text-xs text-slate-500 dark:...`。
- [ ] **Step 2: 流式与滚动** — running 气泡尾部 `<span class="chat-caret">` 闪烁光标(scoped keyframes,`prefers-reduced-motion` 降级);**替换无条件 scrollToBottom**(函数实体随状态进 `useAliangChat`,ChatMessageList 只持有滚动容器 ref 并 emit `scroll-state`):滚动容器 `@scroll` 记录 `nearBottom = scrollHeight - scrollTop - clientHeight < 120`,delta 到达时仅 `nearBottom` 才自动滚;上翻出现右下浮动按钮 `absolute bottom-24 right-6 rounded-full bg-primary text-white shadow-lg size-10`(带未读计数,点击回底;**z 序 ≤999**,全局约束 8)。
- [ ] **Step 3: 输入区** — `<textarea rows="1">` 自动增高(`input` 时 `style.height='auto'; style.height=Math.min(scrollHeight,144)+'px'`),容器 `rounded-2xl border ... focus-within:border-primary`,Enter 发送/Shift+Enter 换行(IME `isComposingEvent` 判定沿用),running 时停止键占位(Task 9 接帧,本任务先占 UI 位);`__CHAT_IDLE_TIMEOUT_MS__` 不动。
- [ ] **Step 4: 侧栏/空状态/设置条/审批卡** — 侧栏条目 `mx-2 rounded-lg px-3 py-2`(active 加 `before:` 品牌左竖条 + `bg-primary/10 text-primary`);空状态品牌 hero(渐变圆 + 标题 + 副文案);设置条控件视觉统一(圆角/边框/focus 环),**testid 与标签形态不变**;**ChatApprovalCard 重设计(spec §D,语义零变化)**:盾形徽章(warning 色调)+ 状态 chip(pending=warning/responding)+ expires 倒计时徽标保留(`approval-expires` testid 不变);summary 默认 4 行点按展开;command/`approvalDetailText` mono 块样式升级;四决策按钮视觉统一(accept=emerald 实底/accept_for_session=emerald 软底/decline=amber 外框/cancel=slate 外框,`availableDecisions` 门控逻辑不动)。
- [ ] **Step 5: 深色/移动验证** — `npx playwright test tests/e2e/mobile-responsive.spec.js` 全绿;手测 dark class 下各面(`document.documentElement.classList.add('dark')`)。
- [ ] **Step 6: 提交** — `git commit -m "新增:聊天页品牌绿视觉现代化(气泡/头像/滚动跟随/textarea 输入/侧栏/空状态)"`

---

### Task 9: 停止按钮(ai.stop)

**Files:** Modify `useAliangChat.js` + `ChatComposer.vue` + `ChatPage.vue`;Test: e2e `tests/e2e/chat-stop.spec.js`(新)

- [ ] **Step 1: e2e(失败先行)** — FakeWebSocket 场景:发送消息进入 running → 断言出现停止键(`data-testid="chat-stop-button"`)→ 点击 → `window.__aiFrames` 含 `{type:'ai.stop', session_id}`。
- [ ] **Step 2: 实现** — `stopRun()`:目标 id 复用 `steerTargetId`(本地会话 aiSessionId / 设备壳 managed ?? entry.id 同构);`sendAI({ type:'ai.stop', session_id: target })`;Composer 的 running 态发送键位切换停止键(图标 `stop_circle` + i18n `chat_stop`)。**stop 帧不是终态**(spec §2.1):点击后仅置视觉 stopping 态,终态由 done/error 帧既有路径收尾。
- [ ] **Step 3: e2e 绿 + 提交** — `git commit -m "新增:聊天页运行中停止按钮(接 ai.stop,终态由既有收敛路径负责)"`

---

### Task 10: 审批等待期硬拦

**Files:** Modify `useAliangChat.js` + `ChatComposer.vue`;Test: e2e `tests/e2e/chat-approval-block.spec.js`(新)

- [ ] **Step 1: e2e(失败先行)** — 注入 `ai.approval.request` 帧 → 断言输入 disabled + `data-testid="chat-approval-block-hint"` 可见 → 响应审批后输入恢复。
- [ ] **Step 2: 实现** — `sendDisabled` 追加条件 `|| activeApprovals.length > 0`;提示条文案 i18n `chat_approvalBlockHint`(zh:「有待审批请求,请先处理后再发送」)。仅拦活跃会话自身审批(壳路由到他卡不受影响,与 activeApprovals 现有口径一致)。
- [ ] **Step 3: e2e 绿 + 提交** — `git commit -m "新增:审批等待期硬拦发送(对齐手机端防顶翻会话)"`

---

### Task 11: steer 融入消息流

**Files:** Modify `ChatMessageList.vue`(改用 `mergeTimeline`)+ 相关 props;Test: `chatTimeline.test.js` 已有 + e2e 追加

- [ ] **Step 1: e2e(失败先行)** — 注入 running 状态 → 提交 steer → 断言 `chat-steer-item` 出现在消息流内(user 气泡位)且带 `chat-steer-status` 六态文案;ack `applied` 后 chip 更新;重载后消失(瞬态)。
- [ ] **Step 2: 实现** — 时间线渲染改 `mergeTimeline(activeMessages, activeSteerItems)`;steer 项渲染为右对齐气泡 `rounded-2xl border border-primary/40 bg-primary/5 px-3 py-2 text-xs` + ⚡徽 + 状态 chip(六态映射沿用 `steerStatusLabel`);**chat-steer-item/chat-steer-status testid 保留**;steerBlocked 提示条保留。
- [ ] **Step 3: e2e 绿 + 提交** — `git commit -m "新增:steer 插话融入消息时间线(user 气泡形态+状态徽,对齐手机端)"`

---

### Task 12: effort「下轮生效」chip

**Files:** Modify `ChatSettingsBar.vue` + `useAliangChat.js`;Test: e2e 追加 `tests/e2e/chat-settings.spec.js`

- [ ] **Step 1: e2e(失败先行)** — 改 effort select → `data-testid="chat-effort-pending"` 出现(「下轮生效」)→ 注入 `ai.run.started`(同 session)→ 徽标消失。
- [ ] **Step 2: 实现** — `applySessionSettings` 里 model/effort 变更置 `conv.pendingConfigKeys`(Set);`ai.run.started` 路由到会话时清空;chip 渲染 `rounded bg-amber-100 text-amber-700 text-[10px] px-1`(zh:「下轮生效」)。
- [ ] **Step 3: e2e 绿 + 提交** — `git commit -m "新增:effort/模型改动「下轮生效」角标(ai.run.started 后清除)"`

---

### Task 13: 撤回重发全链

**Files:** Modify `ChatMessageBubble.vue`(hover 菜单)/ `ChatComposer.vue`(编辑态)/ `useAliangChat.js`(状态机+发送链)/ 新 `ChatRecallBanner.vue`;Test: e2e `tests/e2e/chat-recall.spec.js`(新)

- [ ] **Step 1: e2e(失败先行)** — 核心帧序断言(`window.__aiFrames`):

```js
// 场景 A(会话空闲):两条历史消息(本地会话,user 消息带 messageId)→ hover
// 第二条 user 气泡 → 点 data-testid="chat-recall-button" → 断言
// chat-recall-banner 可见且输入框被回填原文 → 修改文本 → 发送 →
// expect(frames.filter(f=>f.type==='ai.session.create').at(-1))
//   .toMatchObject({ truncate_before_message_id: 'm2' });
// expect(最后一帧 create.transcript).toBeUndefined();          // 铁律
// expect(frames.filter(f=>f.type==='ai.message').at(-1).message_id)
//   .not.toBe('m2');                                            // 新 id
// 场景 B(running 会话):先断言 ai.stop 帧先于 truncate create 帧
// 场景 C(banner 取消):无任何 create 帧新增
// 场景 D(claudecode 会话):hover 菜单无撤回入口
//   〔执行留痕 2026-11-01〕claudecode 本地会话在本窗口造不出来(新建/恢复/
//   清单三入口全锁 agentcore),场景 D 改测「存量无 messageId 消息」同一
//   canRecall 分支(失败=复制在/撤回无);provider 维度由 chatRecall.test.js
//   单测钉死。另:设备壳会话撤回整体禁用(spec §6 壳 journal 分叉),新增
//   场景 F 钉壳禁用分支。
// 场景 E(stop 超时中止):running 会话发起撤回并确认,FakeWebSocket 对
// ai.stop 不回任何终态帧(短超时经 window.__CHAT_IDLE_TIMEOUT_MS__ 同款
// app 前注入机制提供,如 __CHAT_STOP_TIMEOUT_MS__)→ 断言 __aiFrames 只有
// ai.stop、无 truncate create 帧;chat-recall-banner 与输入框回填保留
// (编辑态不丢),可再次确认重试
```

- [ ] **Step 2: 实现**

1. **messageId 写入**:`sendMessage` 非 steer 分支,`const messageId = generateId();` 提前,user 气泡 `{ role:'user', content:text, messageId, provider: conv.provider, ts: now() }`(ai.message 帧用同一 id)。
2. **hover 菜单**:气泡容器 `group relative`,`opacity-0 group-hover:opacity-100` 操作条 = 复制(`navigator.clipboard`)+ 撤回(`canRecall(msg)` 为真才渲染;`data-testid="chat-recall-button"`)。
3. **编辑态状态机**(`useAliangChat`):`recallState = ref(null)` → `{ convId, targetMessageId, originalText }`;进入:截断预览文案(i18n `chat_recallBanner`)+ 输入回填;退出:恢复草稿原文;切换:重置;slash 补全编辑态禁用(Composer prop `recallActive` → `onSlashInput` 直接 return)。
4. **发送链**(`sendRecall`):
   ```
   running → sendAI(ai.stop) → await 终态(done/error 帧 hook;10s 超时 → 中止:
   保留编辑态 + createError=t('chat_recallStopTimeout'),return)
   → waitForCreateAck(conv, 5000, () => recallCreatePayload(conv, targetMessageId))
     (走既有确认门)
   → ack created → conv.messages = sliceMessagesBefore(conv.messages, targetMessageId)
     + push 新 user 气泡(编辑后文本,新 messageId) → push assistant 占位
   → sendAI(ai.message) — 与 sendMessage 尾段完全同构(看门狗/异常分支复用)
   ```
   ⚠️ **waitForCreateAck 签名改动**(评审 Issue 5):现状它内部**自己**发 `sendAI(sessionCreatePayload(conv))`(ChatPage.vue:866-870),不接受外部载荷——须在 useAliangChat.js 给它加可选第三参 `payloadFn = () => sessionCreatePayload(conv)`,常规调用方零变化,sendRecall 传入撤回载荷构造器。
   create 超时/被拒 → 不发 ai.message,编辑态保留 + 提示(i18n `chat_recallUnconfirmed`),重试安全(truncate 幂等)。
5. **设备壳 id 接线**(评审 Issue 4,前提补齐):`applyDeviceTranscript`(ChatPage.vue:1116-1126)现状只映射 `{role, content, status, ts}`、**丢弃 id**——补 `messageId: m.id || ''`(wire 字段是 `id`,`models/agent.go:229`;id 来源链 = `agent_inventory_agentcore.go:148` `ID: row.MessageID` 即 journal message_id 本尊,锚点语义成立)。`applyDeviceSnapshotTranscript` 同样补 `messageId: m.message_id || ''`(快照行字段名 `message_id`;快照会整体覆盖展示流,不补 id 则快照刷新后撤回入口消失。与修订后 spec §A 口径一致:快照 id 系 journal 同源,锚点透传;快照内容仍不作喂入——撤回路径根本没有 transcript 喂入)。
6. **provider 门控**:`canRecall` 已含 provider 判定(仅 agentcore)。
- [ ] **Step 3: e2e 绿 + 提交** — `git commit -m "新增:聊天消息撤回重发全链(stop→journal 截断 create→新消息,转录级真撤回)"`

---

### Task 14: 可交互 option 审批卡

**Files:** Create `chat/ChatOptionCard.vue`;Modify `useAliangChat.js`(帧处理)/ `ChatMarkdown.vue`(静态卡)/ i18n;Test: e2e `tests/e2e/chat-option.spec.js`(新)

- [ ] **Step 1: 测试(失败先行)**

e2e(`tests/e2e/chat-option.spec.js`):
- 交互卡:注入 `ai.option.request` 帧(options 2 项,multi=false,allow_custom=true)→ 断言 `data-testid="chat-option-card"` 渲染两项 → 点选第 1 项(仅高亮,未提交)→ 点 `data-testid="chat-option-submit"` → `__aiFrames` 含 `{type:'ai.option.response', option_id, selected:['a'], custom_text:''}`;注入 `ai.option.cancelled` → 卡消失(带系统条)。
- **静态卡防双份**:注入含 ` ```aliang-options ` 围栏的 assistant 消息 → `data-testid="chat-option-static"` 渲染(折叠卡);随后同 `option_id` 的 `ai.option.request` 到达 → 静态卡隐藏(交互卡在场)。

vitest(`ChatOptionCard.test.js`,createApp 手法,全局约束 10):选中态切换(单选互斥/多选可叠加)、无选中时提交禁用、allow_custom 文本进入 custom_text、提交后折叠为「已选择:…」终态、失败(props error)恢复可选。
- [ ] **Step 2: 实现**

1. `useAliangChat`:`handleAIEvent` 增加 `ai.option.request` case(会话归属判定与审批族同构:aiSessionId 命中 → 设备壳三键 → 活跃兜底带来源标),`conv.optionCards` 数组 upsert(key=`session_id:option_id`);`ai.option.cancelled` → 移除 + 系统条;`ai.option.response` 走裸 sendAI(与审批卡同构,不进确认门)。
2. `ChatOptionCard.vue`:标题/header/summary + 选项行(`multi ? checkbox : radio` 语义,点行仅本地选中)+ previews `<details>` 折叠 + allow_custom 文本输入 + 底部确认按钮(无选中禁用;提交后卡片折叠为「已选择:…」终态,失败恢复);**两步制作答**。
3. `ChatMarkdown`:内容含 `aliang-options` 围栏 → `extractAliangOptions` 得块,渲染为静态只读卡(实施修正:交互卡为主路径,静态卡不做折叠交互;原「静态折叠卡」措辞作废)(`data-testid="chat-option-static"`);Bubble 层比对 `hiddenOptionIds`(该会话交互卡的 option_id 集合)→ 命中则静态卡隐藏(防双份;正文 strip 无条件先行——Task 4/7 评审钉死的最终流)。
- [ ] **Step 3: e2e 绿 + 提交** — `git commit -m "新增:结构化方案交互审批卡(ai.option 两步作答+静态围栏卡防双份)"`

---

### Task 15: i18n 审计 + 全量回归 + 构建收口

**Files:** Modify `src/i18n/zh.js` / `en.js`

- [ ] **Step 1: i18n 查漏审计** — Task 8-14 已按全局约束 9 各自成对补键;本步全量 grep 模板中 `t('chat_` 引用与两语言键集比对,查漏补缺(至少应含:`chat_stop, chat_approvalBlockHint, chat_recallBanner, chat_recallButton, chat_recallConfirm, chat_recallCancel, chat_recallStopTimeout, chat_recallUnconfirmed, chat_effortPending, chat_optionTitle, chat_optionSubmit, chat_optionSelected, chat_optionCustom, chat_copyMessage, chat_copied, chat_scrollToBottom`),`npx vitest run src/i18n/i18n.test.js` 绿。
- [ ] **Step 2: 全量回归**

```bash
npx vitest run
npx playwright test
cd ../.. && go build ./... && go vet ./app/http/services/ ./app/http/models/
```

Expected: 全绿(基线 flake 按 §全局约束 5 处理)。
- [ ] **Step 3: 生产构建** — `cd app/website && npm run build` 无错;`ls dist/assets` 确认 chunk 产出。
- [ ] **Step 4: 提交收口**

```bash
git add -A && git commit -m "$(cat <<'EOF'
新增:聊天页重构收口(i18n 成对审计+全量回归绿+构建验证)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 5: 手工冒烟清单(交回用户前)** — 新建会话发消息(流式/markdown/代码块复制)/撤回重发(含 running 场景)/停止按钮/effort 改档+下轮生效徽/steer 插话六态/审批硬拦+四决策/option 卡两步作答/深色切换/390px 移动宽度/重载持久化。

---

## 依赖关系

```
Task 0 → 1 → 2            (Go 底座)
Task 0 → 3 → 4 → 5        (前端纯逻辑底座)
Task 0 → 6                (组件化拆分,依赖 0;与 1-5 无耦合可并行)
Task 7..14 顺序执行        (全部依赖 6;7 另依赖 4;11 依赖 5;13 依赖 2+5)
Task 15 收口               (依赖全部)
```
