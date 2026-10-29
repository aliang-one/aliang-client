package services

import (
	"fmt"
	"testing"
	"time"
)

// runStart 身份并入的写点纪律：空 payload 不得洗掉已钉身份（2026-10-29
// already-in-use 事故第 3 层：server 重投不带 resume 字段，空串覆盖把
// confirmBinding 钉下的恢复点抹掉，下一回合退化 fresh）。
func TestMergeRunStartIdentityLockedPinIfEmpty(t *testing.T) {
	session := &agentAISession{resumeSessionID: "pinned-resume", reservedNativeSessionID: "pinned-reserved"}
	mergeRunStartIdentityLocked(session, "", "")
	if session.resumeSessionID != "pinned-resume" || session.reservedNativeSessionID != "pinned-reserved" {
		t.Fatalf("空 payload 洗掉了已钉身份: resume=%q reserved=%q", session.resumeSessionID, session.reservedNativeSessionID)
	}
	mergeRunStartIdentityLocked(session, "new-resume", "new-reserved")
	if session.resumeSessionID != "new-resume" || session.reservedNativeSessionID != "new-reserved" {
		t.Fatalf("非空新值未覆盖: resume=%q reserved=%q", session.resumeSessionID, session.reservedNativeSessionID)
	}
	mergeRunStartIdentityLocked(session, "", "only-reserved")
	if session.resumeSessionID != "new-resume" {
		t.Fatalf("单边空值洗掉了另一边的已钉值: resume=%q", session.resumeSessionID)
	}
	if session.reservedNativeSessionID != "only-reserved" {
		t.Fatalf("单边非空新值未生效: reserved=%q", session.reservedNativeSessionID)
	}
}

// 「Session ID … already in use」分类器：真实报错命中，同族 resume-missing 与
// 无关错误不误判。
func TestIsAgentAISessionIDInUse(t *testing.T) {
	hit := "Error: Session ID 40a6e56f-b1c4-4e6d-9a0b-14fa86d5a2f2 is already in use"
	if !isAgentAISessionIDInUse(hit) {
		t.Fatal("真实 already-in-use 报错未被识别")
	}
	if isAgentAISessionIDInUse("No conversation found with session ID abc") {
		t.Fatal("resume-missing 被误判为身份占用")
	}
	if isAgentAISessionIDInUse("invalid api key") {
		t.Fatal("无关错误被误判为身份占用")
	}
}

// 空锚不得退化为无条件撤销：expected 为空时保留已钉恢复点，指名道姓才清。
func TestClearAgentAIResumeSessionIDEmptyAnchorNoop(t *testing.T) {
	manager := newAgentAIManager()
	manager.sessions["s1"] = &agentAISession{id: "s1", runSeq: 3, resumeSessionID: "pinned"}
	manager.clearAgentAIResumeSessionID("s1", 3, "")
	if got := manager.sessions["s1"].resumeSessionID; got != "pinned" {
		t.Fatalf("空锚清掉了已钉恢复点: %q", got)
	}
	manager.clearAgentAIResumeSessionID("s1", 3, "other")
	if got := manager.sessions["s1"].resumeSessionID; got != "pinned" {
		t.Fatalf("expected 不匹配也被清掉: %q", got)
	}
	manager.clearAgentAIResumeSessionID("s1", 3, "pinned")
	if got := manager.sessions["s1"].resumeSessionID; got != "" {
		t.Fatalf("指名道姓的撤销未生效: %q", got)
	}
}

// releaseReservedBinding：reserved 释放并同步清会话侧身份；confirmed 是合法
// resume 锚绝不释放；expected 不匹配不误删。
func TestReleaseReservedBinding(t *testing.T) {
	manager := newAgentAIManager()
	manager.sessions["s1"] = &agentAISession{id: "s1", reservedNativeSessionID: "res-1"}
	manager.bindings["s1"] = agentAIBindingRecord{ConversationID: "s1", Provider: "claudecode", NativeSessionID: "res-1", State: "reserved", BindingVersion: 1}
	manager.releaseReservedBinding("s1", "res-1")
	if _, ok := manager.bindings["s1"]; ok {
		t.Fatal("reserved 绑定未被删除")
	}
	if got := manager.sessions["s1"].reservedNativeSessionID; got != "" {
		t.Fatalf("会话侧 reservedNativeSessionID 未同步清空: %q", got)
	}

	manager.bindings["s2"] = agentAIBindingRecord{ConversationID: "s2", Provider: "claudecode", NativeSessionID: "nat-2", State: "confirmed", BindingVersion: 2}
	manager.releaseReservedBinding("s2", "")
	if _, ok := manager.bindings["s2"]; !ok {
		t.Fatal("confirmed 绑定被误删（它是合法 resume 锚）")
	}

	manager.bindings["s3"] = agentAIBindingRecord{ConversationID: "s3", Provider: "claudecode", NativeSessionID: "nat-3", State: "reserved", BindingVersion: 1}
	manager.releaseReservedBinding("s3", "other-id")
	if _, ok := manager.bindings["s3"]; !ok {
		t.Fatal("expected 不匹配仍误删了绑定")
	}
}

// 会话驱逐时 reserved 绑定随会话丢弃，confirmed 保留（合法 resume 锚）。
func TestEvictDropsReservedBindingKeepsConfirmed(t *testing.T) {
	manager := newAgentAIManager()
	base := time.Now()
	for i := 0; i < agentAISessionResidentCap; i++ {
		id := fmt.Sprintf("s%d", i)
		manager.sessions[id] = residentAISession(id, base.Add(time.Duration(i)*time.Minute), false)
	}
	manager.bindings["s0"] = agentAIBindingRecord{ConversationID: "s0", Provider: "claudecode", NativeSessionID: "res-0", State: "reserved", BindingVersion: 1}
	manager.bindings["s1"] = agentAIBindingRecord{ConversationID: "s1", Provider: "claudecode", NativeSessionID: "nat-1", State: "confirmed", BindingVersion: 1}

	manager.mu.Lock()
	manager.evictOldestIdleAISessionLocked()
	manager.mu.Unlock()

	if _, ok := manager.sessions["s0"]; ok {
		t.Fatal("最老空闲会话 s0 未被驱逐，前置条件不成立")
	}
	if _, ok := manager.bindings["s0"]; ok {
		t.Fatal("被驱逐会话的 reserved 绑定未随会话丢弃")
	}
	if _, ok := manager.bindings["s1"]; !ok {
		t.Fatal("未被驱逐会话的 confirmed 绑定被误删")
	}
}

// 僵尸强拆只撤半个身份会喂出下回合同 id 碰撞：撤销时清掉等于起跑快照的
// reservedNativeSessionID。
func TestRevokeRunResumePointLockedClearsReserved(t *testing.T) {
	manager := newAgentAIManager()
	g := &agentAIRunGuard{sessionID: "s1", runSeq: 2, resumeAtStart: "", bindingAtStart: "res-x"}
	manager.sessions["s1"] = &agentAISession{id: "s1", runSeq: 2, resumeSessionID: "", reservedNativeSessionID: "res-x", guard: g}

	manager.mu.Lock()
	revokedResume, dropped := manager.revokeRunResumePointLocked(g)
	manager.mu.Unlock()

	if revokedResume != "" || dropped != "" {
		t.Fatalf("本轮无中途钉点,不应有撤销产出: resume=%q binding=%q", revokedResume, dropped)
	}
	if got := manager.sessions["s1"].reservedNativeSessionID; got != "" {
		t.Fatalf("起跑快照同值的 reserved 身份未被清空: %q", got)
	}

	// 非本回合来源的 reserved 身份不动。
	g2 := &agentAIRunGuard{sessionID: "s2", runSeq: 5, resumeAtStart: "", bindingAtStart: ""}
	manager.sessions["s2"] = &agentAISession{id: "s2", runSeq: 5, reservedNativeSessionID: "keep-me", guard: g2}
	manager.mu.Lock()
	manager.revokeRunResumePointLocked(g2)
	manager.mu.Unlock()
	if got := manager.sessions["s2"].reservedNativeSessionID; got != "keep-me" {
		t.Fatalf("非起跑快照来源的 reserved 身份被误清: %q", got)
	}
}
