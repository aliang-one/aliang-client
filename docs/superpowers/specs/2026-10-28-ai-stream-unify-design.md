# AliangCode 聊天链路统一到 agent（WS 透传 + 错误必达 + 自愈可见）— 设计文档

日期：2026-10-28
状态：设计稿（待评审）
范围：Go 后端（core 侧 WS 透传 + 自愈可观察性 + agent 侧一行路由注册）+ 前端（错误契约/看门狗）；手机端零改动，agent 的 AI 管理器/journal 零改动
前置事实（2026-10-27 liang-dev 实测复盘）：
- 仪表盘 AliangCode 聊天 WS（`/api/agent/ai/stream`）由 **core 进程本地处理**
  （`agent_handler.go:59` `HandleAIStream` → `h.service.DispatchLocalAI`），
  会话存 core 内存 + 浏览器 localStorage——**不落 journal、不进 vibe 清单**，
  agent 记录与手机完全看不到（服务器上 `~/.aliang/ai-sessions` 目录不存在为证）。
- 两进程的路由表**各自独立**：core 用 `routes.go` 的 `RegisterRoutes`（56431），
  agent 用 `app/agentruntime/server.go` 的 `registerAgentRoutes`（56433，
  **当前不含** `/api/agent/ai/stream`——手机是经云端 remote-WS 隧道的 AI 分支
  （`agent_remote_ws.go:833-847`）触达同一 AI 管理器，不走 56433 HTTP）。
  handler `HandleAIStream` 当前无条件本地 dispatch（`agent_handler.go:65-87`），
  本单给它加 `services.IsUserAgentRuntime()` 角色分支——**agent 侧只缺一行路由
  注册**（本单补上），管理器与 journal 路径零改动。
- core 的 AI 上游自愈守卫（`agent_ai_proxy_guard.go` / `agent_ai_agentcore.go:279`）
  在 56432 不可达时静默 `POST /api/run/start` 拉起代理（修 10-13/10-15 事故的
  有意设计）——用户在未登录仪表盘上看到「代理莫名开了」即此（服务端
  `StartService` 只看启动状态 READY/CONFIGURED，即设备会话恢复，不看浏览器
  登录态；「未登录不能开代理」目前只是前端 JS 门）。
- 2026-10-27 15:52 UTC 实测：部署重启后首条聊天触发自愈拉起代理，但该回合
  **静默挂死 8 分钟**（142 次 CA 检查 + 33 次 GLM 网关隧道连接，无任何
  ai.error/ai.done），浏览器只有无限转圈——本地聊天路径的失败不可见是真实痛点。

## 1. 背景与目标

用户拍板（2026-10-27 深夜）：
- **统一**：「aliangcode 的对话应该统一访问我们的 agent 的对话记录」——仪表盘
  发起的会话必须与手机同源（journal + vibe 清单）；
- **绝不回退**：agent 不可达时仪表盘聊天直接报错，**绝不**退回 core 本地跑
  （单一真相源，永不产生进不了记录的对话）；
- **错误必达**：「会话期间发生的错误也要正确展示出来」——任何失败必须有终端
  事件且 UI 可见，挂死类静默必须有兜底；
- **自愈可见**：AI 自动拉起代理时不再静默（用户已同意「保留自愈 + 补可观察性」）。

**非目标**：手机端任何改动；core 本地 manager 代码删除（仅摘除聊天触达路径，
标 deprecated，清理另单）；历史 localStorage 会话迁移；`/api/run/start` 鉴权
体系重设计（自愈语义保留）。

## 2. 方案取舍

| 方案 | 结论 |
|---|---|
| **A1. WS TCP 级透传（Hijack 双向管道，本设计）** | core 不理解聊天协议、零协议漂移风险；审批/超时/会话语义全部收敛在 agent 一处；实现 ~100 行 |
| A2. 消息级中继（core 双端 JSON pump） | core 可注入/改写帧（现无需求），两端协议处理重复、长期漂移风险；否 |
| B. core 本地 manager 落 journal + 注册进清单扫描 | 双真相源，跨进程一致性复杂易漂移；否（用户拍板单一真相源） |

## 3. 架构与数据流

```
浏览器 WS → core:56431 /api/agent/ai/stream
   ├─ core 角色（!services.IsUserAgentRuntime()，新行为）：
   │    Hijack 浏览器连接 → 拨 services.UserAgentBaseURL() 的 56433 同端点
   │    → 写升级请求 → 双向 io.Copy 透传
   │    agent 不可达/握手失败 → 完成 Upgrade 后 Close(1011, "device agent offline")
   └─ agent 角色：现有 HandleAIStream → 本地 AI manager → runAgentCore
        → journal(~/.aliang/ai-sessions) + 审批 + vibe 清单（手机同款，零改动）
```

统一收益链：聊天会话跑在 agent → `agentCoreJournalStart` 落 journal →
既有「扫描第三路」把它带进 vibe 清单 → agent 记录/手机自动可见，**零额外登记代码**。

**agent 侧唯一改动**：`registerAgentRoutes`（`app/agentruntime/server.go:113-138`）
追加一行 `mux.HandleFunc("/api/agent/ai/stream", agentHandler.HandleAIStream)`，
并加注册存在性测试（`server.go:125-135` 的注释记录着 2026-10-21 漏注册 404
事故——本行测试就是那次教训的守卫）。除此之外 agent 端零改动。

## 4. 详细设计

### 4.1 WS 透传（core 侧，新文件 `app/http/handlers/ai_stream_proxy.go`）

- `HandleAIStream`（`agent_handler.go:65`）开头分支：
  `services.IsUserAgentRuntime()` 为真 → 现有本地 dispatch 原样保留（agent 角色零改动）；
  为假 → 调 `proxyAIStream(w, r)`，**删除** `DispatchLocalAI` 调用路径。
- `proxyAIStream` 步骤：
  1. Hijack 浏览器连接（复用 `aiStreamUpgrader`，`agent_handler.go` 既有）——
     **必须取 `Hijack()` 返回的 `bufio.ReadWriter` 并先排干其 `Buffered()` 字节**
     再进入裸 `io.Copy`：gorilla Upgrade 经带缓冲 reader 完成握手，回环场景下
     握手后早期 WS 帧可能已躺在缓冲里，裸 conn 拷贝会静默吞帧（先写缓冲残留，
     再泵裸连接）；
  2. `net.DialTimeout("tcp", hostPort, 5s)`，hostPort 取 `services.UserAgentBaseURL()`
     的 host（与 `proxyToUserAgentClient`（:444-445）同源地址解析）；
  3. 手写最小升级请求（`GET /api/agent/ai/stream HTTP/1.1` + `Upgrade: websocket` +
     `Connection: Upgrade` + `Sec-WebSocket-Key`（新随机值）+ `Sec-WebSocket-Version: 13`
     + `Host`），读 agent 响应行，非 `101` → 本侧先
     `WriteControl(CloseMessage, FormatCloseMessage(1011, "device agent offline"))`
     再 `Close()`（gorilla 裸 Close 不发 close 帧，浏览器只会看到 1006，
     §4.4 的 1011/「连接中断」区分就失效了；reason 在 123 字节限内）；
     浏览器侧 101 由**手写响应**完成（Upgrade 会二次 Hijack 且不暴露 bufio，
     无法与裸泵共存），但 **host 策略与 `aiStreamUpgrader.CheckOrigin` 同源复用**
     （抽 `aiStreamHostAllowed(r)` 共用）——经非 localhost 主机名访问的语义与
     今天（403）保持一致，绝不因透传悄悄放宽；
  4. 成功后两根 goroutine `io.Copy` 双向泵，任一侧 EOF/错误 → 双侧 `Close()`。
  不透传原始 `Sec-WebSocket-Key`/`Accept`（两侧握手各自独立完成，core 只在
  TCP 字节层面架桥——浏览器↔core 的握手由 aiStreamUpgrader 完成，core↔agent
  的握手自造新 key）。
- 不复制 `X-Aliang-Agent-Proxy` 等业务头（56433 的 `HandleAIStream` 不读它们）。

### 4.2 core 本地分支退场

- `HandleAIStream` 的 core 角色不再触达 `DispatchLocalAI`；**`DispatchLocalAI`
  与 core 侧本地 AI 管理器路径**保留（调用方审计确认仅 ai/stream 一处），
  注释标 `Deprecated: chat goes through the agent process since 2026-10-28;
  kept for reference, removal tracked separately`。`HandleAIStream` 本体继续
  服务 agent 角色（本地 dispatch），不是 deprecated。

### 4.3 自愈可观察性

- `agent_ai_proxy_guard.go` 的 `heal` 闭包与 `agent_ai_agentcore.go:279` 同位置：
  生产上有**两个 heal 闭包**（`ensureAIUpstreamReachableForRun`（proxy_guard.go:80）
  与 `agentCoreEnsureUpstream`（agentcore.go:277）），都委托给共享的
  `ensureAIUpstreamReachable`——通知逻辑放在**共享函数**里（`deps` 增加 notifier
  注入位），一处覆盖两个调用点。POST 返回后**以自愈后的复测结果为准**（
  `ensureAIUpstreamReachable` 本就有 re-dial——heal 通知挂在 re-dial 成功之后，
  而不是 `client.Post` 的 `err==nil`：非 root 403 时 POST「成功」但代理并没
  起来，按旧写法会误报）：re-dial 通了 →
  `logger.Info("AI 上游不可达，已自动拉起本机代理(56432)")` + 可注入 notifier
  （仿 `auth_runtime_hooks.go` 的 `desktopNotifier` 模式，测试替身）
  `desktop.Notify("aliang-gateway", "检测到 AI 代理未运行，已自动拉起")`；
  re-dial 仍失败 → 维持静默（失败路径已有「人话」ai.error 到 UI）。

### 4.4 前端错误契约（ChatPage.vue）

| 失败场景 | 现状 | 本单行为 |
|---|---|---|
| WS close（透传失败/agent 中途断） | catch 兜底文案 | `onclose` 事件带 code/reason：区分「设备 agent 不在线」(1011) 与「连接中断」；气泡报错 + 复位 `aiSessionCreated` 走既有确认门重试 |
| 运行中挂死（无终端事件） | 无限转圈 | **无活动看门狗**：从 `ai.message` 发出即武装（覆盖 `[发出 → ai.run.started]` 的无主窗口——挂死同样会发生在 run 尚未标记时），连续 `CHAT_IDLE_TIMEOUT_MS`（常量 90_000，测试可注入短值）无任何帧（delta/thinking/status/progress/usage 一律算活动；喂狗点统一在 `handleAIEvent` 入口，覆盖全部帧型）→ 前端合成气泡错误「AI 长时间无响应，请重试」并复位；收到任何帧即重置计时；`ai.done`/`ai.error`/socket close 时解除 |
| create 阶段失败 | 内联提示（上一单已建） | 保持 |
| run 期 `ai.error` | 进气泡（协议既有） | 保持 |

看门狗挂在 `handleAIEvent` 入口统一喂狗；`ai.done`/`ai.error`/socket close 时清除。

### 4.5 localStorage 存量会话

`conv.aiSessionId`（`local-*`）继续使用；跨 agent 重启后首次发送走既有「每条消息
前重发 create」确认门 → agent 侧 lazy 重建会话并带全设置，无丢失。浏览器本地
历史消息不迁移（UI 里仍可见，仅不进统一记录——与今天一致）。

## 5. 错误处理与边界

| 场景 | 行为 |
|---|---|
| agent 进程离线（拨号失败） | core Upgrade 后 `Close(1011, "device agent offline")`；前端气泡「设备 agent 不在线，请检查设备」；绝不本地跑 |
| agent 中途崩溃/重启 | 管道断 → 浏览器侧 close → 气泡「连接中断」+ 复位；下次发送 create 重建会话 |
| agent 在但 56432 未起 | 现有自愈（本单加可见性）；自愈失败 → 人话 ai.error（既有） |
| 回合挂死（agent 在但内核无输出） | 90s 无帧看门狗 → 前端可见报错（服务端 terminal-event 审计为后续项，UI 兜底先行） |
| core 与 agent 协议版本错位 | 透传不解析协议，agent 侧校验器天然把关 |

## 6. 测试与验收

**Go 单测**
1. **agent 侧路由注册存在性**：`registerAgentRoutes` 的 mux 上 `/api/agent/ai/stream`
   可达（2026-10-21 漏注册 404 事故的守卫测试，防回归）；
2. `proxyAIStream`：agent 拨号失败 → 浏览器侧收到 1011 close（内存内 conn 替身）；
3. agent 在 → 双向帧透传（写一帧/收一帧断言字节往返，含 Hijack 缓冲排干不吞帧）；
4. runtime 角色不透传（现有本地 dispatch 测试零回归）；
5. 自愈 heal 成功（re-dial 通）→ notifier 被调 + INFO 日志（注入替身）；re-dial 失败 → 不通知。

**E2E（前端，stub WS）**
6. 打开后立即 close(1011) → 聊天显示「设备 agent 不在线」且不发本地帧；
7. 看门狗（注入 300ms 短超时）：run started 后无帧 → 气泡出现「AI 长时间无响应」。

**人工验收（部署 liang-dev 后）**
1. 仪表盘发消息 → 回复正常流式；`.aliang/ai-sessions` 出现 journal；
2. **手机 AI 会话清单出现同一条会话**（统一的核心验收）；
3. 杀 agent 进程后发消息 → 「设备 agent 不在线」报错（非静默、非本地跑）；
4. 重启 core 后（56432 下）发消息 → 桌面通知「已自动拉起」+ 代理起来 + 回复正常；
5. 390px 移动端聊天回归正常。

## 7. 风险

| 风险 | 缓解 |
|---|---|
| Hijack 手写升级请求细节错（头序/大小写） | 单测用 httptest + 真实 gorilla 服务端对端验证；goencoding 由标准库保证 |
| Hijack 缓冲吞帧（Upgrade 后早期帧滞留 bufio） | §4.1 步骤 1 强制排干 `Buffered()`；单测第 3 项显式覆盖 |
| agent 地址解析在双进程拓扑（liang-dev 实态）与单进程形态不同 | 复用 `UserAgentBaseURL()`——它就是现有 HTTP 代理的同一真相源，行为一致 |
| 看门狗误伤长思考（推理模型 90s 无帧） | delta/thinking/progress/usage 任何帧都算活动；90s 静默对「无任何中间帧」的活回合已属异常 |
| core 侧 manager 残留被其他入口误用 | 审计调用方（仅 ai/stream 一处）；deprecated 注释 + 另单清理 |
