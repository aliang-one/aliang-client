package config

import (
	"sort"
	"strings"
)

// removedAIRuleProviders 列出产品已停用的 AI provider。归一化在配置加载、
// customer 配置合并与已提交配置视图三处剥除这些条目（不区分大小写），
// 保证存量配置文件与外部 API patch 都无法让它们复活。
var removedAIRuleProviders = []string{"vscode"}

// presetAIRuleSpellingByNormalizedKey 返回「归一化键 -> 出厂预置拼写」映射，
// 作为大小写变体折叠时的规范拼写来源。
func presetAIRuleSpellingByNormalizedKey() map[string]string {
	out := make(map[string]string, len(PresetAIRuleProviders))
	for _, preset := range PresetAIRuleProviders {
		if preset.Key == "" {
			continue
		}
		out[NormalizeAIProviderKey(preset.Key)] = preset.Key
	}
	return out
}

// canonicalAIRuleKey 已知 provider 返回出厂拼写，未知 provider 保持原拼写。
func canonicalAIRuleKey(key string, spelling map[string]string) string {
	if canonical, ok := spelling[NormalizeAIProviderKey(key)]; ok {
		return canonical
	}
	return key
}

// CanonicalAIRuleProviderKey 返回已知 AI provider 的出厂规范拼写
// （如 anthropic/ANTHROPIC → Anthropic）；未知自定义 provider 原样返回。
func CanonicalAIRuleProviderKey(key string) string {
	return canonicalAIRuleKey(key, presetAIRuleSpellingByNormalizedKey())
}

// IsRemovedAIRuleProvider 报告该 provider 是否属于已停用产品（大小写不敏感）。
func IsRemovedAIRuleProvider(key string) bool {
	normalized := NormalizeAIProviderKey(key)
	for _, removed := range removedAIRuleProviders {
		if NormalizeAIProviderKey(removed) == normalized {
			return true
		}
	}
	return false
}

// NormalizeCustomerAIRules 把同一 provider 的大小写变体折叠为单个规范条目，
// 并剥除 removedAIRuleProviders 中的已停用 provider。规范拼写跟随出厂默认
// 配置（PresetAIRuleProviders）；未知自定义 provider 保持原拼写。不修改入参。
//
// 折叠语义（确定性）：按原始键排序，首个条目定基线（label/editable/include
// 顺序），Enble 取或，Include 并集按大小写不敏感去重。
func NormalizeCustomerAIRules(rules map[string]*CustomerAIRuleSetting) map[string]*CustomerAIRuleSetting {
	if len(rules) == 0 {
		return rules
	}

	removed := make(map[string]struct{}, len(removedAIRuleProviders))
	for _, key := range removedAIRuleProviders {
		removed[NormalizeAIProviderKey(key)] = struct{}{}
	}
	spelling := presetAIRuleSpellingByNormalizedKey()

	keys := make([]string, 0, len(rules))
	for key := range rules {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make(map[string]*CustomerAIRuleSetting, len(rules))
	for _, key := range keys {
		rule := rules[key]
		if rule == nil {
			continue
		}
		if _, drop := removed[NormalizeAIProviderKey(key)]; drop {
			continue
		}

		target := canonicalAIRuleKey(key, spelling)
		existing, ok := out[target]
		if !ok {
			out[target] = cloneCustomerAIRule(rule)
			continue
		}
		mergeCustomerAIRule(existing, rule)
	}
	return out
}

func cloneCustomerAIRule(rule *CustomerAIRuleSetting) *CustomerAIRuleSetting {
	if rule == nil {
		return nil
	}
	cloned := *rule
	if rule.Include != nil {
		cloned.Include = append([]string(nil), rule.Include...)
	}
	return &cloned
}

func mergeCustomerAIRule(base, extra *CustomerAIRuleSetting) {
	if extra == nil {
		return
	}
	if extra.Enble != nil {
		if base.Enble == nil {
			base.Enble = copyBoolPtr(extra.Enble)
		} else if *extra.Enble {
			*base.Enble = true
		}
	}
	if strings.TrimSpace(base.Label) == "" {
		base.Label = extra.Label
	}
	if base.Editable == nil {
		base.Editable = copyBoolPtr(extra.Editable)
	}
	for _, include := range extra.Include {
		if !containsAIRuleInclude(base.Include, include) {
			base.Include = append(base.Include, include)
		}
	}
}

func containsAIRuleInclude(includes []string, value string) bool {
	for _, existing := range includes {
		if strings.EqualFold(existing, value) {
			return true
		}
	}
	return false
}

func copyBoolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
