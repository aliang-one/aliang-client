# AI run 链路韧性加固 — 设计文档

- 日期：2026-10-27（v3，经三轮 spec 评审修订）
- 状态：设计已获用户确认；自愈语义用户拍板：**判死 + 报错，重试权在用户**
- 范围：客户端仓（本仓）agent 侧 AI run 链路；服务端/手机端仅列出对齐契约，不在本设计内实现

## 1. 背景与问题定性（2026-10-27 事故实证）

用户会话 `ai_aVW2Gpd4uS58`（原生 `agentcore-1793027396121549000-2`，provider=agentcore，项目 vibe_on_phone）：

- runSeq=7 于 03:00:17 启动，正常执行工具调用至 03:09:38（最后一次 AUTO-APPROVE Bash），此后**再无任何内核事件、无终态、无日志**——内核 goroutine 卡死在 `agentcore.Run` 内部，回合永不结束。
- `runAgentCore` 的进度心跳 goroutine（见 §2 洞 1.4）**持续向手机直写 `ai.run.progress`**，服务端因此一直显示「运行中」。
- 会话在 agent 内存中永久停留「运行中」；用户 08:21 发送的消息走 steer 通道进入死回合队列，无人消费——手机零反应、日志零痕迹（No reply）。
- 手机端/服务端自行将对话标记为 Failed；agent 端不知情——**三方状态撕裂**，Retry 会被吞进同一个死队列。
- 同窗口平台 REST 凭据过期（`/api/transfers/inbox` 401 风暴自 03:00:32 起持续 6 小时以上，随 hello/registered_ack 周期反复触发）；WS 因使用另一套凭据仍在线，设备显示「在线」完全掩盖凭据已死；本地零提示、无退避。

定性：**系统性功能缺失**，非单点 bug。链路四层各自把「失败」静默化了。

**事故取证边界**：看门狗三道闸（idle 10min / no-progress 60min / tool-wait 1h）为何全部未产生有效收敛，仅凭现有日志**不可判定**——因为 kill 链路本身零观测（静默 cancel、无日志），且 cancel 对 ctx 盲区（写侧堵死）无效。本设计的 P0 修复同时解决「收敛」与「可诊断」：修复后任何卡死模式都会留下明确日志轨迹。

## 2. 排查结论（洞清单，含代码锚点，均经核实）

### L1 内核传输层（`app/agentcore/`）
| # | 洞 | 锚点 |
|---|---|---|
| 1.1 | SSE 流式读取无「块间空闲超时」：`http.Client` 有意不设整体超时（注释：怕误杀长回合），仅 `ResponseHeaderTimeout: 120s` 兜首字节；对端半开后 Read 永久阻塞 | `client.go:70` |
| 1.2 | 内核事件帧经 `pipeWriter.Write`（`io.Pipe`）进流式管线，写侧不感知 ctx：若堵在写侧，cancel 无法解开，`agentcore.Run` 永不返回——**看门狗 kill 即使触发也无效的候选卡点之一**（另一候选为 1.1 的流读停滞；写侧堵死可解释「kill 已发生但僵尸仍存」，因 kill 本身零日志无法从日志排除） | `agent_ai_agentcore.go:883-886` |
| 1.3 | 客户端**已有**瞬态重试：`maxRetries = 3`、指数退避、尊重 Retry-After，覆盖连接错/429/5xx（初版设计误判为缺失，已更正）。真实缺口有两个：(a) mid-stream 停滞无检测（1.1）；(b) `isRetryable` 对 EOF/超时**默认可重试**（`client.go:281-283`）——新增的块间超时错误若不显式排除，会落入既有重试循环整轮重跑，导致 start/text 等事件帧**重复下发手机** | `client.go:35-40, 154-180, 271-283` |
| 1.4 | 进度心跳旁路：`runAgentCore` 独立 goroutine 每 `agentAIRunProgressInterval` 直写 `ai.run.progress`（不经内核管道、不经流式解析器），`progressStop` 仅在 `runAgentCore` 返回时关闭——僵尸回合期间心跳**永远在流**。它解释了「服务端一直显示运行中」，也会击穿一切基于「事件帧时间」的判死判据（见 §3.3 口径） | `agent_ai_agentcore.go:738-763` |
| — | effort 档位降档重试（唯一有的「回合级」重试，与本设计无冲突） | `loop.go:290-295` |

### L2 回合生命周期（`app/http/services/agent_ai.go` 等）
| # | 洞 | 锚点 |
|---|---|---|
| 2.1 | **kill 链路零观测且可能无效**：看门狗 kill = `setKillReason` + `cancel`，两者均无日志；cancel 后若内核卡在 ctx 盲区（1.2），无任何升级手段，defer 的 `clearRunning`/终态帧永久悬空。工具结果帧本身是**同步回填**的（`executeTools` 后立即 `writer.ToolResults`），「工具帧生命周期缝隙」假设不成立（初版误判，已更正）；工具等待期 `agentAIToolWaitCeiling`（1h）是唯一不暂停的天花板 | `agent_ai.go:1294-1352`；`loop.go:362-366`；`agent_execution_guard.go:140` |
| 2.2 | 终态完全依赖 `agentcore.Run` 返回。现存可复用机制：`agentAIRunEmitter` 已有 per-run 终态闸（`terminal` 锁存，终态后静默丢帧）+ `[ai.run.terminal]` 日志 + `rememberPendingTerminal` 记账——guardian 应构建在其上，而非平行新造 | `agent_ai_agentcore.go:890`；`agent_ai.go:488-490, 949-969` |
| 2.3 | 交互会话硬顶 48h 形同虚设；且代码明文不变量：`agentAIHardCeiling` **必须大于** `agentAIApprovalTimeout`（24h），否则长审批先被硬顶误杀——直接把 48h 降到 2h 违反该不变量，必须改造计时口径 | `agent_execution_guard.go:98-109` |
| 2.4 | 僵尸迟返回的非帧副作用无守卫：`appendAssistantHistory` / journal append / `setAgentAIResumeSessionIDIfEmpty`（`runAgentCore` 收尾段）可能与 Retry 新回合竞争（`clearRunning` 有 runSeq 守卫，这些写没有） | `agent_ai_agentcore.go:936-946` |
| — | 已排除：shell 工具层健康（默认 30s / 上限 10min 超时） | `tools_shell.go:24, 27` |

### L3 消息反馈与状态收敛
| # | 洞 | 锚点 |
|---|---|---|
| 3.1 | steer 入队即回 `queued`，无活性窗、无死信：回合死掉后插话被无声吞没；收尾追补 `GetFollowUpMessages` 与 steer 同队列，活性窗设计需一并覆盖 | `agent_ai_agentcore.go:1023`；`loop.go:313-318` |
| 3.2 | 手机 Failed 为服务端/手机端单方判定，agent 端不收敛，Retry 重复入死队列 | 跨仓契约 |
| 3.3 | 「事件帧时间」判据必须排除 1.4 的进度心跳，否则一切判死永不触发 | 见 §3.3 口径 |

### L4 平台通道健康
| # | 洞 | 锚点 |
|---|---|---|
| 4.1 | REST 调用（`callAgentServer`）凭据过期后无人续期、无退避、无提示；WS 与 REST 凭据分叉（WS 活、REST 401）导致设备「假在线」。401 触发节奏随 hello/registered_ack 周期（被 vibe hello 风暴放大），非独立定时器——退避必须挂在 `callAgentServer` 公共层而非某个调用方 | `agent_service.go:1812`；`agent_transfer_inbox.go:44, 48, 74` |

## 3. 设计

### 3.0 总则
- 自愈语义（用户已拍板）：**判死 + 报错，重试权在用户**。任何判定路径不自动重跑回合。
- 判死 = 有界时间检测到无进展 → 向手机发出**含原因的终态错误事件** → 会话回到 idle → 用户 Retry（= 重发消息 = 新 runSeq `--resume` 原生会话，上下文不丢；agent 侧现有机制已支持，无需新端点）。
- 终态唯一性：一个 run 无论从哪条路径退出，**恰好发出一个终态事件**——guardian 构建在现有 `agentAIRunEmitter` 终态闸之上（终态锁存 + `[ai.run.terminal]` 记账），不平行新造。

### 3.1 L1 内核传输层
1. **块间空闲超时** `streamIdleTimeout`（默认 90s，env `ALIANG_AGENTCORE_STREAM_IDLE_TIMEOUT` 可覆盖，0=禁用仅测试用）：SSE 读循环每次阻塞读包一层 deadline（timer + ctx select 或连接级 `SetReadDeadline`）；窗口内收不到任何字节（含 ping、事件）即判定「上游流停滞」，中断读取并返回**专属错误类型**。依据：上游协议有 ping 心跳，正常长回合不会静默 90s；真机实测后校准默认值。
2. **与既有重试的交互（关键）**：块间超时错误必须**标记为不可重试**（或重试门控加「本尝试尚未向宿主发出任何事件帧」——两者取前者，语义更简单），防止既有 `maxRetries=3` 循环整轮重跑造成事件帧重复下发。既有建流期瞬态重试保持不变。
3. **Emit 可取消**：`pipeWriter.Write` 改造为可被 run ctx 解除（写侧 goroutine select `{写完成, ctx.Done→Close pipe}` 解阻塞，或换带缓冲 channel + 独立 flush goroutine）。保证堵在写侧时 cancel 能解开、`Run` 能返回。

### 3.2 L2 回合生命周期
1. **kill 可观测**：看门狗每次触发必须 `logger.Warn`（kill 原因、session、runSeq、已运行时长、当前 activity 状态快照）。
2. **强拆升级**：cancel 后启动 grace（`agentAIKillGrace` 默认 30s）；grace 内 run 未返回（per-run done channel 探测）→ 强制收尾：经 run emitter 发终态错误帧 + 会话复位，日志记「强拆」。僵尸后续帧由 emitter 终态锁存静默丢弃；僵尸的**非帧副作用**（history/journal/resume 写）一律加 runSeq 守卫，落后于当前 runSeq 即丢弃。
3. **终态保证 guardian**：`runAgentCore` 正常完成/错误/panic recover/强拆，任何路径恰好一个终态；实现复用 emitter 终态闸与 `[ai.run.terminal]` 记账。
4. **硬顶口径改造**：`agentAIHardCeiling` 语义改为「**引擎活跃时长**上限」——累计计时排除三种合法等待态（审批等待、工具等待、子代理等待），默认降到 **2h**（env 可覆盖）。可计算性：看门狗 ticker（`agentAIIdleCheckInterval`）逐拍采样，处于任一等待态（`awaiting()` / `pendingToolUses>0` / `activeSubagents>0`）的拍不计入引擎时钟；等待态各自的既有天花板继续独立生效（审批 `agentAIApprovalTimeout` 24h、工具 `agentAIToolWaitCeiling` 1h、子代理 `agentAISubagentWaitCeiling` 6h）——**每种状态都有自己的有界上限，硬顶只管真实干活时长**。原代码注释「硬顶必须 > 审批超时」的不变量随口径更新改写（等待态已不计入，原比较对象失去意义）；`agentAISubagentWaitCeiling < 硬顶` 的大小关系约束同步复核改写（3h 子代理批处理由 6h 子代理天花板管辖，不再被硬顶截断）。`agentAIIdleWindow` 10min、`agentAINoProgressWindow` 60min 本轮不动。

### 3.3 L3 消息反馈与状态收敛
1. **活性事件口径**：定义 per-run `lastActivityAt`，run 启动时播种（`ai.run.started` 计入，覆盖 MCP 工具枚举等启动期静默）；此后**只**由以下帧刷新：内核事件帧（ai.delta / ai.thinking / ai.command / 工具生命周期帧 / 子代理 task_progress 等经流式管线的一切，开放式枚举）、审批请求/决策帧、steer applied ack。**明确排除** `ai.run.progress` 心跳（1.4）。
2. **等待态豁免（有时限的豁免）**：活性判死的豁免镜像 `idlePaused()` 三个成分，且每个成分由既有天花板约束——审批等待 ≤ `agentAIApprovalTimeout`（24h）、工具等待 ≤ `agentAIToolWaitCeiling`（1h）、子代理等待 ≤ `agentAISubagentWaitCeiling`（6h）。即等待态不判「无响应死」（合法的 5-10min 工具调用、长审批、长子代理批处理都不误杀），由各自天花板收敛；**非等待态**才适用 3.3.2 的 stale 阈值。
3. **stale running 自检（P0）**：agent 侧每分钟扫描运行中会话：非等待态且 `lastActivityAt` 距今 > `staleRunThreshold`（默认 5min）→ 判死；等待态不按 stale 阈值判死，超各自天花板后判死（正常由组件天花板/watchdog 先行触发，scanner 是兜底）。scanner 不自己发终态——通过 manager 注册的 **per-run 强制收尾回调**唤起 guardian（与看门狗强拆同一条通道），终态唯一性仍由 guardian 终态闸保证。这是兜住一切未知卡点的最终兜底；本次事故形态（非等待态停滞）的收敛上界从 6 小时无界压缩到 ≤5min+grace。
4. **steer 活性窗**：`deliver` 排队成功后绑定 run 的 `lastActivityAt`；超过 `steerLivenessWindow`（默认 5min，**与 stale 阈值对齐、不得短于它**：agentcore 传输层重试链最长 3 次 × ResponseHeaderTimeout 120s + 退避，全程不 bump 活性，最坏合法静默约 6min；窗口短于扫描器就会抢在它前面误杀本会自愈的回合。对齐后本窗是扫描器判据的严格子集——多一条「排队时长 ≥ 窗」，只会更晚、不会更早判死）未 applied **且 run 处于非等待态** → 手机收到 steer 失败反馈（语义码 `turn_stalled`，文案「回合无响应已判死，请重试」），并触发 3.3.3 判死。run 处于合法等待态（工具/子代理/审批执行中）时不误报——插话继续排队，由下一次 LLM 调用前的 drain 取走；等待态僵尸由各自天花板+scanner 收敛。`GetFollowUpMessages` 收尾追补与 steer 同队列，一并纳入该窗口。
5. **状态收敛契约**：agent 判死必发终态事件 → 服务端/手机 sweeper 与 agent 终态对齐；Retry 语义如 §3.0。服务端 sweeper、手机 Retry UI 已存在，跨仓仅需对齐口径（P3）。

### 3.4 L4 平台通道健康
1. **401 刷新重试**：`callAgentServer` 公共层收到 401 → **本地**刷新 token（`ensureFreshRegisterAuthHeaderForPush` → `auth.EnsureFreshAccessTokenForPush`）后重试一次；仍失败对该 endpoint 按指数退避调度。刻意不走 `RequestUserAgentSyncAfterAuth`（那条路自身经 `callAgentServer` 注册路径，同步调用会递归）。**边界**：本地刷新只在 token *临近过期* 时才真换新头；服务端提前吊销但未过期时返回同一个头，重试会再吃一个 401，最终由退避 + 告警收敛——用户可感知的结果是「收到告警」，不是「自动恢复」。两条配套约束：**(a)** 重试的第二次失败若不是 401（网络/5xx），按原错误向上返回，**不**记入 401 退避与告警——否则会把网络抖动误判成凭据问题，告警内容也会误导；**(b)** 凭据轮转（重新登录/续期后 Authorization 头与触发退避时不同）立即作废该端点退避（按头的短指纹比对）——否则告警引导用户重新登录后，注册仍会被残余退避挡住最多一个窗口。退避必须挂在公共层而非某个调用方：401 触发节奏随 hello/registered_ack 周期（被 vibe hello 风暴放大），inbox 拉取是已知一路但非唯一一路。
2. **限频告警**：401 连续 ≥3 次或持续 >5min → 发设备级告警事件到手机（限频每 30min 一条），文案如实描述「设备在线但平台凭据过期，文件传输等功能暂不可用」。告警发送通道是包级 `var`，**必须用 `init()` 赋值**：`agentTransferFetchInbox` 等是包级 `func var`，声明处初始化会让 Go 的初始化依赖分析经 `GetSharedAgentService → … → callAgentServer → record401` 闭环，报 `initialization cycle` 编译错误。

## 4. 参数汇总

| 参数 | 默认 | env 覆盖 | 备注 |
|---|---|---|---|
| streamIdleTimeout | 90s | `ALIANG_AGENTCORE_STREAM_IDLE_TIMEOUT` | 块间无任何字节即触发；错误不可重试 |
| staleRunThreshold | 5min | `ALIANG_AI_STALE_RUN` | scanner 周期 1min；豁免镜像 idlePaused 三成分、各由既有天花板约束（见 §3.3.2） |
| steerLivenessWindow | 5min | `ALIANG_AGENTCORE_STEER_LIVENESS` | **与 staleRunThreshold 对齐、不得短于它**：传输层重试链（120s×最多 3 次 + 退避，全程不 bump）最坏合法静默约 6min，窗口更短会抢在扫描器前误杀本会自愈的回合；对齐后本窗是扫描器判据的严格子集 |
| agentAIKillGrace | 30s | `ALIANG_AI_KILL_GRACE` | cancel 后未返回即强拆 |
| agentAIHardCeiling | 2h（原 48h） | `ALIANG_AI_HARD_CEILING`（已有） | **口径改为引擎活跃时长（排除审批/工具/子代理三种等待态）** |
| agentAIApprovalTimeout | 24h（不变） | `ALIANG_AI_APPROVAL_TIMEOUT`（已有） | 审批等待态天花板 |
| agentAIToolWaitCeiling | 1h（不变） | 已有常量 | 工具等待态天花板 |
| agentAISubagentWaitCeiling | 6h（不变） | 已有常量 | 子代理等待态天花板（与硬顶的大小关系约束随口径改写） |
| 401 告警限频 | 30min | 常量即可 | |

## 5. 测试与验证

单测（全部新增，遵循现有 services 包测试惯例）：
1. 半开流注入（服务端挂起不关连接）→ 块间超时触发、错误终态、会话回 idle；
2. 块间超时错误**不触发**既有重试（无重复事件帧下发）；
3. Emit 写侧堵死注入 → ctx 解开、Run 返回、guardian 发终态；
4. cancel 后 grace 强拆 → 终态恰好一个、emitter 终态闸丢弃僵尸后续帧、僵尸非帧副作用被 runSeq 守卫丢弃；
5. **仅进度心跳流动时**（心跳正常、无内核事件）stale scanner 仍判死——防心跳击穿；
6. 审批等待 3h（> 硬顶 2h）**不被硬顶误杀**；审批等待超 24h 被 scanner 判死；
7. steer 入死队列 → 活性窗超时降级反馈 `turn_stalled`；follow-ups 同窗口覆盖；
8. 401 → 刷新重试一次 → 退避 → 限频告警各一例；
9. 健康的 7 分钟工具调用回合**不被 stale 误杀**（工具等待态豁免生效）；
10. 工具执行中插话 → 不触发 `turn_stalled`，插话在下一轮流前被 drain；
11. 审批等待中插话 → 不触发判死反馈（等待态豁免与 steer 窗口不矛盾）；
12. 子代理批处理 3h 不被 2h 引擎硬顶误杀（等待态不计入引擎时钟）；纯引擎活跃 2h 被硬顶正常收敛。

回归：agentcore run/steer/journal/MCP 既有测试全绿（注意预存 flake 清单，勿当回归）。
真机冒烟：正常回合；人为断上游 90s 内手机收到错误；Retry 续上下文；持续 401 场景收到限频告警。

## 6. 明确不做（YAGNI）

- 内核子进程化（远期备选，故障隔离更彻底但需重构审批桥/管线/MCP 注入）。
- 自动重试回合（用户已拍板）。
- 手机端/服务端 UI 改动（跨仓另行）。
- `agentAINoProgressWindow` 收紧（本轮不动，待新观测数据支持后另行）。

## 7. 分期

- **P0 止血（僵尸回合闭环）**：L1 全部 + L2 全部 + §3.3.1 活性口径（纯数据结构，无行为改动）+ §3.3.2 等待态豁免（随 stale 自检同批落地）+ §3.3.3 stale running 自检。收敛上界按卡死模式分列（「模式 → 上界」表，替代单一公式）：

  | 卡死模式 | 收敛上界 |
  |---|---|
  | 非等待态停滞（流停滞/写侧堵死，**预期最常见形态**） | ≤ max(空闲窗口 10min, stale 5min) + killGrace 30s ≈ **10.5min** |
  | 工具等待态僵尸 | toolWaitCeiling 1h + grace |
  | 子代理等待态僵尸 | subagentWaitCeiling 6h + grace |
  | 审批等待态僵尸 | approvalTimeout 24h（审批 ctx 超时先行，scanner 兜底） |

  收益陈述：本次 6 小时无界僵尸 → 每种模式都有确定上界；最常见的非等待态停滞 ≤10.5min。kill 链路全程可观测。
- **P1 反馈闭环**：§3.3.4/§3.3.5（steer 活性窗含 follow-ups 覆盖）、§3.3 末条状态收敛契约 —— 吞消息变为可见错误。
- **P2 通道健康**：L4 全部 —— 401 有定位、有续期、有退避、有告警。
- **P3 跨仓对齐**：服务端 sweeper 口径、Retry 契约验证（真机）。
