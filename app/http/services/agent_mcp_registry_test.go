package services

// agent_mcp_registry_test.go 统一 MCP 注册表(~/.aliang/mcp.json)文件层单测。
import (
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

func TestAliangMCPRegistryPathUnderHome(t *testing.T) {
	p := aliangMCPRegistryPath("/h")
	if !strings.HasSuffix(filepath.ToSlash(p), "/h/.aliang/mcp.json") {
		t.Fatalf("path = %s", p)
	}
}
