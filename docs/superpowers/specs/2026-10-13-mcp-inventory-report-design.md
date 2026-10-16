# MCP 能力清单上报(设备级)设计

- 日期:2026-10-13
- 状态:已评审通过(设计对话定稿)
- 涉及仓库:alianggate(客户端,本仓)、AliangPhoneServer(服务端,`~/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer`)

## 1. 背景与目标

2026-10-13 两仓审计确认:

- 客户端已经把 6 个 AI CLI 工具本体(codex/claude/claudecode/opencode/pi/gemini)的名称/路径/可用性/版本/effort 档,经 `agent.hello` 与 HTTP `/api/agent/status` 上报,服务端落 `devices.tools` JSON 列并经 `publicDevice` 透给手机 App。
- **各 CLI 配置的 MCP 服务器完全不在上报里**:Claude 侧客户端虽有解析(`claudeTierMCPArgs`,agent_ai_claude_policy.go:116-167)但仅用于远程 run 注入 `--strict-mcp-config`;Codex 侧 `~/.codex/config.toml` 的 `[mcp_servers]` 无任何解析;服务端 zod v3 默认 strip + hello 手工白名单读键,即便客户端塞 MCP 字段也会被无痕丢弃。

目标:把「这台电脑支持哪些 MCP 服务」纳入设备能力清单,与现有 tools/agent_tools 同构地存到云端(PhoneServer),供将来规划中的「统筹 AI」(持续对话、跨需求编排)消费。本期只做数据面(上报/存储/透出),不做消费端 UI 或编排接入。

## 2. 决策记录

| 决策点 | 结论 | 理由 |
|---|---|---|
| 首要目的 | 先存储到云端,数据面打通 | 统筹 AI 是将来时,先把能力清单补全 |
| 字段范围 | 名称 + 传输类型 + 命令行(argv);env 值排除;疑似密钥参数打码 | 兼顾「能推断能力」与安全 |
| CLI 覆盖 | 仅 claude + codex | 两个真正支持远程 run 的 provider;其余 4 个本就 detection-only |
| 负载位置 | 挂在 `AgentTool` 上(`mcp_servers` 字段),随现有 `tools[]` 流动 | 语义正确;客户端一结构体一填充;服务端两处白名单;`devices.tools` 是 JSON 列零迁移 |
| 项目级 `.mcp.json` | 不采集 | 随 cwd 变化,不属于「设备级能力」 |
| claudecode 别名 | 与 claude 同源同清单 | 同一 CLI 的别名,避免统筹 AI 特判 |
| 变更传播 | 复用 hello 全量重发,不新增 rev 机制 | hello 本就四时机重发(连接/周期/vibe 变化/改名) |

## 3. 客户端设计(alianggate)

### 3.1 数据模型(models/agent.go)

```go
// AgentMCPServer 是设备上一个 CLI 配置的 MCP 服务器摘要(脱敏后)。
type AgentMCPServer struct {
    Name      string `json:"name"`
    Transport string `json:"transport"`          // "stdio" | "http" | "sse"
    Command   string `json:"command,omitempty"`  // stdio: argv join 后的命令行
    URL       string `json:"url,omitempty"`      // http/sse 端点
    Source    string `json:"source,omitempty"`   // claude: "user"|"plugin";codex: "config"
}

// AgentTool 追加:
// MCPServers 是该 CLI 配置的 MCP 服务器清单(设备级,脱敏后),nil=未采集或无。
MCPServers []AgentMCPServer `json:"mcp_servers,omitempty"`
```

### 3.2 采集与解析(新文件 `app/http/services/agent_mcp_inventory.go`)

**Claude**(`~/.claude.json` 顶层 `mcpServers`,经现有 `agentHome()` 解析路径):

- 采集顶层 user scope 条目(`source: "user"`);`type` 缺省视为 `stdio`
- 采集已启用插件的 MCP(`enabledPlugins` 门控 + 插件 `.mcp.json`,逻辑参照 `readPluginMCPServers`,agent_ai_claude_policy.go:219-263;`source: "plugin"`)
- **不读项目级 `.mcp.json`**
- 与 `claudeTierMCPArgs` 的关系:本设计需要「纯解析、无注入副作用」的函数。从 agent_ai_claude_policy.go 拆出可复用的读取层(`readMCPServers` 系已有雏形),run 注入路径改为调用同一读取层——**行为不变,只收编数据源**

**Codex**(`~/.codex/config.toml`,BurntSushi/toml v1.4.0 已在 go.mod):

- 解析 `[mcp_servers.<name>]` 表:`command`(+`args`)→ stdio;`url` → 一律归类 `http`(codex 配置不区分 sse/http,统一按 http 上报;无 url 且无 command 的条目丢弃)
- `enabled = false` 的条目跳过,不占用 24 条上限额度(2026-10-16 增补:官方语义「临时停用而不删配置」,codex 自身不会启动停用的服务器,上报它会让能力清单虚增;`nil`/缺省/`true` 均视为启用)
- env 键读取后立即丢弃,不进任何结构体

**填充规则**(`detectAgentToolsWith`,agent_service.go:1473):

- `claude` 与 `claudecode`(别名回落命中时)填充同一份 Claude 清单
- `codex` 填充 Codex 清单
- `opencode`/`pi`/`gemini` 不填(nil)
- 解析失败/文件缺失 → nil,不阻塞工具本体上报(fail-soft,与现有「失败留空视为未知」一致)

### 3.3 脱敏

- env 值永不进入 `AgentMCPServer`(解析层直接丢弃)
- argv join 前逐项检查:长度 >32 且不含空白的 token → 替换为 `<redacted:N>`(N=原长度)
- **凭据形态 header 参数定向打码(2026-10-16 增补)**:argv 元素内含 `authorization`(同时覆盖 `Proxy-Authorization`)或 `api-key`(同时覆盖 `X-Api-Key`)锚点——ASCII 大小写折叠匹配,header 名与冒号之间容忍空白与引号(`Authorization :`、`--header={"Authorization": "…"}` 形态同样拦截;名字后跟其他字符如 `authorizations:` 不构成锚点)——时,保留「锚点前前缀(如 `--header=`)+ header 名 + 已知 scheme 词(Bearer/Basic/Token/Digest)」,凭据主体替换为 `<redacted:N>`(N=原字节长)——不受长度阈值与空白豁免约束。动机:`--header "Authorization: Bearer ntn_…"` 恰是「单元素含空白」形态,通用规则的白名单豁免挡不住它;未知 scheme 词视为凭据整体打码(宁可错杀,自然语言里出现锚点字样会被误伤,可接受)。锚点定位不得用 `strings.ToLower` 的下标切原串(个别 Unicode rune 会让 ToLower 改变字节长度 → 下标错位/越界 panic),须做 ASCII 折叠扫描
- URL 的 query 参数值长度 >24 → 值替换为 `<redacted>`
- URL userinfo 一律打码(`<redacted>`),fragment 一律丢弃——非 query 形态的凭据同样不得外泄
- **URL 解析失败 fail-closed(2026-10-16 增补)**:`url.Parse` 失败的畸形 URL(未转义空白/百分号、控制字符等)无法可靠定位 userinfo/query/fragment 边界,整串替换为固定 `<redacted-url>`,宁可少报也不泄漏
- 名称截 64 字符,command/URL 截 512 字符,每工具上限 24 条(超出截断并丢弃,不做分页)
- 截断按字符(rune 安全,复用 `truncateAgentText`),避免劈开多字节字符产生无效 UTF-8

### 3.4 缓存

照搬版本探测的按内容缓存模式(`detectAgentToolsWith` 注释:「探测按二进制内容缓存,稳态零开销」):以「本次解析实际读取的全部文件(路径+mtime+size)」为键缓存解析结果。**Claude 的清单是复合源**(`~/.claude.json` + `.claude/settings.json` 的 enabledPlugins + 插件安装清单 + 各插件 `.mcp.json`),缓存键必须覆盖所有被读文件——任一文件变更即失效,否则切换插件不刷新。Codex 单文件同理。文件未变直接返回缓存;`detectAgentTools` 会被快照与 `findAgentTool` 多处调用,缓存避免重复读盘。

## 4. 服务端设计(AliangPhoneServer)

### 4.1 schema 与规范化

- `server/src/schemas.ts`:`agentToolSchema`(现 :14-30)追加 `mcp_servers: z.array(mcpServerSchema).max(24).optional()`;`mcpServerSchema` = `{name: string().max(64), transport: enum["stdio","http","sse"], command?: string, url?: string, source?: string}`(name 加 max(64) 与客户端截断对称)
- `server/src/modules/device/normalize.ts`:`normalizeAgentTools`(现 :66-80)白名单追加 `mcp_servers` 透传——逐条只保留五个字段,数量截 24,command/url 截 512,与客户端上限一致(双端兜底)
- **入口白名单的精确分工**(避免实现时误判单一闸门):仅 WS hello 走 `normalizeAgentTools`(modules/agent/handler.ts:318);注册(modules/device/register.ts:74)与 HTTP 同步(modules/agent/statusSync.ts:43)直接存 zod parse 后的 `input.tools`,它们的唯一闸门是 `agentToolSchema`——所以两处(schemas + normalize)都必须改,缺一不可

### 4.2 存储与暴露

- 落 `devices.tools` JSON 列,零迁移(注册/HTTP 同步/WS hello 三个现有入口在 schemas+normalize 两处加白后自动生效,见 §4.1 分工)
- `publicDevice` 序列化器(modules/device/serializers.ts:27,29)整包透出 `tools: device.tools ?? []`,不逐子字段剥皮 → App/GET /api/devices 免费可见,无需改动

### 4.3 兼容性

| 组合 | 行为 |
|---|---|
| 旧客户端 → 新服务端 | 无 `mcp_servers` 字段,optional 收下为 undefined,无影响 |
| 新客户端 → 旧服务端 | zod strip 静默丢弃,无报错、无日志(现状即如此,无害) |
| 部署顺序 | 先服务端后客户端(惯例;实际任意顺序均安全) |

## 5. 变更传播

不新增 rev/心跳字段。hello 本就全量携带 `tools[]` 且四时机重发(agent_remote_ws.go:448/:528/:538/agent_rename_cache.go:245),MCP 配置文件变更 → 下次快照重新解析(mtime 缓存失效)→ 下个周期 hello 自动携带新清单。10s 心跳与 `agent_tools_rev` 注册表机制不动。

## 6. 测试

**客户端**(纯逻辑测试文件不带 GOOS 后缀,避开 `_windows_test.go` 在非 Windows 静默跳过的陷阱):

- Claude 解析:顶层条目、插件门控(enabledPlugins 未启用不采)、畸形 JSON fail-soft、缺文件 nil
- Codex 解析:stdio 形态(command+args)、url 形态、无 command 无 url 丢弃、畸形 TOML fail-soft
- 脱敏:长 token 打码、URL query 打码、名称/长度/数量上限
- 填充:detect 注入测试(claude/claudecode/codex 有,其余 nil;claudecode 别名回落同清单)
- 缓存:mtime/size 变化失效,未变复用
- run 注入收编:claudeTierMCPArgs 重构后既有测试全绿(行为不变)

**服务端**:

- normalize/schema:新字段收下与逐字段白名单、数量/长度截断、旧 agent 无字段兼容、非法 transport 拒绝
- 既有 tools 上报测试全绿

## 7. 流程约定

- 客户端:本 worktree 分支 `worktree-feat+mcp-inventory-report`;服务端另行开分支(实现阶段创建)
- 全中文提交标题(「新增:」),不自动 commit,推送前信息定稿(master 保护禁强推)
- 先部署服务端,再发客户端

## 8. 本期不做(YAGNI)

- 项目级 `.mcp.json` 采集
- opencode/gemini/pi 的 MCP 解析
- rev 漂移机制、心跳字段扩展
- 手机 App UI 渲染、审批/策略消费、run 工具集组装
- MCP server 的 tools/prompt 能力探测(需连服务器,成本高且不稳)
