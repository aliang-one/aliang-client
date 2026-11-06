# 设计:aliang-peer 专门入口与开关——claudecode/codex 获得跨设备 peer 工具(一期)

日期:2026-11-03
状态:已获用户口头批准的设计定稿(brainstorming 产出,待 spec 评审)
关联:2026-11-02 内核感知通道审计(9-agent,15 论断 0 推翻);peer-link 阶段 1+2 已上线(v1.1.91 + PhoneServer v1.0.157)

## 1. 背景与动机

2026-11-02 审计定案了各 AI 内核对跨设备能力的感知现状:

- **agentcore(内置内核)**:独享 peer 五件套(`peer.exec/peer.fetch_file/peer.device_info/peer.device_note/peer.device_list`),注入点 `agent_ai_agentcore.go:1386-1388`(`for tool := range agentCorePeerTools(run) { registry.Register }`),仅拉无推。
- **claudecode / codex(外部 CLI)**:平台**不注入**任何 peer 工具。唯一通道是用户手动注册 `aliang mcp` stdio 服务端(`cmd/mcp.go:19` 给出 claude 接入命令、`:23-25` 给出 codex config.toml 接入片段)——「平台不注入、用户注册才可见」。
- 无任何用户可见的入口与开关控制这条通道;stdio 面的存在感为零。

用户拍板:跨设备能力要「用起来」,这是基本要求;claudecode 与 codex **一期就要**;且这个 MCP 必须有**专门的入口和开关**。

## 2. 目标

1. claudecode 与 codex 内核自动获得与 agentcore **语义等价**的 peer 五件套:执行后端、门禁、审计同源;工具名按各内核面现有惯例(stdio 面 `peer_exec`/`peer_fetch_file`/`device_list`/`device_info`/`device_note`,agentcore 面点名 `peer.device_list` 等),一期**不做统一改名**(见 §8.4)。
2. 提供一个**专门入口 + 总开关**:用户可在设置里看到「设备互访 (Aliang Peer)」这张平台托管卡,一键开/关;开关同时管三内核(agentcore 注入 + claude/codex 注册),单一真值源。
3. 全链路复用现有门禁与安全设施,**零新增凭据、零新增远端授权语义**。

## 3. 非目标(一期明确不做)

- peer push/send 方向、`file.write` 类远程写工具(现有五件套仅拉无推,保持不变)。
- per-kernel 细分开关(一期单一总开关;要细分二期再拆)。
- codex/claude 的 per-run HTTP 桥注入(被否方案,见 §5.2)。
- isolated 信任档下提供 peer 工具(isolated 语义为零本地配置加载,天然不含,不特判)。
- 手机端独立 UI(手机端读同一配置源,一期只保证仪表盘入口可用;手机端展示跟随现有 MCP 设置通道自动出现,不单独开发)。

## 4. 现状锚点(已核实事实,行号为 2026-11-02 审计快照)

| 事实 | 锚点 |
|------|------|
| stdio MCP 服务端已存在,server 名 `aliang-peer`,五工具描述符齐备 | `cmd/mcp.go:159`(server 名)、`:227-336`(peerMCPTools)、文件头 `:2-28`(claude/codex 接入文档) |
| stdio 面三层认证:同用户进程身份 + 56433 loopbackOnly + 出网凭据不出进程;目标机门禁对 MCP 路径同样生效 | `agent_peer_tools.go:1097-1102` 注释;`cmd/mcp.go:225-226` |
| peer 后端与内核解耦:`execute: func(ctx, input) (string, bool)` 统一走 `agentPeerServiceProvider()` | `agent_peer_tools.go:923,940`(`agentCorePeerToolBridge`);`PeerExecLocal/PeerFetchFileLocal` 门面 `:1097+` |
| agentcore 注入点 | `agent_ai_agentcore.go:1386-1388`(评审复核:该区间现为 peer 注入注释块,实际循环 `for _, tool := range m.agentCorePeerTools(run)` 在 :1394;门控推荐落点为 `agentCorePeerTools` 函数本体,`agent_peer_tools.go:967`) |
| 工具名三面两套、描述两处手维护:stdio 面无前缀混名(peer_exec/peer_fetch_file/device_list/device_info/device_note,claude 与 codex 共用,`cmd/mcp.go:229-314`);agentcore 面点名(peer.exec/peer.fetch_file/peer.device_info/peer.device_note/peer.device_list);两处描述为近似副本,**仅执行后端同源** | `cmd/mcp.go:227-337` vs `agent_peer_tools.go:967+` |
| sanitized 档重建 mcp-config 时只合并顶层 user 作用域 mcpServers(→ claude 注册 user 作用域可在 sanitized 存活)。⚠️ 该合并以服务端下发的 `project_mcp_trusted` 为闸(默认 false,`agent_ai_claude_policy.go:184`):不可信项目时根本不重建 `--mcp-config`,可见性靠 claude 自身默认加载 user 作用域——验收须覆盖该情形 | `cmd/mcp.go:20-21` 注释;`agent_ai_claude_policy.go:183-217`(`claudeTierMCPArgs`) |
| 宿主已有读写 codex config.toml 的先例(工具上报采集) | `agent_service.go:1513-1576`(`detectAgentToolsWith`) |
| 宿主已有写内核原生配置文件的先例(quick_setup 写 provider 指纹) | `quick_setup_catalog.go:125,180-202` |
| MCP/技能开关持久化模式:显式改键点唯一写点 + preserve + 无条件头行兜底 | 本仓既有 MCP 管理体系(master 36f7105 加固) |

## 5. 总体方案

### 5.1 选定机制:平台管理的 stdio 注册

平台把已存在的 `aliang mcp` stdio 服务端**注册进两内核的原生配置文件**:

- **claudecode**:`~/.claude.json` user 作用域 `mcpServers["aliang-peer"]`,`command` = 当前 agent 二进制绝对路径(`os.Executable()`)+ 参数 `mcp`。覆盖面:TUI 会话、full/sanitized headless 都可见(sanitized 重建 mcp-config 本就合并 user 作用域,见 §4);isolated 档不含(符合隔离语义,不特判)。
- **codex**:`~/.codex/config.toml` 写 `[mcp_servers.aliang-peer]`(command 同上)。这是 codex 唯一可用的注入通道(审计实锤 codex 无运行时注入面)。
- **agentcore**:注入点受同一开关门控——开关关时 `agentCorePeerTools` 不注册进 registry。

选这条路的理由:一处机制同时满足「两内核 + 专门入口 + 开关」三个要求;服务端代码(`cmd/mcp.go`)与工具后端已 100% 存在,一期只写「注册管理 + 开关 + UI」。

### 5.2 被否方案

- **per-run HTTP 桥扩容**(在 ask_user 的 `aliang` server 上 AddTool):claude 可行但 codex 无通道,且无独立 server 身份,不满足「专门入口」;弃。
- **自动写 provider 式指纹**(quick_setup 风格):只解决认证指向,不提供工具面;弃。
- **prompt 协议**(教模型用围栏发起、宿主代执行):协议黑客味重、可靠性依赖模型格式遵守;弃。

## 6. 详细设计

### 6.1 注册写入器

新增一个注册管理器(建议独立文件,如 `agent_peer_mcp_registration.go`),职责:

- **家目录解析**走既有 `agentHome()`(`app/http/services/agent_home.go:17`,与 `readMCPServersFile` 先例一致,兼容 root agent 下登录用户家目录解析),不写字面 `~`。
- **claude 写入**:读 `<agentHome>/.claude.json` → 设置 `mcpServers["aliang-peer"] = {type:"stdio", command:<abs>, args:["mcp"]}` → 合并写回。合并语义:**保留用户其它一切键与 server 条目,只动本键**(JSON 结构化编辑,不整文件字符串重写;沿用 MCP 开关加固的「唯一写点 + preserve」模式)。
- **codex 写入**:读 `<agentHome>/.codex/config.toml` → 合并 `[mcp_servers.aliang-peer]` 段(command/args 同上)→ 合并写回。**绝不整文件重写**;复用/扩展现有 codex config 读写设施(§4 有采集先例),缺设施则引入最小 TOML 编辑能力,计划期定具体依赖。
- **幂等**:重复执行结果一致;command 与当前二进制路径不一致时更新为当前值(二进制升级/移动后的自愈)。
- **移除**:开关关 → 删除本键(claude)与本段(codex),其余内容不动。
- **命名空间约定**:键名 `aliang-peer` 是平台保留名。若用户此前按 `cmd/mcp.go` 文档手工注册过同名条目(命令可能是相对名 `aliang`),开关为开时平台归一为托管条目;开关为关时一并移除并在状态回显中说明(文档本就引导用户用这个名字,视为同一入口)。

### 6.2 开关与持久化

- 配置:客户配置新增顶层键(建议 `peer_mcp: {enable: bool}`,精确落位与既有配置结构对齐,**计划期决策点**,约束:唯一写点、与 MCP/技能开关同族持久化模式、服务端与 agent 状态同步走既有配置通道)。
- 默认值:**开**——对齐 agentcore 现状(今天就是默认注入),升级不产生行为回退。
- 真值源:单一配置键。agent 侧注册写入器、agentcore 注入门控、UI 卡片三处都只读它。

### 6.3 设置入口(UI)

- 仪表盘设置页 MCP 管理区新增**平台托管卡**:「设备互访 (Aliang Peer)」,与用户自建 MCP 卡视觉区分(托管标记)。
- 卡片内容:总开关、五个工具名列表(按 **stdio 面命名** `peer_exec/peer_fetch_file/device_list/device_info/device_note` 展示——本卡是 claude/codex 的入口,以它们实际可见的名字为准)、目标机门禁状态提示(peer_exec_enabled 默认关等,静态说明文案即可,一期不拉远端状态)、**实际注册状态回显**(claude ✓/✗、codex ✓/✗,见 §7)。
- i18n:中英文案齐备(跟随现有 `t()` 体系)。

### 6.4 agentcore 门控

门控落在 `agentCorePeerTools` 函数本体(推荐,单点管所有调用方,`agent_peer_tools.go:967`)或其调用循环(`agent_ai_agentcore.go:1394`):开关关 → 返回空/跳过注册。readOnly run 现有剥离行为不变。

### 6.5 启动自愈

agent 启动时(开关为开):校验两内核配置中条目存在且 command 指向当前二进制;缺失/漂移 → 静默修复 + 日志。开关为开但修复失败 → 仅 Warn,不影响启动,状态回显如实呈现。

### 6.6 安全与门禁(零新增)

- 工具执行门禁不变:目标机 `peer_exec_enabled` 默认关 + 云端 peer ACL + 全量审计,全部在服务层,stdio/agentcore/任何调用方同规则。
- 开关只控制「内核工具面可见性」,不触碰任何远端授权语义。
- 用户自己终端里开的 claude/codex 同样获得 peer 工具(stdio 注册的全局性)——**定性为特性**;不想暴露就关开关。
- 命令路径为绝对路径,不依赖 PATH 解析;Windows 注意引号转义。

## 7. 错误处理与状态回显

- 配置文件损坏/不可写/JSON/TOML 解析失败 → **只 Warn 不阻断**:不阻断内核 run、不阻断 agent 启动、不阻断开关状态保存(开关状态与注册执行解耦)。
- 状态回显必须诚实:注册结果按内核逐个呈现(claude ✓/✗、codex ✓/✗、失败原因短文案),**不做静默假绿**。
- 回程:内核经 stdio 调用失败时收到 `agentPeerReadableError` 可读错误(既有设施),与 agentcore 体验一致。

## 8. 测试计划

1. **注册写入器单测**(核心):
   - claude.json 合并:保留用户既有 mcpServers 与顶层键;幂等(两次写入结果一致);command 漂移自愈;开关关→仅删本键。
   - config.toml 合并:保留用户段落(含非 mcp 段);幂等;开关关→仅删本段;畸形 TOML 的失败路径(不写回、返回可读错误)。
   - 用户同名条目归一与移除语义(§6.1 命名空间约定)。
2. **agentcore 门控单测**:开关关 → registry 无 peer 工具;开 → 与现状一致(回归锚)。
3. **启动自止单测**:条目缺失/路径漂移/开关关三种初始态的修复行为。
4. **语义等价契约测试**:现状两处描述符(`peerMCPTools` `cmd/mcp.go:227`;`agentCorePeerTools` `agent_peer_tools.go:967`)是手维护近似副本——三个工具名不同、描述措辞有差,**仅执行后端同源**。一期不统一改名;契约测试钉「语义等价」:按名映射对照后,入参 schema 字段集一致、后端调用同一服务方法、关键行为约束(exec 串行/fetch 需对端开关)两边描述均存在。
5. **跨平台**:注册写入器测试须在 GOOS 尾段命名上避开平台后缀陷阱(仓库守则),新测试先 `-list` 确认真实执行。

## 9. 验收标准(真机)

1. 开关默认开,升级后 claude 会话问「列出我的设备」→ AI 调 `device_list`(stdio 面;agentcore 面同名工具为 `peer.device_list`)返回设备清单;codex 同。
2. 「从 XX 设备拉 ~/a.txt」→ `peer_fetch_file` → 本机 AliangInbox 落地,手机收到 transfer.progress。
3. 目标机 `peer_exec_enabled` 关闭时,`peer_exec`(stdio 面;agentcore 面为 `peer.exec`)返回可读门禁拒绝。
4. 开关关掉后:新起的 claude/codex 会话工具消失,agentcore peer 工具消失;UI 状态如实显示已注销。
5. ask_user、审批卡、trust tier 行为零回归(本设计不触碰这些链路)。
6. sanitized 档 + 项目不可信(`project_mcp_trusted=false` 默认)场景:aliang-peer 工具经 claude 默认 user 作用域加载仍可见(此路径不重建 `--mcp-config`)。

## 10. 风险与开放问题

- **工具面全局暴露**:stdio 注册对本机所有 claude/codex 进程生效(含用户私人终端会话)。已定性为特性;若后续要收窄,二期可加作用域选项。
- **config.toml TOML 编辑依赖**:仓库**无在役 TOML 写设施**——quick_setup 的 `mergeCodexTOML` 引擎已随 render 链退役(`quick_setup_catalog.go:195`);计划期必须为最小 TOML 编辑能力立项(引入库或最小实现),「不整文件重写」是硬约束。
- **CLAUDE.md/AGENTS.md 不动**:本设计不写任何项目级指令文件;隔离档/审计语义不变。
- **开放问题(计划期决策点)**:①配置键精确落位与命名;②TOML 库选型;③设置卡在手机端的呈现时机(一期不开发,仅保证配置同源)。
