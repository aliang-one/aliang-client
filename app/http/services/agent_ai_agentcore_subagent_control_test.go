package services

// 子 agent 控制面（L3，2026-11-09）的宿主侧契约测试：
//
//   - steer 队列：排队/drain/关闭/nil 安全——指令「进入上下文」只发生在
//     内核 GetSteeringMessages 的 drain 那一刻，closed 后 push 拒绝。
//   - 控制表：register/stop（单个与全停）/steerTo/active/unregister 幂等。
//   - per-task 停止：只掐被点名的子 run，兄弟子 run 照常 completed；
//     被停的走 interrupted 终态（与父级取消同一相位语义）。
//   - steer 注入：追加指令真实出现在子 run 的下一次 LLM 请求里（上游可见）。
//   - 断点续跑：resume_task_id 把 sidechain 简报注入子 run 首轮请求；
//     没有 sidechain 时静默跳过（不伪造历史）。
//   - 消息路由：ai.subagent.control 的 list/stop/steer 与各失败分支。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/app/agentcore"
	"aliang.one/nursorgate/app/http/models"
)

// --- steer 队列 ---

func TestAgentCoreSubagentSteerQueue(t *testing.T) {
	q := newAgentCoreSubagentSteerQueue()
	if got := q.drain(); got != nil {
		t.Fatalf("空队列 drain 应为 nil, got %v", got)
	}
	if !q.push("先看 README") {
		t.Fatal("push 应成功")
	}
	if !q.push("再看 go.mod") {
		t.Fatal("第二条 push 应成功")
	}
	got := q.drain()
	if len(got) != 2 || got[0].Role != "user" || got[0].Content != "先看 README" || got[1].Content != "再看 go.mod" {
		t.Fatalf("drain 顺序/内容错: %+v", got)
	}
	if got2 := q.drain(); got2 != nil {
		t.Fatalf("drain 应清空队列, got %v", got2)
	}
	// 关闭后拒绝。
	q.close()
	if q.push("迟到的指令") {
		t.Fatal("closed 后 push 应拒绝")
	}
	// nil 安全：全部方法可空指针调用。
	var nilQ *agentCoreSubagentSteerQueue
	if nilQ.push("x") {
		t.Fatal("nil push 应 false")
	}
	if nilQ.drain() != nil {
		t.Fatal("nil drain 应 nil")
	}
	nilQ.close() // 不炸即过
}

// --- 控制表 ---

func TestAgentCoreSubagentControlTable(t *testing.T) {
	c := newAgentCoreSubagentControl()
	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	ha := c.register("agentcore-m1-researcher-1", "researcher", cancelA)
	hb := c.register("agentcore-m1-reviewer-2", "reviewer", cancelB)
	if ha == nil || hb == nil || ha.taskID == "" || hb.taskID == "" {
		t.Fatalf("register 应返回句柄: %v %v", ha, hb)
	}
	if c.count() != 2 {
		t.Fatalf("count = %d, want 2", c.count())
	}
	list := c.active()
	if len(list) != 2 {
		t.Fatalf("active = %d, want 2", len(list))
	}

	// steer 到存在的句柄。
	if !c.steerTo("agentcore-m1-researcher-1", "换个方向") {
		t.Fatal("steerTo 应成功")
	}
	if got := ha.steer.drain(); len(got) != 1 || got[0].Content != "换个方向" {
		t.Fatalf("steer 未入队: %+v", got)
	}
	// steer 到不存在的 task_id 拒绝。
	if c.steerTo("ghost", "x") {
		t.Fatal("steerTo ghost 应 false")
	}

	// 单停：只取消被点名的。
	if got := c.stop("agentcore-m1-reviewer-2"); got != 1 {
		t.Fatalf("单停返回 %d, want 1", got)
	}
	if ctxB.Err() == nil {
		t.Fatal("被点名句柄的 ctx 应已取消")
	}
	if ctxA.Err() != nil {
		t.Fatal("兄弟句柄的 ctx 不应被连坐")
	}
	// 重复停同一句柄：幂等无害，句柄仍在表里（摘除由子 run 收尾的 unregister
	// 负责），所以仍返回 1——「停止已请求过」不等于「task 不存在」。
	if got := c.stop("agentcore-m1-reviewer-2"); got != 1 {
		t.Fatalf("重复停返回 %d, want 1（句柄未 unregister）", got)
	}
	// ghost 从未登记，返回 0。
	if got := c.stop("ghost"); got != 0 {
		t.Fatalf("停 ghost 返回 %d, want 0", got)
	}

	// unregister 摘除 + 关队列；幂等。hb 只被 stop 过（没 unregister），它仍
	// 在表里——stop 与 unregister 是两件事，摘表责任在子 run 收尾。
	c.unregister(ha.taskID)
	c.unregister(ha.taskID)
	if c.count() != 1 {
		t.Fatalf("只摘 ha 后 count = %d, want 1（hb 未 unregister）", c.count())
	}
	c.unregister(hb.taskID)
	if c.count() != 0 {
		t.Fatalf("hb 也摘除后 count = %d", c.count())
	}
	if ha.steer.push("x") {
		t.Fatal("unregister 后队列应已关闭")
	}

	// 全停。
	ctxC, cancelC := context.WithCancel(context.Background())
	c.register("t3", "r3", cancelC)
	c.register("t4", "r4", cancelC)
	if got := c.stop(""); got != 2 {
		t.Fatalf("全停返回 %d, want 2", got)
	}
	if ctxC.Err() == nil {
		t.Fatal("全停应取消全部句柄")
	}

	// nil 安全 + 空 taskID 自动发号。
	var nilC *agentCoreSubagentControl
	if nilC.register("x", "x", nil) != nil {
		t.Fatal("nil control register 应 nil")
	}
	if n := nilC.stop(""); n != 0 {
		t.Fatalf("nil stop = %d", n)
	}
	auto := c.register("", "auto", func() {})
	if auto == nil || !strings.HasPrefix(auto.taskID, "subagent-") {
		t.Fatalf("空 taskID 应自动发号: %+v", auto)
	}
}

// requestContainsText 判断一次 LLM 请求的全部消息里是否出现某段文本。
// Content 两态兼容（string 直读 / 块数组取 type=text）。
func requestContainsText(req agentcore.MessagesRequest, substr string) bool {
	for _, msg := range req.Messages {
		switch v := msg.Content.(type) {
		case string:
			if strings.Contains(v, substr) {
				return true
			}
		case []interface{}:
			for _, block := range v {
				if m, ok := block.(map[string]interface{}); ok && m["type"] == "text" {
					if t, ok := m["text"].(string); ok && strings.Contains(t, substr) {
						return true
					}
				}
			}
		}
	}
	return false
}

// --- per-task 停止：只掐被点名的 ---

func TestAgentCoreSubagentPerTaskStop(t *testing.T) {
	// A 桥的上游永远挂着（等停止或测试收尾解阻）；B 桥上游直接 end_turn。
	blockDone := make(chan struct{})
	var hangSeen int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 按 task 文本分派（不是按到达序）：两个桥并发起跑，谁先到不确定，
		// 用请求体区分「挂死的」与「秒回的」才与调度顺序无关。
		body, _ := io.ReadAll(r.Body)
		isHang := strings.Contains(string(body), "会挂的任务")
		if isHang {
			mu.Lock()
			hangSeen++
			mu.Unlock()
			select {
			case <-r.Context().Done(): // 被 per-task 停止掐断
			case <-blockDone:
			}
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		fmt.Fprint(w, "data: "+`{"type":"message_start","message":{"id":"msg_b","model":"glm-5.2"}}`+"\n\n"+
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n"+
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"B 完成"}}`+"\n\n"+
			`data: {"type":"content_block_stop","index":0}`+"\n\n"+
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`+"\n\n")
	}))
	defer func() { close(blockDone); srv.Close() }()
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)

	j := newAgentCoreJournal(t.TempDir())
	control := newAgentCoreSubagentControl()
	mkBridge := func(name string) *agentCoreSubagentBridge {
		m := &agentCoreSubagentMirror{
			registry:  newAgentAISubagentRegistry(),
			activity:  &agentAIActivity{},
			sessionID: "s-pertask",
			messageID: "m-pertask",
			journal:   j,
		}
		return &agentCoreSubagentBridge{
			client:      agentcore.NewClient(agentCoreBaseURL()),
			mainModel:   "glm-5.2",
			name:        name,
			description: "测试",
			body:        "你是测试员。",
			projectPath: t.TempDir(),
			mirror:      m,
			control:     control,
		}
	}
	bridgeA, bridgeB := mkBridge("hang-a"), mkBridge("quick-b")

	var wg sync.WaitGroup
	wg.Add(2)
	type outcome struct {
		isErr bool
		text  string
	}
	results := make([]outcome, 2)
	go func() {
		defer wg.Done()
		res := bridgeA.Execute(context.Background(), map[string]interface{}{"task": "会挂的任务"}, nil)
		results[0] = outcome{res.IsError, res.Content}
	}()
	go func() {
		defer wg.Done()
		res := bridgeB.Execute(context.Background(), map[string]interface{}{"task": "会完成的任务"}, nil)
		results[1] = outcome{res.IsError, res.Content}
	}()

	// 等 A 桥把请求打出去（挂死分支已进入），再点名停它。
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := hangSeen
		mu.Unlock()
		if n >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// task_id 形如 agentcore-<messageID>-<name>-<seq>；两桥 mirror 独立、各自
	// seq 从 1 起，task_id 由 messageID+name 消歧。
	if got := control.stop("agentcore-m-pertask-hang-a-1"); got != 1 {
		t.Fatalf("停止 hang-a 返回 %d, want 1（control=%v）", got, control.active())
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("per-task 停止后两个子 run 均未返回")
	}

	if !results[0].isErr {
		t.Fatalf("被停的子 run 应返回错误结果: %+v", results[0])
	}
	if results[1].isErr || !strings.Contains(results[1].text, "B 完成") {
		t.Fatalf("兄弟子 run 不应被连坐: %+v", results[1])
	}
	if control.count() != 0 {
		t.Fatalf("收尾后 control 应清空, got %d", control.count())
	}
	// 相位：被停的是 interrupted，兄弟是 completed。
	phases := map[string]string{}
	for _, line := range readJournalLines(t, j.dir, "s-pertask") {
		if line["kind"] == "subagent" && line["event"] == "finish" {
			phases[fmt.Sprint(line["task_id"])] = fmt.Sprint(line["status"])
		}
	}
	if phases["agentcore-m-pertask-hang-a-1"] != "interrupted" {
		t.Fatalf("被停子 run 相位 = %v, want interrupted", phases)
	}
	if phases["agentcore-m-pertask-quick-b-1"] != "completed" {
		t.Fatalf("兄弟子 run 相位 = %v, want completed", phases)
	}
}

// --- steer 注入：追加指令真实进子 run 的下一次 LLM 请求 ---

func TestAgentCoreSubagentSteerReachesNextRequest(t *testing.T) {
	// 首请求挂 300ms 给测试留 push 窗口，然后回 tool_turn 触发第二次调用；
	// 第二次请求（drain 之后）必须带上追加指令。
	toolTurn := `{"type":"message_start","message":{"id":"msg_t","model":"glm-5.2"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"u1","name":"Read"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`
	endTurn := agentCoreTextTurn("done", "")
	up := &agentCoreUpstream{turns: []string{toolTurn, endTurn}}
	var reqCount int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed agentcore.MessagesRequest
		_ = json.Unmarshal(body, &parsed)
		up.mu.Lock()
		up.reqs = append(up.reqs, parsed)
		up.mu.Unlock()
		mu.Lock()
		reqCount++
		idx := reqCount
		mu.Unlock()
		w.Header().Set("content-type", "text/event-stream")
		if idx == 1 {
			time.Sleep(300 * time.Millisecond) // push 窗口
			fmt.Fprint(w, "data: "+toolTurn+"\n\n")
			return
		}
		fmt.Fprint(w, "data: "+endTurn+"\n\n")
	}))
	defer srv.Close()
	t.Cleanup(srv.Close)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)

	control := newAgentCoreSubagentControl()
	m := &agentCoreSubagentMirror{
		registry:  newAgentAISubagentRegistry(),
		activity:  &agentAIActivity{},
		sessionID: "s-steer",
		messageID: "m-steer",
	}
	bridge := &agentCoreSubagentBridge{
		client:      agentcore.NewClient(agentCoreBaseURL()),
		mainModel:   "glm-5.2",
		name:        "researcher",
		description: "调研",
		body:        "你是调研员。",
		projectPath: t.TempDir(),
		mirror:      m,
		control:     control,
	}
	resCh := make(chan agentcore.ToolResult, 1)
	go func() {
		resCh <- bridge.Execute(context.Background(), map[string]interface{}{"task": "调研"}, nil)
	}()
	// 等首轮请求落地再追加指令（task_id 可由 mirror.start 的格式预推）。
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := reqCount
		mu.Unlock()
		if n >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !control.steerTo("agentcore-m-steer-researcher-1", "重点看 main.go") {
		t.Fatalf("追加指令未入队: active=%v", control.active())
	}
	var res agentcore.ToolResult
	select {
	case res = <-resCh:
	case <-time.After(15 * time.Second):
		t.Fatal("子 run 未返回")
	}
	if res.IsError {
		t.Fatalf("子 run 不应失败: %+v", res)
	}
	reqs := up.requests()
	if len(reqs) < 2 {
		t.Fatalf("上游请求数 = %d, want ≥2", len(reqs))
	}
	second := reqs[1]
	if !requestContainsText(second, "重点看 main.go") {
		t.Fatalf("追加指令未出现在下一次 LLM 请求: %+v", second.Messages)
	}
	// 且首轮请求里没有（指令是在两次调用之间排进来的）。
	if requestContainsText(reqs[0], "重点看 main.go") {
		t.Fatalf("追加指令不应出现在首轮请求: %+v", reqs[0].Messages)
	}
}

// firstUserMessageText 提取请求首条消息的文本。Content 有两种形态：纯 string
// （buildRequest 组装时）与块数组（applyCacheBreakpoints 升级后）——断言必须
// 两态兼容，只认 string 会在缓存断点生效时恒空。
func firstUserMessageText(req agentcore.MessagesRequest) string {
	if len(req.Messages) == 0 {
		return ""
	}
	switch v := req.Messages[0].Content.(type) {
	case string:
		return v
	case []interface{}:
		var b strings.Builder
		for _, block := range v {
			if m, ok := block.(map[string]interface{}); ok && m["type"] == "text" {
				if t, ok := m["text"].(string); ok {
					b.WriteString(t)
				}
			}
		}
		return b.String()
	default:
		return ""
	}
}

// --- 断点续跑：resume_task_id 注入简报 ---

func TestAgentCoreSubagentResumeBriefingInjected(t *testing.T) {
	// journal 里先落一个被中断子 agent 的 start + 两回合 sidechain，
	// 再以 resume_task_id 派发：首轮请求的 user 消息必须带上简报与本次任务。
	j := newAgentCoreJournal(t.TempDir())
	sid := "s-resume"
	j.appendSubagentStart(sid, "agentcore-m1-researcher-1", "researcher", "调研", "查加密方案")
	j.appendSubagentTurn(sid, "agentcore-m1-researcher-1", 1, "第一轮：我先看看目录", "", 100)
	j.appendSubagentTurn(sid, "agentcore-m1-researcher-1", 2, "结论草稿：AES-GCM 更合适", "", 80)

	var gotFirst string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 0, 4096)
		buf := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buf)
			body = append(body, buf[:n]...)
			if err != nil {
				break
			}
		}
		var req agentcore.MessagesRequest
		if json.Unmarshal(body, &req) == nil {
			mu.Lock()
			gotFirst = firstUserMessageText(req)
			mu.Unlock()
		}
		w.Header().Set("content-type", "text/event-stream")
		fmt.Fprint(w, "data: "+agentCoreTextTurn("完成", "")+"\n\n")
	}))
	defer srv.Close()
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)

	m := &agentCoreSubagentMirror{
		registry:  newAgentAISubagentRegistry(),
		activity:  &agentAIActivity{},
		sessionID: sid,
		messageID: "m2",
		journal:   j,
	}
	bridge := &agentCoreSubagentBridge{
		client:      agentcore.NewClient(agentCoreBaseURL()),
		mainModel:   "glm-5.2",
		name:        "researcher",
		description: "调研",
		body:        "你是调研员。",
		projectPath: t.TempDir(),
		mirror:      m,
	}
	res := bridge.Execute(context.Background(), map[string]interface{}{
		"task":           "接着把方案定稿",
		"resume_task_id": "agentcore-m1-researcher-1",
	}, nil)
	if res.IsError {
		t.Fatalf("续跑派发不应失败: %+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, want := range []string{"续跑上下文", "AES-GCM 更合适", "第一轮：我先看看目录", "接着把方案定稿"} {
		if !strings.Contains(gotFirst, want) {
			t.Fatalf("首轮消息缺 %q: %.400q", want, gotFirst)
		}
	}
}

func TestAgentCoreSubagentResumeWithoutSidechain(t *testing.T) {
	// resume_task_id 指向不存在的 sidechain：静默跳过，task 原文原样进首轮
	// ——不注入「续跑上下文」的空壳，也不报错（诚实降级）。
	var gotFirst string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 0, 4096)
		buf := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buf)
			body = append(body, buf[:n]...)
			if err != nil {
				break
			}
		}
		var req agentcore.MessagesRequest
		if json.Unmarshal(body, &req) == nil {
			mu.Lock()
			gotFirst = firstUserMessageText(req)
			mu.Unlock()
		}
		w.Header().Set("content-type", "text/event-stream")
		fmt.Fprint(w, "data: "+agentCoreTextTurn("完成", "")+"\n\n")
	}))
	defer srv.Close()
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)

	j := newAgentCoreJournal(t.TempDir()) // 空目录：无任何 sidechain
	m := &agentCoreSubagentMirror{
		registry:  newAgentAISubagentRegistry(),
		activity:  &agentAIActivity{},
		sessionID: "s-resume-empty",
		messageID: "m3",
		journal:   j,
	}
	bridge := &agentCoreSubagentBridge{
		client:      agentcore.NewClient(agentCoreBaseURL()),
		mainModel:   "glm-5.2",
		name:        "researcher",
		description: "调研",
		body:        "你是调研员。",
		projectPath: t.TempDir(),
		mirror:      m,
	}
	res := bridge.Execute(context.Background(), map[string]interface{}{
		"task":           "从头调研",
		"resume_task_id": "agentcore-ghost-researcher-1",
	}, nil)
	if res.IsError {
		t.Fatalf("不应失败: %+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotFirst != "从头调研" {
		t.Fatalf("无 sidechain 时应原样透传 task, got %.200q", gotFirst)
	}
}

// --- 简报重建器 ---

func TestAgentCoreSubagentSidechainBriefing(t *testing.T) {
	j := newAgentCoreJournal(t.TempDir())
	sid := "s-brief"
	if got := j.subagentSidechainBriefing(sid, "agentcore-x-1", 0); got != "" {
		t.Fatalf("空 journal 应返回空串, got %q", got)
	}
	j.appendSubagentTurn(sid, "agentcore-x-1", 1, "第一轮", "", 10)
	j.appendSubagentTurn(sid, "agentcore-x-1", 2, "第二轮：结论", "", 20)
	// 别的 task_id 的行不得混入。
	j.appendSubagentTurn(sid, "agentcore-x-2", 1, "别人家的回合", "", 5)

	got := j.subagentSidechainBriefing(sid, "agentcore-x-1", 0)
	if got == "" {
		t.Fatal("有 sidechain 应返回非空简报")
	}
	if !strings.Contains(got, "第一轮") || !strings.Contains(got, "第二轮：结论") {
		t.Fatalf("简报缺回合文本: %q", got)
	}
	if strings.Contains(got, "别人家的回合") {
		t.Fatalf("别的 task_id 混入: %q", got)
	}

	// nil journal 安全。
	var nilJ *agentCoreJournal
	if nilJ.subagentSidechainBriefing(sid, "x", 0) != "" {
		t.Fatal("nil journal 应返回空串")
	}
}

// --- 消息路由 ---

func TestSubagentControlMsgRouting(t *testing.T) {
	m := newAgentAIManager()
	var acks []map[string]interface{}
	write := func(payload interface{}) error {
		if m, ok := payload.(map[string]interface{}); ok {
			acks = append(acks, m)
		}
		return nil
	}

	// 缺 session_id。
	m.subagentControl(map[string]interface{}{"action": "list"}, write)
	if acks[len(acks)-1]["result"] != "error" {
		t.Fatalf("缺 session_id 应 error: %v", acks[len(acks)-1])
	}
	// 会话不存在。
	m.subagentControl(map[string]interface{}{"session_id": "no-such", "action": "list"}, write)
	if acks[len(acks)-1]["result"] != "not_running" {
		t.Fatalf("会话不存在应 not_running: %v", acks[len(acks)-1])
	}
	// 会话在、控制面未接线。
	m.mu.Lock()
	m.sessions["s-ctl"] = &agentAISession{id: "s-ctl", runSeq: 1}
	m.mu.Unlock()
	m.subagentControl(map[string]interface{}{"session_id": "s-ctl", "action": "list"}, write)
	if acks[len(acks)-1]["result"] != "not_running" {
		t.Fatalf("控制面未接线应 not_running: %v", acks[len(acks)-1])
	}
	// 缺 action。
	m.mu.Lock()
	m.sessions["s-ctl"].subagentControl = newAgentCoreSubagentControl()
	m.mu.Unlock()
	m.subagentControl(map[string]interface{}{"session_id": "s-ctl"}, write)
	if acks[len(acks)-1]["result"] != "error" {
		t.Fatalf("缺 action 应 error: %v", acks[len(acks)-1])
	}
	// list：空表。
	m.subagentControl(map[string]interface{}{"session_id": "s-ctl", "action": "list"}, write)
	last := acks[len(acks)-1]
	if last["result"] != "ok" {
		t.Fatalf("list 应 ok: %v", last)
	}
	// steer 到不存在的 task_id。
	m.subagentControl(map[string]interface{}{"session_id": "s-ctl", "action": "steer", "task_id": "ghost", "content": "x"}, write)
	if acks[len(acks)-1]["result"] != "not_found" {
		t.Fatalf("steer ghost 应 not_found: %v", acks[len(acks)-1])
	}
	// stop 到不存在的 task_id。
	m.subagentControl(map[string]interface{}{"session_id": "s-ctl", "action": "stop", "task_id": "ghost"}, write)
	if acks[len(acks)-1]["result"] != "not_found" {
		t.Fatalf("stop ghost 应 not_found: %v", acks[len(acks)-1])
	}
	// steer 到真实句柄：queued，且内容真的进了队列。
	ctx, cancel := context.WithCancel(context.Background())
	ctl := m.sessions["s-ctl"].subagentControl
	h := ctl.register("agentcore-m-ctl-researcher-1", "researcher", cancel)
	defer cancel()
	m.subagentControl(map[string]interface{}{"session_id": "s-ctl", "action": "steer", "task_id": h.taskID, "content": "看 main.go"}, write)
	if acks[len(acks)-1]["result"] != "queued" {
		t.Fatalf("steer 应 queued: %v", acks[len(acks)-1])
	}
	if got := h.steer.drain(); len(got) != 1 || got[0].Content != "看 main.go" {
		t.Fatalf("指令未入队: %+v", got)
	}
	// steer 空内容拒绝。
	m.subagentControl(map[string]interface{}{"session_id": "s-ctl", "action": "steer", "task_id": h.taskID, "content": "  "}, write)
	if acks[len(acks)-1]["result"] != "error" {
		t.Fatalf("空 content 应 error: %v", acks[len(acks)-1])
	}
	// list 带 1 个在跑。
	m.subagentControl(map[string]interface{}{"session_id": "s-ctl", "action": "list"}, write)
	last = acks[len(acks)-1]
	subs, _ := last["subagents"].([]map[string]interface{})
	if len(subs) != 1 || subs[0]["task_id"] != h.taskID {
		t.Fatalf("list 应含在跑句柄: %v", last)
	}
	// stop 真句柄 → stopping + ctx 取消 + 表清空。
	m.subagentControl(map[string]interface{}{"session_id": "s-ctl", "action": "stop", "task_id": h.taskID}, write)
	if acks[len(acks)-1]["result"] != "stopping" {
		t.Fatalf("stop 应 stopping: %v", acks[len(acks)-1])
	}
	if ctx.Err() == nil {
		t.Fatal("stop 后句柄 ctx 应取消")
	}
	// stop 只 cancel；摘表发生在子 run 收尾的 unregister（测试里没有真实子
	// run，手动补一次）——回合外残留句柄的清理责任在子 run 自身，不靠 stop。
	ctl.unregister(h.taskID)
	if ctl.count() != 0 {
		t.Fatalf("unregister 后表未清空: %d", ctl.count())
	}
	// 未知 action。
	m.subagentControl(map[string]interface{}{"session_id": "s-ctl", "action": "explode"}, write)
	if acks[len(acks)-1]["result"] != "unsupported" {
		t.Fatalf("未知 action 应 unsupported: %v", acks[len(acks)-1])
	}
}

// TestSubagentControlAckShape 钉住回执帧型：type 恒 ai.subagent.control.ack，
// 结果词表封闭（前端/PhoneServer 按此渲染，词外值一律视为错误）。
func TestSubagentControlAckShape(t *testing.T) {
	valid := map[string]bool{"ok": true, "stopping": true, "queued": true, "not_found": true,
		"not_running": true, "unsupported": true, "error": true}
	m := newAgentAIManager()
	var last map[string]interface{}
	write := func(payload interface{}) error {
		if mm, ok := payload.(map[string]interface{}); ok {
			last = mm
		}
		return nil
	}
	m.subagentControl(map[string]interface{}{"session_id": "none", "action": "list"}, write)
	if last["type"] != models.AgentEventAISubagentControlAck {
		t.Fatalf("回执帧型错: %v", last["type"])
	}
	if !valid[fmt.Sprint(last["result"])] {
		t.Fatalf("结果词越界: %v", last["result"])
	}
}
