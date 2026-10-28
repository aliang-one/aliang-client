package services

import (
	"context"
	"encoding/json"
	"errors"
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

// 串行 dial 的尾延迟是乘法：单 server 卡满 10s，24 个就是 4 分钟启动空窗。
// 这里钉死「两个 server 同时在飞」——用屏障代替计时，串行实现会在第一个
// dial 上等满超时而失败。
func TestAgentCoreMCPServerToolDefsDialsConcurrently(t *testing.T) {
	session := startTestMCPSession(t, "echo", func(map[string]interface{}) (string, bool) { return "ok", false })
	var arrived int32
	release := make(chan struct{})
	var once sync.Once
	m := &agentAIManager{}
	m.coreMCPDialForTest = func(ctx context.Context, _ agentCoreMCPRawServer) (*mcp.ClientSession, func(), error) {
		if atomic.AddInt32(&arrived, 1) == 2 {
			once.Do(func() { close(release) })
		}
		select {
		case <-release:
		case <-time.After(3 * time.Second):
			return nil, nil, errors.New("dial 未并发：第二个 server 等不到同时建连")
		}
		return session, func() {}, nil
	}
	bridges, closeAll := m.agentCoreMCPServerToolDefs(context.Background(), []agentCoreMCPRawServer{
		{Name: "alpha", Command: "x"},
		{Name: "beta", Command: "y"},
	}, nil)
	defer closeAll()
	if len(bridges) != 2 {
		t.Fatalf("两个 server 应各产出 1 桥（并发建连）: %+v", bridges)
	}
	// 工具顺序必须与配置顺序一致：顺序变了上游 prompt 缓存全部失效。
	if !strings.HasPrefix(bridges[0].Name(), "mcp__alpha__") || !strings.HasPrefix(bridges[1].Name(), "mcp__beta__") {
		t.Fatalf("桥顺序应与 server 配置顺序一致: %q %q", bridges[0].Name(), bridges[1].Name())
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

// 回归（审计 ①）：SSE 传输的 GET 长流绑在建连 ctx 上（go-sdk v1.8.0 用
// NewRequestWithContext 建 GET 并在其退出时关传输）。旧实现 WithTimeout(10s)
// 且枚举完工具立即 cancel，等于把刚建好的 SSE 连接掐死。此处钉死三条：
// 枚举后会话 ctx 未取消、无 deadline、closeAll 后必须被 cancel（防泄漏）。
func TestAgentCoreMCPServerToolDefsSessionContextLifetime(t *testing.T) {
	session := startTestMCPSession(t, "lookup", func(map[string]interface{}) (string, bool) { return "ok", false })
	m := &agentAIManager{}
	var sessionCtx context.Context
	var closeFnCalls int32
	m.coreMCPDialForTest = func(ctx context.Context, _ agentCoreMCPRawServer) (*mcp.ClientSession, func(), error) {
		sessionCtx = ctx
		return session, func() { atomic.AddInt32(&closeFnCalls, 1) }, nil
	}
	bridges, closeAll := m.agentCoreMCPServerToolDefs(context.Background(), []agentCoreMCPRawServer{
		{Name: "sse-like", Type: "sse", URL: "https://mcp.example.com/sse"},
	}, nil)
	if len(bridges) != 1 {
		t.Fatalf("应产出 1 桥: %+v", bridges)
	}
	if sessionCtx == nil {
		t.Fatal("dial 未被调用")
	}
	if err := sessionCtx.Err(); err != nil {
		t.Fatalf("枚举完成后会话 ctx 不得取消（SSE 长流会被掐断）: %v", err)
	}
	if _, ok := sessionCtx.Deadline(); ok {
		t.Fatal("会话 ctx 不得带 deadline（SSE 长流到期即断）")
	}
	closeAll()
	if err := sessionCtx.Err(); err != context.Canceled {
		t.Fatalf("closeAll 后会话 ctx 应被 cancel，got %v", err)
	}
	if atomic.LoadInt32(&closeFnCalls) != 1 {
		t.Fatalf("closeFn 调用次数 = %d, want 1", closeFnCalls)
	}
}

// agentCoreMCPDialWithTimeout 的超时路径：握手不绑会话 ctx deadline，超时由
// 定时器外部施加；迟到返回的会话必须被收尸关闭（防孤儿进程/连接）。
func TestAgentCoreMCPDialWithTimeoutDrainsLateSession(t *testing.T) {
	var drained int32
	dial := func(ctx context.Context, _ agentCoreMCPRawServer) (*mcp.ClientSession, func(), error) {
		<-ctx.Done()
		return nil, func() { atomic.AddInt32(&drained, 1) }, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := time.Now()
	session, closeFn, err := agentCoreMCPDialWithTimeout(dial, ctx, agentCoreMCPRawServer{Name: "slow"}, 50*time.Millisecond)
	if session != nil || closeFn != nil || err == nil {
		t.Fatalf("超时应返回 (nil, nil, err)，got session=%v closeFn!=nil=%v err=%v", session, closeFn != nil, err)
	}
	if !strings.Contains(err.Error(), "connect timeout") {
		t.Fatalf("错误文案应含 connect timeout: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("超时未按 50ms 生效: %s", elapsed)
	}
	// 调用方超时后会 cancel ctx——握手 goroutine 随即返回，收尸路径关闭会话。
	cancel()
	deadline := time.Now().Add(agentCoreMCPDialDrainGrace + time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&drained) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&drained) != 1 {
		t.Fatalf("迟到返回的 closeFn 未被收尸调用（泄漏）")
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
	return startAgentCoreApprovalHarness(t, policy, turns)
}

// startAgentCoreMCPApprovalRunWithMCP 在骨架之上再注入一条 MCP server：把
// server 条目写进测试 home 的 .claude.json（走真实采集路径
// agentCoreMCPRawServersIn），dial 替换成直接返回内存 session（忽略 raw，
// closeFn 置空——session 由 startTestMCPSession 的 t.Cleanup 关）。
// 需要 mcp__ 工具「真实执行到 handler」的场景（显式放行后的派发、端到端
// 桥接）用它；mcpServerName 留空即退回无 MCP 的原骨架。
func startAgentCoreMCPApprovalRunWithMCP(t *testing.T, policy ApprovalPolicy, mcpServerName string, mcpSession *mcp.ClientSession, turns ...string) *agentCoreMCPApprovalHarness {
	t.Helper()
	return startAgentCoreApprovalHarness(t, policy, turns, harnessMCP(mcpServerName, mcpSession))
}

// agentCoreHarnessOptions 是 startAgentCoreApprovalHarness 的可参数化项
// （零值=原骨架常量，见 default 字段填充）。既有用例经上面两个薄包装以
// 默认值进入；新用例（子 agent e2e 系）用 options 覆写 session/messageID/
// 首条 user 消息/readOnly/子 agent 定义文件。
type agentCoreHarnessOptions struct {
	sessionID     string
	messageID     string
	firstUser     string
	readOnly      bool
	mcpServerName string
	mcpSession    *mcp.ClientSession
	// subagentFiles 是 .claude/agents/ 下的 文件名→内容。必须在
	// setTestAgentHome **之后**由 harness 代写——调用方预写会被 harness
	// 换 HOME 掉（T3 偏差③的顺序坑，收敛时一并修掉）。
	subagentFiles map[string]string
}

type agentCoreHarnessOption func(*agentCoreHarnessOptions)

func harnessSession(sessionID, messageID string) agentCoreHarnessOption {
	return func(o *agentCoreHarnessOptions) { o.sessionID = sessionID; o.messageID = messageID }
}

func harnessFirstUser(text string) agentCoreHarnessOption {
	return func(o *agentCoreHarnessOptions) { o.firstUser = text }
}

func harnessReadOnly() agentCoreHarnessOption {
	return func(o *agentCoreHarnessOptions) { o.readOnly = true }
}

func harnessMCP(serverName string, session *mcp.ClientSession) agentCoreHarnessOption {
	return func(o *agentCoreHarnessOptions) { o.mcpServerName = serverName; o.mcpSession = session }
}

func harnessSubagentDefs(files map[string]string) agentCoreHarnessOption {
	return func(o *agentCoreHarnessOptions) { o.subagentFiles = files }
}

// startAgentCoreApprovalHarness 是参数化的组装核心：真 runAgentCore + 事件
// 收集器 + 上游记录器 + 空内存 service 策略注入。options 未覆写的字段取
// 原骨架默认（session/messageID 常量、首条 user 消息「调一下工具」）。
func startAgentCoreApprovalHarness(t *testing.T, policy ApprovalPolicy, turns []string, opts ...agentCoreHarnessOption) *agentCoreMCPApprovalHarness {
	t.Helper()
	cfgOptions := agentCoreHarnessOptions{
		sessionID: "conv-agentcore-mcp-approval",
		messageID: "msg-mcp-approval",
		firstUser: "调一下工具",
	}
	for _, opt := range opts {
		opt(&cfgOptions)
	}

	setTestAgentHome(t) // journal 根与 MCP 采集都从 HOME 解析，双隔离
	for name, body := range cfgOptions.subagentFiles {
		writeTestHomeFile(t, os.Getenv("HOME"), filepath.Join(".claude", "agents", name), body)
	}
	if cfgOptions.mcpServerName != "" && cfgOptions.mcpSession != nil {
		writeTestHomeFile(t, os.Getenv("HOME"), ".claude.json",
			`{"mcpServers":{"`+cfgOptions.mcpServerName+`":{"command":"injected-for-test"}}}`)
	}
	up, srv := newAgentCoreUpstream(t, turns...)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")

	m := newAgentAIManager()
	if cfgOptions.mcpServerName != "" && cfgOptions.mcpSession != nil {
		m.coreMCPDialForTest = func(context.Context, agentCoreMCPRawServer) (*mcp.ClientSession, func(), error) {
			return cfgOptions.mcpSession, func() {}, nil
		}
	}
	// 空 service：approvalService() 不落 GetSharedAgentService()（测试进程里
	// 那个可能带真实云端配置）；策略经内存表注入。
	svc := &AgentService{}
	m.service = svc

	project := t.TempDir()
	setAgentCoreMCPTestPolicy(t, svc, project, policy)

	runSeq := 1
	registerTestAISession(t, m, &agentAISession{
		id:      cfgOptions.sessionID,
		runSeq:  runSeq,
		history: []agentAIMessage{{Role: "user", Content: cfgOptions.firstUser}},
	})

	log := &agentCoreEventLog{}
	run := agentAIRun{
		sessionID:   cfgOptions.sessionID,
		runSeq:      runSeq,
		messageID:   cfgOptions.messageID,
		provider:    "agentcore",
		model:       "glm-5.2",
		projectPath: project,
		activity:    newAgentAIActivity(),
		readOnly:    cfgOptions.readOnly,
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		m.runAgentCore(context.Background(), run, log.write)
	}()
	return &agentCoreMCPApprovalHarness{m: m, log: log, up: up, finished: finished, sessionID: cfgOptions.sessionID}
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
		// 注入同名 MCP 工具（server=srv + tool=tool → 桥名恰为 mcpTool）：
		// 显式放行后内核必须真实执行到 handler——桥已接入注册表，
		// 「未知工具」不允许再出现。
		session := startTestMCPSession(t, "tool", func(args map[string]interface{}) (string, bool) {
			q, _ := args["q"].(string)
			return "pong:" + q, false
		})
		h := startAgentCoreMCPApprovalRunWithMCP(t, policy, "srv", session,
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

		// 审批放行 + 桥注册 + 内核派发全链穿透：tool_result 是 handler 的
		// 真实返回，非「未知工具」、非拒绝、非错误。
		content, isErr := h.lastToolResult(t)
		if isErr {
			t.Fatalf("tool_result is_error=true, want 真实执行（%q）", content)
		}
		if !strings.Contains(content, "pong:ping") {
			t.Fatalf("tool_result = %q, want 含 pong:ping（显式放行→桥→内核全链真实执行）", content)
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

// ---------------------------------------------------------------------------
// 端到端：runAgentCore 的 MCP 工具注入全链（spec §3）
// ---------------------------------------------------------------------------

// TestRunAgentCoreMCPEndToEnd 锁死 MCP 注入在真 run 里的完整链路：
//
//	采集（home 的 .claude.json）→ 建连（dial 注入的内存 session）→ 包桥注册
//	（Registry）→ 审批（显式放行，全程无卡）→ 真实执行（tool_result 回填
//	handler 输出）→ journal v2 工具行 → run 收尾后 closeAll 被调用。
//
// 任何一环断裂都有独立断言可见：桥不在注册表 → tool_result 变「未知工具」；
// 审批没放行 → ai.approval.request 出现；journal 回调没接 → tool 行缺失；
// closeAll 没被 defer → closeCount 恒 0。
func TestRunAgentCoreMCPEndToEnd(t *testing.T) {
	session := startTestMCPSession(t, "echo", func(args map[string]interface{}) (string, bool) {
		q, _ := args["q"].(string)
		return "echo:" + q, false
	})

	setTestAgentHome(t) // journal 根与 MCP 采集都从 HOME 解析，一并隔离
	writeTestHomeFile(t, os.Getenv("HOME"), ".claude.json",
		`{"mcpServers":{"srv":{"command":"injected-for-test"}}}`)
	up, srv := newAgentCoreUpstream(t,
		agentCoreToolTurn("toolu_e2e", "mcp__srv__echo", `{"q":"hi"}`),
		agentCoreTextTurn("好", "的"),
	)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")

	m := newAgentAIManager()
	// dial 注入：忽略 raw、直接给内存 session；closeFn 计数供收尾断言。
	var closeCount int32
	m.coreMCPDialForTest = func(context.Context, agentCoreMCPRawServer) (*mcp.ClientSession, func(), error) {
		return session, func() { atomic.AddInt32(&closeCount, 1) }, nil
	}
	// 空 service + 设备策略显式放行该 mcp 工具：全程不得出现审批卡。
	svc := &AgentService{}
	m.service = svc
	project := t.TempDir()
	setAgentCoreMCPTestPolicy(t, svc, project, ApprovalPolicy{
		Scheme:          "custom",
		DefaultDecision: decisionRequireApproval,
		Rules: []approvalRule{{
			ID:       "mcp-e2e-allow",
			Match:    approvalRuleMatch{Tool: []string{"mcp__srv__echo"}},
			Decision: decisionAutoApprove,
		}},
	})

	const sessionID = "conv-agentcore-mcp-e2e"
	registerTestAISession(t, m, &agentAISession{
		id:      sessionID,
		runSeq:  1,
		history: []agentAIMessage{{Role: "user", Content: "调一下 echo"}},
	})

	log := &agentCoreEventLog{}
	run := agentAIRun{
		sessionID:   sessionID,
		runSeq:      1,
		messageID:   "msg-mcp-e2e",
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
	select {
	case <-finished:
	case <-time.After(30 * time.Second):
		t.Fatalf("runAgentCore 没有收敛（事件 %v）", log.types())
	}

	// 1) 终止事件契约：恰 1 ai.done、0 ai.error、0 审批卡（显式放行短路）。
	if got := log.count(models.AgentEventAIDone); got != 1 {
		t.Fatalf("ai.done 数量 = %d, want 1（事件 %v）", got, log.types())
	}
	if got := log.count(models.AgentEventAIError); got != 0 {
		t.Fatalf("ai.error 数量 = %d, want 0（%v）", got, log.filter(models.AgentEventAIError))
	}
	if got := log.count(models.AgentEventAIApprovalRequest); got != 0 {
		t.Fatalf("ai.approval.request 数量 = %d, want 0（显式放行不得问手机，事件 %v）", got, log.types())
	}

	// 2) 真实执行穿透审批+桥+内核：第二轮上游请求带 tool_result=handler 输出。
	reqs := up.requests()
	if len(reqs) != 2 {
		t.Fatalf("上游请求次数 = %d, want 2（工具回合后应带 tool_result 继续，事件 %v）", len(reqs), log.types())
	}
	second := reqs[1]
	if len(second.Messages) < 3 {
		t.Fatalf("第二轮 messages 只有 %d 条, want ≥3（user/assistant(tool_use)/user(tool_result)）", len(second.Messages))
	}
	var toolContent string
	var toolIsErr bool
	foundResult := false
	last := second.Messages[len(second.Messages)-1]
	blocks, _ := last.Content.([]interface{})
	for _, raw := range blocks {
		block, _ := raw.(map[string]interface{})
		if block == nil {
			continue
		}
		if bt, _ := block["type"].(string); bt == agentcore.BlockTypeToolResult {
			toolContent, _ = block["content"].(string)
			toolIsErr, _ = block["is_error"].(bool)
			foundResult = true
			break
		}
	}
	if !foundResult {
		t.Fatalf("第二轮最后一条消息没有 tool_result: %+v", last)
	}
	if toolIsErr {
		t.Fatalf("tool_result is_error=true, want 真实执行（%q）", toolContent)
	}
	if !strings.Contains(toolContent, "echo:hi") {
		t.Fatalf("tool_result = %q, want 含 echo:hi（审批+桥+内核全链穿透）", toolContent)
	}

	// 3) journal v2 五类行齐且顺序固定：session / user / turn(1) / tool /
	// turn(2) / assistant / end。turn 是逐回合 transcript 行（每回合必落），
	// 工具回合夹在 turn(1) 与 turn(2) 之间。
	lines := readJournalLines(t, agentCoreJournalDir(), sessionID)
	if len(lines) != 7 {
		t.Fatalf("journal 行数 = %d, want 7: %v", len(lines), lines)
	}
	if lines[0]["kind"] != "session" {
		t.Errorf("头行 = %v", lines[0])
	}
	if lines[1]["role"] != "user" || lines[1]["text"] != "调一下 echo" {
		t.Errorf("user 行 = %v", lines[1])
	}
	if lines[2]["kind"] != "turn" || lines[2]["stop_reason"] != "tool_use" {
		t.Errorf("turn(1) 行 = %v, want kind=turn stop_reason=tool_use", lines[2])
	}
	if lines[3]["kind"] != "tool" || lines[3]["tool"] != "mcp__srv__echo" ||
		lines[3]["summary"] != `q="hi"` || lines[3]["is_error"] != false {
		t.Errorf("tool 行 = %v, want kind=tool tool=mcp__srv__echo summary=q=\"hi\" is_error=false", lines[3])
	}
	if lines[4]["kind"] != "turn" || lines[4]["stop_reason"] != "end_turn" ||
		lines[4]["text"] != "好的" {
		t.Errorf("turn(2) 行 = %v, want kind=turn stop_reason=end_turn text=好的", lines[4])
	}
	if lines[5]["role"] != "assistant" || lines[5]["text"] != "好的" {
		t.Errorf("assistant 行 = %v", lines[5])
	}
	if lines[6]["kind"] != "end" || lines[6]["status"] != "success" {
		t.Errorf("end 行 = %v, want status=success", lines[6])
	}

	// 4) run 收尾后 closeAll 被调用恰好一次（defer 走 detached goroutine，
	// closeFn 可能在 waitFinished 之后才执行——轮询等，不许竞断）。
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(&closeCount) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&closeCount); got != 1 {
		t.Fatalf("closeAll 调用次数 = %d, want 1（MCP 会话必须被关停）", got)
	}
}

// agentCoreToolDefNames 把请求工具表压成名字列表（失败信息可读用）。
func agentCoreToolDefNames(defs []agentcore.ToolDef) []string {
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Name)
	}
	return names
}

// lastRequestToolResult 取第 idx 条上游请求最后一条消息里的 tool_result
// （内容 + is_error）。找不到即 fatal——脚本以工具回合开头时它必须存在。
func lastRequestToolResult(t *testing.T, reqs []agentcore.MessagesRequest, idx int) (string, bool) {
	t.Helper()
	last := reqs[idx].Messages[len(reqs[idx].Messages)-1]
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
	t.Fatalf("第 %d 条请求最后一条消息没有 tool_result: %+v", idx, last)
	return "", false
}

// ---------------------------------------------------------------------------
// 端到端：runAgentCore 的子 agent 注入（spec 2026-10-27-agentcore-subagents）
// ---------------------------------------------------------------------------

// TestRunAgentCoreSubagentEndToEnd 锁死子 agent 注入在真 run 里的完整链路：
//
//	采集（home 的 .claude/agents/reviewer.md）→ 包桥注册（主 run 第 1 回合
//	工具表含 reviewer）→ 主 run tool_use 调 reviewer → 桥递归子 run（复用
//	主 run 的 client/围栏/审批）→ 子 run 按 frontmatter 白名单只见 Read →
//	子 run 文本结果穿透回主 run 第 2 回合的 tool_result → journal 记 Task 行
//	→ run 收敛。
//
// 红阶段锚点：未接线时主 run 的 tool_use 得「未知工具: reviewer」，上游停在
// 2 条请求、tool_result 无「评审完成」——下方断言即红。
func TestRunAgentCoreSubagentEndToEnd(t *testing.T) {
	// allow_all（DefaultDecision=auto_approve、零规则）：reviewer 非 mcp__
	// 前缀不走默认门，全程短路放行、零审批卡。
	h := startAgentCoreApprovalHarness(t, builtinAllowAllPolicy(), []string{
		agentCoreToolTurn("toolu_sub1", "reviewer", `{"task":"评审 main.go"}`),
		agentCoreTextTurn("评审完成：无问题", ""),
		agentCoreTextTurn("好", "的"),
	},
		harnessSession("conv-agentcore-subagent-e2e", "msg-subagent-e2e"),
		harnessFirstUser("评审一下 main.go"),
		harnessSubagentDefs(map[string]string{
			"reviewer.md": "---\nname: reviewer\ndescription: 代码评审\ntools: Read\n---\n你是评审员。",
		}),
	)
	h.waitFinished(t)
	log := h.log

	// 1) 终止事件契约：恰 1 ai.done、0 ai.error、0 审批卡（reviewer 非 mcp__）。
	if got := log.count(models.AgentEventAIDone); got != 1 {
		t.Fatalf("ai.done 数量 = %d, want 1（事件 %v）", got, log.types())
	}
	if got := log.count(models.AgentEventAIError); got != 0 {
		t.Fatalf("ai.error 数量 = %d, want 0（%v）", got, log.filter(models.AgentEventAIError))
	}
	if got := log.count(models.AgentEventAIApprovalRequest); got != 0 {
		t.Fatalf("ai.approval.request 数量 = %d, want 0（事件 %v）", got, log.types())
	}

	// 2) 请求链形状：主 run 第 1 回合 → 子 run（第 2 条）→ 主 run 第 2 回合
	// （第 3 条，带 Task 的 tool_result）。未接线时「未知工具: reviewer」直接
	// 回填、run 停在 2 条——本断言与下方 tool_result 内容断言共同构成红锚。
	reqs := h.up.requests()
	if len(reqs) != 3 {
		t.Fatalf("上游请求次数 = %d, want 3（主1+子1+主2，事件 %v）", len(reqs), log.types())
	}
	hasReviewer := false
	for _, def := range reqs[0].Tools {
		if def.Name == "reviewer" {
			hasReviewer = true
		}
	}
	if !hasReviewer {
		t.Fatalf("主 run 第 1 回合工具表缺 reviewer: %v", agentCoreToolDefNames(reqs[0].Tools))
	}

	// 子 run 请求（第 2 条）的白名单：只见 Read 不见 Write，也不得再见
	// reviewer（depth=1：子 registry 不含子 agent 桥）。
	subHasRead, subHasWrite, subHasReviewer := false, false, false
	for _, def := range reqs[1].Tools {
		switch def.Name {
		case "Read":
			subHasRead = true
		case "Write":
			subHasWrite = true
		case "reviewer":
			subHasReviewer = true
		}
	}
	if !subHasRead || subHasWrite {
		t.Fatalf("子 run 工具表白名单失效: read=%v write=%v tools=%v",
			subHasRead, subHasWrite, agentCoreToolDefNames(reqs[1].Tools))
	}
	if subHasReviewer {
		t.Fatalf("子 run 工具表不得含子 agent 桥（depth=1）: %v", agentCoreToolDefNames(reqs[1].Tools))
	}

	// 3) 子 run 真实执行穿透递归：主 run 第 2 回合（第 3 条请求）的
	// tool_result 是子 run 的收尾文本，非「未知工具」、非错误。
	content, isErr := lastRequestToolResult(t, reqs, 2)
	if isErr {
		t.Fatalf("tool_result is_error=true, want 真实执行（%q）", content)
	}
	if !strings.Contains(content, "评审完成：无问题") {
		t.Fatalf("tool_result = %q, want 含「评审完成：无问题」（子 run 结果穿透递归回主 run）", content)
	}

	// 4) journal：恰好一行 Task 行（kind=tool tool=reviewer，非错误，summary
	// 形态与 MCP e2e 的 q="hi" 口径看齐）。
	lines := readJournalLines(t, agentCoreJournalDir(), h.sessionID)
	taskRows := 0
	for _, row := range lines {
		if row["kind"] == "tool" && row["tool"] == "reviewer" {
			taskRows++
			if row["summary"] != `task="评审 main.go"` {
				t.Errorf("Task 行 summary = %v, want task=\"评审 main.go\"", row["summary"])
			}
			if row["is_error"] != false {
				t.Errorf("Task 行 is_error = %v, want false: %v", row["is_error"], row)
			}
		}
	}
	if taskRows != 1 {
		t.Fatalf("journal Task 行（tool=reviewer）数量 = %d, want 1: %v", taskRows, lines)
	}
}

// TestRunAgentCoreSubagentApprovalPropagation 护栏（写完即绿）：子 run 内的
// mcp__ 调用必须过主 run 同一道审批门（默认门：无显式规则命中的 mcp__*
// 强制手机审批）。Task 4 默认门已合入 master，接线让子 Config 复用主 run 的
// Approve 闭包后本测试即绿——它防的是将来有人给子 Config 换独立 Approve
// 闭包（或置 nil）绕过审批门。
//
// 脚本四回合：主 run 调 reviewer → 子 run 调 mcp__srv__echo（触发审批卡，
// 测试侧拒绝）→ 子 run 收尾 → 主 run 收尾。拒绝后子 run 与主 run 都必须
// 继续存活（恰 1 ai.done、0 ai.error）。
func TestRunAgentCoreSubagentApprovalPropagation(t *testing.T) {
	session := startTestMCPSession(t, "echo", func(args map[string]interface{}) (string, bool) {
		q, _ := args["q"].(string)
		return "echo:" + q, false
	})
	// 零命中规则 + DefaultDecision=auto_approve（镜像 NoRuleMatch 用例）：
	// reviewer（非 mcp__）短路放行；子 run 的 mcp__srv__echo 无显式规则命中
	// → 默认门强制手机审批。
	h := startAgentCoreApprovalHarness(t, ApprovalPolicy{
		Scheme:          "custom",
		DefaultDecision: decisionAutoApprove,
		Rules: []approvalRule{{
			ID:       "unrelated-tool",
			Match:    approvalRuleMatch{Tool: []string{"Other"}},
			Decision: decisionAutoApprove,
		}},
	}, []string{
		agentCoreToolTurn("toolu_sub_ap", "reviewer", `{"task":"用 echo 查一下"}`),
		agentCoreToolTurn("toolu_sub_mcp", "mcp__srv__echo", `{"q":"hi"}`),
		agentCoreTextTurn("子收尾", ""),
		agentCoreTextTurn("好", "的"),
	},
		harnessSession("conv-agentcore-subagent-approval", "msg-subagent-approval"),
		harnessFirstUser("评审并查询"),
		harnessSubagentDefs(map[string]string{
			"reviewer.md": "---\nname: reviewer\ndescription: 代码评审\ntools: Read\n---\n你是评审员。",
		}),
		harnessMCP("srv", session),
	)
	// 等子 run 的 mcp__ 审批卡并拒绝（复用 harness 的交互路径）。
	h.decline(t)
	h.waitFinished(t)
	log := h.log

	// 审批事件：pending + resolved/decline 两条，工具名原样上卡。
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
	if tool, _ := requests[0]["tool_name"].(string); tool != "mcp__srv__echo" {
		t.Errorf("审批 tool_name = %q, want mcp__srv__echo", tool)
	}

	// 拒绝后流程继续：恰 1 ai.done、0 ai.error。
	if got := log.count(models.AgentEventAIDone); got != 1 {
		t.Fatalf("ai.done 数量 = %d, want 1（事件 %v）", got, log.types())
	}
	if got := log.count(models.AgentEventAIError); got != 0 {
		t.Fatalf("ai.error 数量 = %d, want 0（%v）", got, log.filter(models.AgentEventAIError))
	}

	// 穿透验证：子 run 带着拒绝回填继续（第 3 条请求的 tool_result 含
	// 「用户拒绝」），收尾文本穿透回主 run 第 4 条请求的 Task tool_result。
	reqs := h.up.requests()
	if len(reqs) != 4 {
		t.Fatalf("上游请求次数 = %d, want 4（主1+子2+主2，事件 %v）", len(reqs), log.types())
	}
	if content, isErr := lastRequestToolResult(t, reqs, 2); !isErr || !strings.Contains(content, "用户拒绝") {
		t.Fatalf("子 run tool_result = (%q, is_error=%v), want 含「用户拒绝」的错误回填", content, isErr)
	}
	if content, isErr := lastRequestToolResult(t, reqs, 3); isErr || !strings.Contains(content, "子收尾") {
		t.Fatalf("主 run Task tool_result = (%q, is_error=%v), want 含「子收尾」（拒绝后子 run 照常完成）", content, isErr)
	}
}

// TestRunAgentCoreSubagentReadOnlyNotInjected 护栏（接线后写完即绿）：readOnly
// run 不注入子 agent 桥（mirror MCP 的只读决策——同一判据 run.readOnly）。
// 防将来回归（如注入点挪到只读判定之前/之外）。
func TestRunAgentCoreSubagentReadOnlyNotInjected(t *testing.T) {
	// 与兄弟测试对齐：同样注入空 service + 显式策略（而非靠纯文本脚本绕开
	// 审批面），readOnly 剥离子 agent 不依赖「恰好没调工具」的偶然性。
	h := startAgentCoreApprovalHarness(t, builtinAllowAllPolicy(), []string{
		agentCoreTextTurn("好的", ""),
	},
		harnessSession("conv-agentcore-subagent-readonly", "msg-subagent-readonly"),
		harnessFirstUser("只读问一句"),
		harnessSubagentDefs(map[string]string{
			"reviewer.md": "---\nname: reviewer\ndescription: 代码评审\ntools: Read\n---\n你是评审员。",
		}),
		harnessReadOnly(),
	)
	h.waitFinished(t)
	log := h.log

	// run 照常收敛：只读剥离不是失败。
	if got := log.count(models.AgentEventAIDone); got != 1 {
		t.Fatalf("ai.done = %d, want 1（事件 %v）", got, log.types())
	}
	if got := log.count(models.AgentEventAIError); got != 0 {
		t.Fatalf("ai.error = %d, want 0（%v）", got, log.filter(models.AgentEventAIError))
	}
	// 行为级锁死：上游工具表零 reviewer 桥（没注册就没 advertised）。
	reqs := h.up.requests()
	if len(reqs) != 1 {
		t.Fatalf("上游请求次数 = %d, want 1", len(reqs))
	}
	for _, def := range reqs[0].Tools {
		if def.Name == "reviewer" {
			t.Fatalf("readOnly run 的上游工具表含子 agent 桥: %s", def.Name)
		}
	}
	// journal 无 Task 行：没注入就没调用轨迹。
	for _, row := range readJournalLines(t, agentCoreJournalDir(), h.sessionID) {
		if row["kind"] == "tool" {
			t.Fatalf("readOnly run 的 journal 不该有 tool 行: %v", row)
		}
	}
}

// TestRunAgentCoreReadOnlySkipsMCPInjection 锁死「只读 run 不注入 MCP」：
// home 里真实存在 server 条目 + dial 注入计数，readOnly run 全程 0 次 dial、
// 上游工具表零 mcp__ 条目，run 本身照常收敛。对齐 claude 只读 fork 的
// --strict-mcp-config {} 先例（withAgentReadOnlyPolicy）；执行态 goal run
// （goalIdentity 非 nil、readOnly=false）不受影响——非 readOnly 的注入路径
// 由 TestRunAgentCoreMCPEndToEnd 覆盖。
func TestRunAgentCoreReadOnlySkipsMCPInjection(t *testing.T) {
	session := startTestMCPSession(t, "echo", func(map[string]interface{}) (string, bool) {
		return "echo:hi", false
	})
	setTestAgentHome(t)
	writeTestHomeFile(t, os.Getenv("HOME"), ".claude.json",
		`{"mcpServers":{"srv":{"command":"injected-for-test"}}}`)
	up, srv := newAgentCoreUpstream(t, agentCoreTextTurn("好的", ""))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")

	m := newAgentAIManager()
	var dialCount int32
	m.coreMCPDialForTest = func(context.Context, agentCoreMCPRawServer) (*mcp.ClientSession, func(), error) {
		atomic.AddInt32(&dialCount, 1)
		return session, func() {}, nil
	}

	const sessionID = "conv-agentcore-mcp-readonly"
	registerTestAISession(t, m, &agentAISession{
		id:      sessionID,
		runSeq:  1,
		history: []agentAIMessage{{Role: "user", Content: "只读问一句"}},
	})
	log := &agentCoreEventLog{}
	run := agentAIRun{
		sessionID:   sessionID,
		runSeq:      1,
		messageID:   "msg-mcp-readonly",
		provider:    "agentcore",
		model:       "glm-5.2",
		projectPath: t.TempDir(),
		activity:    newAgentAIActivity(),
		readOnly:    true,
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		m.runAgentCore(context.Background(), run, log.write)
	}()
	select {
	case <-finished:
	case <-time.After(30 * time.Second):
		t.Fatalf("runAgentCore 没有收敛（事件 %v）", log.types())
	}

	// run 照常收敛：只读剥离 MCP 不是失败。
	if got := log.count(models.AgentEventAIDone); got != 1 {
		t.Fatalf("ai.done = %d, want 1（事件 %v）", got, log.types())
	}
	if got := log.count(models.AgentEventAIError); got != 0 {
		t.Fatalf("ai.error = %d, want 0（%v）", got, log.filter(models.AgentEventAIError))
	}
	// 行为级锁死：home 里有 server 条目也一次都不 dial（只读不建连）。
	if got := atomic.LoadInt32(&dialCount); got != 0 {
		t.Fatalf("readOnly run 的 MCP dial 次数 = %d, want 0", got)
	}
	// 上游工具表零 mcp__ 条目：没注册就没 advertised。
	reqs := up.requests()
	if len(reqs) != 1 {
		t.Fatalf("上游请求次数 = %d, want 1", len(reqs))
	}
	for _, def := range reqs[0].Tools {
		if strings.HasPrefix(def.Name, "mcp__") {
			t.Fatalf("readOnly run 的上游工具表含 MCP 工具: %s", def.Name)
		}
	}
}
