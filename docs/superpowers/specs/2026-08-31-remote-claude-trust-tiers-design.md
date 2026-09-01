# 设计：远程 Claude 会话三档信任（本地能力对齐）

- 日期：2026-08-31
- 状态：已评审（设计对话确认），待实现
- 范围：本仓库（agent 客户端）`app/http/services` 中 claude headless 执行链；服务端（仓库外）需配套下发 `trust_level`

## 1. 背景与问题

网关以 headless 模式逐轮拉起真实 claude CLI：`claude --print --verbose --output-format stream-json --include-partial-messages`（agent_ai.go `newClaudeCodeAITool`）。远程策略启用时（`claude_remote_policy`），`withClaudeRemotePolicy` 强制 `--setting-sources ""`，导致远程会话与本地交互式存在能力落差。

### 1.1 实证基线（2026-08-31，claude 2.1.156，macOS，临时项目含 demo-skill / hello.md / .mcp.json）

| 配置 | 工具数 | `Skill` 工具 | MCP servers | 用户/插件 skills | 项目 skills | 项目 CLAUDE.md |
|---|---|---|---|---|---|---|
| 交互式等效（无旗子） | 33 | ✓ | 14（用户 10 + 插件 + 项目 pending） | ✓ | ✓（裸名） | ✓ |
| `--setting-sources ""`（现网关） | 27 | ✓ | 0 | ✗ | ✗ | ✗ |
| `--setting-sources user` | 33 | ✓ | 14（仅用户级；项目 .mcp.json 不加载） | ✓（125+ 条） | ✗ | ✗ |
| `--setting-sources user,project` | 86* | ✓ | 15（含项目 .mcp.json） | ✓ | ✓（裸名） | ✓ |
| `""` + 清洗插件 `--plugin-dir` | 27 | ✓ | 0 | ✗ | ✓（`aliang-project:` 前缀） | ✗ |

\* 86 vs 33 为 MCP 连接时序差异（server 连上后 mcp__ 工具计入），非 scope 差异。

补充实证：
- CLAUDE.md 与 `project` scope 绑定：`user` 与 `""` 均不加载项目 CLAUDE.md（暗号实验）；CLI 无单独注入 memory 的旗子（`--bare` 帮助文本证实 CLAUDE.md 发现随 setting-sources 关闭）。
- 清洗插件下模型可成功调用：`Skill{"skill":"aliang-project:demo-skill"}` → 输出正确。
- `disableSkillShellExecution` 是官方设置（claude v2.1.91+），禁用 skills/commands 来自 user/project/plugin 源的内联 `` !`cmd` `` shell 执行；全局开关，不区分来源。

### 1.2 合并语义事实（设计承重墙）

- 权限规则：多来源 union，评估顺序 **deny > ask > allow**（类型优先，不看 scope；CLI `--settings` 与文件来源同级）。
- Hooks：所有来源同事件 hooks **全部并跑**；命令 hook `exit 2` 硬否决。
- MCP 同名：**local > project > user**。
- skills/commands 同名：**project > user**；插件天然命名空间隔离。
- 已知版本坑：claude 2.1.x headless `--print` 不触发 PermissionRequest；用户 `permissions.ask` 规则压制 PreToolUse hook 的 approve，工具被拒（agent_ai.go 注释 + 代码已按 2.2 分界选策略）。

## 2. 目标与非目标

**目标**
1. 远程会话能力向本地交互式对齐：用户级/插件 skills、用户级 MCP、项目 CLAUDE.md、项目 skills/commands。
2. 以服务端可下发的**三档信任**表达信任差异，默认档安全边界不回退。
3. 「完全信任」档实现与本地**能力面 + 审批量一致**（能力面 = skills/MCP/CLAUDE.md/hooks 全生效；审批量 = 本地审批量。CLI 参数与本地交互式并非逐字节相同——本档仍注入审批桥 hooks 与 `--permission-mode default`）。

**非目标**
- 不实现 MCP 白名单粒度过滤（`--strict-mcp-config` 显式合并已保证确定性，细粒度过滤留待后续需求）。
- 不做 untrusted 项目 CLAUDE.md 注入（`--append-system-prompt` 补丁，YAGNI，见 §9）。
- 不改 codex / opencode 路径。

## 3. 三档信任设计

服务端在 `claude_remote_policy` 中新增 `trust_level`：

| | 隔离 `isolated` | 清洗 `sanitized`（默认） | 完全信任 `full` |
|---|---|---|---|
| `--setting-sources` | `""` | `user` | `user,project` |
| 项目 skills/commands/agents | 无 | 临时清洗插件（§4） | claude 原生 |
| 调用名 | — | `aliang-project:<name>` | 裸名（=本地） |
| 项目 CLAUDE.md / 项目 settings/hooks | ✗ | ✗ | ✓（=真本地） |
| 用户级 skills/commands/插件 | ✗ | ✓ | ✓ |
| 用户级 MCP | ✗ | ✓ 原生；受信项目 `.mcp.json` 走显式合并注入（§5） | ✓ 原生全量（含项目 .mcp.json） |
| 注入 `permissions.ask` | 现默认列表 | 现默认列表 | **不下发** |
| `disableSkillShellExecution` | 注入 | 注入 | 不注入 |
| 审批桥（hooks + `--permission-mode default`） | 注入 | 注入 | 注入 |

要点：
- 三档均保留审批桥；差别是「谁放行」：隔离/清洗档危险操作逐条弹手机；完全信任档用户 allow 规则直接放行，仅本地配置 ask/deny 的操作弹手机（=本地审批量）。
- 档位完全取代旧字段：`trust_level` 有效时（含 `full`），`project_skill_trusted` / `project_capability_mode` 不再参与判定，sanitized 档插件构建与列表暴露**无条件**执行。
- 兼容映射（`claude_remote_policy` 存在且 policy 启用时，按序判定）：
  1. `trust_level` 为 `isolated` / `sanitized` / `full` → 直接采用；
  2. `trust_level` 为其他值 → `isolated`（fail-closed）；
  3. 无 `trust_level`：`project_skill_trusted=true && project_capability_mode=sanitized_plugin` → `sanitized`；**其余一律 `isolated`**（含旧 `project_skill_trusted=false`、字段缺失、空 policy——保持旧版行为逐字节不变：旧的非受信项目升级后不会突然暴露项目能力）。
  4. `claude_remote_policy` 缺失（policy 未启用）→ 不走本机制，行为同现状。
- 2.1.x 守门：claude < 2.2.0 且档位非 `isolated` 时，**整档降级为 `isolated`**（setting-sources 回 `""`、清洗插件不构建——不是只换旗子），并通知：本地 `logger.Warn` + 在该 run 的 `ai.run.started` 事件 payload 增加扩展字段 `policy_notice`，**固定为结构化对象** `{"effective":"isolated","requested":"<原档位>","reason":"<见下>"}`。`reason` 枚举：`claude_version_below_2_2`（版本守门）、`sanitize_failed`（清洗拷贝失败）、`mcp_merge_failed`（MCP 合并失败）——**服务端解析须接受全部三个枚举值**。服务端按此结构解析。复用 `detectClaudeApprovalHookStrategy` 的**版本探测机制与缓存**（注意其返回值是 hook 策略而非版本号，降级判定需独立解析版本）。ask 列表例外：被降级的 full 档 `permissionAsk` 为空且**不重新填充默认列表**——<2.2 走 PreToolUse 策略本就不注入 `permissions.ask`，重新填充不可观测。

## 4. 清洗插件（sanitized 档）细则

### 4.1 结构与生命周期

```
$TMPDIR/aliang-claude-project-*/
├── .claude-plugin/plugin.json      {"name":"aliang-project",...}
├── skills/<name>/SKILL.md          清洗重写副本
│   └── <资源文件>                   符号链接 → 项目原文件
├── commands/<相对路径>.md           清洗重写副本
└── agents/<name>.md                【新增】清洗后的项目子代理
```

- 每 `runCLIPass` 重建：`os.MkdirTemp` → 挂 `--plugin-dir` → `defer os.RemoveAll`；无缓存（YAGNI）。
- 资源符号链接保渐进式披露（references/*.md 等）；运行中项目文件变动导致的短暂悬空可接受。
- 容量上限沿用 500/500（skill 数 / 单 skill 资源数），agents 同限；**修改点：超限由静默跳过改为 `logger.Warn`**（随 capability payload 上报暂不做——无现成通道，YAGNI；有真实排查需求时随 `policy_notice` 机制扩展）。
- 失败处理（fail-closed）：拷贝/写盘失败 → 本轮降级 `isolated` + 错误上报服务端。

### 4.2 frontmatter 白名单（白名单制，新增字段默认丢弃）

| 字段 | 处理 | 理由 |
|---|---|---|
| `name` / `description` / `argument-hint` | 保留 | 发现与展示 |
| `user-invocable` / `disable-model-invocation` | 保留 | 调用面控制 |
| `context: fork` | **改为保留** | 纯执行隔离，无权限含义 |
| `agent: <名>` | **改为保留** | 配合 agents/ 拷贝，引用不再悬空 |
| `allowed-tools` | 继续剥 | 权限授予物：2.2+ PermissionRequest 仅在「需要问」时触发，skill 自授权即绕桥（远程 RCE 通道）；ask>allow 可中和但同时压掉用户 allow，两难，故剥离 |
| `hooks` | 继续剥 | harness 直执行，不过审批桥 |
| `model` | 继续剥 | 远程模型由服务端选择 |

清洗目的边界：skill **正文**让模型做什么不清洗——模型发起的工具调用必然过桥；清洗掐的是**绕过模型的执行通道**（hooks / allowed-tools / `` !`cmd` `` 预执行）。

### 4.3 agents/ 拷贝（新增）

`.claude/agents/*.md` → 清洗后拷入插件 `agents/`：保留 `name` / `description` / `tools`（agent 的 tools 是**限制**集非授权）；剥 `hooks`、`model`。

## 5. MCP 显式合并（sanitized 档）

- `user` scope 下用户级 MCP 原生加载（含用户 settings 启用插件自带的 MCP，=本地行为）。
- 项目 `.mcp.json` 在 `user` scope 下不加载；若服务端标记项目 MCP 受信，网关合并 `~/.claude.json` **顶层 `mcpServers`（user scope，不含 `projects` 下的按项目条目）** + 项目 `.mcp.json`（同名 **project 覆盖 user**，与 CLI 本地语义一致）为临时 config，`--strict-mcp-config` 一次注入——优先级确定性 100%。
- 服务端未标记 → 不注入项目 MCP（用户级不受影响）。
- **插件 MCP（2026-09-01 已补齐）**：合并层枚举已启用插件的 `.mcp.json`（双形态：`mcpServers` 包裹式与平铺式；`enabledPlugins` 显式 `true` 才算启用，缺省视为禁用）并入 config，命名 `plugin:<名>:<server>` 与原生一致。冒烟实证：strict 注入下 15 server（用户 10 + 插件 4 + 项目 1），命名与连接状态和原生加载逐项一致。

## 6. 命名空间对齐（歧义与展示名修复）

能力验证链：`onClaudeInit` 抓 init 事件 `slash_commands` → `recordClaudeCapabilities` → `filterVerifiedClaudeCommands` 过滤手机列表。`filterVerifiedClaudeCommands` 已有**后缀回退**（`HasSuffix(capability, ":"+name)`），因此裸名列表在 sanitized 档不会被整列过滤（现有测试 `TestSlashCommandsRequireTrustAndSystemInitForProjectSkills` 已断言 `aliang-project:deploy` 能力下裸名 `deploy` 存活）。真实缺陷是：

1. **同名歧义**：user 级 `deploy` 与插件能力 `aliang-project:deploy` 会经后缀回退**互相误匹配**，列表展示与实际可调条目可能错位；
2. **展示名 ≠ 调用名**：列表发裸名 `demo-skill`，实际 Skill 调用/斜杠调用需 `aliang-project:demo-skill`，手机端按裸名下发会失败。

修改：sanitized 档生效时 `collectProjectSlashCommands` 生成的项目条目 name 以 `aliang-project:` 前缀（`source` 仍为 `project`），与 init 事件、Skill 工具调用名三方一致，歧义与错位同时消除；**同步更新现有测试中断言裸名 `deploy` 的用例**。`full` 档不变（裸名，后缀回退无歧义）。

## 7. 代码改动清单（本仓库）

| 文件 | 改动 |
|---|---|
| `app/http/services/agent_ai.go` | `parseAgentAIClaudeRemotePolicy`：新增 `trust_level` 解析与兼容映射；`policy.settingSources = nil` 保留（档位表独占 `--setting-sources`，消息级 `setting_sources` 为 legacy 丢弃——旗子构建只读档位）；`permissionAsk` 空值不再强制填默认（按档位决定）。`claudeApprovalHookSettings`：`disableSkillShellExecution` 与 `permissions.ask` 随档位（full 不注入）。**`withClaudeApprovalHook`：不再无条件把 `--setting-sources` 清回 `""`（现状 agent_ai.go:6792-6793 会吃掉档位值），改为携带 effective 档位的 setting-sources（isolated 保持 `""`）；同步更新固化该行为的测试 `TestClaudeApprovalHookDisablesFilesystemSettingsWithoutRemotePolicy`**。2.1.x 守门降级 |
| `app/http/services/agent_ai_claude_policy.go` | `withClaudeRemotePolicy`：按档位拼 `--setting-sources`；`full` 跳过插件；MCP 显式合并注入。`sanitizedClaudeMarkdown`：白名单加 `context`/`agent`。`prepareClaudeProjectCapabilityPlugin`：加 `agents/` 清洗拷贝；超限日志 |
| `app/http/services/agent_slash_commands.go` | `parseSlashFrontmatter` 增加 `context`/`agent` 字段解析；sanitized 档项目条目加 `aliang-project:` 前缀（含更新现有裸名断言测试）；**`includeProjectClaude` 门控（agent_slash_commands.go:52）改为按 effective 档位判定**（现状基于 `projectSkillTrusted`，full 档旧字段缺失会误排除项目条目）；降级经 `policy_notice` 上报（超限仅日志，见 §4.1） |

服务端（仓库外）：`claude_remote_policy.trust_level` 下发；手机端按 session/project 设置档位，`full` 需显式确认。

## 8. 数据流

```
手机端设置项目信任档 → 服务端落库 → run 消息携带 claude_remote_policy.trust_level
→ parseAgentAIClaudeRemotePolicy 映射 {settingSources, sanitize, askList, skillShell}
→ runCLIPass: withClaudeRemotePolicy（--setting-sources / --plugin-dir / --strict-mcp-config）
→ withClaudeApprovalHook（桥 hooks + 按档位 permissions）
→ claude 进程 → init 事件（onClaudeInit 校验能力）→ slash 列表对齐下发
```

## 9. 风险与已知限制

1. **full 档 = 项目 settings/hooks 全生效**：用户在手机端显式确认后才启用；误信恶意仓库的后果与本地 `cd` 进该仓库开 claude 相同。
2. **2.1.x 无法安全对齐**（ask 压制无解）：守门自动降级；建议 agent 机 claude 升级 ≥ 2.2。
3. **OAuth MCP 无法远程授权**（硬伤）：本地 `/mcp` 预授权一次，凭据 `~/.claude/.credentials.json` 复用。
4. **跨轮后台状态丢失**（进程模型硬伤）：不在本设计范围。
5. **`disableSkillShellExecution` 全局开关**：sanitized 档用户自己的 `` !`cmd` `` skill 同被降级（模型改用 Bash 过桥），接受。
6. **env 优先级（2026-08-31 已实证）**：`--settings`/settings 文件的 `env` 块**压过进程继承 env**（nc 监听实测：请求打到 settings 指定端点而非进程 env 端点）。即 sanitized/full 档下用户 settings env 将决定模型路由 = 本地一致（可接受）；但服务端**不能**依赖进程 env 强制路由，需通过档位说明向用户暴露这一点。
7. **sanitized 档显式合并只覆盖 user + project 两源**：local scope（`~/.claude.json` 按 project 的 `mcpServers`）在 `--strict-mcp-config` 注入下不会加载，属接受的能力收缩（非遗漏）。
8. untrusted 项目需要 CLAUDE.md 的场景：暂以 `full` 档覆盖；`--append-system-prompt` 注入补丁留待真实需求。
9. ~~**插件 MCP 合并缺口**~~（已关闭，2026-09-01）：`claudeTierMCPArgs` 现枚举已启用插件（`enabledPlugins` 门控）并入合并 config，冒烟实证 15/15 对等（§5）。
10. **无头内置安全命令层（冒烟 G 系列副产品）**：claude 2.1.x 无头模式对 `echo`/`whoami` 等只读安全命令**免审批直接执行**（与档位无关，isolated 亦然）。审批桥只对 mutating 命令触发（`touch` 实测：注入 allow 放行 / 无规则无头拒绝）。对用户是好事（弹窗更少），服务端审批量预估需按此校准。
11. ~~**slash 列表路径不经版本守门**~~（已关闭，2026-09-01）：列表已接入 init 快照版本守门——快照 <2.2 或不可解析时 effective tier 按 isolated 生效（项目条目排除）；残留窗口仅剩「尚无快照」（首次 run 前，以 `verified:false` 呈现、首次 run 后自纠）与「快照滞后于二进制升级」（fail-closed、下次 run 自纠），均已注释于 agent_slash_commands.go。列表是展示层，run 路径的守门才是执行边界。

## 10. 测试计划

**单元测试**（Go，现有 `_test.go` 风格）
- 三档旗子矩阵：`--setting-sources` / `--plugin-dir` / `--strict-mcp-config` / `permissions` / `disableSkillShellExecution` 组合断言——**必须覆盖 `withClaudeApprovalHook` 之后的最终 args**（而非仅 `withClaudeRemotePolicy` 输出），防止 hook 层再次改写 setting-sources；更新固化旧行为的 `TestClaudeApprovalHookDisablesFilesystemSettingsWithoutRemotePolicy`。
- 兼容映射：旧字段 `trusted=true && mode=sanitized_plugin` → sanitized；**旧 `trusted=false`（含 mode 缺失/未知）→ isolated**；`trust_level: full` → full（且旧字段不再参与）；未知 `trust_level` 值 → isolated；三者皆缺 → isolated；`claude_remote_policy` 缺失 → 机制整体不启用。
- 白名单：`context`/`agent` 保留，`allowed-tools`/`hooks`/`model` 剥离；agents/ 拷贝与剥离。
- 2.1.x 守门：版本 < 2.2.0 降级 `""` + warning。
- MCP 合并：同名 project 覆盖 user；受信标记缺失不注入。

**冒烟验证**（真实 claude CLI，临时项目，沿用本轮实证脚本）——2026-08-31 已全部执行，结果如下：
1. ✅ sanitized：`aliang-project:demo-skill` 在 init `slash_commands`；模型 Skill 调用成功输出 DEMO_HELLO_OK；CLAUDE.md 暗号不可见（禁工具单轮对照）。`context: fork` 生效未单独验证（白名单保留该字段，由 claude 原生处理）。
2. ✅ full：暗号 SMOKE-MARKER-77 直答（禁工具单轮，非工具读取）；项目 demo-skill 裸名；MCP 计数 = 用户 10 + 插件 4 + 项目 1 = 15。
3. ✅ isolated（守门降级产物）：暗号不可见、MCP 0、tools 27。
4. ✅ 承重墙实测：隔离 scope + 注入 `allow: Bash(touch:*)` → touch 成功建文件；同法无 allow → 无头拒绝、文件未创建。副产品见 §9.10（只读安全命令免审批层）。
5. ✅ env 优先级：settings env 压过进程 env（结论回写 §9.6）。
6. ✅（2026-09-01 复测关闭）strict 注入抑制插件 MCP（10 vs 14）→ 插件合并实现后复测：strict 下 15/15 与原生逐项一致（命名+状态），§9.9 关闭。
