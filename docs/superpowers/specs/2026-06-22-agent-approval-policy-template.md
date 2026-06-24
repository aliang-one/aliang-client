# Agent AI 审批策略模板（server-side policy + agent-side evaluation）设计

> 修订：校正真实代码拓扑（4 个面）、策略挂载到既有 device settings、利用既有 `device.settings.updated` 推送、对齐 agent 契约。

## 目标

把"哪些 AI 工具调用需要人工审批"从 **agent 硬编码** 改为 **服务端可治理的策略模板**，并让无害操作（`Read`/`Grep`/`Glob`/`TaskUpdate` 等）在 agent 本地秒放行、**不上报服务端**，只有真正关键的（文件改写、危险命令）才打扰人。

## 背景：当前问题

审批靠 Claude Code 的 hook 拦截工具调用（`app/http/services/agent_ai.go`）：

- hook 配置 `"matcher": "*"`（`agent_ai.go:3172`）——匹配**所有**工具。
- hook 策略探测（`detectClaudeApprovalHookStrategy` `agent_ai.go:3123`）在 `claude --version` 失败时**静默回退**到旧版 `PreToolUse`，对每次工具调用都触发。
- `handleClaudeApprovalHook`（`agent_ai.go:1010`）校验 session/token 后**无条件**走 `requestApproval`（`agent_ai.go:943`）上报服务端等人工（≤9min），**全程无工具维度的放行逻辑**。

三者相乘 → 生产日志实测 `Read/Grep/Glob/TaskUpdate` 也产生审批请求（`~/.aliang/agent/agent.log` 中 `approval-hook: INVOKED` 约 40% 是只读/无害工具）。这是"审批洪流"的根因。

## 与既有设计的关系

- 本文定**"审什么"**（策略：某操作 → 放行 / 上报 / 拒绝）。
- [`docs/ai-approval-reliability-design.md`](../../ai-approval-reliability-design.md) 定**"审批决策怎么可靠送达"**（ACK/重投/重连对账）。
- 两者正交：策略引擎决定是否调用 `requestApproval`；一旦决定上报，送达可靠性沿用既有 `ai.approval.*` 机制，本文不改动。

## 真实代码拓扑（4 个面）

| 面 | 仓库 / 路径 | 技术栈 | 本设计职责 |
|---|---|---|---|
| **agent** | 本仓库 `alianggate`（Go） | Go | 缓存 + 本地求值 + 执行；hash 校验 |
| **server 后端** | `~/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer/server/src` | Express + WS + better-sqlite3 + jose + zod | 模板 CRUD、resolve、hash、下发；挂 device settings |
| **admin Web** | `AliangPhoneServer/web/src` | React+Vite（**当前近乎空壳**） | 暂不投入；配置走手机端 |
| **手机端** | `~/MyProgram/AiProgram/vibe_on_phone/AliangVibeCodingPhone/src` | React Native + zustand + react-navigation | 审批操作 + per-device 策略配置 UI |

> "app 端"在本设计里 = **手机 RN app**（`AliangVibeCodingPhone`）。admin Web 暂不实现，留空。

### 既有可复用的关键链路（服务端）

- `PATCH /api/devices/:deviceId/settings`（`server/src/modules/routes/devices.ts:176`）：`deviceSettingsSchema`(zod) + `requireUserId` + `getAccessibleDeviceOrThrow` + `rememberAudit({eventType:'device.settings.update'})` + 向 agent 推 `device.settings.updated`、向 mobile 推 `device.updated`。
- agent 侧已处理 `device.settings.updated`（本仓库 `app/http/services/agent_service.go` 的 `applyRemoteDeviceSettings`）。
- 审批动作：`POST /api/approvals/:approvalId/respond`（`server/src/modules/routes/misc.ts:203`）+ 手机端 `screens/operations/ApprovalCenterScreen.tsx` + `api/approvals.ts` + `store/slices/approvalSlice.ts`。
- agent 鉴权：`device_token` 作 `Authorization: Bearer`（`server/src/modules/agent/routes.ts`）；agent HTTP 既有 `/api/agent/status`（+ `/api/v1/agent/status`）。
- **agent 契约权威**：`AliangPhoneServer/docs/agent-cloud-contract/{README.md,samples.json,schema.json}`，`npm run contract:agent` 验证。

## 已锁定决策

| 决策 | 取值 |
|---|---|
| 模板粒度 | **按设备/安装（device_id）** |
| 求值位置 | **agent 本地**（缓存 + 求值） |
| 姿态 | **fail-safe 兜底 + 危险命令清单（dangerous-first）** |
| 方案集 | **balanced（fail-safe，系统默认）/ allow-all / custom** |
| 配置深度 | **preset + 开关微调**（custom v1 = copy balanced + 勾选；自由正则 v2） |
| 策略挂载 | **scheme 挂 device settings**（白嫖审计/鉴权/`device.settings.updated` 推送） |
| 同步 | **push（device.settings.updated 带 hash）+ pull（每轮对话前 hash 校验，断线兜底）** |
| 版本指纹 | **SHA-256(canonical JSON)**，全方案统一 |

## 数据模型

### `ApprovalPolicy`（agent 求值 + 缓存 + hash 的对象）

```jsonc
{
  "version": 12,                          // 单调递增，服务端分配
  "hash": "sha256:...",                   // 对 canonical JSON（不含 hash 字段）计算
  "scheme": "balanced",                   // balanced | allow_all | custom
  "device_id": "dev-...",
  "rules": [ /* ApprovalRule[]，有序 */ ],
  "default_decision": "require_approval"  // allow_all => "auto_approve"
}
```

### `ApprovalRule`（首条命中生效）

```jsonc
{
  "id": "dangerous-bash",
  "match": { "tool": ["Bash"], "command_regex": "rm\\s+-rf|\\bsudo\\b" },
  "decision": "auto_approve" | "require_approval" | "auto_deny",
  "reason": "危险命令，需审批"
}
```

求值：按 `rules` 顺序首条命中；都不命中 → `default_decision`。**危险规则必须排在安全规则之前**（dangerous-first），默认模板遵循。

### 服务端存储（better-sqlite3，2 张表 + device settings 字段）

| 位置 | 内容 |
|---|---|
| **device settings 字段**（扩 `deviceSettingsSchema`） | `approval_policy: { scheme: 'balanced'\|'allow_all'\|'custom', custom_version?: number, hash: 'sha256:...' }`。scheme 选择 = 改 device settings，**复用审计/鉴权/推送**。 |
| `system_preset_templates` | balanced / allow_all 系统模板（`scheme`+`version`+`rules_json`+`default_decision`+`is_active`） |
| `device_custom_templates` | custom 副本（`id`+`device_id`+`version`+`rules_json`+`default_decision`+`hash`+`created_from_preset_version`） |

> custom 创建 = "从 balanced 最新版 copy 进 `device_custom_templates` + 算 hash"。

**Resolve 逻辑**（`server/src/modules/approval/policy.ts` 新增）：查 device settings 的 `approval_policy.scheme`；无 → balanced；balanced/allow_all → `system_preset_templates` 最新 active；custom → `device_custom_templates[device_id]` 最新 version；对 resolved JSON 算 hash 返回。

## 四端职责

### server 后端（`AliangPhoneServer/server/src`）

| 文件 | 动作 | 内容 |
|---|---|---|
| `modules/routes/devices.ts` | 改 | `PATCH /api/devices/:deviceId/settings` 的 `deviceSettingsSchema` 增 `approval_policy`；写入后随 `device.settings.updated` 推给 agent（带 hash） |
| `modules/routes/devices.ts` 或新 `modules/routes/approvalPolicy.ts` | 改/新 | 手机端 custom 编辑：`PATCH /api/devices/:deviceId/approval-policy/custom`（勾选式，后端重算 hash、version+1、更新 device settings hash、再推 `device.settings.updated`） |
| `modules/agent/routes.ts`（或 auth 路由区） | 新 | agent-facing：`GET /api/agent/approval-policy/hash`、`GET /api/agent/approval-policy`（device_token Bearer 鉴权） |
| `modules/approval/policy.ts` | 新 | resolve + hash 计算 + CRUD 辅助 |
| `database.ts` | 改 | 建表 `system_preset_templates` / `device_custom_templates`；种子 balanced/allow-all |
| `schemas.ts` | 改 | zod schema：`approvalPolicySchema`、`approvalRuleSchema`、扩展 `deviceSettingsSchema` |
| `docs/agent-cloud-contract/{README,samples.json,schema.json}` | 改 | 新增 agent-facing policy 接口 + `device.settings.updated.approval_policy` 字段；`npm run contract:agent` 验证 |

### agent（本仓库 `alianggate`）

| 文件 | 动作 | 内容 |
|---|---|---|
| `app/http/services/agent_approval_policy.go` | 新 | `ApprovalPolicy`/`ApprovalRule` 类型、`evaluateApprovalPolicy` 引擎、缓存 load/save（`approval_policy.json`，同 `device_identity.json` 套路）、`fetchPolicyHash`/`fetchPolicy`、`ensurePolicyBeforeRun`、二进制内置 balanced |
| `app/http/services/agent_ai.go` | 改 | `handleClaudeApprovalHook:1010` + `codexAppServerApprovalResult:1958` 插求值；`runUserMessage:524` 插 hash 校验；请求附带 `matched_rule_id`/`policy_version` |
| `app/http/services/agent_service.go` | 改 | `applyRemoteDeviceSettings` 识别 `approval_policy.hash` 变化 → 触发全量拉取（白嫖既有 `device.settings.updated` 推送） |
| `app/http/models/agent_protocol.go` | 改 | 审批请求 payload 增 `policy_version`/`matched_rule_id`（可选上下文） |
| `app/http/services/agent_approval_policy_test.go` | 新 | 引擎单测 + 同步 + 缓存重启幸存 |

### 手机端（`AliangVibeCodingPhone/src`）

| 文件 | 动作 | 内容 |
|---|---|---|
| `screens/devices/DeviceDetailScreen.tsx` | 改 | per-device 策略配置入口：scheme 三选 + custom 勾选 |
| `screens/operations/ApprovalCenterScreen.tsx` | 改 | 审批卡展示"为何上报"（命中规则 + policy version） |
| `api/devices.ts`（或新 `api/approvalPolicy.ts`） | 改/新 | `PATCH /api/devices/:id/settings`（带 approval_policy）+ `PATCH .../approval-policy/custom` |
| `store/slices/approvalSlice.ts` | 改 | 审批项携带 policy 上下文字段 |

### admin Web（`AliangPhoneServer/web/src`）

本期**不实现**（近乎空壳）。配置能力先在手机端交付。

## 求值引擎（agent 端）

纯函数，可单测：

```go
type policyDecision string
const (
  decisionAutoApprove     policyDecision = "auto_approve"
  decisionRequireApproval policyDecision = "require_approval"
  decisionAutoDeny        policyDecision = "auto_deny"
)
// 首条命中，否则 defaultDecision。返回 (decision, matchedRuleID)。
func evaluateApprovalPolicy(p ApprovalPolicy, toolName string, toolInput json.RawMessage) (policyDecision, string)
```

集成（两处审批入口都接，覆盖 Claude 与 Codex）：

```go
switch evaluateApprovalPolicy(policy, toolName, toolInput) {
case decisionAutoApprove:
  return claudeApprovalHookDecision(event, true, reason)   // 本地秒放行，不调 requestApproval
case decisionAutoDeny:
  return claudeApprovalHookDecision(event, false, reason)
case decisionRequireApproval:
  req := buildClaudeApprovalRequest(run, raw)
  req.MatchedRuleID = matchedID; req.PolicyVersion = policy.Version
  return m.requestApproval(...)                             // 既有上报路径
}
```

allow-all = `default_decision=auto_approve` 且无 require 规则 → 引擎统一处理，无特殊分支。

## 同步协议（push + pull）

**push（近实时，白嫖既有链路）**：手机端改 scheme/custom → 服务端 `device.settings.updated {approval_policy:{scheme,version,hash}}` → agent `applyRemoteDeviceSettings` 发现 hash 变 → 触发全量 `GET /api/agent/approval-policy` → 落盘 + 更新内存。重连时 device settings 随 `agent.hello` 重推，覆盖断线期间变更。

**pull（断线兜底）**：`runUserMessage`（`agent_ai.go:524`）spawn CLI 前：

```
1. GET /api/agent/approval-policy/hash  （短超时 3s）
2. 失败 → 用本地缓存；缓存也无 → 二进制内置 balanced；记 warn，绝不阻塞
3. hash == 本地 → 用缓存
4. hash != 本地 → GET 全量 → 落盘 + 更新
```

- 离线退化链：**远程缓存 → 二进制内置 balanced**，**永不退化 allow-all**。
- push 负责常态更新（已免费），pull 仅作断线/漏推兜底，绝大多数对话 pull 命中缓存、零开销。

## 可见性（auto-approve ≠ 不可见）

auto-approve 仍写活动流：既有 `claudeToolUseEvents`（`agent_ai.go:2532`）从输出流解析 `ai.command`/`ai.file_change`/`ai.task`，与审批决策独立——放行不抑制它们。可选加"由策略规则 X 自动放行"轻量标注。allow-all 尤其依赖此可见性。

## 默认模板内容

### balanced（fail-safe，dangerous-first）

| # | 规则 id | match | decision |
|---|---|---|---|
| 1 | `dangerous-bash` | tool=Bash，regex=`rm\s+(-[a-zA-Z]*r[a-zA-Z]*f\|-[a-zA-Z]*f[a-zA-Z]*r)\|\bsudo\b\|\bdd\s\|mkfs\|>\s*/dev/(sd\|nvme\|disk)\|git\s+push\|git\s+reset\s+--hard\|git\s+clean\s+-fd\|\bcurl\b.*-X\s*(POST\|PUT\|DELETE\|PATCH)\|\bwget\b\|\bkill\s+-9\|\bkillall\b\|shutdown\|reboot\|halt\|\bchmod\s+-R\b\|\bchown\s+-R\b\|npm\s+publish` | require_approval |
| 2 | `file-mutation` | tool=[Write,Edit,MultiEdit,NotebookEdit] | require_approval |
| 3 | `readonly-tools` | tool=[Read,Grep,Glob,LS,TodoWrite,TaskUpdate,TaskCreate,WebSearch] | auto_approve |
| 4 | `safe-bash` | tool=Bash，regex=`^(ls\|cat\|head\|tail\|wc\|pwd\|echo\|grep\|rg\|find\|file\|stat\|git\s+(status\|diff\|log\|show\|branch))\b` | auto_approve |
| — | default | — | **require_approval** |

> 第 1 条置前是 defense-in-depth：即便安全规则写宽（误加 `git\b→approve`），`git push` 仍先被第 1 条拦下。

### allow-all

`rules: []`，`default_decision: auto_approve`。仍写活动流。

## 安全考量

1. 危险命令 regex 是 best-effort（Bash 图灵完备）→ `default_decision=require_approval` 兜漏网，兜底必须 fail-safe。
2. 安全规则要窄、要锚定（`^git\s+(status|diff|log)\b` 而非 `git`）——fail-safe 下真正的风险面。
3. dangerous-first 求值顺序是硬约束；自定义乱序由策略作者负责。
4. 模板可信：服务端签发 + 版本号；agent 永不自行放宽；缓存损坏/缺失且失联 → 二进制内置 balanced，永不 allow-all。

## 分期与向后兼容

- **Phase A（agent，本仓库）**：引擎 + 缓存 + hash 校验 + 集成，**带二进制内置 balanced**。独立上线——fetch 失败即退化内置 balanced，**立刻消除 read/grep 洪流**，不等服务端/手机端。最小可交付价值单元。
- **Phase B（服务端）**：2 表 + device settings 扩字段 + resolve/hash + agent-facing API + 契约更新。接入后 agent 获可配置性 + push 同步。
- **Phase C（手机端）**：DeviceDetailScreen scheme 选择 + custom 勾选；ApprovalCenterScreen 展示 policy 上下文。
- 向后兼容：Phase A 不改既有 `ai.approval.*` 协议；未升级服务端时 agent 优雅退化；device settings 新字段对旧 agent 透明忽略。

## 验证（TDD）

- **引擎**（先写失败测试）：只读工具→approve；文件改写→escalate；`rm -rf`→escalate；`grep`/`ls`→approve；dangerous-first（宽泛 `git\b` 安全规则下 `git push` 仍 escalate）；未识别工具→default(fail-safe)；allow-all→全 approve；`auto_deny`。
- **同步**：push（device.settings.updated hash 变 → 重拉）；pull hash 命中/不命中；fetch 失败→缓存/内置（永不阻塞、永不 allow-all）。
- **缓存**：进程重启从 `approval_policy.json` 恢复。
- **契约**：服务端 `npm run contract:agent` 通过；agent 侧 `app/http/services/agent_service_test.go` 增 policy 字段消费回归。
- 全量 `go test ./app/http/services/` 维持既有绿基线。

## 开放问题 / v2

- WS 独立推送 `ai.approval.policy.updated`（当前借 device.settings.updated 已够用）。
- custom 自由正则编辑器（v1 仅勾选）。
- 粒度细化：per-(device, project)。
- 策略继承：组织 → 用户 → 设备。
- hook matcher 是否从 `"*"` 收窄（性能优化，非必需——引擎已 gate）。
- admin Web 是否补配置 UI（当前走手机端）。
