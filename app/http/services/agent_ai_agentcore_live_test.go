package services

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

// 这一条是**真出网**的宿主路径冒烟，默认跳过：要求本机 56432 本地代理在跑，
// 并且会真的花一次模型调用。用 AGENTCORE_LIVE=1 打开。
//
// 为什么需要它（两层真出网各管一半）：
//
//	app/agentcore/live_smoke_test.go  内核吃得下真实网关的 SSE 帧
//	本测试                            这些帧经宿主管线后，手机收到的事件仍正确
//
// 现有 TestRunAgentCoreEmitsSingleDone 用的是 httptest 假上游——它能证明
// 「我们按契约解析」，但假服务器永远不会冒出真实网关才有的事件形状
// （`event:` 行、`ping`、末尾的 `message_stop`）。真实帧多一个、少一个，
// 去重逻辑（suffixNotStreamed）就可能把文本吞掉或重复，而事件计数看起来
// 一切正常。所以这里断言的仍是**手机真实收到的那批事件**，只是上游换成真的。
func TestLiveRunAgentCoreThroughLocalProxy(t *testing.T) {
	if os.Getenv("AGENTCORE_LIVE") != "1" {
		t.Skip("设置 AGENTCORE_LIVE=1 才跑真出网宿主路径冒烟（需要 56432 本地代理）")
	}
	model := strings.TrimSpace(os.Getenv("AGENTCORE_LIVE_MODEL"))
	if model == "" {
		model = "glm-5.2"
	}

	// 与内核冒烟同一条出网路径：零凭据，靠 56432 注入 Authorization-Inner。
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:56432")
	t.Setenv("ANTHROPIC_MODEL", "")

	m := newAgentAIManager()
	const sessionID = "conv-agentcore-live"
	const runSeq = 1
	m.registerAISessionLocked(&agentAISession{
		id:      sessionID,
		runSeq:  runSeq,
		history: []agentAIMessage{{Role: "user", Content: "只回复两个字：你好"}},
	})

	log := &agentCoreEventLog{}
	run := agentAIRun{
		sessionID:   sessionID,
		runSeq:      runSeq,
		messageID:   "msg-live-1",
		provider:    "agentcore",
		model:       model,
		projectPath: t.TempDir(),
		activity:    newAgentAIActivity(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	m.runAgentCore(ctx, run, log.write)

	if got := log.count(models.AgentEventAIDone); got != 1 {
		t.Fatalf("ai.done 数量 = %d, want 1（事件序列 %v）", got, log.types())
	}
	if got := log.count(models.AgentEventAIError); got != 0 {
		t.Fatalf("ai.error 数量 = %d, want 0（事件 %v）", got, log.filter(models.AgentEventAIError))
	}
	if first := log.types()[0]; first != models.AgentEventAIRunStarted {
		t.Fatalf("首帧 = %s, want %s（序列 %v）", first, models.AgentEventAIRunStarted, log.types())
	}
	if got := log.count(models.AgentEventAISessionBound); got != 1 {
		t.Fatalf("ai.session.bound 数量 = %d, want 1（序列 %v）", got, log.types())
	}

	deltas := log.filter(models.AgentEventAIDelta)
	if len(deltas) == 0 {
		t.Fatalf("一条 ai.delta 都没有（事件 %v）", log.types())
	}
	var text strings.Builder
	for _, d := range deltas {
		s, _ := d["delta"].(string)
		text.WriteString(s)
	}
	if strings.TrimSpace(text.String()) == "" {
		t.Fatalf("delta 拼起来是空的（事件 %v）", log.types())
	}

	// 助手文本必须进历史，下一轮才看得到自己说过什么。
	m.mu.Lock()
	history := append([]agentAIMessage(nil), m.sessions[sessionID].history...)
	m.mu.Unlock()
	if len(history) != 2 || history[1].Role != "assistant" {
		t.Fatalf("history = %+v, want 追加一条 assistant", history)
	}

	preview := text.String()
	if len([]rune(preview)) > 60 {
		preview = string([]rune(preview)[:60]) + "…"
	}
	t.Logf("真出网宿主路径 OK: model=%s deltas=%d text=%q", model, len(deltas), preview)
}
