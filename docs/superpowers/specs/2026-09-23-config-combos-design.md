# 快速配置 v3：配置组合（套餐）管理设计

日期：2026-09-23
状态：已确认（用户批准）
前置：v2（安装检测 + 备份恢复 + 智能合并）已合并 master（e400314）
范围：alianggate 后端（quick_setup 域 + 新增 SQLite store）+ 前端（QuickSetupModal 重构）

## 1. 背景与目标

v2 的快速配置是「单发」流程：选服务端 key → 服务端 render → apply。用户需要的是**配置组合（套餐）管理**：

- 每个 agent 可有多个命名组合（default* / config2 / config3…），星标记默认
- 每个组合含多个文件（config.toml、auth.json…），文件用**变量**（base_url/api_key/model）占位
- configure 表单填变量值；apply 把选中组合的文件应用到磁盘
- 用户原始配置绝不能丢：apply 前必须保证已备份

## 2. 已确认的产品决策

| 维度 | 决定 |
|---|---|
| 组合存储 | **纯本地**；组合定义存 **SQLite**（复用 GORM store 模式），用户原始配置备份保持文件系统（v2 原样） |
| 与 v2 流程关系 | **取代重做**：组合体系完全替换「选 key→render→apply」；安装检测/备份恢复/当前配置查看保留 |
| 变量集 | **三标准变量**：base_url / api_key / model；configure 表单设置 |
| default 星标 | **仅预选**（打开弹窗/切 agent 时预选该组合），不自动写盘 |
| 支持 agent | **四个**：claude-code、codex、opencode、**pi（新增）**；仍按「本机检测到才展示」过滤 |
| 新建组合入口 | **三个全做**：空白模板 / 复制现有 / 导入磁盘当前配置 |
| Apply 语义 | **逐字落盘**（组合即完整期望状态）；v2 智能合并退役 |

## 3. 方案选型

- **A. 后端权威存储 + 前端即时渲染（选定）**：组合存 SQLite；模板占位符由前端本地替换做零延迟预览；apply 走现有端点（备份先行/原子写/回滚全复用），后端新增占位符校验兜底。
- B. 后端渲染：每次改变量都请求 render——预览有往返延迟，一致性收益小。否决。
- C. 组合存前端 localStorage：清缓存即丢组合，与「不能搞丢」冲突。否决。

## 4. 数据模型（SQLite）

新文件 `app/http/storage/quick_setup_combo_store.go`，复用 `software_config_store.go` 模式（统一数据 DB `cache.GetUnifiedDataDBPath()`、AutoMigrate、`New...WithDBPath` 测试注入）：

```go
type QuickSetupCombo struct {
    ID        int64             `json:"id" gorm:"primaryKey;autoIncrement"`
    Software  string            `json:"software" gorm:"uniqueIndex:idx_combo_sw_name;size:64;notNull"`
    Name      string            `json:"name" gorm:"uniqueIndex:idx_combo_sw_name;size:128;notNull"`
    IsDefault bool              `json:"is_default"`
    Variables map[string]string `json:"variables" gorm:"serializer:json"` // base_url/api_key/model
    Files     []QuickSetupComboFile `json:"files" gorm:"serializer:json"`
    CreatedAt time.Time
    UpdatedAt time.Time
}

type QuickSetupComboFile struct {
    Code    string `json:"code"`    // 对应 software 声明的 file code
    Content string `json:"content"` // 模板（含 {{base_url}} 等占位符）
}
```

- `software+name` 复合唯一索引；组合名重复 → 400「组合名已存在」
- `is_default` 每 software 至多一个：**设默认 = 事务内先 UPDATE 清同 software 其他 default 再设置**
- 数量上限：每 software ≤50 个组合（防御性；超限 400）
- files 数量 = 该 software 声明的 Files 数（claude-code 1 / codex 2 / opencode 1 / pi 2），code 必须匹配声明
- 明文 key 存 DB：与备份目录同机等暴露面（v2 写入用户配置文件的 key 本就明文），无新增风险

### 4.0 base_url 预设由 catalog 下发（单一事实源）

catalog 响应顶层增加 `presets` 字段：

```json
{ "base_url_local": "http://127.0.0.1:56432", "base_url_public": "https://api.aliang.one" }
```

后端从 `config.DefaultHTTPProxyAddr` 常量与 `resolveQuickSetupInferenceBaseURL` 派生（**禁止前后端硬编码**，沿用 v2 铁律）；前端 configure 下拉的两个预设、种子/空白模板的 base_url 预填全部取自该字段。

### 4.1 自动种子

Catalog 时发现某 installed software **零组合** → 幂等种子一个「默认」组合：标准模板（§6.3）+ 占位符，`variables.base_url` 预填公网推理地址，api_key/model 留占位。种子在 catalog 读路径内完成（带锁，防并发双种子），二次 catalog 不重复建。

## 5. 占位符与渲染

- 语法：`{{base_url}}`、`{{api_key}}`、`{{model}}`；**纯字符串顺序替换**（替换后的值不再扫描——无递归/注入问题；刻意不用 Go text/template）
- **前端即时渲染**：configure 改变量 → 渲染视图实时变（零往返）
- **后端 apply 兜底**：内容匹配 `\{\{\s*[a-zA-Z_][a-zA-Z0-9_]*\s*\}\}` → 400，报错含文件名与变量名
- 已知限制（接受）：配置内容里出现字面 `{{xxx}}` 会被当占位符拒绝——真实 agent 配置不会出现

## 6. 组合生命周期

### 6.1 三入口（POST /combos 的 source）

| source | 行为 |
|---|---|
| `blank` | 后端按 software 生成骨架：各 agent 标准文件 + 占位符（§6.3 模板） |
| `copy` | 复制指定组合（`copy_from_id`），名字自动加「副本」后缀（超长截断保唯一） |
| `disk` | 后端读该 software 声明的目标文件**原样**存为模板（不做占位符反推）；文件不存在 → 400 提示 |

变量值初始：blank/copy 继承模板预设或源组合；disk 导入后 variables 为空——用户经 configure 或「插入变量」按钮补。

### 6.2 编辑与保存

- 内容/变量改动**显式保存**（PUT，避免每击键打 API）；切换文件 tab/组合/关闭弹窗有未保存改动 → 提示保存/放弃
- 文件编辑器提供「插入变量」按钮（光标处插 `{{api_key}}` 等），配合 disk 导入流程

### 6.3 内置模板（blank/种子用）

| agent | 文件 | 模板要点 |
|---|---|---|
| claude-code | `~/.claude/settings.json` | `{"env":{"ANTHROPIC_BASE_URL":"{{base_url}}","ANTHROPIC_AUTH_TOKEN":"{{api_key}}","ANTHROPIC_MODEL":"{{model}}"}}`；base_url **不带 /v1** |
| codex | `~/.codex/config.toml` | `model = "{{model}}"`、`model_provider = "aliang"`、`[model_providers.aliang]`（base_url 带 /v1、env_key=OPENAI_API_KEY、wire_api="responses"）；`~/.codex/auth.json` → `{"OPENAI_API_KEY":"{{api_key}}"}` |
| opencode | `~/.config/opencode/opencode.json` | `$schema` + 单 provider（aliang）npm/baseURL={{base_url}}/v1/options.key={{api_key}} + `model: "aliang/{{model}}"` |
| pi | `~/.pi/agent/models.json` | `providers.aliang: {baseUrl:"{{base_url}}", apiKey:"{{api_key}}", api:"anthropic-messages", models:[{id:"{{model}}"}]}`；**base_url 不带 /v1**（pi 的 anthropic-messages 自动追加 /v1/messages，实施时按 pi.dev 文档验证）；`~/.pi/agent/settings.json` → `{"defaultProvider":"aliang","defaultModel":"{{model}}"}` |

模板细节以各 agent 官方文档为准，实施时对真实 CLI 验证。

## 7. Apply 与备份语义（「不能搞丢」三层保障）

1. **组合定义层**：SQLite 事务写
2. **用户原始配置层**：v2 first-backup-wins **原样**——apply 前检查 manifest，文件存在且无条目才备份（`kind:"original"`）；**组合 A→B 切换永不覆盖原始备份**；`existed_before:false`（我们新建）恢复时删除；恢复入口保留
3. **写入过程层**：原子写 + 失败回滚契约不动
- Apply = 逐字落盘（前端发渲染后内容，后端校验+备份+写入）；**已知取舍**：应用最小组合会整体替换用户 settings.json 等（主题等非冲突键被清）——有备份可恢复，且「导入磁盘」入口可把用户现状存为组合后再改
- 「运行时检查是否备份过」= apply 时服务端自动完成；default 仅预选，不自动写盘

## 8. API 契约

全部 `RequireDashboardSession`：

| 端点 | 方法 | 说明 |
|---|---|---|
| `/api/quick-setup/catalog` | GET | **增强**：`{softwares(+installed+Files 声明), api_keys, combos, presets}`；触发种子 |
| `/api/quick-setup/combos` | POST | 创建 `{software, name, source, copy_from_id?, variables?, files?}` → **响应 `{combo}`（完整组合）** |
| `/api/quick-setup/combos/{id}` | PUT | 保存 `{name?, variables?, files?}` → **响应 `{combo}`（更新后完整组合，前端就地替换）** |
| `/api/quick-setup/combos/{id}` | DELETE | 删除（default 可删）→ **响应 `{id}`** |
| `/api/quick-setup/combos/{id}/default` | POST | 设默认（事务）→ **响应 `{combos}`（该 software 全部组合，前端整组替换）** |
| `/api/quick-setup/apply` | POST | **复用**：新增占位符校验；路径白名单/备份先行/原子写/回滚零改动 |
| `/api/quick-setup/config-state`、`/restore` | — | 原样 |
| `/api/quick-setup/render`、`/models` | — | **删除**（连同路由与服务端渲染链） |

CRUD 响应体原则：单条变更回 `{combo}`，多条受影响（set-default）回 `{combos}`，删除回 `{id}`；列表以 catalog 为准，前端用变更响应就地维护本地状态，不整页重拉。

## 9. 前端（QuickSetupModal 按手绘稿重构）

```
左侧栏             顶部：组合 tabs [default*][config2][config3][+新建]      右侧：[apply]
(installed 过滤,   第二行：文件 tabs [config.toml][auth.json]                    [configure]
 四 agent)         主区：渲染视图（变量已替换 = apply 所见）⇄「编辑模板」切换
                   底部：「当前配置」页签（StatePanel 复用，含备份卡片/一键恢复）
```

- **渲染视图为默认**：所见即 apply 所得；「编辑模板」切到模板原文 textarea（显式保存），防「改渲染结果被变量覆盖」歧义
- **configure** → 子组件 `QuickSetupConfigurePanel`：base_url（预设下拉：本地 `127.0.0.1:56432` / 公网 `api.aliang.one` / 自定义）、api_key（手输或从 catalog keys 选）、model（手输）→ 确定 = PUT 保存 → 渲染视图即时刷新
- **apply** → 渲染后内容逐文件 POST apply → 成功进 `QuickSetupResultPanel`（复用）；apply 前前端预检占位符未替换则禁用按钮
- 组合 tab 菜单：重命名 / 设默认 / 删除（确认弹窗）；「+新建」三入口选择弹窗
- 组件拆分：`QuickSetupConfigurePanel.vue` 新增；`QuickSetupResultPanel.vue`/`QuickSetupStatePanel.vue` 复用
- **v2 的 custom-* 自定义软件概念移除**（组合体系覆盖其场景；后续需要再加）
- i18n：`qs_*` 平铺键族 zh/en 成对新增

## 10. v2 代码退役范围

- **删除**：`/render`、`/models` 端点与路由；渲染链（renderCodexFiles/renderOpenCodeFiles/claude settings 渲染器/blank 模板生成之外的旧模板函数）、`mergeCodexTOML`、`mergeQuickSetupJSONObjects`、模型列表拉取链（fetchQuickSetupModels 等）及其测试
- **保留**：catalog（增强）、apply（增强校验）、备份/恢复全套、config-state、`writeConfigFile`、`quickSetupTargetUserFn` 等钩子、格式校验（json/toml parse）
- 前端 `quickSetupApi.js` 删 render/models 函数；`filterInstalledQuickSetupSoftwares` 等纯函数保留
- **custom-* 后端分支一并删除**：catalog 的 custom 软件构造、apply 的 custom 路径白名单分支（`quick_setup_allowedRoot` 的 `custom-*` 分支、`quickSetupBuiltInPathAllowed` 相关分支）随前端概念移除成为死代码，按「取代重做」原则清掉（spec §9 custom-* 移除的后端对应项）

## 11. 错误处理

| 场景 | 行为 |
|---|---|
| 占位符未替换 apply | 400 指明文件与变量（前端预检禁用按钮双保险） |
| 组合名重复 / 超 50 个 | 400 |
| software 未声明该 file code | 400（复用路径白名单语义） |
| disk 导入文件不存在 | 400 提示 |
| default 组合被删 | 允许；该 agent 无默认，前端回退第一个 |
| SQLite 打不开 | 500 + 路径信息（store 现有风格） |
| manifest 损坏 | 拒绝 apply（v2 原样 fail-safe） |

## 12. 测试策略

- **store 层**（注入临时 DB 路径）：CRUD、复合唯一约束、default 唯一性事务、JSON 列 round-trip、50 上限
- **服务层**：种子幂等；三入口初始内容（blank 含占位符 / copy 改名 / disk 原样+不存在报错）；占位符替换与校验（正则、未替换清单）
- **apply**：占位符拒绝断言；既有备份/回滚/路径安全/symlink 契约测试**全部保持绿**
- **前端**：占位符替换、变量完整性预检纯函数 vitest
- 已知预存 flake（agent_ai.go 子进程超时三件套）按既有记忆处理，不属本特性

## 13. 涉及文件清单

后端新增：
- `app/http/models/quick_setup_combo.go`
- `app/http/storage/quick_setup_combo_store.go`
- `app/http/services/quick_setup_combo_service.go`（CRUD/种子/模板/校验）
- `app/http/handlers/quick_setup_combo_handler.go` + 路由

后端修改：
- `app/http/services/quick_setup_catalog.go`（pi 检测规则 + Files 声明 + 空白模板表）
- `app/http/services/quick_setup_apply.go`（占位符校验）
- `app/http/services/quick_setup_service.go`（catalog 增强、render/models 删除）
- `app/http/models/quick_setup.go`、`app/http/routes/routes.go`
- `app/http/services/quick_setup_render.go`（大部删除）`quick_setup_merge.go`（删除）

前端：
- `QuickSetupModal.vue`（重构）、`QuickSetupConfigurePanel.vue`（新增）
- `services/quickSetupApi.js`、`utils/quickSetupState.js`(+test)、`i18n/zh.js`/`en.js`
- `QuickSetupResultPanel.vue`/`QuickSetupStatePanel.vue`（复用）
