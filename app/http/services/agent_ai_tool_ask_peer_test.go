package services

import (
	"testing"
)

// D1(spec 2026-11-02):本桥 MCP 名 ↔ agentcore 点号名映射表断言(防三面
// 漂移)。stdio 面的对应断言在 cmd/mcp_test.go(stdio 后三个无 peer_ 前缀
// 是历史现状,勿「修正」)。
func TestAgentPeerMCPBridgeToolNamesMapAgentcoreNames(t *testing.T) {
	mapping := map[string]string{
		"peer_exec":        AgentPeerToolEventExec,
		"peer_fetch_file":  AgentPeerToolEventFetchFile,
		"peer_device_info": AgentPeerToolEventDeviceInfo,
		"peer_device_list": AgentPeerToolEventDeviceList,
		"peer_device_note": AgentPeerToolEventDeviceNote,
	}
	defs := agentPeerMCPBridgeTools()
	if len(defs) != len(mapping) {
		t.Fatalf("bridge has %d tools, mapping has %d", len(defs), len(mapping))
	}
	seen := make(map[string]bool, len(defs))
	for _, def := range defs {
		seen[def.name] = true
		if _, ok := mapping[def.name]; !ok {
			t.Fatalf("unexpected bridge tool %q", def.name)
		}
		if def.description == "" || len(def.inputSchema) == 0 {
			t.Fatalf("tool %q missing description/schema", def.name)
		}
	}
	mgr := newAgentAIManager()
	coreNames := make(map[string]bool)
	for _, tool := range mgr.agentCorePeerTools(agentAIRun{}) {
		coreNames[tool.Name()] = true
	}
	if len(coreNames) != len(mapping) {
		t.Fatalf("agentcore bridge has %d peer tools, mapping has %d (mapping stale)", len(coreNames), len(mapping))
	}
	for bridgeName, coreName := range mapping {
		if !seen[bridgeName] {
			t.Fatalf("bridge missing tool %q", bridgeName)
		}
		if !coreNames[coreName] {
			t.Fatalf("agentcore bridge missing tool %q (mapping stale)", coreName)
		}
	}
}

// peerMCPText 结果语义:payload 带 error → (text,true);否则 result 文本。
func TestAgentPeerMCPTextFromPayload(t *testing.T) {
	if text, isErr := peerMCPText(map[string]interface{}{"result": "ok"}); isErr || text != "ok" {
		t.Fatalf("got %q %v", text, isErr)
	}
	if text, isErr := peerMCPText(map[string]interface{}{"error": "boom"}); !isErr || text != "boom" {
		t.Fatalf("got %q %v", text, isErr)
	}
}
