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

## Completed
