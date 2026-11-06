# AliangCode 内核 LLM 自定义设置 — 设计文档

日期:2026-10-28
状态:已评审(用户逐节确认)

## 1. 背景与问题

AliangCode(内置 Go 内核)的 LLM 相关设置目前完全继承 claudecode 口径:

- **Base URL**:子进程 env `ANTHROPIC_BASE_URL` → `~/.claude/settings.json` env 块 → 兜底 `http://127.0.0.1:56432`(`app/http/services/agent_ai_agentcore.go` `agentCoreBaseURL`)。**回环硬闸**:非回环地址一律拒绝、强制回落 56432 平台代理——刻意设计,因远端直连需要完整 mTLS 客户端凭据,内核没有。
- **API Key**:env/settings `ANTHROPIC_AUTH_TOKEN` → 用户 API key 列表兜底(带缓存)(`agentCoreAuthToken`)。
- **模型 / effort / 上下文窗口**:手机会话设置唯一权威(`agentCoreModel` 等)。

没有任何 AliangCode 专属的自定义入口:无专属 env 变量、无配置字段、无仪表盘面板。用户多次提出要支持自定义 LLM 设置,均未落地。

唯一现成的「半入口」是仪表盘自定义环境变量(`CustomEnvSettings.vue`),但 base URL 被回环闸挡死,且要求用户知道 `ANTHROPIC_*` 变量名,非产品化入口。

## 2. 目标与非目标

### 目标

本机(agent 全局)级别的 AliangCode LLM 自定义设置,**影响这台机器上全部 AliangCode 会话**,仅两个字段:

- **Base URL**(Anthropic Messages 兼容端点;内核协议=Anthropic SSE,OpenAI 协议端点不支持,见 `app/agentcore/client.go`)
- **API Key**

### 非目标(明确排除)

- 模型/effort/contextWindow 的本机兜底或覆盖(维持「手机/聊天页权威」设计)
- 仪表盘聊天页补 contextWindow 选择器(另立任务)
- 修正模型选 default 被误导性拒绝的文案与 `chatSettings.js` 过时注释(另立任务)
- OpenAI 协议端点支持
- 配置变更时中断进行中的 run(下一回合生效即可)

### 已拍板的关键决策

| 决策点 | 结论 |
|---|---|
| 存储位置 | 本机 agent 全局配置(Config 体系),非手机 per-session |
| 字段范围 | 仅 Base URL + API Key |
| 回环闸新语义 | 仪表盘显式配置放行(远端也放行);**非回环地址 ⇒ API Key 必填**(配对校验,保存端+读取端双保险);继承链(env/settings)维持现有回环闸不动 |
| 实现方案 | 方案 A:Config 加字段 + `/api/config/customer` 同通道 + 读取链插显式层 |

## 3. 设计

### 3.1 配置模型(`processor/config/types.go`)

```go
// AgentCoreLLMConfig 是 AliangCode 内核的显式 LLM 端点设置。
// nil 或 Enabled=false 时整层不生效,行为完全回落继承链(现状不变)。
type AgentCoreLLMConfig struct {
    Enabled bool   `json:"enabled"`
    BaseURL string `json:"base_url,omitempty"`
    APIKey  string `json:"api_key,omitempty"`
}
```

**字段落点:必须挂在 `CustomerConfig` 下(即配置文件 `customer.agentcore_llm`),不是 Config 顶层。** 原因:`/api/config/customer` 通道只读写 `customer` 子树——顶层键会被 `normalizeCustomerPayload` 以 400 拒绝,GET 回程也只返回 customer 子树(`customer_config_service.go`)。

`customer` 子树有**前后端共 4 处硬编码可编辑键白名单**,本功能必须同步扩展,漏一处则保存 400 / 字段被静默剥离:

1. `app/http/services/customer_config_service.go` `validateEditableCustomerKeys` 的 switch(保存校验)
2. `processor/config/types.go` `extractCustomerUnknownFields` 的 switch(加载校验)
3. `processor/config/types.go` `customerUnknownKeyErrors` 的错误信息串(与上两处的「editable customer fields are [...]」文案同源,需一并更新,共 3 处字符串)
4. `app/website/src/components/SettingsPage.vue` `normalizeCustomerConfig` / `createDefaultCustomerConfig` 的显式键清单(前端按清单重建 payload,未列出的键被剥离——`traffic_mirror`/`geoip` 曾在此被剥离,前车之鉴)

- `CustomerConfig` 新增字段:`AgentCoreLLM *AgentCoreLLMConfig \`json:"agentcore_llm,omitempty"\``
- 新增取值器 `EffectiveAgentCoreLLM()`(对齐 `EffectiveCustomEnvVars` 的 nil-guard 形状,读 `c.Customer`):nil / `Enabled=false` / BaseURL 为空 → 返回 nil,整层关闭

**校验规则**(保存端 `app/http/services/customer_config_service.go` 校验 + 读取端防御双保险,防手改配置文件绕过):

- `Enabled=true` 时:`base_url` 必须可解析(scheme+host);**非回环地址 ⇒ `api_key` 必填**;回环地址 key 可空
- 校验不过:读取端视为整层关闭并 Warn 一行(不静默、不 crash)

### 3.2 读取链(`app/http/services/agent_ai_agentcore.go`)

优先级三层,显式层插最前:

```
① 显式配置(Enabled 且合法)→ ② env/settings 继承链(现状逻辑,回环闸只在此层生效)→ ③ 兜底
```

- `agentCoreBaseURL()`:①命中直接返回(回环/远端都放行);未命中现有逻辑原样
- `agentCoreAuthToken()`:①的 key 非空直接返回,**不进** `agentCoreCachedUserAPIKey` 缓存;未命中走现有 env/settings → 用户 API key
- ①生效时 Info 一行:`agentcore: 使用自定义 LLM 端点 <url>`(不打 key);关闭/回落不刷屏

**3.2.1 upstream 探测分叉**(`agentCoreEnsureUpstream`):①命中时**跳过 56432 自愈**(`POST /api/run/start` 对外部端点无意义),改为对自定义地址做同款轻量 TCP 拨测(2s 超时),失败即报错;①未命中原逻辑原样。

**3.2.2 生效时机**:每次 run 重新读 `config.GetGlobalConfig()`,保存后下一回合立即生效,无需重启;运行中不热切换。

### 3.3 仪表盘 UI

新组件 `app/website/src/components/settings/AgentCoreSettings.vue`,结构对齐 `CustomEnvSettings.vue`(props: config/loading/saving/error/successMessage;emits save;watch config 深比较):

- 启用开关 + 说明文案
- Base URL 文本框(placeholder 示例:`https://your-gateway.example.com`)
- API Key 密码框(带显隐切换)
- 前端校验与后端同规则,不过关禁用保存并就地提示
- 校验逻辑抽纯函数到 `app/website/src/utils/agentCoreSettings.js`(对齐 `chatSettings.js` 纯函数+vitest 模式)
- i18n 新增 `agentCore_*` 键(`app/website/src/i18n/zh.js` 与 `app/website/src/i18n/en.js`);`SettingsPage.vue` 注册新 pane,保存时 `agentcore_llm` 合入同一 `nextConfig`,走既有 `POST /api/config/customer`(payload `{customer}`,路由 `app/http/routes/routes.go:216`)
- **清空字段必须可达**:`buildCustomerConfigPatch` → `buildConfigPatch` 对空串返回 undefined(键整个不进 patch),而后端 `deepMergeJSONObjects` 只增不减——不清空键会把旧值永久留在配置里。`agentcore_llm` 的 `base_url`/`api_key` 两个字符串字段需要比照现有 `proxy.username/password` 的 force-include 模式(`SettingsPage.vue` `buildCustomerConfigPatch`):「next 为空且 current 非空」时显式把空值写进 patch。`enabled` 是布尔值,`buildConfigPatch` 的 `Object.is` 路径本就会把变化写进 patch,**不做** force-include(照搬字符串模式对布尔值不触发且无意义)
- 范围纪律:`normalizeCustomerConfig` 键清单里 `traffic_mirror`/`http1_drop`/`geoip` 缺失是**预存技术债**,本功能只加 `agentcore_llm`,不顺手修历史遗漏

### 3.4 错误处理

| 场景 | 行为 |
|---|---|
| 保存时校验不过 | 后端 400 带原因,前端就地提示 |
| 自定义端点拨测失败 | 现有 `agentCoreEnsureUpstream` 错误通道 → `ai.error`,文案「自定义 LLM 端点不可达 \<url\>: \<原因\>」 |
| 自定义端点 401/403 | 现状透传,不特判 |
| 手改配置文件致字段非法 | 3.1 读取端防御:整层关闭 + Warn |
| 运行中改配置 | 下一回合生效 |

### 3.5 测试

- **Go 单测**:
  - `types`:取值器边界(nil/关闭/空 URL/非法 URL)
  - `agent_ai_agentcore`:优先级矩阵(custom on/off × env 有无 × 回环/远端 × key 有无)
  - ensureUpstream 自定义分支(不拨 56432、拨自定义地址)
  - `customer_config_service` 保存校验(非回环缺 key 拒绝等)
- **vitest**:`utils/agentCoreSettings.js` 校验纯函数;**清空字段往返**(设 key → 保存 → 清空 → 保存 → patch 含空 `api_key`),回归守护 §3.3 的 force-include
- **真机冒烟**:面板保存 → 发一条 AliangCode 消息 → 日志见「使用自定义 LLM 端点」+ 流量打到自定义地址

## 4. 风险与权衡

- **api_key 明文存本机配置文件**:`custom_env_vars` 本就支持塞 `ANTHROPIC_AUTH_TOKEN`,风险面无新增;不引入加密存储(YAGNI)。
- **显式远端 + key 的安全边界**:显式配置=用户自己担责;配对校验挡住「只配 URL 没配 key」的 401 谜团;继承链回环闸不动,意外继承的远端地址仍被拒绝。
- **协议边界**:仅 Anthropic Messages 兼容端点;UI 文案需说明,避免用户填 OpenAI 协议端点后困惑。

## 5. 关联文件清单(实现入口)

| 文件 | 改动 |
|---|---|
| `processor/config/types.go` | 新增 `AgentCoreLLMConfig` + `CustomerConfig` 字段 + `EffectiveAgentCoreLLM()`;**白名单 switch(:914)与错误串(:877)** |
| `app/http/services/customer_config_service.go` | 保存校验;**白名单 `validateEditableCustomerKeys`(:410)与其错误串** |
| `app/http/services/agent_ai_agentcore.go` | `agentCoreBaseURL`/`agentCoreAuthToken` 优先级层;`agentCoreEnsureUpstream` 分叉 |
| `app/website/src/components/settings/AgentCoreSettings.vue` | 新组件 |
| `app/website/src/utils/agentCoreSettings.js` | 校验纯函数 |
| `app/website/src/components/SettingsPage.vue` | 注册 pane + 保存合并 + **`normalizeCustomerConfig`/`createDefaultCustomerConfig` 键清单** + **`buildCustomerConfigPatch` 清空 force-include** |
| `app/website/src/i18n/zh.js` / `app/website/src/i18n/en.js` | `agentCore_*` 键(zh/en) |

⚠️ 白名单共 4 处逻辑 + 3 处同源错误文案串,必须同一提交内全部扩展,任何一处遗漏 = 保存 400 或字段被静默剥离。
