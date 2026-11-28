# 会话「后台中」状态 + 状态链路修复批 设计稿

- 日期:2026-11-28
- 状态:待拍板/待实施
- 依据:`t3-status-chain-adversarial-verdict-20261128`(30-agent 对抗检查定谳)+ 用户产品拍板:「主 turn 已结束但后台仍在干活」显示独立状态**后台中(background)**,比笼统 running 更细粒度。
- 关联需求:SYNC-15(TUI 内容快而完整到达)/ SYNC-16(长任务过程可见)family;T3 借鉴消息状态工作项。

## 1. 状态语义表(目标态)

| 状态 | 采集层判据 | 显示 |
|------|-----------|------|
| `running` | pid.json busy 且 `claudeBusyTurnEndedAfterRecord` 无 turn_duration 反证(现状不变) | 进行中 |
| `background`(新) | 主 turn 已结束(reconcile 判定)**但** `<sessionId>/subagents/*.jsonl` 任一文件 mtime < 3min | 后台中 |
| `idle` | 无 busy;主转录与 subagent 均静默(现状不变) | 空闲/已暂停 |
| `closed` | 窗外/终态(现状不变) | 已完成 |

- `waiting`(pid.json 权限挂起,现落 default→idle)**本期不动**,挂账另开条目。
- codex 腿不新增状态(rollout mtime 已覆盖 running,codex 无 subagent 转录形态)。

## 2. 采集层(alianggate `agent_inventory.go`)

1. **overlay 第四输入**:对 reconcile 判 idle 的 claude 会话,stat 其 `<projectsDir>/<sessionDir>/subagents/` 目录取最新文件 mtime;`< backgroundFreshnessWindow`(3min,与 `claudeStatusFreshnessWindow` 同值不同常量,便于分调)→ `Status = "background"`。
2. **45c63fab 剔除保留**:walk 主通道仍不进 subagents 目录(防 #542 挤占窗口);回喂走独立轻量扫描——只对在册会话做 1 次目录 ReadDir + stat,不读文件内容。约 300 会话/周期,开销可控;目录不存在即跳过。
3. **reconcile 本体不动**:`claudeBusyTurnEndedAfterRecord` 仍一票判「主 turn 结束」(11-18 定案,阳性验证健全);结束后的 subagent 检查只产生 `background`,**永不回 running**。
4. **已知局限(记录在案)**:subagent 单个超长工具调用(如 10min Bash)期间转录无写入,>3min 后台态衰减为 idle——与主转录新鲜度同款局限,v1 接受;窗口调宽会复活「后台僵尸」家族(11-18 修的),不做。
5. 单测 `agent_inventory_background_status_test.go`:turn_duration 后 subagent 活跃→background;subagent 静默→idle;busy 正常→running 不回归;reconcile 阳性用例不回归;background 3min 衰减→idle。

## 3. 协议与推送(alianggate)

1. status 新增 `background` 值:hello 帧指纹=整结构体 sha1(已含 status 字段),无需改;POST /api/agent/status 同。
2. 契约测试钉住:delta 帧 `session_changes` 携带 background 翻转条目;`agent_hello_delta.go:341` 误导注释顺手修正(语义随 §5.3 修复)。

## 4. PhoneServer(独立仓 `AliangPhoneServer`)

1. schema:`status` 若为 z.string 无需改,实施时核对 `agentStatusSyncSchema`;若枚举则补 `background`。
2. upsert 透传 `background`(reported.ts)。runState 冻结闸(:489)本期不动(rank4 挂账)。
3. **settle 语义(随 §5.1 修复合入,顺序不可倒)**:`releaseDeviceRunningSessions` 的收口范围从 `running` 扩为 `running + background`(agent 真重启时后台 lane 同死,应一并收口)。**前提=裁决 socket 门控已修**;在 §5.1 落地前 background 不得入 settle 范围(否则被 20s 误杀连环抹)。
4. 墓碑保守删:`removeReportedVibeSessions` 的跳过集(`status==='running'`)扩为 `running|background`,防 delta 误删后台条目。
5. 显示端最小支持:status 值透传给手机/网页(纯透传,无新接口)。

## 5. 服务端三修(rank1/2/3,本批必须含——否则后台中显示不稳)

1. **rank1 裁决加固**:重启裁决只在「新 socket 建立且旧连接已断」时触发;同一长连 socket 上的 digest re-hello 不参与裁决(agent 连续性以 socket 生命周期为准;restartSettle 注释自证的既有风险)。测试:同 socket 双 hello 不触发裁决。
2. **rank2 接线**:hello handler 消费 `session_changes`(逐条复用 upsert 单条路径),schema 补键;契约测试「delta 帧翻转 ≤1 周期落库」。spec 2026-11-26-hello-delta-protocol.md §3.3「无需新写侧」假设作废,以本条为准。
3. **rank3 计数**:registered 回包新增 `server_session_count`(服务端真实总量),agent 侧 `reconcileRegisteredServerSessions` 改用新字段;`vibe_session_count` 保留原语义(本帧 changed 条数)。**发布顺序:先 rank2 后 rank3 同批上线**——单修任一会打开「翻转硬丢失」窗口(rank2+3 双向耦合地雷)。

## 6. 显示端

1. 网页(app/website,本仓):会话卡「后台中」形态(独立标签+呼吸动画,配色与 running 区分)。
2. 手机 RN(独立仓):同款状态——**跨仓紧随批**,本批先落 agent+服务端+网页。
3. 背景:status 纯字符串透传,显示端改动仅为渲染分支。

## 7. 验收标准

- 采集 API:77058/1977 类会话(turn 已结束、subagent 活跃)显示 `background`;正常 running/idle 不回归;3min 衰减生效。
- 端到端:后台中会话在网页可见;agent 真重启后 background 随裁决收口。
- rank1:pod 日志 `[agent-restart] detected` 仅在真重连出现(现每 20-40s 连环 → 0)。
- rank2/3:稳态 full 帧占比 52% → <5%;翻转 ≤1 周期落库;出向流量 ~162MB/天/设备 → 降两个量级。
- 止血(独立于本批):装 master CI 出的 v1.1.148-local(含 5cd2fc0a)后,误杀连环立即消失——本批的 rank1 加固是防「版本/行为再次错配」的治本层。

## 8. 不做/挂账

- waiting 独立状态(另开条目)
- rank4 runState 冻结 / phone-owned 整行跳过(潜伏陷阱,单独批;咬「手机续跑到 TUI」流)
- 手机 RN 展示(紧随批)
- hello 全量→增量结构性改造(已有 spec,本批 rank2/3 是其修复面)
- f09b worktree 残留改动:丢弃(=已合并 edd522cb 的字节级陈旧拷贝,提交即回退 master 演进)

## 9. 实施切分建议

| 切片 | 内容 | 仓 | 依赖 |
|------|------|----|------|
| S1 | 采集层 background + 单测 | alianggate | 无 |
| S2 | PhoneServer rank1 裁决加固 + rank2 接线(先行) | PhoneServer | 无 |
| S3 | PhoneServer rank3 计数 + settle/墓碑扩 background + schema | PhoneServer | S2 |
| S4 | 网页展示 | alianggate(website) | S1 |
| S5 | 手机 RN 展示 | RN 仓 | S3 |
