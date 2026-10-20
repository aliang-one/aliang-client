package services

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSteerStdin 记录写入的行，并模拟管道关闭。
type fakeSteerStdin struct {
	mu     sync.Mutex
	lines  []string
	closed bool
}

func (f *fakeSteerStdin) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, errFakeStdinClosed
	}
	f.lines = append(f.lines, strings.TrimSpace(string(p)))
	return len(p), nil
}

func (f *fakeSteerStdin) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeSteerStdin) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.lines...)
}

var errFakeStdinClosed = &fakeClosedError{}

type fakeClosedError struct{}

func (*fakeClosedError) Error() string { return "stdin closed" }

func newSteerTestRun() agentAIRun {
	return agentAIRun{sessionID: "s1", runSeq: 1, messageID: "m1"}
}

func steerAcks(t *testing.T, payloads []interface{}) []map[string]interface{} {
	t.Helper()
	out := make([]map[string]interface{}, 0, len(payloads))
	for _, p := range payloads {
		m, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		if m["type"] == "ai.steer.ack" {
			out = append(out, m)
		}
	}
	return out
}

// 启动前到达的 steer 必须先排队，start 写入 prompt 后再冲刷，且 prompt 与
// steer 都是合法的 stream-json user 行。
func TestClaudeSteerQueuesBeforeStartThenFlushes(t *testing.T) {
	stdin := &fakeSteerStdin{}
	var mu sync.Mutex
	var payloads []interface{}
	write := func(v interface{}) error {
		mu.Lock()
		payloads = append(payloads, v)
		mu.Unlock()
		return nil
	}
	control := newAgentAIClaudeSteerControl(newSteerTestRun(), stdin, write)

	result, err := control.deliver(agentAISteerMessage{MessageID: "steer-1", Content: "改做别的事", CreatedAt: time.Now()})
	if err != nil || result != "queued" {
		t.Fatalf("deliver before start = (%q,%v)，期望 queued", result, err)
	}
	if got := stdin.snapshot(); len(got) != 0 {
		t.Fatalf("start 之前不应写 stdin，got %v", got)
	}

	control.start("原始 prompt")

	lines := stdin.snapshot()
	if len(lines) != 2 {
		t.Fatalf("期望 prompt + 1 条 steer 两行，got %d: %v", len(lines), lines)
	}
	// 第一行必须是 prompt
	var first map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("prompt 不是合法 JSON 行: %v (%q)", err, lines[0])
	}
	if first["type"] != "user" {
		t.Fatalf("prompt 行 type=%v，期望 user", first["type"])
	}
	msg, _ := first["message"].(map[string]interface{})
	content, _ := msg["content"].([]interface{})
	block, _ := content[0].(map[string]interface{})
	if block["text"] != "原始 prompt" {
		t.Fatalf("prompt 文本错位: %v", block["text"])
	}
	// 第二行是 steer
	if !strings.Contains(lines[1], "改做别的事") {
		t.Fatalf("第二条 steer 未写入: %v", lines[1])
	}

	acks := steerAcks(t, payloads)
	if len(acks) != 1 || acks[0]["result"] != "applied" || acks[0]["message_id"] != "steer-1" {
		t.Fatalf("排队 steer 的 ack 不符合预期: %v", acks)
	}
}

// start 之后的 steer 立即写入并 ack applied。
func TestClaudeSteerDeliversAfterStart(t *testing.T) {
	stdin := &fakeSteerStdin{}
	var mu sync.Mutex
	var payloads []interface{}
	write := func(v interface{}) error {
		mu.Lock()
		payloads = append(payloads, v)
		mu.Unlock()
		return nil
	}
	control := newAgentAIClaudeSteerControl(newSteerTestRun(), stdin, write)
	control.start("p")

	result, err := control.deliver(agentAISteerMessage{MessageID: "s2", Content: "插话", CreatedAt: time.Now()})
	if err != nil || result != "applied" {
		t.Fatalf("deliver after start = (%q,%v)，期望 applied", result, err)
	}
	if got := stdin.snapshot(); len(got) != 2 {
		t.Fatalf("期望 2 行，got %v", got)
	}
	// ack 由调用方（manager.steer）统一发出，deliver 只返回结果串。
	if acks := steerAcks(t, payloads); len(acks) != 0 {
		t.Fatalf("deliver 不应自行 ack，got %v", acks)
	}
}

// closeInput（收到 result）之后到达的 steer 必须被拒绝为 not_running，
// 而不是静默丢弃——手机要靠它把气泡标失败。
func TestClaudeSteerRejectsAfterCloseInput(t *testing.T) {
	stdin := &fakeSteerStdin{}
	control := newAgentAIClaudeSteerControl(newSteerTestRun(), stdin, func(interface{}) error { return nil })
	control.start("p")
	control.closeInput()

	if !stdin.closed {
		t.Fatal("closeInput 应关闭 stdin")
	}
	result, err := control.deliver(agentAISteerMessage{MessageID: "s3", Content: "迟到", CreatedAt: time.Now()})
	if result != "not_running" || err == nil {
		t.Fatalf("closeInput 后 deliver = (%q,%v)，期望 not_running + error", result, err)
	}
}

// run 结束时仍未投递的 steer 必须逐个 ack，避免手机气泡永远 pending。
func TestClaudeSteerCloseAcksQueuedItems(t *testing.T) {
	stdin := &fakeSteerStdin{}
	var mu sync.Mutex
	var payloads []interface{}
	write := func(v interface{}) error {
		mu.Lock()
		payloads = append(payloads, v)
		mu.Unlock()
		return nil
	}
	control := newAgentAIClaudeSteerControl(newSteerTestRun(), stdin, write)
	if _, err := control.deliver(agentAISteerMessage{MessageID: "q1", Content: "a", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.deliver(agentAISteerMessage{MessageID: "q2", Content: "b", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	control.close("not_running", "turn ended", "turn_ended")

	acks := steerAcks(t, payloads)
	if len(acks) != 2 {
		t.Fatalf("期望 2 条 ack，got %d: %v", len(acks), acks)
	}
	for _, ack := range acks {
		if ack["result"] != "not_running" || ack["code"] != "turn_ended" {
			t.Fatalf("ack 不符合预期: %v", ack)
		}
	}
}

// stream-json 输入模式下：必须带 --input-format stream-json、prompt 不得进
// argv、stdinPrompt 保留原文供附件/只读包装器复用。
func TestClaudeToolStreamJSONInputMovesPromptOffArgv(t *testing.T) {
	prompt := "运行 \"npm run build\" 并解释 %APPDATA% 与 a & b"
	tool := newClaudeCodeAIToolForOSWithInput("darwin", "claude", "/usr/local/bin/claude", prompt, "glm-5.2", "high", "", true)

	if !tool.stdinStreamJSON {
		t.Fatal("stdinStreamJSON 应为 true")
	}
	if tool.stdinPrompt != prompt {
		t.Fatalf("stdinPrompt = %q，期望原 prompt", tool.stdinPrompt)
	}
	for _, arg := range tool.args {
		if arg == prompt || strings.Contains(arg, "%APPDATA%") {
			t.Fatalf("prompt 不得出现在 argv: %v", tool.args)
		}
	}
	var hasInputFormat bool
	for i := 0; i+1 < len(tool.args); i++ {
		if tool.args[i] == "--input-format" && tool.args[i+1] == "stream-json" {
			hasInputFormat = true
		}
	}
	if !hasInputFormat {
		t.Fatalf("缺少 --input-format stream-json: %v", tool.args)
	}
	// 输出侧参数保持不变
	for _, flag := range []string{"--print", "--verbose", "--output-format", "--include-partial-messages"} {
		found := false
		for _, arg := range tool.args {
			if arg == flag {
				found = true
			}
		}
		if !found {
			t.Fatalf("缺少 CLI 参数 %s: %v", flag, tool.args)
		}
	}
}

// 不支持 stream-json 输入时保持历史行为：prompt 回到 argv 末位。
func TestClaudeToolWithoutStreamJSONKeepsArgvPrompt(t *testing.T) {
	tool := newClaudeCodeAIToolForOSWithInput("darwin", "claude", "/usr/local/bin/claude", "hello", "", "", "", false)
	if tool.stdinStreamJSON {
		t.Fatal("stdinStreamJSON 应为 false")
	}
	if len(tool.args) == 0 || tool.args[len(tool.args)-1] != "hello" {
		t.Fatalf("prompt 应回到 argv 末位，got %v", tool.args)
	}
	if tool.stdinPrompt != "" {
		t.Fatalf("stdinPrompt 应为空，got %q", tool.stdinPrompt)
	}
}

// 能力位：只有探测到 stream-json 输入支持时才宣称 ai_steer_claude。
func TestAgentAICapabilitiesAdvertiseClaudeSteerOnlyWhenSupported(t *testing.T) {
	withSteer := agentAICapabilitiesForTools(true, false, false, false, false, true)
	if !agentAIStringSliceContains(withSteer, "ai_steer_claude") {
		t.Fatalf("capabilities %v missing ai_steer_claude", withSteer)
	}
	withoutSteer := agentAICapabilitiesForTools(true, false, false, false, false, false)
	if agentAIStringSliceContains(withoutSteer, "ai_steer_claude") {
		t.Fatalf("capabilities %v unexpectedly contain ai_steer_claude", withoutSteer)
	}
}
