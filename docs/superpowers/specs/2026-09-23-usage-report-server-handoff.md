# usage.report 服务端契约交接（PhoneServer / aliang-phone-agent-server）

日期：2026-09-24
来源 spec：`docs/superpowers/specs/2026-09-23-claude-code-usage-awareness-design.md`（§5.1 消息契约 / §6 服务端表）
状态：客户端（alianggate `feat/usage-tracker` 分支）已实现并测试通过；本文档是 PhoneServer 侧的唯一待办输入。

## 1. 背景

agent 客户端增量解析用户机器上 Claude Code 的本地会话 JSONL，按（本地小时 × 模型）聚合
token/请求数，经现有 agent WebSocket 通道以 `usage.report` 消息上报。用途：后台行为材料
（coding 力度 / 宠物活跃度），**非计费数据，感性统计容忍小误差**。第一期只入库一张表，
无任何用户端查询接口。

与既有 `ai.usage`（平台远程 AI run 的逐轮用量）互不影响，勿混淆。

## 2. 消息契约（agent → PhoneServer，现有 WS 通道）

```json
{
  "type": "usage.report",
  "records": [
    {
      "device_id": "<agent 设备标识>",
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

- `records` 为数组，单批 ≤ 500 条（单批 JSON 约 100–250KB——**PhoneServer 的 WS 读帧上限
  必须能容纳**，联调时确认）。
- `hour_start` = agent 本地时区小时桶起点（epoch 秒）；`tz` = agent 本地 IANA 时区名。
- 语义：桶为**累计快照**（非增量），客户端可能重复推送同一桶。

## 3. 服务端处理要求

1. WS 分发器加 `type == "usage.report"` 分支（ClientSends 注册表已声明 Required: type + records）。
2. 校验 `records` 为数组；非数组按现有 `AgentEventError` 错误应答约定回复。
3. 逐条 upsert 入表，键 `(device_id, hour_start, model)`，**整行覆盖**（快照语义）；
   单条失败跳过，不影响批内其他消息。
4. 第一期不做任何查询接口。

## 4. 表结构

```sql
ai_usage_hourly (
  id                    BIGSERIAL PRIMARY KEY,
  device_id             VARCHAR NOT NULL,
  hour_start            BIGINT  NOT NULL,
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

（spec §6 原文；如 PhoneServer 侧命名/迁移惯例不同，按其惯例等价落地，唯一约束必须保留。）

## 5. 部署顺序

**先服务端后客户端**。旧服务端对未知 WS type 的默认行为需在联调时确认：若忽略未知
type 则天然兼容；若会报错/断连，则必须等本契约的服务端实现先上线。

## 6. 联调清单

- [ ] 服务端能接收单批 500 条（帧大小确认）
- [ ] 重复推送同一桶 → 数据不重复（upsert 生效）
- [ ] 离线 agent 重连后补推 → 缺口被补齐
- [ ] 时钟漂移/时区：`hour_start` + `tz` 组合在后台展示侧正确换算
