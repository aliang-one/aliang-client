package services

// hello 增量协议(sync_mode=full|delta|heartbeat,spec
// docs/superpowers/specs/2026-11-26-hello-delta-protocol.md,2026-11-27 实施)。
// 稳态 agent→server 上行从全量快照(~415KB/hello)降为变更增量/心跳。
//
// 兼容铁律(2026-11-26 双仓侦察坐实,逐条有测试锁定):
//   - WS hello 脸是裸 JSON.parse(无 zod):旧服务端对 sync_mode 等新字段「无视」;
//     未变段必须「省略字段」绝不发 []——服务端 [] = 清空、缺失 = 保存量(双通道现状);
//   - 心跳帧必须保留身份小件:capabilities 缺失会被服务端抹成 [](级联关
//     operationProgressSupported/ai_run_protocol_v2 闸),name/platform/agent_version
//     缺失会落垃圾默认值;
//   - HTTP register 脸(status_sync,zod strip 脸,仅注册一发)不走本协议,恒全量;
//   - 全量锚点:reason=connect/server_requested、连续 delta 达自愈阈值、无基线、
//     registered 对账发现服务端会话归零(拓扑上重启必杀 socket→connect 全量已兜底,
//     对账是 PG flush 回滚类残余窗口的保险);
//   - protocol_version 是纯展示字段(服务端零消费),不能当协商信号。
//
// 指纹纪律(防心跳杀手,既有裁定见 agent_system.go agentProfileHash 注释):
//   - 指纹盖内容不盖采集时刻:所有时间字段量化到分钟——projects.LastActiveAt 与
//     活跃会话 UpdatedAt 走 time.Now() 兜底链(agent_inventory.go:291/340),不量化
//     则活跃机心跳永远打不出;
//   - tools/agent_tools 指纹剔除 description(codexAppServerAvailable 运行时探针
//     会翻转 Description,不剔则偶发假 delta);
//   - delta 帧的 session_changes 携带完整会话条目(message_count/display_message_count
//     必须在,服务端详情回源触发器依赖它们);session_removed_ids 是窗口出清墓碑,
//     服务端保守删(有手机绑定/在跑跳过)。

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sync"

	"aliang.one/nursorgate/app/http/models"
)

const (
	// 自愈锚点:连续 N 次 delta 后强制回全量,防指纹漂移/丢帧导致的基线漂移
	// (服务端 PG flush 连续失败后回滚的残余窗口由它封底)。
	agentHelloDeltaSelfHealEvery = 3
	// registered 对账容忍:服务端对清单有过滤语义(mcp/去重),数目不必逐条相等;
	// 只对「服务端归零而基线非零」升格 full。误报 full 无害(多推一帧全量),
	// 漏报 stale 才有害,阈值取小。
	agentHelloDeltaReconcileZeroFloor = 2
)

// agentRFC3339QuantizeRe 把 RFC3339 时间戳量化到分钟(指纹归一用)。作用在
// json.Unmarshal 之后的「裸字符串值」上(无引号),只匹配完整 ISO 形态,
// 不会误伤普通字符串值;归一统一落 Z 制式,时区差异随之归零。
var agentRFC3339QuantizeRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T(\d{2}:\d{2}):\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`)

// agentHelloFramePlan 是一帧 hello 的降档决策:mode 决定携带哪些段。
type agentHelloFramePlan struct {
	mode           string // full | delta | heartbeat
	selfHeal       bool
	forceReason    string
	includeTools      bool
	includeAgentTools bool
	includeSubagents  bool
	includeHistory    bool
	includeProjects   bool
	includeDirs       bool
	sessionChanges    []models.AgentVibeSession
	sessionRemoved    []string
}

// agentHelloDeltaState 持有上帧基线(段指纹+会话哈希)与簿记,跨重连存活;
// connect 锚点的全量帧会整体重建基线。
type agentHelloDeltaState struct {
	sequenceMu sync.Mutex
	mu         sync.Mutex

	deltasSinceFull   int
	forceFullReason   string
	baselined         bool
	toolsFP           string
	agentToolsFP      string
	subagentsFP       string
	historyFP         string
	projectsFP        string
	dirsFP            string
	sessionHashes     map[string]string
	lastSessionsCount int
}

// beginSequence 把 plan→apply→commit 三步串行化,防并发 reason 交叉簿记。
func (d *agentHelloDeltaState) beginSequence() func() {
	d.sequenceMu.Lock()
	return d.sequenceMu.Unlock
}

// agentStableSectionFingerprint 内容指纹:JSON 归一(时间量化到分钟+剔除
// description 运行时探针面)后 sha1[0:12]。map 序列化按 key 排序,确定性成立。
func agentStableSectionFingerprint(v interface{}) string {
	return agentStableFingerprint(v, "description")
}

func agentStableFingerprint(v interface{}, dropKeys ...string) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	var out interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return ""
	}
	normalized := agentNormalizeFingerprintNode(out, dropKeys)
	nb, err := json.Marshal(normalized)
	if err != nil {
		return ""
	}
	sum := sha1.Sum(nb)
	return hex.EncodeToString(sum[:12])
}

func agentNormalizeFingerprintNode(node interface{}, dropKeys []string) interface{} {
	switch typed := node.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for k, v := range typed {
			drop := false
			for _, dk := range dropKeys {
				if k == dk {
					drop = true
					break
				}
			}
			if drop {
				continue
			}
			out[k] = agentNormalizeFingerprintNode(v, dropKeys)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i, v := range typed {
			out[i] = agentNormalizeFingerprintNode(v, dropKeys)
		}
		return out
	case string:
		return agentRFC3339QuantizeRe.ReplaceAllString(typed, `${1}:00Z`)
	default:
		return node
	}
}

// agentSessionHashes 从清单会话数组提取 id→内容哈希(时间量化同上,活跃会话
// 的 UpdatedAt 翻新至多造成每分钟一次 delta,那是真变化)。
func agentSessionHashes(v interface{}) map[string]string {
	out := map[string]string{}
	sessions, ok := v.([]models.AgentVibeSession)
	if !ok {
		return out
	}
	for _, session := range sessions {
		out[session.ID] = agentStableFingerprint(session)
	}
	return out
}

// plan 依据 reason 与基线决定本帧形态。payload 是全量构建结果(builder 无状态,
// 每次全量收集——增量省的是线,不是算)。
func (d *agentHelloDeltaState) plan(reason string, payload map[string]interface{}) agentHelloFramePlan {
	d.mu.Lock()
	defer d.mu.Unlock()
	if reason == "connect" || reason == "server_requested" {
		return agentHelloFramePlan{mode: "full", forceReason: "anchor:" + reason}
	}
	if !d.baselined {
		return agentHelloFramePlan{mode: "full", forceReason: "no_baseline"}
	}
	if d.forceFullReason != "" {
		return agentHelloFramePlan{mode: "full", forceReason: d.forceFullReason}
	}
	if d.deltasSinceFull >= agentHelloDeltaSelfHealEvery {
		return agentHelloFramePlan{mode: "full", selfHeal: true}
	}

	toolsFP := agentStableSectionFingerprint(payload["tools"])
	agentToolsFP := agentStableSectionFingerprint(payload["agent_tools"])
	subagentsFP := agentStableSectionFingerprint(payload["subagents"])
	historyFP := agentStableSectionFingerprint(payload["history"])
	projectsFP := agentStableSectionFingerprint(payload["projects"])
	dirsFP := agentStableSectionFingerprint(payload["authorized_directories"])

	sectionsUnchanged := toolsFP == d.toolsFP && agentToolsFP == d.agentToolsFP &&
		subagentsFP == d.subagentsFP && historyFP == d.historyFP &&
		projectsFP == d.projectsFP && dirsFP == d.dirsFP

	current := agentSessionHashes(payload["vibe_sessions"])
	var changes []models.AgentVibeSession
	var removed []string
	if sessions, ok := payload["vibe_sessions"].([]models.AgentVibeSession); ok {
		for _, session := range sessions {
			h, exists := current[session.ID]
			if !exists {
				continue
			}
			if prev, seen := d.sessionHashes[session.ID]; !seen || prev != h {
				changes = append(changes, session)
			}
		}
	}
	for id := range d.sessionHashes {
		if _, ok := current[id]; !ok {
			removed = append(removed, id)
		}
	}

	if sectionsUnchanged && len(changes) == 0 && len(removed) == 0 {
		return agentHelloFramePlan{mode: "heartbeat"}
	}
	return agentHelloFramePlan{
		mode:              "delta",
		includeTools:      toolsFP != d.toolsFP,
		includeAgentTools: agentToolsFP != d.agentToolsFP,
		includeSubagents:  subagentsFP != d.subagentsFP,
		includeHistory:    historyFP != d.historyFP,
		includeProjects:   projectsFP != d.projectsFP,
		includeDirs:       dirsFP != d.dirsFP,
		sessionChanges:    changes,
		sessionRemoved:    removed,
	}
}

// applyPlan 按决策改写上线帧:未变段「省略字段」绝不发 [](服务端 [] = 清空)。
// 直接改写入参 map(builder 每帧新建,无共享风险)。
func (d *agentHelloDeltaState) applyPlan(payload map[string]interface{}, plan agentHelloFramePlan) map[string]interface{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch plan.mode {
	case "heartbeat":
		payload["sync_mode"] = "heartbeat"
		for _, key := range []string{"tools", "agent_tools", "subagents", "history", "projects", "vibe_sessions", "authorized_directories"} {
			delete(payload, key)
		}
		return payload
	case "delta":
		payload["sync_mode"] = "delta"
		if !plan.includeTools {
			delete(payload, "tools")
		}
		if !plan.includeAgentTools {
			delete(payload, "agent_tools")
		}
		if !plan.includeSubagents {
			delete(payload, "subagents")
		}
		if !plan.includeHistory {
			delete(payload, "history")
		}
		if !plan.includeProjects {
			delete(payload, "projects")
		}
		if !plan.includeDirs {
			delete(payload, "authorized_directories")
		}
		delete(payload, "vibe_sessions")
		if len(plan.sessionChanges) > 0 {
			payload["session_changes"] = plan.sessionChanges
		}
		if len(plan.sessionRemoved) > 0 {
			payload["session_removed_ids"] = plan.sessionRemoved
		}
		return payload
	default:
		payload["sync_mode"] = "full"
		return payload
	}
}

// commit 写成功后推进基线(失败路径不进这里=基线不动,下个 tick 重试,与
// remoteHello gate 的既有范式一致)。
func (d *agentHelloDeltaState) commit(plan *agentHelloFramePlan, fullPayload map[string]interface{}) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch plan.mode {
	case "full":
		d.deltasSinceFull = 0
		d.forceFullReason = ""
		d.toolsFP = agentStableSectionFingerprint(fullPayload["tools"])
		d.agentToolsFP = agentStableSectionFingerprint(fullPayload["agent_tools"])
		d.subagentsFP = agentStableSectionFingerprint(fullPayload["subagents"])
		d.historyFP = agentStableSectionFingerprint(fullPayload["history"])
		d.projectsFP = agentStableSectionFingerprint(fullPayload["projects"])
		d.dirsFP = agentStableSectionFingerprint(fullPayload["authorized_directories"])
		d.sessionHashes = agentSessionHashes(fullPayload["vibe_sessions"])
		d.baselined = true
	case "delta":
		d.deltasSinceFull++
		if plan.includeTools {
			d.toolsFP = agentStableSectionFingerprint(fullPayload["tools"])
		}
		if plan.includeAgentTools {
			d.agentToolsFP = agentStableSectionFingerprint(fullPayload["agent_tools"])
		}
		if plan.includeSubagents {
			d.subagentsFP = agentStableSectionFingerprint(fullPayload["subagents"])
		}
		if plan.includeHistory {
			d.historyFP = agentStableSectionFingerprint(fullPayload["history"])
		}
		if plan.includeProjects {
			d.projectsFP = agentStableSectionFingerprint(fullPayload["projects"])
		}
		if plan.includeDirs {
			d.dirsFP = agentStableSectionFingerprint(fullPayload["authorized_directories"])
		}
		if d.sessionHashes == nil {
			d.sessionHashes = map[string]string{}
		}
		for _, session := range plan.sessionChanges {
			d.sessionHashes[session.ID] = agentStableFingerprint(session)
		}
		for _, id := range plan.sessionRemoved {
			delete(d.sessionHashes, id)
		}
	case "heartbeat":
		// 基线不动
	}
	d.lastSessionsCount = len(d.sessionHashes)
}

// registeredServerSessionCount 从 agent.registered 回包读「服务端会话总量」。
// rank3(2026-11-28):优先 server_session_count(真实总量);缺失(老服务端)
// 回落 vibe_session_count——其语义是本帧变更条数,delta/heartbeat 帧恒 0,
// 拿它当总量对账曾造成强制全量永动,仅作兼容回退。
func registeredServerSessionCount(msg map[string]interface{}) (int, bool) {
	if v, ok := msg["server_session_count"].(float64); ok {
		return int(v), true
	}
	if v, ok := msg["vibe_session_count"].(float64); ok {
		return int(v), true
	}
	return 0, false
}

// forceFull 由外部信号(registered 对账等)要求下一帧回全量。
func (d *agentHelloDeltaState) forceFull(reason string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.forceFullReason == "" {
		d.forceFullReason = reason
	}
}

// reconcileRegisteredServerSessions 用 agent.registered 回包里服务端的当前
// 会话总量做轻对账:服务端归零而基线非零 = 内存态丢失信号,升格下帧 full。
// 数目相近的偏差是服务端过滤语义(mcp/去重),不触发。
// 字段语义(2026-11-28 rank3 修复):对账源=server_session_count(真实总量);
// 旧字段 vibe_session_count 是「本帧变更条数」而非总量,delta/heartbeat 帧恒
// 为 0——旧实现拿它当总量,每个 delta/heartbeat 后必误判归零,强制全量与
// delta 严格 60s 交替(线上 754 帧实测 52% 全量永动,~162MB/天/设备)。
// 仅在新字段缺席时(老服务端)回落旧字段,行为与旧版一致。
func (d *agentHelloDeltaState) reconcileRegisteredServerSessions(serverSessions int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if serverSessions == 0 && len(d.sessionHashes) >= agentHelloDeltaReconcileZeroFloor {
		if d.forceFullReason == "" {
			d.forceFullReason = "registered_reconcile_server_zero"
		}
	}
}
