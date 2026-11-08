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
			// mesh 转发目标 toNode:<deviceID>[:direct|:deny|:fallback:<deviceID2>]
			// (Task 7 策略 grammar):本层只做形态校验(deviceID 非空、策略段
			// 白名单、fallback 目标非空);能力校验(mesh 总开关 + peer 存在)在
			// 运行输入编译层(processor/routing,见 meshForwardAvailable)把关。
			if deviceID, policy, isMesh := ParseMeshRuleTarget(target); isMesh {
				if deviceID == "" || !isValidMeshPolicy(policy) {
					return newRoutingSchemaValidationError("invalid_target", targetField,
						fmt.Sprintf("invalid mesh target %q: want toNode:<deviceID>[:direct|:deny|:fallback:<deviceID2>]", target))
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

// MeshRuleTargetPrefix 是 mesh 转发规则目标的形态前缀(processor/routing 的
// SnapshotTargetMeshPrefix 同值镜像——config 不能反向 import routing,会成环;
// 两侧同值由 routing 包测试 TestSnapshotTargetMeshPrefixMirrorsConfigConstant 钉死)。
// 形态:toNode:<deviceID>[:policy],deviceID 非空;policy 为 Task 7 失败策略段
// (缺省=direct)。mesh 是规则驱动出口,不作 default_egress。
// 大小写不对称刻意:编译产物恒小写(routing 编译层只产 "toNode:" 一种拼法),
// 本层按前缀精确匹配、不做大小写归一。
const MeshRuleTargetPrefix = "toNode:"

// mesh 失败策略段关键字(Task 7 grammar,规则源(mesh:<id>,<policy>)与规则
// 目标(toNode:<id>:<policy>)两侧共用;链头执行层(processor/tcp dialByRoute)
// 解析决策字符串时按同值比较)。
const (
	MeshPolicyDirect   = "direct"   // mesh 拨出失败 → 回落 direct
	MeshPolicyDeny     = "deny"     // mesh 拨出失败 → 拒绝连接(不落 direct)
	MeshPolicyFallback = "fallback" // mesh 拨出失败 → 拨指定节点 fallback:<deviceID2>
)

// ParseMeshRuleTarget 解析 toNode:<deviceID>[:policy] 形态的规则 target
// (Task 7 策略 grammar)。policy ∈ {"", "direct", "deny", "fallback:<deviceID2>"}。
// ok=false 表示不是 mesh 形态(无 toNode: 前缀);ok=true 且 deviceID=="" 表示
// 「是 mesh 形态但不可解析」(deviceID 空/未知策略段/fallback 目标空/超出四形
// 态的段数),由调用方按校验错误处理。deviceID 与 fallback 目标 TrimSpace、
// 保留原大小写(与 peer 比对);策略关键字小写精确(编译产物恒小写,与既有
// 「前缀精确、不做大小写归一」口径一致)。deviceID 不允许含 ':'——含冒号的
// id 无法与策略段区分,直接判非法形态。
func ParseMeshRuleTarget(target string) (deviceID, policy string, ok bool) {
	rest, found := strings.CutPrefix(target, MeshRuleTargetPrefix)
	if !found {
		return "", "", false
	}
	parts := strings.Split(strings.TrimSpace(rest), ":")
	switch len(parts) {
	case 1:
		return strings.TrimSpace(parts[0]), "", true
	case 2:
		id := strings.TrimSpace(parts[0])
		switch strings.TrimSpace(parts[1]) {
		case MeshPolicyDirect, MeshPolicyDeny:
			return id, strings.TrimSpace(parts[1]), true
		}
		return "", "", true
	case 3:
		id := strings.TrimSpace(parts[0])
		kw := strings.TrimSpace(parts[1])
		second := strings.TrimSpace(parts[2])
		if id != "" && kw == MeshPolicyFallback && second != "" {
			return id, MeshPolicyFallback + ":" + second, true
		}
		return "", "", true
	default:
		return "", "", true
	}
}

// isValidMeshPolicy 策略段白名单:缺省("")/direct/deny,或 fallback:<非空目标>。
func isValidMeshPolicy(policy string) bool {
	switch policy {
	case "", MeshPolicyDirect, MeshPolicyDeny:
		return true
	}
	if rest, found := strings.CutPrefix(policy, MeshPolicyFallback+":"); found {
		return rest != ""
	}
	return false
}

func normalizeRuleTarget(target string) (string, error) {
	switch target {
	case "direct", "toAliang", "toSocks":
		return target, nil
	default:
		// mesh 规则目标 toNode:<deviceID>[:policy](Task 7 策略 grammar):
		// 各段 TrimSpace 后按白名单重建;策略关键字小写精确。
		// default_egress 也走本函数归一,但其 Validate 白名单不接受 mesh
		// 形态——mesh 由规则命中驱动,不允许成为默认出口。
		if deviceID, policy, isMesh := ParseMeshRuleTarget(target); isMesh && deviceID != "" && isValidMeshPolicy(policy) {
			out := MeshRuleTargetPrefix + deviceID
			if policy != "" {
				out += ":" + policy
			}
			return out, nil
		}
		return "", fmt.Errorf("unknown target: %s", target)
	}
}
