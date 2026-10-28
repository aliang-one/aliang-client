package services

// 审批 ack 双写（2026-10-28 跨端会话统一，spec §4.2.2）。
//
// 背景：审批决策的 ack（ai.approval.ack，带 delivery_id）原先只写「送来决策
// 的那条 socket」。仪表盘抢答手机发起的回合时，ack 进浏览器 WS，PhoneServer
// 重投引擎收不到 ack，已生效决策被重投至多 10 次（agent 端靠 completedApprovals
// 回 duplicate 幂等兜底，但服务端状态滞留）。修法 = ack 双写：决策到达的
// socket（现有）+ run 发起的 socket（session.originSocket，Task 5 落）。

import (
	"context"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

// countAckFrames 排干通道并统计 ack 帧数。通道里可能混有错误/状态帧
// （approval() 的 no_match 路径还带 ai.status），只数 ack。
func countAckFrames(ch chan map[string]interface{}) int {
	n := 0
	for len(ch) > 0 {
		if p := <-ch; p["type"] == models.AgentEventAIApprovalAck {
			n++
		}
	}
	return n
}

// registerApprovalAckFixture 直接注册挂起 waiter + 会话（绕过 requestApproval
// 的 writer 检查，不拉真 CLI），并把会话的 run 发起 socket 指到 originSocketID。
// respond 通道先取局部变量：approval() 会把 waiter 从 map 删除后再喂通道，
// 断言方不得再经 m.approvals 裸读（-race 会抓）。
func registerApprovalAckFixture(t *testing.T, m *agentAIManager, sessionID, approvalID, originSocketID string) chan agentAIApprovalResponse {
	t.Helper()
	respond := make(chan agentAIApprovalResponse, 1)
	m.mu.Lock()
	m.approvals[agentAIApprovalMapKey(sessionID, approvalID)] = &agentAIApprovalWaiter{
		sessionID: sessionID,
		runSeq:    1,
		request:   agentAIApprovalRequest{ID: approvalID, respond: respond},
	}
	m.sessions[sessionID] = &agentAISession{id: sessionID, runSeq: 1, originSocket: originSocketID}
	m.mu.Unlock()
	return respond
}

func TestApprovalAckDualWrite(t *testing.T) {
	m := newTestAISocketsManager(t)
	defer m.closeAll()
	// runCh 模拟 run 发起通道（手机远端）；decideCh 模拟决策到达通道（本机）。
	runCh := make(chan map[string]interface{}, 8)
	decideCh := make(chan map[string]interface{}, 8)
	runSock := m.registerAISocket("remote", func(p interface{}) error { runCh <- p.(map[string]interface{}); return nil })
	decideSock := m.registerAISocket("local", func(p interface{}) error { decideCh <- p.(map[string]interface{}); return nil })
	respond := registerApprovalAckFixture(t, m, "s1", "a1", runSock.id)

	// 决策从 decideSock 到达（仪表盘抢答手机回合的场景）。
	m.approval(map[string]interface{}{
		"type": models.AgentEventAIApprovalResponse, "session_id": "s1",
		"approval_id": "a1", "decision": "accept",
		agentAIOriginSocketField: decideSock.id,
	}, func(p interface{}) error { decideCh <- p.(map[string]interface{}); return nil })

	if got := <-respond; got.Decision != models.AgentAIApprovalDecisionAccept {
		t.Fatalf("决策未送达 waiter: %+v", got)
	}
	// ack 出现在两条通道：决策通道同步内联；run 通道经 pump 异步落。
	waitUntilFor(t, time.Second, func() bool { return countAckFrames(runCh) >= 1 })
	// settle 后断言恰一：双写是「每通道一条」，不是跨 socket 重复灌
	// （跨 socket 双 enqueue 回归此处必炸）。
	time.Sleep(50 * time.Millisecond)
	if got := countAckFrames(runCh); got != 0 {
		t.Fatalf("run 通道 settle 后仍冒 ack, 实际 %d 条（应恰 1）", got+1)
	}
	if got := countAckFrames(decideCh); got != 1 {
		t.Fatalf("决策通道应恰 1 条 ack, 实际 %d 条", got)
	}
}

func TestApprovalAckNoDuplicateOnSameSocket(t *testing.T) {
	m := newTestAISocketsManager(t)
	defer m.closeAll()
	ch := make(chan map[string]interface{}, 8)
	// role="remote" 的 socket 恒居 agentAIRemoteSocketID 单键（sock.id=="remote"）。
	sock := m.registerAISocket("remote", func(p interface{}) error { ch <- p.(map[string]interface{}); return nil })
	respond := registerApprovalAckFixture(t, m, "s1", "a1", sock.id)

	// 决策从同一条（run 发起的）socket 到达：ack 只写一次，不得双写重复。
	m.approval(map[string]interface{}{
		"type": models.AgentEventAIApprovalResponse, "session_id": "s1",
		"approval_id": "a1", "decision": "accept",
		agentAIOriginSocketField: agentAIRemoteSocketID,
	}, func(p interface{}) error { ch <- p.(map[string]interface{}); return nil })

	if got := <-respond; got.Decision != models.AgentAIApprovalDecisionAccept {
		t.Fatalf("决策未送达 waiter: %+v", got)
	}
	// 决策通道的写是同步的；再给 pump 一个窗口，排除「异步重复写」漏检。
	time.Sleep(50 * time.Millisecond)
	if acks := countAckFrames(ch); acks != 1 {
		t.Fatalf("同 socket ack 应只写一次,实际 %d", acks)
	}
}

// TestOptionResponseRefreshesOriginSocket 覆盖移交项 N1：option 作答也要刷新
// session.originSocket——option 续接回合里新冒出的审批 ack 要写回「作答这条
// socket」，不刷新的话 ack 双写仍指向上一回合的旧 origin（写到旧通道）。
func TestOptionResponseRefreshesOriginSocket(t *testing.T) {
	m := newTestAISocketsManager(t)
	defer m.closeAll()
	_, _, writer := captureAIWriter(t)
	m.mu.Lock()
	m.sessions["s1"] = &agentAISession{id: "s1", runSeq: 2, originSocket: "stale-socket"}
	m.mu.Unlock()

	m.optionResponse(map[string]interface{}{
		"type": models.AgentEventAIOptionResponse, "session_id": "s1", "option_id": "opt_unknown",
		"selected":               []interface{}{"a"},
		agentAIOriginSocketField: "aisock-9",
	}, writer)

	m.mu.Lock()
	got := m.sessions["s1"].originSocket
	m.mu.Unlock()
	if got != "aisock-9" {
		t.Fatalf("optionResponse 未刷新 session.originSocket, got %q, want aisock-9", got)
	}
}

// TestApprovalRequestCarriesExpiresAt 验证 ai.approval.request pending 帧与
// 两端快照（全量 pendingApprovalsSnapshot / 按会话
// pendingApprovalsSnapshotForSession）都带 expires_at（RFC3339，Task 8）。
// 断言必须持 mu 锁：requestApproval 在 goroutine 里跑，sent 是共享变量。
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
	// 全量快照带 expires_at
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
	// 按会话快照也要带（Task 6 的 pendingApprovalsSnapshotForSession 占位处）
	perSession := m.pendingApprovalsSnapshotForSession("s-exp")
	if len(perSession) != 1 {
		t.Fatalf("按会话快照应 1 项,实际 %d", len(perSession))
	}
	if _, ok := perSession[0]["expires_at"].(string); !ok {
		t.Fatal("按会话快照缺 expires_at")
	}
	// 喂一个 cancel 决策让 goroutine 走到 return（否则泄漏一个阻塞 select，
	// ctx=Background 永不 Done）；resolved 分支会再写一帧覆盖 sent，此时
	// 断言均已结束，不影响结果。
	m.mu.Lock()
	if w := m.approvals[agentAIApprovalMapKey("s-exp", "a-exp")]; w != nil {
		w.request.respond <- agentAIApprovalResponse{Decision: models.AgentAIApprovalDecisionCancel}
	}
	m.mu.Unlock()
	m.mu.Lock()
	delete(m.approvals, agentAIApprovalMapKey("s-exp", "a-exp"))
	m.mu.Unlock()
}
