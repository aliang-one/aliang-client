# ChatPage 会话设置条（目录 / 模型 / effort）— 设计文档

日期：2026-10-27
状态：设计稿（待评审）
范围：仅前端（`app/website`）+ 一行协议文档补录；后端零改动
前置事实：本单是「仪表盘 AliangCode 聊天入口」（b517d3b，已合并推送）的后续——
用户反馈 AliangCode 应具备 Claude Code 同款能力，但当前 ChatPage 建会话只发
`project_path: conv.projectPath || ''`（该值 UI 从未让用户设置，恒为空 → 落
home）+ `provider`，没有 model、没有 effort，三项能力全部不可见不可配。

## 1. 背景与目标

后端 `ai.session.create` 处理器（`app/http/services/agent_ai.go:1470-1560`）
**早已支持** `project_path` / `model` / `effort` / `context_window` 等字段：
建会话时清洗并存入 session，existing 分支（:1507-1529）支持对同一 `session_id`
重发 create **原位更新** model/effort。agentcore 内核侧：`project_path` 经
`resolveAgentAICWD`（:8667，空值回落 home，存在性+symlink 校验）→ system
prompt `<env>` 块的 Working directory；`model` 经 `agentCoreModel`（只认
`glm-*`，默认 `glm-5.3`，env 兜底；注意 glm-only 是回环网关的环境约束，代码层
`agentCoreModelUnusable` 只挡空值与 `<synthetic>`，非 glm 值不会被服务端拒绝）；`effort` 转请求体 `output_config.effort`
（`agent_ai_agentcore.go:229-232`，档位白名单即内核 `NormalizeEffortLevel`，
`app/agentcore/loop.go:151-168`：low/medium/high/xhigh/max，无法识别静默丢弃）。
claudecode 路径同样接受 model/effort（经 claude CLI `--model`/`--effort`，
effort 合法清单相同）。缺口纯在 web 前端。

**目标**

- ChatPage 头部下方新增**常驻设置条**：工作目录、模型、effort 三项可见可配
  （UI 形态用户拍板：常驻设置条，非抽屉/向导）；
- 目录来源（用户拍板）：**最近项目 + 手输路径**——最近项目取
  `GET /api/agent/sessions` 各会话的 `project_path` 去重，另给手输框；
  不新增目录浏览接口（无通用 fs list 端点，本单不补）;
- 模型清单（用户拍板）：**固定常用档 + 自定义**——前端常量维护；
- 生效时机（用户拍板）：**目录建会话后锁定（只读展示），模型/effort 随时可改，
  下一条消息生效**（重发 create 原位更新）；
- 手输路径不存在时的 create 失败要**可见**：当前 ChatPage 对「无运行气泡的
  ai.error」静默丢弃（`ai.error` handler 只找 running 气泡），必须补。

**非目标（留待后续批次）**

- skill 内核加载、CLAUDE.md/项目上下文、MCP 可视化（内核缺 skill 与上下文
  加载是事实，但本单按用户拍板只做设置栏）；
- 目录浏览接口（`GET /api/agent/fs/list` 类）；
- context_window 配置 UI（后端支持但用户未点名，YAGNI）；
- 手机端任何改动；
- 服务端、PhoneServer 任何行为改动。

## 2. 方案取舍

| 方案 | 结论 |
|---|---|
| **A. 头部下方常驻设置条一行（本设计）** | ✅ 三项状态一眼可见（用户抱怨的正是「看不见」），改模型两次点击；代价 ~36px 头部高度 |
| B. 顶栏齿轮 → 右侧抽屉 | 否：当前值不可见，每次改都要开抽屉 |
| C. 新建会话向导弹窗 | 否：与「模型随时改」冲突，还得再补会话中入口 |

目录/模型清单两个子决策同理：**最近项目+手输** 零后端改动一版能用（浏览接口
要新增鉴权端点+路径穿越校验，~100 行带测试，收益是体验）；**固定清单+自定义**
前端一个常量文件（后端无模型清单接口，为四个字符串加接口偏重）。

## 3. 架构与数据流

```
设置条控件 (ChatPage)
  ├─ 目录 ▾ ──┐  GET /api/agent/sessions → project_path 去重 →「最近项目」
  │           └─ 手输路径 → conv.projectPath（未锁定时可写）
  ├─ 模型 ▾ ── conv.model（'' = 默认/跟随本机配置）
  └─ effort ▾ ─ conv.effort（'' = 不发送）

sendMessage()（首次，!conv.aiSessionCreated）
  └─ ai.session.create { session_id, project_path, provider, mode: 'vibe',
                         model: conv.model || '', effort: conv.effort || '' }

模型/effort onChange（已建会话且 socket 在线）
  └─ 立即重发同一 create 载荷 → 后端 existing 分支原位更新 → 下一条消息生效

ai.session.created 回显（含服务端清洗后的绝对路径/model/effort）
  └─ conv.projectPath ← 回显值（用户看到 home → 绝对路径的解析结果）

sendMessage 走「create 确认门」：先发 create 等 ai.session.created 回执
（或 create 期 ai.error / 5s 超时），确认成功才发 ai.message——目录无效时
消息绝不经 ai.message 的 lazy-create 落 home 跑；socket 掉线期间的设置漂移
由每次发送前重发 create 自动补同步。
ai.error（无 message_id = create 阶段失败；运行期错误帧全带 message_id）
  └─ 设置条下方一次性内联错误；目录保持可改（dirLocked 未置位），纠正路径后
     下一条消息重发 create 即成功
```

## 4. 详细设计

### 4.1 新文件 `app/website/src/utils/chatSettings.js`

逻辑抽纯函数（ChatPage 无组件测试是现状，纯函数可 vitest 直测）：

```js
export const AGENTCORE_MODEL_OPTIONS = [
  { value: '',                  label: 'default' },   // i18n key
  { value: 'glm-5.3',           label: 'glm-5.3' },
  { value: 'glm-5.3[1m]',       label: 'glm-5.3[1m]' },  // quick-setup 实际写入形态
  { value: 'glm-5.3-flash',     label: 'glm-5.3-flash' },
  { value: 'glm-5.3-flash[1m]', label: 'glm-5.3-flash[1m]' },
  { value: 'glm-5.2',           label: 'glm-5.2' },
  { value: 'glm-5.1',           label: 'glm-5.1' },
  { value: '__custom__',        label: 'custom' },    // 选中后出现输入框
];

export const EFFORT_OPTIONS = [
  { value: '', label: 'default' },   // '' = 不发送，服务端不覆盖
  { value: 'low', label: 'low' },
  { value: 'medium', label: 'medium' },
  { value: 'high', label: 'high' },
  { value: 'xhigh', label: 'xhigh' },
  { value: 'max', label: 'max' },
];  // 与内核 NormalizeEffortLevel 白名单一致，超集值会被静默丢弃，故必须同源

export function recentProjectsFromSessions(sessions, { limit = 8 } = {});
// 取 sessions[].project_path 非空值，去重（保留最新一次出现的顺序，输入已按
// updatedAt 倒序），截断 limit。输入非数组/含 null 容错，返回 string[]。
// （输入按 updatedAt 倒序时，某路径的首次出现即其最新会话——实现保留首次出现顺序，与「最新优先」语义一致。）

export function isDirectoryLocked(conv);
// conv.dirLocked === true → 锁定。dirLocked 由 ChatPage 在收到
// ai.session.created 回显时置位（锁定 = 服务端确认建会话成功）；create 被拒的
// 会话保持可改，纠正路径后下一条消息即落正确目录；legacy 会话（无该字段）
// 不锁定。

export function sessionCreatePayload(conv);
// { type:'ai.session.create', session_id: conv.aiSessionId,
//   project_path: conv.projectPath || '', provider: conv.provider || 'claudecode',
//   mode: 'vibe', model: conv.model || '', effort: conv.effort || '' }
```

模型清单事实依据：`agentCoreModel` 的 env 兜底即本机 quick-setup 写的
`glm-5.3[1m]` 形态（带 `[1m]` 后缀网关接受，`agent_ai_agentcore_test.go:86`
有透传断言），故清单含 `[1m]` 变体；claudecode 会话经网关跑的同样是 glm 模型
（`agent_ai_claude_steer_test.go:198` 实证 `glm-5.2` 直通 claude CLI 参数），
同一清单两端通用。

### 4.2 ChatPage 设置条 UI

位置：顶栏（标题/重命名/删除/关闭）`</div>` 之后、消息区之前，全宽一行。

- **目录控件**：胶囊按钮显示当前路径（`conv.projectPath` 截断中段，如
  `~/My…/alianggate`，title 悬浮全路径；空值显示「默认（home）」）。点击弹
  absolute 定位小面板：
  - 「最近项目」分组：`recentProjectsFromSessions` 结果，逐项点击即选中；
  - 手输输入框 + 确定按钮：任意字符串接受（不做前端形态校验——agent 可能是
    Windows，合法性由服务端 `resolveAgentAICWD` 在 create 时裁决）；
  - 面板外点击/Esc 关闭。
- **模型控件**：`<select>`，选项 = `AGENTCORE_MODEL_OPTIONS`（label 走 i18n
  映射）；选「自定义…」时同一行出现文本输入框，确认写入 `conv.model`。
- **effort 控件**：`<select>`，选项 = `EFFORT_OPTIONS`。
- **锁定语义**：`isDirectoryLocked(conv)` 为真 → 目录控件渲染为只读文本
  （仍带 tooltip），模型/effort 控件不变；`activeRunning` 为真 → 三项全部
  `disabled`。
- **移动端**：设置条 `overflow-x-auto`，路径文本 `max-w-[10rem] truncate`，
  触控目标 ≥40px（沿用 mobile-responsive 约定）。
- 数据变更即写 `conv` 字段 + `saveToStorage()`（字段随现有
  `conversationForStorage` 拷贝自动持久化，无需改持久化层）。

### 4.3 协议接线（ChatPage 脚本）

- `sendMessage()` 中现有 `sendAI({type:'ai.session.create', ...})` 改为
  `sendAI(sessionCreatePayload(conv))`——多出 model/effort 两字段，其余不变。
- 模型/effort onChange：若 `conv.aiSessionCreated` 且 socket 就绪（判定 =
  现有 `aiSocket && aiSocket.readyState === WebSocket.OPEN`，ChatPage.vue:643
  一带，项目里没有 `socketOpen` 标志量）→ `sendAI(sessionCreatePayload(conv))`
  立即重发；运行中禁用控件故无中途竞态。
  - 实施简化（等价超集）：`sendMessage` 每条消息前都重发 create（后端 existing
    分支对本页自管会话幂等），并加确认门（等回执再发 ai.message，5s 超时兜底
    提示）；socket 掉线期间的设置变更由此自动补同步，无需脏标记。
- `ai.session.created` handler（现仅置 `conv.aiReady=true`）追加：
  `if (e.project_path) conv.projectPath = e.project_path;`（回显是服务端
  `resolveAgentAICWD` 清洗后的绝对路径，同步回 UI）；model/effort 同理回读
  （服务端不清洗这两个值，回读仅为多端一致）。
- 新增 create 阶段错误分支：`ai.error` 到达时若**无 `message_id`**（单条件：
  运行期所有错误出口都带 message_id，create 期全不带；「无 running 气泡」不可
  作条件——sendMessage 在发 create 前已推入占位气泡。该判定只覆盖 create 与
  ai.message 流量；`ai.approval.response` 出错理论上 message_id 可为空，实践
  不可达）→ 设置条下方渲染一次性错误（`e.error` 原文），确认门拦住
  `ai.message`；目录未锁定，纠正路径后下次发送自动重试；该错误不进消息流。
- 最近项目数据：ChatPage 挂载时（或首次点开目录面板时）调
  `getAgentSessions()`（`services/agentApi.js` 已有），失败静默空列表。选
  「首次点开面板时拉取 + 内存缓存」：避免每次进聊天页多一个请求。

### 4.4 协议文档补录（后端仅文档行）

`app/http/models/agent_protocol.go:284` 的 `ai.session.create` Optional 清单
缺 `effort`（handler `agent_ai.go:1488` 实际读取；`context_window` 等亦未列但
本单不扩面）。追加 `effort` 一项，使契约表与实现一致。

### 4.5 i18n（`app/website/src/i18n/zh.js` / `en.js`）

新增 key（中/英各一份，约 10 个）：`chat_settingsDirectory`（工作目录/
Directory）、`chat_settingsModel`（模型/Model）、`chat_settingsEffort`
（推理力度/Effort）、`chat_pathDefault`（默认（home）/Default (home)）、
`chat_recentProjects`（最近项目/Recent projects）、`chat_customModel`
（自定义…/Custom…）、`chat_customPathPlaceholder`（输入 agent 机器上的绝对路径/
Absolute path on the agent machine）、`chat_pathConfirm`（确定/Confirm）、
`chat_createFailed`（会话创建失败/Failed to create session）。
既有 key 复用：无。

## 5. 错误处理与边界

| 场景 | 行为 |
|---|---|
| 手输路径不存在/是文件/是 symlink 悬挂 | create 回 `ai.error`（`resolveAgentAICWD` → `cleanExistingAgentDirectory` 报错），设置条下方一次性内联提示原文；确认门拦住 ai.message（不落 home 跑）；目录保持可改（dirLocked 未置位），纠正路径后下一条消息重发 create 成功 |
| 路径含 `~` | 服务端 `ExpandHomePath` 已展开（`agent_execution_guard.go:360`），前端原样透传 |
| 空 projectPath | 服务端落 home（`resolveAgentAICWD` :8669-8675），UI 显示「默认（home）」 |
| `/api/agent/sessions` 失败/离线 | 最近项目空列表，手输路径不受影响 |
| 运行中改设置 | 控件 disabled，不可能发生；回合结束恢复 |
| 旧 localStorage 会话（无新字段） | 字段 undefined → 载荷 `|| ''` 归空值，行为与今天完全一致（向后兼容） |
| `ai.session.created` 回显与本地值不一致 | 以回显为准写回（服务端是路径清洗权威） |
| 同一会话先 claudecode 后 agentcore | 目录/模型/effort 语义两 provider 通用，无分支 |

## 6. 测试与验收

**单元（vitest，`app/website/src/utils/chatSettings.test.js`）**

1. `recentProjectsFromSessions`：去重、最新优先、limit 截断、非数组/null 容错、
   空路径过滤；
2. `isDirectoryLocked`：`aiSessionCreated` 真/假、有消息/无消息、null conv；
3. `sessionCreatePayload`：model/effort 空值归 ''、provider 缺省 claudecode、
   字段齐全性。

**构建与回归**：`npm test`（vitest 全量）+ `npm run build` 通过；既有 112+
用例零回归。

**E2E 冒烟（新建 spec 文件；仓库现无任何 chat WS stub 代码——`auth-consistency.spec.js`
只有 `page.route('**/api/**')` + init-script 的模式可参照，捕获 `send()` 帧、注入
`ai.session.created`/`ai.error` 的 WebSocket fake 是本单新写的工作量，不算复用）**：

- 设置条渲染三项控件；新建会话目录可选、可手输；
- 模型选 `glm-5.2` 后发送消息，捕获的 `ai.session.create` 帧含
  `model:'glm-5.2'`；
- 已建会话改模型触发第二次 create 帧（同 session_id）；
- 手输坏路径 → `ai.error` stub → 设置条下方出现错误且下次发送重发 create。

**人工验收清单**（部署 liang-dev 后）

1. 新建 agentcore 会话，设置条三控件可见；目录下拉列出本机历史项目；
2. 手输一个不存在路径发消息 → 内联报错（非静默），路径纠正后发送成功；
3. 选中有效目录发消息 → 回答内容体现项目上下文（如问到当前目录结构）；
4. 模型切 `glm-5.2` 再发消息 → journal session 行 model 字段变化（开发者核对
   `.aliang/ai-sessions`）；
5. effort 切 high/xhigh 各发一条 → 回答风格有差异、无 effort 被拒错误；
6. 发送过消息的会话：目录只读、模型/effort 仍可改且下一条生效；
7. 刷新页面 → conv 三字段从 localStorage 恢复；
8. 390px 宽度下设置条不溢出、控件可触控；claudecode 旧会话行为不变。

## 7. 风险

| 风险 | 缓解 |
|---|---|
| `ai.error` 无 message_id 判定 create 阶段失败不够严谨（run 中错误也可能先于气泡？） | 双条件：无 running 气泡 **且** 无 message_id；误判的代价仅是多一条内联提示+多一次 create 重发，无状态破坏 |
| 模型清单漂移（glm 网关后续加新档） | 常量单文件 + 自定义输入兜底，过时只需改一处 |
| `effort` 值服务端不校验白名单（内核静默丢弃） | 前端下拉与 `NormalizeEffortLevel` 白名单同源，超集值选不到 |
| ChatPage 已 1100+ 行 | 本单只加设置条 UI + 接线，逻辑进 `chatSettings.js`；不再给 ChatPage 增负（文件拆分超出本单范围） |

实现注记：目录控件用 CSS 端截断（truncate，max-w-[16rem]）而非中段省略；
目录面板 Esc 仅绑手输框、点击遮罩只盖设置条行。
弹层面板与遮罩设在滚动行外层（overflow 裁剪），遮罩 fixed 全屏点击关闭；目录面板 Esc 在面板层拦截并阻断冒泡（防关整页）。
