package services

// remote-assist P1:会话状态机权威 + 云端信令接线(Task 4)。状态迁移 clock
// 注入可测;终端接线由 onActivated/onEnded 钩子挂接(Task 5)。
// 威胁模型与授权边界见 spec docs/superpowers/specs/2026-11-26-remote-assist-design.md §10。

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
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

type RemoteAssistSnapshot struct {
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
	mu sync.Mutex

	now func() time.Time
	// clockAdvance 仅测试注入:可变时钟推进器(配合 AdvanceClock 驱动时间
	// 跳变);生产构造恒为 nil,AdvanceClock 退化为 no-op 推进。
	clockAdvance func(time.Duration)

	session       *remoteAssistSession
	endReason     assistEndReason // 终态粘性:槽位释放后保留,Snapshot 据此汇报 ended
	endedAt       time.Time
	lastSessionID string

	lastEnded time.Time // 冷却锚点(仅非过期终态落点,见 finishLocked)

	// onActivated/onEnded 状态迁移后的外部联动钩子(Task 5 终端腿接线:
	// approved 即建协助 PTY、终态即关闭)。nil=无联动。锁纪律:钩子一律
	// 锁外投递(见 runPendingHooks)——钩子会取终端管理器等其他锁,禁止与
	// m.mu 嵌套(mac-agent 1.1.120 s.mu 锁内二次加锁自死锁同型事故)。
	onActivated func(sessionID string)
	onEnded     func(sessionID string, reason assistEndReason)
	// hooksPending 锁内固化的待投递钩子闭包(钩子引用与参数在迁移当下取值,
	// 避免锁外重读字段的数据竞争),解锁后 runPendingHooks 统一投递。
	hooksPending []func()

	// views 是观看端扇出注册表(Task 5,设备本地;Task 7 观看 WS 注册进来):
	// 会话 id → viewID(连接身份) → 扇出函数。与 m.mu 分锁——扇出在 PTY 输出
	// 复制循环上调用,不得与状态迁移锁互相牵制。
	viewsMu sync.Mutex
	views   map[string]map[string]assistViewFan
}

func newRemoteAssistManager(now func() time.Time) *remoteAssistManager {
	if now == nil {
		now = time.Now
	}
	return &remoteAssistManager{now: now}
}

// OnActivated 设置批准激活钩子(approved 迁移提交后锁外调用,sessionID 为
// 已激活会话)。重复设置覆盖前者。Task 5 接线协助终端创建。
func (m *remoteAssistManager) OnActivated(fn func(sessionID string)) {
	m.mu.Lock()
	m.onActivated = fn
	m.mu.Unlock()
}

// OnEnded 设置终态钩子(任意路径收敛 ended 后锁外调用,含云端 end 帧、
// 本地撤销/禁用、pending 过期与时长上限懒扫)。Task 5 接线协助终端关闭
// 与观看页收摊。
func (m *remoteAssistManager) OnEnded(fn func(sessionID string, reason assistEndReason)) {
	m.mu.Lock()
	m.onEnded = fn
	m.mu.Unlock()
}

// enqueueHookLocked 前提:持锁。钩子引用与参数在此刻固化。
func (m *remoteAssistManager) enqueueHookLocked(fn func()) {
	if fn != nil {
		m.hooksPending = append(m.hooksPending, fn)
	}
}

// runPendingHooks 锁外调用:投递并清空队列。绝不持锁执行——钩子可能再入
// manager(如 Task 5 终端关闭回调里调 Snapshot),sync.Mutex 不可重入,锁内
// 执行即自死锁。
func (m *remoteAssistManager) runPendingHooks() {
	m.mu.Lock()
	pending := m.hooksPending
	m.hooksPending = nil
	m.mu.Unlock()
	for _, fn := range pending {
		fn()
	}
}

// 四哨兵导出(Task 6):HTTP 面的 handler 层要 errors.Is 做错误→HTTP 语义
// 映射(ErrAgentFS 家族同款先例);errAssistNotActive/errAssistIDMismatch 仅
// 服务内部(WS 分派/终端腿)使用,保持 unexported。
var (
	ErrAssistCooldown   = errors.New("assist: request cooldown")
	ErrAssistBusy       = errors.New("assist: session already active or pending")
	ErrAssistNotPending = errors.New("assist: no pending session")
	// errAssistNotActive 供 Task 4/5 网络接线使用(信令仅在 active 态合法)。
	errAssistNotActive    = errors.New("assist: no active session")
	errAssistIDMismatch   = errors.New("assist: session id mismatch")
	ErrAssistNothingToEnd = errors.New("assist: nothing to end")
)

// ---------------------------------------------------------------------------
// assist 信令上行 writer 缝(Task 4,agentPunchFrameSink 同族范式)
// ---------------------------------------------------------------------------

// assistFrameSink 是 assist 信令帧的注入出口(集成测试与诊断面缝,仅测试
// 装置置位,生产路径恒 nil):*AndSignal 的自发帧、OnRemoteFrame 的回帧,以
// 及协助 PTY 输出复合 writer 的上行腿(任务 5 起,terminal.output/exit/
// error 同走此缝)均经 assistFrameWriter 投出。刻意只服务 assist 帧族——
// 不新增给 assist 之外路径用的全局可变缝(计划铁律)。
var assistFrameSinkMu sync.RWMutex
var assistFrameSinkFn func(map[string]interface{}) error

func assistFrameSinkLoad() func(map[string]interface{}) error {
	assistFrameSinkMu.RLock()
	defer assistFrameSinkMu.RUnlock()
	return assistFrameSinkFn
}

// AssistSetFrameSink 注入 assist 信令帧出口:sink 非 nil 时 assist 帧一律经
// sink 投出(调用方自行收集/路由),nil 恢复生产默认(currentRemoteWriterFunc)。
// 生产路径恒不置位,零影响。进程级全局:测试用完必须恢复 nil(见
// newTestAssistManagerWithSink 的 Cleanup 纪律),否则污染同包其他测试。并发安全。
func AssistSetFrameSink(sink func(map[string]interface{}) error) {
	assistFrameSinkMu.Lock()
	assistFrameSinkFn = sink
	assistFrameSinkMu.Unlock()
}

// SetFrameSink 管理器侧的缝注入糖(测试按计划行文经 manager 注入;缝本体
// 是进程级全局,见 AssistSetFrameSink)。
func (m *remoteAssistManager) SetFrameSink(sink func(map[string]interface{}) error) {
	AssistSetFrameSink(sink)
}

// assistFrameWriter assist 信令上行 writer(agentPunchFrameWriter 同构):
// 测试缝 sink 优先,生产回落 currentRemoteWriterFunc(断连 nil 即失败——
// 信令发不出去就报错,不该静默吞掉让用户等一个永远不会被审批的会话)。
func assistFrameWriter() func(interface{}) error {
	return func(v interface{}) error {
		if payload, ok := v.(map[string]interface{}); ok {
			if sink := assistFrameSinkLoad(); sink != nil {
				return sink(payload)
			}
		}
		w := currentRemoteWriterFunc()
		if w == nil {
			return errors.New("agent assist: remote writer offline")
		}
		return w(v)
	}
}

// assistFrame 信令帧骨架:载荷保持最小(session_id 恒带;设备身份字段由
// 平台侧从连接取,设备不自带,见计划附录 A 帧契约)。
func assistFrame(typ string, sessionID string) map[string]interface{} {
	return map[string]interface{}{"type": typ, "session_id": sessionID}
}

func (m *remoteAssistManager) Request(message string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sid, _, err := m.requestLocked(message)
	return sid, err
}

// requestLocked 前提:持锁。created=本次新建会话(区别于 requested 态的
// 同态留言更新)——RequestAndSignal 据此决定信令失败时是否回滚。
func (m *remoteAssistManager) requestLocked(message string) (string, bool, error) {
	now := m.now()
	// 冷却窗口从「回到 idle」起算,只防终态后的连发骚扰(spec §10 限频)。
	// pending 过期豁免锚点(见 finishLocked)——过期已含 5 分钟自然等待,
	// 立即重申是合法路径。
	if m.session == nil && !m.lastEnded.IsZero() && now.Sub(m.lastEnded) < assistRequestCooldown {
		return "", false, ErrAssistCooldown
	}
	if m.session != nil {
		switch m.stateLocked() {
		case assistStateRequested:
			m.session.message = strings.TrimSpace(message) // 同态更新留言
			return m.session.id, false, nil
		case assistStateActive:
			return "", false, ErrAssistBusy
		}
	}
	id := fmt.Sprintf("%s%d", assistIDPrefix, now.UnixNano())
	m.session = &remoteAssistSession{id: id, message: strings.TrimSpace(message), requestedAt: now}
	m.endReason = ""
	return id, true, nil
}

// RequestAndSignal 申请协助 + 上行 assist.request。信令发不出去(离线/断连)
// 时直接失败让调用方呈现,不静默——发不出去的申请永远不会进 admin 队列。
// 本次新建的会话随即回滚到纯净 idle(不置粘性 ended、不设 60s 冷却锚,基础
// 设施抖动不该让用户吃连发冷却,spec §10 冷却针对的是「终态已触达 admin
// 队列」的骚扰面);同态留言更新路径信令失败不回滚(原会话早已在队列里,
// 保留旧留言继续等审批)。
func (m *remoteAssistManager) RequestAndSignal(message string) (string, error) {
	m.mu.Lock()
	sid, created, err := m.requestLocked(message)
	m.mu.Unlock()
	if err != nil {
		return "", err
	}
	frame := assistFrame(models.AgentEventAssistRequestUp, sid)
	frame["message"] = strings.TrimSpace(message)
	if err := assistFrameWriter()(frame); err != nil {
		if created {
			m.rollbackCreated(sid)
		}
		return "", err
	}
	return sid, nil
}

// rollbackCreated 信令未送达的新建会话回滚。仅当状态仍是 requested 且
// session id 未变才回滚(解锁到写失败的窗口里云端帧可能已改状态,那种情况
// 以现状为准不回滚,白盒守卫见 TestAssistRollbackGuardKeepsApprovedSession)。
//
// 并发口径(评审 Fix 3 勘误):「唯一 HTTP 端点=单调用方」的前提不成立——
// Go http server 并发处理请求,两个 POST /assist/request 即双调用方(A 新建+
// 信令失败 / B 同态更新+信令成功),A 的回滚可抹掉 B 已上行的会话:窗口窄
// (A 的解锁→写失败 vs B 的同态更新,同为信令抖动期的连点),云端按
// pending_expired 自愈、本地无脏态,但体验断档(用户刚发的申请从 admin
// 队列消失)。P1 接受此窄窗不加 signaled-since-creation 标记(YAGNI:标记
// 要在 requestLocked/rollbackCreated 双点维护创建代次,收益仅覆盖连点抖动
// 场景);出现实际投诉再补,补法即该标记。
func (m *remoteAssistManager) rollbackCreated(sid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session != nil && m.session.approvedAt.IsZero() && m.session.id == sid {
		m.session = nil
		m.endReason = ""
	}
}

func (m *remoteAssistManager) Cancel() error {
	m.mu.Lock()
	err := m.cancelLocked()
	m.mu.Unlock()
	m.runPendingHooks()
	return err
}

// cancelLocked 前提:持锁。
func (m *remoteAssistManager) cancelLocked() error {
	if m.stateLocked() != assistStateRequested {
		return ErrAssistNotPending
	}
	m.finishLocked(AssistEndClient)
	return nil
}

// CancelAndSignal 撤销申请 + 上行 assist.cancel(附录 A #4:客户端发起的
// 收口恒 cancel 帧型,云端无需动作仅同步房间)。终态先本地落定,信令失败
// 不回滚(撤销意图已达成,云侧 room 定时器与 device_offline 语义自愈),
// 错误照实上抛由 HTTP 调用方(Task 6)决定是否呈现。
func (m *remoteAssistManager) CancelAndSignal() error {
	m.mu.Lock()
	sid := ""
	if m.session != nil {
		sid = m.session.id
	}
	err := m.cancelLocked()
	m.mu.Unlock()
	m.runPendingHooks()
	if err != nil {
		return err
	}
	return assistFrameWriter()(assistFrame(models.AgentEventAssistCancel, sid))
}

func (m *remoteAssistManager) CloudApproved(sessionID string) error {
	m.mu.Lock()
	if m.stateLocked() != assistStateRequested {
		m.mu.Unlock()
		return ErrAssistNotPending
	}
	if m.session.id != sessionID {
		m.mu.Unlock()
		return errAssistIDMismatch
	}
	m.session.approvedAt = m.now()
	if fn := m.onActivated; fn != nil {
		sid := m.session.id
		m.enqueueHookLocked(func() { fn(sid) })
	}
	m.mu.Unlock()
	m.runPendingHooks()
	return nil
}

func (m *remoteAssistManager) CloudRejected(sessionID string) error {
	m.mu.Lock()
	if m.stateLocked() != assistStateRequested || m.session.id != sessionID {
		m.mu.Unlock()
		return ErrAssistNotPending
	}
	m.finishLocked(AssistEndAdmin) // 拒绝按 admin 侧终态计,审计同源
	m.mu.Unlock()
	m.runPendingHooks()
	return nil
}

func (m *remoteAssistManager) End(reason assistEndReason) error {
	m.mu.Lock()
	err := m.endLocked(reason)
	m.mu.Unlock()
	m.runPendingHooks()
	return err
}

// endLocked 前提:持锁。
func (m *remoteAssistManager) endLocked(reason assistEndReason) error {
	if m.stateLocked() == assistStateIdle {
		return ErrAssistNothingToEnd
	}
	m.finishLocked(reason)
	return nil
}

// EndAndSignal 本地终态收口 + 上行信令。上行帧型按附录 A #4 分流:客户端
// 发起的收口(client_end,撤销申请/结束会话/总开关关闸)恒 assist.cancel;
// 设备侧原因(device_restart 等)走 assist.end 带 reason(帧族注释:该帧
// 双向)。信令失败不回滚终态(本地已终态,云侧自愈语义见 CancelAndSignal),
// 错误照实上抛。idle 时返回 ErrAssistNothingToEnd 且不上行——禁用路径的
// 常态幂等分支。
func (m *remoteAssistManager) EndAndSignal(reason assistEndReason) (string, error) {
	m.mu.Lock()
	sid := ""
	if m.session != nil {
		sid = m.session.id
	}
	err := m.endLocked(reason)
	m.mu.Unlock()
	m.runPendingHooks()
	if err != nil {
		return "", err
	}
	var frame map[string]interface{}
	if reason == AssistEndClient {
		frame = assistFrame(models.AgentEventAssistCancel, sid)
	} else {
		frame = assistFrame(models.AgentEventAssistEnd, sid)
		frame["reason"] = string(reason)
	}
	return sid, assistFrameWriter()(frame)
}

func (m *remoteAssistManager) CloudEnd(sessionID string, reason assistEndReason) error {
	m.mu.Lock()
	if m.stateLocked() == assistStateIdle || (m.session != nil && m.session.id != sessionID) {
		m.mu.Unlock()
		return errAssistIDMismatch
	}
	m.finishLocked(reason)
	m.mu.Unlock()
	m.runPendingHooks()
	return nil
}

// Sweep 收敛时间驱动的终态:pending 5 分钟未审批过期,active 超 2 小时上限。
// 由 Snapshot 懒扫驱动(网络层亦可周期调用,Task 4 定)。懒扫落终态同样触发
// 终态钩子(Task 5 的时长上限关终端依赖它),故 Snapshot/Sweep/AdvanceClock
// 三个收敛入口解锁后统一 runPendingHooks。
func (m *remoteAssistManager) Sweep() {
	m.mu.Lock()
	m.sweepLocked()
	m.mu.Unlock()
	m.runPendingHooks()
}

// sweepLocked 前提:持锁,且 requested/active 分支必有活跃会话(stateLocked
// 仅在 session != nil 时返回这两态)。
func (m *remoteAssistManager) sweepLocked() {
	now := m.now()
	switch m.stateLocked() {
	case assistStateRequested:
		if now.Sub(m.session.requestedAt) >= assistPendingExpiry {
			m.finishLocked(AssistEndExpired)
		}
	case assistStateActive:
		if now.Sub(m.session.approvedAt) >= assistDurationCap {
			m.finishLocked(AssistEndDurationCap)
		}
	}
}

// stateLocked 空会话=idle;会话存在则按终态标记区分。
func (m *remoteAssistManager) stateLocked() assistState {
	if m.session == nil {
		return assistStateIdle
	}
	// 防御分支:现行不变量「session != nil ⇒ endReason == ""」(终态时
	// finishLocked 恒置 session=nil)下走不到这里,但留下它使本函数对
	// future 字段演化稳健,勿删。
	if m.endReason != "" {
		return assistStateEnded
	}
	if m.session.approvedAt.IsZero() {
		return assistStateRequested
	}
	return assistStateActive
}

// finishLocked 终态即释放槽位是刻意决策:观看页/审计需要的历史由各自面
// 持有,状态机保持最小态。endReason/endedAt/lastSessionID 留在 manager 上,
// Snapshot 据此继续汇报 ended 直到下一次 Request 重置。
//
// 冷却锚点仅对非过期终态落点:pending 过期本就含 5 分钟无人应答的自然等
// 待,过期后须可立即重申(锚点会让同刻重申被 60s 冷却误拦);其余终态
// (撤销/拒绝/主动结束)都可能已触达 admin 队列,须防连发骚扰(spec §10)。
func (m *remoteAssistManager) finishLocked(reason assistEndReason) {
	if m.session == nil {
		return
	}
	sid := m.session.id
	now := m.now()
	m.endReason = reason
	m.endedAt = now
	m.lastSessionID = sid
	if reason != AssistEndExpired {
		m.lastEnded = now
	}
	m.session = nil
	// 终态钩子锁外投递(锁纪律见 onEnded 字段注释):钩子引用与参数在此刻
	// 固化,runPendingHooks 解锁后统一执行——Task 5 的关闭终端联动绝不持
	// m.mu 跑。
	if fn := m.onEnded; fn != nil {
		m.enqueueHookLocked(func() { fn(sid, reason) })
	}
}

// Snapshot 当前状态快照。读路径懒扫一次,过期/时长上限在读取时即收敛,
// 无需独立定时器。终态后会话槽位已释放,按粘性终态字段回填 SessionID。
// 懒扫可能落终态,钩子在解锁后投递(锁纪律见 runPendingHooks)。
func (m *remoteAssistManager) Snapshot() RemoteAssistSnapshot {
	m.mu.Lock()
	m.sweepLocked()
	var snap RemoteAssistSnapshot
	if m.session != nil {
		snap = RemoteAssistSnapshot{
			SessionID:   m.session.id,
			State:       m.stateLocked(),
			Message:     m.session.message,
			RequestedAt: m.session.requestedAt.Format(time.RFC3339),
		}
		if !m.session.approvedAt.IsZero() {
			snap.ApprovedAt = m.session.approvedAt.Format(time.RFC3339)
		}
	} else if m.endReason != "" {
		snap = RemoteAssistSnapshot{
			SessionID: m.lastSessionID,
			State:     assistStateEnded,
			EndReason: m.endReason,
			EndedAt:   m.endedAt.Format(time.RFC3339),
		}
	}
	m.mu.Unlock()
	m.runPendingHooks()
	return snap // endReason=="" 时 snap 为零值=idle
}

// AdvanceClock 推进注入时钟并立即收敛(仅测试用:生产构造 clockAdvance 为
// nil,真实时钟不可推进,时间驱动终态由 Snapshot 懒扫/周期 Sweep 覆盖)。
// 推进+收敛在同一临界区内完成,共享时钟的写与懒扫读不构成竞争。
func (m *remoteAssistManager) AdvanceClock(d time.Duration) {
	m.mu.Lock()
	if m.clockAdvance != nil {
		m.clockAdvance(d)
	}
	m.sweepLocked()
	m.mu.Unlock()
	m.runPendingHooks()
}

// ---------------------------------------------------------------------------
// 云端下行帧分派(Task 4):assist.approved / rejected / end / state
// ---------------------------------------------------------------------------

// OnRemoteFrame 处理云端下行 assist 帧(handleRemoteAgentMessage 的 assist
// case 直调;诊断/集成测试亦可直投),驱动状态机并返回需要写回云端的帧
// ——分派路径刻意把 writer 知识留在调用方(经 writeJSON 上行),manager
// 只做纯分派,与 *AndSignal 家族(自发信令,内部走 assistFrameWriter)分工。
//
// 帧语义(计划附录 A):
//   - assist.approved{session_id}:requested→active,触发 onActivated 钩子
//     (锁外投递,Task 5 终端腿)。无回帧。
//   - assist.rejected{session_id}:requested→ended(admin_end)。无回帧。
//   - assist.end{session_id,reason}:→ended(reason);reason 为必填契约字段,
//     缺失即拒帧。无回帧(设备侧状态已终态,云端不需要设备确认)。
//   - assist.state:P1 仅记录日志(房间状态镜像留给 P2+),不改状态、无回帧。
//
// 幂等与守卫:
//   - session_id 不匹配/状态不符返回错误且不改状态(manager 守卫本就如此,
//     防串会话);调用方(handleRemoteAgentMessage)将错误转成 error 帧
//     上抛,读循环不受影响。
//   - 已终会话的重复 end 帧(云端重发/时序竞争)幂等吞掉:返回 nil 不报错
//     ——读循环不得因云端补帧错误风暴。判定按粘性终态字段:state==ended
//     且(空闲槽位或 id 匹配刚终会话)即视为重复投递。
func (m *remoteAssistManager) OnRemoteFrame(msg map[string]interface{}) ([]map[string]interface{}, error) {
	switch remoteString(msg, "type") {
	case models.AgentEventAssistApproved:
		if err := m.CloudApproved(remoteString(msg, "session_id")); err != nil {
			return nil, err
		}
		return nil, nil
	case models.AgentEventAssistRejected:
		if err := m.CloudRejected(remoteString(msg, "session_id")); err != nil {
			return nil, err
		}
		return nil, nil
	case models.AgentEventAssistEnd:
		sid := remoteString(msg, "session_id")
		reason := remoteString(msg, "reason")
		if reason == "" {
			// reason 是契约必填字段(附录 A #3/#4):缺失帧拒收,不得静默
			// 替换成某个默认原因——审计终态必须真实。
			return nil, fmt.Errorf("assist: end frame missing reason (session=%s)", sid)
		}
		dup, err := m.cloudEndOnce(sid, assistEndReason(reason))
		if err != nil {
			return nil, err
		}
		if dup {
			logger.Info(fmt.Sprintf("[ASSIST] cloud end frame for already-ended session session_id=%s reason=%s (duplicate, swallowed)", sid, reason))
		}
		return nil, nil
	case models.AgentEventAssistState:
		// P1 仅记录日志(计划附录 A #5:房间状态镜像留给 P2+);帧到达证明
		// 房间侧仍在关注本会话,落一条观测日志即可,不改状态、不回帧。
		logger.Info(fmt.Sprintf("[ASSIST] cloud state frame session_id=%s state=%s",
			remoteString(msg, "session_id"), remoteString(msg, "state")))
		return nil, nil
	default:
		return nil, fmt.Errorf("assist: unsupported frame type %q", remoteString(msg, "type"))
	}
}

// cloudEndOnce 幂等收敛:重复投递判定与终态迁移同一临界区,消除先查后改
// (isDuplicateEnd → CloudEnd 两段锁)的竞态窗口——窗口内终态恰落定会让
// CloudEnd 的 idle 守卫报 id mismatch,分派臂误发一帧 agent.error 给合法
// 的重复投递。守卫语义与 CloudEnd 逐字一致(idle/错 id → errAssistIDMismatch,
// 状态不变),Task 2 的 CloudEnd 守卫面不受影响。
func (m *remoteAssistManager) cloudEndOnce(sid string, reason assistEndReason) (dup bool, err error) {
	m.mu.Lock()
	// 重复投递:粘性终态存在,且当前无活跃会话(idle)或活跃会话 id 与粘性
	// 终态 id 一致;id 不一致的活跃会话不是重复帧,走下方守卫拒绝。
	if m.endReason != "" && (m.session == nil || m.session.id == sid) {
		m.mu.Unlock()
		return true, nil
	}
	if m.stateLocked() == assistStateIdle || (m.session != nil && m.session.id != sid) {
		m.mu.Unlock()
		return false, errAssistIDMismatch
	}
	m.finishLocked(reason)
	m.mu.Unlock()
	m.runPendingHooks()
	return false, nil
}

// ---------------------------------------------------------------------------
// 协助终端腿(Task 5):approved 激活 PTY、终态回收、观看端扇出注册表。
//
// ID 方案(刻意):终端 session_id == 状态机会话 id(两者同为 assist-<nanos>,
// 一个 id 两个子系统)。前缀即路由键——remote-WS 终端帧分派点据此把 assist-
// 前缀帧导给协助腿门控(见 handleAssistTerminalFrame),云端/审计全链同 id
// 可对账,跨子系统再映射 id 的复杂度不值得。
//
// 保活语义铁律(spec §14 坑 2):协助终端经 createWithSink 落进 m.sessions
// 与手机腿会话同池同族——断线走 markAllDetached 保留、30m idle 才回收;
// connSink 恒 nil 故永不进网页腿 orphanTimers 延迟回收链(那链只由
// ScheduleWebOrphanClose 登记,网页腿专属)。
// ---------------------------------------------------------------------------

// isActiveSession 报告 sid 是否当前 active 会话(id 匹配+active 态,单锁内
// 取值)。协助终端帧门控与激活钩子再验共用;刻意不内嵌懒扫——门控是读路径,
// 时长上限的收敛由 Snapshot 懒扫/周期 Sweep 驱动,边界秒级的残留输入无实害。
func (m *remoteAssistManager) isActiveSession(sid string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.session != nil && m.session.id == sid && m.stateLocked() == assistStateActive
}

// assistViewFan 观看端扇出出口:一条观看连接的写出口。viewID=连接身份
// (观看端重连=旧 viewID 注销+新 viewID 注册,由 Task 7 观看 WS 调用)。
type assistViewFan func(map[string]interface{}) error

// RegisterViewSink 注册观看端扇出出口(会话→viewID→扇出)。同一会话允许多
// 观看连接(spec §13-3 默认);幂等覆盖同 viewID。
//
// Task 7 观看 WS 调用纪律:注册前必须先确认会话仍 active(isActiveSession)
// ——收摊路径 notifyAndClearViews 是「先删桶再调 sink」,结束后才注册的观看
// 端会得到一个永不被通知的孤儿桶(收不到 assist.end);连接退出必须
// UnregisterViewSink 防桶泄漏。
func (m *remoteAssistManager) RegisterViewSink(sid, viewID string, fn assistViewFan) {
	m.viewsMu.Lock()
	if m.views == nil {
		m.views = make(map[string]map[string]assistViewFan)
	}
	if m.views[sid] == nil {
		m.views[sid] = make(map[string]assistViewFan)
	}
	m.views[sid][viewID] = fn
	m.viewsMu.Unlock()
}

// UnregisterViewSink 注销观看端(连接退出/会话收摊)。会话最后一个 sink 注销
// 后连会话桶一起清,防键累积。
func (m *remoteAssistManager) UnregisterViewSink(sid, viewID string) {
	m.viewsMu.Lock()
	if sinks := m.views[sid]; sinks != nil {
		delete(sinks, viewID)
		if len(sinks) == 0 {
			delete(m.views, sid)
		}
	}
	m.viewsMu.Unlock()
}

// fanOutToViews 把一帧扇给该会话全部在册观看端。单路失败仅记日志不阻断他路
// (复制循环的帧照常出),扇出快照锁内取、锁外执行——PTY 输出路径不与
// viewsMu 长持。每个 sink 独立 recover:fan 是外部代码,跑在 PTY 复制循环
// goroutine(裸 go,panic 即杀进程)与钩子 drain 路径上,safeGo 同款纪律——
// 单 sink 爆炸按失败计,循环继续。
//
// Task 7 起扇出错误语义升级:fan 报错(观看连接死/写超时)即自动注销该
// viewID——P1 策略「任何 fan 错误→注销」,观看端重连=旧注销+新注册,注册
// 表不积累死 sink(错误与 panic 都按失败计,同走注销)。
func (m *remoteAssistManager) fanOutToViews(sid string, frame map[string]interface{}) {
	m.viewsMu.Lock()
	type viewSink struct {
		viewID string
		fn     assistViewFan
	}
	sinks := make([]viewSink, 0, len(m.views[sid]))
	for id, fn := range m.views[sid] {
		sinks = append(sinks, viewSink{viewID: id, fn: fn})
	}
	m.viewsMu.Unlock()
	for _, s := range sinks {
		if m.fanOneView(sid, s.viewID, s.fn, frame, "view fan-out failed") != nil {
			m.UnregisterViewSink(sid, s.viewID)
		}
	}
}

// notifyAndClearViews 收摊:先给全部在册观看端发终帧(assist.end{reason})再
// 清空该会话注册表。观看端收到终帧后由服务端关闭连接(Task 7)。返回被通知
// 的 sink 数(观测用)。recover 纪律同 fanOutToViews。
func (m *remoteAssistManager) notifyAndClearViews(sid string, frame map[string]interface{}) int {
	m.viewsMu.Lock()
	type viewSink struct {
		viewID string
		fn     assistViewFan
	}
	sinks := make([]viewSink, 0, len(m.views[sid]))
	for id, fn := range m.views[sid] {
		sinks = append(sinks, viewSink{viewID: id, fn: fn})
	}
	delete(m.views, sid)
	m.viewsMu.Unlock()
	notified := 0
	for _, s := range sinks {
		if m.fanOneView(sid, s.viewID, s.fn, frame, "view end notify failed") == nil {
			notified++
		}
	}
	return notified
}

// fanOneView 投递单路观看端:错误与 panic 都按失败计(仅日志,不阻断他路;
// panic 不 recover 即顺 Go 语义杀全进程,带走所有活 PTY)。
func (m *remoteAssistManager) fanOneView(sid, viewID string, fn assistViewFan, frame map[string]interface{}, failLog string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			logger.Warn(fmt.Sprintf("[ASSIST] %s session_id=%s view=%s panic=%v", failLog, sid, viewID, r))
			err = fmt.Errorf("view sink panicked: %v", r)
		}
	}()
	if err = fn(frame); err != nil {
		logger.Warn(fmt.Sprintf("[ASSIST] %s session_id=%s view=%s error=%v", failLog, sid, viewID, err))
	}
	return err
}

// assistEndFrame 观看端终帧(assist.end 双向帧族的设备侧用途)。
func assistEndFrame(sid string, reason assistEndReason) map[string]interface{} {
	return map[string]interface{}{
		"type":       models.AgentEventAssistEnd,
		"session_id": sid,
		"reason":     string(reason),
	}
}

// AssistRegisterViewSink / AssistUnregisterViewSink 观看 WS 的注册面
// (Task 7 handler 腿;remoteAssistManager 未导出,同 AssistTerminalReplayTo
// 的导出小壳先例)。注册纪律见 RegisterViewSink 文档:注册前必须确认会话
// 在 requested/active(结束后注册=永不被通知的孤儿桶);连接退出必须注销。
func (s *AgentService) AssistRegisterViewSink(sessionID, viewID string, fn func(map[string]interface{}) error) {
	if s == nil || s.assist == nil {
		return
	}
	s.assist.RegisterViewSink(sessionID, viewID, fn)
}

func (s *AgentService) AssistUnregisterViewSink(sessionID, viewID string) {
	if s == nil || s.assist == nil {
		return
	}
	s.assist.UnregisterViewSink(sessionID, viewID)
}

// AssistViewBucketEmpty 会话观看桶是否已空(测试观察缝:钉 handler 腿
// 「连接退出必须注销」的回归;生产零调用)。
func (s *AgentService) AssistViewBucketEmpty(sessionID string) bool {
	if s == nil || s.assist == nil {
		return true
	}
	s.assist.viewsMu.Lock()
	defer s.assist.viewsMu.Unlock()
	return len(s.assist.views[sessionID]) == 0
}

// AssistViewEndFrame 观看端终帧载荷(handler 在 register-vs-end 复检兜底
// 分支补发;assistEndFrame 未导出,同族导出小壳)。
func AssistViewEndFrame(sessionID string, reason string) map[string]interface{} {
	return assistEndFrame(sessionID, assistEndReason(reason))
}

// ——— 服务侧接线:approved 激活/终态回收/复合 writer ———

// AssistTerminalReplayTo 把协助会话的 ring 回放流发给 writeJSON(Task 7
// 观看 WS 的续屏腿。计划允许的小偏差:sendReplay/attachHistory 挂在
// agentTerminalManager 上且未导出,handlers 包不可达,按 SetDirForTest
// 先例在 services 落一个导出小壳)。语义:
//   - active 态(终端在册):live ring 回放,与 attachLive 同款 outputGate
//     持锁纪律(回放帧与活输出互斥,zero seam);
//   - 会话已退(tombstone):按 exited 形态回放;
//   - 无会话(尚未建终端的 requested 态/未知名):no-op 优雅跳过——
//     等待模式无 ring 可回,连接静默进入 live 等待。
// attachToken 恒空串:观看端不是 attach 语义的请求方(无 token 路由),
// 帧保持 legacy 字节形态。
func (s *AgentService) AssistTerminalReplayTo(sessionID string, writeJSON agentTerminalWriter) {
	if s == nil || s.terminal == nil || writeJSON == nil {
		return
	}
	m := s.terminal
	m.mu.Lock()
	session := m.sessions[sessionID]
	if session != nil {
		// 引用先取、锁先释(单向锁序 m.mu → outputGate),gate 横跨快照与
		// 每帧发送——回放帧绝不与活 output 交错(sendReplay 同款纪律)。
		m.mu.Unlock()
		m.sendReplay(sessionID, session.ring, terminalReplayStatusLive, &session.outputGate, "", writeJSON)
		return
	}
	tomb := m.history[sessionID]
	m.mu.Unlock()
	if tomb != nil {
		m.sendReplay(sessionID, tomb.ring, terminalReplayStatusExited, nil, "", writeJSON)
	}
}

// assistTerminalCloseWaitCap 是关闭路径等待在途 create 的总窗上限:短窗
// (agentTerminalCreateWaitTimeout,照手机腿 input 同款)内不落地时转后台
// 续等的界。cwd 冷缓存解析实测可超 10s(见 WarmAgentAuthorizedExecutionDirectories
// 注释),30s 覆盖之。var(非 const)同 agentTerminalCreateWaitTimeout 处理:
// 测试缩短它以触达放弃分支。
var assistTerminalCloseWaitCap = 30 * time.Second

// wireAssistTerminalLeg 接线协助终端腿:approved 激活建 PTY、终态关 PTY+观看
// 端收摊。NewAgentService 构造期一次性调用(照 webTerminal.onLock 构造期注入
// 先例:早于任何流量,评审指令);测试夹具亦可直调。OnActivated/OnEnded 本就
// 是覆盖式设置,无需额外幂等防。
func (s *AgentService) wireAssistTerminalLeg() {
	if s.assist == nil {
		return
	}
	s.assist.OnActivated(s.assistOnActivated)
	s.assist.OnEnded(s.assistOnEnded)
}

// assistOnActivated approved 激活联动(状态机锁外投递):池满即终态收口
// terminal_limit(上行让云端与 admin 得知,而不是挂着 active 却无终端可用;
// spec §14 坑 1——手机在用会话不被挤掉),有坑则建会话专属 PTY。
//
// enabled 不复查:approved 帧本身经 remoteAgentMessageRequiresEnabledDevice
// 门,disabled 设备收不到它。
func (s *AgentService) assistOnActivated(sessionID string) {
	if s.terminal == nil || s.assist == nil {
		return // 防御:半构造服务不进腿(接线体依赖两字段)
	}
	// 会话在钩子排队期间可能已被终态(批准/过期边界竞态:两钩子可由不同
	// goroutine 各自 drain,runPendingHooks 无全局串行化),再验 active 才动。
	if !s.assist.isActiveSession(sessionID) {
		return
	}
	if !s.terminal.hasHeadroom() {
		if _, err := s.assist.EndAndSignal(AssistEndTerminalLimit); err != nil {
			logger.Warn(fmt.Sprintf("[ASSIST] terminal_limit end failed session_id=%s error=%v", sessionID, err))
		}
		return
	}
	msg := map[string]interface{}{
		"type":       models.AgentEventTerminalCreate,
		"session_id": sessionID,
		// cwd 留空:引擎 resolveAgentTerminalCWD→resolveAgentCWDFallbackHome
		// 兜底——有同步项目目录时落首个授权目录,零目录时落家目录(与手机腿
		// create 的空 cwd 同语义)。rows/cols 缺省:引擎 normalizeTerminal
		// Dimension 落 24/80。
	}
	// create 照手机腿派发点(DispatchTerminalFrame create 臂)同款离读循环:
	// beginCreate 同步登记(紧随的 terminal.input 可等待)+ safeGo 异步起跑
	// ——createWithSink 本体是阻塞调用(shell 解析/cwd 解析/进程起跑),本钩
	// 子跑在 WS 读循环与懒扫路径上,不得内联。顺序保持:激活与终态钩子的同
	// 一会话竞态由 close 路径的 waitForCreate 兜(见 assistCloseTerminal)。
	release := s.terminal.beginCreate(sessionID)
	safeGo("assist.terminal.create", func() {
		defer release()
		s.terminal.createWithSink(msg, s.assistTerminalCompositeWriter(sessionID), nil)
	})
}

// assistOnEnded 终态联动(状态机锁外投递):关协助 PTY(创建在途也兜住)+
// 观看端收终帧收摊。
//
// 时序真相(评审勘误):m.close 只是触发异步 session.kill(),terminal.exit
// 帧由 waitTerminal 的 goroutine 在进程死后才发;而 notifyAndClearViews 在
// 本钩子 goroutine 上同步执行——注册表几乎总是先清空,观看端收不到
// terminal.exit(至多抢到竞态尾巴)。观看页生命周期以 assist.end 为唯一终
// 态信标,**不得等 terminal.exit**(Task 7 观看 WS 收 assist.end 即关连接)。
func (s *AgentService) assistOnEnded(sessionID string, reason assistEndReason) {
	if s.terminal == nil {
		return
	}
	s.assistCloseTerminal(sessionID)
	if s.assist != nil {
		s.assist.notifyAndClearViews(sessionID, assistEndFrame(sessionID, reason))
	}
}

// assistCloseTerminal 关闭协助 PTY。快批快收竞态(approve 后云端立刻 end):
// 先按手机腿 input 同款短窗等待在途 create 落地再杀(get==nil 不发 close,
// 避免 m.close 对无会话路径补发的 exit 帧误导 admin);短窗内仍不落地(cwd
// 冷缓存解析可超 10s)转后台续等有界窗口,读循环不挂长。session id 全局唯一
// (nanos),迟到的关闭只可能落在本会话自己的 PTY 上,跨会话误杀不存在;
// 后台续等也放弃的极端残局由闲置回收兜底(attached idle 24h / detach 30m)。
func (s *AgentService) assistCloseTerminal(sid string) {
	if s.assistWaitCreateSettled(sid, agentTerminalCreateWaitTimeout) {
		s.assistKillTerminal(sid)
		return
	}
	safeGo("assist.terminal.close", func() {
		if s.assistWaitCreateSettled(sid, assistTerminalCloseWaitCap) {
			s.assistKillTerminal(sid)
			return
		}
		logger.Warn(fmt.Sprintf("[ASSIST] terminal close gave up waiting for in-flight create session_id=%s (idle reaper will collect)", sid))
	})
}

// assistWaitCreateSettled 等在途 create 收尾:creating 表无本会话条目(已
// 落地或从未起跑)即真;cap 窗口内逐轮续等(agentTerminalCreateWaitTimeout
// 单轮),超窗放弃返回 false。
func (s *AgentService) assistWaitCreateSettled(sid string, cap time.Duration) bool {
	deadline := time.Now().Add(cap)
	for s.assistCreateInFlight(sid) {
		if time.Now().After(deadline) {
			return false
		}
		s.terminal.waitForCreate(sid, agentTerminalCreateWaitTimeout)
	}
	return true
}

// assistCreateInFlight 只读探针:creating 表是否仍有本会话在途 create。
// agent_terminal.go 本任务只许加 hasHeadroom(硬约束 2),探针落本文件同包
// 读内表,零引擎行为改动。
func (s *AgentService) assistCreateInFlight(sid string) bool {
	s.terminal.mu.Lock()
	defer s.terminal.mu.Unlock()
	_, ok := s.terminal.creating[sid]
	return ok
}

// assistKillTerminal 杀在册协助会话(get==nil 跳过——PTY 已自然退出或从未
// 建成,补发的合成 exit 帧只会误导)。close 走引擎标准路径:kill 后出口帧由
// waitTerminal 经会话 liveWriter(=复合 writer)回放扇出。
func (s *AgentService) assistKillTerminal(sid string) {
	if s.terminal.get(sid) == nil {
		return
	}
	s.terminal.close(map[string]interface{}{
		"type":       models.AgentEventTerminalClose,
		"session_id": sid,
	}, s.assistTerminalCompositeWriter(sid))
}

// assistTerminalCompositeWriter 协助 PTY 的输出复合 writer(engine
// agentTerminalWriter 形态,createWithSink 直接受用并经 swapConn 成为会话
// liveWriter):每帧两路——
//  1. 上行恒走 assistFrameWriter()(每次写入动态解析:测试缝或当前连接
//     writer),admin 重连后活输出自然跟上,无需重 attach;
//  2. terminal.output/exit/error 扇给观看端注册表。terminal.created 刻意不
//     扇:观看 xterm 是纯文本镜像,created/resized 是交互腿的控制帧,观看页
//     生命周期由 assist.state/assist.end 驱动(Task 7)。quota challenge/
//     resolved 帧同样不扇(仅上行)——那是 admin 与设备的裁决面,文本镜像
//     无需感知。
//
// 不对称性(P1 刻意接受):admin 断连时输出在上行腿落地丢弃,观看端仍全量
// 收到——用户实时围观是本特性优先级最高的面;admin 重连后由 ring 回放补齐。
// 断连期上行失败不逐帧告警(ring 已留底,逐帧 warn=日志风暴);观看端写失败
// 仅记日志,两路互不阻断。
func (s *AgentService) assistTerminalCompositeWriter(sid string) agentTerminalWriter {
	return func(v interface{}) error {
		frame, ok := v.(map[string]interface{})
		if !ok {
			// 非标载荷(防御):只上行,不扇出
			return assistFrameWriter()(v)
		}
		upErr := assistFrameWriter()(frame)
		switch remoteString(frame, "type") {
		case models.AgentEventTerminalOutput, models.AgentEventTerminalExit, models.AgentEventTerminalError:
			if s.assist != nil {
				s.assist.fanOutToViews(sid, frame)
			}
		}
		return upErr
	}
}

// ——— remote-WS 终端帧分派点前置门控 ———

// handleAssistTerminalFrame 在 remote-WS terminal 帧分派调用点
// (handleRemoteAgentMessage 的 terminal 四帧 case)前置:assist- 前缀帧归协
// 助腿门控(enabled+active+id 匹配→放行 input/resize 走 DispatchTerminal
// Frame;其余 assist- 前缀帧型拒,quota.resolved 裁决臂除外,见下),无前缀
// 帧返回 false 走手机腿原路径(零改动)。返回 true = 本帧已被协助腿处置
// (放行或拒绝),调用方不得再走手机腿。
//
// 拒绝面语义(P1 帧契约,计划附录 A:设备侧只收 input/resize 下行):
//   - terminal.create:创建在批准时由设备自授权完成,云端永不创建 assist
//     终端——收到即拒(防云侧缺陷/串帧凭空起 PTY)。
//   - terminal.close:关闭来自状态机 End(admin 敲 exit 使 PTY 自然退出、
//     云端随后收口 assist.end),直接 close 帧不在 P1 契约内——拒。
//   - 其余(replay/sessions 等):replay 是云端房间侧职责,一律拒防残帧。
//   - 唯一例外:terminal.quota.resolved 裁决走 handleRemoteAgentMessage 的
//     独立 case,不经本门——「裁决必须照常送达」优先于前缀拒面(裁决对
//     无会话 id 幂等吞掉),「一律拒」以上表述均以本例外为准。
func (s *AgentService) handleAssistTerminalFrame(msg map[string]interface{}, writeJSON func(interface{}) error) bool {
	sid := remoteString(msg, "session_id")
	if !strings.HasPrefix(sid, assistIDPrefix) {
		return false // 无前缀=手机腿会话,原路径处置
	}
	if !s.RemoteAssistEnabled() {
		_ = writeJSON(agentTerminalErrorPayload(sid, errors.New("remote assist is disabled for this device")))
		return true
	}
	// active+id 匹配单锁内取值:会话刚结束/串会话的残留输入在此拒,防死
	// 会话输入打到无主 PTY 上。
	if s.assist == nil || !s.assist.isActiveSession(sid) {
		_ = writeJSON(agentTerminalErrorPayload(sid, errors.New("terminal session not available: assist session is not active")))
		return true
	}
	switch remoteString(msg, "type") {
	case models.AgentEventTerminalInput, models.AgentEventTerminalResize:
		// 放行走统一分派口(connSink=nil:手机腿语义,无网页孤儿身份)。分派
		// 口内对 input/resize 复核 remoteTerminalEnabled 闸——远程终端总闸关
		// 时 settings 推送本就 closeAll 全部会话,协助终端同族受闸一致。
		s.DispatchTerminalFrame(msg, writeJSON, nil, s.remoteTerminalEnabled)
		return true
	default:
		_ = writeJSON(agentTerminalErrorPayload(sid, errors.New("terminal frame not accepted: assist session only takes input/resize from cloud")))
		return true
	}
}
