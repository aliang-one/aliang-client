package services

// 2026-11-05 子 agent 审计盲点修复的宿主侧契约测试（盲点 1 宿主半/3/5 + 并行
// 前提的审批互斥）：
//
//   - 盲点 1（宿主半）：子 agent 桥实现 agentcore.TimeoutHint——桥的 Execute
//     是一整段子 run，默认 10 分钟窗口会把子 run 里等手机审批（SLA 24h）连坐
//     掐死。上限与 claude 侧看门狗共用同一把旋钮
//     ALIANG_AI_SUBAGENT_WAIT_CEILING（默认 6h）。
//   - 并行前提：桥声明 agentcore.Parallelizable=true；内核会并发执行同批桥，
//     因此 agentCoreSubagentTools 必须把审批闭包互斥化——两子 run 同时到审批
//     门时串行进闸（手机审批卡是按单卡语义设计的，不能并发弹两张打架）。
//   - 盲点 3：技能清单块注入子 run System（渐进披露对子 agent 生效——子
//     registry 有 Read，模型照清单路径读 SKILL.md 即可）。
//   - 盲点 5：task 入参字节封顶（rune 安全），防超长 task 直接砸穿子 run
//     首轮请求。

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"aliang.one/nursorgate/app/agentcore"
)

// --- 盲点 1 宿主半：超时覆盖 ---

func TestAgentCoreSubagentToolCeiling(t *testing.T) {
	t.Setenv("ALIANG_AI_SUBAGENT_WAIT_CEILING", "30m")
	if got := agentCoreSubagentToolCeiling(); got != 30*time.Minute {
		t.Fatalf("env 覆盖 = %v, want 30m", got)
	}

	t.Setenv("ALIANG_AI_SUBAGENT_WAIT_CEILING", "0")
	if got := agentCoreSubagentToolCeiling(); got <= 0 {
		t.Fatalf("0（关）不得产生无上限或零窗口, got %v——必须回落默认", got)
	}

	t.Setenv("ALIANG_AI_SUBAGENT_WAIT_CEILING", "")
	if got := agentCoreSubagentToolCeiling(); got != agentAISubagentWaitCeiling {
		t.Fatalf("未设置应回落 claude 侧同一默认 = %v, got %v", agentAISubagentWaitCeiling, got)
	}

	t.Setenv("ALIANG_AI_SUBAGENT_WAIT_CEILING", "not-a-duration")
	if got := agentCoreSubagentToolCeiling(); got != agentAISubagentWaitCeiling {
		t.Fatalf("非法值应回落默认 = %v, got %v", agentAISubagentWaitCeiling, got)
	}
}

func TestAgentCoreSubagentBridgeImplementsHintAndParallel(t *testing.T) {
	var hint agentcore.TimeoutHint = &agentCoreSubagentBridge{name: "r", toolCeiling: 6 * time.Hour}
	if got := hint.TimeoutHint(); got != 6*time.Hour {
		t.Fatalf("TimeoutHint = %v, want 6h", got)
	}
	var par agentcore.Parallelizable = &agentCoreSubagentBridge{name: "r"}
	if !par.Parallelizable() {
		t.Fatalf("子 agent 桥必须声明可并行（否则内核批处理永远不并发）")
	}
}

// TestAgentCorePeerExecBridgeTimeoutHint：peer.exec 桥的内核窗口必须是
// agentPeerToolTimeoutMs（600s clamp + 30s 网络宽限）——默认 600s 窗口会把
// 满档超时请求在窗口边缘掐掉，拿不到结构化 timeout 结果。
func TestAgentCorePeerExecBridgeTimeoutHint(t *testing.T) {
	m := &agentAIManager{coreJournal: newAgentCoreJournal(t.TempDir())}
	tools := m.agentCorePeerTools(agentAIRun{sessionID: "s-hint"})
	if len(tools) != 5 {
		t.Fatalf("peer tools = %d, want 5", len(tools))
	}
	found := false
	for _, tool := range tools {
		if tool.Name() != "peer.exec" {
			continue
		}
		found = true
		h, ok := tool.(agentcore.TimeoutHint)
		if !ok {
			t.Fatalf("peer.exec 桥必须实现 agentcore.TimeoutHint")
		}
		// 与 630s 字面比较（而非与 agentPeerToolTimeoutMs 自比——同一单位
		// 错误会恒绿，2026-11-06 评审 C1）；>=time.Minute 防单位回归。
		if got := h.TimeoutHint(); got != 630*time.Second {
			t.Fatalf("peer.exec TimeoutHint = %v, want 630s", got)
		}
		if got := h.TimeoutHint(); got < time.Minute {
			t.Fatalf("TimeoutHint 疑似单位错误（毫秒直赋 Duration）: %v", got)
		}
	}
	if !found {
		t.Fatalf("注册表里没有 peer.exec 桥")
	}
}

// --- 盲点 3：技能清单进子 run System ---

func TestAgentCoreSubagentSubSystem(t *testing.T) {
	skills := "\n\n<available_skills>\n- demo: 演示技能 (read /home/u/.claude/skills/demo/SKILL.md)\n</available_skills>\n"
	b := &agentCoreSubagentBridge{name: "r", body: "你是评审员。", skillsBlock: skills}
	sys := b.subSystem()
	if !strings.Contains(sys, "你是评审员。") || !strings.Contains(sys, "SKILL.md") {
		t.Fatalf("子 run System 必须同时含定义正文与技能清单: %q", sys)
	}
	if iSkills := strings.Index(sys, "available_skills"); iSkills >= 0 {
		if iBody := strings.Index(sys, "评审员"); iBody >= 0 && iBody < iSkills {
			t.Fatalf("技能清单应位于定义正文之前（先看清单再看专项指令）: %q", sys)
		}
	}
	// 空 skillsBlock 与历史拼接逐字节等价（prompt + "\n\n" + body）。
	plain := (&agentCoreSubagentBridge{name: "r", body: "x"}).subSystem()
	if plain != agentCoreSystemPrompt("")+"\n\nx" {
		t.Fatalf("空技能块必须与历史拼接等价: %q", plain)
	}
}

// --- 盲点 5：task 字节封顶 ---

func TestTruncateAgentCoreSubagentTask(t *testing.T) {
	short := "评审 main.go"
	if got := truncateAgentCoreSubagentTask(short, 64*1024); got != short {
		t.Fatalf("短文必须原样: %q", got)
	}
	exact := strings.Repeat("a", 64*1024)
	if got := truncateAgentCoreSubagentTask(exact, 64*1024); got != exact {
		t.Fatalf("恰好达上限必须原样")
	}
	// 超限 ASCII：截到上限内 + 标记 + 合法 UTF-8。
	big := strings.Repeat("a", 64*1024+123)
	got := truncateAgentCoreSubagentTask(big, 64*1024)
	if !strings.Contains(got, "传输层截断") || !strings.Contains(got, fmt.Sprint(len(big))) {
		t.Fatalf("标记缺失或未含原始长度: tail=%q", got[max(0, len(got)-120):])
	}
	if !utf8.ValidString(got) {
		t.Fatalf("截断结果必须是合法 UTF-8")
	}
	// CJK 多字节：不能劈开字符（劈开=非法 UTF-8 直接送上游）。
	cjk := strings.Repeat("汉", 50000) // 150KB
	cut := truncateAgentCoreSubagentTask(cjk, 64*1024)
	if !utf8.ValidString(cut) {
		t.Fatalf("CJK 截断劈开了多字节字符")
	}
	if strings.Contains(cut, "�") {
		t.Fatalf("CJK 截断不得留下替换符: %q", cut[len(cut)-200:])
	}
}

// TestAgentCoreSubagentBridgeTruncatesTaskToUpstream 行为级验证：超长 task
// 真正发给子 run 上游的，是截断后的版本。
func TestAgentCoreSubagentBridgeTruncatesTaskToUpstream(t *testing.T) {
	up, srv := newAgentCoreUpstream(t, agentCoreTextTurn("ok", ""))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")
	_ = up

	client := agentcore.NewClient(agentCoreBaseURL())
	bridge := &agentCoreSubagentBridge{client: client, mainModel: "glm-5.2", name: "r", body: "x"}
	big := strings.Repeat("长", 40*1024) // 120KB CJK
	res := bridge.Execute(context.Background(), map[string]interface{}{"task": big}, nil)
	if res.IsError {
		t.Fatalf("截断后的 task 应正常执行: %+v", res)
	}
	reqs := up.requests()
	if len(reqs) == 0 || len(reqs[0].Messages) == 0 {
		t.Fatalf("上游未收到请求: %d", len(reqs))
	}
	sent := fmt.Sprintf("%v", reqs[0].Messages[0].Content)
	if len(sent) >= len(big) {
		t.Fatalf("上游收到的是原始超长 task（len=%d）,截断未生效", len(sent))
	}
	if !strings.Contains(sent, "传输层截断") {
		t.Fatalf("发送内容缺少截断标记: tail=%q", sent[max(0, len(sent)-200):])
	}
}

// --- 并行前提：审批闭包互斥 ---

// TestAgentCoreSubagentApprovesSerializedAcrossSubruns：两个子 run 并发执行、
// 各自的工具调用都到审批门时，审批闭包必须串行进闸（并发峰值=1）。
// 内核并行批会让桥并发跑，桥共用 agentCoreSubagentTools 互斥化后的同一份
// approve——没有互斥，手机审批卡会并发弹两张。
func TestAgentCoreSubagentApprovesSerializedAcrossSubruns(t *testing.T) {
	toolTurn := `{"type":"message_start","message":{"id":"msg_a","model":"glm-5.2"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"u1","name":"Read"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`
	endTurn := agentCoreTextTurn("done", "")
	up, srv := newAgentCoreUpstream(t, toolTurn, endTurn, toolTurn, endTurn)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")

	var cur, peak atomic.Int32
	rawApprove := func(_ context.Context, _ agentcore.ApprovalRequest) (agentcore.ApprovalResponse, error) {
		c := cur.Add(1)
		for {
			old := peak.Load()
			if c <= old || peak.CompareAndSwap(old, c) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond) // 拉开窗口，无互斥必然撞出峰值 2
		cur.Add(-1)
		return agentcore.ApprovalResponse{Approved: true}, nil
	}

	setTestAgentHome(t)
	writeSubagentDef(t, os.Getenv("HOME"), "a.md", "---\nname: sub-a\n---\nx")
	writeSubagentDef(t, os.Getenv("HOME"), "b.md", "---\nname: sub-b\n---\nx")

	m := newAgentAIManager()
	client := agentcore.NewClient(agentCoreBaseURL())
	// 互斥来自 agentCoreApprovalGate（runAgentCore 对 kernelApprove 统一
	// wrap；agentCoreSubagentTools 不再自包）——测试直接用同一道门。
	gate := newAgentCoreApprovalGate()
	tools := m.agentCoreSubagentTools(agentAIRun{sessionID: "s-approve", projectPath: os.Getenv("HOME"), model: "glm-5.2"},
		client, nil, gate.wrap(rawApprove), nil, nil, nil, nil)
	// 原生派发面（2026-11-09）：唯一 task 路由器，两次并发调用以 subagent_type
	// 选不同角色——审批互斥属性不变（同一道 gate 罩两个并发子 run）。
	if len(tools) != 1 || tools[0].Name() != "task" {
		t.Fatalf("应为唯一 task 路由器, got %+v", tools)
	}
	router := tools[0]

	var wg sync.WaitGroup
	wg.Add(2)
	results := make([]string, 2)
	started := time.Now()
	for i, typ := range []string{"sub-a", "sub-b"} {
		go func(i int, typ string) {
			defer wg.Done()
			res := router.Execute(context.Background(), map[string]interface{}{"subagent_type": typ, "task": "干活"}, nil)
			results[i] = fmt.Sprintf("isErr=%v content=%.80q", res.IsError, res.Content)
		}(i, typ)
	}
	wg.Wait()
	elapsed := time.Since(started)
	t.Logf("execute 结果: %v", results)
	t.Logf("上游请求数: %d, 审批总耗时: %v", len(up.requests()), elapsed)

	if got := peak.Load(); got != 1 {
		t.Fatalf("审批并发峰值 = %d, want 1（gate 必须串行化审批闭包）", got)
	}
	if n := len(up.requests()); n != 4 {
		t.Fatalf("上游请求数 = %d, want 4（两个子 run 各两回合）", n)
	}
	// C9：峰值计数是概率性检出（并发窗口 40ms），补时间下限——互斥成立时
	// 两次审批严格串行，总耗时必须 ≥ 2×sleep（留 30ms 调度余量）；无互斥
	// 时两段 sleep 重叠，总耗时会显著低于该下限。
	if elapsed < 2*40*time.Millisecond-30*time.Millisecond {
		t.Fatalf("审批总耗时 %v 低于串行下限 50ms——互斥可能失效（sleep 未叠加）", elapsed)
	}
}

// TestAgentCoreSubagentsParallelViaKernelLoop 端到端（2026-11-06 评审 C3）：
// 两个**真实子 agent 桥**装进同一 Registry，经内核 agentcore.Run 的并行批
// （Parallelizable）同时跑——现有审批互斥测试是直调桥 Execute、绕过了内核
// 调度；本测试用 -race 锁住真实共享面（同一 client/env/桥实例经
// executeTools→executeParallelBatch），并验证主层审批（对桥工具本身）与子
// run 内部审批共用同一道 gate、峰值=1。
//
// 上游脚本对任何请求交错都正确：req0=主 run 双 tool_use；req1/req2（两子
// run 的首轮）都是 toolTurn；req3/req4（两子 run 的次轮）都是 endTurn。
func TestAgentCoreSubagentsParallelViaKernelLoop(t *testing.T) {
	// 原生派发面（2026-11-09）：同一 task 工具连调两次、subagent_type 选角
	// ——并行批语义不变（相邻 Parallelizable 调用并发）。
	dualTurn := `{"type":"message_start","message":{"id":"msg_dual","model":"glm-5.2"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"u-a","name":"task"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"subagent_type\":\"sub-a\",\"task\":\"A 任务\"}"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"u-b","name":"task"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"subagent_type\":\"sub-b\",\"task\":\"B 任务\"}"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":1}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`
	toolTurn := `{"type":"message_start","message":{"id":"msg_t","model":"glm-5.2"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"u1","name":"Read"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`
	endTurn := agentCoreTextTurn("done", "")
	up, srv := newAgentCoreUpstream(t, dualTurn, toolTurn, toolTurn, endTurn, endTurn)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")

	var cur, peak, calls atomic.Int32
	rawApprove := func(_ context.Context, _ agentcore.ApprovalRequest) (agentcore.ApprovalResponse, error) {
		calls.Add(1)
		c := cur.Add(1)
		for {
			old := peak.Load()
			if c <= old || peak.CompareAndSwap(old, c) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		cur.Add(-1)
		return agentcore.ApprovalResponse{Approved: true}, nil
	}

	setTestAgentHome(t)
	writeSubagentDef(t, os.Getenv("HOME"), "a.md", "---\nname: sub-a\n---\nx")
	writeSubagentDef(t, os.Getenv("HOME"), "b.md", "---\nname: sub-b\n---\nx")

	m := newAgentAIManager()
	client := agentcore.NewClient(agentCoreBaseURL())
	gate := newAgentCoreApprovalGate()
	// 状态镜像接线（2026-11-07）：并行批内每个桥实例独立 taskID,started/
	// progress/completed 帧齐发——实测场景「一次派 N 个子 agent」手机可见。
	visSpy := &mirrorSpy{act: &agentAIActivity{}}
	visMirror := &agentCoreSubagentMirror{
		registry:  newAgentAISubagentRegistry(),
		activity:  visSpy.act,
		emit:      visSpy.emit,
		sessionID: "s-kernel-parallel",
		messageID: "msg-kernel-parallel",
	}
	bridges := m.agentCoreSubagentTools(
		agentAIRun{sessionID: "s-kernel-parallel", projectPath: t.TempDir(), model: "glm-5.2"},
		client, &agentcore.ToolEnv{ProjectPath: t.TempDir()}, gate.wrap(rawApprove), nil, nil, visMirror, nil)
	if len(bridges) != 1 || bridges[0].Name() != "task" {
		t.Fatalf("应为唯一 task 路由器, got %+v", bridges)
	}
	registry := agentcore.NewRegistry()
	for _, b := range bridges {
		registry.Register(b)
	}

	cfg := &agentcore.Config{
		Client:  client,
		Model:   "glm-5.2",
		System:  "测试",
		Tools:   registry,
		Env:     &agentcore.ToolEnv{ProjectPath: t.TempDir()},
		Approve: gate.wrap(rawApprove),
		Logf:    func(string, ...interface{}) {},
	}
	res, err := agentcore.Run(context.Background(), cfg, []agentcore.Message{{Role: "user", Content: "并行干两件事"}})
	if err != nil {
		t.Fatalf("主 run 失败: %v", err)
	}
	_ = res

	// 6 = 主 run 回合1（dual）+ 两子 run 首轮（toolTurn×2）+ 两子 run 次轮
	//（endTurn×2）+ 主 run 回合2（脚本用尽，upstream 默认 end_turn 收尾）。
	if n := len(up.requests()); n != 6 {
		t.Fatalf("上游请求数 = %d, want 6", n)
	}
	if got := peak.Load(); got != 1 {
		t.Fatalf("审批并发峰值 = %d, want 1（主层+子层共用 gate）", got)
	}
	// 4 次：主层对 sub-a/sub-b 两个桥工具的审批 + 两个子 run 内 Read 的审批。
	if got := calls.Load(); got != 4 {
		t.Fatalf("审批调用数 = %d, want 4", got)
	}

	// 状态镜像：两个桥各自 started+completed，taskID 互异，无 progress 缺位。
	visSpy.mu.Lock()
	started, completed := 0, 0
	taskIDs := map[string]bool{}
	for _, f := range visSpy.frames {
		if t2, _ := f["type"].(string); t2 != "ai.subagent" {
			continue
		}
		switch p, _ := f["phase"].(string); p {
		case "started":
			started++
			if tid, _ := f["task_id"].(string); tid != "" {
				taskIDs[tid] = true
			}
		case "completed":
			completed++
		}
	}
	visSpy.mu.Unlock()
	if started != 2 || completed != 2 {
		t.Fatalf("镜像事件 started=%d completed=%d, want 2/2", started, completed)
	}
	if len(taskIDs) != 2 {
		t.Fatalf("并行桥 taskID 必须互异: %v", taskIDs)
	}
	if got := visSpy.act.activeSubagents.Load(); got != 0 {
		t.Fatalf("activeSubagents = %d, want 0（begin/end 配平）", got)
	}
}
