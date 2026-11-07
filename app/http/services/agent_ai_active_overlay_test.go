package services

import (
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

// 2026-11-07 S1(桌面新建会话手机不可见)回归:
// create-only 会话(cancel==nil)必须进活跃覆盖层(idle),否则永远不进 hello
// 清单、手机永远看不到;origin 必须按发起 socket 推导,恒写 "managed" 会被
// 服务端映射成 'phone',把 dashboard 会话的来源角标翻错。
func TestActiveVibeSessionsSnapshotIncludesIdleCreateOnlySessions(t *testing.T) {
	manager := newAgentAIManager()
	manager.mu.Lock()
	manager.sessions["local-run"] = &agentAISession{
		id:           "local-run",
		provider:     "agentcore",
		projectPath:  "/tmp/proj-a",
		originSocket: agentAIRemoteSocketID,
		cancel:       func() {},
		history:      []agentAIMessage{{Role: "user", Content: "running prompt", CreatedAt: time.Now().UTC()}},
	}
	manager.sessions["local-idle"] = &agentAISession{
		id:           "local-idle",
		provider:     "agentcore",
		projectPath:  "/tmp/proj-a",
		originSocket: "aisock-3",
		history:      []agentAIMessage{{Role: "user", Content: "draft prompt", CreatedAt: time.Now().UTC()}},
	}
	manager.mu.Unlock()

	snapshot := manager.vibeSessionsSnapshot(true)
	byID := map[string]models.AgentVibeSession{}
	for _, session := range snapshot {
		byID[session.ID] = session
	}
	running, ok := byID["local-run"]
	if !ok {
		t.Fatalf("running session missing from snapshot: %+v", snapshot)
	}
	if running.Status != "running" {
		t.Fatalf("running Status = %q, want running", running.Status)
	}
	if running.Origin != "phone" {
		t.Fatalf("running Origin = %q, want phone (remote socket)", running.Origin)
	}
	idle, ok := byID["local-idle"]
	if !ok {
		t.Fatalf("create-only session missing from snapshot: %+v", snapshot)
	}
	if idle.Status != "idle" {
		t.Fatalf("idle Status = %q, want idle", idle.Status)
	}
	if idle.Origin != "dashboard" {
		t.Fatalf("idle Origin = %q, want dashboard (aisock socket)", idle.Origin)
	}
	if idle.ManagedConversationID != "local-idle" {
		t.Fatalf("idle ManagedConversationID = %q, want local-idle", idle.ManagedConversationID)
	}
	if idle.Title == "" {
		t.Fatalf("idle Title empty, want first user message")
	}
}

// 覆盖层归一不得把 idle 会话改写成 running(旧实现硬编码 running)。
func TestNormalizeActiveAgentVibeSessionPreservesIdleStatus(t *testing.T) {
	got := normalizeActiveAgentVibeSession(models.AgentVibeSession{ID: "x", Status: "idle"})
	if got.Status != "idle" {
		t.Fatalf("Status = %q, want idle preserved", got.Status)
	}
	got = normalizeActiveAgentVibeSession(models.AgentVibeSession{ID: "x"})
	if got.Status != "running" {
		t.Fatalf("Status = %q, want running default", got.Status)
	}
}
