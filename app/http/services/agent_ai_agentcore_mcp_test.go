package services

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/agentcore"
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

func TestAgentCoreMCPListToolsFollowsCursor(t *testing.T) {
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
