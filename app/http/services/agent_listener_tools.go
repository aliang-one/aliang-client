package services

// agent_listener_tools.go 监听器 LLM 工具面(specs/005-event-listener §5/FR-9,
// plan Task 12):4 个工具(listener_create/list/update/delete)经 agentPeer 同款
// 桥(agentCorePeerToolBridge)注入 agentcore run;处理器是纯函数,Task 13 的
// stdio MCP 面直接复用。
//
// 分层纪律:本层只做模型入参的本地预检(快失败,省一次云端往返)+ 会话上下文
// 回填(target_session_id 缺省=当前会话、approval_warning),权威校验(422)在
// server,身份/设备归属闸在 agent_listener_api.go(I1 id-guard + 设备面
// deviceIdOverride 双保险)——本层不得弱化任何一道既有闸,也不抢答 server 的
// schema 错误。
//
// 审批警示词表事实(文档化本地决策):会话级 approval_mode 词表是
// ""/ask_all/bypass_all/read_only(agent_approval_policy.go),没有字面 "require"
// 挡位;'require' 是逐调用的策略决策。ask_all(每次必审)与 read_only(写/
// 执行族必审)是会把无人值守的注入回合困死在审批卡上的两个挡位 → 警示;
// 默认 ""(balanced 策略,常见操作自动放行)与 bypass_all → 不警示。
//
// 缝纪律(同 Task 7-11):复用 agent_listener_api.go 的云端客户端缝
// (ListenerCreateViaCloud 族 + listenerCloudBaseURL/sharedAgentService),
// 不另起炉灶;测试经既有 seam 注入假 cloud。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"aliang.one/nursorgate/app/agentcore"
	"aliang.one/nursorgate/processor/config"
)

// 监听器事件源类型常量(§1 契约;wire 值冻结,勿改)。
const (
	listenerSourceTypeFileTail    = "file_tail"
	listenerSourceTypeProcessExit = "process_exit"
	listenerSourceTypeHTTPPoll    = "http_poll"
	listenerSourceTypeWebhook     = "webhook"
)

// listenerApprovalWarningText 审批警示文案(逐字冻结,评审修订 2026-11-21:
// 点名实际挡位而非不存在的 'require' 词——会话词表无 require,触发面就是
// ask_all/read_only 两挡):监听触发的新回合无人审批会在
// agentAIApprovalTimeout 被守卫判死。
const listenerApprovalWarningText = "目标会话审批模式为 ask_all/read_only:监听触发的新回合无人审批会在超时后被判死,请保持设备在线或改用免审会话"

// listenerToolContext 工具面运行上下文(纯函数处理器的唯一外部输入):agentcore
// 桥传 run 的字段;Task 13 stdio MCP 面传它自己的会话上下文。无会话面时
// sessionID 置零——此时 target_session_id 必填(1a 现实,I2 fail-fast;1b 将
// 放开为省略自动建会话),approvalModeOf 置 nil 即跳过审批警示。
type listenerToolContext struct {
	// sessionID 当前会话 id;target_session_id 缺省时回填(回填后仍空则
	// 本地报错,见 listenerToolCreate I2)。
	sessionID string
	// approvalModeOf 目标会话审批模式解析(入参=生效的目标会话 id,含缺省
	// 回填后的值)。nil 或返回空串 = 未知/默认,不警示。
	approvalModeOf func(targetSessionID string) string
}

// listenerApprovalModeNeedsWarning 判定会话审批挡位是否对无人值守的注入
// 回合构成「审批卡判死」风险(词表映射见文件头注释)。
func listenerApprovalModeNeedsWarning(mode string) bool {
	return mode == agentAIApprovalModeAskAll || mode == agentAIApprovalModeReadOnly
}

// --- 纯函数处理器(Task 13 stdio MCP 面复用) --------------------------------

// listenerToolCreate 注册监听器(spec §5):本地预检(source.type 枚举 /
// file+process 源设备归属 / {{event}} 占位符 / mode 枚举)→ target_session_id
// 缺省回填当前会话(仍为空则本地 fail-fast,I2)→ ListenerCreateViaCloud
// (设备面,归属闸在彼处)。返回 {id, status, target_session_id} + 命中风险
// 挡位时的 approval_warning。
func listenerToolCreate(ctx context.Context, rc listenerToolContext, title, sourceType string, sourceParams map[string]interface{}, sourceDeviceID, promptTemplate, mode, targetSessionID string) (map[string]interface{}, error) {
	// I1:nil params 归一为 {}:webhook 是广告面上「params 省略」的合法调用
	// 形态,Marshal(nil map) 会把 "params":null 送上 wire——钉死 {} 形状,
	// server 侧零容忍分支。
	if sourceParams == nil {
		sourceParams = map[string]interface{}{}
	}
	sourceType = strings.TrimSpace(sourceType)
	switch sourceType {
	case listenerSourceTypeFileTail, listenerSourceTypeProcessExit, listenerSourceTypeHTTPPoll, listenerSourceTypeWebhook:
	default:
		return nil, fmt.Errorf("invalid source type %q: must be one of file_tail / process_exit / http_poll / webhook", sourceType)
	}

	mode = strings.TrimSpace(mode)
	if mode != "once" && mode != "recurring" {
		return nil, fmt.Errorf("invalid mode %q: must be once or recurring", mode)
	}

	if !strings.Contains(promptTemplate, "{{event}}") {
		return nil, fmt.Errorf("prompt_template must contain the {{event}} placeholder (rendered with the fired event payload)")
	}

	// 设备归属闸:file/process 源的事件采集在本机 agent,file_tail watcher /
	// process 句柄都只能落本机——LLM 指定异机设备属于模型幻觉或越权,本地
	// 拒绝并回显本机设备 id 供自查;空缺=本机默认( wire 层 ViaCloud 仍统一
	// 钉本机,双保险)。webhook/http_poll 是 server 侧源,device_id 入参被
	// 忽略(server 侧归一 "server"),不因带了异机 id 报错——语义上它就无设备可言。
	if sourceType == listenerSourceTypeFileTail || sourceType == listenerSourceTypeProcessExit {
		svc := GetSharedAgentService()
		if svc == nil {
			return nil, fmt.Errorf("%w: agent service unavailable", errListenerCloudOffline)
		}
		identity, shouldRun := svc.remoteConnectionSnapshot()
		if !shouldRun || identity.deviceID == "" {
			return nil, fmt.Errorf("%w: agent remote identity unavailable", errListenerCloudOffline)
		}
		if supplied := strings.TrimSpace(sourceDeviceID); supplied != "" && supplied != identity.deviceID {
			return nil, fmt.Errorf("invalid source device_id %q: file_tail/process_exit sources must run on THIS device (%s) — leave device_id empty or pass this device's id; use http_poll/webhook for remote/server-side events", supplied, identity.deviceID)
		}
	}

	if targetSessionID = strings.TrimSpace(targetSessionID); targetSessionID == "" {
		targetSessionID = strings.TrimSpace(rc.sessionID)
	}
	// I2 fail-fast(1a 现实):解析后仍为空 = 调用方既没传也没有会话上下文
	// (如 Task 13 stdio 无会话面)。1a 阶段 target 必填,本地拒绝省一次云端
	// 往返;1b 放开为省略=命中时自动建会话(spec §1 空 session 语义)。
	if targetSessionID == "" {
		return nil, fmt.Errorf("target_session_id 必填(1a):省一次云端往返;1b 将支持省略自动建会话")
	}

	params, err := json.Marshal(sourceParams)
	if err != nil {
		return nil, fmt.Errorf("encode source params: %w", err)
	}
	id, status, err := ListenerCreateViaCloud(ctx,
		strings.TrimSpace(title),
		ListenerCloudSource{Type: sourceType, Params: json.RawMessage(params)},
		promptTemplate,
		mode,
		targetSessionID,
		// SV-3 溯源:发起 run 的会话 id 上 wire(server created_by=ai:<sid>);
		// rc.sessionID 为空(stdio MCP 无会话面)则省略,server 兜底
		// user:<deviceId>。注意这是「发起会话」,与 target_session_id(事件
		// 注入目标,可异会话)语义不同。
		rc.sessionID,
	)
	if err != nil {
		return nil, err
	}
	result := map[string]interface{}{
		"id":                id,
		"status":            status,
		"target_session_id": targetSessionID,
	}
	if rc.approvalModeOf != nil {
		if targetMode := rc.approvalModeOf(targetSessionID); listenerApprovalModeNeedsWarning(targetMode) {
			result["approval_warning"] = listenerApprovalWarningText
		}
	}
	return result, nil
}

// listenerToolList 全量列表:ListenerListViaCloud 取行后经 ListenerMirror
// 类型钉契约再序列化(键=snake_case server 契约,与侧栏面板同源——spec
// 用户故事 5「无两套口径」);镜像类型未声明的 server 新字段会被丢弃——
// 展示语义有损可接受,契约漂移由 store 面测试钉住。
func listenerToolList(ctx context.Context) ([]map[string]interface{}, error) {
	rows, err := ListenerListViaCloud(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]interface{}, 0, len(rows))
	for i := range rows {
		raw, err := json.Marshal(rows[i])
		if err != nil {
			return nil, fmt.Errorf("encode listener row: %w", err)
		}
		var row map[string]interface{}
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, fmt.Errorf("decode listener row: %w", err)
		}
		out = append(out, row)
	}
	return out, nil
}

// listenerToolPatchKeys update 面允许的补丁键(其余键丢弃:server
// listenerPatchSchema 是权威闸,本层白名单是给模型的快失败提示,防拼写噪声)。
// updated_at 是 LWW 乐观锁戳,显式携带优先于本地镜像回填(见下)。
var listenerToolPatchKeys = []string{"title", "prompt_template", "cooldown_ms", "mode", "status", "updated_at"}

// listenerToolUpdate 部分更新:patch 键白名单过滤 + 本地枚举预检(M1,镜像
// create 的本地姿态)→ 本地镜像行携带 updated_at(LWW 期望值,server 必填;
// 镜像行缺失时透传,server 422 兜底)→ ListenerUpdateViaCloud(用户面 PATCH)。
func listenerToolUpdate(ctx context.Context, id string, patch map[string]interface{}) (map[string]interface{}, error) {
	id = strings.TrimSpace(id)
	// id 闸前置(与 ViaCloud 共用 listenerValidID):畸形 id 是注入面风险,
	// 不得在拒绝前先做镜像盘读。
	if !listenerValidID(id) {
		return nil, fmt.Errorf("invalid listener id")
	}
	filtered := make(map[string]interface{}, len(patch))
	for _, key := range listenerToolPatchKeys {
		if v, ok := patch[key]; ok {
			filtered[key] = v
		}
	}
	if len(filtered) == 0 {
		return nil, fmt.Errorf("empty patch: set at least one of %s", strings.Join(listenerToolPatchKeys, " / "))
	}
	// M1 本地枚举预检:与 create 同姿态(快失败省一次云端往返;server 422
	// 仍是权威闸,此处不抢答 schema 形态、只拦枚举值)。
	if v, ok := filtered["status"].(string); ok && v != "active" && v != "paused" {
		return nil, fmt.Errorf("invalid status %q: must be active or paused", v)
	}
	if v, ok := filtered["mode"].(string); ok && v != "once" && v != "recurring" {
		return nil, fmt.Errorf("invalid mode %q: must be once or recurring", v)
	}
	// LWW 期望值 updated_at:调用方显式携带(server 行,如从 listener_list 读到
	// 的比本地镜像新)优先;否则从本地镜像行回填;两者皆无则透传缺省,server
	// 422 兜底提示必填。
	if s, ok := filtered["updated_at"].(string); !ok || strings.TrimSpace(s) == "" {
		if row, found := getListenerMirror(id); found && strings.TrimSpace(row.UpdatedAt) != "" {
			filtered["updated_at"] = strings.TrimSpace(row.UpdatedAt)
		}
	}
	if err := ListenerUpdateViaCloud(ctx, id, filtered); err != nil {
		return nil, err
	}
	return map[string]interface{}{"id": id, "updated": true}, nil
}

// listenerToolDelete 删除(用户面 DELETE;畸形 id 在 ViaCloud 层被 id-guard
// 本地拒绝,零云端调用)。
func listenerToolDelete(ctx context.Context, id string) (map[string]interface{}, error) {
	if err := ListenerDeleteViaCloud(ctx, strings.TrimSpace(id)); err != nil {
		return nil, err
	}
	return map[string]interface{}{"id": strings.TrimSpace(id), "deleted": true}, nil
}

// listenerToolJSONErr 把处理器错误打成模型可读 JSON 结果(成功面也是 JSON
// 对象,错误保持同构)。
func listenerToolJSONErr(err error) (string, bool) {
	out, _ := json.Marshal(map[string]interface{}{"error": err.Error()})
	return string(out), true
}

// --- 审批模式解析(聊天层同源取法) -------------------------------------------

// listenerToolApprovalModeOf 解析目标会话的审批挡位:内存会话表优先;
// 重启后会话未回内存时从 journal 头行兜底——与聊天层 create/message 的
// approval_mode 回填取法同源(agent_ai.go)。未知会话返回空串(不猜,
// 调用方按默认挡位处理=不警示)。锁纪律:不持 m.mu 读 journal。
func (m *agentAIManager) listenerToolApprovalModeOf(sessionID string) string {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return ""
	}
	m.mu.Lock()
	session := m.sessions[sessionID]
	m.mu.Unlock()
	if session != nil {
		return session.approvalMode
	}
	if m.coreJournal == nil {
		return ""
	}
	if _, head, err := readAgentCoreJournalHistoryFrom(m.coreJournal.dir, sessionID); err == nil {
		return head.approvalMode
	}
	return ""
}

// --- agentcore 桥(per-run 注入) ---------------------------------------------

// agentCoreListenerTools 构建监听器四件工具桥(agentCorePeerToolBridge 同款)。
// 门控:customer.event_listeners 总闸(默认开,显式 false 关=零注入);
// readOnly run 只保 listener_list 读面——任务拍板的细粒度门控(宽于 peer 族
// 「readOnly 全剥」),故注入点在 runAgentCore 的 readOnly 块外(见
// agent_ai_agentcore.go)。journal 落行与 peer 桥同款(桥接工具自带 onCall)。
func (m *agentAIManager) agentCoreListenerTools(run agentAIRun) []agentcore.Tool {
	if !config.GetGlobalConfig().EffectiveEventListenersEnabled() {
		return nil
	}
	journal := func(tool, summary string, isErr bool, result string) {
		if m == nil || m.coreJournal == nil || strings.TrimSpace(run.sessionID) == "" {
			return
		}
		m.coreJournal.appendTool(run.sessionID, tool, summary, isErr, result)
	}
	rc := listenerToolContext{
		sessionID:      run.sessionID,
		approvalModeOf: m.listenerToolApprovalModeOf,
	}

	listenerBridge := func(name, description string, schema map[string]interface{}, execute func(ctx context.Context, input map[string]interface{}) (string, bool)) agentcore.Tool {
		return &agentCorePeerToolBridge{
			name:        name,
			description: description,
			schema:      schema,
			execute:     execute,
			journal:     journal,
		}
	}

	create := listenerBridge("listener_create",
		"Register an event listener for the user (替用户注册监听;事件命中后会以新回合注入绑定的会话,target_session_id 省略即当前会话,无当前会话时必填). "+
			"Sources: file_tail (tail a local file, params: path, match, optional max_bytes) and process_exit (params: command or pid, optional expect_code) run on THIS device only — leave source.device_id empty or pass this device's id; "+
			"http_poll (params: url, optional interval_s, match) and webhook (no params; the create result carries the callback URL) run server-side — device_id is ignored. "+
			"prompt_template MUST contain the {{event}} placeholder (rendered with the fired event payload). mode: once (fires once then expires) or recurring. "+
			"Returns {id, status, target_session_id} plus approval_warning when the target session's approval mode would strand unattended turns on approval cards.",
		peerToolSchema([]string{"title", "source", "prompt_template", "mode"}, map[string]interface{}{
			"title": map[string]interface{}{"type": "string", "description": "Human-readable listener title, e.g. 盯 build.log 等编译报错"},
			"source": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"type":      map[string]interface{}{"type": "string", "enum": []interface{}{"file_tail", "process_exit", "http_poll", "webhook"}, "description": "Event source type"},
					"device_id": map[string]interface{}{"type": "string", "description": "Event-source device; file_tail/process_exit: this device only (leave empty for this machine, foreign ids are rejected); http_poll/webhook: ignored (server-side)"},
					"params":    map[string]interface{}{"type": "object", "description": "Source params per type (file_tail: path/match/max_bytes; process_exit: command|pid/expect_code; http_poll: url/interval_s/match; webhook: none)"},
				},
				"required": []interface{}{"type"},
			},
			"prompt_template":   map[string]interface{}{"type": "string", "description": "Prompt injected when the listener fires; must contain {{event}}"},
			"mode":              map[string]interface{}{"type": "string", "enum": []interface{}{"once", "recurring"}, "description": "once = fire once then expire (default 24h window); recurring = repeat with cooldown (default 10min)"},
			"target_session_id": map[string]interface{}{"type": "string", "description": "Session that receives the injected turn; omit for the current session. Required when there is no current session context (1a; a future release will allow empty = create a new session on fire)"},
		}),
		func(ctx context.Context, input map[string]interface{}) (string, bool) {
			source, _ := input["source"].(map[string]interface{})
			var sourceType, sourceDeviceID string
			var sourceParams map[string]interface{}
			if source != nil {
				sourceType = remoteString(source, "type")
				sourceDeviceID = remoteString(source, "device_id")
				sourceParams, _ = source["params"].(map[string]interface{})
			}
			result, err := listenerToolCreate(ctx, rc,
				inputStringField(input, "title"),
				sourceType,
				sourceParams,
				sourceDeviceID,
				inputStringField(input, "prompt_template"),
				inputStringField(input, "mode"),
				inputStringField(input, "target_session_id"),
			)
			if err != nil {
				return listenerToolJSONErr(err)
			}
			out, err := json.Marshal(result)
			if err != nil {
				return listenerToolJSONErr(err)
			}
			return string(out), false
		})

	list := listenerBridge("listener_list",
		"List every listener in this Aliang account (same source as the sidebar listener panel — no second schema): id, title, source_type, status, mode, target_session_id, last_fired_at, fired_count, cooldown_ms, error_note, updated_at. Read-only.",
		peerToolSchema(nil, map[string]interface{}{}),
		func(ctx context.Context, _ map[string]interface{}) (string, bool) {
			rows, err := listenerToolList(ctx)
			if err != nil {
				return listenerToolJSONErr(err)
			}
			out, err := json.Marshal(rows)
			if err != nil {
				return listenerToolJSONErr(err)
			}
			return string(out), false
		})

	update := listenerBridge("listener_update",
		"Patch a listener by id: pause (status=paused), resume (status=active — also revives an error row), or edit title / prompt_template (must keep {{event}}) / mode / cooldown_ms. "+
			"The updated_at LWW stamp is carried automatically from the local mirror; pass your own only to retry after a 409 conflict.",
		peerToolSchema([]string{"id"}, map[string]interface{}{
			"id":              map[string]interface{}{"type": "string", "description": "Listener id (L-...) from listener_list"},
			"title":           map[string]interface{}{"type": "string", "description": "New title"},
			"prompt_template": map[string]interface{}{"type": "string", "description": "New prompt template (must contain {{event}})"},
			"cooldown_ms":     map[string]interface{}{"type": "integer", "description": "Recurring dedupe window in ms (default 600000)"},
			"mode":            map[string]interface{}{"type": "string", "enum": []interface{}{"once", "recurring"}, "description": "once or recurring"},
			"status":          map[string]interface{}{"type": "string", "enum": []interface{}{"active", "paused"}, "description": "active = run/revive; paused = hold"},
			"updated_at":      map[string]interface{}{"type": "string", "description": "LWW 乐观锁戳,ISO UTC Z;省略则自动携带本地镜像值——仅在遇 409 冲突重试时从 listener_list 取最新值填入"},
		}),
		func(ctx context.Context, input map[string]interface{}) (string, bool) {
			patch := make(map[string]interface{})
			for _, key := range listenerToolPatchKeys {
				if v, ok := input[key]; ok {
					patch[key] = v
				}
			}
			result, err := listenerToolUpdate(ctx, inputStringField(input, "id"), patch)
			if err != nil {
				return listenerToolJSONErr(err)
			}
			out, err := json.Marshal(result)
			if err != nil {
				return listenerToolJSONErr(err)
			}
			return string(out), false
		})

	del := listenerBridge("listener_delete",
		"Delete a listener by id. The listener stops firing immediately and disappears from every device's sidebar panel.",
		peerToolSchema([]string{"id"}, map[string]interface{}{
			"id": map[string]interface{}{"type": "string", "description": "Listener id (L-...) from listener_list"},
		}),
		func(ctx context.Context, input map[string]interface{}) (string, bool) {
			result, err := listenerToolDelete(ctx, inputStringField(input, "id"))
			if err != nil {
				return listenerToolJSONErr(err)
			}
			out, err := json.Marshal(result)
			if err != nil {
				return listenerToolJSONErr(err)
			}
			return string(out), false
		})

	if run.readOnly {
		// readOnly run 只保读面(任务拍板):写三件剥离,list 保留供模型感知
		// 既有监听器。注入点在 runAgentCore 的 readOnly 块外才走得进来。
		return []agentcore.Tool{list}
	}
	return []agentcore.Tool{create, list, update, del}
}
