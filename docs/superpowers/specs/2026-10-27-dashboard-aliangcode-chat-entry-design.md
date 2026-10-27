# 仪表盘 AliangCode 聊天入口 — 设计文档

日期：2026-10-27
状态：设计稿（待评审）
范围：仅前端（`app/website`），后端零改动
前置事实：agentcore（内置 golang 内核）聊天管线已就绪（`runAgentCore`，
`app/http/services/agent_ai_agentcore.go`），手机端「AI 会话」与仪表盘走同一
`/api/agent/ai/stream` 通道；`ChatPage.vue` 全屏聊天页已存在但**从未被挂载**——
`App.vue:29-31` 的 `<ChatPage v-if="currentPage === 'chat'" />` 与仪表盘按钮同属
一处 `Chat feature hidden temporarily` 注释块，`useNavigation.showChat()` 因此
零调用方。

## 1. 背景与目标

仪表盘侧栏「快速配置」与「更多设置」之间曾有一个「快捷聊天」按钮，如今被包在
HTML 注释里（`DashboardPage.vue:164-175`，标注 `Chat feature hidden temporarily`），
其点击处理器 `openQuickChat` 在脚本中已不存在——注释块不参与渲染与编译，属于
惰性残留标记；一旦去掉注释就会因未定义处理器暴露问题。挂载点
`App.vue:29-31` 同样被注释，两处必须一起恢复，否则点了入口只得到空白主区域。

**目标**

- 复活该入口，指向 **AliangCode（agentcore）** 聊天：点击进入现有 ChatPage，
  新会话预置 `provider=agentcore`（2026-10-26 用户拍板：复用 ChatPage，不另造 UI）；
- 桌面侧栏与移动端可折叠面板快捷工具区**两端都要有**（用户拍板）；
- 历史会话照旧列出、各按自己的 provider 继续；入口只影响「新建会话」的默认
  provider（用户拍板：预置新会话，不做 provider 过滤视图、不做进入即新建）；
- 顺手补上**会话重命名**（用户确认本单需要：对话列表已有，改名没有）。

**非目标**

- **跨设备选择**（用户拍板：不进本单，单独立项）。当前聊天 WS 直连本机 agent，
  AI 内核跑在打开仪表盘的机器上；跨设备需服务端中继改造，量级不同。
- agentcore 的 `/` 斜杠指令面扩展（内核无 claude/codex 插件概念，后续另议）。
- provider 过滤视图 / 会话列表分组改造。
- 登录提示文案里遗留的「快捷聊天」字样清理（`auth_loginPrompt` 等）。
- 服务端、PhoneServer、agent 任何改动。

## 2. 方案取舍

| 方案 | 结论 |
|---|---|
| **A. 复活按钮 + 复用 ChatPage + 导航层传 provider 预置（本设计）** | ✅ 零新 UI、零后端改动；预置值经模块级 ref 传递，随页面刷新自然重置，无需持久化 |
| B. 为 AliangCode 新建专属聊天页 | 否：WS/流式/审批逻辑与 ChatPage 完全重复，双轨维护 |
| C. 仪表盘内嵌聊天面板 | 否：与全屏 ChatPage 双轨，且窄栏塞不下消息流 |
| D. 用 URL query / 路由传参 | 否：项目是无路由 SPA（`currentPage` ref 切页），引入 query 解析不值当 |

## 3. 架构与数据流

```
DashboardPage 按钮
   └─ showChat({ provider: 'agentcore' })
        └─ useNavigation: chatProviderPreset.value = 'agentcore'; currentPage = 'chat'
             └─ App.vue <ChatPage v-if="currentPage === 'chat'" />   ← 本次解注释
                  └─ createConversation(): provider = chatProviderPreset.value || 'claudecode'
                       └─ ai.session.create { provider: 'agentcore', mode: 'vibe' }
                            └─ 后端 runAgentCore(...)  ← 既有管线，零改动
```

`chatProviderPreset` 为模块级 `ref('')`：单页应用内跨组件共享；浏览器刷新后重置为
空 → ChatPage 新建会话回到 `claudecode` 默认，不引入陈旧状态。

**预置值只在页面生命周期内有效**：首次从入口进入后，本次页面会话中再点「+新建
对话」都继承 `agentcore`（入口语义即「AliangCode 聊天」，页内新建理应同源）；
刷新页面回到 `claudecode` 默认。ChatPage 无 provider 选择器，这是已知取舍，
不做页内切换。

后端侧确认：`agentAIUseAgentCore(provider)`（`agent_ai.go:3900`）只认显式
`"agentcore"`，不匹配 `auto`（`auto` 走独立的 `agentAIUseAgentCoreFallback`,
`:3921`）；故预置必须传字面量 `agentcore`，不能依赖 auto 派发。

## 4. 详细设计

### 4.1 挂载点恢复（`app/website/src/App.vue`，必须先做）

去掉 `App.vue:29-31` 的 `Chat feature hidden temporarily` 注释块，恢复：

```vue
<ChatPage v-if="currentPage === 'chat'" />
```

- `ChatPage` 组件（`App.vue:41`）与 `const { currentPage } = useNavigation()`
  （`:57`）均已就位，`currentPage` 目前只因这一行被注释而无人消费；
- 这是整条链路的硬前置：不做这一步，`currentPage = 'chat'` 只会渲染出空白主区域，
  §6 验收清单第 4 项必然失败。

### 4.2 导航层（`app/website/src/composables/useNavigation.js`）

- 新增模块级 `const chatProviderPreset = ref('')`；
- `showChat(options)` 接受可选 `{ provider }`，有值则写入 preset，无值置空；
- 在返回值中暴露 `chatProviderPreset`（ChatPage 读取）。

改动约 6 行，与现有 `showPage` 单页模式同构。

### 4.3 仪表盘入口（`app/website/src/components/DashboardPage.vue`）

**桌面侧栏**（替换 164-175 行注释死块）

- 结构与「快速配置」「更多设置」按钮完全一致（`w-full flex items-center gap-3
  px-4 py-2.5 bg-white ... rounded text-sm font-medium hover:border-primary`）；
- icon `smart_toy`（`material-symbols-outlined text-slate-400 text-lg`）；
- 文案 `t('dash_aliangChat')`；
- `:disabled="!isAuthenticated"`，disabled 样式与快速配置同款
  （`cursor-not-allowed opacity-60`）——与「快速配置」同门禁，未登录不可用；
- `@click="showChat({ provider: 'agentcore' })"`。

**移动端面板**（快捷工具块，紧随快速配置按钮之后）

- 同款按钮，`px-4 py-3`（触控高度 ≥40px，遵守
  `docs/superpowers/specs/2026-10-08-mobile-responsive-design.md` 约定）；
- 同样受 `!isAuthenticated` 禁用约束。

`useNavigation` 解构处（`DashboardPage.vue:1027`）追加 `showChat`。

### 4.4 ChatPage 接入（`app/website/src/components/ChatPage.vue`）

**provider 预置**

- `createConversation()`（:410）新建会话对象时：
  `provider: chatProviderPreset.value || 'claudecode'`；
- 其余逻辑不变（`ai.session.create` 已在 :928 使用 `conv.provider`）；
- 取 preset 的读取点在 `ChatPage.vue` 脚本顶部——该文件（`:266`）已
  `import { useNavigation }` 并解构 `{ showDashboard }`（`:272`），**只追加
  `chatProviderPreset` 到既有解构**，不新增 import。

**斜杠指令面屏蔽**

现状：`slashProviderOf()`（:319）把一切非 codex 映射为 `'claude'`，agentcore 会话
会去拉 claude 的 `/` 指令面并展示——而这些命令（plugins、项目级命令）在内核里
并不存在，属于误导。

处理（**纯客户端抑制：根本不发请求**）：

- `slashProviderOf()` 对 `agentcore`（含 `agent-core` / `agent_core` 归一）返回
  空串 `''`。注意：后端只把**字面量** `"agentcore"` 路由到内核
  （`agent_ai.go:3900`），`agent-core` / `agent_core` 实际不会跑内核，这里的归一
  只是防御性宽松匹配——**不要**反过来据此放宽后端的 provider 判定；
- `ensureSlashCommands()`（:513）在 provider 为空时直接 return——**不得**带着
  空 provider 发 `slash.commands.list`：后端 `normalizeSlashProvider`
  （`agent_slash_commands.go:154`）把未知/空值当作「未指定」，会返回
  claude+codex+opencode 的**全部**命令，正好是要避免的误导；
- `onSlashInput()` 在 provider 为空时不打开补全面板（`/` 按普通文本发送）；
- 既有 `slashCommandsByProvider['']` 自然为 `undefined` → 列表为空，无需额外分支。

即：屏蔽靠「客户端不发、不显示」，与后端 `normalizeSlashProvider` 的
「空值=返回全量」语义**方向相反**，不能以此作为实现依据。

### 4.5 会话重命名（新增）

**数据**：仅 `conv.title` 字段，落 localStorage（`saveToStorage()` 既有逻辑）。
不同步 native 会话、不同步服务端——**这是刻意取舍**：CLI 侧改名体系（pid 文件 /
index customTitle / 跨仓 `ai.session.rename`）坑多且有并发问题，本单不碰。

**交互**：

- 会话列表项：hover 时右侧出现铅笔图标（移动端常显），点击进入行内编辑；
- 顶栏标题：同样加铅笔图标，点击进入行内编辑；
- 编辑态为 `<input>`：Enter 提交、Esc 取消、失焦提交；
- 提交时 `title.trim()` 后写入；**清空则存空串 `''`**，由既有渲染口径
  `c.title || t('chat_emptyTitle')` 在渲染期兜底显示「新对话」。严禁把
  `t('chat_emptyTitle')` 的译文写进 `conv.title`：那会把某个语言的文案固化进
  localStorage，切语言后标题不跟随；
- 「首条消息前 20 字」自动标题逻辑（`:907`）不变——它只在 `!conv.title` 时触发，
  用户清空标题后下一条消息会重新生成，这是可接受的既有行为；
- 长度上限 60 字符（与列表 truncate 视觉匹配）；
- 编辑输入框点击不得触发 `selectConversation`（`@click.stop`）；
- **列表行结构必须改造**：三个分组（今天/昨天/更早）的行当前是
  `<button type="button" @click="selectConversation(c.id)">` 包一个
  `<div class="truncate">`（`ChatPage.vue:30-38`、`:42-51`、`:55-64`）。把铅笔按钮或
  行内 `<input>` 塞进 `<button>` 内部违反 HTML 内容模型（交互元素不得嵌套），
  `@click.stop` 只能挡冒泡、挡不住非法的 DOM 嵌套与焦点/激活异常。三处需一并改为
  非按钮容器（`div role="button" tabindex="0"` + `@keydown.enter` / `@keydown.space`，
  保留原 `@click` 处理器），编辑态与铅笔作为兄弟节点置于容器内；
- 改名不改变 `updatedAt`（避免重命名把会话顶到「今天」分组首位——分组按
  `updatedAt` 计算，改名不是内容更新）。

**旧数据兼容**：无 `title` 的历史会话渲染与改名均照旧工作。

### 4.6 i18n（`app/website/src/i18n/zh.js` / `en.js`）

新增 key（两语言必须同时添加，`i18n.test.js` 校验 key 对齐）：

| key | zh | en |
|---|---|---|
| `dash_aliangChat` | AliangCode 聊天 | AliangCode Chat |
| `chat_rename` | 重命名 | Rename |
| `chat_renamePlaceholder` | 输入会话名称 | Enter a conversation name |

同时**删除** `dash_quickChat`（`zh.js:403` / `en.js:403`）：注释块移除后它不再有
任何引用。现有 i18n 死 key 断言只覆盖 `qs_` 前缀，删除与否都不会让测试失败，
属于顺手清理。

**注意**：`dash_quickChat` 与登录提示里的「快捷聊天」字样（`auth_loginPrompt` 等）
无关，后者不在本单范围。

## 5. 错误处理与边界

- **未登录**：按钮禁用（与快速配置一致），不改现有行为；
- **平台代理未起**：后端 `agentCoreEnsureUpstream` 自愈一次，失败则页面收到
  `ai.error`，ChatPage 既有错误渲染承担——本单不新增处理；
- **provider 预置残留**：模块级 ref 在页面会话内保持（页内「+新建对话」同源），
  刷新后重置为 `claudecode`；ChatPage 无页内 provider 切换，见 §3 已知取舍；
- **旧 localStorage 会话**：`provider` 字段缺失时按 `claudecode` 兜底（既有行为）。

## 6. 测试与验收

**单测（vitest，`app/website/src`）**

- `useNavigation`：`showChat({provider})` 写入 preset；无参调用清空 preset；
- 若重命名纯逻辑抽成函数（建议抽 `normalizeConversationTitle(raw)`）：
  空串/空白 → 返回 `''`（渲染期兜底，不写入译文）；超长截断 60 字符；首尾空白剥离。

**回归**：跑 `app/website/tests/e2e/mobile-responsive.spec.js`。该文件不只断言快速
配置弹窗——它还覆盖仪表盘布局、控制面板折叠、设置子页、Agent 入口与登录弹窗
（`:46-145`），正是本单改动的移动端面板所在区域，属于必跑的回归网。

**不新增**：不为入口按钮单独写 e2e（无独立路由，入口可由上述回归网覆盖）。

**手动验收清单**

1. 登录后，桌面侧栏「快速配置」与「更多设置」之间出现「AliangCode 聊天」；
2. 移动端展开控制面板，快捷工具区出现同款按钮，触控可达；
3. 未登录时按钮置灰不可点；
4. 点击进入 ChatPage（**主区域正常渲染，不是空白**），新建会话说一句话 → 后端走
   agentcore（证据：`~/.aliang/ai-sessions` journal 出现该 session 的
   header/user/assistant 行）；
5. 输入 `/` 不弹斜杠补全面板（agentcore 会话）；
6. 列表与顶栏均可重命名，Enter 生效、Esc 取消；**清空标题后显示「新对话」而非
   固化的旧译文**；刷新后标题保持；
7. 旧会话（provider=claudecode）点开仍按 claude 继续，斜杠补全正常；
8. 移动端 390px 宽度下侧栏抽屉与入口布局不破。

## 7. 风险

| 风险 | 缓解 |
|---|---|
| agentcore 仅认 glm / 回环 56432，用户以为和终端 claude 一样 | 非本单引入（既有能力矩阵），验收清单 4 用 journal 佐证走的是内核 |
| 本地标题与 native 会话标题不一致，用户困惑 | 本单明确只做本地标题；若后续要同步，按 CLI 改名体系单独立项 |
| 移动端面板新增按钮挤压布局 | 沿用既有 `space-y-3` 流式布局，按钮 `py-3`，e2e 已在 390/768/1280 三档守护 |
