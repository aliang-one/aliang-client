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
}

func newRemoteAssistManager(now func() time.Time) *remoteAssistManager {
	if now == nil {
		now = time.Now
	}
	return &remoteAssistManager{now: now}
}

var (
	errAssistCooldown   = errors.New("assist: request cooldown")
	errAssistBusy       = errors.New("assist: session already active or pending")
	errAssistNotPending = errors.New("assist: no pending session")
	// errAssistNotActive 供 Task 4/5 网络接线使用(信令仅在 active 态合法)。
	errAssistNotActive    = errors.New("assist: no active session")
	errAssistIDMismatch   = errors.New("assist: session id mismatch")
	errAssistNothingToEnd = errors.New("assist: nothing to end")
)

func (m *remoteAssistManager) Request(message string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	// 冷却窗口从「回到 idle」起算,只防终态后的连发骚扰(spec §10 限频)。
	// pending 过期豁免锚点(见 finishLocked)——过期已含 5 分钟自然等待,
	// 立即重申是合法路径。
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
	m.finishLocked(AssistEndClient)
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
	m.finishLocked(AssistEndAdmin) // 拒绝按 admin 侧终态计,审计同源
	return nil
}

func (m *remoteAssistManager) End(reason assistEndReason) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stateLocked() == assistStateIdle {
		return errAssistNothingToEnd
	}
	m.finishLocked(reason)
	return nil
}

func (m *remoteAssistManager) CloudEnd(sessionID string, reason assistEndReason) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stateLocked() == assistStateIdle || (m.session != nil && m.session.id != sessionID) {
		return errAssistIDMismatch
	}
	m.finishLocked(reason)
	return nil
}

// Sweep 收敛时间驱动的终态:pending 5 分钟未审批过期,active 超 2 小时上限。
// 由 Snapshot 懒扫驱动(网络层亦可周期调用,Task 4 定)。
func (m *remoteAssistManager) Sweep() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked()
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
	now := m.now()
	m.endReason = reason
	m.endedAt = now
	m.lastSessionID = m.session.id
	if reason != AssistEndExpired {
		m.lastEnded = now
	}
	m.session = nil
}

// Snapshot 当前状态快照。读路径懒扫一次,过期/时长上限在读取时即收敛,
// 无需独立定时器。终态后会话槽位已释放,按粘性终态字段回填 SessionID。
func (m *remoteAssistManager) Snapshot() remoteAssistSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked()
	if m.session != nil {
		snap := remoteAssistSnapshot{
			SessionID:   m.session.id,
			State:       m.stateLocked(),
			Message:     m.session.message,
			RequestedAt: m.session.requestedAt.Format(time.RFC3339),
		}
		if !m.session.approvedAt.IsZero() {
			snap.ApprovedAt = m.session.approvedAt.Format(time.RFC3339)
		}
		return snap
	}
	if m.endReason != "" {
		return remoteAssistSnapshot{
			SessionID: m.lastSessionID,
			State:     assistStateEnded,
			EndReason: m.endReason,
			EndedAt:   m.endedAt.Format(time.RFC3339),
		}
	}
	return remoteAssistSnapshot{} // idle
}

// AdvanceClock 推进注入时钟并立即收敛(仅测试用:生产构造 clockAdvance 为
// nil,真实时钟不可推进,时间驱动终态由 Snapshot 懒扫/周期 Sweep 覆盖)。
// 推进+收敛在同一临界区内完成,共享时钟的写与懒扫读不构成竞争。
func (m *remoteAssistManager) AdvanceClock(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.clockAdvance != nil {
		m.clockAdvance(d)
	}
	m.sweepLocked()
}
