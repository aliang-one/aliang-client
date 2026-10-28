# 手机↔本机会话统一（双向续聊 + steer 全支持 + approval 双端同态）— 设计文档

日期：2026-10-28
状态：设计稿（已获用户口头批准，待 spec 评审）
范围：alianggate（Go 后端 + 仪表盘前端 ChatPage）+ AliangPhoneServer（服务端）+ AliangVibeCodingPhone（手机 App，React Native）；三仓分阶段落地，每阶段部署验证。
关联前置：`2026-10-28-ai-stream-unify-design.md`（聊天 WS 透传统一记录，已合入 master cd84195）——本设计在其地基上补「可见性双向 + 续聊双向 + 审批同态」。

---

## 1. 背景与现状结论（2026-10-28 三路只读调研，file:line 均在 master b6e9e56 核实）

记录层统一已达成：手机 `ai.message` 与仪表盘聊天（core TCP 透传到 agent 56433）汇入同一个
`agentAIManager`，provider=agentcore 的回合统一落 `<agentHome()>/.aliang/ai-sessions/<sessionID>.jsonl`
（`agent_ai_agentcore.go:975-986`），读侧三源聚合（codex+claude+agentcore，`agent_inventory.go:553-563`），
手机经 `hello.vibe_sessions`（10s 指纹 tick）、本机经 `GET /api/agent/sessions` 看同一份数据。

但「统一」只到**记录/清单层**，四个面向的缺口是本设计的对象：

1. **审批可见性单通道**：三 provider 通道的审批已汇于同一 `requestApproval`（`agent_ai.go:3543`），
   任何一端已可凭 approval_id 抢答（先到先得、幂等）。但 run 的一切事件（含审批帧）只写
   **发起回合的那条 socket** 的 emitter——手机发起的回合仪表盘看不到审批卡，反之亦然
   （`agent_ai.go:2709-2713、2824-2827`）。
2. **本机看不到手机的会话（聊天窗口径）**：ChatPage 会话列表是浏览器 localStorage
   （`ChatPage.vue:794-833`，会话 id 由前端自造，`aliangSessionId='local-xxx'` 独立命名空间），
   从不读后端清单；create 载荷缺 `resume_session_id/source_session_id`（`project_path` 字段虽在
   `chatSettings.js:62-71` 已有，但手机会话不在 localStorage 里，无值可填——缺的是「清单回填」语义，
   见 §4.3）→ 手机会话在本机聊天窗不可见、不可续聊。
3. **续聊的上下文恢复缺口**：对不在内存的 agentcore 会话发 `ai.message` 走 lazy-create，
   只凭载荷字段重建，**不读磁盘 journal、不查 bindings**（`agent_ai.go:1749-1821`）——旧会话续不上上下文；
   claude/codex 靠 `--resume`/thread resume 可续，但 Web 端从不带 resume 字段；
   resume-missing 是静默降级（`agent_ai.go:4123-4144`）。
4. **记录缺口**：`/compact` 等真实模型调用的斜杠命令不落 journal；agentcore 内置内核工具不落 tool 行
   （仅 MCP+子 agent 有轨迹）；失败/停止回合已流出的部分 assistant 输出不落盘；
   steer 先于 run 到达产生无头行文件（tool 轨迹被读侧静默丢弃，`agent_inventory_agentcore.go:149-157`）；
   claude/codex 通道的 steer 不落 journal（`journalSteerUserMessage` 仅挂 default/coreSteer 分支，
   `agent_ai.go:2447-2461`）；ChatPage 无 run 中插话 UI（`sending` 守卫直接禁用提交，`ChatPage.vue:1451`），
   本机 steer 的传输层/路由/ack 全部现成（`agent_remote_ws.go:988-989`、`ai_stream_proxy.go` 裸泵）。

### 审批机制现状细节（设计输入）

- 汇聚点：`requestApproval`（`agent_ai.go:3543`）三通道共用同一 waiter 表/状态机/超时（默认 24h，
  env `ALIANG_AI_APPROVAL_TIMEOUT`）；审批等待期豁免 idle/工具看门狗。
- 决策回程：`ai.approval.response`（accept/accept_for_session/decline/cancel）→ `m.approval()`
  （`agent_ai.go:3189-3256`）归一化→`completedApprovals` 内存去重→喂 waiter→发
  `ai.approval.ack{result: applied|duplicate|no_match}`；同时向 **run 的 writer** 补发
  `status=resolved` 的 request 帧（`agent_ai.go:3570-3580`）。
- **ack 去向缺陷**：ack 只发「送来决策的那条 socket」（`agent_ai.go:3202-3217`）——仪表盘抢答手机回合时
  ack 进浏览器 WS，PhoneServer 重投引擎收不到 ack，已生效决策被重投至多 10 次（agent 靠
  completedApprovals 回 duplicate 幂等兜底，服务端状态滞留）。
- 本机刷新/断线无补卡：`emitApprovalSync`/`pendingApprovalsSnapshot`（`agent_ai.go:3264-3303`）
  **只在远端 WS 注册路径被调用**，本地流无人调用；ChatPage 重载清空 approvals（`ChatPage.vue:819-821`）。
- ChatPage 不处理 `ai.approval.cancelled`（事件分支仅 request，`ChatPage.vue:1400-1404`）。
- `accept_for_session` 仅 codex 真正映射 scope=session；claude/agentcore 当普通 accept 且不落
  会话级规则（`agent_ai.go:3745-3747、1656-1657`）。
- 本机可答手机回合的审批（`DispatchLocalAI` 路由 `ai.approval.response`，`agent_remote_ws.go:989-991`），
  但 `ai.option.response`/`tool_ask.response` 走 default 分支直接 unsupported（`agent_remote_ws.go:984-1013`）。
- 审批状态 agent 侧纯内存（重启即失）；PhoneServer 侧已落 DB+审计（docs/ai-approval-reliability-design.md
  Phase 3，重投 2/4/8/16/30s 至多 10 次，3min 离线宽限后 `ai.approval.cancelled` 撤单）。

### 会话标识与续聊合同现状细节（设计输入）

- 会话 ID 由调用方指定（`ai.session.create` 必填 `session_id`，手机现实为 UUID）；内存表容量 8（LRU 驱逐，
  `agent_execution_guard.go:49`），驱逐/重启后靠 lazy-create——但 lazy-create 不恢复上下文。
- 三套 ID 命名空间混在同一份 vibe 清单：`claude_<uuid>`/`codex_<id>` 前缀条目、agentcore journal 裸
  conversation ID、内存托管裸 ID；`annotateManagedVibeSessions`（`agent_ai_identity.go:325-367`）已给每条
  补 `source_session_id`（剥前缀取原生 ID）并与 bindings 匹配回填 origin/binding_state——**映射产物现成**。
- claude/codex 续聊全链路现成：`--resume`（`agent_ai.go:8154-8158`）/thread resume；前提是载荷带
  `resume_session_id`+正确的 `project_path`（resume 按 project 目录定位转录文件，换目录必 resume-missing）。
- agentcore 续聊只有「id 换手」没有「history 换手」：`cfg.SessionID` 仅帧标签，内核上下文全靠内存 history
  （`agent_ai_agentcore.go:421-438`）；`transcript` 载荷（`remoteAgentAIHistory`，`agent_ai.go:2901-2924`）
  是唯一现成喂历史入口。
- journal 读侧解析器 `readAgentCoreSessionMetaWithOptions`（`agent_inventory_agentcore.go:58`）已能完整解析
  journal 行格式（msg/tool/end/session 行、残行容忍）——读回 history 可直接复用。
- 对 running 会话发 `ai.message` 直接报错（`agent_ai.go:1834-1838`）；steer 的 ack 词汇表
  queued/applied/error/not_running/unsupported/held 已是跨通道统一契约。

---

## 2. 目标（用户拍板，2026-10-28）

1. **双向续聊**：本机聊天发起的会话，手机上可以继续；本机也可以看到手机的会话并继续。
   ChatPage 会话列表以**后端统一清单为准**（手机/本机/终端导入会话同列表、点开即续聊）。
2. **steer 全支持**：本机聊天窗 run 中插话（与手机同语义）；所有通道的插话进统一记录。
3. **approval 双端同态**：不管手机端审核还是本地审核，**状态统一**——任何一端都能看到待审批项、
   都能应答、应答结果实时反映到另一端；会话级放行（accept_for_session）三通道语义齐平。
4. **记录缺口修复**：斜杠命令/内核工具/半截输出/无头行竞态全部落账，统一记录完整可信。

**非目标**：
- 不改 core 进程的 AI 透传架构（维持 ai-stream-unify 的 TCP 泵 + 绝不本地回退）；
- 不重做 PhoneServer 审批 DB/重投引擎（只修 ack 消费对齐）;
- 不把 localStorage 历史会话迁移进 journal（本地旧草稿仅保留展示，标注为本地缓存）；
- 不做手机端实时逐字观看本机会话以外的推送优化（手机端逐字观看属阶段 4 可选项，
  依赖 PhoneServer 代订阅，合同本设计已预留）。

## 3. 方案取舍（已拍板）

| 决策点 | 选定 | 否决项与理由 |
|---|---|---|
| 审批双端可见 | **agent 进程内订阅表广播**（本设计 §4.1） | PhoneServer 中心广播：本机浏览器↔服务端无实时通道，需新拉 WS 跨两层，改动面大 |
| ChatPage 清单来源 | **REST（/api/agent/sessions+详情）+ WS 事件驱动刷新** | WS 协议扩展推送清单：动协议契约、三端对齐成本高 |
| agentcore 跨端/重启续聊 | **journal 读回（agent 端 lazy-load）** | 服务端回填 transcript：每次续聊都要服务端中转，且 journal 缺口一样存在 |
| 实施范围 | **三仓全链路**，四阶段每阶段部署验证 | 只做 alianggate：手机端续聊入口缺失，目标不完整 |

## 4. 设计

### 4.1 会话订阅表（核心机制）

**agentAIManager 新增订阅表**：`subscribers map[sessionID]map[socketID]chan` + `sockets map[socketID]*socketEntry`。
socketEntry 含 writer、角色（remote/local）、订阅集合。两条 socket 通道都注册：
手机远端 WS（`agent_remote_ws.go` 注册点）与本地透传 WS（`DispatchLocalAI` 首帧后注册、连接关闭注销）。

**事件路由规则**：
- run 流式事件（ai.delta/ai.thinking/ai.tool/ai.run.started/ai.done/…）：发**发起通道 + 该会话全部订阅者**。
  发起者即订阅者（发起时自动入订阅表），去重后单写。
- 审批族事件（ai.approval.request/resolved/cancelled、tool_ask、option）：在上一条基础上**额外广播给全部
  活跃 socket**（不看该会话也能看到待审批——双端审核的关键语义）。
- 单 socket 写失败：断该 socket、清理订阅，不影响 run 与其他 socket。

**新协议消息**（本地 WS 立即可用——透传是裸 TCP 泵零改动；远端 WS 同帧型预留，供阶段 3 PhoneServer 代订阅）：
- `ai.session.subscribe {session_id}` → agent 回 `ai.session.snapshot {session_id, transcript:[...], pending_approvals:[...], status}`
- `ai.session.unsubscribe {session_id}`
- 订阅握手即推：转录快照（journal/原生转录读侧现成解析）+ pendingApprovalsSnapshot（原语现成
  `agent_ai.go:3264-3303`，改为按 socket 推送）。

契约表方向基准=远端 /ws/agent 链路（client=agent 拨号方）：subscribe/unsubscribe 登记在 ServerSends（agent 收），snapshot/ai.run.notice 登记在 ClientSends（agent 发）；本机 /api/agent/ai/stream 通道复用同帧型不经此表。

**安全边界不变**：本地 WS 维持 localhost-only 门（`aiStreamHostAllowed`），远端 WS 维持 `aiControlEnabled`
设备门；订阅不做跨设备权限升级——手机订阅某会话仍需经 PhoneServer 现有会话归属校验（阶段 3 落实）。

**订阅生命周期**：发起回合的 socket 在回合开始时自动入订阅表，**回合结束不自动退订**——订阅随 socket
生命周期存续（socket 关闭或显式 unsubscribe 才清理），保证回合结束后该端仍能收到后续审批/决议事件。

### 4.2 approval 双端同态

1. **fan-out**：审批事件经 §4.1 订阅表+全 socket 广播。pending 帧新增可选字段 `expires_at`（RFC3339），
   两端 UI 显示剩余时间（默认 24h 超时对两端用户此前均不可解释）。
2. **ack 双写**：`m.approval()` 的 ack 改为同时发「决策到达的 socket」与「发起 run 的通道」（各按送达能力，
   发不出去仅记日志）。修掉仪表盘抢答手机回合时 PhoneServer 重投 10 次的滞留。
3. **补卡**：订阅/重连握手推 pending 快照（§4.1）；ChatPage 增加 `ai.approval.cancelled` 分支清卡；
   ChatPage 的 handledApprovalIds 仍作幂等兜底。
4. **accept_for_session 齐平**：新增会话级批准记忆（内存 + 随会话生命周期，LRU 驱逐即失效），
   挂 `evaluateApprovalDecisionForHook`/`agentCoreApprove` 前检查：命中会话级放行的同族操作直接放行并落
   journal 审计行。claude/agentcore 通道语义对齐 codex 的 scope=session；**「同族操作」的匹配语义
   （同工具名/同命令形态/同 kind，以 codex 现行 scope=session 行为为准绳）在实现计划阶段对照
   `agent_ai.go:5603-5673` 的 codex permissions 实现钉死，不在此拍板**。
5. **本机作答补全**：`DispatchLocalAI` 补 `ai.option.response`/`tool_ask.response` 两个 case
   （manager 层通道与渠无关，现走 default 报 unsupported）。

### 4.3 双向续聊

**ChatPage 统一清单（后端为准）**：
- 会话列表改读 `GET /api/agent/sessions`（现成三源聚合+活跃 overlay）+ `GET /api/agent/session`（转录回放）；
  WS 事件（run started/done、审批到达）触发列表与打开中会话的刷新。
- 条目展示来源标记（手机/本机/终端导入，依据 origin/source_session_id 现成字段；journal 头行补 `origin`）。
- localStorage 降级为草稿与 UI 偏好（进行中未发送的输入、滚动位置等），不再是会话真相。

**续聊载荷（Web 补字段）**：
- create/ai.message 补 `resume_session_id`（=清单条目 source_session_id）、`source_session_id`、
  `project_path`（清单原值回填，防 claude resume 按 cwd 定位转录失配）。
- 后端已全量解析这三个字段（`agent_ai.go:1601/1804/1919`），**后端零改动**。

**agentcore journal 读回（agent 端 lazy-load）**：
- `message()` 对不在内存的会话：若 `.aliang/ai-sessions/<sessionID>.jsonl` 存在 → 读回 msg 行重建 history
  （复用读侧解析器），装回内存再跑；journal 不存在才走现 lazy-create。
- 头行补写幂等：`runAgentCore` 入口写头行改为「文件存在但无 session 头行时补写」（修 steer 先于 run 的无头行竞态）。
- journal 头行新增 `origin` 字段（phone|dashboard），头行缺失时读侧兜底 unknown。

**resume-missing 显式化**：claude/codex 原生会话找不到时，从静默降级改为发
`ai.run.notice {code: resume_missing}` 事件帧（经订阅表广播），两端 UI 提示「原生会话不存在，已按新会话继续」。

**并发仲裁统一**：会话 running 时，两端的输入提交统一切 steer 模式（发 `ai.steer`）；仅空闲时发
`ai.message`。`ai.message` 对 running 会话的报错保留为最后防线（直连 API 场景）。

### 4.4 steer 补齐

- ChatPage 补 run 中插话：解掉 `sending` 守卫，running 状态（由 WS 事件维护）下提交走 `ai.steer`。
  传输/路由/ack 全现成，纯前端。
- claude/codex 通道 journal steer 行：在 `steer()` 投递 switch 的 claude/codex 分支补
  `journalSteerUserMessage`（签名通用，依赖 coreJournal 实例——为 CLI 通道补一个轻量 journal 写侧实例，
  只写 user 行，不写头行外的结构）。
- steer 的 ack（queued/not_running）经订阅表回发订阅者，本机可见插话被受理/被拒。

### 4.5 记录缺口修复

| 缺口 | 修法 |
|---|---|
| `/compact` 等真实模型调用斜杠命令无痕 | agentcore 斜杠命令执行点落 user 行（=命令原文）+ assistant 行（=摘要结果）；内核自动压缩（OnCompact）同补 |
| 内置内核工具（Bash/Read/Edit）无 tool 行 | `executeOneInner` 工具完成后落 tool 行；输入输出超长截断（如 4KB），防 journal 膨胀 |
| 失败/停止回合半截输出丢失 | defer end 行之前，若已流出部分 assistant 文本则补 assistant 行（尽力而为，与现有成功路径同一 append 通道） |
| 审批决策无本地审计 | `rememberCompletedApprovalLocked` 同步追加 journal 审计行（approval_id/decision/来源 socket 角色） |
| 头行缺失降级 | §4.3 幂等补写头行 |

## 5. 各仓改动清单

**alianggate 后端**：订阅表与 fan-out（agentAIManager）、socket 注册/注销生命周期、`ai.session.subscribe/
unsubscribe/snapshot`、审批 ack 双写、`expires_at`、会话级批准记忆、DispatchLocalAI 补 option/tool_ask case、
journal 读回 lazy-load、头行幂等补写、journal 写侧补齐（斜杠命令/内核 tool 行/半截输出/审计行/origin）、
CLI 通道 steer journal 行、`ai.run.notice` 帧型。
**alianggate 前端（ChatPage）**：统一清单列表与来源标记、点开续聊（载荷补 resume/source/project_path）、
run 中 steer 输入、审批卡（cancelled 分支/补卡/倒计时/accept_for_session 第四键已有）、resume_missing 提示。
**PhoneServer**：手机续聊载荷回填（清单/详情取 source_session_id+project_path 填入 resume 字段）、
App 打开会话时代发 `ai.session.subscribe`（代订阅，事件按会话路由到打开该会话的 App 连接）、
审批 ack 消费对齐（认双写 ack，去重逻辑不变）。
**手机 App**：会话卡片/详情页补续聊输入框（走现成 ai.message/ai.steer 通道）、审批卡倒计时展示、
来源标记展示。**阶段 4 可选**：实时逐字观看本机会话（依赖代订阅）。

## 6. 错误处理

- fan-out 单 socket 失败：断连清理，不影响 run；发起通道失败维持现有 run 失败语义。
- journal 读回对残行/无头行照实回填（残行丢弃策略与读侧一致），不伪造内容；读回失败（IO 错）则回退
  lazy-create 空上下文并回 `ai.run.notice {code: history_restore_failed}`。
- 抢答幂等不变：先到先得 applied，后到 duplicate；completedApprovals 去重缓存保留（重启失效由两端
  快照补卡兜住展示层）。
- 订阅不存在的会话：snapshot 返回空转录+清单态，不报错（列表刷新场景友好）。
- resume-missing / history_restore_failed 均显式事件帧，不再静默。

## 7. 测试策略

- **Go 单测**：订阅表生命周期（注册/注销/去重/单 socket 失败隔离）；审批 fan-out 双 socket 可见性；
  ack 双写幂等；pending 快照内容；会话级批准记忆命中与失效；journal 读回（正常/无头行/残行/不存在）；
  头行幂等补写；斜杠命令与内核 tool 行落盘；半截输出落盘；CLI steer journal 行。注意既有护栏：
  测试进程必须惰性把 localUserAgentBaseURL 指向 127.0.0.1:1（防真机 56433 误伤，v1.1.78 护栏）。
- **前端 E2E**（app/website/tests/e2e/）：统一清单加载三源会话；点开手机会话看到转录并可续聊；
  run 中 steer 被受理（ack 展示）；审批卡双 socket 各自应答（先到先得+另一端 resolved 同步）；
  cancelled 清卡；agent 离线仍不回退（既有 chat-unify.spec.js 语义保持）。
- **跨仓联调清单**：手机续聊本机会话（带 resume 字段）；本机答手机回合审批+抢答后服务端无重投
  （看 PhoneServer 日志 attempts 停止递增）；手机 steer 本机 run；/compact 后两端转录一致。

## 8. 阶段划分与验收门

| 阶段 | 仓 | 交付 | 验收门 |
|---|---|---|---|
| 1 | alianggate 后端 | §4.1/4.2 全部 + §4.3 后端部分（journal 读回/头行幂等/origin/notice）+ §4.4 后端 + §4.5 | Go 单测全绿；部署 liang-dev；Agent 活动页逐项核验四类新落盘行真实出现：①斜杠命令的 user+assistant 行 ②内置内核 tool 行（含超长截断）③失败回合半截 assistant 行 ④审批审计行；steer 竞态场景确认头行被幂等补写 |
| 2 | alianggate 前端 | ChatPage 统一清单/续聊/steer/审批卡补齐 | E2E 全绿；liang-dev 浏览器人工验证双端场景 |
| 3 | PhoneServer | 续聊载荷回填、代订阅、ack 消费对齐 | 部署；手机端续聊本机会话联调通过 |
| 4 | 手机 App | 续聊输入/倒计时/来源标记（+可选实时观看） | 出包真机冒烟（iOS 配方见 memory ios-build-install-recipe） |

## 9. 风险与已知取舍

- journal 读回的上下文=journal 所见：斜杠命令与半截输出在历史 journal 文件中本就无痕（本设计只保证
  **新**回合起落账），旧文件续聊时这些内容缺失属预期。
- 订阅表使 run 事件写路径多出 N 个订阅者：单 socket 写必须带超时/非阻塞缓冲，防慢消费者拖垮 run
  goroutine（实现细节：每订阅者 buffered chan + 丢弃计数，溢出断开该订阅者）。
- PhoneServer 代订阅引入「服务端按会话路由事件」的新职责：阶段 3 的主要工作量；若 App 多端登录同账号，
  需定义事件扇出到多设备的行为（首版：全部在线设备）。
- 会话级批准记忆是内存态（随会话 LRU/重启失效）：与 codex 现状一致，不持久化到策略规则（持久化另单）。
- 手机端改动出包成本重（RN 出包+真机），放最后阶段，前两阶段在本机即可完整验证双端语义（浏览器扮演本机端）。
