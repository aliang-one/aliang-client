package services

// ai.subagent 镜像帧的管线透传契约（2026-11-07）。
//
// 背景：agentcore 子agent状态镜像（agentCoreSubagentMirror）把
// ai.subagent 帧写进内核 Emit 管道（io.Pipe → streamAgentAIStdout 解码）。
// 解码器此前没有该 type 的分支——帧被静默丢弃，本地仪表盘 WS 与云端
// （PhoneServer→手机 App SubagentRows）全都收不到：已合并的状态镜像功能
// 实际空转。本文件锁死透传契约：镜像帧必须原样到达 writeJSON
// （下行 = origin 写 + 本地扇出 + 云端，三路各取所需）。
//
// 桥侧（registry/activity 看门狗喂食）已在 mirror 内完成——解码器只做
// 转发，不重复喂钟。

import (
	"bytes"
	"encoding/json"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

func TestAgentCoreMirrorFrameRelayedThroughPipeline(t *testing.T) {
	frame := map[string]interface{}{
		"type":          models.AgentEventAISubagent,
		"session_id":    "sess-relay",
		"message_id":    "msg-relay",
		"task_id":       "agentcore-researcher-1",
		"phase":         "progress",
		"subagent_type": "researcher",
		"current_step":  "工具 #2: Grep",
		"last_tool":     "Grep",
		"tool_uses":     2,
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got []map[string]interface{}
	streamOut := agentTerminalWriter(func(v interface{}) error {
		if m, ok := v.(map[string]interface{}); ok {
			got = append(got, m)
		}
		return nil
	})
	run := agentAIRun{sessionID: "sess-relay", messageID: "msg-relay", runSeq: 1, activity: newAgentAIActivityForRelay()}

	streamStructuredAIDelta(
		bytes.NewReader(append(raw, '\n')),
		agentAIOutputClaudeStreamJSON,
		run, streamOut, nil, nil, nil, &claudeRetryInfo{}, nil,
	)

	if len(got) == 0 {
		t.Fatalf("ai.subagent 帧被解码器丢弃——镜像帧死在管道里，手机/仪表盘全收不到")
	}
	last := got[len(got)-1]
	if last["type"] != models.AgentEventAISubagent {
		t.Fatalf("透传帧 type = %v, want ai.subagent", last["type"])
	}
	if last["task_id"] != "agentcore-researcher-1" || last["phase"] != "progress" {
		t.Fatalf("透传帧字段丢失: %v", last)
	}
	if last["session_id"] != "sess-relay" {
		t.Fatalf("透传帧 session_id = %v, want sess-relay", last["session_id"])
	}
}

func newAgentAIActivityForRelay() *agentAIActivity { return &agentAIActivity{} }
