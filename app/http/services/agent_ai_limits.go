package services

import "strings"

const (
	agentAIContextWindowMin     = 100_000
	agentAIContextWindowMax     = 1_000_000
	agentAIContextWindowDefault = 200_000
)

// clampAgentAIContextWindow normalizes a client-supplied context window.
// 0/negative means "unset" and is preserved so callers can tell "older server
// sent nothing" (CLI default) apart from an explicit choice.

func clampAgentAIContextWindow(v int) int {
	if v <= 0 {
		return 0
	}
	if v < agentAIContextWindowMin {
		return agentAIContextWindowMin
	}
	if v > agentAIContextWindowMax {
		return agentAIContextWindowMax
	}
	return v
}

// agentAIMaxTokensMin/Max bound the per-conversation output cap.
//
// 下限取 1024：再低连一个中等大小的工具调用参数都写不完，只会制造
// stop_reason=max_tokens 的截断（内核见到被截断的 tool_use 会整批丢弃）。
// 上限取 128k：超过它没有已知模型能兑现，且会把「小模型」的 400 降档重试
// 从 1 轮拖成多轮。0 表示「未设置」——内核用 defaultMaxTokens（64000）。

const (
	agentAIMaxTokensMin = 1_024
	agentAIMaxTokensMax = 128_000
)

// parseAgentAIDisabledMCPServers 解析会话级 MCP 开关。
//
// 第二个返回值区分「键缺失」（不动既有设置）与「显式空数组」（全部启用）——
// 这两者在协议上是不同意图，合并成一个 nil 会让用户无法重新打开全部 server。

func parseAgentAIDisabledMCPServers(msg map[string]interface{}) ([]string, bool) {
	if msg == nil {
		return nil, false
	}
	if _, ok := msg["disabled_mcp_servers"]; !ok {
		return nil, false
	}
	raw := remoteStringSlice(msg, "disabled_mcp_servers")
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, name := range raw {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out, true
}

// parseAgentAIDisabledSkills 解析会话级技能开关，语义与
// parseAgentAIDisabledMCPServers 完全一致（缺键=不动、空数组=全部启用）。

func parseAgentAIDisabledSkills(msg map[string]interface{}) ([]string, bool) {
	if msg == nil {
		return nil, false
	}
	if _, ok := msg["disabled_skills"]; !ok {
		return nil, false
	}
	return normalizeAgentAINameList(remoteStringSlice(msg, "disabled_skills")), true
}

// normalizeAgentAINameList 去空白、去重、保序。

func normalizeAgentAINameList(raw []string) []string {
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, name := range raw {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// clampAgentAIMaxTokens 归一化客户端下发的输出上限。0/负数表示未设置并
// 原样保留（区分「旧客户端没发」与「显式选择」）。

func clampAgentAIMaxTokens(v int) int {
	if v <= 0 {
		return 0
	}
	if v < agentAIMaxTokensMin {
		return agentAIMaxTokensMin
	}
	if v > agentAIMaxTokensMax {
		return agentAIMaxTokensMax
	}
	return v
}
