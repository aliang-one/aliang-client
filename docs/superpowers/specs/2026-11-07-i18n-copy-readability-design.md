# 网页端 i18n 文案易读性改进 — 设计

日期:2026-11-07
范围:`app/website/src/i18n/zh.js` + `en.js`(仅改值;键名除重复键修复外不动)

## 背景与目标

全文 1450 行文案整体质量尚可,但存在四类可读性问题:重复键静默覆盖、标点/人称/术语不统一、开发者视角文案暴露给用户、个别长句过绕。目标:一次批改让网页端中文文案口径一致、说人话。

## 改动清单

### ① 重复键 bug 修复(zh + en 同步)

| 键 | 现状 | 处置 |
|---|---|---|
| `dash_serverLinkOffline` | zh 541/568 两处定义,短版「离线」静默覆盖带 `{time}` 详情版;en 544/571 同款 | 已查实两个调用点均活(`DashboardPage.vue:1282` 用短版、`:1498` 用 `{time}` 插值版)→ 保留短版键名,长版迁到新键 `dash_serverLinkOfflineDetail`,1498 调用点改用新键(zh+en 同步) |
| `settings_scopeDevice` / `settings_scopeLocal` | zh 593-594 与 607-608 重复(同值);en 仅一份 | 删 zh 多余一份 |

### ② 格式统一(机械替换,约 70+ 处,中文侧)

- 省略号:值内 `...` → `…`(50 处多数派向 19 处规范版靠拢);**en.js 保持 `...` 不动**
- 括号:值内半角 `()` → 全角 `（）`(如 `(平台托管)` → `（平台托管）`)
- 冒号:值内半角 `:` → 全角 `：`(如 `主体:` → `主体：`;注意保留 `KEY=VALUE`、URL、`Host:Port`、示例值里的半角)
- 引号:弯引号 `""` → 「」(`rules_entryErrorItem` 等)
- 人称:以 grep `您` 为准全量替换为「你」(共 9 行,含 `sys_tunFirstTimeDesc`;不按枚举执行,防漏)
- 术语:
  - `dash_filterChat: '过滤：Chat'` → `过滤：聊天`;`dash_filterImage` → `过滤：图片`(与 `dash_filterStream: '过滤：流式'` 对齐)
  - token 口径统一小写 `token`:「输入 Tokens/输出 Tokens/总 Tokens」列头 → 「输入 token/输出 token/总 token」

### ③ 术语人话化(10 处)

- 「客户配置」→「配置」、「客户代理」→「上游代理」
- `rules_proxyDesc: '此处仅可编辑 customer.proxy 字段。'` → 去字段名,说「配置本机使用的上游代理」
- 涉及键:`rules_title`、`rules_description`、`rules_customerProxy`、`rules_customerOnly`、`rules_proxyDesc`、`rules_formHint`、`rules_loadingConfig`、`settings_configSaved`、`settings_configLoadFailed`、`settings_configSaveFailed`、`settings_loginRequiredSettingsDesc`

### ④ 烂句重写(约 12 处,en.js 对应键同步重写英文)

| 键 | 问题 | 方向 |
|---|---|---|
| `user_accountOverviewDesc` | 暴露 `/api/user-center/profile` | 「当前账户的基础信息概览。」 |
| `user_subscriptionsDesc` | 暴露「摘要 API」 | 「当前有效套餐数量。」 |
| `user_packageProgressDesc` | 暴露 `/api/user-center/usage/progress` | 「各套餐的用量进度。」 |
| `user_apiKeysDesc` | 「右侧列出」布局耦合 | 「当前账户已创建的 API Key,点击查看详情。」 |
| `rules_geoipCommaMulti` | 「逗号写法在当前输入框会被拆坏」暴露实现 | 「geoip 多国请用 + 分隔(如 geoip:CN+HK),不支持逗号分隔」;④ 中所有新文案一律遵循 ② 标点规则(全角括号等) |
| `chat_approvalNotFound` | ①过长 ②文中「会话内放行」与按钮 `chat_approvalAcceptSession`「本会话批准」不一致(真 bug) | 精简且与按钮文案一致 |
| `status_idleDescription` | 技术细节过载 | 两句内:无新活动 + 超时自动隐藏 |
| `agentCore_description` | 「这是…不是…」绕 | 只讲「连到哪」,模型在聊天页选一句话带过 |
| `settings_sectionStatusDesc` | 口语化括号补充冗长 | 仅此一处精简;允许顺手做单句修剪,不得新增键 |

## 不动清单

- 全部键名(除 `dash_serverLinkOfflineDetail` 新增);`i18n.test.js` 的两条断言(zh/en 键集一致 + `qs_` 死键检测)不受影响
- Go 侧文案、`.vue` 注释、组件逻辑
- en.js 省略号(英文惯例 `...`)
- 占位符 `{xxx}` 与键值插值语法

## 验证

1. 改前先 grep `app/website` 全域(含 `tests/e2e/` 与 `src/**/*.test.js`)中硬编码旧文案,产出受影响断言清单
2. 全量 `vitest` + `e2e`(locale zh-CN)+ `build`;断言随新文案同步修正
3. worktree 切分支实施,不直接动 master;完成后按惯例提交(标题「修复:/优化:」中文格式)

## 风险

- 标点全角化可能碰断 e2e 字符串断言 → 靠第 1 步预判 + 全量 e2e 兜底
- `dash_filterChat/Image` 若组件以英文值做判断逻辑(非纯展示)需查证,实施时先 grep 组件用法再改
