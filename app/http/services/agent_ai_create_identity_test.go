package services

// create/message(v2 面)的身份字段纪律回归:聊天页每条消息前都会重发一次
// create 且不带 resume 键,空串覆写会把 confirmBinding 钉下的原生会话 id 抹空,
// 下一回合内核重新铸造 → confirmBinding mismatch,会话从第二回合起轮轮必败
// (2026-11-02 新装机 agentcore 实锤:`native session id mismatch: expected
// agentcore-…-1, got agentcore-…-2`)。本文件锁死三层防线:
//
//	① create 既有会话分支:身份三字段空值不覆写(与 mergeRunStartIdentityLocked 同纪律)
//	② create 新建分支:confirmed 绑定为空 resume 供种(镜像 runStart 反推)
//	③ message 懒建分支:同 ②,且内核必须复用绑定 id(端到端:ai.session.bound 回带绑定 id)

import (
	"sync"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

func findAIFrames(mu *sync.Mutex, events *[]map[string]interface{}, frameType string) []map[string]interface{} {
	mu.Lock()
	defer mu.Unlock()
	var out []map[string]interface{}
	for _, f := range *events {
		if t, _ := f["type"].(string); t == frameType {
			out = append(out, f)
		}
	}
	return out
}

// ① 既有会话收到不带身份键的 create(聊天页每消息重发的形态),已钉身份
// 必须原样保留;带显式身份的 create(服务端权威改写)仍要正常覆盖。
func TestCreateExistingSessionPreservesPinnedIdentityOnEmptyPayload(t *testing.T) {
	m := newAgentAIManager()
	const sid = "s-create-identity-preserve"
	m.mu.Lock()
	m.sessions[sid] = &agentAISession{
		id:              sid,
		provider:        "agentcore",
		resumeSessionID: "agentcore-111-1",
		bindingVersion:  2,
	}
	m.mu.Unlock()

	mu, events, writer := captureAIWriter(t)
	m.create(map[string]interface{}{
		"session_id":   sid,
		"provider":     "agentcore",
		"project_path": t.TempDir(),
	}, writer)
	if created := findAIFrames(mu, events, models.AgentEventAISessionCreated); len(created) != 1 {
		t.Fatalf("expect exactly one ai.session.created, got %d (%v)", len(created), func() []string {
			var types []string
			mu.Lock()
			defer mu.Unlock()
			for _, f := range *events {
				s, _ := f["type"].(string)
				types = append(types, s)
			}
			return types
		}())
	}

	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess.resumeSessionID != "agentcore-111-1" {
		t.Fatalf("空 payload 洗掉了已钉 resumeSessionID: %q", sess.resumeSessionID)
	}
	if sess.bindingVersion != 2 {
		t.Fatalf("空 payload 洗掉了已钉 bindingVersion: %d", sess.bindingVersion)
	}

	// 非空新值照常覆盖(服务端权威改写路径不受本修复影响)。
	mu2, events2, writer2 := captureAIWriter(t)
	m.create(map[string]interface{}{
		"session_id":        sid,
		"provider":          "agentcore",
		"project_path":      t.TempDir(),
		"resume_session_id": "agentcore-999-9",
		"binding_version":   7,
	}, writer2)
	if created := findAIFrames(mu2, events2, models.AgentEventAISessionCreated); len(created) != 1 {
		t.Fatalf("expect exactly one ai.session.created on explicit overwrite, got %d", len(created))
	}
	m.mu.Lock()
	sess = m.sessions[sid]
	m.mu.Unlock()
	if sess.resumeSessionID != "agentcore-999-9" || sess.bindingVersion != 7 {
		t.Fatalf("非空身份未覆盖: resume=%q bindingVersion=%d", sess.resumeSessionID, sess.bindingVersion)
	}
}

// ② 新建会话分支:绑定已 confirmed 时,空 resume 必须从绑定供种(agent 重启
// 丢内存后,重启后的第一回合 create 不带 resume,不供种则内核重铸必撞 mismatch)。
func TestCreateNewSessionSeedsResumeFromConfirmedBinding(t *testing.T) {
	m := newAgentAIManager()
	const sid = "s-create-identity-seed"
	m.mu.Lock()
	m.bindings[sid] = agentAIBindingRecord{
		ConversationID:  sid,
		Provider:        "agentcore",
		NativeSessionID: "agentcore-222-1",
		State:           "confirmed",
		BindingVersion:  1,
	}
	m.mu.Unlock()

	_, _, writer := captureAIWriter(t)
	m.create(map[string]interface{}{
		"session_id":   sid,
		"provider":     "agentcore",
		"project_path": t.TempDir(),
	}, writer)

	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess == nil {
		t.Fatal("create 未注册会话")
	}
	if sess.resumeSessionID != "agentcore-222-1" {
		t.Fatalf("confirmed 绑定未供种 resumeSessionID: %q", sess.resumeSessionID)
	}
}

// ② 的边界:reserved 绑定不供种——fresh 铸造/预留是 reserved 的正当路径,
// 供种会抢走 reserveBinding 的锚。
func TestCreateReservedBindingNotSeeded(t *testing.T) {
	m := newAgentAIManager()
	const sid = "s-create-identity-reserved"
	m.mu.Lock()
	m.bindings[sid] = agentAIBindingRecord{
		ConversationID:  sid,
		Provider:        "agentcore",
		NativeSessionID: "res-should-not-seed",
		State:           "reserved",
		BindingVersion:  1,
	}
	m.mu.Unlock()

	_, _, writer := captureAIWriter(t)
	m.create(map[string]interface{}{
		"session_id":   sid,
		"provider":     "agentcore",
		"project_path": t.TempDir(),
	}, writer)

	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess == nil {
		t.Fatal("create 未注册会话")
	}
	if sess.resumeSessionID != "" {
		t.Fatalf("reserved 绑定被误供种: %q", sess.resumeSessionID)
	}
}

// ③ 端到端:懒建路径(会话不在内存,首条 ai.message 直达)必须从 confirmed
// 绑定供种,内核复用绑定 id——断言 ai.session.bound 回带绑定原生 id 且零
// ai.error。修复前内核重铸新 id,confirmBinding 必然 mismatch 报错。
func TestMessageLazyCreateSeedsResumeFromConfirmedBinding(t *testing.T) {
	setTestAgentHome(t) // journal 根在 newAgentAIManager 构造时解析
	up, srv := newAgentCoreUpstream(t, agentCoreTextTurn("你好", "，世界"))
	// agentCoreBaseURL 只接受回环地址;httptest 正好是 127.0.0.1:PORT。
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")
	defer func() { _ = up }()

	m := newAgentAIManager()
	const sid = "s-lazy-identity-seed"
	m.mu.Lock()
	m.bindings[sid] = agentAIBindingRecord{
		ConversationID:  sid,
		Provider:        "agentcore",
		NativeSessionID: "agentcore-333-1",
		State:           "confirmed",
		BindingVersion:  1,
	}
	m.mu.Unlock()

	mu, events, writer := captureAIWriter(t)
	m.message(map[string]interface{}{
		"session_id":   sid,
		"message_id":   "msg-lazy-1",
		"content":      "打个招呼",
		"provider":     "agentcore",
		"model":        "glm-5.2",
		"project_path": t.TempDir(),
	}, writer)

	// 回合异步起跑:先等终态,再断言(轮询模式与 agent_service_test 同款)。
	bound := waitForAgentEvent(t, mu, events, models.AgentEventAISessionBound, nil)
	waitForAgentEvent(t, mu, events, models.AgentEventAIDone, nil)

	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess == nil {
		t.Fatal("懒建未注册会话")
	}
	if sess.resumeSessionID != "agentcore-333-1" {
		t.Fatalf("懒建未从 confirmed 绑定供种: %q", sess.resumeSessionID)
	}
	if errs := findAIFrames(mu, events, models.AgentEventAIError); len(errs) != 0 {
		t.Fatalf("懒建回合不应有 ai.error(重铸即 mismatch): %v", errs)
	}
	if native, _ := bound["native_session_id"].(string); native != "agentcore-333-1" {
		t.Fatalf("内核未复用绑定原生 id: bound=%q want agentcore-333-1", native)
	}
}
