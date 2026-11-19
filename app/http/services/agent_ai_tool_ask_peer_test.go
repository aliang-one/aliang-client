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
	// 「故意无 CLI 面」显式在册(与 cmd/mcp_test.go 的 noBridgeFace 同款双向
	// 守卫):mesh 诊断读面四件(mesh.*,读面批 2026-11-20)只在 agentcore 面
	// +stdio 面双面存在,本桥刻意不注入。agentcore 面总数必须恰好等于
	// 「有 bridge 面 5 件 + 故意无 bridge 面 4 件」——将来给 agentcore 面加
	// 第 10 个工具而忘配 CLI 面又忘入册,这里直接红,不许静默漏面。
	noBridgeFace := map[string]bool{
		"mesh.readiness": true, "mesh.identity": true,
		"mesh.stun_probe": true, "mesh.lan_candidates": true,
	}
	if len(coreNames) != len(mapping)+len(noBridgeFace) {
		t.Fatalf("agentcore bridge has %d peer tools, want mapping %d + no-bridge-face %d (mapping stale)",
			len(coreNames), len(mapping), len(noBridgeFace))
	}
	for coreName := range noBridgeFace {
		if !coreNames[coreName] {
			t.Fatalf("agentcore bridge missing no-bridge-face tool %q (registry stale)", coreName)
		}
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
