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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"aliang.one/nursorgate/app/agentcore"
)

type mirrorSpy struct {
	mu     sync.Mutex
	frames []map[string]interface{}
	raw    [][]byte
	act    *agentAIActivity // 可选：断言 begin/endSubagent 配平时注入
	// failEmit 置位时 emit 记录后返回错误（模拟 ctx 守卫先关 emit 管道的
	// 必败通道——TerminalUnsent 兜底路径的测试开关）。
	failEmit bool
}

func (s *mirrorSpy) emit(frame []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raw = append(s.raw, frame)
	var m map[string]interface{}
	if err := json.Unmarshal(frame, &m); err == nil {
		s.frames = append(s.frames, m)
	}
	if s.failEmit {
		return errTestEmitPipeClosed
	}
	return nil
}

// errTestEmitPipeClosed 模拟 io.Pipe 关闭后的写错误（io.ErrClosedPipe 同族）。
var errTestEmitPipeClosed = errors.New("test: emit pipe closed")

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
	// 契约（2026-11-10 ③ 排队可见化）：首帧 queued（排队登记前置）、次帧
	// started、末帧 completed、中间只允许 progress。
	if len(phases) < 3 {
		t.Fatalf("事件数 = %d, 至少 queued+started+completed: %v", len(phases), phases)
	}
	if phases[0] != "queued" || phases[1] != "started" || phases[len(phases)-1] != "completed" {
		t.Fatalf("事件序列异常: %v", phases)
	}
	for _, p := range phases[2 : len(phases)-1] {
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
	if tid, _ := first["task_id"].(string); !strings.HasPrefix(tid, "agentcore-msg-mirror-researcher-") {
		t.Fatalf("task_id 必须带 agentcore 前缀+回合 messageID+桥名（跨回合消歧/并行去串线）: %v", tid)
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
// 2026-11-10 起序列不再恰为 [started failed]：重试活性旁路（vm129 审计）
// 让不可达上游的重试链每次重试都发一条 progress 帧——序列变为
// [queued, started, progress×N, failed]，N≥0（重试次数由内核退避策略定）。
// 契约：首帧 queued（2026-11-10 ③ 排队登记前置）、次帧 started、末帧 failed、
// 中间只允许 progress，且连接类失败至少出现一条
// 「上游重试」progress（旁路的回归守卫——删掉桥的 OnRetry 分支本条即红）。
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
	// 契约（2026-11-10 ③ 排队可见化）：首帧 queued（排队登记前置）、次帧
	// started、末帧 failed、中间只允许 progress，且连接类失败至少出现一条
	// 「上游重试」progress（旁路的回归守卫——删掉桥的 OnRetry 分支本条即红）。
	if len(phases) < 3 || phases[0] != "queued" || phases[1] != "started" || phases[len(phases)-1] != "failed" {
		t.Fatalf("失败路径事件序列 = %v, want [queued, started, progress×N, failed]", phases)
	}
	for i, p := range phases[2 : len(phases)-1] {
		if p != "progress" {
			t.Fatalf("中间帧[%d] = %v, 只允许 progress", i+1, phases)
		}
	}
	// 连接类失败（127.0.0.1:1 拒连）必然走重试链：至少一条重试 progress，
	// current_step 带类别词（connection_error）。
	spy.mu.Lock()
	retrySeen := false
	for _, f := range spy.frames {
		if f["phase"] == "progress" && strings.Contains(fmt.Sprint(f["current_step"]), "上游重试 #") {
			retrySeen = true
		}
	}
	spy.mu.Unlock()
	if !retrySeen {
		t.Fatalf("重试活性旁路缺失：不可达上游的重试链应发「上游重试」progress, phases=%v", phases)
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
		taskID, tracked := mirror.start("researcher", "调研", "任务文本")
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

// newMirrorWithJournalForTest 构造带 journal 的 mirror（emit 不接——本组测试
// 只验落盘，不验帧）。返回 mirror 与 journal 目录。
func newMirrorWithJournalForTest(t *testing.T, sessionID, messageID string) (*agentCoreSubagentMirror, string) {
	t.Helper()
	dir := t.TempDir()
	return &agentCoreSubagentMirror{
		registry:  newAgentAISubagentRegistry(),
		activity:  &agentAIActivity{},
		sessionID: sessionID,
		messageID: messageID,
		journal:   newAgentCoreJournal(dir),
	}, dir
}

// TestAgentCoreSubagentMirrorJournalsLifecycle：tracked 子 run 的生命周期落
// journal——start 行（kind=subagent/event=start）→ 子 run 内置工具关联行
// （kind=tool + subagent_task_id，该路径有 Read 调用）→ finish 行（result=
// 子 run 最终文本）。tracked 模式下子 run 内置工具绝不落无标记 tool 行
// （防双行：原 b.onToolCall 与 appendToolLine 只能走其一）。
func TestAgentCoreSubagentMirrorJournalsLifecycle(t *testing.T) {
	finalText := "调研结论：AES-GCM 为宜"
	toolTurn := `{"type":"message_start","message":{"id":"msg_p","model":"glm-5.2"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"u1","name":"Read"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`
	endTurn := `{"type":"message_start","message":{"id":"msg_e","model":"glm-5.2","usage":{"input_tokens":120}}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + finalText + `"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":120,"output_tokens":45}}`
	up, srv := newAgentCoreUpstream(t, toolTurn, endTurn)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")
	_ = up

	mirror, dir := newMirrorWithJournalForTest(t, "sess-j", "msg-j")
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
	if res := bridge.Execute(context.Background(), map[string]interface{}{"task": "调研加密解密"}, nil); res.IsError {
		t.Fatalf("Execute 失败: %+v", res)
	}

	lines := readJournalLines(t, dir, "sess-j")
	var start, finish map[string]interface{}
	var taggedToolRows []map[string]interface{}
	for _, m := range lines {
		switch m["kind"] {
		case "subagent":
			if m["event"] == "start" {
				start = m
			} else if m["event"] == "finish" {
				finish = m
			}
		case "tool":
			if _, tagged := m["subagent_task_id"]; tagged {
				taggedToolRows = append(taggedToolRows, m)
			} else if m["tool"] == "Read" {
				t.Fatalf("tracked 模式下子 run 内置工具不得落无标记 tool 行（journal 双行）: %v", m)
			}
		}
	}
	if start == nil || finish == nil {
		t.Fatalf("start/finish 行缺失: %v", lines)
	}
	taskID, _ := start["task_id"].(string)
	if !strings.HasPrefix(taskID, "agentcore-msg-j-researcher-") {
		t.Fatalf("journal task_id 缺回合 messageID 消歧: %v", taskID)
	}
	if start["name"] != "researcher" || start["description"] != "加密解密调研" {
		t.Fatalf("start 行身份字段错: %v", start)
	}
	if finish["task_id"] != taskID || finish["status"] != "completed" {
		t.Fatalf("finish 行键错: %v", finish)
	}
	if result, _ := finish["result"].(string); result != finalText {
		t.Fatalf("finish result = %q, want 子 run 最终文本 %q", result, finalText)
	}
	if summary, _ := finish["summary"].(string); summary != finalText {
		t.Fatalf("finish summary = %q, want 最终文本（200 rune 内不截）: %q", summary, finalText)
	}
	if tokens, _ := finish["total_tokens"].(float64); tokens != 165 {
		t.Fatalf("finish total_tokens = %v, want 165", finish["total_tokens"])
	}
	if len(taggedToolRows) == 0 {
		t.Fatalf("缺 subagent_task_id 关联 tool 行（子 run 有 Read 调用）: %v", lines)
	}
	sawRead := false
	for _, row := range taggedToolRows {
		if row["subagent_task_id"] != taskID {
			t.Fatalf("关联行 task_id 不匹配: %v", row)
		}
		if row["tool"] == "Read" {
			sawRead = true
		}
	}
	if !sawRead {
		t.Fatalf("Read 工具的关联行缺失: %v", taggedToolRows)
	}
	// activity 配平不受 journal 接线影响。
	if got := mirror.activity.activeSubagents.Load(); got != 0 {
		t.Fatalf("activeSubagents = %d, want 0", got)
	}
}

// TestAgentCoreSubagentMirrorTaskIDDisambiguatesPerMessage：两个 mirror 实例
// （不同回合 messageID）各 start 同名桥，返回的 taskID 必须不同（spec H1：
// mirror 每回合新建、seq 归零——裸 id 跨回合重号会让读侧聚合串线）。
func TestAgentCoreSubagentMirrorTaskIDDisambiguatesPerMessage(t *testing.T) {
	m1 := newMirrorForTest(t, func([]byte) error { return nil })
	m2 := &agentCoreSubagentMirror{
		registry:  newAgentAISubagentRegistry(),
		activity:  &agentAIActivity{},
		sessionID: "sess-mirror",
		messageID: "msg-other",
	}
	id1, ok1 := m1.start("researcher", "调研", "任务文本")
	id2, ok2 := m2.start("researcher", "调研", "任务文本")
	if !ok1 || !ok2 {
		t.Fatalf("start 未追踪: %q/%v %q/%v", id1, ok1, id2, ok2)
	}
	if id1 == id2 {
		t.Fatalf("跨回合 taskID 重号: %q == %q", id1, id2)
	}
	if !strings.HasPrefix(id1, "agentcore-msg-mirror-researcher-") || !strings.HasPrefix(id2, "agentcore-msg-other-researcher-") {
		t.Fatalf("taskID 形态缺 messageID 段: %q / %q", id1, id2)
	}
}

// TestAgentCoreSubagentMirrorUntrackedNoJournal：registry 拒收（此处 nil 守卫
// 直接返 false）→ tracked=false → journal 必须**零**落盘（无 start/finish/
// 工具行）——拒收的轨迹不写半行，孤儿行比丢轨迹更伤读侧聚合。
func TestAgentCoreSubagentMirrorUntrackedNoJournal(t *testing.T) {
	dir := t.TempDir()
	mirror := &agentCoreSubagentMirror{
		registry:  nil, // 拒收：nil 接收者守卫直接返 false
		activity:  &agentAIActivity{},
		sessionID: "sess-cap",
		messageID: "m",
		journal:   newAgentCoreJournal(dir),
	}
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
	if _, err := os.Stat(filepath.Join(dir, agentCoreJournalFileName("sess-cap"))); !os.IsNotExist(err) {
		t.Fatalf("拒收场景 journal 必须零落盘: err=%v", err)
	}
	if got := mirror.activity.activeSubagents.Load(); got != 0 {
		t.Fatalf("拒收场景不得动 activity 计数: %d", got)
	}
}

// TestTruncateAgentCoreMCPOutputRuneSafe：journal result 的 rune 安全截断——
// 字节封顶回退 UTF-8 边界（绝不送非法字节序列），去标记版。
func TestTruncateAgentCoreMCPOutputRuneSafe(t *testing.T) {
	s := "密钥调研结论：AES-GCM 模式为宜，注意 IV 只用一次。"
	cut := truncateAgentCoreMCPOutputRuneSafe(s, 10)
	if len(cut) > 10 {
		t.Fatalf("截断超字节上限: len=%d", len(cut))
	}
	if !utf8.ValidString(cut) || strings.ContainsRune(cut, utf8.RuneError) {
		t.Fatalf("截断产出非法字节/U+FFFD: %q", cut)
	}
	if !strings.HasPrefix(s, cut) {
		t.Fatalf("截断结果必须是原文前缀: %q", cut)
	}
	// 恰好多字节边界内：6 字节=「中文」完整；5 字节劈开「文」→ 回退到「中」。
	if got := truncateAgentCoreMCPOutputRuneSafe("中文", 6); got != "中文" {
		t.Fatalf("完整多字节串应原样: %q", got)
	}
	if got := truncateAgentCoreMCPOutputRuneSafe("中文", 5); got != "中" {
		t.Fatalf("边界回退错误: %q", got)
	}
	if got := truncateAgentCoreMCPOutputRuneSafe("短文", 100); got != "短文" {
		t.Fatalf("短文应原样: %q", got)
	}
	if got := truncateAgentCoreMCPOutputRuneSafe("短文", 0); got != "短文" {
		t.Fatalf("maxBytes<=0 应原样: %q", got)
	}
	if got := truncateAgentCoreMCPOutputRuneSafe("短文", -5); got != "短文" {
		t.Fatalf("负 maxBytes 应原样: %q", got)
	}
}

func TestAgentCoreSubagentAbandonQueuedUnsentFallback(t *testing.T) {
	// 复核修复（2026-11-10 ③ CONFIRMED P2）：排队期整回合停止/看门狗击杀时
	// ctx 守卫先关 emit 管道，abandonQueued 的终态帧必败（确定性丢失）——
	// 必须置位 TerminalUnsent 交 finalizeRunSubagents 补发，否则面板永卡
	// queued（queued 帧出去时管道还活着）。与 finish 路径的兜底同构。
	spy := &mirrorSpy{failEmit: true}
	mirror := newMirrorForTest(t, spy.emit)
	id, tracked := mirror.queue("researcher", "调研", "任务")
	if !tracked {
		t.Fatalf("queue 应登记成功")
	}
	mirror.abandonQueued(id, "排队中被停止")
	drained := mirror.registry.drainUnsentTerminals()
	if len(drained) != 1 || drained[0].TaskID != id || drained[0].Status != "interrupted" {
		t.Fatalf("emit 失败应置位 TerminalUnsent 并可补发: %+v", drained)
	}
	// 幂等：补发后再次 drain 为空。
	if again := mirror.registry.drainUnsentTerminals(); len(again) != 0 {
		t.Fatalf("TerminalUnsent 应一次性消费: %+v", again)
	}
}
