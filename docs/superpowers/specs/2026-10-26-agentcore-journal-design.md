# AliangCode（agentcore）调用记录持久化 — 设计文档

日期：2026-10-26
状态：已评审通过（设计阶段）
范围：仅 agent 客户端（alianggate），零 PhoneServer / 前端改动

## 1. 背景与问题

「AliangCode」是手机端 provider `agentcore`（平台内置内核）的显示名（2026-10-26 由
"Aliang Agent" 改名，见 AliangVibeCodingPhone `src/utils/modelIntensity.ts:457`）。
agent 侧对应 `normalizeAgentAIProvider` 的 `agentcore` 分支
（`app/http/services/agent_execution_guard.go:482`）与进程内内核 `runAgentCore`
（`app/http/services/agent_ai_agentcore.go:407`）。

现状：agentcore 调用**只在运行期间**出现在 agent 页面的 AI 会话列表里
（`activeVibeSessionsSnapshot` 活跃覆盖层），回合结束后彻底消失：

1. `clearRunning` 把 `session.cancel = nil`（`app/http/services/agent_ai.go:3586`）→
   活跃快照不再包含；
2. agentcore 是进程内内核（`app/agentcore` loop 直连 Messages API），不 spawn
   claude/codex CLI，**不写任何本地转录文件**；
3. 历史列表唯一数据源是本地文件扫描（claude 扫 `~/.claude/projects`、codex 扫
   `~/.codex`，`collectAgentVibeSessions`，`app/http/services/agent_inventory.go:465`）；
   转录回放 `readAgentVibeSessionDetailFile` 同样按文件分发
   （`app/http/services/agent_detail.go:282`）。

结果：AliangCode 会话结束后在仪表盘不留任何调用记录，详情也不可回放。

关键约束（决定了「边跑边落盘」而不是「跑完导出」）：
`clearRunning` 收尾时会把内存 history 裁成热窗（`trimAgentAIHistory`，16 条 / 64000
字符（`maxChars = 64000`），`agent_ai.go:7398`）——早期消息在 run 结束时已从内存消失，
事后导出拿不全。

## 2. 目标与非目标

**目标**

- agentcore 会话回合结束后永久留在 agent 页面 AI 会话历史列表，provider 显示为
  `agentcore`（手机端 providerLabel 自动呈现 "AliangCode"，无需服务端改动）；
- 点开可回放对话内容（user/assistant 文本回合），agent 进程重启后记录仍在；
- 运行中不出双卡（与活跃覆盖层同 ID 合并）。

**非目标（明确不做）**

- 不做「agent 重启后继续对话」（resume 仍走内存热窗语义）；
- 不记录工具调用明细 / thinking（回放初版只有对话文本回合；可后续扩展）；
- assistant 行只在回合**成功**出口写：IsError/runErr/stopped 出口上的部分输出
  随热窗裁剪一起丢失，这是有意接受的行为（与「尽力而为留痕」一致），不要在
  实现时顺手「修」它；
- 不动 PhoneServer、不动前端、不动 claude/codex 通道任何行为。

## 3. 方案取舍

| 方案 | 结论 |
|---|---|
| **A. 自有转录目录 + 自有轻量格式**（本设计） | ✅ 与真实 Claude Code 数据完全隔离；reader 纯函数可测；与 codex 通道同构 |
| B. 写 claude 兼容行到自有目录，复用 claude reader | 否：与 claude reader 内部约定强耦合，需伪造 uuid/parentUuid/cwd 等大量字段，reader 一改就悄悄坏 |
| C. PhoneServer 服务端持久化 | 否：跨仓改动，与「agent 本地扫描上报」架构相悖，离线不可见 |
| （参考反例）写伪 claude 文件进 `~/.claude/projects` | 绝不：污染真实 CC 数据，触发幽灵校验/改名/30 天清理的未定义交互 |

## 4. 数据流总览

```
runAgentCore（写入侧）                    扫描侧（既有架构加第三路）
run 开始 ──→ 确保头行 ┐
用户消息 ──→ user 行  ├→ ~/.aliang/ai-sessions/<sessionID>.jsonl
run 成功 ──→ assistant 行     │        ↓ (size,mtime) 缓存扫描
run 终态 ──→ end 行   ┘   collectAgentCoreVibeSessions
                                   ↓
                          collectAgentVibeSessions 三路合并
                                   ↓
                     GET /api/agent/sessions + 详情回放(.aliang 分支)
```

存储根：`<agentHome()>/.aliang/ai-sessions/`。`agentHome()`
（`app/http/services/agent_home.go:17`）与 claude/codex 扫描同一根，天然保证
「写入进程」与「扫描进程」看到同一目录（user-agent 运行时即用户家目录）。

## 5. 落盘格式

每会话一个文件：`ai-sessions/<sessionID>.jsonl`（`sessionID` = agent 会话 ID，
即 `agentAISession.id`）。行式 JSON，UTF-8，`\n` 分隔：

```jsonl
{"kind":"session","session_id":"…","project_path":"…","model":"…","created_at":"RFC3339"}
{"kind":"msg","role":"user","message_id":"…","text":"…","ts":"RFC3339"}
{"kind":"msg","role":"assistant","message_id":"…","text":"…","ts":"RFC3339"}
{"kind":"end","status":"success|stopped|error","error":"…","ts":"RFC3339"}
```

规则：

- **头行只在文件不存在时写**（`ensureHeader`）；后续回合直接追加，头行不重写。
  若 projectPath/model 变化（同 ID 换项目），以先写下的头为准、不改旧行——列表
  字段以头行为准，详见 §8 注。
- `message_id` 用于 journal 内存去重（每会话一个小容量已写集合，防同一条消息
  因重入/重试写两行；进程重启后集合清零，重复场景本就不存在——重发即新
  messageID）。
- assistant 行 = 该轮**完整** assistant 文本（与手机端「一回合一条助手消息」渲染
  口径一致；多工具回合的中间文本已由上游在 output 累计器里拼接）。
- user 行内容取自 `session.history` 里的**真实用户 content**，不取 `run.prompt`
  （run.prompt 是拼接产物 ≠ 原始消息，2026-10-20 已有踩坑记录）。
- 半行容忍：崩溃残留的残行 JSON 解析失败即跳过该行，不影响其余行。
- 会话 ID 做文件名安全化（只允许 `[A-Za-z0-9._-]`，其余转 `_`），防路径注入。

## 6. 写入挂点（provider 精确判别，不碰 claude/codex 路径）

新文件 `app/http/services/agent_ai_agentcore_journal.go`，实现带 mutex 的
`agentCoreJournal`：`ensureHeader / appendMsg(role, text) / appendEnd(status, errMsg)`，
全部 `os.OpenFile(O_APPEND|O_CREATE|O_WRONLY)` 逐行追加。**写失败只 Warn，
绝不影响 run**——留痕是尽力而为。

| # | 时机 | 挂点 | 写入 |
|---|---|---|---|
| 1 | run 开始 | `runAgentCore` 入口（`agent_ai_agentcore.go:407` 之后、`agentCoreEnsureUpstream` 之前） | 确保头行 + user 行 |
| 2 | 运行中插话 | steer 入口 `coreControl.deliver` 成功分支（`agent_ai.go:2156` 附近，core 分支已知） | user 行 |
| 3 | 回合成功 | `m.appendAssistantHistory(...)` 之后（`agent_ai_agentcore.go:597`） | assistant 行 |
| 4 | run 终态 | `runAgentCore` 四个出口（stopped / IsError / runErr / 成功）统一收敛为单个 defer | end 行 |

注：`runAgentCore` 还有一个早期 `bindingErr` 返回（`agentcore.go:557`）——defer 收敛
天然覆盖它，同样会落 end 行（status=error），无需单独处理。

**为什么 user 行收进 `runAgentCore` 入口、而不是 ai.send 的 user-history append 处**：
挂点处 provider 往往还是 `"auto"`，agentcore 判定（含 `agentAIUseAgentCoreFallback`
的 fallback 路径，`agent_ai.go:3829`：auto + 外部 CLI 不可用）发生在 run 派发时
（`agent_ai.go:3736` 附近才落 `run.provider = "agentcore"`）。在派发前写会**系统性
漏掉 fallback 用户的首条 user 行**——那恰是内置内核的第一类用户。入口处
`session.history` 最后一条 user 就是本轮触发消息，真实 content 直接可得。

只挂 agentcore 路径：ai.send 入口**不需要**任何改动；steer 的
`default: coreControl.deliver` 分支本身就是 agentcore 专属。

边界行为（有意为之）：挂点 1 读 user 行**不做** `runSeq` 守卫（不镜像
`agentCoreMessages` 的 `session.runSeq == run.runSeq` 检查）——stale run 竞争下
多写一行 user 属可容忍的尽力而为损失，守卫反而引入「该写没写」的新失败模式。

## 7. 扫描接入（与 codex 通道同构）

新文件 `app/http/services/agent_inventory_agentcore.go`：

- `collectAgentCoreVibeSessions(scanDirs []string) []models.AgentVibeSession`
  - 扫 `<agentHome()>/.aliang/ai-sessions/*.jsonl`，复用现成 recency-cap walker
    `findRecentAgentFilesWithStats`（`agent_inventory.go:952` 同款）与
    `(size, mtime)` 解析缓存 `agentVibeParseCache`（key 含 path，天然区分三个根）；
  - 每文件产 `AgentVibeSession`：`ID` = 裸 sessionID（见 §8）、`Provider=Tool="agentcore"`、
    `Status="closed"`、`Mode="vibe"`、`ProjectPath` 过 `cleanAgentProjectPath` +
    `isSafeAgentProjectPath` + scanDirs 早过滤（同 claude 通道 `agent_inventory.go:916`
    的口径，连 session 行都不读）、`Title` = 首条 user 文本过 `isJunkAgentTitle` 截
    200、`MessageCount`/`DisplayMessageCount`、`Model`、`CreatedAt`（头行）/`UpdatedAt`
    （最后一条真实消息 ts，mtime 不配当活跃钟——沿用 claude 通道的
    mtime-provenance 模式，即 agent_inventory.go 里的
    `transcriptFreshnessFromMtime` 局部 map 的原则）；
  - 无头行文件**容忍**（2026-10-26 依实现修正：原稿写「整文件跳过」，但 steer
    先于 run 到达、或头行写失败降级时，合法 msg 行会先于头行落盘——无头行是
    合法中间态）：ID 兜底取文件名（去 `.jsonl`），`CreatedAt`/`UpdatedAt` 回落
    消息 ts；整文件损坏（一个可解析行都没有）才整体跳过不产幽灵卡。
- `collectAgentVibeSessions`（`agent_inventory.go:465`）加第三路合并，排序与
  `agentVibeSummaryMaxSessions` 截断沿用现有逻辑。

详情回放：**两处都要改**，缺一回放端到端必挂：

1. `findAgentVibeSessionDetail`（`agent_detail.go:227`）的两个文件枚举来源——
   `candidateAgentVibeSessionFiles`（`:246`）与 fallback
   `allAgentVibeSessionDetailFiles`（`:269`）——目前只 glob `~/.codex/sessions`、
   `~/.codex/archived_sessions`、`~/.claude/projects` 三个根；都要加
   `<agentHome()>/.aliang/ai-sessions`，否则 `.aliang` 路径永远到不了分发器；
2. `readAgentVibeSessionDetailFile`（`agent_detail.go:282`）加 `.aliang` 目录分支 →
   `readAgentCoreSessionMetaWithOptions`（journal 累积全部回合，回放按 claude/codex
   reader 同一套转录条数/分页上限（`agentVibeTranscriptMaxMessages` 那套）+
   `IncludePageMeta` 分页 + `DisplayMessageCount`，口径对齐 claude/codex reader）。

## 8. ID 与「进行中」合并语义（关键设计点）

落盘条目 **ID = 裸 agent 会话 ID（无前缀）**，与活跃覆盖层
`activeVibeSessionsSnapshot`（`agent_ai.go:88`，条目 ID 同用裸 `session.id`）
同一 ID 空间：

- **运行中**：`overlayActiveAgentVibeSessions`（`agent_inventory.go:209`）按 ID 命中
  → `mergeActiveAgentVibeSession` 合成一条（status=running），不出双卡；
- **结束后**：只剩文件条目（status=closed），正常留痕；
- claude/codex 的 `claude_`/`codex_` 前缀空间天然隔离，无冲突；
- 例外说明：claude/codex run 的活跃覆盖层同样用裸 ID、与其 `claude_`/`codex_`
  文件条目并存——那是既有行为，本设计不改变它，只保证 agentcore 自洽。

列表字段以头行为准的同 ID 换项目场景：极罕见（sessionID 稳定绑定一个对话），
初版接受「头行优先」，不改不补。

## 9. 保留策略与失败容忍

- 列表条数：沿用全局 recency cap（`agentVibeSessionFileScanLimit` /
  `agentVibeSummaryMaxSessions`），agentcore 与 claude/codex 竞争同一热榜，无特殊待遇；
- 磁盘上限：**新会话文件创建时** prune，只保留最新 `agentCoreJournalMaxFiles = 100`
  个（按 mtime，最旧先删）；扫描只读不删。prune 必须在 journal mutex 内执行；
  被删文件若仍有活跃写入者，Linux 上写入会继续进入已 unlink 的 inode（该会话
  后续留痕静默丢失）——这是尽力而为 journal 可接受的损失模式，实现里注释说明，
  不做「跳过打开中的文件」这类复杂化；
- 安全过滤沿用现有口径：`isSafeAgentProjectPath` / scanDirs / `isJunkAgentTitle`；
- journal 写失败（磁盘满/权限）：单行失败 Warn 后继续，不重试不阻塞 run；
- 目录创建失败（极端）：journal 变 no-op，run 完全不受影响。

## 10. 测试计划

- **journal 写入**：头行只写一次；多回合追加顺序正确；`message_id` 去重；半行
  容忍（手写残行后继续 append 可读）；写失败不影响 run（注入 IO 错误，run 正常
  完成）；文件名安全化。
- **reader**：摘要字段完整；热窗截断；`DisplayMessageCount`；mtime 缓存命中不重复
  解析；无头行文件容忍（ID 兜底文件名）；整文件损坏跳过；scanDirs 早过滤。
- **collect 合并**：与活跃覆盖层同 ID merge（运行中不出双卡）；junk title；三路
  合并后排序/截断不变。
- **detail 端到端（关键回归位）**：必须**穿过 `findAgentVibeSessionDetail`** 按
  session ID 查详情、断言能到达 journaled 文件并返回 agentcore 转录——只直调
  `readAgentVibeSessionDetailFile` 会假绿（枚举根漏配根本到不了分发器）；另含
  分页正确性。
- **fallback 路径**：provider=auto + 无外部 CLI 注入的 run（`runAgentCore` 承接），
  首条 user 行必须在 journal 里（防「fallback 用户漏首行」回归）。
- **端到端**：现有 `agent_ai_agentcore_run_test.go` 场景扩展，断言 journal 落盘
  内容与 run 输出一致。

## 11. 改动文件清单

| 文件 | 动作 |
|---|---|
| `app/http/services/agent_ai_agentcore_journal.go` | 新增：journal 写入（§6） |
| `app/http/services/agent_ai_agentcore_journal_test.go` | 新增 |
| `app/http/services/agent_ai_agentcore.go` | 挂点 1/3/4（入口头行+user 行、成功 assistant、终态 defer） |
| `app/http/services/agent_ai.go` | 挂点 2（steer core 分支；ai.send 入口不动） |
| `app/http/services/agent_inventory_agentcore.go` + `_test.go` | 新增：扫描 + reader（§7） |
| `app/http/services/agent_inventory.go` | `collectAgentVibeSessions` 三路合并 |
| `app/http/services/agent_detail.go` | 枚举两函数加 `.aliang` 根 + 分发加 `.aliang` 分支（§7 两处） |

## 12. 风险与回滚

- 风险极低：全部为追加式新路径，不触碰 claude/codex 任何现有行为；journal 故障
  退化方式是「不留痕」，不是「坏 run」。
- 回滚 = 移除挂点调用即可，落盘文件残留无害（扫描通道随代码一起移除）。
- 观测：journal 首次创建/写失败各打一条 Info/Warn 日志，带 sessionID。
