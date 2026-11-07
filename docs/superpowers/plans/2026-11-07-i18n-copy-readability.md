# 网页端 i18n 文案易读性改进 — 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 统一 `zh.js`/`en.js` 标点、人称、术语口径,修复重复键 bug,重写面向开发者的烂句,让网页端文案说人话。

**Architecture:** 纯 i18n 值级修改(zh.js + en.js)+ 一处 DashboardPage.vue 键名引用改线。键集不变(仅 `dash_serverLinkOfflineDetail` 新增、重复定义删除),`i18n.test.js` 的两条守卫(zh/en 键集一致 + `qs_` 死键检测)全程作为护栏。

**Tech Stack:** Vue 3 + vite, vitest(`npm test`), playwright e2e(`npm run test:e2e`)

**Spec:** `docs/superpowers/specs/2026-11-07-i18n-copy-readability-design.md`

**工作目录:** 本 plan 假定在 worktree `/Users/mac/MyProgram/GoProgram/nursor/alianggate/.worktrees/i18n-copy-readability`(分支 `fix/i18n-copy-readability`)内执行,所有相对路径基于 worktree 根。

**Commit 惯例:** 标题「修复:/优化:」全中文 + HEREDOC,footer 含「🤖 Generated with [Claude Code](https://claude.com/claude-code)」与「Co-Authored-By: Claude Code <noreply@anthropic.com>」。

---

### Task 0: worktree 环境准备与基线绿

**Files:** 无改动(仅装依赖、验基线)

- [ ] **Step 0.1: 安装前端依赖**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate/.worktrees/i18n-copy-readability/app/website
npm ci
```

预期:安装成功,无 ERR。若 rollup/esbuild 平台二进制缺失报错,按 npm-cli#4828 教训:不要重新生成 lockfile,检查 node_modules/.bin 存在性后重试一次。

- [ ] **Step 0.2: 验证基线 vitest 全绿**

```bash
npm test
```

预期:全部通过(i18n 键集守卫 + 各组件测试)。**基线若有预存失败,记录下来,后续任务不碰。**

---

### Task 1: 重复键修复 + DashboardPage 接线

**Files:**
- Modify: `app/website/src/i18n/zh.js:541`(键名改写)
- Modify: `app/website/src/i18n/zh.js:607-608`(删重复定义)
- Modify: `app/website/src/i18n/en.js:544`(键名改写)
- Modify: `app/website/src/components/DashboardPage.vue:1498`(调用点改键)

- [ ] **Step 1.1: zh.js 长版键改名**

zh.js:541(当前内容):

```js
  dash_serverLinkOffline: 'mTLS 链接当前离线。上次成功握手于 {time}。',
```

改为(仅键名,值暂不动——值级标点在 Task 3 统一):

```js
  dash_serverLinkOfflineDetail: 'mTLS 链接当前离线。上次成功握手于 {time}。',
```

- [ ] **Step 1.2: zh.js 删重复的 settings_scope 定义**

删除 zh.js:607-608 两行(593-594 的同键定义保留):

```js
  settings_scopeDevice: '本机生效，换设备不同步',
  settings_scopeLocal: '仅本机浏览器',
```

(即删掉第二处;注意 606 行 `settings_comingSoon` 与 609 行注释行保留)

- [ ] **Step 1.3: en.js 长版键改名**

en.js:544(当前内容):

```js
  dash_serverLinkOffline: 'mTLS link is currently offline. Last successful handshake was {time}.',
```

改为:

```js
  dash_serverLinkOfflineDetail: 'mTLS link is currently offline. Last successful handshake was {time}.',
```

- [ ] **Step 1.4: DashboardPage.vue 调用点改键**

DashboardPage.vue:1498(当前内容):

```js
    return t('dash_serverLinkOffline', { time: formatRelativeTimestamp(serverLinkLastConnectedAt.value) });
```

改为:

```js
    return t('dash_serverLinkOfflineDetail', { time: formatRelativeTimestamp(serverLinkLastConnectedAt.value) });
```

(1282 行的 badge 调用点 `t('dash_serverLinkOffline')` **不改**——它消费短版「离线」)

- [ ] **Step 1.5: 验证唯一性与键集守卫**

```bash
grep -c "dash_serverLinkOffline:" app/website/src/i18n/zh.js app/website/src/i18n/en.js   # 各应恰为 1
grep -c "dash_serverLinkOfflineDetail:" app/website/src/i18n/zh.js app/website/src/i18n/en.js  # 各应恰为 1
grep -n "settings_scopeDevice" app/website/src/i18n/zh.js  # 应恰为 1 行
npm test -- src/i18n/i18n.test.js
```

预期:vitest 通过(键集一致 + 死键检测)。

- [ ] **Step 1.6: Commit**

```bash
git add app/website/src/i18n/zh.js app/website/src/i18n/en.js app/website/src/components/DashboardPage.vue docs/superpowers/specs/2026-11-07-i18n-copy-readability-design.md docs/superpowers/plans/2026-11-07-i18n-copy-readability.md
git commit -m "$(cat <<'EOF'
修复:i18n 重复键静默覆盖——dash_serverLinkOffline 详情长文案迁 Detail 新键并接线,settings_scope 重复定义去重

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: zh.js 格式统一(标点/人称/术语)

**Files:**
- Modify: `app/website/src/i18n/zh.js`(值内机械替换,约 80 处)

每个子步骤的通用流程:**先 grep 出行清单 → 人工区分值/注释(注释行不动)→ 按行替换 → 复核 grep 归零**。注释(`//` 之后、`<!-- -->` 之内)里的标点一律不动。

- [ ] **Step 2.1: 省略号统一 `...` → `…`(仅值内)**

```bash
grep -n '\.\.\.' app/website/src/i18n/zh.js
```

逐行判断:匹配 `: '....'` 值内的改;`//` 注释内的跳过。对确认的值行执行(行号从 grep 结果取,以下为示意):

```bash
perl -pi -e 's/\.\.\./…/g if $. == N1 || $. == N2' app/website/src/i18n/zh.js
```

已知值内含省略号的键(供核对):`qs_loading`、`qs_selectKeyPh`、`qs_applying`、`sys_refreshing`、`chat_placeholder`、`chat_sending`、`dash_loading`、`user_refreshing`、`sync_*` 族、`cert_removing` 等约 40 处;`guide_stepCounter` 断言(`正在执行：步骤一已完成…`)已是全角不动。

复核:

```bash
grep -n '\.\.\.' app/website/src/i18n/zh.js   # 仅剩注释行命中
```

- [ ] **Step 2.2: 值内半角括号 → 全角(约 4 处)**

```bash
grep -nE ": '[^']*\(" app/website/src/i18n/zh.js
```

已知命中:`peerMcp_subAgentcoreDesc`/`peerMcp_subClaudeDesc`/`peerMcp_subCodexDesc` 的 `(平台托管)`、`settings_sectionAgentDesc` 类。逐键 Edit 为 `（）`。**排除代码/示例类半角括号**(如有,如 `(KEY=VALUE)` 已是全角的不管)。

- [ ] **Step 2.3: 值内标签半角冒号 → 全角(排除技术串)**

两类 grep 覆盖:

```bash
grep -nE ": '[^']*[^]: [^']*" app/website/src/i18n/zh.js    # '标签: 值' 形式
grep -nE ": '[^']*[^\\]: '?" app/website/src/i18n/zh.js | grep -E "[一-龥]:"  # 值尾中文冒号
```

**改**(标签冒号,约 25 处):`sys_subject`(`主体:`→`主体：`)、`sys_validity`、`sys_finger`、`cert_subject`、`cert_issuer`、`cert_validity`、`cert_fingerprint`、`cert_installPath`、`cert_operation`、`cert_result`、`cert_info`、`cert_time`、`cert_lastRefreshed`(`上次刷新: {time}`→`上次刷新：{time}`)、`cert_cn`(`CN:`→`CN：`)、`cert_issuerLabel`、`cert_validYears`、`cert_certPath`、`cert_keyPath`、`cert_source`、`cert_keyAlgorithm`、`logs_levelAll`…`logs_levelError`(`级别: 全部`→`级别：全部`,6 行)、`user_currentLabel`/`user_totalLabel`(若为半角)。

**不改**(技术串):`guide_stepProxyCliDesc` 的 `127.0.0.1:56432`、`rules_serverError` 的 `Host:Port` 与 `cd.liangsqrt.com:27750`、`rules_geoipInvalid`/`rules_geoipCommaMulti`/`rules_geoipCommaMultiProxy` 的 `geoip:CN+HK`、`rules_proxyRulesPlaceholder` 示例段、所有 URL。

- [ ] **Step 2.4: 弯引号 → 「」**

```bash
grep -n '[""'']' app/website/src/i18n/zh.js
```

已知命中:`rules_entryErrorItem: '条目“{entry}”：{message}'` → `条目「{entry}」：{message}`。其余命中逐键处理为「」。

- [ ] **Step 2.5: 人称统一「您」→「你」(恰 9 行)**

```bash
grep -n '您' app/website/src/i18n/zh.js   # 预期 9 行
perl -pi -e 's/您/你/g if $. == N1 || ... ' app/website/src/i18n/zh.js
```

预期行(供核对):`qs_description`(5)、`qs_signInDesc`(10)、`sys_tunFirstTimeDesc`(128)、`sys_tunConflictHint`(131)、`sys_modeSwitchSuccess`(199)、`settings_loginRequiredSettingsDesc`(655)、`settings_loginRequiredLogsDesc`(657)、`settings_loginRequiredAgentDesc`(664)、`update_dismissedSubtitle`(785)。

复核:

```bash
grep -c '您' app/website/src/i18n/zh.js   # 应为 0
```

- [ ] **Step 2.6: 过滤术语与 token 口径**

```js
  dash_filterChat: '过滤：Chat',     → '过滤：聊天'
  dash_filterImage: '过滤：Image',   → '过滤：图片'
  dash_inputTokens: '输入 Tokens',   → '输入 token'
  dash_outputTokens: '输出 Tokens',  → '输出 token'
  dash_totalTokens: '总 Tokens',     → '总 token'
```

(组件 `<option value="chat">` 的 value 硬编码与文案无关,已查实安全;`chat_settingsMaxTokens` 已是小写 `token` 不动)

- [ ] **Step 2.7: 验证与提交**

```bash
npm test
git add app/website/src/i18n/zh.js
git commit -m "$(cat <<'EOF'
优化:i18n 中文文案标点、人称与术语口径统一(省略号/括号/标签冒号/引号/您→你/过滤术语/token 小写)

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: 「客户配置」术语人话化(zh + en 同步,11 键)

**Files:**
- Modify: `app/website/src/i18n/zh.js`
- Modify: `app/website/src/i18n/en.js`

逐键新旧对照(执行者按此表精确替换;en 按同表):

| 键 | zh 旧 | zh 新 | en 旧 | en 新 |
|---|---|---|---|---|
| `rules_title`(816) | 客户配置 | 代理与规则 | Customer Configuration | Proxy & Rules |
| `rules_description`(817) | 管理客户代理、AI 规则和代理规则。 | 管理上游代理、AI 规则和分流规则。 | Manage the customer-facing proxy, AI rules, and proxy rules stored by the customer config API. | Manage the upstream proxy, AI rules, and routing rules. |
| `rules_customerOnly`(819) | 仅客户 | 仅客户侧 | Customer only | Customer-side only |
| `rules_loadingConfig`(821) | 正在加载客户配置… | 正在加载配置… | Loading customer configuration… | Loading configuration… |
| `rules_customerProxy`(822) | 客户代理 | 上游代理 | Customer Proxy | Upstream Proxy |
| `rules_proxyDesc`(823) | 此处仅可编辑 customer.proxy 字段。 | 配置本机使用的上游代理。 | Only customer.proxy fields are editable here. | Configure the upstream proxy used by this machine. |
| `rules_formHint`(870) | 此处保存仅影响客户代理、AI 规则与代理规则，不改变本机核心设置；保存后立即生效。 | 此处保存仅影响上游代理、AI 规则与代理规则，不改变本机核心设置；保存后立即生效。 | Saving here only affects the customer proxy, AI rules, and proxy rules — …(同后半) | Saving here only affects the upstream proxy, AI rules, and proxy rules — it never changes core settings on this machine and takes effect immediately. |
| `settings_loginRequiredSettingsDesc`(655) | 在后端认证会话恢复或你在本页登录之前，客户配置编辑将保持锁定状态。 | 在认证会话恢复或你在本页登录之前，配置编辑将保持锁定。 | Customer config editing stays blocked until a backend auth session is restored or you log in with the account card on this page. | Configuration editing stays blocked until a backend auth session is restored or you log in with the account card on this page. |
| `settings_configSaved`(665) | 客户配置已成功保存。 | 配置已成功保存。 | Customer configuration saved successfully. | Configuration saved successfully. |
| `settings_configLoadFailed`(666) | 加载客户配置失败。 | 加载配置失败。 | Failed to load customer configuration. | Failed to load configuration. |
| `settings_configSaveFailed`(667) | 保存客户配置失败。 | 保存配置失败。 | Failed to save customer configuration. | Failed to save configuration. |

注:①行号以 Task 2 完成后的文件为准,可能有 ±N 漂移——以键名为准 grep 定位。②`rules_loadingConfig` 现值用 `…` 转义写法而非字面 `…`,替换时按键名定位匹配,新值写字面 `…` 即可(功能等价)。③macOS BSD grep 对部分 `[^]` 构造解析不同,2.3 步 grep 报错时改用已知键清单逐键处理。

- [ ] **Step 3.1: 按 对照表 逐键替换(zh + en)**
- [ ] **Step 3.2: 复核零残留**

```bash
grep -c '客户配置\|客户代理\|customer.proxy\|Customer Configuration\|Customer Proxy\|customer config\|customer proxy' app/website/src/i18n/zh.js app/website/src/i18n/en.js
```

预期:zh 为 0;en 中 `Customer only`→已改,若 `customer` 残留出现在本表外的键(如注释)则人工判断。

- [ ] **Step 3.3: 验证与提交**

```bash
npm test
git add app/website/src/i18n/zh.js app/website/src/i18n/en.js
git commit -m "$(cat <<'EOF'
优化:「客户配置/客户代理」术语人话化为「配置/上游代理」(zh+en 同步 11 键)

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: 烂句重写(zh + en,9 键)

**Files:**
- Modify: `app/website/src/i18n/zh.js`
- Modify: `app/website/src/i18n/en.js`

逐键完整新值(执行者直接照抄;zh 须遵循全角标点规则):

**① `chat_approvalNotFound`(zh:239 / en:239)——含与按钮文案一致性修复**

zh 新:
```
该审批已过期或已被处理，请重新发送消息。若想让本会话内同类操作自动放行，下次审批时选择「本会话批准」。
```
en 新(`chat_approvalAcceptSession` en 值为 `'Approve session'`,表述对齐):
```
This approval has expired or was already handled. Send the message again. To auto-approve similar actions in this session, choose "Approve session" next time.
```

**② `user_accountOverviewDesc`(zh:1028 / en:1025)**

zh 新:`当前账户的基础信息概览。`
en 新:`Basic info overview of the current account.`

**③ `user_subscriptionsDesc`(zh:1024 / en:1021)**

zh 新:`当前账户的有效套餐数量。`
en 新:`Number of active packages on this account.`

**④ `user_packageProgressDesc`(zh:1082 / en:1079)**

zh 新:`各套餐的用量进度。`
en 新:`Usage progress for each package.`

**⑤ `user_apiKeysDesc`(zh:1040 / en:1037)——去布局耦合**

zh 新:`当前账户已创建的 API Key，点击查看详情。`
en 新:`API Keys created on this account. Select one to view details.`

**⑥ `rules_geoipCommaMulti`(zh:835 / en:833)——去实现暴露**

zh 新:`geoip 多国请用 + 分隔（如 geoip:CN+HK），不支持逗号分隔`
en 新:`Use + to separate multiple geoip countries (e.g. geoip:CN+HK); comma-separated lists are not supported`
(注意:en 保持半角括号——英文惯例)

**⑦ `status_idleDescription`(zh:914 / en:911)——精简**

zh 新:`最近 {seconds} 秒内没有新的 AI 访问活动。连接结束后超过 {minutes} 分钟无新流量会自动隐藏。`
en 新:`No new AI activity in the last {seconds} seconds. Connections hide automatically after {minutes} minutes of inactivity.`

**⑧ `agentCore_description`(zh:1377 / en:1374)——去绕**

zh 新:`决定 AliangCode 连接哪个 LLM 端点（Anthropic Messages 兼容）。模型本身在聊天页按会话选择，这里只管「连到哪」。未启用时沿用 Claude Code 的默认配置与平台代理。`
en 新:`Decides which LLM endpoint AliangCode connects to (Anthropic Messages compatible). The model itself is chosen per conversation on the chat page — this setting only controls "where to connect". When disabled, the Claude Code default configuration and platform proxy are used.`

**⑨ `settings_sectionStatusDesc`(zh:592 / en:595)——单处精简(范围内唯一自由润色)**

zh 新:`只读观测：实时日志；页尾「其他」集中了设备级杂项开关。`
en 新:`Read-only observation: live logs. Device-level switches live under "Other" at the bottom.`

- [ ] **Step 4.1: 按上表逐键替换(zh + en)**
- [ ] **Step 4.2: 验证与提交**

```bash
grep -c '/api/user-center' app/website/src/i18n/zh.js app/website/src/i18n/en.js   # 应为 0 0
grep -c '会话内放行\|Allow for session' app/website/src/i18n/zh.js app/website/src/i18n/en.js  # 应为 0 0
npm test
git add app/website/src/i18n/zh.js app/website/src/i18n/en.js
git commit -m "$(cat <<'EOF'
优化:面向用户文案重写——删 API 路径与布局耦合暴露,审批自动放行指引与按钮文案对齐,长句精简(zh+en 9 键)

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: 全量验证

**Files:** 无新改动(仅跑测试;断言修正见 Step 5.3)

- [ ] **Step 5.1: 全量 vitest**

```bash
npm test
```

预期全绿。失败则逐个判断:文案断言过时 → 修断言随新文案;逻辑回归 → 回滚该处。

- [ ] **Step 5.2: e2e(注意 4174 旧 server 陷阱)**

```bash
lsof -ti :4174 | xargs kill -9 2>/dev/null; sleep 1   # 杀残留 dev server,防 reuseExistingServer 打旧代码
npm run test:e2e
```

预期:全部通过。**若 4174 被主仓或其他 worktree 的活跃 dev server 占用,先与用户确认再杀。** 失败的文案断言按新文案修正(记忆:e2e 断言中文须 locale zh-CN)。

- [ ] **Step 5.3: 修受影响断言(如有)并提交**

预判侦察已确认 e2e 硬编码断言与本次改动交集极小;若跑出断言失败,修正后:

```bash
git add -A && git status   # 提交前查 status,只 add 测试文件与预期变更
git commit -m "$(cat <<'EOF'
修复:e2e/vitest 断言随新文案同步修正

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 5.4: build 验证**

```bash
npm run build
```

预期:构建成功(embed 占位 dist 若缺失,从主仓 `app/website/dist` 复制占位后重试——transfer-worktree-build-gotchas 教训仅涉 Go embed,纯前端 build 无此依赖,但首跑失败时按此排查)。

---

## 完成定义

1. Task 0–5 全部 checkbox 勾选,vitest + e2e + build 三绿
2. `grep '您'`、`grep '/api/user-center'`、重复键检查全部归零
3. 分支 `fix/i18n-copy-readability` 上 4–5 个提交(0 为无提交的环境准备)
4. **合并回 master 由用户决定,本计划不含 merge/push 步骤**
