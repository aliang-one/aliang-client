package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/tunnel"
	"aliang.one/nursorgate/common/cache"
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
	// ErrAssistNotPending);失败须显式改测试,防止守卫面静默漂移。
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
			wantErr:   ErrAssistNotPending,
			wantState: assistStateActive,
		},
		{
			name:      "CloudRejected 错 id 报错且仍 requested",
			setup:     func(m *remoteAssistManager) string { sid, _ := m.Request(""); return sid },
			act:       func(m *remoteAssistManager, sid string) error { return m.CloudRejected(sid + "-x") },
			wantErr:   ErrAssistNotPending,
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
	if err := m.CancelAndSignal(); !errors.Is(err, ErrAssistNotPending) {
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
	if _, err := m.EndAndSignal(AssistEndClient); !errors.Is(err, ErrAssistNothingToEnd) {
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
	// P2 Task 1 起含 rrweb 页面镜像帧族(rr.event/rr.snapshot/nav)——镜像
	// 下行同为控制面能力,禁用设备不接受。
	for _, typ := range []string{
		models.AgentEventAssistApproved,
		models.AgentEventAssistRejected,
		models.AgentEventAssistEnd,
		models.AgentEventAssistState,
		models.AgentEventAssistRrEvent,
		models.AgentEventAssistRrSnapshot,
		models.AgentEventAssistNav,
	} {
		if !remoteAgentMessageRequiresEnabledDevice(typ) {
			t.Fatalf("assist frame %s must be behind the enabled-device gate", typ)
		}
	}
}

// ——— 远程协助 P2 Task 1:rrweb 页面镜像帧族下行纯中继 ———

// assistRrEnvHygiene 驱动 handleRemoteAgentMessage 的密闭纪律(照
// agent_legacy_arm_guard_test.go):分派臂 setRemoteConnectionState →
// saveStateLocked 落 agent 状态文件(路径经 cache 目录/HOME 解析),HOME/
// ALIANG_DATA_DIR 指临时目录 + 重算 cache 单例(Cleanup LIFO 先于 t.Setenv
// 还原,防单例指着已删 temp 目录泄漏给同包后续测试)。
func assistRrEnvHygiene(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	t.Cleanup(cache.ResetCacheDirForTest)
}

func TestAssistRrFrameRelayToViews(t *testing.T) {
	assistRrEnvHygiene(t)
	h := newTestAssistManagerWithFakeTerminal(t)
	s := h.svc
	sid, err := s.assist.RequestAndSignal("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.assist.OnRemoteFrame(map[string]interface{}{
		"type": models.AgentEventAssistApproved, "session_id": sid,
	}); err != nil {
		t.Fatal(err)
	}

	probe := &assistViewProbe{}
	s.AssistRegisterViewSink(sid, "view-probe", probe.fan)
	t.Cleanup(func() { s.AssistUnregisterViewSink(sid, "view-probe") })

	// ① assist.rr.event:approved 后经 handleRemoteAgentMessage 注入,探针
	// 收到原样帧(session_id/data 全量透传),零回帧(纯中继)。
	coll, write := newPayloadCollector()
	s.handleRemoteAgentMessage(context.Background(), map[string]interface{}{
		"type":       models.AgentEventAssistRrEvent,
		"session_id": sid,
		"data":       `{"type":3,"data":{"source":2,"texts":["echo routed"]}}`,
	}, write)
	var gotRr map[string]interface{}
	if !assistWaitUntil(2*time.Second, func() bool {
		f, ok := probe.frameOfType(models.AgentEventAssistRrEvent)
		gotRr = f
		return ok && strings.Contains(fmt.Sprint(f["data"]), `"texts"`)
	}) {
		t.Fatal("view probe must receive the relayed assist.rr.event verbatim")
	}
	if gotRr["session_id"] != sid {
		t.Fatalf("relayed rr frame must preserve session_id, got %v want %v", gotRr["session_id"], sid)
	}
	if errs := coll.ofTypes(models.AgentEventError); len(errs) != 0 {
		t.Fatalf("rr relay must not emit any error frame, got %v", errs)
	}

	// ② assist.rr.snapshot 同族中继(FullSnapshot 大帧同走纯中继,语义一致)。
	collSnap, writeSnap := newPayloadCollector()
	s.handleRemoteAgentMessage(context.Background(), map[string]interface{}{
		"type":       models.AgentEventAssistRrSnapshot,
		"session_id": sid,
		"data":       `{"type":2,"data":{"node":"<html/>"}}`,
	}, writeSnap)
	if !assistWaitUntil(2*time.Second, func() bool { return probe.Got(`"node":"<html/>"`) }) {
		t.Fatal("view probe must receive the relayed assist.rr.snapshot verbatim")
	}
	if errs := collSnap.ofTypes(models.AgentEventError); len(errs) != 0 {
		t.Fatalf("snapshot relay must not emit any error frame, got %v", errs)
	}

	// ③ assist.nav 同族(url 载荷全量透传)。
	collNav, writeNav := newPayloadCollector()
	s.handleRemoteAgentMessage(context.Background(), map[string]interface{}{
		"type":       models.AgentEventAssistNav,
		"session_id": sid,
		"url":        "https://assist.example/page-2",
	}, writeNav)
	if !assistWaitUntil(2*time.Second, func() bool {
		_, ok := probe.frameOfType(models.AgentEventAssistNav)
		return ok
	}) {
		t.Fatal("view probe must receive the relayed assist.nav frame")
	}
	nav, _ := probe.frameOfType(models.AgentEventAssistNav)
	if nav["url"] != "https://assist.example/page-2" || nav["session_id"] != sid {
		t.Fatalf("assist.nav must be relayed verbatim (url/session_id preserved), got %v", nav)
	}
	if errs := collNav.ofTypes(models.AgentEventError); len(errs) != 0 {
		t.Fatalf("nav relay must not emit any error frame, got %v", errs)
	}

	// ④ 串会话/陈旧 sid:静默丢弃——零 error 帧、探针零新帧(纯中继面对
	// 残帧不做错误风暴;P1 OnRemoteFrame 的 error 帧语义只属于状态机信令族)。
	before := probe.count()
	collMis, writeMis := newPayloadCollector()
	s.handleRemoteAgentMessage(context.Background(), map[string]interface{}{
		"type":       models.AgentEventAssistRrEvent,
		"session_id": "assist-stale-ghost",
		"data":       `{"type":3}`,
	}, writeMis)
	if errs := collMis.ofTypes(models.AgentEventError); len(errs) != 0 {
		t.Fatalf("mismatched sid must be dropped silently without an error frame, got %v", errs)
	}
	if n := probe.count(); n != before {
		t.Fatalf("mismatched sid must not reach view sinks, probe frames %d -> %d", before, n)
	}
}

func TestAssistRrRelayRejectedOnDisabledDevice(t *testing.T) {
	assistRrEnvHygiene(t)
	// enabled-device 门全路径(gate 成员测试的互补面):禁用设备收 rr 帧族
	// 得标准 error 帧、探针零帧。forceDisconnectRemote 会触达 ai/tunnel/
	// terminal/assist 四管理器,本夹具给齐全四件(空管理器惰性零副作用)
	// ——assistHarness 的裸 svc 无 ai/tunnel,走禁用路径必 nil 指针,不可复用。
	svc := &AgentService{
		terminal: newAgentTerminalManager(),
		assist:   newRemoteAssistManager(nil),
		ai:       newAgentAIManager(),
		tunnel:   tunnel.NewManager(nil),
	}
	svc.mu.Lock()
	svc.state.RemoteAssistEnabled = true // 总闸开着:拒绝必须来自 enabled-device 门而非总闸
	svc.state.Enabled = false            // 设备禁用 → remoteControlAllowed=false
	svc.mu.Unlock()

	probe := &assistViewProbe{}
	svc.AssistRegisterViewSink("assist-ghost", "view-probe", probe.fan)
	t.Cleanup(func() { svc.AssistUnregisterViewSink("assist-ghost", "view-probe") })

	coll, write := newPayloadCollector()
	svc.handleRemoteAgentMessage(context.Background(), map[string]interface{}{
		"type":       models.AgentEventAssistRrEvent,
		"session_id": "assist-ghost",
		"data":       `{"type":2}`,
	}, write)
	errs := coll.ofTypes(models.AgentEventError)
	if len(errs) != 1 || !strings.Contains(fmt.Sprint(errs[0]["error"]), "disabled") {
		t.Fatalf("disabled device must reject rr frames with the standard error frame, got %v", errs)
	}
	if n := probe.count(); n != 0 {
		t.Fatalf("disabled device must not relay, probe got %d frames", n)
	}
}

// ——— Task 5:协助终端腿(approved 激活 + session 前缀门控 + 池余量) ———

// fakeAssistSpawner 协助腿专用假 startProcess:假 PTY 的 stdin 是可回读的
// buffer(断言输入),stdout 是可注入的管道(驱动输出扇出),kill 会话即死
// (记 kill 计数)。照 fakeTerminalSpawner(agent_terminal_attach_test.go)
// 夹具骨架,加输入/输出探针。
type fakeAssistSpawner struct {
	mu      sync.Mutex
	spawned int
	killed  int
	cwds    []string
	// inputBufs 按会话 id 记假 PTY 的 stdin(buffer 输入断言)。
	inputBufs map[string]*bytes.Buffer
	// emitPipes 按会话 id 记假 PTY 的输出管道写端(测试注入输出)。
	emitPipes map[string]*io.PipeWriter
	// alive 按会话 id 记 PTY 是否仍在被 kill 之前(真 exit 走管道关闭语义,
	// 此处 kill 计数即关闭)。
	alive map[string]bool
	// exitDone 控制假 waiter 的归返(默认 nil=立即退出,与手机腿夹具同款
	// 陷阱:立刻退出会让 waitTerminal 抢先清表,生命周期断言失真——这里
	// 全部用 blockExit 长活)。
	blockExit chan struct{}
	// exitOnce 使 blockExit 只关一次(quiesce 与「进程死亡」驱动共用:
	// 测试中途放行(tombstone 用例)后 quiesce 的 Cleanup 不得二关 panic)。
	exitOnce sync.Once
}

func newFakeAssistSpawner() *fakeAssistSpawner {
	return &fakeAssistSpawner{
		inputBufs: make(map[string]*bytes.Buffer),
		emitPipes: make(map[string]*io.PipeWriter),
		alive:     make(map[string]bool),
		blockExit: make(chan struct{}),
	}
}

// releaseExit 放行全部假 waiter(幂等):模拟「进程死亡」——生产 kill 后
// 真进程退出、waiter 归返、tombstone 落 history;假身 kill 只记数不归返,
// 需要 tombstone 的用例显式调用本方法补上这步。
func (f *fakeAssistSpawner) releaseExit() {
	f.exitOnce.Do(func() { close(f.blockExit) })
}

func (f *fakeAssistSpawner) start(sessionID string, shell string, cwd string, rows int, cols int) (*agentTerminalSession, []io.Reader, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spawned++
	f.cwds = append(f.cwds, cwd)
	f.alive[sessionID] = true
	stdin := &bytes.Buffer{}
	f.inputBufs[sessionID] = stdin
	pipeReader, pipeWriter := io.Pipe()
	f.emitPipes[sessionID] = pipeWriter
	handle := &agentTerminalHandle{
		input: nopWriteCloser{Writer: stdin},
		wait: func() (int, error) {
			<-f.blockExit
			return 0, nil
		},
		kill: func() error {
			f.mu.Lock()
			f.killed++
			f.alive[sessionID] = false
			f.mu.Unlock()
			return nil
		},
		close: func() error { return nil },
	}
	return newAgentTerminalSession(sessionID, shell, cwd, handle, false, rows, cols), []io.Reader{pipeReader}, nil
}

// spawnedCount 已 spawn 的假 PTY 总数(收尾静默用)。
func (f *fakeAssistSpawner) spawnedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spawned
}

// assistHarness 绑 assist manager + 假终端引擎的 AgentService 装置:
// startProcess 换假、wireAssistTerminalLeg 接线、上行 sink 收帧。
// Cleanup 纪律(次序刻意,LIFO):先放行假 PTY 并静等每个会话的 exit 帧
// 落上行缝,再摘缝——exit 帧经复合 writer 上行,若在摘缝后才写,
// assistFrameWriter 会回落 currentRemoteWriterFunc → 触发共享 AgentService
// 构造(重量级副作用,测试绝不引入)。
type assistHarness struct {
	svc          *AgentService
	spawner      *fakeAssistSpawner
	up           []map[string]interface{}
	upMu         sync.Mutex
	collectFrame func(map[string]interface{}) error
}

func newTestAssistManagerWithFakeTerminal(t *testing.T) *assistHarness {
	t.Helper()
	dir := t.TempDir()
	setAgentAuthorizedExecutionDirectoriesCache([]string{dir})
	t.Cleanup(func() { setAgentAuthorizedExecutionDirectoriesCache(nil) })

	// 预置 authorized 目录缓存让 resolveAgentTerminalCWD 走授权分支而非
	// 全量扫描(冷扫描秒级,测试噪音)。
	h := &assistHarness{spawner: newFakeAssistSpawner()}
	// 可变时钟(照 newTestAssistManager 桩):测试经 AdvanceClock 跳 60s 冷却
	// 与时间窗,免真实 sleep。
	tick := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	h.svc = &AgentService{
		terminal: newAgentTerminalManager(),
		assist:   newRemoteAssistManager(func() time.Time { return tick }),
	}
	h.svc.assist.clockAdvance = func(d time.Duration) { tick = tick.Add(d) }
	// 门控复核 RemoteAssistEnabled(服务 s.mu 下的 state 顶层字段),夹具
	// 直接置位(生产由 NewAgentService 装载持久化态);Enabled+Registered
	// +nil Device 让 remoteTerminalEnabled 同放行(放行的 input 走统一
	// 分派口的 enabled 闸)。
	h.svc.mu.Lock()
	h.svc.state.RemoteAssistEnabled = true
	h.svc.state.Enabled = true
	h.svc.state.Registered = true
	h.svc.mu.Unlock()
	h.svc.terminal.startProcess = h.spawner.start
	t.Cleanup(func() { AssistSetFrameSink(nil) })
	h.collectFrame = func(f map[string]interface{}) error {
		h.upMu.Lock()
		h.up = append(h.up, f)
		h.upMu.Unlock()
		return nil
	}
	AssistSetFrameSink(h.collectFrame)
	h.svc.wireAssistTerminalLeg()
	t.Cleanup(func() { h.quiesceFakeTerminals(t) })
	return h
}

// quiesceFakeTerminals 放行全部假 PTY(blockExit)并静等每个「此刻仍在 live
// 表」会话的 exit 帧落上行缝。期望集取快照于放行前:fake waiter 被 kill 后仍
// 卡在 blockExit,waitTerminal 未走完 active 分支的会话放行后各回一帧 exit;
// 已不在表的(closeAll 清场、composite 合成路径)走 inactive 分支不回帧,不
// 计入。waiter 为 nil 的白盒会话(池占位)无 goroutine,不计入。超时硬失败,
// 防止静默退化成上面注释的共享服务构造。
func (h *assistHarness) quiesceFakeTerminals(t *testing.T) {
	t.Helper()
	h.svc.terminal.mu.Lock()
	pending := make(map[string]bool, len(h.svc.terminal.sessions))
	for id, sess := range h.svc.terminal.sessions {
		if sess != nil && sess.waiter != nil {
			pending[id] = true
		}
	}
	h.svc.terminal.mu.Unlock()
	h.spawner.releaseExit()
	deadline := time.Now().Add(5 * time.Second)
	for len(pending) > 0 && time.Now().Before(deadline) {
		for id := range pending {
			if h.upHasExitFor(id) {
				delete(pending, id)
			}
		}
		if len(pending) == 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(pending) > 0 {
		missing := make([]string, 0, len(pending))
		for id := range pending {
			missing = append(missing, id)
		}
		t.Fatalf("quiesce: exit frames missing for %v (uplink seam must see them before removal)", missing)
	}
}

func (h *assistHarness) upSnapshot() []map[string]interface{} {
	h.upMu.Lock()
	defer h.upMu.Unlock()
	return append([]map[string]interface{}(nil), h.up...)
}

// upOfTypes 取上行帧里指定类型(保序)。
func (h *assistHarness) upOfTypes(want ...string) []map[string]interface{} {
	set := make(map[string]bool, len(want))
	for _, t := range want {
		set[t] = true
	}
	var out []map[string]interface{}
	for _, p := range h.upSnapshot() {
		if t, ok := p["type"].(string); ok && set[t] {
			out = append(out, p)
		}
	}
	return out
}

// upHasData 报告上行是否已出现含该子串的 terminal.output 帧(输出腿断言)。
func (h *assistHarness) upHasData(substr string) bool {
	for _, p := range h.upOfTypes(models.AgentEventTerminalOutput) {
		if strings.Contains(fmt.Sprint(p["data"]), substr) {
			return true
		}
	}
	return false
}

// upGot 保留计划测试草稿里的语义名(uplinkGot 同义)。
func (h *assistHarness) upGot(substr string) bool { return h.upHasData(substr) }

// upHasExitFor 报告该会话的 terminal.exit 帧是否已落上行缝。
func (h *assistHarness) upHasExitFor(sid string) bool {
	for _, p := range h.upOfTypes(models.AgentEventTerminalExit) {
		if p["session_id"] == sid {
			return true
		}
	}
	return false
}

// fakeTerminalCWD 返回协助会话 PTY 的 cwd(空=未创建)。
func (h *assistHarness) fakeTerminalCWD(sid string) string {
	if s := h.svc.terminal.get(sid); s != nil {
		return s.cwd
	}
	return ""
}

// fakeTerminalAlive 报告 PTY 是否仍在引擎 live 表且未被 kill。
func (h *assistHarness) fakeTerminalAlive(sid string) bool {
	if h.svc.terminal.get(sid) == nil {
		return false
	}
	h.spawner.mu.Lock()
	defer h.spawner.mu.Unlock()
	return h.spawner.alive[sid]
}

// fakeTerminalEmit 从假 PTY 注入输出字节(驱动复制循环→复合 writer 双腿)。
func (h *assistHarness) fakeTerminalEmit(sid string, data string) {
	h.spawner.mu.Lock()
	w := h.spawner.emitPipes[sid]
	h.spawner.mu.Unlock()
	if w == nil {
		return
	}
	_, _ = w.Write([]byte(data))
}

// assistViewProbe 测试用观看端探针:收帧+收终帧即关。
type assistViewProbe struct {
	mu     sync.Mutex
	frames []map[string]interface{}
	closed bool
}

func (p *assistViewProbe) fan(frame map[string]interface{}) error {
	p.mu.Lock()
	p.frames = append(p.frames, frame)
	p.mu.Unlock()
	return nil
}

func (p *assistViewProbe) Got(substr string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, f := range p.frames {
		if strings.Contains(fmt.Sprint(f["data"]), substr) {
			return true
		}
	}
	return false
}

// count 探针已收帧总数(rr 中继「静默丢弃」断言用:前后计数不变)。
func (p *assistViewProbe) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.frames)
}

// frameOfType 返回探针收到的首个指定帧型帧(原样引用,中继透传断言用)。
func (p *assistViewProbe) frameOfType(typ string) (map[string]interface{}, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, f := range p.frames {
		if t, _ := f["type"].(string); t == typ {
			return f, true
		}
	}
	return nil, false
}

func (p *assistViewProbe) endFrame() (map[string]interface{}, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, f := range p.frames {
		if t, _ := f["type"].(string); t == models.AgentEventAssistEnd {
			return f, true
		}
	}
	return nil, false
}

// assistWaitUntil 轮询断言辅助:cond 在超时前变真返回 true。
func assistWaitUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func TestAssistTerminalLifecycle(t *testing.T) {
	h := newTestAssistManagerWithFakeTerminal(t)
	sid, err := h.svc.assist.RequestAndSignal("")
	if err != nil {
		t.Fatal(err)
	}

	// approved → 终端异步创建(create 离钩子体跑)。cwd=授权目录首项(零
	// 同步目录设备上则落家目录——resolveAgentCWDFallbackHome 兜底,引擎
	// 语义被既有 cwd fallback 测试族钉住)。
	if _, err := h.svc.assist.OnRemoteFrame(map[string]interface{}{
		"type": models.AgentEventAssistApproved, "session_id": sid,
	}); err != nil {
		t.Fatal(err)
	}
	if !assistWaitUntil(5*time.Second, func() bool { return h.fakeTerminalCWD(sid) != "" }) {
		t.Fatal("terminal must be created at approval with home cwd")
	}

	// output 经复合 writer:同时写上行 sink 与观看扇出。
	probe := &assistViewProbe{}
	h.svc.assist.RegisterViewSink(sid, "view1", probe.fan)
	h.fakeTerminalEmit(sid, "hello")
	if !assistWaitUntil(5*time.Second, func() bool { return h.upGot("hello") && probe.Got("hello") }) {
		t.Fatalf("output must fan out to both uplink and view sinks (up=%v view=%d frames)",
			h.upOfTypes(models.AgentEventTerminalOutput), len(probe.frames))
	}

	// End → 终端关闭、观看 sink 收 assist.end{reason}、注册表清空。
	if err := h.svc.assist.End(AssistEndAdmin); err != nil {
		t.Fatal(err)
	}
	if !assistWaitUntil(5*time.Second, func() bool { return !h.fakeTerminalAlive(sid) }) {
		t.Fatal("end must close the assist terminal")
	}
	endFrame, ok := probe.endFrame()
	if !ok {
		t.Fatalf("view sink must receive assist.end, got %v", probe.frames)
	}
	if endFrame["reason"] != string(AssistEndAdmin) || endFrame["session_id"] != sid {
		t.Fatalf("assist.end frame = %v, want reason=%s session=%s", endFrame, AssistEndAdmin, sid)
	}
	// 注册表已清:后续扇出无接收者(内部态断言)。
	h.svc.assist.viewsMu.Lock()
	_, hasViews := h.svc.assist.views[sid]
	h.svc.assist.viewsMu.Unlock()
	if hasViews {
		t.Fatal("end must clear the view sink registry for the session")
	}
}

// TestAssistTerminalCreationIsAsyncOffReadLoop 钉激活钩子不阻塞投递方:
// approved 帧处理(含钩子 drain)必须同步返回,create 在后台落地——钩子跑在
// WS 读循环与懒扫路径上,内联阻塞=卡死读循环(任务铁律)。
func TestAssistTerminalCreationIsAsyncOffReadLoop(t *testing.T) {
	h := newTestAssistManagerWithFakeTerminal(t)
	sid, _ := h.svc.assist.RequestAndSignal("")
	start := time.Now()
	if _, err := h.svc.assist.OnRemoteFrame(map[string]interface{}{
		"type": models.AgentEventAssistApproved, "session_id": sid,
	}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("approved frame handling blocked %v (create must be off the hook body)", d)
	}
	if !assistWaitUntil(5*time.Second, func() bool { return h.fakeTerminalCWD(sid) != "" }) {
		t.Fatal("terminal must still land asynchronously")
	}
}

// TestAssistTerminalRapidApproveEndRace 钉快批快收竞态:approve 后立刻 End,
// 关闭路径必须兜住在途 create(等待落地再杀),最终 PTY 必死、不得存活悬挂。
func TestAssistTerminalRapidApproveEndRace(t *testing.T) {
	h := newTestAssistManagerWithFakeTerminal(t)
	sid, _ := h.svc.assist.RequestAndSignal("")
	if _, err := h.svc.assist.OnRemoteFrame(map[string]interface{}{
		"type": models.AgentEventAssistApproved, "session_id": sid,
	}); err != nil {
		t.Fatal(err)
	}
	// 立刻终态——create 大概率仍在途(几毫秒内)。
	if err := h.svc.assist.End(AssistEndClient); err != nil {
		t.Fatal(err)
	}
	if !assistWaitUntil(8*time.Second, func() bool { return !h.fakeTerminalAlive(sid) }) {
		t.Fatal("rapid approve→end must still close the terminal (in-flight create awaited)")
	}
}

// TestAssistTerminalEndBeforeApproveIsNoOp 终态先于激活:pending 撤销不建
// PTY(激活钩子只在 approved 迁移触发,requested→ended 无激活)。
func TestAssistTerminalEndBeforeApproveIsNoOp(t *testing.T) {
	h := newTestAssistManagerWithFakeTerminal(t)
	if _, err := h.svc.assist.RequestAndSignal(""); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.assist.End(AssistEndClient); err != nil {
		t.Fatal(err)
	}
	h.spawner.mu.Lock()
	spawned := h.spawner.spawned
	h.spawner.mu.Unlock()
	if spawned != 0 {
		t.Fatalf("ending a pending (never approved) session must not spawn a terminal, spawned=%d", spawned)
	}
}

func TestAssistTerminalPrefixRouting(t *testing.T) {
	h := newTestAssistManagerWithFakeTerminal(t)
	s := h.svc
	// 无 assist- 前缀帧:门控返回 false(调用点继续走手机腿 Dispatch
	// TerminalFrame,既有行为零改动)。
	if s.handleAssistTerminalFrame(map[string]interface{}{
		"type":       models.AgentEventTerminalCreate,
		"session_id": "plain-mobile-session",
	}, func(interface{}) error { return nil }) {
		t.Fatal("non-assist-prefixed frames must fall through to the mobile leg")
	}

	// assist- 前缀帧在无 active 会话时一律拒(防会话结束后残留输入)。
	rejected := false
	var gotErr string
	errWrite := func(v interface{}) error {
		if m, ok := v.(map[string]interface{}); ok {
			if t, _ := m["type"].(string); t == models.AgentEventTerminalError {
				rejected = true
				gotErr = fmt.Sprint(m["error"])
			}
		}
		return nil
	}
	if !s.handleAssistTerminalFrame(map[string]interface{}{
		"type":       models.AgentEventTerminalInput,
		"session_id": "assist-ghost",
		"data":       "x",
	}, errWrite) {
		t.Fatal("assist-prefixed frame must be handled by the assist leg")
	}
	if !rejected || !strings.Contains(gotErr, "not active") {
		t.Fatalf("assist-prefixed input without an active session must be rejected with a not-active error, got rejected=%t err=%q", rejected, gotErr)
	}

	// assist- 前缀的 create/close/replay 在无 active 会话时同样走 not-active
	// 拒(active 门先于帧型分派;active 态的「仅 input/resize」拒面在下文)。
	for _, typ := range []string{
		models.AgentEventTerminalCreate,
		models.AgentEventTerminalClose,
		models.AgentEventTerminalReplay,
	} {
		rejected = false
		gotErr = ""
		if !s.handleAssistTerminalFrame(map[string]interface{}{
			"type":       typ,
			"session_id": "assist-ghost",
		}, errWrite) {
			t.Fatalf("assist-prefixed %s must be handled (rejected) by the assist leg", typ)
		}
		if !rejected || !strings.Contains(gotErr, "not active") {
			t.Fatalf("assist-prefixed %s without an active session must be rejected with a not-active error, got rejected=%t err=%q", typ, rejected, gotErr)
		}
	}

	// active 会话时:input 放行落到引擎(mobile 腿分派口),无拒绝帧;
	// stdin buffer 收到字节即路由穿透实证。
	sid, _ := s.assist.RequestAndSignal("")
	if _, err := s.assist.OnRemoteFrame(map[string]interface{}{
		"type": models.AgentEventAssistApproved, "session_id": sid,
	}); err != nil {
		t.Fatal(err)
	}
	if !assistWaitUntil(5*time.Second, func() bool { return h.fakeTerminalCWD(sid) != "" }) {
		t.Fatal("terminal must be live before routing input")
	}
	collActive, writeActive := newPayloadCollector()
	if !s.handleAssistTerminalFrame(map[string]interface{}{
		"type":       models.AgentEventTerminalInput,
		"session_id": sid,
		"data":       "echo routed\r",
	}, writeActive) {
		t.Fatal("active-session input must be handled by the assist leg")
	}
	if errs := collActive.ofTypes(models.AgentEventTerminalError); len(errs) != 0 {
		t.Fatalf("active-session input must dispatch into the engine, got errors %v", errs)
	}
	if !assistWaitUntil(5*time.Second, func() bool {
		h.spawner.mu.Lock()
		defer h.spawner.mu.Unlock()
		return strings.Contains(h.spawner.inputBufs[sid].String(), "echo routed")
	}) {
		t.Fatal("input must reach the assist PTY stdin")
	}

	// active 态下 create/close/replay 一律拒(P1 帧契约:创建在批准时由设备
	// 完成,云端永不创建;关闭来自状态机 End 而非云端 close 帧)。
	for _, typ := range []string{
		models.AgentEventTerminalCreate,
		models.AgentEventTerminalClose,
		models.AgentEventTerminalReplay,
	} {
		collRej, writeRej := newPayloadCollector()
		if !s.handleAssistTerminalFrame(map[string]interface{}{
			"type": typ, "session_id": sid,
		}, writeRej) {
			t.Fatalf("active-session assist-prefixed %s must be handled by the assist leg", typ)
		}
		errs := collRej.ofTypes(models.AgentEventTerminalError)
		if len(errs) != 1 || !strings.Contains(fmt.Sprint(errs[0]["error"]), "input/resize") {
			t.Fatalf("active-session assist-prefixed %s must be rejected once with the input/resize copy, got %v", typ, errs)
		}
	}
}

func TestAssistTerminalPoolHeadroom(t *testing.T) {
	h := newTestAssistManagerWithFakeTerminal(t)
	s := h.svc

	// 占满 4 坑(手机腿会话,非 assist 前缀)。killer 记录被杀者,断言
	// 「一个不少」用 exit 后计数复核。
	killed := make(chan string, agentMaxTerminalSessions)
	s.terminal.mu.Lock()
	for i := 0; i < agentMaxTerminalSessions; i++ {
		id := fmt.Sprintf("mobile-%d", i)
		s.terminal.sessions[id] = &agentTerminalSession{
			id:        id,
			killer:    func() error { killed <- id; return nil },
			token:     new(struct{}),
			startedAt: time.Now(),
		}
	}
	s.terminal.mu.Unlock()

	sid, _ := s.assist.RequestAndSignal("")
	if _, err := s.assist.OnRemoteFrame(map[string]interface{}{
		"type": models.AgentEventAssistApproved, "session_id": sid,
	}); err != nil {
		t.Fatal(err)
	}

	// approved 撞满池 → EndAndSignal(AssistEndTerminalLimit):状态终态、
	// 上行 assist.end{reason=terminal_limit}、无 PTY 被挤掉。
	if !assistWaitUntil(5*time.Second, func() bool {
		snap := s.assist.Snapshot()
		return snap.State == assistStateEnded && snap.EndReason == AssistEndTerminalLimit
	}) {
		snap := s.assist.Snapshot()
		t.Fatalf("full pool must end the approval with terminal_limit, got %+v", snap)
	}
	ends := h.upOfTypes(models.AgentEventAssistEnd)
	if len(ends) == 0 {
		t.Fatal("terminal_limit end must uplink assist.end")
	}
	last := ends[len(ends)-1]
	if last["reason"] != string(AssistEndTerminalLimit) || last["session_id"] != sid {
		t.Fatalf("terminal_limit uplink frame = %v", last)
	}
	// 手机会话一个都不少(kill 零发生 + live 表仍满 4)。
	select {
	case id := <-killed:
		t.Fatalf("mobile session %s must never be evicted by an assist approval", id)
	default:
	}
	s.terminal.mu.Lock()
	mobileCount := 0
	for id := range s.terminal.sessions {
		if strings.HasPrefix(id, "mobile-") {
			mobileCount++
		}
	}
	s.terminal.mu.Unlock()
	if mobileCount != agentMaxTerminalSessions {
		t.Fatalf("mobile sessions must never be evicted by an assist approval, alive=%d", mobileCount)
	}
	if h.fakeTerminalCWD(sid) != "" {
		t.Fatal("no assist terminal may be created when the pool is full")
	}
}

// TestAssistTerminalCompositeWriterUplinkDropViewsLive 钉不对称性:上行 writer
// 断连(admin 掉线,frameWriter 失败)时观看端仍收全量输出(P1 刻意接受:
// 围观优先,admin 重连由 ring 回放补齐)。
func TestAssistTerminalCompositeWriterUplinkDropViewsLive(t *testing.T) {
	h := newTestAssistManagerWithFakeTerminal(t)
	s := h.svc
	sid, _ := s.assist.RequestAndSignal("")
	if _, err := s.assist.OnRemoteFrame(map[string]interface{}{
		"type": models.AgentEventAssistApproved, "session_id": sid,
	}); err != nil {
		t.Fatal(err)
	}
	if !assistWaitUntil(5*time.Second, func() bool { return h.fakeTerminalCWD(sid) != "" }) {
		t.Fatal("terminal must be live")
	}
	// 换掉上行 sink 为失败 writer(admin 掉线等价)。还原挂 Cleanup 于换帧
	// 之后立即注册——断言中途失败时失败缝不会跨测试泄漏;收尾静默
	// (quiesceFakeTerminals)依赖 exit 帧落上行缝,失败缝会让该等待不满足。
	AssistSetFrameSink(func(map[string]interface{}) error { return errors.New("uplink offline") })
	t.Cleanup(func() { AssistSetFrameSink(h.collectFrame) })
	probe := &assistViewProbe{}
	s.assist.RegisterViewSink(sid, "view1", probe.fan)
	h.fakeTerminalEmit(sid, "still-visible")
	if !assistWaitUntil(5*time.Second, func() bool { return probe.Got("still-visible") }) {
		t.Fatal("view sinks must keep receiving output while the uplink leg is down")
	}
}

// TestAssistViewSinkRegistrySemantics 钉注册表 API 语义:幂等覆盖同 viewID、
// 注销最后一个 sink 清会话桶、多 sink 并存扇出。
func TestAssistViewSinkRegistrySemantics(t *testing.T) {
	h := newTestAssistManagerWithFakeTerminal(t)
	s := h.svc
	sid, _ := s.assist.RequestAndSignal("")
	if _, err := s.assist.OnRemoteFrame(map[string]interface{}{
		"type": models.AgentEventAssistApproved, "session_id": sid,
	}); err != nil {
		t.Fatal(err)
	}
	if !assistWaitUntil(5*time.Second, func() bool { return h.fakeTerminalCWD(sid) != "" }) {
		t.Fatal("terminal must be live")
	}
	p1, p2 := &assistViewProbe{}, &assistViewProbe{}
	s.assist.RegisterViewSink(sid, "v1", p1.fan)
	s.assist.RegisterViewSink(sid, "v2", p2.fan)
	h.fakeTerminalEmit(sid, "multi")
	if !assistWaitUntil(5*time.Second, func() bool { return p1.Got("multi") && p2.Got("multi") }) {
		t.Fatal("all registered view sinks must receive output")
	}
	// 幂等覆盖:同 viewID 重注册换函数,旧函数不再收。
	p1b := &assistViewProbe{}
	s.assist.RegisterViewSink(sid, "v1", p1b.fan)
	s.assist.UnregisterViewSink(sid, "v2")
	h.fakeTerminalEmit(sid, "after-swap")
	if !assistWaitUntil(5*time.Second, func() bool { return p1b.Got("after-swap") }) {
		t.Fatal("re-registered sink must receive output")
	}
	if p1.Got("after-swap") {
		t.Fatal("overwritten sink must no longer receive output")
	}
	if p2.Got("after-swap") {
		t.Fatal("unregistered sink must no longer receive output")
	}
	// 注销最后一个 sink → 会话桶清空。
	s.assist.UnregisterViewSink(sid, "v1")
	s.assist.viewsMu.Lock()
	_, has := s.assist.views[sid]
	s.assist.viewsMu.Unlock()
	if has {
		t.Fatal("session bucket must be dropped with its last sink")
	}
}

// TestAssistViewSinkPanicDoesNotKillFanLoop 钉评审 Fix 3:扇出的 sink 是外部
// 代码,单 sink panic 必须被 recover 成单路失败(其余 sink 照常收帧,测试进程
// 活着——fan 跑在 PTY 复制循环 goroutine 上,panic 不 recover 即杀全进程)。
func TestAssistViewSinkPanicDoesNotKillFanLoop(t *testing.T) {
	h := newTestAssistManagerWithFakeTerminal(t)
	s := h.svc
	sid, _ := s.assist.RequestAndSignal("")
	if _, err := s.assist.OnRemoteFrame(map[string]interface{}{
		"type": models.AgentEventAssistApproved, "session_id": sid,
	}); err != nil {
		t.Fatal(err)
	}
	if !assistWaitUntil(5*time.Second, func() bool { return h.fakeTerminalCWD(sid) != "" }) {
		t.Fatal("terminal must be live")
	}
	healthy := &assistViewProbe{}
	s.assist.RegisterViewSink(sid, "v-boom", func(map[string]interface{}) error {
		panic("view sink exploded")
	})
	s.assist.RegisterViewSink(sid, "v-ok", healthy.fan)
	// 多轮投递:健康 sink 每轮都收到(循环没被第一爆带走);爆掉的
	// v-boom 第一轮 panic 后即被自动注销,后续轮次不再触达(见下方注)。
	for i := 0; i < 3; i++ {
		h.fakeTerminalEmit(sid, "round")
	}
	if !assistWaitUntil(5*time.Second, func() bool {
		healthy.mu.Lock()
		defer healthy.mu.Unlock()
		return len(healthy.frames) >= 3
	}) {
		t.Fatalf("healthy sink must keep receiving frames across a panicking sibling, got %d", len(healthy.frames))
	}
	// 终态路径的 panic 同样被吞:notifyAndClearViews 收摊收住爆掉的 sink。
	// 注(Task 7 扇出错误自动注销语义):v-boom 在第一轮 panic 后即被
	// fanOutToViews 自动注销,后续轮次只驱动健康 sink——本用例的断言
	// (健康 sink 每轮都收到)不依赖爆掉 sink 的持续在册,勿误读为
	// 「panic sink 反复重注册」。
	s.assist.RegisterViewSink(sid, "v-boom-end", func(map[string]interface{}) error {
		panic("end notify exploded")
	})
	if err := s.assist.End(AssistEndAdmin); err != nil {
		t.Fatal(err) // 走到这里 = panic 被 recover,进程与断言都活着
	}
	if endFrame, ok := healthy.endFrame(); !ok || endFrame["reason"] != string(AssistEndAdmin) {
		t.Fatalf("healthy sink must still get assist.end alongside a panicking sibling, got %+v", healthy.frames)
	}
}

// TestAssistHasHeadroom 钥匙面:空 manager 有余量,占满即无。
func TestAssistHasHeadroom(t *testing.T) {
	m := newAgentTerminalManager()
	if !m.hasHeadroom() {
		t.Fatal("fresh manager must report headroom")
	}
	m.mu.Lock()
	for i := 0; i < agentMaxTerminalSessions; i++ {
		m.sessions[fmt.Sprintf("h-%d", i)] = &agentTerminalSession{id: fmt.Sprintf("h-%d", i)}
	}
	m.mu.Unlock()
	if m.hasHeadroom() {
		t.Fatal("full manager must report no headroom")
	}
}

// TestAssistTerminalCloseGiveUpOnBlockedCreate 钉放弃分支(评审 Fix 2):
// startProcess 卡死的在途 create,缩短总窗后后台关闭 goroutine 必须放弃
// (warn 日志)且不挂死;spawner 放行后延迟落地的会话仍被收——两条真身路径:
//  1. closeAll(remote 循环终态/forceDisconnect 的生产收摊面,Task 4 已接线);
//  2. assistCloseTerminal 直调(任何后续 end 迁移会再走它,含未来 dup 收口)。
//
// 放弃后不入 closeAll 面的孤儿由闲置回收兜底(24h attached),不在本测试面。
func TestAssistTerminalCloseGiveUpOnBlockedCreate(t *testing.T) {
	// 缩窗:总窗 200ms + 单轮 50ms(agentTerminalCreateWaitTimeout 是全局
	// var,restore 走 defer)。
	oldCap := assistTerminalCloseWaitCap
	assistTerminalCloseWaitCap = 200 * time.Millisecond
	defer func() { assistTerminalCloseWaitCap = oldCap }()
	oldWait := agentTerminalCreateWaitTimeout
	agentTerminalCreateWaitTimeout = 50 * time.Millisecond
	defer func() { agentTerminalCreateWaitTimeout = oldWait }()

	h := newTestAssistManagerWithFakeTerminal(t)
	s := h.svc

	// 卡死的 startProcess:批准前换上,create 永不落地直到放行。
	blocked := make(chan struct{})
	spawnEntered := make(chan struct{}, 1)
	s.terminal.startProcess = func(sessionID string, shell string, cwd string, rows int, cols int) (*agentTerminalSession, []io.Reader, error) {
		spawnEntered <- struct{}{}
		<-blocked
		return h.spawner.start(sessionID, shell, cwd, rows, cols)
	}
	t.Cleanup(func() {
		select {
		case <-blocked:
		default:
			close(blocked)
		}
	})

	sid, _ := s.assist.RequestAndSignal("")
	if _, err := s.assist.OnRemoteFrame(map[string]interface{}{
		"type": models.AgentEventAssistApproved, "session_id": sid,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-spawnEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("fake create must enter the blocked spawner")
	}

	// approve→end:短窗内 create 仍卡死 → 转后台续等 → 200ms 总窗放弃。
	// End 同步返回(不挂)即证明读循环不被长等拖死;放弃分支只留 warn。
	// (warn 文本断言不可行:logger 无注入缝,行为断言替代——放弃后烧窗期间
	// 会话不落地、后台 goroutine 不挂死,放行后由追杀路径收。)
	done := make(chan error, 1)
	go func() { done <- s.assist.End(AssistEndAdmin) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("end: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("end must not hang while the create is blocked (background wait is bounded)")
	}
	// 放弃窗口烧完:creating 条目仍在(spawner 卡死),后台 goroutine 已放弃——
	// 直接证据:此刻 end 早已终态而 PTY 未被杀(spawner 未放行、create 未落地)。
	time.Sleep(300 * time.Millisecond)
	if got := s.terminal.get(sid); got != nil {
		t.Fatal("no terminal may exist while the spawner is still blocked")
	}
	if !s.assistCreateInFlight(sid) {
		t.Log("create already settled before give-up (timing); continuing with the landed session")
	}

	// 放行 spawner:延迟 create 落地(会话注册进 live 表)。
	close(blocked)
	if !assistWaitUntil(5*time.Second, func() bool { return s.terminal.get(sid) != nil }) {
		t.Fatal("delayed terminal must land after the spawner unblocks")
	}

	// 追杀路径 1:closeAll(remote 循环终态的生产收摊面)。
	s.terminal.closeAll()
	if s.terminal.get(sid) != nil {
		t.Fatal("closeAll must reclaim the delayed terminal (loop-teardown path)")
	}

	// 追杀路径 2(独立会话再验):再起一个卡死 create 走到放弃,放行落地后
	// 直调 assistCloseTerminal(后续 end 迁移的真身),迟到的会话照样被杀。
	blocked2 := make(chan struct{})
	spawnEntered2 := make(chan struct{}, 1)
	s.terminal.startProcess = func(sessionID string, shell string, cwd string, rows int, cols int) (*agentTerminalSession, []io.Reader, error) {
		spawnEntered2 <- struct{}{}
		<-blocked2
		return h.spawner.start(sessionID, shell, cwd, rows, cols)
	}
	t.Cleanup(func() {
		select {
		case <-blocked2:
		default:
			close(blocked2)
		}
	})
	// 场景 1 的 End 已落 60s 冷却锚(时钟可注入,推进跳出)。
	s.assist.AdvanceClock(61 * time.Second)
	sid2, _ := s.assist.RequestAndSignal("")
	if _, err := s.assist.OnRemoteFrame(map[string]interface{}{
		"type": models.AgentEventAssistApproved, "session_id": sid2,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-spawnEntered2:
	case <-time.After(5 * time.Second):
		t.Fatal("second fake create must enter the blocked spawner")
	}
	if err := s.assist.End(AssistEndClient); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // 烧完缩短的放弃窗
	close(blocked2)
	if !assistWaitUntil(5*time.Second, func() bool { return s.terminal.get(sid2) != nil }) {
		t.Fatal("second delayed terminal must land")
	}
	s.assistCloseTerminal(sid2) // 已无在途 create:立即杀
	if !assistWaitUntil(5*time.Second, func() bool { return !h.fakeTerminalAlive(sid2) }) {
		t.Fatal("assistCloseTerminal must kill the delayed terminal once it has landed")
	}
}

// ——— Task 7:观看 WS 的 services 侧形态 ———

// TestAssistViewFanErrorAutoUnregisters 钉观看 WS 的扇出错误自动注销
// (handler 观看连接的 fanFn 写失败=连接死/超时,P1 策略:任何 fan 错误→
// 注销该 viewID,观看端重连=新注册;注册表不积累死 sink)。错误路径与
// panic 路径都走注销(fanOneView 两者都按失败计)。
func TestAssistViewFanErrorAutoUnregisters(t *testing.T) {
	h := newTestAssistManagerWithFakeTerminal(t)
	s := h.svc
	sid, _ := s.assist.RequestAndSignal("")
	if _, err := s.assist.OnRemoteFrame(map[string]interface{}{
		"type": models.AgentEventAssistApproved, "session_id": sid,
	}); err != nil {
		t.Fatal(err)
	}
	if !assistWaitUntil(5*time.Second, func() bool { return h.fakeTerminalCWD(sid) != "" }) {
		t.Fatal("terminal must be live")
	}
	healthy := &assistViewProbe{}
	s.assist.RegisterViewSink(sid, "v-dead", func(frame map[string]interface{}) error {
		return errors.New("write: broken pipe")
	})
	s.assist.RegisterViewSink(sid, "v-live", healthy.fan)
	// 触发一次输出:死 sink 报错 → 自动注销。
	h.fakeTerminalEmit(sid, "one")
	if !assistWaitUntil(5*time.Second, func() bool { return healthy.Got("one") }) {
		t.Fatal("healthy sink must receive the frame")
	}
	// 注销是 fanOneView 返回错误后的下一步,中间隔着 recover/日志落盘——
	// 有界等待而非裸断言(裸查会在窗口期假红,-race 下偶发)。
	if !assistWaitUntil(5*time.Second, func() bool {
		s.assist.viewsMu.Lock()
		defer s.assist.viewsMu.Unlock()
		_, deadStill := s.assist.views[sid]["v-dead"]
		return !deadStill
	}) {
		t.Fatal("erroring sink must be auto-unregistered after a fan failure")
	}
	s.assist.viewsMu.Lock()
	_, liveStill := s.assist.views[sid]["v-live"]
	s.assist.viewsMu.Unlock()
	if !liveStill {
		t.Fatal("healthy sibling must stay registered")
	}
	// 注销后的后续输出只到健康 sink(死 sink 不再被调用,也无错误堆积)。
	h.fakeTerminalEmit(sid, "two")
	if !assistWaitUntil(5*time.Second, func() bool { return healthy.Got("two") }) {
		t.Fatal("healthy sink must keep receiving after sibling auto-unregister")
	}
}

// TestAssistReplayThenLiveOrderingUnderOneMutex 钉观看 WS 的回放续屏顺序
// 契约(Task 7 拍板形态+实现期锁序修正,handler 的「注册 sink→回放→live」
// 以 services 同包复刻):序列化由 sendReplay 的 outputGate 承担——回放块
// 持 gate(快照+逐帧),live 帧的扇出在 copy 循环里同样于 gate 内(push+
// writeJSON 同 gate),全局锁序恒 outputGate → view.mu,无环。测试形态:
// fanFn 与回放 writeJSON 共用同一把 per-view writeMu 的收集出口(handler
// 同构);回放调用**不持** view.mu(持了会与 copy 循环 gate→fan→view.mu
// 成环——死锁);并发 live 扇出被 gate 挡住,顺序恒 replay* → live。
func TestAssistReplayThenLiveOrderingUnderOneMutex(t *testing.T) {
	h := newTestAssistManagerWithFakeTerminal(t)
	s := h.svc
	sid, _ := s.assist.RequestAndSignal("")
	if _, err := s.assist.OnRemoteFrame(map[string]interface{}{
		"type": models.AgentEventAssistApproved, "session_id": sid,
	}); err != nil {
		t.Fatal(err)
	}
	if !assistWaitUntil(5*time.Second, func() bool { return h.fakeTerminalCWD(sid) != "" }) {
		t.Fatal("terminal must be live")
	}
	// 预灌 ring(admin 早期输出,观看端中途加入前的存量)。复制循环把
	// push→上行→扇出整段收在 outputGate 临界区里——若不设屏障就注册
	// sink,注册可能落进该段中间(窗口 push→扇出,等 ring 或等上行都只
	// 是收窄它:上行先于扇出,见到上行仅证明扇出待发),随后登记的 sink
	// 会把同一块先收成活帧(-race 下乱序假红)。收口:上行见到后对
	// outputGate 做一次 Lock/Unlock 探针——互斥量保证此刻临界区(含扇出)
	// 已完整走完,注册才发生,窗口归零。
	h.fakeTerminalEmit(sid, "replay-bytes-1")
	if !assistWaitUntil(5*time.Second, func() bool { return h.upGot("replay-bytes-1") }) {
		t.Fatal("uplink must carry the prefilled bytes")
	}
	if sess := s.terminal.get(sid); sess != nil {
		sess.outputGate.Lock()
		sess.outputGate.Unlock()
	}

	var writeMu sync.Mutex
	probe := &assistViewProbe{}
	viewWrite := func(v interface{}) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		if frame, ok := v.(map[string]interface{}); ok {
			probe.mu.Lock()
			probe.frames = append(probe.frames, frame)
			probe.mu.Unlock()
		}
		return nil
	}
	fan := func(frame map[string]interface{}) error { return viewWrite(frame) }
	s.assist.RegisterViewSink(sid, "view-1", fan)

	// 并发 live 扇出:后台 goroutine 持续 emit;回放与 live 的互斥由
	// outputGate 保证(copy 循环 push+fan 同 gate),不靠 view.mu。
	go func() {
		h.fakeTerminalEmit(sid, "live-after")
	}()
	// 回放:不持 view.mu(handler 纪律,见函数头注释)。
	s.AssistTerminalReplayTo(sid, viewWrite)

	// 等 live 帧落地(emit 是异步复制,assistWaitUntil 消竞态)。
	if !assistWaitUntil(5*time.Second, func() bool { return probe.Got("live-after") }) {
		t.Fatal("live frame must arrive after replay")
	}

	// 顺序断言:全部 replay 帧必须先于任何含 live 输出的 terminal.output 帧。
	probe.mu.Lock()
	defer probe.mu.Unlock()
	seenLive := false
	for _, f := range probe.frames {
		typ, _ := f["type"].(string)
		if typ == models.AgentEventTerminalReplay {
			if seenLive {
				t.Fatalf("replay frame after live frame: 乱序(回放必须先于 live): %+v", probe.frames)
			}
			continue
		}
		if typ == models.AgentEventTerminalOutput {
			seenLive = true
		}
	}
	if !seenLive {
		t.Fatalf("必须见到 live 帧后于回放: %+v", probe.frames)
	}
}

// spawnerRingHas 探协助 PTY 的 ring 是否已含给定字节(fakeTerminalEmit 的
// 写入到 ring 落地是异步复制,assistWaitUntil 配合消竞态)。
func (h *assistHarness) spawnerRingHas(sid string, substr string) bool {
	s := h.svc.terminal.get(sid)
	if s == nil || s.ring == nil {
		return false
	}
	return strings.Contains(string(s.ring.snapshot()), substr)
}

// TestAssistTerminalReplayToTombstone 钉 AssistTerminalReplayTo 的
// tombstone 分支:会话已退(ring 落 history)后回放按 status=exited 形态
// 发出;活跃会话分atcher(live ring+gate)与 requested 无会话 no-op 分支
// 由 TestAssistReplayThenLiveOrderingUnderOneMutex 与 handler 等待模式
// 用例各自钉住。
func TestAssistTerminalReplayToTombstone(t *testing.T) {
	h := newTestAssistManagerWithFakeTerminal(t)
	s := h.svc
	sid, _ := s.assist.RequestAndSignal("")
	if _, err := s.assist.OnRemoteFrame(map[string]interface{}{
		"type": models.AgentEventAssistApproved, "session_id": sid,
	}); err != nil {
		t.Fatal(err)
	}
	if !assistWaitUntil(5*time.Second, func() bool { return h.fakeTerminalCWD(sid) != "" }) {
		t.Fatal("terminal must be live")
	}
	h.fakeTerminalEmit(sid, "tomb-replay")
	if !assistWaitUntil(5*time.Second, func() bool { return h.spawnerRingHas(sid, "tomb-replay") }) {
		t.Fatal("ring must hold the emitted bytes")
	}
	// 终态:杀 PTY → tombstone 落 history(exit 帧走复合 writer 上行)。
	if err := s.assist.End(AssistEndAdmin); err != nil {
		t.Fatal(err)
	}
	if !assistWaitUntil(5*time.Second, func() bool { return !h.fakeTerminalAlive(sid) }) {
		t.Fatal("end must close the assist terminal")
	}
	// 假身的 kill 不归返 waiter(生产 kill 后真进程退出、waiter 归返):
	// 显式放行 blockExit 模拟进程死亡,waitTerminal 才把会话归档成
	// tombstone(quiesce 的 Cleanup 走同一幂等出口,不二关)。
	h.spawner.releaseExit()
	// history 读必须持 m.mu(waitTerminal 的归档写在锁内,quiesce 同款
	// 纪律):裸读=与 recordTerminalHistoryLocked 竞态,-race 必红。
	if !assistWaitUntil(5*time.Second, func() bool {
		s.terminal.mu.Lock()
		defer s.terminal.mu.Unlock()
		return s.terminal.history[sid] != nil
	}) {
		t.Fatal("tombstone must land in history after end")
	}
	// 回放:按 exited 形态发 terminal.replay 帧。
	var mu sync.Mutex
	var got []map[string]interface{}
	s.AssistTerminalReplayTo(sid, func(v interface{}) error {
		if frame, ok := v.(map[string]interface{}); ok {
			mu.Lock()
			got = append(got, frame)
			mu.Unlock()
		}
		return nil
	})
	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("tombstone replay must emit frames")
	}
	last := got[len(got)-1]
	if last["type"] != models.AgentEventTerminalReplay {
		t.Fatalf("回放帧 type = %v, want terminal.replay", last["type"])
	}
	if last["status"] != terminalReplayStatusExited {
		t.Fatalf("回放 status = %v, want %s", last["status"], terminalReplayStatusExited)
	}
	if final, _ := last["final"].(bool); !final {
		t.Fatalf("末帧必须 final=true: %v", last)
	}
	if data, _ := last["data"].(string); !strings.Contains(data, "tomb-replay") {
		t.Fatalf("回放 data 应含存量字节: %q", data)
	}
}
