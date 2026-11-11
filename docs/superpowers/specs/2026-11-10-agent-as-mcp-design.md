# agent-as-MCP 导出面设计:ai_run_* 三工具(stdio 第 6 工具组)

- 日期:2026-11-10
- 状态:设计对话定稿(四决策已拍板),待评审
- 上游出处:agno 设计目录 Top3 最后一项([[agno-design-catalog-merged]]「agent-as-MCP 导出面(基建已备纯增量)」);同族先例=aliang mcp peer 工具面与 agentcore 记忆层(b75d3cc)
- 借鉴对照:agno agent-as-MCP export(RunRequirement/HITL 经 MCP 暴露)→ 本仓 ai_run_start/status/approve 三件套

## 0. 背景与动机

agentcore(与 claude/codex 两个 CLI 内核)目前只能被手机/仪表盘驱动;外部 MCP 客户端(任意 AI 会话——包括其它 Claude Code 会话、支持 MCP 的 IDE)无法驱动本机 agent。`aliang mcp` stdio 服务端已存在(peer 五工具,同用户 CLI 子进程信任模型),只差把「发 run/看状态/批审批」暴露成工具。这是 P1 多智能体编排的前置件(agent 可寻址化),v1 限 stdio 同用户信任模型,无新鉴权面。

## 1. 目标与非目标

**目标**
1. `aliang mcp` stdio 服务端新增第 6 工具组:`ai_run_start` / `ai_run_status` / `ai_run_approve`,外部 MCP 客户端可完整驱动一次带审批的 run;
2. 异步驱动语义:start 发起即返回(session_id+受理确认),run 后台续跑(「关窗续跑」既有架构),status 轮询;
3. session 契约:工具面只能驱动/批准**本面创建的会话**(CreatedBy=ai_mcp 内存标记,三处校验)——外部 AI 碰不到手机/桌面会话的审批卡;
4. 决议不绕审批门:approve 走 `m.approval`(`ai.approval.response`)既有受理链,全部既有校验原样生效;
5. 门控:`customer.ai_mcp` 三态指针(nil=默认开),关=tools/list 无此组(peer_mcp 关闸同款行为)。

**非目标(v1)**
- 56431 Streamable HTTP MCP 端点(P2 鉴权闭环后另批);
- journal 读面/重放工具(读面大/敏感面多,留后续批次);
- `ai_mcp` 的仪表盘 UI 卡片(默认开即用,手改文件/API 可关,记账后续批次);
- 跨设备导出(本机 stdio 面,不经 peer 中继);
- goal 型 run(goal 请继续用既有 CLI 通道,与 agentCoreSystemPrompt 同款边界)。

## 2. 现状关键事实(实施前提,均已核实)

| 事实 | 位置 |
|---|---|
| `aliang mcp` stdio 服务端:JSON-RPC 2.0(MCP 2024-11-05),initialize/tools/list/tools/call/ping 四方法,现挂 peer 五工具,base=`DefaultUserAgentAddr`(56433) | cmd/mcp.go:3-53,149-165 |
| 工具派发:名字查 `peerMCPTools` 表 → `buildReq(args)` → HTTP 打 base(:360-385);非 2xx/code!=0 → isError 文本;解包只认 `{code,msg,data:{result:string}}` 的 data.result 字符串 | cmd/mcp.go:360-385,424-436 |
| 服务端级说明字段(serverInfo.instructions)已有「设备舰队全局图景」先例 | cmd/mcp.go:193-203 |
| peer_mcp 三态门控先例(`EffectivePeerMCPEnabled`,nil=默认开) | processor/config/types.go:765-777 |
| AI 驱动面=WS `/api/agent/ai/stream`,帧受理分发:`m.message`(ai.message)/`m.runStart`(ai.run.start)/`m.approval`(ai.approval.response);分发器对 ai.* 帧统一有 `s.aiControlEnabled()` 设备门(agent_remote_ws.go:957-984)——门在分发器不在受理函数内 | app/http/services/agent_ai.go:2169,2544,4003;agent_remote_ws.go:957-984 |
| `message()` 帧键:session_id/message_id/content/project_path/provider(+goal_context);懒建分支存在(无会话时按 project_path 建) | agent_ai.go:2172-2276(:2276 `resolveAgentAICWD(remoteString(msg, "project_path"))`) |
| 审批决议受理:`m.approval` 收 `ai.approval.response`(session_id+approval_id+decision+delivery_id),`normalizeAgentAIApprovalDecision` 解析多别名,ack 双写防重投;ack 帧 `{type:ai.approval.ack, result:"applied"|"no_match"|"duplicate"}` 经 writeJSON 写出 | agent_ai.go:4003-4031,10337 |
| 待审批清单读面原语:`m.approvals` waiter map + 现成 `pendingApprovalsSnapshot()`(返回 approval_id/kind/tool_name/command/expires_at 恰好所需形状);`pendingApprovalRequests`(:48-52)是重投 journal 非读面 | agent_ai.go:4107,48-52 |
| AI manager 住 runtime 进程;core 侧 `/api/agent/ai/*` 为 proxy 透传(`proxyIfNeeded` 先例) | routes.go:298-299;agentruntime/server.go:154-158 |
| run 不随断连中断(关窗续跑,后台完成) | 既有架构(聊天页三缺陷修复,27ce585) |
| peer 工具超时:每调用 `timeoutMs` ctx 期限(与 LLM 工具层同款分层) | cmd/mcp.go:96,180 |
| dashboard normalize 两挂键先例(memory_enabled) | agentCoreSettings.js / SettingsPage.vue(记忆批) |

## 3. 设计总览(方案 A:薄 HTTP 端点组)

```
cmd/mcp.go                     # 第 6 工具组三工具 + ai_mcp 门控 + 描述契约句
app/agentruntime/server.go     # 真身端点 /api/agent/ai/mcp/{start,status,approve}(runtime 56433)
app/http/routes/routes.go      # core 侧同路径 proxy 透传(HandleStatus/proxyIfNeeded 先例)
app/http/handlers/agent_handler.go 或新 handler 文件  # 端点 handler(转调 services)
app/http/services/agent_ai.go(或新文件)              # 端点后端:组装帧调既有受理链 + CreatedBy 校验
app/http/services/agent_ai.go  # agentAISession 加 createdBy 字段(内存态)
processor/config/types.go      # AiMCP 三态开关 + EffectiveAgentCoreAIMCPEnabled()
app/website normalize 两挂键   # memory_enabled 同款透传(无 UI)
```

内核零改动。四决策拍板:①形态=stdio 加工具组;②工具=写面三件套;③门控=独立开关默认开;④审批边界=session 契约绑定。

## 4. 三工具规格

### 4.1 ai_run_start

- 入参:`{content(必填), project_path(必填), session_id(可选), provider(可选,缺省 auto)}`;
- 无 session_id → **新建工具面专属会话**:端点后端生成 session_id(UUID)+message_id(UUID),组装 ai.message 帧(content/project_path/provider)调 `m.message` 同链受理(懒建分支),会话打 `createdBy=ai_mcp`;
- 带 session_id → 续聊:校验会话存在 **且** `createdBy=ai_mcp`(否则拒绝,文案见 §6),组装 ai.message 帧追加;
- 返回:`{session_id, accepted:true}`;run 后台续跑(关窗续跑架构);
- 不支持:goal_context(工具面不带,goal 非目标)、resume/绑定族键(外部 AI 无内核会话历史,懒建即全新)。

### 4.2 ai_run_status

- 入参:`{session_id(必填)}`;校验存在 + `createdBy=ai_mcp`;
- 返回:`{state, pending_approvals:[{approval_id, tool, summary}], last_assistant(尾部摘要,截断)}`;
- 读面:manager 内存态(运行态/**pendingApprovalsSnapshot()——读 m.approvals waiter map**/history 尾部)——**不经 journal 磁盘读**(内存态即真相;journal 读面是非目标;pendingApprovalRequests 是重投 journal 非读面)。

### 4.3 ai_run_approve

- 入参:`{session_id(必填), approval_id(必填), decision(必填,accept/deny 等多别名沿解析链)}`;
- 校验会话存在 + `createdBy=ai_mcp` → 组装 `ai.approval.response` 帧(session_id+approval_id+decision+delivery_id=新生成 UUID)调 `m.approval` 同链受理——**决议不绕审批门**:挡位/allowed_roots/重复决议幂等 ack 等全部既有校验原样生效;
- 返回:`{ok:true, applied:true/false}`(applied 语义沿 m.approval 的 ack 结果:决议是否命中在等决议的活 waiter;no_match/重复亦 ack 成功但 applied=false,外部 AI 据此知决议没生效)。

### 4.4 生命周期语义(异步驱动)

三工具均为短命调用:start 受理即返回(不等 run 结束);status 纯读即返;approve 受理即返。长 run 的「等结果」由驱动方 LLM 轮询 status 实现——与 MCP 工具墙钟语义天然匹配,超时由 `timeoutMs` 既有分层管。

## 5. session 契约(安全核心)

- `agentAISession` 加内存字段 `createdBy string`(空=既有来源,零影响);工具面会话打 `ai_mcp` 标记;
- **打标时序**(不持 m.mu 跨 message()):start 先 m.mu 预检(续聊:createdBy==ai_mcp 才放行)→ 调 message() → 再 m.mu 回取 `m.sessions[sessionID]` 补打标;message() 返回后**复检** createdBy==ai_mcp(预检通过后若发生逐出重建——逐出只护 running 会话——重建会话 createdBy 为空,此时 run 照跑但本面后续 status/approve 会拒,属契约降级,文案同「会话不属于 AI 工具面」且 Warn 留痕);
- **ResidentCap=8 降级声明**(agent_execution_guard.go:49):工具面会话被逐出后续聊将「会话不存在」——工具契约在逐出场景下退化为重新 start 新会话,描述契约句注明;
- 校验点三处:续聊 start / status / approve——会话不存在→`会话不存在: <id>`;存在但非本面→`会话不属于 AI 工具面`;
- **runtime(agent)重启**后标记随会话内存态消失(run 本就中断),一致性自然成立;stdio 客户端进程重启不触及 runtime 态——run 与标记均存续,外部 AI 断线重连后 status/approve 仍可用(正向属性);标记不落盘不入 journal(纯内存安全属性,零序列化面);
- 三工具描述统一追加契约句(catalog 既定):「These tools only operate on sessions created by this tool face; sessions from phone/desktop are not accessible.」

## 6. 门控与开关

- `customer.ai_mcp` 三态指针(沿 MCPEnabled/SkillsEnabled/MemoryEnabled 同款):

```go
// AIMCP 是 agent-as-MCP 工具面的总开关(customer.ai_mcp)。三态指针
// (nil=未配置默认开启)、true/false 显式。只门控 ai_run_* 三工具,不碰
// peer 工具面。stdio 同用户信任模型下不引入新权限(同用户本可直打
// 管理口),独立键只为可关。
AIMCP *bool `json:"ai_mcp,omitempty"`
```

- `EffectiveAgentCoreAIMCPEnabled()`(nil=true,沿同款);
- 门控关:`tools/list` 无此组 + `tools/call` 返回 unknown tool(peer_mcp 关闸同款)+ **三端点同拒**(固定文案「agent-as-MCP 工具面已关闭」503)——off=真关,与「独立键只为可关」动机闭环;
- 前端 normalize 两挂键透传(`agentCoreSettings.js` normalizeAgentCoreTuning 第三布尔键同款 + SettingsPage 共享出口,记忆批先例);**UI 卡片 v1 不做**(记账后续批次);
- ⚠️ 实施警告:customer 配置加字段 grep `memory_enabled` 定位副本逐一同步;端点记得双注册+proxy 透传(core 侧沿 `HandleStatus` 的 `proxyIfNeeded` 先例,不是静态文件双注册)。

## 7. 端点组规格

真身落 runtime(`app/agentruntime/server.go`),core 侧 proxy 透传(`routes.go`+handler `proxyIfNeeded`):

| 方法 | 路径 | 后端 |
|---|---|---|
| POST | `/api/agent/ai/mcp/start` | 组帧调 `m.message`(懒建/续聊)+置 createdBy |
| POST | `/api/agent/ai/mcp/status` | 读 manager 内存态 |
| POST | `/api/agent/ai/mcp/approve` | 组帧调 `m.approval` |

- 三端点全 POST+JSON body(工具入参直传);响应载荷 JSON 序列化塞进 `data.result` **字符串字段**(invokeTool 解包契约 :424-436 只认 data.result,peer handler 回 `{"result": text}` 同款)——违反即整次工具调用报废;
- body 上限 `http.MaxBytesReader` 64KB(同族先例);
- **受理结果获取=捕获型 writer(核心机制)**:`m.message`/`m.approval` 无返回值,错误与 ack 全经 writeJSON 帧写出;端点后端传入捕获型 writer(闭包 append 帧到 buffer),受理函数返回后扫帧:message 的 error payload → 取文案回 4xx;无 → accepted;approve 解析 ack 帧 `result` 字段 → `applied=(result=="applied")`(no_match/duplicate 亦 ack 成功但 applied=false)。**writer 生命周期**:HTTP 响应写完后 writer 置惰(后续帧丢弃)——run 的 emitter 持有该 writer 直到 run 终结,不置惰则 delta/terminal 帧持续写进缓冲无限增长;
- **aiControl 设备门 parity**:WS 分发器对 ai.* 帧统一有 `aiControlEnabled()` 门;端点直调绕过分发器=绕过设备门——三端点同款前置校验(关→与手机面同文案拒绝),驱动面行为与手机/桌面完全 parity;
- **busy 守卫**:续聊撞运行中会话 → message() 内 busy 守卫发 `ai session is already running: <id>` 错误帧 → 捕获 writer 取回回 409;
- **并发纪律**:不持 m.mu 跨受理函数(其内部多次取锁,持锁调内加锁=本仓已录事故类);service nil/未就绪防护沿 `HandleAIApprovalHook` 先例。

## 8. 错误与安全

| 场景 | 行为 |
|---|---|
| 会话不存在 | `会话不存在: <id>`(400) |
| 非本面会话 | `会话不属于 AI 工具面`(403) |
| content/decision/approval_id/project_path 缺失 | 400 原文案(start 的 project_path 端点层 400 空值;解析语义与手机面同款:resolveAgentAICWD 空回落 home、无授权清单 ACL——不新增校验,如实声明) |
| AI service 未就绪 | 503 沿先例文案 |
| 决议 no_match/重复 | 200 + `applied:false`(受理链 ack 语义,不报错) |
| 会话运行中续聊 | 409 `ai session is already running: <id>`(message() busy 守卫,捕获 writer 取回) |
| aiControl 设备门关 | 与手机面同文案拒绝(parity) |
| 门控关 | 工具面 tools/list 无此组 + call 返回 unknown + **三端点同拒 503**(off=真关) |

安全终审口径:stdio 同用户信任(无凭据);不新增数据外流面(返回内容=本机会话状态摘要);敏感值禁入工具描述;session 契约防跨会话审批;决议链零绕行。

## 9. 测试计划(要点)

端点层 httptest(runtime 侧):start 新建(返回 session_id+createdBy 标记实据)/续聊/续聊非本面拒绝/**busy 会话 409 文案透传**/status 各态(running/awaiting_approval+清单经 pendingApprovalsSnapshot/last_assistant)/approve 决议受理(经 m.approval 同链)+no_match applied:false/**ack 双写仍落 run socket(跨端同态回归)**/三契约拒绝/**aiControl 门关拒绝**/**门控关三端点 503**/**writer 生命周期(run 帧不滞留缓冲)**/**逐出重建降级(构造 evict 后 start:复检 createdBy 空→后续 status 403+Warn 留痕)**;core 进程侧 **proxy 透传双注册验证**(双注册 404 是实录事故,只测 runtime 不够);工具面层(cmd/mcp.go,沿 mcp_test.go 形态):tools/list 含三工具+描述含契约句/ai_mcp 关=tools/list 无+call 返回 unknown/buildReq 参数组装/后端非 2xx 透传 isError/`data.result` 解包;normalize 透传(agentCoreSettings.test.js 加 ai_mcp 键三态);AI service 未就绪防护。

## 10. 实施影响面

| 文件 | 改动 |
|---|---|
| processor/config/types.go | AiMCP 字段 + Effective 方法 + 副本同步 |
| app/agentruntime/server.go | 真身三端点注册 |
| app/http/routes/routes.go | core 侧 proxy 透传注册 |
| app/http/handlers/(新 handler 文件) | 端点 handler(proxyIfNeeded+转调 services) |
| app/http/services/(新文件或 agent_ai.go 内) | 端点后端:组帧/createdBy 校验/状态读面 |
| app/http/services/agent_ai.go | agentAISession 加 createdBy 字段 |
| cmd/mcp.go | 第 6 工具组 + 门控 + 描述契约句 |
| app/website normalize 两挂键 | ai_mcp 透传 |
| 测试 | 端点层/工具面层/normalize |

## 11. 借鉴对照(agno → 本仓)

| agno 机制 | 本仓落地 |
|---|---|
| agent-as-MCP export(AgentOS 把 agent 能力暴露为 MCP 工具) | ai_run_* 三件套(stdio 第 6 工具组,基建已备纯增量) |
| RunRequirement/HITL 经 MCP 往返 | approve 走 m.approval 同链,审批门零绕行 |
| agent 会话隔离 | session 契约(CreatedBy=ai_mcp 三处校验) |
| 外部编排驱动 agent(P1 编排前置) | 异步驱动语义(start 即返+status 轮询) |
