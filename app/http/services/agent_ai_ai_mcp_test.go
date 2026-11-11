package services

// agent-as-MCP 端点后端测试(spec §9 端点后端部分)。三操作 AgentMCPStart /
// AgentMCPStatus / AgentMCPApprove 的契约、门控、会话归属与 writer 生命周期。
//
// 异步铁律:AgentMCPStart 受理后 run 在 goroutine 跑。任何断言 run 侧效果的
// 用例都必须先等 run 收尾(waitAIMCPRunIdle),否则与 t.Setenv 的清理竞态,
// -race 下必炸。

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

// setAIMCPGateForTest 换工具面总闸 seam(沿 agentCoreMemoryEnabledFn 先例),
// 测试结束还原。
func setAIMCPGateForTest(t *testing.T, on bool) {
	t.Helper()
	prev := agentCoreAIMCPEnabledFn
	agentCoreAIMCPEnabledFn = func() bool { return on }
	t.Cleanup(func() { agentCoreAIMCPEnabledFn = prev })
}

// newAIMCPTestService 造一个"设备在线 + AI 控制开 + 门控开"的 service。
// 顺序敏感:setTestAgentHome 必须先于 newAgentAIManager(journal 根在构造时解析)。
func newAIMCPTestService(t *testing.T) *AgentService {
	t.Helper()
	setTestAgentHome(t)
	setAIMCPGateForTest(t, true)
	m := newAgentAIManager()
	svc := &AgentService{ai: m}
	m.service = svc
	svc.state.Enabled = true
	svc.state.Registered = true
	t.Cleanup(func() { m.closeAll() })
	return svc
}

// registerAIMCPTestSession 直接注册一条工具面会话(持锁版)。
func registerAIMCPTestSession(t *testing.T, m *agentAIManager, session *agentAISession) {
	t.Helper()
	registerTestAISession(t, m, session)
}

// waitAIMCPRunIdle 等 run 收尾:session.cancel 由 runUserMessage 同步置位、
// 由 clearRunning 在 run 末尾清零——这就是"受理后那位 goroutine 已经结束"
// 的观测点。
func waitAIMCPRunIdle(t *testing.T, m *agentAIManager, sid string) {
	t.Helper()
	waitUntilFor(t, 10*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		s := m.sessions[sid]
		return s == nil || s.cancel == nil
	})
}

// createdByOf 读会话的 createdBy 标记。
func createdByOf(t *testing.T, m *agentAIManager, sid string) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[sid]
	if s == nil {
		return "<absent>"
	}
	return s.createdBy
}

// ---------------------------------------------------------------------------
// 1. 新建:返回 session_id + createdBy 打标实据
// ---------------------------------------------------------------------------

func TestAgentMCPStartCreatesSessionAndTagsCreatedBy(t *testing.T) {
	svc := newAIMCPTestService(t)
	m := svc.ai
	_, srv := newAgentCoreUpstream(t, agentCoreTextTurn("你好", "，世界"))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)

	proj := t.TempDir()
	payload, code, err := svc.AgentMCPStart(context.Background(), proj, "hello from mcp", "agentcore", "")
	if err != nil || code != http.StatusOK {
		t.Fatalf("AgentMCPStart = (%v, %d, %v), want 200/nil", payload, code, err)
	}
	sid, _ := payload["session_id"].(string)
	if strings.TrimSpace(sid) == "" {
		t.Fatalf("session_id 为空: %v", payload)
	}
	if accepted, _ := payload["accepted"].(bool); !accepted {
		t.Fatalf("accepted = %v, want true", payload["accepted"])
	}
	// 打标实据:会话必须真的进了 sessions 且带 ai_mcp 标记。
	if got := createdByOf(t, m, sid); got != agentAIMCPCreatedBy {
		t.Fatalf("createdBy = %q, want %q", got, agentAIMCPCreatedBy)
	}
	waitAIMCPRunIdle(t, m, sid)
}

// ---------------------------------------------------------------------------
// 2. 续聊:本面放行,非本面 403
// ---------------------------------------------------------------------------

func TestAgentMCPStartResumesOwnSessionRejectsForeign(t *testing.T) {
	svc := newAIMCPTestService(t)
	m := svc.ai
	_, srv := newAgentCoreUpstream(t, agentCoreTextTurn("再", "一回合"))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)

	proj := t.TempDir()
	own := "mcp-own-session"
	registerAIMCPTestSession(t, m, &agentAISession{
		id: own, mode: "vibe", projectPath: proj, provider: "agentcore",
		createdBy: agentAIMCPCreatedBy,
	})
	payload, code, err := svc.AgentMCPStart(context.Background(), proj, "续聊一句", "agentcore", own)
	if err != nil || code != http.StatusOK {
		t.Fatalf("本面续聊 = (%v, %d, %v), want 200/nil", payload, code, err)
	}
	if got, _ := payload["session_id"].(string); got != own {
		t.Fatalf("续聊返回 session_id = %q, want %q", got, own)
	}
	waitAIMCPRunIdle(t, m, own)

	// 非本面(手机/桌面来源:createdBy 为空)必须拒。
	foreign := "phone-session"
	registerAIMCPTestSession(t, m, &agentAISession{
		id: foreign, mode: "vibe", projectPath: proj, provider: "agentcore",
	})
	payload, code, err = svc.AgentMCPStart(context.Background(), proj, "越权续聊", "agentcore", foreign)
	if code != http.StatusForbidden {
		t.Fatalf("非本面续聊状态码 = %d, want 403(payload=%v err=%v)", code, payload, err)
	}
	if err == nil || !strings.Contains(err.Error(), "会话不属于 AI 工具面") {
		t.Fatalf("非本面续聊 error = %v, want 含「会话不属于 AI 工具面」", err)
	}
	if payload != nil {
		t.Fatalf("非本面续聊载荷 = %v, want nil", payload)
	}
}

// ---------------------------------------------------------------------------
// 3. 必填校验
// ---------------------------------------------------------------------------

func TestAgentMCPStartValidatesRequiredFields(t *testing.T) {
	svc := newAIMCPTestService(t)
	proj := t.TempDir()

	for _, tc := range []struct{ name, content, projectPath string }{
		{"空 content", "   ", proj},
		{"空 project_path", "hi", "  "},
	} {
		payload, code, err := svc.AgentMCPStart(context.Background(), tc.projectPath, tc.content, "agentcore", "")
		if code != http.StatusBadRequest {
			t.Fatalf("%s: 状态码 = %d, want 400(payload=%v err=%v)", tc.name, code, payload, err)
		}
		if err == nil || err.Error() != "content 与 project_path 必填" {
			t.Fatalf("%s: error = %v, want「content 与 project_path 必填」", tc.name, err)
		}
	}
}

// ---------------------------------------------------------------------------
// 4. busy 会话 → 409(message() 的 busy 守卫文案透传)
// ---------------------------------------------------------------------------

func TestAgentMCPStartRejectsBusySessionWith409(t *testing.T) {
	svc := newAIMCPTestService(t)
	m := svc.ai
	proj := t.TempDir()
	busy := "mcp-busy-session"
	m.mu.Lock()
	m.sessions[busy] = &agentAISession{
		id: busy, mode: "vibe", projectPath: proj, provider: "agentcore",
		createdBy: agentAIMCPCreatedBy,
		cancel:    func() {}, // 预置运行中状态
	}
	m.mu.Unlock()

	payload, code, err := svc.AgentMCPStart(context.Background(), proj, "撞运行中会话", "agentcore", busy)
	if code != http.StatusConflict {
		t.Fatalf("busy 状态码 = %d, want 409(payload=%v err=%v)", code, payload, err)
	}
	if err == nil || !strings.Contains(err.Error(), "ai session is already running") {
		t.Fatalf("busy error = %v, want 含「ai session is already running」", err)
	}
	if payload != nil {
		t.Fatalf("busy 载荷 = %v, want nil", payload)
	}
}

// ---------------------------------------------------------------------------
// 5. status 三分支
// ---------------------------------------------------------------------------

func TestAgentMCPStatusReadsOwnSessionOnly(t *testing.T) {
	svc := newAIMCPTestService(t)
	m := svc.ai
	proj := t.TempDir()
	longText := strings.Repeat("答", agentAIMCPLastAssistantCap+50)

	t.Run("idle+摘要", func(t *testing.T) {
		sid := "mcp-status-idle"
		registerAIMCPTestSession(t, m, &agentAISession{
			id: sid, mode: "vibe", projectPath: proj, provider: "agentcore",
			createdBy: agentAIMCPCreatedBy,
			history: []agentAIMessage{
				{Role: "user", Content: "问题"},
				{Role: "assistant", Content: longText},
			},
		})
		payload, code, err := svc.AgentMCPStatus(context.Background(), sid)
		if err != nil || code != http.StatusOK {
			t.Fatalf("status = (%v, %d, %v), want 200/nil", payload, code, err)
		}
		if got, _ := payload["state"].(string); got != "idle" {
			t.Fatalf("state = %q, want idle", got)
		}
		pendings, ok := payload["pending_approvals"].([]map[string]interface{})
		if !ok || pendings == nil {
			t.Fatalf("pending_approvals = %#v(%T), want 空数组(非 nil)", payload["pending_approvals"], payload["pending_approvals"])
		}
		if len(pendings) != 0 {
			t.Fatalf("pending_approvals 长度 = %d, want 0", len(pendings))
		}
		last, _ := payload["last_assistant"].(string)
		if got := len([]rune(last)); got != agentAIMCPLastAssistantCap {
			t.Fatalf("last_assistant rune 长度 = %d, want %d(按 rune 截断,不劈中文)", got, agentAIMCPLastAssistantCap)
		}
		if !strings.HasPrefix(longText, last) {
			t.Fatal("last_assistant 不是原文前缀(截断位置错了)")
		}
	})

	t.Run("awaiting_approval", func(t *testing.T) {
		sid := "mcp-status-await"
		aid := "ap-1"
		m.mu.Lock()
		m.sessions[sid] = &agentAISession{
			id: sid, mode: "vibe", projectPath: proj, provider: "agentcore",
			createdBy: agentAIMCPCreatedBy,
			cancel:    func() {}, // running
		}
		m.approvals[agentAIApprovalMapKey(sid, aid)] = &agentAIApprovalWaiter{
			sessionID: sid,
			runSeq:    1,
			request:   agentAIApprovalRequest{ID: aid, Kind: "bash", Provider: "agentcore", Command: "ls"},
		}
		m.mu.Unlock()
		t.Cleanup(func() {
			m.mu.Lock()
			delete(m.approvals, agentAIApprovalMapKey(sid, aid))
			m.mu.Unlock()
		})

		payload, code, err := svc.AgentMCPStatus(context.Background(), sid)
		if err != nil || code != http.StatusOK {
			t.Fatalf("status = (%v, %d, %v), want 200/nil", payload, code, err)
		}
		if got, _ := payload["state"].(string); got != "awaiting_approval" {
			t.Fatalf("state = %q, want awaiting_approval(running + 有 pending)", got)
		}
		pendings, _ := payload["pending_approvals"].([]map[string]interface{})
		if len(pendings) != 1 {
			t.Fatalf("pending_approvals 长度 = %d, want 1", len(pendings))
		}
		if got, _ := pendings[0]["approval_id"].(string); got != aid {
			t.Fatalf("pending approval_id = %q, want %q", got, aid)
		}
	})

	t.Run("running 零 pending", func(t *testing.T) {
		// 独立钉住 idle→running 那一跳:idle 与 awaiting_approval 各有断言,
		// 但中间的 running 只被 awaiting_approval 顺带走过。若有人把置 running
		// 的条件改成「有 pending 才算 running」,只有本子测会红。
		sid := "mcp-status-running"
		m.mu.Lock()
		m.sessions[sid] = &agentAISession{
			id: sid, mode: "vibe", projectPath: proj, provider: "agentcore",
			createdBy: agentAIMCPCreatedBy,
			cancel:    func() {}, // running
		}
		m.mu.Unlock()

		payload, code, err := svc.AgentMCPStatus(context.Background(), sid)
		if err != nil || code != http.StatusOK {
			t.Fatalf("status = (%v, %d, %v), want 200/nil", payload, code, err)
		}
		if got, _ := payload["state"].(string); got != "running" {
			t.Fatalf("state = %q, want running(cancel!=nil 且零 pending)", got)
		}
		pendings, ok := payload["pending_approvals"].([]map[string]interface{})
		if !ok || len(pendings) != 0 {
			t.Fatalf("pending_approvals = %#v, want 空数组(本子测前提就是零 pending)", payload["pending_approvals"])
		}
	})

	t.Run("非本面403", func(t *testing.T) {
		sid := "phone-status"
		registerAIMCPTestSession(t, m, &agentAISession{
			id: sid, mode: "vibe", projectPath: proj, provider: "agentcore",
		})
		payload, code, err := svc.AgentMCPStatus(context.Background(), sid)
		if code != http.StatusForbidden {
			t.Fatalf("非本面 status 状态码 = %d, want 403(payload=%v err=%v)", code, payload, err)
		}
		if err == nil || !strings.Contains(err.Error(), "会话不属于 AI 工具面") {
			t.Fatalf("非本面 status error = %v, want 含「会话不属于 AI 工具面」", err)
		}
	})

	t.Run("不存在400", func(t *testing.T) {
		payload, code, err := svc.AgentMCPStatus(context.Background(), "no-such-session")
		if code != http.StatusBadRequest {
			t.Fatalf("不存在 status 状态码 = %d, want 400(payload=%v err=%v)", code, payload, err)
		}
		if err == nil || !strings.Contains(err.Error(), "会话不存在") {
			t.Fatalf("不存在 status error = %v, want 含「会话不存在」", err)
		}
	})
}

// ---------------------------------------------------------------------------
// 6. approve 走 m.approval 既有受理链
// ---------------------------------------------------------------------------

func TestAgentMCPApproveSubmitsDecisionThroughApprovalChain(t *testing.T) {
	svc := newAIMCPTestService(t)
	m := svc.ai
	proj := t.TempDir()
	sid := "mcp-approve-session"
	aid := "ap-approve-1"
	respond := make(chan agentAIApprovalResponse, 1)
	m.mu.Lock()
	m.sessions[sid] = &agentAISession{
		id: sid, mode: "vibe", projectPath: proj, provider: "agentcore",
		createdBy: agentAIMCPCreatedBy,
		runSeq:    1,
	}
	m.approvals[agentAIApprovalMapKey(sid, aid)] = &agentAIApprovalWaiter{
		sessionID: sid, runSeq: 1,
		request: agentAIApprovalRequest{ID: aid, Provider: "agentcore", Kind: "bash", respond: respond},
	}
	m.mu.Unlock()

	payload, code, err := svc.AgentMCPApprove(context.Background(), sid, aid, "accept")
	if err != nil || code != http.StatusOK {
		t.Fatalf("approve = (%v, %d, %v), want 200/nil", payload, code, err)
	}
	if applied, _ := payload["applied"].(bool); !applied {
		t.Fatalf("applied = %v, want true(命中活 waiter)", payload["applied"])
	}
	if ok, _ := payload["ok"].(bool); !ok {
		t.Fatalf("ok = %v, want true", payload["ok"])
	}
	select {
	case got := <-respond:
		if got.Decision != models.AgentAIApprovalDecisionAccept {
			t.Fatalf("waiter 收到决策 = %q, want %q", got.Decision, models.AgentAIApprovalDecisionAccept)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("决议没有送达 waiter(未走 m.approval 受理链)")
	}

	t.Run("非本面403", func(t *testing.T) {
		foreign := "phone-approve"
		registerAIMCPTestSession(t, m, &agentAISession{
			id: foreign, mode: "vibe", projectPath: proj, provider: "agentcore",
		})
		payload, code, err := svc.AgentMCPApprove(context.Background(), foreign, "ap-x", "accept")
		if code != http.StatusForbidden {
			t.Fatalf("非本面 approve 状态码 = %d, want 403(payload=%v err=%v)", code, payload, err)
		}
		if err == nil || !strings.Contains(err.Error(), "会话不属于 AI 工具面") {
			t.Fatalf("非本面 approve error = %v, want 含「会话不属于 AI 工具面」", err)
		}
	})

	t.Run("非法decision=受理链原文", func(t *testing.T) {
		payload, code, err := svc.AgentMCPApprove(context.Background(), sid, aid, "maybe")
		if code != http.StatusBadRequest {
			t.Fatalf("非法 decision 状态码 = %d, want 400(payload=%v err=%v)", code, payload, err)
		}
		// 文案必须是受理链原文(不是端点自己编的),外部 AI 据此能自查。
		if err == nil || err.Error() != "unsupported approval decision: maybe" {
			t.Fatalf("非法 decision error = %v, want「unsupported approval decision: maybe」", err)
		}
	})
}

// ---------------------------------------------------------------------------
// 6b. approve 无 waiter:受理链 ack no_match,端点仍成功但必须如实报 applied=false
// ---------------------------------------------------------------------------

// spec §4.3:no_match / duplicate 也 ACK 成功(让服务端停止重投),但 applied
// 必须为 false——这是端点结果对调用方的全部区分价值。若不钉住,把 applied
// 写死成 true 也能让本文件其余用例全绿。
func TestAgentMCPApproveReportsAppliedFalseWhenNoWaiter(t *testing.T) {
	svc := newAIMCPTestService(t)
	m := svc.ai
	proj := t.TempDir()
	sid := "mcp-approve-nowaiter"
	registerAIMCPTestSession(t, m, &agentAISession{
		id: sid, mode: "vibe", projectPath: proj, provider: "agentcore",
		createdBy: agentAIMCPCreatedBy,
		runSeq:    1,
	})

	// m.approvals 里没有该会话的任何 waiter -> 受理链走 no_match 分支。
	payload, code, err := svc.AgentMCPApprove(context.Background(), sid, "no-such-approval-id", "accept")
	if err != nil || code != http.StatusOK {
		t.Fatalf("approve = (%v, %d, %v), want 200/nil(no_match 也是成功受理)", payload, code, err)
	}
	if ok, _ := payload["ok"].(bool); !ok {
		t.Fatalf("ok = %v, want true", payload["ok"])
	}
	if applied, isBool := payload["applied"].(bool); !isBool || applied {
		t.Fatalf("applied = %v(%T), want 精确 false(无活 waiter 时决策未生效)", payload["applied"], payload["applied"])
	}
}

// ---------------------------------------------------------------------------
// 6b. session_id 必填:status / approve 的早返分支
// ---------------------------------------------------------------------------

// status/approve 的 `sid == ""` 早返今天没有任何测试兜底——删掉它整套仍绿
// (终审变异 #4 实测)。这条分支的实际价值:空 sid 会一路走到
// agentAIMCPContractCheck → "会话不存在: "(注意冒号后是空的),把「你没传参数」
// 说成「这个会话不存在」,调用方会拿一个空 id 去反复排查。
func TestAgentMCPStatusRejectsEmptySessionID(t *testing.T) {
	svc := newAIMCPTestService(t)
	for _, sid := range []string{"", "   "} {
		payload, code, err := svc.AgentMCPStatus(context.Background(), sid)
		if code != http.StatusBadRequest {
			t.Fatalf("status(%q) 状态码 = %d, want 400(payload=%v err=%v)", sid, code, payload, err)
		}
		if err == nil || err.Error() != "session_id 必填" {
			t.Fatalf("status(%q) error = %v, want「session_id 必填」(不能被当成「会话不存在」)", sid, err)
		}
	}
}

func TestAgentMCPApproveRejectsEmptySessionID(t *testing.T) {
	svc := newAIMCPTestService(t)
	for _, sid := range []string{"", "   "} {
		payload, code, err := svc.AgentMCPApprove(context.Background(), sid, "ap-1", "accept")
		if code != http.StatusBadRequest {
			t.Fatalf("approve(%q) 状态码 = %d, want 400(payload=%v err=%v)", sid, code, payload, err)
		}
		if err == nil || err.Error() != "session_id 必填" {
			t.Fatalf("approve(%q) error = %v, want「session_id 必填」", sid, err)
		}
	}
}

// ---------------------------------------------------------------------------
// 7. 门控关:三操作各 503
// ---------------------------------------------------------------------------

func TestAgentMCPOperationsRejectedWhenGateOff(t *testing.T) {
	svc := newAIMCPTestService(t)
	setAIMCPGateForTest(t, false) // 后注册的 cleanup 先跑,还原顺序由 t.Cleanup LIFO 保证
	proj := t.TempDir()

	checks := []struct {
		name string
		call func() (map[string]interface{}, int, error)
	}{
		{"start", func() (map[string]interface{}, int, error) {
			return svc.AgentMCPStart(context.Background(), proj, "hi", "agentcore", "")
		}},
		{"status", func() (map[string]interface{}, int, error) {
			return svc.AgentMCPStatus(context.Background(), "any")
		}},
		{"approve", func() (map[string]interface{}, int, error) {
			return svc.AgentMCPApprove(context.Background(), "any", "ap", "accept")
		}},
	}
	for _, tc := range checks {
		payload, code, err := tc.call()
		if code != http.StatusServiceUnavailable {
			t.Fatalf("%s 门控关状态码 = %d, want 503(payload=%v err=%v)", tc.name, code, payload, err)
		}
		if err == nil || err.Error() != "agent-as-MCP 工具面已关闭" {
			t.Fatalf("%s 门控关 error = %v, want「agent-as-MCP 工具面已关闭」", tc.name, err)
		}
	}
}

// ---------------------------------------------------------------------------
// 8. 设备 AI 控制门关:三操作各 403(与手机面 parity)
// ---------------------------------------------------------------------------

func TestAgentMCPOperationsRejectedWhenAIControlDisabled(t *testing.T) {
	svc := newAIMCPTestService(t)
	svc.mu.Lock()
	svc.state.Enabled = false
	svc.mu.Unlock()
	proj := t.TempDir()

	checks := []struct {
		name string
		call func() (map[string]interface{}, int, error)
	}{
		{"start", func() (map[string]interface{}, int, error) {
			return svc.AgentMCPStart(context.Background(), proj, "hi", "agentcore", "")
		}},
		{"status", func() (map[string]interface{}, int, error) {
			return svc.AgentMCPStatus(context.Background(), "any")
		}},
		{"approve", func() (map[string]interface{}, int, error) {
			return svc.AgentMCPApprove(context.Background(), "any", "ap", "accept")
		}},
	}
	for _, tc := range checks {
		payload, code, err := tc.call()
		if code != http.StatusForbidden {
			t.Fatalf("%s 设备门关状态码 = %d, want 403(payload=%v err=%v)", tc.name, code, payload, err)
		}
		if err == nil || err.Error() != "AI control is disabled for this device" {
			t.Fatalf("%s 设备门关 error = %v, want「AI control is disabled for this device」", tc.name, err)
		}
	}
}

// ---------------------------------------------------------------------------
// 9. writer 生命周期:响应返回后置惰,run 帧不再进缓冲
// ---------------------------------------------------------------------------

func TestAgentMCPCaptureWriterGoesInertAfterResponse(t *testing.T) {
	// 先验纯单元语义:置惰后 write 无条件丢弃。
	cw := &agentAIMCPCaptureWriter{}
	if err := cw.write(map[string]interface{}{"type": "probe"}); err != nil {
		t.Fatalf("write 返回 %v, want nil", err)
	}
	if got := len(cw.snapshot()); got != 1 {
		t.Fatalf("置惰前帧数 = %d, want 1", got)
	}
	cw.inertize()
	if err := cw.write(map[string]interface{}{"type": "probe2"}); err != nil {
		t.Fatalf("置惰后 write 返回 %v, want nil", err)
	}
	if got := len(cw.snapshot()); got != 1 {
		t.Fatalf("置惰后帧数 = %d, want 1(不再增长)", got)
	}
	// 非 map 帧忽略(不 panic、不计数)。
	if err := cw.write(nil); err != nil {
		t.Fatalf("nil 帧 write 返回 %v, want nil", err)
	}

	// 再验端点真身:替换 writer 工厂拿到端点内部那个实例。
	svc := newAIMCPTestService(t)
	m := svc.ai
	_, srv := newAgentCoreUpstream(t, agentCoreTextTurn("写", "帧"))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)

	var captured *agentAIMCPCaptureWriter
	prevFactory := agentAIMCPNewCaptureWriter
	agentAIMCPNewCaptureWriter = func() *agentAIMCPCaptureWriter {
		captured = &agentAIMCPCaptureWriter{}
		return captured
	}
	t.Cleanup(func() { agentAIMCPNewCaptureWriter = prevFactory })

	payload, code, err := svc.AgentMCPStart(context.Background(), t.TempDir(), "让他跑一回合", "agentcore", "")
	if err != nil || code != http.StatusOK {
		t.Fatalf("AgentMCPStart = (%v, %d, %v), want 200/nil", payload, code, err)
	}
	if captured == nil {
		t.Fatal("端点未走 agentAIMCPNewCaptureWriter 工厂")
	}
	before := len(captured.snapshot())
	// 前置证明:受理期间这个 writer 真被用过(否则"不增"可能是恒真断言)。
	if before == 0 {
		t.Fatal("受理期间捕获 writer 一帧未收到(工厂替换没接上受理链,后续断言无从成立)")
	}
	// 之后手工写一帧:writer 已被端点置惰,缓冲必须不增。
	_ = captured.write(map[string]interface{}{"type": models.AgentEventAIDelta, "delta": "late"})
	if after := len(captured.snapshot()); after != before {
		t.Fatalf("响应后写入使缓冲从 %d 增到 %d(未置惰 → run 帧会无限堆积)", before, after)
	}
	sid, _ := payload["session_id"].(string)
	waitAIMCPRunIdle(t, m, sid)
	// run 收尾后缓冲仍不增(emitter 全程持有该 writer)。
	if after := len(captured.snapshot()); after != before {
		t.Fatalf("run 收尾后缓冲从 %d 增到 %d, want 不增", before, after)
	}
}

// ---------------------------------------------------------------------------
// 10. 契约不补标:被拒的续聊不得改动标记;新建受理后必须补标
// ---------------------------------------------------------------------------

func TestAgentMCPStartDoesNotTagForeignOrDriftedSessions(t *testing.T) {
	svc := newAIMCPTestService(t)
	m := svc.ai
	proj := t.TempDir()

	// 非本面会话(createdBy 空)被拒后,标记必须保持空——端点绝不"顺手补标"
	// (否则一次越权尝试就能把手机会话升级成本面会话)。
	foreign := "phone-untouched"
	registerAIMCPTestSession(t, m, &agentAISession{
		id: foreign, mode: "vibe", projectPath: proj, provider: "agentcore",
	})
	if _, code, _ := svc.AgentMCPStart(context.Background(), proj, "越权", "agentcore", foreign); code != http.StatusForbidden {
		t.Fatalf("越权续聊状态码 = %d, want 403", code)
	}
	if got := createdByOf(t, m, foreign); got != "" {
		t.Fatalf("被拒后 createdBy = %q, want 空(契约降级不得补标)", got)
	}
	// 同理 status / approve 被拒也不得改动标记。
	if _, code, _ := svc.AgentMCPStatus(context.Background(), foreign); code != http.StatusForbidden {
		t.Fatalf("越权 status 状态码 = %d, want 403", code)
	}
	if _, code, _ := svc.AgentMCPApprove(context.Background(), foreign, "ap", "accept"); code != http.StatusForbidden {
		t.Fatalf("越权 approve 状态码 = %d, want 403", code)
	}
	if got := createdByOf(t, m, foreign); got != "" {
		t.Fatalf("越权读/批后 createdBy = %q, want 空", got)
	}

	// 新建路径受理后必须补标(对照组:证明上面的"未补标"不是因为端点从不打标)。
	_, srv := newAgentCoreUpstream(t, agentCoreTextTurn("新", "会话"))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	payload, code, err := svc.AgentMCPStart(context.Background(), proj, "新建一句", "agentcore", "")
	if err != nil || code != http.StatusOK {
		t.Fatalf("新建 = (%v, %d, %v), want 200/nil", payload, code, err)
	}
	sid, _ := payload["session_id"].(string)
	if got := createdByOf(t, m, sid); got != agentAIMCPCreatedBy {
		t.Fatalf("新建后 createdBy = %q, want %q", got, agentAIMCPCreatedBy)
	}
	waitAIMCPRunIdle(t, m, sid)
}

// ---------------------------------------------------------------------------
// 11. ack 双写仍落 run socket(跨端同态回归)
// ---------------------------------------------------------------------------

func TestAgentMCPApproveAckDualWritesToRunSocket(t *testing.T) {
	svc := newAIMCPTestService(t)
	m := svc.ai
	proj := t.TempDir()
	sid := "mcp-ack-dual"
	aid := "ap-ack-dual"

	// 手插 socket(不经 registerAISocket:那条路会起 pump 把 out 排空,断言拿不到)。
	runSock := &agentAISocket{
		id:     "run-sock",
		role:   "local",
		out:    make(chan map[string]interface{}, 4),
		closed: make(chan struct{}),
		subs:   make(map[string]struct{}),
	}
	respond := make(chan agentAIApprovalResponse, 1)
	m.mu.Lock()
	m.aiSockets[runSock.id] = runSock
	m.sessions[sid] = &agentAISession{
		id: sid, mode: "vibe", projectPath: proj, provider: "agentcore",
		createdBy:    agentAIMCPCreatedBy,
		originSocket: runSock.id,
		runSeq:       1,
	}
	m.approvals[agentAIApprovalMapKey(sid, aid)] = &agentAIApprovalWaiter{
		sessionID: sid, runSeq: 1,
		request: agentAIApprovalRequest{ID: aid, Provider: "agentcore", Kind: "bash", respond: respond},
	}
	m.mu.Unlock()
	t.Cleanup(func() {
		m.mu.Lock()
		delete(m.aiSockets, runSock.id)
		m.mu.Unlock()
	})

	payload, code, err := svc.AgentMCPApprove(context.Background(), sid, aid, "accept")
	if err != nil || code != http.StatusOK {
		t.Fatalf("approve = (%v, %d, %v), want 200/nil", payload, code, err)
	}
	if applied, _ := payload["applied"].(bool); !applied {
		t.Fatalf("applied = %v, want true", payload["applied"])
	}
	// run socket 必须收到同一份 ack(端点侧 origin 为空 → 与 runSocket 不同 →
	// 走双写分支)。
	select {
	case frame := <-runSock.out:
		if got, _ := frame["type"].(string); got != models.AgentEventAIApprovalAck {
			t.Fatalf("run socket 首帧 type = %q, want %q", got, models.AgentEventAIApprovalAck)
		}
		if got, _ := frame["approval_id"].(string); got != aid {
			t.Fatalf("run socket ack approval_id = %q, want %q", got, aid)
		}
		if got, _ := frame["result"].(string); got != "applied" {
			t.Fatalf("run socket ack result = %q, want applied", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run socket 未收到 ack(ack 双写回归)")
	}
}

// ---------------------------------------------------------------------------
// 13. 续聊会话在「预检通过 → 受理」窗口内被逐出重建:run 照跑但契约降级
// ---------------------------------------------------------------------------

// TestAgentMCPStartDegradesWhenResumedSessionEvicted 覆盖 spec §5 的契约降级
// 分支:预检看到本面会话(createdBy=ai_mcp)通过后,会话在 m.message 受理前
// 被 LRU 逐出,受理阶段懒建出一个新的空标记会话。
//
// 确定性构造点:AgentMCPStart 的调用序列是「预检取锁/放锁 → 组帧 →
// agentAIMCPNewCaptureWriter() → m.message」,工厂调用正好落在预检放锁与受理
// 之间,且同 goroutine 同步执行——在工厂里同步删除会话即可精确复现,不需要
// 真的调 LRU(也无法调:逐出只挑 cancel==nil 的空闲会话,而本面会话一旦进
// message() 就同步置了 cancel)。
//
// 断言四点:①受理成功(200 + accepted=true,降级≠拒绝,run 照跑);
// ②createdBy 保持空(不补标);③后续 status 403;④文案「会话不属于 AI 工具面」。
func TestAgentMCPStartDegradesWhenResumedSessionEvicted(t *testing.T) {
	svc := newAIMCPTestService(t)
	m := svc.ai
	_, srv := newAgentCoreUpstream(t, agentCoreTextTurn("降", "级"))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)

	proj := t.TempDir()
	sid := "mcp-evicted-session"
	registerAIMCPTestSession(t, m, &agentAISession{
		id: sid, mode: "vibe", projectPath: proj, provider: "agentcore",
		createdBy:    agentAIMCPCreatedBy,
		lastActiveAt: time.Now().UTC(),
	})

	// 工厂在预检放锁与 m.message 之间被调用:此处同步删会话 = 逐出。
	// 端点只调一次工厂,但仍加一次性守卫(契约若变化,这个测试不会静默变成
	// 只删第一次的假阳性)。
	var once sync.Once
	prevFactory := agentAIMCPNewCaptureWriter
	agentAIMCPNewCaptureWriter = func() *agentAIMCPCaptureWriter {
		once.Do(func() {
			m.mu.Lock()
			delete(m.sessions, sid)
			m.mu.Unlock()
		})
		return prevFactory()
	}
	t.Cleanup(func() { agentAIMCPNewCaptureWriter = prevFactory })

	payload, code, err := svc.AgentMCPStart(context.Background(), proj, "逐出后继续", "agentcore", sid)
	// ① 降级不是拒绝:受理必须成功。
	if err != nil || code != http.StatusOK {
		t.Fatalf("逐出重建分支 = (%v, %d, %v), want 200/nil(降级应照跑)", payload, code, err)
	}
	if accepted, _ := payload["accepted"].(bool); !accepted {
		t.Fatalf("accepted = %v, want true", payload["accepted"])
	}
	if got, _ := payload["session_id"].(string); got != sid {
		t.Fatalf("session_id = %q, want %q", got, sid)
	}
	waitAIMCPRunIdle(t, m, sid)

	// ② 懒建出来的新会话 createdBy 必须保持空(端点绝不补标)。
	if got := createdByOf(t, m, sid); got != "" {
		t.Fatalf("逐出重建后 createdBy = %q, want 空(不补标)", got)
	}
	// ③④ 契约降级的可观测后果:后续 status 拒,文案同「会话不属于 AI 工具面」。
	statusPayload, statusCode, statusErr := svc.AgentMCPStatus(context.Background(), sid)
	if statusCode != http.StatusForbidden {
		t.Fatalf("降级后 status 状态码 = %d, want 403(payload=%v err=%v)", statusCode, statusPayload, statusErr)
	}
	if statusErr == nil || statusErr.Error() != "会话不属于 AI 工具面" {
		t.Fatalf("降级后 status error = %v, want「会话不属于 AI 工具面」", statusErr)
	}
}

// ---------------------------------------------------------------------------
// 12. AI service 未就绪(s.ai=nil)→ 503(设备门必须先放行,否则先撞 403)
// ---------------------------------------------------------------------------

func TestAgentMCPOperationsRejectedWhenAIServiceNotReady(t *testing.T) {
	setTestAgentHome(t)
	setAIMCPGateForTest(t, true)
	svc := &AgentService{} // ai == nil
	svc.state.Enabled = true
	svc.state.Registered = true
	proj := t.TempDir()

	checks := []struct {
		name string
		call func() (map[string]interface{}, int, error)
	}{
		{"start", func() (map[string]interface{}, int, error) {
			return svc.AgentMCPStart(context.Background(), proj, "hi", "agentcore", "")
		}},
		{"status", func() (map[string]interface{}, int, error) {
			return svc.AgentMCPStatus(context.Background(), "any")
		}},
		{"approve", func() (map[string]interface{}, int, error) {
			return svc.AgentMCPApprove(context.Background(), "any", "ap", "accept")
		}},
	}
	for _, tc := range checks {
		payload, code, err := tc.call()
		if code != http.StatusServiceUnavailable {
			t.Fatalf("%s 未就绪状态码 = %d, want 503(payload=%v err=%v)", tc.name, code, payload, err)
		}
		if err == nil || err.Error() != "AI service is not ready" {
			t.Fatalf("%s 未就绪 error = %v, want「AI service is not ready」", tc.name, err)
		}
	}
}
