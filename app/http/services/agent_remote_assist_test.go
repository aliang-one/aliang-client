package services

import (
	"errors"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
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

// ——— Task 4:云端信令接线(帧收发 + 上行 writer 缝) ———

// newTestAssistManagerWithSink 可变时钟 + assist 帧缝的测试装置:缝是进程级
// 全局(assistFrameSink 三件套,照 punch 同族),Cleanup 恢复 nil 防跨测试
// 污染;sink 本体由各测试经 m.SetFrameSink 注入并自行收集外发帧。
func newTestAssistManagerWithSink(t *testing.T) *remoteAssistManager {
	t.Helper()
	t.Cleanup(func() { AssistSetFrameSink(nil) })
	return newTestAssistManager()
}

func TestAssistRequestFrameFlow(t *testing.T) {
	m := newTestAssistManagerWithSink(t) // 注入上行 sink,收集外发帧
	var upFrames []map[string]interface{}
	m.SetFrameSink(func(f map[string]interface{}) error { upFrames = append(upFrames, f); return nil })

	// 设备侧 Request → 上行 assist.request{session_id,message}(载荷保持
	// 最小:设备身份字段由平台侧从连接取,设备不自带)
	sid, err := m.RequestAndSignal("帮我看下")
	if err != nil {
		t.Fatal(err)
	}
	if len(upFrames) != 1 || upFrames[0]["type"] != models.AgentEventAssistRequestUp {
		t.Fatalf("must uplink exactly one assist.request, got %v", upFrames)
	}
	if upFrames[0]["session_id"] != sid || upFrames[0]["message"] != "帮我看下" {
		t.Fatalf("request frame must carry session_id+message: %v", upFrames[0])
	}

	// 云端回 approved(带同 session_id)→ active + 终端激活回调被调
	activated := false
	m.OnActivated(func(id string) { activated = id == sid })
	frames, err := m.OnRemoteFrame(map[string]interface{}{
		"type": models.AgentEventAssistApproved, "session_id": sid,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 0 {
		t.Fatalf("approved must not produce reply frames: %v", frames)
	}
	if !activated {
		t.Fatal("activation hook must fire on approved")
	}
	if got := m.Snapshot().State; got != assistStateActive {
		t.Fatalf("after approved state=%q", got)
	}

	// session_id 不匹配的 approved 必须拒(防串会话)
	if _, err := m.OnRemoteFrame(map[string]interface{}{
		"type": models.AgentEventAssistApproved, "session_id": "assist-other",
	}); err == nil {
		t.Fatal("mismatched approved must fail")
	}
	// 拒帧不得改状态(active 保持,迁移只在成功路径发生)
	if got := m.Snapshot().State; got != assistStateActive {
		t.Fatalf("mismatched approved must not mutate state, state=%q", got)
	}
}

func TestAssistRequestOfflineWriterFails(t *testing.T) {
	m := newTestAssistManagerWithSink(t)
	// 无 sink + 共享远端 writer 未连接(nil)=离线;Request 必须直接失败让
	// 用户看到,不静默——发不出去的申请永远不会进 admin 队列。
	if _, err := m.RequestAndSignal("offline"); err == nil {
		t.Fatal("offline writer must fail the request")
	}
	// 新建回滚到纯净 idle:不置粘性 ended 快照、不设 60s 冷却锚(基础设施
	// 抖动不该让用户吃连发冷却),立即可重试。
	if snap := m.Snapshot(); snap.State != assistStateIdle {
		t.Fatalf("failed signal must roll back to idle, got %+v", snap)
	}
	if _, err := m.Request("retry"); err != nil {
		t.Fatalf("retry after rolled-back request must not hit cooldown: %v", err)
	}
}

func TestAssistUplinkFrameShapes(t *testing.T) {
	m := newTestAssistManagerWithSink(t)
	var up []map[string]interface{}
	m.SetFrameSink(func(f map[string]interface{}) error { up = append(up, f); return nil })

	// requested 态撤销 → assist.cancel(附录 A #4:客户端发起收口恒 cancel)
	sid, err := m.RequestAndSignal("")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.CancelAndSignal(); err != nil {
		t.Fatal(err)
	}
	if len(up) != 2 || up[1]["type"] != models.AgentEventAssistCancel || up[1]["session_id"] != sid {
		t.Fatalf("cancel must uplink assist.cancel{session_id}, got %v", up)
	}

	m.AdvanceClock(61 * time.Second) // 跳出 60s 冷却

	// active 态客户端结束 → 同样 assist.cancel,不带 reason(云端无需动作)
	sid2, err := m.RequestAndSignal("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.OnRemoteFrame(map[string]interface{}{"type": models.AgentEventAssistApproved, "session_id": sid2}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.EndAndSignal(AssistEndClient); err != nil {
		t.Fatal(err)
	}
	if len(up) != 4 || up[3]["type"] != models.AgentEventAssistCancel || up[3]["session_id"] != sid2 {
		t.Fatalf("client-initiated end in active must uplink assist.cancel, got %v", up)
	}
	if _, hasReason := up[3]["reason"]; hasReason {
		t.Fatalf("cancel frame must not carry reason: %v", up[3])
	}

	m.AdvanceClock(61 * time.Second)

	// 设备侧原因(device_restart)→ assist.end{session_id,reason}(帧族
	// 注释:assist.end 双向,设备侧终态上行带原因)
	sid3, err := m.RequestAndSignal("")
	if err != nil {
		t.Fatal(err)
	}
	_ = m.CloudApproved(sid3)
	if _, err := m.EndAndSignal(AssistEndDeviceRestart); err != nil {
		t.Fatal(err)
	}
	if len(up) != 6 || up[5]["type"] != models.AgentEventAssistEnd || up[5]["reason"] != string(AssistEndDeviceRestart) {
		t.Fatalf("device-side end must uplink assist.end{reason}, got %v", up)
	}

	// active 态 CancelAndSignal 走守卫拒绝且不上行
	if err := m.CancelAndSignal(); !errors.Is(err, errAssistNotPending) {
		t.Fatalf("cancel in active must be rejected, got %v", err)
	}
	if len(up) != 6 {
		t.Fatalf("rejected cancel must not uplink: %v", up)
	}
}

func TestAssistEndAndSignalIdleIsIdempotent(t *testing.T) {
	m := newTestAssistManagerWithSink(t)
	var up []map[string]interface{}
	m.SetFrameSink(func(f map[string]interface{}) error { up = append(up, f); return nil })
	if _, err := m.EndAndSignal(AssistEndClient); !errors.Is(err, errAssistNothingToEnd) {
		t.Fatalf("idle EndAndSignal must be a no-op, got %v", err)
	}
	if len(up) != 0 {
		t.Fatalf("idle EndAndSignal must not uplink anything: %v", up)
	}
}

func TestAssistOnRemoteFrameDispatch(t *testing.T) {
	m := newTestAssistManagerWithSink(t)
	m.SetFrameSink(func(map[string]interface{}) error { return nil })

	// rejected → ended/admin_end(拒绝按 admin 侧终态计,Task 2 契约)
	sid, _ := m.Request("")
	frames, err := m.OnRemoteFrame(map[string]interface{}{"type": models.AgentEventAssistRejected, "session_id": sid})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 0 {
		t.Fatalf("rejected must not reply: %v", frames)
	}
	if snap := m.Snapshot(); snap.State != assistStateEnded || snap.EndReason != AssistEndAdmin {
		t.Fatalf("rejected must converge ended/admin_end, got %+v", snap)
	}

	m.AdvanceClock(61 * time.Second)

	// end 下行(admin_end)正常收敛
	sid2, _ := m.Request("")
	_ = m.CloudApproved(sid2)
	if _, err := m.OnRemoteFrame(map[string]interface{}{
		"type": models.AgentEventAssistEnd, "session_id": sid2, "reason": "admin_end",
	}); err != nil {
		t.Fatal(err)
	}
	if snap := m.Snapshot(); snap.State != assistStateEnded || snap.EndReason != AssistEndAdmin {
		t.Fatalf("cloud end must converge ended/admin_end, got %+v", snap)
	}

	// 已终会话的重复 end 帧 → 幂等吞帧不报错(读循环不得错误风暴)
	if _, err := m.OnRemoteFrame(map[string]interface{}{
		"type": models.AgentEventAssistEnd, "session_id": sid2, "reason": "admin_end",
	}); err != nil {
		t.Fatalf("duplicate end must be swallowed idempotently: %v", err)
	}

	m.AdvanceClock(61 * time.Second)

	// 空 reason 的 end 帧必须拒(reason 是契约必填字段)
	sid3, _ := m.Request("")
	_ = m.CloudApproved(sid3)
	if _, err := m.OnRemoteFrame(map[string]interface{}{
		"type": models.AgentEventAssistEnd, "session_id": sid3,
	}); err == nil {
		t.Fatal("end frame without reason must fail")
	}
	if snap := m.Snapshot(); snap.State != assistStateActive {
		t.Fatalf("invalid end frame must not mutate state, state=%q", snap.State)
	}

	// assist.state P1 仅记录日志:无错误、无状态变化、无回帧
	frames, err = m.OnRemoteFrame(map[string]interface{}{
		"type": models.AgentEventAssistState, "session_id": sid3, "state": "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 0 {
		t.Fatalf("state frame must not reply: %v", frames)
	}
	if snap := m.Snapshot(); snap.State != assistStateActive {
		t.Fatalf("state frame must not mutate state, state=%q", snap.State)
	}
}

func TestAssistHooksRunOutsideMutex(t *testing.T) {
	m := newTestAssistManagerWithSink(t)
	m.SetFrameSink(func(map[string]interface{}) error { return nil })
	// 钩子内再入 manager(sync.Mutex 不可重入):若钩子在锁内被调即自死锁。
	// 守卫机制:驱动调用跑在独立 goroutine,完成信号走独立 channel,主测试
	// select 完成信号 vs 2s 超时——真回归(锁内调钩子的死锁、入队后不 drain
	// 的饿死)在 2s 快速 fail,而不是把整包测试吊到包级超时。钩子闭包里调
	// Snapshot 正是锁内调钩子回归的引爆点(重入即挂,挂即被超时捕获)。
	activated := make(chan struct{})
	m.OnActivated(func(string) {
		_ = m.Snapshot()
		close(activated)
	})
	ended := make(chan struct{})
	m.OnEnded(func(string, assistEndReason) {
		_ = m.Snapshot()
		close(ended)
	})
	sid, _ := m.Request("")
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := m.OnRemoteFrame(map[string]interface{}{"type": models.AgentEventAssistApproved, "session_id": sid}); err != nil {
			t.Errorf("approved frame: %v", err)
		}
	}()
	select {
	case <-activated:
	case <-done:
		t.Fatal("OnRemoteFrame returned without firing activation hook (enqueue-without-drain starvation)")
	case <-time.After(2 * time.Second):
		t.Fatal("activation hook must run outside manager mutex (deadlock guard, fail fast at 2s)")
	}
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		if _, err := m.OnRemoteFrame(map[string]interface{}{
			"type": models.AgentEventAssistEnd, "session_id": sid, "reason": "admin_end",
		}); err != nil {
			t.Errorf("end frame: %v", err)
		}
	}()
	select {
	case <-ended:
	case <-done2:
		t.Fatal("OnRemoteFrame returned without firing end hook (enqueue-without-drain starvation)")
	case <-time.After(2 * time.Second):
		t.Fatal("end hook must run outside manager mutex (deadlock guard, fail fast at 2s)")
	}
}

func TestAssistRollbackGuardKeepsApprovedSession(t *testing.T) {
	m := newTestAssistManager()
	sid, _ := m.Request("")
	_ = m.CloudApproved(sid)
	// 白盒钉守卫:信令失败窗口里云端 approved 先到的形态——回滚必须放行
	// 已批准会话(仅「仍 requested 且同 id」才可回滚,见 rollbackCreated)。
	m.rollbackCreated(sid)
	snap := m.Snapshot()
	if snap.State != assistStateActive || snap.SessionID != sid {
		t.Fatalf("approved session must survive rollback, got %+v", snap)
	}
}

func TestAssistRemoteFramesRequireEnabledDevice(t *testing.T) {
	// enabled 门:禁用设备不接受协助信令(与 terminal/AI 控制面同族)。
	for _, typ := range []string{
		models.AgentEventAssistApproved,
		models.AgentEventAssistRejected,
		models.AgentEventAssistEnd,
		models.AgentEventAssistState,
	} {
		if !remoteAgentMessageRequiresEnabledDevice(typ) {
			t.Fatalf("assist frame %s must be behind the enabled-device gate", typ)
		}
	}
}
