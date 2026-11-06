package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

// initialize 响应的 instructions 字段(2026-11-05 感知缺口修补):MCP 协议的
// 服务端级说明,Claude Code 注入模型上下文——LLM 由此获得「设备舰队 + 先查
// device_list」的路由意识。缺失即回退到纯工具描述联想(不够明显,见面谈)。
func TestPeerMCPInitializeResultCarriesInstructions(t *testing.T) {
	res := peerMCPInitializeResult("2024-11-05")
	if res["protocolVersion"] != "2024-11-05" {
		t.Fatalf("协议版本应原样回显, got %v", res["protocolVersion"])
	}
	instructions, ok := res["instructions"].(string)
	if !ok || strings.TrimSpace(instructions) == "" {
		t.Fatal("initialize 响应缺 instructions 字段")
	}
	for _, want := range []string{"device_list", "peer_exec", "peer_fetch_file", "another device"} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("instructions 应包含 %q, got: %s", want, instructions)
		}
	}
	// serverInfo 形状不回归
	si, _ := res["serverInfo"].(map[string]interface{})
	if si == nil || si["name"] != "aliang-peer" {
		t.Fatalf("serverInfo 形状回归: %v", res["serverInfo"])
	}
	// JSON 可序列化(协议面健全性)
	if _, err := json.Marshal(res); err != nil {
		t.Fatalf("marshal: %v", err)
	}
}
