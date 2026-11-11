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

## Completed
