package routing

import (
	"testing"

	"aliang.one/nursorgate/common/model"
	"aliang.one/nursorgate/processor/config"
)

func boolPtr(value bool) *bool {
	return &value
}

func TestRuntimeSnapshot_AIRulesTakePriorityOverCustomerProxyRules(t *testing.T) {
	cfg := &config.Config{
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Type:   "socks5",
				Server: "127.0.0.1:1080",
			},
			AIRules: map[string]*config.CustomerAIRuleSetting{
				"cursor": {
					Enble:   boolPtr(true),
					Include: []string{"cursor.com"},
				},
			},
			ProxyRules: []string{"domains,cursor.com,proxy"},
		},
	}

	snapshot, err := CompileRuntimeSnapshotFromRuntimeInputs(cfg, model.RulesSettings{
		AliangEnabled: true,
		SocksEnabled:  true,
	})
	if err != nil {
		t.Fatalf("CompileRuntimeSnapshotFromRuntimeInputs() error = %v", err)
	}

	decision, err := DecideRouteFromSnapshot(snapshot, &MatchContext{Domain: "cursor.com"})
	if err != nil {
		t.Fatalf("DecideRouteFromSnapshot() error = %v", err)
	}
	if decision != RouteToAliang {
		t.Fatalf("decision = %s, want %s", decision, RouteToAliang)
	}
}

func TestRuntimeSnapshot_CustomerProxyRulesMatchWhenAliangDisabled(t *testing.T) {
	cfg := &config.Config{
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Type:   "http",
				Server: "127.0.0.1:8080",
			},
			AIRules: map[string]*config.CustomerAIRuleSetting{
				"cursor": {
					Enble:   boolPtr(true),
					Include: []string{"cursor.com"},
				},
			},
			ProxyRules: []string{"domains,cursor.com,proxy"},
		},
	}

	snapshot, err := CompileRuntimeSnapshotFromRuntimeInputs(cfg, model.RulesSettings{
		AliangEnabled: false,
		SocksEnabled:  true,
	})
	if err != nil {
		t.Fatalf("CompileRuntimeSnapshotFromRuntimeInputs() error = %v", err)
	}

	decision, err := DecideRouteFromSnapshot(snapshot, &MatchContext{Domain: "cursor.com"})
	if err != nil {
		t.Fatalf("DecideRouteFromSnapshot() error = %v", err)
	}
	if decision != RouteToSocks {
		t.Fatalf("decision = %s, want %s", decision, RouteToSocks)
	}
}

func TestRuntimeSnapshot_CustomerProxyRulesMatchWhenAIRuleDisabled(t *testing.T) {
	cfg := &config.Config{
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Type:   "socks5",
				Server: "127.0.0.1:1080",
			},
			AIRules: map[string]*config.CustomerAIRuleSetting{
				"cursor": {
					Enble:   boolPtr(false),
					Include: []string{"cursor.com"},
				},
			},
			ProxyRules: []string{"domains,cursor.com,proxy"},
		},
	}

	snapshot, err := CompileRuntimeSnapshotFromRuntimeInputs(cfg, model.RulesSettings{
		AliangEnabled: true,
		SocksEnabled:  true,
	})
	if err != nil {
		t.Fatalf("CompileRuntimeSnapshotFromRuntimeInputs() error = %v", err)
	}

	decision, err := DecideRouteFromSnapshot(snapshot, &MatchContext{Domain: "cursor.com"})
	if err != nil {
		t.Fatalf("DecideRouteFromSnapshot() error = %v", err)
	}
	if decision != RouteToSocks {
		t.Fatalf("decision = %s, want %s", decision, RouteToSocks)
	}
}

func TestRuntimeSnapshot_CustomerProxyPlainDomainRuleMatchesSubdomains(t *testing.T) {
	cfg := &config.Config{
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Type:   "socks5",
				Server: "182.138.136.39:27751",
			},
			ProxyRules: []string{"domains,google.com"},
		},
	}

	snapshot, err := CompileRuntimeSnapshotFromRuntimeInputs(cfg, model.RulesSettings{
		AliangEnabled: true,
		SocksEnabled:  true,
	})
	if err != nil {
		t.Fatalf("CompileRuntimeSnapshotFromRuntimeInputs() error = %v", err)
	}

	for _, domain := range []string{
		"google.com",
		"www.google.com",
		"ogads-pa.clients6.google.com",
	} {
		decision, err := DecideRouteFromSnapshot(snapshot, &MatchContext{Domain: domain})
		if err != nil {
			t.Fatalf("DecideRouteFromSnapshot(%q) error = %v", domain, err)
		}
		if decision != RouteToSocks {
			t.Fatalf("decision for %q = %s, want %s", domain, decision, RouteToSocks)
		}
	}
}

func TestRuntimeSnapshot_CompileAIGlobIncludeAsDomainRule(t *testing.T) {
	cfg := &config.Config{
		Customer: &config.CustomerConfig{
			AIRules: map[string]*config.CustomerAIRuleSetting{
				"aliang": {
					Enble:   boolPtr(true),
					Include: []string{"aliang.*"},
				},
			},
		},
	}

	canonical, err := CompileCanonicalRoutingFromRuntimeInputs(cfg, model.RulesSettings{
		AliangEnabled: true,
		SocksEnabled:  false,
	})
	if err != nil {
		t.Fatalf("CompileCanonicalRoutingFromRuntimeInputs() error = %v", err)
	}

	rules := canonical.Routing.Rules
	if len(rules) != 1 {
		t.Fatalf("rules len = %d, want 1 (include 与内置允许清单同源去重), rules = %+v", len(rules), rules)
	}
	rule := rules[0]
	if rule.ID != "ai_allowlist_0" {
		t.Fatalf("rule ID = %q, want %q", rule.ID, "ai_allowlist_0")
	}
	if rule.Type != string(model.RuleTypeDomain) {
		t.Fatalf("rule Type = %q, want %q", rule.Type, model.RuleTypeDomain)
	}
	if rule.Condition != "aliang.*" {
		t.Fatalf("rule Condition = %q, want %q", rule.Condition, "aliang.*")
	}
	if rule.Target != string(SnapshotActionToAliang) {
		t.Fatalf("rule Target = %q, want %q", rule.Target, SnapshotActionToAliang)
	}
}

func TestRuntimeSnapshot_CompileProxyIPCIDRRuleAsIPType(t *testing.T) {
	cfg := &config.Config{
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Type:   "socks5",
				Server: "127.0.0.1:1080",
			},
			ProxyRules: []string{"ip-cidr,121.32.12.0/24,proxy"},
		},
	}

	canonical, err := CompileCanonicalRoutingFromRuntimeInputs(cfg, model.RulesSettings{
		AliangEnabled: false,
		SocksEnabled:  true,
	})
	if err != nil {
		t.Fatalf("CompileCanonicalRoutingFromRuntimeInputs() error = %v", err)
	}

	rules := canonical.Routing.Rules
	if len(rules) != 1 {
		t.Fatalf("rules len = %d, want 1, rules = %+v", len(rules), rules)
	}
	rule := rules[0]
	// 回归锁定：ip-cidr 条目必须编译为 Type=ip，不得再误编译为 domain
	if rule.Type != string(model.RuleTypeIP) {
		t.Fatalf("rule Type = %q, want %q (回归锁定：ip-cidr 不得误编译为 domain)", rule.Type, model.RuleTypeIP)
	}
	if rule.Condition != "121.32.12.0/24" {
		t.Fatalf("rule Condition = %q, want %q", rule.Condition, "121.32.12.0/24")
	}
	if rule.ID != "proxy_rule_0" {
		t.Fatalf("rule ID = %q, want %q", rule.ID, "proxy_rule_0")
	}
	if rule.Target != string(SnapshotActionToSocks) {
		t.Fatalf("rule Target = %q, want %q", rule.Target, SnapshotActionToSocks)
	}
}

func TestRuntimeSnapshot_CompileMixedEntriesIDNumberingAndDedupe(t *testing.T) {
	cfg := &config.Config{
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Type:   "socks5",
				Server: "127.0.0.1:1080",
			},
			AIRules: map[string]*config.CustomerAIRuleSetting{
				"claude": {
					Enble:   boolPtr(true),
					Include: []string{"claude.ai"},
				},
				"aliang": {
					Enble:   boolPtr(true),
					Include: []string{"aliang.*"},
				},
			},
			ProxyRules: []string{
				"ip-cidr,121.32.12.0/24,proxy",
				"domains,google.com,proxy",
				"domains,claude.ai,proxy", // 跨列表重复：被 ai_allowlist 同条件规则遮蔽，应剪除
				"ip-cidr,121.32.12.0/24,proxy", // 列表内重复：按 (kind, cond) 去重
			},
		},
	}

	canonical, err := CompileCanonicalRoutingFromRuntimeInputs(cfg, model.RulesSettings{
		AliangEnabled: true,
		SocksEnabled:  true,
	})
	if err != nil {
		t.Fatalf("CompileCanonicalRoutingFromRuntimeInputs() error = %v", err)
	}

	type wantRule struct {
		id        string
		ruleType  string
		condition string
		target    string
	}
	want := []wantRule{
		{"ai_allowlist_0", string(model.RuleTypeDomain), "aliang.*", string(SnapshotActionToAliang)},
		{"ai_allowlist_1", string(model.RuleTypeDomain), "claude.ai", string(SnapshotActionToAliang)},
		{"proxy_rule_0", string(model.RuleTypeIP), "121.32.12.0/24", string(SnapshotActionToSocks)},
		{"proxy_rule_1", string(model.RuleTypeDomain), "google.com", string(SnapshotActionToSocks)},
	}

	rules := canonical.Routing.Rules
	if len(rules) != len(want) {
		t.Fatalf("rules len = %d, want %d, rules = %+v", len(rules), len(want), rules)
	}
	for i, w := range want {
		got := rules[i]
		if got.ID != w.id || got.Type != w.ruleType || got.Condition != w.condition || got.Target != w.target {
			t.Fatalf("rules[%d] = {ID: %s, Type: %s, Condition: %s, Target: %s}, want {ID: %s, Type: %s, Condition: %s, Target: %s}",
				i, got.ID, got.Type, got.Condition, got.Target, w.id, w.ruleType, w.condition, w.target)
		}
	}
}

func TestRuntimeSnapshot_ProxyIPRuleDecidesRouteByIP(t *testing.T) {
	cfg := &config.Config{
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Type:   "socks5",
				Server: "127.0.0.1:1080",
			},
			ProxyRules: []string{"ip-cidr,121.32.12.0/24,proxy"},
		},
	}

	snapshot, err := CompileRuntimeSnapshotFromRuntimeInputs(cfg, model.RulesSettings{
		AliangEnabled: false,
		SocksEnabled:  true,
	})
	if err != nil {
		t.Fatalf("CompileRuntimeSnapshotFromRuntimeInputs() error = %v", err)
	}

	decision, err := DecideRouteFromSnapshot(snapshot, &MatchContext{Domain: "", IP: "121.32.12.5"})
	if err != nil {
		t.Fatalf("DecideRouteFromSnapshot() error = %v", err)
	}
	if decision != RouteToSocks {
		t.Fatalf("decision for in-range IP = %s, want %s", decision, RouteToSocks)
	}

	decision, err = DecideRouteFromSnapshot(snapshot, &MatchContext{Domain: "", IP: "121.32.13.1"})
	if err != nil {
		t.Fatalf("DecideRouteFromSnapshot() error = %v", err)
	}
	if decision != RouteDirect {
		t.Fatalf("decision for out-of-range IP = %s, want %s", decision, RouteDirect)
	}
}
