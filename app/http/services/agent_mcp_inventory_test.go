package services

import (
	"encoding/json"
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
