package services

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"aliang.one/nursorgate/processor/config"
)

func setOpenVikingGlobal(t *testing.T, ov *config.OpenVikingConfig) {
	t.Helper()
	old := config.GetGlobalConfig()
	customer := &config.CustomerConfig{}
	if ov != nil {
		customer.OpenViking = ov
	}
	config.SetGlobalConfig(&config.Config{Customer: customer})
	t.Cleanup(func() { config.SetGlobalConfig(old) })
}

func readOpenVikingClaudeRaw(t *testing.T, home string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func writeOpenVikingClaudeRaw(t *testing.T, home, content string) {
	t.Helper()
	path := filepath.Join(home, ".claude.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func openVikingClaudeEntryFrom(t *testing.T, home string) (map[string]interface{}, bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		MCPServers map[string]map[string]interface{} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	e, ok := root.MCPServers["openviking"]
	return e, ok
}

// 默认关:enabled 未设=整层关。无既有条目 → 三处都不落。
func TestOpenVikingDisabledByDefault(t *testing.T) {
	home := t.TempDir()
	setOpenVikingGlobal(t, nil)
	if err := EnsureOpenVikingMatchesConfigForTest(home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".aliang", "mcp.json")); !os.IsNotExist(err) {
		t.Fatalf("默认关不得创建注册表文件: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf("默认关不得创建 claude.json: %v", err)
	}
}

// 默认关 + 外来 claude 条目(BYO 异形)→ 保留不劫持。
func TestOpenVikingDisabledKeepsForeignClaudeEntry(t *testing.T) {
	home := t.TempDir()
	foreign := `{"mcpServers":{"openviking":{"type":"http","url":"https://my-own.example/mcp","headers":{"Authorization":"Bearer mine"}}}}`
	writeOpenVikingClaudeRaw(t, home, foreign)
	setOpenVikingGlobal(t, nil)
	if err := EnsureOpenVikingMatchesConfigForTest(home); err != nil {
		t.Fatal(err)
	}
	if got := readOpenVikingClaudeRaw(t, home); got != foreign {
		t.Fatalf("外来条目必须原样保留:\nwant=%s\ngot=%s", foreign, got)
	}
}

// BYO 开启:注册表条目(http+Bearer+管理面裁剪)、codex 托管块(headers
// inline 透传)、claude 条目三处齐落。
func TestOpenVikingBYORegistersAllThreeSurfaces(t *testing.T) {
	home := t.TempDir()
	setOpenVikingGlobal(t, &config.OpenVikingConfig{
		Enabled:  boolPtr(true),
		Endpoint: "https://ov.example.com/mcp",
		Mode:     "byo",
		APIKey:   "sk-test-123",
	})
	if err := EnsureOpenVikingMatchesConfigForTest(home); err != nil {
		t.Fatal(err)
	}
	// 注册表:条目形态+exclude_tools
	root, _, err := loadAliangMCPRegistryRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	servers, _ := root["servers"].(map[string]interface{})
	raw, err := json.Marshal(servers["openviking"])
	if err != nil {
		t.Fatal(err)
	}
	var entry struct {
		Type         string            `json:"type"`
		URL          string            `json:"url"`
		Headers      map[string]string `json:"headers"`
		ExcludeTools []string          `json:"exclude_tools"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Type != "http" || entry.URL != "https://ov.example.com/mcp" ||
		entry.Headers["Authorization"] != "Bearer sk-test-123" {
		t.Fatalf("注册表条目形态不符: %s", raw)
	}
	if len(entry.ExcludeTools) != len(openVikingExcludeTools) {
		t.Fatalf("exclude_tools 管理面裁剪缺失: %v", entry.ExcludeTools)
	}
	// codex 托管块:inline http_headers(§7.6 透传),无子表头形态
	codex, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	ct := string(codex)
	if !strings.Contains(ct, "[mcp_servers.aliang-mcp-openviking]") ||
		!strings.Contains(ct, `http_headers = {"Authorization" = "Bearer sk-test-123"}`) ||
		strings.Contains(ct, "[mcp_servers.aliang-mcp-openviking.http_headers]") {
		t.Fatalf("codex 托管块形态不符: %s", ct)
	}
	// claude 条目
	e, ok := openVikingClaudeEntryFrom(t, home)
	if !ok || e["url"] != "https://ov.example.com/mcp" {
		t.Fatalf("claude 条目缺失或形态不符: %v", e)
	}
}

// 幂等:二次 ensure 不改写任何面。
func TestOpenVikingEnsureIdempotent(t *testing.T) {
	home := t.TempDir()
	setOpenVikingGlobal(t, &config.OpenVikingConfig{
		Enabled: boolPtr(true), Endpoint: "https://ov.example.com/mcp", Mode: "byo", APIKey: "k1",
	})
	if err := EnsureOpenVikingMatchesConfigForTest(home); err != nil {
		t.Fatal(err)
	}
	claudeBefore := readOpenVikingClaudeRaw(t, home)
	codexBefore, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureOpenVikingMatchesConfigForTest(home); err != nil {
		t.Fatal(err)
	}
	if got := readOpenVikingClaudeRaw(t, home); got != claudeBefore {
		t.Fatalf("claude.json 必须幂等:\nbefore=%s\ngot=%s", claudeBefore, got)
	}
	if got, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml")); !bytes.Equal(codexBefore, got) {
		t.Fatalf("codex config 必须幂等")
	}
}

// BYO 先行:claude 既有同名条目内容不同 → 不覆盖;注册表与 codex 块仍按真值。
func TestOpenVikingBYOForeignClaudeEntryKept(t *testing.T) {
	home := t.TempDir()
	foreign := `{"other":1,"mcpServers":{"openviking":{"type":"http","url":"https://mine.example/mcp","headers":{"Authorization":"Bearer mine"}}}}`
	writeOpenVikingClaudeRaw(t, home, foreign)
	setOpenVikingGlobal(t, &config.OpenVikingConfig{
		Enabled: boolPtr(true), Endpoint: "https://ov.example.com/mcp", Mode: "byo", APIKey: "k1",
	})
	if err := EnsureOpenVikingMatchesConfigForTest(home); err != nil {
		t.Fatal(err)
	}
	e, ok := openVikingClaudeEntryFrom(t, home)
	if !ok || e["url"] != "https://mine.example/mcp" {
		t.Fatalf("BYO claude 条目必须原样保留: %v", e)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"other":1`) {
		t.Fatalf("claude.json 其余顶层键必须原样保留: %s", raw)
	}
	// 注册表与 codex 块仍按真值落
	if _, err := os.Stat(filepath.Join(home, ".codex", "config.toml")); err != nil {
		t.Fatalf("codex 托管块必须落: %v", err)
	}
}

// 关闭:本平台三面齐拆;外来 claude 条目保留。
func TestOpenVikingDisableRemovesOwnedSurfaces(t *testing.T) {
	home := t.TempDir()
	setOpenVikingGlobal(t, &config.OpenVikingConfig{
		Enabled: boolPtr(true), Mode: "byo", APIKey: "k1",
	})
	if err := EnsureOpenVikingMatchesConfigForTest(home); err != nil {
		t.Fatal(err)
	}
	if _, ok := openVikingClaudeEntryFrom(t, home); !ok {
		t.Fatal("前置:开启后 claude 条目应存在")
	}
	setOpenVikingGlobal(t, &config.OpenVikingConfig{Enabled: boolPtr(false)})
	if err := EnsureOpenVikingMatchesConfigForTest(home); err != nil {
		t.Fatal(err)
	}
	if _, ok := openVikingClaudeEntryFrom(t, home); ok {
		t.Fatal("关闭后 claude 条目必须移除")
	}
	// codex 托管块摘除后文件可能残留为空壳(同步器只摘块不删文件),断言内容
	codexRaw, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(codexRaw), "aliang-mcp-openviking") {
		t.Fatalf("关闭后 codex 托管块必须摘除: %s", codexRaw)
	}
	root, _, err := loadAliangMCPRegistryRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	if servers, _ := root["servers"].(map[string]interface{}); len(servers) != 0 {
		t.Fatalf("关闭后注册表必须空: %v", servers)
	}
}

// 关闭 + 外来条目(非本平台形态)→ claude 保留。
func TestOpenVikingDisableKeepsForeignClaudeEntry(t *testing.T) {
	home := t.TempDir()
	foreign := `{"mcpServers":{"openviking":{"type":"http","url":"https://mine.example/mcp"}}}`
	writeOpenVikingClaudeRaw(t, home, foreign)
	setOpenVikingGlobal(t, &config.OpenVikingConfig{Enabled: boolPtr(false)})
	if err := EnsureOpenVikingMatchesConfigForTest(home); err != nil {
		t.Fatal(err)
	}
	if got := readOpenVikingClaudeRaw(t, home); got != foreign {
		t.Fatalf("外来条目必须原样保留:\nwant=%s\ngot=%s", foreign, got)
	}
}

// managed 模式:端点未就绪(api_server 空)→ 诚实报错走跳过语义,三处不落。
func TestOpenVikingManagedSkipsWhenEndpointUnset(t *testing.T) {
	home := t.TempDir()
	setOpenVikingGlobal(t, &config.OpenVikingConfig{Enabled: boolPtr(true)})
	err := ensureOpenVikingLocked(home)
	if err == nil || !strings.Contains(err.Error(), "managed 铸 key 未就绪") {
		t.Fatalf("managed 未就绪必须返回跳过语义错误: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf("跳过 boot 不得写 claude.json: %v", err)
	}
}

// managed 全链(spec §7.1):铸 key 端点受理 → 三面按签发 key/endpoint 落齐。
func TestOpenVikingManagedMintsViaCredentialEndpoint(t *testing.T) {
	var gotAuth atomic.Value
	credSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		if r.Method != http.MethodPost || r.URL.Path != "/api/agent/openviking/credential" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","endpoint":"https://ov-minted.example/mcp","api_key":"sk-ov-minted"}`))
	}))
	defer credSrv.Close()
	home := t.TempDir()
	old := config.GetGlobalConfig()
	config.SetGlobalConfig(&config.Config{
		Core:     &config.CoreConfig{APIServer: credSrv.URL},
		Customer: &config.CustomerConfig{OpenViking: &config.OpenVikingConfig{Enabled: boolPtr(true)}},
	})
	t.Cleanup(func() { config.SetGlobalConfig(old) })
	if err := EnsureOpenVikingMatchesConfigForTest(home); err != nil {
		t.Fatal(err)
	}
	// 签发 endpoint 覆盖配置缺省(凭据签发方是真值)
	e, ok := openVikingClaudeEntryFrom(t, home)
	if !ok || e["url"] != "https://ov-minted.example/mcp" {
		t.Fatalf("claude 条目应使用签发 endpoint: %v", e)
	}
	root, _, err := loadAliangMCPRegistryRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	servers, _ := root["servers"].(map[string]interface{})
	raw, _ := json.Marshal(servers["openviking"])
	if !strings.Contains(string(raw), "sk-ov-minted") {
		t.Fatalf("注册表应持有签发 key: %s", raw)
	}
	// 设备 JWT 附带契约:有会话时 Bearer 形态;无会话时空(强制在服务端)
	if v := gotAuth.Load(); v != nil {
		if s, _ := v.(string); s != "" && !strings.HasPrefix(s, "Bearer ") {
			t.Fatalf("认证头必须 Bearer 形态: %q", s)
		}
	}
}

// 生效助手:默认关、端点缺省回落、mode 归一(未知值按 managed)。
func TestEffectiveOpenVikingHelpers(t *testing.T) {
	var nilCfg *config.Config
	if nilCfg.EffectiveOpenVikingEnabled() {
		t.Fatal("nil config 必须默认关")
	}
	setOpenVikingGlobal(t, nil)
	cfg := config.GetGlobalConfig()
	if cfg.EffectiveOpenVikingEnabled() {
		t.Fatal("Enabled 缺省必须关")
	}
	if m := cfg.EffectiveOpenVikingMode(); m != "managed" {
		t.Fatalf("缺省 mode 必须 managed, got %q", m)
	}
	setOpenVikingGlobal(t, &config.OpenVikingConfig{Enabled: boolPtr(true), Mode: "BYO"})
	cfg = config.GetGlobalConfig()
	if !cfg.EffectiveOpenVikingEnabled() {
		t.Fatal("显式开启必须生效")
	}
	if m := cfg.EffectiveOpenVikingMode(); m != "byo" {
		t.Fatalf("mode 大小写归一失败: %q", m)
	}
	if ep := cfg.EffectiveOpenVikingEndpoint(); ep != config.OpenVikingDefaultEndpoint {
		t.Fatalf("端点缺省回落失败: %q", ep)
	}
	setOpenVikingGlobal(t, &config.OpenVikingConfig{Enabled: boolPtr(true), Mode: "wat", Endpoint: "  https://x/mcp  "})
	cfg = config.GetGlobalConfig()
	if m := cfg.EffectiveOpenVikingMode(); m != "managed" {
		t.Fatalf("未知 mode 必须按 managed: %q", m)
	}
	if ep := cfg.EffectiveOpenVikingEndpoint(); ep != "https://x/mcp" {
		t.Fatalf("端点必须 trim: %q", ep)
	}
}

// deepMerge 专项回归(§11.2-8):patch 只带 openviking.enabled 不丢
// endpoint/api_key/mode 现值(缺键 preserve)。
func TestCustomerDeepMergePreservesOpenVikingFields(t *testing.T) {
	base := map[string]interface{}{
		"openviking": map[string]interface{}{
			"enabled":  false,
			"endpoint": "https://ov.example.com/mcp",
			"mode":     "byo",
			"api_key":  "sk-keep",
		},
	}
	patch := map[string]interface{}{
		"openviking": map[string]interface{}{"enabled": true},
	}
	merged := deepMergeJSONObjects(base, patch)
	ov, _ := merged["openviking"].(map[string]interface{})
	if ov == nil {
		t.Fatal("openviking 段丢失")
	}
	if v, _ := ov["enabled"].(bool); !v {
		t.Fatalf("enabled 应翻真: %v", ov)
	}
	if ov["endpoint"] != "https://ov.example.com/mcp" || ov["api_key"] != "sk-keep" || ov["mode"] != "byo" {
		t.Fatalf("缺键必须 preserve: %v", ov)
	}
}

// BYO 检测:claude 侧拒覆盖时置位;status 透出。
func TestOpenVikingByoDetectionFlag(t *testing.T) {
	home := t.TempDir()
	writeOpenVikingClaudeRaw(t, home, `{"mcpServers":{"openviking":{"type":"http","url":"https://mine.example/mcp"}}}`)
	setOpenVikingGlobal(t, &config.OpenVikingConfig{
		Enabled: boolPtr(true), Endpoint: "https://ov.example.com/mcp", Mode: "byo", APIKey: "k1",
	})
	if err := EnsureOpenVikingMatchesConfigForTest(home); err != nil {
		t.Fatal(err)
	}
	if !openVikingByoDetected.Load() {
		t.Fatal("BYO 拒覆盖必须置检测标志")
	}
}

// capture 触发门(§7.3「MCP 建连健康」):拨测判死(连败≥阈值)时静默不上传。
func TestOpenVikingCaptureGatedByProbeHealth(t *testing.T) {
	st, srv := startOpenVikingCaptureStub(t)
	home := t.TempDir()
	setOpenVikingRegistryEntry(t, home, srv.URL, nil)
	setOpenVikingGlobal(t, &config.OpenVikingConfig{Enabled: boolPtr(true), Mode: "byo", APIKey: "k"})
	openVikingProbeFails.Store(int32(openVikingProbeFailMax)) // 判死态
	if err := openvikingCaptureRun(context.Background(), "s", "u", "a"); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&st.calls); got != 0 {
		t.Fatalf("拨测判死不得上传, got %d", got)
	}
	openVikingProbeFails.Store(0) // 复位,恢复后应续流
	if err := openvikingCaptureRun(context.Background(), "s", "u", "a"); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&st.calls); got != 1 {
		t.Fatalf("恢复后应续流, got %d", got)
	}
}
