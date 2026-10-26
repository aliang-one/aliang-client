package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/agentcore"
	"aliang.one/nursorgate/app/http/models"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func writeTestHomeFile(t *testing.T, home, rel, body string) {
	t.Helper()
	path := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir 失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
}

func TestAgentCoreMCPRawServersIn(t *testing.T) {
	home := t.TempDir()
	writeTestHomeFile(t, home, ".claude.json", `{
		"mcpServers": {
			"github": {"command": "npx", "args": ["-y", "gh-mcp"], "env": {"GH_TOKEN": "secret"}},
			"docs": {"type": "http", "url": "https://mcp.example.com/sse"},
			"off": {"command": "nope", "disabled": true},
			"broken": {"args": ["no-command"]},
			"badtype": {"env": {"A": 1}}
		}
	}`)
	got := agentCoreMCPRawServersIn(home)
	if len(got) != 2 {
		t.Fatalf("条目数 = %d, want 2（disabled 与畸形跳过）: %+v", len(got), got)
	}
	byName := map[string]agentCoreMCPRawServer{}
	for _, s := range got {
		byName[s.Name] = s
	}
	if s := byName["github"]; s.Command != "npx" || len(s.Args) != 2 || s.Env["GH_TOKEN"] != "secret" {
		t.Fatalf("stdio 条目解析错误: %+v", s)
	}
	if s := byName["docs"]; s.URL != "https://mcp.example.com/sse" {
		t.Fatalf("http 条目解析错误: %+v", s)
	}
}

func TestAgentCoreMCPRawServersInPlugins(t *testing.T) {
	home := t.TempDir()
	writeTestHomeFile(t, home, ".claude/plugins/installed_plugins.json",
		`{"plugins": {"reader@market": [{"scope": "user", "installPath": "`+home+`/.claude/plugins/cache/reader"}]}}`)
	writeTestHomeFile(t, home, ".claude/settings.json", `{"enabledPlugins": {"reader@market": true}}`)
	writeTestHomeFile(t, home, ".claude/plugins/cache/reader/.mcp.json",
		`{"mcpServers": {"lookup": {"command": "reader-mcp"}}}`)
	got := agentCoreMCPRawServersIn(home)
	if len(got) != 1 || got[0].Name != "plugin:reader:lookup" || got[0].Command != "reader-mcp" {
		t.Fatalf("插件源解析错误: %+v", got)
	}
}

func TestAgentCoreMCPSanitizeSegment(t *testing.T) {
	cases := map[string]string{
		"plugin:reader:lookup": "plugin_reader_lookup",
		"glm-camp":             "glm-camp",
		"a.b/c":                "a_b_c",
		"":                     "srv",
	}
	for in, want := range cases {
		if got := sanitizeAgentCoreMCPSegment(in); got != want {
			t.Fatalf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
	if got := sanitizeAgentCoreMCPSegment(strings.Repeat("x", 100)); len(got) != 64 {
		t.Fatalf("长段应截 64: %d", len(got))
	}
}

// 桥必须满足内核工具接口（编译期钉死，防签名漂移）。
var _ agentcore.Tool = (*agentCoreMCPBridge)(nil)

// startTestMCPSession 起一个内存 MCP server（零进程）并返回已连接的 client
// session。handler 返回 (text, isError)。注意：ToolHandler 收到的
// Arguments 是 json.RawMessage（go-sdk v1.8.0 语义），这里解成 map 再交给
// handler。
func startTestMCPSession(t *testing.T, toolName string, handler func(args map[string]interface{}) (string, bool)) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test-srv", Version: "0"}, nil)
	server.AddTool(&mcp.Tool{Name: toolName, Description: "测试工具", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}},
	}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := map[string]interface{}{}
		_ = json.Unmarshal(req.Params.Arguments, &args)
		text, isErr := handler(args)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: isErr}, nil
	})
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(context.Background(), serverTransport, nil); err != nil {
		t.Fatalf("server 连接失败: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-cli", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client 连接失败: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestAgentCoreMCPBridgeExecute(t *testing.T) {
	session := startTestMCPSession(t, "lookup", func(args map[string]interface{}) (string, bool) {
		if args["q"] == "boom" {
			return "查无此人", true
		}
		return "结果:" + args["q"].(string), false
	})
	var journaled []string
	bridge := &agentCoreMCPBridge{
		session: session, rawTool: "lookup", name: "mcp__test-srv__lookup",
		description: "测试工具",
		schema:      map[string]interface{}{"type": "object"},
		onCall:      func(tool, summary string, isErr bool) { journaled = append(journaled, tool+"|"+summary) },
	}
	res := bridge.Execute(context.Background(), map[string]interface{}{"q": "hi"}, nil)
	if res.IsError || !strings.Contains(res.Content, "结果:hi") {
		t.Fatalf("执行结果错误: %+v", res)
	}
	res = bridge.Execute(context.Background(), map[string]interface{}{"q": "boom"}, nil)
	if !res.IsError || !strings.Contains(res.Content, "查无此人") {
		t.Fatalf("IsError 语义错误: %+v", res)
	}
	if len(journaled) != 2 || !strings.HasPrefix(journaled[0], "mcp__test-srv__lookup|") {
		t.Fatalf("journal 回调缺失: %v", journaled)
	}
}

func TestAgentCoreMCPBridgeRecoversPanic(t *testing.T) {
	session := startTestMCPSession(t, "evil", func(map[string]interface{}) (string, bool) {
		return "", false
	})
	bridge := &agentCoreMCPBridge{session: session, rawTool: "evil", name: "mcp__s__evil"}
	// onCall 里故意 panic：Execute 必须兜住不炸整轮（纵深防御）。
	bridge.onCall = func(string, string, bool) { panic("journal boom") }
	res := bridge.Execute(context.Background(), map[string]interface{}{}, nil)
	if res.IsError {
		t.Fatalf("onCall panic 不应影响已成功的调用结果: %+v", res)
	}
}

// 注：in-memory server 不分页，这里测的是单页路径。多页 cursor 跟随
// （listAgentCoreMCPTools 的 nextCursor 循环）未覆盖——需手写分页
// JSON-RPC transport，已记账。
func TestAgentCoreMCPListToolsSinglePage(t *testing.T) {
	session := startTestMCPSession(t, "t1", func(map[string]interface{}) (string, bool) { return "ok", false })
	tools, err := listAgentCoreMCPTools(context.Background(), session)
	if err != nil {
		t.Fatalf("ListTools 失败: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "t1" {
		t.Fatalf("工具列表错误: %+v", tools)
	}
}

func TestTruncateAgentCoreMCPOutput(t *testing.T) {
	small := truncateAgentCoreMCPOutput("短输出")
	if small != "短输出" {
		t.Fatalf("不超限应原样: %q", small)
	}
	big := strings.Repeat("a", 70*1024) + "\n" + strings.Repeat("b", 10)
	got := truncateAgentCoreMCPOutput(big)
	if len(got) >= len(big) || !strings.Contains(got, "原始") {
		t.Fatalf("超限应截断并注明: len=%d", len(got))
	}
}

func TestAgentCoreMCPBridgeName(t *testing.T) {
	seen := map[string]bool{}
	names := []string{
		agentCoreMCPBridgeName(strings.Repeat("s", 100), strings.Repeat("t", 100), seen), // 极端长 server+tool：5+64+2+64=135 必须钳回 128
		agentCoreMCPBridgeName(strings.Repeat("s", 100), strings.Repeat("t", 100), seen), // 同名再注册：序号消解且仍 ≤128
		agentCoreMCPBridgeName("a.b", "lookup", seen),                                    // 清洗后 a_b
		agentCoreMCPBridgeName("a:b", "lookup", seen),                                    // 清洗后同段：不许静默遮蔽
	}
	dup := map[string]bool{}
	for _, n := range names {
		if len(n) > 128 {
			t.Fatalf("工具名超 128: len=%d %q", len(n), n)
		}
		for _, r := range n {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			default:
				t.Fatalf("工具名含非法字符 %q: %q", r, n)
			}
		}
		if dup[n] {
			t.Fatalf("工具名重复: %q", n)
		}
		dup[n] = true
	}
	if names[2] == names[3] {
		t.Fatalf("清洗后同段的两个 server 不得同名: %q", names[2])
	}
	if !strings.HasPrefix(names[2], "mcp__a_b__lookup") || !strings.HasPrefix(names[3], "mcp__a_b__lookup") {
		t.Fatalf("base 拼装错误: %q %q", names[2], names[3])
	}
}

func TestAgentCoreMCPServerToolDefsSkipsHeaders(t *testing.T) {
	m := &agentAIManager{}
	bridges, closeAll := m.agentCoreMCPServerToolDefs(context.Background(), []agentCoreMCPRawServer{
		{Name: "authed", Type: "http", URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer x"}},
	}, nil)
	defer closeAll()
	if len(bridges) != 0 {
		t.Fatalf("带 headers 的 http server 应预判跳过（不 dial）: %+v", bridges)
	}
}

func TestAgentCoreMCPBridgeRecoversOuterPanic(t *testing.T) {
	var journaled []bool
	// session 置 nil：CallTool 里解引用 nil 必 panic，打的是外层兜底路径。
	bridge := &agentCoreMCPBridge{session: nil, rawTool: "x", name: "mcp__s__x",
		onCall: func(tool, summary string, isErr bool) { journaled = append(journaled, isErr) }}
	res := bridge.Execute(context.Background(), map[string]interface{}{}, nil)
	if !res.IsError || !strings.Contains(res.Content, "MCP 工具执行崩溃") {
		t.Fatalf("外层 panic 应定向报错: %+v", res)
	}
	if strings.Contains(res.Content, "goroutine") {
		t.Fatalf("报错文案不得泄漏 panic 栈: %q", res.Content)
	}
	if len(journaled) != 1 || !journaled[0] {
		t.Fatalf("journal 应记 isErr=true: %v", journaled)
	}
}

func TestAgentCoreMCPServerToolDefsSkipsDeadServer(t *testing.T) {
	m := &agentAIManager{}
	bridges, closeAll := m.agentCoreMCPServerToolDefs(context.Background(), []agentCoreMCPRawServer{
		{Name: "ghost", Command: "definitely-not-a-real-binary"},
	}, nil)
	defer closeAll()
	if len(bridges) != 0 {
		t.Fatalf("dial 失败的 server 应跳过: %+v", bridges)
	}
}

// startHTTPTestMCPServer 起一个真 streamable HTTP MCP server（SDK 现成
// handler），返回其 /mcp 端点。用于打通 dial→list→bridge 全链真 transport。
func startHTTPTestMCPServer(t *testing.T, toolNames []string, handler func(args map[string]interface{}) (string, bool)) string {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "http-srv", Version: "0"}, nil)
	for _, name := range toolNames {
		server.AddTool(&mcp.Tool{Name: name, Description: "测试工具", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}},
		}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := map[string]interface{}{}
			_ = json.Unmarshal(req.Params.Arguments, &args)
			text, isErr := handler(args)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: isErr}, nil
		})
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

func TestAgentCoreMCPServerToolDefsHTTPDial(t *testing.T) {
	endpoint := startHTTPTestMCPServer(t, []string{"echo"}, func(args map[string]interface{}) (string, bool) {
		return "回显:" + args["q"].(string), false
	})
	m := &agentAIManager{}
	bridges, closeAll := m.agentCoreMCPServerToolDefs(context.Background(), []agentCoreMCPRawServer{
		{Name: "remote", Type: "http", URL: endpoint},
	}, nil)
	defer closeAll()
	if len(bridges) != 1 {
		t.Fatalf("http dial 应产出 1 桥: %+v", bridges)
	}
	res := bridges[0].Execute(context.Background(), map[string]interface{}{"q": "hi"}, nil)
	if res.IsError || !strings.Contains(res.Content, "回显:hi") {
		t.Fatalf("全链执行失败: %+v", res)
	}
}

func TestAgentCoreMCPServerToolDefsToolCap(t *testing.T) {
	names := make([]string, 130)
	for i := range names {
		names[i] = "t" + strconv.Itoa(i)
	}
	endpoint := startHTTPTestMCPServer(t, names, func(map[string]interface{}) (string, bool) { return "ok", false })
	m := &agentAIManager{}
	bridges, closeAll := m.agentCoreMCPServerToolDefs(context.Background(), []agentCoreMCPRawServer{
		{Name: "big", Type: "http", URL: endpoint},
	}, nil)
	defer closeAll()
	if len(bridges) != agentCoreMCPToolCap {
		t.Fatalf("130 工具应熔断在 cap=%d, got %d", agentCoreMCPToolCap, len(bridges))
	}
	uniq := map[string]bool{}
	for _, b := range bridges {
		uniq[b.Name()] = true
	}
	if len(uniq) != len(bridges) {
		t.Fatalf("熔断后不得重名: %d/%d", len(uniq), len(bridges))
	}
}

func TestAgentCoreMCPBridgeTransportError(t *testing.T) {
	var isErrs []bool
	session := startTestMCPSession(t, "t", func(map[string]interface{}) (string, bool) { return "ok", false })
	bridge := &agentCoreMCPBridge{session: session, rawTool: "t", name: "mcp__s__t",
		onCall: func(tool, summary string, isErr bool) { isErrs = append(isErrs, isErr) }}
	if err := session.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	res := bridge.Execute(context.Background(), map[string]interface{}{}, nil)
	if !res.IsError || !strings.Contains(res.Content, "MCP 调用失败") {
		t.Fatalf("关停后调用应走错误路径（非 panic）: %+v", res)
	}
	if len(isErrs) != 1 || !isErrs[0] {
		t.Fatalf("onCall 应收到 isErr=true: %v", isErrs)
	}
}

// ---------------------------------------------------------------------------
// MCP 默认审批门（spec §7）
//
// matchedID 为空（无显式设备规则命中、落到策略 DefaultDecision）的 mcp__*
// 工具一律强制手机审批；显式规则（含放行）优先于该默认。全部走真
// runAgentCore：内核在注册表派发**之前**先过 Approve（loop.go executeOne），
// 所以即便 MCP 桥接线（入注册表）尚未落地，脚本的 mcp__ tool_use 也真实地
// 穿过审批门。
// ---------------------------------------------------------------------------

// agentCoreMCPApprovalHarness 聚合一次 run 的manager/事件日志/上游/结束信号。
type agentCoreMCPApprovalHarness struct {
	m         *agentAIManager
	log       *agentCoreEventLog
	up        *agentCoreUpstream
	finished  chan struct{}
	sessionID string
}

// setAgentCoreMCPTestPolicy 把策略注进 service 的内存 per-path 表。
// effectiveApprovalPolicyForPath 内存优先于磁盘缓存，因此每个用例看到的
// 策略完全由这里决定——互不渗漏，也不受真实磁盘缓存影响。
func setAgentCoreMCPTestPolicy(t *testing.T, svc *AgentService, projectPath string, p ApprovalPolicy) {
	t.Helper()
	if err := compileApprovalRules(&p); err != nil {
		t.Fatalf("测试策略 CommandRegex 编译失败: %v", err)
	}
	svc.mu.Lock()
	svc.setEffectivePolicyForPathLocked(projectPath, p)
	svc.mu.Unlock()
}

// startAgentCoreMCPApprovalRun 与 Decline 用例同一套骨架：真 runAgentCore +
// 事件收集器 + 上游记录器。策略在 run 启动前注入（审批只发生在 run 内，
// 先注后跑即确定）。
func startAgentCoreMCPApprovalRun(t *testing.T, policy ApprovalPolicy, turns ...string) *agentCoreMCPApprovalHarness {
	t.Helper()
	setTestAgentHome(t) // journal 根在 newAgentAIManager 构造时解析
	up, srv := newAgentCoreUpstream(t, turns...)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")

	m := newAgentAIManager()
	// 空 service：approvalService() 不落 GetSharedAgentService()（测试进程里
	// 那个可能带真实云端配置）；策略经内存表注入。
	svc := &AgentService{}
	m.service = svc

	project := t.TempDir()
	setAgentCoreMCPTestPolicy(t, svc, project, policy)

	const sessionID = "conv-agentcore-mcp-approval"
	const runSeq = 1
	m.registerAISessionLocked(&agentAISession{
		id:      sessionID,
		runSeq:  runSeq,
		history: []agentAIMessage{{Role: "user", Content: "调一下工具"}},
	})

	log := &agentCoreEventLog{}
	run := agentAIRun{
		sessionID:   sessionID,
		runSeq:      runSeq,
		messageID:   "msg-mcp-approval",
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
	return &agentCoreMCPApprovalHarness{m: m, log: log, up: up, finished: finished, sessionID: sessionID}
}

// decline 等到手机审批卡并模拟点「拒绝」。respond 带缓冲(1)，发送不阻塞。
func (h *agentCoreMCPApprovalHarness) decline(t *testing.T) {
	t.Helper()
	waiter := waitForAgentCoreApproval(t, h.m, h.sessionID, h.log)
	waiter.request.respond <- agentAIApprovalResponse{Decision: models.AgentAIApprovalDecisionDecline}
}

// waitFinished 等 run 收敛——「审批拒绝后回合继续存活」的存活断言入口。
func (h *agentCoreMCPApprovalHarness) waitFinished(t *testing.T) {
	t.Helper()
	select {
	case <-h.finished:
	case <-time.After(30 * time.Second):
		t.Fatalf("runAgentCore 没有收敛（事件 %v）", h.log.types())
	}
}

// lastToolResult 取第二轮上游请求里的 tool_result（内容 + is_error）。
// 第二轮存在本身即证明工具回合后模型还在继续对话（未被审批掐死）。
func (h *agentCoreMCPApprovalHarness) lastToolResult(t *testing.T) (string, bool) {
	t.Helper()
	reqs := h.up.requests()
	if len(reqs) != 2 {
		t.Fatalf("上游请求次数 = %d, want 2（工具回合后应带 tool_result 继续下一轮，事件 %v）", len(reqs), h.log.types())
	}
	second := reqs[1]
	if len(second.Messages) < 3 {
		t.Fatalf("第二轮 messages 只有 %d 条, want ≥3（user/assistant(tool_use)/user(tool_result)）", len(second.Messages))
	}
	last := second.Messages[len(second.Messages)-1]
	blocks, _ := last.Content.([]interface{})
	for _, raw := range blocks {
		block, _ := raw.(map[string]interface{})
		if block == nil {
			continue
		}
		if bt, _ := block["type"].(string); bt == agentcore.BlockTypeToolResult {
			content, _ := block["content"].(string)
			isErr, _ := block["is_error"].(bool)
			return content, isErr
		}
	}
	t.Fatalf("第二轮最后一条消息没有 tool_result: %+v", last)
	return "", false
}

// TestAgentCoreMCPApprovalDefaultGate 锁死 MCP 默认审批门的三个口径：
//
//  1. 无显式规则命中（规则不命中/零规则）+ mcp__ 前缀 → 强制手机审批，
//     审批拒绝后回合继续存活（照抄 Decline 用例的存活断言）；
//  2. 显式设备规则优先于默认门——精确匹配的 auto_approve 规则照常短路，
//     手机不收卡、内核照常派发执行；
//  3. allow_all 策略（DefaultDecision=auto_approve、零规则）下 mcp__* 仍需
//     批，而同策略的非 mcp 工具维持原行为（默认放行，不被门拦截）。
//
// 场景 1/3 的策略默认全是放行：没有默认门时这些调用会被本地短路、手机
// 收不到任何审批卡——「waitForAgentCoreApproval 等得到等待项」就是门生效
// 的判据，等不到即红。
func TestAgentCoreMCPApprovalDefaultGate(t *testing.T) {
	const mcpTool = "mcp__srv__tool"

	t.Run("NoRuleMatchRequiresApproval", func(t *testing.T) {
		policy := ApprovalPolicy{
			Scheme:          "custom",
			DefaultDecision: decisionAutoApprove,
			// 唯一规则只碰别的工具：证明「规则不命中」时门仍然生效。
			Rules: []approvalRule{{
				ID:       "unrelated-tool",
				Match:    approvalRuleMatch{Tool: []string{"Other"}},
				Decision: decisionAutoApprove,
			}},
		}
		h := startAgentCoreMCPApprovalRun(t, policy,
			agentCoreToolTurn("toolu_mcp1", mcpTool, `{"q":"ping"}`),
			agentCoreTextTurn("好", "的"),
		)
		h.decline(t)
		h.waitFinished(t)

		// 手机审批路径真的走了：pending + resolved/decline 两条，工具名原样上卡。
		requests := h.log.filter(models.AgentEventAIApprovalRequest)
		if len(requests) != 2 {
			t.Fatalf("ai.approval.request 数量 = %d, want 2（pending + resolved，事件 %v）", len(requests), h.log.types())
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
		if tool, _ := requests[0]["tool_name"].(string); tool != mcpTool {
			t.Errorf("审批 tool_name = %q, want %q", tool, mcpTool)
		}

		// 回合存活：恰好一个 ai.done、零 ai.error（拒绝一次工具不是失败）。
		if got := h.log.count(models.AgentEventAIDone); got != 1 {
			t.Fatalf("ai.done 数量 = %d, want 1（事件 %v）", got, h.log.types())
		}
		if got := h.log.count(models.AgentEventAIError); got != 0 {
			t.Fatalf("ai.error 数量 = %d, want 0（%v）", got, h.log.filter(models.AgentEventAIError))
		}

		// 内核收到的是拒绝理由，不是放行后的执行结果。
		content, isErr := h.lastToolResult(t)
		if !isErr || !strings.Contains(content, "用户拒绝") {
			t.Fatalf("tool_result = (%q, is_error=%v), want 含「用户拒绝」的错误回填", content, isErr)
		}
	})

	t.Run("ExplicitRuleOverridesDefaultGate", func(t *testing.T) {
		// 默认 require_approval：放行只能来自显式规则本身（matchedID 非空），
		// 默认门不得越权改判。
		policy := ApprovalPolicy{
			Scheme:          "custom",
			DefaultDecision: decisionRequireApproval,
			Rules: []approvalRule{{
				ID:       "mcp-explicit-allow",
				Match:    approvalRuleMatch{Tool: []string{mcpTool}},
				Decision: decisionAutoApprove,
			}},
		}
		h := startAgentCoreMCPApprovalRun(t, policy,
			agentCoreToolTurn("toolu_mcp2", mcpTool, `{"q":"ping"}`),
			agentCoreTextTurn("好", "的"),
		)
		// 不 decline：若误走云端审批，run 会吊在等手机上，waitFinished 超时即红。
		h.waitFinished(t)

		// 短路放行：零审批事件、零 ai.error、恰好一个 ai.done。
		if got := h.log.count(models.AgentEventAIApprovalRequest); got != 0 {
			t.Fatalf("ai.approval.request 数量 = %d, want 0（显式放行不问手机，事件 %v）", got, h.log.types())
		}
		if got := h.log.count(models.AgentEventAIDone); got != 1 {
			t.Fatalf("ai.done 数量 = %d, want 1（事件 %v）", got, h.log.types())
		}
		if got := h.log.count(models.AgentEventAIError); got != 0 {
			t.Fatalf("ai.error 数量 = %d, want 0（%v）", got, h.log.filter(models.AgentEventAIError))
		}

		// 审批通过、内核真的派发了执行：回填不是「用户拒绝」。MCP 桥接入
		// 注册表是后续接线（Task 5），此刻内核回报「未知工具」——恰恰证明
		// 已越过审批门到达派发；接线落地后此处应改为断言真实执行结果。
		content, _ := h.lastToolResult(t)
		if strings.Contains(content, "用户拒绝") || strings.Contains(content, "审批") {
			t.Fatalf("tool_result = %q, want 非拒绝（显式规则应放行到执行派发）", content)
		}
		if !strings.Contains(content, "未知工具") {
			t.Fatalf("tool_result = %q, want 含「未知工具」（接线前的派发证据）", content)
		}
	})

	t.Run("AllowAllStillGatesMCP", func(t *testing.T) {
		// allow_all（DefaultDecision=auto_approve、零规则）：别的工具全放行，
		// mcp__* 也不能豁免默认门。
		h := startAgentCoreMCPApprovalRun(t, builtinAllowAllPolicy(),
			agentCoreToolTurn("toolu_mcp3", "mcp__db__query", `{"sql":"select 1"}`),
			agentCoreTextTurn("好", "的"),
		)
		h.decline(t)
		h.waitFinished(t)

		if got := h.log.count(models.AgentEventAIApprovalRequest); got != 2 {
			t.Fatalf("ai.approval.request 数量 = %d, want 2（allow_all 不豁免 mcp__*，事件 %v）", got, h.log.types())
		}
		if got := h.log.count(models.AgentEventAIDone); got != 1 {
			t.Fatalf("ai.done 数量 = %d, want 1（事件 %v）", got, h.log.types())
		}
		if got := h.log.count(models.AgentEventAIError); got != 0 {
			t.Fatalf("ai.error 数量 = %d, want 0（%v）", got, h.log.filter(models.AgentEventAIError))
		}
		content, isErr := h.lastToolResult(t)
		if !isErr || !strings.Contains(content, "用户拒绝") {
			t.Fatalf("tool_result = (%q, is_error=%v), want 含「用户拒绝」的错误回填", content, isErr)
		}
	})

	t.Run("AllowAllDoesNotGateNonMCP", func(t *testing.T) {
		// 同为 allow_all、同样无显式规则：非 mcp 工具维持原行为——默认放行，
		// 不因默认门被拦去问手机。"Zap" 既不是路径围栏工具也不是 Bash，
		// 边界钩子不约束它，判定点干净。
		h := startAgentCoreMCPApprovalRun(t, builtinAllowAllPolicy(),
			agentCoreToolTurn("toolu_zap", "Zap", `{"x":1}`),
			agentCoreTextTurn("好", "的"),
		)
		h.waitFinished(t)

		if got := h.log.count(models.AgentEventAIApprovalRequest); got != 0 {
			t.Fatalf("ai.approval.request 数量 = %d, want 0（非 mcp 工具不得被门拦截，事件 %v）", got, h.log.types())
		}
		if got := h.log.count(models.AgentEventAIDone); got != 1 {
			t.Fatalf("ai.done 数量 = %d, want 1（事件 %v）", got, h.log.types())
		}
		if got := h.log.count(models.AgentEventAIError); got != 0 {
			t.Fatalf("ai.error 数量 = %d, want 0（%v）", got, h.log.filter(models.AgentEventAIError))
		}
		content, _ := h.lastToolResult(t)
		if strings.Contains(content, "用户拒绝") || strings.Contains(content, "审批") {
			t.Fatalf("tool_result = %q, want 非拒绝（allow_all 默认放行非 mcp 工具）", content)
		}
	})
}
