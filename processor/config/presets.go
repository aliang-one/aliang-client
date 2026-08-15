package config

import (
	"encoding/json"
	"strings"
)

// PresetAIRuleProviders 从嵌入的默认配置自动生成。
// 在包初始化时从 customer.ai_rules 提取。
// 声明在 types.go 中，此处 init() 负责填充。

func init() {
	var cfg struct {
		Customer *struct {
			AIRules map[string]*CustomerAIRuleSetting `json:"ai_rules"`
		} `json:"customer"`
	}
	if err := json.Unmarshal(defaultConfigData, &cfg); err != nil {
		return
	}
	if cfg.Customer == nil || len(cfg.Customer.AIRules) == 0 {
		return
	}
	providers := make([]AIRuleProviderPreset, 0, len(cfg.Customer.AIRules))
	for key, rule := range cfg.Customer.AIRules {
		label := rule.Label
		if label == "" {
			label = strings.ToUpper(key[:1]) + key[1:]
		}
		editable := rule.Editable != nil && *rule.Editable
		providers = append(providers, AIRuleProviderPreset{
			Key:            key,
			Label:          label,
			DefaultInclude: rule.Include,
			Editable:       editable,
		})
	}
	PresetAIRuleProviders = providers
}
