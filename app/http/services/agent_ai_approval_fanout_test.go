package services

// 审批 ack 双写（2026-10-28 跨端会话统一，spec §4.2.2）。
//
// 背景：审批决策的 ack（ai.approval.ack，带 delivery_id）原先只写「送来决策
// 的那条 socket」。仪表盘抢答手机发起的回合时，ack 进浏览器 WS，PhoneServer
// 重投引擎收不到 ack，已生效决策被重投至多 10 次（agent 端靠 completedApprovals
// 回 duplicate 幂等兜底，但服务端状态滞留）。修法 = ack 双写：决策到达的
// socket（现有）+ run 发起的 socket（session.originSocket，Task 5 落）。

import (
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
	if got := countAckFrames(decideCh); got < 1 {
		t.Fatalf("决策通道缺 ack 帧, 实际 %d 条 ack", got)
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
