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
	"testing"
	"time"

	"aliang.one/nursorgate/app/agentcore"
	"aliang.one/nursorgate/app/http/models"
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
// 关键性质：这些分支**都不问用户**。如果策略短路坏了，require_approval 会真的
// 去等云端审批（超时默认 24h），测试会挂死——所以这里同时是「不许误走云端」
// 的守卫。
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

	// 越界写入 → 项目边界规则直接拒绝（不进策略、不弹审批）。
	resp, err = approve(context.Background(), agentcore.ApprovalRequest{
		ToolName: "Write",
		Input:    map[string]interface{}{"file_path": filepath.Join(string(filepath.Separator), "etc", "passwd")},
	})
	if err != nil {
		t.Fatalf("越界 write 审批返回错误: %v", err)
	}
	if resp.Approved {
		t.Fatalf("越界 write 必须被拒绝，实际 %+v", resp)
	}
	if !strings.Contains(resp.Reason, "策略") {
		t.Errorf("拒绝原因 = %q, want 含「策略」（设备策略拒绝，不是用户拒绝）", resp.Reason)
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
// 头行 + user 行 + assistant 行 + end(success)，顺序固定。
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
	if len(lines) != 4 {
		t.Fatalf("journal 行数 = %d, want 4: %v", len(lines), lines)
	}
	if lines[0]["kind"] != "session" || lines[0]["model"] != "glm-5.2" {
		t.Errorf("头行 = %v", lines[0])
	}
	if lines[1]["role"] != "user" || lines[1]["text"] != "打个招呼" {
		t.Errorf("user 行 = %v（必须是 history 里的真实用户 content）", lines[1])
	}
	if lines[2]["role"] != "assistant" || lines[2]["text"] != "你好，世界" {
		t.Errorf("assistant 行 = %v", lines[2])
	}
	if lines[3]["kind"] != "end" || lines[3]["status"] != "success" {
		t.Errorf("end 行 = %v", lines[3])
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
