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
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

// agentAISocketQueueCap 单 socket 事件队列深度。溢出=消费者死了/卡了，断开。
const agentAISocketQueueCap = 64

// agentAISocketSubCap 单 socket 订阅数上限；超限全清再加（与 completedApprovals
// 的 map 封顶同风格——防常驻 remote socket 的订阅集随会话结束无限累积）。
// 全清而非逐出最旧的取舍：订阅集没有时间戳，逐出策略需要额外记账；且订阅方
// （重连后的对端）拿快照后会重新订阅当前关注的会话，旧订阅本就是失联残留，
// 清掉不构成正确性问题。
const agentAISocketSubCap = 64

// agentAISnapshotTranscriptCap 订阅快照的转录条数上限：只保最近 N 条（journal
// 文件顺序即时间序）。与清单扫描的行数上限同数量级（agentVibeIndexMaxLines=240）。
const agentAISnapshotTranscriptCap = 200

// agentAIOriginSocketField 入站消息的内部来源 socket 标记。由两条分发路径
// （handleRemoteAgentMessage AI 分支盖 "remote"、HandleAIStream 盖本地 socket
// id）在分发前注入，message()/create()/runUserMessage 读取后落到
// session.originSocket。以 "__" 前缀命名，禁止原样转发到任何对端。
const agentAIOriginSocketField = "__origin_socket"

// agentAIRemoteSocketID 远端 WS 的常驻 socket id（写函数是间接层，重连自动换道）。
const agentAIRemoteSocketID = "remote"

// AgentAIOriginSocketField 供 handlers 包盖标记用（与 agentAIOriginSocketField
// 同值）。内部代码一律用小写 agentAIOriginSocketField，导出别名仅供 handlers
// 包盖标记。
const AgentAIOriginSocketField = agentAIOriginSocketField

type agentAISocket struct {
	id        string
	role      string // "remote" | "local"；与常驻 socket id agentAIRemoteSocketID 值有意同词，语义不同（角色分类 vs 注册表键）：role=="remote" 的 socket 恒被改写为单键 agentAIRemoteSocketID，勿互相替代。
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

// alive 报告 socket 是否仍活跃（closed 未触发）。唯一的关闭极性 helper：
// 历史上曾并存反极性的 isClosed，已删除（2026-10-28 评审）。
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
// 残留消息随 socket 对象一起被 GC。取出后再查一次 closed：closed 与 out
// 同时就绪时 select 随机取舍，避免 socket 已死后还排空残留（远端重注册
// 时防止新旧两台 pump 并发写同一间接层）。
func (s *agentAISocket) pump() {
	for {
		select {
		case <-s.closed:
			return
		case payload := <-s.out:
			if !s.alive() {
				return
			}
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
//
// 所有权契约：调用方负责在连接生命周期结束时 unregisterAISocket，否则
// pump goroutine 与队列在 socket 意外死亡（写失败/溢出）前持续存活。
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
	// onDead 只做「从注册表摘除」，且只摘「仍是自己」的条目——旧远端被驱逐
	// 后若残留一次迟到的溢出 onDead，不得误删同键的新 socket。**绝不**再走
	// sock.closeOnce.Do——调用点在 closeOnce.Do 闭包内，sync.Once 持锁期间
	// 同 goroutine 再入同一 Once 会自死锁（doSlow 在闭包返回后才落 done）。
	sock.onDead = func(id string) {
		m.mu.Lock()
		if cur := m.aiSockets[id]; cur == sock {
			delete(m.aiSockets, id)
		}
		m.mu.Unlock()
	}
	var evicted *agentAISocket
	if role == "remote" {
		// 远端是常驻单例，恒居 agentAIRemoteSocketID 单键之下：先摘旧（间接层
		// 写函数恒同一闭包，正常不会重复注册；防御测试/重入）。旧 socket 只
		// 记下、解锁后再关——closeOnce 内部互斥不得与 m.mu 嵌套：enqueue 溢出/
		// pump 写失败路径在 Once 闭包内取 m.mu（onDead），持 m.mu 进 Do 会
		// AB-BA 死锁（2026-10-28 spec 评审）。
		if old := m.aiSockets[agentAIRemoteSocketID]; old != nil {
			evicted = old
			delete(m.aiSockets, agentAIRemoteSocketID)
		}
		sock.id = agentAIRemoteSocketID
	}
	m.aiSockets[sock.id] = sock
	// origin 悬空修复（2026-11-09，11-04 生产实证）：本地 socket 断线即从注册
	// 表摘除，但会话的 originSocket 仍指向死 id——ack 双写与按 origin 的投递
	// 永远落空（同 optionResponse 作答刷新 TestOptionResponseRefreshesOriginSocket
	// 的缺口家族）。新本地 socket 注册时把悬空 origin 重指向自己；远端常驻键
	// 恒存（单例键不会悬空）与仍存活的 origin 均不扰动。
	if role == "local" {
		m.refreshStaleSessionOriginsLocked(sock.id)
	}
	m.mu.Unlock()
	if evicted != nil {
		evicted.closeOnce.Do(func() { close(evicted.closed) })
	}
	go sock.pump()
	return sock
}

// refreshStaleSessionOriginsLocked 把 originSocket 指向已不在注册表的会话
// 重指向新注册的本地 socket。调用方必须持有 m.mu（registerAISocket 内联调用）。
func (m *agentAIManager) refreshStaleSessionOriginsLocked(newSocketID string) {
	for _, s := range m.sessions {
		if s == nil || s.originSocket == "" || s.originSocket == agentAIRemoteSocketID || s.originSocket == newSocketID {
			continue
		}
		if m.aiSockets[s.originSocket] == nil {
			s.originSocket = newSocketID
		}
	}
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
		if len(s.subs) >= agentAISocketSubCap {
			cleared := len(s.subs)
			s.subs = make(map[string]struct{})
			// 观测点（评审 M5）：超限全清必须留痕——否则「我的 socket 突然
			// 收不到事件了」没有任何排查入口。Warn 在 m.mu+s.mu 内同步打，
			// logger 自持锁、与注册表锁无交集，无死锁环。
			logger.Warn(fmt.Sprintf("[AI-SOCKET] subscribe cap reached, socket=%s cleared=%d", socketID, cleared))
		}
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

// buildSessionSnapshotPayload 订阅握手回包载荷（spec §4.1）：转录快照（journal
// 读侧现成解析）+ 待审批 + 状态。尽力而为：转录取不到（非 journal 会话/读失
// 败）就只回 pending+status，不报错。纯读、不触任何 socket——调用方先 build
// 再订阅再入队（2026-11-09 审计序仲裁：journal 读取发生在订阅前，订阅后
// fanOut 才可能向本 socket 投递 delta，而快照载荷此后才入队，队列 FIFO 保证
// 订阅后的 delta 恒在快照之后）。
func (m *agentAIManager) buildSessionSnapshotPayload(sessionID string) map[string]interface{} {
	if sessionID == "" {
		return nil
	}
	payload := map[string]interface{}{
		"type":       models.AgentEventAISessionSnapshot,
		"session_id": sessionID,
		"status":     m.agentAISessionStatusForSnapshot(sessionID),
	}
	// 转录读 manager 自己的 journal（生产构造=agentCoreJournalDir() 同源；
	// 测试注入临时目录时快照跟写侧同源，绝不读真机 ~/.aliang）。
	dir := agentCoreJournalDir()
	if m.coreJournal != nil && m.coreJournal.dir != "" {
		dir = m.coreJournal.dir
	}
	// 展示面走带 end 系统条的口径（G5 问题悬空修复）：end(error) 行折成
	// role=system 的系统条进快照转录；模型上下文回装仍走纯对话口径
	// readAgentCoreJournalHistoryFrom，两口径在同一解析器上分叉（见
	// readAgentCoreJournalTranscriptFrom 注释）。
	if hist, _, err := readAgentCoreJournalTranscriptFrom(dir, sessionID, true); err == nil && len(hist) > 0 {
		// 传输层限幅（评审 I1）：单条文本按清单口径截断（agentVibeTranscript-
		// MaxContentRunes，与 agent_inventory_agentcore.go 的转录生成同一
		// helper/常量，rune 安全不断 UTF-8），总数只保最近
		// agentAISnapshotTranscriptCap 条——一条倾倒大文件的 assistant 消息
		// 原样上 WS 就是几十 MB。解析器保持全文：Task 11 内存续聊恢复复用
		// readAgentCoreJournalHistoryFrom，截断只发生在快照传输层。
		if len(hist) > agentAISnapshotTranscriptCap {
			hist = hist[len(hist)-agentAISnapshotTranscriptCap:]
		}
		tr := make([]map[string]interface{}, 0, len(hist))
		for _, msg := range hist {
			row := map[string]interface{}{
				"role": msg.Role,
				"text": truncateAgentText(msg.Content, agentVibeTranscriptMaxContentRunes),
			}
			if msg.MessageID != "" {
				row["message_id"] = msg.MessageID
			}
			if !msg.CreatedAt.IsZero() {
				row["ts"] = msg.CreatedAt.UTC().Format(time.RFC3339)
			}
			if len(msg.Attachments) > 0 {
				row["attachments"] = msg.Attachments
			}
			tr = append(tr, row)
		}
		payload["transcript"] = tr
	}
	// pending_approvals 恒在场（2026-11-09 快照权威清卡契约）：空集=权威
	// 「无在挂」——旧实现空时整字段缺席，前端 running 态无从区分「无在挂」与
	// 「旧版 agent」，只能只 upsert 不清卡，别端已作答的卡在桌面永 pending。
	// 前端按字段在场与否决定是否权威清卡（缺席=旧 agent，不越权）。
	payload["pending_approvals"] = m.pendingApprovalsSnapshotForSession(sessionID)
	return payload
}

// enqueueSessionSnapshot 把已构建载荷投进 socket pump 队列，与 delta 同通道
// FIFO 保序（2026-11-09 审计：旧实现在读 goroutine 上内联直写，可抢在已入队
// delta 之前到达——客户端先收全 done 快照再收迟到 delta，delta 被 done 屏障
// 吞掉）。socket 缺席（本地半订阅极端场景，评审 M4 / manager 未就绪 socketID=
// ""）回落直写，保持只读握手语义。
//
// 本修复覆盖的正是审计事故形态：刷新/重连后新连接是**纯订阅者**（旧 origin
// socket 已死），其 delta 全部走 fanOutFrom→enqueue，故快照入同一队列即保序。
// 残余两窗（现架构属性，均自愈，不构成回归）：
//  1. 构建→订阅之间落进 journal 的 delta 既不在快照里、也还没订阅到（微秒级，
//     回合末终态快照/详情补齐）；
//  2. 本连接即 origin 时 delta 走 fanOutWrite 的内联直写（不受队列序约束），
//     可与队列里的快照竞争——该形态前端本就有本地 running 气泡，无吞流后果。
func (m *agentAIManager) enqueueSessionSnapshot(socketID string, payload map[string]interface{}, writeJSON agentTerminalWriter) {
	if payload == nil {
		return
	}
	m.mu.Lock()
	s := m.aiSockets[socketID]
	m.mu.Unlock()
	if s != nil && s.alive() {
		s.enqueue(payload)
		return
	}
	// socket 缺席（本地半订阅极端场景，评审 M4）：只能直写，只读握手语义不变。
	if writeJSON != nil {
		_ = writeJSON(payload)
	}
}

// pendingApprovalsSnapshotForSession 单会话版待审批快照（字段口径与全量版
// pendingApprovalsSnapshot 一致，含 expires_at，spec §4.2.1）。
func (m *agentAIManager) pendingApprovalsSnapshotForSession(sessionID string) []map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]map[string]interface{}, 0, 4)
	for _, waiter := range m.approvals {
		if waiter == nil || waiter.sessionID != sessionID {
			continue
		}
		entry := map[string]interface{}{
			"session_id":  waiter.sessionID,
			"approval_id": waiter.request.ID,
			"kind":        waiter.request.Kind,
			"provider":    waiter.request.Provider,
		}
		if waiter.request.Command != "" {
			entry["command"] = waiter.request.Command
		}
		if waiter.request.ToolName != "" {
			entry["tool_name"] = waiter.request.ToolName
		}
		if !waiter.expiresAt.IsZero() {
			entry["expires_at"] = waiter.expiresAt.Format(time.RFC3339)
		}
		out = append(out, entry)
	}
	return out
}

// agentAISessionStatusForSnapshot 会话态：cancel 非空=running；查无=idle
// （订阅未知会话不报错——列表刷新场景友好，spec §6）。
func (m *agentAIManager) agentAISessionStatusForSnapshot(sessionID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[sessionID]; s != nil && s.cancel != nil {
		return "running"
	}
	return "idle"
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

// fanOutWrite 包一层 origin 写函数：origin 内联（保 emitter 的互斥序与终态
// 屏障语义），随后非阻塞扇出副本。审批族广播、其余仅订阅者。参数序与
// fanOutFrom 对齐（originSocketID 在前）。
// origin 写失败也照常扇出（错误原样返给调用方）——源连接死亡不该剥夺其他
// 端的事件；阻塞的 origin 写会推迟扇出。
// 契约：payload 在本函数返回后视为冻结——同一 map 实例会异步投递给 N 个
// pump，调用方（emitter.emit）此后不得原地改写。
func (m *agentAIManager) fanOutWrite(originSocketID, sessionID string, originWrite agentTerminalWriter) agentTerminalWriter {
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

// RegisterLocalAIChatSocket 注册一条本地聊天 WS 出站 socket，返回其 id
// （空串=manager 未就绪，调用方跳过注册与标记）。
func (s *AgentService) RegisterLocalAIChatSocket(write agentTerminalWriter) string {
	if s.ai == nil || write == nil {
		return ""
	}
	if sock := s.ai.registerAISocket("local", write); sock != nil {
		return sock.id
	}
	return ""
}

// UnregisterLocalAIChatSocket 注销本地聊天 socket（幂等；空串或 manager 未
// 就绪时 no-op）。
func (s *AgentService) UnregisterLocalAIChatSocket(id string) {
	if id == "" || s.ai == nil {
		return
	}
	s.ai.unregisterAISocket(id)
}
