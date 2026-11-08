package config

import (
	"strings"
	"testing"
)

// validCanonicalRoutingSchema 返回直连分支启用、无规则的最小合法 schema。
func validCanonicalRoutingSchema() *CanonicalRoutingSchema {
	return &CanonicalRoutingSchema{
		Version: CanonicalRoutingSchemaVersion,
		Ingress: CanonicalIngressConfig{Mode: "tun"},
		Egress:  CanonicalEgressConfig{Direct: CanonicalEgressBranch{Enabled: true}},
		Routing: CanonicalRoutingConfig{Rules: []CanonicalRoutingRule{}},
	}
}

func TestCanonicalRoutingSchemaValidate_MeshTargetEmptyDeviceRejected(t *testing.T) {
	cfg := validCanonicalRoutingSchema()
	cfg.Routing.Rules = append(cfg.Routing.Rules, CanonicalRoutingRule{
		ID:        "r0",
		Type:      "domain",
		Condition: "example.com",
		Enabled:   true,
		Target:    "toNode:",
	})

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil error, want invalid_target (mesh 目标 deviceID 非空)")
	}
	if !strings.Contains(err.Error(), "toNode:") {
		t.Fatalf("Validate() error = %v, want message mentioning target %q", err, "toNode:")
	}
}

func TestCanonicalRoutingSchemaValidate_MeshDefaultEgressRejected(t *testing.T) {
	cfg := validCanonicalRoutingSchema()
	cfg.Routing.DefaultEgress = "toNode:dev-b"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil error, want unknown_target (mesh 是规则驱动出口,不作默认出口)")
	}
}

// TestCanonicalRoutingSchemaValidate_MeshPolicyForms Task 7 策略 grammar:
// toNode:<id>[:direct|:deny|:fallback:<id2>] 四形态接受,策略段白名单外与
// fallback 目标空拒绝。
func TestCanonicalRoutingSchemaValidate_MeshPolicyForms(t *testing.T) {
	accepted := []string{
		"toNode:dev-b",
		"toNode:dev-b:direct",
		"toNode:dev-b:deny",
		"toNode:dev-b:fallback:dev-c",
	}
	for _, target := range accepted {
		cfg := validCanonicalRoutingSchema()
		cfg.Routing.Rules = append(cfg.Routing.Rules, CanonicalRoutingRule{
			ID: "r0", Type: "domain", Condition: "example.com", Enabled: true, Target: target,
		})
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() target=%s error = %v, want accepted", target, err)
		}
	}

	rejected := []string{
		"toNode:dev-b:fallback:",     // fallback 目标为空
		"toNode:dev-b:bogus",         // 未知策略段
		"toNode:dev-b:direct:extra",  // 超出四形态的段数
		"toNode::deny",               // deviceID 空
		"toNode:dev-b:DENY",          // 策略关键字小写精确(编译产物恒小写)
		"toNode:dev-b:fallback:dev-c:direct", // fallback 目标含策略段(段数超限)
	}
	for _, target := range rejected {
		cfg := validCanonicalRoutingSchema()
		cfg.Routing.Rules = append(cfg.Routing.Rules, CanonicalRoutingRule{
			ID: "r0", Type: "domain", Condition: "example.com", Enabled: true, Target: target,
		})
		if err := cfg.Validate(); err == nil {
			t.Fatalf("Validate() target=%s error = nil, want rejected", target)
		}
	}
}

// TestNormalizeRuleTarget_MeshPolicyForms 归一化:策略段白名单内原样(各段
// TrimSpace 重建),白名单外报错——mesh 目标不允许 default_egress 复用本函数
// 放行的既有口径不变。
func TestNormalizeRuleTarget_MeshPolicyForms(t *testing.T) {
	cases := []struct{ in, want string }{
		{"toNode:dev-b", "toNode:dev-b"},
		{"toNode:dev-b:direct", "toNode:dev-b:direct"},
		{"toNode:dev-b:deny", "toNode:dev-b:deny"},
		{"toNode:dev-b:fallback:dev-c", "toNode:dev-b:fallback:dev-c"},
		{"toNode: dev-b : fallback: dev-c ", "toNode:dev-b:fallback:dev-c"},
	}
	for _, tc := range cases {
		got, err := normalizeRuleTarget(tc.in)
		if err != nil {
			t.Fatalf("normalizeRuleTarget(%q) error = %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("normalizeRuleTarget(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	for _, in := range []string{"toNode:dev-b:bogus", "toNode:dev-b:fallback:", "toNode:dev-b:a:b:c"} {
		if got, err := normalizeRuleTarget(in); err == nil {
			t.Fatalf("normalizeRuleTarget(%q) = %q, nil error, want rejected", in, got)
		}
	}
}

// TestParseMeshRuleTarget 链头执行层(processor/tcp dialByRoute)消费的解析
// 契约:四形态 deviceID/策略拆分正确,非 mesh 前缀 ok=false。
func TestParseMeshRuleTarget(t *testing.T) {
	cases := []struct {
		in           string
		wantDeviceID string
		wantPolicy   string
		wantOK       bool
	}{
		{"toNode:dev-b", "dev-b", "", true},
		{"toNode:dev-b:direct", "dev-b", "direct", true},
		{"toNode:dev-b:deny", "dev-b", "deny", true},
		{"toNode:dev-b:fallback:dev-c", "dev-b", "fallback:dev-c", true},
		{"toNode:", "", "", true},              // mesh 形态但 deviceID 空 → 调用方判非法
		{"toNode:dev-b:bogus", "", "", true},   // 未知策略段 → 非法
		{"toNode:a:b:c:d", "", "", true},       // 段数超限 → 非法
		{"direct", "", "", false},
		{"toAliang", "", "", false},
	}
	for _, tc := range cases {
		deviceID, policy, ok := ParseMeshRuleTarget(tc.in)
		if deviceID != tc.wantDeviceID || policy != tc.wantPolicy || ok != tc.wantOK {
			t.Fatalf("ParseMeshRuleTarget(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.in, deviceID, policy, ok, tc.wantDeviceID, tc.wantPolicy, tc.wantOK)
		}
	}
}
