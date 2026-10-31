package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/app/agentcore"
	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

// 这一组测试锁死**宿主侧**的内置内核接线，与 app/agentcore 的内核测试互补：
//
//	内核测试   证明「帧序列正确」（内核 → 管道）
//	本文件     证明「帧经既有管线后，手机看到的事件正确」（管道 → 手机）
//
// 最容易出错也最难自查的是**终止事件的数量**：少一个 ai.done，手机上这个
// 气泡永远「进行中」；多一个，手机多出一个重复气泡。内核的终止帧和宿主的
// 收尾各有一条发射路径，所以这里必须断言「恰好一个」，而不是「至少一个」。

// agentCoreEventLog 收集 writeJSON 写出的每一帧。
//
// 生产环境 writeJSON 写的是云连接；测试里换成这个收集器，于是断言的正是
// 手机真实收到的那批事件。
type agentCoreEventLog struct {
	mu     sync.Mutex
	frames []map[string]interface{}
}

func (l *agentCoreEventLog) write(v interface{}) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var frame map[string]interface{}
	if err := json.Unmarshal(raw, &frame); err != nil {
		return err
	}
	l.mu.Lock()
	l.frames = append(l.frames, frame)
	l.mu.Unlock()
	return nil
}

func (l *agentCoreEventLog) snapshot() []map[string]interface{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]map[string]interface{}(nil), l.frames...)
}

func (l *agentCoreEventLog) types() []string {
	frames := l.snapshot()
	out := make([]string, len(frames))
	for i, f := range frames {
		out[i], _ = f["type"].(string)
	}
	return out
}

func (l *agentCoreEventLog) filter(eventType string) []map[string]interface{} {
	var out []map[string]interface{}
	for _, f := range l.snapshot() {
		if t, _ := f["type"].(string); t == eventType {
			out = append(out, f)
		}
	}
	return out
}

func (l *agentCoreEventLog) count(eventType string) int { return len(l.filter(eventType)) }

func (l *agentCoreEventLog) first(eventType string) map[string]interface{} {
	if frames := l.filter(eventType); len(frames) > 0 {
		return frames[0]
	}
	return nil
}

func (l *agentCoreEventLog) indexOf(eventType string) int {
	for i, f := range l.snapshot() {
		if t, _ := f["type"].(string); t == eventType {
			return i
		}
	}
	return -1
}

// agentCoreUpstream 是一个按顺序回放脚本回合的 /v1/messages 上游。
//
// 每个元素是一个「回合」：与 client_test.go 的 writeSSE 同形，一条 data: 行。
// 脚本用尽后回一个干净的 end_turn，避免内核挂死。
//
// 它同时记录每次请求体。这一半同样重要：**手机显式传下来的 model 到底有没有
// 真的发到上游**，只有在这里能证实——中间任何一层把它替换成默认值，手机上的
// 模型选择都会变成摆设，而事件流看起来一切正常。
type agentCoreUpstream struct {
	mu    sync.Mutex
	reqs  []agentcore.MessagesRequest
	turns []string
}

func newAgentCoreUpstream(t *testing.T, turns ...string) (*agentCoreUpstream, *httptest.Server) {
	t.Helper()
	up := &agentCoreUpstream{turns: turns}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("读取请求体失败: %v", err)
		}
		var parsed agentcore.MessagesRequest
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Errorf("请求体不是合法 JSON: %v\n%s", err, body)
		}

		up.mu.Lock()
		up.reqs = append(up.reqs, parsed)
		idx := len(up.reqs) - 1
		up.mu.Unlock()

		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		frame := `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`
		if idx < len(up.turns) {
			frame = up.turns[idx]
		}
		fmt.Fprintf(w, "data: %s\n\n", frame)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return up, srv
}

func (u *agentCoreUpstream) requests() []agentcore.MessagesRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]agentcore.MessagesRequest(nil), u.reqs...)
}

// agentCoreTextTurn 是一个纯文本回合，拆成两段 delta——这样「ai.delta 数量
// 等于文本 delta 数量」才有区分度（一段的话，去重与不去重都是 1）。
func agentCoreTextTurn(first, second string) string {
	return `{"type":"message_start","message":{"id":"msg_t1","model":"glm-5.2"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + first + `"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + second + `"}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`
}

// agentCoreToolTurn 是一个工具回合：模型要求调用 name，参数是 inputJSON。
//
// 参数必须走 input_json_delta 的 partial_json——内核是按字符串累积、在
// content_block_stop 时才 Unmarshal 的，直接塞 input 对象测不到那条路径。
func agentCoreToolTurn(toolUseID, name, inputJSON string) string {
	return `{"type":"message_start","message":{"id":"msg_` + toolUseID + `","model":"glm-5.2"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"` + toolUseID + `","name":"` + name + `"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":` + strconv.Quote(inputJSON) + `}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`
}

// TestRunAgentCoreEmitsSingleDone 是内置内核的端到端事件契约。
//
// 它跑的是真正的 runAgentCore：内核 → io.Pipe → streamAgentAIStdout →
// writeJSON。因此断言的就是手机收到的东西，而不是某个中间态。
func TestRunAgentCoreEmitsSingleDone(t *testing.T) {
	setTestAgentHome(t) // journal 根在 newAgentAIManager 构造时解析
	up, srv := newAgentCoreUpstream(t, agentCoreTextTurn("你好", "，世界"))
	// agentCoreBaseURL 只接受回环地址；httptest 正好是 127.0.0.1:PORT。
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")

	m := newAgentAIManager()
	const sessionID = "conv-agentcore-done"
	const runSeq = 3
	m.registerAISessionLocked(&agentAISession{
		id:      sessionID,
		runSeq:  runSeq,
		history: []agentAIMessage{{Role: "user", Content: "打个招呼"}},
	})

	log := &agentCoreEventLog{}
	run := agentAIRun{
		sessionID:   sessionID,
		runSeq:      runSeq,
		messageID:   "msg-host-1",
		provider:    "agentcore",
		model:       "glm-5.2",
		projectPath: t.TempDir(),
		activity:    newAgentAIActivity(),
	}
	m.runAgentCore(context.Background(), run, log.write)

	// 1) 恰好一个 ai.done —— 多一个手机多一个气泡，少一个永远「进行中」。
	if got := log.count(models.AgentEventAIDone); got != 1 {
		t.Fatalf("ai.done 数量 = %d, want 1（事件序列 %v）", got, log.types())
	}
	// 2) 不许有 ai.error —— 成功回合被报成失败，手机弹错误气泡。
	if got := log.count(models.AgentEventAIError); got != 0 {
		t.Fatalf("ai.error 数量 = %d, want 0（事件 %v）", got, log.filter(models.AgentEventAIError))
	}
	// 3) ai.run.started 必须最先发：手机靠它把气泡置为 running。
	if first := log.types()[0]; first != models.AgentEventAIRunStarted {
		t.Fatalf("首帧 = %s, want %s（序列 %v）", first, models.AgentEventAIRunStarted, log.types())
	}
	// 4) 恰好一个 ai.session.bound，且早于任何 ai.delta（先绑定再流内容）。
	if got := log.count(models.AgentEventAISessionBound); got != 1 {
		t.Fatalf("ai.session.bound 数量 = %d, want 1（序列 %v）", got, log.types())
	}
	boundAt := log.indexOf(models.AgentEventAISessionBound)
	deltaAt := log.indexOf(models.AgentEventAIDelta)
	if boundAt < 0 || deltaAt < 0 || boundAt > deltaAt {
		t.Fatalf("ai.session.bound(%d) 必须早于首个 ai.delta(%d)", boundAt, deltaAt)
	}
	// 5) 两段文本 delta → 恰好两条 ai.delta（finalized assistant 帧不得重复计入）。
	deltas := log.filter(models.AgentEventAIDelta)
	if len(deltas) != 2 {
		t.Fatalf("ai.delta 数量 = %d, want 2（事件 %v）", len(deltas), log.types())
	}
	var text strings.Builder
	for _, d := range deltas {
		s, _ := d["delta"].(string)
		text.WriteString(s)
	}
	if got := text.String(); got != "你好，世界" {
		t.Errorf("delta 拼起来 = %q, want %q", got, "你好，世界")
	}
	// 6) 所有事件的 session_id 必须一致，否则手机会把内容挂到别的会话上。
	for _, f := range log.snapshot() {
		if sid, _ := f["session_id"].(string); sid != sessionID {
			t.Fatalf("事件 %v 的 session_id = %q, want %q", f["type"], sid, sessionID)
		}
	}
	// 7) 绑定的 native_session_id 必须落进 resume 位，下一轮才续得上。
	bound := log.first(models.AgentEventAISessionBound)
	native, _ := bound["native_session_id"].(string)
	if native == "" {
		t.Fatalf("ai.session.bound 没带 native_session_id: %v", bound)
	}
	if done := log.first(models.AgentEventAIDone); done["claude_session_id"] != native {
		t.Errorf("ai.done.claude_session_id = %v, want %q", done["claude_session_id"], native)
	}
	// 8) 助手文本要进历史，否则下一轮模型看不到自己刚说过什么。
	m.mu.Lock()
	history := append([]agentAIMessage(nil), m.sessions[sessionID].history...)
	m.mu.Unlock()
	if len(history) != 2 || history[1].Role != "assistant" {
		t.Fatalf("history = %+v, want 追加一条 assistant", history)
	}
	if history[1].Content != "你好，世界" {
		t.Errorf("assistant 历史 = %q, want %q", history[1].Content, "你好，世界")
	}
	// 9) 手机传下来的 model 必须原样到达上游。
	//
	// 用户的要求是「每次调用的时候，手机上需要显式的传输」——模型选择由手机
	// 权威决定。中间任何一层把它换成默认值，手机上的选择就成了摆设，而事件
	// 流看起来毫无异样，所以只能在这里钉住。
	reqs := up.requests()
	if len(reqs) != 1 {
		t.Fatalf("上游请求次数 = %d, want 1", len(reqs))
	}
	if reqs[0].Model != "glm-5.2" {
		t.Errorf("上游请求 model = %q, want glm-5.2（手机显式传的值）", reqs[0].Model)
	}
	if !reqs[0].Stream {
		t.Error("上游请求 stream = false, want true")
	}
}

// TestRunAgentCoreRejectsEmptyHistory 覆盖「会话没有可发送的消息」这条前置。
//
// 没有它，用户会看到一个 ai.run.started 之后什么都没有的气泡。
func TestRunAgentCoreRejectsEmptyHistory(t *testing.T) {
	setTestAgentHome(t) // journal 根在 newAgentAIManager 构造时解析
	// 上游不该被访问；指到一个必然连不上的回环端口也不影响（这里先判空）。
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:1")

	m := newAgentAIManager()
	const sessionID = "conv-agentcore-empty"
	const runSeq = 1
	m.registerAISessionLocked(&agentAISession{id: sessionID, runSeq: runSeq})

	log := &agentCoreEventLog{}
	run := agentAIRun{
		sessionID:   sessionID,
		runSeq:      runSeq,
		messageID:   "msg-empty",
		provider:    "agentcore",
		model:       "glm-5.2",
		projectPath: t.TempDir(),
		activity:    newAgentAIActivity(),
	}
	m.runAgentCore(context.Background(), run, log.write)

	if got := log.count(models.AgentEventAIError); got != 1 {
		t.Fatalf("ai.error 数量 = %d, want 1（序列 %v）", got, log.types())
	}
	if got := log.count(models.AgentEventAIDone); got != 0 {
		t.Fatalf("空会话不该发 ai.done，实际 %d 条", got)
	}
}

// TestAgentCoreApprovePolicyShortCircuits 锁死审批桥的本地短路。
//
// 关键性质：项目内的读写不问用户。2026-10-28 起唯一不再本地判死的是
// outside-project-path——它升级为 require_approval（与 outside-project-bash
// 同口径），末尾用例改走「等审批登记 + 替用户拒绝」的交互路径。
//
// 工具名必须与内核注册表逐字一致（Read / Write，大写开头）：策略规则的
// Match.Tool 是**大小写敏感**的精确比较，写成 "read" 会一条规则都不命中，
// 全部落到 DefaultDecision=require_approval，等于每个读文件都要用户点一次。
func TestAgentCoreApprovePolicyShortCircuits(t *testing.T) {
	m := newAgentAIManager()
	// 显式给一个空 service，避免落到 GetSharedAgentService()（测试进程里
	// 那个可能是 nil，也可能带着真实云端配置）。
	m.service = &AgentService{}

	project := t.TempDir()
	run := agentAIRun{sessionID: "conv-approve", projectPath: project}
	approve := m.agentCoreApprove(run, func(interface{}) error { return nil })

	// 项目内只读 → balanced 策略的 readonly-tools 规则放行。
	resp, err := approve(context.Background(), agentcore.ApprovalRequest{
		ToolName: "Read",
		Input:    map[string]interface{}{"file_path": filepath.Join(project, "a.txt")},
	})
	if err != nil {
		t.Fatalf("read 审批返回错误: %v", err)
	}
	if !resp.Approved {
		t.Fatalf("项目内 read 应被策略放行，实际 %+v", resp)
	}

	// 项目内写入 → file-mutation 规则放行。
	resp, err = approve(context.Background(), agentcore.ApprovalRequest{
		ToolName: "Write",
		Input:    map[string]interface{}{"file_path": filepath.Join(project, "b.txt")},
	})
	if err != nil {
		t.Fatalf("write 审批返回错误: %v", err)
	}
	if !resp.Approved {
		t.Fatalf("项目内 write 应被策略放行，实际 %+v", resp)
	}

	// 越界写入 → 2026-10-28 降级：项目边界规则升级为 require_approval，
	// 不再本地判死——审批请求必须真的登记（等用户在手机上拍板）。
	// goroutine 发起审批，主 goroutine 等登记后替用户「拒绝」。
	type approveResult struct {
		resp agentcore.ApprovalResponse
		err  error
	}
	resultCh := make(chan approveResult, 1)
	go func() {
		resp, err := approve(context.Background(), agentcore.ApprovalRequest{
			ToolName: "Write",
			Input:    map[string]interface{}{"file_path": filepath.Join(string(filepath.Separator), "etc", "passwd")},
		})
		resultCh <- approveResult{resp: resp, err: err}
	}()
	waiter := waitForAgentCoreApproval(t, m, "conv-approve", &agentCoreEventLog{})
	waiter.request.respond <- agentAIApprovalResponse{Decision: models.AgentAIApprovalDecisionDecline}
	select {
	case got := <-resultCh:
		if got.err != nil {
			t.Fatalf("越界 write 审批返回错误: %v", got.err)
		}
		if got.resp.Approved {
			t.Fatalf("越界 write 被用户拒绝后必须不执行，实际 %+v", got.resp)
		}
		if !strings.Contains(got.resp.Reason, "用户拒绝") {
			t.Errorf("拒绝原因 = %q, want 含「用户拒绝」", got.resp.Reason)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("替用户拒绝后审批没有返回")
	}
}

// waitForAgentCoreApproval 轮询 manager 的等待表，直到出现该会话的审批请求。
//
// 审批是**异步**发生的（内核先发 tool_use，宿主才登记等待项），所以测试不能
// 直接读一次 map。这里同时是「审批请求真的登记了」的断言——如果登记失败，
// 手机永远收不到弹窗，run 会一路等到 24h 超时。
func waitForAgentCoreApproval(t *testing.T, m *agentAIManager, sessionID string, log *agentCoreEventLog) *agentAIApprovalWaiter {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		var found *agentAIApprovalWaiter
		for _, waiter := range m.approvals {
			if waiter != nil && waiter.sessionID == sessionID {
				found = waiter
				break
			}
		}
		m.mu.Unlock()
		if found != nil {
			return found
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("10s 内没有等到 %s 的审批等待项（事件 %v）", sessionID, log.types())
	return nil
}

// TestRunAgentCoreApprovalDeclineKeepsTurnAlive 覆盖审批的**交互**分支。
//
// 与 TestAgentCoreApprovePolicyShortCircuits 互补：那里测的是本地短路（不问
// 用户），这里测的是真的问用户、用户拒绝之后会发生什么。要证明的四件事：
//
//  1. 手机先收到 status=pending 的 ai.approval.request（弹窗依据）；
//  2. 拒绝之后收到 status=resolved + decision=decline（弹窗收起依据）；
//  3. 工具**没有执行**，模型收到的是拒绝理由（tool_result is_error=true）；
//  4. 本轮**继续跑完**——拒绝一次工具不等于终止对话，仍要恰好一个 ai.done。
//
// 触发工具选 PowerShell 而不是 Bash：内置 balanced 策略里 Bash 有 safe/dangerous
// 两条正则规则，选命令要小心不要误命中；PowerShell 一条规则都没有，必然落到
// DefaultDecision=require_approval，是唯一确定性的触发器。
func TestRunAgentCoreApprovalDeclineKeepsTurnAlive(t *testing.T) {
	const command = "New-Item -ItemType File -Path pwned.txt"
	up, srv := newAgentCoreUpstream(t,
		agentCoreToolTurn("toolu_pwsh1", "PowerShell", `{"command":`+strconv.Quote(command)+`}`),
		agentCoreTextTurn("好的", "，那我换个方式"),
	)
	setTestAgentHome(t) // journal 根在 newAgentAIManager 构造时解析
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")

	m := newAgentAIManager()
	// 空 service：approvalService() 用内置 balanced 策略，绝不碰云端配置。
	m.service = &AgentService{}

	const sessionID = "conv-agentcore-approval"
	const runSeq = 1
	m.registerAISessionLocked(&agentAISession{
		id:      sessionID,
		runSeq:  runSeq,
		history: []agentAIMessage{{Role: "user", Content: "帮我建个文件"}},
	})

	log := &agentCoreEventLog{}
	project := t.TempDir()
	run := agentAIRun{
		sessionID:   sessionID,
		runSeq:      runSeq,
		messageID:   "msg-approval-1",
		provider:    "agentcore",
		model:       "glm-5.2",
		projectPath: project,
		activity:    newAgentAIActivity(),
	}

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		m.runAgentCore(context.Background(), run, log.write)
	}()

	waiter := waitForAgentCoreApproval(t, m, sessionID, log)
	// 模拟手机点「拒绝」。respond 是带缓冲(1)的通道，发送不会阻塞。
	waiter.request.respond <- agentAIApprovalResponse{Decision: models.AgentAIApprovalDecisionDecline}

	select {
	case <-finished:
	case <-time.After(30 * time.Second):
		t.Fatal("拒绝审批后 runAgentCore 没有收敛（应继续跑完本轮）")
	}

	// 1) 弹窗：先 pending，后 resolved/decline。
	requests := log.filter(models.AgentEventAIApprovalRequest)
	if len(requests) != 2 {
		t.Fatalf("ai.approval.request 数量 = %d, want 2（pending + resolved，事件 %v）", len(requests), log.types())
	}
	if status, _ := requests[0]["status"].(string); status != "pending" {
		t.Errorf("首条审批状态 = %q, want pending", status)
	}
	if status, _ := requests[1]["status"].(string); status != "resolved" {
		t.Errorf("次条审批状态 = %q, want resolved", status)
	}
	if decision, _ := requests[1]["decision"].(string); decision != models.AgentAIApprovalDecisionDecline {
		t.Errorf("审批决定 = %q, want %q", decision, models.AgentAIApprovalDecisionDecline)
	}
	if tool, _ := requests[0]["tool_name"].(string); tool != "PowerShell" {
		t.Errorf("审批 tool_name = %q, want PowerShell", tool)
	}

	// 2) 终止事件仍然恰好一个，且没有错误——拒绝不是失败。
	if got := log.count(models.AgentEventAIDone); got != 1 {
		t.Fatalf("ai.done 数量 = %d, want 1（事件 %v）", got, log.types())
	}
	if got := log.count(models.AgentEventAIError); got != 0 {
		t.Fatalf("ai.error 数量 = %d, want 0（%v）", got, log.filter(models.AgentEventAIError))
	}

	// 3) 工具没有执行：命令会建的文件不存在。
	if _, err := os.Stat(filepath.Join(project, "pwned.txt")); err == nil {
		t.Fatal("审批被拒绝，命令却真的执行了（pwned.txt 存在）")
	}

	// 4) 模型收到的是拒绝理由，而不是命令输出——这一条同时证明内核没执行工具。
	reqs := up.requests()
	if len(reqs) != 2 {
		t.Fatalf("上游请求次数 = %d, want 2（拒绝后应继续下一轮）", len(reqs))
	}
	for i, req := range reqs {
		if req.Model != "glm-5.2" {
			t.Errorf("第 %d 次请求 model = %q, want glm-5.2", i+1, req.Model)
		}
	}
	second := reqs[1].Messages
	if len(second) < 3 {
		t.Fatalf("第二轮 messages 只有 %d 条，want ≥3（user/assistant(tool_use)/user(tool_result)）", len(second))
	}
	last := second[len(second)-1]
	blocks, _ := last.Content.([]interface{})
	var result *agentcore.ContentBlock
	for _, raw := range blocks {
		block, _ := raw.(map[string]interface{})
		if block == nil {
			continue
		}
		if bt, _ := block["type"].(string); bt == agentcore.BlockTypeToolResult {
			content, _ := block["content"].(string)
			isErr, _ := block["is_error"].(bool)
			result = &agentcore.ContentBlock{Type: agentcore.BlockTypeToolResult, Content: content, IsError: isErr}
			break
		}
	}
	if result == nil {
		t.Fatalf("第二轮最后一条消息没有 tool_result：%+v", last)
	}
	if !result.IsError {
		t.Error("拒绝后的 tool_result 必须 is_error=true，否则模型会以为自己跑成功了")
	}
	if text, _ := result.Content.(string); !strings.Contains(text, "用户拒绝") {
		t.Errorf("tool_result 内容 = %q, want 含「用户拒绝」", text)
	}
}

// ---------------------------------------------------------------------------
// steering 通道（宿主侧）
// ---------------------------------------------------------------------------

// TestAgentCoreSteerControlQueuesThenAcksApplied 锁死三态口径：
// deliver 只能报 queued（消息还在队列里），真正 applied 的时刻是 drain。
//
// 把 deliver 直接报成 applied 是**撒谎**：消息可能还在队列里 run 就结束了，
// 手机却已经显示「已发送」。这条测试就是为了不让那个谎言混进来。
func TestAgentCoreSteerControlQueuesThenAcksApplied(t *testing.T) {
	log := &agentCoreEventLog{}
	run := agentAIRun{sessionID: "conv-steer-host", runSeq: 5}
	control := newAgentAICoreSteerControl(run, log.write)

	result, err := control.deliver(agentAISteerMessage{MessageID: "steer-1", Content: "顺便跑下测试"})
	if err != nil {
		t.Fatalf("deliver 返回错误: %v", err)
	}
	if result != "queued" {
		t.Fatalf("deliver 结果 = %q, want queued（此时还没进上下文）", result)
	}
	if got := log.count(models.AgentEventAISteerAck); got != 0 {
		t.Fatalf("drain 之前不该有 ack，实际 %d 条", got)
	}

	msgs := control.drain()
	if len(msgs) != 1 {
		t.Fatalf("drain 取到 %d 条，want 1", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("插话角色 = %q, want user", msgs[0].Role)
	}
	if text, _ := msgs[0].Content.(string); text != "顺便跑下测试" {
		t.Errorf("插话内容 = %q, want %q", text, "顺便跑下测试")
	}

	acks := log.filter(models.AgentEventAISteerAck)
	if len(acks) != 1 {
		t.Fatalf("applied ack 数量 = %d, want 1（事件 %v）", len(acks), log.types())
	}
	if acked, _ := acks[0]["result"].(string); acked != "applied" {
		t.Errorf("ack.result = %q, want applied", acked)
	}
	if id, _ := acks[0]["message_id"].(string); id != "steer-1" {
		t.Errorf("ack.message_id = %q, want steer-1", id)
	}

	// drain 取走即清空：再取一次必须空，否则同一句插话会进上下文两次。
	if again := control.drain(); len(again) != 0 {
		t.Fatalf("二次 drain 取到 %d 条，want 0", len(again))
	}
}

// TestAgentCoreSteerControlRejectsAfterClose 覆盖 run 结束后的迟到插话。
//
// 手机可能在 ai.done 之后、UI 收起之前按下发送。必须回 not_running，且把
// 队列里残留的插话也 ack 掉——否则那几条会永远停在「发送中」。
func TestAgentCoreSteerControlRejectsAfterClose(t *testing.T) {
	log := &agentCoreEventLog{}
	control := newAgentAICoreSteerControl(agentAIRun{sessionID: "conv-steer-closed", runSeq: 1}, log.write)

	if _, err := control.deliver(agentAISteerMessage{MessageID: "steer-left", Content: "还在吗"}); err != nil {
		t.Fatalf("close 之前 deliver 应成功: %v", err)
	}
	control.close("not_running", "agentcore turn ended before steer was applied", "turn_ended")

	acks := log.filter(models.AgentEventAISteerAck)
	if len(acks) != 1 {
		t.Fatalf("残留插话应被 ack 掉，实际 %d 条（事件 %v）", len(acks), log.types())
	}
	if got, _ := acks[0]["result"].(string); got != "not_running" {
		t.Errorf("残留 ack.result = %q, want not_running", got)
	}
	if code, _ := acks[0]["code"].(string); code != "turn_ended" {
		t.Errorf("残留 ack.code = %q, want turn_ended", code)
	}

	// close 之后再插话：拒绝，不再进队列。
	result, err := control.deliver(agentAISteerMessage{MessageID: "steer-late", Content: "再补一句"})
	if err == nil {
		t.Fatalf("close 之后 deliver 应报错，实际 result=%q", result)
	}
	if result != "not_running" {
		t.Errorf("close 之后 deliver 结果 = %q, want not_running", result)
	}
	if got := control.drain(); len(got) != 0 {
		t.Errorf("close 之后 drain 取到 %d 条，want 0", len(got))
	}
}

// TestDrainAgentCoreSteeringRejectsStaleRunSeq 是 runSeq 守卫。
//
// 会话的 runSeq 已经推进（新一轮开始），旧 run 的 drain 就不能再取队列——
// 否则上一轮迟到的插话会被折进下一轮的上下文，用户看到的是「我说的话跑到了
// 另一个问题里」。
func TestDrainAgentCoreSteeringRejectsStaleRunSeq(t *testing.T) {
	m := newAgentAIManager()
	const sessionID = "conv-steer-seq"
	m.registerAISessionLocked(&agentAISession{id: sessionID, runSeq: 2})

	control := newAgentAICoreSteerControl(agentAIRun{sessionID: sessionID, runSeq: 2}, func(interface{}) error { return nil })
	m.attachCoreSteer(sessionID, 2, control)
	if _, err := control.deliver(agentAISteerMessage{MessageID: "steer-seq", Content: "跑测试"}); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	if got := m.drainAgentCoreSteering(agentAIRun{sessionID: sessionID, runSeq: 1}); len(got) != 0 {
		t.Fatalf("旧 runSeq 取到 %d 条插话，want 0", len(got))
	}
	got := m.drainAgentCoreSteering(agentAIRun{sessionID: sessionID, runSeq: 2})
	if len(got) != 1 {
		t.Fatalf("当前 runSeq 取到 %d 条，want 1", len(got))
	}
}

// TestRunAgentCoreSurfacesUpstreamErrorBody 锁死「网关把错误塞进 200 响应体」
// 时用户看到什么。
//
// 实测（2026-10-25，56432 本地代理）：限流回的是 HTTP 200 + 裸 JSON，不是
// 非 2xx、也不是 SSE。修复前这条错误在 parseSSE 里被整段忽略，内核当成
// 「空回合」正常收尾，手机上是一条空回复气泡，用户完全看不到真实原因。
// 这里断言的是**手机收到的 ai.error 文本**：必须带上游原文。
func TestRunAgentCoreSurfacesUpstreamErrorBody(t *testing.T) {
	setTestAgentHome(t) // MCP 采集也从 HOME 解析：不隔离会去 dial 真实 ~/.claude.json 的 server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"error":{"message":"model not found","type":"invalid_request_error"},"type":"error"}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")

	m := newAgentAIManager()
	const sessionID = "conv-agentcore-upstream-error"
	const runSeq = 1
	m.registerAISessionLocked(&agentAISession{
		id:      sessionID,
		runSeq:  runSeq,
		history: []agentAIMessage{{Role: "user", Content: "你好"}},
	})

	log := &agentCoreEventLog{}
	run := agentAIRun{
		sessionID:   sessionID,
		runSeq:      runSeq,
		messageID:   "msg-upstream-err",
		provider:    "agentcore",
		model:       "glm-5.2",
		projectPath: t.TempDir(),
		activity:    newAgentAIActivity(),
	}
	m.runAgentCore(context.Background(), run, log.write)

	if got := log.count(models.AgentEventAIDone); got != 0 {
		t.Fatalf("失败回合不该发 ai.done，实际 %d 条（序列 %v）", got, log.types())
	}
	errs := log.filter(models.AgentEventAIError)
	if len(errs) != 1 {
		t.Fatalf("ai.error 数量 = %d, want 1（序列 %v）", len(errs), log.types())
	}
	msg, _ := errs[0]["error"].(string)
	if !strings.Contains(msg, "model not found") {
		t.Errorf("ai.error 应带上游原文，实际 %q", msg)
	}
	if !strings.Contains(msg, "invalid_request_error") {
		t.Errorf("ai.error 应带上游错误类型，实际 %q", msg)
	}
}

// ---------------------------------------------------------------------------
// 调用记录 journal（runAgentCore 挂点）
// ---------------------------------------------------------------------------

// setTestAgentHome 同时隔离 HOME/USERPROFILE——Windows 上 UserHomeDir 优先读
// USERPROFILE，只设 HOME 会让 journal 落进真实 profile（2026-10 计划评审）。
func setTestAgentHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// registerTestAISession 是 registerAISessionLocked 的取锁包装（文档要求
// 持 m.mu 调用）。
func registerTestAISession(t *testing.T, m *agentAIManager, session *agentAISession) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registerAISessionLocked(session)
}

// readAgentCoreJournalRows 读测试会话的 journal 文件并解析成行列表。
func readAgentCoreJournalRows(t *testing.T, sessionID string) []map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(agentCoreJournalDir(), agentCoreJournalFileName(sessionID)))
	if err != nil {
		t.Fatalf("读取 journal: %v", err)
	}
	var out []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var row map[string]interface{}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("行解析失败 %q: %v", line, err)
		}
		out = append(out, row)
	}
	return out
}

// TestRunAgentCoreJournalsTranscript 端到端：一次成功 run 必须留下
// 头行 + user 行 + turn 行（逐回合 transcript）+ assistant 行 + end(success)。
// turn 行是本回合 assistant 完整文本/thinking 的留档，与 run 末整轮累计的
// assistant 行并存——前者给「完整对话」复盘，后者给仪表盘消息气泡。
func TestRunAgentCoreJournalsTranscript(t *testing.T) {
	setTestAgentHome(t)
	up, srv := newAgentCoreUpstream(t, agentCoreTextTurn("你好", "，世界"))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")

	m := newAgentAIManager()
	const sessionID = "conv-journal-ok"
	registerTestAISession(t, m, &agentAISession{
		id:      sessionID,
		runSeq:  1,
		history: []agentAIMessage{{Role: "user", Content: "打个招呼"}},
	})

	log := &agentCoreEventLog{}
	run := agentAIRun{
		sessionID:   sessionID,
		runSeq:      1,
		messageID:   "msg-host-j1",
		provider:    "agentcore",
		model:       "glm-5.2",
		projectPath: t.TempDir(),
		activity:    newAgentAIActivity(),
	}
	m.runAgentCore(context.Background(), run, log.write)

	lines := readAgentCoreJournalRows(t, sessionID)
	// 2026-10-31 单一真源门:带正文回合已完成 → run 级累计 assistant 行不再
	// 落(内容=各回合拼接,与 turn 行构成重复表示,detail 回放会出重复气泡)。
	// journal 契约 = session / user / turn / end 四行。
	if len(lines) != 4 {
		t.Fatalf("journal 行数 = %d, want 4: %v", len(lines), lines)
	}
	if lines[0]["kind"] != "session" || lines[0]["model"] != "glm-5.2" {
		t.Errorf("头行 = %v", lines[0])
	}
	if lines[1]["role"] != "user" || lines[1]["text"] != "打个招呼" {
		t.Errorf("user 行 = %v（必须是 history 里的真实用户 content）", lines[1])
	}
	// turn 行：本回合 assistant 完整文本。
	if lines[2]["kind"] != "turn" || lines[2]["text"] != "你好，世界" || lines[2]["turn"] != float64(1) {
		t.Errorf("turn 行 = %v（逐回合 transcript）", lines[2])
	}
	if lines[3]["kind"] != "end" || lines[3]["status"] != "success" {
		t.Errorf("end 行 = %v(累计 assistant 行已由 turn 行取代,不再落)", lines[3])
	}
	_ = up
}

// TestRunAgentCoreJournalsAutoProviderFirstUserLine 防回归：fallback 用户
// （provider=auto）的 run 同样必须留下首条 user 行——这正是挂点收进
// runAgentCore 入口的原因（派发前 provider 还是 auto）。
func TestRunAgentCoreJournalsAutoProviderFirstUserLine(t *testing.T) {
	setTestAgentHome(t)
	_, srv := newAgentCoreUpstream(t, agentCoreTextTurn("好", ""))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")

	m := newAgentAIManager()
	const sessionID = "conv-journal-auto"
	registerTestAISession(t, m, &agentAISession{
		id:      sessionID,
		runSeq:  1,
		history: []agentAIMessage{{Role: "user", Content: "自动档"}},
	})
	log := &agentCoreEventLog{}
	run := agentAIRun{
		sessionID:   sessionID,
		runSeq:      1,
		messageID:   "msg-host-j2",
		provider:    "auto",
		projectPath: t.TempDir(),
		activity:    newAgentAIActivity(),
	}
	m.runAgentCore(context.Background(), run, log.write)

	lines := readAgentCoreJournalRows(t, sessionID)
	if len(lines) < 2 || lines[1]["role"] != "user" || lines[1]["text"] != "自动档" {
		t.Fatalf("fallback 用户的首条 user 行缺失: %v", lines)
	}
}

// TestRunAgentCoreJournalsWriteFailureDoesNotAffectRun journal 写失败绝不影响
// run（spec §10）：把 journal 指到一个「文件冒充的目录」上，MkdirAll 必败，
// run 仍须正常完成（恰好一个 done、无 error）。
func TestRunAgentCoreJournalsWriteFailureDoesNotAffectRun(t *testing.T) {
	home := t.TempDir()
	blocker := filepath.Join(home, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	setTestAgentHome(t)
	up, srv := newAgentCoreUpstream(t, agentCoreTextTurn("好", ""))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")

	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(blocker) // 文件冒充目录 → 所有写失败
	const sessionID = "conv-journal-iofail"
	registerTestAISession(t, m, &agentAISession{
		id:      sessionID,
		runSeq:  1,
		history: []agentAIMessage{{Role: "user", Content: "照常跑"}},
	})
	log := &agentCoreEventLog{}
	run := agentAIRun{
		sessionID:   sessionID,
		runSeq:      1,
		messageID:   "msg-host-j4",
		provider:    "agentcore",
		model:       "glm-5.2",
		projectPath: t.TempDir(),
		activity:    newAgentAIActivity(),
	}
	m.runAgentCore(context.Background(), run, log.write)

	if got := log.count(models.AgentEventAIDone); got != 1 {
		t.Fatalf("ai.done = %d, want 1（写失败不得影响 run）: %v", got, log.types())
	}
	if got := log.count(models.AgentEventAIError); got != 0 {
		t.Fatalf("ai.error = %d, want 0: %v", got, log.filter(models.AgentEventAIError))
	}
	_ = up
}

// TestRunAgentCoreJournalsErrorEnd 错误出口也要落 end 行，但绝不落 assistant 行。
func TestRunAgentCoreJournalsErrorEnd(t *testing.T) {
	setTestAgentHome(t)
	// 不配可用上游（BASE_URL 指向必然连不上的回环端口）→ agentCoreEnsureUpstream
	// 或内核请求失败。若发现该测试因自愈逻辑产生重副作用，参考现有
	// TestRunAgentCoreRejectsEmptyHistory 的处理方式调整，但断言不变：
	// journal 末行必须是 end/status=error，且全程无 assistant 行。
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("ANTHROPIC_MODEL", "")

	m := newAgentAIManager()
	const sessionID = "conv-journal-err"
	registerTestAISession(t, m, &agentAISession{
		id:      sessionID,
		runSeq:  1,
		history: []agentAIMessage{{Role: "user", Content: "会失败"}},
	})
	log := &agentCoreEventLog{}
	run := agentAIRun{
		sessionID:   sessionID,
		runSeq:      1,
		messageID:   "msg-host-j3",
		provider:    "agentcore",
		model:       "glm-5.2",
		projectPath: t.TempDir(),
		activity:    newAgentAIActivity(),
	}
	m.runAgentCore(context.Background(), run, log.write)

	lines := readAgentCoreJournalRows(t, sessionID)
	if len(lines) < 3 {
		t.Fatalf("journal 行数 = %d, want >=3: %v", len(lines), lines)
	}
	last := lines[len(lines)-1]
	if last["kind"] != "end" || last["status"] != "error" {
		t.Errorf("end 行 = %v, want status=error", last)
	}
	for _, l := range lines {
		if l["role"] == "assistant" {
			t.Errorf("错误出口不应有 assistant 行: %v", l)
		}
	}
}

// TestZombieStoppedRunRevokesResumePoint 是僵尸收尾的**真实路径**用例。
//
// 生产里判死的每条路径都先 kill() → cancel()，所以僵尸迟返回必然先在
// ctx.Err() != nil 的 stopped 早退处 return，永远走不到成功收尾段。撤销因此
// 必须发生在强拆链路（finalizeStalledGuard）里。本用例完整复现该形态：
//
//	上游卡在流中途 → system/init 已消费、绑定已落库 → kill + forceFinalize
//	→ 放行上游，让 run goroutine 迟返回。
//
// 断言里最关键的一条是 journal end 记 "stopped"：它证明用例走的确实是真实
// stopped 早退，而不是被测试人工造出来、生产中到不了的收尾分支。
func TestZombieStoppedRunRevokesResumePoint(t *testing.T) {
	setTestAgentHome(t)
	release := make(chan struct{})
	var releaseOnce sync.Once
	entered := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		// 不回任何帧：run 卡在流式中途，正是僵尸的形态。
		<-release
	}))
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		srv.Close()
	})
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")

	m := newAgentAIManager()
	const sessionID = "conv-agentcore-zombie-stopped"
	const runSeq = 9
	activity := newAgentAIActivity()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	registerTestAISession(t, m, &agentAISession{
		id:       sessionID,
		runSeq:   runSeq,
		cancel:   cancel,
		activity: activity,
		history:  []agentAIMessage{{Role: "user", Content: "跑到一半被强拆"}},
	})

	log := &agentCoreEventLog{}
	run := agentAIRun{
		sessionID:   sessionID,
		runSeq:      runSeq,
		messageID:   "msg-zombie-stopped",
		provider:    "agentcore",
		model:       "glm-5.2",
		projectPath: t.TempDir(),
		activity:    activity,
		cancel:      cancel,
	}
	// 与生产一致：走 attachRunGuard 挂载，起跑快照才会被采集。
	run.guard = m.attachRunGuard(run, log.write)
	if run.guard == nil {
		t.Fatal("guard 未挂载，用例覆盖不到强拆链路")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		m.runAgentCore(ctx, run, log.write)
	}()

	// 1) 等 system/init 被消费：绑定落库即僵尸已在流式途中钉下恢复点。
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		bound := m.bindings[sessionID].NativeSessionID
		resume := m.sessions[sessionID].resumeSessionID
		m.mu.Unlock()
		if bound != "" && resume != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("等不到绑定落库（binding=%q resume=%q）", bound, resume)
		}
		time.Sleep(2 * time.Millisecond)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("上游没收到请求，run 未真正卡在流式中途")
	}

	// 2) 判死：kill 负责 cancel，宽限到期强拆。这里同步驱动 finalize
	// （定时器那条链由既有 run_guard 用例覆盖），避免用例空等 30s。
	run.guard.kill("stalled")
	run.guard.forceFinalize("stalled")

	// 3) 放行上游，让僵尸迟返回。
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("run goroutine 未返回")
	}

	// 4) 证明走的是真实 stopped 早退：被 cancel 的僵尸写不出 success。
	rows := readAgentCoreJournalRows(t, sessionID)
	last := rows[len(rows)-1]
	if status, _ := last["status"].(string); status != "stopped" {
		t.Fatalf("journal end status = %q, want stopped（僵尸必经 ctx.Err() 早退，事件 %v）", status, log.types())
	}
	for _, row := range rows {
		if role, _ := row["role"].(string); role == "assistant" {
			t.Fatalf("journal 出现僵尸 assistant 行：%v", row)
		}
	}

	m.mu.Lock()
	resume := m.sessions[sessionID].resumeSessionID
	binding := m.bindings[sessionID]
	history := append([]agentAIMessage(nil), m.sessions[sessionID].history...)
	m.mu.Unlock()

	if resume != "" {
		t.Fatalf("resumeSessionID = %q, want 空（僵尸不得为 Retry 留恢复点）", resume)
	}
	if binding.NativeSessionID != "" {
		t.Fatalf("bindings 仍指向僵尸会话 %q（runStart 会从 confirmed 绑定取 resumeID）", binding.NativeSessionID)
	}
	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1（僵尸的 assistant 输出必须被丢弃）", len(history))
	}
}

// TestZombieLateNativeSessionIDDoesNotRebind 锁死 onNativeSessionID 的围栏。
//
// 被 cancel 却忽略取消、卡住很久的僵尸，完全可能在强拆撤销之后才吐出
// system/init。没有围栏时它会把 m.bindings 与 session.resumeSessionID 重新
// 写回去，污染照样成立。围栏必须挡在 confirmBinding 之前，且不发
// ai.session.bound（手机不该看到一个已判死回合的绑定帧）。
func TestZombieLateNativeSessionIDDoesNotRebind(t *testing.T) {
	setTestAgentHome(t)
	// identity 状态走全局 cache 单例，必须重置才能隔离到临时目录。
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	t.Cleanup(cache.ResetCacheDirForTest)

	m := newAgentAIManager()
	const sessionID = "conv-agentcore-late-bind"
	const runSeq = 4
	activity := newAgentAIActivity()
	_, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	registerTestAISession(t, m, &agentAISession{
		id:       sessionID,
		runSeq:   runSeq,
		cancel:   cancel,
		activity: activity,
	})

	log := &agentCoreEventLog{}
	run := agentAIRun{
		sessionID: sessionID,
		runSeq:    runSeq,
		runID:     "run-late-bind",
		messageID: "msg-late-bind",
		provider:  "agentcore",
		activity:  activity,
		cancel:    cancel,
	}
	run.guard = m.attachRunGuard(run, log.write)
	if run.guard == nil {
		t.Fatal("guard 未挂载")
	}

	// 先走一遍真实强拆链路，撤销已经发生。
	run.guard.kill("stalled")
	run.guard.forceFinalize("stalled")

	// 僵尸迟到的 system/init。
	_, fenced, err := m.bindAgentCoreNativeSession(run, log.write, "agentcore-late-1")
	if err != nil {
		t.Fatalf("围栏丢弃不该报错: %v", err)
	}
	if !fenced {
		t.Fatal("强拆之后到达的绑定必须被围栏丢弃")
	}

	m.mu.Lock()
	binding := m.bindings[sessionID]
	resume := m.sessions[sessionID].resumeSessionID
	m.mu.Unlock()
	if binding.NativeSessionID != "" {
		t.Fatalf("迟到绑定写回 m.bindings：%q", binding.NativeSessionID)
	}
	if resume != "" {
		t.Fatalf("迟到绑定写回 resumeSessionID：%q", resume)
	}
	if got := log.count(models.AgentEventAISessionBound); got != 0 {
		t.Fatalf("僵尸迟到绑定不得发 ai.session.bound，实际 %d 帧", got)
	}
}

// TestFinalizeStalledGuardRevokesMidRunResumePoint 锁死撤销的判据边界。
//
// 撤销只在「起跑时没有恢复点」时动手，且绑定还必须与起跑快照不同——否则可能
// 把上一轮合法恢复点误清。guard 身份不匹配时一律不碰会话。
func TestFinalizeStalledGuardRevokesMidRunResumePoint(t *testing.T) {
	type testCase struct {
		name           string
		resumeAtStart  string
		bindingAtStart string
		midRunResume   string
		midRunBinding  string
		guardMismatch  bool
		wantResume     string
		wantBinding    string
		wantCode       string
		wantOK         bool
	}
	cases := []testCase{
		{
			name:           "起跑已有恢复点不撤销",
			resumeAtStart:  "agentcore-prev",
			bindingAtStart: "agentcore-prev",
			midRunResume:   "agentcore-new",
			midRunBinding:  "agentcore-new",
			wantResume:     "agentcore-new",
			wantBinding:    "agentcore-new",
			wantOK:         true,
		},
		{
			// 可达形态：绑定由云端预分配（reserveBinding 写 State:"reserved"，
			// 只占住 native id，run.resumeSessionID 仍为空）。此时起跑快照
			// bindingAtStart 非空、resumeAtStart 为空，撤销必须忍住不动——这个
			// 绑定在起跑前就存在，不属于本回合。已确认（confirmed）的形态在起跑
			// 路径就会填 run.resumeSessionID，resumeAtStart 非空、更早命中另一条
			// 判据退出，走不到这里。
			name:           "起跑已有绑定不撤销",
			bindingAtStart: "agentcore-prev",
			midRunBinding:  "agentcore-prev",
			wantResume:     "",
			wantBinding:    "agentcore-prev",
			wantOK:         true,
		},
		{
			name:          "guard 身份不匹配不碰会话",
			midRunResume:  "agentcore-new",
			midRunBinding: "agentcore-new",
			guardMismatch: true,
			wantResume:    "agentcore-new",
			wantBinding:   "agentcore-new",
			wantCode:      "guard_replaced",
			wantOK:        false,
		},
		{
			name:          "起跑无恢复点且中途钉入则撤销",
			midRunResume:  "agentcore-new",
			midRunBinding: "agentcore-new",
			wantResume:    "",
			wantBinding:   "",
			wantOK:        true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
			cache.ResetCacheDirForTest()
			t.Cleanup(cache.ResetCacheDirForTest)

			m := newAgentAIManager()
			const sessionID = "conv-finalize-revoke"
			activity := newAgentAIActivity()
			_, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			session := &agentAISession{
				id:       sessionID,
				runSeq:   2,
				cancel:   cancel,
				activity: activity,
			}
			m.mu.Lock()
			m.sessions[sessionID] = session
			if tc.bindingAtStart != "" {
				m.bindings[sessionID] = agentAIBindingRecord{
					ConversationID:  sessionID,
					NativeSessionID: tc.bindingAtStart,
					State:           "confirmed",
				}
			}
			m.mu.Unlock()

			run := agentAIRun{
				sessionID:       sessionID,
				runID:           "run-revoke",
				messageID:       "msg-revoke",
				runSeq:          session.runSeq,
				resumeSessionID: tc.resumeAtStart,
				activity:        activity,
				cancel:          cancel,
			}
			rec := &recordingWriter{}
			guard := m.attachRunGuard(run, rec.write)
			if guard == nil {
				t.Fatal("guard 未挂载")
			}

			// 模拟流式途中 confirmBinding 的钉入。
			m.mu.Lock()
			session.resumeSessionID = tc.midRunResume
			if tc.midRunBinding != "" {
				m.bindings[sessionID] = agentAIBindingRecord{
					ConversationID:  sessionID,
					NativeSessionID: tc.midRunBinding,
					State:           "confirmed",
				}
			}
			if tc.guardMismatch {
				session.guard = newAgentAIRunGuard(m, run, rec.write)
			}
			m.mu.Unlock()

			code, ok := m.finalizeStalledGuard(guard, rec.write)
			if code != tc.wantCode || ok != tc.wantOK {
				t.Fatalf("finalizeStalledGuard = (%q, %t), want (%q, %t)", code, ok, tc.wantCode, tc.wantOK)
			}

			m.mu.Lock()
			gotResume := session.resumeSessionID
			gotBinding := m.bindings[sessionID].NativeSessionID
			m.mu.Unlock()
			if gotResume != tc.wantResume {
				t.Fatalf("resumeSessionID = %q, want %q", gotResume, tc.wantResume)
			}
			if gotBinding != tc.wantBinding {
				t.Fatalf("binding = %q, want %q", gotBinding, tc.wantBinding)
			}
			if tc.wantOK && tc.wantBinding == "" && tc.midRunBinding != "" {
				// 撤销必须落盘：重启后 loadIdentityState 不能把僵尸绑定读回来。
				path, err := agentAIIdentityPath()
				if err != nil {
					t.Fatalf("identity path: %v", err)
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("persistIdentityState 未被触达: %v", err)
				}
				if strings.Contains(string(raw), tc.midRunBinding) {
					t.Fatalf("identity 文件仍含僵尸绑定 %q: %s", tc.midRunBinding, raw)
				}
			}
		})
	}
}

// TestZombieSuccessWritesFencedWhenGuardReplaced 锁死成功收尾段的纵深防御：
// /clear 把 runSeq 归零后，下一回合会复用同一个 runSeq，只比 runSeq 挡不住
// 旧回合的僵尸迟返回；guard 指针不同即判为非本回合。
//
// 这里只断言 history/journal——恢复点与绑定的撤销是 finalizeStalledGuard 的
// 职责（判死路径必经 ctx.Err() 早退，走不到这段成功收尾）。
func TestZombieSuccessWritesFencedWhenGuardReplaced(t *testing.T) {
	setTestAgentHome(t)
	_, srv := newAgentCoreUpstream(t, agentCoreTextTurn("你好", "，世界"))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")

	m := newAgentAIManager()
	const sessionID = "conv-agentcore-reuse"
	const runSeq = 1
	registerTestAISession(t, m, &agentAISession{
		id:      sessionID,
		runSeq:  runSeq,
		cancel:  func() {},
		history: []agentAIMessage{{Role: "user", Content: "旧回合的提问"}},
	})

	log := &agentCoreEventLog{}

	// 必须用 newAgentAIRunGuard 构造：markRunGuardDone 会对 session.guard
	// 调 markDone()（close(g.done)），裸字面量的 nil done 会 panic。
	currentRun := agentAIRun{sessionID: sessionID, runSeq: runSeq, activity: newAgentAIActivity(), cancel: func() {}}
	currentGuard := newAgentAIRunGuard(m, currentRun, log.write)

	run := agentAIRun{
		sessionID:   sessionID,
		runSeq:      runSeq,
		messageID:   "msg-zombie",
		provider:    "agentcore",
		model:       "glm-5.2",
		projectPath: t.TempDir(),
		activity:    newAgentAIActivity(),
	}
	run.guard = newAgentAIRunGuard(m, run, log.write) // 僵尸自己的 guard

	// 会话当前挂的是「新回合」的 guard，runSeq 却与僵尸相同（/clear 复用）。
	m.mu.Lock()
	m.sessions[sessionID].guard = currentGuard
	m.mu.Unlock()

	m.runAgentCore(context.Background(), run, log.write)

	m.mu.Lock()
	history := append([]agentAIMessage(nil), m.sessions[sessionID].history...)
	m.mu.Unlock()
	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1（guard 不同即非本回合，不得写入）", len(history))
	}
	rows := readAgentCoreJournalRows(t, sessionID)
	if len(rows) == 0 {
		t.Fatal("journal 为空，缺 end 行")
	}
	for _, row := range rows {
		if role, _ := row["role"].(string); role == "assistant" {
			t.Fatalf("journal 出现僵尸 assistant 行：%v", row)
		}
	}
	// endStatus 必须保持默认的 "error"：收尾被守卫拒绝时若记 success，
	// journal 就会出现 end(success) 却查无 assistant 行的自相矛盾。
	last := rows[len(rows)-1]
	if status, _ := last["status"].(string); status != "error" {
		t.Fatalf("journal end status = %q, want error（收尾被守卫拒绝，不得记 success）", status)
	}
	if got := log.count(models.AgentEventAIDone); got != 1 {
		t.Fatalf("ai.done 数量 = %d, want 1", got)
	}

	// 收尾 defer 的 guard 接线必须承重：僵尸退出时不得碰新回合的
	// cancel / guard / done。三条断言各锁一处（clearRunning 的 guard、
	// markRunGuardDone 的 guard、会话复位），删任一处都会让本用例变红。
	m.mu.Lock()
	session := m.sessions[sessionID]
	currentGuardAfter := session.guard
	cancelAfter := session.cancel
	m.mu.Unlock()

	if currentGuardAfter != currentGuard {
		t.Fatalf("session.guard = %p, want 新回合的 guard %p（僵尸收尾不得摘掉新回合的 guard）",
			currentGuardAfter, currentGuard)
	}
	select {
	case <-currentGuard.done:
		t.Fatal("新回合 guard 的 done 被关闭：宽限期强拆保护被僵尸收尾禁用")
	default:
	}
	if cancelAfter == nil {
		t.Fatal("session.cancel 被僵尸收尾清空：新回合的 stop 能力被拆掉")
	}
}

// TestZombieLateRunCLICleanupKeepsNewTurn 锁死 runCLI 收尾 defer 的 guard 身份。
//
// runCLI 在 m.runAgentCore(...) 返回之后还要跑自己的 clearRunning——内核路径
// 是同一 goroutine 内联调用，所以僵尸迟返回时，agentcore 内部那两个带 guard
// 的 defer 会正确跳过，控制权却回到 runCLI 的 defer。若那里只比 runSeq，
// /clear 归零后新回合复用同一个 runSeq，guardOwns 恒真，僵尸就会把新回合的
// cancel / activity / guard / steer 通道一并拆掉。
func TestZombieLateRunCLICleanupKeepsNewTurn(t *testing.T) {
	setTestAgentHome(t)

	m := newAgentAIManager()
	const sessionID = "conv-agentcore-runcli-late"
	const runSeq = 1
	registerTestAISession(t, m, &agentAISession{
		id:      sessionID,
		runSeq:  runSeq,
		cancel:  func() {},
		history: []agentAIMessage{{Role: "user", Content: "旧回合的提问"}},
	})

	log := &agentCoreEventLog{}

	// 新回合（/clear 之后）挂在会话上的 guard A。
	newTurnRun := agentAIRun{sessionID: sessionID, runSeq: runSeq, activity: newAgentAIActivity(), cancel: func() {}}
	newTurnGuard := newAgentAIRunGuard(m, newTurnRun, log.write)
	// 新回合的 steer 通道 C：真实时序里它在僵尸仍在中途时就已挂上（attachCoreSteer
	// 只比 runSeq），所以僵尸收尾时抓到的正是它。
	newTurnSteer := newAgentAICoreSteerControl(newTurnRun, log.write)

	m.mu.Lock()
	m.sessions[sessionID].guard = newTurnGuard
	m.sessions[sessionID].coreSteer = newTurnSteer
	m.mu.Unlock()

	// 上游在**请求到达时**才挂 C——此时内核的attachCoreSteer 已经跑过，
	// 等价于「僵尸在飞、新回合接管会话」的真实交错，且不依赖 sleep。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		if session := m.sessions[sessionID]; session != nil {
			session.guard = newTurnGuard
			session.coreSteer = newTurnSteer
		}
		m.mu.Unlock()

		if _, err := io.ReadAll(r.Body); err != nil {
			t.Errorf("读取请求体失败: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "data: %s\n\n", agentCoreTextTurn("你好", "，世界"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")

	run := agentAIRun{
		sessionID:   sessionID,
		runSeq:      runSeq,
		messageID:   "msg-runcli-zombie",
		provider:    "agentcore",
		model:       "glm-5.2",
		projectPath: t.TempDir(),
		activity:    newAgentAIActivity(),
	}
	run.guard = newAgentAIRunGuard(m, run, log.write) // 僵尸自己的 guard B

	m.runCLI(context.Background(), run, log.write)

	m.mu.Lock()
	session := m.sessions[sessionID]
	cancel := session.cancel
	curGuard := session.guard
	curSteer := session.coreSteer
	m.mu.Unlock()

	if cancel == nil {
		t.Fatal("session.cancel 被僵尸的 runCLI 收尾清空：新回合的 stop 能力被拆掉")
	}
	if curGuard != newTurnGuard {
		t.Fatalf("session.guard = %p, want 新回合的 guard %p（僵尸收尾不得摘掉新回合的 guard）", curGuard, newTurnGuard)
	}
	select {
	case <-newTurnGuard.done:
		t.Fatal("新回合 guard 的 done 被关闭：宽限期强拆保护被僵尸收尾禁用")
	default:
	}
	if curSteer != newTurnSteer {
		t.Fatalf("session.coreSteer = %p, want 新回合的通道 %p（僵尸收尾不得关闭/摘掉新回合的 steer）", curSteer, newTurnSteer)
	}
	if code, err := curSteer.deliver(agentAISteerMessage{MessageID: "late-steer", Content: "还在吗"}); err != nil || code != "queued" {
		t.Fatalf("新回合 steer deliver = (%q, %v), want (queued, nil)：通道已被关闭", code, err)
	}
}

// agentCoreSingleTextTurn 与 agentCoreTextTurn 同形，但把整段文本用
// strconv.Quote 转义后塞进一个 delta——需要换行（aliang-options 围栏）时用它。
func agentCoreSingleTextTurn(text string) string {
	return `{"type":"message_start","message":{"id":"msg_opt","model":"glm-5.2"}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + strconv.Quote(text) + `}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`
}

// TestZombieSuccessDoesNotEmitOptionRequestWhenGuardReplaced 锁死方案请求的
// guard 围栏。
//
// emitOptionRequest 原本只校验 session.runSeq != run.runSeq——那正是本任务
// 废除的不稳定身份（/clear 归零后新回合复用同一个值）。僵尸迟返回时若它仍
// 写 session.pendingOption，新回合正在等用户选择的那张卡就会被顶掉：用户点
// 下去 option_id 对不上，选择被静默丢弃。
//
// 「本回合」子用例是阳性对照：它证明这段 assistant 文本确实能被提取成方案块，
// 免得围栏测试因为文本写错而假绿。
func TestZombieSuccessDoesNotEmitOptionRequestWhenGuardReplaced(t *testing.T) {
	const optionText = "给你两个方案：\n\n```aliang-options\n" +
		`{"title":"选择方案","options":[{"id":"a","label":"方案A"},{"id":"b","label":"方案B"}],"allow_custom":true}` +
		"\n```\n"

	cases := []struct {
		name        string
		zombie      bool
		wantFrames  int
		wantPending bool
	}{
		{name: "本回合照常发方案请求", zombie: false, wantFrames: 1, wantPending: true},
		{name: "僵尸迟返回不发方案请求", zombie: true, wantFrames: 0, wantPending: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setTestAgentHome(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := io.ReadAll(r.Body); err != nil {
					t.Errorf("读取请求体失败: %v", err)
				}
				w.Header().Set("content-type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, "data: %s\n\n", agentCoreSingleTextTurn(optionText))
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}))
			t.Cleanup(srv.Close)
			t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
			t.Setenv("ANTHROPIC_MODEL", "")

			m := newAgentAIManager()
			const sessionID = "conv-agentcore-option-fence"
			const runSeq = 1
			registerTestAISession(t, m, &agentAISession{
				id:      sessionID,
				runSeq:  runSeq,
				cancel:  func() {},
				history: []agentAIMessage{{Role: "user", Content: "旧回合的提问"}},
			})

			log := &agentCoreEventLog{}
			currentRun := agentAIRun{sessionID: sessionID, runSeq: runSeq, activity: newAgentAIActivity(), cancel: func() {}}
			currentGuard := newAgentAIRunGuard(m, currentRun, log.write)

			run := agentAIRun{
				sessionID:   sessionID,
				runSeq:      runSeq,
				messageID:   "msg-option-zombie",
				provider:    "agentcore",
				model:       "glm-5.2",
				projectPath: t.TempDir(),
				activity:    newAgentAIActivity(),
			}
			run.guard = newAgentAIRunGuard(m, run, log.write)

			// 两种形态都让会话挂 currentGuard；本回合把 run.guard 也换成它
			// （身份一致），僵尸则保留自己那个不同的 guard B。
			m.mu.Lock()
			m.sessions[sessionID].guard = currentGuard
			m.mu.Unlock()
			if !tc.zombie {
				run.guard = currentGuard
			}

			m.runAgentCore(context.Background(), run, log.write)

			m.mu.Lock()
			pending := m.sessions[sessionID].pendingOption
			m.mu.Unlock()

			if got := log.count(models.AgentEventAIOptionRequest); got != tc.wantFrames {
				t.Fatalf("ai.option.request 数量 = %d, want %d", got, tc.wantFrames)
			}
			if gotPending := pending != nil; gotPending != tc.wantPending {
				t.Fatalf("session.pendingOption 存在 = %v, want %v（pending=%+v）", gotPending, tc.wantPending, pending)
			}
		})
	}
}

// 【本轮应修-4】L1 半开流必须变成一条 ai.error。
//
// 2026-10-27 事故形态：上游发了半帧就静默，连接既不断也不再有字节，Read 永久
// 阻塞，整轮 run 卡在内核里，手机永远停在「进行中」，而事件流里一条错误都没有。
// idleTimeoutReader 就是为这个形态加的（stream_idle.go），本用例锁死它一路
// 走到手机的效果。
//
// 构造要点（改动前先读完再动）：
//   - 上游只写**一帧**合法 SSE 后保持连接 OPEN。绝不能写完就 return：handler
//     返回会让 transport 收到 FIN，parseSSE 把 EOF 当**正常结束**，用例会退化
//     成"上游优雅收尾"，永远绿。
//   - 窗口用环境变量压到 200ms。默认 90s，真等就是把整包测试拖死；而这条路径
//     之外的任何断言都替代不了它（半开 ≠ EOF ≠ 4xx）。
func TestRunAgentCoreReportsStalledStreamAsError(t *testing.T) {
	setTestAgentHome(t)

	release := make(chan struct{})
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// 一帧合法 SSE：证明"确实收到了上游的字节"，随后的静默才是停滞，
		// 而不是握手失败。
		_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"type":"message_start","message":{"id":"msg_stall","model":"glm-5.2"}}`)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release // 半开：不再写一个字节，也不关连接
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")
	t.Setenv("ALIANG_AGENTCORE_STREAM_IDLE_TIMEOUT", "200ms")

	m := newAgentAIManager()
	const sessionID = "conv-agentcore-stream-stall"
	const runSeq = 1
	m.registerAISessionLocked(&agentAISession{
		id:      sessionID,
		runSeq:  runSeq,
		history: []agentAIMessage{{Role: "user", Content: "你好"}},
	})

	log := &agentCoreEventLog{}
	run := agentAIRun{
		sessionID:   sessionID,
		runSeq:      runSeq,
		messageID:   "msg-stream-stall",
		provider:    "agentcore",
		model:       "glm-5.2",
		projectPath: t.TempDir(),
		activity:    newAgentAIActivity(),
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		m.runAgentCore(context.Background(), run, log.write)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("runAgentCore 未在停滞窗口后返回：半开流把整轮 run 卡死了（事件 %v）", log.types())
	}

	// 恰好一条 ai.error：少一条手机一直「进行中」，多一条手机看到重复错误气泡。
	errs := log.filter(models.AgentEventAIError)
	if len(errs) != 1 {
		t.Fatalf("ai.error 数量 = %d, want 1（序列 %v）", len(errs), log.types())
	}
	msg, _ := errs[0]["error"].(string)
	if strings.TrimSpace(msg) == "" {
		t.Fatalf("ai.error 的 error 为空：手机只会看到一条没有原因的失败 %v", errs[0])
	}
	if got := log.count(models.AgentEventAIDone); got != 0 {
		t.Fatalf("停滞回合不该发 ai.done，实际 %d 条（序列 %v）", got, log.types())
	}
	// 停滞不可重试：重试会让本尝试已下发的 start/text 帧变成重复内容。
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("上游请求次数 = %d, want 1（停滞必须不可重试）", got)
	}
}

// ---------------------------------------------------------------------------
// 失败回合半截输出落盘（Task 14，spec §4.5）
// ---------------------------------------------------------------------------

// journalPartialAssistantRows 取 journal 里的 assistant msg 行。
func journalPartialAssistantRows(t *testing.T, dir, sessionID string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, row := range readJournalLines(t, dir, sessionID) {
		if row["kind"] == "msg" && row["role"] == "assistant" {
			out = append(out, row)
		}
	}
	return out
}

// TestRunAgentCoreStoppedJournalsPartialOutput：停止的回合若已流出一截文本，
// journal 必须留半截输出（-partial 行）——不然跨端/重启回放看到的是「这轮
// 从未存在」，用户停之前读到一半的内容凭空消失。-partial 后缀防与成功行的
// message_id 撞 appendMsg 去重键；僵尸回合（finalized 后迟返回）不落行。
func TestRunAgentCoreStoppedJournalsPartialOutput(t *testing.T) {
	setTestAgentHome(t)
	journalDir := t.TempDir()
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(journalDir)

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "data: "+`{"type":"message_start","message":{"id":"msg_p","model":"glm-5.2"}}`+"\n\n")
		_, _ = fmt.Fprint(w, "data: "+`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
		_, _ = fmt.Fprint(w, "data: "+`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"停前半截"}}`+"\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release // 观察到 ai.delta 并 cancel 后放行
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")

	const sessionID = "conv-partial-stop"
	const runSeq = 2
	registerTestAISession(t, m, &agentAISession{
		id:      sessionID,
		runSeq:  runSeq,
		history: []agentAIMessage{{Role: "user", Content: "写个长篇"}},
	})

	log := &agentCoreEventLog{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.runAgentCore(ctx, agentAIRun{
			sessionID:   sessionID,
			runSeq:      runSeq,
			messageID:   "msg-partial-stop1",
			provider:    "agentcore",
			model:       "glm-5.2",
			projectPath: t.TempDir(),
			activity:    newAgentAIActivity(),
		}, log.write)
	}()

	// 流管线先 capture（output 缓冲）再 writeJSON(ai.delta)，看到 delta 即
	// 半截文本已进缓冲——此时 cancel，无竞态。
	deadline := time.Now().Add(10 * time.Second)
	for log.count(models.AgentEventAIDelta) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("等首个 ai.delta 超时（事件 %v）", log.types())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("cancel 后 runAgentCore 未返回（事件 %v）", log.types())
	}

	rows := journalPartialAssistantRows(t, journalDir, sessionID)
	if len(rows) != 1 {
		t.Fatalf("assistant 行数 = %d, want 恰 1 条 -partial 行: %v", len(rows), rows)
	}
	if rows[0]["message_id"] != "assistant_msg-partial-stop1-partial" {
		t.Errorf("partial 行 message_id = %v, want assistant_msg-partial-stop1-partial", rows[0]["message_id"])
	}
	if text, _ := rows[0]["text"].(string); !strings.Contains(text, "停前半截") {
		t.Errorf("partial 行 text = %q, want 含停前半截", text)
	}
	// end 行必须是 stopped：证明走的确实是真实停止出口。
	jr := readJournalLines(t, journalDir, sessionID)
	last := jr[len(jr)-1]
	if last["kind"] != "end" || last["status"] != "stopped" {
		t.Fatalf("end 行 = %v, want status=stopped（事件 %v）", last, log.types())
	}
}

// TestRunAgentCoreErrorJournalsPartialOutput：中途失败的回合（文本已流出、
// 上游随后报错）同样要留半截输出。该回合走 result.IsError 出口——内核终止帧
// 已转成 ai.error，宿主只补 journal。
func TestRunAgentCoreErrorJournalsPartialOutput(t *testing.T) {
	setTestAgentHome(t)
	journalDir := t.TempDir()
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(journalDir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "data: "+`{"type":"message_start","message":{"id":"msg_e","model":"glm-5.2"}}`+"\n\n")
		_, _ = fmt.Fprint(w, "data: "+`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
		_, _ = fmt.Fprint(w, "data: "+`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"错前半截"}}`+"\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// 裸 JSON 错误信封（2xx 内）：invalid_request_error 不可重试，单请求
		// 即失败——文本已流出、回合中途报错，正是半截形态。
		_, _ = fmt.Fprint(w, `{"error":{"type":"invalid_request_error","message":"mid-stream boom"}}`+"\n\n")
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")

	const sessionID = "conv-partial-err"
	const runSeq = 1
	registerTestAISession(t, m, &agentAISession{
		id:      sessionID,
		runSeq:  runSeq,
		history: []agentAIMessage{{Role: "user", Content: "写点东西"}},
	})

	log := &agentCoreEventLog{}
	m.runAgentCore(context.Background(), agentAIRun{
		sessionID:   sessionID,
		runSeq:      runSeq,
		messageID:   "msg-partial-err1",
		provider:    "agentcore",
		model:       "glm-5.2",
		projectPath: t.TempDir(),
		activity:    newAgentAIActivity(),
	}, log.write)

	if got := log.count(models.AgentEventAIError); got != 1 {
		t.Fatalf("ai.error 数量 = %d, want 1（内核终止帧，序列 %v）", got, log.types())
	}

	rows := journalPartialAssistantRows(t, journalDir, sessionID)
	if len(rows) != 1 {
		t.Fatalf("assistant 行数 = %d, want 恰 1 条 -partial 行: %v", len(rows), rows)
	}
	if rows[0]["message_id"] != "assistant_msg-partial-err1-partial" {
		t.Errorf("partial 行 message_id = %v, want assistant_msg-partial-err1-partial", rows[0]["message_id"])
	}
	if text, _ := rows[0]["text"].(string); !strings.Contains(text, "错前半截") {
		t.Errorf("partial 行 text = %q, want 含错前半截", text)
	}
	jr := readJournalLines(t, journalDir, sessionID)
	last := jr[len(jr)-1]
	if last["kind"] != "end" || last["status"] != "error" {
		t.Fatalf("end 行 = %v, want status=error", last)
	}
}
