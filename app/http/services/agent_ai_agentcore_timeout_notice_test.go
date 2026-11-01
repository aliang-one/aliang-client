package services

import (
	"encoding/json"
	"sync"
	"testing"

	"aliang.one/nursorgate/app/agentcore"
	"aliang.one/nursorgate/app/http/models"
)

// TestToolTimeoutEmitsRunNotice 覆盖「工具超时要通知到用户」这条链路。
//
// 背景：工具超时后内核只回模型一句错误，模型通常自己重试——用户那边看到的
// 只是「半天没动静」。2026-11-01 事故里那条命令卡了 1 小时 58 分，用户全程在
// 手机上等，没有任何提示。这里验证宿主会发一条 ai.run.notice(code=tool_timeout)。
func TestToolTimeoutEmitsRunNotice(t *testing.T) {
	var mu sync.Mutex
	var frames []map[string]interface{}
	writer := agentTerminalWriter(func(v interface{}) error {
		mu.Lock()
		defer mu.Unlock()
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		var m map[string]interface{}
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}
		frames = append(frames, m)
		return nil
	})

	m := newAgentAIManager()
	run := agentAIRun{sessionID: "s-timeout", messageID: "msg-1"}
	diag := &agentCoreDiagnostics{}

	hooks := agentCoreLoopHooks(m, run, diag, writer)

	// 1) 超时 → 发一条 notice
	hooks.OnToolCall("Bash", map[string]interface{}{"command": "find ~"}, agentcore.ToolResult{
		Content:  "[命令超时，已终止（上限 30s）]",
		IsError:  true,
		TimedOut: true,
	})

	mu.Lock()
	defer mu.Unlock()
	if len(frames) == 0 {
		t.Fatal("工具超时未发出任何事件")
	}
	found := false
	for _, f := range frames {
		if f["type"] == models.AgentEventAIRunNotice && f["code"] == "tool_timeout" {
			found = true
			if f["session_id"] != "s-timeout" {
				t.Errorf("notice session_id 错: %v", f["session_id"])
			}
			if f["detail"] != "Bash" {
				t.Errorf("notice detail 应为工具名，实际: %v", f["detail"])
			}
		}
	}
	if !found {
		t.Fatalf("未找到 code=tool_timeout 的 ai.run.notice，frames=%v", frames)
	}
}

// TestToolTimeoutNoticeDedupedPerTool 验证同一 run 内同一工具只提示一次：
// 模型重试同一命令很常见，每条都弹会把提示条刷成噪音。
func TestToolTimeoutNoticeDedupedPerTool(t *testing.T) {
	var mu sync.Mutex
	count := 0
	writer := agentTerminalWriter(func(v interface{}) error {
		raw, _ := json.Marshal(v)
		var f map[string]interface{}
		_ = json.Unmarshal(raw, &f)
		if f["type"] == models.AgentEventAIRunNotice && f["code"] == "tool_timeout" {
			mu.Lock()
			count++
			mu.Unlock()
		}
		return nil
	})

	m := newAgentAIManager()
	run := agentAIRun{sessionID: "s-dedup", messageID: "msg-1"}
	hooks := agentCoreLoopHooks(m, run, &agentCoreDiagnostics{}, writer)

	timedOut := agentcore.ToolResult{Content: "timeout", IsError: true, TimedOut: true}
	for i := 0; i < 3; i++ {
		hooks.OnToolCall("Bash", map[string]interface{}{"command": "find ~"}, timedOut)
	}
	// 另一个工具超时应当再提示一次。
	hooks.OnToolCall("Grep", map[string]interface{}{"pattern": "x"}, timedOut)

	mu.Lock()
	defer mu.Unlock()
	if count != 2 {
		t.Fatalf("期望 2 条 notice（Bash 去重后 1 条 + Grep 1 条），实际 %d", count)
	}
}

// TestNonTimeoutErrorDoesNotNotice 验证普通报错不发超时提示（避免噪音）。
func TestNonTimeoutErrorDoesNotNotice(t *testing.T) {
	var mu sync.Mutex
	count := 0
	writer := agentTerminalWriter(func(v interface{}) error {
		raw, _ := json.Marshal(v)
		var f map[string]interface{}
		_ = json.Unmarshal(raw, &f)
		if f["type"] == models.AgentEventAIRunNotice {
			mu.Lock()
			count++
			mu.Unlock()
		}
		return nil
	})

	m := newAgentAIManager()
	hooks := agentCoreLoopHooks(m, agentAIRun{sessionID: "s-plain", messageID: "m"}, &agentCoreDiagnostics{}, writer)
	hooks.OnToolCall("Bash", map[string]interface{}{"command": "false"}, agentcore.ToolResult{
		Content: "[退出码 1]",
		IsError: true,
	})

	mu.Lock()
	defer mu.Unlock()
	if count != 0 {
		t.Fatalf("普通报错不应发超时提示，实际 %d 条", count)
	}
}
