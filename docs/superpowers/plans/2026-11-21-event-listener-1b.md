# 事件监听器 1b 实施计划(process_exit + http_poll + webhook)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox syntax.

**Goal:** 落地 spec §3 的三个剩余源——process_exit(agent 侧命令执行/pid 监控)、http_poll(server 侧定时拉取+指纹比对)、webhook(server 侧回调端点+token 鉴权);同时放开 REST/工具面的 file_tail-only 400 闸。

**Architecture:** 全部复用 1a 基础设施(判定链/触发引擎/通知/DB/REST 代理/工具面),三个源仅新增「事件产生」的实现面。server 侧 http_poll 定时器+webhook 端点,sweepers.ts 1h 间隔扫溢出/过期(1a 已有);agent 侧 process_exit 复用 file_tail 的 watcher 管理器+safeGo+outbox。

**Tech Stack:** 同 1a。

**Base:** `worktree-event-listener-1b` 分支 @ `ab34bbc2`(含 1a 全量)。

---

### Task S1: Server — REST 闸放开 + webhook 端点 + http_poll 定时器

**Files:**
- Modify: `server/src/modules/routes/listeners.ts`(400 `listener_source_not_in_1a` → 放行;webhook POST 生成 token+URL 存入 `webhook_token_hash`;webhook 回调端点 `POST /api/listener/webhook/{id}/{token}`)
- Modify: `server/src/modules/listeners/sync.ts`(webhook 触发入口)
- Create: `server/src/modules/listeners/pollers.ts`(http_poll 定时器管理:DB 驱动 interval,boot 重建)
- Modify: `server/src/modules/listeners/trigger.ts`(触发来源放宽:直接调用的 process_exit/webhook/http_poll 不经 claim-CAS 的 file_tail 路径——实际上 claim-CAS 对所有源通用,只需调用)
- Test: `server/test/listeners/sources.test.ts`(新建,覆盖三源最小链路)

### Task S2: Server — webhook 端点

**Files:**
- Modify: `server/src/modules/routes/listeners.ts`(POST /api/listener/webhook/{id}/{token} 端点,body ≤64KB 超限 413,token 错 401,once 触发后 410 Gone)
- Test: 覆盖 201/401/410/413 分支

### Task A1: Agent — process_exit watcher

**Files:**
- Create: `app/http/services/agent_listener_processexit.go`(代启命令 or 挂 pid,等退出,比对 expect_code,命中→上行)
- Test: `app/http/services/agent_listener_processexit_test.go`

### Task A2: Agent — 工具面 source 枚举放开

**Files:**
- Modify: `app/http/services/agent_listener_tools.go`(listenerToolCreate 放开三源注册,webhook/http_poll params 校验)
- Modify: `app/http/routes/listeners.go` 或 REST 层同步放开(400→201)

### Task V1: 全量验证
- Go build/test 全绿
- Server test 全绿(2 预存)
- Web 全绿

---

**已知约束:**
- http_poll 的 JSON 字段条件语法在 1b 仅做指纹变化(spec §3 权威)
- webhook 仅长效 token(1b 不做一次性 token 双轨)
- process_exit 的 `pid` 挂接仅限 1b 代启模式;agent 重启后 process 源不重建置 error(照 1a 设计)
- REST 的 `source.device_id` 覆写逻辑仅对 file_tail/process_exit;http_poll/webhook 固定 "server"
