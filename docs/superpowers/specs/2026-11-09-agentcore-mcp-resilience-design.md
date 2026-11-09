# agentcore MCP 健壮性包设计:检活重连 + tool 级 include/exclude 过滤

- 日期:2026-11-09
- 状态:已批准(设计对话定稿,待实施)
- 上游设计:docs/superpowers/specs/2026-10-26-agentcore-mcp-design.md(v1 MCP 客户端)
- 借鉴出处:agno MCPTools `refresh_connection` 与 `_check_tools_filters`(agno-agi/agno,libs/agno/agno/tools/mcp/mcp.py),经两轮调研核验为本仓真实缺口

## 0. 背景与动机

agentcore 内核的 MCP 会话是 **per-run 一次性拨号**:`agentCoreMCPServerToolDefs` 在 run 启动时并发 dial 全部 server,run 结束经 `go closeMCP()` 关停。run 中途 server 死亡(.stdio 子进程崩溃 / http 端断连)时,该 server 全部工具**永久失效**——桥的 `Execute` 只把错误文本返回给模型(`agent_ai_agentcore_mcp.go:173-177`),无探活、无重连。

这与 tool 级 `tool_wait_timeout` 的 1h+ 墙钟直接矛盾:长跑 Goal run 里 stdio server 崩溃后,模型反复调用只会反复收到「MCP 调用失败」,没有任何自愈路径。

工具级过滤同样缺失:现只有 server 级 `Disabled`(配置读通道跳过)与会话级 `disabledMCPServers`(dial 前整 server 剔除)。想关掉某 server 的个别工具(如危险的写操作工具)做不到;且未来加过滤时若名单写错工具名,会**静默**不生效——配置错误被吞。

## 1. 目标与非目标

**目标**
1. run 内 MCP 工具调用失败(transport 层)时自动探活重连并单次重试,死 server 可恢复;
2. 连续重连失败有判死上限,防拨号风暴;
3. `include_tools`/`exclude_tools` 配置字段贯通:注册表 API 可写、落盘可往返、消费侧生效;
4. 名单引用不存在的工具名时 fail loud(include)或显式警告(exclude),消灭静默配置错误。

**非目标(v1)**
- 仪表盘 UI(字段仅文件+API;UI 后续批次);
- 主动 ping/周期检活(会话 per-run 全新,起点必活;只做反应式重连);
- journal 头记录过滤结果(仅 logger);
- claude/codex 内核的 MCP 过滤语义(本设计是 agentcore 消费侧专属);
- HTTP transport 的 Headers 注入(沿 v1 限制,带 Headers 的条目仍被预筛跳过,不参与重连)。

## 2. 现状关键事实(实施前提,均已核实)

| 事实 | 位置 |
|---|---|
| 会话 per-run 一次性拨号,run 结束 `go closeMCP()` | app/http/services/agent_ai_agentcore.go:1363,1377 |
| 桥直持 `b.session`,CallTool 失败只回错误文本 | app/http/services/agent_ai_agentcore_mcp.go:148-184 |
| dial 超时外部施加,迟到 dial 由 detached goroutine 收尸 | agent_ai_agentcore_mcp.go:338-370 |
| server 级 Disabled 读通道跳过 | agent_ai_agentcore_mcp.go:127-129 |
| 会话级 disabledMCPServers 在 dial 前过滤 | app/http/services/agent_ai_agentcore.go:1349-1362 |
| 测试 dial 注入钩子 `coreMCPDialForTest` | agent_ai_agentcore_mcp.go:420-423 |
| 注册表条目结构无 include/exclude;`aliangMCPEntryToMap` 逐字段显式序列化 | app/http/services/agent_mcp_registry.go:51-59,213 |
| 注册表 root 加载保留未知字段 | agent_mcp_registry.go:68-70 |
| 内核每 run 串行执行工具;桥 per-run 不复用 | agent_ai_agentcore.go:1370-1378 注释 |
| 工具名上限 128(cap 截断在装配循环) | agent_ai_agentcore_mcp.go:492-495 |

## 3. 设计总览

两块独立增量,同一文件群落地:

1. **检活重连(会话持有者)**:`agentCoreMCPSessionHolder` 承接会话生命周期,同 server 全部桥共享;transport 层失败触发「关旧→重拨→换新→单次重试」,连续失败判死。
2. **tool 级过滤**:配置字段贯通注册表管理面与消费面;ListTools 后、cap 与命名前过滤;include 未知名硬失败,exclude 未知名软警告。

## 4. 检活重连(会话持有者)

### 4.1 结构

```go
// 同 server 全部桥共享的会话持有者:锁内换会话,closeAll 永远关到当前会话。
type agentCoreMCPSessionHolder struct {
    mu         sync.Mutex
    session    *mcp.ClientSession
    closeFn    func() // 关当前会话(closeFn 内含 cancel,沿阶段一成功路径语义)
    raw        agentCoreMCPRawServer
    dial       func(context.Context, agentCoreMCPRawServer) (*mcp.ClientSession, func(), error)
    consecFail int    // 连续「重连后仍失败」计数
    dead       bool   // run 内判死:后续调用快速失败,不再拨号
}
```

- 桥改造:`agentCoreMCPBridge.session *mcp.ClientSession` → `holder *agentCoreMCPSessionHolder`。
- holder 的 dial 与测试注入同源:构造时取「`coreMCPDialForTest` 优先,否则 `agentCoreMCPDial`」的同一 resolved 值(阶段一现有逻辑),保证测试能驱动重连路径。
- 阶段一成功路径的 `closeFn`(内含 cancel)移入 holder;closeAll 改为遍历 holder 调 `holder.Close()`(锁内关当前会话)。**换会话后 closeAll 关到的是新会话,旧会话已在换时关闭——零泄漏。**

### 4.2 失败分类(重连触发判据)

| 情形 | 语义 | 是否重连 |
|---|---|---|
| `CallTool` 返回 `err != nil` | transport/协议层失败(连接死、超时、协议错) | **是** |
| `CallTool` 成功返回但 `IsError=true` | 工具逻辑报错,协议链路健康 | 否,原样返回 |
| panic | 沿现有 recover 兜底 | 否(panic 非连接病) |

### 4.3 重连流程(holder.call,锁内)

1. `session.CallTool(...)`;成功 → `consecFail=0`,返回。
2. 失败且 `dead` → 快速失败,文本见 §6。
3. 失败且未判死 → **重连**:
   a. 关旧会话(closeFn);
   b. `agentCoreMCPDialWithTimeout(dial, ctx, raw, agentCoreMCPInitTimeout)` 重拨——ctx 从 run ctx **派生带 cancel 的子 ctx**(沿阶段一成功路径语义:session ctx 只带 cancel 不带 deadline,deadline 由 agentCoreMCPDialWithTimeout 外部施加),新 closeFn 一并 cancel 子 ctx,保住 holder 的主动收尸能力;
   c. 重拨失败 → `consecFail++`;≥ `agentCoreMCPReconnectDeadCap`(常量,=3)→ `dead=true`;**`holder.session` 保持为已关闭的旧会话,不得置 nil**——判死快速失败路径与拨号计数断言(测试 2:初始 1+重连 3=4 次)依赖此语义;返回「(已自动重连)」错误文本;
   d. 重拨成功 → 换 `session`/`closeFn` → **重试 CallTool 一次**;成功 → `consecFail=0` 返回结果;仍失败 → 同 c 的记账与返回。

**嵌入决策(已批准)**:判死阈值 K=3;重连只认 transport 层错误。判死与计数随 run 结束( holder 随 closeAll 释放)自然清零,下一 run 全新开始。

### 4.4 并发与安全红线

- 内核每 run 串行执行工具;holder 锁是纵深防御(子 agent/future 并发),不承担正确性主责。
- 日志只打 `sanitizeAgentCoreMCPSegment(server)` 段;raw.Env/Headers 值永不进日志/错误文本(v1 红线沿用)。CallTool 的 `err.Error()` 进工具结果文本是既有行为,不扩大。

## 5. tool 级 include/exclude 过滤

### 5.1 字段与副本同步清单(逐一列死,实施时逐项验证)

| # | 位置 | 改动 |
|---|---|---|
| 1 | `agentCoreMCPRawEntry`(消费解析) | +`IncludeTools []string \`json:"include_tools,omitempty"\`` +`ExcludeTools []string \`json:"exclude_tools,omitempty"\`` |
| 2 | `agentCoreMCPRawServer`(带入 dial 阶段) | 同两字段透传 |
| 3 | `aliangMCPServerEntry`(注册表管理面) | 同两字段 |
| 4 | `aliangMCPEntryToMap` + 反向解析(结构体↔map) | 显式落盘/读取(仅非空字段落盘,沿现有口径) |
| 5 | `validateAliangMCPEntry` | trim/dedup/单列表上限 64 条/条目非空字符串校验 |
| 6 | claude 内核路径(`agent_ai_claude_policy.go`) | **显式剔除两键**。事实前提:`readAliangMCPRegistryInto`(`agent_mcp_registry.go:119-184`)是通用 map 逐键透传(仅剥 `enabled`,其余全保留),`claudeTierMCPArgs` 将合并 map 整包 `json.Marshal` 进 `--mcp-config` 临时文件(`agent_ai_claude_policy.go:210,224`)——注册表写入的新字段**必然逐字进入 claude 载荷**。剔除落点=claude 侧合并产物处剥 `include_tools`/`exclude_tools`;**硬约束:不得剔在 `readAliangMCPRegistryInto` 本体**——agentcore 消费侧(`agentCoreMCPRawServersIn`)走同一合并函数,剔了整个特性失效。回归测试锁死 |

### 5.2 过滤语义(ListTools 之后、cap 与命名之前)

- `include_tools` 非空 → 白名单交集:保留名单内的工具;
  - **名单引用了工具清单中不存在的名字 → 整 server 判失败跳过**(走现有 `fail(server, err)` 语义:Warn+skipped++,会话就地关闭)——把配置错误从「静默丢工具」变成「显式报错」;
  - **计数互斥**:过滤校验放在 `connected++` **之前**——判失败的 server 只进 skipped,不进 connected,杜绝「已连接 N(跳过 K)」同 server 双计;
- `exclude_tools` → 黑名单剔除;
  - **未知名仅 Warn**(剔除尚不存在的工具可能是有意预留,不判失败);
- 过滤发生在 128 cap 计数之前:被过滤的工具不占预算;
- 汇总日志:`server=X 工具过滤 保留 N/M(include=%d exclude=%d)`(server 用 sanitize 段)。

### 5.3 校验规则(validateAliangMCPEntry)

- **写通道**(validateAliangMCPEntry,fail-closed):逐项 TrimSpace,去空串,dedup(保首现顺序);**单列表超 64 条 → 拒绝**(返回 error,不截断);
- **读通道**(消费侧,手改注册表文件兜底):过滤执行前对名单做同一 trim/去空串/dedup 归一(沿 type 归一先例「读侧兜住手改文件」);读通道不做条数上限(那是写通道纪律),归一后照常过滤;
- 不校验名字格式(工具名合法性由 server 侧决定,预校验在 dial 期做真实存在性校验,见 §5.2);
- include 与 exclude 同时列同一工具:exclude 胜(先白名单交集后黑名单剔除,自然得出);文档注明该语义。

## 6. 错误处理与文案

| 场景 | 文本(进模型/journal) |
|---|---|
| 重连后重试仍失败 | `MCP 调用失败(已自动重连): <err>` |
| 判死后调用 | `MCP server 已在本次运行内判死(连续 %d 次重连失败),本 run 内请勿继续调用该 server 工具`——次数从常量 `agentCoreMCPReconnectDeadCap` 取值拼接,不硬编码 |
| include 未知名(server 整体跳过) | logger Warn:`server=X include_tools 引用不存在的工具: [a,b]`(模型侧沿现有「server 跳过」行为:该 server 工具不存在) |
| exclude 未知名 | logger Warn:`server=X exclude_tools 未匹配任何工具: [a]`,其余照常 |

## 7. 测试计划(复用 `coreMCPDialForTest` 注入,文件 agent_ai_agentcore_mcp_test.go)

1. **重连成功**:fake dial 第一次返回的 session CallTool 返回 error → 自动重拨 → 第二个 session 成功;断言:返回成功结果、旧 session 已 Close、consecFail 清零;
2. **判死**:fake dial 恒败 → 第 1-3 次调用各触发一次重拨 → 第 4 次调用不再 dial(记录 dial 调用次数断言=初始 1+重连 3);
3. **IsError 不重连**:session 返回 `IsError=true` 结果 → 无重拨;
4. **include 白名单**:3 工具 include 2 → 桥表只含 2;include 引用不存在的名字 → 该 server 全部工具缺席 + fail 日志;
5. **exclude 黑名单**:剔除生效;未知名仅警告不判失败;
6. **cap 交互**:过滤后数量才计入 128 cap(先构造超 cap 场景);
7. **closeAll 换会话**:重连后触发 closeAll → 新会话被关、旧会话在换时已关;
8. **注册表往返**:API 写 include/exclude → 落盘读回一致;空串/重复被 normalize(清洗落盘),超 64 条被拒绝(fail-closed error);
9. **claude 隔离回归**:注册表条目带 include/exclude → claudeTierMCPArgs 产物不含新字段;
10. **读通道归一**:手改注册表文件写入带空白/重复/空串项的名单(如 `" foo "`)→ 消费侧归一后过滤生效(覆盖 §5.3 读通道语义,测试 4/5 只覆盖已清洗名单)。

## 8. 实施影响面

| 文件 | 改动 |
|---|---|
| app/http/services/agent_ai_agentcore_mcp.go | holder 结构、桥改造、重连流程、过滤逻辑(主战场) |
| app/http/services/agent_mcp_registry.go | 条目字段+序列化+校验 |
| app/http/services/agent_ai_claude_policy.go | claude 侧合并产物剔除 include/exclude 两键(§5.1 条目 6,硬约束:不动 readAliangMCPRegistryInto) |
| app/http/services/agent_ai_agentcore_mcp_test.go | 测试 |
| app/http/services/agent_mcp_registry_test.go / agent_ai_claude_policy_mcp_registry_test.go | 往返与隔离回归 |
| app/http/handlers/agent_mcp_registry_handler_test.go(已存在) | 测试 8 若走 handler 层落此处(计划阶段择位) |

不改:agent_ai_agentcore.go 的调用点(签名不变)、清单层、仪表盘。

## 9. 借鉴对照(agno → 本仓)

| agno 机制 | 本仓落地 |
|---|---|
| `refresh_connection`:每次工具调用 `is_alive()` → 死则 `connect(force=True)` 重连 → `build_tools()` 重列;失败仅 warning 跳过该工具 | 反应式重连(我们 per-run 全新会话,起点必活,无需主动 ping);失败语义同为「不炸 run」 |
| 连接生命周期绑到调用生命周期 | 简化为 run 内反应式:死链在下次调用时自愈 |
| `_check_tools_filters`:名单引用不存在工具 → 连接期快速失败 | include 硬失败 / exclude 软警告(二分是我们按「预留」语义做的适配) |
| `_MCP_TOOL_PAGINATION_MAX_PAGES` 分页防死循环 | 已有等价(`listAgentCoreMCPTools` nextCursor 分页) |
| `tool_name_prefix` 防撞名 | 已有更强等价(`mcp__<server>__<tool>` 构造保证,不引入) |
