package services

// agentcore 子 agent 状态镜像（ai.subagent）契约测试（2026-11-07）。
//
// 背景：ai.subagent 生命周期通道此前是 claude 专属——agentcore 子 run 静默
// （Emit/Hooks nil），用户在手机上派 3 个子 agent 调研时什么都看不到（实测
// 2026-11-06）。本文件锁死宿主侧镜像：桥 Execute 经 agentCoreSubagentMirror
// 把 started/progress/completed|failed 以 claude 侧同款 payload
// （agentAISubagentPayload）发进 ai 帧管道，手机端 SubagentRows 直接渲染；
// 同时联动 activity.beginSubagent/endSubagent（idle 暂停+watchdog subagent
// ceiling 对 agentcore 子 run 同样生效）。
//
// 并行前提：并行批内每个桥实例各自持独立 taskID，事件互不串线。

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/app/agentcore"
)

type mirrorSpy struct {
	mu     sync.Mutex
	frames []map[string]interface{}
	raw    [][]byte
	act    *agentAIActivity // 可选：断言 begin/endSubagent 配平时注入
}

func (s *mirrorSpy) emit(frame []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raw = append(s.raw, frame)
	var m map[string]interface{}
	if err := json.Unmarshal(frame, &m); err == nil {
		s.frames = append(s.frames, m)
	}
	return nil
}

func (s *mirrorSpy) phases() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.frames))
	for _, f := range s.frames {
		if t, _ := f["type"].(string); t == "ai.subagent" {
			p, _ := f["phase"].(string)
			out = append(out, p)
		}
	}
	return out
}

func (s *mirrorSpy) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.frames)
}

func newMirrorForTest(t *testing.T, emit func([]byte) error) *agentCoreSubagentMirror {
	t.Helper()
	return &agentCoreSubagentMirror{
		registry:  newAgentAISubagentRegistry(),
		activity:  &agentAIActivity{},
		emit:      emit,
		sessionID: "sess-mirror",
		messageID: "msg-mirror",
	}
}

// TestAgentCoreSubagentMirrorLifecycle：正常路径事件序列
// started → progress（子 run 工具调用驱动，带 last_tool/tool_uses）→
// completed（带 summary/duration），帧形与 claude 侧同款
// （type/session_id/message_id/task_id/phase/subagent_type/...）。
func TestAgentCoreSubagentMirrorLifecycle(t *testing.T) {
	// endTurn 带 usage：子 run token 用量应进 completed 帧（对照 claude 侧
	// progress usage.total_tokens）。
	endTurn := `{"type":"message_start","message":{"id":"msg_e","model":"glm-5.2","usage":{"input_tokens":120}}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"调研结论：AES-GCM 为宜"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":120,"output_tokens":45}}`
	up, srv := newAgentCoreUpstream(t, endTurn)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")
	_ = up

	spy := &mirrorSpy{}
	mirror := newMirrorForTest(t, spy.emit)
	client := agentcore.NewClient(agentCoreBaseURL())
	bridge := &agentCoreSubagentBridge{
		client:      client,
		mainModel:   "glm-5.2",
		name:        "researcher",
		description: "加密解密调研",
		body:        "你是调研员。",
		projectPath: t.TempDir(),
		env:         &agentcore.ToolEnv{ProjectPath: t.TempDir()},
		approve: func(ctx context.Context, call agentcore.ApprovalRequest) (agentcore.ApprovalResponse, error) {
			return agentcore.ApprovalResponse{Approved: true}, nil
		},
		onToolCall: func(tool, summary string, isErr bool) {},
		mirror:     mirror,
	}
	res := bridge.Execute(context.Background(), map[string]interface{}{"task": "调研加密解密"}, nil)
	if res.IsError {
		t.Fatalf("Execute 失败: %+v", res)
	}

	phases := spy.phases()
	if len(phases) < 2 {
		t.Fatalf("事件数 = %d, 至少 started+completed: %v", len(phases), phases)
	}
	if phases[0] != "started" || phases[len(phases)-1] != "completed" {
		t.Fatalf("事件序列异常: %v", phases)
	}
	for _, p := range phases[1 : len(phases)-1] {
		if p != "progress" {
			t.Fatalf("中段只能是 progress: %v", phases)
		}
	}

	// 帧形与 claude 侧同款（逐键）。
	spy.mu.Lock()
	first := spy.frames[0]
	spy.mu.Unlock()
	for _, key := range []string{"type", "session_id", "message_id", "task_id", "phase", "subagent_type", "description", "spawn_depth"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("started 帧缺键 %s: %v", key, first)
		}
	}
	if first["type"] != "ai.subagent" || first["session_id"] != "sess-mirror" || first["message_id"] != "msg-mirror" {
		t.Fatalf("started 帧头错误: %v", first)
	}
	if first["subagent_type"] != "researcher" || first["description"] != "加密解密调研" {
		t.Fatalf("started 帧身份错误: %v", first)
	}
	if tid, _ := first["task_id"].(string); !strings.HasPrefix(tid, "agentcore-researcher-") {
		t.Fatalf("task_id 必须带 agentcore 前缀+桥名（并行去串线）: %v", tid)
	}
	if depth, _ := first["spawn_depth"].(float64); depth != 1 {
		t.Fatalf("spawn_depth = %v, want 1", first["spawn_depth"])
	}

	// 终态帧：summary 带子 run 最终文本。
	spy.mu.Lock()
	last := spy.frames[len(spy.frames)-1]
	spy.mu.Unlock()
	if summary, _ := last["summary"].(string); !strings.Contains(summary, "AES-GCM") {
		t.Fatalf("completed 帧 summary 应含最终文本: %v", last)
	}
	if _, ok := last["duration_ms"]; !ok {
		t.Fatalf("completed 帧应带 duration_ms: %v", last)
	}
	if tokens, _ := last["total_tokens"].(float64); tokens != 165 {
		t.Fatalf("completed 帧 total_tokens = %v, want 165（子 run usage 透传）", last["total_tokens"])
	}

	// activity 联动：begin/end 配平 + progress 喂了 watchdog 进度钟。
	if got := mirror.activity.activeSubagents.Load(); got != 0 {
		t.Fatalf("activeSubagents = %d, want 0（begin/end 必须配平）", got)
	}
}

// TestAgentCoreSubagentMirrorProgressCarriesToolUses：子 run 的工具调用必须
// 驱动 progress 事件（last_tool/tool_uses）——「看到状态」的核心诉求。
func TestAgentCoreSubagentMirrorProgressCarriesToolUses(t *testing.T) {
	toolTurn := `{"type":"message_start","message":{"id":"msg_p","model":"glm-5.2"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"u1","name":"Read"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`
	endTurn := agentCoreTextTurn("done", "")
	up, srv := newAgentCoreUpstream(t, toolTurn, endTurn)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")
	_ = up

	spy := &mirrorSpy{}
	mirror := newMirrorForTest(t, spy.emit)
	bridge := &agentCoreSubagentBridge{
		client:      agentcore.NewClient(agentCoreBaseURL()),
		mainModel:   "glm-5.2",
		name:        "researcher",
		description: "调研",
		body:        "x",
		projectPath: t.TempDir(),
		env:         &agentcore.ToolEnv{ProjectPath: t.TempDir()},
		approve: func(ctx context.Context, call agentcore.ApprovalRequest) (agentcore.ApprovalResponse, error) {
			return agentcore.ApprovalResponse{Approved: true}, nil
		},
		onToolCall: func(tool, summary string, isErr bool) {},
		mirror:     mirror,
	}
	if res := bridge.Execute(context.Background(), map[string]interface{}{"task": "干活"}, nil); res.IsError {
		t.Fatalf("Execute 失败: %+v", res)
	}

	spy.mu.Lock()
	var progress map[string]interface{}
	for _, f := range spy.frames {
		if p, _ := f["phase"].(string); p == "progress" {
			progress = f
			break
		}
	}
	spy.mu.Unlock()
	if progress == nil {
		t.Fatalf("子 run 工具调用必须驱动 progress 事件: %d 帧", len(spy.frames))
	}
	if lastTool, _ := progress["last_tool"].(string); lastTool != "Read" {
		t.Fatalf("progress last_tool = %v, want Read", progress["last_tool"])
	}
	if uses, _ := progress["tool_uses"].(float64); uses < 1 {
		t.Fatalf("progress tool_uses = %v, want >=1", progress["tool_uses"])
	}
	if step, _ := progress["current_step"].(string); !strings.Contains(step, "Read") {
		t.Fatalf("progress current_step 应含工具名: %v", progress["current_step"])
	}
	if mirror.activity.lastProgressAt.Load() == 0 {
		t.Fatalf("progress 必须喂 subagentProgress 进度钟（长任务防 no-progress 误杀）")
	}
}

// TestAgentCoreSubagentMirrorFailedPath：子 run 失败 → failed 终态帧。
func TestAgentCoreSubagentMirrorFailedPath(t *testing.T) {
	spy := &mirrorSpy{}
	mirror := newMirrorForTest(t, spy.emit)
	bridge := &agentCoreSubagentBridge{
		client:    agentcore.NewClient("http://127.0.0.1:1"),
		mainModel: "glm-5.2",
		name:      "researcher",
		body:      "x",
		mirror:    mirror,
	}
	res := bridge.Execute(context.Background(), map[string]interface{}{"task": "调研"}, nil)
	if !res.IsError {
		t.Fatalf("不可达上游应失败")
	}
	phases := spy.phases()
	if len(phases) != 2 || phases[0] != "started" || phases[1] != "failed" {
		t.Fatalf("失败路径事件序列 = %v, want [started failed]", phases)
	}
}

// TestAgentCoreSubagentMirrorNilIsNoop：mirror=nil（宿主未接线/旧路径）必须
// 完全无感——不炸、不发。
func TestAgentCoreSubagentMirrorNilIsNoop(t *testing.T) {
	up, srv := newAgentCoreUpstream(t, agentCoreTextTurn("ok", ""))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")
	_ = up
	bridge := &agentCoreSubagentBridge{
		client:    agentcore.NewClient(agentCoreBaseURL()),
		mainModel: "glm-5.2",
		name:      "r",
		body:      "x",
	}
	res := bridge.Execute(context.Background(), map[string]interface{}{"task": "t"}, nil)
	if res.IsError {
		t.Fatalf("nil mirror 不应影响执行: %+v", res)
	}
}

// TestAgentCoreSubagentMirrorParallelTaskIDs：并行批里每个桥实例的 taskID
// 互不相同（三内核同款去串线；实测场景=一次派 3 个子 agent）。
func TestAgentCoreSubagentMirrorParallelTaskIDs(t *testing.T) {
	mirror := newMirrorForTest(t, func([]byte) error { return nil })
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		taskID, tracked := mirror.start("researcher", "调研")
		if !tracked || taskID == "" {
			t.Fatalf("start 未追踪: %q %v", taskID, tracked)
		}
		if seen[taskID] {
			t.Fatalf("taskID 重复: %q", taskID)
		}
		seen[taskID] = true
	}
	mirror.finish("researcher", seenSlice(seen)[0], "completed", "done", 10*time.Millisecond, 165)
}

func seenSlice(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
