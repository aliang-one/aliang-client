package cmd

import (
	"os"
	"path/filepath"
	"testing"

	processorconfig "aliang.one/nursorgate/processor/config"
)

// 配置加载入口必须归一化 ai_rules：大小写变体折叠为出厂拼写、剥除已停用
// provider（vscode）。否则存量文件里的小写 anthropic 会让前端渲染两张卡，
// vscode 也会继续参与 AI 加速路由。
func TestLoadConfigFromBytes_NormalizesAIRules(t *testing.T) {
	resetConfigPipelineStateForTest(t)

	data := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"ai_rules": {
				"anthropic": {"enble": true, "include": ["api.anthropic.com"]},
				"Anthropic": {"enble": false},
				"vscode": {"enble": true, "include": ["api.githubcopilot.com"]}
			}
		}
	}`)

	cfg, err := LoadConfigFromBytes(data)
	if err != nil {
		t.Fatalf("LoadConfigFromBytes() error = %v", err)
	}
	if cfg.Customer == nil {
		t.Fatal("customer config missing")
	}

	rules := cfg.Customer.AIRules
	if _, exists := rules["anthropic"]; exists {
		t.Fatalf("lowercase \"anthropic\" must be folded at load, got keys %v", loadedAIRuleKeys(rules))
	}
	if _, exists := rules["vscode"]; exists {
		t.Fatalf("removed provider \"vscode\" must be stripped at load, got keys %v", loadedAIRuleKeys(rules))
	}
	rule := rules["Anthropic"]
	if rule == nil {
		t.Fatalf("canonical \"Anthropic\" missing, got keys %v", loadedAIRuleKeys(rules))
	}
	if rule.Enble == nil || !*rule.Enble {
		t.Fatalf("Anthropic.enble = %v, want true (OR across variants)", rule.Enble)
	}
	if len(rule.Include) != 1 || rule.Include[0] != "api.anthropic.com" {
		t.Fatalf("Anthropic.include = %v, want [api.anthropic.com]", rule.Include)
	}
}

func TestLoadConfig_NormalizesAIRules(t *testing.T) {
	resetConfigPipelineStateForTest(t)

	content := `{
		"customer": {
			"ai_rules": {
				"vscode": {"enble": true, "include": ["api.githubcopilot.com"]},
				"openai": {"enble": true, "include": ["api.openai.com"]}
			}
		}
	}`
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	rules := cfg.Customer.AIRules
	if _, exists := rules["vscode"]; exists {
		t.Fatalf("removed provider \"vscode\" must be stripped at load, got keys %v", loadedAIRuleKeys(rules))
	}
	if rules["openai"] == nil {
		t.Fatalf("openai rule must be preserved, got keys %v", loadedAIRuleKeys(rules))
	}
}

func loadedAIRuleKeys(rules map[string]*processorconfig.CustomerAIRuleSetting) []string {
	keys := make([]string, 0, len(rules))
	for key := range rules {
		keys = append(keys, key)
	}
	return keys
}
