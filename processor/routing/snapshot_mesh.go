// snapshot_mesh 承载 mesh 转发规则源的编译辅助(自 snapshot.go 拆出,评审
// M3):规则源尾段解析(splitMeshTail,Task 7 多段尾策略 grammar)、编译期
// 能力检查(meshForwardAvailable)、条目编译(classifyEntryForCompile)及
// 类型映射(entryKindToRuleType)/dns 判定(isDNSEntry)等同主题函数。
// 与 snapshot.go 同包,纯按主题聚拢 + Task 7 新 grammar,无耦合变化。
package routing

import (
	"strings"

	"aliang.one/nursorgate/common/model"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/match"
)

// SnapshotTargetMeshPrefix 是 mesh 转发目标在快照/canonical 规则上的前缀形态
// (toNode:<deviceID>)。不并入 SnapshotAction 枚举——SnapshotAction 的四值
// 枚举语义(direct/toAliang/toSocks/deny)保持不变, mesh 转发目标是带参数的
// 目标(deviceID 内嵌在字符串里), 由决策层按前缀识别并整串透传
// (RouteDecision 本就是 string 类型, 消费方为 mesh 出口转发层)。
// Task 7 起目标可携带失败策略段: toNode:<deviceID>[:direct|:deny|:fallback:<id2>]。
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

// meshTailState 是 splitMeshTail 的三态结果。
type meshTailState int

const (
	// meshTailNone 无 mesh: 段：非 mesh 条目，交回常规分类。
	meshTailNone meshTailState = iota
	// meshTailParsed 合法 mesh 尾：base、deviceID 与策略段可用。
	meshTailParsed
	// meshTailInvalid 含 mesh: 段但形态不可解析：整条静默跳过，
	// 不得回落常规分类（防静默错编译，见 splitMeshTail 注释）。
	meshTailInvalid
)

// splitMeshTail 剥离 mesh 转发尾段：任一逗号段以 mesh: 开头（大小写不敏感）
// 即判定该条目为 mesh 语法，mesh: 段必须携带非空 deviceID 且 base 非空。
// Task 7 多段尾策略 grammar：mesh: 段之后允许恰一个失败策略段——
// direct|deny|fallback:<deviceID>（关键字大小写不敏感、归一小写；fallback
// 目标保留原大小写且非空）；其余任何段（未知策略段、第二个策略段、空段）
// 以及 deviceID/base 为空 → meshTailInvalid，整条静默跳过不编译——回落常规
// 分类会把 "domains,x.com,mesh:dev-b,bogus" 静默错编译成普通代理规则
// （domains 前缀分支吞掉 mesh: 段）。
// 必须在 match.ClassifyEntry 之前调用——ClassifyEntry 的 domains/ip-cidr 前缀
// 分支会忽略多余尾段，兜底分支会把未知尾段当动作词噪声剥掉或按 host:port
// 提取（"dns,mesh:dev-b" 甚至会被 SplitHostPort 吃成域名 "mesh"），两种路径
// 都会吞掉 mesh 目标。
// 返回剥尾后的 base 条目、deviceID 与归一化策略段(""=缺省 direct /
// "direct" / "deny" / "fallback:<id2>")。
func splitMeshTail(raw string) (base, meshDeviceID, meshPolicy string, state meshTailState) {
	parts := strings.Split(raw, ",")
	meshIdx := -1
	for i, part := range parts {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(part)), meshTailPrefix) {
			meshIdx = i
			break
		}
	}
	if meshIdx < 0 {
		return raw, "", "", meshTailNone
	}
	// mesh: 段后至多一个策略段；多出的任何段（第二段/未知段/空段）非法。
	tailParts := parts[meshIdx+1:]
	if len(tailParts) > 1 {
		return "", "", "", meshTailInvalid
	}
	policy := ""
	if len(tailParts) == 1 {
		normalized, ok := normalizeMeshPolicySegment(tailParts[0])
		if !ok {
			return "", "", "", meshTailInvalid
		}
		policy = normalized
	}
	tail := strings.TrimSpace(parts[meshIdx])
	deviceID := strings.TrimSpace(tail[len(meshTailPrefix):])
	if deviceID == "" {
		return "", "", "", meshTailInvalid
	}
	base = strings.TrimSpace(strings.Join(parts[:meshIdx], ","))
	if base == "" {
		return "", "", "", meshTailInvalid
	}
	return base, deviceID, policy, meshTailParsed
}

// normalizeMeshPolicySegment 归一化策略段(Task 7 grammar)：关键字
// direct/deny/fallback 大小写不敏感、归一为小写常量；fallback 目标 TrimSpace
// 保留原大小写(与 deviceID 口径一致)且必须非空。未知段 ok=false。
func normalizeMeshPolicySegment(seg string) (string, bool) {
	s := strings.TrimSpace(seg)
	if s == "" {
		return "", false
	}
	if strings.EqualFold(s, config.MeshPolicyDirect) {
		return config.MeshPolicyDirect, true
	}
	if strings.EqualFold(s, config.MeshPolicyDeny) {
		return config.MeshPolicyDeny, true
	}
	// fallback:<deviceID>——关键字等值忽略大小写，目标保留原样。
	fallbackKwLen := len(config.MeshPolicyFallback)
	if len(s) > fallbackKwLen+1 {
		kw := s[:fallbackKwLen]
		sep := s[fallbackKwLen]
		id := strings.TrimSpace(s[fallbackKwLen+1:])
		if sep == ':' && strings.EqualFold(kw, config.MeshPolicyFallback) && id != "" {
			return config.MeshPolicyFallback + ":" + id, true
		}
	}
	return "", false
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
// mesh 目标携带 Task 7 失败策略段：toNode:<id>[:direct|:deny|:fallback:<id2>]，
// 策略段校验在解析期完成（见 splitMeshTail），fallback 目标不要求已列入
// peers——运行时拨不到指定节点由链头失败策略的「限一次防链」语义收口。
// condition 为空 = 条目非法、mesh 形态不可解析或 mesh 能力不可用，调用方静默跳过。
func classifyEntryForCompile(cfg *config.Config, raw string, fallbackTarget SnapshotAction) (ruleType, condition string, target SnapshotAction) {
	base, meshDeviceID, meshPolicy, state := splitMeshTail(raw)
	if state == meshTailInvalid {
		return "", "", ""
	}
	if state == meshTailParsed {
		if !meshForwardAvailable(cfg, meshDeviceID) {
			return "", "", ""
		}
		meshTarget := SnapshotTargetMeshPrefix + meshDeviceID
		if meshPolicy != "" {
			meshTarget += ":" + meshPolicy
		}
		if isDNSEntry(base) {
			return snapshotRuleTypeDNS, snapshotDNSConditionAll, SnapshotAction(meshTarget)
		}
		kind, cond, err := match.ClassifyEntry(base)
		if err != nil || cond == "" {
			return "", "", ""
		}
		return entryKindToRuleType(kind), cond, SnapshotAction(meshTarget)
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
