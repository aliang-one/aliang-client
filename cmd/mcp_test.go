package cmd

// aliang mcp stdio 面单测：JSON-RPC 分发、tools/list 形状、tools/call 到本地
// 56433 路由的调用形状与错误语义、协议版本协商、通知不应答。

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type mcpTestRecorder struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	methods []string
	paths   []string
	bodies  []string
}

func newMCPTestServer(t *testing.T, handler http.HandlerFunc) *mcpTestRecorder {
	t.Helper()
	rec := &mcpTestRecorder{t: t}
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.methods = append(rec.methods, r.Method)
		rec.paths = append(rec.paths, r.URL.Path)
		rec.bodies = append(rec.bodies, string(raw))
		rec.mu.Unlock()
		handler(w, r)
	})
	rec.srv = httptest.NewServer(wrapped)
	t.Cleanup(rec.srv.Close)
	return rec
}

func (rec *mcpTestRecorder) last() (method, path, body string) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.paths) == 0 {
		rec.t.Fatal("no request reached the mock local agent")
	}
	return rec.methods[len(rec.methods)-1], rec.paths[len(rec.paths)-1], rec.bodies[len(rec.bodies)-1]
}

// feed 把一行 JSON-RPC 喂给服务端并返回应答行（要求必须有一条应答）。
func feed(t *testing.T, srv *peerMCPServer, line string) map[string]interface{} {
	t.Helper()
	var out bytes.Buffer
	srv.out = &out
	srv.handleLine([]byte(line))
	if out.Len() == 0 {
		t.Fatalf("no response for %s", line)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, out.String())
	}
	return resp
}

func newMCPServerForTest(t *testing.T, rec *mcpTestRecorder) *peerMCPServer {
	t.Helper()
	return &peerMCPServer{baseURL: rec.srv.URL, client: &http.Client{}}
}

// pinAIMCPGateConfig 把门控读配置的路径钉到临时文件，返回写入函数。
//
// 必须钉住的理由：agentMCPGateConfigPath 默认指向开发机真实的
// ~/.aliang/config.json，不 pin 的话每条用例的 gate 值取决于跑测试的人有没有
// 手改过配置——本机绿、CI 红，且 gate off 用例根本没有办法造夹具。
// t.Cleanup 无条件还原：case 失败/fatal 时也要还回去，否则同包后续用例会
// 沿着坏路径跑（同进程内是全局变量）。
func pinAIMCPGateConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if contents != "" {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("write gate fixture: %v", err)
		}
	}
	orig := agentMCPGateConfigPath
	agentMCPGateConfigPath = func() string { return path }
	t.Cleanup(func() { agentMCPGateConfigPath = orig })
	return path
}

// toolNamesFromList 从 tools/list 应答里取出名字集合。
func toolNamesFromList(t *testing.T, resp map[string]interface{}) map[string]map[string]interface{} {
	t.Helper()
	if resp["error"] != nil {
		t.Fatalf("tools/list errored: %v", resp["error"])
	}
	result, _ := resp["result"].(map[string]interface{})
	tools, _ := result["tools"].([]interface{})
	out := make(map[string]map[string]interface{}, len(tools))
	for _, item := range tools {
		m, _ := item.(map[string]interface{})
		name, _ := m["name"].(string)
		out[name] = m
	}
	return out
}

// toolCallText 解出 tools/call 应答的文本块与 isError。
func toolCallText(t *testing.T, resp map[string]interface{}) (string, bool) {
	t.Helper()
	if resp["error"] != nil {
		t.Fatalf("tools/call errored: %v", resp["error"])
	}
	result, _ := resp["result"].(map[string]interface{})
	content, _ := result["content"].([]interface{})
	if len(content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(content))
	}
	block, _ := content[0].(map[string]interface{})
	text, _ := block["text"].(string)
	isErr, _ := result["isError"].(bool)
	return text, isErr
}

func TestPeerMCPToolsList_HasFivePeerToolsAndThreeAIMCPTools(t *testing.T) {
	// 钉夹具：本用例断言的是「peer 五工具」的份额，而 ai_run_* 的在场与否
	// 取决于 ai_mcp 门控——不钉的话开发机上若有人关了 ai_mcp，这条会莫名
	// 从 8 变 5 而看不出是门控还是工具表回归。
	pinAIMCPGateConfig(t, "") // 文件不存在 = 默认开
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("tools/list must not touch the local agent")
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if resp["error"] != nil {
		t.Fatalf("tools/list errored: %v", resp["error"])
	}
	result, _ := resp["result"].(map[string]interface{})
	tools, _ := result["tools"].([]interface{})
	// 5 peer + 3 ai_run_*（门控默认开）。
	if len(tools) != 8 {
		t.Fatalf("tools = %d, want 8", len(tools))
	}
	want := map[string]bool{
		"peer_exec": false, "peer_fetch_file": false, "device_list": false, "device_info": false, "device_note": false,
		"ai_run_start": false, "ai_run_status": false, "ai_run_approve": false,
	}
	for _, item := range tools {
		m, _ := item.(map[string]interface{})
		name, _ := m["name"].(string)
		if _, ok := want[name]; !ok {
			t.Fatalf("unexpected tool %q", name)
		}
		want[name] = true
		if m["inputSchema"] == nil || m["description"] == "" {
			t.Fatalf("tool %q missing schema/description", name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("missing tool %q", name)
		}
	}
}

func TestPeerMCPCallExec_PostsToLocalRoute(t *testing.T) {
	pinAIMCPGateConfig(t, "")
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"result":"exit_code=0 | pong"}}`))
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"peer_exec","arguments":{"device_id":"device-c","command":"echo pong","timeout_ms":5000}}}`)
	if resp["error"] != nil {
		t.Fatalf("tools/call errored: %v", resp["error"])
	}
	result, _ := resp["result"].(map[string]interface{})
	if result["isError"] == true {
		t.Fatalf("unexpected isError: %v", result)
	}
	content, _ := result["content"].([]interface{})
	if len(content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(content))
	}
	block, _ := content[0].(map[string]interface{})
	if !strings.Contains(block["text"].(string), "exit_code=0 | pong") {
		t.Fatalf("text = %v", block["text"])
	}

	method, path, body := rec.last()
	if method != http.MethodPost || path != "/api/agent/peer/exec" {
		t.Fatalf("call = %s %s", method, path)
	}
	if !strings.Contains(body, `"device_id":"device-c"`) || !strings.Contains(body, `"timeout_ms":5000`) {
		t.Fatalf("body = %s", body)
	}
}

func TestPeerMCPDeviceList_GET_NoBody(t *testing.T) {
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"result":"1 device(s) in this account:"}}`))
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"device_list","arguments":{}}}`)
	result, _ := resp["result"].(map[string]interface{})
	if result["isError"] == true {
		t.Fatalf("device_list errored: %v", result)
	}
	method, path, body := rec.last()
	if method != http.MethodGet || path != "/api/agent/peer/devices" {
		t.Fatalf("call = %s %s", method, path)
	}
	if body != "" {
		t.Fatalf("GET must carry no body, got %s", body)
	}
}

func TestPeerMCPDeviceInfo_QueryParam(t *testing.T) {
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("device_id") != "device-x" {
			t.Errorf("device_id query = %q", r.URL.Query().Get("device_id"))
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"result":"device device-x"}}`))
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"device_info","arguments":{"device_id":"device-x"}}}`)
	result, _ := resp["result"].(map[string]interface{})
	if result["isError"] == true {
		t.Fatalf("device_info errored: %v", result)
	}
	if method, path, _ := rec.last(); method != http.MethodGet || path != "/api/agent/peer/device-info" {
		t.Fatalf("call = %s %s", method, path)
	}
}

func TestPeerMCPCall_LocalAgentRefusal_IsToolError(t *testing.T) {
	// 云端拒绝经本地面回 502+Msg（服务端已脱敏）→ MCP isError 文本。
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"code":502,"msg":"peer api status 409: not_allowed","data":null}`))
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"peer_exec","arguments":{"device_id":"device-c","command":"false"}}}`)
	result, _ := resp["result"].(map[string]interface{})
	if result["isError"] != true {
		t.Fatalf("refusal must be isError: %v", result)
	}
	content, _ := result["content"].([]interface{})
	block, _ := content[0].(map[string]interface{})
	if !strings.Contains(block["text"].(string), "not_allowed") {
		t.Fatalf("text = %v", block["text"])
	}
}

func TestPeerMCPUnknownTool_IsErrorWithoutHTTP(t *testing.T) {
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unknown tool must not reach the local agent")
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"evil_tool","arguments":{}}}`)
	result, _ := resp["result"].(map[string]interface{})
	if result["isError"] != true {
		t.Fatalf("unknown tool must be isError: %v", result)
	}
}

func TestPeerMCPInitialize_EchoesProtocolVersion(t *testing.T) {
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("initialize must not touch the local agent")
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":7,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`)
	result, _ := resp["result"].(map[string]interface{})
	if result["protocolVersion"] != "2024-11-05" {
		t.Fatalf("protocolVersion = %v", result["protocolVersion"])
	}
	info, _ := result["serverInfo"].(map[string]interface{})
	if info["name"] != "aliang-peer" {
		t.Fatalf("serverInfo = %v", info)
	}
}

func TestPeerMCPNotification_NoReply(t *testing.T) {
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("must not dial") })
	srv := newMCPServerForTest(t, rec)
	var out bytes.Buffer
	srv.out = &out
	srv.handleLine([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if out.Len() != 0 {
		t.Fatalf("notification must not be answered: %s", out.String())
	}
}

func TestPeerMCPUnknownMethod_RPCError(t *testing.T) {
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("must not dial") })
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":8,"method":"resources/list"}`)
	errObj, _ := resp["error"].(map[string]interface{})
	if errObj == nil || errObj["code"].(float64) != -32601 {
		t.Fatalf("error = %v", resp["error"])
	}
}

func TestPeerMCPBaseURL_EnvOverrides(t *testing.T) {
	t.Setenv("ALIANG_USER_AGENT_ADDR", "")
	if got := peerMCPBaseURL(); got != "http://127.0.0.1:56433" {
		t.Fatalf("default = %s", got)
	}
	t.Setenv("ALIANG_USER_AGENT_ADDR", "127.0.0.1:9999")
	if got := peerMCPBaseURL(); got != "http://127.0.0.1:9999" {
		t.Fatalf("host:port = %s", got)
	}
	t.Setenv("ALIANG_USER_AGENT_ADDR", "http://127.0.0.1:7777/")
	if got := peerMCPBaseURL(); got != "http://127.0.0.1:7777" {
		t.Fatalf("url = %s", got)
	}
}

func TestPeerMCPCall_LargeResultWithin4MiBCap(t *testing.T) {
	// 合法满输出(stdout/stderr 各 256KiB 裸字节)经 JSON 转义可膨胀到 >1MiB;
	// 响应读侧封顶必须与注册表面 MaxOutputBytes(4MiB)对齐,否则合法结果被
	// 截断成非法 JSON、整次调用误报 "not JSON" 报废。
	payload := strings.Repeat("a\"b\\c\n", 200000) // 1.4MiB 裸字节,转义后 ~1.8MiB
	raw, err := json.Marshal(map[string]interface{}{
		"code": 0, "msg": "success",
		"data": map[string]string{"result": payload},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(raw) <= 1<<20 {
		t.Fatalf("test payload too small: %d bytes, want > 1MiB", len(raw))
	}
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"peer_exec","arguments":{"device_id":"device-c","command":"big"}}}`)
	if resp["error"] != nil {
		t.Fatalf("tools/call errored: %v", resp["error"])
	}
	result, _ := resp["result"].(map[string]interface{})
	if result["isError"] == true {
		t.Fatalf("large legal result must not be isError: %v", result)
	}
	content, _ := result["content"].([]interface{})
	block, _ := content[0].(map[string]interface{})
	if !strings.Contains(block["text"].(string), `a"b\c`) {
		t.Fatalf("payload mangled: %.80v", block["text"])
	}
}

// D1(spec 2026-11-02):stdio 面工具名 ↔ bridge 名映射断言。后三个无
// peer_ 前缀是历史现状(claude mcp add 存量用户工具引用),勿改名。
//
// 第 6 组 ai_run_* 不在本映射里：它不走「stdio 工具 → agentcore LLM 工具桥」
// 那条路（bridge 是给 agentcore 内核注入工具用的），而是 stdio 面直打
// /api/agent/ai/mcp/* 端点。把两者混在一张表里，等于宣称 ai_run_* 也在内核
// 工具清单里——那是错的，会让后来人照着这张表去 services 侧找不存在的桥。
func TestPeerMCPToolNamesMapBridgeNames(t *testing.T) {
	// bridge 名(services/agent_ai_tool_ask_peer.go)
	bridgeNames := map[string]bool{
		"peer_exec": true, "peer_fetch_file": true, "peer_device_info": true,
		"peer_device_list": true, "peer_device_note": true,
	}
	// stdio 名 → bridge 名(历史映射)
	mapping := map[string]string{
		"peer_exec":       "peer_exec",
		"peer_fetch_file": "peer_fetch_file",
		"device_info":     "peer_device_info",
		"device_list":     "peer_device_list",
		"device_note":     "peer_device_note",
	}
	seen := map[string]bool{}
	for _, tool := range peerMCPTools {
		if isAIMCPToolName(tool.name) {
			continue
		}
		bridge, ok := mapping[tool.name]
		if !ok {
			t.Fatalf("unexpected stdio tool %q (mapping stale)", tool.name)
		}
		if !bridgeNames[bridge] {
			t.Fatalf("stdio tool %q maps to unknown bridge name %q", tool.name, bridge)
		}
		seen[tool.name] = true
	}
	if len(seen) != len(mapping) {
		t.Fatalf("stdio has %d tools, mapping has %d", len(seen), len(mapping))
	}
}

// ---------------------------------------------------------------------------
// agent-as-MCP 第 6 工具组（ai_run_*）+ customer.agentcore.ai_mcp 门控
//
// 本节所有用例都经 pinAIMCPGateConfig 钉住配置路径、经 newMCPTestServer 钉住
// baseURL。这两条不是仪式：前者防读开发机真配置导致随人红绿，后者防请求打到
// 127.0.0.1:56433 —— 那是用户本机活体 agent，一次误打真的会起 run 或掐断
// 用户正在跑的 AI 会话（2026-10-27 真出过事故）。
// ---------------------------------------------------------------------------

// gate 开（配置文件不存在 = 默认开）→ tools/list 三工具在，且 start 的描述
// 带 session 契约句（"tool face"）。契约句是工具面与外部 LLM 之间的边界声明，
// 删掉它模型就会去猜手机/桌面会话的 id。
func TestAIMCPToolsListIncludesThreeTools(t *testing.T) {
	pinAIMCPGateConfig(t, "")
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("tools/list must not touch the local agent")
	})
	srv := newMCPServerForTest(t, rec)

	byName := toolNamesFromList(t, feed(t, srv, `{"jsonrpc":"2.0","id":10,"method":"tools/list"}`))
	for _, name := range []string{"ai_run_start", "ai_run_status", "ai_run_approve"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("gate on 缺工具 %q（现有 %v）", name, byName)
		}
	}
	desc, _ := byName["ai_run_start"]["description"].(string)
	if !strings.Contains(desc, "tool face") {
		t.Fatalf("ai_run_start 描述缺 session 契约句: %v", desc)
	}
	// 逐出提示也必须在描述里：否则模型拿到 session not found 会拿旧 id 重试。
	if !strings.Contains(desc, "evicted") {
		t.Fatalf("ai_run_start 描述缺逐出提示: %v", desc)
	}
}

// gate off → tools/list 三工具缺席、peer 五工具仍在。后半句是同一条断言的
// 一半：证明门控只罩这一组，不会顺带把 device_list 等关掉（那会让外部 CLI
// 的跨设备能力凭空消失）。
func TestAIMCPToolsListGateOff(t *testing.T) {
	pinAIMCPGateConfig(t, `{"customer":{"agentcore":{"ai_mcp":false}}}`)
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("tools/list must not touch the local agent")
	})
	srv := newMCPServerForTest(t, rec)

	byName := toolNamesFromList(t, feed(t, srv, `{"jsonrpc":"2.0","id":11,"method":"tools/list"}`))
	for _, name := range []string{"ai_run_start", "ai_run_status", "ai_run_approve"} {
		if _, ok := byName[name]; ok {
			t.Fatalf("gate off 仍列出 %q", name)
		}
	}
	for _, name := range []string{"peer_exec", "peer_fetch_file", "device_list", "device_info", "device_note"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("gate off 误伤 peer 工具 %q", name)
		}
	}
}

// gate off → tools/call 也必须是 unknown tool。只过滤 list 不过滤 call 是经典
// 越门形态：模型记着上一轮工具名照样能打进来，甚至比没门控更糟（用户以为
// 已经关了）。
func TestAIMCPCallGateOffIsUnknownTool(t *testing.T) {
	pinAIMCPGateConfig(t, `{"customer":{"agentcore":{"ai_mcp":false}}}`)
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("gate off 的 tools/call 不得触达本地 agent")
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"ai_run_status","arguments":{"session_id":"s1"}}}`)
	text, isErr := toolCallText(t, resp)
	if !isErr {
		t.Fatalf("gate off 必须是 isError: %q", text)
	}
	if !strings.Contains(text, "unknown tool") {
		t.Fatalf("gate off 文案应与 peer_mcp 关闸同款（unknown tool），got %q", text)
	}
}

// start 的请求形状 + 返回文本原样透传。
//
// 关键断言是「返回文本 == data.result 里的那段 JSON 字符串」：端点把 payload
// 序列化成字符串塞进 result，工具面必须原样吐出；再 JSON 解包一次或再编码
// 一次，模型看到的都是被二次转义的 \{\"session_id\"... 文本。
func TestAIMCPCallStartPostsToEndpoint(t *testing.T) {
	pinAIMCPGateConfig(t, "")
	const resultJSON = `{"session_id":"s1","accepted":true}`
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 用 json.Marshal 造信封而不是拼字符串：result 字段要是 JSON 字符串，
		// 手拼最容易在转义层出错，测试夹具自己先出错就失去意义。
		raw, _ := json.Marshal(map[string]interface{}{
			"code": 0, "msg": "success",
			"data": map[string]interface{}{"result": resultJSON},
		})
		_, _ = w.Write(raw)
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":"ai_run_start","arguments":{"content":"fix the build","project_path":"/tmp/proj"}}}`)
	text, isErr := toolCallText(t, resp)
	if isErr {
		t.Fatalf("start errored: %q", text)
	}
	if text != resultJSON {
		t.Fatalf("工具返回文本必须原样等于 data.result，got %q want %q", text, resultJSON)
	}

	method, path, body := rec.last()
	if method != http.MethodPost || path != "/api/agent/ai/mcp/start" {
		t.Fatalf("call = %s %s", method, path)
	}
	var sent map[string]interface{}
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("body 不是 JSON: %v (%s)", err, body)
	}
	if sent["content"] != "fix the build" || sent["project_path"] != "/tmp/proj" {
		t.Fatalf("body 字段 = %v", sent)
	}
	// 可选键非空才带：空 provider/session_id 传下去会让端点把「新建」误判成续聊。
	if _, ok := sent["provider"]; ok {
		t.Fatalf("未指定 provider 时不应带该键: %v", sent)
	}
	if _, ok := sent["session_id"]; ok {
		t.Fatalf("未指定 session_id 时不应带该键: %v", sent)
	}
}

// approve 三字段逐个到位。端点按 body["approval_id"]/["decision"] 取值，键名
// 驼峰化或漏一个都是静默空参（服务端只会回 no_match，看不出是工具面的锅）。
func TestAIMCPCallApprovePostsAllThreeFields(t *testing.T) {
	pinAIMCPGateConfig(t, "")
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"result":"{\"applied\":true}"}}`))
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":14,"method":"tools/call","params":{"name":"ai_run_approve","arguments":{"session_id":"s1","approval_id":"ap-9","decision":"accept"}}}`)
	text, isErr := toolCallText(t, resp)
	if isErr {
		t.Fatalf("approve errored: %q", text)
	}
	if text != `{"applied":true}` {
		t.Fatalf("approve 返回文本 = %q", text)
	}

	method, path, body := rec.last()
	if method != http.MethodPost || path != "/api/agent/ai/mcp/approve" {
		t.Fatalf("call = %s %s", method, path)
	}
	var sent map[string]interface{}
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("body 不是 JSON: %v (%s)", err, body)
	}
	if sent["session_id"] != "s1" || sent["approval_id"] != "ap-9" || sent["decision"] != "accept" {
		t.Fatalf("body 三字段不齐: %v", sent)
	}
}

// 后端失败 → isError 文本透传，与既有 peer 工具的语义一致（服务端已脱敏的
// msg 是要给模型看的原因，不能被吞成通用错误）。
func TestAIMCPCallStatusBackendErrorIsToolError(t *testing.T) {
	pinAIMCPGateConfig(t, "")
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":500,"msg":"session s1 not found","data":null}`))
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":15,"method":"tools/call","params":{"name":"ai_run_status","arguments":{"session_id":"s1"}}}`)
	text, isErr := toolCallText(t, resp)
	if !isErr {
		t.Fatalf("500 必须映射成 isError: %q", text)
	}
	if !strings.Contains(text, "session s1 not found") {
		t.Fatalf("服务端 msg 应原样透传，got %q", text)
	}
}

// 门控三态 + fail-open 的直接单测。这里是最容易写反的一处：nil/false 分不清
// 会让「没配过」和「配了 false」等价——那就是升级后默认关、老用户功能静默消失。
func TestAIMCPGateDefaultsOnWhenConfigMissingOrUnparsable(t *testing.T) {
	cases := []struct {
		name string
		body string // "" = 文件不存在
		want bool
	}{
		{"文件不存在", "", true},
		{"字段缺失(nil)", `{"customer":{"agentcore":{}}}`, true},
		{"整棵 customer 缺失", `{}`, true},
		{"显式 true", `{"customer":{"agentcore":{"ai_mcp":true}}}`, true},
		{"显式 false", `{"customer":{"agentcore":{"ai_mcp":false}}}`, false},
		{"非法 JSON fail-open", `{"customer":{`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pinAIMCPGateConfig(t, tc.body)
			if got := agentMCPGateOn(); got != tc.want {
				t.Fatalf("agentMCPGateOn() = %v, want %v (fixture: %q)", got, tc.want, tc.body)
			}
		})
	}
}

// 路径 seam 自身也要能容错：拿不到用户目录时（$HOME 被清空的 daemon 环境）
// 返回空字符串，门控必须当「读不到 = 默认开」，而不是 panic 或空指针。
func TestAIMCPGateOnEmptyPathIsOn(t *testing.T) {
	orig := agentMCPGateConfigPath
	agentMCPGateConfigPath = func() string { return "" }
	t.Cleanup(func() { agentMCPGateConfigPath = orig })
	if !agentMCPGateOn() {
		t.Fatal("空路径应视为读不到配置 → 默认开")
	}
}
