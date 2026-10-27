package services

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// P7：回灌的手机契约要被剥掉
// ---------------------------------------------------------------------------

func TestStripAgentAIPhoneContract(t *testing.T) {
	userText := "帮我看看这个仓库"

	wrapped := wrapAgentAIPhoneContract(userText)
	if got := stripAgentAIPhoneContract(wrapped); got != userText {
		t.Fatalf("剥契约后应还原用户原文, got %q", got)
	}
	// 两处独立注入（同一段文本被重复包）也要全部剥掉。
	if got := stripAgentAIPhoneContract(wrapped + "\n\n" + wrapped); got != userText+"\n\n"+userText {
		t.Fatalf("多次注入应全部剥掉, got %q", got)
	}

	// 普通文本不动。
	if got := stripAgentAIPhoneContract(userText); got != userText {
		t.Fatalf("无契约文本不应变化, got %q", got)
	}

	// 别的 system-reminder 是 CC 自己的合法上下文，不能一起删。
	foreign := "<system-reminder>\n当前目录是 /tmp\n</system-reminder>\n\n" + userText
	if got := stripAgentAIPhoneContract(foreign); got != foreign {
		t.Fatalf("第三方提醒不应被动, got %q", got)
	}

	// 锚点命中但形状认不出：宁可不剥，也不能猜着删用户内容。
	malformed := userText + " " + agentAIPhoneContractMarker
	if got := stripAgentAIPhoneContract(malformed); got != malformed {
		t.Fatalf("形状不明应原样返回, got %q", got)
	}

	// 只剩契约（无用户正文）→ 空串，调用方据此丢弃这一条。
	if got := stripAgentAIPhoneContract(agentAIPhoneContractReminder()); got != "" {
		t.Fatalf("纯契约应剥成空, got %q", got)
	}
}

func TestRemoteAgentAIHistoryStripsPhoneContract(t *testing.T) {
	msg := map[string]interface{}{
		"transcript": []interface{}{
			map[string]interface{}{
				"role": "user", "id": "m-1",
				"content": wrapAgentAIPhoneContract("手机消息"),
			},
			map[string]interface{}{
				// 只有契约、没有用户正文的行：剥完是空，整条丢掉。
				"role": "assistant", "id": "m-2",
				"content": agentAIPhoneContractReminder(),
			},
			map[string]interface{}{
				"role": "assistant", "id": "m-3",
				"content": "收到",
			},
		},
	}

	history := remoteAgentAIHistory(msg)
	if len(history) != 2 {
		t.Fatalf("历史条数 = %d, want 2（纯契约行应被丢弃）: %+v", len(history), history)
	}
	if history[0].Content != "手机消息" {
		t.Fatalf("history[0] = %q, want 手机消息（契约不得进历史）", history[0].Content)
	}
	if history[1].Content != "收到" {
		t.Fatalf("history[1] = %q, want 收到", history[1].Content)
	}
}

// ---------------------------------------------------------------------------
// P8：system 角色历史（initial_context）要进内核的 system prompt
// ---------------------------------------------------------------------------

func TestAgentCoreMessagesCarriesSystemContext(t *testing.T) {
	m := newAgentAIManager()
	const sessionID = "conv-system-context"
	const runSeq = 1
	initial := "这是一个从 goal 分叉出来的会话：目标是把登录改成 JWT。"
	m.registerAISessionLocked(&agentAISession{
		id:     sessionID,
		runSeq: runSeq,
		history: []agentAIMessage{
			{Role: "system", MessageID: "initial_context", Content: initial},
			{Role: "user", Content: wrapAgentAIPhoneContract("继续")},
		},
	})

	msgs, sessionContext := m.agentCoreMessages(agentAIRun{sessionID: sessionID, runSeq: runSeq})

	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Fatalf("messages 应只有一条 user（system 不进 messages）, got %+v", msgs)
	}
	if got, _ := msgs[0].Content.(string); got != "继续" {
		t.Fatalf("user 内容应剥掉契约, got %q", got)
	}
	if !strings.Contains(sessionContext, initial) {
		t.Fatalf("system 历史应进 sessionContext, got %q", sessionContext)
	}

	prompt := agentCoreSystemPromptWithContext(t.TempDir(), sessionContext)
	if !strings.Contains(prompt, "<session-context>") || !strings.Contains(prompt, initial) {
		t.Fatalf("system prompt 缺少会话初始上下文: %s", prompt)
	}
	if strings.Contains(prompt, agentAIPhoneContractMarker) {
		t.Fatal("system prompt 不应残留手机契约")
	}
	// 无 system 历史时不得凭空长出 <session-context> 段。
	if bare := agentCoreSystemPrompt(t.TempDir()); strings.Contains(bare, "<session-context>") {
		t.Fatalf("无初始上下文时不应有空段: %s", bare)
	}
	if !strings.Contains(prompt, agentAIOptionSystemPrompt) {
		t.Fatal("options 协议必须保留在 prompt 末尾")
	}
}
