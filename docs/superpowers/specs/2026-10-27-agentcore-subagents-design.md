# AliangCode（agentcore）子 agent 支持 — 设计文档

日期：2026-10-27
状态：设计稿（待评审）
范围：仅 agent 客户端（alianggate），零 PhoneServer / 前端改动
前置依赖：无（journal v2 tool 行、MCP 桥均已合入 master：4a3586e）

## 1. 背景与目标

AliangCode（agentcore，内置 golang 内核）目前只有 8 个内置工具 + MCP 桥，没有
子 agent 能力。用户在 `~/.claude/agents/*.md` 里为 claude 写好的子 agent 定义
（CC 格式：frontmatter `name/description/tools/model` + 正文指令）对 AliangCode
不可见。

**目标**（2026-10-27 用户拍板）

- 定义复用 `~/.claude/agents`（user 域，零二次配置——与 MCP 复用配置源同一精神）；
- 模型可通过派生工具发起子 agent 循环执行专项任务；
- 执行轨迹全部记 journal tool 行（用户拍板：Task 调用与子 agent 内部工具调用
  都记，零新格式），回放按时间序自然展开。

**非目标**

- 嵌套派生（v1 子 agent 不再开子 agent，depth=1）；
- 并发子 agent（内核 executeTools 本就串行，v1 顺其自然）；
- 项目域 `.claude/agents`（随 cwd 变化，与设备清单「非设备能力」口径一致）；
- CC 高级特性（Resources/Prompts/hooks/ sidechain 等）；
- 内核内置 8 工具自身的调用记录改造（journal v2 范围仍限 MCP + 子 agent 桥）。

## 2. 方案取舍

| 方案 | 结论 |
|---|---|
| **A. 宿主包装，内核零改动（本设计）** | ✅ 每个子 agent 定义包成一个实现 `agentcore.Tool` 的桥，Execute 里递归 `agentcore.Run(子 Config)`；与 MCP 桥同款分层，内核保持「无 subagent 概念、禁 import services、可独立测试」 |
| B. 内核原生 Task 工具 + Config.Subagents | 否：内核引入子 agent 生命周期/配置解析，破坏包边界 |
| C. 内核加工具工厂钩子 | 否：为单一消费方加抽象，YAGNI |

历史注：内核曾刻意不做 Task 工具（8 工具锁死由 `TestDefaultRegistryHasEightTools`
背书）——本设计不改内核注册表，Task 桥由宿主在 run 组装期注入（与 MCP 桥同一
接入点），内核测试面不受影响。

## 3. 架构与数据流

```
runAgentCore 组装（MCP 注入块之后、cfg := 之前）
  ├→ 采集 ~/.claude/agents/*.md（user 域、不递归、上限 24——解析规则与
  │     agent_subagent_inventory.go 同源，采集层独立实现可注入测试）
  ├→ readOnly run：跳过注入（mirror MCP 只读决策——只读不开子 agent 副作用通道）
  ├→ 定义集为空：不注入任何子 agent 工具（无定义即无能力，诚实呈现）
  └→ 每个定义包成 agentCoreSubagentBridge（实现 agentcore.Tool）→ registry.Register
        Name() = 定义名（frontmatter name，缺省回落文件名去 .md）
        Description() = frontmatter description（截 500，模型靠它选）
        InputSchema() = {task: string 必填}（子 agent 的任务输入）
        Execute(ctx, input, env) =
          子 Config := System=基础人设+定义正文 / Tools=内置 8（frontmatter
            tools 白名单生效，内核大写规范名精确匹配含 LS）/ MCP 桥（继承主 run
            已建连 session）/ 无子 agent 工具（depth=1 天然实现）/ Model=主 run
            模型 / Approve=同一审批闭包
            Emit=nil（内核对 nil 不产帧——子 run 静默，防照抄父 Emit 把子帧
            交错进父管道破坏手机流式渲染；回放靠 journal）
            Hooks=nil（父 Hooks 消费共享 steering/followUp 队列，复用会让子
            run 窃走主 run 待插话）
          journal：Task 调用行（onCall 由宿主在主层包装）+ 子 run 内部工具调用
            经子 Config 组装的 onCall 逐调用记 tool 行
          res, err := agentcore.Run(ctx, 子Config, [{role:user, content:task}])
          err → is_error ToolResult（模型自决）；主 run 不挂
```

递归深度=1 的实现：子 Config 的 registry 组装**不包含**子 agent 桥（结构上
不可能嵌套，非运行时拦截）。

## 4. 定义采集（agentCoreSubagentDefsIn）

- 数据源：`<home>/.claude/agents/*.md`（不递归；与 `agent_subagent_inventory.go`
  的 user 域口径一致——该项目域不采集的理由沿用：随 cwd 变化非设备能力）；
- frontmatter 解析：`name`（缺省回落文件名去 .md；**无 frontmatter 或解析失败
  的文件不跳过**——沿用设备清单语义保留回落名，仅当回落后名字为空才跳过；
  frontmatter 块本身损坏导致正文混入的，正文截断规则兜底）、`description`、
  `tools`（逗号分隔白名单，缺省=全量）、`model`（见 §6 模型策略）；
- 正文（frontmatter 之后的全部内容）= 子 agent 的指令文本，拼入子 run 系统
  提示（截 8KB，防病态大文件）；
- 上限 24 个（与设备清单 subagent 清单一致），按文件名排序保证确定性截断；
- 畸形文件（frontmatter 解析失败）跳过 + Warn；
- **安全约束**：定义文件是用户自写配置，正文进系统提示前仍做长度截断；name
  作为工具名过 `sanitizeAgentCoreMCPSegment` 同款白名单清洗（复用，防 API 400
  与重名——清洗冲突沿用序号消解）。

## 5. 工具面与审批

- 子 agent 工具集 = 内置 8 工具（frontmatter `tools:` 白名单生效——名单按内核
  规范名精确匹配，未知名忽略+Warn）+ MCP 桥（继承主 run 已建连的
  `[]agentcore.Tool` 中的 mcp__ 桥，同一 session 复用，不重复建连）；
- **审批贯穿**：子 Config.Approve = 主 run 同一审批闭包——子 agent 内的工具
  调用照过审批管线（默认门 mcp__* 照旧、显式规则照常）；子 agent 不是绕审批
  的通道；
- ScopeCheck/ShellPolicy：沿用主 run 的（readOnly run 根本不注入子 agent；
  执行态 goal run 的 allowed_commands 白名单对子 agent 同样生效——同一闭包）。

## 6. 模型策略

frontmatter `model` 字段尽力而为：值为主 run 当前模型或空 → 沿用；非空但非
glm-*（claude 系名最常见）→ **忽略并 Warn（每定义每 run 一次）**（网关只路由
glm-*，claude-* 必 model_not_found 的教训已在 agent_ai_agentcore.go 落账），
回落主 run 模型。v1 不做名字映射表。

## 7. journal 轨迹

- Task 工具调用本身：宿主在主层包装 onCall → `appendTool(sessionID, name,
  summary="task="+输入摘要, isErr)`（与 MCP 桥同一机制）；
- 子 agent 内部工具调用：子 Config 组装时注入同款 onCall → `appendTool(
  sessionID, tool, summary, isErr)`——回放按时间序自然展开，主/子归属可从
  工具名辨（Task 行 vs 内置名/mcp__ 名）；
- 零新格式、零 reader 改动（tool 行机制已上线）。

## 8. 超时与失败容忍

- 子 run 继承 Execute 的 ctx——内核单工具 10 分钟上限天然罩住整个子循环；
- 子 run 内单工具仍各有 10 分钟上限（内核语义不变）；
- 子 run err / maxIterations 撞顶（50）→ Task 桥返回 is_error ToolResult
  （含错误摘要），模型自决重试或放弃；主 run 存活；
- 建连/采集失败（目录缺失等）→ 定义集为空 → 不注入（run 照常）。

## 9. 测试计划

- 定义采集：frontmatter 解析/name 回落/tools 白名单/畸形跳过/24 上限确定性/正文截断；
- 桥：Name/Description/InputSchema 形态；Execute 成功（子 run 返回文本→ToolResult）；
- depth=1：子 Config 工具表无子 agent 工具（结构断言）；
- tools 白名单：frontmatter 限制后子 Config 工具表精确匹配；
- 模型策略：glm 名沿用/claude 名忽略回落+Warn；
- 审批贯穿：子 run 内调用 mcp__ 工具 → 默认门弹审批（复用 Task 4 基建）；
- journal：Task 行+子 run 内部工具行都落盘（复用 e2e 基建）；
- readOnly：不注入（dial/注册断言同 MCP readOnly 测试样式）；
- 失败容忍：子 run 内核报错 → Task is_error → 主 run 存活收尾；
- 端到端：真 runAgentCore + 注入定义 + 两回合上游脚本（调子 agent→收尾）→
  事件/journal 全链断言。

## 10. 改动文件清单（预估）

| 文件 | 动作 |
|---|---|
| `app/http/services/agent_ai_agentcore_subagent.go` + `_test.go` | 新增：定义采集、task 桥、子 Config 组装 |
| `app/http/services/agent_ai_agentcore.go` | 修改：runAgentCore 组装注入（MCP 块之后）+ readOnly 门沿用 |
| 测试基建文件（mcp_test.go / run_test.go） | 修改：e2e 复用 |

（行级挂点实现计划阶段钉死。）

## 11. 风险与回滚

- 子 run 递归深度失控 → 结构性排除（子 registry 无子 agent 工具）；
- 子 run 失败扩散 → is_error 隔离，主 run 存活（有测试锁定）；
- 审批绕过 → 同一审批闭包贯穿（有测试锁定）；
- 回滚 = 移除组装注入即可，journal tool 行残留无害。
