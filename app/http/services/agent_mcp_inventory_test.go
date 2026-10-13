package services

import (
	"encoding/json"
	"strings"
	"testing"

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
}
