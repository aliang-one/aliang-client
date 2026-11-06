# 设计：聊天图片全链路——持久附件缓存 + journal 附件化 + 网页显示与发送

日期：2026-11-05
状态：设计已与用户对齐（通道=A 本地持久缓存；范围=显示链+网页发图）
批次性质：alianggate 单仓（PhoneServer 零改动）；Go 后端 + Vue3 前端

## 0. 审计结论（2026-11-05 chat-image-support-audit，复核零推翻）

手机发图链路到内核当轮全通（COS 直传→600s 签名 URL→agent 下载→claude stdin image block / codex --image），断点全在 alianggate 网页侧三处：journal 只落 text（纯图消息整条不落）→ 读侧三层丢 → 前端无附件概念。网页历史源=agent journal 与手机历史源=PhoneServer DB 是两条独立链。

## 1. 目标

1. 手机发的图片消息在网页 ChatPage 完整显示（含纯图消息）。
2. 网页端可以发送图片，进对话历史、进内核上下文、进 journal。
3. 通道=A 本地持久缓存：下载/上传时落 `<EffectiveAgentHome>/aliang-attachments/<sessionID>/`，journal 存相对引用，网页经 agent 回环端点取图。零 PhoneServer 改动、离线可用、无 30 天限制。

## 2. 非目标

- 手机端看不到网页发的图（PhoneServer 零改动；不对称性记录在案，跨仓同步另立批次）
- claude/codex 原生转录的历史图块渲染（phase 1 只做 `[图片]` 占位防整条消失）
- agentcore 跨 run 图失忆（内核侧限制，不属于显示链）
- 存量 journal 不可回填（落盘时已丢，只对新消息生效）
- 缓存跨机迁移/云备份

## 3. 架构

### 3.1 持久附件缓存（写点收敛为三处）

目录：`<EffectiveAgentHome>/aliang-attachments/<sessionID>/<messageID>-<idx><ext>`（目录 0755，沿用 TUI 缓存先例 agent_ai_attachment_download.go:110-165 的 agentAIAttachmentCacheDir 模式扩展 session 键）。

写点（单写点原则，内核注入路径不受扰动——持久化是附加动作）：
1. **runCLI 下载路径**：downloadAgentAIAttachments 成功后副本落缓存，run.attachments[].PersistPath 置相对引用；
2. **steer 路径**：buildSteerImageBlocks 已即时下载转内存——同点落副本；
3. **网页上传端点**：直接写缓存，返回引用。

配额纪律：单文件 ≤10MiB（对齐手机协议）、单消息 ≤8、会话缓存 ≤256MiB（超出拒绝新写入+可读错误）、全局 ≤1GiB 启动清扫（按会话最旧驱逐，实现为启动时一次性 sweep+测试）。**已知口径差**：resolve/下载侧允许 20MiB/文件（maxAttachmentBytes），10-20MiB 带内的图能进内核但跳过持久化（无网页显示）——这是有意降级，须有可观察的测试钉住；孤儿缓存文件（recall/truncateBefore 后）由全局 sweep 收敛，不做即时删除。

### 3.2 journal 行附件化（治本断点①）

- `agentAIJournalAttachment{Name, Mime, Bytes, Ref, Origin}`（Ref=相对缓存根，journal 不落绝对路径；Origin=phone|web）。
- `appendUser`/`appendMsg` 追加变参 `attachments ...[]agentAIJournalAttachment`；行 JSON 增 `attachments`（非空才写）；**空文本守卫改为「text 空且 attachments 空」才 return**——纯图消息整条落盘。kind=turn 行不动（内核回答本批次无图）。
- **签名变更触及的全部 5 个调用点**（第 1 个是主写点，评审修正——原 spec 误把 agent_ai.go:2596 当消息路径）：
  1. `agent_ai_agentcore.go:1153`（runAgentCore 内消息主路径 user 行，content 来自 lastUserHistoryMessage）——**附件从 run.attachments 透传**（run.attachments 已在 runUserMessage agent_ai.go:3323 填充；runCLI 下载先于本落账，时序成立）；
  2. `agent_ai.go:4698` 区段（journalSteerUserMessage，steer 路径）——steer 附件透传；
  3. `agent_ai_agentcore.go:712`（/compact）、:1756（auto-compact）、`agent_ai.go:2596`（本地斜杠命令）——纯文本语义不变，附件变参缺省不传（签名兼容零改动）。
- 缓存文件名统一按 **messageID+idx**（steer 行亦有 MessageID，不引入 steer 序号）；会话无 messageID 的边缘用 runSeq 兜底。
- 孤儿文件说明：recall/truncateBefore 只删 journal 行，缓存文件留待全局 sweep 按帽驱逐（不做即时删除）。

### 3.3 读侧透传（治本断点②）

- `agentAIMessage` 增 `Attachments []agentAIAttachmentView{Name,Mime,Bytes,Ref}`。
- `agent_ai_journal_history.go` msg+turn 两 case：解析行 attachments；**空文本但有附件的行保留**（Content 空，前端渲染缩略图分支）。
- `agent_ai_sockets.go` 订阅快照：同透传。
- `agent_inventory.go` claude/codex 原生转录 reader：image block/input_image → 文本占位 `[图片]`（防纯图行消失；无可达 URL，完整渲染属 phase 2）。
- `agent_inventory_agentcore.go`：msg/turn 行 attachments 透传（journal 已有则带出）。

### 3.4 本地 HTTP 端点（治本断点②→③的桥）

双注册（routes.go register + agentruntime/server.go mux——aa80152 的 404 教训）：
- `POST /api/agent/ai/attachment/upload`：multipart 取 file + `session_id` 字段（非空校验，契约钉死见 §3.6）；loopback + 拒跨源 Origin（照抄 transfers D13 模式 agent_handler.go:780 区段）；mime 白名单 jpeg/png/gif/webp；≤10MiB；落对应会话缓存目录，返回 `{ref,name,mime,bytes}`。
- `GET /api/agent/ai/attachment?ref=<rel>`：ref 经 filepath.Clean + agentPathInsideDirectory(缓存根) 守卫（防穿越）；按扩展名/mime 表回 Content-Type；流式回文件；缺失 404。loopback 由既有服务器绑定保证（loopbackOnly 红线）。

### 3.5 agent 附件解析扩展（网页发图的内联前提）

`resolveAgentAIAttachments`（agent_ai.go:3512-3641）的本地路径守卫从「必须在项目目录内」扩展为「项目目录 **或** 持久附件缓存根」（缓存根是 agent 自控目录，非用户任意路径——逃逸防线不弱化）。网页 WS 载荷带 `{type:'image', name, path:<缓存绝对路径>}`，命中缓存根即视为已持久化（PersistPath 直接置位，不再重复落盘）。

### 3.6 前端

- **useAliangChat.js**：transcript/snapshot/detail 归一化保留 `attachments`（Ref→`/api/agent/ai/attachment?ref=` URL）。**第四个丢图点必须同修**：`applyDeviceTranscript`（~:771-780）过滤 `m.content` truthy、`applyDeviceSnapshotTranscript`（~:793+）过滤 `m.text` truthy——两处改为「content/text 为空但 attachments 非空也保留」；注意 snapshot→detail 拆分：空文本纯图行保留在快照与详情转录中，但**不进续聊喂给内核的 deviceTranscript**（内核续聊上下文仍纯文本语义，本批次不把图回灌内核历史）。发送：`ai.message` 载荷带 `attachments:[{type:'image',name,path}]`；上传在途时禁发；**edit-resend 只重发文本不带附件**（与现有 content-only 编辑语义一致）。
- **ChatComposer.vue**：图片选择按钮（accept image/*，≤8 张，客户端 mime/大小预检），即时上传+chips（可移除），发送按钮守卫。
- **ChatMessageBubble.vue**：新增**附件渲染分支**（任何 role）：缩略图条（`<img :src>`、lazy、max-h-40 圆角、点击新窗看原图、横向滚动条，触控目标≥40px、暗色适配、mobile 溢出哨兵安全）；**user 气泡文本仍纯文本插值——铁律不破**，附件是兄弟 DOM 元素而非 markdown 管线；纯图消息文本空则只渲染缩略图条。
- i18n zh/en：chat_attach* 键（发送/上传失败/超限文案）。
- **上传端点 sessionID 契约钉死**：multipart `session_id` 字段（校验非空），服务端落对应会话目录。

## 4. 验收

1. Go 单测：缓存服务（写入/配额/清扫/穿越拒绝）、journal 写读（纯图行/带字行/老 reader 兼容）、history+快照透传、上传/取图端点（httptest：mime/大小/loopback守卫/穿越）、resolve 守卫扩展。
2. `go build ./...` + 全 services/cmd peer 无关包不回归（对照已知 flake 基线清单）。
3. 前端 vitest：useAliangChat 透传、气泡附件分支、Composer 上传流；`playwright mobile-responsive.spec.js` 绿（哨兵）。
4. 真机验收（部署后）：手机发纯图→网页显示；网页发图→内核能描述图内容→刷新网页历史仍在；手机消息在网页历史含缩略图。

## 5. 风险锚点

- journal 行加键对老 reader 的兼容（map 索引式读取天然忽略未知键——全包测试实证）。
- user 气泡纯文本铁律：附件渲染走独立分支，绝不复用 markdown 管线。
- 路径穿越：取图/上传两端点 + resolve 守卫三处都要测试穿越用例。
- 双注册漏一 = core 代理 404（aa80152 教训）。
- 内核注入路径（temp 下载/base64）一行不动，持久化纯附加——失败仅降级为「无网页显示」，不影响内核收图。
- 前端发图在途禁发（防载荷半截）；WS 断线重连后 chips 状态保留。
