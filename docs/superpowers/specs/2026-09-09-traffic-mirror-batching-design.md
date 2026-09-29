# 流量镜像批量聚合升级设计（Traffic Mirror Batching）

日期：2026-09-09
状态：已确认（用户批准）
范围：alianggate（Go 客户端）+ MonodirCursorTraffic（Node.js 抓包服务端）

## 1. 背景与问题

alianggate 的 traffic mirror 功能将匹配域名的 TLS 明文流量以 StreamChunk/FlowEvent 形式
异步 POST 到抓包服务（MonodirCursorTraffic，端点 `POST /mirror`）。当前转发管道
（`processor/mirror/forwarder.go`）存在一个核心瓶颈：

- **每条 chunk 一个 HTTP POST**。TCP Read 切片通常仅几百字节到几 KB，高流量域名下产生海量小请求。
- 单 worker 同步发送 + 10s 超时，容易把 1024 容量的 channel 打满，开始丢消息（静默降级）。
- `resp.Body.Close()` 未 drain，HTTP keep-alive 连接无法复用，每个 POST 重新建连，放大开销。

服务端（`src/server/web-server.js`）单条 ingest，`flow-manager.js` 已具备 offset gap
检测与 `mark_incomplete` 标记能力，靠 `flow_id + direction + offset` 重建字节流。

## 2. 约束（已与用户确认）

| 维度 | 决定 |
|---|---|
| 网络环境 | 内网/本机 → 不做 TLS/强认证 |
| 完整性要求 | 允许偶发丢失 → 保持 fire-and-forget，不做重试/ACK/磁盘缓冲 |
| 兼容策略 | 两端同步升级 → 协议可自由扩展；旧单条端点保留用于测试 |

## 3. 方案选型

- **A. 批量聚合（选定）**：worker 攒批，凑满条数/字节或超时即整批发送。直击痛点，改动面最小。
- B. A + gzip 压缩/二进制帧：内网带宽不稀缺，收益递减，两端加编解码层不值得。
- C. WebSocket 流式长连接：服务端 ingest 语义重写 + 背压管理，与"允许丢失"定位不匹配。

## 4. 协议（两端契约）

```
POST /mirror/batch
Content-Type: application/json

请求体: {"messages": [ <StreamChunk|FlowEvent>, ... ]}
        消息格式与现单条 /mirror 完全相同，仅多包一层 messages 数组。

响应:   {"ok": true, "accepted": 198, "failed": 2}
        ok 表示批次已被受理（不表示每条都成功）；单条失败计入 failed。
```

- 批内顺序 = 发送顺序；服务端逐条顺序处理 → `flow_start` 先于 chunk 的时序保持不变。
- 批量发送延迟（≤200ms）远小于服务端 `maxGapWaitMs=5s`，不会误触发 gap 超时。
- 旧 `POST /mirror` 端点保留不删（联调、排障、旧测试仍可用）。

**批量端点 URL 推导规则（客户端）**：配置下发的 `traffic_mirror.target` 保持现状
（形如 `http://host:3000/mirror`，**不需要用户改配置**）。客户端 forwarder 内部推导
批量端点：

1. 先去掉 target 末尾的 `/`（防 `http://host:3000/mirror/` 推导出 `//batch` 404）；
2. 若 target 以 `/batch` 结尾 → 直接作为批量端点使用（幂等，防重复追加）；
3. 否则 → 追加 `/batch`。

即 `http://host:3000/mirror` → `http://host:3000/mirror/batch`。单条端点仅从原始
target 派生，不受影响。配套在 `docs/tutorials/zh_CN/usage-guide.md` 的
`traffic_mirror.target` 示例处加一行说明：路径后缀保持 `/mirror`，客户端会自动
追加 `/batch`，请勿自行改成 `/mirror/batch`（避免双重追加）。

## 5. 客户端改动（alianggate，仅 `processor/mirror/forwarder.go`）

### 5.1 攒批 worker

```go
const (
    batchMaxMessages   = 200
    batchMaxBytes      = 512 * 1024      // 防单批过大
    batchFlushInterval = 200 * time.Millisecond
)
```

run loop（保持单 goroutine，保序天然成立）：

1. 从 `ch` 逐条收消息，append 到 pending；累计条数与字节数（按每条消息 marshal 后
   的 JSON 字节数计，实现上构建批次时逐条编码累计即可，不做二次估算）。
2. 满足任一条件即 flush：条数 ≥ 200 / 累计字节 ≥ 512KB / 距上次 flush ≥200ms
   且 pending 非空（定时器在每次 flush 后重置；凑不满也发，保证低流量下延迟有界）。
3. flush = 整批 `json.Marshal` 包成 `{"messages":[...]}` → `POST /mirror/batch`。
4. `Stop()`：`close(ch)` → 排空 pending → flush 尾批 → close(done)。（保证 flow_end 不丢）

### 5.2 顺带修复

- **drain body**：POST 成功后 `io.Copy(io.Discard, io.LimitReader(resp.Body, 4KB))`
  再 `Close`，恢复 keep-alive 连接复用。
- **丢弃可观测**：`Forwarder` 增加 `dropped atomic.Uint64`；`Enqueue` 队列满时递增
  （保留现有 Debug 日志）。

## 6. 服务端改动（MonodirCursorTraffic）

- `src/server/web-server.js` 新增 `POST /mirror/batch`：
  - 校验 `messages` 为数组，否则 400。
  - 逐条 `try/catch` 调 `flowManager.handleMirrorMessage(msg)`；单条失败跳过并计数，
    不影响批内其他消息。
  - 返回 `{ ok, accepted, failed }`。
- `ingestStats` 增加 `totalBatches` 计数。
- `maxBodySize`（10MB）无需调整：512KB 批上限远小于现有限值。
- `config.js` 增加可选 `ingest.maxBatchSize`（默认 500）作为服务端防御性上限，
  超过的批次整批 400 拒绝（客户端 200 上限远低于此，正常不触发）。

## 7. 错误处理

| 场景 | 行为 |
|---|---|
| 客户端队列满（1024） | 丢消息 + dropped 计数；服务端 gap 标记兜底（与现状一致） |
| POST 失败/超时 | 整批丢弃，不重试；Debug 日志 |
| 批内单条非法 | 服务端跳过该条，其余照常入库 |
| 客户端 Stop | 排空 channel + flush 尾批 |
| 服务端不可达 | 与现状相同：Debug 日志，主链路不受影响 |

## 8. 测试计划

客户端（alianggate）：
- 新增 `processor/mirror/forwarder_test.go`：
  - 满 200 条即触发一次 POST；
  - 200ms 超时冲刷（不满也发）；
  - `Stop()` 排空尾批；
  - 批内顺序与入队顺序一致；
  - 队列满时 dropped 计数递增；
  - 服务端 500/超时时不阻塞后续批次；
  - 批量端点 URL 推导：target 以 `/batch` 结尾时不重复追加；否则追加一次 `/batch`。
- 改造 `processor/tcp/route_override_test.go` 中的 e2e 测试：
  httptest server 改为接收 `/mirror/batch` 并解包校验消息。
- 保持连接复用验证：httptest 统计连接数（`Close` 前 drain 生效后，多次 POST 复用同一连接）。

服务端（MonodirCursorTraffic）：
- `test-ingest.js` 增加 batch 用例：
  - chunk / flow_start / flow_end 混合批次正确入库；
  - 批内含非法消息时 accepted/failed 计数正确；
  - `messages` 非数组返回 400。

冒烟：真实流量（如 `*.cursor.sh`）跑一轮，dashboard 验证流完整性与 gap 标记行为不变。

## 9. 明确不做（YAGNI）

重试、ACK、磁盘缓冲、压缩、TLS/认证、转发参数配置化。

## 10. 风险与缓解

- **单批体积**：极端突发下 512KB 批仍偏大 → 字节上限截断批，且服务端 maxBodySize 10MB 兜底。
- **两端不同步部署**：旧客户端只打 `/mirror`（端点保留，不受影响）；新客户端打
  `/mirror/batch`（需服务端先就绪）→ 部署顺序：先服务端后客户端。
- **行为回归**：现有 e2e 测试覆盖镜像触发与路由语义，改造后全量跑通即可回归。
