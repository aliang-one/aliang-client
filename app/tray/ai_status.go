package tray

import (
	"fmt"
	"strings"

	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/statistic"
)

const maxAIProviders = 4

// AIProviderStatus describes the tray display state for a single AI provider.
type AIProviderStatus struct {
	Key      string
	Label    string
	Enabled  bool
	Detected bool
	Active   bool
}

// FormatAIStatusTitle returns the tray menu title for a provider.
// Returns empty string if the provider is not enabled.
func FormatAIStatusTitle(p AIProviderStatus) string {
	if !p.Enabled {
		return ""
	}
	if p.Active || p.Detected {
		return fmt.Sprintf("\u2705 %s", p.Label)
	}
	return fmt.Sprintf("\u23f3 %s", p.Label)
}

// BuildAIStatusFromTracker builds provider statuses by inspecting the in-process
// AI activity tracker and the global config. Used by the direct-mode TrayApp.
func BuildAIStatusFromTracker() []AIProviderStatus {
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil || len(cfg.Customer.AIRules) == 0 {
		return nil
	}

	aiSummary := statistic.GetDefaultAIActivityTracker().Summary()

	// Build a set of provider keys that have recent traffic.
	trafficSet := make(map[string]*statistic.AIActivityDetection, len(aiSummary.RecentProviderTraffic))
	for _, d := range aiSummary.RecentProviderTraffic {
		trafficSet[config.NormalizeAIProviderKey(d.ProviderKey)] = d
	}

	var result []AIProviderStatus
	for _, preset := range config.PresetAIRuleProviders {
		providerKey := config.NormalizeAIProviderKey(preset.Key)
		rule, exists := config.FindCustomerAIRule(cfg.Customer.AIRules, providerKey)
		if !exists || rule == nil || rule.Enble == nil || !*rule.Enble {
			continue
		}

		label := rule.Label
		if label == "" {
			label = preset.Label
		}

		detected := false
		active := false
		if d, ok := trafficSet[providerKey]; ok {
			detected = true
			active = d.Active
		}

		result = append(result, AIProviderStatus{
			Key:      providerKey,
			Label:    label,
			Enabled:  true,
			Detected: detected,
			Active:   active,
		})
	}
	return result
}

// BuildAIStatusFromIPCData extracts provider statuses from the IPC response's
// ai_status field. Used by the companion apps.
func BuildAIStatusFromIPCData(data map[string]interface{}) []AIProviderStatus {
	raw, ok := data["ai_status"]
	if !ok || raw == nil {
		return nil
	}

	items, ok := raw.([]interface{})
	if !ok || len(items) == 0 {
		return nil
	}

	result := make([]AIProviderStatus, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		key, _ := m["key"].(string)
		label, _ := m["label"].(string)
		enabled, _ := m["enabled"].(bool)
		detected, _ := m["detected"].(bool)
		active, _ := m["active"].(bool)
		if !enabled {
			continue
		}
		result = append(result, AIProviderStatus{
			Key:      key,
			Label:    label,
			Enabled:  enabled,
			Detected: detected,
			Active:   active,
		})
	}
	return result
}

func trayIconProviderKey(key string) string {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "vscode", "copilot", "githubcopilot", "github-copilot":
		return "vscode"
	case "cursor":
		return "cursor"
	case "openai", "chatgpt":
		return "openai"
	case "anthropic", "claude":
		return "anthropic"
	default:
		return ""
	}
}
