# Agent 标签页三段式重构 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Agent 标签页重构为「状态 → AI 会话 → 终端」三段，消除 6 项重复，终端支持查看 scrollback，「历史同步」加「仅元信息」小字。

**Architecture:** 前端新增 `useAgentStatus` 共享 composable（单例消费 `/api/agent/status`，消灭双拉与逐行重复判定）；AI 会话合并为单一历史列表+「进行中」徽章；后端新增 `GET /api/agent/terminal/{sessionId}/scrollback` 读 ring buffer。依据 spec：`docs/superpowers/specs/2026-10-13-agent-tab-redesign-design.md`。

**Tech Stack:** Go 1.25（chi 无，标准库 mux，Go 1.22+ PathValue）、Vue 3 Options API、Vitest、Playwright。

**前置注意：**
- 所有工作在 worktree `/Users/mac/MyProgram/GoProgram/nursor/alianggate-agent-tab`（Task 0 创建）中进行，勿动主工作区。
- 提交信息全中文「新增：/重构：/修复：」，页脚加 `Co-Authored-By: Claude Code <noreply@anthropic.com>`。项目规则：执行者每次 commit 前需用户已批准本计划的执行（在 Execution Handoff 时确认一次即可）。
- i18n zh.js/en.js 逐行对齐约定：**两个文件必须在相同相对位置做相同的增删**。
- 主工作区 master 有未提交的 agent_inventory.go（codex 排序修复）与本任务无关，勿触碰。

---

### Task 0: 创建 worktree 与环境准备

**Files:** 无代码改动

- [ ] **Step 1: 创建 worktree**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
git worktree add ../alianggate-agent-tab -b feat/agent-tab-redesign master
cd ../alianggate-agent-tab
```

Expected: 输出 `branch 'feat/agent-tab-redesign' set up to track...` 或 `Preparing worktree`，目录存在。

- [ ] **Step 2: 前端依赖安装**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate-agent-tab/app/website
npm install
```

Expected: 安装完成无 ERR（已有 lock 文件，允许 warn）。

- [ ] **Step 3: 验证基线**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate-agent-tab
go build ./... && echo GO_OK
cd app/website && npm test 2>&1 | tail -5
```

Expected: `GO_OK`；vitest 现有测试全绿（记录基线，后面对比）。

---

### Task 1: 后端 — scrollback 快照服务（模型 + 限长 + manager 方法）

**Files:**
- Modify: `app/http/models/agent.go`（AgentTerminalRuntime 结构体之后，约 :177）
- Modify: `app/http/services/agent_terminal.go`（常量区 + manager 方法区）
- Modify: `app/http/services/agent_service.go`（RuntimeSessions 方法之后，约 :357）
- Test: `app/http/services/agent_terminal_scrollback_test.go`（新建）

- [ ] **Step 1: 写失败测试**

新建 `app/http/services/agent_terminal_scrollback_test.go`：

```go
package services

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestNormalizeAgentTerminalScrollbackLimit(t *testing.T) {
	if got := normalizeAgentTerminalScrollbackLimit(0); got != agentTerminalScrollbackDefaultBytes {
		t.Fatalf("normalize(0) = %d, want default %d", got, agentTerminalScrollbackDefaultBytes)
	}
	if got := normalizeAgentTerminalScrollbackLimit(-1); got != agentTerminalScrollbackDefaultBytes {
		t.Fatalf("normalize(-1) = %d, want default", got)
	}
	if got := normalizeAgentTerminalScrollbackLimit(1024); got != 1024 {
		t.Fatalf("normalize(1024) = %d, want 1024", got)
	}
	if got := normalizeAgentTerminalScrollbackLimit(1 << 30); got != agentTerminalScrollbackMaxBytes {
		t.Fatalf("normalize(huge) = %d, want cap %d", got, agentTerminalScrollbackMaxBytes)
	}
}

func TestAgentTerminalScrollbackLiveSession(t *testing.T) {
	m := newAgentTerminalManager()
	ring := newTerminalRingBuffer(4096)
	ring.push([]byte("hello scrollback"))
	m.sessions["s1"] = &agentTerminalSession{id: "s1", ring: ring}

	got, ok := m.scrollbackSnapshot("s1", 0)
	if !ok {
		t.Fatal("expected live session snapshot")
	}
	if got.Status != terminalReplayStatusLive || got.Data != "hello scrollback" || got.Truncated {
		t.Fatalf("unexpected response: %+v", got)
	}
	if got.Encoding != "text" || got.SessionID != "s1" {
		t.Fatalf("unexpected envelope: %+v", got)
	}
	if _, err := time.Parse(time.RFC3339, got.CapturedAt); err != nil {
		t.Fatalf("captured_at not RFC3339: %v", err)
	}
}

func TestAgentTerminalScrollbackTombstone(t *testing.T) {
	m := newAgentTerminalManager()
	ring := newTerminalRingBuffer(4096)
	ring.push([]byte("old output"))
	m.history["s2"] = &agentTerminalHistory{shell: "bash", exitedAt: time.Now(), ring: ring}

	got, ok := m.scrollbackSnapshot("s2", 0)
	if !ok || got.Status != terminalReplayStatusExited || got.Data != "old output" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
}

func TestAgentTerminalScrollbackMissing(t *testing.T) {
	m := newAgentTerminalManager()
	if _, ok := m.scrollbackSnapshot("nope", 0); ok {
		t.Fatal("expected ok=false for unknown session")
	}
}

func TestAgentTerminalScrollbackTruncatesTailAndKeepsUTF8(t *testing.T) {
	ring := newTerminalRingBuffer(8192)
	payload := "你好，世界" + strings.Repeat("x", 100)
	ring.push([]byte(payload))

	m := newAgentTerminalManager()
	m.sessions["s3"] = &agentTerminalSession{id: "s3", ring: ring}

	got, ok := m.scrollbackSnapshot("s3", 50)
	if !ok {
		t.Fatal("expected snapshot")
	}
	if !got.Truncated {
		t.Fatalf("expected truncated=true, got %+v", got)
	}
	if len(got.Data) > 50 {
		t.Fatalf("data len %d exceeds limit 50", len(got.Data))
	}
	if !utf8.ValidString(got.Data) {
		t.Fatalf("data must be valid UTF-8, got %q", got.Data)
	}
	if !strings.HasSuffix(payload, got.Data) {
		t.Fatalf("data %q must be a suffix of payload", got.Data)
	}
}

func TestAgentTerminalScrollbackConcurrentWithOutput(t *testing.T) {
	m := newAgentTerminalManager()
	ring := newTerminalRingBuffer(1 << 16)
	m.sessions["s4"] = &agentTerminalSession{id: "s4", ring: ring}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			ring.push([]byte("0123456789"))
		}
	}()
	for i := 0; i < 50; i++ {
		if _, ok := m.scrollbackSnapshot("s4", 0); !ok {
			t.Fatal("expected snapshot while output streaming")
		}
	}
	<-done
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate-agent-tab
go test ./app/http/services/ -run 'Scrollback|normalizeAgentTerminalScrollbackLimit' -v 2>&1 | tail -10
```

Expected: 编译失败 `undefined: normalizeAgentTerminalScrollbackLimit`（或同类 undefined）。

- [ ] **Step 3: 最小实现**

`app/http/models/agent.go` — 在 `AgentTerminalRuntime` 结构体后新增：

```go
type AgentTerminalScrollback struct {
	SessionID  string `json:"session_id"`
	Encoding   string `json:"encoding"`
	Status     string `json:"status"` // "live" | "exited"
	Data       string `json:"data"`
	Truncated  bool   `json:"truncated"`
	CapturedAt string `json:"captured_at"`
}
```

`app/http/services/agent_terminal.go` — 在文件常量区（如 `agentTerminalReplayChunkBytes` 附近或文件顶部常量块）新增：

```go
const (
	agentTerminalScrollbackDefaultBytes = 64 * 1024
	agentTerminalScrollbackMaxBytes     = 512 * 1024
)
```

同文件，在 `attachHistory` 方法后新增（`unicode/utf8` 已在该文件 imports 中，无需新增）：

```go
// normalizeAgentTerminalScrollbackLimit clamps a client-provided byte limit:
// non-positive falls back to the default; anything above the cap is clamped.
func normalizeAgentTerminalScrollbackLimit(limit int) int {
	if limit <= 0 {
		return agentTerminalScrollbackDefaultBytes
	}
	if limit > agentTerminalScrollbackMaxBytes {
		return agentTerminalScrollbackMaxBytes
	}
	return limit
}

// scrollbackSnapshot returns the recent output (scrollback) of a terminal
// session: the live ring for a running session, or the tombstone ring for an
// exited one. ok is false when neither exists (unknown or reaped session).
// Lock order follows the documented one-way rule: m.mu is released BEFORE
// outputGate is taken for a live session; a tombstone ring has no live
// writers and is read under m.mu alone.
func (m *agentTerminalManager) scrollbackSnapshot(sessionID string, maxBytes int) (models.AgentTerminalScrollback, bool) {
	if m == nil {
		return models.AgentTerminalScrollback{}, false
	}
	capturedAt := time.Now().UTC().Format(time.RFC3339)
	maxBytes = normalizeAgentTerminalScrollbackLimit(maxBytes)

	m.mu.Lock()
	if session := m.sessions[sessionID]; session != nil {
		m.mu.Unlock()
		session.outputGate.Lock()
		data := session.ring.snapshot()
		session.outputGate.Unlock()
		return newAgentTerminalScrollback(sessionID, data, terminalReplayStatusLive, maxBytes, capturedAt), true
	}
	tomb := m.history[sessionID]
	if tomb != nil {
		data := tomb.ring.snapshot()
		m.mu.Unlock()
		return newAgentTerminalScrollback(sessionID, data, terminalReplayStatusExited, maxBytes, capturedAt), true
	}
	m.mu.Unlock()
	return models.AgentTerminalScrollback{}, false
}

// newAgentTerminalScrollback keeps the last maxBytes bytes of data, marking
// truncated when the head was dropped. The head is advanced past any partial
// multi-byte rune so the payload stays valid UTF-8 (interior chunk ends are
// already rune-aligned by the ring feed).
func newAgentTerminalScrollback(sessionID string, data []byte, status string, maxBytes int, capturedAt string) models.AgentTerminalScrollback {
	truncated := false
	if len(data) > maxBytes {
		data = data[len(data)-maxBytes:]
		truncated = true
		for len(data) > 0 && !utf8.RuneStart(data[0]) {
			data = data[1:]
		}
	}
	return models.AgentTerminalScrollback{
		SessionID:  sessionID,
		Encoding:   "text",
		Status:     status,
		Data:       string(data),
		Truncated:  truncated,
		CapturedAt: capturedAt,
	}
}
```

`app/http/services/agent_service.go` — 在 `RuntimeSessions` 方法后新增（跟随既有 `h.service.XXX` 导出模式，handler 不直接触碰非导出 manager）：

```go
// TerminalScrollback returns a snapshot of a terminal session's recent output
// for the dashboard read-only view. ok is false when no live session or
// history tombstone matches sessionID.
func (s *AgentService) TerminalScrollback(sessionID string, maxBytes int) (models.AgentTerminalScrollback, bool) {
	if s == nil || s.terminal == nil {
		return models.AgentTerminalScrollback{}, false
	}
	return s.terminal.scrollbackSnapshot(sessionID, maxBytes)
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
go test ./app/http/services/ -run 'Scrollback' -v 2>&1 | tail -15
```

Expected: 全部 PASS。再跑一次带 race（该包此前有 race 修复史）：

```bash
go test ./app/http/services/ -run 'Scrollback' -race -v 2>&1 | tail -5
```

Expected: PASS，无 DATA RACE。

- [ ] **Step 5: Commit**

```bash
git add app/http/models/agent.go app/http/services/agent_terminal.go app/http/services/agent_service.go app/http/services/agent_terminal_scrollback_test.go
git commit -m "$(cat <<'EOF'
新增：终端 scrollback 快照服务——live ring/墓碑/gone 三态与限长规整

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: 后端 — HTTP 端点与路由注册

**Files:**
- Modify: `app/http/handlers/agent_handler.go`（HandleVibeSession 之后，约 :285）
- Modify: `app/http/routes/routes.go`（:273 `runtime-sessions` 注册行之后）
- Test: `app/http/handlers/agent_terminal_scrollback_handler_test.go`（新建）

- [ ] **Step 1: 写失败测试**

新建 `app/http/handlers/agent_terminal_scrollback_handler_test.go`：

```go
package handlers

import (
	"net/http/httptest"
	"testing"
)

// 405 / 缺 sessionId 两个分支在触碰 service 之前返回，零值 handler 即可测。
func TestHandleTerminalScrollbackMethodNotAllowed(t *testing.T) {
	h := &AgentHandler{}
	req := httptest.NewRequest("POST", "/api/agent/terminal/x/scrollback", nil)
	rec := httptest.NewRecorder()
	h.HandleTerminalScrollback(rec, req)
	if rec.Code != 405 {
		t.Fatalf("code = %d, want 405", rec.Code)
	}
}

func TestHandleTerminalScrollbackRequiresSessionID(t *testing.T) {
	h := &AgentHandler{}
	req := httptest.NewRequest("GET", "/api/agent/terminal//scrollback", nil)
	rec := httptest.NewRecorder()
	h.HandleTerminalScrollback(rec, req)
	if rec.Code != 400 {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./app/http/handlers/ -run 'HandleTerminalScrollback' -v 2>&1 | tail -5
```

Expected: 编译失败 `h.HandleTerminalScrollback undefined`。

- [ ] **Step 3: 实现 handler 与路由**

`app/http/handlers/agent_handler.go` — 在 `HandleVibeSession` 之后新增（`strconv`/`strings`/`common` 均已 import；校验顺序 method→id→proxy，缺 id 的请求在代理前快速失败）：

```go
// HandleTerminalScrollback returns a terminal session's recent output
// (?limit= bytes, default 64KiB, cap 512KiB). Mirrors HandleRuntimeSessions:
// the terminal manager lives in the user-agent runtime, so non-runtime
// processes proxy there via proxyIfNeeded.
func (h *AgentHandler) HandleTerminalScrollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("sessionId"))
	if sessionID == "" {
		common.ErrorBadRequest(w, "sessionId is required", nil)
		return
	}
	if h.proxyIfNeeded(w, r) {
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	resp, ok := h.service.TerminalScrollback(sessionID, limit)
	if !ok {
		common.ErrorNotFound(w, "terminal session not found")
		return
	}
	common.Success(w, resp)
}
```

`app/http/routes/routes.go` — 在 `register("/api/agent/runtime-sessions", ...)` 行之后新增（方法前缀模式，同文件已有 `{id}` 路由约定注释）：

```go
	// {sessionId} 路由用方法前缀模式注册（同 quick-setup combos 约定，避免 ServeMux panic）
	register("GET /api/agent/terminal/{sessionId}/scrollback", h.Agent.HandleTerminalScrollback, http.MethodGet)
```

- [ ] **Step 4: 运行测试确认通过**

```bash
go test ./app/http/handlers/ -run 'HandleTerminalScrollback' -v 2>&1 | tail -5
go build ./... && echo BUILD_OK
```

Expected: 2 个测试 PASS；`BUILD_OK`。

- [ ] **Step 5: Commit**

```bash
git add app/http/handlers/agent_handler.go app/http/routes/routes.go app/http/handlers/agent_terminal_scrollback_handler_test.go
git commit -m "$(cat <<'EOF'
新增：仪表盘终端输出端点 GET /api/agent/terminal/{sessionId}/scrollback

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: 前端 — agentApi + useAgentStatus 共享 composable

**Files:**
- Modify: `app/website/src/services/agentApi.js`（文件末尾）
- Create: `app/website/src/composables/useAgentStatus.js`
- Test: `app/website/src/composables/useAgentStatus.test.js`（新建）

- [ ] **Step 1: 写失败测试**

新建 `app/website/src/composables/useAgentStatus.test.js`。

⚠️ 必须 mock `../i18n` 与 `../services/agentApi`：`i18n/index.js` 在模块作用域直接访问 `localStorage`（vitest 默认 node 环境未定义，会 `ReferenceError` 崩在 import 阶段而非断言阶段）；agentApi 又引 `stores/auth` 同样有 localStorage 副作用。mock 两者是既有先例（`src/services/quickSetupApi.test.js` 同款注释）。

```js
import { beforeEach, describe, expect, it, vi } from 'vitest';

// i18n/index.js 模块作用域访问 localStorage，node 环境未定义——mock 掉。
// t 的 mock 把参数拼进返回值（'key:1/2'），供占位符类断言使用
vi.mock('../i18n', () => ({
  useI18n: () => ({
    t: (key, params) => (params && typeof params === 'object' ? `${key}:${Object.values(params).join('/')}` : key),
  }),
}));
// stores/auth（经 agentApi 引入）同样有 localStorage 副作用
vi.mock('../services/agentApi', () => ({
  getAgentStatus: vi.fn(),
}));

import { resetForTests, useAgentStatus } from './useAgentStatus';

describe('useAgentStatus', () => {
  beforeEach(() => resetForTests());

  it('shares one module-level state across consumers', () => {
    const a = useAgentStatus();
    const b = useAgentStatus();
    expect(a.status).toBe(b.status); // 同一份模块级状态 ref
    a.applyStatus({ runtime: { online: true }, enabled: true, bound: true });
    expect(b.agentEnabled.value).toBe(true); // 独立 consumer 的 computed 读到同一份状态
  });

  it('derives runtime/agent state from status', () => {
    const s = useAgentStatus();
    expect(s.runtimeOnline.value).toBe(true); // 无数据时默认视为在线
    s.applyStatus({ runtime: { online: false }, enabled: false, bound: false });
    expect(s.runtimeOnline.value).toBe(false);
    expect(s.agentEnabled.value).toBe(false);

    s.applyStatus({ runtime: { online: true }, enabled: true, bound: true });
    expect(s.agentEnabled.value).toBe(true);
  });

  it('computes tool availability summary', () => {
    const s = useAgentStatus();
    s.applyStatus({
      runtime: { online: true },
      enabled: true,
      bound: true,
      tools: [
        { id: 'codex', available: true },
        { id: 'claude', available: false },
      ],
    });
    expect(s.primaryTools.value).toHaveLength(2);
    expect(s.toolAvailabilitySummary.value).toContain('1');
  });
});
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate-agent-tab/app/website
npm test -- useAgentStatus 2>&1 | tail -5
```

Expected: FAIL，`Cannot find module './useAgentStatus'`。

- [ ] **Step 3: 实现 composable 与 API 封装**

新建 `app/website/src/composables/useAgentStatus.js`：

```js
import { computed, ref } from 'vue';
import { getAgentStatus } from '../services/agentApi';
import { useI18n } from '../i18n';

// 模块级共享状态：Agent 标签页所有组件共用一次 /api/agent/status 拉取，
// 状态判定逻辑只在这里写一遍（此前 AgentSettings 与 AgentActivitySettings 各有一份逐行重复）。
const status = ref(null);
const loading = ref(false);
const error = ref('');
let loaded = false;
let inFlight = null;

async function ensureLoaded() {
  if (loaded && status.value) return status.value;
  if (inFlight) return inFlight;
  inFlight = (async () => {
    loading.value = true;
    error.value = '';
    try {
      status.value = await getAgentStatus();
      loaded = true;
      return status.value;
    } catch (err) {
      error.value = err instanceof Error ? err.message : String(err);
      return null;
    } finally {
      loading.value = false;
      inFlight = null;
    }
  })();
  return inFlight;
}

async function refresh() {
  loaded = false;
  return ensureLoaded();
}

// enable/disable 等变更接口返回同一 AgentStatusResponse，直接写入共享状态
function applyStatus(next) {
  if (!next) return;
  status.value = next;
  loaded = true;
}

function resetForTests() {
  status.value = null;
  loading.value = false;
  error.value = '';
  loaded = false;
  inFlight = null;
}

export function useAgentStatus() {
  const { t } = useI18n();

  const runtimeOnline = computed(() => (!status.value?.runtime ? true : Boolean(status.value.runtime.online)));
  const agentEnabled = computed(() => runtimeOnline.value && Boolean(status.value?.enabled && status.value?.bound));
  const statusLabel = computed(() => {
    if (!runtimeOnline.value) return t('agent_runtimeOffline');
    if (agentEnabled.value) return t('agent_enabled');
    return t('agent_disabled');
  });
  const statusBadgeClass = computed(() => {
    if (!runtimeOnline.value) return 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300';
    if (agentEnabled.value) return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300';
    return 'bg-slate-200 text-slate-600 dark:bg-slate-700 dark:text-slate-300';
  });
  const primaryTools = computed(() => {
    const tools = Array.isArray(status.value?.tools) ? status.value.tools : [];
    return tools.filter((tool) => ['codex', 'claude', 'claudecode'].includes(tool.id));
  });
  const toolAvailabilitySummary = computed(() => t('agent_toolsSummary', {
    available: primaryTools.value.filter((tool) => tool.available).length,
    total: primaryTools.value.length,
  }));
  const syncLabel = computed(() => status.value?.sync_status
    || (status.value?.registered ? t('agent_syncRegistered') : t('agent_syncNotRegistered')));

  return {
    status, loading, error,
    runtimeOnline, agentEnabled, statusLabel, statusBadgeClass,
    primaryTools, toolAvailabilitySummary, syncLabel,
    ensureLoaded, refresh, applyStatus,
  };
}

export { resetForTests };
```

`app/website/src/services/agentApi.js` — 文件末尾追加：

```js
export async function getAgentTerminalScrollback(sessionId, { limit } = {}) {
  const params = new URLSearchParams();
  if (limit) params.set('limit', String(limit));
  const query = params.toString();
  return request(
    `/api/agent/terminal/${encodeURIComponent(sessionId)}/scrollback${query ? `?${query}` : ''}`,
    { method: 'GET' }
  );
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
npm test -- useAgentStatus 2>&1 | tail -5
```

Expected: 3 个测试 PASS。

- [ ] **Step 5: Commit**

```bash
git add app/website/src/composables/useAgentStatus.js app/website/src/composables/useAgentStatus.test.js app/website/src/services/agentApi.js
git commit -m "$(cat <<'EOF'
新增：useAgentStatus 共享状态（消除 /api/agent/status 同页双拉与重复判定）

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: 前端 — AgentSettings 状态卡重构 + 健康度迁入 + 同步小字

**Files:**
- Modify: `app/website/src/components/settings/AgentSettings.vue`
- Modify: `app/website/src/i18n/zh.js`、`app/website/src/i18n/en.js`（agent_historyMeta 与 agent_pre*Ok 区块，两文件同位置）

- [ ] **Step 1: i18n 键调整（zh/en 同位置）**

zh.js：在 `agent_historyMeta: '{files} 个文件 · {size}',` 行后新增一行：

```js
  agent_historyMetaHint: '仅同步项目与会话的元信息摘要（目录、文件数、大小等），不会上传任何代码或对话内容。',
```

en.js 同位置：

```js
  agent_historyMetaHint: 'Only metadata summaries of projects and sessions (directories, file counts, sizes) are synced — never your code or conversation content.',
```

zh.js 删除以下 6 行（ ok 文案随「前置条件仅显示未通过项」废弃；`agent_preRegisteredOk` 追加提示的 spec 备选随之取消，提示由 `agent_historyMetaHint` 承担）：

```
agent_preRuntimeOk / agent_preRegisteredOk / agent_preBoundOk /
agent_preToolOk / agent_preTerminalOk / agent_preAIOk
```

en.js **删除同样 6 行**（保持行号对齐）。

- [ ] **Step 2: AgentSettings.vue 模板改造**

`setup()` 改为（composable 的 ref 从 setup 返回后，模板与 `this.xxx` 均可直接用）：

```js
  setup() {
    const { t } = useI18n();
    return { t, ...useAgentStatus() };
  },
```

script 头部 import 增加：

```js
import { useAgentStatus } from '../../composables/useAgentStatus';
```

模板修改 ⓪：状态行中的设备摘要段落（约 :52-55，`<p v-if="deviceSummary">…devices…`）**整段删除**——设备信息由下述 meta 行唯一呈现（消除「设备状态 3 处」重复的最后一处）；同时 computed 中的 `deviceStatusClass` 变为死代码，一并删除（`deviceStatusLabel`/`deviceStatusPillClass` 保留，弹窗设备信息块仍在用）。

模板修改 ①：四格统计 `<dl>` 整块（约 :60-91）替换为 meta 行 + 健康度块：

```html
    <div class="mt-5 flex flex-wrap items-center gap-x-4 gap-y-1.5 border-t border-slate-100 px-5 py-4 text-[11px] font-medium text-slate-600 dark:border-slate-800 dark:text-slate-300 sm:px-6">
      <span v-if="deviceSummary" class="flex min-w-0 items-center gap-1.5">
        <span class="material-symbols-outlined text-sm text-slate-400">devices</span>
        <span class="truncate">{{ deviceSummary }}</span>
      </span>
      <span class="flex items-center gap-1.5">
        <span class="material-symbols-outlined text-sm text-slate-400">build</span>
        {{ toolAvailabilitySummary }}
      </span>
      <span class="flex items-center gap-1.5" :title="syncTitle">
        <span class="material-symbols-outlined text-sm text-slate-400">sync</span>
        {{ syncLabel }}
      </span>
    </div>

    <!-- 注册 / 连接健康度：正常一行摘要，出错才展开 -->
    <div v-if="healthVisible" class="border-t border-slate-100 px-5 py-4 dark:border-slate-800 sm:px-6">
      <template v-if="healthHasProblem">
        <div class="space-y-1.5">
          <div class="flex flex-wrap items-center gap-2">
            <span class="text-[10px] font-bold uppercase tracking-wide text-slate-400">{{ t('agent_registration_label') }}</span>
            <span class="shrink-0 rounded-full px-2 py-0.5 text-[10px] font-bold" :class="registrationBadgeClass">{{ registrationLabel }}</span>
            <span v-if="status?.registration_message" class="min-w-0 flex-1 truncate text-[10px] text-rose-600 dark:text-rose-300" :title="status.registration_message">{{ status.registration_message }}</span>
          </div>
          <div class="flex flex-wrap items-center gap-2">
            <span class="text-[10px] font-bold uppercase tracking-wide text-slate-400">{{ t('agent_connection_label') }}</span>
            <span class="shrink-0 rounded-full px-2 py-0.5 text-[10px] font-bold" :class="connectionBadgeClass">{{ connectionLabel }}</span>
            <span v-if="status?.connection_message" class="min-w-0 flex-1 truncate text-[10px] text-rose-600 dark:text-rose-300" :title="status.connection_message">{{ status.connection_message }}</span>
          </div>
        </div>
        <button
          v-if="canRetry"
          type="button"
          class="mt-2 inline-flex min-h-11 items-center gap-1 rounded-lg px-2 text-[11px] font-semibold text-primary transition-colors hover:bg-primary/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 disabled:opacity-60"
          :disabled="retrying"
          @click="retryRegistration"
        >
          <span class="material-symbols-outlined text-sm">{{ retrying ? 'progress_activity' : 'sync' }}</span>
          {{ retrying ? t('agent_health_retrying') : t('agent_health_retry') }}
        </button>
      </template>
      <p v-else class="text-[11px] text-slate-500 dark:text-slate-400">
        {{ t('agent_registration_label') }} {{ registrationLabel }} · {{ t('agent_connection_label') }} {{ connectionLabel }}
      </p>
    </div>
```

模板修改 ②：设置弹窗「启动前置条件」块（约 :146-159）整体替换为仅未通过项：

```html
    <div v-if="failedPrerequisites.length" class="rounded-lg border border-amber-200 bg-amber-50/70 px-3 py-3 dark:border-amber-500/30 dark:bg-amber-500/10">
      <p class="text-[11px] font-bold uppercase tracking-[0.18em] text-amber-700 dark:text-amber-300">{{ t('agent_prerequisites') }}</p>
      <div class="mt-3 space-y-2">
        <div
          v-for="item in failedPrerequisites"
          :key="item.key"
          class="flex items-start gap-2 text-[11px] text-amber-700 dark:text-amber-200"
        >
          <span class="material-symbols-outlined mt-0.5 text-sm">radio_button_unchecked</span>
          <span class="leading-5">{{ item.label }}</span>
        </div>
      </div>
    </div>
```

模板修改 ③：「历史同步」标题行（约 :162-165）下加小字：

```html
      <p class="mt-1.5 text-[10px] leading-4 text-slate-400 dark:text-slate-500">{{ t('agent_historyMetaHint') }}</p>
```

- [ ] **Step 3: AgentSettings.vue script 改造**

`data()` 删除 `status: null` 与 `loading: false`（改用 composable 的共享 ref），新增 `retrying: false`：

```js
  data() {
    return {
      binding: false,
      settingsModalOpen: false,
      launchLoading: false,
      commandLine: '',
      cwd: '',
      lastLaunch: null,
      feedback: '',
      feedbackType: 'info',
      retrying: false,
    };
  },
```

computed 删除：`runtimeOnline`、`agentEnabled`、`statusLabel`、`statusBadgeClass`、`primaryTools`、`availablePrimaryTools`、`toolAvailabilitySummary`、`syncLabel`、`runtimeLabel`、`runtimeTitle`（后两个随四格统计删除）、`deviceStatusClass`（随 ⓪ 删除）。保留并新增：

```js
    canLaunchCommands() {
      return this.agentEnabled && this.remoteTerminalFeatureEnabled;
    },
    failedPrerequisites() {
      return [
        { key: 'runtime', ok: this.runtimeOnline, label: this.t('agent_preRuntimeMissing') },
        { key: 'registered', ok: Boolean(this.status?.registered), label: this.t('agent_preRegisteredMissing') },
        { key: 'bound', ok: this.agentEnabled, label: this.t('agent_preBoundMissing') },
        { key: 'tool', ok: this.primaryTools.some((tool) => tool.available), label: this.t('agent_preToolMissing') },
        { key: 'terminal', ok: this.remoteTerminalFeatureEnabled, label: this.t('agent_preTerminalDisabled') },
        { key: 'ai', ok: this.aiControlFeatureEnabled, label: this.t('agent_preAIDisabled') },
      ].filter((item) => !item.ok);
    },
    // —— 注册 / 连接健康度（自 AgentActivitySettings 迁入，唯一呈现处）——
    healthVisible() {
      return Boolean(this.status?.registration_state || this.status?.connection_state);
    },
    registrationLabel() {
      const map = {
        registered: 'agent_reg_registered',
        rejected: 'agent_reg_rejected',
        login_required: 'agent_reg_login_required',
        unregistered: 'agent_reg_unregistered',
        not_configured: 'agent_reg_not_configured',
      };
      const key = map[this.status?.registration_state];
      return key ? this.t(key) : '';
    },
    registrationBadgeClass() {
      switch (this.status?.registration_state) {
        case 'registered': return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300';
        case 'rejected': return 'bg-rose-100 text-rose-700 dark:bg-rose-500/15 dark:text-rose-300';
        case 'login_required': return 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300';
        default: return 'bg-slate-200 text-slate-600 dark:bg-slate-700 dark:text-slate-300';
      }
    },
    connectionLabel() {
      const map = {
        connected: 'agent_conn_connected',
        connecting: 'agent_conn_connecting',
        error: 'agent_conn_error',
        disconnected: 'agent_conn_disconnected',
      };
      const key = map[this.status?.connection_state];
      return key ? this.t(key) : '';
    },
    connectionBadgeClass() {
      switch (this.status?.connection_state) {
        case 'connected': return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300';
        case 'connecting': return 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300';
        case 'error': return 'bg-rose-100 text-rose-700 dark:bg-rose-500/15 dark:text-rose-300';
        default: return 'bg-slate-200 text-slate-600 dark:bg-slate-700 dark:text-slate-300';
      }
    },
    canRetry() {
      const r = this.status?.registration_state;
      const c = this.status?.connection_state;
      // login_required / not_configured 需要用户操作（登录/配置），不是重试能解决的
      if (r === 'login_required' || r === 'not_configured') return false;
      return r !== 'registered' || c !== 'connected';
    },
    healthHasProblem() {
      if (!this.healthVisible) return false;
      return this.canRetry
        || this.status?.registration_state !== 'registered'
        || this.status?.connection_state !== 'connected';
    },
```

methods 改造（启停/刷新改走共享状态；重试注册迁入）：

```js
    async refreshStatus() {
      const result = await this.refresh();
      if (!result) {
        this.setFeedback(this.error || this.t('agent_statusFailed'), 'error');
      }
    },
    async turnOnAgent() {
      this.binding = true;
      try {
        this.applyStatus(await enableAgent());
        this.setFeedback(this.t('agent_enableSuccess'), 'success');
      } catch (err) {
        this.setFeedback(err instanceof Error ? err.message : this.t('agent_enableFailed'), 'error');
      } finally {
        this.binding = false;
      }
    },
    async turnOffAgent() {
      this.binding = true;
      try {
        this.applyStatus(await disableAgent());
        this.setFeedback(this.t('agent_disabledFeedback'), 'success');
      } catch (err) {
        this.setFeedback(err instanceof Error ? err.message : this.t('agent_disableFailed'), 'error');
      } finally {
        this.binding = false;
      }
    },
    // 重试注册与「开启」同一接口（POST /api/agent/enable），仅出错态可见
    async retryRegistration() {
      this.retrying = true;
      try {
        this.applyStatus(await enableAgent());
      } catch (_) {
        // 刷新后的 registration/connection 状态会反映结果
      } finally {
        this.retrying = false;
      }
    },
```

`mounted()` 改为（**评审决策：挂载即刷新**——`ensureLoaded` 首次成功后永不重拉，而 agent 状态常被手机端带外改变；`refresh()` 走 loaded=false + ensureLoaded，双组件同时挂载仍由 inFlight 去重为一次请求，新鲜度与「每入口一次请求」两全）：

```js
  mounted() {
    this.refreshStatus();
  },
```

- [ ] **Step 4: 构建验证**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate-agent-tab/app/website
npm run build 2>&1 | tail -3
```

Expected: 构建成功无报错（未用变量/缺失 key 会在 vite 构建或 eslint 阶段暴露）。

- [ ] **Step 5: Commit**

```bash
git add app/website/src/components/settings/AgentSettings.vue app/website/src/i18n/zh.js app/website/src/i18n/en.js
git commit -m "$(cat <<'EOF'
重构：Agent 状态卡收敛重复状态展示，健康度唯一化，历史同步补「仅元信息」提示

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: 前端 — AI 会话单一列表 + 「进行中」徽章

**Files:**
- Modify: `app/website/src/components/settings/AgentActivitySettings.vue`
- Modify: `app/website/src/i18n/zh.js`、`en.js`（agent_activity_* 区块，两文件同位置）

- [ ] **Step 1: i18n 新键（zh/en 同位置，agent_activity_title 附近）**

zh.js：

```js
  agent_activity_running: '进行中',
```

en.js：

```js
  agent_activity_running: 'Running',
```

- [ ] **Step 2: 模板改造**

头部状态徽章（约 :11-15）整块删除；「注册 / 连接健康度」块（约 :33-75）整块删除。

列表行（约 :106-134）按钮内、展开箭头前加「进行中」徽章；`v-memo` 依赖补 `s.__running`：

```html
            <li
              v-for="s in group.sessions"
              :key="s.id"
              v-memo="[s.id, s.updated_at, s.message_count, s.__running, expandedId === s.id]"
            >
```

```html
                <span
                  v-if="s.__running"
                  class="inline-flex shrink-0 items-center gap-1 rounded-full bg-emerald-100 px-2 py-0.5 text-[9px] font-bold text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300"
                >
                  <span class="h-1 w-1 rounded-full bg-emerald-500 motion-safe:animate-pulse"></span>
                  {{ t('agent_activity_running') }}
                </span>
```

计数引用替换（**4 处**）：`:94` `sessions.length` → `mergedSessions.length`；**:85** 空态条件 `v-else-if="!sessions.length"` → `!mergedSessions.length`（关键：历史列表为空但 runtime 有活跃会话时，必须渲染列表而非空态占位，否则「刚创建、尚未入库」的会话永远不可见）；`:186` 分页 `v-if="... sessions.length ..."` → `mergedSessions.length`；`:221` `pageInfo` 的 count 同理。`offline` 标志的赋值逻辑不动（语义=历史接口不可达时的提示），仅上述长度判断切到 merged。

- [ ] **Step 3: script 改造**

import 改为：

```js
import { getAgentRuntimeSessions, getAgentSessions, getAgentSessionDetail } from '../../services/agentApi';
```

`data()` 删除 `status: null`、`retrying: false`，新增 `runtimeAi: []` 与 `pollTimer: null`。

computed 删除：`runtimeOnline`、`agentEnabled`、`statusLabel`、`statusBadgeClass`、`healthVisible`、`registrationLabel`、`registrationBadgeClass`、`connectionLabel`、`connectionBadgeClass`、`canRetry`。新增：

```js
    runningIds() {
      return new Set(this.runtimeAi.map((session) => session.id));
    },
    decoratedSessions() {
      const historyIds = new Set(this.sessions.map((session) => session.id));
      const runtimeOnly = this.runtimeAi
        .filter((session) => !historyIds.has(session.id))
        .map((session) => ({ ...session, __running: true }));
      return [
        ...runtimeOnly,
        ...this.sessions.map((session) => ({
          ...session,
          __running: this.runningIds.has(session.id),
        })),
      ];
    },
    // 单一列表：运行中会话置顶，其余按 updated_at 原序
    mergedSessions() {
      const running = [];
      const rest = [];
      for (const session of this.decoratedSessions) {
        (session.__running ? running : rest).push(session);
      }
      return [...running, ...rest];
    },
```

`totalPages` / `pagedSessions` / `groupedPagedSessions` 的数据源 `this.sessions` 全部改为 `this.mergedSessions`（`totalPages` 的 length 同理）。

methods 删除 `loadStatus`、`retryRegistration`。新增：

```js
    // runtime-sessions 仅服务于「进行中」徽章的时效性；失败不阻塞历史列表
    async loadRuntimeAi() {
      try {
        const data = await getAgentRuntimeSessions();
        this.runtimeAi = Array.isArray(data?.ai_conversations) ? data.ai_conversations : [];
      } catch (_) {
        // 徽章缺失可接受
      }
    },
```

`mounted` / `beforeUnmount`：

```js
  mounted() {
    this.loadSessions();
    this.loadRuntimeAi();
    this.pollTimer = window.setInterval(() => {
      if (document.visibilityState === 'visible') this.loadRuntimeAi();
    }, 5000);
  },
  beforeUnmount() {
    if (this.pollTimer) window.clearInterval(this.pollTimer);
  },
```

- [ ] **Step 4: 构建验证**

```bash
npm run build 2>&1 | tail -3
```

Expected: 构建成功。

- [ ] **Step 5: Commit**

```bash
git add app/website/src/components/settings/AgentActivitySettings.vue app/website/src/i18n/zh.js app/website/src/i18n/en.js
git commit -m "$(cat <<'EOF'
重构：AI 会话合并为单一列表，运行中会话徽章置顶，移除与健康度/状态卡重复的区块

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: 前端 — 终端卡独立 + scrollback 查看弹窗

**Files:**
- Modify: `app/website/src/components/settings/AgentRuntimeSessions.vue`
- Modify: `app/website/src/i18n/zh.js`、`en.js`（agent_live_* 区块，两文件同位置）

- [ ] **Step 1: i18n 新键（zh/en 同位置，agent_live_* 区块内）**

zh.js：

```js
  agent_live_output: '查看输出',
  agent_live_outputTitle: '终端输出',
  agent_live_outputEmpty: '暂无输出',
  agent_live_outputLive: '输出中',
  agent_live_outputExited: '已退出',
  agent_live_outputTruncated: '仅显示最近部分输出',
  agent_live_outputLoadFailed: '终端输出读取失败',
  agent_live_outputRefresh: '刷新',
```

en.js：

```js
  agent_live_output: 'View output',
  agent_live_outputTitle: 'Terminal output',
  agent_live_outputEmpty: 'No output yet',
  agent_live_outputLive: 'Live',
  agent_live_outputExited: 'Exited',
  agent_live_outputTruncated: 'Showing most recent output only',
  agent_live_outputLoadFailed: 'Failed to load terminal output',
  agent_live_outputRefresh: 'Refresh',
```

- [ ] **Step 2: 模板改造**

分区标题（约 :6-12）：`t('agent_live_title')` 改为 `t('agent_live_terminalTitle')`，描述改 `t('agent_live_terminalDesc')`（组件瘦身为纯终端卡，保留实时脉冲徽章与刷新按钮）。

AI 对话卡整块删除：`<article>`（约 :33-81，smart_toy 图标那张）。外层 `grid grid-cols-1 gap-5 lg:grid-cols-2` 容器（:32）随之删除（只剩一张卡，直接让 `<article>` 占满宽度）；终端卡的 `<header>`（终端图标+`agent_live_terminalTitle`+描述+计数徽章，约 :84-97）整块删除——标题/描述已在分区标题出现，删除后避免重复（分区标题的 `agent_live_terminalTitle` 改造保留）。为不丢信息：分区标题行（实时徽章旁）补终端计数徽章 `<span class="rounded-full bg-slate-100 px-2 py-0.5 text-[10px] font-bold text-slate-600 dark:bg-slate-800 dark:text-slate-300">{{ terminals.length }}</span>`；原 grid 容器上的 `aria-live="polite"` 迁移到终端 `<article>` 上。

终端行（约 :107-130）在 cwd 段落之后加查看按钮：

```html
              <button
                type="button"
                class="mt-2 inline-flex min-h-9 items-center gap-1 rounded-lg border border-slate-200 px-2.5 text-[10px] font-bold text-slate-600 transition-colors hover:border-primary/40 hover:bg-primary/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-primary/10"
                @click.stop="openScrollback(terminal)"
              >
                <span class="material-symbols-outlined text-sm">visibility</span>
                {{ t('agent_live_output') }}
              </button>
```

`</section>` 之前追加 scrollback 弹窗（遵循移动端约定：dvh + flex-col + min-h-0 内滚 + 触控 ≥40px）：

```html
    <div
      v-if="scrollbackOpen"
      class="fixed inset-0 z-[1090] flex items-end justify-center bg-slate-950/65 backdrop-blur-sm sm:items-center sm:p-4"
      role="dialog"
      aria-modal="true"
      :aria-label="t('agent_live_outputTitle')"
      @click.self="closeScrollback"
    >
      <div class="flex h-[85dvh] w-full max-w-3xl flex-col overflow-hidden rounded-t-2xl border border-slate-200 bg-white shadow-2xl dark:border-slate-700 dark:bg-slate-900 sm:h-[80dvh] sm:rounded-2xl">
        <div class="flex shrink-0 items-center justify-between gap-3 border-b border-slate-200 bg-slate-50/85 px-4 py-3 dark:border-slate-700 dark:bg-slate-800/65">
          <div class="flex min-w-0 items-center gap-2">
            <h3 class="truncate text-sm font-bold text-slate-900 dark:text-slate-100">{{ t('agent_live_outputTitle') }}</h3>
            <span
              v-if="scrollbackStatus"
              class="shrink-0 rounded-full px-2 py-0.5 text-[10px] font-bold"
              :class="scrollbackStatus === 'live'
                ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300'
                : 'bg-slate-200 text-slate-600 dark:bg-slate-700 dark:text-slate-300'"
            >
              {{ scrollbackStatus === 'live' ? t('agent_live_outputLive') : t('agent_live_outputExited') }}
            </span>
            <span v-if="scrollbackTruncated" class="hidden text-[10px] text-slate-400 sm:inline">{{ t('agent_live_outputTruncated') }}</span>
          </div>
          <div class="flex shrink-0 items-center gap-1">
            <button
              type="button"
              class="inline-flex h-11 items-center gap-1 rounded-lg px-2.5 text-xs font-bold text-slate-600 transition-colors hover:bg-slate-100 disabled:opacity-60 dark:text-slate-300 dark:hover:bg-slate-800"
              :disabled="scrollbackLoading"
              @click="fetchScrollback"
            >
              <span class="material-symbols-outlined text-base" :class="{ 'motion-safe:animate-spin': scrollbackLoading }">refresh</span>
              {{ t('agent_live_outputRefresh') }}
            </button>
            <button
              type="button"
              class="flex h-11 w-11 items-center justify-center rounded-lg text-slate-500 transition-colors hover:bg-slate-100 hover:text-slate-700 dark:hover:bg-slate-800 dark:hover:text-slate-200"
              :aria-label="t('agent_closeSettings')"
              @click="closeScrollback"
            >
              <span class="material-symbols-outlined text-lg">close</span>
            </button>
          </div>
        </div>
        <div class="min-h-0 flex-1 overflow-auto bg-slate-950 p-4">
          <p v-if="scrollbackLoading && !scrollbackData" class="text-xs text-slate-400">{{ t('agent_live_loading') }}</p>
          <p v-else-if="scrollbackError" class="text-xs text-rose-300">{{ scrollbackError }}</p>
          <p v-else-if="!scrollbackData" class="text-xs text-slate-500">{{ t('agent_live_outputEmpty') }}</p>
          <pre v-else class="whitespace-pre-wrap break-words font-mono text-[11px] leading-5 text-slate-100">{{ scrollbackData }}</pre>
        </div>
        <p v-if="scrollbackTruncated" class="shrink-0 border-t border-slate-800 px-4 py-2 text-[10px] text-slate-400 sm:hidden">{{ t('agent_live_outputTruncated') }}</p>
      </div>
    </div>
```

- [ ] **Step 3: script 改造**

import 改为：

```js
import { getAgentRuntimeSessions, getAgentTerminalScrollback } from '../../services/agentApi';
```

`data()` 删除 `aiConversations: []`，新增：

```js
      scrollbackOpen: false,
      scrollbackLoading: false,
      scrollbackData: '',
      scrollbackStatus: '',
      scrollbackTruncated: false,
      scrollbackError: '',
      scrollbackSessionId: '',
```

`loadRuntimeSessions` 中删除 ai_conversations 赋值与 getAgentSessions 兜底（组件不再消费 AI 会话），失败仅记 error：

```js
    async loadRuntimeSessions() {
      if (this.loading) return;
      this.loading = true;
      this.error = '';
      try {
        const data = await getAgentRuntimeSessions();
        this.terminals = Array.isArray(data?.terminals) ? data.terminals : [];
        this.collectedAt = data?.collected_at || new Date().toISOString();
      } catch (_) {
        this.error = this.t('agent_live_loadFailed');
      } finally {
        this.hasLoaded = true;
        this.loading = false;
      }
    },
```

methods 删除 `sessionTitle`、`aiMeta`，新增：

```js
    async openScrollback(terminal) {
      this.scrollbackSessionId = terminal.id;
      this.scrollbackOpen = true;
      this.scrollbackData = '';
      this.scrollbackError = '';
      this.scrollbackStatus = '';
      this.scrollbackTruncated = false;
      await this.fetchScrollback();
    },
    async fetchScrollback() {
      if (!this.scrollbackSessionId || this.scrollbackLoading) return;
      this.scrollbackLoading = true;
      try {
        const data = await getAgentTerminalScrollback(this.scrollbackSessionId);
        this.scrollbackData = data?.data || '';
        this.scrollbackStatus = data?.status || '';
        this.scrollbackTruncated = Boolean(data?.truncated);
        this.scrollbackError = '';
      } catch (_) {
        this.scrollbackError = this.t('agent_live_outputLoadFailed');
      } finally {
        this.scrollbackLoading = false;
      }
    },
    closeScrollback() {
      this.scrollbackOpen = false;
    },
```

- [ ] **Step 4: 构建验证**

```bash
npm run build 2>&1 | tail -3
```

Expected: 构建成功。

- [ ] **Step 5: Commit**

```bash
git add app/website/src/components/settings/AgentRuntimeSessions.vue app/website/src/i18n/zh.js app/website/src/i18n/en.js
git commit -m "$(cat <<'EOF'
重构：终端卡独立并支持查看输出回放（消费 scrollback 端点）

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: 全量验证

**Files:** 无新改动（只验证）

- [ ] **Step 1: Go 全量（注意预存 flake：agent_ai 子进程测试高并行偶败，单包复跑即绿，见仓库惯例）**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate-agent-tab
go build ./... && go vet ./app/http/... && go test ./app/http/services/ ./app/http/handlers/ 2>&1 | tail -5
```

Expected: BUILD/vet 无错；两个包 PASS（如遇 agent_ai 子进程超时 flake，复跑该包一次确认非本次引入）。

- [ ] **Step 2: 前端单测**

```bash
cd app/website && npm test 2>&1 | tail -5
```

Expected: 全绿（含新 useAgentStatus 3 例）。

- [ ] **Step 3: e2e 哨兵（移动端适配约定：390/768/1280 必须过）**

```bash
npm run test:e2e 2>&1 | tail -8
```

Expected: `mobile-responsive.spec.js` 通过（弹窗为 dvh+flex-col+min-h-0 结构、触控目标 ≥40px，已按约定实现）。

- [ ] **Step 4: 生产构建**

```bash
npm run build 2>&1 | tail -3
```

Expected: 构建成功。

- [ ] **Step 5: 手动走查清单（dev server）**

```bash
npm run dev
```

按 spec §5 走查：未登录占位 → 登录后三段渲染 → 启停按钮 → 历史会话点击回放 → 运行中徽章置顶 → 终端「查看输出」弹窗 → 历史同步小字可见 → 390px 宽弹窗内滚正常。

- [ ] **Step 6: 收尾（分支留存，由用户决定合并）**

不自动合并、不自动 push。向用户汇报分支名 `feat/agent-tab-redesign` 与全部提交清单。

## 8. 执行后遗留账（最终评审归档，合并时一并决策）

以下事项经评审明确 defer，合并后对话上下文即丢失，此处为仓库内唯一记录：

| # | 事项 | 现状 | 建议去向 |
|---|---|---|---|
| 1 | 终端「查看输出」按钮/弹窗 aria 细化（按钮有可见文本；无关联描述） | 未实现 | a11y 打磨批量 |
| 2 | 两处弹窗（AgentSettings 设置、终端输出）均无 Escape 关闭（QuickSetupModal 有先例） | 未实现 | 弹窗 UX 批量 |
| 3 | AgentActivitySettings 与 AgentRuntimeSessions 双组件各自 5s 轮询 /api/agent/runtime-sessions | 有意保留（前者取 ai_conversations 做徽章，后者取 terminals；合并需共享轮询 store） | 轮询整合任务 |
| 4 | 终端输出弹窗原样渲染 ANSI 转义序列（ring buffer 为原始字节）；合适修复层在后端 `newAgentTerminalScrollback` 做 CSI/OSC 剥离，使 `encoding:"text"` 名副其实 | 未实现 | 后端小任务 |
| 5 | `common.Error(w, http.StatusMethodNotAllowed, ...)` 全仓 ~80 处把真实 405 映射成 500（ErrorCodeToHTTPStatus 落 default）；新 handler 已用 http.Error 绕开并留注释 | 家族性待修（最小修法：ErrorCodeToHTTPStatus 对 ≥400 的原生 HTTP 状态直通） | 独立清理任务 |

另记录（最终评审 M 级发现，合并决策时向用户明示）：页面段落顺序现状为「状态→终端→AI 会话」（master 原序），spec §2 箭头写的是「状态→AI 会话→终端」——SettingsPage 不在文件清单内，属可辩护解读，顺序是否调整由用户定。
