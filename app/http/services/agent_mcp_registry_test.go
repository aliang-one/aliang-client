package services

// agent_mcp_registry_test.go 统一 MCP 注册表(~/.aliang/mcp.json)文件层单测。
import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRegistryFile(t *testing.T, home, content string) {
	t.Helper()
	path := filepath.Join(home, ".aliang", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadAliangMCPRegistryIntoMergesEnabledEntries(t *testing.T) {
	home := t.TempDir()
	writeRegistryFile(t, home, `{
	  "version": 1,
	  "servers": {
	    "blender": {"type":"stdio","command":"/usr/bin/uvx","args":["blender-mcp"],"enabled":true},
	    "off":     {"type":"stdio","command":"x","enabled":false},
	    "weather": {"type":"http","url":"https://api.example.com/mcp"},
	    "bad":     {"type":"stdio"},
	    "future":  {"type":"weird","command":"x"}
	  }
	}`)
	dst := map[string]interface{}{"legacy": map[string]interface{}{"command": "keep"}}
	got := readAliangMCPRegistryInto(home, dst)
	if got != 2 {
		t.Fatalf("merged count = %d, want 2 (blender+weather)", got)
	}
	if _, ok := dst["off"]; ok {
		t.Fatal("enabled=false 条目不得并入")
	}
	if _, ok := dst["bad"]; ok {
		t.Fatal("缺 command 的 stdio 条目不得并入")
	}
	if _, ok := dst["future"]; ok {
		t.Fatal("未知 transport 条目不得并入")
	}
	b, ok := dst["blender"].(map[string]interface{})
	if !ok || b["command"] != "/usr/bin/uvx" {
		t.Fatalf("blender 条目形态不对: %#v", dst["blender"])
	}
	if _, has := b["enabled"]; has {
		t.Fatal("并入 claude 形态时必须剥掉注册表专属 enabled 键")
	}
	if _, ok := dst["legacy"]; !ok {
		t.Fatal("dst 既有键不得被破坏")
	}
}

func TestReadAliangMCPRegistryIntoRegistryWinsOverLegacy(t *testing.T) {
	home := t.TempDir()
	writeRegistryFile(t, home, `{"version":1,"servers":{"dup":{"command":"from-registry"}}}`)
	dst := map[string]interface{}{"dup": map[string]interface{}{"command": "from-claude"}}
	if got := readAliangMCPRegistryInto(home, dst); got != 1 {
		t.Fatalf("count = %d, want 1", got)
	}
	if m := dst["dup"].(map[string]interface{}); m["command"] != "from-registry" {
		t.Fatalf("注册表同名键必须覆盖遗留源, got %#v", dst["dup"])
	}
}

func TestReadAliangMCPRegistryIntoToleratesMissingAndBroken(t *testing.T) {
	home := t.TempDir() // 无文件
	dst := map[string]interface{}{}
	if got := readAliangMCPRegistryInto(home, dst); got != 0 || len(dst) != 0 {
		t.Fatalf("缺文件必须零并入, got %d %v", got, dst)
	}
	writeRegistryFile(t, home, `not json`)
	if got := readAliangMCPRegistryInto(home, dst); got != 0 {
		t.Fatalf("坏文件必须零并入(容忍不降级), got %d", got)
	}
	// version 前向兼容:未知更高版本照读,不拒读不迁移
	writeRegistryFile(t, home, `{"version":9,"servers":{"a":{"command":"x"}}}`)
	if got := readAliangMCPRegistryInto(home, map[string]interface{}{}); got != 1 {
		t.Fatalf("高 version 必须前向兼容读取, got %d", got)
	}
}

func TestReadAliangMCPRegistryIntoGuardClauses(t *testing.T) {
	// 双护栏:home 空串 / dst nil 各返回 0 且不 panic
	if got := readAliangMCPRegistryInto("", map[string]interface{}{}); got != 0 {
		t.Fatalf("home 空串护栏失效, got %d", got)
	}
	if got := readAliangMCPRegistryInto(t.TempDir(), nil); got != 0 {
		t.Fatalf("dst nil 护栏失效, got %d", got)
	}
}

func TestReadAliangMCPRegistryIntoSkipsReservedAndMalformed(t *testing.T) {
	home := t.TempDir()
	writeRegistryFile(t, home, `{
	  "version": 1,
	  "servers": {
	    "aliang-peer":      {"type":"stdio","command":"shadow"},
	    "aliang-mcp-ghost": {"type":"stdio","command":"shadow2"},
	    "junk":             "text",
	    "strfalse":         {"type":"stdio","command":"x","enabled":"false"},
	    "good":             {"type":"stdio","command":"y"}
	  }
	}`)
	dst := map[string]interface{}{}
	if got := readAliangMCPRegistryInto(home, dst); got != 1 {
		t.Fatalf("merged count = %d, want 1 (仅 good)", got)
	}
	for _, name := range []string{"aliang-peer", "aliang-mcp-ghost", "junk", "strfalse"} {
		if _, ok := dst[name]; ok {
			t.Fatalf("条目 %s 不得并入", name)
		}
	}
	if m, ok := dst["good"].(map[string]interface{}); !ok || m["command"] != "y" {
		t.Fatalf("good 条目形态不对: %#v", dst["good"])
	}
}

func TestAliangMCPRegistryPathUnderHome(t *testing.T) {
	p := aliangMCPRegistryPath("/h")
	if !strings.HasSuffix(filepath.ToSlash(p), "/h/.aliang/mcp.json") {
		t.Fatalf("path = %s", p)
	}
}

func TestUpsertAliangMCPServerValidates(t *testing.T) {
	home := t.TempDir()
	cases := []struct {
		name   string
		server string
		entry  aliangMCPServerEntry
	}{
		{"bad-name", "bad name!", aliangMCPServerEntry{Command: "x"}},
		{"reserved", "aliang-evil", aliangMCPServerEntry{Command: "x"}},
		{"stdio-no-command", "a", aliangMCPServerEntry{}},
		{"http-no-url", "a", aliangMCPServerEntry{Type: "http"}},
		{"bad-type", "a", aliangMCPServerEntry{Type: "weird", Command: "x"}},
	}
	for _, c := range cases {
		if err := upsertAliangMCPServerForTest(home, c.server, c.entry); err == nil {
			t.Fatalf("%s 必须被拒绝", c.name)
		}
	}
}

func TestUpsertDeleteRoundTripPreservesUnknowns(t *testing.T) {
	home := t.TempDir()
	writeRegistryFile(t, home, `{"version":1,"custom_top":"keepme","servers":{
	  "old":{"command":"keep-old","future_field":{"n":42}}}}`)
	disabled := false
	if err := upsertAliangMCPServerForTest(home, "blender", aliangMCPServerEntry{
		Type: "stdio", Command: "/usr/bin/uvx", Args: []string{"blender-mcp"},
		Env: map[string]string{"K": "v"}, Enabled: &disabled,
	}); err != nil {
		t.Fatal(err)
	}
	root, existed, err := loadAliangMCPRegistryRoot(home)
	if err != nil || !existed {
		t.Fatalf("load: existed=%v err=%v", existed, err)
	}
	if root["custom_top"] != "keepme" {
		t.Fatalf("未知顶层键必须保留, got %#v", root["custom_top"])
	}
	servers := root["servers"].(map[string]interface{})
	old := servers["old"].(map[string]interface{})
	if old["future_field"] == nil {
		t.Fatal("未知 server 字段必须保留")
	}
	// 数字字面量:UseNumber 往返后 42 仍是 42(json.Number)
	if ff, ok := old["future_field"].(map[string]interface{}); !ok || ff["n"] != json.Number("42") {
		t.Fatalf("数字字面量必须保真, got %#v", old["future_field"])
	}
	if servers["blender"] == nil {
		t.Fatal("blender 未写入")
	}
	// enabled=false 显式落盘
	if b, _ := servers["blender"].(map[string]interface{})["enabled"].(bool); b {
		t.Fatal("显式 false 必须落盘")
	}
	// 删除
	existed, err = deleteAliangMCPServerForTest(home, "blender")
	if err != nil || !existed {
		t.Fatalf("delete: existed=%v err=%v", existed, err)
	}
	if existed, _ = func() (bool, error) { return deleteAliangMCPServerForTest(home, "blender") }(); existed {
		t.Fatal("二次删除必须报不存在")
	}
}

func TestUpsertIdempotentNoRewrite(t *testing.T) {
	home := t.TempDir()
	e := aliangMCPServerEntry{Command: "x"}
	if err := upsertAliangMCPServerForTest(home, "a", e); err != nil {
		t.Fatal(err)
	}
	p := aliangMCPRegistryPath(home)
	st1 := stampAgentMCPFile(p)
	if err := upsertAliangMCPServerForTest(home, "a", e); err != nil {
		t.Fatal(err)
	}
	if stampAgentMCPFile(p) != st1 {
		t.Fatal("同值重写必须幂等跳写(mtime 不变)")
	}
}

func TestUpsertCreatesMissingFile(t *testing.T) {
	home := t.TempDir()
	if err := upsertAliangMCPServerForTest(home, "a", aliangMCPServerEntry{Command: "x"}); err != nil {
		t.Fatal(err)
	}
	root, existed, err := loadAliangMCPRegistryRoot(home)
	if err != nil || !existed {
		t.Fatalf("首次写必须建文件: existed=%v err=%v", existed, err)
	}
	if v, ok := root["version"].(json.Number); !ok || v.String() != "1" {
		t.Fatalf("首建文件必须带 version=1, got %#v", root["version"])
	}
}

func TestUpsertCapExceededRejected(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < aliangMCPServerMaxCount; i++ {
		name := "sa" + fmt.Sprint(i) // 合法名 sa0..sa23
		if err := upsertAliangMCPServerForTest(home, name, aliangMCPServerEntry{Command: "x"}); err != nil {
			t.Fatalf("第 %d 条不应拒绝: %v", i, err)
		}
	}
	if err := upsertAliangMCPServerForTest(home, "overflow", aliangMCPServerEntry{Command: "x"}); err == nil {
		t.Fatal("第 25 条必须被拒(cap=24)")
	}
	// 已有条目的更新不受 cap 影响(替换语义;sa0 为循环写入的既有条目)
	if err := upsertAliangMCPServerForTest(home, "sa0", aliangMCPServerEntry{Command: "y"}); err != nil {
		t.Fatalf("替换既有条目不应受 cap 拦: %v", err)
	}
}

func TestRegistryRoundTripKeepsAngleBytes(t *testing.T) {
	// spec §10:SetEscapeHTML(false)——用户内容里的 <>& 不得被改写成 < 等
	home := t.TempDir()
	if err := upsertAliangMCPServerForTest(home, "esc", aliangMCPServerEntry{
		Command: `echo "<b>&</b>"`,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(aliangMCPRegistryPath(home))
	if err != nil {
		t.Fatal(err)
	}
	// 判别式用裸形态:SetEscapeHTML 开启时会产出 <b>&;引号本身
	// 会被 JSON 转义成 \" 属正常行为,不作为判别对象(评审第 3 轮实证)。
	if !strings.Contains(string(raw), `<b>&</b>`) {
		t.Fatalf("转义语义违例(HTML 转义不该发生): %s", raw)
	}
}
