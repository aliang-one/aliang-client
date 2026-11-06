# 设计:peer MCP 按内核细分开关——总开关+三目标子键(aliangcode 工具面/claudecode 注册/codex 注册)

- 日期:2026-11-05
- 状态:设计定稿(brainstorming 产出,用户已批方案 A 与 §设计,"给我做完")
- 仓:alianggate(agent 后端 + website 前端,PhoneServer 零改动)
- 基线:master@eec587b
- 前序:2026-11-03 aliang-peer 平台托管 spec(§3 非目标明确「per-kernel 细分开关二期再拆」——本设计即二期)

## 1. 目标

1. `customer.peer_mcp` 从单布尔升级为「总开关+三目标子键」:agentcore 工具面注入、claudecode 自动注册、codex 自动注册各自可独立开/关。
2. PeerMCPSettings 卡整卡迁入「Agent › aliangcode」子 tab(与 MCP 全局开关/技能全局开关同族的「内核能力开关」区),「状态 › 其他」不再有该卡。
3. 旧数据语义无损兼容:`{enable:false}` → 三面全关;`{enable:true}`/缺省 → 三面全开。

## 2. 非目标

- PhoneServer 侧改动(customer 配置为透传存储,服务端零 peer_mcp 引用,已核实)。
- 跨设备执行(peer_exec_enabled)/文件传输(peer_fetch_enabled)设备门禁开关的写通道(另一条线,置灰占位不变)。
- 工具描述触发词强化与系统提示层设备舰队感知(上轮讨论的 a/b 补法,独立小项,不在本设计)。
- MCP 全局开关(kernel 连接 MCP server 的客户端开关)——已有且已在 aliangcode 子 tab,语义不同不混。

## 3. 数据模型(processor/config/types.go)

```go
// 三态指针语义与 Enable 同族:nil=跟随总开关,true/false=显式。
type PeerMCPConfig struct {
    Enable           *bool `json:"enable,omitempty"`            // 总开关(现有键),关=三面全关
    AgentcoreEnabled *bool `json:"agentcore_enabled,omitempty"` // agentcore 工具面注入
    ClaudeEnabled    *bool `json:"claude_enabled,omitempty"`    // claudecode 自动注册
    CodexEnabled     *bool `json:"codex_enabled,omitempty"`     // codex 自动注册
}
```

命名与 agentcore 调优块 `mcp_enabled/skills_enabled` 的 `*_enabled` 族对齐。三个有效读点(同族同式,均含总开关前置):

```
EffectivePeerMCPAgentCoreEnabled() = master && (子键 nil→true)
EffectivePeerMCPClaudeEnabled()    = master && (子键 nil→true)
EffectivePeerMCPCodexEnabled()     = master && (子键 nil→true)
```

master=`EffectivePeerMCPEnabled()`(现有函数保留不动)。config 包单测矩阵:master(nil/false/true)× 子键(nil/false/true) 对三读点断言。

## 4. 门控与端点(app/http/services + handlers)

| 读点 | 现状 | 改为 |
|---|---|---|
| agentcore 注入(`agentCorePeerTools`,agent_ai_agentcore.go 调用) | `EffectivePeerMCPEnabled()` | `EffectivePeerMCPAgentCoreEnabled()` |
| 启动自愈(`EnsurePeerMCPMatchesConfig`,agentruntime/server.go 启动调) | 单 register 布尔对齐两内核 | 逐内核:claude 按 `EffectivePeerMCPClaudeEnabled()`、codex 按 `EffectivePeerMCPCodexEnabled()` |
| apply(`SetPeerMCPRegistration(enable bool)`,POST /api/agent/peer/mcp) | `{enable}` 布尔,两内核一起注/销 | 不再收布尔,按当前配置逐内核应用(仪表盘两段式不变:先 PATCH customer 再 POST apply) |
| 状态回显(`PeerMCPStatus`,GET) | `enabled + registration[]` | `enabled + targets[]{kernel,expected,state,error}`(expected=该内核有效值;expected≠state 即漂移,UI 如实呈现) |

实现要点:
- `ApplyPeerMCPRegistration(home, register bool)` 重构出单内核原语 `applyPeerMCPKernel(home, kernel string, register bool) PeerMCPApplyResult`;双内核旧签名保留为包装(现有测试与调用方零改动)。
- `PeerMCPApplyResult` 增加 `expected` 字段回显;`registration` JSON 键名改为 `targets` 的同时**保留 `registration` 旧键一次**(前端与契约测试同步切换后,旧键下轮删——或者直接一次性切换,本仓前后端同发,无跨端兼容负担;定一次性切换)。
- 失败语义不变:单内核失败不影响另一内核,Error 逐内核透出,只 Warn 不阻断。

## 5. 前端(app/website)

**基线变化(2026-11-05 执行前发现)**:master@04563ae 已由另一条线把 PeerMCPSettings 卡收进 AgentPage 新增的第四子 tab「设备互访」(`peer-access`,经 `agent/PeerAccessSettings.vue` 包装,门禁置灰占位同卡),子 tab 词表已扩为 4 项,子 tab 样式改 navitem 下划线。本设计的「整卡迁入 aliangcode 子 tab」**作废**——卡片现位(设备互访子 tab)即为终态,本设计只升级卡内开关粒度:

- PeerMCPSettings 卡升级:总开关行(现有)下方新增三行细分开关(agentcore 工具面/注册到 claudecode/注册到 codex),每行右侧带该内核注册状态徽章;expected≠state 时徽章加「漂移」提示;apply 诊断行、保存按钮、两段式流程不变(PATCH customer → POST apply,apply 不再带布尔载荷)。
- `normalizePeerMCPConfig` 扩展:仅显式布尔才产出对应键(三态契约原样保留——缺键/非布尔不进 patch,不清用户配置)。
- i18n:三细分开关标题/描述、漂移提示,zh/en 同步。

## 6. 错误处理/边界

- codex/claude 未安装或家目录不可得:apply 逐内核报 Error、注册状态如实 missing(现有语义,不改)。
- 子键显式 false + 总开关 true:该内核关;总开关 false:三内核全关(子键值被压制,回显 expected=false)。
- runtime 与 core 两态短暂分歧:沿用「状态回显如实呈现 + 启动自愈收敛」既有约定。
- readOnly run:peer 工具剥离判据不变(本设计不触碰)。

## 7. 测试

- config 包:三读点 × master/子键 6 组矩阵(含缺省全开、master false 压制子键 true)。
- services 包:自愈逐内核矩阵(master false+子键 true → 两内核都注销;master true+claude false+codex nil → claude 注销、codex 保持注册)、apply 逐内核 best-effort、状态回显 targets 形状与 expected。
- handlers:POST 不带 enable 的 apply 语义。
- 前端 vitest:normalize 三态契约扩展、AgentPage 含 PeerMCPSettings 的渲染断言(无 VTU,以词表/存在性断言为准)。
- 全量:vitest + mobile-responsive e2e 哨兵 + go build/test 相关包。

## 8. 交付物与验收

1. 三个目标开关在「Agent › 设备互访」子 tab 卡内可独立点击生效;总开关一键全关。
2. 旧配置数据升级后行为不变(全开者仍全开,显式关者仍关)。
3. 状态回显含 per-target expected/state,漂移可见。
4. 后端:agentcore 注入按 `EffectivePeerMCPAgentCoreEnabled()` 门控;自愈逐内核对齐。
