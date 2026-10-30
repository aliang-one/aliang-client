package services

// 设备互访阶段 1（spec §5.3/§7/§10）工具门面单测：POST 变体调用形状、四件
// 工具描述符与 handler 对 mock 服务端的调用形状、sanitizePeerError 剥
// token/signature、agentcore 桥与注册表共享 handler。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/common/cache"
)

// peerToolsTestServer 起一个记录请求的 mock 云服务端，返回 canned 响应。
type peerToolsTestServer struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	methods []string
	paths   []string
	auths   []string
	devIDs  []string
	bodies  []string
}

func newPeerToolsTestServer(t *testing.T, status int, response string) *peerToolsTestServer {
	t.Helper()
	ts := &peerToolsTestServer{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		ts.mu.Lock()
		ts.methods = append(ts.methods, r.Method)
		ts.paths = append(ts.paths, r.URL.Path)
		ts.auths = append(ts.auths, r.Header.Get("Authorization"))
		ts.devIDs = append(ts.devIDs, r.Header.Get("X-Aliang-Device-ID"))
		ts.bodies = append(ts.bodies, string(raw))
		ts.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	})
	ts.srv = httptest.NewServer(mux)
	t.Cleanup(ts.srv.Close)
	return ts
}

func (ts *peerToolsTestServer) last() (method, path, auth, devID, body string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if len(ts.methods) == 0 {
		ts.t.Fatal("no request reached the mock server")
	}
	return ts.methods[len(ts.methods)-1], ts.paths[len(ts.paths)-1],
		ts.auths[len(ts.auths)-1], ts.devIDs[len(ts.devIDs)-1], ts.bodies[len(ts.bodies)-1]
}

// peerToolsTestEnv 密闭 HOME/DATA 目录，并给工具层换上指向 mock 服务端的
// 桩服务与基址（t.Cleanup 还原）。
func peerToolsTestEnv(t *testing.T, ts *peerToolsTestServer) *AgentService {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	t.Cleanup(cache.ResetCacheDirForTest)

	// user-agent 运行时语义：effectiveUserAuthorizationLocked 读 forwarded
	// 凭据而非全局 auth 存储（单测不得碰真实登录态）。
	t.Setenv(AgentRuntimeEnv, "1")

	svc := &AgentService{client: &http.Client{Timeout: 5 * time.Second}}
	svc.mu.Lock()
	svc.state.Registered = true
	svc.state.DeviceID = "device-b"
	svc.forwardedUserAuthorization = "Bearer peer-test-token"
	svc.mu.Unlock()

	origProvider := agentPeerServiceProvider
	agentPeerServiceProvider = func() *AgentService { return svc }
	t.Cleanup(func() { agentPeerServiceProvider = origProvider })

	if ts != nil {
		origURL := agentPeerServerURL
		agentPeerServerURL = func() string { return ts.srv.URL }
		t.Cleanup(func() { agentPeerServerURL = origURL })
	}
	return svc
}

func TestDoAgentServerPOST_HeadersBodyAndStatus(t *testing.T) {
	ts := newPeerToolsTestServer(t, http.StatusOK, `{"ok":true}`)
	svc := peerToolsTestEnv(t, ts)

	raw, err := svc.doAgentServerPOST(context.Background(), ts.srv.URL+"/api/peer/exec",
		map[string]interface{}{"target_device_id": "device-b", "command": "ls"})
	if err != nil {
		t.Fatalf("post failed: %v", err)
	}
	if !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("body = %s", raw)
	}
	method, path, auth, devID, body := ts.last()
	if method != http.MethodPost || path != "/api/peer/exec" {
		t.Fatalf("request = %s %s", method, path)
	}
	if auth != "Bearer peer-test-token" {
		t.Fatalf("Authorization = %q", auth)
	}
	if devID != "device-b" {
		t.Fatalf("X-Aliang-Device-ID = %q", devID)
	}
	var sent map[string]interface{}
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("request body not json: %v", body)
	}
	if sent["target_device_id"] != "device-b" || sent["command"] != "ls" {
		t.Fatalf("request body shape = %v", sent)
	}
}

func TestDoAgentServerPOST_StatusErrorCarriesReason(t *testing.T) {
	ts := newPeerToolsTestServer(t, http.StatusConflict, `{"reason":"busy"}`)
	svc := peerToolsTestEnv(t, ts)

	_, err := svc.doAgentServerPOST(context.Background(), ts.srv.URL+"/api/peer/exec", nil)
	var statusErr agentPeerHTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("err = %v, want agentPeerHTTPStatusError", err)
	}
	if statusErr.status != http.StatusConflict {
		t.Fatalf("status = %d", statusErr.status)
	}
}

func TestDoAgentServerPOST_RequiresAuthAndRegistration(t *testing.T) {
	ts := newPeerToolsTestServer(t, http.StatusOK, `{}`)
	svc := peerToolsTestEnv(t, ts)

	svc.mu.Lock()
	svc.forwardedUserAuthorization = ""
	svc.mu.Unlock()
	if _, err := svc.doAgentServerPOST(context.Background(), ts.srv.URL+"/x", nil); err == nil ||
		!strings.Contains(err.Error(), "authorization") {
		t.Fatalf("empty auth must fail: %v", err)
	}

	svc.mu.Lock()
	svc.forwardedUserAuthorization = "Bearer x"
	svc.state.Registered = false
	svc.mu.Unlock()
	if _, err := svc.doAgentServerPOST(context.Background(), ts.srv.URL+"/x", nil); err == nil ||
		!strings.Contains(err.Error(), "registered") {
		t.Fatalf("unregistered must fail: %v", err)
	}
}

func TestDoAgentServerPOST_CtxDeadlineHonored(t *testing.T) {
	svc := peerToolsTestEnv(t, nil)
	// 指向一个不存在的端口：连接错误由 ctx 截断返回。
	orig := agentPeerServerURL
	agentPeerServerURL = func() string { return "http://127.0.0.1:1" }
	t.Cleanup(func() { agentPeerServerURL = orig })

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := svc.doAgentServerPOST(ctx, "http://127.0.0.1:1/api/peer/exec", nil)
	if err == nil {
		t.Fatal("expected error for unreachable endpoint")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("ctx deadline not honored: %v", elapsed)
	}
}

func TestAgentPeerExecHandler_CallShapeAndResultText(t *testing.T) {
	ts := newPeerToolsTestServer(t, http.StatusOK, `{"exit_code":0,"stdout":"peer-hello\n","stderr":"","duration_ms":12}`)
	peerToolsTestEnv(t, ts)

	res := agentPeerExecPayload(map[string]interface{}{
		"type":       "peer.exec",
		"request_id": "req-1",
		"device_id":  "device-b",
		"command":    "echo peer-hello",
		"timeout_ms": float64(5000),
		"workdir":    "/tmp",
	})
	if res["type"] != "peer.exec.result" || res["request_id"] != "req-1" {
		t.Fatalf("result shape = %v", res)
	}
	text, _ := res["result"].(string)
	for _, want := range []string{"exit_code=0", "peer-hello", "duration_ms=12", "device device-b"} {
		if !strings.Contains(text, want) {
			t.Fatalf("result text %q missing %q", text, want)
		}
	}
	_, path, _, _, body := ts.last()
	if path != "/api/peer/exec" {
		t.Fatalf("path = %s", path)
	}
	var sent map[string]interface{}
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("body = %s", body)
	}
	if sent["target_device_id"] != "device-b" || sent["command"] != "echo peer-hello" ||
		sent["timeout_ms"] != float64(5000) || sent["workdir"] != "/tmp" {
		t.Fatalf("peer/exec body shape = %v", sent)
	}
}

func TestAgentPeerExecHandler_RejectedReasonReadable(t *testing.T) {
	ts := newPeerToolsTestServer(t, http.StatusForbidden, `{"reason":"not_allowed"}`)
	peerToolsTestEnv(t, ts)

	res := agentPeerExecPayload(map[string]interface{}{
		"type": "peer.exec", "request_id": "req-2", "device_id": "device-b", "command": "ls",
	})
	text, _ := res["error"].(string)
	if text == "" {
		t.Fatalf("expected readable error, got %v", res)
	}
	if !strings.Contains(text, "peer ACL or device switch off") {
		t.Fatalf("reason not readable: %q", text)
	}
	if strings.Contains(text, `{"reason"`) {
		t.Fatalf("raw body leaked: %q", text)
	}
}

func TestAgentPeerExecHandler_MissingParams(t *testing.T) {
	peerToolsTestEnv(t, nil)
	res := agentPeerExecPayload(map[string]interface{}{"type": "peer.exec", "request_id": "req-3"})
	if res["error"] == nil {
		t.Fatalf("missing params must error: %v", res)
	}
}

func TestAgentPeerFetchFileHandler(t *testing.T) {
	ts := newPeerToolsTestServer(t, http.StatusOK, `{"transfer_id":"tr-77"}`)
	peerToolsTestEnv(t, ts)

	res := agentPeerFetchFilePayload(map[string]interface{}{
		"type": "peer.fetch_file", "request_id": "req-4", "device_id": "device-b", "remote_path": "~/report.txt",
	})
	text, _ := res["result"].(string)
	for _, want := range []string{"tr-77", "transfer inbox", "transfer.progress"} {
		if !strings.Contains(text, want) {
			t.Fatalf("result %q missing %q", text, want)
		}
	}
	_, path, _, _, body := ts.last()
	if path != "/api/transfers" {
		t.Fatalf("path = %s", path)
	}
	var sent map[string]interface{}
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("body = %s", body)
	}
	if sent["source_device_id"] != "device-b" || sent["source_path"] != "~/report.txt" {
		t.Fatalf("transfers body shape = %v", sent)
	}
}

func TestAgentDeviceNoteHandler(t *testing.T) {
	ts := newPeerToolsTestServer(t, http.StatusOK, `{"entries":[{"key":"deploy_root","value":"/srv/app","updated_by":"llm"}]}`)
	peerToolsTestEnv(t, ts)

	res := agentDeviceNotePayload(map[string]interface{}{
		"type": "peer.device_note", "request_id": "req-5", "device_id": "device-x",
		"key": "deploy_root", "value": "/srv/app",
	})
	text, _ := res["result"].(string)
	if !strings.Contains(text, "deploy_root") || !strings.Contains(text, "device-x") || !strings.Contains(text, "updated_by=llm") {
		t.Fatalf("result = %q", text)
	}
	method, path, _, _, body := ts.last()
	if method != http.MethodPatch {
		t.Fatalf("method = %s, want PATCH", method)
	}
	if path != "/api/devices/device-x/metadata" {
		t.Fatalf("path = %s", path)
	}
	var sent struct {
		Entries []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("body = %s", body)
	}
	if len(sent.Entries) != 1 || sent.Entries[0].Key != "deploy_root" || sent.Entries[0].Value != "/srv/app" {
		t.Fatalf("entries shape = %+v", sent)
	}
}

func TestAgentDeviceNoteHandler_DefaultsToLocalDevice(t *testing.T) {
	ts := newPeerToolsTestServer(t, http.StatusOK, `{}`)
	peerToolsTestEnv(t, ts)

	res := agentDeviceNotePayload(map[string]interface{}{
		"type": "peer.device_note", "request_id": "req-6", "key": "k", "value": "v",
	})
	if res["error"] != nil {
		t.Fatalf("unexpected error: %v", res)
	}
	_, path, _, _, _ := ts.last()
	if path != "/api/devices/device-b/metadata" {
		t.Fatalf("path = %s, want local device id default", path)
	}
}

func TestAgentDeviceInfoHandler_LocalSnapshot(t *testing.T) {
	peerToolsTestEnv(t, nil)
	// 不起 mock 服务端：本地分支绝不发出网络请求。
	res := agentDeviceInfoPayload(map[string]interface{}{"type": "peer.device_info", "request_id": "req-7"})
	if res["error"] != nil {
		t.Fatalf("local device_info failed: %v", res)
	}
	text, _ := res["result"].(string)
	for _, want := range []string{"hostname:", "platform:", "agent_version:", "load:", "projects:", "authorized_directories:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("local snapshot missing %q:\n%s", want, text)
		}
	}
}

func TestAgentDeviceInfoHandler_RemoteDeviceAndMetadata(t *testing.T) {
	var mu sync.Mutex
	sawDevices, sawMeta := false, false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices/device-x", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sawDevices = true
		mu.Unlock()
		_, _ = w.Write([]byte(`{"name":"Liang Dev","platform":"linux-amd64","agent_version":"1.1.80","status":"online","load":{"cpu_usage_pct":12,"mem_usage_pct":40,"load_avg_1":0.5}}`))
	})
	mux.HandleFunc("/api/devices/device-x/metadata", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sawMeta = true
		mu.Unlock()
		_, _ = w.Write([]byte(`{"entries":[{"key":"deploy_root","updated_by":"llm"},{"key":"wifi_ssid","updated_by":"user"}]}`))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	peerToolsTestEnv(t, nil)
	origURL := agentPeerServerURL
	agentPeerServerURL = func() string { return ts.URL }
	t.Cleanup(func() { agentPeerServerURL = origURL })

	res := agentDeviceInfoPayload(map[string]interface{}{
		"type": "peer.device_info", "request_id": "req-8", "device_id": "device-x",
	})
	mu.Lock()
	defer mu.Unlock()
	if !sawDevices || !sawMeta {
		t.Fatalf("both GETs must fire: devices=%t meta=%t", sawDevices, sawMeta)
	}
	text, _ := res["result"].(string)
	for _, want := range []string{"Liang Dev", "linux-amd64", "1.1.80", "cpu=12%", "2 entries (deploy_root, wifi_ssid)", "updated_by: llm, user"} {
		if !strings.Contains(text, want) {
			t.Fatalf("remote snapshot missing %q:\n%s", want, text)
		}
	}
}

func TestAgentDeviceInfoHandler_WrapperBodyTolerated(t *testing.T) {
	ts := newPeerToolsTestServer(t, http.StatusOK, `{"data":{"exit_code":3,"stderr":"boom"}}`)
	peerToolsTestEnv(t, ts)

	res := agentPeerExecPayload(map[string]interface{}{
		"type": "peer.exec", "request_id": "req-9", "device_id": "device-b", "command": "false",
	})
	text, _ := res["result"].(string)
	if !strings.Contains(text, "exit_code=3") || !strings.Contains(text, "boom") {
		t.Fatalf("wrapped body not unwrapped: %q", text)
	}
}

func TestAgentDeviceListHandler_SlimDirectory(t *testing.T) {
	// 安全字段白名单：host/location/load/IP 一律不得进 LLM 上下文
	//（「LLM 路径零泄漏」纪律，spec §7.3）。本机 device-b 置顶。
	payload := `[
		{"id":"device-c","name":"win-box","platform":"windows-amd64","status":"offline","agent_version":"1.1.86","peer_exec_enabled":true,"host":"192.168.1.44","location":"bedroom","load":{"cpu_usage_pct":77}},
		{"id":"device-b","name":"mac-studio","platform":"darwin-arm64","status":"online","agent_version":"1.1.89","peer_exec_enabled":false,"host":"10.0.0.2"},
		{"device_id":"device-d","name":"headless","status":"online"}
	]`
	ts := newPeerToolsTestServer(t, http.StatusOK, payload)
	peerToolsTestEnv(t, ts)

	res := agentPeerDeviceListPayload(map[string]interface{}{"type": "peer.device_list", "request_id": "req-10"})
	if res["error"] != nil {
		t.Fatalf("device_list failed: %v", res)
	}
	text, _ := res["result"].(string)
	for _, want := range []string{
		"3 device(s)",
		"id=device-b | mac-studio (this machine) | platform=darwin-arm64 | status=online | agent=1.1.89 | peer_exec=disabled",
		"id=device-c | win-box | platform=windows-amd64 | status=offline | agent=1.1.86 | peer_exec=enabled",
		// device_id 兼容键也要吃到 id；缺 platform/agent 落 "-"。
		"id=device-d | headless | platform=- | status=online | agent=-",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("directory missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "192.168.1.44") || strings.Contains(text, "10.0.0.2") ||
		strings.Contains(text, "bedroom") || strings.Contains(text, "cpu=77") {
		t.Fatalf("host/location/load leaked into LLM context:\n%s", text)
	}
	// 本机置顶：device-b 行必须先于 device-c 行出现。
	if strings.Index(text, "id=device-b") > strings.Index(text, "id=device-c") {
		t.Fatalf("self device must sort first:\n%s", text)
	}
	_, path, _, _, _ := ts.last()
	if path != "/api/devices" {
		t.Fatalf("path = %s, want /api/devices", path)
	}
}

func TestAgentDeviceListHandler_ServerError(t *testing.T) {
	ts := newPeerToolsTestServer(t, http.StatusUnauthorized, `{"error":"token expired"}`)
	peerToolsTestEnv(t, ts)

	res := agentPeerDeviceListPayload(map[string]interface{}{"type": "peer.device_list", "request_id": "req-11"})
	if res["error"] == nil {
		t.Fatalf("expected structured error, got %+v", res)
	}
}

func TestSanitizePeerError_StripsTokenAndSignature(t *testing.T) {
	in := "dial failed: Get \"https://api.example.com/v1/peer/exec?token=abc123&x=1&q-signature=zzz&trace=ok\": connection reset"
	out := sanitizePeerError(errors.New(in))
	if strings.Contains(out, "abc123") || strings.Contains(out, "zzz") {
		t.Fatalf("secret survived sanitization: %q", out)
	}
	if !strings.Contains(out, "x=1") || !strings.Contains(out, "api.example.com") {
		t.Fatalf("benign parts must survive: %q", out)
	}
	// 预签名 URL 整体打码（纵深防御，同 sanitizeTransferError 模式）。
	out2 := sanitizePeerError(errors.New("put failed https://bucket.cos.ap-beijing.myqcloud.com/obj?sign=q-sign-algorithm%3Dsha1&token=xyz"))
	if strings.Contains(out2, "q-sign-algorithm") || strings.Contains(out2, "token=xyz") {
		t.Fatalf("presigned url survived: %q", out2)
	}
	// 限长 500。
	long := errors.New("x " + strings.Repeat("长", 600))
	if got := sanitizePeerError(long); len(got) > 501 {
		t.Fatalf("length cap failed: %d", len(got))
	}
	if sanitizePeerError(nil) != "" {
		t.Fatal("nil error must sanitize to empty")
	}
}

func TestAgentToolRegistry_ContainsPeerTools(t *testing.T) {
	reg := agentToolRegistry()
	want := map[string]struct {
		event    string
		readOnly bool
	}{
		"peer_exec":       {AgentPeerToolEventExec, false},
		"peer_fetch_file": {AgentPeerToolEventFetchFile, false},
		"device_info":     {AgentPeerToolEventDeviceInfo, true},
		"device_note":     {AgentPeerToolEventDeviceNote, false},
		"device_list":     {AgentPeerToolEventDeviceList, true},
	}
	for id, wantCaps := range want {
		tool, ok := reg.Get(wantCaps.event)
		if !ok {
			t.Fatalf("tool %s (event %s) not registered", id, wantCaps.event)
		}
		if tool.ID != id {
			t.Fatalf("event %s mapped to %s, want %s", wantCaps.event, tool.ID, id)
		}
		if tool.Caps.ReadOnly != wantCaps.readOnly {
			t.Fatalf("tool %s read_only = %t, want %t", id, tool.Caps.ReadOnly, wantCaps.readOnly)
		}
		if tool.Handler == nil {
			t.Fatalf("tool %s handler nil", id)
		}
	}
	// rev 随新工具 bump——心跳漂移检测靠它重拉 tools.list。
	if agentToolRegistryRev < 4 {
		t.Fatalf("rev = %d, want >= 4 after peer tools", agentToolRegistryRev)
	}
	if tool, _ := reg.Get(AgentPeerToolEventExec); tool.Caps.TimeoutMs != 640000 {
		t.Fatalf("peer_exec TimeoutMs = %d, want 640000 (fence 600s < ctx 630s < guard 640s)", tool.Caps.TimeoutMs)
	}
	if tool, _ := reg.Get(AgentPeerToolEventExec); tool.Caps.MaxOutputBytes != 4<<20 {
		t.Fatalf("peer_exec MaxOutputBytes = %d, want 4MiB (escaped worst-case result must fit)", tool.Caps.MaxOutputBytes)
	}
}

func TestAgentCorePeerTools_BridgeSharesHandlers(t *testing.T) {
	ts := newPeerToolsTestServer(t, http.StatusOK, `{"exit_code":0,"stdout":"bridge-ok"}`)
	peerToolsTestEnv(t, ts)

	m := &agentAIManager{coreJournal: newAgentCoreJournal(t.TempDir())}
	tools := m.agentCorePeerTools(agentAIRun{sessionID: "sess-bridge"})
	if len(tools) != 5 {
		t.Fatalf("tools = %d, want 5", len(tools))
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name()] = true
	}
	for _, want := range []string{"peer.exec", "peer.fetch_file", "peer.device_info", "peer.device_note", "peer.device_list"} {
		if !names[want] {
			t.Fatalf("missing bridge tool %s (have %v)", want, names)
		}
	}
	// 与内置内核工具名集不重叠（桥接工具约定）。
	for name := range names {
		if agentCoreKernelBuiltinTools[name] {
			t.Fatalf("bridge tool %s collides with kernel builtin", name)
		}
	}

	// peer_exec 桥与注册表 handler 共享实现：同一 mock 服务端、同一调用形状。
	var execTool = tools[0]
	for _, tool := range tools {
		if tool.Name() == "peer.exec" {
			execTool = tool
		}
	}
	res := execTool.Execute(context.Background(), map[string]interface{}{
		"device_id": "device-b", "command": "echo bridge-ok",
	}, nil)
	if res.IsError || !strings.Contains(res.Content, "exit_code=0") || !strings.Contains(res.Content, "bridge-ok") {
		t.Fatalf("bridge execute = %+v", res)
	}
	_, path, _, _, body := ts.last()
	if path != "/api/peer/exec" {
		t.Fatalf("path = %s", path)
	}
	if !strings.Contains(body, `"target_device_id":"device-b"`) {
		t.Fatalf("body = %s", body)
	}
}

func TestPeerReasonReadable(t *testing.T) {
	cases := map[string]string{
		"busy":           "busy running another peer command",
		"device_offline": "offline",
		"not_allowed":    "peer ACL",
		"rate_limited":   "rate limited",
		"custom_reason":  "rejected: custom_reason",
		"":               "rejected by server",
	}
	for reason, want := range cases {
		if got := peerReasonReadable(reason); !strings.Contains(got, want) {
			t.Fatalf("peerReasonReadable(%q) = %q, want contains %q", reason, got, want)
		}
	}
}

func TestDoAgentServerPOST_DedicatedClientWaitsPastEightSeconds(t *testing.T) {
	// 对抗审查准 blocker：peer 编排必须走专用无 Timeout 客户端（ctx 驱动）。
	// 共享 s.client（Timeout 8s）会把 >8s 的长命令在 A 侧掐成 transport 超时，
	// 诱导 LLM 重试 → 目标机重复执行。这里让 mock 服务端 9s 后才应答：必须
	// 等到响应，且耗时 ≥9s。
	peerToolsTestEnv(t, nil)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(9 * time.Second)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"exit_code":0}`))
	}))
	t.Cleanup(slow.Close)
	origURL := agentPeerServerURL
	agentPeerServerURL = func() string { return slow.URL }
	t.Cleanup(func() { agentPeerServerURL = origURL })

	svc := &AgentService{client: &http.Client{Timeout: 3 * time.Second}}
	svc.mu.Lock()
	svc.state.Registered = true
	svc.state.DeviceID = "device-b"
	svc.forwardedUserAuthorization = "Bearer t"
	svc.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	started := time.Now()
	// svc.client 的 3s Timeout 对本调用无效：doAgentServerJSON 走专用客户端。
	raw, err := svc.doAgentServerPOST(ctx, slow.URL+"/api/peer/exec", nil)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("dedicated client must wait past 8s: %v", err)
	}
	if !strings.Contains(string(raw), "exit_code") {
		t.Fatalf("body = %s", raw)
	}
	if elapsed < 9*time.Second {
		t.Fatalf("returned early at %v — response was not really awaited", elapsed)
	}
}

func TestAgentPeerExecHandler_MissingExitCodeIsStructuredError(t *testing.T) {
	// should-fix 1：响应缺 exit_code（或解析失败）绝不能解析成 exit_code=0
	// 假成功。
	ts := newPeerToolsTestServer(t, http.StatusOK, `{"stdout":"partial"}`)
	peerToolsTestEnv(t, ts)

	res := agentPeerExecPayload(map[string]interface{}{
		"type": "peer.exec", "request_id": "req-nec", "device_id": "device-b", "command": "ls",
	})
	text, _ := res["error"].(string)
	if !strings.Contains(text, "missing exit_code") {
		t.Fatalf("expected structured error, got %v", res)
	}
	if _, hasResult := res["result"]; hasResult {
		t.Fatalf("must not return a fake success result: %v", res)
	}
}

func TestAgentPeerFetchFileHandler_TransferNestedShell(t *testing.T) {
	// 对抗审查 blocker 2：fetch 编排回 201 + {transfer:{transfer_id,...}} 嵌套壳。
	ts := newPeerToolsTestServer(t, http.StatusCreated, `{"transfer":{"transfer_id":"tr-nest-9","status":"pending"}}`)
	peerToolsTestEnv(t, ts)

	res := agentPeerFetchFilePayload(map[string]interface{}{
		"type": "peer.fetch_file", "request_id": "req-nest", "device_id": "device-b", "remote_path": "a.txt",
	})
	text, _ := res["result"].(string)
	if !strings.Contains(text, "tr-nest-9") {
		t.Fatalf("nested transfer shell not unwrapped: %v", res)
	}
}

func TestAgentPeerExecHandler_TimeoutPreClamped(t *testing.T) {
	// should-fix 6：timeout_ms 越界本地预钳，不让服务端 Zod 拒绝。
	ts := newPeerToolsTestServer(t, http.StatusOK, `{"exit_code":0}`)
	peerToolsTestEnv(t, ts)

	agentPeerExecPayload(map[string]interface{}{
		"type": "peer.exec", "request_id": "req-clamp", "device_id": "device-b",
		"command": "ls", "timeout_ms": float64(500),
	})
	_, _, _, _, body := ts.last()
	var sent map[string]interface{}
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("body = %s", body)
	}
	if sent["timeout_ms"] != float64(1000) {
		t.Fatalf("timeout_ms = %v, want clamped 1000", sent["timeout_ms"])
	}
}

func TestAgentDeviceNoteHandler_OverLengthFailsLocally(t *testing.T) {
	// should-fix 6：value 超 4096 字节本地预检报可读错误，不发起网络请求。
	peerToolsTestEnv(t, nil)
	res := agentDeviceNotePayload(map[string]interface{}{
		"type": "peer.device_note", "request_id": "req-len",
		"key": "k", "value": strings.Repeat("v", 5000),
	})
	text, _ := res["error"].(string)
	if !strings.Contains(text, "too long") {
		t.Fatalf("expected local length error, got %v", res)
	}
}

func TestPeerStatusErrorReason_IncludesDetails(t *testing.T) {
	// should-fix 6：ZodError details 不能被丢弃。
	statusErr := agentPeerHTTPStatusError{
		status: 400,
		body:   `{"error":"validation_error","details":[{"path":["timeout_ms"],"message":"Number must be greater than or equal to 1000"}]}`,
	}
	reason := peerStatusErrorReason(statusErr)
	if !strings.Contains(reason, "timeout_ms") || !strings.Contains(reason, "1000") {
		t.Fatalf("details dropped: %q", reason)
	}
}
