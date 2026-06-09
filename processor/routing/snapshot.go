package routing

import (
	"fmt"
	"sort"
	"strings"

	"aliang.one/nursorgate/common/model"
	"aliang.one/nursorgate/processor/config"
)

type SnapshotAction string

const (
	SnapshotActionDirect   SnapshotAction = "direct"
	SnapshotActionToAliang SnapshotAction = "toAliang"
	SnapshotActionToSocks  SnapshotAction = "toSocks"
	SnapshotActionDeny     SnapshotAction = "deny"
)

type SnapshotBranchCapabilities struct {
	direct   bool
	toAliang bool
	toSocks  bool
}

func NewSnapshotBranchCapabilities(direct, toAliang, toSocks bool) SnapshotBranchCapabilities {
	return SnapshotBranchCapabilities{
		direct:   direct,
		toAliang: toAliang,
		toSocks:  toSocks,
	}
}

func (c SnapshotBranchCapabilities) Direct() bool {
	return c.direct
}

func (c SnapshotBranchCapabilities) ToAliang() bool {
	return c.toAliang
}

func (c SnapshotBranchCapabilities) ToSocks() bool {
	return c.toSocks
}

type SnapshotRule struct {
	id        string
	ruleType  string
	condition string
	enabled   bool
	target    SnapshotAction
}

func NewSnapshotRule(id, ruleType, condition string, enabled bool, target SnapshotAction) SnapshotRule {
	return SnapshotRule{
		id:        id,
		ruleType:  strings.ToLower(strings.TrimSpace(ruleType)),
		condition: strings.ToLower(strings.TrimSpace(condition)),
		enabled:   enabled,
		target:    target,
	}
}

func (r SnapshotRule) ID() string {
	return r.id
}

func (r SnapshotRule) Type() string {
	return r.ruleType
}

func (r SnapshotRule) Condition() string {
	return r.condition
}

func (r SnapshotRule) Enabled() bool {
	return r.enabled
}

func (r SnapshotRule) Target() SnapshotAction {
	return r.target
}

type RuntimeSnapshot struct {
	ingressMode     string
	capabilities    SnapshotBranchCapabilities
	rules           []SnapshotRule
	defaultAction   SnapshotAction
	explicitDeny    SnapshotAction
	hasExplicitDeny bool
	strictDeny      bool
}

func NewRuntimeSnapshotForDecision(capabilities SnapshotBranchCapabilities, rules []SnapshotRule, defaultAction SnapshotAction, strictDeny bool) *RuntimeSnapshot {
	rulesCopy := make([]SnapshotRule, len(rules))
	copy(rulesCopy, rules)
	hasDeny := false
	for _, rule := range rulesCopy {
		if rule.target == SnapshotActionDeny {
			hasDeny = true
			break
		}
	}

	return &RuntimeSnapshot{
		ingressMode:     "tun",
		capabilities:    capabilities,
		rules:           rulesCopy,
		defaultAction:   defaultAction,
		explicitDeny:    SnapshotActionDeny,
		hasExplicitDeny: hasDeny,
		strictDeny:      strictDeny,
	}
}

func (s *RuntimeSnapshot) IngressMode() string {
	if s == nil {
		return "tun"
	}
	return s.ingressMode
}

func (s *RuntimeSnapshot) BranchCapabilities() SnapshotBranchCapabilities {
	if s == nil {
		return SnapshotBranchCapabilities{direct: true}
	}
	return s.capabilities
}

func (s *RuntimeSnapshot) Rules() []SnapshotRule {
	if s == nil {
		return nil
	}
	out := make([]SnapshotRule, len(s.rules))
	copy(out, s.rules)
	return out
}

func (s *RuntimeSnapshot) DefaultAction() SnapshotAction {
	if s == nil {
		return SnapshotActionDirect
	}
	return s.defaultAction
}

func (s *RuntimeSnapshot) ExplicitDenyAction() SnapshotAction {
	if s == nil {
		return SnapshotActionDeny
	}
	return s.explicitDeny
}

func (s *RuntimeSnapshot) HasExplicitDenyRule() bool {
	if s == nil {
		return false
	}
	return s.hasExplicitDeny
}

func (s *RuntimeSnapshot) StrictUnavailableBranchDeny() bool {
	if s == nil {
		return false
	}
	return s.strictDeny
}

func CompileRuntimeSnapshot(canonical *config.CanonicalRoutingSchema) (*RuntimeSnapshot, error) {
	if canonical == nil {
		return nil, fmt.Errorf("canonical routing schema is nil")
	}

	if err := canonical.Validate(); err != nil {
		return nil, err
	}

	rules := make([]SnapshotRule, 0, len(canonical.Routing.Rules))
	hasDeny := false
	for _, rule := range canonical.Routing.Rules {
		target := SnapshotAction(rule.Target)
		rules = append(rules, SnapshotRule{
			id:        rule.ID,
			ruleType:  strings.ToLower(strings.TrimSpace(rule.Type)),
			condition: strings.ToLower(strings.TrimSpace(rule.Condition)),
			enabled:   rule.Enabled,
			target:    target,
		})
		if target == SnapshotActionDeny {
			hasDeny = true
		}
	}

	defaultAction := SnapshotActionDirect
	if canonical.Routing.DefaultEgress != "" {
		defaultAction = SnapshotAction(canonical.Routing.DefaultEgress)
	}

	return &RuntimeSnapshot{
		ingressMode: strings.ToLower(strings.TrimSpace(canonical.Ingress.Mode)),
		capabilities: SnapshotBranchCapabilities{
			direct:   canonical.Egress.Direct.Enabled,
			toAliang: canonical.Egress.ToAliang.Enabled,
			toSocks:  canonical.Egress.ToSocks.Enabled,
		},
		rules:           rules,
		defaultAction:   defaultAction,
		explicitDeny:    SnapshotActionDeny,
		hasExplicitDeny: hasDeny,
		strictDeny:      true,
	}, nil
}

func CompileRuntimeSnapshotFromLegacyConfig(cfg *model.RoutingRulesConfig) (*RuntimeSnapshot, error) {
	if cfg == nil {
		return nil, fmt.Errorf("routing rules config is nil")
	}

	rules := make([]SnapshotRule, 0, len(cfg.Aliang.Rules)+len(cfg.ToSocks.Rules)+len(cfg.Direct.Rules))
	for _, rule := range cfg.Aliang.Rules {
		rules = append(rules, SnapshotRule{
			id:        rule.ID,
			ruleType:  strings.ToLower(strings.TrimSpace(string(rule.Type))),
			condition: strings.ToLower(strings.TrimSpace(rule.Condition)),
			enabled:   rule.Enabled,
			target:    SnapshotActionToAliang,
		})
	}
	for _, rule := range cfg.ToSocks.Rules {
		rules = append(rules, SnapshotRule{
			id:        rule.ID,
			ruleType:  strings.ToLower(strings.TrimSpace(string(rule.Type))),
			condition: strings.ToLower(strings.TrimSpace(rule.Condition)),
			enabled:   rule.Enabled,
			target:    SnapshotActionToSocks,
		})
	}
	for _, rule := range cfg.Direct.Rules {
		rules = append(rules, SnapshotRule{
			id:        rule.ID,
			ruleType:  strings.ToLower(strings.TrimSpace(string(rule.Type))),
			condition: strings.ToLower(strings.TrimSpace(rule.Condition)),
			enabled:   rule.Enabled,
			target:    SnapshotActionDirect,
		})
	}

	hasDeny := false
	for _, rule := range rules {
		if rule.target == SnapshotActionDeny {
			hasDeny = true
			break
		}
	}

	return &RuntimeSnapshot{
		ingressMode: "tun",
		capabilities: SnapshotBranchCapabilities{
			direct:   true,
			toAliang: cfg.Settings.AliangEnabled,
			toSocks:  cfg.Settings.SocksEnabled,
		},
		rules:           rules,
		defaultAction:   SnapshotActionDirect,
		explicitDeny:    SnapshotActionDeny,
		hasExplicitDeny: hasDeny,
		strictDeny:      false,
	}, nil
}

func CompileRuntimeSnapshotFromRuntimeInputs(cfg *config.Config, switches model.RulesSettings) (*RuntimeSnapshot, error) {
	canonical, err := compileCanonicalRoutingFromRuntimeInputs(cfg, switches)
	if err != nil {
		return nil, err
	}

	return CompileRuntimeSnapshot(canonical)
}

func CompileCanonicalRoutingFromRuntimeInputs(cfg *config.Config, switches model.RulesSettings) (*config.CanonicalRoutingSchema, error) {
	return compileCanonicalRoutingFromRuntimeInputs(cfg, switches)
}

func compileCanonicalRoutingFromRuntimeInputs(cfg *config.Config, switches model.RulesSettings) (*config.CanonicalRoutingSchema, error) {
	canonical := &config.CanonicalRoutingSchema{
		Version: config.CanonicalRoutingSchemaVersion,
		Ingress: config.CanonicalIngressConfig{Mode: "tun"},
		Egress: config.CanonicalEgressConfig{
			Direct:   config.CanonicalEgressBranch{Enabled: true},
			ToAliang: config.CanonicalEgressBranch{Enabled: switches.AliangEnabled},
			ToSocks:  config.CanonicalSocksEgressBranch{Enabled: false},
		},
		Routing: config.CanonicalRoutingConfig{Rules: []config.CanonicalRoutingRule{}},
	}

	var aiDomains []string
	if switches.AliangEnabled {
		aiDomains = collectAIDomains(cfg)
	}
	proxyRuleDomains := collectProxyRuleDomains(cfg)

	upstreamType, hasToSocks := resolveToSocksUpstreamType(cfg)
	canonical.Egress.ToSocks.Enabled = switches.SocksEnabled && hasToSocks
	if canonical.Egress.ToSocks.Enabled {
		canonical.Egress.ToSocks.Upstream.Type = upstreamType
	}

	rules := make([]config.CanonicalRoutingRule, 0, len(aiDomains)+len(proxyRuleDomains))
	for i, domain := range aiDomains {
		rules = append(rules, config.CanonicalRoutingRule{
			ID:        fmt.Sprintf("ai_allowlist_%d", i),
			Type:      string(model.RuleTypeDomain),
			Condition: domain,
			Enabled:   true,
			Target:    string(SnapshotActionToAliang),
		})
	}

	for i, domain := range proxyRuleDomains {
		rules = append(rules, config.CanonicalRoutingRule{
			ID:        fmt.Sprintf("proxy_rule_%d", i),
			Type:      string(model.RuleTypeDomain),
			Condition: domain,
			Enabled:   true,
			Target:    string(resolveProxyRuleTarget(canonical.Egress.ToSocks.Enabled)),
		})
	}
	canonical.Routing.Rules = rules

	// Keep direct as the default egress. The customer proxy should only be used
	// by explicit proxy_rules matches instead of hijacking all unmatched traffic.
	canonical.Routing.DefaultEgress = string(SnapshotActionDirect)

	if err := canonical.Validate(); err != nil {
		return nil, fmt.Errorf("compile runtime canonical routing failed: %w", err)
	}

	return canonical, nil
}

func collectAIDomains(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}

	domains := make([]string, 0)
	if cfg.Customer != nil && len(cfg.Customer.AIRules) > 0 {
		providers := make([]string, 0, len(cfg.Customer.AIRules))
		for provider := range cfg.Customer.AIRules {
			providers = append(providers, provider)
		}
		sort.Strings(providers)

		for _, provider := range providers {
			rule := cfg.Customer.AIRules[provider]
			if rule == nil || rule.Enble == nil || !*rule.Enble {
				continue
			}
			domains = append(domains, rule.Include...)
		}
	}

	domains = append(domains, cfg.EffectiveAIAllowlist()...)
	return dedupeNormalizedDomains(domains)
}

func collectProxyRuleDomains(cfg *config.Config) []string {
	if cfg == nil || cfg.Customer == nil {
		return nil
	}

	rules := cfg.Customer.ProxyRules
	if len(rules) == 0 {
		return nil
	}

	domains := make([]string, 0, len(rules))
	for _, rawRule := range rules {
		domain, ok := parseProxyRuleDomain(rawRule)
		if !ok {
			continue
		}
		domains = append(domains, domain)
	}
	return dedupeNormalizedDomains(domains)
}

func parseProxyRuleDomain(rawRule string) (string, bool) {
	rule := strings.TrimSpace(rawRule)
	if rule == "" {
		return "", false
	}

	parts := strings.Split(rule, ",")
	if len(parts) == 1 {
		normalized := strings.ToLower(strings.TrimSpace(parts[0]))
		return normalized, normalized != ""
	}

	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	if len(parts) < 2 {
		return "", false
	}

	first := strings.ToLower(parts[0])
	if first == "domain" || first == "domains" {
		normalized := strings.ToLower(parts[1])
		return normalized, normalized != ""
	}

	normalized := strings.ToLower(parts[len(parts)-1])
	if strings.EqualFold(normalized, "proxy") && len(parts) >= 2 {
		normalized = strings.ToLower(parts[len(parts)-2])
	}
	return normalized, normalized != ""
}

func resolveToSocksUpstreamType(cfg *config.Config) (string, bool) {
	if cfg != nil && cfg.Customer != nil && cfg.Customer.Proxy != nil {
		if !cfg.Customer.Proxy.IsEnabled() {
			return "", false
		}
		proxyType := strings.ToLower(strings.TrimSpace(cfg.Customer.Proxy.Type))
		switch proxyType {
		case "http":
			return "http", true
		case "socks5":
			return "socks", true
		}
	}

	return "", false
}

func resolveProxyRuleTarget(toSocksEnabled bool) SnapshotAction {
	if toSocksEnabled {
		return SnapshotActionToSocks
	}
	return SnapshotActionDirect
}

func dedupeNormalizedDomains(in []string) []string {
	if len(in) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		normalized := strings.ToLower(strings.TrimSpace(raw))
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}
