package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// url.Parse 失败(userinfo 非法转义、fragment 非法转义、userinfo 未转义空白
// 等形态)时必须 fail-closed:无法可靠定位 userinfo/query/fragment 边界的 URL
// 整串替换为固定 <redacted-url>,凭据不得借畸形 URL 绕过脱敏原样上报。
func TestRedactMCPURLParseFailure(t *testing.T) {
	for _, in := range []string{
		"https://alice:s3cret%zz@host/mcp",   // userinfo 非法转义
		"https://host/m#access_token=abc%zz", // fragment 非法转义
		"https://user:my pass@host/mcp",      // userinfo 未转义空白
	} {
		if got := redactMCPURL(in); got != "<redacted-url>" {
			t.Fatalf("parse-failure url %q must collapse to <redacted-url>, got %q", in, got)
		}
	}
}

// 凭据形态的 Authorization/api-key header 参数必须定向打码:保留「header 名 +
// 已知 scheme 词」前缀,凭据主体替换为 <redacted:N>——不受「含空白豁免」与
// 32 长度阈值约束(含空白的 Bearer 参数正是真实泄漏形态)。
func TestRedactMCPAuthHeaderForms(t *testing.T) {
	tok34 := strings.Repeat("t", 34)
	cases := []struct{ name, in, want string }{
		{"bearer 形态", "Authorization: Bearer " + tok34, "Authorization: Bearer <redacted:34>"},
		{"--header= 单元素形态", "--header=Authorization: Bearer " + tok34, "--header=Authorization: Bearer <redacted:34>"},
		{"大小写不敏感", "authorization: bearer " + tok34, "authorization: bearer <redacted:34>"},
		{"Proxy-Authorization", "Proxy-Authorization: Basic " + tok34, "Proxy-Authorization: Basic <redacted:34>"},
		{"短凭据也打码(不受 32 阈值约束)", "Authorization: Bearer shortkey9", "Authorization: Bearer <redacted:9>"},
		{"api-key 无 scheme 语义:冒号后整体打码", "X-Api-Key: sk-live-0123456789abcdef", "X-Api-Key: <redacted:24>"},
		{"未知 scheme 视为凭据整体打码", "Authorization: ntn_" + tok34, "Authorization: <redacted:38>"},
		{"冒号后无内容:原样", "Authorization:", "Authorization:"},
		{"自然语句仍放行", "this is a natural language sentence for humans", "this is a natural language sentence for humans"},
	}
	for _, c := range cases {
		if got := redactMCPToken(c.in); got != c.want {
			t.Fatalf("%s: redactMCPToken(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
	// 端到端:mcp-remote 认证配置形态(bearer 在独立 argv 元素里)不得泄漏
	cmd := redactMCPCommand([]string{"npx", "-y", "mcp-remote", "--header", "Authorization: Bearer " + tok34})
	if strings.Contains(cmd, tok34) {
		t.Fatalf("bearer credential must not survive redactMCPCommand: %q", cmd)
	}
	if !strings.Contains(cmd, "Authorization: Bearer <redacted:34>") {
		t.Fatalf("header shape must be preserved with redacted body: %q", cmd)
	}
}

// Unicode 安全:锚点定位不得拿 strings.ToLower 结果的字节下标切原串——
// ToLower 对个别 rune 会改变字节长度(Ⱥ U+023A 2→3 字节扩张、İ 2→1 收缩),
// 下标错位轻则劈烂前缀、重则切片越界 panic 打死 hello 链路(无 recover)。
func TestRedactMCPAuthHeaderUnicodeSafety(t *testing.T) {
	tok34 := strings.Repeat("t", 34)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("must not panic: %v", r)
		}
	}()
	// ToLower 扩张形态:lower 里的锚点下标 > 原串对应位置
	growing := strings.Repeat("Ⱥ", 9) + "Authorization: Bearer " + tok34
	if got := redactMCPToken(growing); strings.Contains(got, tok34) {
		t.Fatalf("growing-rune prefix must still redact: %q", got)
	}
	// ToLower 收缩形态:下标前移,前缀被劈烂
	shrinking := strings.Repeat("İ", 3) + "Authorization: Bearer " + tok34
	got := redactMCPToken(shrinking)
	if strings.Contains(got, tok34) {
		t.Fatalf("shrinking-rune prefix must still redact: %q", got)
	}
	if !strings.Contains(got, "Authorization: Bearer <redacted:34>") {
		t.Fatalf("header prefix must stay intact, got %q", got)
	}
}

// 名字与冒号之间的空白/引号形态同样拦截:「Authorization :」「Authorization\t:」
// 与 --header={"Authorization": "…"} JSON 形态都是形似凭据 header,不得因
// 冒号未紧跟名字而漏网;名字后跟其他字符(authorizations:)不构成锚点。
func TestRedactMCPAuthHeaderLooseColon(t *testing.T) {
	tok34 := strings.Repeat("t", 34)
	if got := redactMCPToken("Authorization : Bearer " + tok34); strings.Contains(got, tok34) {
		t.Fatalf("space-before-colon must be caught: %q", got)
	}
	if got := redactMCPToken("Authorization\t: Bearer " + tok34); strings.Contains(got, tok34) {
		t.Fatalf("tab-before-colon must be caught: %q", got)
	}
	jsonForm := `--header={"Authorization": "Bearer ` + tok34 + `"}`
	if got := redactMCPToken(jsonForm); strings.Contains(got, tok34) {
		t.Fatalf("json header form must be caught: %q", got)
	}
	if got := redactMCPToken("authorizations are: " + tok34); !strings.Contains(got, tok34) {
		t.Fatalf("non-header word must pass through generic rule: %q", got)
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

func TestTruncateMCPUTF16Semantics(t *testing.T) {
	// 星面字符:1 rune = 2 UTF-16 码元。服务端 zod max 按 JS length(码元)计,
	// 客户端截断必须按同一计量,否则贴限的 emoji 名字/命令会被服务端整条拒绝。
	emojiName := strings.Repeat("🎉", 40) // 40 runes = 80 码元
	got := truncateMCPName(emojiName)
	if n := utf8.RuneCountInString(got); n > 32 { // 64 码元 ÷ 2
		t.Fatalf("emoji name must fit 64 UTF-16 units, got %d runes", n)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncated name must stay valid UTF-8")
	}
	// ASCII 行为不变(码元==rune==字节)
	if got := truncateMCPName(strings.Repeat("a", 70)); len(got) != 64 {
		t.Fatalf("ascii cap unchanged, got %d", len(got))
	}
	// 命令行同理:512 码元上限。无空白长 token 会先被打码(<redacted:N>,
	// 天然短)——再补一条含空白的长参数(不打码)真实触达整体截断路径。
	emojiArg := strings.Repeat("🎉", 300) // 300 runes = 600 码元
	cmd := redactMCPCommand([]string{"x", emojiArg})
	if units := utf16Length(cmd); units > 512 {
		t.Fatalf("command must fit 512 UTF-16 units, got %d", units)
	}
	spacy := strings.Repeat("🎉 ", 300) // 600 码元 emoji + 300 空白,不打码
	cmd2 := redactMCPCommand([]string{"x", spacy})
	if units := utf16Length(cmd2); units > 512 {
		t.Fatalf("spacy emoji command must fit 512 UTF-16 units, got %d", units)
	}
	if !utf8.ValidString(cmd2) {
		t.Fatalf("truncated command must stay valid UTF-8")
	}
}

// utf16Length 与服务端 JS string length 同计量:星面字符按 2 计。
func utf16Length(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
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
	// "   "(全空白名)条目必须被丢弃:截断后空名跳过,不得产出条目
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(
		`{"mcpServers":{"context7":{"type":"http","url":"https://mcp.context7/mcp"},"   ":{"command":"ghost"}}}`), 0o644); err != nil {
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

// 两态契约:即使 home 下无任何 claude 配置,stamps 也必须照交(全缺失指纹),
// 否则 Task 6 的缓存会把空清单永久缓存,「配置后来出现」永远感知不到。
func TestClaudeMCPServerInventoryInMissingHome(t *testing.T) {
	out, stamps := claudeMCPServerInventoryIn(filepath.Join(t.TempDir(), "nonexistent"))
	if out != nil {
		t.Fatalf("empty home must yield nil, got %+v", out)
	}
	if len(stamps) != 3 {
		t.Fatalf("missing config must still yield 3 stamps, got %d: %+v", len(stamps), stamps)
	}
	for _, s := range stamps {
		if s.exists {
			t.Fatalf("all stamps must be exists=false, got %+v", s)
		}
	}
}

// 存在但空的配置 → 空清单(nil)但仍有指纹(stamps 覆盖被统计的文件)。
func TestClaudeMCPServerInventoryInEmptyConfig(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, stamps := claudeMCPServerInventoryIn(home)
	if out != nil {
		t.Fatalf("empty mcpServers must yield nil, got %+v", out)
	}
	if len(stamps) != 3 {
		t.Fatalf("want 3 stamps, got %d: %+v", len(stamps), stamps)
	}
}

// 畸形 .claude.json fail-soft:清单归 nil,但 stamps 照交(缓存据此感知
// 文件后来被修好)。
func TestClaudeMCPServerInventoryInMalformedJSON(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, stamps := claudeMCPServerInventoryIn(home)
	if out != nil {
		t.Fatalf("malformed claude.json must fail soft, got %+v", out)
	}
	if len(stamps) != 3 {
		t.Fatalf("stamps must still be reported, got %d", len(stamps))
	}
}

func TestCodexMCPServerInventoryIn(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	configTOML := `
[mcp_servers.serena]
command = "uvx"
args = ["--from", "git+https://example/serena", "serena", "start-mcp-server"]
env = { SECRET_TOKEN = "` + strings.Repeat("x", 40) + `" }

[mcp_servers.webmcp]
url = "https://mcp.example/stream"

[mcp_servers.broken]
url = ""
`
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(configTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	out, stamps := codexMCPServerInventoryIn(home)
	if len(out) != 2 {
		t.Fatalf("want serena+webmcp, got %+v", out)
	}
	if out[0].Name != "serena" || out[0].Transport != "stdio" {
		t.Fatalf("serena wrong: %+v", out[0])
	}
	if !strings.HasPrefix(out[0].Command, "uvx --from") {
		t.Fatalf("serena command wrong: %q", out[0].Command)
	}
	if strings.Contains(out[0].Command, strings.Repeat("x", 40)) {
		t.Fatalf("env 值绝不能出现在 command: %q", out[0].Command)
	}
	if out[1].Name != "webmcp" || out[1].Transport != "http" || out[1].URL != "https://mcp.example/stream" {
		t.Fatalf("webmcp wrong: %+v", out[1])
	}
	if len(stamps) != 1 {
		t.Fatalf("want 1 stamp, got %d", len(stamps))
	}
	// 缺文件也要交指纹(out==nil 且 stamps len==1),否则缓存永驻空结果
	missingOut, missingStamps := codexMCPServerInventoryIn(t.TempDir())
	if missingOut != nil || len(missingStamps) != 1 {
		t.Fatalf("missing config must yield nil + 1 stamp, got %+v %+v", missingOut, missingStamps)
	}
}

func TestCodexMCPServerInventoryInMalformed(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("not [ valid toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := codexMCPServerInventoryIn(home)
	if out != nil {
		t.Fatalf("malformed toml must fail soft, got %+v", out)
	}
}

// codex 官方 enabled 布尔(enabled=false = 临时停用而不删配置,codex 自身
// 不会启动该服务器):停用条目不是可用设备能力,不得上报。
func TestCodexMCPServerInventoryInEnabled(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	configTOML := `
[mcp_servers.aaa-disabled]
command = "x"
enabled = false

[mcp_servers.bbb-default-on]
command = "y"

[mcp_servers.ccc-disabled-url]
url = "https://h/m"
enabled = false

[mcp_servers.ddd-explicit-on]
command = "z"
enabled = true
`
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(configTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := codexMCPServerInventoryIn(home)
	if len(out) != 2 {
		t.Fatalf("want only enabled entries, got %+v", out)
	}
	if out[0].Name != "bbb-default-on" || out[1].Name != "ddd-explicit-on" {
		t.Fatalf("enabled entries wrong: %+v", out)
	}
}

// 停用条目不得占用 mcpServerMaxCount 额度:5 个字典序在前的停用条目 + 24 个
// 启用条目,正确行为是 24 个启用条目全部上报(停用项在额度判断前跳过)。
func TestCodexMCPServerInventoryInEnabledDoesNotConsumeCap(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&b, "[mcp_servers.a-disabled-%d]\ncommand = \"x\"\nenabled = false\n\n", i)
	}
	for i := 0; i < 24; i++ {
		fmt.Fprintf(&b, "[mcp_servers.b-on-%02d]\ncommand = \"y\"\n\n", i)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := codexMCPServerInventoryIn(home)
	if len(out) != 24 {
		t.Fatalf("24 enabled entries must all survive, got %d", len(out))
	}
	for _, srv := range out {
		if strings.Contains(srv.Name, "disabled") {
			t.Fatalf("disabled entry leaked into report: %+v", srv)
		}
	}
}

// spec §6 数量上限:超过 mcpServerMaxCount 的合法条目恰截到 24。
func TestAgentMCPServersFromClaudeRawCountCap(t *testing.T) {
	raw := map[string]interface{}{}
	for i := 0; i < 30; i++ {
		raw[fmt.Sprintf("srv-%02d", i)] = map[string]interface{}{"command": "npx"}
	}
	out := agentMCPServersFromClaudeRaw(raw, "user")
	if len(out) != 24 {
		t.Fatalf("want exactly 24 entries, got %d", len(out))
	}
	// 截断保留字典序前 24 个:srv-00..srv-23
	if out[0].Name != "srv-00" || out[23].Name != "srv-23" {
		t.Fatalf("cap must keep lexicographically first 24, got %s..%s", out[0].Name, out[23].Name)
	}
}

func TestCachedAgentMCPServersInvalidation(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.WriteFile(configPath, []byte("[mcp_servers.a]\ncommand = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	parseCalls := 0
	parse := func(h string) ([]models.AgentMCPServer, []agentMCPFileStamp) {
		parseCalls++
		return codexMCPServerInventoryIn(h)
	}
	first := cachedAgentMCPServers(home, "codex", parse)
	second := cachedAgentMCPServers(home, "codex", parse)
	if parseCalls != 1 {
		t.Fatalf("unchanged files must hit cache, parseCalls=%d", parseCalls)
	}
	if len(first) != 1 || len(second) != 1 || first[0].Name != "a" {
		t.Fatalf("cached results wrong: %+v %+v", first, second)
	}
	// 文件变更 → 缓存失效重解析
	if err := os.WriteFile(configPath, []byte("[mcp_servers.b]\ncommand = \"y\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// mtime 粒度问题:确保 mtime 变化
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(configPath, future, future); err != nil {
		t.Fatal(err)
	}
	third := cachedAgentMCPServers(home, "codex", parse)
	if parseCalls != 2 {
		t.Fatalf("changed file must invalidate cache, parseCalls=%d", parseCalls)
	}
	if third[0].Name != "b" {
		t.Fatalf("fresh parse result wrong: %+v", third)
	}
	// 命中返回副本:改写返回值不得污染缓存
	if len(third) > 0 {
		third[0].Name = "mutated"
		fourth := cachedAgentMCPServers(home, "codex", parse)
		if fourth[0].Name != "b" {
			t.Fatalf("cache must be isolated from returned slice, got %+v", fourth[0])
		}
		if parseCalls != 2 {
			t.Fatalf("mutation must not trigger reparse, parseCalls=%d", parseCalls)
		}
	}
}

func TestAgentMCPServersForDispatch(t *testing.T) {
	// 非采集对象一律 nil(不触达文件系统)
	for _, id := range []string{"opencode", "pi", "gemini", "", "  "} {
		if got := agentMCPServersFor(id); got != nil {
			t.Fatalf("tool %q must yield nil, got %+v", id, got)
		}
	}
}

// carry-forward(Task 5 评审):锁定 codex 解码语义。
func TestCodexMCPServerInventoryInDecodeSemantics(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	// (a) 单条目类型不匹配(args 混入数字)→ 整文件不可解码 → 归零 + stamp 照交
	//     (codex 自身 serde 强类型同样无法加载,语义对齐)
	bad := "[mcp_servers.good]\ncommand = \"x\"\n\n[mcp_servers.bad]\ncommand = \"y\"\nargs = [\"--flag\", 42]\n"
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	out, stamps := codexMCPServerInventoryIn(home)
	if out != nil || len(stamps) != 1 {
		t.Fatalf("mixed-type args must zero the file but keep stamp, got %+v %+v", out, stamps)
	}
	// (b) env 值为数组等非字符串形态 → 容忍,条目正常上报
	good := "[mcp_servers.k]\ncommand = \"x\"\nenv = { K = [1, 2] }\n"
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	out2, _ := codexMCPServerInventoryIn(home)
	if len(out2) != 1 || out2[0].Name != "k" {
		t.Fatalf("tolerant env must keep entry, got %+v", out2)
	}
	// (c) 24 上限(codex 侧)
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&sb, "[mcp_servers.s%02d]\ncommand = \"c\"\n", i)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out3, _ := codexMCPServerInventoryIn(home)
	if len(out3) != 24 || out3[0].Name != "s00" {
		t.Fatalf("codex cap wrong: %d entries, first=%+v", len(out3), out3[0])
	}
}

// carry-forward(Task 4 评审):user-first 溢出时插件条目被外层截断——
// 验证 claudeMCPServerInventoryIn 的外层 out[:mcpServerMaxCount] 分支:
// user 恰 24 条合法 + 插件 2 条 → 恰 24 条且全是 user 源。
func TestClaudeMCPServerInventoryInOuterTruncation(t *testing.T) {
	home := t.TempDir()
	var sb strings.Builder
	sb.WriteString(`{"mcpServers":{`)
	for i := 0; i < 24; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `"a%02d":{"command":"npx"}`, i)
	}
	sb.WriteString(`}}`)
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	// 启用一个插件(其 .mcp.json 含 2 个条目)
	pluginsDir := filepath.Join(home, ".claude", "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "installed_plugins.json"), []byte(`{
	"plugins":{"capplugin@market":[{"scope":"user","installPath":"`+filepath.Join(home, "plug-cap")+`"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(
		`{"enabledPlugins":{"capplugin@market":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "plug-cap"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "plug-cap", ".mcp.json"), []byte(
		`{"mcpServers":{"cap-one":{"command":"npx"},"cap-two":{"command":"npx"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := claudeMCPServerInventoryIn(home)
	if len(out) != 24 {
		t.Fatalf("want exactly 24 entries after outer truncation, got %d", len(out))
	}
	for _, s := range out {
		if s.Source != "user" {
			t.Fatalf("plugin entries must be truncated away by user-first overflow, got %+v", s)
		}
	}
	if out[0].Name != "a00" || out[23].Name != "a23" {
		t.Fatalf("outer truncation must keep a00..a23, got %s..%s", out[0].Name, out[23].Name)
	}
}

func TestDetectAgentToolsWithFillsMCPServers(t *testing.T) {
	fake := map[string][]models.AgentMCPServer{
		// claudecode 单独映射:接线循环按 defs[i].ID 原样调 mcpInventoryFor,
		// 「别名同源」的归属在 agentMCPServersFor 的 switch 里,fake 绕过它。
		"claude":     {{Name: "ctx", Transport: "http", URL: "https://x"}},
		"claudecode": {{Name: "ctx", Transport: "http", URL: "https://x"}},
		"codex":      {{Name: "serena", Transport: "stdio", Command: "uvx"}},
	}
	orig := mcpInventoryFor
	mcpInventoryFor = func(toolID string) []models.AgentMCPServer { return fake[toolID] }
	t.Cleanup(func() { mcpInventoryFor = orig })

	tools := detectAgentToolsWith(func(cmd string) (string, error) {
		return "/fake/bin/" + cmd, nil
	})
	byID := map[string]models.AgentTool{}
	for _, tool := range tools {
		byID[tool.ID] = tool
	}
	if len(byID["claude"].MCPServers) != 1 {
		t.Fatalf("claude must carry mcp list")
	}
	if len(byID["claudecode"].MCPServers) != 1 {
		t.Fatalf("claudecode alias must share claude list")
	}
	if len(byID["codex"].MCPServers) != 1 {
		t.Fatalf("codex must carry mcp list")
	}
	for _, id := range []string{"opencode", "pi", "gemini"} {
		if byID[id].MCPServers != nil {
			t.Fatalf("%s must not carry mcp list", id)
		}
	}
}

func TestDetectAgentToolsWithSkipsUnavailableMCPServers(t *testing.T) {
	orig := mcpInventoryFor
	mcpInventoryFor = func(toolID string) []models.AgentMCPServer {
		return []models.AgentMCPServer{{Name: "x", Transport: "stdio", Command: "x"}}
	}
	t.Cleanup(func() { mcpInventoryFor = orig })

	// 无任何 CLI 可用 → 不采集 MCP(能力=CLI 存在才有意义)
	tools := detectAgentToolsWith(func(cmd string) (string, error) {
		return "", fmt.Errorf("not found")
	})
	for _, tool := range tools {
		if tool.MCPServers != nil {
			t.Fatalf("unavailable tool %s must not carry mcp list", tool.ID)
		}
	}
}

func TestAgentMCPServersForAliasSharesClaudeSlot(t *testing.T) {
	origHome := mcpInventoryHome
	origInv := mcpInventoryFor
	t.Cleanup(func() { mcpInventoryHome = origHome; mcpInventoryFor = origInv })

	// 注意:agentMCPServersFor 内部直接调 cachedAgentMCPServers+InventoryIn,
	// 不经 mcpInventoryFor;此处用 mcpInventoryHome 指到临时 home,
	// 让 claude/claudecode 真实走同一个 claude 缓存槽。
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(
		`{"mcpServers":{"ctx":{"type":"http","url":"https://x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mcpInventoryHome = func() string { return home }

	viaClaude := agentMCPServersFor("claude")
	viaAlias := agentMCPServersFor("claudecode")
	if len(viaClaude) != 1 || len(viaAlias) != 1 {
		t.Fatalf("claude & claudecode must both resolve, got %+v / %+v", viaClaude, viaAlias)
	}
	if viaAlias[0].Name != "ctx" {
		t.Fatalf("alias must read same claude config, got %+v", viaAlias[0])
	}
}
