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
