package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const CanonicalRoutingSchemaVersion = 1

type CanonicalRoutingSchema struct {
	Version int                    `json:"version"`
	Ingress CanonicalIngressConfig `json:"ingress"`
	Egress  CanonicalEgressConfig  `json:"egress"`
	Routing CanonicalRoutingConfig `json:"routing"`
}

type CanonicalIngressConfig struct {
	Mode string `json:"mode"`
}

type CanonicalEgressConfig struct {
	Direct   CanonicalEgressBranch      `json:"direct"`
	ToAliang CanonicalEgressBranch      `json:"toAliang"`
	ToSocks  CanonicalSocksEgressBranch `json:"toSocks"`
}

type CanonicalEgressBranch struct {
	Enabled bool `json:"enabled"`
}

type CanonicalSocksEgressBranch struct {
	Enabled  bool                   `json:"enabled"`
	Upstream CanonicalSocksUpstream `json:"upstream"`
}

type CanonicalSocksUpstream struct {
	Type string `json:"type"`
}

type CanonicalRoutingConfig struct {
	Rules         []CanonicalRoutingRule `json:"rules"`
	DefaultEgress string                 `json:"default_egress,omitempty"`
}

type CanonicalRoutingRule struct {
	ID        string `json:"id,omitempty"`
	Type      string `json:"type,omitempty"`
	Condition string `json:"condition,omitempty"`
	Enabled   bool   `json:"enabled"`
	Target    string `json:"target"`
}

type RoutingSchemaValidationError struct {
	Code    string
	Field   string
	Message string
}

func (e *RoutingSchemaValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

func newRoutingSchemaValidationError(code string, field string, message string) *RoutingSchemaValidationError {
	return &RoutingSchemaValidationError{
		Code:    code,
		Field:   field,
		Message: message,
	}
}

func NormalizeRoutingSchemaJSON(input []byte) (*CanonicalRoutingSchema, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(input, &root); err != nil {
		return nil, fmt.Errorf("failed to parse routing schema JSON: %w", err)
	}

	if _, ok := root["ingress"]; ok {
		return normalizeCanonicalPayload(input)
	}

	if len(root) == 0 {
		return nil, fmt.Errorf("non-canonical routing payload; migrate to canonical keys ingress/egress/routing with targets direct|toSocks|toAliang")
	}

	legacyKeys := make([]string, 0, len(root))
	for key := range root {
		legacyKeys = append(legacyKeys, key)
	}
	sort.Strings(legacyKeys)
	if len(legacyKeys) == 1 {
		return nil, fmt.Errorf("unsupported legacy key %q; migrate to canonical keys ingress/egress/routing", legacyKeys[0])
	}
	return nil, fmt.Errorf("unsupported legacy keys %v; migrate to canonical keys ingress/egress/routing", legacyKeys)
}

func normalizeCanonicalPayload(input []byte) (*CanonicalRoutingSchema, error) {
	var cfg CanonicalRoutingSchema
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse canonical routing schema: %w", err)
	}

	if cfg.Version == 0 {
		cfg.Version = CanonicalRoutingSchemaVersion
	}
	if cfg.Version != CanonicalRoutingSchemaVersion {
		return nil, fmt.Errorf("unsupported routing schema version: %d", cfg.Version)
	}

	if cfg.Ingress.Mode == "" {
		cfg.Ingress.Mode = "tun"
	}

	for i := range cfg.Routing.Rules {
		target, err := normalizeRuleTarget(cfg.Routing.Rules[i].Target)
		if err != nil {
			return nil, fmt.Errorf("routing.rules[%d].target normalize failed: %w", i, err)
		}
		cfg.Routing.Rules[i].Target = target
	}

	if cfg.Routing.DefaultEgress != "" {
		normalizedDefault, err := normalizeRuleTarget(cfg.Routing.DefaultEgress)
		if err != nil {
			return nil, fmt.Errorf("routing.default_egress normalize failed: %w", err)
		}
		cfg.Routing.DefaultEgress = normalizedDefault
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func (cfg *CanonicalRoutingSchema) Validate() error {
	if cfg.Ingress.Mode != "tun" && cfg.Ingress.Mode != "http" {
		return newRoutingSchemaValidationError("invalid_enum", "ingress.mode", fmt.Sprintf("must be one of [tun http], got %q", cfg.Ingress.Mode))
	}

	if !cfg.Egress.Direct.Enabled {
		return newRoutingSchemaValidationError("branch_required_enabled", "egress.direct.enabled", "direct must exist and be enabled")
	}

	if cfg.Egress.ToSocks.Enabled {
		if cfg.Egress.ToSocks.Upstream.Type != "socks" && cfg.Egress.ToSocks.Upstream.Type != "http" {
			return newRoutingSchemaValidationError("invalid_enum", "egress.toSocks.upstream.type", fmt.Sprintf("must be one of [socks http] when toSocks is enabled, got %q", cfg.Egress.ToSocks.Upstream.Type))
		}
	} else if cfg.Egress.ToSocks.Upstream.Type != "" && cfg.Egress.ToSocks.Upstream.Type != "socks" && cfg.Egress.ToSocks.Upstream.Type != "http" {
		return newRoutingSchemaValidationError("invalid_enum", "egress.toSocks.upstream.type", fmt.Sprintf("must be one of [socks http] when provided, got %q", cfg.Egress.ToSocks.Upstream.Type))
	}

	for i := range cfg.Routing.Rules {
		targetField := fmt.Sprintf("routing.rules[%d].target", i)
		target := cfg.Routing.Rules[i].Target
		switch target {
		case "direct":
			if !cfg.Egress.Direct.Enabled {
				return newRoutingSchemaValidationError("disabled_target_branch", targetField, "target branch direct is disabled")
			}
		case "toAliang":
			if !cfg.Egress.ToAliang.Enabled {
				return newRoutingSchemaValidationError("disabled_target_branch", targetField, "target branch toAliang is disabled")
			}
		case "toSocks":
			if !cfg.Egress.ToSocks.Enabled {
				return newRoutingSchemaValidationError("disabled_target_branch", targetField, "target branch toSocks is disabled")
			}
		default:
			// mesh 转发目标 toNode:<deviceID>:本层只做形态校验(deviceID 非空);
			// 能力校验(mesh 总开关 + peer 存在)在运行输入编译层
			// (processor/routing,见 meshForwardAvailable)把关。
			if deviceID, isMesh := splitMeshRuleTarget(target); isMesh {
				if deviceID == "" {
					return newRoutingSchemaValidationError("invalid_target", targetField, fmt.Sprintf("mesh target requires non-empty device id, got %q", target))
				}
				continue
			}
			return newRoutingSchemaValidationError("unknown_target", targetField, fmt.Sprintf("unknown target: %s", target))
		}
	}

	if cfg.Routing.DefaultEgress != "" {
		switch cfg.Routing.DefaultEgress {
		case "direct":
			if !cfg.Egress.Direct.Enabled {
				return newRoutingSchemaValidationError("unresolvable_default_egress", "routing.default_egress", "default egress target direct is disabled")
			}
		case "toAliang":
			if !cfg.Egress.ToAliang.Enabled {
				return newRoutingSchemaValidationError("unresolvable_default_egress", "routing.default_egress", "default egress target toAliang is disabled")
			}
		case "toSocks":
			if !cfg.Egress.ToSocks.Enabled {
				return newRoutingSchemaValidationError("unresolvable_default_egress", "routing.default_egress", "default egress target toSocks is disabled")
			}
		default:
			return newRoutingSchemaValidationError("unknown_target", "routing.default_egress", fmt.Sprintf("unknown target: %s", cfg.Routing.DefaultEgress))
		}
	}

	return nil
}

// meshRuleTargetPrefix 是 mesh 转发规则目标的形态前缀(processor/routing 的
// SnapshotTargetMeshPrefix 同值镜像——config 不能反向 import routing,会成环)。
// 形态:toNode:<deviceID>,deviceID 非空。mesh 是规则驱动出口,不作 default_egress。
const meshRuleTargetPrefix = "toNode:"

// splitMeshRuleTarget 解析 toNode:<deviceID> 形态的规则 target。
// ok=false 表示不是 mesh 形态;deviceID 为 trim 后的值(可能为空,由调用方校验)。
func splitMeshRuleTarget(target string) (deviceID string, ok bool) {
	rest, found := strings.CutPrefix(target, meshRuleTargetPrefix)
	if !found {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

func normalizeRuleTarget(target string) (string, error) {
	switch target {
	case "direct", "toAliang", "toSocks":
		return target, nil
	default:
		// mesh 规则目标 toNode:<deviceID>:规整 deviceID 空白后原样返回
		// (前缀精确匹配,deviceID 保留原大小写与 peer 比对)。
		// default_egress 也走本函数归一,但其 Validate 白名单不接受 mesh
		// 形态——mesh 由规则命中驱动,不允许成为默认出口。
		if deviceID, isMesh := splitMeshRuleTarget(target); isMesh && deviceID != "" {
			return meshRuleTargetPrefix + deviceID, nil
		}
		return "", fmt.Errorf("unknown target: %s", target)
	}
}
