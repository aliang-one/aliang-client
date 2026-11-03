package config

import (
	"encoding/json"
	"testing"
)

// peer_mcp 三态:nil=默认开(对齐 agentcore peer 注入现状,升级不回退)、
// true/false 显式。加载/保存白名单两处都必须放行该键,否则保存 400。
func TestEffectivePeerMCPEnabledDefaults(t *testing.T) {
	var c Config
	if !c.EffectivePeerMCPEnabled() {
		t.Fatal("nil Config 应默认开启")
	}
	c.Customer = &CustomerConfig{}
	if !c.EffectivePeerMCPEnabled() {
		t.Fatal("PeerMCP 未配置应默认开启")
	}
	on := true
	c.Customer.PeerMCP = &PeerMCPConfig{Enable: &on}
	if !c.EffectivePeerMCPEnabled() {
		t.Fatal("显式 true 应开启")
	}
	off := false
	c.Customer.PeerMCP = &PeerMCPConfig{Enable: &off}
	if c.EffectivePeerMCPEnabled() {
		t.Fatal("显式 false 应关闭")
	}
}

func TestPeerMCPKeyAcceptedOnLoad(t *testing.T) {
	// fixture 必须带合法 core 段:Config.Validate 过完 customer 白名单后会
	// 无条件校验 core.api_server 必填,缺 core 段会以
	// 「core.api_server is required」无关错误失败,误导排查方向。
	raw := `{"core":{"api_server":"https://api.example.com"},"customer":{"peer_mcp":{"enable":false}}}`
	var c Config
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("peer_mcp 应被加载白名单放行, got: %v", err)
	}
	if c.EffectivePeerMCPEnabled() {
		t.Fatal("显式 false 应关闭")
	}
}
