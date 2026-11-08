package routing

import (
	"fmt"
	"sort"
	"strings"

	"aliang.one/nursorgate/common/model"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/match"
)

type SnapshotAction string

const (
	SnapshotActionDirect   SnapshotAction = "direct"
	SnapshotActionToAliang SnapshotAction = "toAliang"
	SnapshotActionToSocks  SnapshotAction = "toSocks"
	SnapshotActionDeny     SnapshotAction = "deny"
)

// SnapshotTargetMeshPrefix 是 mesh 转发目标在快照/canonical 规则上的前缀形态
// (toNode:<deviceID>)。不并入 SnapshotAction 枚举——SnapshotAction 的四值
// 枚举语义(direct/toAliang/toSocks/deny)保持不变, mesh 转发目标是带参数的
// 目标(deviceID 内嵌在字符串里), 由决策层按前缀识别并整串透传
// (RouteDecision 本就是 string 类型, 消费方为 mesh 出口转发层)。
const SnapshotTargetMeshPrefix = "toNode:"

// mesh 规则源尾段关键字(dns 条目不经 match.ClassifyEntry, 见 classifyEntryForCompile)。
const meshTailPrefix = "mesh:"

// snapshotRuleTypeDNS / snapshotDNSConditionAll: DNS 代转发规则(design §6 层1/
// §9.3 规则驱动)。Condition 恒为 all(DNS 代转发消费侧语义);连接级决策一律
// 不命中(见 decision_engine.checkRuleBySnapshot),代转发目标由 Task 8 扫快照
// 规则取首条启用项(多 peer 无歧义: 首条命中)。仅存在于快照/canonical 规则层,
// 不进 model.RuleType 枚举(legacy RoutingRuleSet 无此形态)。
const (
	snapshotRuleTypeDNS     = "dns"
	snapshotDNSConditionAll = "all"
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

// CompileRuntimeSnapshot 将 canonical schema(如 routing apply store 的持久化
// 产物)物化为决策快照。
//
// 不变式(M5,防后人读错快照):store 快照不驱动连接级决策——连接级决策的
// 两处调用方(processor/rules/engine 与 processor/tcp/tls)都走
// CompileRuntimeSnapshotFromRuntimeInputs 按需重编译,不读本函数产物;store
// 快照只用于开关状态派生与持久化回放。因此 mesh 能力闸(meshForwardAvailable:
// 总开关+peer 存在)只在 runtime-inputs 编译路径把关,本函数对 toNode: 目标
// 不做能力检查、按前缀放行是安全的——落盘 canonical 在写入时
// (customer_config_service)已经过 runtime-inputs 闸,手改文件注入的越权
// toNode 规则由 Validate 的形态校验拦下 deviceID 空/未知形态,peer 越权则由
// 决策层按 runtime inputs 按需重编译天然规避——手写 canonical 的 toNode 规则
// 不在 runtime-inputs 规则源中,对决策天然惰性。
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

	var aiEntries []string
	if switches.AliangEnabled {
		aiEntries = collectAIEntries(cfg)
	}
	proxyEntries := collectProxyRuleEntries(cfg)

	upstreamType, hasToSocks := resolveToSocksUpstreamType(cfg)
	canonical.Egress.ToSocks.Enabled = switches.SocksEnabled && hasToSocks
	if canonical.Egress.ToSocks.Enabled {
		canonical.Egress.ToSocks.Upstream.Type = upstreamType
	}

	// appendRules 按共享内核分类结果追加规则：
	// - 域名/IP 由 match.ClassifyEntry 统一判定（ip-cidr 等条目编译为 Type=ip，不再误编译为域名）；
	// - mesh 转发尾段（mesh:<deviceID>）先于 ClassifyEntry 剥出（见 splitMeshTail），
	//   编译为 Target=toNode:<deviceID> 的规则；能力不可用/未知 peer 静默跳过；
	// - 去重按 (kind, cond) 全局进行——与 ai_allowlist 同条件的 proxy 规则本就排在后面永不可达，提前剪除；
	//   mesh 规则的键追加 target——两条 dns 规则指向不同 peer 语义不同，都保留供决策取首条；
	// - ID 按前缀独立连续编号（ai_allowlist_0.. / proxy_rule_0..），mesh 条目与
	//   proxy 条目同池编号（mesh 本就是 proxy_rules 的转发变体），与既有产物风格一致。
	// 编译函数每条连接都会被调用：非法条目在此静默跳过——旧代码会把此类条目编译为
	// 永不命中的死规则，现改为直接跳过；保存期校验由配置校验层（Task 10）承担。
	// 不打日志、不报错，避免每连接日志风暴。
	rules := make([]config.CanonicalRoutingRule, 0, len(aiEntries)+len(proxyEntries))
	seen := make(map[string]struct{}, len(aiEntries)+len(proxyEntries))
	appendRules := func(entries []string, idPrefix string, target SnapshotAction) {
		index := 0
		for _, raw := range entries {
			ruleType, condition, entryTarget := classifyEntryForCompile(cfg, raw, target)
			if condition == "" || entryTarget == "" {
				continue
			}
			key := ruleType + "|" + condition
			if strings.HasPrefix(string(entryTarget), SnapshotTargetMeshPrefix) {
				key += "|" + string(entryTarget)
			}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			rules = append(rules, config.CanonicalRoutingRule{
				ID:        fmt.Sprintf("%s_%d", idPrefix, index),
				Type:      ruleType,
				Condition: condition,
				Enabled:   true,
				Target:    string(entryTarget),
			})
			index++
		}
	}

	// 有意行为(design §4 允许):AI 池条目带 mesh 尾同样会编译为
	// ai_allowlist_N + toNode:<deviceID> 目标,覆盖默认 toAliang——
	// mesh 尾是条目级转发声明,与所在池无关。
	appendRules(aiEntries, "ai_allowlist", SnapshotActionToAliang)
	appendRules(proxyEntries, "proxy_rule", resolveProxyRuleTarget(canonical.Egress.ToSocks.Enabled))
	canonical.Routing.Rules = rules

	// Keep direct as the default egress. The customer proxy should only be used
	// by explicit proxy_rules matches instead of hijacking all unmatched traffic.
	canonical.Routing.DefaultEgress = string(SnapshotActionDirect)

	if err := canonical.Validate(); err != nil {
		return nil, fmt.Errorf("compile runtime canonical routing failed: %w", err)
	}

	return canonical, nil
}

// collectAIEntries 返回原始 AI 条目（含 Enable 过滤后的 include 与内置允许清单），
// 不做归一化去重——分类与去重统一由 appendRules 按 (kind, cond) 进行。
func collectAIEntries(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}

	entries := make([]string, 0)
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
			entries = append(entries, rule.Include...)
		}
	}

	entries = append(entries, cfg.EffectiveAIAllowlist()...)
	return entries
}

// collectProxyRuleEntries 返回原始 proxy_rules 条目，解析与去重由 appendRules 统一承担。
func collectProxyRuleEntries(cfg *config.Config) []string {
	if cfg == nil || cfg.Customer == nil {
		return nil
	}
	return cfg.Customer.ProxyRules
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

// meshTailState 是 splitMeshTail 的三态结果。
type meshTailState int

const (
	// meshTailNone 无 mesh: 段：非 mesh 条目，交回常规分类。
	meshTailNone meshTailState = iota
	// meshTailParsed 合法 mesh 尾：base 与 deviceID 可用。
	meshTailParsed
	// meshTailInvalid 含 mesh: 段但形态不可解析：整条静默跳过，
	// 不得回落常规分类（防静默错编译，见 splitMeshTail 注释）。
	meshTailInvalid
)

// splitMeshTail 剥离 mesh 转发尾段：任一逗号段以 mesh: 开头（大小写不敏感）
// 即判定该条目为 mesh 语法，mesh: 段必须是末段且 deviceID/base 非空，
// 返回剥尾后的 base 条目与 deviceID。
// 必须在 match.ClassifyEntry 之前调用——ClassifyEntry 的 domains/ip-cidr 前缀
// 分支会忽略多余尾段，兜底分支会把未知尾段当动作词噪声剥掉或按 host:port
// 提取（"dns,mesh:dev-b" 甚至会被 SplitHostPort 吃成域名 "mesh"），两种路径
// 都会吞掉 mesh 目标。
// 失败策略（止血方案）：mesh: 段之后若还有任何段（含空段，如
// "mesh:dev-b,direct" 或尾逗号）、deviceID 为空或 base 为空 → meshTailInvalid，
// 整条静默跳过不编译——回落常规分类会把 "domains,x.com,mesh:dev-b,direct"
// 静默错编译成普通代理规则（domains 前缀分支吞掉 mesh: 段）。
// 多段尾（mesh:x,direct/deny/fallback:y）解析随 Task 7 落地，当前不认多段尾。
func splitMeshTail(raw string) (base, meshDeviceID string, state meshTailState) {
	parts := strings.Split(raw, ",")
	meshIdx := -1
	for i, part := range parts {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(part)), meshTailPrefix) {
			meshIdx = i
			break
		}
	}
	if meshIdx < 0 {
		return raw, "", meshTailNone
	}
	if meshIdx != len(parts)-1 {
		return "", "", meshTailInvalid
	}
	tail := strings.TrimSpace(parts[meshIdx])
	deviceID := strings.TrimSpace(tail[len(meshTailPrefix):])
	if deviceID == "" {
		return "", "", meshTailInvalid
	}
	base = strings.TrimSpace(strings.Join(parts[:meshIdx], ","))
	if base == "" {
		return "", "", meshTailInvalid
	}
	return base, deviceID, meshTailParsed
}

// isDNSEntry 判断剥尾后的条目是否 DNS 关键字条目（dns，大小写不敏感）。
func isDNSEntry(base string) bool {
	return strings.EqualFold(strings.TrimSpace(base), "dns")
}

// meshForwardAvailable 编译期 mesh 能力检查：总开关 EffectiveEnabled 且
// deviceID 存在于 customer.mesh.peers。不通过 → 条目静默跳过（与非法条目
// 同款先例，见 appendRules）。
// 不做 ≠self 检查（分层防御分工）：自指已在保存期由 Config.ValidateMesh
// 拒绝（spec §3.4/§7 层0，config 层拿不到本机 device_id 故由提交路径显式调）；
// 手改配置文件绕过保存校验的漏网自指，由转发层 path vector 兜底拒环。
func meshForwardAvailable(cfg *config.Config, deviceID string) bool {
	if cfg == nil || cfg.Customer == nil || cfg.Customer.Mesh == nil {
		return false
	}
	mesh := cfg.Customer.Mesh
	if !mesh.EffectiveEnabled() {
		return false
	}
	for _, peer := range mesh.Peers {
		if strings.TrimSpace(peer.DeviceID) == deviceID {
			return true
		}
	}
	return false
}

// classifyEntryForCompile 将原始规则条目编译为 (ruleType, condition, target)。
// mesh 尾段先剥出再分类（meshTailInvalid 整条静默跳过，见 splitMeshTail）；
// dns,mesh:<id> 不经 ClassifyEntry，直接构造恒真 DNS 代转发规则
// （Condition=all，design §9.3 规则驱动；该规则不参与连接级决策，由
// Task 8 的 meshDNSForwardTarget 扫快照规则取首条启用项）。
// condition 为空 = 条目非法、mesh 形态不可解析或 mesh 能力不可用，调用方静默跳过。
func classifyEntryForCompile(cfg *config.Config, raw string, fallbackTarget SnapshotAction) (ruleType, condition string, target SnapshotAction) {
	base, meshDeviceID, state := splitMeshTail(raw)
	if state == meshTailInvalid {
		return "", "", ""
	}
	if state == meshTailParsed {
		if !meshForwardAvailable(cfg, meshDeviceID) {
			return "", "", ""
		}
		meshTarget := SnapshotAction(SnapshotTargetMeshPrefix + meshDeviceID)
		if isDNSEntry(base) {
			return snapshotRuleTypeDNS, snapshotDNSConditionAll, meshTarget
		}
		kind, cond, err := match.ClassifyEntry(base)
		if err != nil || cond == "" {
			return "", "", ""
		}
		return entryKindToRuleType(kind), cond, meshTarget
	}

	kind, cond, err := match.ClassifyEntry(raw)
	if err != nil || cond == "" {
		return "", "", ""
	}
	return entryKindToRuleType(kind), cond, fallbackTarget
}

// entryKindToRuleType 将共享内核 EntryKind 映射为 canonical 规则 Type 字符串。
func entryKindToRuleType(kind match.EntryKind) string {
	switch kind {
	case match.EntryIP:
		return string(model.RuleTypeIP)
	case match.EntryGeoIP:
		return string(model.RuleTypeGeoIP)
	default:
		return string(model.RuleTypeDomain)
	}
}
