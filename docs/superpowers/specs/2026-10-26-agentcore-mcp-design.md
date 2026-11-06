# AliangCode（agentcore）MCP 客户端支持 — 设计文档

日期：2026-10-26
状态：设计稿（待评审）
范围：仅 agent 客户端（alianggate），零 PhoneServer / 前端改动（一处前端容忍度风险见 §8）
前置依赖：journal 分支**已并入 master**（合并提交 `290fe3d`），journal v2 工具行
直接在 master HEAD 上的 journal 实现上扩展。

## 1. 背景与目标

AliangCode（agentcore，内置 golang 内核）目前只有 8 个内置工具，不支持 MCP。
用户在 claude 里配好的 MCP server（`~/.claude.json` 顶层 `mcpServers` + 已启用
插件 `.mcp.json`）对 AliangCode 不可见。

**目标**

- AliangCode 会话可发现并调用上述 user 域 MCP server 的工具（2026-10-26 用户拍板：
  复用 claude 配置源，零二次配置）；
- 工具调用默认逐次手机审批（用户拍板：走现有审批管线，默认 `mcp__*` 需批，
  设备策略可显式放行）；
- journal v2 记工具行（用户拍板：连带扩展），回放可见 MCP 调用轨迹。

**非目标**

- 项目级 `.mcp.json`（后续按需另议，需先解决信任门）；
- MCP server 配置管理界面（增删改仍用 claude 自己的方式）；
- 长驻连接缓存（v1 per-run 起停；有性能诉求再加）；
- Resources / Prompts / Sampling 等 MCP 能力（只做 Tools）；
- 内核内置 8 工具的调用记录（journal v2 只记 MCP 工具）。

## 2. 方案取舍

| 方案 | 结论 |
|---|---|
| **A. 内核零改动，宿主组装（本设计）** | ✅ MCP 工具包成 `agentcore.Tool`（四方法接口，tools.go:85）经 `Registry.Register` 注入；内核保持「禁 import services、无 MCP 概念」；审批/journal/生命周期全在宿主层 |
| B. 内核加 MCP 集成 | 否：内核引入 MCP 依赖与连接管理，破坏包边界与可独立测试性 |
| C. 内核加动态工具源接口 | 否：为单一消费方加抽象，YAGNI；方案 A 的宿主组装已覆盖 |

## 3. 架构与数据流

```
runAgentCore 入口（现有 Config 组装点附近）
  ├→ 采集原始 MCP 配置（§4，新解析层，非脱敏清单层）
  ├→ per-run 建连：stdio spawn / http 直连（go-sdk v1.8.0 client）
  │     Client.Connect(ctx, transport, opts) → ClientSession.ListTools
  ├→ 每工具包成 mcpBridgeTool（实现 agentcore.Tool，§6）→ registry.Register
  │     （在 DefaultRegistry 之后注册；mcp__ 前缀保证与内置 8 工具零碰撞，
  │       Register「后注册者胜」不会覆盖内置工具）
  ├→ run：模型 tool_use → 内核 executeOne（Approve→ScopeCheck→Execute）
  │     MCP 工具 Execute = ClientSession.CallTool + journal v2 tool 行（§7）
  └→ run 结束 defer：关闭全部 ClientSession（stdio 子进程随之退出）
```

失败容忍：单个 server 建连/ListTools 失败 → 跳过该 server + Warn（尽力而为，
与 journal 同精神）；全部失败 → run 照常进行（纯内置 8 工具），不报错不阻断。

## 4. 配置采集（原始解析层，安全约束）

**为什么不能复用 `agent_mcp_inventory.go`**：清单层输出是**脱敏**后的
（env 值剥离、token 打码），用于上报展示；连接需要**原始** command/args/env。
新写轻量解析 `agentCoreMCPRawServersIn(home)`：

- 数据源与清单层同源同规则：`<home>/.claude.json` 顶层 `mcpServers`
  （user scope）+ 已启用插件 `installPath/.mcp.json`（插件发现复用
  `agent_ai_claude_policy.go` 的 installed_plugins.json × enabledPlugins 规则，
  仅 user scope、缺 installPath 跳过、fail-closed）；
- 条目形态：`command+args+env`（stdio）/ `url+headers`（http/sse，sse 归 http
  处理，与设备清单口径一致）；`disabled: true` 跳过；
- 条目上限 24 个（与设备清单一致），超出截断 + Info 日志；
- **安全约束（硬性）**：env/headers 值只存在于内存传给 go-sdk，**永不**进
  日志、journal、上报、审批卡的展示字段（审批卡只显示工具名+输入参数摘要）；
- 畸形条目（缺 command/url、JSON 坏）跳过该条 + Warn，不影响其余（与清单层
  「codex 单条目类型不匹配使整文件归零」的 serde 严格语义**不同**——这里是
  尽力而为连接层，不是上报契约）。

## 5. 连接与生命周期

- **per-run**：runAgentCore 组装 Config 时建连，run 结束 defer 全关；
- ListTools **跟随 `nextCursor` 分页**直到取尽或触及 128 上限（部分 server 启用
  分页，只取第一页会静默丢工具）；
- 初始化超时 10s（Connect+ListTools 合计）；超时按失败跳过该 server；
- 工具调用超时继承内核单工具 10 分钟上限（Execute 的 ctx 已带）；
- **已知限制（v1，2026-10-26 评审裁定记档）**：go-sdk v1.8.0 的
  `StreamableClientTransport`/`SSEClientTransport` 无 Headers 字段——带鉴权
  headers 的 http/sse server 无法注入凭据，会在初始化阶段失败被跳过（预判
  跳过+专门 Warn，不吃超时预算），run 降级纯内置工具。stdio（claude.json
  主流形态）不受影响；headers 注入待后续版本以自定义 RoundTripper 补齐。
- 工具总数上限 **128**（防 prompt 爆炸）：按 server 顺序收集，超限截断 + Warn；
  内置 8 工具不受影响。

## 6. 工具包装（mcpBridgeTool）

实现 `agentcore.Tool` 四方法：

- `Name()` = `mcp__<server>__<tool>`（与 CC 命名一致：宿主工具卡/活动追踪按名
  分派免费复用；前缀保证与内置工具、与 tool_ask 白名单命名空间零冲突）。
  **字符集合法化（硬约束）**：Anthropic API 工具名必须匹配
  `^[a-zA-Z0-9_-]{1,128}$`——`<server>` 段做白名单清洗（非法字符转 `_`）+ 截断
  + 清洗后冲突追加序号（清单层插件复合名是 `plugin:<p>:<s>` 带冒号，直接复用
  必产出 API 400 的非法工具名，整轮请求报废）；
- `Description()` = MCP 工具 description（截 500）；
- `InputSchema()` = go-sdk Tool 的 inputSchema JSON 直接解码为
  `map[string]interface{}`（内核 Defs 直接下发，无二次转换）；
- `Execute(ctx, input, env)` = `ClientSession.CallTool`；成功→ToolResult 文本
  拼接（content blocks 的 text 依序拼接，截 64KB 与内置工具一致）；
  `IsError=true` 的结果→错误文本 ToolResult（模型自决重试，与内置工具语义
  一致）；transport 错误→错误 ToolResult（不 panic——注意 master 近期刚修过
  内核工具链 panic 带死整机的问题 d2da2b5，Execute 内必须全量 recover 兜底）。

## 7. 审批

统一走内核 `Approve` 回调（`agentCoreApprove` → `evaluateApprovalDecision`），
新增**默认规则**：工具名 `mcp__` 前缀 → `require_approval`（设备策略显式
auto_approve/auto_deny 优先于默认规则）。审批卡展示：工具名 + 输入参数摘要
（JSON 截 500，env/headers 不可能出现在 input 里——它们在配置层）。

## 8. journal v2：工具行

- 行格式（追加一类 kind，向后兼容）：

```jsonl
{"kind":"tool","tool":"mcp__github__create_issue","summary":"repo=aliang-one/x title=…","is_error":false,"ts":"RFC3339"}
```

- 写入点：`mcpBridgeTool.Execute` 返回前（宿主层，不经内核 Hooks）；summary =
  输入参数的稳定摘要（key=value 按序拼接，截 200）；is_error 取 Execute 结果；
- **不记**：全量参数、结果全文、env/headers；
- reader：tool 行进 `Transcript`（Role=`"tool"`），**DisplayMessageCount 只数
  user/assistant 行**（工具行是轨迹不是对话气泡）；
- **回放呈现风险（唯一前端关联点）**：手机/服务端对 Transcript 未知 role 的
  容忍度未验证。实现期先核对其解析路径；若不容忍：回放端先过滤 tool 行降级
  （数据已在文件里，随时可放开），前端改动留待用户点头后另做。

## 9. 测试计划

- 配置采集：claude.json 解析/插件源/disabled 跳过/畸形条目跳过/24 上限/env 不落日志（日志断言）；
- 建连与生命周期：stdio spawn 成功/失败跳过/http 形态/初始化超时/run 结束全关（进程退出断言）；
- 工具包装：命名前缀/schema 直传/ListTools→Registry 注入/128 截断/CallTool 成功与 IsError/transport 错误不 panic（注入故障 server）；
- 审批：mcp__* 默认 require_approval/策略显式放行/拒绝理由回填模型回合继续；
- journal v2：tool 行格式/summary 截断/is_error/reader 转录含 tool 行且 DisplayMessageCount 不计/旧格式文件（无 tool 行）向后兼容；
- 端到端：run 内调 MCP 工具→审批→执行→journal 四类行齐（session/user/tool/end）。

## 10. 改动文件清单（预估）

| 文件 | 动作 |
|---|---|
| `app/http/services/agent_ai_agentcore_mcp.go` + `_test.go` | 新增：原始配置采集、连接管理、mcpBridgeTool、journal tool 行包装 |
| `app/http/services/agent_ai_agentcore.go` | 修改：Config 组装点接入（建连/注册/defer 关闭）+ 审批默认规则一行 |
| `app/http/services/agent_ai_agentcore_journal.go` | 修改：appendTool（+去重豁免，tool 行不参与 message_id 去重） |
| `app/http/services/agent_inventory_agentcore.go` | 修改：reader 解析 tool 行→Transcript（DisplayMessageCount 不计） |

（具体行级挂点在实现计划阶段钉死。）

## 11. 风险与回滚

- 风险集中点：stdio 子进程泄漏（defer 全关 + run ctx 取消联动关闭）；恶意
  server 输出爆炸（64KB 截断）；审批绕过（默认规则的测试锁定）；
- 回滚 = 移除 Config 组装点接入即可，journal tool 行残留无害（reader 向后兼容）；
- 观测：建连成功/失败、工具注入数量、截断各一条 Info/Warn，带 server 名（不含 env）。
