package config

// 多通道竞速纯逻辑单测(裁决/候选解析/override 钩子);探测循环不起 goroutine。

import (
	"fmt"
	"testing"
)

func TestPickAgentServer(t *testing.T) {
	probes := func(items ...interface{}) []agentRaceProbe {
		out := make([]agentRaceProbe, 0, len(items)/2)
		for i := 0; i+1 < len(items); i += 2 {
			base := items[i].(string)
			ttfb := int64(-1)
			if v, ok := items[i+1].(int); ok && v >= 0 {
				ttfb = int64(v)
			}
			out = append(out, agentRaceProbe{base: base, ttfbMs: ttfb})
		}
		return out
	}
	cases := []struct {
		name      string
		probes    []agentRaceProbe
		incumbent string
		want      string
	}{
		{"最快者胜", probes("a", 500, "b", 200), "a", "b"},
		{"30%滞回护现任:挑战者仅快16%", probes("a", 1200, "b", 1003), "a", "a"},
		{"挑战者快过70%夺位", probes("a", 1000, "b", 600), "a", "b"},
		{"现任探败滞回失效直接让位", probes("a", -1, "b", 900), "a", "b"},
		{"全败保持粘滞", probes("a", -1, "b", -1), "a", ""},
		{"现任为空取最快", probes("a", 800, "b", 300), "", "b"},
		{"平手取先见序", probes("a", 300, "b", 300), "", "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pickAgentServer(tc.probes, tc.incumbent)
			if got != tc.want {
				t.Fatalf("want %q got %q (probes=%v incumbent=%s)", tc.want, got, tc.probes, tc.incumbent)
			}
		})
	}
}

func TestAgentServerCandidates(t *testing.T) {
	cfg := &Config{Core: &CoreConfig{AliangServer: &AliangServerConfig{
		AgentServer:  "https://a.example",
		AgentServers: []string{"https://b.example/", "https://a.example", " https://c.example ", ""},
	}}}
	got := cfg.AgentServerCandidates()
	want := []string{"https://b.example", "https://a.example", "https://c.example"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("candidates: want %v got %v", want, got)
	}
	// 无数组:回落单值链(agent_server)。
	cfg2 := &Config{Core: &CoreConfig{AliangServer: &AliangServerConfig{AgentServer: "https://solo.example"}}}
	got2 := cfg2.AgentServerCandidates()
	if fmt.Sprint(got2) != fmt.Sprint([]string{"https://solo.example"}) {
		t.Fatalf("solo: got %v", got2)
	}
}

func TestAgentServerOverrideHook(t *testing.T) {
	defer SetAgentServerOverride("")
	cfg := &Config{Core: &CoreConfig{AliangServer: &AliangServerConfig{AgentServer: "https://cfg.example"}}}
	if got := cfg.AgentBaseURL(); got != "https://cfg.example" {
		t.Fatalf("pre-override: got %q", got)
	}
	SetAgentServerOverride("https://winner.example/")
	if got := cfg.AgentBaseURL(); got != "https://winner.example" {
		t.Fatalf("post-override: got %q(应去尾斜杠)", got)
	}
	SetAgentServerOverride("")
	if got := cfg.AgentBaseURL(); got != "https://cfg.example" {
		t.Fatalf("cleared: got %q", got)
	}
}
