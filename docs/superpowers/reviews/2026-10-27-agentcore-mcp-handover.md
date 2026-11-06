# agentcore MCP 客户端 + 子 agent — 交接与真机冒烟清单

日期：2026-10-27（子 agent 章节同日追加）
分支：`feature/agentcore-mcp`（已合入 master `4a3586e`）＋ `feature/agentcore-subagents`（6 commits，基于 4a3586e，终审前）
状态：MCP **已合入**；子 agent **终审前**（全包测试绿、vet/gofmt 干净）
Spec：`docs/superpowers/specs/2026-10-26-agentcore-mcp-design.md` + `2026-10-27-agentcore-subagents-design.md`
Plan：`docs/superpowers/plans/2026-10-26-agentcore-mcp.md` + `2026-10-27-agentcore-subagents.md`

## 子 agent 交付一览（feature/agentcore-subagents）

| 任务 | 内容 | 关键 commit |
|---|---|---|
| T1 | 定义采集（~/.claude/agents CC 格式，tools/model/正文，名消解避内核保留名） | dfd7e0b/9b199ae |
| T2 | 递归桥（子 Config Emit/Hooks 置零、depth=1 结构保证、tools 白名单、glm 模型策略、journal 装饰器、失败容忍） | d06719b/c759017 |
| T3 | runAgentCore 接入（readOnly 不注入、审批贯穿、e2e×3+组装收敛到参数化 harness、内核名从注册表派生） | 28dff75/cbcd242 |

## 子 agent 真机冒烟清单（合并后必查）

1. 写一个 `~/.claude/agents/reviewer.md` → AliangCode 会话让它派发 → 手机无审批卡（内置工具非 mcp）/ 有则按策略 → 子 run 结果回填主会话 → journal 回放见 Task 行（tool=reviewer，summary=task=…）
2. frontmatter `tools: Read` 白名单 → 子 agent 尝试 Write 应被「未知工具」拒（单测已锁，真机复核）
3. readOnly/goal fork run → 上游请求工具表零子 agent 条目（单测已锁，真机复核 fork 场景）
4. frontmatter `model: sonnet` → 日志 Warn 回落主模型，子 run 正常
5. 观测：启动日志「注入 N 个子 agent 桥」Info


## 交付一览

| 任务 | 内容 | 关键 commit |
|---|---|---|
| T1 | 原始配置采集（复用清单层解析，未脱敏连接专用，段白名单清洗） | 26e9220 |
| T2 | 连接编排+工具桥（in-memory 可测、分页取尽、panic 双层兜底、64KB 自截、名钳 128+冲突序号、per-server 超时、headers 预判跳过） | d05862e/6371a57/1a8f459/3b02aa0 |
| T3 | journal v2 工具行 + reader（回放轨迹、不计对话计数、全序/游标分页断言） | 6df9a9d/57b1ba4 |
| T4 | 审批默认门（matchedID 空 + mcp__ 前缀 → 手机审批；显式规则优先） | 50aa6d1 |
| T5 | runAgentCore 接入（采集-建连-注册-解耦关停）+ readOnly 跳过注入（mirror claude 先例）+ e2e | e2b943f/1aee02a/bdcbde2 |
| 终审 | 观测 Info 两条 + UTF-8 劈半落账 | （收尾 commit） |

## 真机冒烟清单（合并后必查，按序）

1. **回放容忍度（spec §8 指派核对，终审点名未留痕——必查首项）**：手机/服务端对 Transcript `role="tool"` 行的解析容忍度。若不容忍：reader 加过滤降级（数据已在文件），前端改动另议
2. 给 `~/.claude.json` 配一个真实 stdio MCP server（如 npx 形态）→ AliangCode 会话里让它调用 → 手机弹审批卡（默认门）→ 放行 → 结果回填 → 会话回放可见 tool 行
3. 显式放行规则的 server → 调用无审批卡直接执行
4. kill 一个 server 子进程 → 后续 run 该 server 被跳过（Warn），run 不挂
5. readOnly/goal fork run → 上游请求工具表零 `mcp__` 条目（已有单测锁定，真机复核 goal fork 场景）
6. 观测：启动日志可见「已连接 N/M 个 server」「注入 K 个 MCP 工具」两条 Info

## 已知限制（均已落账代码注释/spec）

- **headers 注入（v1 限制）**：SDK 传输层无 Headers 字段，带鉴权头的 http/sse server 预判跳过（spec §5 已知限制节）；后续以自定义 RoundTripper 补
- **慢关停**：stdio 关停最坏 10-15s/个；closeAll 已解耦到 detached goroutine（防拖 run 收尾），串行实现保留；建连仍串行（最坏 ~4 分钟/24 server，v1 取舍已注释）
- **多页 cursor 测试缺口**：in-memory 标准 server 不分页，nextCursor 跟随逻辑无自动化覆盖（需手写 JSON-RPC transport，已记账）
- **UTF-8 劈半**：5 处字节级截断切点可能劈多字节 rune → U+FFFD，纯观感降级（收尾 commit 落账）
- **scopeCheck 纵深**：readOnly 拒绝清单含 mcp__ 前缀（防注入条件与围栏条件漂移的保险）

## 评审过程中修掉的真 bug（供未来复盘）

- 桥 Execute 外层 defer 缺 `recover()`（计划继承 bug，兜底实为死代码；scratch 程序实证穿透）
- 工具名总长可达 135 > API 上限 128（整轮 400）+ 清洗后同名静默遮蔽
- 计划的共享 10s 超时预算会误杀排后 server → per-server
- nil InputSchema 直通 → `"input_schema": null` → 单畸形工具炸整 run
- readOnly run 可调外部副作用 MCP 工具（围栏缺口，mirror claude 先例堵上）
