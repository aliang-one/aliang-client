package routing

import (
	"net"
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
				"domains,claude.ai,proxy",      // 跨列表重复：被 ai_allowlist 同条件规则遮蔽，应剪除
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

func TestRuntimeSnapshot_InvalidEntrySilentlySkipped(t *testing.T) {
	cfg := &config.Config{
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Type:   "socks5",
				Server: "127.0.0.1:1080",
			},
			ProxyRules: []string{
				"my_server.com",      // 下划线字符集非法：静默跳过，不产出规则
				"domains,google.com", // 合法条目：正常产出
			},
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
		t.Fatalf("rules len = %d, want 1 (非法条目应被静默跳过), rules = %+v", len(rules), rules)
	}
	if rules[0].Condition != "google.com" || rules[0].Type != string(model.RuleTypeDomain) {
		t.Fatalf("rules[0] = {Type: %s, Condition: %s}, want {Type: %s, Condition: google.com}",
			rules[0].Type, rules[0].Condition, model.RuleTypeDomain)
	}
}

func TestRuntimeSnapshot_CompileGeoIPRuleAsGeoIPType(t *testing.T) {
	cfg := &config.Config{
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Type:   "socks5",
				Server: "127.0.0.1:1080",
			},
			AIRules: map[string]*config.CustomerAIRuleSetting{
				"aliang": {
					Enble:   boolPtr(true),
					Include: []string{"aliang.*"},
				},
			},
			ProxyRules: []string{
				"domains,google.com,proxy",
				"geoip:CN+HK+US",
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
		{"proxy_rule_0", string(model.RuleTypeDomain), "google.com", string(SnapshotActionToSocks)},
		{"proxy_rule_1", string(model.RuleTypeGeoIP), "CN+HK+US", string(SnapshotActionToSocks)},
	}

	rules := canonical.Routing.Rules
	if len(rules) != len(want) {
		t.Fatalf("rules len = %d, want %d, rules = %+v", len(rules), len(want), rules)
	}
	geoipCount := 0
	for i, w := range want {
		got := rules[i]
		if got.ID != w.id || got.Type != w.ruleType || got.Condition != w.condition || got.Target != w.target {
			t.Fatalf("rules[%d] = {ID: %s, Type: %s, Condition: %s, Target: %s}, want {ID: %s, Type: %s, Condition: %s, Target: %s}",
				i, got.ID, got.Type, got.Condition, got.Target, w.id, w.ruleType, w.condition, w.target)
		}
		if got.Type == string(model.RuleTypeGeoIP) {
			geoipCount++
		}
	}
	if geoipCount != 1 {
		t.Fatalf("geoip rule count = %d, want 恰好 1 条 geoip 规则", geoipCount)
	}
}

func TestRuntimeSnapshot_CompileGeoIPClashStyleRegression(t *testing.T) {
	cfg := &config.Config{
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Type:   "socks5",
				Server: "127.0.0.1:1080",
			},
			ProxyRules: []string{"GEOIP,CN,proxy"},
		},
	}

	canonical, err := CompileCanonicalRoutingFromRuntimeInputs(cfg, model.RulesSettings{
		AliangEnabled: false,
		SocksEnabled:  false,
	})
	if err != nil {
		t.Fatalf("CompileCanonicalRoutingFromRuntimeInputs() error = %v", err)
	}

	rules := canonical.Routing.Rules
	if len(rules) != 1 {
		t.Fatalf("rules len = %d, want 1, rules = %+v", len(rules), rules)
	}
	rule := rules[0]
	// 回归锁定：Clash 风格 GEOIP,CN,proxy 必须编译为 Type=geoip，不得再误编译为 domain "CN"
	if rule.Type != string(model.RuleTypeGeoIP) {
		t.Fatalf("rule Type = %q, want %q (回归锁定：GEOIP,CN,proxy 不得误编译为 domain)", rule.Type, model.RuleTypeGeoIP)
	}
	if rule.Condition != "CN" {
		t.Fatalf("rule Condition = %q, want %q", rule.Condition, "CN")
	}
	if rule.Target != string(SnapshotActionDirect) {
		t.Fatalf("rule Target = %q, want %q (socks 关闭 → direct)", rule.Target, SnapshotActionDirect)
	}
}

func TestDecideRouteFromSnapshot_GeoIPConditionMatchesCaseInsensitively(t *testing.T) {
	// geoipLookup 是包级 seam（非并行安全）：替换后 t.Cleanup 恢复，本测试不加 t.Parallel。
	orig := geoipLookup
	t.Cleanup(func() { geoipLookup = orig })

	// geoip 条目放 AI include 面：AliangEnabled=true → target=toAliang，
	// 命中/不命中产生不同 Route（toAliang vs 默认 direct），保证断言非空洞。
	cfg := &config.Config{
		Customer: &config.CustomerConfig{
			AIRules: map[string]*config.CustomerAIRuleSetting{
				"aliang": {
					Enble:   boolPtr(true),
					Include: []string{"geoip:CN+HK"},
				},
			},
		},
	}

	snapshot, err := CompileRuntimeSnapshotFromRuntimeInputs(cfg, model.RulesSettings{
		AliangEnabled: true,
		SocksEnabled:  false,
	})
	if err != nil {
		t.Fatalf("CompileRuntimeSnapshotFromRuntimeInputs() error = %v", err)
	}

	rules := snapshot.Rules()
	if len(rules) != 1 {
		t.Fatalf("snapshot rules len = %d, want 1, rules = %+v", len(rules), rules)
	}
	// 快照层 condition 已小写化为 "cn+hk"——匹配必须大小写不敏感才能命中
	if rules[0].Condition() != "cn+hk" {
		t.Fatalf("snapshot condition = %q, want %q (快照层小写化)", rules[0].Condition(), "cn+hk")
	}

	// 命中实证：lookup 返回 HK，小写化 condition "cn+hk" 经 EqualFold 适配后命中 → RouteToAliang
	geoipLookup = func(ip net.IP) (string, error) { return "HK", nil }
	decision, err := DecideRouteFromSnapshot(snapshot, &MatchContext{Domain: "", IP: "1.2.3.4"})
	if err != nil {
		t.Fatalf("DecideRouteFromSnapshot() error = %v", err)
	}
	if decision != RouteToAliang {
		t.Fatalf("decision for HK IP = %s, want %s (geoip 命中实证)", decision, RouteToAliang)
	}

	// 不命中：lookup 返回 US → 走默认 direct
	geoipLookup = func(ip net.IP) (string, error) { return "US", nil }
	decision, err = DecideRouteFromSnapshot(snapshot, &MatchContext{Domain: "", IP: "1.2.3.4"})
	if err != nil {
		t.Fatalf("DecideRouteFromSnapshot() error = %v", err)
	}
	if decision != RouteDirect {
		t.Fatalf("decision for US IP = %s, want %s (不命中走默认)", decision, RouteDirect)
	}
}
