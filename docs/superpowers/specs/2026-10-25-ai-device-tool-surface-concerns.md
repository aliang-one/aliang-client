# AI 设备工具面（MCP）—— 待探索的问题清单

- 日期:2026-10-25
- 状态:**仅记录,不实现**。当前阶段先做设备间文件传输(见 `2026-10-25-device-file-transfer-design.md`),本文件留待后续单独探索。
- 来源:2026-10-25 对 file-transfer spec 的工程评审 + 用户对场景定义的关键修正。

## 1. 场景定义(用户 2026-10-25 原话,优先级最高)

> 「我们的场景是提供能力,用户还是在某个具体设备和项目下运行 claudecode 和 codex 这种,只是对话的过程中,提供了我们的调用 alianggate 管理能力的上下文。」

> 「并不是让用户在服务端对话,让大模型根据用户的描述去某台设备上执行执行,这中问题太多了,比如不同设备上都有同一个项目,那么 llm 肯定会卡壳,所以这些不是我们的场景。」

要点:

- 宿主 agent(CC/Codex)**已经在**某台设备的某个项目目录里运行 → 设备与项目由宿主上下文天然确定,不存在歧义。
- alianggate 只负责**追加一个工具面 + 一段上下文**,让宿主在用户明确指名远端时能跨设备操作。
- **不是**服务端编排、**不是**服务端选设备、**不是**单一可变 `Target` 的模型。

## 2. 已撤回的批评(记录以免重犯)

| 曾经的批评 | 撤回原因 |
|---|---|
| 「单一可变 `Target` 撑不住跨设备」 | 那是 commandGen 的约束。宿主自身上下文已确定设备,不存在 `Target`。 |
| 「跨设备项目身份需要 `repo_key`」 | 只有「服务端按名字猜设备」时才致命。宿主自己知道自己在哪个目录。 |
| 「跨设备写是零审批路径」 | 前提是走 commandGen。若工具走**宿主自己的 MCP 面**,MCP 工具调用天然受 CC 权限系统管辖。 |

## 3. 仍然成立的缺口(按严重度)

### 3.1 [P0] 用户自己开的 TUI 会话,一个 alianggate 工具都没有

现有 `mcp__aliang__*` **只在 agent 自己 spawn 的 run 里**注入:

- `app/http/services/agent_ai_tool_ask.go:405-460` `withAgentToolAskBridge()` 构造
  `{"type":"http","url": baseURL+"/mcp/aliang/"+token, "timeout":3600000}`,
  合并进 `--mcp-config` 临时文件,server 名 `aliang`。
- 配合 `--strict-mcp-config`(`app/http/services/agent_ai.go:7690`、`agent_goal.go:1841`、
  `agent_ai_claude_policy.go:170/216/328`)。
- 用户在终端手动 `claude` 启动的会话,配置里没有这条 → **工具面完全不发生**。

### 3.2 [P0] 「skill / 上下文」没有投放口

- `buildMCPServer()` 用 `mcp.NewServer(&mcp.Implementation{Name:"aliang-interact"}, nil)`
  —— ServerOptions 传 nil,`InitializeResult.Instructions` 恒空。
- go-sdk v1.8.0 `mcp/server.go:75` 有 `Instructions string`,`:956`/`:2105` 会写进 InitializeResult。
- 这是 MCP 里写「怎么用我这套工具」的标准位置。
- **待实测**:CC 客户端是否把 `instructions` 注入模型上下文、以什么形式。

### 3.3 [P1] 常驻注册的形状与它带来的新风险

- **端点不能依赖 agent 进程存活**。http 型 server 在 agent 挂掉时是「连接失败」,CC 每次启动都会卡一下。
  更稳的是 **stdio shim**:极小二进制,`os.Executable()` 定位,连本机端口;不通就返回干净的 tool error。
- **token 落 0600 文件**。
- **注册走 `claude mcp add` / `codex mcp add`,不要手改** `~/.claude.json` / `~/.codex/config.toml`。
  全仓现在只有读路径(`agent_mcp_inventory.go:290-300,331-346`),不要开手写的头。
- **代价**:`--strict-mcp-config` 是同名 server 的防伪闸。项目里的 `.mcp.json` 可以声明一个也叫
  `aliang` 的 server;`app/http/models/agent_ai_tool_ask.go:6-25` 的精确名白名单
  (`mcp__aliang__ask_user` 等三条)正是为这个加的。常驻注册后这层没了,恶意仓库可影子覆盖。
  → 要么保留前缀区分,要么接受并在文档写明。

### 3.4 [P1] `transfer_file` 必须异步

2GB 传不完一个 MCP 调用。工具只返回 `transfer_id`,进度/完成走轮询或事件。
file-transfer spec 没写这条,因为它假设的是服务端编排。

### 3.5 [P1] 单槽太粗

`app/http/services/agent_file_upload.go:38-41` 包级单槽、`:66-75` 无条件互斥。
「一句话触发两个传输」时立刻打架。至少做成小池子或 per-transfer。

### 3.6 [P1] 远程 `edit` 与 transfer 不是一个风险等级

transfer 的产物落在收件箱,宿主看得见、用户能审;远程 `edit` 是**在另一台机器上改文件、本地没有 diff 可看**
—— 这才是「零审批远端写」的真正形态。
→ P1 只做 read/grep/list/transfer;edit 单独立项且必须带 diff 预览。

### 3.7 [P1] `RunWithGuards` 默认 10s 超时,且超时后丢弃 handler goroutine

- `app/tools/dispatch.go`:超时写 `tool_timeout` 并**放弃等待**(不是取消),远端动作还在跑。
- 跨设备调用是网络操作,10s 太短;新工具要显式设 `Caps.TimeoutMs`,
  并容忍「调用方已放弃、结果仍会到达」。

### 3.8 [P2] 设备上报的自由文本只能进工具**返回值**

`commandGen/toolset.ts:33` 把设备 `description`/`parameters` 原样塞进 tool def
(配合 `normalize.ts:168-174`「no semantic whitelist」),跨设备合并后构成 A→B 提示注入通道。
新工具面不要继承:设备文本只能出现在**工具返回值**,绝不能进**工具定义**或**系统提示**。

### 3.9 [P2] 热点项目清单降级为「便利索引」

只用于「同步到上周那个项目」这种模糊指代,不作为正确性设施。
项目身份 = `(deviceId, path)`(`modules/device/reported.ts:173-179`)。

## 4. 建议的形状(待评审)

一句话:**MCP 面只做宿主做不到的事 —— 够到别的设备;本机能做的交给 CC 原生工具。**

- 工具集:`list_devices` / `list_projects(device_id?)` / `read_file(device_id, path)` /
  `grep(device_id, ...)` / `transfer_file(...)`。
  **不要**把 list_dir/read_file 复刻成本机版 —— CC 已有,多一份只抢上下文、多一次权限弹窗。
- 本机 agent 的 `device_id` 作为默认值,`device_id` 是可选参数(缺省=本机)。
- `instructions` 写:设备树是什么、`device_id` 怎么来、传输是异步的、收件箱在哪。
  **不写**具体设备清单 —— 那是运行期数据。
- 能力位(capabilities)用于**发现**,不用于**授权**。授权永远是 CC 权限系统 + 用户点头。

## 5. 待探索问题

1. CC 是否消费 `InitializeResult.instructions`?以什么形式注入?有无长度上限?
2. CC 的 MCP 工具权限确认粒度:能否对 `mcp__aliang__transfer_file` 单独设 allow/ask/deny?
3. Codex 的 MCP 支持(`codex mcp add`)与 CC 的差异面。
4. stdio shim 的实现成本 vs 直接 http 型的体验差异(启动卡顿实测)。
5. 常驻注册与 `--strict-mcp-config` 防伪的替代方案。
6. 跨设备工具在「agent 未上线」时的降级语义(建议:tool error 带 `retryable` 结构化字段)。
7. 是否需要一个 `device_id` 的稳定短别名(设备名可重名)。
