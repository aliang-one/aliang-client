# 设计:统一 MCP 注册表——alianggate 第一方 MCP 资产,三内核一套配置(aliangcode 原生 MCP 管理)

日期:2026-11-05
状态:**已实施**(2026-11-06,分支 feat/aliang-mcp-registry,19 commits,未推送未合并)。brainstorming 四问定稿 → spec 评审两轮(3+0 缺陷)→ 计划评审三轮(4+1+2 缺陷全修)→ subagent 逐任务执行(Task 0-11,每任务实现者+spec 评审+质量评审三段)→ 全量回归绿(Go 六包 + vitest 282/282)。实现取舍注明:§7.2 管理卡片以注册表原文为数据源渲染可编辑行(全部即 aliang 行),遗留条目只读展示由其正下方的集成清单卡片天然承担,来源列以「aliang + 附加遗留来源」提示呈现——与 §7.2 文字的等价实现,非功能偏差。遗留待办:真机冒烟清单(Task 11 Step 4 六项,待装新包后执行)+ PhoneServer source=aliang 容忍性核验(部署清单项)
关联:2026-11-05 MCP 注册链路审计(5-agent workflow + 主会话第一手读码,全部锚点已核实);peer MCP 体系(2026-11-03 入口开关 + 2026-11-05 按内核细分,写盘纪律的先例来源)

## 1. 背景与动机

现状盘点(全部已核实,见 §4 锚点):

- **agentcore(aliangcode 内置内核)**自 MCP 能力上线起就在消费第三方 MCP server,但配置源是 **Claude Code 的用户配置文件** `~/.claude.json` 顶层 `mcpServers` + 已启用插件 `.mcp.json`。机器上没装 Claude Code 时,这份文件要么不存在、要么名义上是 alianggate 代替 Claude 创建的——**概念归属错误**。
- **claudecode(外部 CLI)**由宿主在 sanitized/full 档合并注入(`claudeTierMCPArgs`),数据源同样是 claude.json user 域 + 插件 + 项目 `.mcp.json`。
- **codex(外部 CLI)**只能用户手动往 `~/.codex/config.toml` 写 `[mcp_servers.*]`,宿主不管。
- **仪表盘对 MCP 只有只读清单**(GET `/api/agent/integrations`)与全局/会话开关,**没有「新增/编辑/删除第三方 MCP server」的任何 UI 或 API 写通道**。唯一写 `mcpServers` 条目的端点是 aliang-peer 托管注册,且只动 `aliang-peer` 一个键。

结论:注册第三方 MCP 目前唯一途径是手动编辑 Claude 的配置文件(或 `claude mcp add`)。alianggate 需要一个**第一方、自洽、三内核通用**的 MCP 管理能力。

用户拍板(四问四答):

1. **统一注册表**:配置是 alianggate 的第一方资产,三个内核都是消费者;
2. **独立文件** `~/.aliang/mcp.json`(不挂 customer 配置深合并);
3. **合并 + 注册表胜出**:`~/.claude.json` 遗留源继续读,同名键注册表覆盖,存量零感;
4. **三内核一期全上**:agentcore 直读 + claudecode 合并注入 + codex config.toml 托管块。

## 2. 目标

1. alianggate 拥有唯一真值源 `~/.aliang/mcp.json` 的 MCP 注册表;**没有 Claude Code 的机器完全自洽**。
2. 仪表盘 UI 与 HTTP API 提供注册表的**增/删/改/查/启停**;写 API 落盘后 codex 托管块同步,agentcore/claudecode 每 run 现读天然即时生效。
3. 遗留源(`~/.claude.json` 顶层 + 插件声明)**继续生效**,合并语义唯一且可预测:claude.json → 插件 → 注册表(同名后者胜)。
4. 集成清单页为每条 server 标注来源(`aliang` / `claude.json` / `plugin`),用户可见「哪条是谁配的」。

## 3. 非目标(一期明确不做)

- 注册表云同步 / PhoneServer 下发 / 跨设备漫游(设备本地资产;将来要同步时是「同步一个文件」的问题)。
- per-server 健康探测、连通性测试按钮。
- per-server 超时/工具上限配置(沿用全局常量 24 server / 128 工具 / 10s 建连)。
- 「从 claude.json 一键导入」迁移工具(选了合并方案,不需要迁移)。
- 手机端独立管理 UI(聊天页会话级「更多 › MCP」开关按 server 键过滤,注册表新条目自动出现在该列表;手机端管理面板二期)。
- peer MCP 五件套与 `customer.peer_mcp` 开关体系的任何改动(两条通道完全独立,见 §8)。

## 4. 现状锚点(2026-11-05 审计快照,行号已核实)

| 事实 | 锚点 |
|------|------|
| agentcore 采集链:`~/.claude.json` 顶层 mcpServers + 已启用插件 `.mcp.json`,畸形/disabled 跳过,cap 截断 | `app/http/services/agent_ai_agentcore_mcp.go:94-102` |
| 常量:24 server / 128 工具 / 10s 建连 / 描述截 500B / 迟到 dial 收尸 2s / 建连并发 8 | `agent_ai_agentcore_mcp.go:27-44` |
| http/sse 带自定义 headers 的条目 agentcore v1 直接跳过(go-sdk transport 无 Headers 字段) | `agent_ai_agentcore_mcp.go:272-274,403-409` |
| 注入点:runAgentCore 每 run 采集→建连→包桥(`mcp__<server>__<tool>` 白名单清洗)→注册 | `agent_ai_agentcore.go:1303-1343`;清洗 `agent_ai_agentcore_mcp.go:67-87` |
| 三层门控:全局 MCP 开关(默认开)/ readOnly run 剥离 / 会话级 `run.disabledMCPServers` 过滤 | `agent_ai_agentcore.go:1304-1336`;开关定义 `processor/config/types.go:630-634,653-665` |
| MCP 默认门:无显式规则命中的 `mcp__*` 调用一律强制手机审批 | `agent_ai_agentcore.go:2121-2125` |
| agentHome:非 root = `$HOME`(本机 `/Users/mac`);root 场景解析 console 属主 | `app/http/services/agent_home.go:17-20`;`internal/runtimepath/runtimepath.go:230` |
| claude 合并注入:claudeTierMCPArgs 合并 user 域→插件→项目 `.mcp.json` 后 `--mcp-config`;判据 `projectMCPTrusted` | `app/http/services/agent_ai_claude_policy.go:152-170,183-217`;`agent_ai.go:966` |
| claude.json 读取器(顶层 `mcpServers`,缺失静默容忍) | `agent_ai_claude_policy.go:152-168` |
| **写盘纪律先例(claude.json)**:全量往返保留未知键 / UseNumber / SetEscapeHTML(false) / tmp+rename 原子写 / 幂等跳写 | `agent_peer_mcp.go:252-314,415-443` |
| **写盘纪律先例(codex config.toml)**:文本级块拼接 `[mcp_servers.<name>]`,表头到下一表头整块替换/删除,其余内容原样;非规范表头形态拒写 | `agent_peer_mcp.go:318-360+` |
| peer 托管名常量与启动自愈挂点 | `agent_peer_mcp.go:34`;`app/agentruntime/server.go:90` |
| 只读清单端点与脱敏视图;声明「绝不写配置文件」 | `app/http/routes/routes.go:298`;`agent_integrations.go:10,114,274-277` |
| 集成清单 server 结构体:**`source` 字段已存在**,现枚举 `user`(claude.json)/`plugin`/`config`(codex),填充于 mcp 清单采集 | `agent_integrations.go:52-68(57-58)`;`app/http/services/agent_mcp_inventory.go:320-321,394` |
| source 已被前端渲染(`sourceLabels()`)且随设备上报进云端 `mcp_servers[]` | `app/website/src/utils/integrations.js`;`AgentIntegrationsSettings.vue:53`;`app/http/services/agent_service.go:1558-1559` |
| codex 清单采集读 config.toml 全部 `[mcp_servers.*]`,**无托管前缀过滤**(现存 aliang-peer 块也照此透出) | `agent_mcp_inventory.go:356-396` |
| claudeTierMCPArgs 两条 nil 快速路径:`!projectMCPTrusted` 直接返 nil;项目 `.mcp.json` 零贡献也返 nil(→原生发现);strict 模式原生会抑制插件 server,故 payload 必须重含插件条目 | `agent_ai_claude_policy.go:184-186,195-197,325-329` |
| 前端:设置页 Agent tab 四子 tab(records/user-agent/aliangcode/peer-access);集成清单在 user-agent | `app/website/src/components/settings/agent/AgentPage.vue:32,37-88,120-125` |

## 5. 数据模型与落盘

### 5.1 文件:`~/.aliang/mcp.json`

```json
{
  "version": 1,
  "servers": {
    "blender": {
      "type": "stdio",
      "command": "/opt/homebrew/bin/uvx",
      "args": ["blender-mcp"],
      "env": {},
      "headers": {},
      "enabled": true
    },
    "weather": {
      "type": "http",
      "url": "https://api.example.com/mcp",
      "headers": { "Authorization": "Bearer xxx" },
      "enabled": true
    }
  }
}
```

- 路径:`<agentHome()>/.aliang/mcp.json`,与 `~/.aliang/config.json` 并列、独立文件。
- 写盘纪律完全复用 peer 注册先例:全量往返保留未知顶层键与未知 server 字段、`UseNumber` 保数字字面量、`SetEscapeHTML(false)`(json.Encoder 带尾换行)、tmp+rename 原子写、内容未变幂等跳写。
- `version` 语义:本设计只写 `1`;读取时**容忍未知更高版本**(不拒读、不迁移、不降级改写),未知版本仅 Warn——前向兼容,绝不做迁移行为。
- 文件不存在时 GET 返回空表(`{"version":1,"servers":{}}` 视图),首次写才创建。

### 5.2 字段校验(写通道强制,读通道容忍跳过)

| 字段 | 规则 |
|------|------|
| 名称(servers 键) | `^[a-zA-Z0-9_-]{1,64}$`;**禁止 `aliang-` 前缀**(避让 peer 托管名 `aliang-peer` 与本设计托管名 `aliang-mcp-*`);64 上限对齐 `sanitizeAgentCoreMCPSegment` 截断口径 |
| `type` | `stdio` \| `http` \| `sse`;缺省按 stdio 处理 |
| `command` | stdio 必填非空(http/sse 忽略) |
| `url` | http/sse 必填非空(stdio 忽略) |
| `args` | 字符串数组(可选) |
| `env` | `map[string]string`(可选) |
| `headers` | `map[string]string`(可选,仅 http/sse) |
| `enabled` | bool,缺省 true;false = 该条目在所有内核与清单中按禁用态呈现 |
| 条目总数 | ≤ 24(对齐 `agentCoreMCPServerCap`),超出整笔拒绝 |

读通道(启动/run 现场加载)对畸形条目容忍:跳过 + Warn,与现有遗留源行为同口径。`enabled:false` 条目在合并视图与集成清单中**均不采集**(与 agentcore 建连跳过同口径;清单不新增 disabled 呈现字段,YAGNI)。

## 6. 三内核消费链路

### 6.1 合并语义(唯一真值序)

```
~/.claude.json 顶层 mcpServers ─┐
已启用插件 .mcp.json ────────────┤ 合并,同名键后者胜
~/.aliang/mcp.json ──────────────┘
═══> 通用合并视图(供三内核消费)
```

### 6.2 agentcore(内置内核)

`agentCoreMCPRawServersIn` 追加第三源:claude.json + 插件(现状)之后合并注册表,注册表同名键覆盖;随后既有逻辑不动——headers 预筛跳过、全局开关置空、readOnly 剥离、会话级过滤、审批默认门、cap 截断。注册表 `enabled:false` 条目在采集层即跳过(同 `Disabled` 语义)。

### 6.3 claudecode(外部 CLI)

`claudeTierMCPArgs` 合并顺序改为:**claude.json user 域 → 插件 → 注册表 → 项目 `.mcp.json`**(项目域保持最后,项目条目对项目特定工具仍优先)。注册表 `enabled:false` 条目不并入。headers 字段原样透传(claude `--mcp-config` 原生支持;agentcore 侧照旧跳过并 Warn,见 §5.2 读通道容忍)。

**必须同步处理现有两条 nil 快速路径**,否则注册表会在最常见配置下被静默丢弃(评审第 1 轮实锤,`agent_ai_claude_policy.go:184-186,195-197`):

1. **不可信项目分支(原 `!projectMCPTrusted` 直接返 nil)**:改为**只要注册表贡献了任何条目就仍然发 flags**——payload = claude.json user 域 + 插件 + 注册表(不含项目 `.mcp.json`)。注册表是 user 域第一方资产,不受 `projectMCPTrusted` 门控;项目文件的信任闸语义不变。payload 重含 claude.json + 插件条目是必须的:strict 模式下 claude 原生会抑制插件 server(`agent_ai_claude_policy.go:325-329` 注释即此原因),漏掉会改变既有条目的可见性。
2. **零贡献分支(原「项目 `.mcp.json` 贡献零个名字 → 返 nil 走原生发现」)**:返 nil 条件从「项目文件零贡献」收紧为「**项目文件与注册表双双零贡献**」——注册表有任意条目时必发 flags(claude 原生发现看不见 `~/.aliang/mcp.json`,不发即静默丢)。两路径不发 flags 时行为与现状完全一致。

### 6.4 codex(外部 CLI)

注册表写 API 的第二段把每条启用条目同步为 `~/.codex/config.toml` 的托管块:

- 表头形态 `[mcp_servers.aliang-mcp-<name>]`(`aliang-` 前缀避让用户自己的 `[mcp_servers.*]`;`<name>` 即注册表键,TOML 裸键允许 `-`)。
- 复用 peer 的文本级块拼接纪律:表头行到下一 `[` 表头/文件尾整块替换或删除;文件其余内容(注释/空行/键序)原样保留,绝不全文件 toml 重编码;**非规范表头形态(引号/尾注释等价变体)拒写**并提示手动规范——防重复表定义打碎用户文件。
- 多块 diff 维护:同步 = 对 `aliang-mcp-*` 全集做增/改/删,使 config.toml 中的托管块集合 === 注册表启用条目集合;一次同步只触碰自己的块。
- **启动自愈**:挂在与 peer 注册自愈同点位(`app/agentruntime/server.go` 现有钩子旁),纠正「注册表已写但 codex 拼块当时被拒(用户表头不规范)后用户已手工修复」的漂移,以及用户手删托管块的恢复。自愈同样受非规范表头拒写保护,不盲拼。

## 7. API 与 UI

### 7.1 HTTP API(routes.go 新增,鉴权与现有 agent 端点同族)

| 路由 | 语义 |
|------|------|
| `GET /api/agent/mcp` | 返回注册表原文(管理编辑必须见原文;文件在用户自己机器上) |
| `PUT /api/agent/mcp/servers/{name}` | 新增或整体替换该条目(body = §5.1 条目对象);先全量校验,失败 4xx 带原因 |
| `DELETE /api/agent/mcp/servers/{name}` | 删除条目;不存在返回 404 |

写 API 内部两段式:第一段落盘 `mcp.json`(原子写);第二段同步 codex 托管块。第二段失败(如 codex 表头不规范拒写)时**第一段不回滚**——注册表已是真值,codex 侧由启动自愈在用户修复表头后补齐;响应中带 `codex_sync` 结果字段说明。路由注册遵循 routes.go 既有 method-prefix ServeMux 惯例与既有路径参数解析方式(勿用会双重注册 panic 的新形态,对照 `routes.go:254-259` 现状)。

**source 字段:扩展既有枚举,不是新增字段**(评审第 1 轮实锤——`source` 已存在于 `AgentIntegrationMCPServer`、前端 `sourceLabels()` 已渲染、且随设备上报进云端 `mcp_servers[]` 合同):

- 既有值 `user`(claude.json)/`plugin`/`config`(codex)一律**保留原名不改**——改名是云端合同 break change,超出本设计范围。
- 注册表条目新增枚举值 `aliang`,**由 claude 侧采集器打标**(注册表来源且非 `aliang-mcp-*` 前缀)——放 claude 侧而非 codex 侧,保证没装 codex 的机器清单行为一致;两采集器都读时 `appendUniqueString` 天然去重,无双标。注册表文件的 (size,mtime) stamp 必须并入该采集器的缓存 stamp 列表,否则 PUT/DELETE 后清单不刷新。
- 脱敏口径不变。
- **跨仓验证点**:新增枚举值 `aliang` 会随设备上报流向 PhoneServer;若其侧对 source 有严格 zod 枚举会整包拒(呼应「标签字段勿用严格枚举」教训),部署清单须核验 PhoneServer 侧 schema 对新值的容忍性,必要时同步扩枚举。

### 7.2 前端(设置页 → Agent → user-agent 子 tab)

集成清单所在子 tab 顶部新增「MCP 服务器管理」卡片(新组件 `AgentMCPRegistrySettings.vue`,挂点 `AgentPage.vue` user-agent 分支、`AgentIntegrationsSettings` 之上):

- 表格列:名称 / 类型 / 入口(command 或 url)/ enabled 开关 / 来源标签(来自集成清单既有 source 字段:`aliang`/`user`/`plugin`/`config`)。
- **管理操作只作用于 source 含 `aliang` 的行**(清单多源同名合并时 source 是逗号串,如 `aliang,user`——判断用「包含」而非全等,前端 `sourceLabels()` 已按逗号拆分);其余行在本卡片中只读展示(编辑/删除仅面向注册表条目,PUT/DELETE 只达注册表;遗留条目的启停走聊天页会话级开关,现状不变)。
- 新增/编辑:弹窗表单;`type` 切换联动 stdio 字段组(command/args/env)与 http 字段组(url/headers);客户端校验镜像 §5.2 服务端规则。
- 删除:确认弹窗,提示「该条目将从所有内核移除(codex 托管块同步删除)」。
- env/headers 用键值对行编辑器;文案走 i18n(沿用网站现有 i18n 机制,验收用 `strings grep` 键存在性口径)。
- 保存即调 PUT/DELETE,成功后刷新集成清单(现有多轮询/刷新机制沿用)。

### 7.3 生效语义(用户视角)

- 注册表改动**即时生效**,无需重启 agent:agentcore/claudecode 每 run 现场读合并视图;codex 由写 API 同步拼块。
- 聊天页「更多 › MCP」会话级开关按 server 原始键过滤——注册表条目自动出现在该列表,行为与遗留条目完全一致。
- 全局「能力接入 › MCP 全局开关」语义不变(仅作用 agentcore)。

## 8. 与既有体系的关系(不冲突声明)

| 既有设施 | 关系 |
|----------|------|
| peer MCP 五件套 + `customer.peer_mcp` 四键 | 完全独立两条通道:peer 是宿主 Go 桥 + `aliang-peer` 托管条目;本设计只动注册表文件与 `aliang-mcp-*` 托管块。名称前缀双重避让(注册表键禁 `aliang-`,托管块固定 `aliang-mcp-`) |
| MCP 全局开关 `customer.agentcore.mcp_enabled` | 语义不变,继续只 gate agentcore 建连 |
| readOnly run / 会话级 MCP 开关 / 审批默认门 | 语义不变,注册表条目与遗留条目同等对待 |
| `~/.claude.json` 写方(用户 CLI / peer 注册) | 全量往返互不踩踏:peer 注册只动 `aliang-peer` 键;本设计根本不写 claude.json |
| codex 用户自己的 `[mcp_servers.<name>]` | 不触碰。注册表条目以 `aliang-mcp-` 前缀并存;若用户手动配过同名服务,codex 侧出现两份 server(工具名按 server 命名空间隔离),一期文档说明即可 |
| codex 侧集成清单采集(`codexMCPServerInventoryIn` 读全部 `[mcp_servers.*]`,无前缀过滤) | **须加 `aliang-mcp-*` 前缀过滤**——否则每条注册表条目在清单中出现两次(`<name>` source=aliang 与 `aliang-mcp-<name>` source=config,评审第 1 轮实锤)。现存 `aliang-peer` codex 行保持现状不动(既有行为,不属本设计) |

## 9. 安全红线

1. env/headers 值只进 transport 构造,永不进日志、清单、journal(现有红线沿用)。
2. codex 拼块前非规范表头拒写,绝不重编码整个 toml。
3. 注册表键禁 `aliang-` 前缀 + 条目数上限 + 写通道原子写——防呆与防踩踏。
4. 写 API 只写注册表文件与 codex 托管块,不碰 claude.json、不碰 customer 配置。

## 10. 测试

- **注册表文件层**:往返保留未知键/未知字段/数字字面量;转义语义(`<>&` 不被改写);tmp+rename 原子写;幂等跳写;校验拒绝矩阵(坏名/缺 command/缺 url/超 cap/`aliang-` 前缀)。
- **合并语义**:三源合并同名键注册表胜出;`enabled:false` 三内核与清单一致跳过;claudeTierMCPArgs 四源顺序(项目最后、注册表在项目前);**两条 nil 快速路径的新条件**——不可信项目分支注册表非空仍发 flags(payload=claude.json+插件+注册表、不含项目文件)、零贡献分支收紧为「项目文件与注册表双双零贡献」才返 nil;`projectMCPTrusted=false` 时注册表仍并入、项目文件仍不并入。
- **清单与上报**:source 枚举扩展 `aliang` 且既有 `user/plugin/config` 原名不变;codex 采集侧 `aliang-mcp-*` 过滤后清单无重复行;设备上报 `mcp_servers[]` 含 `aliang` 值的结构兼容性用例。
- **codex 托管块**:多块增/改/删 diff;与用户自有块、peer `aliang-peer` 块共存;幂等跳写;非规范表头拒写(注册/注销两分支)。
- **agentcore 注入回归**:扩展现有 `agent_ai_agentcore_mcp_test.go`——注册表条目进入 raws、headers 跳过口径不变、会话级开关对注册表键生效。
- **API**:PUT/DELETE 校验、404、两段式 codex_sync 失败时第一段落盘语义。
- **前端**:vitest(表单联动/客户端校验/i18n 键)+ e2e(UI 新增 blender → 集成清单出现 `source=aliang` 条目)。
- 注意仓库既有基线:跨平台测试名避开 GOOS 尾段;`services` 包两枚预存 race flake 勿当回归。

## 11. 被否方案(备查)

1. **挂进 `~/.aliang/config.json` customer 键**:customer 深合并「patch nil=跳过」语义无法表达删条目,嵌套表残留旧键;表结构与三态开关混一个写通道。否。
2. **注册表独占(硬切换,不再读 claude.json)**:存量条目(含 `claude mcp add` 配的)静默消失 = break change。否。
3. **沿用 claude.json 只加写通道**:文件名义归属 Claude,没装 Claude 的机器概念别扭;CLI 与宿主双方共写同一文件有踩踏面。否。
4. **codex 不接**:「统一注册表」名不副实,codex 永远二等公民。否。

## 12. 回滚

删除 `~/.aliang/mcp.json` + 重启 agent(清掉 codex 托管块可由自愈反向执行或在 UI 删条目)→ 行为完全回到现状。代码层面各消费点均为「多一个合并源」,摘除即回滚。
