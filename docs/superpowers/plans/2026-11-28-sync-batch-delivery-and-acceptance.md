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
| alianggate（Go agent + 网页） | master | `47f2c8d8` | 已推送；`v1.1.134` 含 RUN-03 进程退出收口 |
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

**设备升级。** `liang-dev`（222.1）升到 v1.1.133。升级后新版 core 自己托管
agent，与 `aliang-agent.service` 抢 `~/.aliang/agent/agent.pid` 锁导致无限重启，
已禁用该 unit。`0.196` 不在 aliangboard 台账，本机与 222.1 均无到它的密钥，
未升级。本机按用户要求暂不动（其他会话在测试）。

**未做。** DEBT-09 口令轮换需安排发布窗口。RUN-01 的手动续跑入口、RUN-04
连续中断熔断未开始。
