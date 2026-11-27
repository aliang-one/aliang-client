# 远程协助 P1(终端腿全链)实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 客户端仪表盘一键申请协助 → admin 审批(跨仓,本计划交付设备侧+协议契约)→ 协助会话内 admin 经云端在客户端家目录操作真终端,客户端观看页实时只读回显,会话结束全量回收。

**Architecture:** 设备 core 是会话权威:新增 `remoteAssistManager` 管状态机(requested/approved/active/ended),经既有 device↔cloud remote WS(`handleRemoteAgentMessage` 帧分派)收发 `assist.*` 帧;协助终端复用 `agentTerminalManager`(session id `assist-<id>` 前缀路由到 assist 门控分支,手机式 detach 保活);终端输出用复合 writer 同时上行云端(给 admin)与本机扇出(给观看页)。观看页 WS 为纯服务端推送(入站全忽略),结构性只读。

**Tech Stack:** Go(gorilla/websocket 既有)、Vue3.5+Vite5+Tailwind(app/website)、@xterm/xterm ^6(已装)、vitest。

**Spec:** `docs/superpowers/specs/2026-11-26-remote-assist-design.md`(commit fbec4263)

**范围边界(先读):**
- 本计划=spec §15 的 P1,只做终端腿。页面腿(rrweb)是 P2 另立计划;admin 控制台与云端房间是平台侧仓库,本计划只交付 §A 帧契约(附录),对方照契约实现。
- 真机端到端验收(申请→审批→操作)需平台侧就绪后进行;本计划的完成线=设备侧全绿+契约测试全绿+前端 vitest 全绿。
- 实现前先跑: `go build ./...` 确认基线绿;测试一律 `go test ./app/http/services/ -run <name>` 风格(本仓 vitest 须 cd `app/website` 跑,worktree 历史坑)。

---

## 文件结构总览

| 动作 | 文件 | 职责 |
|---|---|---|
| 新建 | `app/http/services/agent_remote_assist.go` | 会话状态机权威 + assist 帧收发 + 终端腿接线 + 观看连接注册表 + 复合 writer |
| 新建 | `app/http/services/agent_remote_assist_test.go` | 状态机/门控/终端腿/扇出全套测试 |
| 修改 | `app/http/models/agent_protocol.go` | assist 帧族常量(既有 terminal 帧复用,不加终端新帧) |
| 新建 | `app/http/handlers/agent_handler_assist.go` | 三 HTTP 端点 + 观看 WS handler(照 web-terminal handler 模板) |
| 新建 | `app/http/handlers/agent_handler_assist_test.go` | 闸门矩阵/双注册/透传测试 |
| 修改 | `app/http/routes/routes.go` + `app/agentruntime/server.go` | 双注册(同批提交,route_parity_test 兜底) |
| 修改 | `app/http/services/agent_remote_ws.go` | `handleRemoteAgentMessage` 加 assist case + requires-enabled 集合扩充 |
| 修改 | `app/website/src/...`(见 Task 9/10) | 入口按钮+弹窗+观看页+composable+i18n |

**不动**:`agent_terminal.go` 引擎主体(仅允许加只读 `hasHeadroom()` 小方法,Task 5)、`agent_web_terminal.go`(vault 与 `DispatchTerminalFrame` 本体——门控加在 remote-WS 调用点,见 Task 5)、手机腿既有分派逻辑。

---

### Task 1: 协议帧族常量

**Files:**
- Modify: `app/http/models/agent_protocol.go:25-35` 旁(既有 terminal 常量族处)
- Test: `app/http/models/agent_protocol_test.go`(若已有则追加,没有则新建)

- [ ] **Step 1: 写失败测试**(帧型常量与 JSON 往返)

```go
func TestAssistFrameTypeConstants(t *testing.T) {
	// 锚定字符串本身:云端/平台侧按契约实现,字符串漂移=跨仓断链
	cases := map[string]string{
		models.AgentEventAssistRequestUp: "assist.request",
		models.AgentEventAssistCancel:    "assist.cancel",
		models.AgentEventAssistApproved:  "assist.approved",
		models.AgentEventAssistRejected:  "assist.rejected",
		models.AgentEventAssistEnd:       "assist.end",
		models.AgentEventAssistState:     "assist.state",
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("assist frame constant drift: got %q want %q", got, want)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/models/ -run TestAssistFrameTypeConstants -v`
Expected: FAIL(常量未定义,编译错误)

- [ ] **Step 3: 最小实现**——在 `agent_protocol.go` 的 terminal 常量块后追加:

```go
// 远程协助帧族(remote-assist P1)。终端面复用既有 terminal.* 帧族
// (session_id 带 assist- 前缀即路由 assist 门控),本族只承载会话信令。
const (
	AgentEventAssistRequestUp = "assist.request"  // 设备→云端:申请协助(带留言)
	AgentEventAssistCancel    = "assist.cancel"   // 设备→云端:撤销申请/结束会话
	AgentEventAssistApproved  = "assist.approved" // 云端→设备:admin 已批准(带 session_id)
	AgentEventAssistRejected  = "assist.rejected" // 云端→设备:admin 已拒绝
	AgentEventAssistEnd       = "assist.end"      // 双向:任一侧结束(带 reason,见 assistEndReasons)
	AgentEventAssistState     = "assist.state"    // 云端→设备:房间状态镜像(预留,P1 仅记录日志)
)
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/models/ -run TestAssistFrameTypeConstants -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git commit -o app/http/models/agent_protocol.go app/http/models/agent_protocol_test.go -m "远程协助P1:assist帧族常量与契约锚定测试"
```

(注:本仓提交规范=全中文信息;commit 用 `-o <路径>` 限定路径,防并行会话暂存混包——下同,不再重复标注。)

---

### Task 2: 会话状态机核心(纯逻辑)

**Files:**
- Create: `app/http/services/agent_remote_assist.go`
- Test: `app/http/services/agent_remote_assist_test.go`

设计要点:状态机与网络/终端完全解耦(clock 注入),网络接线在 Task 4/5 挂上去。

- [ ] **Step 1: 写失败测试**(迁移矩阵 + 守卫)

```go
package services

import (
	"testing"
	"time"
)

func newTestAssistManager() *remoteAssistManager {
	now := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	return newRemoteAssistManager(now)
}

func TestAssistStateMachineHappyPath(t *testing.T) {
	m := newTestAssistManager()
	sid, err := m.Request("请帮我看下配置")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if !strings.HasPrefix(sid, "assist-") {
		t.Fatalf("session id must be prefixed assist-: %q", sid)
	}
	if got := m.Snapshot().State; got != assistStateRequested {
		t.Fatalf("after request state=%q", got)
	}
	// 同态重复请求=更新留言不新建
	sid2, _ := m.Request("改留言")
	if sid2 != sid {
		t.Fatalf("duplicate request must reuse id: %q vs %q", sid2, sid)
	}
	if err := m.CloudApproved(sid); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if got := m.Snapshot().State; got != assistStateActive {
		t.Fatalf("after approve state=%q", got)
	}
	if err := m.End(AssistEndAdmin); err != nil {
		t.Fatalf("end: %v", err)
	}
	snap := m.Snapshot()
	if snap.State != assistStateEnded || snap.EndReason != AssistEndAdmin {
		t.Fatalf("after end %+v", snap)
	}
}

func TestAssistStateMachineGuards(t *testing.T) {
	m := newTestAssistManager()
	// idle 态不能 approve/end
	if err := m.CloudApproved("assist-x"); err == nil {
		t.Fatal("approve from idle must fail")
	}
	if err := m.End(AssistEndClient); err == nil {
		t.Fatal("end from idle must fail")
	}
	sid, _ := m.Request("")
	_ = m.CloudApproved(sid)
	// active 态不能再 Request(单设备单活跃)
	if _, err := m.Request("second"); err == nil {
		t.Fatal("request while active must fail")
	}
	// ended 后进入 60s 冷却
	_ = m.End(AssistEndClient)
	if _, err := m.Request("cooldown"); err == nil {
		t.Fatal("request during cooldown must fail")
	}
}

func TestAssistPendingExpiry(t *testing.T) {
	m := newTestAssistManager()
	sid, _ := m.Request("")
	m.AdvanceClock(6 * time.Minute) // pending 5m 过期
	snap := m.Snapshot()
	if snap.State != assistStateEnded || snap.EndReason != AssistEndExpired {
		t.Fatalf("pending must expire to ended/expired, got %+v", snap)
	}
	// 过期后可重新申请
	if _, err := m.Request("again"); err != nil {
		t.Fatalf("request after expiry: %v", err)
	}
}

func TestAssistDurationCap(t *testing.T) {
	m := newTestAssistManager()
	sid, _ := m.Request("")
	_ = m.CloudApproved(sid)
	m.AdvanceClock(2*time.Hour + time.Minute)
	if snap := m.Snapshot(); snap.EndReason != AssistEndDurationCap {
		t.Fatalf("duration cap must fire, got %+v", snap)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestAssist -v`
Expected: FAIL(类型未定义)

- [ ] **Step 3: 最小实现**

```go
package services

// remote-assist P1:会话状态机权威。纯逻辑,clock 注入可测;网络信令与
// 终端接线由 assistOnRemoteFrame / activateTerminal 挂接(Task 4/5)。
// 威胁模型与授权边界见 spec docs/superpowers/specs/2026-11-26-remote-assist-design.md §10。

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	assistIDPrefix        = "assist-"
	assistPendingExpiry   = 5 * time.Minute
	assistDurationCap     = 2 * time.Hour
	assistRequestCooldown = 60 * time.Second
	assistAdminAbsentCap  = 10 * time.Minute // 判定在云端(admin WS 在云端),设备只收终帧
)

type assistState string

const (
	assistStateIdle      assistState = ""
	assistStateRequested assistState = "requested"
	assistStateActive    assistState = "active"
	assistStateEnded     assistState = "ended"
)

// assistEndReason 六个 spec §5 终态原因 + 设备侧创建失败补充,字符串即契约。
type assistEndReason string

const (
	AssistEndAdmin         assistEndReason = "admin_end"
	AssistEndClient        assistEndReason = "client_end"
	AssistEndOffline       assistEndReason = "device_offline"
	AssistEndAdminTimeout  assistEndReason = "admin_timeout"
	AssistEndDurationCap   assistEndReason = "duration_cap"
	AssistEndDeviceRestart assistEndReason = "device_restart"
	AssistEndExpired       assistEndReason = "pending_expired"
	AssistEndTerminalLimit assistEndReason = "terminal_limit"
)

type remoteAssistSnapshot struct {
	SessionID   string          `json:"session_id"`
	State       assistState     `json:"state"`
	Message     string          `json:"message"`
	EndReason   assistEndReason `json:"end_reason,omitempty"`
	RequestedAt string          `json:"requested_at,omitempty"`
	ApprovedAt  string          `json:"approved_at,omitempty"`
	EndedAt     string          `json:"ended_at,omitempty"`
}

type remoteAssistSession struct {
	id          string
	message     string
	requestedAt time.Time
	approvedAt  time.Time
}

type remoteAssistManager struct {
	mu        sync.Mutex
	now       func() time.Time
	lastEnded time.Time // 冷却锚点
	session   *remoteAssistSession
	endReason assistEndReason
}

func newRemoteAssistManager(now func() time.Time) *remoteAssistManager {
	return &remoteAssistManager{now: now}
}

var (
	errAssistCooldown    = errors.New("assist: request cooldown")
	errAssistBusy        = errors.New("assist: session already active or pending")
	errAssistNotPending  = errors.New("assist: no pending session")
	errAssistNotActive   = errors.New("assist: no active session")
	errAssistIDMismatch  = errors.New("assist: session id mismatch")
	errAssistNothingToEnd = errors.New("assist: nothing to end")
)

func (m *remoteAssistManager) Request(message string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	// 冷却窗口从「回到 idle」起算:requested 撤销/终态均可立即重申——冷却只
	// 防终态后的连发骚扰(spec §10 限频)。
	if m.session == nil && !m.lastEnded.IsZero() && now.Sub(m.lastEnded) < assistRequestCooldown {
		return "", errAssistCooldown
	}
	if m.session != nil {
		switch m.stateLocked() {
		case assistStateRequested:
			m.session.message = strings.TrimSpace(message) // 同态更新留言
			return m.session.id, nil
		case assistStateActive:
			return "", errAssistBusy
		}
	}
	id := fmt.Sprintf("%s%d", assistIDPrefix, now.UnixNano())
	m.session = &remoteAssistSession{id: id, message: strings.TrimSpace(message), requestedAt: now}
	m.endReason = ""
	return id, nil
}

func (m *remoteAssistManager) Cancel() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stateLocked() != assistStateRequested {
		return errAssistNotPending
	}
	m.finishLocked("", AssistEndClient)
	return nil
}

func (m *remoteAssistManager) CloudApproved(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stateLocked() != assistStateRequested {
		return errAssistNotPending
	}
	if m.session.id != sessionID {
		return errAssistIDMismatch
	}
	m.session.approvedAt = m.now()
	return nil
}

func (m *remoteAssistManager) CloudRejected(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stateLocked() != assistStateRequested || m.session.id != sessionID {
		return errAssistNotPending
	}
	m.finishLocked("", AssistEndAdmin) // 拒绝按 admin 侧终态计,审计同源
	return nil
}

func (m *remoteAssistManager) End(reason assistEndReason) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stateLocked() == assistStateIdle {
		return errAssistNothingToEnd
	}
	m.finishLocked("", reason)
	return nil
}

func (m *remoteAssistManager) CloudEnd(sessionID string, reason assistEndReason) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stateLocked() == assistStateIdle || (m.session != nil && m.session.id != sessionID) {
		return errAssistIDMismatch
	}
	m.finishLocked("", reason)
	return nil
}

func (m *remoteAssistManager) Sweep() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	switch m.stateLocked() {
	case assistStateRequested:
		if now.Sub(m.session.requestedAt) >= assistPendingExpiry {
			m.finishLocked("", AssistEndExpired)
		}
	case assistStateActive:
		if now.Sub(m.session.approvedAt) >= assistDurationCap {
			m.finishLocked("", AssistEndDurationCap)
		}
	}
}

// stateLocked 空会话=idle;会话存在则按终态标记区分。
func (m *remoteAssistManager) stateLocked() assistState {
	if m.session == nil {
		return assistStateIdle
	}
	if m.endReason != "" {
		return assistStateEnded
	}
	if m.session.approvedAt.IsZero() {
		return assistStateRequested
	}
	return assistStateActive
}

func (m *remoteAssistManager) finishLocked(_ string, reason assistEndReason) {
	if m.session != nil {
		m.lastEnded = m.now()
		m.endReason = reason
		m.session = nil // 终态快照由调用方先取;会话槽位立即释放回 idle+冷却
	}
}
```

(注:`Snapshot()`/`AdvanceClock()` 未含在上文——Snapshot 返回 `remoteAssistSnapshot`(组装+RFC3339);**AdvanceClock 推进时钟后立即触发 `Sweep()`**(或 Snapshot 惰性评估过期,二选一实现,语义等价——上面的过期/上限测试依赖此语义);实现时补齐并让上面测试全绿。终态即释放槽位是刻意决策:观看页/审计需要的历史由各自面持有,状态机保持最小态。)

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services/ -run TestAssist -v`
Expected: PASS(4 个测试全绿)

- [ ] **Step 5: Commit**

```bash
git commit -o app/http/services/agent_remote_assist.go app/http/services/agent_remote_assist_test.go -m "远程协助P1:会话状态机权威(纯逻辑,clock注入可测)"
```

---

### Task 3: 挂到 AgentService + 总开关与 gates API

**Files:**
- Modify: `app/http/services/agent_service.go`(AgentService 结构加字段,照 `webTerminal` 字段与 `newWebTerminalVault()` 接线模式,见 `agent_service.go:272-283`)
- Modify: `app/http/services/agent_service.go`(持久化配置:照 `remote_terminal_enabled` 的读写与保存路径,**零值必须=false**;实测锚点≈`agent_service.go:1422` 的缺省 true 分支——`if _, ok := persisted.Device["remote_terminal_enabled"]; !ok {…=true}`,**切勿镜像该模式**——与 ≈`:2755` 的 settings 应用处;spec §14 坑 6。**评审勘误(Task 3 实施期裁决)**:`persisted.Device` 是结构体且注册(`:1826`)与每次状态同步(`syncAgentInventoryLocked :2482`)都会被服务端回显设备对象**整体替换**,注册上行载荷只含 device_id 平台无从回显本地键——开关若放 Device 内会被每次同步抹回 false。正确落点=`agentState` 顶层字段(`json:"remote_assist_enabled"` 无 omitempty),与 webTerminal 独立 state 文件同款纪律(agent_web_terminal.go:5-6 先例注释);Task 3 已按此实施并通过规格评审)
- Create: `app/http/handlers/agent_handler_assist.go`(本 Task 先只放 gates 端点)
- Modify: `app/http/routes/routes.go:324` 旁 + `app/agentruntime/server.go:177` 旁(双注册)
- Test: `app/http/services/agent_service_test.go` 追加 + `app/http/handlers/agent_handler_assist_test.go`

- [ ] **Step 1: 写失败测试**(开关缺省 false + 持久化往返 + 端点闸)

```go
func TestRemoteAssistEnabledDefaultsFalse(t *testing.T) {
	// 零值纪律:新装设备未配置时必须是 false(照 web_terminal_enabled 同款断言)
	s := newAssistTestService(t) // 照 agent_service_test.go 既有夹具构造
	if s.RemoteAssistEnabled() {
		t.Fatal("remote_assist_enabled must default to false")
	}
}

func TestHandleAssistGatesToggle(t *testing.T) {
	// GET 返回 {enabled:false};POST {enabled:true} 后 GET 变 true;重启夹具后仍 true(持久化)
}
```

(`newAssistTestService` 照既有 `agent_service_test.go` 构造夹具;持久化断言复用该文件的 state 落盘/重载手法。)

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestRemoteAssistEnabled -v`
Expected: FAIL

- [ ] **Step 3: 实现**

1. `AgentService` 加字段 `assist *remoteAssistManager`,在 `NewAgentService()` 构造(`agent_service.go:266` 旁)用真实时钟初始化。
2. 持久化:设备 state 配置加 `remote_assist_enabled`(照 `remote_terminal_enabled` 的读写与保存路径;**不进任何缺省 true 分支**)。
3. `RemoteAssistEnabled()` 读侧 + `SetRemoteAssistEnabled(bool)` 写侧(写侧翻转 false 时若会话活跃→`End(AssistEndClient)` 并关终端,照 `webTerminal.onLock → closeAll` 联动模式 `agent_service.go:282`)。
4. handler:`HandleAssistGates`(GET/POST,照 `HandlePeerGates` 形)+ core 腿透传(`proxyIfNeeded`,普通 HTTP)。
5. 双注册两处(route_parity_test 会兜底,红=漏一边)。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services/ -run TestRemoteAssistEnabled -v && go test ./app/http/handlers/ -run TestHandleAssistGates -v && go test ./app/agentruntime/ -run TestAgentRouteParity -v`
Expected: PASS ×3

- [ ] **Step 5: Commit**

```bash
git commit -o app/http/services/agent_service.go app/http/handlers/agent_handler_assist.go app/http/handlers/agent_handler_assist_test.go app/http/routes/routes.go app/agentruntime/server.go -m "远程协助P1:总开关+持久化+gates端点(双注册)"
```

---

### Task 4: 云端信令接线(帧收发 + 上行 writer 缝)

**Files:**
- Modify: `app/http/services/agent_remote_assist.go`(加帧处理)
- Modify: `app/http/services/agent_remote_ws.go:730` 旁(`handleRemoteAgentMessage` 加 case;`remoteAgentMessageRequiresEnabledDevice` 集合加 assist 帧型)
- Test: `app/http/services/agent_remote_assist_test.go`

上行 writer 照 `agentPunchFrameWriter` 缝范式(`agent_peer_punch.go:1440`):生产=`currentRemoteWriterFunc()`,测试缝=注入 sink。**不新增全局可变缝给 assist 之外的路径**。

- [ ] **Step 1: 写失败测试**(帧驱动状态机 + 回帧形状)

```go
func TestAssistRequestFrameFlow(t *testing.T) {
	m := newTestAssistManagerWithSink(t) // 注入上行 sink,收集外发帧
	var upFrames []map[string]interface{}
	m.SetFrameSink(func(f map[string]interface{}) error { upFrames = append(upFrames, f); return nil })

	// 设备侧 Request → 上行 assist.request{session_id,message}
	sid, err := m.RequestAndSignal("帮我看下")
	if err != nil { t.Fatal(err) }
	if len(upFrames) != 1 || upFrames[0]["type"] != models.AgentEventAssistRequestUp {
		t.Fatalf("must uplink exactly one assist.request, got %v", upFrames)
	}

	// 云端回 approved(带同 session_id)→ active + 终端激活回调被调
	activated := false
	m.OnActivated(func(id string) { activated = id == sid })
	frames, err := m.OnRemoteFrame(map[string]interface{}{
		"type": models.AgentEventAssistApproved, "session_id": sid,
	})
	if err != nil { t.Fatal(err) }
	if !activated { t.Fatal("activation hook must fire on approved") }

	// session_id 不匹配的 approved 必须拒(防串会话)
	if _, err := m.OnRemoteFrame(map[string]interface{}{
		"type": models.AgentEventAssistApproved, "session_id": "assist-other",
	}); err == nil {
		t.Fatal("mismatched approved must fail")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestAssistRequestFrameFlow -v`
Expected: FAIL

- [ ] **Step 3: 实现**

1. `remoteAssistManager` 加:`frameSink`(测试缝,生产 nil)、`onActivated`/`onEnded` 钩子(终端腿/观看页订阅)、`RequestAndSignal/CancelAndSignal/EndAndSignal`(状态迁移成功后经 `assistWriter()` 上行对应帧)、`OnRemoteFrame(msg map[string]interface{}) ([]map[string]interface{}, error)`(处理 approved/rejected/end/state 四种入帧,回需要写回云端的帧)。
2. `assistWriter()`:`agentPunchFrameWriter` 同构——sink 优先,否则 `currentRemoteWriterFunc()`,nil 时返回错误(信令发不出去,Request 直接失败让用户看到,不静默)。
3. `handleRemoteAgentMessage` 加 case:`models.AgentEventAssistApproved/Rejected/End/State` → `s.assist.OnRemoteFrame(msg)` → 回帧经该连接 writeJSON 上行。**enabled 门**:把四个帧型加进 `remoteAgentMessageRequiresEnabledDevice` 的集合(设备禁用态不接受协助)。
4. 设备 disable/登出路径(`closeAllWithReason` 同族处)追加:活跃 assist 会话 `End(AssistEndClient)`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services/ -run TestAssist -v`
Expected: PASS(含 Task 2 既有测试)

- [ ] **Step 5: Commit**

```bash
git commit -o app/http/services/agent_remote_assist.go app/http/services/agent_remote_assist_test.go app/http/services/agent_remote_ws.go -m "远程协助P1:云端信令接线(帧分派+上行writer缝+enabled门)"
```

---

### Task 5: 协助终端腿(approved 激活 + session 前缀门控)

**Files:**
- Modify: `app/http/services/agent_remote_assist.go`(激活/回收接线)
- Modify: `app/http/services/agent_remote_ws.go`(≈`:1016-1021` 的 terminal 帧分派调用点:按 session_id 前缀 `assist-` 先行检查再委托 `s.DispatchTerminalFrame`,其余走手机腿零改动)。**`DispatchTerminalFrame` 本体在 `agent_web_terminal.go:457`(vault 同文件)——本任务不动该文件**,门控加在 remote-WS 调用点(观看页从不发输入、本机所有者是会话授权方,调用点门控机械上完备)
- Modify: `app/http/services/agent_terminal.go`(仅加只读 `hasHeadroom()` 小方法)
- Test: `app/http/services/agent_remote_assist_test.go`

**语义铁律(spec §14 坑 2):** 协助终端注册进**手机式 detach 保活面**(断线保留、30m idle 才回收),**勿进网页腿 orphanTimers**。

- [ ] **Step 1: 写失败测试**

```go
func TestAssistTerminalLifecycle(t *testing.T) {
	m := newTestAssistManagerWithFakeTerminal(t) // startProcess 换 fake(照 agent_terminal_attach_test.go 夹具)
	sid, _ := m.RequestAndSignal("")
	_ = m.OnRemoteFrame(map[string]interface{}{"type": models.AgentEventAssistApproved, "session_id": sid})

	// approved 即创建终端,cwd=家目录(resolveAgentCWDFallbackHome 兜底),id=assist-<id>
	if got := m.fakeTerminalCWD(sid); got == "" {
		t.Fatal("terminal must be created at approval with home cwd")
	}

	// output 经复合 writer:同时写上行 sink 与观看扇出
	viewSink := m.RegisterViewSink(sid, "view1")
	m.fakeTerminalEmit(sid, "hello\r\n")
	if !m.uplinkGot(sid, "hello") || !viewSink.Got("hello") {
		t.Fatal("output must fan out to both uplink and view sinks")
	}

	// End → 终端关闭、观看 sink 收 assist.end、注册表清空
	_ = m.End(AssistEndAdmin)
	if m.fakeTerminalAlive(sid) || viewSink.Closed() != true {
		t.Fatal("end must close terminal and notify view sinks")
	}
}

func TestAssistTerminalPrefixRouting(t *testing.T) {
	// session_id 无 assist- 前缀的 terminal.create 走手机腿门控(既有行为不变);
	// assist- 前缀帧在无 active 会话时一律拒(防会话结束后残留输入)
}

func TestAssistTerminalPoolHeadroom(t *testing.T) {
	// 满 4 会话时 approved → End(AssistEndTerminalLimit),不挤掉手机会话
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestAssistTerminal -v`
Expected: FAIL

- [ ] **Step 3: 实现**

1. `OnActivated` 钩子接线:approved → 检查 `agentTerminalManager` 余量(加只读 `hasHeadroom()` 小方法)→ 不足则 `End(AssistEndTerminalLimit)`;充足则构造 `msg{session_id, cwd:""(家目录兜底), rows/cols 默认}` 调 `createWithSink(msg, assistCompositeWriter(uplink), connSink=nil)`,输出帧落复合 writer。
2. 复合 writer:包 `upstream func(interface{}) error` + 观看 sink 注册表;`terminal.output/exit/error` 帧写两路(任一路失败不阻断另一路,错误仅日志)。
3. **remote-WS 调用点门控**(不动 `DispatchTerminalFrame` 本体):在 `agent_remote_ws.go` 的 terminal 帧分派处,`strings.HasPrefix(session_id, assistIDPrefix)` → 校验 `RemoteAssistEnabled() && 会话 active && id 匹配` → 委托 `DispatchTerminalFrame`;前缀帧校验失败回 terminal.error(文案复用手机腿「disabled」同族);**无前缀帧原路径直送手机腿,零改动**。
4. `End` 收尾:close 终端 + 通知观看 sink(`assist.end` 帧)+ 清注册表。`remoteLoopExitCleanup` 的不可恢复终态(`closeAll` 处)追加 assist `End(AssistEndDeviceRestart)`(spec §14 坑 3:重连后不复活)。
5. 禁用开关翻转(Task 3 的 SetRemoteAssistEnabled(false))若 active → `End(AssistEndClient)`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services/ -run TestAssist -v && go test ./app/http/services/ -run TestAgentTerminal -v`
Expected: PASS(assist 全绿 + 既有 terminal 测试零回归)

- [ ] **Step 5: Commit**

```bash
git commit -o app/http/services/agent_remote_assist.go app/http/services/agent_remote_assist_test.go app/http/services/agent_remote_ws.go app/http/services/agent_terminal.go -m "远程协助P1:协助终端腿(前缀门控+复合writer扇出+池余量+回收联动)"
```

---

### Task 6: HTTP 三端点(request/session/end)

**Files:**
- Modify: `app/http/handlers/agent_handler_assist.go`
- Test: `app/http/handlers/agent_handler_assist_test.go`

- [ ] **Step 1: 写失败测试**

```go
// 闸门矩阵(照 web-terminal 双闸测试矩阵形态):
// ①无 cookie 非 loopback → 401 ②loopback → 放行 ③同源 LAN + cookie → 放行 ④跨站 Origin → 拒
// POST /api/agent/assist/request{message?}  → 200 {session_id,state};开关关 → 明确错误帧
//   冷却中 429 语义(common.Error 现有码位);开关关时任何 assist 端点 403
// GET  /api/agent/assist/session → 200 Snapshot(JSON)
// POST /api/agent/assist/end     → 200;idle 时 409
// core 腿全部透传(proxyIfNeeded;照 FS 端点 markWebTerminalLoopbackSource 前置模式)
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/handlers/ -run TestHandleAssist -v`
Expected: FAIL

- [ ] **Step 3: 实现**:三端点 + 审计(`assist/request`、`assist/end`、`assist/cancel`,照 `WebTerminalAuditTerminal` 的审计落点模式;不审 session 轮询)。请求体 ≤4KiB(照 `parseWebTerminalFSBody`)。core 腿 mark 真源标记头后再 proxy。

- [ ] **Step 4: 跑测试确认通过** + route_parity

Run: `go test ./app/http/handlers/ -run TestHandleAssist -v && go test ./app/agentruntime/ -run TestAgentRouteParity -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git commit -o app/http/handlers/agent_handler_assist.go app/http/handlers/agent_handler_assist_test.go -m "远程协助P1:客户端三端点(申请/会话态/结束)+审计"
```

---

### Task 7: 观看 WS(`/api/agent/assist/stream`)

**Files:**
- Modify: `app/http/handlers/agent_handler_assist.go`
- Test: `app/http/handlers/agent_handler_assist_test.go`

- [ ] **Step 1: 写失败测试**

```go
// 照 HandleWebTerminalStream 四段模板的测试矩阵(agent_handler_web_terminal_test.go 夹具):
// ①双闸矩阵(无 cookie 非 loopback=401 / loopback=放行 / 同源 LAN+cookie=放行 / 跨站 Origin=拒)
// ②透传腿 Origin+Cookie 复检(app/http/handlers/ai_stream_proxy.go:47/:56 历史坑——显式断言)
// ③会话闸:无 active 会话连上即关(1011 带原因);active 时放行
// ④只读结构性断言:连接发任何帧(含伪 terminal.input)→ 服务端不产生任何 PTY 写
//   (fake terminal input 计数器恒 0)且不回错误风暴(静默忽略或单条提示帧)
// ⑤回放续屏:连上先收 ring 回放帧(attachHistory/sendReplay 复用),再收 live
// ⑥会话 ended → 该连接收到 assist.end 后被服务端关闭
// ⑦SetReadLimit 小值(入站只有 ping,64KiB 足够;勿照抄远端 4MiB)
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/handlers/ -run TestHandleAssistStream -v`
Expected: FAIL

- [ ] **Step 3: 实现**:upgrade → 双闸 → 会话闸 → `sendReplay(sid, viewWriter)` → `RegisterViewSink` → 读循环**忽略全部入站帧**(只 `SetReadDeadline` 续活)→ defer Unregister。core 腿 `proxyAIStream`(WS 透传,复检同源+Cookie)。

- [ ] **Step 4: 跑测试确认通过** + route_parity + 既有 web-terminal WS 测试零回归

Run: `go test ./app/http/handlers/ -run 'TestHandleAssist|TestHandleWebTerminal' -v && go test ./app/agentruntime/ -run TestAgentRouteParity -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git commit -o app/http/handlers/agent_handler_assist.go app/http/handlers/agent_handler_assist_test.go -m "远程协助P1:观看WS(纯推送只读+回放续屏+透传复检)"
```

---

### Task 8: 契约测试(模拟云端对端的全设备侧链)

**Files:**
- Create: `app/http/services/agent_remote_assist_contract_test.go`

- [ ] **Step 1: 写集成测试**(不启真网络,直接驱动 `handleRemoteAgentMessage` + fake 上行 writer,照 `TestAgentServiceRejectsRemoteCommandsAfterDisable` 手法):

```
全链脚本:enabled=true → HTTP request(夹具直调 handler)→ 断言上行 assist.request
→ handleRemoteAgentMessage(approved)→ 断言终端创建(fake startProcess)
→ handleRemoteAgentMessage(terminal.input,session_id=assist-*)→ fake PTY 收到字节
→ fake PTY 出帧 → 上行 writer 与 view sink 双双收到
→ handleRemoteAgentMessage(assist.end{admin_end})→ PTY 关、view 收 end、池释放
→ 60s 冷却内再申请 → 429
```

- [ ] **Step 2: 跑测试**

Run: `go test ./app/http/services/ -run TestAssistContract -v`
Expected: PASS

- [ ] **Step 3: 全量回归**: `go build ./... && go test ./app/http/... ./app/agentruntime/...`(预存 flaky 见仓内基线,勿误判)
- [ ] **Step 4: Commit**

```bash
git commit -o app/http/services/agent_remote_assist_contract_test.go -m "远程协助P1:模拟云端对端契约测试(设备侧全链)"
```

---

### Task 9: 前端入口 + 申请弹窗

**Files:**
- Create: `app/website/src/composables/useAssist.js`
- Create: `app/website/src/components/assist/AssistDialog.vue`
- Modify: `app/website/src/components/DashboardPage.vue:154`(桌面)/`:367`(移动)顶栏加「远程协助」按钮(照 showChat 双处模式;行号为评审时实测,实现时以 grep showChat 重新定位为准)
- Modify: `app/website/src/i18n/zh.js` + `en.js`(`assist_` 前缀词条;全角标点/「」/「你」口径)
- Test: `app/website/src/composables/__tests__/useAssist.test.js`(vitest,**cd app/website 跑**)

- [ ] **Step 1: 写失败 vitest**(composable 状态机:idle→requested(带留言)→approved→active→ended/rejected;冷却 429 映射文案;WS 重连 800ms×2 退避照 `useAliangChat.ensureAISocket` 模式)

- [ ] **Step 2: 跑确认失败** → **Step 3: 实现**(composable + 弹窗 + 双处按钮 + 词条)。弹窗文案必须含授权边界(spec §8.1 同款):「管理员将获得此设备的终端操作权限,全程可被你观看」。
- [ ] **Step 4: 跑确认通过**:`cd app/website && npx vitest run src/composables/__tests__/useAssist.test.js`
- [ ] **Step 5: Commit**(git add 上述前端文件后 `-o` 限定路径提交,中文信息)

---

### Task 10: 前端观看页

**Files:**
- Create: `app/website/src/components/assist/AssistWatchPage.vue`
- Modify: 路由注册(照 ChatPage 的路由注册处;实现时先 `grep -rn "ChatPage" app/website/src/router*` 定位)
- Test: `app/website/src/components/assist/__tests__/assistWatch.test.js`

- [ ] **Step 1: 写失败 vitest**:页面状态机(connected/waiting/ended+原因文案)/只读 xterm 接帧(`\r\n` 写入、`disableStdin` 断言)/断线重连续屏/`assist.end` 后停 WS。
- [ ] **Step 2-4: 失败→实现→通过**(xterm 用法照既有 `app/website/src/composables/useTerminalSocket.js` + `app/website/src/components/terminal/TerminalPane.vue` 封装;DOM renderer;页面返回层接 `useEscStack`,spec §9)。
- [ ] **Step 5: Commit**。

---

### Task 11: 收尾(审计核对 + 真机验收清单)

- [ ] 审计项核对:assist/request、assist/cancel、assist/end、terminal create/close、开关翻转——全落 audit 表(boot 已扩表则直接用;缺表则先扩,照 web-terminal 批先例)。
- [ ] **评审遗留加固(Task 10 质量评审根因裁定)**:观看 WS 等待模式(requested)零流量,服务端 90s 读死线每 90s 静默断连、客户端重连预算耗尽→审批到达白屏(客户端侧已加 ensureConnected 兜底)。正确修法=服务端给观看 WS 加 ping ticker(每 ~30s 发 protocol ping,浏览器自动 pong,刷新读死线;浏览器无法主动 ping,只能服务端发)。small Go 改动+测试。
- [ ] `go build ./... && go test ./app/http/... ./app/agentruntime/...` 全绿;`cd app/website && npx vitest run` 全绿。
- [ ] 真机清单(平台侧就绪后执行,挂 TODO):LAN 开 56431 → 申请 → admin 批准 → admin 敲命令客户端实时见 → admin 断网客户端见 detach 保活 → 结束后终端消失+入口失效 → 开关关闭=功能整体消失 → Windows 0.196 ConPTY 一轮。
- [ ] Commit 收尾(如有散件)。

---

## 附录 A:平台侧帧契约(跨仓 seam,本计划不实现)

云端/admin 控制台需实现(字符串精确对齐 Task 1 常量):

1. **审批队列**:消费设备上行 `assist.request{type,session_id,message}`——设备身份(device_id/account)与时间戳由云端从已鉴权的设备连接补齐,不在设备载荷里(spec §6「角色由服务端按连接身份判定」同款哲学;Task 4 已按此实施,此条为实施勘误后的契约口径);出队展示;同意→下行 `assist.approved{session_id}`;拒绝→`assist.rejected{session_id}`。
2. **admin 操作页 WS**:经云端房间中继 `terminal.input/resize`(目标 session_id=assist-*)下行给设备;把设备上行的 `terminal.output/exit/error` 回给 admin 连接。
3. **admin 掉线判定**:admin 房间连接断开 10 分钟仍未重连 → 下行 `assist.end{reason:"admin_timeout"}`。
4. **结束透传**:admin 点结束 → `assist.end{reason:"admin_end"}`;客户端点结束经设备 `assist.cancel` 上行,云端无需动作(设备已终态),仅同步房间。
5. **`assist.state`**:P1 设备侧仅记录日志;房间状态镜像留给 P2+。
6. 终端帧族与手机腿同形(`type`/`session_id`/`data`/`rows`/`cols`),session_id 恒带 `assist-` 前缀。

## 附录 B:已对齐的用户决定(实现不再问)

终端绕过保险箱 PIN(显式请求即授权)/观看页 P1 仅终端视图(页面 Tab 属 P2)/观看页允许多连接(皆只读)/会话上限 2h、pending 5m、冷却 60s/admin 掉线 10min 判定在云端。
