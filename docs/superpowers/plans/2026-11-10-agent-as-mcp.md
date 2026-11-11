# agent-as-MCP 导出面实施计划(ai_run_* 三工具)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `aliang mcp` stdio 服务端新增 ai_run_start/status/approve 三工具,外部 MCP 客户端可完整驱动一次带审批的 agentcore run(session 契约隔离手机/桌面会话)。

**Architecture:** 方案 A 薄 HTTP 端点组——真身三端点落 runtime(56433),core 侧 proxy 透传;端点后端经**捕获型 writer** 复用 `m.message`/`m.approval` 帧式受理链(受理结果从捕获帧扫出),writer 在 HTTP 响应后置惰;session 契约=`createdBy` 内存标记三处校验;门控 `customer.ai_mcp` 三态默认开,关=工具缺席+端点同拒 503。spec=docs/superpowers/specs/2026-11-10-agent-as-mcp-design.md(两轮评审定稿)。

**Tech Stack:** Go(原生 ServeMux 方法前缀路由/httptest)、cmd/mcp.go 既有 JSON-RPC stdio 面、前端 normalize 两挂键(无 UI)、vitest。

---

## 全局纪律(每任务适用)

1. **worktree 操作**(Task 0 建),不在主仓改代码。gopls「not in workspace」误报一律无视;**禁止 `-race`**(仓内预存 flake)。
2. 注释中文,风格对齐文件内既有注释;提交标题「新增:/修复:/测试:」全中文,HEREDOC+两条 footer(`🤖 Generated with [Claude Code](https://claude.com/claude-code)` + `Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>`)。
3. **测试纪律**:goroutine/异步回调闭包内禁 `t.Fatalf`;新测试先 `-v -run` 确认真实执行;全包回归必须整体 ok。
4. **前置事实速查**(实施者不需要再探):受理函数无返回值(错误/ack 全走 writeJSON 帧);`agentAIErrorPayload(sessionID, messageID, err)` 造错误帧;busy 守卫文案 `ai session is already running: <id>`(agent_ai.go:2456);`ai.approval.ack` 帧 `{type, result: "applied"|"no_match"|"duplicate"}`;`session.history []agentAIMessage{Role,MessageID,Content,CreatedAt}`(:473);`session.cancel != nil` = running(TUI 裸注册会话无 cancel,工具面会话必经 run 无此形态);`pendingApprovalsSnapshot()`(:4107) 读 m.approvals waiter map;`agentTerminalWriter` 形态以 services 包内实际类型为准(对齐后写捕获 writer);冷子进程 `config.GetGlobalConfig()` 返回 nil(不加载 customer config)。

---

### Task 0: worktree 准备

**Files:** 无代码改动。

- [ ] **Step 1: 建 worktree + 拷 dist + 拷文档**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
git worktree add ../alianggate-aimcp -b feat/agent-as-mcp
cp -R app/website/dist /Users/mac/MyProgram/GoProgram/nursor/alianggate-aimcp/app/website/
W=/Users/mac/MyProgram/GoProgram/nursor/alianggate-aimcp
mkdir -p $W/docs/superpowers/specs $W/docs/superpowers/plans
cp docs/superpowers/specs/2026-11-10-agent-as-mcp-design.md $W/docs/superpowers/specs/
cp docs/superpowers/plans/2026-11-10-agent-as-mcp.md $W/docs/superpowers/plans/
```

- [ ] **Step 2: 基线绿**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate-aimcp && go build ./... && go test ./app/http/services -run '^TestAgentCoreMCP' -count=1 && go test ./cmd -count=1
```
Expected: 全 ok(基线不绿先停)。

---

### Task 1: customer.ai_mcp 开关 + normalize 两挂键

**Files:**
- Modify: `processor/config/types.go`(AgentCoreTuningConfig :699 附近 + Effective 方法区)
- Modify: `app/website/src/utils/agentCoreSettings.js` + `agentCoreSettings.test.js`(normalize 第三布尔键)
- Test: `processor/config/agentcore_config_test.go`

- [ ] **Step 1: 字段**(紧邻 MemoryEnabled,同款三态指针)

```go
	// AIMCP 是 agent-as-MCP 工具面的总开关(customer.ai_mcp)。三态指针沿
	// MCPEnabled/SkillsEnabled/MemoryEnabled 先例：nil=未配置（默认开启）、
	// true/false 显式。只门控 ai_run_* 三工具（aliang mcp stdio 第 6 工具组），
	// 不碰 peer 工具面。stdio 同用户信任模型下不引入新权限（同用户本可直打
	// 管理口），独立键只为可关。
	AIMCP *bool `json:"ai_mcp,omitempty"`
```

- [ ] **Step 2: Effective 方法**(紧邻 EffectiveAgentCoreMemoryEnabled)

```go
// EffectiveAgentCoreAIMCPEnabled 返回 agent-as-MCP 工具面是否开启。
// 默认开启(同家族):nil(未配置)与 true 等价。
func (c *Config) EffectiveAgentCoreAIMCPEnabled() bool {
	if c == nil || c.Customer == nil || c.Customer.AgentCore == nil {
		return true
	}
	if c.Customer.AgentCore.AIMCP == nil {
		return true
	}
	return *c.Customer.AgentCore.AIMCP
}
```

- [ ] **Step 3: 测试**(沿 TestEffectiveAgentCoreMemoryEnabled 的 6 例表驱动同款,换字段名) + `go test ./processor/config -count=1` 全绿。

- [ ] **Step 4: 前端 normalize 两挂键**(memory_enabled 同款,漏挂=永不落盘):
  1. `agentCoreSettings.js` normalizeAgentCoreTuning 按既有布尔键同款透传 `ai_mcp`(非布尔/null 不透传、缺键不产出);
  2. `agentCoreSettings.test.js` 期望值加该键(透传/非布尔/缺键三组);
  3. SettingsPage normalizeCustomerConfig 经共享出口自动接通(记忆批已验,补注释即可)。
  Run: `cd app/website && npx vitest run src/utils/agentCoreSettings.test.js` 全绿。

- [ ] **Step 5: 副本同步排查** + Commit

```bash
grep -rn "memory_enabled" --include="*.go" --include="*.vue" --include="*.js" . | grep -v node_modules | grep -v worktrees
# 逐一核对 Go 影子副本;前端 UI 卡片 v1 不做(记账)
git add processor/config/ app/website/src/utils/agentCoreSettings.js app/website/src/utils/agentCoreSettings.test.js app/website/src/components/SettingsPage.vue
git commit -m "$(cat <<'EOF'
新增:customer.ai_mcp agent-as-MCP 工具面三态开关(默认开)+normalize 两挂键透传

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: session 契约标记 + 端点后端核心(services)

**Files:**
- Modify: `app/http/services/agent_ai.go`(agentAISession 加 `createdBy string`)
- Create: `app/http/services/agent_ai_ai_mcp.go`(端点后端:捕获 writer/三操作/契约校验)
- Test: `app/http/services/agent_ai_ai_mcp_test.go`

- [ ] **Step 1: 写失败测试**(新文件;沿包内既有 httptest/manager 构造形态)

覆盖(spec §9 端点后端部分;handler 层归 Task 3):
1. `AgentMCPStart` 新建:返回 session_id;manager 内该会话 `createdBy=="ai_mcp"` 实据;
2. `AgentMCPStart` 续聊:带本面 session_id 追加成功;带非本面 id → 403 `会话不属于 AI 工具面`;
3. `AgentMCPStart` 空 content/project_path → 400 原文案;
4. busy:预置运行中会话(构造 cancel 非 nil)→ 409 文案含 `ai session is already running`;
5. `AgentMCPStatus`:本面会话返回 state/pending_approvals/last_assistant;非本面 403;不存在 400(spec §8 基线);
6. `AgentMCPApprove`:决议帧经 m.approval 受理(ack 捕获,applied 映射);非本面 403;decision 非法 → 受理链原文;
7. 门控关(EffectiveAgentCoreAIMCPEnabled seam 或注入 false)→ 503 固定文案;
8. aiControl 关(构造设备开关关态,对齐 `aiControlEnabled` 的读取路径)→ 手机面同文案;
9. writer 置惰:run 帧不滞留(捕获 buffer 在响应后不再增长——用可注入的帧源断言);
10. 逐出重建降级:构造逐出后 start → 复检 createdBy 空 → Warn + 后续 status 403;
11. **ack 双写仍落 run socket**(跨端同态回归:注册 originSocket 的 manager 形态,approve 后断言 run socket 收到 ack——沿 m.approval 双写语义);
12. **AI service 未就绪**(s.ai=nil)→ 503 先例文案。

测试注入 seam:manager 经 `AgentService.ai` 真身(包内直构),config 门经包级 seam 变量(沿 agentCoreMemoryEnabledFn 先例: `var agentCoreAIMCPEnabledFn = func() bool { return config.GetGlobalConfig().EffectiveAgentCoreAIMCPEnabled() }`,测试可换)。

- [ ] **Step 2: 实现**(核心代码骨架;**agentTerminalWriter 实际类型名以包内为准**,对齐后落捕获 writer)

```go
package services

// agent-as-MCP 端点后端(spec §4/§5/§7):外部 MCP 客户端经本机 HTTP 驱动
// agentcore。受理复用 m.message/m.approval 帧式链(错误/ack 全走 writeJSON
// 帧),结果经捕获型 writer 扫帧取回;HTTP 响应后 writer 置惰(run 的 emitter
// 持 writer 至 run 终结,不置惰则缓冲无限增长)。

const agentAIMCPCreatedBy = "ai_mcp"

// agentAIMCPCaptureWriter 捕获型 writer:受理期间收集帧;置惰后丢弃。
type agentAIMCPCaptureWriter struct {
	mu     sync.Mutex
	frames []map[string]interface{}
	inert  bool
}

// write 适配 agentTerminalWriter 实际签名 func(interface{}) error
// (agent_terminal.go:34)——带 ok 守卫类型断言,非 map 帧忽略。
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

func (w *agentAIMCPCaptureWriter) snapshot() []map[string]interface{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]map[string]interface{}(nil), w.frames...)
}

func (w *agentAIMCPCaptureWriter) inertize() {
	w.mu.Lock()
	w.inert = true
	w.mu.Unlock()
}

// scanAIMCPErrorFrames 找错误帧(agentAIErrorPayload 形态,键以实际为准):
// 返回 (文案, true);无错误帧 → ("", false)。
func scanAIMCPErrorFrames(frames []map[string]interface{}) (string, bool) { /* 实现按 agentAIErrorPayload 实际键 */ }

// scanAIMCPAckResult 找 ai.approval.ack 帧的 result 字段。
func scanAIMCPAckResult(frames []map[string]interface{}) string { /* type=="ai.approval.ack" → result */ }

// 端点后端三操作(AgentService 导出方法,handler 层直调)。
// 返回 (载荷, HTTP 状态码, err==nil 恒定;错误用状态码+载荷文案表达)。

func (s *AgentService) AgentMCPStart(ctx context.Context, projectPath, content, provider, sessionID string) (map[string]interface{}, int, error) {
	if !agentCoreAIMCPEnabledFn() {
		return nil, http.StatusServiceUnavailable, errors.New("agent-as-MCP 工具面已关闭")
	}
	if !s.aiControlEnabled() {
		return nil, http.StatusForbidden, errors.New("AI control is disabled for this device")
	}
	if s.ai == nil {
		return nil, http.StatusServiceUnavailable, errors.New("AI service is not ready")
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
	if sid != "" {
		existing, has := m.sessions[sid]
		if !has || existing == nil {
			m.mu.Unlock()
			return nil, http.StatusBadRequest, errors.New("会话不存在: " + sid) // spec §8 基线 400
		}
		if existing.createdBy != agentAIMCPCreatedBy {
			m.mu.Unlock()
			return nil, http.StatusForbidden, errors.New("会话不属于 AI 工具面")
		}
	} else {
		// 新建路径:防御性检查 UUID 撞名(撞上既有非本面会话则换号重试语义
		// 交由调用方;UUID 空间下实际不可达,检查只为契约严密)
		sid = agentAIMCPNewSessionID()
		for {
			// 存在即重试(UUID 空间不可达,判向从严:任何既有会话都换号)
			if cur, has := m.sessions[sid]; !has || cur == nil {
				break
			}
			sid = agentAIMCPNewSessionID()
		}
	}
	m.mu.Unlock()

	cw := &agentAIMCPCaptureWriter{}
	frame := map[string]interface{}{
		"session_id": sid, "message_id": agentAIMCPNewSessionID(),
		"content": content, "project_path": projectPath,
	}
	if provider != "" {
		frame["provider"] = provider
	}
	m.message(frame, cw.write)
	if errMsg, bad := scanAIMCPErrorFrames(cw.snapshot()); bad {
		return nil, agentAIMCPStatusForError(errMsg), errors.New(errMsg)
	}
	// 打标时序(spec §5 定稿):不持 m.mu 跨 message()(其内部多次取锁,
	// 持锁调内加锁=本仓已录事故类)。
	// 新建路径:受理成功即无条件补标(UUID 预检已排除撞名);
	// 续聊路径:回取复检 createdBy==ai_mcp 才维持——逐出重建的空白会话
	// **不补标**(Warn 留痕),本 run 照跑但后续 status/approve 拒绝,
	// 契约降级为重新 start 新会话。
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

func (s *AgentService) AgentMCPStatus(ctx context.Context, sessionID string) (map[string]interface{}, int, error) {
	// 门控/aiControl/nil 同款前置(略,同 Start)
	m := s.ai
	// 状态读面:vibeSessionsSnapshot(true) 过滤(与仪表盘同源 state 计算;
	// 字段 ID/Status——models.AgentVibeSession 无 State 字段)。
	// awaiting_approval 派生规则(写死):Status==running 且
	// pendingApprovalsSnapshot() 过滤 session_id 非空 → "awaiting_approval";
	// 否则透传 Status(idle/running)。
	for _, vs := range m.vibeSessionsSnapshot(true) {
		if vs.ID == sessionID { /* 命中即组装载荷;未命中走 sessions map 直查兜底(idle 且无 run) */ }
	}
	// 会话存在性+createdBy 校验走 m.mu 直查(snapshot 不含 idle-absent 态时兜底)
	// pendingApprovalsSnapshot() 过滤 session_id
	// last_assistant:session.history 尾部向前找 assistant 角色,截断 400 rune
}

func (s *AgentService) AgentMCPApprove(ctx context.Context, sessionID, approvalID, decision string) (map[string]interface{}, int, error) {
	// 前置同 Start;契约同 Status;然后:
	cw := &agentAIMCPCaptureWriter{}
	m.approval(map[string]interface{}{
		"session_id": sessionID, "approval_id": approvalID,
		"decision": decision, "delivery_id": agentAIMCPNewSessionID(),
	}, cw.write)
	if errMsg, bad := scanAIMCPErrorFrames(cw.snapshot()); bad {
		return nil, agentAIMCPStatusForError(errMsg), errors.New(errMsg)
	}
	applied := scanAIMCPAckResult(cw.snapshot()) == "applied"
	cw.inertize()
	return map[string]interface{}{"ok": true, "applied": applied}, http.StatusOK, nil
}

// agentAIMCPStatusForError 把受理链错误文案映射 HTTP 码(busy→409/
// 会话不存在→400(spec §8 基线,非 404)/其余→400;白名单前缀匹配,
// 措辞漂移落 400 安全向)。503(门控/未就绪)不经此函数,直接 http.Error。
func agentAIMCPStatusForError(msg string) int { /* strings.Contains 分诊 */ }
```

`agentAISession` 加字段(agent_ai.go :413 区域,projectPath 邻位):

```go
	// createdBy 是会话创建来源标记(内存态,零序列化):ai_mcp=agent-as-MCP
	// 工具面创建,空=既有来源(手机/桌面/恢复装载)。工具面三操作以此做
	// session 契约校验(runtime 重启随会话态消失,一致性自然成立)。
	createdBy string
```

`agentCoreAIMCPEnabledFn` seam 变量(沿 agentCoreMemoryEnabledFn 先例)。`agentAIMCPNewSessionID()` 用仓内既有 uuid 生成器(grep `uuid.NewString\|NewUUID` 对齐;无则 crypto/rand 16 字节 hex)。

- [ ] **Step 3: 跑测试**

```bash
go build ./... && go vet ./app/http/services/ && go test ./app/http/services -run 'TestAgentMCP' -count=1 -v
```
Expected: 全 PASS(既有回归不破)。

- [ ] **Step 4: Commit**

```bash
git add app/http/services/agent_ai.go app/http/services/agent_ai_ai_mcp.go app/http/services/agent_ai_ai_mcp_test.go
git commit -m "$(cat <<'EOF'
新增:agent-as-MCP 端点后端——捕获 writer 复用 message/approval 受理链,createdBy 会话契约三处校验

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: runtime 三端点 + core proxy 透传

**Files:**
- Modify: `app/http/handlers/agent_handler.go`(AgentHandler 三方法,HandleAIApprovalHook :302 邻位)
- Modify: `app/agentruntime/server.go`(真身注册,ai 组邻位)
- Modify: `app/http/routes/routes.go`(core 侧注册,proxy 先例)
- Test: `app/http/handlers/agent_ai_mcp_handler_test.go` + `app/agentruntime/server_test.go` 增测

- [ ] **Step 1: handler 三方法**(沿 HandleAIApprovalHook 形态:POST only+64KB MaxBytesReader+common 信封)

```go
// HandleAIMCPStart POST /api/agent/ai/mcp/start — agent-as-MCP 工具面发起。
func (h *AgentHandler) HandleAIMCPStart(w http.ResponseWriter, r *http.Request) {
	h.handleAIMCPOp(w, r, func(ctx context.Context, body map[string]string) (map[string]interface{}, int, error) {
		return h.service.AgentMCPStart(ctx, body["project_path"], body["content"], body["provider"], body["session_id"])
	})
}
// HandleAIMCPStatus / HandleAIMCPApprove 同款(字段各自:session_id / session_id+approval_id+decision)

// handleAIMCPOp 公共壳:method 闸+body http.MaxBytesReader 64KB(spec §7 同族先例)
// +转调+信封。
// 信封(spec §7 硬契约,违反=工具面整体报废):
//   成功 → common.Success(w, map[string]interface{}{"result": jsonStr})
//     ——载荷 JSON 序列化塞 data.result 字符串(HandlePeerExec 同款,agent_handler.go:162;
//       invokeTool 解包只认 data.result,cmd/mcp.go:424-436)
//   失败 → 4xx 业务码经 common.Error 4 参映射(errors.go ErrorCodeToHTTPStatus);
//     503 无业务码映射 → http.Error(w, msg, 503) 真状态码(HandlePeerMcp :297 惯例)
func (h *AgentHandler) handleAIMCPOp(w http.ResponseWriter, r *http.Request, op func(context.Context, map[string]string) (map[string]interface{}, int, error)) { /* 实现沿 HandleAIApprovalHook 读体形态 */ }
```

- [ ] **Step 2: 双注册**

agentruntime/server.go(ai 组 :154 邻位):
```go
	// agent-as-MCP 工具面端点(spec §7):真身在此,core routes.go 侧 proxy 透传
	// (HandleStatus proxyIfNeeded 先例,非静态双注册)。
	mux.HandleFunc("POST /api/agent/ai/mcp/start", agentHandler.HandleAIMCPStart)
	mux.HandleFunc("POST /api/agent/ai/mcp/status", agentHandler.HandleAIMCPStatus)
	mux.HandleFunc("POST /api/agent/ai/mcp/approve", agentHandler.HandleAIMCPApprove)
```

routes.go(:299 approval-hook 邻位):
```go
	// agent-as-MCP 工具面端点:真身在 runtime,core 侧 proxy 透传(handler 内
	// proxyIfNeeded 分流,HandleStatus 先例)。
	register("/api/agent/ai/mcp/start", h.Agent.HandleAIMCPStart, http.MethodPost)
	register("/api/agent/ai/mcp/status", h.Agent.HandleAIMCPStatus, http.MethodPost)
	register("/api/agent/ai/mcp/approve", h.Agent.HandleAIMCPApprove, http.MethodPost)
```

- [ ] **Step 3: handler httptest**(handlers 包):POST only/壳层信封(成功 result 字符串形态+op 注入假实现回 4xx 各码断言 common.Error 映射)/503 走 http.Error 真状态码断言。**门控关 503 与 aiControl 关 403 两支不在此处**——需翻 services 未导出 seam,落 Task 2 的 services 包测试(清单 #7/#8 已含);本处只测壳。**core 进程 proxy 分流**:沿 HandleStatus 既有 proxy 测试形态(grep `proxyIfNeeded` 测试先例对齐)断言非 runtime 形态透传调用。
- [ ] **Step 4: agentruntime 注册冒烟**(沿记忆批 `TestRegisterAgentRoutesCoversAgentMemoryEndpoints` 形态):真调 `registerAgentRoutes(http.NewServeMux())` 探三 POST pattern 各得非 404。
- [ ] **Step 5: 验证 + Commit**

```bash
go build ./... && go vet ./app/... && go test ./app/http/handlers ./app/agentruntime ./app/http/services -count=1
git add app/http/handlers/agent_handler.go app/http/handlers/agent_ai_mcp_handler_test.go app/agentruntime/server.go app/agentruntime/server_test.go app/http/routes/routes.go
git commit -m "$(cat <<'EOF'
新增:agent-as-MCP 三端点——runtime 真身+core proxy 透传,common 信封+64KB body 闸

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: cmd/mcp.go 第 6 工具组 + 门控

**Files:**
- Modify: `cmd/mcp.go`(三工具描述/入参 schema/buildReq/门控)
- Test: `cmd/mcp_test.go` 增测

- [ ] **Step 1: 工具表**(沿 peerMCPTools 结构加 aiMCPTools 或并入同表——**并入同表更简**:invokeTool/ tools/list 已按表驱动;门控过滤在 list/call 两处做)

三工具(name/description/入参 schema/buildReq→POST 打 base 对应端点):

```go
// agent-as-MCP 工具组(spec §4):描述统一追加 session 契约句——外部 AI 只能
// 驱动本工具面创建的会话,手机/桌面会话不可见。
{name: "ai_run_start",
 description: "Start an agentcore run on this device (async: returns session_id immediately; poll ai_run_status). Sessions created here are tool-face-private: phone/desktop sessions are not accessible.",
 // 入参:content(必填 string)/project_path(必填 string)/session_id(可选)/provider(可选)
 buildReq: → POST /api/agent/ai/mcp/start, body {project_path, content, provider?, session_id?}}
 // 描述追加(spec §5):sessions may be evicted under memory pressure or after
 // agent restart — start a fresh session if status reports it missing.
{name: "ai_run_status",
 description: "Read agentcore session state (running/idle/awaiting_approval), pending approvals and last assistant summary. Tool-face sessions only.",
 // 入参:session_id(必填)
 buildReq: → POST /api/agent/ai/mcp/status, body {session_id}}
{name: "ai_run_approve",
 description: "Submit an approval decision (accept/deny aliases per server) for a pending approval in a tool-face session. applied=false means the decision did not land (no_match/duplicate).",
 // 入参:session_id/approval_id/decision 三必填
 buildReq: → POST /api/agent/ai/mcp/approve, body {session_id, approval_id, decision}}
```

schema 形态与 peer 工具的 inputSchema 逐字对齐(cmd/mcp.go 既有 peerMCPTools 的 schema 写法)。

- [ ] **Step 2: 门控**(冷子进程 `config.GetGlobalConfig()` 为 nil——**直读配置文件**):

```go
// agentMCPGateOn 读 ~/.aliang/config.json 的 customer.agentcore.ai_mcp 三态
// (nil/缺失/文件不存在=默认开)。冷子进程不加载 customer config(全局配置
// 仅 agent 主进程 SetGlobalConfig),工具面门控直读文件;读取失败=默认开
// (fail-open 沿家族加法能力先例,读错 Warn)。
func agentMCPGateOn() bool { /* os.ReadFile(agentMCPGateConfigPath()) + 展开 + json.Unmarshal
	到局部 struct{Customer struct{AgentCore struct{AIMCP *bool}}};AIMCP==nil→true;否则 *AIMCP */ }

// agentMCPGateConfigPath 包级 seam(默认 runtimepath.UserConfigPath 先例形态
// 指向 ~/.aliang/config.json;测试可换临时 fixture——UserStateDir 无 env 覆盖,
// 固定路径会让测试写真家目录)。
var agentMCPGateConfigPath = func() string { /* runtimepath 用户配置路径 */ }
```

- 门控点两处:`tools/list` 组装时 gate off 则不含三工具;`invokeTool` 命中三工具名且 gate off → `unknown tool: <name>`(peer_mcp 关闸同款文案形态)。
- serverInfo.instructions 追加一句(沿 :193-203 先例):工具组语义与「AI 驱动需设备 AI control 开启」提示。

- [ ] **Step 3: 工具面测试**(沿 mcp_test.go 既有 stdio 驱动形态):
  - tools/list 含三工具且描述含契约句关键词;
  - 门控 off(临时配置文件 fixture)→ tools/list 无 + call 返回 unknown tool;
  - 三工具 buildReq 组装(端点路径/方法/body 字段);
  - 后端非 2xx/code!=0 → isError 文本透传;
  - `data.result` 解包:端点回 `{"code":0,"data":{"result":"<json 字符串>"}}` → 工具文本为该字符串。
- [ ] **Step 4: 验证 + Commit**

```bash
go build ./... && go vet ./cmd/ && go test ./cmd -count=1
git add cmd/mcp.go cmd/mcp_test.go
git commit -m "$(cat <<'EOF'
新增:aliang mcp 第6工具组 ai_run_start/status/approve——session 契约描述+ai_mcp 门控(配置直读)

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: 全量验证

**Files:** 无新改动。

- [ ] **Step 1: 后端全量**

```bash
go build ./... && go vet ./app/... ./cmd/... && go test ./app/http/services ./app/http/handlers ./app/agentruntime ./app/agentcore ./processor/config ./cmd -count=1
```
Expected: 全 ok。预存失败(auth/agent_ai 子进程超时族)单包复跑确认非回归后记录。

- [ ] **Step 2: 关键回归**

```bash
go test ./app/http/services -run '^TestAgentMCP|^TestAgentCoreMemory|^TestAgentCoreMCP' -count=1 -v
```

- [ ] **Step 3: 前端**

```bash
cd app/website && npx vitest run src/utils/agentCoreSettings.test.js && npm run build
```

- [ ] **Step 4: 冒烟口径(部署前)**

`aliang mcp` 挂进任一 MCP 客户端(如 Claude Code mcp 配置)→ tools/list 见三工具 → ai_run_start 发一条 → ai_run_status 轮询到完成摘要 → 构造审批卡 → ai_run_approve accept → run 续行;手机同项目会话的审批卡对工具不可见(契约)。

---

## 计划外备忘

- `agentTerminalWriter`/`agentAIErrorPayload`/`AgentVibeSession` 字段名以仓内实际为准(计划已注对齐点)。
- ai_run_start 不带 goal_context/resume 族键——spec 非目标,勿加。
- `ai_mcp` UI 卡片 v1 不做:记账 TODOS(默认开即用,手改文件/API 可关)。
- uuid 生成:仓内有现成 helper 就复用,无则 crypto/rand。
