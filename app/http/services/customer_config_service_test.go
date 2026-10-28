package services

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/storage"
	"aliang.one/nursorgate/common/model"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/routing"
	"aliang.one/nursorgate/processor/setup"
	"aliang.one/nursorgate/processor/statistic"
)

func TestMergeCustomerPayload_PreservesOmittedFields(t *testing.T) {
	baseCfg := &config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.aliang.one"},
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Type:   "http",
				Server: "127.0.0.1:8080",
			},
			AIRules: map[string]*config.CustomerAIRuleSetting{
				"openai": {
					Enble:    customerBoolPtr(true),
					Include:  []string{"api.openai.com"},
					Editable: customerBoolPtr(false),
				},
			},
			ProxyRules: []string{"domain,example.com,proxy"},
		},
	}

	mergedRaw, nextCfg, err := mergeCustomerPayload(baseCfg, []byte(`{
		"proxy":{"type":"socks5"},
		"ai_rules":{"openai":{"exclude":["chatgpt.com"]},"claude":{"enble":true}}
	}`))
	if err != nil {
		t.Fatalf("mergeCustomerPayload() error = %v", err)
	}

	if !strings.Contains(mergedRaw, `"server":"127.0.0.1:8080"`) {
		t.Fatalf("merged payload should preserve proxy.server, got %s", mergedRaw)
	}
	if nextCfg.Customer == nil || nextCfg.Customer.Proxy == nil {
		t.Fatalf("merged config missing customer.proxy: %+v", nextCfg.Customer)
	}
	if got := nextCfg.Customer.Proxy.Server; got != "127.0.0.1:8080" {
		t.Fatalf("proxy.server = %q, want preserved value", got)
	}
	if got := nextCfg.Customer.Proxy.Type; got != "socks5" {
		t.Fatalf("proxy.type = %q, want socks5", got)
	}
	if len(nextCfg.Customer.ProxyRules) != 1 {
		t.Fatalf("proxy_rules should remain unchanged, got %+v", nextCfg.Customer.ProxyRules)
	}

	openai := nextCfg.Customer.AIRules["openai"]
	if openai == nil {
		t.Fatalf("openai rule missing from merged config: %+v", nextCfg.Customer.AIRules)
	}
	if openai.Enble == nil || !*openai.Enble {
		t.Fatalf("openai.enble should be preserved as true, got %+v", openai.Enble)
	}
	if len(openai.Include) != 1 || openai.Include[0] != "api.openai.com" {
		t.Fatalf("openai.include = %v, want [api.openai.com]", openai.Include)
	}
	if openai.Editable == nil || *openai.Editable {
		t.Fatalf("openai.editable should be preserved as false, got %+v", openai.Editable)
	}

	claude := nextCfg.Customer.AIRules["claude"]
	if claude == nil || claude.Enble == nil || !*claude.Enble {
		t.Fatalf("claude.enble should be added as true, got %+v", claude)
	}
}

func TestMergeCustomerPayload_ModelMappingEnableRoundTrip(t *testing.T) {
	baseCfg := &config.Config{
		Core:     &config.CoreConfig{APIServer: "https://api.aliang.one"},
		Customer: &config.CustomerConfig{},
	}

	cases := []struct {
		name    string
		payload string
	}{
		{
			name:    "wrapped customer payload",
			payload: `{"customer":{"model_mapping":{"enable":true,"rules":{"src":"dst"}}}}`,
		},
		{
			name:    "direct customer payload",
			payload: `{"model_mapping":{"enable":true,"rules":{"src":"dst"}}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mergedRaw, nextCfg, err := mergeCustomerPayload(baseCfg, []byte(tc.payload))
			if err != nil {
				t.Fatalf("mergeCustomerPayload() error = %v", err)
			}
			if !strings.Contains(mergedRaw, `"enable":true`) {
				t.Fatalf("merged payload = %s, want model_mapping.enable true", mergedRaw)
			}
			if err := nextCfg.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}

			effective := nextCfg.EffectiveModelMapping()
			if effective == nil {
				t.Fatal("EffectiveModelMapping() = nil, want enabled config")
			}
			if got := effective.EffectiveRules()["src"]; got != "dst" {
				t.Fatalf("EffectiveRules()[src] = %q, want dst", got)
			}

			committedRaw, err := json.Marshal(nextCfg)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			if !strings.Contains(string(committedRaw), `"enable":true`) {
				t.Fatalf("committed payload = %s, want model_mapping.enable true", committedRaw)
			}
			if strings.Contains(string(committedRaw), `"enabled"`) {
				t.Fatalf("committed payload = %s, want no legacy model_mapping.enabled key", committedRaw)
			}

			var committedCfg config.Config
			if err := json.Unmarshal(committedRaw, &committedCfg); err != nil {
				t.Fatalf("decode committed payload: %v", err)
			}
			if got := committedCfg.EffectiveModelMapping(); got == nil {
				t.Fatal("committed EffectiveModelMapping() = nil, want enabled config")
			}
		})
	}
}

func TestMergeCustomerPayload_CustomEnvVarsRoundTrip(t *testing.T) {
	baseCfg := &config.Config{
		Core:     &config.CoreConfig{APIServer: "https://api.aliang.one"},
		Customer: &config.CustomerConfig{},
	}

	cases := []struct {
		name    string
		payload string
	}{
		{
			name:    "wrapped customer payload",
			payload: `{"customer":{"custom_env_vars":{"enable":true,"vars":{"ANTHROPIC_BASE_URL":"https://gw.example"}}}}`,
		},
		{
			name:    "direct customer payload",
			payload: `{"custom_env_vars":{"enable":true,"vars":{"ANTHROPIC_BASE_URL":"https://gw.example"}}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mergedRaw, nextCfg, err := mergeCustomerPayload(baseCfg, []byte(tc.payload))
			if err != nil {
				t.Fatalf("mergeCustomerPayload() error = %v", err)
			}
			if err := nextCfg.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			got := nextCfg.EffectiveCustomEnvVars()
			if got["ANTHROPIC_BASE_URL"] != "https://gw.example" {
				t.Fatalf("EffectiveCustomEnvVars()[ANTHROPIC_BASE_URL] = %q, want https://gw.example", got["ANTHROPIC_BASE_URL"])
			}
			if !strings.Contains(mergedRaw, "custom_env_vars") {
				t.Fatalf("merged payload missing custom_env_vars: %s", mergedRaw)
			}
		})
	}
}

func TestResolveBaseConfigForCustomerUpdate_PrefersStartupLocalConfigOverHomeConfig(t *testing.T) {
	config.ResetGlobalConfigForTest()
	config.ResetEffectiveConfigCommitCoordinatorForTest()
	t.Cleanup(func() {
		config.ResetGlobalConfigForTest()
		config.ResetEffectiveConfigCommitCoordinatorForTest()
		_ = os.Remove(startupLocalCustomerBaseConfigPath)
	})

	tempHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tempHome, ".aliang"), 0755); err != nil {
		t.Fatalf("mkdir temp home .aliang failed: %v", err)
	}
	t.Setenv("HOME", tempHome)

	if err := os.WriteFile(startupLocalCustomerBaseConfigPath, []byte(`{
		"core":{"api_server":"https://sub2api.liang.home"},
		"customer":{"proxy":{"type":"socks5","server":"127.0.0.1:1080"}}
	}`), 0644); err != nil {
		t.Fatalf("write startup local config failed: %v", err)
	}

	if err := os.WriteFile(filepath.Join(tempHome, ".aliang", "config.json"), []byte(`{
		"core":{"api_server":"https://api.aliang.one"},
		"customer":{"proxy":{"type":"http","server":"127.0.0.1:1081"}}
	}`), 0644); err != nil {
		t.Fatalf("write home config failed: %v", err)
	}

	baseCfg, err := resolveBaseConfigForCustomerUpdate()
	if err != nil {
		t.Fatalf("resolveBaseConfigForCustomerUpdate() error = %v", err)
	}
	if got := baseCfg.APIBaseURL(); got != "https://sub2api.liang.home" {
		t.Fatalf("APIBaseURL = %q, want startup local config value", got)
	}
}

func TestResolveCustomerConfigPersistPath_FallsBackToRuntimeDirWithoutHome(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("HOME", "")
	t.Setenv("ALIANG_DATA_DIR", runtimeDir)

	resolvedPath, err := resolveServiceConfigPathForMode(setup.RuntimeModeDaemon, customerConfigFilePath)
	if err != nil {
		t.Fatalf("resolveServiceConfigPathForMode() error = %v", err)
	}

	wantPath := filepath.Join(runtimeDir, "config.json")
	if resolvedPath != wantPath {
		t.Fatalf("resolved path = %q, want %q", resolvedPath, wantPath)
	}
}

func TestDisabledAIAccelerationDomains_ReturnsDisabledProviderDomains(t *testing.T) {
	previousCfg := &config.Config{
		Customer: &config.CustomerConfig{
			AIRules: map[string]*config.CustomerAIRuleSetting{
				"cursor": {
					Enble:   customerBoolPtr(true),
					Include: []string{"*.cursor.sh", "api.cursor.com"},
				},
				"openai": {
					Enble:   customerBoolPtr(true),
					Include: []string{"api.openai.com"},
				},
			},
		},
	}
	nextCfg := &config.Config{
		Customer: &config.CustomerConfig{
			AIRules: map[string]*config.CustomerAIRuleSetting{
				"cursor": {
					Enble:   customerBoolPtr(false),
					Include: []string{"*.cursor.sh", "api.cursor.com"},
				},
				"openai": {
					Enble:   customerBoolPtr(true),
					Include: []string{"api.openai.com"},
				},
			},
		},
	}

	domains := disabledAIAccelerationDomains(previousCfg, nextCfg)
	got := strings.Join(domains, ",")
	if !strings.Contains(got, "*.cursor.sh") || !strings.Contains(got, "api.cursor.com") {
		t.Fatalf("disabled domains = %v, want cursor domains", domains)
	}
	if strings.Contains(got, "api.openai.com") {
		t.Fatalf("disabled domains = %v, should not include still-enabled openai", domains)
	}
}

func TestCloseDisabledAIAcceleratedConnections_ClosesMatchingTrackedConnection(t *testing.T) {
	conn := &customerConfigCloseCountingConn{}
	_ = statistic.NewTCPTracker(conn, &M.Metadata{
		Route:    "RouteToALiang",
		HostName: "api.cursor.sh",
		ConnID:   "cursor-conn",
	}, statistic.DefaultManager)

	previousCfg := &config.Config{
		Customer: &config.CustomerConfig{
			AIRules: map[string]*config.CustomerAIRuleSetting{
				"cursor": {
					Enble:   customerBoolPtr(true),
					Include: []string{"*.cursor.sh"},
				},
			},
		},
	}
	nextCfg := &config.Config{
		Customer: &config.CustomerConfig{
			AIRules: map[string]*config.CustomerAIRuleSetting{
				"cursor": {
					Enble:   customerBoolPtr(false),
					Include: []string{"*.cursor.sh"},
				},
			},
		},
	}

	closeDisabledAIAcceleratedConnections(previousCfg, nextCfg)

	if conn.closeCount != 1 {
		t.Fatalf("tracked connection close count = %d, want 1", conn.closeCount)
	}
}

func TestReadConfigFromPath_FallsBackToRuntimeDirWithoutHome(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("HOME", "")
	t.Setenv("ALIANG_DATA_DIR", runtimeDir)

	configPath := filepath.Join(runtimeDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{
		"core":{"api_server":"https://daemon.example.com"},
		"customer":{"proxy":{"type":"socks5","server":"127.0.0.1:1080"}}
	}`), 0644); err != nil {
		t.Fatalf("write runtime config failed: %v", err)
	}

	fileCfg, found, err := readConfigFromPath(customerConfigFilePath)
	if err != nil {
		t.Fatalf("readConfigFromPath() error = %v", err)
	}
	if !found {
		t.Fatal("expected runtime config file to be found")
	}
	if got := fileCfg.APIBaseURL(); got != "https://daemon.example.com" {
		t.Fatalf("APIBaseURL = %q, want runtime config value", got)
	}
}

func TestUpdateCommittedCustomerConfig_RefreshesOutboundProxyRegistry(t *testing.T) {
	config.ResetGlobalConfigForTest()
	config.ResetEffectiveConfigCommitCoordinatorForTest()
	storage.ResetSoftwareConfigDBForTest()
	registry := outbound.GetRegistry()
	registry.Clear()
	t.Cleanup(func() {
		config.ResetGlobalConfigForTest()
		config.ResetEffectiveConfigCommitCoordinatorForTest()
		storage.ResetSoftwareConfigDBForTest()
		registry.Clear()
	})

	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("ALIANG_CACHE_DIR", filepath.Join(tempHome, ".aliang-cache"))

	baseCfg := &config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.aliang.one"},
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Enable:   customerBoolPtr(true),
				Type:     "socks5",
				Server:   "182.138.136.39:27751",
				Username: "clash",
				Password: "old-pass",
			},
			ProxyRules: []string{"domains,google.com"},
		},
	}
	config.SetGlobalConfig(baseCfg)
	if err := registry.RefreshCustomerProxy(baseCfg); err != nil {
		t.Fatalf("RefreshCustomerProxy(base) error = %v", err)
	}

	oldProxy, err := registry.Get("socks")
	if err != nil {
		t.Fatalf("registry.Get(\"socks\") error = %v", err)
	}
	if got := oldProxy.Addr(); got != "182.138.136.39:27751" {
		t.Fatalf("old proxy addr = %q, want 182.138.136.39:27751", got)
	}

	service := NewCustomerConfigService()
	if _, err := service.UpdateCommittedCustomerConfig([]byte(`{
		"customer": {
			"proxy": {
				"enable": true,
				"type": "http",
				"server": "cd.liangsqrt.com:27750",
				"username": "clash",
				"password": "asd123456"
			}
		}
	}`)); err != nil {
		t.Fatalf("UpdateCommittedCustomerConfig() error = %v", err)
	}

	if _, err := registry.Get("socks"); err == nil {
		t.Fatal("registry.Get(\"socks\") error = nil, want stale socks proxy removed")
	}
	httpProxy, err := registry.Get("http")
	if err != nil {
		t.Fatalf("registry.Get(\"http\") error = %v", err)
	}
	if got := httpProxy.Addr(); got != "cd.liangsqrt.com:27750" {
		t.Fatalf("http proxy addr = %q, want cd.liangsqrt.com:27750", got)
	}
}

func TestUpdateCommittedCustomerConfig_DisablingProxyRefreshesRoutingRuntime(t *testing.T) {
	config.ResetGlobalConfigForTest()
	config.ResetEffectiveConfigCommitCoordinatorForTest()
	config.ResetRoutingApplyStoreForTest()
	storage.ResetSoftwareConfigDBForTest()
	registry := outbound.GetRegistry()
	registry.Clear()
	t.Cleanup(func() {
		config.ResetGlobalConfigForTest()
		config.ResetEffectiveConfigCommitCoordinatorForTest()
		config.ResetRoutingApplyStoreForTest()
		storage.ResetSoftwareConfigDBForTest()
		registry.Clear()
	})

	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("ALIANG_CACHE_DIR", filepath.Join(tempHome, ".aliang-cache"))

	baseCfg := &config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.aliang.one"},
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Enable: customerBoolPtr(true),
				Type:   "socks5",
				Server: "127.0.0.1:1080",
			},
			ProxyRules: []string{"domains,google.com"},
		},
	}
	config.SetGlobalConfig(baseCfg)
	if err := registry.RefreshCustomerProxy(baseCfg); err != nil {
		t.Fatalf("RefreshCustomerProxy(base) error = %v", err)
	}

	seedCanonical, err := routing.CompileCanonicalRoutingFromRuntimeInputs(baseCfg, routingSwitches(true, true))
	if err != nil {
		t.Fatalf("CompileCanonicalRoutingFromRuntimeInputs(base) error = %v", err)
	}
	seedRaw, err := json.Marshal(seedCanonical)
	if err != nil {
		t.Fatalf("marshal seed canonical error = %v", err)
	}
	if _, err := config.GetRoutingApplyStore().Apply(seedRaw, func(canonical *config.CanonicalRoutingSchema) (any, error) {
		return routing.CompileRuntimeSnapshot(canonical)
	}); err != nil {
		t.Fatalf("seed routing apply error = %v", err)
	}

	service := NewCustomerConfigService()
	if _, err := service.UpdateCommittedCustomerConfig([]byte(`{
		"customer": {
			"proxy": {
				"enable": false
			}
		}
	}`)); err != nil {
		t.Fatalf("UpdateCommittedCustomerConfig() error = %v", err)
	}

	if _, err := registry.Get("socks"); err == nil {
		t.Fatal("registry.Get(\"socks\") error = nil, want disabled proxy removed")
	}
	activeCanonical := config.GetRoutingApplyStore().ActiveCanonicalSchema()
	if activeCanonical == nil {
		t.Fatal("active canonical routing schema is nil")
	}
	if activeCanonical.Egress.ToSocks.Enabled {
		t.Fatalf("active toSocks.enabled = true, want false after customer.proxy.enable=false")
	}
	if len(activeCanonical.Routing.Rules) != 1 {
		t.Fatalf("active routing rules = %d, want 1", len(activeCanonical.Routing.Rules))
	}
	if got := activeCanonical.Routing.Rules[0].Target; got != string(routing.SnapshotActionDirect) {
		t.Fatalf("proxy rule target = %q, want direct when customer proxy is disabled", got)
	}
}

func routingSwitches(aliangEnabled, socksEnabled bool) model.RulesSettings {
	return model.RulesSettings{
		AliangEnabled: aliangEnabled,
		SocksEnabled:  socksEnabled,
	}
}

// customer 配置保存不得重置 store 已有的 toSocks 开关：用户关闭的开关在
// customer 提交后必须保持关闭（即使上游代理仍配置且可用）。
func TestRefreshRoutingRuntimeAfterCustomerConfigCommit_PreservesDisabledToSocksSwitch(t *testing.T) {
	config.ResetGlobalConfigForTest()
	config.ResetRoutingApplyStoreForTest()
	t.Cleanup(func() {
		config.ResetGlobalConfigForTest()
		config.ResetRoutingApplyStoreForTest()
	})

	committedCfg := &config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.aliang.one"},
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Enable: customerBoolPtr(true),
				Type:   "socks5",
				Server: "127.0.0.1:1080",
			},
			ProxyRules: []string{"domains,google.com"},
		},
	}

	// 种子：store 已是 toSocks 关闭状态（用户上一次提交 /api/config/routing）
	seedCanonical, err := routing.CompileCanonicalRoutingFromRuntimeInputs(committedCfg, routingSwitches(true, false))
	if err != nil {
		t.Fatalf("CompileCanonicalRoutingFromRuntimeInputs(seed) error = %v", err)
	}
	// 编译器会把 SocksEnabled && hasToSocks 收敛成 egress 开关；显式关掉再落种子，
	// 确保种子状态就是「toSocks 关闭 + 上游已配」
	seedCanonical.Egress.ToSocks.Enabled = false
	seedRaw, err := json.Marshal(seedCanonical)
	if err != nil {
		t.Fatalf("marshal seed canonical error = %v", err)
	}
	if _, err := config.GetRoutingApplyStore().Apply(seedRaw, func(canonical *config.CanonicalRoutingSchema) (any, error) {
		return routing.CompileRuntimeSnapshot(canonical)
	}); err != nil {
		t.Fatalf("seed routing apply error = %v", err)
	}

	if err := refreshRoutingRuntimeAfterCustomerConfigCommit(committedCfg); err != nil {
		t.Fatalf("refreshRoutingRuntimeAfterCustomerConfigCommit() error = %v", err)
	}

	activeCanonical := config.GetRoutingApplyStore().ActiveCanonicalSchema()
	if activeCanonical == nil {
		t.Fatal("active canonical routing schema is nil")
	}
	if activeCanonical.Egress.ToSocks.Enabled {
		t.Fatal("toSocks.enabled = true after customer commit, want preserved false")
	}
}

// store 为空（首次保存）时沿用默认 true——与既有行为一致。
func TestRefreshRoutingRuntimeAfterCustomerConfigCommit_EmptyStoreDefaultsToSocksOn(t *testing.T) {
	config.ResetGlobalConfigForTest()
	config.ResetRoutingApplyStoreForTest()
	t.Cleanup(func() {
		config.ResetGlobalConfigForTest()
		config.ResetRoutingApplyStoreForTest()
	})

	committedCfg := &config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.aliang.one"},
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Enable: customerBoolPtr(true),
				Type:   "socks5",
				Server: "127.0.0.1:1080",
			},
		},
	}

	if err := refreshRoutingRuntimeAfterCustomerConfigCommit(committedCfg); err != nil {
		t.Fatalf("refreshRoutingRuntimeAfterCustomerConfigCommit() error = %v", err)
	}

	activeCanonical := config.GetRoutingApplyStore().ActiveCanonicalSchema()
	if activeCanonical == nil {
		t.Fatal("active canonical routing schema is nil")
	}
	if !activeCanonical.Egress.ToSocks.Enabled {
		t.Fatal("toSocks.enabled = false for empty store, want default true")
	}
}

// 存量配置文件里是小写 anthropic、patch 用大写 Anthropic 保存时，合并结果必须
// 只剩一个规范键，不得出现两张卡/两个键并存（deepMerge 按大小写敏感合并的缺陷）。
func TestMergeCustomerPayload_FoldsAIRuleCaseVariants(t *testing.T) {
	baseCfg := &config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.aliang.one"},
		Customer: &config.CustomerConfig{
			AIRules: map[string]*config.CustomerAIRuleSetting{
				"anthropic": {
					Enble:   customerBoolPtr(true),
					Include: []string{"api.anthropic.com"},
				},
			},
		},
	}

	mergedRaw, nextCfg, err := mergeCustomerPayload(baseCfg, []byte(`{
		"ai_rules":{"Anthropic":{"enble":false}}
	}`))
	if err != nil {
		t.Fatalf("mergeCustomerPayload() error = %v", err)
	}

	if nextCfg.Customer == nil {
		t.Fatal("merged config missing customer")
	}
	rules := nextCfg.Customer.AIRules
	if _, exists := rules["anthropic"]; exists {
		t.Fatalf("lowercase \"anthropic\" must be folded away, got keys %v", aiRuleKeys(rules))
	}
	rule := rules["Anthropic"]
	if rule == nil {
		t.Fatalf("canonical \"Anthropic\" missing, got keys %v", aiRuleKeys(rules))
	}
	if rule.Enble == nil || *rule.Enble {
		t.Fatalf("Anthropic.enble = %v, want false (patch value wins)", rule.Enble)
	}
	if len(rule.Include) != 1 || rule.Include[0] != "api.anthropic.com" {
		t.Fatalf("Anthropic.include = %v, want preserved [api.anthropic.com]", rule.Include)
	}
	if !strings.Contains(mergedRaw, `"Anthropic"`) || strings.Contains(mergedRaw, `"anthropic"`) {
		t.Fatalf("merged raw must contain only canonical key, got %s", mergedRaw)
	}
}

// 已停用的 vscode provider 不允许通过 customer patch 复活。
func TestMergeCustomerPayload_DropsRemovedVscodeProvider(t *testing.T) {
	baseCfg := &config.Config{
		Core:     &config.CoreConfig{APIServer: "https://api.aliang.one"},
		Customer: &config.CustomerConfig{},
	}

	_, nextCfg, err := mergeCustomerPayload(baseCfg, []byte(`{
		"ai_rules":{"vscode":{"enble":true,"include":["api.githubcopilot.com"]}}
	}`))
	if err != nil {
		t.Fatalf("mergeCustomerPayload() error = %v", err)
	}

	if _, exists := nextCfg.Customer.AIRules["vscode"]; exists {
		t.Fatalf("vscode provider must be dropped from merged config, got keys %v", aiRuleKeys(nextCfg.Customer.AIRules))
	}
}

// 已被历史版本污染（两种大小写并存）的 base 也必须在合并时折叠干净。
func TestMergeCustomerPayload_HealsPollutedBaseConfig(t *testing.T) {
	baseCfg := &config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.aliang.one"},
		Customer: &config.CustomerConfig{
			AIRules: map[string]*config.CustomerAIRuleSetting{
				"Anthropic": {Enble: customerBoolPtr(true), Include: []string{"api.aliang.one"}},
				"anthropic": {Enble: customerBoolPtr(false), Include: []string{"api.anthropic.com"}},
			},
		},
	}

	_, nextCfg, err := mergeCustomerPayload(baseCfg, []byte(`{"ai_rules":{}}`))
	if err != nil {
		t.Fatalf("mergeCustomerPayload() error = %v", err)
	}

	rules := nextCfg.Customer.AIRules
	if len(rules) != 1 {
		t.Fatalf("polluted base must fold to 1 provider, got keys %v", aiRuleKeys(rules))
	}
	rule := rules["Anthropic"]
	if rule == nil {
		t.Fatalf("canonical \"Anthropic\" missing, got keys %v", aiRuleKeys(rules))
	}
	if rule.Enble == nil || !*rule.Enble {
		t.Fatalf("Anthropic.enble = %v, want true (OR across variants)", rule.Enble)
	}
	if len(rule.Include) != 2 {
		t.Fatalf("Anthropic.include = %v, want union of both variants", rule.Include)
	}
}

// 已提交配置视图（GET /api/config/customer）必须返回折叠后的形态，
// 前端在首次保存前就能看到单张规范卡片。
func TestExtractCustomerFromConfigContent_NormalizesAIRules(t *testing.T) {
	customer, err := extractCustomerFromConfigContent(`{
		"customer": {
			"proxy": {"enable": true, "type": "socks5", "server": "127.0.0.1:1080"},
			"ai_rules": {
				"anthropic": {"enble": true, "include": ["api.anthropic.com"]},
				"Anthropic": {"enble": false},
				"vscode": {"enble": true, "include": ["api.githubcopilot.com"]}
			}
		}
	}`)
	if err != nil {
		t.Fatalf("extractCustomerFromConfigContent() error = %v", err)
	}

	rawRules, ok := customer["ai_rules"].(map[string]interface{})
	if !ok {
		t.Fatalf("ai_rules missing or wrong type: %T", customer["ai_rules"])
	}
	if _, exists := rawRules["anthropic"]; exists {
		t.Fatalf("lowercase \"anthropic\" must be folded in committed view, got keys %v", rawKeys(rawRules))
	}
	if _, exists := rawRules["vscode"]; exists {
		t.Fatalf("removed provider \"vscode\" must be stripped in committed view, got keys %v", rawKeys(rawRules))
	}
	rule, ok := rawRules["Anthropic"].(map[string]interface{})
	if !ok {
		t.Fatalf("canonical \"Anthropic\" missing, got keys %v", rawKeys(rawRules))
	}
	if enble, _ := rule["enble"].(bool); !enble {
		t.Fatalf("Anthropic.enble = %v, want true (OR across variants)", rule["enble"])
	}
	include, _ := rule["include"].([]interface{})
	if len(include) != 1 || include[0] != "api.anthropic.com" {
		t.Fatalf("Anthropic.include = %v, want preserved [api.anthropic.com]", include)
	}
}

// agentcore_llm 必须在可编辑白名单里,否则保存 400。
func TestValidateEditableCustomerKeysAllowsAgentCoreLLM(t *testing.T) {
	raw := map[string]json.RawMessage{
		"agentcore_llm": json.RawMessage(`{"enabled":true,"base_url":"http://127.0.0.1:8082"}`),
	}
	if err := validateEditableCustomerKeys(raw); err != nil {
		t.Fatalf("agentcore_llm 应放行, got %v", err)
	}
	err := validateEditableCustomerKeys(map[string]json.RawMessage{
		"no_such_key": json.RawMessage(`{}`),
	})
	if err == nil || !strings.Contains(err.Error(), "agentcore_llm") {
		t.Errorf("拒绝文案应含更新后的字段清单, got %v", err)
	}
}

// 保存通道 400:agentcore_llm 远端+无 key 必须在保存时被 Config.Validate 拒绝,
// 不能出现「保存成功、回合时静默整层关闭」的形态。
func TestSaveCustomerConfigRejectsAgentCoreLLMRemoteNoKey(t *testing.T) {
	config.ResetGlobalConfigForTest()
	config.ResetEffectiveConfigCommitCoordinatorForTest()
	storage.ResetSoftwareConfigDBForTest()
	t.Cleanup(func() {
		config.ResetGlobalConfigForTest()
		config.ResetEffectiveConfigCommitCoordinatorForTest()
		storage.ResetSoftwareConfigDBForTest()
	})

	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("ALIANG_CACHE_DIR", filepath.Join(tempHome, ".aliang-cache"))

	config.SetGlobalConfig(&config.Config{
		Core:     &config.CoreConfig{APIServer: "https://api.aliang.one"},
		Customer: &config.CustomerConfig{},
	})

	service := NewCustomerConfigService()
	_, err := service.UpdateCommittedCustomerConfig([]byte(`{
		"customer": {
			"agentcore_llm": {"enabled": true, "base_url": "https://gw.example.com"}
		}
	}`))
	if err == nil {
		t.Fatal("agentcore_llm 远端+无 key 应保存失败, got nil")
	}
	if !strings.Contains(err.Error(), "agentcore_llm.api_key") {
		t.Fatalf("错误信息应含 agentcore_llm.api_key, got %v", err)
	}
}

func aiRuleKeys(rules map[string]*config.CustomerAIRuleSetting) []string {
	keys := make([]string, 0, len(rules))
	for key := range rules {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func rawKeys(raw map[string]interface{}) []string {
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func customerBoolPtr(v bool) *bool {
	return &v
}

type customerConfigCloseCountingConn struct {
	closeCount int
}

func (c *customerConfigCloseCountingConn) Read([]byte) (int, error)         { return 0, nil }
func (c *customerConfigCloseCountingConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *customerConfigCloseCountingConn) Close() error                     { c.closeCount++; return nil }
func (c *customerConfigCloseCountingConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *customerConfigCloseCountingConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *customerConfigCloseCountingConn) SetDeadline(time.Time) error      { return nil }
func (c *customerConfigCloseCountingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *customerConfigCloseCountingConn) SetWriteDeadline(time.Time) error { return nil }
