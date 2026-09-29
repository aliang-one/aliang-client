# 快速配置升级：安装检测 + 原始配置备份/恢复 + 智能合并（Quick Config v2）

日期：2026-09-22
状态：已确认（用户批准）
范围：alianggate 后端（`app/http/services/quick_setup_*`）+ 前端（`app/website/src`）

## 1. 背景与问题

现有快速配置（`app/http/services/quick_setup_service.go`，1485 行）存在四个核心问题：

1. **无安装检测**：`quickSetupSoftwares()`（qs:501-564）硬编码 opencode/codex/claude-code
   三个 software，全部展示，与用户本机实际装了什么无关。
2. **备份只在内存**：`quickSetupFileBackup`（qs:34-37, qs:304-317）是回滚缓冲区，
   进程崩溃即丢；用户看不到自己的原始配置被保存在哪，也没有恢复入口。
3. **盲覆盖写入**：`writeConfigFile`（software_config_service.go:658-709）整体覆盖。
   用户 opencode.json 里的 MCP 服务器/主题、`~/.codex/auth.json` 里的 ChatGPT 登录态
   （`tokens` 字段）都会被冲掉。
4. **应用后无回看**：apply 成功只显示「已写入 N 个文件」（QuickSetupModal.vue:890），
   关掉弹窗后无处查看 agent 配置文件的实际内容。

## 2. 已确认的产品决策

| 维度 | 决定 |
|---|---|
| 写入语义 | **智能合并**：已有文件只更新/注入我们的 keys，保留用户其余设置；缺失文件按模板创建 |
| 检测策略 | CLI 二进制 **或** 配置目录任一命中即「已安装」 |
| claude-code 载体 | 改写 `~/.claude/settings.json` 的 `env` 块；废弃 `~/.claude-code/env.sh` |
| 接入模式 | 每 agent 独立选择：本地加速 `http://127.0.0.1:56432` / 公网直连 `https://api.aliang.one` |
| 配置查看 | 应用结果页 + 常驻查看器（磁盘实时读取） |
| 恢复能力 | v1 含一键恢复原始配置 |

## 3. 方案选型

- **A. 增量演进 + 服务文件拆分（选定）**：在现有 quick_setup 域内扩展，同包拆多文件。
  最大限度复用久经考验的 Apply 安全契约（先全量校验→原子写→失败回滚，现有测试三处锁定）。
- B. 独立 AgentConfigManager 域（`/api/agent-config/*`）：边界最干净，但 apply 安全契约要
  重新实现，前后端大改，周期约 2 倍，两个配置入口并存造成用户困惑。v2 再评估。
- C. 最小改动：无恢复、合并粗糙（TOML 整段替换会覆盖用户其他段），与已确认决策冲突，否决。

## 4. 总体架构

服务层按职责拆分（同包 `services`，`quick_setup_service.go` 仅保留入口签名）：

```
quick_setup_service.go    入口：Catalog/Render/Models/Apply
quick_setup_catalog.go    software 定义 + 安装检测
quick_setup_render.go     渲染器（删 env.sh 渲染器，新增 settings.json 渲染）
quick_setup_merge.go      JSON 深合并 / TOML 行级拼接器 / settings env 块合并
quick_setup_apply.go      Apply 主流程 + 备份调用
quick_setup_backup.go     备份目录管理 + manifest 读写 + restore
quick_setup_snapshot.go   config-state 服务（磁盘实时读取）
```

数据流（v2 与 v1 的差异加粗）：

```
Catalog:  检测已安装 → softwares[].installed (**新增**)
Render:   读磁盘现有内容(**新增**) → 与我们的 keys 合并(**新增**)
          → 预览合并结果（用户可继续手动编辑，编辑后逐字应用）
Apply:    校验 → **备份落盘先行** → 原子写 → 失败回滚（既有契约不变）
ConfigState (**新端点**): 磁盘实时读取托管文件 + manifest 备份清单
Restore   (**新端点**): 按 software 整体还原
```

## 5. 安装检测（quick_setup_catalog.go）

| agent | CLI 探测（复用 `lookPathCLI`，agent_cli_lookup.go:34） | 目录探测 |
|---|---|---|
| claude-code | `claude` | `~/.claude` |
| codex | `codex`（含 ChatGPT.app 内置版） | `~/.codex` |
| opencode | `opencode` | `~/.config/opencode`、`~/.local/share/opencode`、`~/.opencode` 任一 |

- 家目录统一走 `EffectiveAgentHome()`（internal/runtimepath/runtimepath.go:221，
  root 场景自动解析登录用户），与既有 agent 扫描行为一致。
- Catalog 响应的每个 software 增加 `installed: bool`；**后端返回全量三个 + 标志位，
  前端过滤展示**（后端不知道 UI 意图；custom-* 模板不受影响）。
- `installed` 是建议性标志，**Apply 不硬性阻止未检测到的 agent**（合法场景：刚装完
  CLI 还没跑过、没有目录 → 想先配置）。
- 不加缓存：每次 Catalog 约 5 次 stat + 几次 LookPath，毫秒级，弹窗打开才调用。

## 6. 备份与恢复（quick_setup_backup.go）

### 6.1 目录结构

```
~/.aliang/quick-setup/backups/
  manifest.json
  claude-code/settings.json
  codex/config.toml
  codex/auth.json
  opencode/opencode.json
```

### 6.2 manifest.json 条目格式

```json
{
  "version": 1,
  "backups": [
    {
      "software": "codex",
      "file_code": "config",
      "original_path": "~/.codex/config.toml",
      "backup_path": "~/.aliang/quick-setup/backups/codex/config.toml",
      "backed_up_at": "2026-09-22T10:30:00+08:00",
      "sha256": "…",
      "size": 412,
      "existed_before": true,
      "kind": "original"
    }
  ]
}
```

`kind: "original"` 显式标记「这是用户的原始配置」。`original_path` 存 `~` 形式，
读写时经 home 展开。

### 6.3 关键语义

1. **First-backup-wins**：仅在「目标文件存在 且 manifest 中该 original_path 尚无
   备份」时备份。第二次及以后的 apply 不得用我们写的配置覆盖原始备份，否则恢复会
   回到我们的配置而非用户真正的原始配置。
2. **existed_before: false**（文件是我们新建的）→ 恢复时**删除**该文件，实现真正
   「回到应用前状态」。
3. **恢复 = 按 software 整体还原**：遍历该 software 的 manifest 条目，
   `existed_before: true` 复制备份回原路径，`false` 删除文件；随后清除这些 manifest
   条目（下次 apply 对当时状态重新备份）。
4. **备份先行**：备份写失败 → 在写任何配置文件之前中止 Apply，维持「未通过零副作用」
   不变量。
5. **manifest 损坏**（存在但解析失败）→ 拒绝 Apply 并报错（fail-safe：绝不能把我们的
   配置当「原始配置」重新备份），错误信息含 manifest 绝对路径，日志告警。
6. 备份写沿用现有原子写（temp+rename，0600）与 1MB 上限；manifest 更新在现有
   `quickSetupApplyMu` 锁内。

## 7. 智能合并引擎（quick_setup_merge.go + Render 改造）

**核心架构决策：合并发生在 Render（预览）阶段，不在 Apply 阶段。**

Render 流程：读磁盘现有内容（存在且可解析时）→ 与我们的 keys 合并 → 返回合并结果
作为预览。用户看到的预览即应用后的真实效果；可继续手动编辑；Apply 写入预览内容
（逐字），**现有 Apply 安全契约不动**（先全量校验→原子写→失败回滚，现有
quick_setup 测试约 24 条保持绿色，其中三处锁定的 apply 安全锚点测试：先校验后写 /
失败回滚 / 非法内容零落盘）。

各格式合并规则：

| 文件 | 规则 |
|---|---|
| `opencode.json` | JSON 递归深合并：我们的 keys（`provider` 条目的 npm/baseURL/options、`model`、`small_model`、`$schema`）胜出；用户其余字段（theme、mcp、keybinds…）全保留 |
| `auth.json` | **只设 `OPENAI_API_KEY`，保留 `tokens` 等其他键**（现状整体覆盖会杀掉用户的 ChatGPT 登录态） |
| `config.toml` | 保守行级拼接器：仅更新顶层 `model`/`model_provider` + 替换或追加我们拥有的 `[model_providers.aliang]` 段；其余字节不动（保住用户注释/格式）；状态机须跟踪多行字符串（`'''`/`"""`）避免误判；引入 TOML 解析依赖（如 `github.com/BurntSushi/toml`）**仅做输出校验**（解析验证合法，不用它重写文件） |
| `settings.json` | 合并 `env` 对象（`ANTHROPIC_BASE_URL`/`ANTHROPIC_AUTH_TOKEN`/`ANTHROPIC_MODEL`），保留用户其余 env 键和顶层字段（hooks、permissions…） |

- token 用 `ANTHROPIC_AUTH_TOKEN`——这是相对现状的重命名：现 env.sh 渲染器
  （qs:1129）用的是 `ANTHROPIC_API_KEY`。改用 AUTH_TOKEN 与 agent 运行链路的 env
  继承一致（Bearer 语义）；本地代理模式实际鉴权由 56432 代理注入
  `Authorization-Inner`，配置里放着真实 key 无害。
- 磁盘文件不可解析/不可读 → Render 降级：返回模板兜底 + 警告 note +
  `merged_from_disk: false`，不阻断。
- claude-code 的 Files 定义从 `~/.claude-code/env.sh` 换成 `~/.claude/settings.json`
  （quickSetupAllowedRoot 相应变为 `~/.claude`；旧 env.sh 成为孤儿文件，不自动删）。

### 7.1 baseURL 规则

模式只换 host 根：

- `local` → `http://127.0.0.1:56432`，直接引用 `processor/config/defaults.go` 的
  `DefaultHTTPProxyAddr` 常量（禁止再次硬编码）
- `public` → 走现有 `resolveQuickSetupInferenceBaseURL`（控制面 backend.aliang.one →
  推理面 api.aliang.one）

`/v1` 后缀规则：codex/opencode 的 base_url 带 `/v1`（与现状一致）；**claude 的
`ANTHROPIC_BASE_URL` 不带 `/v1`——这是相对现状的行为变更**：现 env.sh 渲染器
（renderClaudeCodeFiles qs:1128）经 `quickSetupProviderBaseURL` 会给 anthropic
provider 也补 `/v1`，新 settings.json 渲染器不得复用该 helper 的补全结果
（Claude Code 自行追加 `/v1/messages`）。

### 7.2 已知取舍（v1 明确接受）

- 用户换选 key 后，上次写入的旧 provider 条目会残留（无害冗余定义），清理留 v2。
- Render 与 Apply 之间磁盘被外部改动的竞态：窗口小（用户正停留在弹窗内），有备份
  兜底，接受。
- Models 端点不变：模型列表始终走公网拉取，本地模式只影响写入的 baseURL。

## 8. API 契约

现有端点（全部保持 `RequireDashboardSession` 鉴权）：

| 端点 | 变化 |
|---|---|
| `GET /api/quick-setup/catalog` | software 增加 `installed: bool` |
| `POST /api/quick-setup/render` | 请求增加 `mode: "local"\|"public"`（缺省 `public`）；响应增加 `merged_from_disk: bool`，notes 可含合并警告 |
| `POST /api/quick-setup/apply` | 行为增加备份落盘先行；响应增加 `backups: [{original_path, backup_path, existed_before}]` |

新增端点：

```
GET /api/quick-setup/config-state?software=codex
→ { files:   [{ path, exists, size, mtime, content, format, managed_by_aliang }],
    backups: [{ original_path, backup_path, backed_up_at, sha256, kind }] }

POST /api/quick-setup/restore    { "software": "codex" }
→ { restored: [path], deleted: [path], failed: [{ path, error }] }
```

- `content` ≤1MB（与 Apply 上限一致），`format` 沿用 software 文件声明。
- `managed_by_aliang` 判定（启发式）：JSON 检查特征键（`env.ANTHROPIC_BASE_URL`
  指向我们的 host、provider id 为 `aliang`）；TOML 检查 `[model_providers.aliang]`
  段存在。
- restore 逐文件执行，单文件失败（如 Windows 文件占用）不中断其余，响应分别列出
  `restored` 与失败项。

路由注册于 `app/http/routes/routes.go`（QuickSetup handler 增加两个方法）。

## 9. 前端（QuickSetupModal 迭代，不改两栏布局）

1. **模板侧栏过滤**：内置 software 只显示 `installed === true` 的；custom-* 始终显示；
   三个内置都未检测到 → 空状态「未检测到已安装的 AI Agent」+ 检测依据说明。
2. **接入模式切换**：配置面板加分段控件「本地加速 / 公网直连」，每 agent 独立记忆
   （默认公网直连）；选「本地加速」显示提示「依赖本软件的本地推理代理（登录后自动启动）」。
3. **渲染预览**：`merged_from_disk` 为 true 时顶部显示淡色横幅「已合并你现有的配置，
   其余设置将保留」。
4. **应用结果页**：apply 成功后切换结果视图——写入文件路径 + 最终内容 + 「已备份
   原始配置」卡片（备份路径列表）+「查看当前配置」按钮。
5. **「当前配置」常驻页签**：抽成子组件 `QuickSetupStatePanel.vue`：托管文件实时
   内容 + 刷新按钮 + `managed_by_aliang` 徽标 + 原始备份卡片（路径/时间/大小）。
6. **一键恢复**：备份卡片「恢复原始配置」按钮 → 确认弹窗列出将还原/删除的文件 →
   调 restore → 成功后刷新 config-state。
7. i18n：`qs_*` 平铺 key 族在 `i18n/zh.js` / `i18n/en.js` 同步新增（约 15 个）。
8. `quickSetupApi.js` 增加 `fetchConfigState` / `restoreConfig`；`quickSetupState.js`
   增加纯函数（installed 过滤、模式状态）并补单测。

非目标（v2 再评估）：OnboardingGuide 的 `quick-setup` 步骤完成态打通（现硬编码
false，OnboardingGuide.vue:260）；旧 provider 残留清理。

## 10. 错误处理

| 场景 | 行为 |
|---|---|
| manifest.json 损坏 | 拒绝 apply，错误信息含 manifest 绝对路径，日志告警 |
| 备份写失败 | 写配置前中止（零副作用），报错 |
| 磁盘文件不可解析（用户写坏 JSON/TOML） | Render 降级模板兜底 + 警告；用户仍可逐字 apply（有备份兜底可恢复） |
| 磁盘文件读取失败（权限） | 同上降级 |
| 本地加速但 56432 未监听 | 不阻断（baseURL 只是字符串），仅 UI 提示依赖 |
| restore 单文件失败 | 逐文件报告部分成功，失败项列出 |

## 11. 测试策略

Go 单测沿用 `t.Setenv("HOME", t.TempDir())` + 现有函数钩子注入模式
（`quickSetupTargetUserFn`/`quickSetupWriteConfigFileFn` 等）：

- **合并引擎**：JSON 深合并（用户字段保留、我们的键胜出、env 嵌套合并）；auth.json
  保留 `tokens`；TOML 拼接器（段替换、顶层键更新、多行字符串安全、注释保留）；输出经
  TOML 解析校验。
- **检测**：CLI 命中 / 目录命中 / 双命中 / 双未命中 四态（注入 homeDir + lookPath
  stub；注意 `lookPathCLI` 目前是直接函数调用、无钩子变量，实施时需新增
  `quickSetupLookPathCLIFn` 钩子，沿用现有 `quickSetup*Fn` 注入模式）。
- **备份/恢复**：first-backup-wins（二次 apply 不覆盖原始备份）；`existed_before:false`
  恢复即删除；manifest 损坏阻断 apply；restore 后 manifest 条目清除、再 apply 重新备份。
- **Apply**：现有全部契约保持绿 + 新增备份断言（响应字段、落盘文件、权限 0600）。
- **Render**：mode→baseURL 映射表（local/public × 三 software × 带/不带 `/v1`）、
  合并降级路径、`merged_from_disk` 标志。

前端：`quickSetupState.test.js` 补 installed 过滤 / 模式状态纯函数单测
（沿用现有 vitest 模式）。

## 12. 涉及文件清单

后端：
- `app/http/services/quick_setup_service.go` → 拆分为上列 7 个文件
- `app/http/handlers/quick_setup_handler.go`（+2 端点方法）
- `app/http/models/quick_setup.go`（新类型 + 字段）
- `app/http/routes/routes.go`（+2 路由）
- `go.mod`（+`github.com/BurntSushi/toml`，仅校验用）

前端：
- `app/website/src/components/QuickSetupModal.vue`
- `app/website/src/components/QuickSetupStatePanel.vue`（新增）
- `app/website/src/services/quickSetupApi.js`
- `app/website/src/utils/quickSetupState.js`（+ 测试）
- `app/website/src/i18n/zh.js` / `en.js`
