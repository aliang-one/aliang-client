package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEffectiveAgentCoreMaxIdenticalToolTurns：打转守卫阈值的读取语义——
// nil/未配置/负数一律 0（内核按默认值兜底），正数原样，越界钳到 [0,50]。
func TestEffectiveAgentCoreMaxIdenticalToolTurns(t *testing.T) {
	cases := []struct {
		name string
		cfg  *Config
		want int
	}{
		{"空 Config", &Config{}, 0},
		{"Customer 为 nil", &Config{Customer: nil}, 0},
		{"agentcore 层为 nil", &Config{Customer: &CustomerConfig{}}, 0},
		{"未配置走默认", &Config{Customer: &CustomerConfig{AgentCore: &AgentCoreTuningConfig{}}}, 0},
		{"显式 5 原样", &Config{Customer: &CustomerConfig{AgentCore: &AgentCoreTuningConfig{MaxIdenticalToolTurns: 5}}}, 5},
		{"显式 3 原样(覆盖路径)", &Config{Customer: &CustomerConfig{AgentCore: &AgentCoreTuningConfig{MaxIdenticalToolTurns: 3}}}, 3},
		{"负数回落默认", &Config{Customer: &CustomerConfig{AgentCore: &AgentCoreTuningConfig{MaxIdenticalToolTurns: -1}}}, 0},
		{"越界上钳 50", &Config{Customer: &CustomerConfig{AgentCore: &AgentCoreTuningConfig{MaxIdenticalToolTurns: 999}}}, 50},
	}
	for _, tc := range cases {
		if got := tc.cfg.EffectiveAgentCoreMaxIdenticalToolTurns(); got != tc.want {
			t.Errorf("%s: EffectiveAgentCoreMaxIdenticalToolTurns = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestAgentCoreTuningValidate：保存通道校验——0 合法(=默认),1..50 合法,
// 负数/超 50 拒绝。
func TestAgentCoreTuningValidate(t *testing.T) {
	for _, v := range []int{0, 1, 5, 50} {
		if err := (&AgentCoreTuningConfig{MaxIdenticalToolTurns: v}).Validate(); err != nil {
			t.Errorf("MaxIdenticalToolTurns=%d 应合法, got %v", v, err)
		}
	}
	for _, v := range []int{-1, 51, 100} {
		err := (&AgentCoreTuningConfig{MaxIdenticalToolTurns: v}).Validate()
		if err == nil {
			t.Errorf("MaxIdenticalToolTurns=%d 应被拒绝", v)
			continue
		}
		if !strings.Contains(err.Error(), "max_identical_tool_turns") {
			t.Errorf("错误应点名字段, got %v", err)
		}
	}
	if err := (*AgentCoreTuningConfig)(nil).Validate(); err != nil {
		t.Errorf("nil 接收者应通过(上层 nil-guard), got %v", err)
	}
}

// TestAgentCoreTuningJSONRoundTrip：线格式键是 max_identical_tool_turns
// (snake_case);发错键会被 encoding/json 静默丢弃——这是「保存了但不生效」
// 的经典来源,锁死。
func TestAgentCoreTuningJSONRoundTrip(t *testing.T) {
	raw := []byte(`{"customer":{"agentcore":{"max_identical_tool_turns":7}}}`)
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Customer == nil || cfg.Customer.AgentCore == nil {
		t.Fatal("customer.agentcore 层没解析出来")
	}
	if got := cfg.Customer.AgentCore.MaxIdenticalToolTurns; got != 7 {
		t.Errorf("MaxIdenticalToolTurns = %d, want 7", got)
	}
	out, err := json.Marshal(&Config{Customer: &CustomerConfig{AgentCore: &AgentCoreTuningConfig{MaxIdenticalToolTurns: 7}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"max_identical_tool_turns":7`) {
		t.Errorf("线格式键必须是 max_identical_tool_turns, got %s", out)
	}
	// 0 用 omitempty 序列化掉——「未配置」与「显式 0」在线上等价,都是默认。
	out, err = json.Marshal(&Config{Customer: &CustomerConfig{AgentCore: &AgentCoreTuningConfig{}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "max_identical_tool_turns") {
		t.Errorf("0 不应出现在线载荷(omitempty), got %s", out)
	}
}

// TestCustomerUnknownKeyAcceptsAgentcore：白名单收编 customer.agentcore——
// 既有的未知键检测不许把它当入侵者拒掉(改一处必改三处的回归钉)。
func TestCustomerUnknownKeyAcceptsAgentcore(t *testing.T) {
	root := map[string]json.RawMessage{
		"customer": json.RawMessage(`{"agentcore":{"max_identical_tool_turns":5}}`),
	}
	unknown, _, err := extractCustomerUnknownFields(root)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(unknown) != 0 {
		t.Errorf("customer.agentcore 被当成未知键: %v", unknown)
	}

	bad := map[string]json.RawMessage{
		"customer": json.RawMessage(`{"agentcore_typo":{}}`),
	}
	unknown, _, err = extractCustomerUnknownFields(bad)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(unknown) == 0 {
		t.Fatal("未知键必须仍被收集")
	}
}
