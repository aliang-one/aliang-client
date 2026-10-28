package services

import (
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/agentcore"
)

// 这一组锁死「压缩落库不得抹掉立身上下文」的契约：
//
//	历史首部的 system 条目（initial_context：fork 快照 / goal 上下文）只由
//	ai.session.create / ai.run.start 写入一次；压缩产物只含 user/assistant
//	（内核 messages 不收 system），原样落库会把它永久抹掉，而摘要由内核
//	messages 生成（不含 system），摘要补不回来。两层防御各有一条测试：
//	  1. replaceAgentCoreHistory 落库前把旧历史的非 user/assistant 条目拼回
//	     头部（守卫层）；
//	  2. agentCoreSystemContextFromHistory 历史缺 system 条目时回落
//	     session.initialContext（兜底层）。
//
// 变异守卫：摘掉 replaceAgentCoreHistory 的保留逻辑，第一条即红。

func newAgentCoreCompactTestSession() *agentAISession {
	return &agentAISession{
		id:             "s-compact",
		mode:           "vibe",
		projectPath:    "/proj",
		provider:       "agentcore",
		model:          "glm-5.3",
		initialContext: "目标：修复 compact 链路。约束：只改 agentcore。",
		runSeq:         3,
		history: []agentAIMessage{
			{Role: "system", MessageID: "initial_context", Content: "目标：修复 compact 链路。约束：只改 agentcore。", CreatedAt: time.Now().UTC()},
			{Role: "user", Content: "第一轮问题", CreatedAt: time.Now().UTC()},
			{Role: "assistant", Content: "第一轮回答", CreatedAt: time.Now().UTC()},
			{Role: "user", Content: "第二轮问题", CreatedAt: time.Now().UTC()},
		},
		activity: newAgentAIActivity(),
	}
}

// TestReplaceAgentCoreHistoryPreservesSystemEntries 锁守卫层：自动压缩落库后
// system 条目必须仍在历史首部，user/assistant 的压缩形状不受影响。
func TestReplaceAgentCoreHistoryPreservesSystemEntries(t *testing.T) {
	mgr := newAgentAIManager()
	sess := newAgentCoreCompactTestSession()
	mgr.mu.Lock()
	mgr.sessions[sess.id] = sess
	mgr.mu.Unlock()

	// OnCompact 钩子落库用的就是这一入口（replaceAgentCoreHistory）。
	mgr.replaceAgentCoreHistory(sess.id, sess.runSeq, []agentcore.Message{
		agentcore.TextMessage("user", "以下是本会话此前内容的压缩摘要：\n\n- 用户在修 compact 链路"),
		agentcore.TextMessage("user", "第二轮问题"),
	})

	mgr.mu.Lock()
	got := append([]agentAIMessage(nil), sess.history...)
	mgr.mu.Unlock()

	if len(got) == 0 {
		t.Fatal("压缩后历史为空")
	}
	head := got[0]
	if head.Role != "system" || head.MessageID != "initial_context" {
		t.Fatalf("历史首部 = %q/%q, want system/initial_context（立身上下文被压缩落库抹掉）", head.Role, head.MessageID)
	}
	if head.Content != "目标：修复 compact 链路。约束：只改 agentcore。" {
		t.Fatalf("initial_context 内容被改写: %q", head.Content)
	}
	// 压缩产物原样跟在 system 条目之后。
	if len(got) != 3 {
		t.Fatalf("压缩后历史 %d 条, want 3（system + 摘要 + 尾部）", len(got))
	}
	if got[1].Role != "user" || !strings.Contains(got[1].Content, "压缩摘要") {
		t.Fatalf("第二条应是压缩摘要, got %q/%q", got[1].Role, got[1].Content)
	}
	if got[2].Content != "第二轮问题" {
		t.Fatalf("尾部原文丢失: %q", got[2].Content)
	}
}

// TestReplaceAgentCoreHistoryRunSeqGuardUnchanged 锁 runSeq 守卫语义不被本次
// 改动顺带破坏：runSeq 已前进时不得覆盖历史。
func TestReplaceAgentCoreHistoryRunSeqGuardUnchanged(t *testing.T) {
	mgr := newAgentAIManager()
	sess := newAgentCoreCompactTestSession()
	mgr.mu.Lock()
	mgr.sessions[sess.id] = sess
	mgr.mu.Unlock()

	before := len(sess.history)
	mgr.replaceAgentCoreHistory(sess.id, sess.runSeq+1, []agentcore.Message{
		agentcore.TextMessage("user", "压缩摘要"),
	})
	mgr.mu.Lock()
	same := len(sess.history) == before && sess.history[0].MessageID == "initial_context"
	mgr.mu.Unlock()
	if !same {
		t.Fatal("runSeq 不匹配时不得覆盖历史")
	}
}

// TestAgentCoreSystemContextFromHistoryFallsBackToInitialContext 锁兜底层：
// 历史有 system 条目时以历史为准；历史缺失（被旧版本落库冲掉/尾部裁剪）时
// 回落 session.initialContext；两者都无时为空。
func TestAgentCoreSystemContextFromHistoryFallsBackToInitialContext(t *testing.T) {
	withSystem := []agentAIMessage{
		{Role: "system", MessageID: "initial_context", Content: "历史里的上下文"},
		{Role: "user", Content: "hi"},
	}
	if got := agentCoreSystemContextFromHistory(withSystem, "字段里的上下文"); got != "历史里的上下文" {
		t.Fatalf("历史有 system 条目时应以历史为准, got %q", got)
	}

	withoutSystem := []agentAIMessage{{Role: "user", Content: "hi"}}
	if got := agentCoreSystemContextFromHistory(withoutSystem, "字段里的上下文"); got != "字段里的上下文" {
		t.Fatalf("历史缺 system 条目时应回落 initialContext, got %q", got)
	}
	if got := agentCoreSystemContextFromHistory(withoutSystem, ""); got != "" {
		t.Fatalf("两者皆无时应为空, got %q", got)
	}
}

// TestAgentCoreMessagesNextRunSystemKeepsInitialContext 锁消费端全链：压缩
// 落库后，下一轮 run 取到的 System 上下文（agentCoreMessages 的第二返回值，
// 经 agentCoreSystemPromptWithContext 拼进内核 Config.System）仍含立身上下文。
func TestAgentCoreMessagesNextRunSystemKeepsInitialContext(t *testing.T) {
	mgr := newAgentAIManager()
	sess := newAgentCoreCompactTestSession()
	mgr.mu.Lock()
	mgr.sessions[sess.id] = sess
	mgr.mu.Unlock()

	// 自动压缩落库（同 OnCompact 路径）。
	mgr.replaceAgentCoreHistory(sess.id, sess.runSeq, []agentcore.Message{
		agentcore.TextMessage("user", "压缩摘要"),
		agentcore.TextMessage("user", "第二轮问题"),
	})

	msgs, sessionContext := mgr.agentCoreMessages(agentAIRun{sessionID: sess.id, runSeq: sess.runSeq})
	if !strings.Contains(sessionContext, "目标：修复 compact 链路") {
		t.Fatalf("压缩后下一轮 run 的 System 上下文丢失立身上下文: %q", sessionContext)
	}
	if len(msgs) == 0 || msgs[0].Role != "user" {
		t.Fatalf("压缩后下一轮 messages 首条应可发送: %+v", msgs)
	}
	// System prompt 全链：立身上下文进 <session-context> 块。
	prompt := agentCoreSystemPromptWithContext("/proj", sessionContext)
	if !strings.Contains(prompt, "<session-context>") || !strings.Contains(prompt, "目标：修复 compact 链路") {
		t.Fatal("System prompt 未包含立身上下文")
	}

	// 无 initial_context 的普通会话不受影响：压缩后无 system 条目、System
	// 上下文为空、prompt 无 <session-context> 块。
	plain := &agentAISession{
		id: "s-plain", mode: "vibe", projectPath: "/proj", provider: "agentcore",
		runSeq: 1, activity: newAgentAIActivity(),
		history: []agentAIMessage{
			{Role: "user", Content: "普通问题"},
			{Role: "assistant", Content: "普通回答"},
		},
	}
	mgr.mu.Lock()
	mgr.sessions[plain.id] = plain
	mgr.mu.Unlock()
	mgr.replaceAgentCoreHistory(plain.id, plain.runSeq, []agentcore.Message{
		agentcore.TextMessage("user", "压缩摘要"),
		agentcore.TextMessage("assistant", "普通回答"),
	})
	mgr.mu.Lock()
	plainHistory := append([]agentAIMessage(nil), plain.history...)
	mgr.mu.Unlock()
	for _, item := range plainHistory {
		if item.Role == "system" {
			t.Fatalf("无 initial_context 的会话压缩后不应出现 system 条目: %+v", plainHistory)
		}
	}
	if _, ctx := mgr.agentCoreMessages(agentAIRun{sessionID: plain.id, runSeq: plain.runSeq}); ctx != "" {
		t.Fatalf("普通会话的 System 上下文应为空, got %q", ctx)
	}
}
