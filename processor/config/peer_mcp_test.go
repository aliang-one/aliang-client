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

// 三目标子键(nil=跟随总开关)× 总开关合取矩阵。UI 可达态之外还要覆盖
// 「master 关压制子 true」——安全件语义:总开关关=三面全关。
func TestEffectivePeerMCPSubTargets(t *testing.T) {
	b := func(v bool) *bool { return &v }
	cases := []struct {
		name          string
		master, sub   *bool
		want          bool
	}{
		{"全缺省", nil, nil, true},
		{"master关_子true被压制", b(false), b(true), false},
		{"master开_子nil跟随", b(true), nil, true},
		{"master开_子false", b(true), b(false), false},
		{"master缺省_子false", nil, b(false), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{Customer: &CustomerConfig{PeerMCP: &PeerMCPConfig{
				Enable:           tc.master,
				AgentcoreEnabled: tc.sub,
				ClaudeEnabled:    tc.sub,
				CodexEnabled:     tc.sub,
			}}}
			if got := c.EffectivePeerMCPAgentCoreEnabled(); got != tc.want {
				t.Fatalf("agentcore = %v, want %v", got, tc.want)
			}
			if got := c.EffectivePeerMCPClaudeEnabled(); got != tc.want {
				t.Fatalf("claude = %v, want %v", got, tc.want)
			}
			if got := c.EffectivePeerMCPCodexEnabled(); got != tc.want {
				t.Fatalf("codex = %v, want %v", got, tc.want)
			}
		})
	}
}

// nil PeerMCP / nil Customer 防御:三读点与 master 同款默认真。
func TestEffectivePeerMCPSubTargetsNilDefense(t *testing.T) {
	var c Config
	if !c.EffectivePeerMCPAgentCoreEnabled() || !c.EffectivePeerMCPClaudeEnabled() || !c.EffectivePeerMCPCodexEnabled() {
		t.Fatal("nil Config 三读点应默认开启")
	}
	c.Customer = &CustomerConfig{}
	if !c.EffectivePeerMCPAgentCoreEnabled() || !c.EffectivePeerMCPClaudeEnabled() || !c.EffectivePeerMCPCodexEnabled() {
		t.Fatal("PeerMCP 未配置三读点应默认开启")
	}
}
