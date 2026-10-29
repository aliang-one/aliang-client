# 手机↔本机会话统一 阶段1（alianggate 后端）实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地 spec（`docs/superpowers/specs/2026-10-28-cross-device-session-unify-design.md`）阶段 1 的全部后端能力：会话订阅表与事件 fan-out、审批双端同态（可见性/ack 双写/sync 补卡/expires_at/会话级放行齐平）、agentcore journal 读回续聊、journal 记录缺口补齐（斜杠命令/内核工具行/半截输出/审批审计/头行幂等+origin）、resume-missing 显式化。

**Architecture:** agentAIManager 是唯一汇聚点。新增 socket 注册表 + 按会话订阅表（新文件），run 事件在 emitter 写出点做「origin 内联 + 订阅者/审批族扇出」，审批 ack 双写到「决策到达 socket + run 发起 socket」，journal 写侧补齐 + 新增读回历史函数供 `message()` lazy-create 装回内存。

**Tech Stack:** Go 1.x（仓库现行）、gorilla/websocket（既有）、表驱动契约测试（models/agent_protocol_test.go 风格）。

**Spec 修正（本计划相对 spec 的四处偏离，均已获设计期调研支撑并经计划评审确认）：**
1. spec §4.4「claude/codex 通道补 journal steer 行」**不执行**：CLI 通道的 steer 本就落在原生转录（claude stream-json 输入 / codex rollout），统一清单三源聚合已可见；再写 `.aliang/ai-sessions` 会为同一会话制造**第二条清单条目**（`claude_<uuid>` 原生条目 + 裸 ID journal 条目）。Task 1 在 spec 落 amendment。
2. spec §4.5「斜杠命令落 journal」范围收敛为 **agentcore 通道**：claude 的 `/compact` 是真实模型回合（原生转录已有），codex 斜杠命令同理由各自原生转录承载；只有 agentcore（无原生转录）需要 journal。与偏离 1 同根因。
3. spec §4.4 第 3 条「steer 的 ack 经订阅表回发订阅者」**首版不做**：插话方自己的 socket 本就直接收到 ack（现有 `emitAgentAISteerAck` 语义），另一端在下一次清单/转录刷新可见；推送级 steer ack 对端广播推迟到有真实需求再做。
4. spec §4.5 审批审计行**不含「来源 socket 角色」字段**（spec 措辞落空）：`rememberCompletedApprovalLocked` 是多路径汇聚点，无来源上下文可带；审计以 decision/ts 为准，来源归属由 PhoneServer DB 审计承担。Task 1 的 spec amendment 一并落这两条。

**执行约定（开工前读一遍）：**
- 开发在新 worktree 分支（Task 1 创建），绝不直接改 master 工作区。
- 每个任务以全中文提交收尾（「新增：/修复：」前缀），提交脚注 `Co-Authored-By: Claude Code <noreply@anthropic.com>`。**整仓规则：所有 git commit 需用户在会话中明示同意过一次即可按计划逐任务提交；若未获同意，把 commit 步骤改为暂存不动并汇报。**
- 测试硬护栏（memory 已存案，违反会伤真机/必炸）：
  - 涉及 `localUserAgentBaseURL` 的测试进程必须**惰性**把基址指向 `127.0.0.1:1`（v1.1.78 护栏，防真 POST 掐断本机 56433 的在途 AI run）。
  - 构造 manager 一律 `newAgentAIManager()`，禁止 `&agentAIManager{}`（nil map panic）。
  - 回调 goroutine 里禁止 `t.Fatal`（用 `t.Error` + channel 同步）。
  - macOS sun_path 103 字节限制用 `ccPeerShortSocketPath`（本计划不涉及，备忘）。
- 预存 flake 基线（遇到先对照，不当回归）：`agent_ai.go` 子进程 2s/5s 硬超时 3 用例高并行偶败（单包复跑即绿）；`-race` 下 `TestStartProxyWhenSessionReady`（services）与 `TestHandleAgentAuthRejectedNotificationRules`（handlers）master 基线可复现；`processor/auth TestSessionAuthorityProxyAdmissionLinearizesWithDemotion` 基线 5/10。
- 每任务完成即 `go build ./...` + 该包 `go test`；全量回归在 Task 15。
- gopls 对 worktree 文件的误报（undefined 等）一律无视，以 `go build`/`go test` 为准。

**行号基准：** 本计划行号基于 worktree HEAD `b6e9e56`（=master）。执行时若行号有 ±30 漂移，以符号名 grep 定位为准，不要盲按行号改。

---

### Task 1: worktree 建立与 spec amendment

**Files:**
- Create: 新 worktree `../../feature-xdevice-session-unify`（目录名随意，分支 `feature/cross-device-session-unify-phase1`）
- Modify: `docs/superpowers/specs/2026-10-28-cross-device-session-unify-design.md`（amendment）
- Create: 本计划文件随 worktree 带入

- [ ] **Step 1: 建 worktree**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
git worktree add .claude/worktrees/xdsu1 -b feature/cross-device-session-unify-phase1 master
```

- [ ] **Step 2: 带入未跟踪文档并修补 embed 占位（新 worktree 首次构建两坑，见 memory transfer-worktree-build-gotchas）**

```bash
cp docs/superpowers/specs/2026-10-28-cross-device-session-unify-design.md .claude/worktrees/xdsu1/docs/superpowers/specs/
cp docs/superpowers/plans/2026-10-28-cross-device-session-unify-phase1.md .claude/worktrees/xdsu1/docs/superpowers/plans/
# 首次 go build 前：从主仓拷 dist（若 app/website/dist 未被 git 跟踪）
ls app/website/dist >/dev/null 2>&1 && cp -R app/website/dist .claude/worktrees/xdsu1/app/website/dist || true
```

- [ ] **Step 3: spec amendment（§4.4 与 §4.5 处）**

在 spec 文档 §4.4「steer 补齐」第二条后追加：

```markdown
> **Amendment（2026-10-28，实施期修正）：** claude/codex 通道**不补** `.aliang/ai-sessions`
> journal steer 行。CLI 通道的插话本就落在原生转录（claude stream-json 输入消息、codex
> rollout），读侧三源聚合已可见；再写 journal 会为同一会话制造第二条清单条目
> （`claude_<uuid>` 原生条目 + 裸 ID journal 条目）。§4.5 斜杠命令落 journal 同理收敛为
> **仅 agentcore 通道**（claude `/compact` 是真实模型回合、codex 由 rollout 承载）。
> §4.4 第 3 条「steer ack 回发订阅者」首版不做（插话方 socket 已直接收 ack，对端靠清单
> 刷新可见）；§4.5 审批审计行不含来源 socket 角色字段（多路径汇聚点无来源上下文，来源
> 归属由 PhoneServer DB 审计承担）。
```

- [ ] **Step 4: 提交（需用户已同意逐任务提交）**

```bash
cd .claude/worktrees/xdsu1
git add docs/superpowers
git commit -m "新增:跨端会话统一阶段1计划与spec修正(订阅表/审批同态/journal补齐)

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 2: 协议帧与契约登记

> **修订（实施期，commit 518bc64）：** 本任务正文给出的落位与测试断言被质量评审推翻后已换表修正——契约表方向基准=远端 /ws/agent 链路（client=agent 拨号方）：subscribe/unsubscribe 登记在 **ServerSends**（agent 收），snapshot/ai.run.notice 登记在 **ClientSends**（agent 发）。下文正文保留原稿仅供追溯，以 518bc64 与 spec §4.1 方向基准句为准。

**Files:**
- Modify: `app/http/models/agent_protocol.go`（常量段 ~44-100、契约表 ClientSends ~239 / ServerSends ~289 / Streams ~323）
- Test: `app/http/models/agent_protocol_contract_test.go`（新建）

- [ ] **Step 1: 写失败测试（契约形状锁）**

新建 `app/http/models/agent_protocol_contract_test.go`，仿 `agent_protocol_test.go` 现有风格：

```go
package models

import "testing"

// 2026-10-28 跨端会话统一：新帧型契约锁（spec §4.1/§4.3）。
func TestContractCrossDeviceSessionUnifyFrames(t *testing.T) {
	c := DefaultAgentProtocolContract()
	wantTypes := map[string]struct {
		inClient bool
		inServer bool
	}{
		AgentEventAISessionSubscribe:   {true, false},
		AgentEventAISessionUnsubscribe: {true, false},
		AgentEventAISessionSnapshot:    {false, true},
		AgentEventAIRunNotice:          {false, true},
	}
	for typ, want := range wantTypes {
		foundClient, foundServer := false, false
		for _, e := range c.ClientSends {
			if e.Type == typ {
				foundClient = true
			}
		}
		for _, e := range c.ServerSends {
			if e.Type == typ {
				foundServer = true
			}
		}
		if foundClient != want.inClient || foundServer != want.inServer {
			t.Fatalf("contract %s: client=%t server=%t, want client=%t server=%t", typ, foundClient, foundServer, want.inClient, want.inServer)
		}
	}
	// 审批 request 的 expires_at 进 Optional 表
	for _, e := range c.ClientSends {
		if e.Type == AgentEventAIApprovalRequest {
			ok := false
			for _, f := range e.Optional {
				if f == "expires_at" {
					ok = true
				}
			}
			if !ok {
				t.Fatalf("ai.approval.request Optional 缺 expires_at")
			}
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./app/http/models/ -run TestContractCrossDeviceSessionUnifyFrames -v
```
预期：FAIL（常量未定义，编译错误）。

- [ ] **Step 3: 实现常量与契约**

`agent_protocol.go` 常量段（`AgentEventAIApprovalRequest` 一族旁，~59-68）加：

```go
	AgentEventAISessionSubscribe   = "ai.session.subscribe"   // client→agent: 订阅一个会话的实时事件
	AgentEventAISessionUnsubscribe = "ai.session.unsubscribe" // client→agent: 退订
	AgentEventAISessionSnapshot    = "ai.session.snapshot"    // agent→client: 订阅握手回包（转录+待审批+状态）
	AgentEventAIRunNotice          = "ai.run.notice"          // agent→client: 非致命运行期提示（resume_missing 等）
```

`ClientSends` 表（~239 附近）加三行：

```go
				{Type: AgentEventAISessionSubscribe, Required: []string{"type", "session_id"}, Optional: []string{}},
				{Type: AgentEventAISessionUnsubscribe, Required: []string{"type", "session_id"}, Optional: []string{}},
```

`ServerSends` 表（~289 附近）加两行：

```go
				{Type: AgentEventAISessionSnapshot, Required: []string{"type", "session_id"}, Optional: []string{"transcript", "pending_approvals", "status"}},
				{Type: AgentEventAIRunNotice, Required: []string{"type", "session_id", "code"}, Optional: []string{"message_id", "detail"}},
```

`Streams` 表（~323 附近）加一条：

```go
			{Name: "ai_run_notice", Event: AgentEventAIRunNotice, Required: []string{"session_id", "code"}, Optional: []string{"message_id", "detail"}, Description: "Non-fatal run notices (e.g. resume_missing: native session absent, continuing as a fresh conversation). Informational; runs always continue."},
```

同表审批流 Optional 补 `expires_at`。

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./app/http/models/ -run TestContractCrossDeviceSessionUnifyFrames -v
```
预期：PASS。

- [ ] **Step 5: 提交**

```bash
git add app/http/models/agent_protocol.go app/http/models/agent_protocol_contract_test.go
git commit -m "新增:跨端会话统一协议帧(订阅/快照/运行提示)+审批expires_at契约

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 3: socket 注册表与订阅表（核心数据结构）

**Files:**
- Create: `app/http/services/agent_ai_sockets.go`
- Modify: `app/http/services/agent_ai.go`（agentAIManager 结构体 35-61 加字段；newAgentAIManager 986-998 初始化）
- Test: `app/http/services/agent_ai_sockets_test.go`（新建）

- [ ] **Step 1: 写失败测试**

```go
package services

import (
	"sync"
	"testing"
	"time"
)

func newTestAISocketsManager(t *testing.T) *agentAIManager {
	t.Helper()
	return newAgentAIManager()
}

func TestAISocketRegisterUnsubscribeLifecycle(t *testing.T) {
	m := newTestAISocketsManager(t)
	var mu sync.Mutex
	written := 0
	sock := m.registerAISocket("local", func(interface{}) error {
		mu.Lock()
		written++
		mu.Unlock()
		return nil
	})
	if sock == nil || sock.id == "" {
		t.Fatal("socket 未注册")
	}
	m.subscribeAISocket(sock.id, "s1")
	sock.enqueue(map[string]interface{}{"type": "probe"})
	waitUntilFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return written == 1
	})
	m.unregisterAISocket(sock.id)
	if got := len(m.aiSockets); got != 0 {
		t.Fatalf("注销后残留 socket: %d", got)
	}
}

func TestAISocketSlowConsumerDropped(t *testing.T) {
	m := newTestAISocketsManager(t)
	block := make(chan struct{})
	sock := m.registerAISocket("local", func(interface{}) error {
		<-block // 故意不消费
		return nil
	})
	defer close(block)
	// 灌满队列 +1 溢出
	for i := 0; i < agentAISocketQueueCap+1; i++ {
		sock.enqueue(map[string]interface{}{"type": "flood", "i": i})
	}
	waitUntilFor(t, time.Second, func() bool {
		select {
		case <-sock.closed:
			return true
		default:
			return false
		}
	})
	m.mu.Lock()
	_, alive := m.aiSockets[sock.id]
	m.mu.Unlock()
	if alive {
		t.Fatal("溢出 socket 应从注册表摘除")
	}
}

func TestAISocketSubscribeUnknownSessionOK(t *testing.T) {
	m := newTestAISocketsManager(t)
	sock := m.registerAISocket("local", func(interface{}) error { return nil })
	m.subscribeAISocket(sock.id, "no-such-session") // 不存在会话允许订阅（列表刷新场景）
	m.unsubscribeAISocket(sock.id, "no-such-session")
	m.unregisterAISocket(sock.id)
}

func waitUntilFor(t *testing.T, d time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("waitUntilFor 超时")
}
```

- [ ] **Step 2: 跑测试确认失败（编译错：registerAISocket 未定义）**

```bash
go test ./app/http/services/ -run TestAISocket -v
```

- [ ] **Step 3: 实现 `agent_ai_sockets.go`**

```go
package services

// AI socket 注册表与会话订阅表（2026-10-28 跨端会话统一，spec §4.1）。
//
// 两条出站通道都注册进来：手机远端 WS（role="remote"，写函数是重连换道的
// currentRemoteWriter 间接层，连接级生命周期≈进程级）与本机聊天透传 WS
// （role="local"，每条连接一个，HandleAIStream 注册/注销）。
//
// 事件扇出规则（fanOutFrom）：
//   - run 流式事件 → 发起通道（origin 内联，保序）+ 订阅该会话的 socket；
//   - 审批族事件（request/resolved/cancelled/option/tool_ask）→ 额外广播给
//     全部活跃 socket（不看该会话也能看到待审批——双端审核关键语义）。
//
// 慢消费者保护（spec §9）：每 socket 一条带缓冲队列 + 独立 pump goroutine；
// 溢出即断开该 socket（摘出注册表），绝不阻塞 run goroutine。

import (
	"fmt"
	"strings"
	"sync"

	"aliang.one/nursorgate/common/logger"
)

// agentAISocketQueueCap 单 socket 事件队列深度。溢出=消费者死了/卡了，断开。
const agentAISocketQueueCap = 64

// agentAIOriginSocketField 入站消息的内部来源 socket 标记。由两条分发路径
// （handleRemoteAgentMessage AI 分支盖 "remote"、HandleAIStream 盖本地 socket
// id）在分发前注入，message()/create()/runUserMessage 读取后落到
// session.originSocket。以 "__" 前缀命名，禁止原样转发到任何对端。
const agentAIOriginSocketField = "__origin_socket"

// agentAIRemoteSocketID 远端 WS 的常驻 socket id（写函数是间接层，重连自动换道）。
const agentAIRemoteSocketID = "remote"

type agentAISocket struct {
	id        string
	role      string // "remote" | "local"
	write     agentTerminalWriter
	out       chan map[string]interface{}
	closed    chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	subs      map[string]struct{}
	// onDead 由 registerAISocket 注入：溢出/写失败断开时把自身从注册表摘除。
	// 经 closeOnce 保证只触发一次。
	onDead func(id string)
}

func (s *agentAISocket) alive() bool {
	select {
	case <-s.closed:
		return false
	default:
		return true
	}
}

// enqueue 非阻塞投递；队列满 = 消费者不健康，断开该 socket（幂等）并从
// 注册表摘除（onDead）。
func (s *agentAISocket) enqueue(payload map[string]interface{}) {
	select {
	case <-s.closed:
		return
	default:
	}
	select {
	case s.out <- payload:
	default:
		s.closeOnce.Do(func() {
			close(s.closed)
			if s.onDead != nil {
				s.onDead(s.id)
			}
		})
		logger.Warn(fmt.Sprintf("[AI-SOCKET] slow consumer dropped socket=%s role=%s", s.id, s.role))
	}
}

func (s *agentAISocket) subscribed(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.subs[sessionID]
	return ok
}

// pump 把队列事件串行写到 socket。写失败视为连接死亡：关 closed 摘活。
// 不 close(out)——enqueue 与 close 并发会 panic（send on closed channel），
// 残留消息随 socket 对象一起被 GC。
func (s *agentAISocket) pump() {
	for {
		select {
		case <-s.closed:
			return
		case payload := <-s.out:
			if err := s.write(payload); err != nil {
				s.closeOnce.Do(func() {
					close(s.closed)
					if s.onDead != nil {
						s.onDead(s.id)
					}
				})
				logger.Warn(fmt.Sprintf("[AI-SOCKET] write failed, socket detached id=%s role=%s err=%v", s.id, s.role, err))
				return
			}
		}
	}
}

// registerAISocket 注册一条出站 socket 并启动 pump。write 为 nil 时返回 nil
// （远端离线等场景，注册表容忍空缺）。
func (m *agentAIManager) registerAISocket(role string, write agentTerminalWriter) *agentAISocket {
	if write == nil {
		return nil
	}
	m.mu.Lock()
	if m.aiSockets == nil {
		m.aiSockets = make(map[string]*agentAISocket)
	}
	m.aiSocketSeq++
	sock := &agentAISocket{
		id:     fmt.Sprintf("aisock-%d", m.aiSocketSeq),
		role:   role,
		write:  write,
		out:    make(chan map[string]interface{}, agentAISocketQueueCap),
		closed: make(chan struct{}),
		subs:   make(map[string]struct{}),
	}
	// onDead 只做「从注册表摘除」，**绝不**再走 sock.closeOnce.Do——调用点
	// 在 closeOnce.Do 闭包内，sync.Once 持锁期间同 goroutine 再入同一 Once
	// 会自死锁（doSlow 在闭包返回后才落 done）。
	sock.onDead = func(id string) {
		m.mu.Lock()
		delete(m.aiSockets, id)
		m.mu.Unlock()
	}
	if role == "remote" {
		// 远端是常驻单例：先摘旧（间接层写函数恒同一闭包，正常不会重复注册；
		// 防御测试/重入）。
		for id, s := range m.aiSockets {
			if s.role == "remote" {
				s.closeOnce.Do(func() { close(s.closed) })
				delete(m.aiSockets, id)
			}
		}
		sock.id = agentAIRemoteSocketID
	}
	m.aiSockets[sock.id] = sock
	m.mu.Unlock()
	go sock.pump()
	return sock
}

func (m *agentAIManager) unregisterAISocket(id string) {
	m.mu.Lock()
	sock := m.aiSockets[id]
	delete(m.aiSockets, id)
	m.mu.Unlock()
	if sock != nil {
		sock.closeOnce.Do(func() { close(sock.closed) })
	}
}

// ensureRemoteAISocket 幂等保证远端常驻 socket 在册。write 传间接层写函数
// （AgentService 侧 currentRemoteWriter 包装），重连时该函数自动换道。
func (m *agentAIManager) ensureRemoteAISocket(write agentTerminalWriter) {
	if write == nil {
		return
	}
	m.mu.Lock()
	if s := m.aiSockets[agentAIRemoteSocketID]; s != nil && s.alive() {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	m.registerAISocket("remote", write)
}

func (m *agentAIManager) subscribeAISocket(socketID, sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.aiSockets[socketID]; s != nil {
		s.mu.Lock()
		s.subs[sessionID] = struct{}{}
		s.mu.Unlock()
	}
}

func (m *agentAIManager) unsubscribeAISocket(socketID, sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.aiSockets[socketID]; s != nil {
		s.mu.Lock()
		delete(s.subs, sessionID)
		s.mu.Unlock()
	}
}

// agentAIEventIsApprovalFamily 审批族判定：广播到全部 socket（spec §4.1）。
func agentAIEventIsApprovalFamily(payload map[string]interface{}) bool {
	switch strings.TrimSpace(fmt.Sprint(payload["type"])) {
	case models.AgentEventAIApprovalRequest,
		models.AgentEventAIApprovalCancelled,
		models.AgentEventAIOptionRequest,
		models.AgentEventToolAskRequest,
		models.AgentEventAIOptionCancelled:
		return true
	default:
		return false
	}
}

// fanOutFrom 把一个已由 origin 内联写出的事件副本扇出给订阅者（审批族则
// 广播给全部其他 socket）。绝不阻塞：enqueue 非阻塞。origin 写失败不重试
// （origin 的错误语义归调用方）。
func (m *agentAIManager) fanOutFrom(originID, sessionID string, payload map[string]interface{}) {
	m.mu.Lock()
	var targets []*agentAISocket
	broadcast := agentAIEventIsApprovalFamily(payload)
	for id, s := range m.aiSockets {
		if id == originID || s == nil || !s.alive() {
			continue
		}
		if broadcast || s.subscribed(sessionID) {
			targets = append(targets, s)
		}
	}
	m.mu.Unlock()
	for _, s := range targets {
		s.enqueue(payload)
	}
}
```

`agentAIManager` 结构体（agent_ai.go:35-61）加三字段，`newAgentAIManager`（986-998）初始化 map：

```go
	// —— 跨端会话统一（2026-10-28）——
	// aiSockets 是出站 socket 注册表；aiSocketSeq 发号。originSocket 不在
	// manager：随消息 __origin_socket 字段走，落到 session.originSocket。
	aiSockets   map[string]*agentAISocket
	aiSocketSeq int
```

```go
		aiSockets:          make(map[string]*agentAISocket),
```

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./app/http/services/ -run TestAISocket -v
```
预期：3 个 PASS。

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai_sockets.go app/http/services/agent_ai_sockets_test.go app/http/services/agent_ai.go
git commit -m "新增:AI socket 注册表与会话订阅表(慢消费者断开/审批族广播)

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 4: emitter 写出点 fan-out 接线

**Files:**
- Modify: `app/http/services/agent_ai.go`（runEmitter ~1000；`__origin_socket` 盖标记与 session 字段归 Task 5）
- Test: `app/http/services/agent_ai_sockets_test.go`（追加）

- [ ] **Step 1: 写失败测试**

```go
func TestRunEventFanOutToSubscriber(t *testing.T) {
	m := newTestAISocketsManager(t)
	originWritten := make(chan struct{}, 8)
	subWritten := make(chan map[string]interface{}, 8)
	origin := m.registerAISocket("local", func(p interface{}) error {
		originWritten <- struct{}{}
		return nil
	})
	sub := m.registerAISocket("local", func(p interface{}) error {
		subWritten <- p.(map[string]interface{})
		return nil
	})
	m.subscribeAISocket(sub.id, "s1")

	write := m.fanOutWrite("s1", origin.id, func(interface{}) error { return nil })
	_ = write(map[string]interface{}{"type": "ai.delta", "delta": "x"})

	<-originWritten // origin 已内联写出
	select {
	case got := <-subWritten:
		if got["type"] != "ai.delta" {
			t.Fatalf("订阅者收到错误事件: %v", got["type"])
		}
	case <-time.After(time.Second):
		t.Fatal("订阅者未收到 run 事件")
	}
}

func TestApprovalBroadcastReachesNonSubscriber(t *testing.T) {
	m := newTestAISocketsManager(t)
	got := make(chan map[string]interface{}, 8)
	bystander := m.registerAISocket("local", func(p interface{}) error {
		got <- p.(map[string]interface{})
		return nil
	})
	write := m.fanOutWrite("s1", agentAIRemoteSocketID, func(interface{}) error { return nil })
	_ = write(map[string]interface{}{"type": models.AgentEventAIApprovalRequest, "session_id": "s1"})
	select {
	case p := <-got:
		if p["type"] != models.AgentEventAIApprovalRequest {
			t.Fatalf("期望审批广播,收到 %v", p["type"])
		}
	case <-time.After(time.Second):
		t.Fatal("未订阅 socket 未收到审批广播")
	}
	_ = bystander
}

func TestOriginSocketNotDuplicated(t *testing.T) {
	m := newTestAISocketsManager(t)
	count := make(chan struct{}, 32)
	sock := m.registerAISocket("local", func(interface{}) error { count <- struct{}{}; return nil })
	// 包装内层的 originWrite 恰好执行 1 次（内联）；fan-out 跳过 origin socket，
	// sock 自己的写函数不再被调——总数必须是 1，出现 2 即去重失效。
	write := m.fanOutWrite("s1", sock.id, func(interface{}) error { count <- struct{}{}; return nil })
	_ = write(map[string]interface{}{"type": "ai.delta"})
	waitUntilFor(t, time.Second, func() bool { return len(count) == 1 })
	time.Sleep(50 * time.Millisecond)
	if n := len(count); n != 1 {
		t.Fatalf("origin 事件应恰好写一次,实际 %d", n)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./app/http/services/ -run 'TestRunEventFanOut|TestApprovalBroadcast|TestOriginSocket' -v
```

- [ ] **Step 3: 实现 fanOutWrite + runEmitter 接线**

`agent_ai_sockets.go` 追加：

```go
// fanOutWrite 包一层 origin 写函数：origin 内联（保 emitter 的互斥序与终态
// 屏障语义），随后非阻塞扇出副本。审批族广播、其余仅订阅者。
func (m *agentAIManager) fanOutWrite(sessionID, originSocketID string, originWrite agentTerminalWriter) agentTerminalWriter {
	if originWrite == nil {
		return nil
	}
	return func(payload interface{}) error {
		err := originWrite(payload)
		if row, ok := payload.(map[string]interface{}); ok {
			m.fanOutFrom(originSocketID, sessionID, row)
		}
		return err
	}
}
```

`runEmitter`（agent_ai.go:1000）在构造 emitter 后、返回前加（读 runEmitter 现体，保持既有 onTerminal 等逻辑不动）：

```go
	em := newAgentAIRunEmitter(run, write)
	// 跨端同态（spec §4.1）：origin 仍内联，事件副本扇出到订阅 socket。
	// originSocketID 从消息分发路径盖的 __origin_socket 来；空串=旧调用方
	// （测试/TUI 内部路径），扇出自然退化为「无 origin 去重」——全部 socket
	// 都可能收到副本，与 origin 内联并存属可接受重复（仅测试路径）。
	em.write = m.fanOutWrite(run.sessionID, run.originSocketID, em.write)
	return em
```

`agentAIRun` 结构体加字段 `originSocketID string`（grep `type agentAIRun struct` 定位）。
（注意：`session.originSocket` 字段与「msg 标记 → run.originSocketID」的填充链在 **Task 5** 落地，本任务
只加 emitter 包装与 run 字段——Task 4 独立可编译可测：originSocketID 此阶段恒空串，扇出退化为
「无 origin 去重」，由测试里显式传 id 驱动。）

- [ ] **Step 4: 跑测试确认通过 + 全包回归**

```bash
go test ./app/http/services/ -run 'TestRunEventFanOut|TestApprovalBroadcast|TestOriginSocket|TestAISocket' -v
go build ./...
```

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai_sockets.go app/http/services/agent_ai_sockets_test.go app/http/services/agent_ai.go
git commit -m "新增:run 事件 emitter 写出点订阅扇出(审批族广播/origin 内联保序)

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 5: 两条分发路径盖 origin 标记 + 远端常驻 socket

**Files:**
- Modify: `app/http/handlers/agent_handler.go`（HandleAIStream 69-99：注册/注销本地 socket + 盖标记）
- Modify: `app/http/services/agent_remote_ws.go`（handleRemoteAgentMessage AI 分支 833-881：盖 "remote"；runRemoteAgentSession 413-613：ensureRemoteAISocket）
- Test: `app/http/services/agent_ai_sockets_test.go`（追加 surface/origin 断言级别的单元测试；handler 层由 Task 14 E2E 覆盖）

- [ ] **Step 1: 写失败测试（manager 级：origin 标记透传）**

```go
func TestMessageStampsOriginSocketOnSession(t *testing.T) {
	m := newTestAISocketsManager(t)
	svc := &AgentService{ai: m}
	done := make(chan struct{})
	// provider=agentcore 但无上游：run 会失败退出，我们只验证 session 落标。
	sock := m.registerAISocket("local", func(interface{}) error { return nil })
	msg := map[string]interface{}{
		"type":        "ai.message",
		"session_id":  "origin-stamp-s1",
		"message_id":  "m1",
		"content":     "hi",
		"provider":    "agentcore",
		agentAIOriginSocketField: sock.id,
	}
	go func() {
		defer close(done)
		svc.DispatchLocalAI(msg, func(interface{}) error { return nil })
	}()
	waitUntilFor(t, 3*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		s := m.sessions["origin-stamp-s1"]
		return s != nil && s.originSocket == sock.id
	})
	<-done
}
```

（若 `AgentService{ai: m}` 字段名不同，grep `s.ai` 的赋值点确认；现有测试文件里已有同款构造先例就照抄。）

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./app/http/services/ -run TestMessageStampsOriginSocket -v
```

- [ ] **Step 3: 实现**

1. `agentAISession` 结构体加 `originSocket string`（注释：本会话当前 run 的发起 socket id，审批 ack 双写用）。
2. `message()`（1676）：`m.mu.Lock()` 读到 session 后（含 lazy-create 注册后），`session.originSocket = strings.TrimSpace(remoteString(msg, agentAIOriginSocketField))`（放在 `session.lastActiveAt` 同一临界区）。`create()` 同款处理。`runUserMessage` 里把 `run.originSocketID = session.originSocket`（构造 run 前，锁内）。
3. `HandleAIStream`（agent_handler.go:69-99）读循环外注册 socket 并盖标记：

```go
	// 跨端同态（spec §4.1）：本地聊天连接注册进 socket 注册表，事件扇出与
	// 审批广播靠它；连接退出即注销。
	sock := h.service.RegisterLocalAIChatSocket(writeJSON)
	defer h.service.UnregisterLocalAIChatSocket(sock)
```

`AgentService` 加两个薄包装（放 agent_ai_sockets.go，操作 `s.ai`）：

```go
// RegisterLocalAIChatSocket 注册一条本地聊天 WS 出站 socket，返回其 id。
func (s *AgentService) RegisterLocalAIChatSocket(write agentTerminalWriter) string {
	if s.ai == nil || write == nil {
		return ""
	}
	return s.ai.registerAISocket("local", write).id
}

func (s *AgentService) UnregisterLocalAIChatSocket(id string) {
	if id == "" {
		return
	}
	s.ai.unregisterAISocket(id)
}
```

HandleAIStream 读循环里分发前盖标记（`h.service.DispatchLocalAI(msg, writeJSON)` 之前）：

```go
		if sock != "" {
			msg[services.AgentAIOriginSocketField] = sock
		}
```

（`AgentAIOriginSocketField` 需导出别名：`var AgentAIOriginSocketField = agentAIOriginSocketField` 放 agent_ai_sockets.go，或直接把常量改导出 `AgentAIOriginSocketField` 并同步引用——选后者，一处常量。）

4. 远端路径：`handleRemoteAgentMessage` AI 分支 case 入口（833 行 case 行之后）盖：

```go
		msg[AgentAIOriginSocketField] = agentAIRemoteSocketID
```

`runRemoteAgentSession` 写函数就绪后（~449 writeJSON 定义后）加：

```go
	s.ai.ensureRemoteAISocket(writeJSON)
```

5. `run.originSocketID` 传递链：`message()` 构造 `messageRun` 后、`m.runEmitter(messageRun, writeJSON)` 前补 `messageRun.originSocketID = session.originSocket`（注意 lazy-create 分支 session 在其后才存在——把该赋值放在 lazy-create 注册之后的第二个临界区里，与 originSocket 落 session 同点；runStart()/steer() 路径若有独立 emitter 构造，同款补齐）。

- [ ] **Step 4: 跑测试确认通过 + 编译**

```bash
go test ./app/http/services/ -run TestMessageStampsOriginSocket -v
go build ./...
```

- [ ] **Step 5: 提交**

```bash
git add app/http/handlers/agent_handler.go app/http/services/agent_remote_ws.go app/http/services/agent_ai.go app/http/services/agent_ai_sockets.go app/http/services/agent_ai_sockets_test.go
git commit -m "新增:双分发路径盖 origin socket 标记+远端常驻 socket 注册

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 6: DispatchLocalAI 补 subscribe/unsubscribe/option/tool_ask 路由

**Files:**
- Modify: `app/http/services/agent_remote_ws.go`（DispatchLocalAI 978-1012）
- Test: `app/http/services/agent_ai_sockets_test.go`（追加）

- [ ] **Step 1: 写失败测试**

```go
func TestDispatchLocalAISubscribeRepliesSnapshot(t *testing.T) {
	m := newTestAISocketsManager(t)
	svc := &AgentService{ai: m}
	sock := m.registerAISocket("local", func(interface{}) error { return nil })
	replies := make(chan map[string]interface{}, 8)
	svc.DispatchLocalAI(map[string]interface{}{
		"type":       models.AgentEventAISessionSubscribe,
		"session_id": "any-session",
		agentAIOriginSocketField: sock.id,
	}, func(p interface{}) error {
		replies <- p.(map[string]interface{})
		return nil
	})
	select {
	case p := <-replies:
		if p["type"] != models.AgentEventAISessionSnapshot {
			t.Fatalf("期望快照回包,收到 %v", p["type"])
		}
	case <-time.After(time.Second):
		t.Fatal("订阅未回快照")
	}
}

func TestDispatchLocalAIOptionResponseRouted(t *testing.T) {
	m := newTestAISocketsManager(t)
	svc := &AgentService{ai: m}
	// 无 pending option → 应回 approval_not_found 类状态而非 unsupported
	replies := make(chan map[string]interface{}, 8)
	svc.DispatchLocalAI(map[string]interface{}{
		"type": models.AgentEventAIOptionResponse, "session_id": "s", "option_id": "x", "choice": 0,
	}, func(p interface{}) error { replies <- p.(map[string]interface{}); return nil })
	waitUntilFor(t, time.Second, func() bool { return len(replies) > 0 })
	p := <-replies
	if p["type"] == models.AgentEventAIError && strings.Contains(fmt.Sprint(p["error"]), "unsupported") {
		t.Fatalf("option.response 仍走 unsupported 分支: %v", p)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./app/http/services/ -run 'TestDispatchLocalAISubscribe|TestDispatchLocalAIOption' -v
```

- [ ] **Step 3: 实现**

DispatchLocalAI switch（978-1012）加三个 case：

```go
	case models.AgentEventAISessionSubscribe, models.AgentEventAISessionUnsubscribe:
		// 跨端同态（spec §4.1）：本机聊天连接订阅任意会话（手机发起的、终端
		// 导入的都行）。透传是裸泵，此端点只存在于 agent 进程。
		socketID := strings.TrimSpace(remoteString(msg, agentAIOriginSocketField))
		sessionID := remoteString(msg, "session_id")
		if sessionID == "" {
			_ = writeJSON(agentAIErrorPayload("", "", errors.New("ai.session.subscribe missing session_id")))
			return
		}
		switch msgType {
		case models.AgentEventAISessionSubscribe:
			m.subscribeAISocket(socketID, sessionID)
			s.ai.emitSessionSnapshot(sessionID, writeJSON)
		case models.AgentEventAISessionUnsubscribe:
			m.unsubscribeAISocket(socketID, sessionID)
		}
	case models.AgentEventAIOptionResponse:
		s.ai.optionResponse(msg, writeJSON)
	case models.AgentEventToolAskResponse:
		// 与远端同语义：不受本地开关门（run 阻塞在 ask_user 上时必须能答）。
		s.ai.toolAskResponse(msg, writeJSON)
```

（注意 DispatchLocalAI 是 `func (s *AgentService)`，`m := s.ai` 视现体局部变量情况。）

`emitSessionSnapshot` 加在 agent_ai_sockets.go：

```go
// emitSessionSnapshot 订阅握手回包：转录快照（读侧现成解析）+ 待审批 + 状态。
// 尽力而为：转录取不到（非 journal 会话/读失败）就只回 pending+status，不报错。
func (m *agentAIManager) emitSessionSnapshot(sessionID string, writeJSON agentTerminalWriter) {
	if writeJSON == nil || sessionID == "" {
		return
	}
	transcript := agentAIVibeSessionTranscriptForSnapshot(sessionID) // 见下
	payload := map[string]interface{}{
		"type":       models.AgentEventAISessionSnapshot,
		"session_id": sessionID,
		"status":     m.agentAISessionStatusForSnapshot(sessionID),
	}
	if len(transcript) > 0 {
		payload["transcript"] = transcript
	}
	if pending := m.pendingApprovalsSnapshotForSession(sessionID); len(pending) > 0 {
		payload["pending_approvals"] = pending
	}
	_ = writeJSON(payload)
}
```

配套三个小函数（放 agent_ai_sockets.go）：
- `pendingApprovalsSnapshotForSession(sessionID)`：复制 `pendingApprovalsSnapshot()`（3262-3290）按 sessionID 过滤。
- `agentAISessionStatusForSnapshot`：`m.mu` 下查 `sessions[sessionID]`，`cancel != nil` → "running"，否则 "idle"；查无 → "idle"（订阅未知会话不报错，spec §6）。
- `agentAIVibeSessionTranscriptForSnapshot`：复用读侧——`collectAgentCoreVibeSessions(nil)` 太重；直接调 `readAgentCoreSessionMetaWithOptions` 家族：grep 该文件确认最小可用入口（若无单会话读取入口，新加 `agentCoreJournalTranscript(sessionID) []models.AgentVibeMessage`，逐行解析 msg 行为 `{role, text, ts}`，Task 9 的 `readAgentCoreJournalHistory` 会共享这个解析器——**先实现本函数底层解析器，Task 9 复用**）。

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./app/http/services/ -run 'TestDispatchLocalAI' -v
```

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_remote_ws.go app/http/services/agent_ai_sockets.go app/http/services/agent_ai_sockets_test.go
git commit -m "新增:本地流订阅/退订/快照路由+option与tool_ask本机作答入口

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 7: 审批 ack 双写

**Files:**
- Modify: `app/http/services/agent_ai.go`（approval() 3189-3260）
- Test: `app/http/services/agent_ai_approval_fanout_test.go`（新建）

- [ ] **Step 1: 写失败测试**

测试目标：决策从「非发起 socket」到达时，ack 同时写到「决策 socket」与「run 发起 socket（=session.originSocket 对应 socket）」；同一 socket 不重复写。

```go
func TestApprovalAckDualWrite(t *testing.T) {
	m := newTestAISocketsManager(t)
	runCh := make(chan map[string]interface{}, 8) // 模拟 run 发起通道（手机远端）
	decideCh := make(chan map[string]interface{}, 8)
	runSock := m.registerAISocket("remote", func(p interface{}) error { runCh <- p.(map[string]interface{}); return nil })
	decideSock := m.registerAISocket("local", func(p interface{}) error { decideCh <- p.(map[string]interface{}); return nil })

	// 造一个挂起 waiter（绕过 requestApproval 的 writer 检查，直接注册）。
	// respond 通道先取局部变量：approval() 会把 waiter 从 map 删除后再喂通道，
	// goroutine 起跑后不得再经 m.approvals 裸读（-race 会抓）。
	respCh := make(chan agentAIApprovalResponse, 1)
	m.mu.Lock()
	m.approvals[agentAIApprovalMapKey("s1", "a1")] = &agentAIApprovalWaiter{
		sessionID: "s1", runSeq: 1,
		request: agentAIApprovalRequest{ID: "a1", respond: respCh},
	}
	m.sessions["s1"] = &agentAISession{id: "s1", runSeq: 1, originSocket: runSock.id}
	m.mu.Unlock()

	// 决策从 decideSock 到达
	decideWrite := func(p interface{}) error { decideCh <- p.(map[string]interface{}); return nil }
	go func() {
		m.approval(map[string]interface{}{
			"type": models.AgentEventAIApprovalResponse, "session_id": "s1",
			"approval_id": "a1", "decision": "accept",
			agentAIOriginSocketField: decideSock.id,
		}, decideWrite)
	}()
	resp := <-respCh
	if resp.Decision != models.AgentAIApprovalDecisionAccept {
		t.Fatalf("决策未送达 waiter: %v", resp)
	}
	// ack 出现在两条通道
	waitUntilFor(t, time.Second, func() bool {
		return len(runCh) > 0 && len(decideCh) > 0
	})
}

func TestApprovalAckNoDuplicateOnSameSocket(t *testing.T) {
	m := newTestAISocketsManager(t)
	ch := make(chan map[string]interface{}, 8)
	sock := m.registerAISocket("remote", func(p interface{}) error { ch <- p.(map[string]interface{}); return nil })
	m.mu.Lock()
	m.approvals[agentAIApprovalMapKey("s1", "a1")] = &agentAIApprovalWaiter{
		sessionID: "s1", runSeq: 1,
		request: agentAIApprovalRequest{ID: "a1", respond: make(chan agentAIApprovalResponse, 1)},
	}
	m.sessions["s1"] = &agentAISession{id: "s1", runSeq: 1, originSocket: sock.id}
	m.mu.Unlock()
	m.approval(map[string]interface{}{
		"type": models.AgentEventAIApprovalResponse, "session_id": "s1",
		"approval_id": "a1", "decision": "accept",
		agentAIOriginSocketField: agentAIRemoteSocketID,
	}, func(p interface{}) error { ch <- p.(map[string]interface{}); return nil })
	waitUntilFor(t, time.Second, func() bool { return len(ch) >= 1 })
	time.Sleep(50 * time.Millisecond)
	acks := 0
	for len(ch) > 0 {
		if p := <-ch; p["type"] == models.AgentEventAIApprovalAck {
			acks++
		}
	}
	if acks != 1 {
		t.Fatalf("同 socket ack 应只写一次,实际 %d", acks)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./app/http/services/ -run TestApprovalAck -v
```

- [ ] **Step 3: 实现**

`approval()`（3189）改造 ack 闭包（3199-3212）：

```go
	// 跨端同态（spec §4.2.2）：ack 双写——决策到达的 socket（现有语义，重投
	// 引擎的对端确认）+ run 发起的 socket（修「仪表盘抢答手机回合时 PhoneServer
	// 收不到 ack 重投 10 次」）。同 socket 只写一次。
	originSocket := strings.TrimSpace(remoteString(msg, agentAIOriginSocketField))
	runSocket := ""
	m.mu.Lock()
	if s := m.sessions[sessionID]; s != nil {
		runSocket = s.originSocket
	}
	m.mu.Unlock()
	ack := func(result string) {
		payload := map[string]interface{}{
			"type":        models.AgentEventAIApprovalAck,
			"session_id":  sessionID,
			"approval_id": approvalID,
			"result":      result,
		}
		if deliveryID != "" {
			payload["delivery_id"] = deliveryID
		}
		if writeJSON != nil {
			_ = writeJSON(payload)
		}
		if runSocket != "" && runSocket != originSocket {
			m.mu.Lock()
			sock := m.aiSockets[runSocket]
			m.mu.Unlock()
			if sock != nil && sock.alive() {
				sock.enqueue(payload)
			}
		}
	}
```

- [ ] **Step 4: 跑测试确认通过 + 审批相关既有用例回归**

```bash
go test ./app/http/services/ -run TestApprovalAck -v
go test ./app/http/services/ -run 'Approval' -v
```

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai.go app/http/services/agent_ai_approval_fanout_test.go
git commit -m "修复:审批 ack 双写(决策socket+run发起socket),根除抢答后服务端重投滞留

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 8: 审批 expires_at + 订阅快照带审批

**Files:**
- Modify: `app/http/services/agent_ai.go`（requestApproval 3519-3599、agentAIApprovalWaiter 680-685、pendingApprovalsSnapshot 3262-3290、agentAIApprovalRequestPayload 8980-9024）
- Test: `app/http/services/agent_ai_approval_fanout_test.go`（追加）

- [ ] **Step 1: 写失败测试**

```go
func TestApprovalRequestCarriesExpiresAt(t *testing.T) {
	m := newTestAISocketsManager(t)
	var mu sync.Mutex
	var sent map[string]interface{}
	write := func(p interface{}) error {
		mu.Lock()
		sent = p.(map[string]interface{})
		mu.Unlock()
		return nil
	}
	run := agentAIRun{sessionID: "s-exp", runSeq: 1, messageID: "m1", provider: "agentcore"}
	m.mu.Lock()
	m.sessions["s-exp"] = &agentAISession{id: "s-exp", runSeq: 1, originSocket: "remote"}
	m.mu.Unlock()
	go func() {
		_, _ = m.requestApproval(context.Background(), run, write, agentAIApprovalRequest{ID: "a-exp"})
	}()
	waitUntilFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return sent != nil
	})
	mu.Lock()
	if _, ok := sent["expires_at"].(string); !ok {
		mu.Unlock()
		t.Fatalf("ai.approval.request 缺 expires_at: %v", sent)
	}
	mu.Unlock()
	// waiter 上也要有（快照用）
	m.mu.Lock()
	w := m.approvals[agentAIApprovalMapKey("s-exp", "a-exp")]
	m.mu.Unlock()
	if w == nil || w.expiresAt.IsZero() {
		t.Fatal("waiter.expiresAt 未记录")
	}
	// 快照带 expires_at
	snap := m.pendingApprovalsSnapshot()
	found := false
	for _, e := range snap {
		if e["approval_id"] == "a-exp" {
			if _, ok := e["expires_at"].(string); !ok {
				t.Fatal("快照缺 expires_at")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("快照缺该审批项")
	}
	m.mu.Lock()
	delete(m.approvals, agentAIApprovalMapKey("s-exp", "a-exp"))
	m.mu.Unlock()
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./app/http/services/ -run TestApprovalRequestCarriesExpiresAt -v
```

- [ ] **Step 3: 实现**

1. `agentAIApprovalWaiter`（680-685）加 `expiresAt time.Time`。
2. `requestApproval`：注册 waiter 前算 `deadline := time.Now().UTC().Add(agentAIApprovalTimeout)`，waiter 存 `expiresAt: deadline`，发送 payload 前注入：

```go
	payload := agentAIApprovalRequestPayload(req)
	payload["expires_at"] = deadline.Format(time.RFC3339)
	...
	if err := writeAgentAIApprovalRequest(ctx, writeJSON, payload, 5, time.Second); err != nil {
```

3. `pendingApprovalsSnapshot` 条目加：

```go
		if !waiter.expiresAt.IsZero() {
			entry["expires_at"] = waiter.expiresAt.Format(time.RFC3339)
		}
```

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./app/http/services/ -run TestApprovalRequestCarriesExpiresAt -v
```

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai.go app/http/services/agent_ai_approval_fanout_test.go
git commit -m "新增:审批 pending 帧与快照带 expires_at(24h 等待两端可见)

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 9: 会话级批准记忆（accept_for_session 三通道齐平）

**Files:**
- Create: `app/http/services/agent_ai_session_approvals.go`
- Modify: `app/http/services/agent_ai.go`（approval() 记录点；handleClaudeApprovalHook 检查点 3692-3750）、`agent_ai_agentcore.go`（agentCoreApprove 1622-1664 检查点）、`agent_ai.go` codexAppServerApprovalResult（5605-5635 检查点）
- Test: `app/http/services/agent_ai_session_approvals_test.go`（新建）

**匹配语义（计划期钉死，spec §4.2.4 留白处）：**
- `kind=command` → 键 = `cmd:<去除首尾空白的原命令串>`（命令逐字匹配——「本会话放行这条命令」，不放大到全部 Bash）；
- 其余 kind（tool/file_change/permissions）→ 键 = `tool:<tool_name>`（同工具名放行；codex permissions 本有原生 scope=session，叠加 agent 侧记忆后不再云端往返）。
- 容量：每会话键上限 64，会话数上限 32（LRU 简化为超限全清，与 completedApprovals 同风格）。随进程内存态，不持久化（spec §9 已声明）。

- [ ] **Step 1: 写失败测试**

```go
func TestSessionApprovalMemoryHitSkipsCloud(t *testing.T) {
	m := newTestAISocketsManager(t)
	// 记忆：会话 s1 放行 command:ls -la
	m.rememberSessionApproval("s1", "claude", models.AgentAIApprovalKindCommand, "Bash", "ls -la")
	if !m.sessionApprovalAllows("s1", "claude", models.AgentAIApprovalKindCommand, "Bash", "ls -la") {
		t.Fatal("同键应命中记忆")
	}
	if m.sessionApprovalAllows("s1", "claude", models.AgentAIApprovalKindCommand, "Bash", "rm -rf /") {
		t.Fatal("不同命令不应命中")
	}
	if m.sessionApprovalAllows("s2", "claude", models.AgentAIApprovalKindCommand, "Bash", "ls -la") {
		t.Fatal("跨会话不应命中")
	}
	if m.sessionApprovalAllows("s1", "codex", models.AgentAIApprovalKindCommand, "Bash", "ls -la") {
		t.Fatal("跨 provider 不应命中")
	}
}

func TestSessionApprovalToolKindMatchesByName(t *testing.T) {
	m := newTestAISocketsManager(t)
	m.rememberSessionApproval("s1", "agentcore", models.AgentAIApprovalKindTool, "mcp__x__y", "")
	if !m.sessionApprovalAllows("s1", "agentcore", models.AgentAIApprovalKindTool, "mcp__x__y", "任何输入") {
		t.Fatal("tool 类按名匹配应命中")
	}
}

func TestSessionApprovalCapClears(t *testing.T) {
	m := newTestAISocketsManager(t)
	for i := 0; i < 100; i++ {
		m.rememberSessionApproval(fmt.Sprintf("s-%d", i), "claude", models.AgentAIApprovalKindTool, "t", "")
	}
	if len(m.sessionApprovals) > 32 {
		t.Fatalf("会话数超限未清: %d", len(m.sessionApprovals))
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./app/http/services/ -run TestSessionApproval -v
```

- [ ] **Step 3: 实现 `agent_ai_session_approvals.go`**

```go
package services

// 会话级批准记忆（2026-10-28 跨端同态，spec §4.2.4）：用户对某回合选
// accept_for_session 时记录 (sessionID, provider, kind, 匹配键)，本会话内
// 同键后续请求直接放行（claude/agentcore/codex 三通道统一语义；codex 同时
// 保留其原生 scope=session 行为）。内存态、随进程消失，不落策略规则。
//
// 匹配键（计划期钉死）：
//   kind=command → "cmd:<原命令>"（逐字匹配，不放大到全部命令）
//   其余         → "tool:<tool_name>"（按工具名）

import (
	"fmt"
	"strings"

	"aliang.one/nursorgate/app/http/models"
)

const (
	agentAISessionApprovalSessionCap = 32
	agentAISessionApprovalKeyCap     = 64
)

func agentAISessionApprovalKey(kind, toolName, command string) string {
	if kind == models.AgentAIApprovalKindCommand && strings.TrimSpace(command) != "" {
		return "cmd:" + strings.TrimSpace(command)
	}
	return "tool:" + toolName
}

// rememberSessionApproval 记录一次会话级放行。容量超限全清（防膨胀）。
func (m *agentAIManager) rememberSessionApproval(sessionID, provider, kind, toolName, command string) {
	if sessionID == "" || provider == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessionApprovals == nil {
		m.sessionApprovals = make(map[string]map[string]struct{})
	}
	if len(m.sessionApprovals) >= agentAISessionApprovalSessionCap {
		if _, alive := m.sessionApprovals[sessionID]; !alive {
			m.sessionApprovals = make(map[string]map[string]struct{})
		}
	}
	keys := m.sessionApprovals[sessionID]
	if keys == nil {
		keys = make(map[string]struct{})
		m.sessionApprovals[sessionID] = keys
	}
	if len(keys) >= agentAISessionApprovalKeyCap {
		m.sessionApprovals[sessionID] = make(map[string]struct{})
		keys = m.sessionApprovals[sessionID]
	}
	keys[provider + "\x00" + agentAISessionApprovalKey(kind, toolName, command)] = struct{}{}
}

// sessionApprovalAllows 查询会话级放行。
func (m *agentAIManager) sessionApprovalAllows(sessionID, provider, kind, toolName, command string) bool {
	if sessionID == "" || provider == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := m.sessionApprovals[sessionID]
	if keys == nil {
		return false
	}
	_, ok := keys[provider+"\x00"+agentAISessionApprovalKey(kind, toolName, command)]
	return ok
}
```

`agentAIManager` 加字段 `sessionApprovals map[string]map[string]struct{}`（newAgentAIManager 无需显式 init，惰性）。

**记录点**：`approval()`（3189）在 waiter 命中、**`m.mu.Unlock()` 之后**、喂 respond 之前（`rememberSessionApproval`
自身取 `m.mu`，绝不能在持有 m.mu 的临界区内调用——否则死锁）：

```go
	if decision == models.AgentAIApprovalDecisionAcceptForSession {
		m.rememberSessionApproval(sessionID, waiter.request.Provider, waiter.request.Kind, waiter.request.ToolName, waiter.request.Command)
	}
```

（插入位置：`m.mu.Unlock()` 之后的 `if waiter == nil` 守卫与 `waiter.request.respond <- response` 之间；
waiter 局部变量此时已拷出，provider/kind/toolName/command 从 `waiter.request` 读。）

**检查点三处**（各在 policy require_approval 落到 requestApproval 之前）：
- `handleClaudeApprovalHook`（~3727 `req := buildClaudeApprovalRequest(run, raw)` 前）：

```go
	if preReq := buildClaudeApprovalRequest(run, raw); m.sessionApprovalAllows(sessionID, run.provider, preReq.Kind, preReq.ToolName, preReq.Command) {
		logger.Info(fmt.Sprintf("approval-hook: SESSION-APPROVED (memory) tool=%s kind=%s session=%s", preReq.ToolName, preReq.Kind, sessionID))
		return claudeApprovalHookDecision(hookEventName, true, "approved for this session"), nil
	}
```

- `agentCoreApprove`（1622，policy 块后、`requestApproval` 前）：

```go
		if pre := buildAgentCoreApprovalRequest(run, call); m.sessionApprovalAllows(run.sessionID, run.provider, pre.Kind, pre.ToolName, pre.Command) {
			logger.Info(fmt.Sprintf("approval-hook: SESSION-APPROVED (memory) tool=%s kind=%s session=%s (agentcore)", pre.ToolName, pre.Kind, run.sessionID))
			return agentcore.ApprovalResponse{Approved: true}, nil
		}
```

- `codexAppServerApprovalResult`（5605，policy 块后）同款，用 `buildCodexApprovalRequest(run, method, params, fileChanges)`。

- [ ] **Step 4: 跑测试确认通过 + 审批族回归**

```bash
go test ./app/http/services/ -run 'TestSessionApproval|Approval' -v
```

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai_session_approvals.go app/http/services/agent_ai_session_approvals_test.go app/http/services/agent_ai.go app/http/services/agent_ai_agentcore.go
git commit -m "新增:会话级批准记忆(accept_for_session 三通道齐平,命令逐字/工具按名)

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 10: journal 头行幂等补写 + origin 字段

**Files:**
- Modify: `app/http/services/agent_ai_agentcore_journal.go`（ensureSessionHeader 73-106）
- Modify: `app/http/services/agent_ai_agentcore.go`（runAgentCore 入口 975-986 传 origin）
- Test: `app/http/services/agent_ai_agentcore_journal_test.go`（追加）

- [ ] **Step 1: 写失败测试**

```go
func TestEnsureSessionHeaderBackfillsHeadlessFile(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	// 模拟 steer 先于 run：文件已存在但无头行
	path := filepath.Join(dir, agentCoreJournalFileName("s-headless"))
	os.WriteFile(path, []byte(`{"kind":"msg","role":"user","message_id":"m1","text":"hi","ts":"2026-10-28T00:00:00Z"}`+"\n"), 0o600)

	j.ensureSessionHeader("s-headless", "/proj", "glm-5.3", "2026-10-28T01:00:00Z", "dashboard")

	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var head map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &head); err != nil {
		t.Fatalf("首行非法: %v", err)
	}
	if head["kind"] != "session" {
		t.Fatalf("首行应补为 session 头,实际 %v", head["kind"])
	}
	if head["origin"] != "dashboard" {
		t.Fatalf("origin 未落: %v", head["origin"])
	}
	if len(lines) != 2 {
		t.Fatalf("原内容应保留,实际 %d 行", len(lines))
	}
	// 幂等：再调一次不重复
	j.ensureSessionHeader("s-headless", "/proj", "glm-5.3", "2026-10-28T01:00:00Z", "dashboard")
	data2, _ := os.ReadFile(path)
	if len(strings.Split(strings.TrimSpace(string(data2)), "\n")) != 2 {
		t.Fatal("幂等补写被破坏")
	}
}

func TestEnsureSessionHeaderOriginPhone(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.ensureSessionHeader("s-origin", "/proj", "m", "2026-10-28T00:00:00Z", "phone")
	data, _ := os.ReadFile(filepath.Join(dir, agentCoreJournalFileName("s-origin")))
	var head map[string]interface{}
	_ = json.Unmarshal(bytes.Split(data, []byte("\n"))[0], &head)
	if head["origin"] != "phone" {
		t.Fatalf("origin=phone 未落: %v", head)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./app/http/services/ -run TestEnsureSessionHeader -v
```

- [ ] **Step 3: 实现**

`ensureSessionHeader` 改签名（加 `origin string`）与逻辑：

```go
// ensureSessionHeader 保证会话文件第一行是头行（kind=session）。文件不存在
// 时创建；存在但无头行（steer 先于 run 的历史竞态，spec §4.3）时整体重写
// 补头——读全量、写 头行+原内容、原子 rename。文件体量有界（消息+截断
// 工具行），重写代价可接受。幂等：已有头行直接返回。
func (j *agentCoreJournal) ensureSessionHeader(sessionID, projectPath, model, createdAt, origin string) {
	if j == nil || j.dir == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	path := filepath.Join(j.dir, agentCoreJournalFileName(sessionID))
	if _, err := os.Stat(path); err == nil {
		if j.hasSessionHeaderLocked(path) {
			return
		}
		j.prependHeaderLocked(path, sessionID, projectPath, model, createdAt, origin)
		return
	}
	// ……原创建路径不变，头行 map 增加 "origin": origin
}
```

两个私有助手：

```go
func (j *agentCoreJournal) sessionHeaderLine(sessionID, projectPath, model, createdAt, origin string) []byte {
	row := map[string]interface{}{
		"kind":         "session",
		"session_id":   sessionID,
		"project_path": projectPath,
		"model":        model,
		"created_at":   createdAt,
		"origin":       origin,
	}
	line, err := jsonMarshalLine(row)
	if err != nil {
		return nil
	}
	return line
}

func (j *agentCoreJournal) hasSessionHeaderLocked(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return false
	}
	var head map[string]interface{}
	if json.Unmarshal(sc.Bytes(), &head) != nil {
		return false
	}
	return head["kind"] == "session"
}

// prependHeaderLocked 整体重写：tmp 文件写 头行+原内容，rename 原子替换。
func (j *agentCoreJournal) prependHeaderLocked(path, sessionID, projectPath, model, createdAt, origin string) {
	head := j.sessionHeaderLine(sessionID, projectPath, model, createdAt, origin)
	if head == nil {
		return
	}
	old, err := os.ReadFile(path)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	var buf bytes.Buffer
	buf.Write(head)
	buf.Write(old)
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		logger.Warn(fmt.Sprintf("agentcore-journal: header backfill write failed session=%s err=%v", sessionID, err))
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		logger.Warn(fmt.Sprintf("agentcore-journal: header backfill rename failed session=%s err=%v", sessionID, err))
		_ = os.Remove(tmp)
		return
	}
	logger.Info(fmt.Sprintf("agentcore-journal: session header backfilled session=%s", sessionID))
}
```

调用点更新：`runAgentCore`（975-986）改传 origin：

```go
	journal.ensureSessionHeader(run.sessionID, run.projectPath, agentCoreModel(run), now, agentCoreJournalOrigin(run))
```

```go
// agentCoreJournalOriginFor 头行来源统一推导（run 路径与斜杠命令路径共用，
// Task 12 也调它）：以发起 socket 推导（Task 5 盖的 originSocketID）——
// remote 常驻 socket=phone，本地聊天 socket（aisock-* 前缀）=dashboard；
// socket 标记缺失（旧调用方/TUI/测试路径）才回落 surface（只认 "phone"，
// normalizeAgentAISurface 白名单仅此一词——刻意不动它，避免碰
// agent_ai.go:1831 的「只增不降」覆写语义）。phone 会话未来被本机续聊时，
// origin 忠实记录「这一回合是谁发的」。
func agentCoreJournalOriginFor(originSocketID, surface string) string {
	switch {
	case originSocketID == agentAIRemoteSocketID:
		return "phone"
	case strings.HasPrefix(originSocketID, "aisock-"):
		return "dashboard"
	case surface == "phone":
		return "phone"
	default:
		return "unknown"
	}
}

func agentCoreJournalOrigin(run agentAIRun) string {
	return agentCoreJournalOriginFor(run.originSocketID, run.surface)
}
```

（`agentAIRun.surface` 字段**已存在**且已从 `session.surface` 填充（agent_ai.go:400/2777），**不要重复添加**。
分发路径无需盖 `msg["surface"]`——origin 全部由 Task 5 的 `__origin_socket` 标记推导。）

`journalSteerUserMessage` 上方注释中「不要试图补写头行」一段改为指向新行为（补写已实现）。

- [ ] **Step 4: 跑测试确认通过 + journal 族回归**

```bash
go test ./app/http/services/ -run 'TestEnsureSessionHeader|AgentCoreJournal' -v
```

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai_agentcore_journal.go app/http/services/agent_ai_agentcore_journal_test.go app/http/services/agent_ai_agentcore.go app/http/services/agent_ai.go app/http/services/agent_remote_ws.go
git commit -m "修复:journal 头行幂等补写(steer 先到竞态)+origin 来源字段

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 11: journal 读回历史（agentcore 跨端/重启续聊）

**Files:**
- Create: `app/http/services/agent_ai_journal_history.go`（解析器 + 读回函数；Task 6 的快照转录底层解析器若未抽公共函数，此处抽）
- Modify: `app/http/services/agent_ai.go`（message() lazy-create 块 1749-1821）
- Test: `app/http/services/agent_ai_journal_history_test.go`（新建）

- [ ] **Step 1: 写失败测试**

```go
func TestReadAgentCoreJournalHistory(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.ensureSessionHeader("s-hist", "/proj", "glm-5.3", "2026-10-28T00:00:00Z", "phone")
	j.appendUser("s-hist", "m1", "第一问", "2026-10-28T00:01:00Z")
	j.appendAssistant("s-hist", "a1", "第一答", "2026-10-28T00:02:00Z")
	j.appendTool("s-hist", "Bash", "ls", false)
	j.appendUser("s-hist", "m2", "第二问", "2026-10-28T00:03:00Z")
	j.appendEnd("s-hist", "success", "")
	// 残行
	f, _ := os.OpenFile(filepath.Join(dir, agentCoreJournalFileName("s-hist")), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"kind":"msg","role":"user"`) // 半行
	f.Close()

	hist, head, err := readAgentCoreJournalHistory("s-hist")
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if len(hist) != 3 {
		t.Fatalf("应回 3 条消息(user/a/user),实际 %d: %+v", len(hist), hist)
	}
	if hist[0].Role != "user" || hist[0].Content != "第一问" {
		t.Fatalf("首条错: %+v", hist[0])
	}
	if head.projectPath != "/proj" || head.model != "glm-5.3" {
		t.Fatalf("头信息错: %+v", head)
	}
}

func TestReadAgentCoreJournalHistoryMissing(t *testing.T) {
	if _, _, err := readAgentCoreJournalHistory("no-such"); err == nil {
		t.Fatal("不存在应报错(调用方回退 lazy-create)")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./app/http/services/ -run TestReadAgentCoreJournalHistory -v
```

- [ ] **Step 3: 实现**

```go
package services

// journal 读回（2026-10-28 跨端续聊，spec §4.3）：agentcore 会话不在内存时，
// 从 journal 文件重建 history 装回内存再跑。读回的是「journal 所见」——
// 斜杠命令/半截输出在旧文件里本就无痕（spec §9 已声明）。残行/非法行跳过。

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type agentCoreJournalHead struct {
	projectPath string
	model       string
	origin      string
}

// readAgentCoreJournalHistory 解析 journal 全部 msg 行为内存 history
//（顺序=文件顺序）。os.ErrNotExist 透传（调用方回退 lazy-create）；
// 其余 IO 错误也返回 error（调用方发 history_restore_failed 提示）。
func readAgentCoreJournalHistory(sessionID string) ([]agentAIMessage, agentCoreJournalHead, error) {
	dir := agentCoreJournalDir()
	if dir == "" || sessionID == "" {
		return nil, agentCoreJournalHead{}, os.ErrNotExist
	}
	data, err := os.ReadFile(filepath.Join(dir, agentCoreJournalFileName(sessionID)))
	if err != nil {
		return nil, agentCoreJournalHead{}, err
	}
	var head agentCoreJournalHead
	var hist []agentAIMessage
	for _, line := range splitJSONLLines(data) {
		var row map[string]interface{}
		if json.Unmarshal(line, &row) != nil {
			continue // 残行容忍
		}
		switch row["kind"] {
		case "session":
			head.projectPath, _ = row["project_path"].(string)
			head.model, _ = row["model"].(string)
			head.origin, _ = row["origin"].(string)
		case "msg":
			role, _ := row["role"].(string)
			text, _ := row["text"].(string)
			msgID, _ := row["message_id"].(string)
			if role != "user" && role != "assistant" || text == "" {
				continue
			}
			msg := agentAIMessage{Role: role, MessageID: msgID, Content: text}
			if ts, ok := row["ts"].(string); ok {
				if t, err := time.Parse(time.RFC3339, ts); err == nil {
					msg.CreatedAt = t
				}
			}
			hist = append(hist, msg)
		}
	}
	return hist, head, nil
}

// splitJSONLLines 切 JSONL：按 \n 分行，跳过空行与残行（不完整 JSON 由调用方
// Unmarshal 失败时丢弃）。
func splitJSONLLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i <= len(data); i++ {
		if i == len(data) || data[i] == '\n' {
			line := data[start:i]
			if len(line) > 0 {
				out = append(out, line)
			}
			start = i + 1
		}
	}
	return out
}
```

`message()` lazy-create 块（1749，在 `lazyHistory := remoteAgentAIHistory(msg)` 之后、limits 截断之前）插入：

```go
		// 跨端/重启续聊（spec §4.3）：agentcore 会话优先从 journal 读回历史。
		// 读回失败（非 ErrNotExist 的 IO 错）发 ai.run.notice 提示后照常空起。
		if lazyProvider == "agentcore" && len(lazyHistory) == 0 {
			hist, head, herr := readAgentCoreJournalHistory(sessionID)
			switch {
			case herr == nil && len(hist) > 0:
				lazyHistory = hist
				headModel = head.model // 头行模型仅兜底：构造 session 的 model: 字段处用 firstNonEmpty(载荷model, headModel)
			case herr != nil && !errors.Is(herr, os.ErrNotExist):
				logger.Warn(fmt.Sprintf("ai.message: journal history restore failed session=%s err=%v", sessionID, herr))
				restoreFailed = herr // notice 帧在 session 注册后发（用 runWrite）
			}
		}
```

（`headModel`/`restoreFailed` 是插入点上方声明的局部变量（`var headModel string` / `var restoreFailed error`）；
构造 session 的 `model:` 字段改 `strings.TrimSpace(remoteString(msg, "model"))` → `firstNonEmpty(strings.TrimSpace(remoteString(msg, "model")), headModel)`；
notice 帧在 lazy-create 注册完成后发，**必须包在 `if restoreFailed != nil` 里**（happy path 上它是 nil，裸发会 nil panic）：

```go
			_ = runWrite(map[string]interface{}{
				"type": models.AgentEventAIRunNotice, "session_id": sessionID,
				"code": "history_restore_failed", "detail": restoreFailed.Error(),
			})
```

放在注册分支 else 之后统一位置。）

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./app/http/services/ -run TestReadAgentCoreJournalHistory -v
go build ./...
```

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai_journal_history.go app/http/services/agent_ai_journal_history_test.go app/http/services/agent_ai.go
git commit -m "新增:journal 读回历史,agentcore 会话跨端/重启续聊装回内存

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 12: agentcore 斜杠命令落 journal

**Files:**
- Modify: `app/http/services/agent_ai.go`（handleLocalSlashCommand 2014-2077 通用分支：journal user+assistant 行）
- Modify: `app/http/services/agent_ai_agentcore.go`（handleAgentCoreCompactCommand 600-687：journal 命令与结果行）
- Test: `app/http/services/agent_ai_slash_journal_test.go`（新建）

- [ ] **Step 1: 写失败测试**

```go
func TestLocalSlashCommandJournaled(t *testing.T) {
	dir := t.TempDir()
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(dir)
	m.mu.Lock()
	m.sessions["s-slash"] = &agentAISession{id: "s-slash", provider: "agentcore", mode: "vibe", surface: "dashboard"}
	m.mu.Unlock()
	sess := func() *agentAISession { m.mu.Lock(); defer m.mu.Unlock(); return m.sessions["s-slash"] }()
	m.handleLocalSlashCommand(sess, "msg-1", "/help", "agentcore", func(interface{}) error { return nil })

	hist, _, err := readAgentCoreJournalHistory("s-slash")
	if err != nil {
		t.Fatalf("journal 未落: %v", err)
	}
	if len(hist) != 2 || hist[0].Content != "/help" || hist[1].Role != "assistant" {
		t.Fatalf("斜杠命令应落 user+assistant 两行,实际 %+v", hist)
	}
}

func TestAgentCoreCompactJournaled(t *testing.T) {
	dir := t.TempDir()
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(dir)
	m.mu.Lock()
	m.sessions["s-c"] = &agentAISession{id: "s-c", provider: "agentcore", surface: "phone"}
	m.mu.Unlock()
	// 无上游：Compact 会失败退出——我们只断言 user 行已落（命令本身留痕）
	sess := func() *agentAISession { m.mu.Lock(); defer m.mu.Unlock(); return m.sessions["s-c"] }()
	m.handleAgentCoreCompactCommand(sess, "msg-2", func(interface{}) error { return nil })
	hist, _, err := readAgentCoreJournalHistory("s-c")
	if err != nil || len(hist) < 1 || hist[0].Content != "/compact" {
		t.Fatalf("/compact 命令行未落: err=%v hist=%+v", err, hist)
	}
}
```

（`handleAgentCoreCompactCommand` 第一参数是 `*agentAISession`，注意其内部 `session.id` 引用。）

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./app/http/services/ -run 'TestLocalSlashCommandJournaled|TestAgentCoreCompactJournaled' -v
```

- [ ] **Step 3: 实现**

`handleLocalSlashCommand`（2014）：进入 builtin 处理前（`assistantID := ...` 附近）先落头行与命令行：

```go
	// 斜杠命令也是对话的一部分（spec §4.5）：agentcore 无原生转录，必须
	// journal 留痕。claude/codex 由各自原生转录承载（spec amendment），这里
	// 仅 provider=agentcore 落。
	if provider == "agentcore" {
		m.coreJournal.ensureSessionHeader(session.id, session.projectPath, session.model, time.Now().UTC().Format(time.RFC3339), agentCoreJournalOriginFor(session.originSocket, session.surface))
		m.coreJournal.appendUser(session.id, messageID, content, time.Now().UTC().Format(time.RFC3339))
	}
```

`applyLocalSlashCommand` 返回 line 后、ai.done 发出前：

```go
	if provider == "agentcore" && line != "" {
		m.coreJournal.appendAssistant(session.id, assistantID, line, time.Now().UTC().Format(time.RFC3339))
	}
```

`handleAgentCoreCompactCommand`（600）：`assistantID` 构造附近加：

```go
	m.coreJournal.ensureSessionHeader(session.id, projectPath, model, time.Now().UTC().Format(time.RFC3339), agentCoreJournalOriginFor(session.originSocket, session.surface))
	m.coreJournal.appendUser(session.id, messageID, "/compact", time.Now().UTC().Format(time.RFC3339))
```

压缩成功分支（`reply(fmt.Sprintf("✓ 上下文已压缩...")` 后）加：

```go
	m.coreJournal.appendAssistant(session.id, assistantID, fmt.Sprintf("✓ 上下文已压缩：约 %s → %s tokens（%d 条早期消息已折叠为摘要）。",
		agentAITokenCountLabel(info.PreTokens), agentAITokenCountLabel(info.PostTokens), info.Removed), time.Now().UTC().Format(time.RFC3339))
```

失败/无上下文分支同款落 assistant 行（`reply(...)` 文本即 journal 文本）。

（origin 统一走 Task 10 的 `agentCoreJournalOriginFor(originSocketID, surface)`：斜杠命令无 run 上下文，
但 `session.originSocket`（Task 5 落字段）同样可用——本机会话的斜杠命令会如实落 `dashboard`，
手机会话落 `phone`，与 run 路径同源；无标记时回落 surface/unknown。）

**注意**：claude 的 `/compact`（`return false` 落回 runUserMessage 的真实模型回合）与 codex 斜杠命令**不落** `.aliang` journal（spec amendment）——测试里对 claude provider 断言 journal 为空可选加一条防回归。

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./app/http/services/ -run 'Journaled' -v
```

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai.go app/http/services/agent_ai_agentcore.go app/http/services/agent_ai_slash_journal_test.go
git commit -m "新增:agentcore 斜杠命令(/compact /clear 等)落 journal user+assistant 行

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 13: 内核工具 tool 行（内置工具）

**Files:**
- Modify: `app/agentcore/loop.go`（LoopHooks 定义 73-101 加 OnToolCall；executeTools 1033-1036 调用）
- Modify: `app/http/services/agent_ai_agentcore.go`（agentCoreLoopHooks 1334-1348 实现 OnToolCall → journal）
- Test: `app/agentcore/loop_toolhook_test.go`（新建，内核级）+ services 侧断言（并入 Task 12 测试文件）

- [ ] **Step 1: 写失败测试（内核级）**

```go
package agentcore

import (
	"context"
	"testing"
)

type hookProbeTool struct{ name string }

func (t *hookProbeTool) Name() string                        { return t.name }
func (t *hookProbeTool) Description() string                 { return "probe" }
func (t *hookProbeTool) InputSchema() map[string]interface{} { return map[string]interface{}{"type": "object"} }
// 注意 agentcore.Tool 的 Execute 签名无 error（app/agentcore/tools.go:90-95）。
func (t *hookProbeTool) Execute(ctx context.Context, input map[string]interface{}, env *ToolEnv) ToolResult {
	return ToolResult{Content: "ok"}
}

func TestOnToolCallHookFires(t *testing.T) {
	var mu = make(chan struct{}, 1)
	fired := false
	// …用既有 runner 测试脚手架（参照 loop 包现有测试的最小 run 用例）构造
	// 一次带工具调用的 run，Hooks.OnToolCall = func(name, input, res){ fired=true; mu<-struct{}{} }
	// 断言：每个工具结果产出处恰好触发一次，name/input/result 正确。
	_ = mu
	_ = fired
	// 实现时按 loop 包现有测试样例补全——此骨架先锁「钩子字段必须存在且被调」。
	t.Skip("骨架占位：实现时按包内既有 runner 测试补全 run 驱动（见 Step 3 说明）")
}
```

（内核包已有 runner 测试基建——实现者 grep `loop` 包 `_test.go` 里最小带工具用例照抄驱动；**必须让测试真实执行**：`go test -run TestOnToolCallHookFires -v` 不能出现 `(skipped)` 之外的 PASS 之外的假象，遵守 memory go-implicit-goos-suffix-test-trap：测试名不含 GOOS 尾段。）

- [ ] **Step 2: 确认失败**

```bash
go test ./app/agentcore/ -run TestOnToolCallHookFires -v
```

- [ ] **Step 3: 实现**

`LoopHooks`（loop.go:73-101，`OnCompact` 字段旁 97-100）加：

```go
	// OnToolCall 每个工具结果产出处触发一次（内置+桥接工具统一）。宿主用于
	// journal 工具轨迹（2026-10-28 spec §4.5）。nil=不启用。
	OnToolCall func(name string, input map[string]interface{}, result ToolResult)
```

`executeTools`（1033-1036）`r.toolCalls = append(...)` 后加：

```go
		if r.cfg.Hooks != nil && r.cfg.Hooks.OnToolCall != nil {
			func() {
				defer func() { _ = recover() }() // 宿主回调不许杀内核（与 Approve 桥同款纵深）
				r.cfg.Hooks.OnToolCall(tu.Name, tu.Input, result)
			}()
		}
```

宿主 `agentCoreLoopHooks`（agent_ai_agentcore.go:1334-1348）加：

```go
		OnToolCall: func(name string, input map[string]interface{}, res agentcore.ToolResult) {
			summary := summarizeAgentCoreKernelTool(name, input)
			m.coreJournal.appendTool(run.sessionID, name, summary, res.IsError)
		},
```

`summarizeAgentCoreKernelTool` 新助手（放 agent_ai_agentcore.go 或 journal 文件）：拼 `name key=value` 摘要，**整体截断 4KB**（`if len(summary) > 4096 { summary = summary[:4096] + "…(truncated)" }`；按 rune 截防切 UTF-8——用 `[]rune` 或 `strings.ToValidUTF8` 处理，参照 36bcc43 的教训）。

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./app/agentcore/ -run TestOnToolCallHookFires -v
go build ./...
```

- [ ] **Step 5: 提交**

```bash
git add app/agentcore/loop.go app/agentcore/loop_toolhook_test.go app/http/services/agent_ai_agentcore.go
git commit -m "新增:内核 LoopHooks.OnToolCall,内置工具轨迹落 journal(4KB 截断防膨胀)

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 14: 半截输出落盘 + 审批审计行 + resume_missing 提示

**Files:**
- Modify: `app/http/services/agent_ai_agentcore.go`（runAgentCore stopped/error 出口 1215-1260）
- Modify: `app/http/services/agent_ai.go`（rememberCompletedApprovalLocked 3903-3918 加 journal 审计；claude resume-missing 自愈点 ~4123-4144 发 ai.run.notice）
- Test: `app/http/services/agent_ai_journal_history_test.go`（追加）

- [ ] **Step 1: 写失败测试**

审批审计行（直接测 remember 路径的 journal 副作用；remember 在锁内，journal 自带锁不死锁）：

```go
func TestApprovalDecisionJournaledAsAuditRow(t *testing.T) {
	dir := t.TempDir()
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(dir)
	m.coreJournal.ensureSessionHeader("s-audit", "/p", "m", time.Now().UTC().Format(time.RFC3339), "phone")
	m.mu.Lock()
	m.rememberCompletedApprovalLocked("ap-1", "s-audit", 3, agentAIApprovalResponse{Decision: models.AgentAIApprovalDecisionDecline})
	m.mu.Unlock()
	// 审计行是 kind=approval——readAgentCoreJournalHistory 只回 msg，直接读文件断言
	data, err := os.ReadFile(filepath.Join(dir, agentCoreJournalFileName("s-audit")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"kind":"approval"`) {
		t.Fatalf("缺审批审计行: %s", data)
	}
}
```

（半截输出与 resume_missing 属集成行为，靠 Task 15 的联调清单与既有 stopped 路径用例覆盖；若实现者能在单测内驱动 stopped 分支则补一条。）

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./app/http/services/ -run TestApprovalDecisionJournaled -v
```

- [ ] **Step 3: 实现**

1. `rememberCompletedApprovalLocked`（3903）末尾加（j.mu 与 m.mu 不同锁、无互调，安全）：

```go
	// 审批审计行（spec §4.5）：决策落统一记录，两端回放可见。尽力而为。
	if m.coreJournal != nil {
		m.coreJournal.appendApprovalAudit(sessionID, approvalID, runSeq, response.Decision)
	}
```

journal 文件加：

```go
// appendApprovalAudit 落 kind=approval 审计行（决策/取消/超时统一入口）。
func (j *agentCoreJournal) appendApprovalAudit(sessionID, approvalID string, runSeq int, decision string) {
	if j == nil || j.dir == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.appendLineLocked(sessionID, map[string]interface{}{
		"kind":        "approval",
		"approval_id": approvalID,
		"run_seq":     runSeq,
		"decision":    decision,
		"ts":          time.Now().UTC().Format(time.RFC3339),
	})
}
```

（读侧 `readAgentCoreSessionMetaWithOptions` 对未知 kind 行为：现状是忽略——确认即可，无需改。）

2. 半截输出：`runAgentCore` 的 `ctx.Err() != nil` stopped 分支（~1225-1247）与 `result.IsError`/`runErr` error 分支（~1250-1263），在 `endStatus` 赋值旁加（三处同款；output 缓冲在锁 `outMu` 下，确认变量作用域可及——不可及则把 journal 半截逻辑挪到各 return 前的公共 defer，用已捕获的局部变量）：

```go
		// 半截输出留痕（spec §4.5）：尽力而为，僵尸回合不打（围栏同款）。
		if m.agentAIRunSideEffectsAllowed(run.sessionID, run.runSeq, run.guard) {
			outMu.Lock()
			partial := output.String()
			outMu.Unlock()
			if strings.TrimSpace(partial) != "" {
				journal.appendAssistant(run.sessionID, agentAssistantMessageID(run.messageID)+"-partial", partial, time.Now().UTC().Format(time.RFC3339))
			}
		}
```

（`-partial` 后缀防与成功行 messageID 撞去重键。）

3. resume_missing：grep 定位 claude/codex resume 自愈降级点（`agent_ai.go` ~4123-4144，符号含 resume-missing/自愈注释），降级生效处加：

```go
			_ = writeJSON(map[string]interface{}{
				"type": models.AgentEventAIRunNotice, "session_id": run.sessionID,
				"message_id": agentAssistantMessageID(run.messageID),
				"code":       "resume_missing",
				"detail":     fmt.Sprintf("native session %s not found; continuing as a fresh conversation", resumeSessionID),
			})
```

（`writeJSON` 取该函数在用的 emitter/runWrite 同名变量。）

- [ ] **Step 4: 跑测试确认通过 + 编译**

```bash
go test ./app/http/services/ -run TestApprovalDecisionJournaled -v
go build ./...
```

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai_agentcore.go app/http/services/agent_ai_agentcore_journal.go app/http/services/agent_ai.go app/http/services/agent_ai_journal_history_test.go
git commit -m "新增:审批审计行/失败回合半截输出落盘/resume_missing 显式提示

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 15: 全量回归、内核自动压缩 journal 行核验、收尾

**Files:**
- Modify: `app/http/services/agent_ai_agentcore.go`（若 OnCompact 宿主 hook 未落 journal 行则补——见 Step 2）
- Test: 全仓

- [ ] **Step 1: 内核自动压缩 journal 行核验**

grep 宿主侧 OnCompact 接线（`agentCoreLoopHooks` 内是否已有 OnCompact 字段消费；`agent_ai_agentcore.go:580-598` `replaceAgentCoreHistory` 的全部调用方）。**规则**：自动压缩（内核 maybeAutoCompact/noteContextOverflow 经 hook 到宿主）必须落 journal（user 行 `"/compact(auto)"` + assistant 行摘要 tokens 摘要）；手动 `/compact`（Task 12 已落）不得重复落。若 hook 路径与手动路径共用 `replaceAgentCoreHistory`，则把自动行落在 **hook 回调处**而非 replace 函数内。缺则补实现 + 一条单测（照 Task 12 测试风格）。

- [ ] **Step 2: 全仓构建 + 测试**

```bash
go build ./...
go vet ./app/http/services/ ./app/http/models/ ./app/agentcore/ 2>/dev/null || true
go test ./... 2>&1 | tail -40
```

预期：全绿或仅命中 memory 预存 flake 清单（子进程超时 3 用例偶败→单包复跑；-race 两枚基线；processor/auth 5/10）。**新改动相关包必须全绿**：

```bash
go test ./app/http/services/ ./app/http/models/ ./app/agentcore/ ./app/http/handlers/
```

- [ ] **Step 3: 收尾核验清单（对照 spec 阶段 1 验收门）**

- [ ] 四类新 journal 行：①斜杠命令 user+assistant（Task 12 测试）②内核 tool 行（Task 13）③失败回合半截 assistant（Task 14）④审批审计行（Task 14 测试）
- [ ] 头行幂等补写 + origin（Task 10 测试）
- [ ] 订阅/快照/审批广播/ack 双写（Task 4/6/7 测试）
- [ ] expires_at（Task 8）、会话级批准记忆（Task 9）
- [ ] journal 读回续聊（Task 11）
- [ ] 协议契约锁（Task 2）

- [ ] **Step 4: 提交收尾（若有 Step 1 补丁）**

```bash
git add -A
git commit -m "新增:内核自动压缩落 journal 行,阶段1收尾回归

Co-Authored-By: Claude Code <noreply@anthropic.com>"
git log --oneline -15
```

- [ ] **Step 5: 汇报**

向用户汇报：任务清单勾选状态、测试结果原文、部署 liang-dev 待办（标准部署管线，memory liang-dev-dual-unit-deploy-agent-startup-fundamentals：本地 tar→scp→换树→构建→stop→换装→enable→先 agent 后 core）、阶段 2（ChatPage 前端）出计划待办。
