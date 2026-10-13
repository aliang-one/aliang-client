package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"aliang.one/nursorgate/app/http/models"
)

// 线格式契约:mcp_servers 是 snake_case 顶层键;空值时整键省略(omitempty)。
func TestAgentToolMCPServersJSONShape(t *testing.T) {
	with := models.AgentTool{ID: "claude", MCPServers: []models.AgentMCPServer{
		{Name: "serena", Transport: "stdio", Command: "uvx serena", Source: "user"},
	}}
	raw, err := json.Marshal(with)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	servers, ok := m["mcp_servers"].([]interface{})
	if !ok || len(servers) != 1 {
		t.Fatalf("want mcp_servers array with 1 entry, got %s", raw)
	}
	first := servers[0].(map[string]interface{})
	if first["name"] != "serena" || first["transport"] != "stdio" || first["command"] != "uvx serena" {
		t.Fatalf("unexpected entry: %v", first)
	}
	if _, ok := first["url"]; ok {
		t.Fatalf("stdio entry must omit url")
	}

	bare, err := json.Marshal(models.AgentTool{ID: "opencode"})
	if err != nil {
		t.Fatalf("marshal bare: %v", err)
	}
	if containsKey(t, bare, "mcp_servers") {
		t.Fatalf("nil MCPServers must omit mcp_servers")
	}
}

func containsKey(t *testing.T, raw []byte, key string) bool {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	_, ok := m[key]
	return ok
}

func TestRedactMCPCommand(t *testing.T) {
	// 短 token 原样保留
	if got := redactMCPCommand([]string{"uvx", "serena"}); got != "uvx serena" {
		t.Fatalf("short tokens kept: %q", got)
	}
	// 长 token(>32 且无空白)打码
	long := strings.Repeat("a", 40)
	got := redactMCPCommand([]string{"node", long})
	if !strings.HasPrefix(got, "node <redacted:40>") {
		t.Fatalf("long token redacted, got %q", got)
	}
	// 含空白的长参数不打码(自然语句),但整体截 512
	spacy := strings.Repeat("word ", 120)
	if len(redactMCPCommand([]string{"echo", spacy})) > 512 {
		t.Fatalf("command truncated to 512")
	}
	// 阈值边界:token 恰 32 保留、33 打码
	if got := redactMCPCommand([]string{"node", strings.Repeat("a", 32)}); got != "node "+strings.Repeat("a", 32) {
		t.Fatalf("token of exactly 32 kept: %q", got)
	}
	if got := redactMCPCommand([]string{"node", strings.Repeat("a", 33)}); got != "node <redacted:33>" {
		t.Fatalf("token of 33 redacted: %q", got)
	}
}

func TestRedactMCPURL(t *testing.T) {
	if got := redactMCPURL("https://mcp.example.com/mcp"); got != "https://mcp.example.com/mcp" {
		t.Fatalf("plain url kept: %q", got)
	}
	long := strings.Repeat("k", 30)
	got := redactMCPURL("https://mcp.example.com/mcp?key=" + long)
	if strings.Contains(got, long) {
		t.Fatalf("long query value must be masked: %q", got)
	}
	if !strings.Contains(got, "key=%3Credacted%3E") && !strings.Contains(got, "key=<redacted>") {
		t.Fatalf("masked value present: %q", got)
	}
	// 阈值边界:query 值恰 24 保留、25 打码
	if got := redactMCPURL("https://mcp.example.com/mcp?k=" + strings.Repeat("v", 24)); got != "https://mcp.example.com/mcp?k="+strings.Repeat("v", 24) {
		t.Fatalf("query value of exactly 24 kept: %q", got)
	}
	got = redactMCPURL("https://mcp.example.com/mcp?k=" + strings.Repeat("v", 25))
	if strings.Contains(got, strings.Repeat("v", 25)) || (!strings.Contains(got, "k=%3Credacted%3E") && !strings.Contains(got, "k=<redacted>")) {
		t.Fatalf("query value of 25 redacted: %q", got)
	}
	// userinfo 凭据不得外泄(即使 URL 无 query)
	got = redactMCPURL("https://alice:sk-live-0123456789abcdef@mcp.example.com/mcp")
	if strings.Contains(got, "sk-live") || strings.Contains(got, "alice") {
		t.Fatalf("userinfo credentials must be masked: %q", got)
	}
	if !strings.Contains(got, "%3Credacted%3E") && !strings.Contains(got, "<redacted>") {
		t.Fatalf("userinfo replaced with marker: %q", got)
	}
	// fragment 一律丢弃,凭据不得借 fragment 外泄
	got = redactMCPURL("https://mcp.example.com/mcp#access_token=sk-frag-0123456789abcdef")
	if strings.Contains(got, "sk-frag") || strings.Contains(got, "access_token") || strings.Contains(got, "#") {
		t.Fatalf("fragment must be dropped: %q", got)
	}
}

func TestTruncateMCPName(t *testing.T) {
	// 多字节截断必须 rune 安全:35 个「中」=105 字节,按字节切会劈出无效 UTF-8
	name := truncateMCPName(strings.Repeat("中", 35))
	if utf8.RuneCountInString(name) > 64 {
		t.Fatalf("name capped at 64 runes, got %d", utf8.RuneCountInString(name))
	}
	if !utf8.ValidString(name) {
		t.Fatalf("truncated name must be valid utf8: %q", name)
	}
	// 上限必须真实触发:70 个「中」恰好截到 64 runes,仍为有效 UTF-8
	long := truncateMCPName(strings.Repeat("中", 70))
	if n := utf8.RuneCountInString(long); n != 64 {
		t.Fatalf("name must be capped at exactly 64 runes, got %d", n)
	}
	if !utf8.ValidString(long) {
		t.Fatalf("truncated name must be valid utf8: %q", long)
	}
}

func TestAgentMCPServersFromClaudeRaw(t *testing.T) {
	raw := map[string]interface{}{
		"b-http": map[string]interface{}{"type": "http", "url": "https://x.example/mcp?token=" + strings.Repeat("t", 30)},
		"a-stdio": map[string]interface{}{
			"type":    "stdio",
			"command": "uvx",
			"args":    []interface{}{"--from", "git+https://example/serena", strings.Repeat("s", 40)},
		},
		"no-cmd":  map[string]interface{}{"type": "stdio"},
		"weird":   map[string]interface{}{"type": "websocket", "url": "wss://x"},
		"not-map": "oops",
	}
	out := agentMCPServersFromClaudeRaw(raw, "user")
	if len(out) != 2 {
		t.Fatalf("want 2 valid entries (sorted a-stdio,b-http), got %d: %+v", len(out), out)
	}
	// 名字按字典序,保证输出确定
	if out[0].Name != "a-stdio" || out[1].Name != "b-http" {
		t.Fatalf("want sorted names, got %+v", out)
	}
	if out[0].Transport != "stdio" || !strings.HasPrefix(out[0].Command, "uvx --from") {
		t.Fatalf("stdio entry wrong: %+v", out[0])
	}
	if strings.Contains(out[0].Command, strings.Repeat("s", 40)) {
		t.Fatalf("long arg token must be redacted: %q", out[0].Command)
	}
	if out[1].Transport != "http" || !strings.HasPrefix(out[1].URL, "https://x.example/mcp") {
		t.Fatalf("http entry wrong: %+v", out[1])
	}
	if strings.Contains(out[1].URL, strings.Repeat("t", 30)) {
		t.Fatalf("url query must be redacted: %q", out[1].URL)
	}
	for _, s := range out {
		if s.Source != "user" {
			t.Fatalf("source tag wrong: %+v", s)
		}
	}
}

func TestClaudeMCPServerInventoryIn(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(
		`{"mcpServers":{"context7":{"type":"http","url":"https://mcp.context7/mcp"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// 启用一个插件 + 禁用一个插件(装了但 enabledPlugins 没开 → 不采)
	pluginsDir := filepath.Join(home, ".claude", "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "installed_plugins.json"), []byte(`{
		"plugins":{"superplugin@market":[{"scope":"user","installPath":"`+filepath.Join(home, "plug-super")+`"}],
		           "sleepyplugin@market":[{"scope":"user","installPath":"`+filepath.Join(home, "plug-sleepy")+`"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(
		`{"enabledPlugins":{"superplugin@market":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for dir, body := range map[string]string{
		"plug-super":  `{"mcpServers":{"super-srv":{"command":"npx","args":["-y","super-mcp"]}}}`,
		"plug-sleepy": `{"mcpServers":{"sleepy-srv":{"command":"npx","args":["sleepy"]}}}`,
	} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, dir, ".mcp.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, stamps := claudeMCPServerInventoryIn(home)
	if len(out) != 2 {
		t.Fatalf("want user context7 + plugin super-srv, got %+v", out)
	}
	if out[0].Name != "context7" || out[0].Source != "user" {
		t.Fatalf("user entry wrong: %+v", out[0])
	}
	if out[1].Name != "plugin:superplugin:super-srv" || out[1].Source != "plugin" {
		t.Fatalf("plugin entry wrong: %+v", out[1])
	}
	// stamps 必须覆盖全部被读文件:settings/registry/claude.json + 已启用插件 .mcp.json
	if len(stamps) != 4 {
		t.Fatalf("want 4 stamps (claude.json+settings+registry+enabled plugin decl), got %d", len(stamps))
	}
}

func TestClaudeMCPServerInventoryInMissingHome(t *testing.T) {
	out, _ := claudeMCPServerInventoryIn(filepath.Join(t.TempDir(), "nonexistent"))
	if out != nil {
		t.Fatalf("empty home must yield nil, got %+v", out)
	}
}
