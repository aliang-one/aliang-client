# T3 Code 借鉴 SYNC 批次交付记录与真机验收清单

日期：2026-11-28
需求来源：[需求清单](../specs/2026-11-25-t3code-borrow-requirements.md) §3 SYNC

## 1. 本批落地范围

本批把 SYNC-01 至 SYNC-16 中标记为本轮实施的项目全部落到三个仓库，
主分支已合并并推送。下表是每条需求在本批的实际落点，「已落地」只表示
代码与对应回归已在主分支，指标是否达标以 §3 的真机验收为准。

| 需求 | 本批落点 | 状态 |
|---|---|---|
| SYNC-01 分段耗时可见 | PhoneServer 归档网页发起回合的时序来源；仪表盘时序按来源分组；未测得与真实零值分开 | 已落地 |
| SYNC-03 回前台立刻接上 | 手机进页即拉最新，已缓存内容先行可读；前台心跳 3 秒，两次无应答识别断线 | 已落地 |
| SYNC-04 连接失效尽快识别 | 手机前台持续在线时约 9～10 秒识别断线；网页有界探活恢复；agent 远程 WS 探测窗口 90 秒缩至 15 秒 | 已落地 |
| SYNC-05 恢复时审批卡与回合状态正确 | 连接恢复后补齐审批与回合状态 | 已落地 |
| SYNC-06 电脑开跑手机从头接上 | 长回合续订与 agent 重连恢复 | 已落地 |
| SYNC-07 发送结果可见且重试只开跑一次 | 发送幂等；回包丢失显示「发送结果待确认」；审批、语音、失败气泡重试均有反馈 | 已落地 |
| SYNC-09 改名多端一致 | 网页改名写入服务端，手机与其他浏览器同步 | 已落地 |
| SYNC-12 TUI 注入及时可见回显 | 注入后观测到本轮回显即放行；转录读取只消费完整行 | 已落地 |
| SYNC-13 网页持续看长回答 | 网页半开连接探活；重试预算耗尽不再把运行中回合画成中断 | 已落地 |
| SYNC-15 TUI 与网页内容快而完整到手机 | TUI 发现即上报；长视图 40 分钟续订；仪表盘渠道归因 | 已落地 |
| SYNC-16 长工具过程可见 | 工具开始即显示名称与计时；长命令复用任务卡并报告输出计数；固定词表显示活动阶段 | 已落地 |
| SYNC-08 通知范围 | 维持现状，本批不动 | 无新增 |
| SYNC-11 休眠区分 | 按过审决定延后 | 延后 |
| SYNC-02 流式上屏延迟、SYNC-10（移入 DEBT-05）、SYNC-14 弱网持续显示 | 本批未单独处理 | 见需求清单 |

## 2. 版本与部署状态

| 仓库 | 分支 | 提交 | 远端 |
|---|---|---|---|
| alianggate（Go agent + 网页） | master | `cdf44a24` | 已推送；`v1.1.134` 构建发布成功，含 RUN-03 |
| AliangPhoneServer | main | `b5b5999` | 已推送；线上跑 `batch-b-r1`（重启裁决 + SUB-06/16 已启用） |
| AliangVibeCodingPhone（RN） | main | `1b139df` | 已推送，iOS 1.0.10 (42) 已装到 iPhone 16 Pro |

PhoneServer 的 k8s 部署已完成（见 §5.1）。真实服务名是
`aliang-phone-agent-server`，本文件早期版本写的 `aliang-phone-server` 有误。

## 3. 真机验收步骤

前置：PhoneServer 已切到新镜像；手机装 1.0.10 (42)；电脑 agent 升级到
v1.1.130；手机与电脑登录同一账号，网络正常。

### 3.1 长工具过程可见（SYNC-16）

1. 手机进入任意会话，让内置内核执行一个耗时 30 秒以上的命令。
2. 预期：1 秒内出现工具名称与计时，运行中计时持续走动。
3. 让命令运行中产生增量输出，预期手机能看到输出计数在增长。
4. 在支持单停的工具上单独停止该工具；其他场景确认停止的是整个回合。

### 3.2 TUI 内容及时到手机（SYNC-15、SYNC-12）

1. 电脑打开 Claude TUI，手机打开同一会话。
2. 在 TUI 里发一条消息，预期 5 秒内手机出现这条内容和后续回答。
3. 从手机往同一会话发消息，预期 TUI 里 1.5 秒内出现回显。
4. 让 TUI 跑一个长回答，预期手机上正文持续更新，不从头重放、不缺段。

### 3.3 前台断线识别与恢复（SYNC-03、SYNC-04）

1. 手机保持前台在会话页，关闭电脑的 agent 进程或断开电脑网络。
2. 预期 10 秒内手机显示断线状态。
3. 恢复网络后，预期连接自动恢复，期间服务端已有的新内容补齐，已显示内容保持可读。
4. 锁屏 30 秒再回来，预期 300 毫秒内已有消息可见，随后追平。

### 3.4 发送幂等与结果反馈（SYNC-07）

1. 手机发消息后立刻切飞行模式再切回，模拟服务端已受理但回包丢失。
2. 预期气泡显示「发送结果待确认」。
3. 对同一条气泡重试，预期原气泡更新为已确认，服务端只开跑一次。
4. 分别在审批提交失败、语音发送失败时确认有明确反馈。

### 3.5 网页改名多端同步（SYNC-09）

1. 网页仪表盘改会话名并保存。
2. 预期 5 秒内手机和另一个浏览器显示新标题，刷新网页后仍一致。

### 3.6 分段耗时可查（SYNC-01）

1. 分别从手机、网页仪表盘、TUI 各发起一次回合。
2. 打开仪表盘时序统计，预期能按发起端分组查到发送与回答的分段耗时。
3. 未测得的段显示为空或「未测得」，不显示成 0。

## 4. 验证记录

- Go：`app/http` 下除 `services` 外全部通过；`services` 全量顺序跑存在
  既有的测试间串扰（`TestAgentAICloseAllConvergesInflightRunsToTerminal`、
  `TestRunStartTruncateRejectsWhileRunActive` 单独跑均通过，master 基线同样
  复现），非本批引入。本批修掉了两处真实问题：TUI 回合轮询间隔与测试
  teardown 的数据竞争、静默对端用例的 hello 等待窗未容纳首次 CLI 冷探测。
- PhoneServer：typecheck 通过；时序、长视图、会话发布相关测试 16 项通过。
  全量中另有 5 个测试文件在基线分支同样失败，属既有问题。
- RN：typecheck 通过；重连、审批恢复、流式合批相关测试 22 项通过。
  全量中另有 4 个套件在基线分支同样失败，属既有问题。
- iOS Release 真机包构建成功并已安装。

## 5. B 批进展（2026-11-28 续）

行动方案 B 批的入口是「先清掉基线失败，再把关键回归接到发布门禁，
然后做 RUN-01～03」。本节按这个顺序推进。

### 5.1 PhoneServer 已部署

`nursor/aliang-phone-agent-server`（注意真实服务名带 `agent`，容器名是
`phone-server`）已通过 aliangboard 滚动更新，新 pod 启动日志确认
Postgres 初始化、1454 会话加载、4 台设备自动重连。部署清单
`deploy/k8s/deployment.yaml` 已同步镜像 tag，它是恢复锚点，换镜像后
必须同步，否则重新应用会静默回滚。

### 5.2 三仓测试基线清零

| 仓 | 清零前 | 清零后 | 修掉的真缺陷 |
|---|---|---|---|
| PhoneServer | 5 个失败 | 1330 文件 / 3658 用例，0 失败 | 2 个 |
| RN | 8 个套件失败 | 302 套件 / 2768 用例，0 失败 | 1 个 |
| Go | `services` 包偶发串扰 | 三轮全量 0 失败 | 0 个（本轮之前已修 2 个） |

PhoneServer 的两个真缺陷。`finalizeTurnActivity` 先 `void` 调
`flushDirtyThinking` 落库一次（此时 `active=true`），紧接着 finalized 链把
同一对象翻成 `active=false` 再落一次，两条异步链完成顺序不定，慢的那次把
终态覆盖回 `active=true`，正是 2026-11-10 那次修复要消灭的「历史详情永转
spinner」。改为回合末只取走 dirty 记账，落库统一交给 finalized 链。
`generatePlan` 调 `buildUserContext` 漏传 `this.now()`，与同文件
`previewContext` 口径不一致，导致「预览看到的内容」与「实际生成用到的内容」
不同。

RN 的真缺陷。`formatHarnessEnvelopes` 在计算 code fence 范围之前就对全文做
正则替换，```md 围栏里的字面 `<command-name>` 也被误改成 `<harness-command>`
卡片。改为先算 range，匹配落在围栏内则原样保留。

其余均为测试过期，逐项对齐新契约。其中 RN `localService` 两个用例值得记：
一个没给第三通道 `hk` 置 fail，hk 以 0ms 假赢把断言引偏；另一个只把时钟推
6 分钟，但 2026-11-03 的「数据面逐出」是 10 分钟，primary 根本没回到选举池。

Go `app/mesh/lanbeacon` 的 3 个用例依赖「`255.255.255.255` 全局广播回环到
同端口全部 listener」，默认路由走 VPN 隧道时该广播发不出去，用例必然各等
4 秒超时。加前置探测，环境不具备时 Skip 并写明原因。

### 5.3 发布门禁（DEBT-07 第二步）

新增 `ai-regression` job，在 `frontend` 之后、`build` 之前跑
`app / agentcore / agentruntime / http / mesh / tools / tunnel` 的全部测试，
`build` 显式 `needs` 它。带一次重试，用于区分稳定回归与偶发串扰；串扰彻底
清零后应去掉重试。

### 5.4 RUN-01/02 云端重启感知

此前 PhoneServer 对 agent 重启是「盲」的。hello 忠实存储 `started_at`（agent
进程自己的启动时刻）却从不比较新旧值，结算只靠「断线 + 15 秒宽限」。agent
快速重启或看门狗拉起时，设备在宽限内就重连、宽限被取消，上一进程的在途
回合和待审批就悬着没人结算。

新增 `device/restartSettle.ts`。hello 的 `started_at` 与 existing 不同即证明
上一进程已不在，不等宽限，就地结算。顺序是「先审批、后回合」，因为
`releaseDeviceRunningSessions` 会跳过仍挂 pending 审批的会话。三种情况刻意
不触发：`started_at` 相等（同进程重发 hello，内核 companion 每 10 秒一次）、
旧 agent 不上报该字段、首次上线。

未覆盖，留给后续：agent 侧的进程丢失判定、手动续跑入口、RUN-03 的托管子
进程收口、RUN-04 的连续中断熔断。

### 5.5 上线即回滚：`started_at` 不是进程启动时刻

重启裁决的 PhoneServer 镜像上线后，线上日志立刻暴露问题：服务端一次滚动
更新，4 台设备各被「检测到重启」2～3 次，且跨设备同步发生。agent 没有理由
这样重启。查 agent 侧发现 `agentHelloPayload` 里的 `started_at` 写的是
`time.Now()`，即**本次 hello 的构建时刻**，不是进程启动时刻。包内本就有语义
正确的 `agentProcessStartedAt`（包初始化时捕获，注释已写明「when this binary
started, not when the remote WS session connected」），只是没被用上。

后果是每次 hello 的 `started_at` 都不同，裁决会把每次重连都当成重启，把在途
回合误结算。镜像随即回滚到 `sync-all-r2`。这个缺陷在接入裁决之前一直存在，
只是没有消费方，所以从未暴露。

修复：`started_at` 改用 `agentProcessStartedAt`，`RFC3339Nano` 保留毫秒（同一
秒内重启也能区分）。补 `TestAgentHelloStartedAtIsProcessStable` 锁死语义，
该测试在还原到 `time.Now()` 时准确失败。随 `v1.1.131` 发布。

重启裁决已于 `batch-b-r1` 上线（2026-11-28）。上线时的验证结果：已升级到
1.1.133 的 `liang-dev` 重连**不再**触发 `detected`；本机 agent 真实重启时正确
触发并结算 1 个在途回合；仍跑旧版的 Windows 设备被版本门禁跳过。健康状态下
`[agent-restart] detected` 只在 agent 真正重启时出现。

### 5.6 B 批收尾（SUB-06 / SUB-16 / RUN-03）

**SUB-06 服务端结算收口子 agent。** `finalizeTurnActivity` 会把 started/progress
的子 agent 与 started 的 command 翻成 interrupted，但它只在 agent 自己发终态帧
时被调。服务端主动结算（`releaseDeviceRunningSessions`，即重启/离线/凭据失效）
漏掉了它——回合状态收敛了，结构化事件里的子 agent 永远停在 started/progress，
手机上就是转圈永不结束。在结算点补调，推送链复用同一条。

**SUB-16 终态带原因（可落地部分）。** 翻 interrupted 时把原因写进
`summary.status_detail`——它是 wire 白名单里既有的「状态说明」字段，RN 直接显示
在 detail 行，手机侧零改动。agent 自己写了更具体的说明时保留。「停止中」状态
与单停入口依赖 SUB-03，属 E 批。

**RUN-03 进程退出收口。** `StopLocalServer` 只做 HTTP shutdown + 停
usage/keepalive，从不调 `closeAll`——run 的 ctx 不被 cancel，进程组组杀就不会
触发，claude/codex 子进程成为孤儿继续写工作区。新增 `AgentService.Shutdown`，
在 HTTP shutdown 之前调。测试起一个带生产同款 Setpgid+组杀的真实子进程，断言
Shutdown 后 5s 内死亡；去掉 closeAll 后该测试准确失败。

**设备升级。** `liang-dev`（222.1）升到 v1.1.134（含 RUN-03）。它用 user-level
systemd 管两个 unit：`aliang.service`（core，`--host 0.0.0.0`）与
`aliang-agent.service`（agent）。**正确的升级步骤**：`systemctl --user stop
aliang-agent.service aliang.service` → 替换 `/home/liang/aliang` → 依次 start
两个 unit。二进制运行中不能覆盖（Text file busy）。

一处误判记下来：升级到 1.1.133 时，重启 unit 的瞬间 core 与 agent unit 短暂
竞争 `~/.aliang/agent/agent.pid` 锁，日志出现 `another_instance_running`，我据此
禁用了 `aliang-agent.service`。实际稳定后是 systemd 管的 agent 在跑，core 并不
托管 agent。禁用后再重启 core，agent 就彻底没了，服务离线。已重新 enable。
判断依据应是 `NRestarts` 与稳定后的进程树，不是重启瞬间的日志。

`0.196` 不在 aliangboard 台账，本机与 222.1 均无到它的密钥，未升级。本机按
用户要求暂不动（其他会话在测试）。

**未做。** DEBT-09 口令轮换需安排发布窗口。RUN-01 的手动续跑入口、RUN-04
连续中断熔断未开始。

## 6. C 批进展（2026-11-28 续）

### 6.1 DEBT-05 第一步：跨端共享回归夹具

行动方案的拆解是「先统一契约和回归夹具，验证后共享纯状态转换」。本批做
第一步，并且顺带补上了一个真实的不对称。

网页端 2026-11-10 起就按会话级 seq 排序（`useAliangChat` 的 `orderBySeqSlots`），
注释里记了事故：数组序在「快照限幅截尾 + 本地草稿 + 详情分页」合并时会丢
真实先后，timestamp 跨端写入会倒挂（云端 position 对实录 47095 反序）。seq
由产生数据的那一端分配，是唯一可比的先后真值。

手机端此前完全没有这层保护：`AgentMessage` 没有 seq 字段，wire 映射不读 seq，
transcript 完全依赖服务端给的数组序。同一份数据在两端可能呈现不同顺序。

契约（占位部分排序）：只重排带 seq 的元素，且只占用它们自己原来的下标位；
无 seq 的条目原地不动；seq 相等时按原下标稳定排序。刻意不是普通 comparator
——对不可比的一对返回 0 会破坏比较器传递性，JS 引擎对不一致比较器的行为
是实现定义，可能把本地草稿甩到别处。

10 个共享样例（权威源在 `docs/superpowers/fixtures/message-order-cases.json`，
RN 仓放逐字节副本并 sha256 对拍）覆盖全序、本地气泡不动、新旧混合、seq 倒挂、
重复投递、seq 相等、seq=0、单条、快照截尾、空列表。网页端 12 项、RN 侧 13 项
测试，两端收敛结果一致。RN 接到 4 个从服务端取数的入口（会话快照、实时尾刷、
tail 刷新、翻更早一页）。

### 6.2 SYNC-02：上屏时序三端打通

要验收「首段/后续文字从产出到可见的延迟 P95 ≤ 500ms」，而现有
`ai_turn_timings` 只测到服务端这一侧，缺「发出 → 客户端上屏」最后一段。

为什么独立建表 `ai_client_render_timings`：那张表在 run 终态时一次性落库且
`ON CONFLICT DO NOTHING`，而客户端上屏打点往往晚于终态到达（网络往返 +
渲染帧），没法合并进同一条记录。独立表按 `(session_id, run_id)` 关联。

时钟口径：两端都用墙钟（`agent_emitted_at_ms` 与 `Date.now()`），可直接相减。
跨设备时钟有偏差，但这是唯一的端到端口径，与 SYNC-01 服务端已用的
`agent_emitted_at_ms` 同源；偏差是系统性偏移，不影响同一设备内的 P95 趋势。

- 服务端：mobile handler 加 `client.timing` 上行分支，逐字段校验（会话须属
  本连接用户，防跨用户写遥测；延迟须为 [0, 1h] 的有限数），只记不回，写失败
  不冒泡。`client` 靠 `ws.scope` 区分（admin=网页，其余=手机）。admin 端点
  `GET /api/admin/ai/client-render-timings` 按端 × 段位出 P50/P95。
- 网页：有真实的 DOM 插入点（`ChatMarkdown` 的 onMounted/updated），比 RN
  更贴近「用户看见」。打点默认开启，`__CHAT_TIMING__ = false` 可关。
- 手机：用 rAF 回调近似「这一帧已交给 UI 线程」。只测当前正在看的会话且前台。
- 两端都是首段每 run 一次、后续文字每 run 最多 5 次。

线上验证：新表随 pod 启动建好（7 列，当前 0 行，等客户端上报）。

### 6.3 顺带根治一个全量偶发

RN `localService` 的 `discoverReachableBaseUrl prob` 约每 2 次全量出现 1 次
失败。根因是 5 处 `mockHealthByChannel` 漏了第三通道 hk（`0c156a6` 引入），hk
以 0ms 与 primary 并列时选择结果不稳定。此前只修过同文件另外两例。全补后
304 套件 / 2796 用例 0 失败。

### 6.4 C 批剩余

- SYNC-14 弱网 10 分钟持续显示、三会话并行：未做。需要弱网注入环境。
- DEBT-04 用协议替换对账：未做，是结构性重构。
- DEBT-05 第二步「共享纯状态转换」：夹具已就绪，但 RN 的 `mergeAgentMessages`
  按WeakMap 缓存与浅比较深度定制，网页端是另一种思路，直接抽共享函数风险大。
  建议等夹具跑稳一段时间再评估。

### 6.5 事故：重启裁决误杀新回合（已修）

**现象**（2026-11-28 02:49～02:51）：用户在手机上发起回合，首段文字刚上屏
2 秒就被标成「Run cancelled (agent_restarted)」。同一会话连续两次回合都被杀。

**根因**：`settleRunsForAgentRestart` 结算的是「该设备当前所有 running
会话」，无法区分「旧进程遗留」和「新进程刚起」。本机 agent 因其他会话测试
在 2 分钟内反复重启，每次重连都触发一次裁决，把新进程起的回合一并结算。

时间线（来自 `ai_Dhi5gO93hYOg` 的 events）：

```
02:49:33  Run started
02:49:53  Run cancelled  (agent_restarted)   ← 第一回合
02:51:34  Run started
02:51:51  首段文字上屏
02:51:53  Run cancelled  (agent_restarted)   ← 第二回合，首段文字后 2 秒
```

**修法**：裁决只结算 `runStartedAt` 早于新进程 `started_at` 的会话。
新进程启动后才起的回合不属于遗留，必须继续跑。没有 `runStartedAt` 的旧
服务端记录无法判断，保守按遗留处理；没传 `agentStartedAt` 时退回原语义。

**验证**：修复部署后本机 agent 又重启一次，日志只有 `detected` 没有
`settled`——当前在跑的回合是重启后才起的，不再被误杀。

**设计上的教训**：RUN-01/02 的裁决把「agent 重启」当成「设备上所有在途回合
都失效」，但重启后的新进程会立刻开始接新回合。裁决必须以「回合启动时刻
vs 进程启动时刻」为界，而不是以「设备」为界。

### 6.6 观察到但未处理

- **启动时 Postgres 连接池打满**：pod 启动加载 1453 会话后集中 flush，
  `pool wait` 峰值 191（`max_connections=200`），约 40 秒写入不可用后自愈。
  每次滚动更新都会重现。不是数据错误，但会让启动窗口内的写入失败。
- **被中断的回合文案**：裁决结算写出的是「Run cancelled」，语义上应是
  「已中断」——cancelled 暗示用户主动停止。RUN-01 验收要求「显示中断及原因」，
  这个文案需要改。
