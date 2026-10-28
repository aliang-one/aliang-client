package config

import (
	"reflect"
	"testing"
)

func aiRuleBoolPtr(v bool) *bool { return &v }

// 大小写变体必须折叠为一个规范键（与出厂预置拼写一致），否则前端会渲染出
// 「Anthropic」和「anthropic」两张卡，且保存时会把补种键永久写回配置文件。
func TestNormalizeCustomerAIRulesFoldsCaseVariantsToPresetSpelling(t *testing.T) {
	rules := map[string]*CustomerAIRuleSetting{
		"anthropic": {
			Include: []string{"api.anthropic.com"},
		},
		"Anthropic": {
			Enble:    aiRuleBoolPtr(true),
			Editable: aiRuleBoolPtr(true),
			Label:    "Anthropic",
			Include:  []string{"api.aliang.one", "api.anthropic.com"},
		},
	}

	got := NormalizeCustomerAIRules(rules)

	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (keys: %v)", len(got), sortedMapKeys(got))
	}
	rule, ok := got["Anthropic"]
	if !ok {
		t.Fatalf("canonical key \"Anthropic\" missing, got keys %v", sortedMapKeys(got))
	}
	if rule.Enble == nil || !*rule.Enble {
		t.Fatalf("enble = %v, want true (OR across variants)", rule.Enble)
	}
	if rule.Label != "Anthropic" {
		t.Fatalf("label = %q, want Anthropic", rule.Label)
	}
	if rule.Editable == nil || !*rule.Editable {
		t.Fatalf("editable = %v, want true", rule.Editable)
	}
	wantInclude := []string{"api.aliang.one", "api.anthropic.com"}
	if !reflect.DeepEqual(rule.Include, wantInclude) {
		t.Fatalf("include = %v, want %v (union, deduped, deterministic order)", rule.Include, wantInclude)
	}
}

// 键名只有单一形态且已规范时必须原样保留，不得被深拷贝改写语义。
func TestNormalizeCustomerAIRulesKeepsCanonicalSingleEntry(t *testing.T) {
	rules := map[string]*CustomerAIRuleSetting{
		"Anthropic": {
			Enble:   aiRuleBoolPtr(false),
			Include: []string{"api.anthropic.com"},
		},
	}

	got := NormalizeCustomerAIRules(rules)

	rule, ok := got["Anthropic"]
	if !ok {
		t.Fatalf("canonical key \"Anthropic\" missing, got keys %v", sortedMapKeys(got))
	}
	if rule.Enble == nil || *rule.Enble {
		t.Fatalf("enble = %v, want false (unchanged)", rule.Enble)
	}
	if !reflect.DeepEqual(rule.Include, []string{"api.anthropic.com"}) {
		t.Fatalf("include = %v, want unchanged", rule.Include)
	}
}

// 已停用产品（vscode/Copilot）必须被剥除，无论大小写形态——存量配置文件与
// API patch 里的 vscode 条目都不允许再存活。
func TestNormalizeCustomerAIRulesDropsRemovedProviders(t *testing.T) {
	rules := map[string]*CustomerAIRuleSetting{
		"vscode": {Enble: aiRuleBoolPtr(true), Include: []string{"api.githubcopilot.com"}},
		"VSCode": {Enble: aiRuleBoolPtr(true), Include: []string{"api.githubcopilot.com"}},
		"openai": {Enble: aiRuleBoolPtr(true), Include: []string{"api.openai.com"}},
	}

	got := NormalizeCustomerAIRules(rules)

	if _, exists := got["vscode"]; exists {
		t.Fatalf("provider \"vscode\" must be removed, got keys %v", sortedMapKeys(got))
	}
	if _, exists := got["VSCode"]; exists {
		t.Fatalf("provider \"VSCode\" must be removed, got keys %v", sortedMapKeys(got))
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (openai only), got keys %v", len(got), sortedMapKeys(got))
	}
}

// 已知 provider 的非规范拼写统一到出厂拼写；未知自定义 provider 保持原样。
func TestNormalizeCustomerAIRulesCanonicalizesKnownProvidersOnly(t *testing.T) {
	rules := map[string]*CustomerAIRuleSetting{
		"ANTHROPIC": {Enble: aiRuleBoolPtr(true), Include: []string{"api.anthropic.com"}},
		"OpenAI":    {Enble: aiRuleBoolPtr(true), Include: []string{"api.openai.com"}},
		"deepseek":  {Enble: aiRuleBoolPtr(true), Include: []string{"api.deepseek.com"}},
	}

	got := NormalizeCustomerAIRules(rules)

	if _, ok := got["Anthropic"]; !ok {
		t.Fatalf("key \"ANTHROPIC\" must be canonicalized to \"Anthropic\", got keys %v", sortedMapKeys(got))
	}
	if _, ok := got["openai"]; !ok {
		t.Fatalf("key \"OpenAI\" must be canonicalized to \"openai\", got keys %v", sortedMapKeys(got))
	}
	if rule, ok := got["deepseek"]; !ok || rule == nil {
		t.Fatalf("custom provider \"deepseek\" must be kept as-is, got keys %v", sortedMapKeys(got))
	}
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3, got keys %v", len(got), sortedMapKeys(got))
	}
}

// 空/nil 输入与 nil 条目必须安全处理。
func TestNormalizeCustomerAIRulesHandlesEmptyAndNilEntries(t *testing.T) {
	if got := NormalizeCustomerAIRules(nil); len(got) != 0 {
		t.Fatalf("nil input: got %v, want empty", got)
	}
	if got := NormalizeCustomerAIRules(map[string]*CustomerAIRuleSetting{}); len(got) != 0 {
		t.Fatalf("empty input: got %v, want empty", got)
	}
	got := NormalizeCustomerAIRules(map[string]*CustomerAIRuleSetting{
		"broken": nil,
		"openai": {Enble: aiRuleBoolPtr(true), Include: []string{"api.openai.com"}},
	})
	if len(got) != 1 {
		t.Fatalf("nil entries must be dropped, got keys %v", sortedMapKeys(got))
	}
}
