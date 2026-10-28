package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEffectiveAgentCoreLLM(t *testing.T) {
	if got := (&Config{}).EffectiveAgentCoreLLM(); got != nil {
		t.Errorf("空 Config → %v, want nil", got)
	}
	if got := (&Config{Customer: &CustomerConfig{}}).EffectiveAgentCoreLLM(); got != nil {
		t.Errorf("无字段 → %v, want nil", got)
	}
	if got := (&Config{Customer: &CustomerConfig{AgentCoreLLM: &AgentCoreLLMConfig{}}}).EffectiveAgentCoreLLM(); got != nil {
		t.Errorf("未启用 → %v, want nil", got)
	}
	if got := (&Config{Customer: &CustomerConfig{AgentCoreLLM: &AgentCoreLLMConfig{Enabled: true}}}).EffectiveAgentCoreLLM(); got != nil {
		t.Errorf("启用但 URL 空 → %v, want nil(整层关闭)", got)
	}
	cfg := &Config{Customer: &CustomerConfig{AgentCoreLLM: &AgentCoreLLMConfig{
		Enabled: true, BaseURL: " https://gw.example.com ", APIKey: "sk-x",
	}}}
	got := cfg.EffectiveAgentCoreLLM()
	if got == nil || got.BaseURL != "https://gw.example.com" {
		t.Errorf("→ %+v, want trim 后的配置副本", got)
	}
	// 返回副本:改副本不影响原配置
	got.BaseURL = " mutated "
	if cfg.Customer.AgentCoreLLM.BaseURL != " https://gw.example.com " {
		t.Error("EffectiveAgentCoreLLM 必须返回副本,不能暴露内部指针")
	}
}

// 读取端防御(spec §3.1):手改配置文件绕过保存校验时,非法形态整层关闭+Warn,
// 绝不把坏配置放进读取链(否则复刻「远端缺 key 静默 403」的谜团)。
func TestEffectiveAgentCoreLLMRejectsInvalid(t *testing.T) {
	noKey := &Config{Customer: &CustomerConfig{AgentCoreLLM: &AgentCoreLLMConfig{
		Enabled: true, BaseURL: "https://gw.example.com", // 远端+无 key:非法
	}}}
	if got := noKey.EffectiveAgentCoreLLM(); got != nil {
		t.Errorf("远端缺 key 应整层关闭, got %+v", got)
	}
	malformed := &Config{Customer: &CustomerConfig{AgentCoreLLM: &AgentCoreLLMConfig{
		Enabled: true, BaseURL: "not a url",
	}}}
	if got := malformed.EffectiveAgentCoreLLM(); got != nil {
		t.Errorf("URL 畸形应整层关闭, got %+v", got)
	}
}

func TestAgentCoreLLMValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *AgentCoreLLMConfig
		wantErr string // 空串 = 期望通过
	}{
		{"nil 不拦", nil, ""},
		{"未启用不拦", &AgentCoreLLMConfig{}, ""},
		{"启用+URL 空", &AgentCoreLLMConfig{Enabled: true}, "base_url"},
		{"启用+URL 畸形", &AgentCoreLLMConfig{Enabled: true, BaseURL: "not a url"}, "base_url"},
		{"启用+回环+无 key 合法", &AgentCoreLLMConfig{Enabled: true, BaseURL: "http://127.0.0.1:8082"}, ""},
		{"启用+localhost+无 key 合法", &AgentCoreLLMConfig{Enabled: true, BaseURL: "http://localhost:8082"}, ""},
		{"启用+远端+无 key 拒", &AgentCoreLLMConfig{Enabled: true, BaseURL: "https://gw.example.com"}, "api_key"},
		{"启用+远端+有 key 合法", &AgentCoreLLMConfig{Enabled: true, BaseURL: "https://gw.example.com", APIKey: "sk-x"}, ""},
	}
	for _, tc := range cases {
		err := tc.cfg.Validate()
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: 未期望错误 %v", tc.name, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("%s: got %v, want 含 %q", tc.name, err, tc.wantErr)
		}
	}
}

// 白名单 switch(types.go extractCustomerUnknownFields)必须放行 agentcore_llm,
// 否则加载即报 "customer.agentcore_llm is forbidden"。
func TestCustomerUnknownKeyAllowsAgentCoreLLM(t *testing.T) {
	root := map[string]json.RawMessage{
		"customer": json.RawMessage(`{"agentcore_llm":{"enabled":true,"base_url":"http://127.0.0.1:8082"}}`),
	}
	unknown, _, err := extractCustomerUnknownFields(root)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(unknown) != 0 {
		t.Errorf("agentcore_llm 被当成未知键: %v", unknown)
	}
}
