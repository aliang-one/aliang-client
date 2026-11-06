# aliangcode 会话六缺陷修复(幽灵卡/重启失忆/双卡/改名断链/错误终态/v2守卫)+App 续聊入口 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development。Steps 用 checkbox。**RN 代码先 invoke react-native-best-practices。**

**Goal:** 修复 25-agent 调查定案的六项 alianggate 会话缺陷 + App 侧 agentcore 续聊入口缺失。

**架构:** 全部为既有路径的修正/补齐——journal 写序调整、create 恢复对齐 message 懒建、守卫模式镜像 runStart 先例、rename 链扩面、end 行进转录、前端去重与入口扩容。协议无新帧,契约表仅 file.read.result 式小登记(若涉)。

**真值源:** 记忆 aliangcode-session-misalign-and-reply-loss.md + 各任务内锚点(实现者须逐项核对,不符即 BLOCKED)。

**⚠️ 全局纪律:** 禁止 git commit/tag(等用户明示);worktree 开发;定向测试 -count=1,禁全量/-race;alianggate 禁跑 go test 于主检出(56433 真机)。

---

## G 组(alianggate,worktree `.worktrees/agentcore-session-fixes`,分支 `fix/agentcore-session-fixes`,基于本地 master)

### Task G1: 幽灵卡——journal 头行/首条 user 行挪到模型校验之后

**现状**: `agent_ai_agentcore.go` runAgentCore 入口 :1143 `journal.ensureSessionHeader` + :1154 `appendUser` 无条件落盘,之后 :1171 `agentCoreEnsureUpstream`、:1176-1182 「未指定模型」校验失败 return——失败回合留下只有 user 行的幽灵会话卡(记忆 aliangcode-new-machine-empty-model 的遗留修复项)。
**修法**: 把 ensureSessionHeader+appendUser(及其依赖的 lastUserHistoryMessage 取句)移到上游/模型校验**之后**;校验失败路径不写任何 journal 行(卡片消失=预期)。**核对项**:①end 诊断行(appendEndDiagnostics deferred)在校验失败路径是否会凭空建头——若 appendLineLocked 会 ensure 头,须保证失败路径连 end 行也不落(或在 error 分支显式跳过 journal);②绑定 reserve/seed 的时序是否依赖头行先落(读 seedConfirmedBindingResumeLocked/confirmBinding 的 journal 依赖——它们依赖 identity.json 而非 journal,预期无依赖,须核实);③去重键(session+role+message_id)在挪动后仍成立。
**测试**: 空模型回合 → journal 目录无新文件/无新行(红:修复前会建卡);正常回合 → 头行+user 行照旧。**Files**: agent_ai_agentcore.go、agent_ai_agentcore_journal.go(若需)、agent_ai_agentcore_journal_test.go/新测试。

### Task G2: 重启失忆——create 路径 journal 历史回装

**现状**: 仪表盘每条消息先发 `ai.session.create`(不带 transcript);create 重建的内存会话 history 为空;**journal 历史回装只存在于 message() 懒建分支**(agent_ai.go:2177-2258,回装 :2194-2216)——agent 进程重启后下回合模型只看到最新一条消息。
**修法**: create 路径(agent_ai.go ~1851)建会话后,若 payload 无 transcript 且该会话 id 的 journal 存在,复用 message 懒建同款回装函数回装 history(并同样 seedConfirmedBindingResume 供恢复点);有 transcript 则仍以 transcript 为准。**核对项**:①回装函数是否可直接复用(签名/耦合);②create 后首条 message 走既有路径时不得二次回装(幂等);③含工具行/end 行的 journal 回装过滤与 message 懒建一致。
**测试**: 造 journal 文件 → create(无 transcript)→ 断言内存 history 非空且顺序正确;有 transcript → transcript 优先。

### Task G3: v2 ai.message 补 provider 族守卫(与 v3 runStart 对齐)

**现状**: message() 对已存在会话 :2326-2331 仅 normalize provider 无守卫;v3 runStart :2464-2470 有 confirmed 绑定族守卫(不同族强制改写)——v2 路径 provider 翻转可把 agentcore 会话改派给 claude CLI。
**修法**: 镜像 runStart 守卫:已存在会话且 confirmed 绑定存在且 `!providersEquivalent(请求 provider, 绑定.Provider)` → 强制改写 msg provider 为绑定值(带注释引用 runStart 先例)。**核对项**:①新会话(无绑定)不受影响;②守卫只改写 provider,不拒绝消息(与 runStart 语义一致);③保留 reserved 绑定的既有豁免(读 runStart 守卫的完整条件再镜像)。
**测试**: agentcore 绑定会话收到 provider=claude 的 ai.message → 实际执行 provider 仍 agentcore(红:修复前翻转)。

### Task G4: 改名链覆盖 agentcore 卡

**现状**: 手机改名经 ai.session.rename → rename 缓存,但 `applyClaudeRenameNames` 只处理 `claude_` 前缀卡——agentcore 卡(裸 id/`ai_<nanoid>` 文件名)改名永不生效。
**修法**: 改名应用面扩到 agentcore journal 卡。**实现方向(实现者先读 agent_rename_cache/agent_rename_namesource 现状再定)**:rename 缓存键若为 native id,agentcore 卡在清单侧取不到 native id(agent_ai_identity.go:389-401 裸 id 返回空)——则按**会话 id**(journal 键)存取改名;最小改法=清单组装处(agent_inventory_agentcore.go)查 rename 缓存时按会话 id 兜底取标题。**核对项**:①不改 claude_ 既有路径;②rename 优先级:缓存标题 > journal 首条消息标题。
**测试**: 写 rename 缓存(会话 id 键)→ 清单卡 title=改名值。

### Task G5: 错误终态进转录(消除「问题悬空」)

**现状**: end(error) 行只进诊断面板(agent_inventory_agentcore.go:224),快照(agent_ai_sockets.go:252)与详情转录都不含——失败提问悬空无任何反馈。
**修法**: ①快照读侧(readAgentCoreJournalHistoryFrom,agent_ai_journal_history.go)把 end(error) 行映射为 role=assistant? **否**——映射为**系统条**新增 kind(system, text=人话终态文案);②详情读侧(agent_inventory_agentcore.go)同样产出系统条;③前端(app/website useAliangChat.js 的 applyDeviceTranscript/applyDeviceSnapshotTranscript)渲染系统条为居中灰色条(镜像仓内既有系统条先例——先找);**App 端本批不做**(手机端后续迭代)。**核对项**:①正常 end(成功)不产系统条;②文案从 end 行 fail reason 人话化(镜像诊断面板的文案函数,可复用则复用);③快照 200 条限幅口径不变。
**测试**: journal 含 end(error) → 快照/详情产系统条;成功 end → 无。

### Task G6(website): 侧栏双卡去重

**现状**: 仪表盘侧栏本地草稿与设备壳合并无去重,同一会话渲染两张卡。
**实现者先行侦察**: 定位侧栏合并逻辑(app/website src 下 DashboardPage/侧栏组件,搜「草稿/draft/device 壳/ai_import」);去重键=会话 id(或草稿与壳的关联字段——读代码定)。修法:合并处按去重键过滤(壳优先或草稿优先,读产品语义:壳有服务端状态应优先,草稿仅本地未发送)。**测试**: 镜像仓内前端测试先例(vitest)。

## A 组(App 仓,兄弟 worktree `AliangVibeCodingPhone-sessionfix`,分支 `feat/agentcore-continue-entry`,基于 main)

### Task A1: agentcore 壳卡接入「另起续聊」

**现状**: 「另起续聊」是 App 唯一带 source_session_id 的续聊通道,但只认 `claude_`/`codex_` 前缀壳(sessionOrigin.ts:28-43、VibeSessionCard.tsx:118/:222/:224);agentcore 壳卡(裸 id,provider='agentcore')拿不到入口。
**修法**: 入口条件扩容:provider==='agentcore' 的壳卡同样给「另起续聊」;续聊创建分支补 provider:'agentcore'(镜像 claude 分支的字段组装)。**核对项**:①source_session_id 传壳卡的 sourceSessionId(PhoneServer 已在列表接口下发);②旧服务端无该 provider 分支时的降级提示;③i18n 文案沿用既有续聊键。
**测试**: 镜像既有卡片/入口测试先例。
**⚠️ RN 代码先 invoke react-native-best-practices。**

## 验收对照

| 缺陷 | 任务 |
|---|---|
| 幽灵卡 | G1 |
| 重启失忆 | G2 |
| v2 翻转缺口 | G3 |
| 改名断链 | G4 |
| 问题悬空 | G5 |
| 侧栏双卡 | G6 |
| agentcore 无续聊入口 | A1 |
