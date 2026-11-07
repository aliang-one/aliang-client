package services

// journal 子agent行读侧测试（spec subagent-panel §二）：kind=subagent
// start/finish 行组装成 Subagents[]，tool 行 subagent_task_id 归组不进主转录。

import (
	"encoding/json"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

// writeSubagentRowsFixture 造一个含两轮各派一个子agent的 journal：
// user → start A → tool A1 → start B → tool B1 → tool A2 → finish A → finish B → assistant。
func writeSubagentRowsFixture(t *testing.T, sessionID string) string {
	t.Helper()
	return writeCoreJournalRawFixture(t, sessionID,
		`{"kind":"session","session_id":"`+sessionID+`","project_path":"/tmp/p","model":"m","created_at":"2026-11-07T01:00:00Z"}`,
		`{"kind":"msg","role":"user","message_id":"m1","text":"探索一下","ts":"2026-11-07T01:00:04Z"}`,
		`{"kind":"subagent","event":"start","task_id":"agentcore-m1-explorer-1","name":"explorer","description":"探索代码","ts":"2026-11-07T01:00:05Z"}`,
		`{"kind":"tool","subagent_task_id":"agentcore-m1-explorer-1","tool":"Read","summary":"main.go","is_error":false,"ts":"2026-11-07T01:00:06Z"}`,
		`{"kind":"subagent","event":"start","task_id":"agentcore-m2-planner-1","name":"planner","description":"规划方案","ts":"2026-11-07T01:00:07Z"}`,
		`{"kind":"tool","subagent_task_id":"agentcore-m2-planner-1","tool":"Grep","summary":"pattern=foo","is_error":false,"ts":"2026-11-07T01:00:08Z"}`,
		`{"kind":"tool","subagent_task_id":"agentcore-m1-explorer-1","tool":"Bash","summary":"ls","is_error":true,"ts":"2026-11-07T01:00:09Z"}`,
		`{"kind":"subagent","event":"finish","task_id":"agentcore-m1-explorer-1","status":"completed","summary":"探索完成","result":"结论全文","total_tokens":123,"duration_ms":456,"ts":"2026-11-07T01:00:10Z"}`,
		`{"kind":"subagent","event":"finish","task_id":"agentcore-m2-planner-1","status":"failed","summary":"规划失败","result":"失败全文","total_tokens":789,"duration_ms":321,"ts":"2026-11-07T01:00:11Z"}`,
		`{"kind":"msg","role":"assistant","message_id":"a1","text":"回答","ts":"2026-11-07T01:00:12Z"}`,
	)
}

func TestReadAgentCoreSubagentRowsAssemble(t *testing.T) {
	setTestAgentHome(t)
	path := writeSubagentRowsFixture(t, "sess-sub-asm")
	session := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: agentVibeTranscriptMaxMessages})
	if len(session.Subagents) != 2 {
		t.Fatalf("Subagents = %d 条, want 2: %+v", len(session.Subagents), session.Subagents)
	}
	a, b := session.Subagents[0], session.Subagents[1]
	// order 切片按行序（=时间序）输出：A 先 start 先排前。
	if a.TaskID != "agentcore-m1-explorer-1" || b.TaskID != "agentcore-m2-planner-1" {
		t.Fatalf("Subagents 顺序错乱: [%+v, %+v]", a, b)
	}
	if a.Status != "completed" || b.Status != "failed" {
		t.Fatalf("Status: %q / %q, want completed / failed", a.Status, b.Status)
	}
	if a.Name != "explorer" || a.Description != "探索代码" {
		t.Fatalf("A start 字段: %+v", a)
	}
	if a.Summary != "探索完成" || a.Result != "结论全文" || a.TotalTokens != 123 || a.DurationMs != 456 {
		t.Fatalf("A finish 字段: %+v", a)
	}
	if a.StartedAt != "2026-11-07T01:00:05Z" || a.EndedAt != "2026-11-07T01:00:10Z" {
		t.Fatalf("A 时间戳: %q / %q", a.StartedAt, a.EndedAt)
	}
	if b.Summary != "规划失败" || b.Result != "失败全文" || b.TotalTokens != 789 || b.DurationMs != 321 {
		t.Fatalf("B finish 字段: %+v", b)
	}
	// start+finish 同 task_id 合并为同一实例（读侧按 task_id 键合并是有意语义）。
	if len(a.Tools) != 2 || len(b.Tools) != 1 {
		t.Fatalf("Tools 归组: A=%d B=%d, want 2 / 1", len(a.Tools), len(b.Tools))
	}
	// 工具行按文件序（时间序）归组，IsError 忠实透传。
	if a.Tools[0].Tool != "Read" || a.Tools[0].IsError || a.Tools[0].Ts != "2026-11-07T01:00:06Z" {
		t.Fatalf("A.Tools[0]: %+v", a.Tools[0])
	}
	if a.Tools[1].Tool != "Bash" || !a.Tools[1].IsError || a.Tools[1].Ts != "2026-11-07T01:00:09Z" {
		t.Fatalf("A.Tools[1]: %+v", a.Tools[1])
	}
	if b.Tools[0].Tool != "Grep" || b.Tools[0].IsError || b.Tools[0].Ts != "2026-11-07T01:00:08Z" {
		t.Fatalf("B.Tools[0]: %+v", b.Tools[0])
	}
	// subagent 行与其工具行都不进主转录：只有 user/assistant 两条，Index 连续。
	if session.MessageCount != 2 {
		t.Fatalf("MessageCount = %d, want 2", session.MessageCount)
	}
	if len(session.Transcript) != 2 || session.Transcript[0].Role != "user" || session.Transcript[1].Role != "assistant" {
		t.Fatalf("Transcript = %+v, want 仅 [user, assistant]", session.Transcript)
	}
	for i, msg := range session.Transcript {
		if msg.Index != i {
			t.Fatalf("Transcript[%d].Index = %d, want %d（子agent行不占转录序号）", i, msg.Index, i)
		}
	}
}

func TestReadAgentCoreNoSubagentRowsOldFormat(t *testing.T) {
	setTestAgentHome(t)
	path := writeCoreJournalRawFixture(t, "sess-sub-old",
		`{"kind":"session","session_id":"sess-sub-old","project_path":"/tmp/p","model":"m","created_at":"2026-11-07T01:00:00Z"}`,
		`{"kind":"msg","role":"user","message_id":"u1","text":"旧格式","ts":"2026-11-07T01:00:05Z"}`,
		`{"kind":"msg","role":"assistant","message_id":"a1","text":"旧回答","ts":"2026-11-07T01:00:06Z"}`,
	)
	session := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: agentVibeTranscriptMaxMessages})
	if len(session.Subagents) != 0 {
		t.Fatalf("旧格式文件 Subagents = %d 条, want 0: %+v", len(session.Subagents), session.Subagents)
	}
}

func TestReadAgentCoreSubagentSameNameCrossTurnsNoMerge(t *testing.T) {
	setTestAgentHome(t)
	// 同名子agent跨两回合各派一次：task_id 各含各自回合 messageID（写侧保证
	// 唯一），读侧绝不合并。
	path := writeCoreJournalRawFixture(t, "sess-sub-xturn",
		`{"kind":"session","session_id":"sess-sub-xturn","project_path":"/tmp/p","model":"m","created_at":"2026-11-07T01:00:00Z"}`,
		`{"kind":"msg","role":"user","message_id":"m1","text":"第一回合","ts":"2026-11-07T01:00:04Z"}`,
		`{"kind":"subagent","event":"start","task_id":"agentcore-m1-explorer-1","name":"explorer","description":"回合1","ts":"2026-11-07T01:00:05Z"}`,
		`{"kind":"subagent","event":"finish","task_id":"agentcore-m1-explorer-1","status":"completed","summary":"s1","ts":"2026-11-07T01:00:06Z"}`,
		`{"kind":"msg","role":"user","message_id":"m2","text":"第二回合","ts":"2026-11-07T01:00:10Z"}`,
		`{"kind":"subagent","event":"start","task_id":"agentcore-m2-explorer-1","name":"explorer","description":"回合2","ts":"2026-11-07T01:00:11Z"}`,
		`{"kind":"subagent","event":"finish","task_id":"agentcore-m2-explorer-1","status":"failed","summary":"s2","ts":"2026-11-07T01:00:12Z"}`,
	)
	session := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: agentVibeTranscriptMaxMessages})
	if len(session.Subagents) != 2 {
		t.Fatalf("同名跨回合 Subagents = %d 条, want 2: %+v", len(session.Subagents), session.Subagents)
	}
	if session.Subagents[0].TaskID == session.Subagents[1].TaskID {
		t.Fatalf("task_id 撞号（跨回合必须唯一）: %q", session.Subagents[0].TaskID)
	}
	if session.Subagents[0].Description != "回合1" || session.Subagents[1].Description != "回合2" {
		t.Fatalf("跨回合实例串扰: %+v", session.Subagents)
	}
	// 只有 start 无 finish 的 entry 保持 Status=started。
	path = writeCoreJournalRawFixture(t, "sess-sub-nofinish",
		`{"kind":"session","session_id":"sess-sub-nofinish","project_path":"/tmp/p","model":"m","created_at":"2026-11-07T01:00:00Z"}`,
		`{"kind":"msg","role":"user","message_id":"m1","text":"还没完","ts":"2026-11-07T01:00:04Z"}`,
		`{"kind":"subagent","event":"start","task_id":"agentcore-m1-hang-1","name":"hang","description":"未终态","ts":"2026-11-07T01:00:05Z"}`,
	)
	session = readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: agentVibeTranscriptMaxMessages})
	if len(session.Subagents) != 1 || session.Subagents[0].Status != "started" {
		t.Fatalf("缺 finish 的 entry 应保持 started: %+v", session.Subagents)
	}
}

func TestReadAgentCoreSubagentToolRowsKeptOutOfTranscript(t *testing.T) {
	setTestAgentHome(t)
	path := writeCoreJournalRawFixture(t, "sess-sub-tools",
		`{"kind":"session","session_id":"sess-sub-tools","project_path":"/tmp/p","model":"m","created_at":"2026-11-07T01:00:00Z"}`,
		`{"kind":"msg","role":"user","message_id":"m1","text":"混合工具","ts":"2026-11-07T01:00:04Z"}`,
		`{"kind":"subagent","event":"start","task_id":"agentcore-m1-explorer-1","name":"explorer","description":"d","ts":"2026-11-07T01:00:05Z"}`,
		// 主会话工具行（无 subagent_task_id）：现行为不变，进转录。
		`{"kind":"tool","tool":"Bash","summary":"主工具","is_error":false,"ts":"2026-11-07T01:00:06Z"}`,
		// 子agent工具行 ×2：归组进 entry.Tools，不进转录。
		`{"kind":"tool","subagent_task_id":"agentcore-m1-explorer-1","tool":"Read","summary":"子工具1","is_error":false,"ts":"2026-11-07T01:00:07Z"}`,
		`{"kind":"tool","subagent_task_id":"agentcore-m1-explorer-1","tool":"Grep","summary":"子工具2","is_error":true,"ts":"2026-11-07T01:00:08Z"}`,
		// 未知 task_id 的子agent工具行（start 行写失败的边缘形态）：不进转录也不落地。
		`{"kind":"tool","subagent_task_id":"agentcore-ghost-1","tool":"Write","summary":"孤儿","is_error":false,"ts":"2026-11-07T01:00:09Z"}`,
	)
	session := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: agentVibeTranscriptMaxMessages})
	toolMsgs := 0
	for _, msg := range session.Transcript {
		if msg.Role == "tool" {
			toolMsgs++
			if msg.Content != "Bash 主工具" {
				t.Fatalf("主工具行渲染被改: %+v", msg)
			}
		}
	}
	if toolMsgs != 1 {
		t.Fatalf("转录 tool 消息 = %d 条, want 1（子agent工具行不得混流）: %+v", toolMsgs, session.Transcript)
	}
	if len(session.Subagents) != 1 || len(session.Subagents[0].Tools) != 2 {
		t.Fatalf("子agent工具归组: %+v", session.Subagents)
	}
	if session.Subagents[0].Tools[0].Summary != "子工具1" || !session.Subagents[0].Tools[1].IsError {
		t.Fatalf("子agent工具字段: %+v", session.Subagents[0].Tools)
	}
}

// 清单/同步路径剥离（评审 H2）：summarizeAgentVibeSessions 出口必须把
// Subagents 置 nil——详情契约 ≠ 清单契约。
func TestSummarizeAgentVibeSessionsStripsSubagents(t *testing.T) {
	sessions := []models.AgentVibeSession{{
		ID: "sess-strip",
		Subagents: []models.AgentVibeSubagent{{
			TaskID: "agentcore-m1-explorer-1",
			Result: strings.Repeat("x", 64*1024),
		}},
	}}
	summaries := summarizeAgentVibeSessions(sessions)
	if len(summaries) != 1 || summaries[0].Subagents != nil {
		t.Fatalf("清单出口未剥离 Subagents: %+v", summaries)
	}
	blob, err := json.Marshal(summaries)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), `"subagents"`) {
		t.Fatalf("清单序列化不得含 subagents 键: %s", string(blob[:min(len(blob), 200)]))
	}
}
