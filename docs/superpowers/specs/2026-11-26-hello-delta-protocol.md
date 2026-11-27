# spec:hello 增量协议(agent→server 同步面根治)

日期:2026-11-26 · 状态:待拍板 · 前置批次:15 天时间窗(a96a3ff2)、vibe 对称双闸(PS 634f319)

## 1. 问题与现状

窗口化后,agent→server 的 WS hello 载荷 = **固定件 + vibe_sessions**:

| 部分 | 内容 | 现量 | 频率 |
|---|---|---|---|
| 固定件 | tools/agent_tools/subagents/**projects**/history≤64/load/system/net | **~300KB** | 每 hello |
| vibe_sessions | 143 条×~800B | ~115KB | 每 hello |
| 触发 | periodic 60s 恒发 + digest 变更 30s 闸 | — | 60-140 次/h |

稳态 ~25-60MB/h 出向,**其中固定件占 ~72% 且 99% 的 hello 与上一帧逐字节相同**。
窗口治的是「推什么」,本 spec 治「推多频繁、推多大」——同步面最后一块根。

## 2. 目标

- 稳态 hello 从「全量快照 ~415KB」降到「**变更增量,典型 <2KB**」;
- 连接建立/服务端重启/agent 重启后的**首帧仍为全量**(服务端权威重建语义不变);
- 服务端存储、手机端 API、WS 下发帧**全部不变**(只动 agent→server 上行);
- 旧服务端 + 新 agent 必须照常工作(向后兼容);旧 agent + 新服务端照常(向前兼容)。

## 3. 协议设计

### 3.1 帧形态

```
hello = {
  ...既有固定件,           // 语义不变
  sync_mode: "full" | "delta",          // 新字段,zod strip 兼容旧服务端
  sections: {                            // delta 时只带变化段
    tools_fingerprint: "sha1:...",       // 未变段只发指纹
    projects: [...]|nil,                 // 变了才发,数组语义=全量替换(沿用现有 upsert)
    session_changes: [ {...完整会话条目...} ],   // 只发变化的会话
    session_removed_ids: ["codex_xxx"],  // 窗口出清的墓碑
  },
}
```

- `sync_mode=full`:既有载荷原样(sync_mode 缺省=full,旧 agent 天然兼容)。
- `sync_mode=delta`:固定件里**只发指纹不发内容**的服务段置 nil;服务端对 nil 段
  **不清空**(与现状「不带=不动」语义一致);session_changes 走既有
  upsertReportedVibeSessions(幂等 upsert,无需新写侧);session_removed_ids 走
  显式 tombstone(现状没有删除语义——这是唯一新增的服务端写面)。

### 3.2 agent 侧变更节流(复用既有 digest 骨架)

- 既有 `agentVibeDigestInterval`(10s tick + 30s 闸)已对 vibe 面做指纹;扩到
  tools/projects/history 三段(各一段 sha1);
- 指纹无变化 → periodic 60s 的 hello **降级为心跳帧**(sync_mode=heartbeat,
  只带时间戳,~200B);
- 任一段指纹变化 → 立即 delta 帧(受既有 30s 闸);
- **全量帧时机**:(a) WS (重)连首帧;(b) 收到 server_requested(服务端重启后点名,
  既有机制);(c) agent 本地快照缓存失效(冷扫描完成后);(d) delta 连续 3 次后
  周期性回全量一次(自愈锚点,防指纹漂移)。

### 3.3 服务端变更(PhoneServer)

- `agentStatusSyncSchema` 增加 sync_mode/session_removed_ids 字段(zod strip 兼容);
- `upsertReportedVibeSessions` 增加 tombstone 分支:removed_ids 按 (deviceId, native id)
  查 ai_import_* 行,未被手机绑定且非 running 的才删(保守删,有绑定/在跑跳过+记日志);
- 固定件 nil 段语义固定为「不动」并落注释(现状事实上是这个语义,补契约钉死);
- **projects 段**:delta 时 projects 数组仍全量(现状 upsert 即全量替换语义,先不改,
  项目数窗口化后 ~15 条≈10KB,不值得为其做 delta)。

## 4. 风险与对策

| 风险 | 对策 |
|---|---|
| 指纹漂移(哈希碰撞/agent 端漏算段) | (d) 周期回全量自愈锚点;服务端校验 sections 指纹与内容不符时回 server_requested |
| tombstone 误删手机绑定会话 | 删除前查 phoneSessionOwnsCliSource + runState,有主/在跑一律跳过 |
| 双端版本错配 | sync_mode 缺省=full:新 agent→旧服务端 = 现状;旧 agent→新服务端 = 现状;无协商需求 |
| 首帧风暴 | 不变(现状已有 helloDetailRefreshGate 60s 冷却闸) |
| 测试 flake 家族 | services 包预存 flake 基线在案;协议测试全部走 hermetic fixture+相对时间 |

## 5. 预期收益(本机画像)

- 稳态(无变更,60s 心跳帧):415KB → **~0.2KB/hello**(-99.9%);
- 典型工作态(每分钟 1-2 条会话变更):415KB → **~2-4KB/hello**;
- 出向流量:~25-60MB/h → **<2MB/h**;服务端 safeParse 从 O(全量) 降到 O(变更);
- helloDetailRefresh 突发、DB flush 批量同步受益。

## 6. 实施切分(供排期)

- P1 agent 侧:指纹扩展+heartbeat 降级+delta 组帧(~1 文件族,TDD);
- P2 服务端:字段+tombstone(保守删)+契约测试;
- P3 联调:双端混布矩阵(新/旧 × 新/旧)+真机流量观测;
- 每步独立可回滚(sync_mode 关掉即回全量)。

## 7. 待拍板

1. 本 spec 是否批准(批准后按 P1→P3 逐批走,每批独立提交)?
2. tombstone 删除面:同意「保守删(有绑定/在跑跳过)」语义吗?
3. 顺带:服务端存量 661+ 行 >30d 旧数据,要不要配一条 TTL 清理(与窗口语义对齐)?
   ——删用户数据,需明确点头。
