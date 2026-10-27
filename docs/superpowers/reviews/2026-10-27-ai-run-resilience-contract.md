# AI run 韧性 — 跨仓契约（客户端 → 服务端/手机）

## 新增/变更的帧

### 1. 判死终态

```json
{"type":"ai.error","session_id":"...","message_id":"assistant_...","error":"回合已停止响应（stalled），已判死，请重试","error_code":"turn_stalled","status":"stalled"}
```

- 语义：一个 run 的**唯一终态**；服务端/手机必须把会话切到 Failed 并允许 Retry。
- `error` 实际文案是 `回合已停止响应（<reason>），已判死，请重试`，`<reason>` 是变体（非固定字符串 `stalled`）；手机**不得**对 `error` 文本做等值匹配，判死只认 `error_code:"turn_stalled"` / `status:"stalled"`。
- Retry = 用户重发消息 = 新 runSeq `--resume` 原生会话，上下文不丢（agent 侧已支持）。
- 服务端不得自动重试。

### 2. steer 失败反馈

```json
{"type":"ai.steer.ack","session_id":"...","message_id":"...","result":"error","error":"回合无响应已判死，请重试","code":"turn_stalled"}
```

- 手机必须把该条插话标为失败（可重发），不得静默忽略。

### 3. 平台通道告警

```json
{"type":"agent.channel.alert","code":"platform_credential_expired","message":"设备在线但平台凭据过期，文件传输等功能暂不可用","consecutive_401":3,"since":"2026-10-27T03:00:32Z"}
```

- 限频 30min 一条。手机据此提示用户重新登录，不得据此把设备标离线。

## 服务端 sweeper 对齐

- agent 尽力为每条判死路径发终态，但存在两条已知残余路径，终态可能到不了客户端：
  - 终态帧与 idle 状态帧抢闸：两者过同一个 emitter 终态闸，谁先谁得。不变量仍是「恰好一个终态」，但若 idle 状态帧先赢，客户端只看到回到 idle、看不到错误文案；判死原因只在 `[ai.run.force_finalize]` 日志与 killReason 里。
  - emitter 写侧阻塞：僵尸正卡在 `e.write` 时这条终态根本发不出去，留 `terminal_not_delivered` 日志，服务端侧只能靠 stale 扫描器收敛。
- 故服务端 sweeper 的「超时无帧即判死」兜底**必须保留**，不得因本文档而移除。
- `ai.run.progress` 心跳**不**代表回合活着（内部已被排除出活性口径），服务端不得以心跳存在与否作为判死依据。

## 真机验证清单

1. 正常回合：对话完整，无重复帧。
2. 人为断上游（防火墙丢包）90s 内手机必须脱离「运行中」（收到错误终态或回到 idle 之一），且 pod 日志出现 `[ai.run.force_finalize]`；两者都无才算失败。Retry 后上下文连续。
3. 长工具调用 7min：不被判死，正常完成。
4. 审批挂起 3h：不被硬顶误杀；24h 后判死。
5. 持续 401 场景：收到 `agent.channel.alert`（30min 内最多一条）。
6. 回合中插话且回合已死：收到 `turn_stalled` ack。
