package statistic

import (
	"net"
	"net/url"
	"sort"
	"strings"

	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/match"
)

type trackedAIProvider struct {
	Key      string
	Label    string
	Patterns []string
}

func currentTrackedAIDomains() []string {
	tracked := make(map[string]struct{})
	for _, provider := range currentTrackedAIProviders() {
		for _, pattern := range provider.Patterns {
			tracked[pattern] = struct{}{}
		}
	}

	return sortTrackedAIDomains(tracked)
}

func matchTrackedAIDomain(host string) string {
	_, matchedPattern, ok := matchTrackedAIProvider(host)
	if !ok {
		return ""
	}
	return matchedPattern
}

func matchTrackedAIProvider(host string) (trackedAIProvider, string, bool) {
	normalizedHost := normalizeAIDomainHost(host)
	if normalizedHost == "" {
		return trackedAIProvider{}, "", false
	}

	for _, provider := range currentTrackedAIProvidersOrdered() {
		for _, pattern := range provider.Patterns {
			if aiDomainMatches(pattern, normalizedHost) {
				return provider, pattern, true
			}
		}
	}

	return trackedAIProvider{}, "", false
}

func currentTrackedAIProviders() []trackedAIProvider {
	ordered := currentTrackedAIProvidersOrdered()
	out := make([]trackedAIProvider, 0, len(ordered))
	for _, provider := range ordered {
		provider.Patterns = dedupeAndSortPatterns(provider.Patterns)
		if provider.Key == "" || provider.Label == "" || len(provider.Patterns) == 0 {
			continue
		}
		out = append(out, provider)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Label == out[j].Label {
			return out[i].Key < out[j].Key
		}
		return out[i].Label < out[j].Label
	})

	return out
}

func currentTrackedAIProvidersOrdered() []trackedAIProvider {
	providers := make(map[string]*trackedAIProvider)
	order := make([]string, 0)
	appendProvider := func(key, label string, patterns []string) {
		provider, created := ensureTrackedAIProviderWithState(providers, key, label)
		if created {
			order = append(order, provider.Key)
		}
		provider.Patterns = append(provider.Patterns, normalizeAIDomainPatterns(patterns)...)
	}

	cfg := config.GetGlobalConfig()
	if cfg != nil && cfg.Customer != nil {
		for key, rule := range cfg.Customer.AIRules {
			if rule == nil {
				continue
			}
			if rule.Enble != nil && !*rule.Enble {
				continue
			}
			appendProvider(key, rule.Label, rule.Include)
		}
	}

	for _, preset := range config.PresetAIRuleProviders {
		appendProvider(preset.Key, preset.Label, preset.DefaultInclude)
	}

	for _, fallback := range builtinTrackedAIProviders() {
		appendProvider(fallback.Key, fallback.Label, fallback.Patterns)
	}

	out := make([]trackedAIProvider, 0, len(order))
	for _, key := range order {
		provider := providers[key]
		provider.Patterns = dedupeAndSortPatterns(provider.Patterns)
		if provider.Key == "" || provider.Label == "" || len(provider.Patterns) == 0 {
			continue
		}
		out = append(out, *provider)
	}

	return out
}

func builtinTrackedAIProviders() []trackedAIProvider {
	return []trackedAIProvider{
		{Key: "openai", Label: "OpenAI", Patterns: []string{"openai.com", "chatgpt.com"}},
		{Key: "claude", Label: "Claude", Patterns: []string{"claude.ai", "anthropic.com"}},
		{Key: "cursor", Label: "Cursor", Patterns: []string{"api.cursor.com"}},
		{Key: "copilot", Label: "Copilot", Patterns: []string{"copilot.microsoft.com"}},
	}
}

func ensureTrackedAIProvider(values map[string]*trackedAIProvider, key, label string) *trackedAIProvider {
	provider, _ := ensureTrackedAIProviderWithState(values, key, label)
	return provider
}

func ensureTrackedAIProviderWithState(values map[string]*trackedAIProvider, key, label string) (*trackedAIProvider, bool) {
	normalizedKey := strings.ToLower(strings.TrimSpace(key))
	if normalizedKey == "" {
		normalizedKey = normalizeProviderLabel(label)
	}
	if normalizedKey == "" {
		return &trackedAIProvider{}, false
	}

	provider, exists := values[normalizedKey]
	if !exists {
		provider = &trackedAIProvider{Key: normalizedKey}
		values[normalizedKey] = provider
	}
	if strings.TrimSpace(label) != "" {
		provider.Label = strings.TrimSpace(label)
	}
	if provider.Label == "" {
		provider.Label = defaultProviderLabel(normalizedKey, label)
	}
	return provider, !exists
}

func normalizeAIDomainPatterns(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if normalized := normalizeAIDomainPattern(value); normalized != "" {
			out = append(out, normalized)
		}
	}
	return out
}

func dedupeAndSortPatterns(values []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) == len(out[j]) {
			return out[i] < out[j]
		}
		return len(out[i]) > len(out[j])
	})
	return out
}

func normalizeProviderLabel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "")
	return value
}

func defaultProviderLabel(key, fallback string) string {
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback)
	}
	switch key {
	case "openai":
		return "OpenAI"
	case "claude":
		return "Claude"
	case "cursor":
		return "Cursor"
	case "copilot":
		return "Copilot"
	case "vscode":
		return "VS Code"
	default:
		if key == "" {
			return "AI"
		}
		return strings.ToUpper(key[:1]) + key[1:]
	}
}

func normalizeAIDomainHost(host string) string {
	return normalizeAIDomainPattern(host)
}

func normalizeAIDomainPattern(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	value = strings.TrimSuffix(value, ".")
	if value == "" {
		return ""
	}

	wildcard := strings.HasPrefix(value, "*.")
	if wildcard {
		value = strings.TrimPrefix(value, "*.")
	}

	if strings.Contains(value, "://") {
		if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
			value = parsed.Host
		}
	}

	// IP 条目（单 IP / CIDR / 区间 / ip-from 形态）直接采用共享内核的归一化 condition，
	// 跳过下方「首个 / 截断」与端口剥离，避免 CIDR/区间被截断变形
	// （如 121.32.12.0/24 → 121.32.12.0），保证统计侧与编译侧看到同一形态。
	// *. wildcard 条目不可能是 IP 形态，走既有归一化路径不受影响；
	// 非法条目（err 非空）同样落回既有路径。
	if !wildcard {
		if kind, cond, err := match.ClassifyEntry(value); err == nil && kind == match.EntryIP {
			return cond
		}
	}

	if cut := strings.IndexRune(value, '/'); cut >= 0 {
		value = value[:cut]
	}

	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	} else if host, port, ok := strings.Cut(value, ":"); ok && isAllDigits(port) {
		value = host
	}

	value = strings.Trim(value, "[]")
	value = strings.TrimSuffix(strings.TrimSpace(value), ".")
	if value == "" {
		return ""
	}

	if wildcard {
		return "*." + value
	}
	return value
}

func aiDomainMatches(pattern, host string) bool {
	return match.MatchDomain(pattern, host)
}

func sortTrackedAIDomains(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}

	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) == len(out[j]) {
			return out[i] < out[j]
		}
		return len(out[i]) > len(out[j])
	})

	return out
}

func isAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}
