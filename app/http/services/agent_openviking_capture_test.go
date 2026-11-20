package services

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"aliang.one/nursorgate/processor/config"
)

// —— openvikingCaptureContent:剥标记块/去空白/截断 ——

func TestOpenVikingCaptureContentSanitize(t *testing.T) {
	// 完整标记块剥离
	got := openvikingCaptureContent("前文\n<openviking-context>注入内容</openviking-context>\n后文")
	if got != "前文\n\n后文" {
		t.Fatalf("标记块剥离不符: %q", got)
	}
	// 未闭合首段(拼接截断形态)也要剥干净
	if got := openvikingCaptureContent("<openviking-context>泄漏尾"); got != "" {
		t.Fatalf("未闭合标记块必须剥空: %q", got)
	}
	// 纯空白 → 空
	if got := openvikingCaptureContent("  \n\t "); got != "" {
		t.Fatalf("纯空白必须归空: %q", got)
	}
	// 截断带标记
	long := strings.Repeat("x", openVikingCaptureMaxContent+100)
	got = openvikingCaptureContent(long)
	if len(got) != openVikingCaptureMaxContent+len("\n…[截断]") {
		t.Fatalf("截断长度不符: %d", len(got))
	}
	if !strings.HasSuffix(got, "…[截断]") {
		t.Fatal("截断必须带标记")
	}
}

// —— capture 传输:httptest 假 MCP 端点 ——

type ovCaptureServer struct {
	mu       sync.Mutex
	auth     string
	calls    int32
	lastBody map[string]interface{}
}

func startOpenVikingCaptureStub(t *testing.T) (*ovCaptureServer, *httptest.Server) {
	t.Helper()
	st := &ovCaptureServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&st.calls, 1)
		st.mu.Lock()
		st.auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		_ = json.Unmarshal(raw, &body)
		st.lastBody = body
		st.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`))
	}))
	t.Cleanup(srv.Close)
	return st, srv
}

func setOpenVikingRegistryEntry(t *testing.T, home, endpoint string, headers map[string]string) {
	t.Helper()
	old := mcpRegistryHome
	mcpRegistryHome = func() string { return home }
	t.Cleanup(func() { mcpRegistryHome = old })
	if err := upsertAliangMCPServerForTest(home, openVikingManagedServerName, aliangMCPServerEntry{
		Type: "http", URL: endpoint, Headers: headers,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenVikingCaptureRunPostsRemember(t *testing.T) {
	st, srv := startOpenVikingCaptureStub(t)
	home := t.TempDir()
	setOpenVikingRegistryEntry(t, home, srv.URL, map[string]string{"Authorization": "Bearer sk-ov-test"})
	setOpenVikingGlobal(t, &config.OpenVikingConfig{Enabled: boolPtr(true), Mode: "byo", APIKey: "unused-by-registry"})

	if err := openvikingCaptureRun(context.Background(), "sess-1", "用户的问题", "助手的回答"); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&st.calls); got != 1 {
		t.Fatalf("应恰好一次上传, got %d", got)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.auth != "Bearer sk-ov-test" {
		t.Fatalf("Bearer 必须来自注册表条目: %q", st.auth)
	}
	params, _ := st.lastBody["params"].(map[string]interface{})
	if params["name"] != "remember" {
		t.Fatalf("必须调 remember 工具: %v", params)
	}
	args, _ := params["arguments"].(map[string]interface{})
	msgs, _ := args["messages"].([]interface{})
	if len(msgs) != 2 {
		t.Fatalf("应两条消息, got %d", len(msgs))
	}
	m0, _ := msgs[0].(map[string]interface{})
	m1, _ := msgs[1].(map[string]interface{})
	if m0["role"] != "user" || m0["content"] != "用户的问题" ||
		m1["role"] != "assistant" || m1["content"] != "助手的回答" {
		t.Fatalf("消息形态不符: %v %v", m0, m1)
	}
}

func TestOpenVikingCaptureDisabledSilent(t *testing.T) {
	st, srv := startOpenVikingCaptureStub(t)
	home := t.TempDir()
	setOpenVikingRegistryEntry(t, home, srv.URL, nil)
	setOpenVikingGlobal(t, nil) // 未启用
	if err := openvikingCaptureRun(context.Background(), "s", "u", "a"); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&st.calls); got != 0 {
		t.Fatalf("未启用不得上传, got %d", got)
	}
}

func TestOpenVikingCaptureMissingRegistrySilent(t *testing.T) {
	st, _ := startOpenVikingCaptureStub(t)
	home := t.TempDir()
	old := mcpRegistryHome
	mcpRegistryHome = func() string { return home }
	t.Cleanup(func() { mcpRegistryHome = old })
	setOpenVikingGlobal(t, &config.OpenVikingConfig{Enabled: boolPtr(true), Mode: "byo", APIKey: "k"})
	if err := openvikingCaptureRun(context.Background(), "s", "u", "a"); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&st.calls); got != 0 {
		t.Fatalf("注册表无条目不得上传, got %d", got)
	}
}

func TestOpenVikingCaptureEmptyContentSilent(t *testing.T) {
	st, srv := startOpenVikingCaptureStub(t)
	home := t.TempDir()
	setOpenVikingRegistryEntry(t, home, srv.URL, nil)
	setOpenVikingGlobal(t, &config.OpenVikingConfig{Enabled: boolPtr(true), Mode: "byo", APIKey: "k"})
	if err := openvikingCaptureRun(context.Background(), "s", "  ", ""); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&st.calls); got != 0 {
		t.Fatalf("双空正文不得上传, got %d", got)
	}
}

// —— §7.5 拨测 ——

func TestOpenVikingProbeHealthURL(t *testing.T) {
	if got := openvikingProbeHealthURL("https://openviking.aliang.xin/mcp"); got != "https://openviking.aliang.xin/health" {
		t.Fatalf("健康端点推导不符: %q", got)
	}
	if got := openvikingProbeHealthURL("https://ov.example/mcp/"); got != "https://ov.example/health" {
		t.Fatalf("尾斜杠形态推导不符: %q", got)
	}
	if got := openvikingProbeHealthURL(""); got != "" {
		t.Fatalf("空入口必须空推导: %q", got)
	}
}

func TestOpenVikingProbeTickTransitions(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer healthy.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()

	openVikingProbeFails.Store(0)
	openVikingProbeOK.Store(false)

	// 未启用:复位跳过
	setOpenVikingGlobal(t, nil)
	openvikingProbeTick()
	if openVikingProbeOK.Load() {
		t.Fatal("未启用不得置健康")
	}

	// 启用+健康:置健康、清零
	setOpenVikingGlobal(t, &config.OpenVikingConfig{Enabled: boolPtr(true), Mode: "byo", APIKey: "k"})
	setOpenVikingRegistryEntry(t, t.TempDir(), healthy.URL, nil)
	openvikingProbeTick()
	if !openVikingProbeOK.Load() || openVikingProbeFails.Load() != 0 {
		t.Fatalf("健康探针状态不符: ok=%v fails=%d", openVikingProbeOK.Load(), openVikingProbeFails.Load())
	}

	// 连续失败:计数爬升,第 3 次置不可达
	setOpenVikingRegistryEntry(t, t.TempDir(), broken.URL, nil)
	for i := 1; i <= openVikingProbeFailMax; i++ {
		openvikingProbeTick()
		if int(openVikingProbeFails.Load()) != i {
			t.Fatalf("失败计数不符: want %d got %d", i, openVikingProbeFails.Load())
		}
		if openVikingProbeOK.Load() {
			t.Fatal("失败期不得置健康")
		}
	}

	// 恢复:清零置健康
	setOpenVikingRegistryEntry(t, t.TempDir(), healthy.URL, nil)
	openvikingProbeTick()
	if !openVikingProbeOK.Load() || openVikingProbeFails.Load() != 0 {
		t.Fatalf("恢复后状态不符: ok=%v fails=%d", openVikingProbeOK.Load(), openVikingProbeFails.Load())
	}
}
