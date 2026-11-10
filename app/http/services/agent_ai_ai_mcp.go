package services

// agent-as-MCP 端点后端(spec §4/§5/§7):外部 MCP 客户端经本机 HTTP 驱动
// agentcore。受理复用 m.message / m.approval 帧式链(错误与 ack 全走 writeJSON
// 帧),受理结果经捕获型 writer 扫帧取回;HTTP 响应返回后 writer 置惰——run 的
// emitter 持有该 writer 直到 run 终结,不置惰则 delta/terminal 帧会持续写进
// 缓冲无限增长(spec §7「核心机制」)。
//
// session 契约(spec §5):工具面只能驱动/批准本面创建的会话,createBy 内存标记
// 三处校验(续聊 start / status / approve)。标记不落盘、不入 journal——runtime
// 重启后标记随会话内存态消失(run 本就中断),一致性自然成立。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
)

// agentAIMCPCreatedBy 是工具面会话的来源标记值(agentAISession.createdBy)。
const agentAIMCPCreatedBy = "ai_mcp"

// agentAIMCPLastAssistantCap last_assistant 摘要的字符(rune)上限。按 rune 截断
// 而非字节:中文回复按字节切会把最后一个字劈成半个乱码。
const agentAIMCPLastAssistantCap = 400

// agentAIMCPCaptureWriter 捕获型 writer:受理期间收集帧;置惰后丢弃。
//
// 生命周期与 HTTP 响应严格对齐:受理函数返回 → 扫帧 → 置惰。置惰后写入是
// 无条件 no-op(返回 nil),让 run 的 emitter 继续正常跑完而不影响应答。
type agentAIMCPCaptureWriter struct {
	mu     sync.Mutex
	frames []map[string]interface{}
	inert  bool
}

// write 适配 agentTerminalWriter(func(interface{}) error)。非 map 帧直接忽略
// (受理链只产 map 帧,这里不为非预期形态报错——writer 的错误语义归调用方,
// 而受理函数的返回值早已被丢弃)。
func (w *agentAIMCPCaptureWriter) write(raw interface{}) error {
	frame, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.inert {
		return nil
	}
	w.frames = append(w.frames, frame)
	return nil
}

// snapshot 拷贝返回已捕获帧(调用方拿到的是切片副本,帧本身不可变——受理链
// 写出的 payload 一经写出即视为冻结,见 fanOutWrite 契约)。
func (w *agentAIMCPCaptureWriter) snapshot() []map[string]interface{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]map[string]interface{}(nil), w.frames...)
}

// inertize 置惰:此后 write 全部丢弃,缓冲不再增长。
func (w *agentAIMCPCaptureWriter) inertize() {
	w.mu.Lock()
	w.inert = true
	w.mu.Unlock()
}

// agentAIMCPNewCaptureWriter 捕获 writer 工厂(测试 seam:测试替换以持有端点
// 内部实际使用的 writer,断言置惰后不再增长;nil 上指针相等性无关紧要,生产
// 路径恒为默认实现)。
var agentAIMCPNewCaptureWriter = func() *agentAIMCPCaptureWriter {
	return &agentAIMCPCaptureWriter{}
}

// scanAIMCPErrorFrames 扫错误帧(agentAIErrorPayload 形态:type=ai.error 且
// 文案在 "error" 键)。返回 (文案, true);无错误帧 → ("", false)。
// message()/approval() 的受理失败都在同一同步段内经 writeJSON 写出,所以
// 「扫到错误帧」等价于「本次受理被拒」。
func scanAIMCPErrorFrames(frames []map[string]interface{}) (string, bool) {
	for _, frame := range frames {
		if frame == nil {
			continue
		}
		if remoteString(frame, "type") != models.AgentEventAIError {
			continue
		}
		return remoteString(frame, "error"), true
	}
	return "", false
}

// scanAIMCPAckResult 取 ai.approval.ack 帧的 result 字段(applied/no_match/
// duplicate)。无 ack → 空串(端点按 applied=false 处理,不报错:决议没生效
// 是受理链的正常语义,不是错误)。
func scanAIMCPAckResult(frames []map[string]interface{}) string {
	for _, frame := range frames {
		if frame == nil {
			continue
		}
		if remoteString(frame, "type") != models.AgentEventAIApprovalAck {
			continue
		}
		return remoteString(frame, "result")
	}
	return ""
}

// agentCoreAIMCPEnabledFn 工具面总闸 seam(沿 agentCoreMemoryEnabledFn 先例,
// 测试可换)。
var agentCoreAIMCPEnabledFn = func() bool {
	return config.GetGlobalConfig().EffectiveAgentCoreAIMCPEnabled()
}

// agentAIMCPNewSessionID 生成工具面的 session_id / message_id / delivery_id。
// 走 uuid v4(与 tool_ask 同源),不落盘:session 身份仍由 agent 内存态权威。
func agentAIMCPNewSessionID() string {
	return uuid.NewString()
}

// agentAIMCPStatusForError 把受理链错误文案映射 HTTP 码:busy → 409、
// 会话不存在 → 400(spec §8 基线,不用 404)、其余 → 400。措辞漂移落 400 是
// 安全向(绝不把「受理失败」映射成 2xx)。
func agentAIMCPStatusForError(msg string) int {
	switch {
	case strings.Contains(msg, "already running"):
		return http.StatusConflict
	case strings.Contains(msg, "会话不存在"):
		return http.StatusBadRequest
	default:
		return http.StatusBadRequest
	}
}

// agentAIMCPPreflight 三操作共同前置(spec §7),顺序固定:
// 门控 → 设备 AI 控制门(与 WS 分发器 peer parity,端点直调绕过分发器) →
// AI service 就绪。守卫在受理链之前,避免「先建了会话再被门拒」。
// 返回 nil 表示放行;非 nil 为 (状态码, error)。
func (s *AgentService) agentAIMCPPreflight() (int, error) {
	if !agentCoreAIMCPEnabledFn() {
		return http.StatusServiceUnavailable, errors.New("agent-as-MCP 工具面已关闭")
	}
	if !s.aiControlEnabled() {
		return http.StatusForbidden, errors.New("AI control is disabled for this device")
	}
	if s.ai == nil {
		return http.StatusServiceUnavailable, errors.New("AI service is not ready")
	}
	return 0, nil
}

// agentAIMCPContractCheck 会话契约校验(spec §5):存在性 + 本面归属。
// 必须持 m.mu 调用。返回 (状态码, error);0 表示通过。
// 文案不含任何本地绝对路径——错误会原样回到外部 AI 客户端。
func agentAIMCPContractCheck(m *agentAIManager, sid string) (int, error) {
	cur, has := m.sessions[sid]
	if !has || cur == nil {
		return http.StatusBadRequest, errors.New("会话不存在: " + sid)
	}
	if cur.createdBy != agentAIMCPCreatedBy {
		return http.StatusForbidden, errors.New("会话不属于 AI 工具面")
	}
	return 0, nil
}

// AgentMCPStart 是 ai_run_start 的端点后端:发起一次异步 run,受理即返回。
//
// 无 sessionID → 新建工具面专属会话(UUID;懒建分支据此落会话)+ 打标;
// 有 sessionID → 续聊,先校验会话存在且属本面。
//
// 并发纪律(spec §7):绝不持 m.mu 跨 m.message——它内部多次取锁,持锁调内
// 加锁 = 本仓已录事故类(自锁死)。
func (s *AgentService) AgentMCPStart(ctx context.Context, projectPath, content, provider, sessionID string) (map[string]interface{}, int, error) {
	if code, err := s.agentAIMCPPreflight(); err != nil {
		return nil, code, err
	}
	content = strings.TrimSpace(content)
	projectPath = strings.TrimSpace(projectPath)
	if content == "" || projectPath == "" {
		return nil, http.StatusBadRequest, errors.New("content 与 project_path 必填")
	}
	m := s.ai
	sid := strings.TrimSpace(sessionID)
	resumed := sid != ""

	m.mu.Lock()
	if resumed {
		if code, err := agentAIMCPContractCheck(m, sid); err != nil {
			m.mu.Unlock()
			return nil, code, err
		}
	} else {
		// UUID 空间下撞名不可达;循环只为契约严密(撞上任何既有会话就换号)。
		sid = agentAIMCPNewSessionID()
		for {
			if cur, has := m.sessions[sid]; !has || cur == nil {
				break
			}
			sid = agentAIMCPNewSessionID()
		}
	}
	m.mu.Unlock()

	frame := map[string]interface{}{
		"session_id":   sid,
		"message_id":   agentAIMCPNewSessionID(),
		"content":      content,
		"project_path": projectPath,
	}
	if strings.TrimSpace(provider) != "" {
		frame["provider"] = strings.TrimSpace(provider)
	}
	cw := agentAIMCPNewCaptureWriter()
	// 受理链:懒建/续聊、busy 守卫、会话级开关、TUI/spawn 预检、provider 解析
	// 全部原样生效——工具面不绕过任何既有校验。
	m.message(frame, cw.write)
	if msg, bad := scanAIMCPErrorFrames(cw.snapshot()); bad {
		cw.inertize()
		return nil, agentAIMCPStatusForError(msg), errors.New(msg)
	}

	// 打标时序(spec §5 定稿):受理之后再取一次锁补标。
	// 新建路径:受理成功即补标(UUID 预检已排除撞名)。
	// 续聊路径:回取复检——预检通过后若发生逐出重建(逐出只护 running 会话,
	// 重建会话 createdBy 为空),**不补标**,契约降级:本 run 照跑,但后续
	// status/approve 会拒(文案同「会话不属于 AI 工具面」),Warn 留痕。
	m.mu.Lock()
	if cur, has := m.sessions[sid]; has && cur != nil {
		if resumed {
			if cur.createdBy != agentAIMCPCreatedBy {
				logger.Warn(fmt.Sprintf("ai-mcp: 续聊会话 createdBy 漂移(疑逐出重建),契约降级 sid=%s", sid))
			}
		} else if cur.createdBy == "" {
			cur.createdBy = agentAIMCPCreatedBy
		}
	}
	m.mu.Unlock()

	cw.inertize()
	return map[string]interface{}{"session_id": sid, "accepted": true}, http.StatusOK, nil
}

// AgentMCPStatus 是 ai_run_status 的端点后端:纯读内存态(running/idle/
// awaiting_approval + 待审批清单 + 助手尾部摘要)。刻意不经 journal 磁盘读
// (spec §4.2:内存态即真相,读面/重放是非目标)。
func (s *AgentService) AgentMCPStatus(ctx context.Context, sessionID string) (map[string]interface{}, int, error) {
	if code, err := s.agentAIMCPPreflight(); err != nil {
		return nil, code, err
	}
	m := s.ai
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return nil, http.StatusBadRequest, errors.New("session_id 必填")
	}
	m.mu.Lock()
	if code, err := agentAIMCPContractCheck(m, sid); err != nil {
		m.mu.Unlock()
		return nil, code, err
	}
	// last_assistant:从尾向前找第一条 assistant 历史,按 rune 截断。
	last := ""
	if cur := m.sessions[sid]; cur != nil {
		for i := len(cur.history) - 1; i >= 0; i-- {
			if cur.history[i].Role == "assistant" {
				last = truncateRunes(cur.history[i].Content, agentAIMCPLastAssistantCap)
				break
			}
		}
	}
	m.mu.Unlock()

	// 两个读原语各自取锁(agentAISessionStatusForSnapshot /
	// pendingApprovalsSnapshotForSession),必须在 m.mu 之外调用。
	state := m.agentAISessionStatusForSnapshot(sid)
	pendings := m.pendingApprovalsSnapshotForSession(sid)
	if pendings == nil {
		// 载荷不许出 null:外部工具按数组消费,JSON null 会让 schema 校验失配。
		pendings = make([]map[string]interface{}, 0)
	}
	if state == "running" && len(pendings) > 0 {
		state = "awaiting_approval"
	}
	return map[string]interface{}{
		"session_id":        sid,
		"state":             state,
		"pending_approvals": pendings,
		"last_assistant":    last,
	}, http.StatusOK, nil
}

// AgentMCPApprove 是 ai_run_approve 的端点后端:决议走 m.approval 既有受理链
// (挡位/allowed_roots/重复决议幂等 ack 等校验全部原样生效,决议零绕行),
// applied 取 ack 结果——no_match/duplicate 亦 ack 成功但 applied=false。
func (s *AgentService) AgentMCPApprove(ctx context.Context, sessionID, approvalID, decision string) (map[string]interface{}, int, error) {
	if code, err := s.agentAIMCPPreflight(); err != nil {
		return nil, code, err
	}
	m := s.ai
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return nil, http.StatusBadRequest, errors.New("session_id 必填")
	}
	m.mu.Lock()
	if code, err := agentAIMCPContractCheck(m, sid); err != nil {
		m.mu.Unlock()
		return nil, code, err
	}
	m.mu.Unlock()

	cw := agentAIMCPNewCaptureWriter()
	m.approval(map[string]interface{}{
		"session_id":  sid,
		"approval_id": strings.TrimSpace(approvalID),
		"decision":    decision,
		// delivery_id 用于服务端重投引擎的对端确认;工具面每次决议新生成。
		"delivery_id": agentAIMCPNewSessionID(),
	}, cw.write)
	if msg, bad := scanAIMCPErrorFrames(cw.snapshot()); bad {
		cw.inertize()
		return nil, agentAIMCPStatusForError(msg), errors.New(msg)
	}
	applied := scanAIMCPAckResult(cw.snapshot()) == "applied"
	cw.inertize()
	return map[string]interface{}{"ok": true, "applied": applied}, http.StatusOK, nil
}
