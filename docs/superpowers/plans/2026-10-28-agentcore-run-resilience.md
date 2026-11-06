# Agentcore 长对话脆弱性加固（真因可见 + 上下文超限可恢复 + 主动压缩兜底）

## 背景（已定位）
会话 `agentcore-1793156443540925000-2` / run `run_JpEM1G_eZE8hC-jz`：runSeq=2 跑 31 回合后，
11:25:32 最后一个成功回合 + Bash 自动批准，31 秒后 11:26:03 直接 `ai.error`，整轮判死。

- 不是本地守卫（maxIterations=50 只用了 31 轮）。
- 是**上游/代理错误被客户端判为不可重试** → 11 分钟长对话一次性报废。
- **真因三层全丢**：内核只把错误放进 result 帧（不写日志）；代理 56432 不记上游响应；宿主把 `endErrMsg` 硬写成 `"agentcore run failed"`，丢弃 `runErr` 真文本。
- 每回合 `in=0 cache_read=0 ctx=0`（网关不报 input token）→ 基于 usage 的上下文计量恒 0，只剩 `EstimateTokens` 兜底。
- 若手机未带 `context_window` → `effectiveContextWindow()=0` → `maybeAutoCompact` 直接 return，**主动压缩永不触发**。
- 网关错误常以 **HTTP 200 + 裸 JSON 信封** 返回（client.go 注释实测 2026-10-25），但 `isContextOverflowError` 只认 4xx → 2xx 信封的超长错误既不被压缩重试、`upstreamError.retryable()` 对 invalid_request 又为 false → 直接判死。

用户诉求：**客户端不要太脆弱，至少要有重试机制**。

## 变更清单（4 个文件）

### Fix 1 — 可观测性：真因不再被吞（最关键）
**`app/agentcore/loop.go`**
- `RunResult` 增加 `ErrMessage string`。
- `finish()`：`isError` 时把 `errText` 写入 `res.ErrMessage`。
- `finishWithErr()`：判死前 `r.logf("[agentcore] run 失败终止 turn=%d err=%v session=%s", r.turns, runErr, r.sid())`。

**`app/http/services/agent_ai_agentcore.go`（~1253）**
- `result.IsError` 分支：`endErrMsg` 优先取 `result.ErrMessage`，其次 `runErr.Error()`（不再写死笼统串），并 `logger.Warn` 落盘。
- 效果：journal 的 `end` 记录 + `aliang_core.log` 从此带真实错误文本。

### Fix 2 — 上下文超限可恢复：识别 2xx 信封
**`app/agentcore/loop.go`**
- 抽 `matchesContextOverflowText(s string) bool`（沿用现有 needle 列表）。
- `isContextOverflowError`：4xx `httpStatusError` 走 `matchesContextOverflowText(body)`；新增 `*upstreamError`（2xx 信封）走 `matchesContextOverflowText(message) || matchesContextOverflowText(body)`。
- 防重复帧：runner 增 `turnFrameEmitted bool`（每回合重置，首个 EventMessageStart/ContentBlock 置 true）；`noteContextOverflow` 里若 `turnFrameEmitted` 为 true 则不恢复（2xx 裸信封天然零帧，安全；此标志只做防御）。
- 效果：`input too long` 以 2xx 信封回时，也走"压缩历史 + 重试本回合"，而不是整轮失败。

### Fix 3 — 主动压缩永不缺席
**`app/agentcore/loop.go`**
- `effectiveContextWindow()`：`cfg.ContextWindow <= 0` 且无 overflow override 时返回 `DefaultContextWindow`（200k），而不是 0。
- 效果：即便手机不传 `context_window`，`maybeAutoCompact` 也按 200k 的 3/4 阈值正常触发，避免上下文无限膨胀直到被上游拒。

### Fix 4 — 重试预算（保守加一档）
**`app/agentcore/client.go`**
- `maxRetries` 3 → 5（仅瞬态类：连接错误 / 429 / 5xx / 2xx 限流过载；退避指数 + 尊重 Retry-After 上限 30s 不变）。
- `upstreamError.retryable()` 增加 `"server_error"`、`"timeout_error"`、`"overloaded_error"`（已有）。
- 不动 streamIdleError（非重试，防重复帧）与 invalid_request（非上下文超限时重试无意义）。

## 测试
- `loop_context_overflow_test.go`：`TestIsContextOverflowError` 增 2xx 信封用例（`{"error":{"type":"invalid_request_error","message":"input is too long"}}` → true；`rate_limit` → false）。
- 新增 `TestEffectiveContextWindowDefaults`：cfg.ContextWindow=0 → 返回 DefaultContextWindow。
- 新增 `TestFinishWithErrSetsErrMessage`：finishWithErr 返回的 `RunResult.ErrMessage` 非空且含错误文本。
- 回归：`go test ./app/agentcore/... ./app/http/services/ -run 'Overflow|Compact|Finish|Retry|ContextWindow' -count=1`；再全量 `go build ./...`。

## 非目标（本次不做）
- run 级断点续跑 / 跨请求 resume（内核内存态，工作量大，另立 spec）。
- 代理 56432 上游响应落日志（服务端侧，另议）。
