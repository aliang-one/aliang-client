# Claude Code 用量感知设计（小时粒度 · 后台材料用途）

日期：2026-09-23
状态：已确认（用户批准）
范围：alianggate（Go agent 客户端，采集+上报）+ nurson/aliang-phone-agent-server（服务端，入库）

## 1. 背景与目标

平台希望感知用户机器上的 AI 对话使用情况（token 用量、API 请求次数），作为
**后台侧的行为材料**：用于判断用户端 coding 力度，并作为宠物活跃度等功能的
输入材料。

关键定位（与用户确认的结论）：

- **感性统计**，不是计费级精确账单——容忍小误差，但要避免数量级的重复计数。
- 第一期**只入库后台一张表**，不做 App 用户端展示、不做查询 API。
- 采集对象：**Claude Code 会话**（读本地 JSONL）。不依赖流量是否经过网关，
  API key 直连、Pro/Max 订阅用量同样可统计。
- 指标：token 数（含 cache 拆分）、API 请求次数、模型、时间。**不做美元成本估算**。

## 2. 为什么读本地 JSONL 而不是网关流量

Claude Code 每条 assistant 消息都会把用量写入本地会话文件
`~/.claude/projects/<项目路径编码>/<会话uuid>.jsonl`，字段包含
`usage`（input/output/cache_creation/cache_read tokens）、`model`、`timestamp`、
`sessionId`、`uuid`。

- agent 常驻用户机器，读文件即可，**与网络路径完全解耦**；
- 解析在用户自己的机器上、只取元数据不碰对话内容，隐私边界清晰；
- 网关/流量镜像方案只能覆盖走网关的流量，且 TLS 明文解析依赖 MITM 前提，不可靠。

已否决的备选：服务端按需拉取（agent 离线无数据、存不了历史趋势）；
Claude Code hook 实时推送（只覆盖受管配置，且最终仍要解析文件，绕路）。

## 3. 架构

```
~/.claude/projects/**/*.jsonl          agent（本仓库）                      PhoneServer                  系统后台
┌────────────────────────┐   ┌───────────────────────────┐   WS   ┌───────────────────┐
│ Claude Code 会话文件    │──▶│ ① usage tracker           │──────▶│ ② usage.report     │──▶ ai_usage_hourly
│ 每行 usage/model/uuid  │轮询│  增量解析(offset水位)      │WriteJSON│  upsert 入库      │    （一张表）
└────────────────────────┘   │  聚合(小时×模型)桶持久化    │        └───────────────────┘
                             └───────────────────────────┘
```

组件边界（`processor/usage/` 新包）：

| 组件 | 职责 | 可测性 |
|---|---|---|
| 解析器 `parser.go` | 纯函数：JSONL 行 → UsageSample，白名单字段反序列化 | 无 IO，直接单测 |
| 跟踪器 `tracker.go` | 文件发现、offset 水位、增量读、聚合桶、桶持久化 | 水位/聚合逻辑单测 |
| 上报器 | dirty 桶经现有 WS 通道推送；离线保留、重连补推 | 推送节奏/补推单测 |

## 4. 采集细节

### 4.1 文件发现

- 扫描根：`agentHome()`（复用现有 EffectiveAgentHome 解析）下的
  `projects/**/*.jsonl`。
- **轮询**，间隔 30s（常量）。不引入 fsnotify：跨平台行为一致，粗统计场景
  30s 延迟足够。
- 首次启动（水位表为空）= **历史回溯**：全量扫一遍存量 JSONL，宠物材料
  立即拥有历史数据。

### 4.2 解析（隐私边界）

- 白名单反序列化：仅 `type`、`sessionId`、`uuid`、`timestamp`、`isSidechain`、
  `message.model`、`message.usage.*`。结构体不含 content 字段——对话内容
  从类型层面就进不了本模块。
- 只处理 `type == "assistant"` 且 `message.usage` 非空的行；每行记一次
  **API 请求**（一次上游响应）。
- subagent 侧链行（`isSidechain == true`）**计入**：它们是真实 API 消耗。
- 单行解析失败 / JSON 非法 → 跳过该行继续（粗统计容忍）。
- **半行**（文件正在被写入，尾部无换行）：不消费，offset 停在最后一个完整行
  边界，下轮继续。

### 4.3 水位（防重复计数）

- GORM + sqlite 表：`file_path → offset`，随读随进。存储复用本地 `aliang.db`
  （现有 GORM sqlite 惯例，参照 `processor/auth/user_info.go`），新增
  `usage_watermarks` 与 `usage_buckets` 两表。
- 文件 size < 已记录 offset（截断/重建）→ 水位归零重扫。
- 文件消失 → 删除水位行（会话文件可能被用户清理；其已聚合数据保留在桶里）。
- 粗统计定位下不追求 inode/fileId 追踪；size 单调增长是 JSONL 的常态。

### 4.4 聚合桶

```
key   = (hour_start, model)
hour_start = 消息 timestamp 转为 agent 本地时区后所在小时桶的起点（epoch 秒）
tz         = agent 本地时区 IANA 名（宠物活跃度关心"本地几点"）
```

每桶累加：`requests`、`input_tokens`、`output_tokens`、`cache_read_tokens`、
`cache_creation_tokens`、`active_sessions`（该小时内出现过的去重 sessionId 数）、
`first_seen`、`last_seen`（epoch 秒）。

**active_sessions 去重集随桶持久化**（JSON 序列化存桶行内）：agent 重启后
同一会话继续写入同桶时不会重复计数。集合大小受"该小时内有活动的会话数"天然
约束，无需封顶。

model 保留原始字符串，不做归一化。桶持久化在本地 sqlite（见 §5 离线补推）。

## 5. 上报协议

### 5.1 消息契约（agent → PhoneServer，现有 WS 通道）

```json
{
  "type": "usage.report",
  "records": [
    {
      "device_id": "<agent 设备标识，复用现有 DeviceID>",
      "hour_start": 1727071200,
      "tz": "Asia/Shanghai",
      "model": "claude-sonnet-4-5",
      "requests": 42,
      "input_tokens": 123456,
      "output_tokens": 23456,
      "cache_read_tokens": 999999,
      "cache_creation_tokens": 12345,
      "active_sessions": 2,
      "first_seen": 1727071500,
      "last_seen": 1727074500
    }
  ]
}
```

消息外层信封沿用现有 WS 消息约定；records 单批 ≤ 500 条，超出分批发送。

### 5.2 节奏与离线补推

- 当前小时桶每 **5 分钟**推送一次全部 dirty 桶（含未确认的历史桶）；
  跨小时边界时立即 flush。
- **服务端按 `(device_id, hour_start, model)` upsert**——桶是累计值语义，
  重复推送无副作用，天然幂等。
- 未成功推送的桶持久化在本地 sqlite，agent 重启/重连后照常补推；
  推送成功后清除 dirty 标记（不删桶，桶随水位推进自然只增）。
- "推送成功"的判定：沿用现有 WS 请求-应答约定——未收到
  `AgentEventError` 错误应答即视为成功；具体封装在实施计划阶段对齐
  `agent_remote_ws.go` 现有调用模式。
- 极端情况（卸载前未推完）丢桶可接受——感性统计定位。

### 5.3 开关与生命周期

- 配置开关 `usage_tracker.enabled`，**默认开**；关闭时不扫描不上报。
- agent 处于 disable 状态时暂停采集与上报（与现有 agent 生命周期一致）。

## 6. 服务端表（PhoneServer 仓库）

```sql
ai_usage_hourly (
  id                    BIGSERIAL PRIMARY KEY,
  device_id             VARCHAR NOT NULL,
  hour_start            BIGINT  NOT NULL,  -- epoch 秒，agent 本地小时桶起点
  tz                    VARCHAR NOT NULL,
  model                 VARCHAR NOT NULL,
  requests              BIGINT  NOT NULL DEFAULT 0,
  input_tokens          BIGINT  NOT NULL DEFAULT 0,
  output_tokens         BIGINT  NOT NULL DEFAULT 0,
  cache_read_tokens     BIGINT  NOT NULL DEFAULT 0,
  cache_creation_tokens BIGINT  NOT NULL DEFAULT 0,
  active_sessions       INT     NOT NULL DEFAULT 0,
  first_seen            BIGINT  NOT NULL DEFAULT 0,
  last_seen             BIGINT  NOT NULL DEFAULT 0,
  updated_at            TIMESTAMP NOT NULL DEFAULT now(),
  UNIQUE (device_id, hour_start, model)
)
```

- upsert：整行覆盖（桶为累计快照，不是增量）。
- 该表定位为**内部材料**：第一期不暴露给 App 用户端查询。
- 多设备同账号：device_id 天然区分，账号级聚合留给未来后台查询侧做。

**服务端仓库工作项**（PhoneServer，量很小但必须显式列出，防止漏做）：

1. GORM 迁移：建 `ai_usage_hourly` 表；
2. WS 消息 `usage.report` 处理器：校验 records 数组、按
   `(device_id, hour_start, model)` upsert 整行覆盖；
3. 第一期不做任何查询接口。

## 7. 错误处理

| 场景 | 行为 |
|---|---|
| 单行 JSON 非法 | 跳过该行，继续 |
| 尾部半行 | offset 停在最后完整行，下轮再读 |
| 文件被截断/重建 | 水位归零重扫（可能轻微高估，可接受） |
| 文件被删除 | 删水位行；已入桶数据保留 |
| 同路径文件重现（删后从备份恢复） | 水位归零重扫，已入桶数据可能重复——与截断同路，可接受 |
| sqlite 写失败 | Warn 日志，下轮重试 |
| WS 离线 | 桶留在本地，重连补推 |
| 服务端 500/超时 | 桶保持 dirty，下轮重推 |
| 时钟回拨 | 一切时间取自消息内 timestamp，不信文件 mtime/系统钟做归因 |

## 8. 测试计划

- **解析器**（fixture 驱动）：真实 JSONL 样本——含 cache 字段的 assistant 行、
  非 assistant 行、无 usage 行、sidechain 行、非法行、半行。
- **水位**：增长推进、截断归零、删除清理、首启全量回溯。
- **聚合**：跨模型分桶、active_sessions 去重、跨小时切桶、时区换算。
- **补推**：离线期间产生增量 → 重连后 dirty 桶完整补发；重复推送幂等（服务端
  语义由契约保证，客户端测试只验"重发相同桶"）。

## 9. 明确不做（YAGNI）

美元成本估算、App 用户端展示与查询 API、会话级/项目级明细上报、
exactly-once 语义、Cursor 等非 Claude Code 工具适配、fsnotify、
网关流量路径的用量统计（现有 HTTPStatsCollector 维持现状不动）。

## 10. 风险与缓解

- **JSONL 格式演进**：Claude Code 版本更新可能改字段名 → 白名单解析对未知
  字段天然容忍；usage 字段缺失时该行不计（宁可少计不多计）。
- **文件量过大拖慢回溯**：首轮回溯按文件逐个处理、每轮 tick 限时，未扫完的
  下轮继续（水位保证断点续扫）。
- **隐私**：解析器白名单字段从类型层面隔离对话内容；上报仅含计数与时区，
  无路径、无内容。
