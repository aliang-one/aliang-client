# AliangCode（agentcore）Journal 落盘 实现计划

> **⚠️ 已被实现取代（2026-10-26）**：本计划已由并行会话在
> `.claude/worktrees/agentcore-journal`（分支 `feature/agentcore-journal`，7 个
> commit）按同一 spec 独立实现并全包测试通过。实现与本计划在 API 命名
> （`agentCoreJournal` 结构体 vs 包级函数）与无头行语义（容忍 vs 跳过，spec 已
> 依实现修正）上有意分歧，均为实质等价或更优。验收结论与遗留项见
> `docs/superpowers/reviews/2026-10-26-agentcore-journal-verification.md`。
> 本文件保留作设计过程存档，**勿按此执行**。

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** agentcore 会话逐行落盘到 `<agentHome()>/.aliang/ai-sessions/<sessionID>.jsonl`，回合结束后永久留在仪表盘 AI 会话历史并可回放。

**Architecture:** 写入侧在 `runAgentCore` 四个时机挂点（入口头行+user 行 / steer 插话 user 行 / 成功 assistant 行 / 终态 defer end 行），全部尽力而为（写失败只 Warn 绝不影响 run）；扫描侧以第三路并入 `collectAgentVibeSessions`，回放经 `findAgentVibeSessionDetail` 的 `.aliang` 分支分发。与 claude/codex 通道同构（同款 recency-cap walker + (size,mtime) 解析缓存 + transcript 窗口）。

**Tech Stack:** Go（package services 内新增两文件、修改四文件）；测试用标准 `testing` + `t.Setenv("HOME", t.TempDir())` 伪造 agentHome + 既有 `agentCoreUpstream` httptest 基建。

**Spec:** `docs/superpowers/specs/2026-10-26-agentcore-journal-design.md`（已评审通过）

---

## 实施纪律（开工前必读）

1. **worktree**：按仓库规约一律在 git worktree 分支上实施，不直接改 master 工作区
   （`git stash -u` → `git worktree add` → `git stash pop`）。⚠️ 新 worktree 无
   `app/website/dist`，`go build ./app/...` 会因 `//go:embed website/dist`（app/embed.go）
   无匹配文件而编译失败——首次构建前先从主仓拷贝：`cp -R <主仓>/app/website/dist <worktree>/app/website/dist`
   （仓内已知坑；只跑 `go test ./app/http/services/` 不受影响）。
2. **提交**：CLAUDE.md 规定提交须用户明示同意。本计划的 commit 步骤在用户批准
   执行计划时视为一并授权；标题用全中文「新增：/修复：」，正文带
   `Co-Authored-By: Claude Code <noreply@anthropic.com>` 结尾。
3. **测试环境**：所有触盘测试第一行 `t.Setenv("HOME", t.TempDir())`（agentHome()
   对非 root 进程解析 HOME，与仓内既有 vibe 测试同口径）。
4. **跨平台测试命名**：测试名不得以 GOOS 后缀结尾（`_windows_test.go` 在 darwin
   静默跳过的仓库前科），一律普通命名。
5. 本计划锁死的两个设计决策（spec 评审 advisory）：
   - journal 锁粒度 = **包级单锁**（`agentCoreJournalStore.mu`，写行/去重/prune 共享）；
   - 实例生命周期 = **无每会话实例**：包级 store + `map[sessionID]去重集合`，
     文件句柄不常驻（每行 open-append-close）。

## 文件结构

| 文件 | 动作 | 职责 |
|---|---|---|
| `app/http/services/agent_ai_agentcore_journal.go` | 新增 | journal 写入器：行格式、头行、去重、prune、包级 API |
| `app/http/services/agent_ai_agentcore_journal_test.go` | 新增 | 写入器单测 + run 级落盘测试（复用 `agentCoreUpstream` 基建） |
| `app/http/services/agent_ai_agentcore.go` | 修改 | runAgentCore 三挂点 + `lastAgentAIUserJournalMessage` |
| `app/http/services/agent_ai.go` | 修改 | steer core 分支挂点（1 处） |
| `app/http/services/agent_inventory_agentcore.go` | 新增 | 收集器 + 摘要缓存 + 回放 reader |
| `app/http/services/agent_inventory_agentcore_test.go` | 新增 | reader/收集器/detail 端到端测试 |
| `app/http/services/agent_inventory.go` | 修改 | `collectAgentVibeSessions` 加第三路（1 行） |
| `app/http/services/agent_detail.go` | 修改 | 候选/兜底两枚举加 `.aliang` 根 + 分发加 `.aliang` 分支（3 处） |

---

### Task 1: journal 写入器

**Files:**
- Create: `app/http/services/agent_ai_agentcore_journal.go`
- Test: `app/http/services/agent_ai_agentcore_journal_test.go`

- [ ] **Step 1: 写失败测试（写入器行为全集）**

新建 `app/http/services/agent_ai_agentcore_journal_test.go`：

```go
package services

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readJournalLines 读回 journal 全部非空行（解析成通用 map 断言用）。
func readJournalLines(t *testing.T, sessionID string) []map[string]interface{} {
	t.Helper()
	path := agentCoreJournalFilePath(sessionID)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("journal 文件不存在: %v", err)
	}
	defer f.Close()
	var out []map[string]interface{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row map[string]interface{}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue // 半行容忍：非法行跳过不计数（与 reader 同语义）
		}
		out = append(out, row)
	}
	return out
}

func TestAgentCoreJournalHeaderOnceAndAppendOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	agentCoreJournalStart("conv-j1", "/tmp/proj-a", "glm-5.2", "m1", "你好")
	agentCoreJournalAssistant("conv-j1", "am1", "回复")
	agentCoreJournalEnd("conv-j1", "success", "")
	// 同 ID 第二回合：头行不得重写（projectPath 变化以先写下的头为准）。
	agentCoreJournalStart("conv-j1", "/tmp/proj-b", "glm-5.3", "m2", "再来")

	rows := readJournalLines(t, "conv-j1")
	if len(rows) != 5 {
		t.Fatalf("行数 = %d, want 5（头+user+assistant+end+user）: %v", len(rows), rows)
	}
	if rows[0]["kind"] != "session" || rows[0]["project_path"] != "/tmp/proj-a" || rows[0]["model"] != "glm-5.2" {
		t.Fatalf("头行错误或被重写: %v", rows[0])
	}
	if rows[1]["kind"] != "msg" || rows[1]["role"] != "user" || rows[1]["message_id"] != "m1" || rows[1]["text"] != "你好" {
		t.Fatalf("user 行错误: %v", rows[1])
	}
	if rows[2]["role"] != "assistant" || rows[2]["text"] != "回复" {
		t.Fatalf("assistant 行错误: %v", rows[2])
	}
	if rows[3]["kind"] != "end" || rows[3]["status"] != "success" {
		t.Fatalf("end 行错误: %v", rows[3])
	}
	if rows[4]["message_id"] != "m2" {
		t.Fatalf("第二回合 user 行错误: %v", rows[4])
	}
}

func TestAgentCoreJournalDedupsByMessageID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	agentCoreJournalStart("conv-dedup", "/tmp/p", "glm-5.2", "same-id", "第一条")
	agentCoreJournalUser("conv-dedup", "same-id", "重复消息")
	rows := readJournalLines(t, "conv-dedup")
	if len(rows) != 2 { // 头 + 1 条 user（同 messageID 的第二条被去重）
		t.Fatalf("行数 = %d, want 2（去重失败）: %v", len(rows), rows)
	}
}

func TestAgentCoreJournalToleratesPartialLine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	agentCoreJournalStart("conv-tol", "/tmp/p", "glm-5.2", "m1", "ok")
	// 模拟崩溃残行：手工追加半行 JSON。
	path := agentCoreJournalFilePath("conv-tol")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"kind":"msg","role":"user","text":"截断的`)
	f.Close()
	agentCoreJournalUser("conv-tol", "m2", "残行之后")
	rows := readJournalLines(t, "conv-tol")
	if len(rows) != 3 {
		t.Fatalf("行数 = %d, want 3（残行被跳过后继续可读）: %v", len(rows), rows)
	}
}

func TestAgentCoreJournalSanitizesFilename(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	hostile := "../../evil\x01id"
	agentCoreJournalStart(hostile, "/tmp/p", "glm-5.2", "m1", "x")
	path := agentCoreJournalFilePath(hostile)
	dir := agentCoreJournalDir()
	if filepath.Dir(path) != dir || !strings.HasSuffix(path, ".jsonl") {
		t.Fatalf("路径逃逸: %s", path)
	}
	if strings.ContainsAny(filepath.Base(path), "/\\:") {
		t.Fatalf("文件名含非法字符: %s", filepath.Base(path))
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("落盘失败: %v", err)
	}
}

func TestAgentCoreJournalPrunesToMaxFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for i := 0; i < agentCoreJournalMaxFiles+5; i++ {
		agentCoreJournalStart("conv-prune-"+string(rune('a'+i%26))+string(rune('0'+i%10))+fmt.Sprint(i), "/tmp/p", "glm-5.2", "m", "x")
	}
	entries, err := os.ReadDir(agentCoreJournalDir())
	if err != nil {
		t.Fatalf("读目录失败: %v", err)
	}
	if len(entries) > agentCoreJournalMaxFiles {
		t.Fatalf("文件数 = %d, want ≤ %d（prune 未生效）", len(entries), agentCoreJournalMaxFiles)
	}
}

func TestAgentCoreJournalWriteFailureIsNoop(t *testing.T) {
	home := t.TempDir()
	// 把 ai-sessions 目录位置占位成普通文件 → 目录创建必然失败 → journal
	// 必须安静退化（no-op），不得 panic、不得影响后续调用。
	if err := os.MkdirAll(filepath.Join(home, ".aliang"), 0o755); err != nil {
		t.Fatalf("准备失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aliang", "ai-sessions"), []byte("occupied"), 0o644); err != nil {
		t.Fatalf("准备失败: %v", err)
	}
	t.Setenv("HOME", home)
	agentCoreJournalStart("conv-fail", "/tmp/p", "glm-5.2", "m1", "x")
	agentCoreJournalEnd("conv-fail", "error", "boom") // 不得 panic
}
```

注意：测试文件顶部 import 需要 `fmt`（prune 测试用到）。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./app/http/services/ -run 'TestAgentCoreJournal' -v`
Expected: 编译失败（`undefined: agentCoreJournalStart` 等）

- [ ] **Step 3: 实现写入器**

新建 `app/http/services/agent_ai_agentcore_journal.go`：

```go
package services

// agentcore journal：把 AliangCode（内置内核 agentcore）的 AI 会话逐行落盘到
// <agentHome()>/.aliang/ai-sessions/<sessionID>.jsonl，让回合结束后会话仍留在
// 仪表盘历史并可回放。设计与取舍见
// docs/superpowers/specs/2026-10-26-agentcore-journal-design.md。
//
// 契约（全部「尽力而为」，写失败只 Warn 绝不影响 run）：
//   - 头行只在文件不存在时写，后续回合直接追加，头行不重写（同 ID 换项目
//     以先写下的头为准）；
//   - 同一 messageID 每会话只写一行（内存去重；进程重启集合清零，重发即新
//     messageID，重复场景本就不存在）；
//   - 新文件创建时 prune，仅保留最新 agentCoreJournalMaxFiles 个（按 mtime）。
//
// 并发模型（spec 评审钉死）：包级单锁 agentCoreJournal.mu 管写入+去重+prune；
// 无每会话实例，文件句柄不常驻（每行 open-append-close）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/internal/logger"
)

const (
	// agentCoreJournalMaxFiles 磁盘上限：只保留最新 N 个会话文件。
	agentCoreJournalMaxFiles = 100
	// agentCoreJournalDedupCap 每会话去重集合容量上限；超过即重置集合——
	// 重置后理论上的重复行只可能出现在「同一条消息重放」场景，本就不存在。
	agentCoreJournalDedupCap = 512
)

// agentCoreJournalRow 是 journal 的唯一行格式，写入与 reader 共用（同包）。
type agentCoreJournalRow struct {
	Kind        string `json:"kind"` // session | msg | end
	SessionID   string `json:"session_id,omitempty"`
	ProjectPath string `json:"project_path,omitempty"`
	Model       string `json:"model,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	Role        string `json:"role,omitempty"`
	MessageID   string `json:"message_id,omitempty"`
	Text        string `json:"text,omitempty"`
	Ts          string `json:"ts,omitempty"`
	Status      string `json:"status,omitempty"` // success | stopped | error
	Error       string `json:"error,omitempty"`
}

type agentCoreJournalStore struct {
	mu    sync.Mutex
	dedup map[string]map[string]struct{}
}

var agentCoreJournal = &agentCoreJournalStore{dedup: make(map[string]map[string]struct{})}

func agentCoreJournalDir() string {
	home := agentHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".aliang", "ai-sessions")
}

// sanitizeAgentCoreJournalName 只允许 [A-Za-z0-9._-]，其余转 `_`，防路径注入。
func sanitizeAgentCoreJournalName(sessionID string) string {
	var b strings.Builder
	for _, r := range sessionID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := b.String()
	if name == "" || name == "." || name == ".." {
		return "session"
	}
	return name
}

func agentCoreJournalFilePath(sessionID string) string {
	dir := agentCoreJournalDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, sanitizeAgentCoreJournalName(sessionID)+".jsonl")
}

func writeAgentCoreJournalLine(path string, row agentCoreJournalRow) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(row)
	if err != nil {
		return err
	}
	// O_RDWR 而非 O_WRONLY：下方残行防护要用 ReadAt(pread) 探测末字节，
	// Linux 对只写 fd 恒返 EBADF（macOS 宽松会掩盖该问题——部署目标恰是
	// Linux）。O_APPEND 语义不受影响：写仍为原子追加。
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	// 残行防护：上个写入者崩溃可能留下无换行尾的半行；先补一个换行，
	// 防止本行被粘到残行上变成又一条非法行（spec §5 半行容忍的写侧配合）。
	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		tail := make([]byte, 1)
		if _, err := f.ReadAt(tail, info.Size()-1); err == nil && tail[0] != '\n' {
			if _, err := f.Write([]byte("\n")); err != nil {
				f.Close()
				return err
			}
		}
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func agentCoreJournalWarnFailed(action string, err error) {
	logger.Warn("[AGENT-CORE-JOURNAL] " + action + "_failed error=" + err.Error())
}

// agentCoreJournalEnsureHeader 保证头行存在（仅当文件不存在时写）。
func agentCoreJournalEnsureHeader(sessionID, projectPath, model string) {
	path := agentCoreJournalFilePath(sessionID)
	if path == "" {
		return
	}
	agentCoreJournal.mu.Lock()
	defer agentCoreJournal.mu.Unlock()
	if _, err := os.Stat(path); err == nil {
		return
	}
	if err := writeAgentCoreJournalLine(path, agentCoreJournalRow{
		Kind:        "session",
		SessionID:   sessionID,
		ProjectPath: projectPath,
		Model:       model,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		agentCoreJournalWarnFailed("header", err)
		return
	}
	agentCoreJournal.pruneLocked()
}

func agentCoreJournalAppendMsg(sessionID, role, messageID, text string) {
	sessionID = strings.TrimSpace(sessionID)
	messageID = strings.TrimSpace(messageID)
	if sessionID == "" || messageID == "" {
		return
	}
	path := agentCoreJournalFilePath(sessionID)
	if path == "" {
		return
	}
	agentCoreJournal.mu.Lock()
	defer agentCoreJournal.mu.Unlock()
	set := agentCoreJournal.dedup[sessionID]
	if set == nil {
		set = make(map[string]struct{})
		agentCoreJournal.dedup[sessionID] = set
	}
	if _, dup := set[messageID]; dup {
		return
	}
	set[messageID] = struct{}{}
	if len(set) > agentCoreJournalDedupCap {
		agentCoreJournal.dedup[sessionID] = make(map[string]struct{})
	}
	if err := writeAgentCoreJournalLine(path, agentCoreJournalRow{
		Kind:      "msg",
		Role:      role,
		MessageID: messageID,
		Text:      text,
		Ts:        time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		agentCoreJournalWarnFailed("msg", err)
	}
}

// agentCoreJournalStart：run 入口挂点——无条件确保头行（空历史 run 也要有头行，
// 否则 defer 落的 end 行成孤儿）；userMessageID 非空才写 user 行。
func agentCoreJournalStart(sessionID, projectPath, model, userMessageID, userText string) {
	agentCoreJournalEnsureHeader(sessionID, projectPath, model)
	if strings.TrimSpace(userMessageID) != "" {
		agentCoreJournalAppendMsg(sessionID, "user", userMessageID, userText)
	}
}

// agentCoreJournalUser：运行中 steer 插话挂点。
func agentCoreJournalUser(sessionID, messageID, text string) {
	agentCoreJournalAppendMsg(sessionID, "user", messageID, text)
}

// agentCoreJournalAssistant：回合成功后的完整 assistant 文本（与手机端
// 「一回合一条助手消息」渲染口径一致）。
func agentCoreJournalAssistant(sessionID, messageID, text string) {
	agentCoreJournalAppendMsg(sessionID, "assistant", messageID, text)
}

// agentCoreJournalEnd：run 终态收敛（defer 单点）。end 行不参与去重——
// 同一会话多次 run 各有一行 end。
func agentCoreJournalEnd(sessionID, status, errMsg string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	path := agentCoreJournalFilePath(sessionID)
	if path == "" {
		return
	}
	agentCoreJournal.mu.Lock()
	defer agentCoreJournal.mu.Unlock()
	if err := writeAgentCoreJournalLine(path, agentCoreJournalRow{
		Kind:   "end",
		Status: status,
		Error:  errMsg,
		Ts:     time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		agentCoreJournalWarnFailed("end", err)
	}
}

// pruneLocked 只保留最新 agentCoreJournalMaxFiles 个 journal 文件（按 mtime，
// 最旧先删）。必须持有 agentCoreJournal.mu 调用；仅在「新文件刚创建」时触发，
// 扫描侧只读不删。被删文件若仍有活跃写入者，Linux 上写入会继续进入已 unlink
// 的 inode（该会话后续留痕静默丢失）——尽力而为 journal 可接受的损失模式
// （spec §9），不做「跳过打开中的文件」这类复杂化。
func (s *agentCoreJournalStore) pruneLocked() {
	dir := agentCoreJournalDir()
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type named struct {
		name string
		mod  time.Time
	}
	var files []named
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, named{e.Name(), info.ModTime()})
	}
	if len(files) <= agentCoreJournalMaxFiles {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	for _, f := range files[agentCoreJournalMaxFiles:] {
		if err := os.Remove(filepath.Join(dir, f.name)); err != nil {
			agentCoreJournalWarnFailed("prune", err)
		}
	}
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./app/http/services/ -run 'TestAgentCoreJournal' -v`
Expected: 6 个测试全部 PASS

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai_agentcore_journal.go app/http/services/agent_ai_agentcore_journal_test.go
git commit -m "$(cat <<'EOF'
新增：agentcore journal 写入器（行格式/头行一次/消息去重/prune 100 文件）

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: runAgentCore 三挂点 + steer 挂点

**Files:**
- Modify: `app/http/services/agent_ai_agentcore.go`（`runAgentCore` 入口/成功出口 + 新 helper）
- Modify: `app/http/services/agent_ai.go`（steer `default: coreControl.deliver` 分支）
- Test: `app/http/services/agent_ai_agentcore_journal_test.go`（追加 run 级测试）

- [ ] **Step 1: 写失败测试（run 级落盘）**

在 `agent_ai_agentcore_journal_test.go` 追加（复用 `agent_ai_agentcore_run_test.go` 的
`agentCoreUpstream`/`agentCoreTextTurn`/`agentCoreEventLog` 基建——同包直接可用）：

```go
// journal 测试文件顶部需追加 import："context"。

// TestRunAgentCoreJournalRecordsTurn 锁死三个挂点：入口 user 行取的是
// session.history 里的真实 content（不是 run.prompt 拼接产物），成功出口写
// assistant 行，defer 写 end(success)。
func TestRunAgentCoreJournalRecordsTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	up, srv := newAgentCoreUpstream(t, agentCoreTextTurn("你好", "，世界"))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")
	_ = up

	m := newAgentAIManager()
	const sessionID = "conv-journal-run"
	const runSeq = 1
	m.registerAISessionLocked(&agentAISession{
		id:      sessionID,
		runSeq:  runSeq,
		history: []agentAIMessage{{Role: "user", MessageID: "msg-real-1", Content: "打个招呼"}},
	})
	log := &agentCoreEventLog{}
	run := agentAIRun{
		sessionID:   sessionID,
		runSeq:      runSeq,
		messageID:   "msg-host-1",
		provider:    "agentcore",
		model:       "glm-5.2",
		projectPath: t.TempDir(),
		activity:    newAgentAIActivity(),
	}
	m.runAgentCore(context.Background(), run, log.write)

	rows := readJournalLines(t, sessionID)
	if len(rows) != 4 {
		t.Fatalf("行数 = %d, want 4（头+user+assistant+end）: %v", len(rows), rows)
	}
	if rows[1]["text"] != "打个招呼" {
		t.Fatalf("user 行必须是真实 history content，got %v", rows[1])
	}
	if rows[2]["role"] != "assistant" {
		t.Fatalf("缺 assistant 行: %v", rows)
	}
	if rows[3]["status"] != "success" {
		t.Fatalf("end 行 status = %v, want success", rows[3]["status"])
	}
}

// TestRunAgentCoreJournalErrorExit 锁死错误出口也落 end 行（空历史早退路径）。
// ANTHROPIC_BASE_URL 钉死不可连地址，防 agentCoreEnsureUpstream 触发真实
// dial + 自愈 POST（与既有 TestRunAgentCoreRejectsEmptyHistory 同口径）。
func TestRunAgentCoreJournalErrorExit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("ANTHROPIC_MODEL", "")
	m := newAgentAIManager()
	const sessionID = "conv-journal-err"
	m.registerAISessionLocked(&agentAISession{id: sessionID, runSeq: 1}) // 空 history
	log := &agentCoreEventLog{}
	run := agentAIRun{
		sessionID: sessionID, runSeq: 1, messageID: "msg-e", provider: "agentcore",
		projectPath: t.TempDir(), activity: newAgentAIActivity(),
	}
	m.runAgentCore(context.Background(), run, log.write)

	rows := readJournalLines(t, sessionID)
	if len(rows) != 2 { // 头 + end（空历史没有 user 行）
		t.Fatalf("行数 = %d, want 2: %v", len(rows), rows)
	}
	if rows[1]["kind"] != "end" || rows[1]["status"] != "error" {
		t.Fatalf("end 行错误: %v", rows[1])
	}
}

// TestRunAgentCoreJournalFallbackFirstUserLine 钉死 spec §6 挂点 1 与 §10
// fallback 回归的成立依据：user 行必须来自 session.history 的真实 content。
// 若实现误取 run.prompt（buildAgentAIPrompt 拼接产物），多行 content 会被
// 会话骨架改写——本测试即红。fallback 场景（provider=auto+无外部 CLI）在
// runCLI 派发时才改写 provider=agentcore，runAgentCore 收到的就是这里这种
// 「带完整 history 的普通 run」，本测试与其状态完全同构。
func TestRunAgentCoreJournalFallbackFirstUserLine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	up, srv := newAgentCoreUpstream(t, agentCoreTextTurn("好", ""))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")
	_ = up

	m := newAgentAIManager()
	const sessionID = "conv-journal-fallback"
	raw := "第一行\n\nUser: 伪装行\n第三行"
	m.registerAISessionLocked(&agentAISession{
		id:      sessionID,
		runSeq:  1,
		history: []agentAIMessage{{Role: "user", MessageID: "msg-fb-1", Content: raw}},
	})
	log := &agentCoreEventLog{}
	run := agentAIRun{
		sessionID: sessionID, runSeq: 1, messageID: "msg-host-9", provider: "agentcore",
		model: "glm-5.2", projectPath: t.TempDir(), activity: newAgentAIActivity(),
	}
	m.runAgentCore(context.Background(), run, log.write)

	rows := readJournalLines(t, sessionID)
	if len(rows) < 2 {
		t.Fatalf("行数 = %d, want ≥2: %v", len(rows), rows)
	}
	if rows[1]["message_id"] != "msg-fb-1" || rows[1]["text"] != raw {
		t.Fatalf("user 行必须逐字等于 history 真实 content（防 run.prompt 回归）, got %v", rows[1])
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./app/http/services/ -run 'TestRunAgentCoreJournal' -v`
Expected: FAIL（journal 行数不符——挂点未接）

- [ ] **Step 3: 接 runAgentCore 三挂点**

`app/http/services/agent_ai_agentcore.go` 的 `runAgentCore`：

(a) 函数开头（`defer m.clearRunning(...)` 与 `defer run.cancel()` 之后、
ai.run.started 写出之前）加：

```go
	// journal 落盘（spec §6 挂点 1）：必须在 agentCoreEnsureUpstream 之前。
	// 头行无条件确保（空历史 run 也要有头行，否则 defer 落的 end 行成孤儿）；
	// user 行取 session.history 最后一条 user 的真实 content（不取 run.prompt，
	// 那是拼接产物——fallback 用户首行正是靠这一条保住，spec §10 回归位）；
	// 刻意不做 runSeq 守卫（stale run 多写一行属可容忍损失）。
	journalUser, _ := m.lastAgentAIUserJournalMessage(run.sessionID)
	agentCoreJournalStart(run.sessionID, run.projectPath, agentCoreModel(run), journalUser.MessageID, journalUser.Content)
	journalStatus, journalErrMsg := "success", ""
	defer func() { agentCoreJournalEnd(run.sessionID, journalStatus, journalErrMsg) }()
```

(b) 六个早退分支各补一行状态标记（`journalStatus/journalErrMsg` 已在作用域内）：

- `agentCoreEnsureUpstream` 失败 return 前：`journalStatus, journalErrMsg = "error", err.Error()`
- `len(messages) == 0` return 前：`journalStatus, journalErrMsg = "error", "会话没有可发送的消息"`
- `bindingErr != nil` return 前：`journalStatus, journalErrMsg = "error", bindingErr.Error()`
- `ctx.Err() != nil` 分支内（`status, errMsg := agentAIRunStoppedStatus(...)` 之后）：`journalStatus, journalErrMsg = "stopped", errMsg`
- `result.IsError` 分支内：`journalStatus = "error"`（内核已发过 ai.error，错误文本不在此可得，留空）
- `runErr != nil` return 前：`journalStatus, journalErrMsg = "error", runErr.Error()`

(c) 成功出口 `m.appendAssistantHistory(...)` 之后一行加：

```go
	agentCoreJournalAssistant(run.sessionID, agentAssistantMessageID(run.messageID), assistantOutput)
```

(d) 同文件追加 helper（放在 `runAgentCore` 之后任意位置）：

```go
// lastAgentAIUserJournalMessage 返回会话历史里最后一条 user 消息（journal
// 落盘用：它才是本轮真实触发消息，run.prompt 是拼接产物 ≠ 原始 content，
// 2026-10-20 踩坑记录）。spec §6：刻意不做 runSeq 守卫。
func (m *agentAIManager) lastAgentAIUserJournalMessage(sessionID string) (agentAIMessage, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil {
		return agentAIMessage{}, false
	}
	for i := len(session.history) - 1; i >= 0; i-- {
		if session.history[i].Role == "user" {
			return session.history[i], true
		}
	}
	return agentAIMessage{}, false
}
```

- [ ] **Step 4: 接 steer 挂点**

`app/http/services/agent_ai.go` 的 ai.steer 派发 switch（`coreControl.deliver` 的
default 分支），改为：

```go
	default:
		result, err = coreControl.deliver(steer)
		if err == nil {
			// agentcore 专属（spec §6 挂点 2）：插话也是 user 消息，入队成功
			// 即落 journal。codex/claude 分支不动。
			agentCoreJournalUser(sessionID, messageID, steer.Content)
		}
	}
```

（`sessionID`/`messageID` 在该函数作用域已有；`steer.Content` 已并入附件文本后缀。）

- [ ] **Step 5: 运行确认通过 + 全包回归**

Run: `go test ./app/http/services/ -run 'TestRunAgentCore|TestAgentCoreJournal' -v`
Expected: 全部 PASS（含既有 `TestRunAgentCore*` 回归）

- [ ] **Step 6: 提交**

```bash
git add app/http/services/agent_ai_agentcore.go app/http/services/agent_ai.go app/http/services/agent_ai_agentcore_journal_test.go
git commit -m "$(cat <<'EOF'
新增：runAgentCore 与 steer 的 journal 挂点（入口 user 行/成功 assistant 行/终态 end 行）

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: 回放 reader

**Files:**
- Create: `app/http/services/agent_inventory_agentcore.go`
- Test: `app/http/services/agent_inventory_agentcore_test.go`

- [ ] **Step 1: 写失败测试（reader 行为全集）**

新建 `app/http/services/agent_inventory_agentcore_test.go`：

```go
package services

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCoreJournalFixture 直接手写 journal 行（与写入器解耦，防实现互相掩护）。
func writeCoreJournalFixture(t *testing.T, sessionID string, lines ...string) string {
	t.Helper()
	dir := agentCoreJournalDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("准备目录失败: %v", err)
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("写 fixture 失败: %v", err)
	}
	return path
}

func TestReadAgentCoreSessionMetaSummary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeCoreJournalFixture(t, "sess-r1",
		`{"kind":"session","session_id":"sess-r1","project_path":"/tmp/proj-x","model":"glm-5.2","created_at":"2026-10-26T01:00:00Z"}`,
		`{"kind":"msg","role":"user","message_id":"u1","text":"帮我看看这个报错","ts":"2026-10-26T01:00:05Z"}`,
		`{"kind":"msg","role":"assistant","message_id":"a1","text":"好的，我看看","ts":"2026-10-26T01:00:09Z"}`,
		`{"kind":"end","status":"success","ts":"2026-10-26T01:00:10Z"}`,
	)
	session, fromMtime := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: agentVibeTranscriptMaxMessages, IncludePageMeta: true})
	if session.ID != "sess-r1" || session.Provider != "agentcore" || session.Tool != "agentcore" {
		t.Fatalf("身份字段错误: %+v", session)
	}
	if session.Status != "closed" || session.Mode != "vibe" {
		t.Fatalf("状态字段错误: %+v", session)
	}
	if session.ProjectPath != "/tmp/proj-x" || session.Model != "glm-5.2" {
		t.Fatalf("头行字段错误: %+v", session)
	}
	if session.Title != "帮我看看这个报错" {
		t.Fatalf("Title = %q, want 首条 user 文本", session.Title)
	}
	if session.MessageCount != 2 || session.DisplayMessageCount == nil || *session.DisplayMessageCount != 2 {
		t.Fatalf("计数错误: count=%d display=%v", session.MessageCount, session.DisplayMessageCount)
	}
	if session.UpdatedAt != "2026-10-26T01:00:09Z" || fromMtime {
		t.Fatalf("UpdatedAt 应取最后一条消息 ts: %q fromMtime=%v", session.UpdatedAt, fromMtime)
	}
	if len(session.Transcript) != 2 || session.TranscriptPage == nil || session.TranscriptPage.TotalCount != 2 {
		t.Fatalf("转录/分页错误: %+v", session.TranscriptPage)
	}
}

func TestReadAgentCoreSessionMetaPagination(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	lines := []string{`{"kind":"session","session_id":"sess-pg","project_path":"/tmp/p","model":"m","created_at":"2026-10-26T01:00:00Z"}`}
	for i := 0; i < 10; i++ {
		lines = append(lines, `{"kind":"msg","role":"user","message_id":"u`+string(rune('0'+i))+`","text":"m`+string(rune('0'+i))+`","ts":"2026-10-26T01:0`+string(rune('0'+i))+`:00Z"}`)
	}
	path := writeCoreJournalFixture(t, "sess-pg", lines...)
	session, _ := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: 4, IncludePageMeta: true})
	if len(session.Transcript) != 4 || !session.TranscriptPage.HasMore {
		t.Fatalf("窗口应截到 4 条且有下一页: %d %v", len(session.Transcript), session.TranscriptPage)
	}
	if session.TranscriptPage.NextBeforeMessageID != session.Transcript[0].ID {
		t.Fatalf("翻页游标错误: %+v", session.TranscriptPage)
	}
	// 用游标翻上一页：window.add 遇到游标 id 即停（游标本身不入窗口），末行
	// 是游标紧前一条。u0..u9 + Limit=4 + 游标 u6 → 窗口缓冲 [u1..u5] →
	// page 取末 4 条 = [u2,u3,u4,u5]。
	prev, _ := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: 4, IncludePageMeta: true, BeforeMessageID: session.TranscriptPage.NextBeforeMessageID})
	if len(prev.Transcript) != 4 || prev.Transcript[len(prev.Transcript)-1].ID != "u5" {
		t.Fatalf("BeforeMessageID 翻页：末行应是游标紧前一条 u5, got %+v", prev.Transcript)
	}
	for _, m := range prev.Transcript {
		if m.ID == "u6" {
			t.Fatalf("游标行本身不得出现在上一页")
		}
	}
}

func TestReadAgentCoreSessionMetaScanDirsFilter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeCoreJournalFixture(t, "sess-filter",
		`{"kind":"session","session_id":"sess-filter","project_path":"/outside/proj","model":"m","created_at":"2026-10-26T01:00:00Z"}`,
		`{"kind":"msg","role":"user","message_id":"u1","text":"x","ts":"2026-10-26T01:00:05Z"}`,
	)
	session, _ := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: 8, ScanDirs: []string{"/allowed"}})
	if session.ID != "" {
		t.Fatalf("scanDirs 外的项目必须整体丢弃, got %+v", session)
	}
}

func TestReadAgentCoreSessionMetaNoHeaderSkipped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeCoreJournalFixture(t, "sess-nohdr",
		`{"kind":"msg","role":"user","message_id":"u1","text":"孤儿消息","ts":"2026-10-26T01:00:05Z"}`,
	)
	session, _ := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: 8})
	if session.ID != "" {
		t.Fatalf("无头行文件必须返回空结构体, got %+v", session)
	}
}

func TestReadAgentCoreSessionMetaMtimeFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeCoreJournalFixture(t, "sess-empty",
		`{"kind":"session","session_id":"sess-empty","project_path":"/tmp/p","model":"m","created_at":"2026-10-26T01:00:00Z"}`,
		`{"kind":"end","status":"error","error":"boom","ts":"2026-10-26T01:00:10Z"}`,
	)
	session, fromMtime := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: 8})
	if session.ID != "sess-empty" {
		t.Fatalf("头行缺失判定错误: %+v", session)
	}
	if session.MessageCount != 0 || session.DisplayMessageCount == nil || *session.DisplayMessageCount != 0 {
		t.Fatalf("空会话计数错误: %d %v", session.MessageCount, session.DisplayMessageCount)
	}
	if !fromMtime {
		t.Fatalf("无消息行时 UpdatedAt 应回落 mtime 并标记 provenance")
	}
	if _, err := time.Parse(time.RFC3339, session.UpdatedAt); err != nil {
		t.Fatalf("UpdatedAt 不是 RFC3339: %q", session.UpdatedAt)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./app/http/services/ -run 'TestReadAgentCoreSessionMeta' -v`
Expected: 编译失败（`undefined: readAgentCoreSessionMetaWithOptions`）

- [ ] **Step 3: 实现 reader**

新建 `app/http/services/agent_inventory_agentcore.go`：

```go
package services

// AliangCode（agentcore）journal 的扫描与回放 reader（spec §7）。与 claude/codex
// 通道同构：同款 recency-cap walker + (size,mtime) 解析缓存 + transcript 窗口；
// 条目 ID 用裸 agent 会话 ID（spec §8，与活跃覆盖层同一 ID 空间，运行中按 ID
// 合并不出双卡）。

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

func collectAgentCoreVibeSessions(scanDirs []string) []models.AgentVibeSession {
	root := agentCoreJournalDir()
	if root == "" {
		return nil
	}
	files, _, _ := findRecentAgentFilesWithStats(root, "*.jsonl", agentVibeSessionFileScanLimit)
	var sessions []models.AgentVibeSession
	for _, path := range files {
		session, _ := readAgentVibeCoreSummary(path, scanDirs)
		if session.ID == "" {
			continue
		}
		sessions = append(sessions, session)
	}
	return sessions
}

// readAgentVibeCoreSummary 带解析缓存的摘要读取（与 claude 通道 readAgentVibeClaudeSummary
// 同构：journal 只追加不回写，同 size+mtime 即视为同内容）。
func readAgentVibeCoreSummary(path string, scanDirs []string) (models.AgentVibeSession, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return models.AgentVibeSession{}, false
	}
	key := agentVibeParseCacheKey(path, scanDirs, agentVibeTranscriptMaxMessages)
	if entry, ok := agentVibeParseCacheLookup(key, info.Size(), info.ModTime()); ok {
		return entry.session, entry.updatedAtFromMtime
	}
	session, updatedAtFromMtime := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: agentVibeTranscriptMaxMessages, ScanDirs: scanDirs})
	agentVibeParseCacheStore(key, info.Size(), info.ModTime(), session, updatedAtFromMtime)
	return session, updatedAtFromMtime
}

// readAgentCoreSessionMetaWithOptions 解析单个 journal 文件。半行容忍：残行
// JSON 解析失败即跳过该行。早退（scanDirs 丢弃/无头行）整体返回空结构体。
func readAgentCoreSessionMetaWithOptions(path string, options agentVibeSessionReadOptions) (models.AgentVibeSession, bool) {
	options = normalizeAgentVibeSessionReadOptions(options)
	window := newAgentVibeTranscriptWindow(options)
	var session models.AgentVibeSession
	session.Provider = "agentcore"
	session.Tool = "agentcore"
	session.Mode = "vibe"
	session.Status = "closed"
	var firstUserPrompt string
	messageCount := 0
	var lastMessageAt time.Time
	updatedAtFromMtime := false
	discard := false
	maxLineBytes := options.MaxLineBytes
	if maxLineBytes <= 0 {
		maxLineBytes = agentVibeJSONLMaxLineBytes
	}
	forEachAgentSessionJSONLLine(path, maxLineBytes, func(line []byte) bool {
		var row agentCoreJournalRow
		if err := json.Unmarshal(line, &row); err != nil {
			return true // 半行容忍
		}
		switch row.Kind {
		case "session":
			session.ID = strings.TrimSpace(row.SessionID)
			session.ProjectPath = cleanAgentProjectPath(row.ProjectPath)
			session.Model = strings.TrimSpace(row.Model)
			session.CreatedAt = normalizeAgentTime(row.CreatedAt)
			if !isSafeAgentProjectPath(session.ProjectPath) {
				discard = true
				return false
			}
			if len(options.ScanDirs) > 0 && !pathUnderAnyScanDir(session.ProjectPath, options.ScanDirs) {
				discard = true
				return false
			}
		case "msg":
			if session.ID == "" {
				return true // 头行前的不良行：跳过（无头行文件靠 ID=="" 在上层跳过）
			}
			messageCount++
			ts := normalizeAgentTime(row.Ts)
			window.add(models.AgentVibeMessage{
				ID:        row.MessageID,
				Role:      row.Role,
				Content:   row.Text,
				Timestamp: ts,
				Index:     messageCount - 1,
			})
			if row.Role == "user" && firstUserPrompt == "" && !isJunkAgentTitle(row.Text) {
				firstUserPrompt = row.Text
			}
			if pt := parseAgentRFC3339(row.Ts); !pt.IsZero() {
				lastMessageAt = pt
			}
		case "end":
			// 终态行不参与转录；列表 Status 恒 closed（spec §7）。
		}
		return true
	})
	if discard || session.ID == "" {
		return models.AgentVibeSession{}, false
	}
	session.MessageCount = messageCount
	count := messageCount
	// journal 无命令/工件行，display == message；非 nil 0 也是「已知 0」。
	session.DisplayMessageCount = &count
	session.Title = truncateAgentText(firstUserPrompt, 200)
	if !lastMessageAt.IsZero() {
		session.UpdatedAt = lastMessageAt.UTC().Format(time.RFC3339)
	} else if info, err := os.Stat(path); err == nil {
		// 无真实消息行：mtime 只代表「文件被摸过」，标记 provenance 不参与
		// 活跃钟（与 claude 通道 transcriptFreshnessFromMtime 同一原则）。
		session.UpdatedAt = info.ModTime().UTC().Format(time.RFC3339)
		updatedAtFromMtime = true
	}
	session.Transcript, session.TranscriptPage = window.page(messageCount, options.IncludePageMeta)
	return session, updatedAtFromMtime
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./app/http/services/ -run 'TestReadAgentCoreSessionMeta' -v`
Expected: 5 个测试全部 PASS

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_inventory_agentcore.go app/http/services/agent_inventory_agentcore_test.go
git commit -m "$(cat <<'EOF'
新增：agentcore journal 回放 reader（摘要/分页/scanDirs 过滤/mtime 兜底）

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: 三路合并

**Files:**
- Modify: `app/http/services/agent_inventory.go:465`（`collectAgentVibeSessions`）
- Test: `app/http/services/agent_inventory_agentcore_test.go`（追加）

- [ ] **Step 1: 写失败测试**

`agent_inventory_agentcore_test.go` 追加：

```go
func TestCollectAgentCoreVibeSessionsMerge(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeCoreJournalFixture(t, "sess-c1",
		`{"kind":"session","session_id":"sess-c1","project_path":"/tmp/p","model":"m","created_at":"2026-10-26T01:00:00Z"}`,
		`{"kind":"msg","role":"user","message_id":"u1","text":"第一条","ts":"2026-10-26T01:00:05Z"}`,
	)
	writeCoreJournalFixture(t, "sess-c2",
		`{"kind":"session","session_id":"sess-c2","project_path":"/tmp/p","model":"m","created_at":"2026-10-26T02:00:00Z"}`,
		`{"kind":"msg","role":"user","message_id":"u1","text":"第二条","ts":"2026-10-26T02:00:05Z"}`,
	)
	// 无头行文件必须被收集器跳过。
	writeCoreJournalFixture(t, "sess-bad", `{"kind":"msg","role":"user","message_id":"u","text":"孤儿","ts":"2026-10-26T03:00:00Z"}`)

	sessions := collectAgentVibeSessions(nil)
	byID := make(map[string]models.AgentVibeSession)
	for _, s := range sessions {
		byID[s.ID] = s
	}
	c1, ok1 := byID["sess-c1"]
	c2, ok2 := byID["sess-c2"]
	if !ok1 || !ok2 {
		t.Fatalf("三路合并缺 agentcore 条目: c1=%v c2=%v", ok1, ok2)
	}
	if c1.Provider != "agentcore" || c1.Tool != "agentcore" || c1.Status != "closed" || c1.Title != "第一条" {
		t.Fatalf("agentcore 条目字段错误: %+v", c1)
	}
	if _, exists := byID["sess-bad"]; exists {
		t.Fatalf("无头行文件不应入列")
	}
	if _, exists := byID["agentcore_sess-c1"]; exists {
		t.Fatalf("agentcore ID 必须是裸 sessionID（spec §8），不得加前缀")
	}
}

// TestOverlayMergesAgentCoreFileEntryWithActive 锁死 spec §8 核心验收：
// 运行中文件条目与活跃覆盖层按裸 ID 合并，不出双卡。
func TestOverlayMergesAgentCoreFileEntryWithActive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeCoreJournalFixture(t, "sess-ov",
		`{"kind":"session","session_id":"sess-ov","project_path":"/tmp/p","model":"m","created_at":"2026-10-26T01:00:00Z"}`,
		`{"kind":"msg","role":"user","message_id":"u1","text":"历史标题","ts":"2026-10-26T01:00:05Z"}`,
	)
	snapshot := agentSyncSnapshot{VibeSessions: collectAgentVibeSessions(nil)}
	active := []models.AgentVibeSession{{
		ID: "sess-ov", Provider: "agentcore", Tool: "agentcore",
		ProjectPath: "/tmp/p", Status: "running",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}}
	merged := overlayActiveAgentVibeSessions(snapshot, active, nil)
	count := 0
	for _, s := range merged.VibeSessions {
		if s.ID == "sess-ov" {
			count++
			if s.Status != "running" {
				t.Fatalf("同 ID 条目应被活跃快照合并为 running, got %s", s.Status)
			}
		}
	}
	if count != 1 {
		t.Fatalf("运行中必须只出一张卡, got %d 条 sess-ov", count)
	}
}
```

（import 需补 `aliang.one/nursorgate/app/http/models`；`time` Task 3 已引入。）

- [ ] **Step 2: 运行确认失败**

Run: `go test ./app/http/services/ -run 'TestCollectAgentCoreVibeSessionsMerge' -v`
Expected: FAIL（合并点未接，`sess-c1` 不在结果里）

- [ ] **Step 3: 接合并点**

`app/http/services/agent_inventory.go` 的 `collectAgentVibeSessions` 改为：

```go
func collectAgentVibeSessions(scanDirs []string) []models.AgentVibeSession {
	sessions := append(collectCodexVibeSessions(scanDirs), collectClaudeVibeSessions(scanDirs)...)
	// 第三路：agentcore journal（AliangCode 内置内核，spec §7）。
	sessions = append(sessions, collectAgentCoreVibeSessions(scanDirs)...)
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt > sessions[j].UpdatedAt
	})
	if len(sessions) > agentVibeSummaryMaxSessions {
		sessions = sessions[:agentVibeSummaryMaxSessions]
	}
	return sessions
}
```

排序与截断沿用现有逻辑（agentcore 与 claude/codex 竞争同一热榜，无特殊待遇）。

- [ ] **Step 4: 运行确认通过 + 全包回归**

Run: `go test ./app/http/services/ -run 'TestCollectAgent|TestAgentVibe' -v`
Expected: PASS（含既有 vibe 测试回归）

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_inventory.go app/http/services/agent_inventory_agentcore_test.go
git commit -m "$(cat <<'EOF'
新增：AI 会话历史三路合并接入 agentcore journal 条目（裸 ID，与活跃覆盖层同空间）

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: 详情回放接线（端到端关键回归位）

**Files:**
- Modify: `app/http/services/agent_detail.go`（三处）
- Test: `app/http/services/agent_inventory_agentcore_test.go`（追加）

- [ ] **Step 1: 写失败测试（必须穿过 findAgentVibeSessionDetail）**

spec §10 明示：只直调 `readAgentVibeSessionDetailFile` 会假绿（枚举根漏配根本
到不了分发器）。测试必须从 `findAgentVibeSessionDetail` 进：

```go
// TestFindAgentVibeSessionDetailReachesAliangRoot 用两条查询路覆盖三处接线：
// sourceSessionID 空 → 兜底枚举(b)+分发(c)；sourceSessionID 非空 → 候选枚举(a)
// （candidateAgentVibeSessionFiles 对空 sourceSessionID 提前返回 nil，所以
// 只测第一条路锁不住接线 (a)）——任一接线缺失对应路必红。
func TestFindAgentVibeSessionDetailReachesAliangRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeCoreJournalFixture(t, "sess-e2e",
		`{"kind":"session","session_id":"sess-e2e","project_path":"/tmp/proj-e","model":"glm-5.2","created_at":"2026-10-26T01:00:00Z"}`,
		`{"kind":"msg","role":"user","message_id":"u1","text":"端到端问题","ts":"2026-10-26T01:00:05Z"}`,
		`{"kind":"msg","role":"assistant","message_id":"a1","text":"端到端回答","ts":"2026-10-26T01:00:09Z"}`,
		`{"kind":"end","status":"success","ts":"2026-10-26T01:00:10Z"}`,
	)
	session := findAgentVibeSessionDetail("sess-e2e", "", "/tmp/proj-e", agentVibeSessionReadOptions{Limit: agentVibeTranscriptMaxMessages})
	if session.ID != "sess-e2e" {
		t.Fatalf("detail 未能到达 .aliang 根（接线缺失）: %+v", session)
	}
	if len(session.Transcript) != 2 || session.Transcript[1].Content != "端到端回答" {
		t.Fatalf("转录内容错误: %+v", session.Transcript)
	}
	// includePageMeta 由分发器强制开启，分页元数据必须在。
	if session.TranscriptPage == nil {
		t.Fatalf("IncludePageMeta 未生效")
	}
	// 第二条路：sourceSessionID 非空 → 走候选枚举 (a)（pattern 包含匹配到
	// .aliang 根）。两路都通才算三处接线齐全。
	byCandidate := findAgentVibeSessionDetail("", "sess-e2e", "/tmp/proj-e", agentVibeSessionReadOptions{Limit: agentVibeTranscriptMaxMessages})
	if byCandidate.ID != "sess-e2e" {
		t.Fatalf("候选枚举路径未能到达 .aliang 根（接线 (a) 缺失）: %+v", byCandidate)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./app/http/services/ -run 'TestFindAgentVibeSessionDetailReachesAliangRoot' -v`
Expected: FAIL（`.aliang` 根未接线，返回空结构体）

- [ ] **Step 3: 接三处**

`app/http/services/agent_detail.go`：

(a) `candidateAgentVibeSessionFiles` 末尾（`.claude/projects` 行之后）加：

```go
	out = append(out, findRecentAgentFiles(filepath.Join(home, ".aliang", "ai-sessions"), pattern, agentVibeDetailCandidateFileLimit)...)
```

(b) `allAgentVibeSessionDetailFiles` 末尾加：

```go
	files = append(files, findRecentAgentFiles(filepath.Join(home, ".aliang", "ai-sessions"), "*.jsonl", 100)...)
```

(c) `readAgentVibeSessionDetailFile` 的两个分支之后加：

```go
	if strings.Contains(path, string(filepath.Separator)+".aliang"+string(filepath.Separator)) {
		session, _ := readAgentCoreSessionMetaWithOptions(path, options)
		return session
	}
```

（`options.IncludePageMeta = true` 已由分发器入口统一设置，无需重复。）

- [ ] **Step 4: 运行确认通过 + 全包回归**

Run: `go test ./app/http/services/ -run 'TestFindAgentVibeSessionDetail' -v && go test ./app/http/services/ -count=1`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_detail.go app/http/services/agent_inventory_agentcore_test.go
git commit -m "$(cat <<'EOF'
新增：详情回放接线 .aliang 根（候选/兜底两枚举 + 分发分支，端到端测试锁死）

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: 全量回归与收尾

- [ ] **Step 1: 全包测试**

Run: `go test ./app/http/services/ -count=1`
Expected: 全部 PASS（预存 flake 见仓库记忆：`processor/auth` 一个基线 flake 不在本包；
若 `agent_ai.go` 子进程超时类测试偶败，单包复跑一次即可，勿当回归）

- [ ] **Step 2: go vet 与构建**

Run: `go vet ./app/http/services/ && go build ./app/...`
Expected: 无输出（成功）。⚠️ 若在**新 worktree** 首次构建报 `website/dist` embed
错误：先从主仓 `cp -R app/website/dist`（见实施纪律第 1 条，仓内已知坑）。

- [ ] **Step 3: 真机冒烟清单（记录到 PR/交接说明，不阻塞）**

1. 手机发一条 AliangCode 会话消息 → 回合结束后仪表盘 AI 会话列表仍可见（provider=AliangCode）
2. 点开详情可回放对话文本
3. 运行中列表只出一张卡（活跃覆盖层与文件条目同 ID 合并）
4. agent 进程重启后记录仍在
5. 造 101 个会话后最旧的被 prune

- [ ] **Step 4: 提交（如Step 1-2 产生了修正）**

仅在前两步产生修正时提交；否则跳过。

---

## 风险与回滚

- 全部为追加式新路径，不触碰 claude/codex 任何现有行为；journal 故障退化方式
  是「不留痕」，不是「坏 run」。
- 回滚 = revert 各 commit 即可，`.aliang` 残留文件无害（扫描通道随代码移除）。
- 观测：journal 首次创建成功不额外打日志（避免噪音）；各写失败 Warn 带 action
  前缀（`header/msg/end/prune_failed`）。

## 测试简省说明（spec §10 对照）

已覆盖：头行一次/追加顺序/去重/半行容忍/写失败 no-op/文件名安全化/prune、摘要
字段完整/分页/scanDirs/mtime 兜底、三路合并+裸 ID、活跃覆盖层同 ID 合并（不出
双卡）、fallback 首条 user 行防 run.prompt 回归、detail 端到端穿 findAgentVibeSessionDetail。

有意简省（评审确认可降为 advisory，后续需要再补）：
- mtime 解析缓存「命中不重复解析」无独立断言——reader 完全复用
  `agentVibeParseCacheLookup/Store` 既有原语，该行为由 claude/codex 通道的既有
  缓存语义背书；
- junk title 过滤无专测——`isJunkAgentTitle` 为既有原语，reader 仅按 claude 通道
  同口径调用一次。
