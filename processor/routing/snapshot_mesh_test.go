package routing

import (
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/common/model"
	"aliang.one/nursorgate/processor/config"
)

// sshIngressOn 与 capableMesh 构造满足编译期能力判据(SPEC §13.2:SSH 入口
// 开启 + 本机身份私钥 + 对端已钉扎 host_key)的配置块。判据升级前 mesh 编译
// 测试的 fixture 只搭「总开关+peer 存在」,升级后不补齐即被 fail-closed 静默
// 跳过;本组 helper 让既有用例只需表达「要测的变量」,能力三件套统一补齐。
func sshIngressOn() *config.SSHIngressConfig {
	return &config.SSHIngressConfig{Enable: boolPtr(true)}
}

func capableMesh(peers ...config.MeshPeer) *config.MeshConfig {
	mesh := &config.MeshConfig{PrivateKey: "test-identity-key", Peers: peers}
	for i := range mesh.Peers {
		if strings.TrimSpace(mesh.Peers[i].HostKey) == "" {
			mesh.Peers[i].HostKey = "ssh-ed25519 AAAA test-pin"
		}
	}
	return mesh
}

func TestSplitMeshTail(t *testing.T) {
	cases := []struct {
		name         string
		raw          string
		wantBase     string
		wantDeviceID string
		wantPolicy   string
		wantState    meshTailState
	}{
		{"域名尾段", "domains,corp.com,mesh:dev-b", "domains,corp.com", "dev-b", "", meshTailParsed},
		{"IP 尾段", "ip-cidr,10.0.0.0/8,mesh:dev-b", "ip-cidr,10.0.0.0/8", "dev-b", "", meshTailParsed},
		{"dns 尾段", "dns,mesh:dev-b", "dns", "dev-b", "", meshTailParsed},
		{"MESH 大小写前缀敏感段", "dns,MESH:dev-b", "dns", "dev-b", "", meshTailParsed},
		{"deviceID 保留大小写", "dns,mesh:Dev-B", "dns", "Dev-B", "", meshTailParsed},
		{"段内空白容忍", "dns, mesh:dev-b ", "dns", "dev-b", "", meshTailParsed},
		// Task 7 多段尾策略 grammar:恰一个策略段 direct|deny|fallback:<id>。
		{"缺省策略段等价 direct", "domains,corp.com,mesh:dev-b,direct", "domains,corp.com", "dev-b", "direct", meshTailParsed},
		{"deny 策略段", "domains,corp.com,mesh:dev-b,deny", "domains,corp.com", "dev-b", "deny", meshTailParsed},
		{"fallback 策略段", "domains,corp.com,mesh:dev-b,fallback:dev-c", "domains,corp.com", "dev-b", "fallback:dev-c", meshTailParsed},
		{"策略关键字大小写不敏感", "dns,mesh:dev-b,DIRECT", "dns", "dev-b", "direct", meshTailParsed},
		{"FALLBACK 关键字大小写不敏感", "dns,mesh:dev-b,FALLBACK:dev-c", "dns", "dev-b", "fallback:dev-c", meshTailParsed},
		{"fallback 目标保留大小写", "dns,mesh:dev-b,fallback:Dev-C", "dns", "dev-b", "fallback:Dev-C", meshTailParsed},
		{"fallback 目标空白容忍", "dns,mesh:dev-b,fallback: dev-c ", "dns", "dev-b", "fallback:dev-c", meshTailParsed},
		{"未知策略段拒绝", "domains,corp.com,mesh:dev-b,proxy", "", "", "", meshTailInvalid},
		{"fallback 目标为空拒绝", "domains,corp.com,mesh:dev-b,fallback:", "", "", "", meshTailInvalid},
		{"fallback 缺目标冒号拒绝", "domains,corp.com,mesh:dev-b,fallback", "", "", "", meshTailInvalid},
		{"两个策略段拒绝", "domains,corp.com,mesh:dev-b,direct,deny", "", "", "", meshTailInvalid},
		{"尾逗号空段拒绝", "domains,corp.com,mesh:dev-b,", "", "", "", meshTailInvalid},
		{"中段 mesh 拒绝", "domains,mesh:dev-b,10.0.0.0/8", "", "", "", meshTailInvalid},
		{"空设备拒绝", "domains,corp.com,mesh:", "", "", "", meshTailInvalid},
		{"裸 mesh 段空 base 拒绝", "mesh:dev-b", "", "", "", meshTailInvalid},
		{"非 mesh 条目", "domains,corp.com,proxy", "domains,corp.com,proxy", "", "", meshTailNone},
		{"无逗号非 mesh 条目", "google.com", "google.com", "", "", meshTailNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, deviceID, policy, state := splitMeshTail(tc.raw)
			if base != tc.wantBase || deviceID != tc.wantDeviceID || policy != tc.wantPolicy || state != tc.wantState {
				t.Fatalf("splitMeshTail(%q) = (%q, %q, %q, %d), want (%q, %q, %q, %d)",
					tc.raw, base, deviceID, policy, state,
					tc.wantBase, tc.wantDeviceID, tc.wantPolicy, tc.wantState)
			}
		})
	}
}

// TestRuntimeSnapshot_CompileMeshPolicyTailForms 四形态规则源编译(Task 7
// grammar):缺省/direct/deny/fallback:dev-c 分别产出带策略段的 toNode 目标,
// 且决策层整串透传(前缀识别,策略段随 decision 抵达执行层)。
func TestRuntimeSnapshot_CompileMeshPolicyTailForms(t *testing.T) {
	cfg := &config.Config{
		Customer: &config.CustomerConfig{
			ProxyRules: []string{
				"domains,default.corp,mesh:dev-b",
				"domains,direct.corp,mesh:dev-b,direct",
				"domains,deny.corp,mesh:dev-b,deny",
				"domains,fb.corp,mesh:dev-b,fallback:dev-c",
			},
			SSHIngress: sshIngressOn(),
			Mesh:       capableMesh(config.MeshPeer{DeviceID: "dev-b"}, config.MeshPeer{DeviceID: "dev-c"}),
		},
	}

	canonical, err := CompileCanonicalRoutingFromRuntimeInputs(cfg, model.RulesSettings{})
	if err != nil {
		t.Fatalf("CompileCanonicalRoutingFromRuntimeInputs() error = %v", err)
	}

	want := []struct{ condition, target string }{
		{"default.corp", "toNode:dev-b"},
		{"direct.corp", "toNode:dev-b:direct"},
		{"deny.corp", "toNode:dev-b:deny"},
		{"fb.corp", "toNode:dev-b:fallback:dev-c"},
	}
	rules := canonical.Routing.Rules
	if len(rules) != len(want) {
		t.Fatalf("rules len = %d, want %d, rules = %+v", len(rules), len(want), rules)
	}
	for i, w := range want {
		if rules[i].Condition != w.condition || rules[i].Target != w.target {
			t.Fatalf("rules[%d] = {Condition: %s, Target: %s}, want {Condition: %s, Target: %s}",
				i, rules[i].Condition, rules[i].Target, w.condition, w.target)
		}
	}

	// 决策层整串透传:策略段不剥落,由链头执行层(processor/tcp dialByRoute)解析。
	snapshot, err := CompileRuntimeSnapshotFromRuntimeInputs(cfg, model.RulesSettings{})
	if err != nil {
		t.Fatalf("CompileRuntimeSnapshotFromRuntimeInputs() error = %v", err)
	}
	cases := []struct{ domain, wantDecision string }{
		{"default.corp", "toNode:dev-b"},
		{"direct.corp", "toNode:dev-b:direct"},
		{"deny.corp", "toNode:dev-b:deny"},
		{"fb.corp", "toNode:dev-b:fallback:dev-c"},
	}
	for _, tc := range cases {
		decision, err := DecideRouteFromSnapshot(snapshot, &MatchContext{Domain: tc.domain})
		if err != nil {
			t.Fatalf("DecideRouteFromSnapshot(%s) error = %v", tc.domain, err)
		}
		if decision != RouteDecision(tc.wantDecision) {
			t.Fatalf("decision(%s) = %s, want %s (mesh 策略段整串透传)", tc.domain, decision, tc.wantDecision)
		}
	}
}

// TestRuntimeSnapshot_MeshMultiSegmentTailRejected 多段尾止血(Task 4)在
// Task 7 grammar 下的新边界:恰一个「已知策略段」放行,未知策略/第二段/空段
// 仍整条静默跳过——尤其不得被静默错编译成普通代理规则(domains 前缀分支会
// 吞掉 mesh: 段)。
func TestRuntimeSnapshot_MeshMultiSegmentTailRejected(t *testing.T) {
	cfg := &config.Config{
		Customer: &config.CustomerConfig{
			ProxyRules: []string{
				"domains,corp.com,mesh:dev-b,bogus", // 未知策略段:整条跳过
				"domains,corp.com,mesh:dev-b,",      // 尾逗号空段:整条跳过
				"domains,ok.com,mesh:dev-b",         // 合法形态对照:正常产出
			},
			SSHIngress: sshIngressOn(),
			Mesh:       capableMesh(config.MeshPeer{DeviceID: "dev-b"}),
		},
	}

	canonical, err := CompileCanonicalRoutingFromRuntimeInputs(cfg, model.RulesSettings{})
	if err != nil {
		t.Fatalf("CompileCanonicalRoutingFromRuntimeInputs() error = %v", err)
	}

	rules := canonical.Routing.Rules
	if len(rules) != 1 {
		t.Fatalf("rules len = %d, want 1 (未知策略段/尾逗号 mesh 条目应整条静默跳过), rules = %+v", len(rules), rules)
	}
	if rules[0].Condition != "ok.com" || rules[0].Target != "toNode:dev-b" {
		t.Fatalf("rules[0] = {Condition: %s, Target: %s}, want {Condition: ok.com, Target: toNode:dev-b}",
			rules[0].Condition, rules[0].Target)
	}
}

func TestRuntimeSnapshot_CompileMeshForwardRule(t *testing.T) {
	cfg := &config.Config{
		Customer: &config.CustomerConfig{
			ProxyRules: []string{
				"domains,internal.corp,mesh:dev-b",
				"ip-cidr,10.0.0.0/8,mesh:dev-b",
				"dns,mesh:dev-b",
			},
			SSHIngress: sshIngressOn(),
			Mesh:       capableMesh(config.MeshPeer{DeviceID: "dev-b"}),
		},
	}

	canonical, err := CompileCanonicalRoutingFromRuntimeInputs(cfg, model.RulesSettings{})
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
		{"proxy_rule_0", string(model.RuleTypeDomain), "internal.corp", "toNode:dev-b"},
		{"proxy_rule_1", string(model.RuleTypeIP), "10.0.0.0/8", "toNode:dev-b"},
		{"proxy_rule_2", "dns", "all", "toNode:dev-b"},
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

	snapshot, err := CompileRuntimeSnapshotFromRuntimeInputs(cfg, model.RulesSettings{})
	if err != nil {
		t.Fatalf("CompileRuntimeSnapshotFromRuntimeInputs() error = %v", err)
	}
	decision, err := DecideRouteFromSnapshot(snapshot, &MatchContext{Domain: "internal.corp"})
	if err != nil {
		t.Fatalf("DecideRouteFromSnapshot() error = %v", err)
	}
	if decision != RouteDecision("toNode:dev-b") {
		t.Fatalf("decision = %s, want toNode:dev-b (mesh 域名规则命中透传)", decision)
	}
}

func TestRuntimeSnapshot_MeshTargetUnknownPeerRejected(t *testing.T) {
	cfg := &config.Config{
		Customer: &config.CustomerConfig{
			ProxyRules: []string{
				"domains,internal.corp,mesh:dev-xyz", // 未知 peer:编译期静默跳过
				"domains,known.com,mesh:dev-b",       // 已知 peer:正常产出
			},
			SSHIngress: sshIngressOn(),
			Mesh:       capableMesh(config.MeshPeer{DeviceID: "dev-b"}),
		},
	}

	canonical, err := CompileCanonicalRoutingFromRuntimeInputs(cfg, model.RulesSettings{})
	if err != nil {
		t.Fatalf("CompileCanonicalRoutingFromRuntimeInputs() error = %v", err)
	}

	rules := canonical.Routing.Rules
	if len(rules) != 1 {
		t.Fatalf("rules len = %d, want 1 (未知 peer 的 mesh 条目应编译期静默跳过), rules = %+v", len(rules), rules)
	}
	if rules[0].Condition != "known.com" || rules[0].Target != "toNode:dev-b" {
		t.Fatalf("rules[0] = {Condition: %s, Target: %s}, want {Condition: known.com, Target: toNode:dev-b}",
			rules[0].Condition, rules[0].Target)
	}
}

func TestRuntimeSnapshot_CompileMeshDNSRuleFirstWins(t *testing.T) {
	cfg := &config.Config{
		Customer: &config.CustomerConfig{
			ProxyRules: []string{
				"dns,mesh:dev-b",
				"dns,mesh:dev-c",
			},
			SSHIngress: sshIngressOn(),
			Mesh:       capableMesh(config.MeshPeer{DeviceID: "dev-b"}, config.MeshPeer{DeviceID: "dev-c"}),
		},
	}

	canonical, err := CompileCanonicalRoutingFromRuntimeInputs(cfg, model.RulesSettings{})
	if err != nil {
		t.Fatalf("CompileCanonicalRoutingFromRuntimeInputs() error = %v", err)
	}

	rules := canonical.Routing.Rules
	if len(rules) != 2 {
		t.Fatalf("rules len = %d, want 2 (两条 dns 指向不同 peer 都保留), rules = %+v", len(rules), rules)
	}
	if rules[0].Type != "dns" || rules[0].Condition != "all" || rules[0].Target != "toNode:dev-b" {
		t.Fatalf("rules[0] = {Type: %s, Condition: %s, Target: %s}, want {Type: dns, Condition: all, Target: toNode:dev-b}",
			rules[0].Type, rules[0].Condition, rules[0].Target)
	}
	if rules[1].Type != "dns" || rules[1].Condition != "all" || rules[1].Target != "toNode:dev-c" {
		t.Fatalf("rules[1] = {Type: %s, Condition: %s, Target: %s}, want {Type: dns, Condition: all, Target: toNode:dev-c}",
			rules[1].Type, rules[1].Condition, rules[1].Target)
	}

	// dns 规则不参与连接级决策:决策层对 dns 规则一律不命中,仅有 dns 规则
	// 可命中时 TCP 决策落默认 direct。DNS 代转发目标由 Task 8 的
	// meshDNSForwardTarget 直接扫快照规则取首条启用项,不走 DecideRouteFromSnapshot。
	snapshot, err := CompileRuntimeSnapshotFromRuntimeInputs(cfg, model.RulesSettings{})
	if err != nil {
		t.Fatalf("CompileRuntimeSnapshotFromRuntimeInputs() error = %v", err)
	}
	decision, err := DecideRouteFromSnapshot(snapshot, &MatchContext{})
	if err != nil {
		t.Fatalf("DecideRouteFromSnapshot() error = %v", err)
	}
	if decision != RouteDirect {
		t.Fatalf("decision = %s, want %s (dns 规则不影响 TCP 决策,仅 dns 可命中时落默认)", decision, RouteDirect)
	}
}

func TestDecideRouteFromSnapshot_MeshDNSRuleSkippedInTCPDecision(t *testing.T) {
	// dns 规则排在域名规则之前也不得劫持连接级决策:first-match 对 dns 规则
	// 一律不命中,由后续域名规则命中;DNS 代转发目标由 Task 8 的
	// meshDNSForwardTarget 直接扫快照规则取首条启用项,不走 DecideRouteFromSnapshot。
	cfg := &config.Config{
		Customer: &config.CustomerConfig{
			ProxyRules: []string{
				"dns,mesh:dev-c",
				"domains,internal.corp,mesh:dev-b",
			},
			SSHIngress: sshIngressOn(),
			Mesh:       capableMesh(config.MeshPeer{DeviceID: "dev-b"}, config.MeshPeer{DeviceID: "dev-c"}),
		},
	}

	snapshot, err := CompileRuntimeSnapshotFromRuntimeInputs(cfg, model.RulesSettings{})
	if err != nil {
		t.Fatalf("CompileRuntimeSnapshotFromRuntimeInputs() error = %v", err)
	}

	decision, err := DecideRouteFromSnapshot(snapshot, &MatchContext{Domain: "internal.corp"})
	if err != nil {
		t.Fatalf("DecideRouteFromSnapshot() error = %v", err)
	}
	if decision != RouteDecision("toNode:dev-b") {
		t.Fatalf("decision = %s, want toNode:dev-b (dns 排前不劫持,由域名规则命中)", decision)
	}
}

// TestMeshDNSForwardTargetFromSnapshot Task 8 门控派生(design §6 层1 规则
// 驱动):启用的 dns 规则 + peer 配有 LANAddrs → (deviceID, lanAddrs, true);
// 无规则/无 LANAddrs → false(不激活,现状直连透传 fail-open);多条取首条。
func TestMeshDNSForwardTargetFromSnapshot(t *testing.T) {
	meshDNSCfg := func(proxyRules []string, peers ...config.MeshPeer) *config.Config {
		return &config.Config{
			Customer: &config.CustomerConfig{
				ProxyRules: proxyRules,
				SSHIngress: sshIngressOn(),
				Mesh:       capableMesh(peers...),
			},
		}
	}

	t.Run("dns 规则命中返回 peer 与 LANAddrs", func(t *testing.T) {
		cfg := meshDNSCfg([]string{"dns,mesh:dev-b"},
			config.MeshPeer{DeviceID: "dev-b", LANAddrs: []string{"192.168.1.20:9443"}})
		id, addrs, ok := meshDNSForwardTargetFromInputs(cfg, model.RulesSettings{})
		if !ok || id != "dev-b" {
			t.Fatalf("meshDNSForwardTargetFromInputs = (%q, %v, %v), want (dev-b, ..., true)", id, addrs, ok)
		}
		if len(addrs) != 1 || addrs[0] != "192.168.1.20:9443" {
			t.Fatalf("lanAddrs = %v, want [192.168.1.20:9443]", addrs)
		}
	})

	t.Run("无 dns 规则不激活", func(t *testing.T) {
		cfg := meshDNSCfg([]string{"domains,x.com"},
			config.MeshPeer{DeviceID: "dev-b", LANAddrs: []string{"192.168.1.20:9443"}})
		if _, _, ok := meshDNSForwardTargetFromInputs(cfg, model.RulesSettings{}); ok {
			t.Fatal("无 dns 规则时不应激活代转发")
		}
	})

	t.Run("relay-only peer 无 LANAddrs 也激活(SPEC §13.2 ③)", func(t *testing.T) {
		// 跨 NAT 形态:peer 无 LAN 地址,交 Transport 选 piko 载体,不再被
		// len(LANAddrs)==0 静默关闭 DNS 代转发。
		cfg := meshDNSCfg([]string{"dns,mesh:dev-b"}, config.MeshPeer{DeviceID: "dev-b", Relay: boolPtr(true)})
		id, addrs, ok := meshDNSForwardTargetFromInputs(cfg, model.RulesSettings{})
		if !ok || id != "dev-b" {
			t.Fatalf("relay-only peer = (%q, %v, %v), want (dev-b, ..., true)", id, addrs, ok)
		}
		if len(addrs) != 0 {
			t.Fatalf("relay-only peer 无 LAN 载体, lanAddrs = %v, want 空", addrs)
		}
	})

	t.Run("多条 dns 规则取首条", func(t *testing.T) {
		cfg := meshDNSCfg([]string{"dns,mesh:dev-b", "dns,mesh:dev-c"},
			config.MeshPeer{DeviceID: "dev-b", LANAddrs: []string{"192.168.1.20:9443"}},
			config.MeshPeer{DeviceID: "dev-c", LANAddrs: []string{"192.168.1.30:9443"}})
		id, _, ok := meshDNSForwardTargetFromInputs(cfg, model.RulesSettings{})
		if !ok || id != "dev-b" {
			t.Fatalf("first-wins 违约: got (%q, %v), want dev-b", id, ok)
		}
	})

	t.Run("策略尾目标剥尾取 deviceID", func(t *testing.T) {
		cfg := meshDNSCfg([]string{"dns,mesh:dev-b,direct"},
			config.MeshPeer{DeviceID: "dev-b", LANAddrs: []string{"192.168.1.20:9443"}})
		id, _, ok := meshDNSForwardTargetFromInputs(cfg, model.RulesSettings{})
		if !ok || id != "dev-b" {
			t.Fatalf("策略尾形态 = (%q, %v), want (dev-b, true)", id, ok)
		}
	})

	t.Run("禁用 dns 规则跳过取首条启用", func(t *testing.T) {
		// 快照级直测 Enabled 过滤:runtime-inputs 编译产物恒启用,落盘 canonical
		// 回放可带禁用条目,门控语义按「首条启用」钉死。
		cfg := meshDNSCfg(nil,
			config.MeshPeer{DeviceID: "dev-b", LANAddrs: []string{"192.168.1.20:9443"}},
			config.MeshPeer{DeviceID: "dev-c", LANAddrs: []string{"192.168.1.30:9443"}})
		snapshot := NewRuntimeSnapshotForDecision(
			NewSnapshotBranchCapabilities(true, false, false),
			[]SnapshotRule{
				NewSnapshotRule("r0", "dns", "all", false, SnapshotAction("toNode:dev-b")),
				NewSnapshotRule("r1", "dns", "all", true, SnapshotAction("toNode:dev-c")),
			},
			SnapshotActionDirect, false)
		id, _, ok := meshDNSForwardTargetFromSnapshot(snapshot, cfg)
		if !ok || id != "dev-c" {
			t.Fatalf("disabled 过滤违约: got (%q, %v), want dev-c", id, ok)
		}
	})

	t.Run("畸形 toNode 目标跳过取下一条", func(t *testing.T) {
		cfg := meshDNSCfg(nil,
			config.MeshPeer{DeviceID: "dev-c", LANAddrs: []string{"192.168.1.30:9443"}})
		snapshot := NewRuntimeSnapshotForDecision(
			NewSnapshotBranchCapabilities(true, false, false),
			[]SnapshotRule{
				NewSnapshotRule("r0", "dns", "all", true, SnapshotAction("toNode:")),
				NewSnapshotRule("r1", "dns", "all", true, SnapshotAction("toNode:dev-c")),
			},
			SnapshotActionDirect, false)
		id, _, ok := meshDNSForwardTargetFromSnapshot(snapshot, cfg)
		if !ok || id != "dev-c" {
			t.Fatalf("畸形目标应跳过: got (%q, %v), want dev-c", id, ok)
		}
	})

	t.Run("mesh 总开关关闭不激活", func(t *testing.T) {
		// 其余能力三件套齐备,单独钉死总开关这一变量。
		disabled := false
		cfg := &config.Config{
			Customer: &config.CustomerConfig{
				ProxyRules: []string{"dns,mesh:dev-b"},
				SSHIngress: sshIngressOn(),
				Mesh: &config.MeshConfig{
					Enable:     &disabled,
					PrivateKey: "test-identity-key",
					Peers:      []config.MeshPeer{{DeviceID: "dev-b", HostKey: "test-pin", LANAddrs: []string{"192.168.1.20:9443"}}},
				},
			},
		}
		if _, _, ok := meshDNSForwardTargetFromInputs(cfg, model.RulesSettings{}); ok {
			t.Fatal("mesh 总开关关闭时编译期即无 dns 规则,不应激活")
		}
	})

	t.Run("nil cfg 不激活", func(t *testing.T) {
		if _, _, ok := meshDNSForwardTargetFromInputs(nil, model.RulesSettings{}); ok {
			t.Fatal("nil cfg 不应激活")
		}
	})
}

// TestMeshDNSForwardTargetMemo10s 门控 memo:10s 窗口内不重编译(fail-open 的
// 热更延迟由 TTL 界定),过期或重置后按新配置派生。
func TestMeshDNSForwardTargetMemo10s(t *testing.T) {
	origTTL := meshDNSForwardMemoTTL
	origCfg := config.GetGlobalConfig()
	defer func() {
		meshDNSForwardMemoTTL = origTTL
		resetMeshDNSForwardMemo()
		config.SetGlobalConfig(origCfg)
	}()
	meshDNSForwardMemoTTL = time.Hour
	resetMeshDNSForwardMemo()

	withRule := &config.Config{
		Customer: &config.CustomerConfig{
			ProxyRules: []string{"dns,mesh:dev-b"},
			SSHIngress: sshIngressOn(),
			Mesh: capableMesh(config.MeshPeer{
				DeviceID: "dev-b", LANAddrs: []string{"192.168.1.20:9443"},
			}),
		},
	}
	withoutRule := &config.Config{
		Customer: &config.CustomerConfig{
			ProxyRules: []string{"domains,x.com"},
			SSHIngress: sshIngressOn(),
			Mesh: capableMesh(config.MeshPeer{
				DeviceID: "dev-b", LANAddrs: []string{"192.168.1.20:9443"},
			}),
		},
	}

	config.SetGlobalConfig(withRule)
	id, _, ok := MeshDNSForwardTarget()
	if !ok || id != "dev-b" {
		t.Fatalf("首次调用 = (%q, %v), want (dev-b, true)", id, ok)
	}

	// TTL 内配置变化不可见(memo 命中,热更容忍 10s 延迟的语义边界)。
	config.SetGlobalConfig(withoutRule)
	id, _, ok = MeshDNSForwardTarget()
	if !ok || id != "dev-b" {
		t.Fatalf("TTL 内应命中 memo: got (%q, %v), want (dev-b, true)", id, ok)
	}

	// 重置(等价过期)后按新配置派生。
	resetMeshDNSForwardMemo()
	if _, _, ok := MeshDNSForwardTarget(); ok {
		t.Fatal("重置后无 dns 规则应返回 false")
	}
}

func TestSnapshotTargetMeshPrefixMirrorsConfigConstant(t *testing.T) {
	// 镜像常量钉死(M1):config 不能反向 import routing(import 成环),
	// 两侧常量必须同值——编译产物形态(toNode:)与 schema 白名单形态由
	// 本断言锁死,任一侧漂移即红。
	if SnapshotTargetMeshPrefix != config.MeshRuleTargetPrefix {
		t.Fatalf("SnapshotTargetMeshPrefix = %q, config.MeshRuleTargetPrefix = %q, want 同值",
			SnapshotTargetMeshPrefix, config.MeshRuleTargetPrefix)
	}
}

// TestMeshForwardAvailableSSHDependency 编译期能力判据升级(SPEC §13.2 ①②):
// SSH 入口依赖降级(MeshEffectivelyEnabled)穿透到编译期——ssh 未启用即降级
// 不可用;本机无身份私钥不能作发起方;对端未钉扎 host_key 能力不成立
// (fail-closed,规则条目静默跳过而非编译出拨不通的 toNode 规则)。
func TestMeshForwardAvailableSSHDependency(t *testing.T) {
	mk := func(ssh *bool, privKey, hostKey string) *config.Config {
		return &config.Config{
			Customer: &config.CustomerConfig{
				Mesh: &config.MeshConfig{
					Enable:     boolPtr(true),
					PrivateKey: privKey,
					Peers:      []config.MeshPeer{{DeviceID: "dev-b", HostKey: hostKey}},
				},
				SSHIngress: &config.SSHIngressConfig{Enable: ssh},
			},
		}
	}
	if !meshForwardAvailable(mk(boolPtr(true), "k", "h"), "dev-b") {
		t.Fatal("ssh 开+身份钥+对端已钉扎 → available")
	}
	if meshForwardAvailable(mk(boolPtr(false), "k", "h"), "dev-b") {
		t.Fatal("ssh 关 → 依赖降级 unavailable")
	}
	if meshForwardAvailable(mk(boolPtr(true), "", "h"), "dev-b") {
		t.Fatal("无身份钥 → unavailable")
	}
	if meshForwardAvailable(mk(boolPtr(true), "k", ""), "dev-b") {
		t.Fatal("对端未钉扎 → unavailable (fail-closed)")
	}
	if meshForwardAvailable(mk(boolPtr(true), "k", "h"), "dev-xyz") {
		t.Fatal("peer 不在列表 → unavailable")
	}
}

// TestMeshDNSForwardTargetRelayOnlyPeer DNS 代转发目标判据升级(SPEC §13.2 ③):
// relay-only peer(跨 NAT,无 LAN 地址)不再被 len(LANAddrs)==0 静默关闭,
// 目标照常返回,载体选择(直拨 vs piko)交 Transport 层。
func TestMeshDNSForwardTargetRelayOnlyPeer(t *testing.T) {
	cfg := &config.Config{
		Customer: &config.CustomerConfig{
			SSHIngress: sshIngressOn(),
			Mesh: capableMesh(config.MeshPeer{
				DeviceID: "dev-b",
				Relay:    boolPtr(true),
			}),
		},
	}
	snapshot := NewRuntimeSnapshotForDecision(
		NewSnapshotBranchCapabilities(true, false, false),
		[]SnapshotRule{
			NewSnapshotRule("r0", "dns", "all", true, SnapshotAction("toNode:dev-b")),
		},
		SnapshotActionDirect, false)
	id, addrs, ok := meshDNSForwardTargetFromSnapshot(snapshot, cfg)
	if !ok || id != "dev-b" {
		t.Fatalf("relay-only peer = (%q, %v, %v), want (dev-b, ..., true)", id, addrs, ok)
	}
	if len(addrs) != 0 {
		t.Fatalf("relay-only peer 无 LAN 载体, lanAddrs = %v, want 空", addrs)
	}
}
