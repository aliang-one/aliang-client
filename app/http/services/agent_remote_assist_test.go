package services

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// newTestAssistManager 可变时钟桩:闭包捕获 tick,经 manager.clockAdvance
// 推进(见 agent_remote_assist.go AdvanceClock),驱动过期/时长上限测试。
func newTestAssistManager() *remoteAssistManager {
	tick := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	m := newRemoteAssistManager(func() time.Time { return tick })
	m.clockAdvance = func(d time.Duration) { tick = tick.Add(d) }
	return m
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
	if got := m.Snapshot().Message; got != "改留言" {
		t.Fatalf("duplicate request must update message: %q", got)
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
	if snap.SessionID != sid {
		t.Fatalf("ended snapshot must carry session id: got %q want %q", snap.SessionID, sid)
	}
	// 过期后可重新申请
	if _, err := m.Request("again"); err != nil {
		t.Fatalf("request after expiry: %v", err)
	}
	// 新会话须清掉粘性 ended 快照(Request 复位 endReason)
	if got := m.Snapshot().State; got != assistStateRequested {
		t.Fatalf("re-request must clear sticky ended snapshot, state=%q", got)
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

func TestAssistGuardMatrix(t *testing.T) {
	// 守卫面矩阵:setup 到目标态 → act 触发动作 → 断言错误哨兵与状态快照。
	// 错误哨兵按当前契约钉死(含 CloudRejected 错 id 走合并守卫归并到
	// errAssistNotPending);失败须显式改测试,防止守卫面静默漂移。
	cases := []struct {
		name      string
		setup     func(m *remoteAssistManager) string
		act       func(m *remoteAssistManager, sid string) error
		wantErr   error
		wantState assistState
		wantEnd   assistEndReason // 仅 wantState==ended 时断言
	}{
		{
			name:      "requested 态 Cancel 正常收敛 client_end",
			setup:     func(m *remoteAssistManager) string { sid, _ := m.Request(""); return sid },
			act:       func(m *remoteAssistManager, _ string) error { return m.Cancel() },
			wantErr:   nil,
			wantState: assistStateEnded,
			wantEnd:   AssistEndClient,
		},
		{
			name: "active 态 Cancel 被拒且状态不变",
			setup: func(m *remoteAssistManager) string {
				sid, _ := m.Request("")
				_ = m.CloudApproved(sid)
				return sid
			},
			act:       func(m *remoteAssistManager, _ string) error { return m.Cancel() },
			wantErr:   errAssistNotPending,
			wantState: assistStateActive,
		},
		{
			name:      "CloudRejected 错 id 报错且仍 requested",
			setup:     func(m *remoteAssistManager) string { sid, _ := m.Request(""); return sid },
			act:       func(m *remoteAssistManager, sid string) error { return m.CloudRejected(sid + "-x") },
			wantErr:   errAssistNotPending,
			wantState: assistStateRequested,
		},
		{
			name:      "CloudRejected 对 id 正常收敛 admin_end",
			setup:     func(m *remoteAssistManager) string { sid, _ := m.Request(""); return sid },
			act:       func(m *remoteAssistManager, sid string) error { return m.CloudRejected(sid) },
			wantErr:   nil,
			wantState: assistStateEnded,
			wantEnd:   AssistEndAdmin,
		},
		{
			name:      "idle 态 CloudEnd 被拒",
			setup:     func(m *remoteAssistManager) string { return "" },
			act:       func(m *remoteAssistManager, _ string) error { return m.CloudEnd("assist-x", AssistEndAdmin) },
			wantErr:   errAssistIDMismatch,
			wantState: assistStateIdle,
		},
		{
			name: "active 态 CloudEnd 错 id 被拒且状态不变",
			setup: func(m *remoteAssistManager) string {
				sid, _ := m.Request("")
				_ = m.CloudApproved(sid)
				return sid
			},
			act:       func(m *remoteAssistManager, sid string) error { return m.CloudEnd(sid+"-x", AssistEndAdmin) },
			wantErr:   errAssistIDMismatch,
			wantState: assistStateActive,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestAssistManager()
			sid := tc.setup(m)
			if err := tc.act(m, sid); !errors.Is(err, tc.wantErr) {
				t.Fatalf("act error = %v, want %v", err, tc.wantErr)
			}
			snap := m.Snapshot()
			if snap.State != tc.wantState {
				t.Fatalf("state = %q, want %q", snap.State, tc.wantState)
			}
			if tc.wantState == assistStateEnded && snap.EndReason != tc.wantEnd {
				t.Fatalf("end reason = %q, want %q", snap.EndReason, tc.wantEnd)
			}
		})
	}
}
