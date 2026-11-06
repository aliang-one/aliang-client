# Agent 标签页三段式重构设计

- 日期：2026-10-13
- 状态：已获用户批准的设计稿（待实现）
- 范围：设置页 Agent 标签页（前端 Vue）+ 终端回放后端端点（Go）
- 不改动：仪表板主页（DashboardPage.vue）、设置页其它标签页、ConfigSyncSettings（配置同步中心）

## 1. 背景与问题

用户反馈「agent 页面重复信息太多」。经排查，重复全部集中在设置页 Agent 标签页内部
（`SettingsPage.vue:173-201` 纵向堆叠的子组件），仪表板主页不含任何 agent 内容。

### 现状重复清单

| # | 重复点 | 位置 |
|---|---|---|
| 1 | 状态徽章渲染 3 次，其中 2 处判定逻辑逐行重复（runtime.online+enabled+bound） | AgentSettings.vue:11-14、AgentActivitySettings.vue:11-15 |
| 2 | `GET /api/agent/status` 同页拉 2 遍（各自 mounted 独立拉，无共享 store） | AgentSettings.vue:479-488、AgentActivitySettings.vue:373-379 |
| 3 | AI 会话列表双份：实时卡与历史列表字段几乎相同，今日会话两处同现 | AgentRuntimeSessions.vue:57-80 vs AgentActivitySettings.vue:99-183 |
| 4 | 注册/同步健康度双轨，「开启」「重试注册」打同一 `POST /api/agent/enable` | AgentSettings.vue:84-90 vs AgentActivitySettings.vue:34-75 |
| 5 | 设备状态在 AgentSettings 内部重复 3 次 | :52-55、:70-76、:124 |
| 6 | 弹窗「启动前置条件」复述卡头+四格已表达的状态 | :146-159 |

### 用户关注的三个信息

1. agent 状态 —— 有，但重复 3 处
2. 历史 agent AI 上下文对话 —— 已有完整实现（`GET /api/agent/sessions` 列表 + `GET /api/agent/session` 分页转录回放），但被实时列表稀释
3. terminal 对话 —— **后端缺口**：只有元数据列表（`GET /api/agent/runtime-sessions` 的 terminals），对话内容（scrollback）仅存于内存 ring buffer 并经远程 WebSocket 提供手机端，无任何 HTTP 端点

### 同步语义（小字提示的事实依据）

同步到平台的载体 `agentSyncSnapshot`（app/http/services/agent_inventory.go:68-80）中：
VibeSessions 经 `summarizeAgentVibeSessions`（agent_inventory.go:220）剥离全部 Transcript，
Projects 仅含 name/path/branch/language/file_count/git_changed_count/total_size/readme 摘要等 meta。
**代码内容与对话内容从不离开本机**，提示文案可如实声明。

注意：设置页另有一个「配置同步中心」（ConfigSyncSettings.vue）确实会上传配置文件正文
（`models.CloudConfigBatch{Configs: []SoftwareConfig}`，SoftwareConfig 含 content），**不得**在该区块写
「只同步 meta」。本设计不触碰该组件。

## 2. 目标

- Agent 标签页信息架构对齐用户三个关注点：状态 → AI 会话 → 终端
- 消除上表 6 项重复
- 终端可查看对话输出（补后端端点）
- 「历史同步」区块加小字，明示仅同步项目 meta、不上传代码，消除用户疑虑

## 3. 设计

### 3.1 共享状态：`useAgentStatus` composable（新增）

新文件 `app/website/src/composables/useAgentStatus.js`：

- 模块级单例 ref 持有 `/api/agent/status` 响应 + loading/error 状态
- `ensureLoaded()`：幂等——已有数据或请求进行中则直接返回，不重复拉取（消除双拉）
- `refresh()`：强制重新拉取（启停/重试注册等变更后调用）
- 状态判定逻辑（runtimeOnline+enabled+bound → 运行中/已开启/已关闭）收敛为 composable 导出的 computed，
  消灭两处逐行重复的判定代码
- 工具可用性摘要（`工具 2/3`）与同步状态摘要同样收敛为 composable 导出的 computed，
  避免在组件里第三次重算
- 出错时暴露 error，由消费方展示

### 3.2 状态卡（重写 AgentSettings.vue 卡片主体）

- 保留：标题 + 状态徽章（全页唯一一处）+ 启停按钮 + 设置按钮
- 四格统计压缩为一行 meta：`设备 xx · 工具 2/3 · 同步已注册`
  （设备状态 3 处 → 1 处；「在线」格语义与徽章重复故删除）
- 注册/连接健康度：**只在状态卡呈现**（AgentActivitySettings 的健康度区块整个移除，见 3.6）。
  正常时仅一行摘要；出错态（rejected/login_required/error/disconnected 等）展开红色详情
  + 重试按钮。动作入口收敛：状态卡的「开启/关闭」是唯一启停入口，「重试注册」仅出错态可见
- 设置弹窗内：
  - 设备信息块保留（device_id、last_seen_at/bound_at/paired_at、capabilities 等——此处是详情不是重复）
  - 「启动前置条件」清单改为**仅显示未通过项**，全部通过则整块隐藏
  - 历史同步区块保留（见 3.5）

### 3.3 AI 会话：单一列表（改 AgentActivitySettings.vue，删实时卡）

- 以历史列表（按天分组）为唯一列表；数据仍来自 `GET /api/agent/sessions`
- 从 `GET /api/agent/runtime-sessions` 取 `ai_conversations`，按 session id 与历史列表匹配：
  - 命中 → 加「进行中」徽章并置顶到列表头部
  - runtime 里有而历史列表没有（刚创建、尚未被扫描入库）→ 直接插入列表顶部
- runtime-sessions 轮询保留（仅服务于「进行中」徽章的时效性），历史列表分页/加载更多逻辑不变
- 转录回放仍走 `GET /api/agent/session`，现有脱敏逻辑（滤内部命令标记与 ws-vb-phone 域名）不动
- `AgentRuntimeSessions.vue` 删除 AI 实时列表部分，瘦身为纯「终端」卡（保留 5s 轮询与刷新按钮）

### 3.4 终端：列表 + scrollback 回放（含后端）

**后端新端点** `GET /api/agent/terminal/{sessionID}/scrollback?limit=<bytes>`

- 路由注册：app/http/routes/routes.go（/api/agent 组内）；handler：app/http/handlers/agent_handler.go
- service：`agentTerminalManager` 新增 `scrollbackSnapshot(sessionID string, maxBytes int)`：
  - live 会话：m.mu 下取 session 引用，持 `outputGate` 期间 `ring.snapshot()`（遵守既有单向锁序
    m.mu → outputGate，见 agent_terminal.go:887-889 注释）
  - 墓碑会话（`agentTerminalHistory.ring`）：m.mu 下直接 snapshot
  - 两处皆无 → 404 `{error: "gone"}`
  - `agentTerminalManager` 为非导出类型，handler 所在包通过既有 `h.service.XXX` 模式的导出包装函数调用
    （如 `services.AgentTerminalScrollback(...)`），命名在实现计划中定稿
- 响应：`{session_id, encoding:"text", status:"live"|"exited", data, truncated, captured_at}`
- limit：默认 64KiB、上限 512KiB；超限取尾部字节并置 `truncated:true`
  （ring 默认容量 floor 64KiB，`ALIANG_TERMINAL_RING_BYTES` 可配置更大，故需 limit 裁剪）
- 鉴权：随现有 `/api/agent/*` 仪表盘 middleware（dashboard cookie session）

**前端**：终端卡每行加「查看输出」按钮 → 弹窗 `<pre>` 展示 scrollback（等宽、内滚、可复制），
弹窗头部标 live/exited 与截断提示；弹窗遵循移动端约定（flex-col + min-h-0 内滚、触控目标 ≥40px）。

### 3.5 同步小字提示

- i18n 新键（zh.js/en.js 成对新增，保持两文件行号对齐约定）：
  - `agent_historyMetaHint`
    - zh：「仅同步项目与会话的元信息摘要（目录、文件数、大小等），不会上传任何代码或对话内容。」
    - en：「Only metadata summaries of projects and sessions (directories, file counts, sizes) are synced. No code or conversation content is ever uploaded.」
- 插入点：AgentSettings.vue 设置弹窗「历史同步」区块标题/摘要行下方（:163-165 区域），
  样式 `text-[10px] text-slate-400` 小字
- 顺带：`agent_preRegisteredOk`（zh.js:962）句尾追加「（仅元信息摘要，不含代码）」，en 同步
- ConfigSyncSettings（配置同步中心）不动

### 3.6 涉及文件总览

| 文件 | 动作 |
|---|---|
| app/website/src/composables/useAgentStatus.js | 新增 |
| app/website/src/components/settings/AgentSettings.vue | 重写卡片主体 + 弹窗前置条件改为未通过项 + 同步小字 |
| app/website/src/components/settings/AgentActivitySettings.vue | 吸收「进行中」徽章合并；**健康度区块整个移除**（健康度只在状态卡呈现）；改用 useAgentStatus |
| app/website/src/components/settings/AgentRuntimeSessions.vue | 删 AI 实时列表，瘦身为纯终端卡 + 「查看输出」弹窗 |
| app/website/src/i18n/zh.js、en.js | 新增 agent_historyMetaHint；agent_preRegisteredOk 追加半句 |
| app/http/services/agent_terminal.go | 新增 scrollbackSnapshot |
| app/http/handlers/agent_handler.go | 新增 scrollback handler |
| app/http/routes/routes.go | 注册 GET /api/agent/terminal/{id}/scrollback |

## 4. 错误处理

- `/api/agent/status` 拉取失败：状态卡显示离线/未知徽章 + 重试入口（现有行为保持，不新增弹窗）
- scrollback 端点 404（会话已消失/墓碑过期）与其它失败：统一为弹窗内一行错误提示 + 可点「刷新」重试，不弹报错框（实现上合并为单一 `agent_live_outputLoadFailed` 文案）
- scrollback 拉取超时/网络错：弹窗内错误行 + 重试按钮
- runtime-sessions 失败：会话列表照常展示历史数据，仅「进行中」徽章缺失，不阻塞主列表

## 5. 测试

- Go（新增）：
  - `agentTerminalManager.scrollbackSnapshot`：live ring 快照、墓碑快照、gone、limit 截断（truncated=true）、
    大 ring 裁剪取尾部、锁序（不嵌套持 outputGate 时获取 m.mu）
  - handler：405 与缺 sessionId 分支（404 为 service 返回的直通分支；limit clamp 在 service 层测试）
- 前端：
  - e2e 哨兵 `tests/e2e/mobile-responsive.spec.js`（390/768/1280）必须通过
  - Agent tab 三段在 390px 宽下可用；弹窗内滚不溢出（min-h-0/dvh 约定）
- 回归：设置页 Agent tab 手动走查——未登录占位、登录后状态卡/会话/终端三段渲染、启停、回放

## 6. 流程约束（项目既定）

- 开发在 git worktree 分支进行，不直接改 master 工作区
- 提交信息全中文「新增：/修复：」格式；未经用户明确要求不执行 git commit
- 移动端适配约定：md: 断点还原桌面布局、弹窗 flex-col + min-h-0 内滚、dvh、触控 ≥40px

## 7. 明确不做（本次）

- 仪表板主页（DashboardPage.vue）的任何重复/死代码清理
- `GET /api/agent/tools` 废弃、AgentStatusResponse legacy 字段清理等后端去重
- ConfigSyncSettings（配置同步中心）的文案与改名
- WS 实时终端流接入前端
