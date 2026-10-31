package services

// 会话级输出预算与 MCP 开关的归一化/解析契约（2026-10-31 设置 IA 重整）。
//
// 这几条是跨端协议面：手机/仪表盘下发 → 这里 clamp → 内核消费。写死的边界
// 一旦漂移，前端 clamp（ChatPage.commitMaxTokens）与后端会各说各话，用户看到
// 「我填了 500 却被跑成 1024」这种无从解释的差异。

import (
	"reflect"
	"testing"
)

func TestClampAgentAIMaxTokens(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, 0},       // 未设置：保留 0，内核用 defaultMaxTokens
		{-1, 0},      // 负数同样视为未设置
		{1, 1024},    // 低于下限 → 抬到 1024
		{1024, 1024}, // 边界
		{32000, 32000},
		{128000, 128000}, // 上边界
		{999999, 128000}, // 超上限 → 压到 128000
	}
	for _, c := range cases {
		if got := clampAgentAIMaxTokens(c.in); got != c.want {
			t.Errorf("clampAgentAIMaxTokens(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseAgentAIDisabledMCPServers(t *testing.T) {
	t.Run("缺键表示不动既有设置", func(t *testing.T) {
		got, set := parseAgentAIDisabledMCPServers(map[string]interface{}{"model": "x"})
		if set {
			t.Fatal("缺键时 set 必须为 false")
		}
		if got != nil {
			t.Fatalf("缺键时返回 %v, want nil", got)
		}
	})

	t.Run("显式空数组表示全部启用", func(t *testing.T) {
		got, set := parseAgentAIDisabledMCPServers(map[string]interface{}{
			"disabled_mcp_servers": []interface{}{},
		})
		if !set {
			t.Fatal("显式空数组时 set 必须为 true")
		}
		if len(got) != 0 {
			t.Fatalf("返回 %v, want 空切片", got)
		}
	})

	t.Run("trim 去空去重且保序", func(t *testing.T) {
		got, set := parseAgentAIDisabledMCPServers(map[string]interface{}{
			"disabled_mcp_servers": []interface{}{" a ", "", "b", "a", 42},
		})
		if !set {
			t.Fatal("set 必须为 true")
		}
		want := []string{"a", "b", "42"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("返回 %v, want %v", got, want)
		}
	})

	t.Run("nil map 安全", func(t *testing.T) {
		got, set := parseAgentAIDisabledMCPServers(nil)
		if set || got != nil {
			t.Fatalf("nil map 应返回 (nil,false)，实得 (%v,%v)", got, set)
		}
	})
}
