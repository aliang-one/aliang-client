package services

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func mustJSON(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	var v map[string]interface{}
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestMergeQuickSetupJSON(t *testing.T) {
	existing := mustJSON(t, `{"theme":"dark","mcp":{"fs":{"command":"x"}},"provider":{"old":{"npm":"@g/old"}}}`)
	incoming := mustJSON(t, `{"$schema":"https://opencode.ai/config.json","model":"aliang/main","provider":{"aliang":{"npm":"@g/aliang","options":{"baseURL":"https://api.aliang.one/v1"}}}}`)

	merged, ok := mergeQuickSetupJSONObjects(existing, incoming)
	if !ok {
		t.Fatal("merge should succeed")
	}
	out, _ := json.Marshal(merged)
	s := string(out)
	for _, want := range []string{`"theme":"dark"`, `"fs"`, `"old"`, `"aliang/main"`, `"baseURL":"https://api.aliang.one/v1"`, `"https://opencode.ai/config.json"`, `"npm":"@g/aliang"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s in %s", want, s)
		}
	}
}

func TestMergeQuickSetupJSONPreservesAuthTokens(t *testing.T) {
	// codex auth.json：只动 OPENAI_API_KEY，保住 ChatGPT 登录态（spec §7）
	existing := mustJSON(t, `{"OPENAI_API_KEY":null,"tokens":{"access_token":"at","account_id":"acc"},"last_refresh":"2026-01-01"}`)
	incoming := mustJSON(t, `{"OPENAI_API_KEY":"sk-aliang"}`)
	merged, ok := mergeQuickSetupJSONObjects(existing, incoming)
	if !ok {
		t.Fatal("merge should succeed")
	}
	out, _ := json.Marshal(merged)
	if !strings.Contains(string(out), `"access_token":"at"`) {
		t.Fatalf("tokens lost: %s", out)
	}
	if !strings.Contains(string(out), `"OPENAI_API_KEY":"sk-aliang"`) {
		t.Fatalf("OPENAI_API_KEY not injected: %s", out)
	}
}

func TestMergeQuickSetupJSONNilExisting(t *testing.T) {
	if _, ok := mergeQuickSetupJSONObjects(nil, mustJSON(t, `{"a":1}`)); !ok {
		t.Fatal("nil existing should merge cleanly")
	}
}

func TestMergeCodexTOML(t *testing.T) {
	existing := "# my codex config\nmodel = \"gpt-4o\"\nmodel_provider = \"openai\"\n\n[model_providers.openai]\nname = \"OpenAI\"\nbase_url = \"https://api.openai.com/v1\"\nwire_api = \"responses\"\n\n# user section\n[mcp_servers.fs]\ncommand = \"uvx\"\n"
	got, err := mergeCodexTOML(existing, "gpt-5.4", "https://api.aliang.one/v1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`model = "gpt-5.4"`, `model_provider = "aliang"`,
		"[model_providers.aliang]", `base_url = "https://api.aliang.one/v1"`,
		"# my codex config", // 顶部注释保留
		"[mcp_servers.fs]",  // 用户其他段保留
		"command = \"uvx\"",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	// 用户自建的其他 provider 表必须原样保留（spec §7.2 惰性残留可接受）；
	// 只是 model_provider 改指 aliang 后不再被引用
	if !strings.Contains(got, "[model_providers.openai]") {
		t.Fatal("user's own table must be preserved")
	}
	if !strings.Contains(got, `model_provider = "aliang"`) {
		t.Fatal("provider switch missing")
	}
}

func TestMergeCodexTOML_AppendsWhenNoTables(t *testing.T) {
	got, err := mergeCodexTOML("", "gpt-5.4", "http://127.0.0.1:56432/v1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "[model_providers.aliang]") {
		t.Fatal("section missing")
	}
}

func TestMergeCodexTOML_MultilineStringSafe(t *testing.T) {
	existing := "instructions = \"\"\"\n[model_providers.aliang]\nnot = \"a table\"\n\"\"\"\n"
	got, err := mergeCodexTOML(existing, "m", "http://x/v1")
	if err != nil {
		t.Fatal(err)
	}
	// 多行字符串内的伪 table 行不得被当作我们的段
	if !strings.Contains(got, "not = \"a table\"") {
		t.Fatalf("multiline content damaged:\n%s", got)
	}
}

func TestMergeCodexTOML_OutputParses(t *testing.T) {
	existing := "model = \"x\"\n[model_providers.openai]\nname=\"o\"\n[[array_of_tables]]\nkey=\"v\"\n"
	got, err := mergeCodexTOML(existing, "m", "http://x/v1")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]interface{}
	if err := toml.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("output not valid TOML: %v\n%s", err, got)
	}
}

// 对抗性检查：行内 table 值含 ]、带引号顶层键、行尾注释、表内同名 model 键。
func TestMergeCodexTOML_AdversarialValues(t *testing.T) {
	existing := "model = \"gpt-4o\" # user picks this\n" +
		"\"model_provider\" = \"old\"\n" +
		"inline = { name = \"a]b\" } # inline table, not a header\n" +
		"list = [{ name = \"x]\" }, { name = \"y\" }]\n" +
		"[profile.fast]\n" +
		"model = \"mini\"\n" // 表内 model 属于该表，必须保留
	got, err := mergeCodexTOML(existing, "m", "http://x/v1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`inline = { name = "a]b" } # inline table, not a header`,
		`list = [{ name = "x]" }, { name = "y" }]`,
		"[profile.fast]",
		`model = "mini"`, // 表内的不动
		`model = "m"`,    // 顶层的重写
		`model_provider = "aliang"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	var v map[string]interface{}
	if err := toml.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("output not valid TOML: %v\n%s", err, got)
	}
}

// 对抗性检查：已有 aliang 段原位替换——旧键值/用户私加行清空，紧随的用户段保留且不粘连。
func TestMergeCodexTOML_ReplacesAliangInPlace(t *testing.T) {
	existing := "[model_providers.aliang]\n" +
		"name = \"Old\"\n" +
		"base_url = \"http://old/v1\"\n" +
		"custom = 1\n" +
		"\n" +
		"[mcp_servers.fs]\n" +
		"command = \"uvx\"\n"
	got, err := mergeCodexTOML(existing, "m", "http://new/v1")
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{`"Old"`, "http://old", "custom = 1"} {
		if strings.Contains(got, gone) {
			t.Fatalf("stale %q survived:\n%s", gone, got)
		}
	}
	for _, want := range []string{"[model_providers.aliang]", "http://new/v1", "[mcp_servers.fs]", `command = "uvx"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if err := toml.Unmarshal([]byte(got), &map[string]interface{}{}); err != nil {
		t.Fatalf("output not valid TOML: %v\n%s", err, got)
	}
}

// 对抗性检查：CRLF 文件——保留行不动，我们插入的行跟随 CRLF，输出可解析。
func TestMergeCodexTOML_CRLF(t *testing.T) {
	existing := "model = \"old\"\r\nmodel_provider = \"openai\"\r\n\r\n[model_providers.openai]\r\nname = \"o\"\r\n"
	got, err := mergeCodexTOML(existing, "m", "http://x/v1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "\r\n") < 4 {
		t.Fatalf("CRLF endings not preserved:\n%q", got)
	}
	if err := toml.Unmarshal([]byte(got), &map[string]interface{}{}); err != nil {
		t.Fatalf("output not valid TOML: %v\n%s", err, got)
	}
}
