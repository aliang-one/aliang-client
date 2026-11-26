package config

import (
	"strings"
	"testing"
)

// TestLLMEndpointValidate 用表驱动钉住 Validate 的拒绝/放行面,重点覆盖
// 评审要求的负向:双协议全 0 / 非法 URL / builtin+base_url / models 形状。
func TestLLMEndpointValidate(t *testing.T) {
	base := func() *LLMEndpoint {
		return &LLMEndpoint{ID: "v1", Name: "v", BaseURL: "https://v.example.com", APIKey: "sk-v",
			ProtocolAnthropic: true}
	}
	cases := []struct {
		name    string
		mutate  func(e *LLMEndpoint)
		wantErr string // 空 = 应通过
	}{
		{"合法基础行", func(e *LLMEndpoint) {}, ""},
		{"models 空数组", func(e *LLMEndpoint) { e.Models = "[]" }, ""},
		{"models 字符串数组", func(e *LLMEndpoint) { e.Models = `["m1"]` }, ""},
		{"models 非 JSON", func(e *LLMEndpoint) { e.Models = "not json" }, "models"},
		{"models 合法 JSON 但非数组", func(e *LLMEndpoint) { e.Models = `{"a":1}` }, "models"},
		{"双协议全 0", func(e *LLMEndpoint) { e.ProtocolAnthropic = false }, "protocol"},
		{"非法 URL", func(e *LLMEndpoint) { e.BaseURL = "not-a-url" }, "base_url"},
		{"builtin 带 base_url", func(e *LLMEndpoint) { e.Builtin = true }, "builtin"},
	}
	for _, tc := range cases {
		e := base()
		tc.mutate(e)
		err := e.Validate()
		if tc.wantErr == "" {
			if err != nil {
				t.Fatalf("%s: want pass, got %v", tc.name, err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("%s: want error containing %q, got nil", tc.name, tc.wantErr)
		}
		if !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("%s: want error containing %q, got %q", tc.name, tc.wantErr, err)
		}
	}
}
