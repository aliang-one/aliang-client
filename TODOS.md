# TODOS

## Security

### Extend request-bound management authentication to all sensitive APIs

**What:** Apply the local management-session boundary introduced for auth snapshots and session events to every sensitive management endpoint.

**Why:** Process-global login state does not establish the identity of an individual HTTP caller, so unrelated local processes or unintended network clients may be able to act with the logged-in user's authority.

**Context:** The auth/proxy consistency work should first make session restore internal to the session-owner process and protect `/api/auth/session` plus session SSE with a request-bound local management session. Follow up by inventorying all sensitive routes, centralizing enforcement in middleware, defining loopback and LAN access policy, validating Origin/CSRF behavior, and adding unauthorized-caller integration tests. Start in `app/http/middleware/`, `app/http/routes/`, and `app/http/server.go`; reuse the session mechanism rather than creating endpoint-specific checks.

**Effort:** L
**Priority:** P2
**Depends on:** Local management-session mechanism from the auth/proxy consistency fix

## Architecture

### Multi-agent orchestration engine (missing capability)

**What:** Build a host-side orchestration layer on top of the existing per-session sub-agent support: planned multi-agent execution for goals (fan-out/fan-in), declarative team/pipeline profiles, controlled depth=2 nesting, and cross-device task scheduling over the peer MCP primitives.

**Why:** As of 2026-11-05 the product's "multi-agent" is model-improvised, single-turn, and stateless — each kernel decides on its own which sub-agents to spawn, and they vanish when the turn ends. There is no host-side orchestration authority (repo-wide keyword audit: crew/swarm/multi-agent/graph all zero hits; the only planner is goal's single-agent bounded loop). The user has decided this is a real product gap, not a nice-to-have.

**Context:** Full gap analysis, constraints (approval threading, depth=1 structural guarantee, journal attribution, timeout budget, codex observability hole), and a four-step candidate roadmap (R1 goal multi-executor → R2 orchestration profiles → R3 controlled depth=2 → R4 cross-device scheduling) live in `docs/superpowers/specs/2026-11-06-multi-agent-orchestration-gap.md`. The same audit's five per-kernel sub-agent defects (sub-run timeout ceiling, parallel sub-agents, skills-in-subrun, peer tool cancellation, task length cap) are already fixed on `fix/agentcore-subagent-blindspots`; the orchestration engine builds on those primitives.

**Effort:** XL
**Priority:** P1
**Depends on:** Sub-agent primitives on fix/agentcore-subagent-blindspots; R1 additionally needs PhoneServer goal-budget coordination

## Architecture

### agentcore MCP 健壮性包最终评审 5 条 NIT(记账不修,合并 commit 1f931f7)

**What:** 打磨 agentcore MCP 检活重连/过滤实现的 5 条非阻塞观察:①过滤汇总日志仅在「过滤确实剔了工具」时打(include 恰等全集时静默),且 `%d` 记归一前原名单长度(`agent_ai_agentcore_mcp.go` 装配循环);②exclude 未知名 Warn 逐名打 `%q` 而非聚合列表(`filterAgentCoreMCPTools`);③重拨失败时模型可见文本取**原** CallTool 错误,重拨失败原因 derr 只进日志(`holder.call` derr 分支);④`Close()` 后调用返回「连续 3 次重连失败」判死文案,与真实原因(holder 已关闭)不符——防御方向正确;⑤spec 测试计划条目 1 的「consecFail 清零」无直接断言(判死计数测试间接覆盖)。

**Why:** 最终评审 READY(0 BLOCKER/0 SHOULD)后余留的观察项,均不影响行为正确性与安全红线;其中④在 run 收尾后的病理性调用才可观测。

**Context:** spec=`docs/superpowers/specs/2026-11-09-agentcore-mcp-resilience-design.md`(§7 测试计划对照第 5 条)。若后续做 MCP 仪表盘 UI 批次或重连策略调优,顺手收敛。

**Effort:** S
**Priority:** P3
**Depends on:** 无

### agentcore 记忆层最终评审遗留账(合并 b75d3cc,均不阻塞)

**What:** 记忆层终审(READY)遗留 6 条:①记忆双注册=双写者,core 手动编辑与 runtime 异步提取/touch 并发直写同一 JSON、store 无跨进程锁——丢更新窗口按 LWW 有意取舍,收敛路径=core 侧补 proxy 面(沿 registry proxyIfNeeded 先例,`agent_memory_handler.go` 头注释已记);②registry 卡 `AgentMCPRegistrySettings.vue:468-470` 的 `isMissingError` 与记忆卡修复前同款「死且反向」掩蔽模式(正则分支吞掉双注册漏注册 404 事故),建议按记忆卡同款「挂 status+收窄」处置;③「跟随默认」无法清回已落盘的显式布尔(deepMergeJSONObjects 跳过 null 且 Merge 只增不减)——卡片 hint 已如实声明,产品上若要支持清除需后端补显式 unset 语义;④slug 大小写敏感,大小写不敏感盘上同项目可能共文件(沿 journal 同族先例);⑤effort 锁存不随 customer 配置切换重探,重启复位(代码注释已记);⑥Task 5 注入预算不计块头尾开销约 65 字符(理论可达 6065,实际不可达)。

**Why:** 终审 READY 后余留,均不影响正确性/安全红线/主流程;①②与多进程写和既有卡片相关,值得后续批次收敛。

**Context:** spec=`docs/superpowers/specs/2026-11-09-agentcore-memory-design.md`;记忆卡修复先例=`AgentMemorySettings.vue` 的 memoryApiRequest 挂 `e.status`+`isMissingError` 收窄形态。

**Effort:** S
**Priority:** P3
**Depends on:** 无

### agent-as-MCP 导出面评审遗留账(ai_run_* 三工具,均不阻塞)

**What:** 五个任务两关评审(规格+质量)余留的观察,按归属分组:

*端点后端(Task 2,`app/http/services/agent_ai_ai_mcp.go`)*
1. **条件键形状无自动化护栏**:status 的 pending_approvals 条目里 `command`/`tool_name`/`expires_at` 是「非空才带」的条件键——变异测试 F(gate 键恒带空串)整套仍绿,说明缺一条「空值不出键」的断言。补法:构造空 Command 的 waiter,断言序列化后 JSON 里没有该键。
2. `agentAIMCPStatusForError` 的第二个 case 与 default 分支等价,可删。
3. history 尾部遍历在 `m.mu` 临界区内(存量同款形态,不是本批引入);history 长时会拉长锁持有时间。
4. `snapshot()` 是浅拷贝,安全性依赖 `fanOutWrite` 的「帧写后不再改」冻结契约;若将来有帧被原地改写,这里会静默串数据。

*端点层(Task 3)*
5. **`HandleAIApprovalHook` 存量 bug**(`app/http/handlers/agent_handler.go:~304`):用 `common.Error(w, http.StatusMethodNotAllowed, ...)`,而 8 行之上的注释明确写着「直传 405 会被映射成 500」——实测确实返回 500。本批新增的 `handleAIMCPOp` 已按正确写法(`http.Error`)实现,老函数未动(不在本批范围)。修法:改成 `http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)`。
6. `cleanExistingAgentDirectory` 的 stat 错误会把**绝对路径**回吐给工具调用方(手机/桌面同款行为,存量)。是否脱敏需产品口径。
7. 已被逐出的工具面会话调 status 返回 `200 {state:"idle"}` 而非 400——「会话不存在」的语义在逐出场景下要不要区分,待定。

*工具面(Task 4)*
8. 工具面会话可能在内存压力下被逐出(ResidentCap=8)或随 agent 重启消失,之后 status/approve 会 403/400。工具描述里已有「会话可能被逐出,重新 start」的提示,但**没有**把「旧 session_id 会永久失效」讲透。
9. `cmd/mcp.go` 的 `invokeTool` 注释写「服务端已脱敏」(`:~359`),但 ai_run_* 三端点回的是本机会话状态摘要,脱敏与否取决于上游 handler,措辞偏乐观。

*其它*
10. `ai_mcp` 的仪表盘 UI 卡片(spec §6 明确 v1 不做):目前只能改文件/API 关闭,产品上缺可见开关。
11. ~~`ai_mcp` 线格式键缺 JSON round-trip 测试~~ **已补**:`TestAgentCoreAIMCPJSONRoundTrip`(键改名变异实测变红)。
12. SettingsPage 层仍缺「ai_mcp 三态透传」的**页面级**断言——`agentCoreSettings.test.js` 已覆盖 `normalizeAgentCoreTuning` 本身的三态(`ai_mcp: false/true/非法类型/缺键`),但 SettingsPage 的 `mapCustomerConfigFromApi` 出口没有专门用例;该页共用同一出口,回归风险低。
13. spec 行文里「第 6 工具组」的序数在群组增删后需要复核(纯文档一致性)。**当前成立**:peer 五工具 + ai_run_* 三工具 = 六组。
14. 记忆批沿用下来的 NIT 里与 `agentAIMCPPreflight` 同型的一条:每次调用都 `config.GetGlobalConfig()` 取指针后读字段,指针在 `SetGlobalConfig` 下会被整体替换(读取方要么拿旧要么拿新、各自视为不可变),因此**当前无竞态**;但若将来有人改成「锁外原地改字段」,这里会静默读到半更新的配置。**注**:`cmd/mcp.go` 的门控不走这条路(冷子进程直读文件,见 `agentMCPGateOn` 注释)。

*终审(全分支)三条已闭环*
15. ~~status/approve 的 `session_id 必填` 早返分支无测试兜底(删掉整套仍绿)~~ **已补**:`TestAgentMCPStatusRejectsEmptySessionID` / `TestAgentMCPApproveRejectsEmptySessionID`(两向变异实测变红)。
16. ~~core 侧三条路由注册存在性无测试(删除注册 / 去掉 `POST ` 前缀两向变异均绿)~~ **已补**:`app/http/routes/ai_mcp_route_test.go` 同时断言注册存在、`mux.Handler` 返回的 pattern 带 `POST ` 前缀、GET 被 ServeMux 兜底成 405 且 `Allow: POST`(两向变异实测变红)。
17. ~~`handleAIMCPOp` 方法闸注释写错折算目标(实为 500 而非 400)~~ **已改**:注释改为「过 ErrorCodeToHTTPStatus 落 default → 500」,结论(必须 `http.Error`)不变。

**Why:** 五任务全部两关评审通过(0 BLOCKER/0 SHOULD 未决),以上均为「不影响正确性、安全红线、主流程」的观察项;其中 5 是存量真 bug(HTTP 500 而非 405),6/7 是产品口径问题,1/11 是可测性缺口。

**Context:** spec=`docs/superpowers/specs/2026-11-10-agent-as-mcp-design.md`;plan=`docs/superpowers/plans/2026-11-10-agent-as-mcp.md`;记忆批同款卡片修复先例=`AgentMemorySettings.vue` 的 `memoryApiRequest` 挂 `e.status`+`isMissingError` 收窄形态。

**Effort:** S
**Priority:** P3
**Depends on:** 无

## Networking

### mesh UDP 打洞载体遗留账(批次 2026-11-12,分支 feat/mesh-udp-punch)

**What:** 打洞一期落地后的七条非阻塞遗留:①端口预测/生日攻击与 coturn 自托管——二期项,闸门=两类真实网络(家宽非对称/蜂窝 CGNAT)真机打洞率数据;②IPv6 打洞(v1 udp4,候选解析 To4 断言对齐 agent_peer_lan);③`advertise_ip` 收下不消费(LAN 提示作候选源,beacon 候选注入同族);④诊断面板 UDP 可用性/载体统计 UI(本期载体可见性已落 [MESH-OUT] 日志行);⑤MeshSettings.vue:521 unconfigured 启发式未考虑 punch_enable(现不可达:punch 无 UI 开关);⑥PhoneServer punch.ts 两条 NIT(result 查表无 TTL 过期检查——A 端 pending 10s≪TTL 120s 无害;audit 元数据缺 from/to 对端 ID,事后关联需 session_id join);⑦v1 无 close 帧(同 session_id 重发=顶替收摊;过期 result 仍转发给 A 的窗口由 A 端 pending miss 兜底)。

**Why:** 均为「不影响正确性、安全红线、主流程」的记账项;验收口径=打洞率锚 70%±7.1% 学术基线(ACM IMC 2025)按网络族分桶,二期投入看一期真机数据。

**Context:** 决策记录=`docs/superpowers/specs/2026-11-12-mesh-udp-punch-design.md`;计划=`docs/superpowers/plans/2026-11-12-mesh-udp-punch.md`;B 侧 mTLS v1 降级(token 门禁+A 钉 B fail-closed,严格强于 peer.lan 纯 token 模型)与 target_device_id 路由信封(转发前剥除)已入 spec §2。

**Effort:** S(逐条独立)
**Priority:** P3
**Depends on:** 真机两类网络实测数据(①②);其余无

## Completed
