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
