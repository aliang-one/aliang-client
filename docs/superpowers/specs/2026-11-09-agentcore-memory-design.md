# agentcore 记忆层设计:写侧每回合提取 + 读侧全量注入(项目级,宿主侧)

- 日期:2026-11-09
- 状态:设计对话定稿(六决策已拍板),待评审
- 上游出处:agno 设计目录 Top1([[agno-design-catalog-merged]]),docs/superpowers/specs/ 2026-11-09-agentcore-mcp-resilience-design.md 为同族先例(MCP 健壮性包,已合 1f931f7)

## 0. 背景与动机

agentcore(AliangCode)是唯一全盲内核:claude CLI 自带 auto-memory、codex 读 AGENTS.md,内核自身 `app/agentcore/` 零 memory 代码——跨会话什么都记不住,每个新会话从零开始重新踩用户上次纠正过的坑(提交信息格式、项目约定、环境特性)。

设计目录(agno 调研)已核验:写侧=提取迷你循环+既有事实内联去重+delete/clear 默认禁;读侧=全量注入,零向量基建。本 spec 把它落成本仓实现。

## 1. 目标与非目标

**目标**
1. agentcore 每个 run 正常收尾后,宿主异步提取本回合长期事实并落盘(项目级);
2. 后续 run(含子 agent)的 system prompt 自动携带既有记忆,注入即计命中;
3. 容量封顶 100 条 + LRU 淘汰,零维护;
4. 管理面三层:落盘文件 + 本地 CRUD API + 仪表盘记忆卡片(含总开关);
5. 内核对记忆只读——写入完全由宿主提取管线控制,模型不能删改自己的记忆。

**非目标(v1)**
- 全局级(跨项目)记忆——条目不带 scope,后续批次再议;
- 向量检索/embedding(全量注入够用);
- LLM 合并压缩(满员走 LRU,不做语义合并);
- 提取模型独立可配(`customer.agentcore.memory_model` 留 v2 口子,本版不实现);
- claude/codex 内核的记忆(各自原生已具备);
- 跨设备记忆同步(PhoneServer 不感知记忆文件)。

## 2. 现状关键事实(实施前提,均已核实)

| 事实 | 位置 |
|---|---|
| effort 拒绝判定(4xx 错误体+2xx 错误信封两形态,`mentionsEffort` 点名 effort/output_config) | app/agentcore/loop.go:940-956(`isEffortRejection`,包内未导出——需加导出包装) |
| run 自身 effort 档位来源 | agent_ai_agentcore.go:1480(`Effort: run.effort`) |
| 内核 effort 拒绝锁存先例(`effortLatched`,拒绝一次后续轮次不再下发) | app/agentcore/loop.go:926-936 |
| 注入面唯一入口,`<env>`/`<session-context>` 块先例在此 | app/http/services/agent_ai_agentcore.go:422(`agentCoreSystemPromptWithContext`) |
| 内核 Run 调用点,messages 快照在作用域内(最后一条 user=本回合用户消息) | agent_ai_agentcore.go:1499 |
| run 正常成功收尾点(`endStatus = "success"`,side-effects-allowed 分支) | agent_ai_agentcore.go:~1575 |
| 僵尸迟返回分支(side effects 全弃,不得触发提取) | agent_ai_agentcore.go:~1592-1609 |
| effort 下发机制(`output_config.effort`,NormalizeEffortLevel,网关契约已验证) | app/agentcore/loop.go:220-257;app/agentcore/types.go:47-49(OutputConfig) |
| 内核 client 只有流式通道;小 LLM 调用+聚合收流先例=Compact | app/agentcore/client.go:203(Stream);app/agentcore/compact.go:185 |
| 模型解析 `agentCoreModel(run.model)`,synthetic/空挡下 | agent_ai_agentcore.go:258-274 |
| LLM 端点解析(平台 relay 56432 兜底 / customer.agentcore_llm 自定义) | processor/config/types.go:641(`EffectiveAgentCoreLLM`) |
| 三态指针开关先例(nil=默认开) MCPEnabled/SkillsEnabled | processor/config/types.go:694-701 |
| journal「尽力而为留痕,写失败只 Warn 不炸 run」纪律 | app/http/services/agent_ai_agentcore_journal.go:8-9 |
| journal 文件名清洗先例(白名单字符+截断) | agent_ai_agentcore_journal.go:53+(`agentCoreJournalFileName`) |
| 子 agent 调 `agentCoreSystemPrompt`(继承基础 prompt,不带会话上下文) | agent_ai_agentcore.go:420-422 注释 |
| API 路由必须双注册(routes.go + registerAgentRoutes),漏一边 404 | 既有教训(Agent 双路由表) |
| customer 配置加字段需 grep 旧字段名同步全部副本(allowlist 族) | 既有教训(customer-config-allowlist-five-places) |
| 目录依赖必须可注入(测试用 t.TempDir) | journal 先例 `newAgentCoreJournal(dir)` |

## 3. 设计总览(方案 A:宿主侧独立模块)

```
app/http/services/
  agent_ai_agentcore_memory.go       # 存储层+提取管线+注入(新,主战场)
  agent_ai_agentcore.go              # 两处接线:system prompt 注入 + run 收尾钩子
  agent_ai_agentcore_memory_test.go  # 测试
  (API handler 落既有 handlers 组织,routes 双注册)
app/website/src/...                  # 仪表盘记忆卡片(设置页 Agent 标签)
app/agentcore/                       # 仅一处触点:IsEffortRejection 导出包装(一行);
                                     # 除此之外零改动(内核纯执行,记忆是宿主关注点)
```

六项已拍板决策:①作用域=仅项目级;②写入时机=每回合 run 正常收尾后异步小提取;③提取模型=同端点同模型+`output_config.effort="low"`;④容量=100 条封顶+LRU 淘汰;⑤管理面=文件+API+仪表盘卡片;⑥工具面=只读宿主写(内核零工具)。

## 4. 存储模型

- **路径**:`<agentHome()>/.aliang/memory/<project-slug>.json`。slug 由 projectPath 全段清洗(`/` 等非白名单字符→`_`,沿 `agentCoreJournalFileName` 先例),超长截断,空 projectPath 的 run 不做记忆(读写全跳过)。**`agentHome()` 不可得 → 存储根目录为空,沿 journal 同款退化:提取/写入/touch/读取全部 no-op**(任何调用方零错误零阻塞)。
- **文件结构**:

```json
{
  "version": 1,
  "project": "/原始/项目路径",
  "next_id": 8,
  "updated_at": "2026-11-09T12:00:00Z",
  "memories": [
    {"id": 7, "content": "提交信息标题用「新增:/修复:」全中文", "created_at": "...", "last_hit_at": "...", "source": "extract"}
  ]
}
```

- 条目字段:`id`(int,next_id 单调递增)、`content`(string)、`created_at`/`last_hit_at`(RFC3339)、`source`(`extract`/`manual`)。手改文件兜底:读入时对每条 TrimSpace、丢空 content、id 非法/重复的条目丢弃并 Warn(沿「读侧兜住手改文件」先例)。
- **容量**:`agentCoreMemoryCap = 100`(常量)。写入导致超限时,按 `last_hit_at` 最旧淘汰(空值回退 `created_at`),每条淘汰 Warn 一行(含 id 与 content 前 40 字)。
- **并发**:进程内单例 store + **跨项目单把全局 mutex**(写频=每回合一次,单锁可接受,不做分文件锁)。损坏文件(非法 JSON)→ **新决策**:`<slug>.json` 原样改名 `<slug>.json.bak` 保留现场,按空库重建,Warn 带损坏摘要前 200 字——不静默覆盖、不丢排查证据(journal 无此先例,此为本设计新增;journal 对坏文件的手段是「补头原子重写保留内容」,形态不同不引据)。

## 5. 写侧管线

- **触发**:run 正常成功收尾点(`endStatus = "success"` 的 side-effects-allowed 分支,agent_ai_agentcore.go:~1575)之后,起 **detached goroutine**。中断/stopped/失败/僵尸迟返回**不提取**——被丢弃的输出没有记忆价值,失败的回合提取只会记进噪音。
- **提取输入**:本回合最后一条 user 消息文本 + 最终助手全文(`assistantOutput`),各截断 4000 字符;**工具转录不进提取输入**(控 token 兼隐私)。
- **提取调用**:`agentcore.NewClient` 端点**复用 `agentCoreBaseURL()`(:216-231) + `agentCoreAuthToken()`(:131)**(含 EffectiveAgentCoreLLM → 回环 env ANTHROPIC_BASE_URL → 56432 兜底全链)——禁止只走 `EffectiveAgentCoreLLM` 单层,否则配置了回环 env 的机器上提取与主 run 分叉。请求 `MessagesRequest{Model: agentCoreModel(run), MaxTokens: 4096}`(显式小 MaxTokens;1024 装不下自己的 ops 包络——单条 500 字符条目≈250-350 token,2-3 条修正 ops 即触顶截断,恰是最有价值的「踩坑修正回合」先死;不设则走 defaultMaxTokens=64000,输出无界)+ effort 档见回退链;流式发起、聚合收流(沿 Compact 先例),取纯文本结果;**收流后核对 `stop_reason`:`max_tokens` 截断=丢弃本次+Warn「提取输出被截断」**(沿 compact.go:324「拒绝用半截摘要」守卫,半截 JSON 必解析失败,与其喂给解析器炸出难懂错误,不如就地定性)。model 不可用(synthetic/空)→ 整次提取跳过。
- **effort 容错回退链**(用户拍板:模型不认 low 时回退当前对话档):
  1. 首选 `Effort: "low"`;
  2. 上游拒绝 → **回退 `agentcore.NormalizeEffortLevel(run.effort)`**(归一后的当前对话档——主 run 实发就是这个归一值,该模型必认;归一后为空**或与已试档相同**则直接跳到 3),重试一次;
  3. 再次拒绝 → **不带 effort**(纯同模型)重试一次;仍失败 → 沿既有失败纪律(Warn+放弃)。
  - 档位比较一律在**归一后**进行(`ultracode`→`max`、`minimal/none`→`low`,原始值直发会与主 run 实发档错位);
  - 拒绝判定**复用内核同一实现**:agentcore 导出 `IsEffortRejection(err)`(包内 `isEffortRejection` 加导出包装,一行改动;services 侧禁止复制判定逻辑——跨包安全决策单点化,防漂移);
  - **进程级锁存**(沿内核 `effortLatched` 思路):首次降级即锁存最终可用档,后续回合直接用降级档发请求,不再每回合白吃一次 400 往返;每次降级 Warn 一行(含降级到哪档)。
- **既有条目对账预算**:提取 prompt 携带的既有条目清单按 `last_hit_at` 降序取**前 20 条**(常量 `agentCoreMemoryDedupListCap = 20`;20×500 字符中文≈3-5千 token 级,预算可控)——满库(100 条×500 字符=5 万字符)全量对账每回合上万 token 不可接受。预算外条目不参与对账(只能被 add,残留重复由 LRU 自然收敛)。
- **提取 prompt 规则**(system 角色):只提取「项目约定/用户偏好/环境事实/踩坑教训」四类值得跨会话记住的长期事实;**禁止**密钥、token、密码、内网地址等敏感值入记忆(宁漏勿泄);输出严格 JSON:`{"ops":[{"op":"add","content":"..."},{"op":"update","id":7,"content":"..."}]}`,无值得记的输出 `{"ops":[]}`。
- **ops 应用**(宿主,锁内):update → 按 id 找到改 content+刷新 `last_hit_at`;add → 追加;然后统一走容量封顶。**单条 content 超 500 字符的 ops(经 trim 后)丢弃该条 + Warn,其余 ops 照常应用**——截断会产生误导性残句,整批放弃太粗暴,丢弃是最小损失面。**宿主不做文本相似度去重**——既有条目(id+content,预算内前 20 条)随 prompt 给提取 LLM 对账,重复由 LLM 转成 update。
- **失败纪律**:LLM 报错/JSON 解析失败/落盘失败 → 各 Warn 一行,提取整体放弃,零影响(run 早已结束)。解析失败时 Warn 带原始返回的前 200 字,便于复盘 prompt 质量。
- **空守卫**:user 文本与助手输出皆空(纯工具回合无正文)→ 跳过提取。

## 6. 读侧注入

- 落点:`agentCoreSystemPromptWithContext` 在 `<env>` 块之后追加 `<memory>` 块。主 run 与子 agent 一视同仁(同一函数;项目级事实对子 agent 同样有用,prompt 代价小)。
- 块格式:

```
<memory>
项目长期记忆(此前会话沉淀的事实,供参考;与当前对话冲突时以当前对话为准):
- [7] 提交信息标题用「新增:/修复:」全中文
- [3] ...
</memory>
```

- **全量注入**,按 `last_hit_at` 降序(无值回退 `created_at`);字符预算 6000,超限从尾部截断(保最新的);每条一行 `[id] content`。
- **注入即命中**:被注入条目的 `last_hit_at` 更新——**异步 best-effort**(goroutine),prompt 构建路径零阻塞、零新增失败面;store 锁内合并写,**逐 id 校验条目仍存在才更新**(防与 API DELETE 竞态把已删条目复活)。淘汰序失真评估:touch 在回合初毫秒级落盘,淘汰在回合末提取时才发生,touch 必先抵达,同回合多条 touch 仅毫秒级平局,LRU 序不受影响——异步化安全。
- 空记忆/开关关/`agentHome()` 不可得 → 不出块,prompt 与今天逐字节一致(现有 prompt 测试不受扰)。
- 目录依赖可注入:store 构造走 `newAgentCoreMemoryStore(dirFunc)` 形态(沿 journal 先例),单测 `t.TempDir`。

## 7. 开关与配置

- `customer.agentcore`(`AgentCoreTuningConfig`)新增:

```go
// MemoryEnabled 是记忆层总开关(提取+注入)。三态指针沿 MCPEnabled/SkillsEnabled
// 先例:nil=未配置(默认开启)、true/false 显式。只作用于 agentcore 内核。
MemoryEnabled *bool `json:"memory_enabled,omitempty"`
```

- `EffectiveAgentCoreMemoryEnabled()`(nil=true,沿同文件先例);关 = 不提取、不注入、API 仍可读(手动管理既有记忆不被开关锁死)。
- ⚠️ 实施警告:customer 配置加字段要 grep 旧字段名(如 `skills_enabled`)定位全部结构体副本与前端表单逐字同步(allowlist 族老坑);API 路由记得 routes.go + registerAgentRoutes **双注册**。

## 8. API 面

沿 MCP 注册表 handler 先例;鉴权沿既有本地管理面。`project` 参数=原始 projectPath(服务端做 slug 映射,客户端不接触 slug):

| 方法 | 路径 | 语义 |
|---|---|---|
| GET | `/api/agent/memory?project=<path>` | 列表(全量,按 last_hit_at 降序) |
| POST | `/api/agent/memory` | 手动增 `{project, content}`(source=manual);超限走同一 LRU |
| PUT | `/api/agent/memory/<id>` | 改 content `{project, content}` |
| DELETE | `/api/agent/memory/<id>?project=<path>` | 删单条 |
| DELETE | `/api/agent/memory?project=<path>` | 清空该项目 |

错误文案口径:项目缺失参数=`project 必填`;id 不存在=`记忆条目不存在: <id>`;content 超长(>500 字符)=`单条记忆超上限 500 字符`(写入侧 trim,不静默截断)。content 上限 500 字符作为**写通道 fail-closed**(沿 MCP 名单 64 条先例),读通道不裁剪;**API 二通道(POST/PUT)超限一律 400 拒绝**,提取 ops 通道超限=丢弃该条(§5,两通道口径不同是拍板过的:API 是用户显式动作给明确错误,提取是后台管道取最小损失)。PUT 改 content:**保留原 `source`、不刷新 `last_hit_at`**——API 侧命中语义只归注入;提取 update 刷新 `last_hit_at`(§5)属「被纠正条目更新保活」,两通道口径不同均为有意,勿互相"纠正"。

## 9. 仪表盘卡片

- 位置:设置页 Agent 标签(沿 MCP/技能分页先例),新「记忆」卡片。
- 能力:项目选择(沿既有项目下拉)→ 条目列表(content/时间/来源徽标)→ 增改删;`memory_enabled` 总开关(三态,默认开,与 MCP 开关同款 UI)。
- i18n:文案沿既定口径(全角标点/「」/「你」/token 小写)。
- 前端测试:vitest 覆盖卡片渲染 + 开关三态 + 删除确认。

## 10. 错误处理与文案(汇总)

| 场景 | 行为 |
|---|---|
| 提取 LLM 失败/解析失败 | Warn(解析失败带原始返回前 200 字),放弃本次,无用户可见影响 |
| 落盘失败 | Warn,记忆丢本次不丢历史 |
| 存储文件损坏 | Warn + `.bak` 备份后按空库重建 |
| 开关关闭 | 零提取零注入;API 可读可管 |
| 敏感内容 | 提取 prompt 层禁止(宁漏勿泄),宿主不做二次正则过滤(v1 口径) |

## 11. 测试计划

存储:JSON 往返 / 手改文件兜底(空串/坏 id 丢弃) / cap 边界(100 恰好放行、101 触发 LRU 淘汰且淘汰序正确) / 损坏文件 `.bak` 重建 / `agentHome()` 空=全路径 no-op / 并发写;提取:fake client(注入 seam,沿 `coreMCPDialForTest` 先例)出 ops → add/update 应用正确 / **单条 ops 超 500 字符丢弃该条其余照常** / LLM 失败仅 Warn / 非法 JSON 仅 Warn / 空回合跳过 / stopped-不提取;**effort 回退链:low 被拒→降归一后 run.effort 重试成功 / run.effort 空直接跳无 effort / run.effort 归一后与 low 相同→直接跳无 effort / 二次被拒→无 effort / 无 effort 终试仍失败→Warn 放弃 / 进程锁存后后续回合不再试 low**;注入:`<memory>` 块出现与排序 / 6000 预算截断 / 空记忆零块 / 子 agent prompt 含块 / 注入后 last_hit_at 已刷新 / **touch 与 DELETE 竞态不复活已删条目**;开关:nil=true / false=零提取零注入;API:CRUD 全链 + 双注册冒烟(GET 走两条路由形态)+ **content>500 拒绝 / project 必填**。

## 12. 实施影响面

| 文件 | 改动 |
|---|---|
| app/http/services/agent_ai_agentcore_memory.go | 新:store+提取管线+注入(主战场) |
| app/http/services/agent_ai_agentcore.go | 两处接线:prompt 注入 + run 收尾钩子 |
| app/http/services/agent_ai_agentcore_memory_test.go | 新:测试 |
| app/agentcore/loop.go | 唯一内核触点:`IsEffortRejection` 导出包装(一行;services 禁止复制判定) |
| processor/config/types.go | `MemoryEnabled` 字段 + `EffectiveAgentCoreMemoryEnabled` + 副本同步 |
| handlers + routes(沿既有组织) | CRUD API + 双注册 |
| app/website/src/...(设置页 Agent 标签) | 记忆卡片 + i18n |
| 既有 prompt 相关测试 | 空记忆断言不破(零块语义保护) |

## 13. 借鉴对照(agno → 本仓)

| agno 机制 | 本仓落地 |
|---|---|
| memory 写侧:回合后 LLM 提取 + 既有事实对账去重 | 同构:提取 ops(add/update) + 既有条目进 prompt 对账,宿主不做相似度 |
| memory 读侧:注入 system prompt | 同构:全量注入 `<memory>` 块,零向量基建 |
| AgentOS 记忆管理 UI | 仪表盘记忆卡片(本仓一次做到位) |
| `memory.delete/clear` 工具 | **不给**——只读宿主写,零失控面(目录既定决策) |
| 用户级 memory scope | v1 只做项目级(非目标列明) |
