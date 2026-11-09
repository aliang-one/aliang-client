# 会话 ID 全链收敛(桌面↔手机错位+不可见)实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让桌面 aliangcode 会话与手机 vibe 共享同一 canonical 会话 id——桌面新建即手机可见(S1),手机续聊落回桌面同一会话/同一 journal(S2),并治愈存量错位会话。

**Architecture:** 复用既有的「managed_conversation_id 别名通道」(agent `annotateManagedVibeSessions` → PhoneServer `upsertReportedVibeSessions` 别名分支)与 `ai_session_redirect` 重定向表。agentcore 的会话 id 天然等于 journal 头行 session_id(客户端铸造、agent 原样采纳),所以只需:①agent 给 agentcore 清单条目补 `SourceSessionID`(绑定表回填)+`ManagedConversationID`(自身 id);②PhoneServer 对「自证首见托管条目」物化 canonical 记录而非铸造 `ai_import_` 壳;③壳 id 冒充 source 的绑定铸造加防线;④存量错位由 agent 端 journal 治愈(合并) + 服务端壳吸收(重定向)双向收敛。

**Tech Stack:** Go (alianggate agent) / TypeScript + vitest (AliangPhoneServer)

**审计依据:** 2026-11-07 九 agent 审计(S1=H2 confirmed;S2=H1 机制链逐行 confirmed)。根因:agentcore 清单条目 `SourceSessionID` 从不赋值(`agent_inventory_agentcore.go:152-166`)→PhoneServer 壳 sourceSessionId=壳 id 冒充原生 id(`imported.ts:28-46`)→resume 错 id 开新内核会话 + journal 文件名=壳 id 恒两文件。

**兼容性:** 两端改动互相向后兼容(旧 agent 上报缺 managed 字段→服务端走老壳路径;旧服务端忽略新字段→老行为)。部署顺序任意。

---

## 背景速查(实现者必读)

三套 id 命名空间:
1. **会话(壳)id**:桌面 dashboard 铸 `local-<convId>`(前端 generateId),手机 `ai_<nanoid>`,导入壳 `ai_import_<sha1(deviceId:source)>`。
2. **journal 头行 session_id** = 会话 id(agent 原样采纳客户端 id,`agent_ai.go:1841-1845`;文件名=消毒后的该 id)。
3. **内核原生 id**:agentcore=`agentcore-<ts>-<n>`(首回合内核自铸,只落绑定表 `m.bindings`,持久化于 `agent_ai_identity.json`);claude/codex=真 uuid。

关键既有机制:
- `annotateManagedVibeSessions`(`agent_ai_identity.go:359-401`):给清单条目按 native id 反查绑定,回填 `Origin=managed/ManagedConversationID/BindingState`。claude/codex 条目(前缀剥离可得 native)已走通;agentcore 条目两路都拿不到 native → 全跳过。
- PhoneServer 别名分支(`reported.ts:342-359`):`raw.managed_conversation_id` 命中已存在的托管记录 → `session.id = managed.id` 合并;**记录缺失时直接 `continue` 丢弃**(这是 S1 的服务端半边缺口)。
- `router.param('sessionId')`(`routes/ai.ts:108-119`):全局消费 `ai_session_redirect`——旧壳 id 的 URL 自动解析到 canonical。
- 事件回程(`aiRun.ts:299`):`aiSessions.get(session_id)` 直查——run 以 canonical id 下发后,事件无需任何重映射。
- digest tick(`agent_remote_ws.go:582-609`):磁盘六类指纹,10s tick+30s 门;内存态 create-only 会话不在指纹面(S1 的 agent 半边缺口)。

---

## Part A — alianggate(agent 端)

**Worktree:** 主仓根执行 `git worktree add .worktrees/session-id-fix -b fix/session-id-convergence`,以下路径均相对仓库根。首次 build 前从主检出 `cp -R app/website/dist .worktrees/session-id-fix/app/website/dist`(embed 占位,见记忆 transfer-worktree-build-gotchas)。

### Task A1: annotateManagedVibeSessions — agentcore 条目回填原生 id + 托管归属

**Files:**
- Modify: `app/http/services/agent_ai_identity.go:359-401` (annotateManagedVibeSessions)
- Test: `app/http/services/agent_ai_identity_annotate_test.go` (新建;先看同目录既有 `agent_ai_identity_test.go`/`agent_title_junk_test.go` 的 manager 构造套路,复用 helper)

- [ ] **Step 1: 写失败测试**(四例)
  1. agentcore 条目 `{ID:"local-abc", Provider:"agentcore", Origin:"dashboard"}`,manager 绑定表含 `{ConversationID:"local-abc", Provider:"agentcore", NativeSessionID:"agentcore-1-1", State:"confirmed"}` → 期望 `SourceSessionID=="agentcore-1-1"`、`ManagedConversationID=="local-abc"`、`BindingState=="confirmed"`、**`Origin` 仍为 `"dashboard"`**(同会话绑定不翻转 origin,保服务端 dashboard 角标)。
  2. agentcore 条目无任何绑定 → `SourceSessionID==""`、`ManagedConversationID=="local-xyz"`(自我声明托管)、Origin 不动。
  3. claude 条目 `{ID:"claude_<uuid>", Provider:"claudecode"}`,绑定 `{ConversationID:"ai_9x", Provider:"claudecode", NativeSessionID:"<uuid>"}` → 回归现状:`Origin=="managed"`、`ManagedConversationID=="ai_9x"`、`SourceSessionID=="<uuid>"`。
  4. `ai_import_h` 前缀的 agentcore 条目(**无绑定**)→ 自我声明不触发:`ManagedConversationID` 保持空(避免服务端复活拆分卡;由 Task A4 治愈)。注:若该条目存在绑定,绑定匹配循环照常可设 ManagedConversationID,此例只断言「自我声明分支未单独点火」。
- [ ] **Step 2: 跑测试确认失败** `go test ./app/http/services/ -run TestAnnotateManagedVibeSessions -count=1`(新文件名按实际;期望 FAIL)
- [ ] **Step 3: 实现**(整段**替换** annotateManagedVibeSessions 循环体为下方代码——含原有 native 推导/origin 缺省/绑定匹配三段,不是追加,否则 `nativeSessionID` 重复声明编译失败)
```go
		nativeSessionID := strings.TrimSpace(session.SourceSessionID)
		if nativeSessionID == "" {
			nativeSessionID = inventoryNativeSessionID(session.Provider, session.ID)
			session.SourceSessionID = nativeSessionID
		}
		// agentcore journal 条目 ID=会话 id(壳),原生句柄只在绑定表;不回填则
		// 服务端壳只能拿壳 id 冒充原生 id,跨端 resume 恒错(2026-11-07 错位)。
		if nativeSessionID == "" {
			if binding, ok := m.bindings[session.ID]; ok &&
				binding.NativeSessionID != "" &&
				providersEquivalent(binding.Provider, session.Provider) {
				nativeSessionID = binding.NativeSessionID
				session.SourceSessionID = nativeSessionID
			}
		}
		// agentcore 会话 id 即托管会话 id(journal 头行 session_id=conversation id),
		// 无绑定也声明托管归属——服务端据此物化 canonical 记录而非铸造壳(S1)。
		// ai_import_ 前缀是历史拆分产物,归 heal 治愈,不自我声明。
		if session.ManagedConversationID == "" &&
			providersEquivalent(session.Provider, "agentcore") &&
			!strings.HasPrefix(session.ID, "ai_import_") {
			session.ManagedConversationID = session.ID
		}
		if session.Origin == "" {
			session.Origin = "external"
		}
		for _, binding := range m.bindings {
			if nativeSessionID == "" || binding.NativeSessionID != nativeSessionID || !providersEquivalent(binding.Provider, session.Provider) {
				continue
			}
			// 同会话绑定(agentcore 常态:条目 id 即绑定会话 id)不翻转 origin
			// ——dashboard/phone 头行语义保留,服务端来源角标不失真;异会话
			// 别名(claude TUI 条目→手机会话)维持 managed 现状。
			if binding.ConversationID != session.ID {
				session.Origin = "managed"
			}
			session.ManagedConversationID = binding.ConversationID
			session.BindingState = binding.State
			session.BindingVersion = binding.BindingVersion
			break
		}
```
- [ ] **Step 4: 跑测试通过** + `go build ./...` + `go vet ./app/http/services/`
- [ ] **Step 5: 汇报等确认后提交**(提交需用户明示同意,标题全中文「修复:…」)

### Task A2: 活跃覆盖层纳入 create-only 会话 + origin 按 socket 推导

**Files:**
- Modify: `app/http/services/agent_ai.go:78-136` (activeVibeSessionsSnapshot)
- Modify: `app/http/services/agent_inventory.go:265-267` (normalizeActiveAgentVibeSession 的强写 running)
- Test: `app/http/services/agent_ai_active_overlay_test.go` (新建;参考 `agent_title_junk_test.go:116` 的 snapshot 构造)

- [ ] **Step 1: 失败测试**(三例)
  1. create-only 会话(`cancel==nil`,history 含 1 条 user 消息,`originSocket:"aisock-3"`)→ 出现在 snapshot,`Status=="idle"`、`Origin=="dashboard"`、`ManagedConversationID==session.id`。
  2. 运行中会话(`cancel` 非 nil,`originSocket:"remote"`)→ `Status=="running"`、`Origin=="phone"`(不再恒 "managed")。
  3. overlay 合并:`normalizeActiveAgentVibeSession` 对已带 `Status:"idle"` 的条目不得改写成 running。
- [ ] **Step 2: 跑测试失败**
- [ ] **Step 3: 实现**
  - activeVibeSessionsSnapshot:删除 `if session == nil || session.cancel == nil { continue }` 改为 `if session == nil { continue }`;`Status` 改 `firstNonEmpty(statusByCancel, "idle")` 形态:
```go
		status := "idle"
		if session.cancel != nil {
			status = "running"
		}
		// origin 按发起 socket 推导(与 journal 头行同源):remote=phone、
		// aisock-*=dashboard。恒写 "managed" 会被服务端映射成 'phone',
		// 把 dashboard 会话的来源角标翻错(2026-11-07)。
		origin := agentCoreJournalOriginFor(session.originSocket, session.surface)
```
  其余字段构造不变(Status/Origin 用上面两值)。
  - normalizeActiveAgentVibeSession: `session.Status = "running"` 改为
```go
	if session.Status == "" {
		session.Status = "running"
	}
```
  - **有意行为变更(测试断言时勿当回归)**:无 `originSocket` 的会话 origin 推导为 `unknown` → 服务端映射 `'tui'`;旧实现恒写 `"managed"` → 映射 `'phone'`。这是修正(managed 不是发起端语义),第二个调用点 agent_service.go:345(scanDirs=nil)同样受影响。
- [ ] **Step 4: 全包测试** `go test ./app/http/services/ -count=1`(注意既有 flake 基线,失败先对照记忆 preexisting-test-flakes;再 `go build ./...`)
- [ ] **Step 5: 等确认提交**

### Task A3: create 纳管即打内存脏标 → digest tick 放行 hello(S1 实时性)

**Files:**
- Modify: `app/http/services/agent_vibe_digest.go` (新增包级 atomic 标记+setter)
- Modify: `app/http/services/agent_ai.go` (registerAISessionLocked 内打标)
- Modify: `app/http/services/agent_remote_ws.go:590-597` (digest tick 的 unchanged 短路)
- Test: `app/http/services/agent_vibe_digest_test.go` 追加(只测标记置位/消费语义;tick 协程不直测)

- [ ] **Step 1: 失败测试**:`markAgentVibeMemorySnapshotDirty()` 后 `consumeAgentVibeMemorySnapshotDirty()` 返回 true 且第二次返回 false;`agentAISession` 经 create 注册路径(create 走不到就用 registerAISessionLocked 直测)后标记为 true。
- [ ] **Step 2: 失败确认**
- [ ] **Step 3: 实现**
```go
// agent_vibe_digest.go
// agentVibeMemorySnapshotDirty: 内存态会话(create-only 纳管)不在六类磁盘
// 指纹面,digest 永远不变 → hello 只能等 60s 周期。create 纳管时置位,
// digest tick 消费一次放行即时 hello(仍受 30s 最小间隔门约束,防创建风暴)。
var agentVibeMemorySnapshotDirty atomic.Bool

func markAgentVibeMemorySnapshotDirty()   { agentVibeMemorySnapshotDirty.Store(true) }
func consumeAgentVibeMemorySnapshotDirty() bool {
	return agentVibeMemorySnapshotDirty.CompareAndSwap(true, false)
}
```
  registerAISessionLocked 末尾调 `markAgentVibeMemorySnapshotDirty()`。
  agent_remote_ws.go digest tick:
```go
				digest := agentVibeDigest(agentHome())
				if digest == "" {
					continue
				}
				if s.remoteHello.baseline(digest) {
					continue
				}
				// 内存态变更(create 纳管)即使磁盘指纹不变也要放行一次 hello;
				// 标记被 30s 门 defer 掉就等 60s 周期兜底,不重试。
				if !consumeAgentVibeMemorySnapshotDirty() && s.remoteHello.unchanged(digest) {
					continue
				}
```
- [ ] **Step 4: 测试+build+vet 通过**
- [ ] **Step 5: 等确认提交**

### Task A4: 治愈存量错位 — ai_import_*.jsonl 并回 canonical journal

**Files:**
- Modify: `app/http/services/agent_ai_identity.go` 或 `agent_ai_agentcore_journal.go` (新增 heal 方法,建议放 journal 文件旁)
- Modify: `app/http/services/agent_service.go:2681-2688` (collectAgentSyncSnapshotWithActiveRuns 开头调用)
- Test: `app/http/services/agent_ai_journal_heal_test.go` (新建;temp dir 造 journal,参考 `agent_ai_journal_history_test.go` 的隔离套路,**绝不触真机 ~/.aliang/ai-sessions**)

- [ ] **Step 1: 失败测试**(存量拓扑=链式,评审修正):temp journal 目录造 `local-a.jsonl`(头行+2 行 msg)与 `ai_import_h.jsonl`(头行+3 行 msg);manager 绑定表两条 confirmed:
  - `{ConversationID:"local-a", Provider:"agentcore", NativeSessionID:"agentcore-1-1"}`
  - `{ConversationID:"ai_import_h", Provider:"agentcore", NativeSessionID:"local-a"}`(**链式:壳绑定的 native=壳 id 冒充,即另一绑定的 ConversationID——这是真实存量形态**,评审实锤;同 native 形态作为第二用例兼容)
  用例 1(链式)→ heal 后 `ai_import_h.jsonl` 删除、`local-a.jsonl` 含 5 行内容行、`m.bindings["ai_import_h"]` 孤儿绑定清除且身份态持久化被触发。用例 2(同 native:两绑定同 NativeSessionID)→ 同样并回。反向用例:无绑定/native 指向不存在的会话/目标会话运行中(`cancel!=nil`)→ 不动文件。
- [ ] **Step 2: 失败确认**
- [ ] **Step 3: 实现**(要点)
  - `func (m *agentAIManager) healSplitAgentCoreJournals()`:持 `m.mu` 枚举 `ai_import_` 前缀绑定,按序两种匹配找目标会话:
    1. **同 native**:`bindingForNative(provider, native)` 命中其他会话;
    2. **链式(存量主形态)**:`m.bindings[binding.NativeSessionID]` 存在且 ConversationID 不同——即壳的"native"本身是另一会话的 id(壳 id 冒充链)。
    目标会话**不在 m.sessions 或 cancel==nil** 才动手。
  - 文件手术走 `m.coreJournal` 既有锁:目标文件先 `ensureSessionHeader`(幂等),逐行追加源文件非头行,成功后 `os.Remove` 源文件并 `delete(m.bindings, 源会话id)` + `persistIdentityState()`;任何一步失败即中止且不删源(幂等:源在即重试)。
  - `collectAgentSyncSnapshotWithActiveRuns` 开头调用一次(hello/sync 双通道单 choke point);文件消失即天然幂等。
- [ ] **Step 4: 测试+`go build ./...`**
- [ ] **Step 5: 等确认提交**

---

## Part B — AliangPhoneServer(服务端)

**Worktree:** `cd ~/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer && git worktree add ../AliangPhoneServer-sessionfix -b fix/managed-session-materialize`(兄弟目录命名,勿用 .worktrees)。`npm ci` 或从主检出软链 node_modules。测试 `npx vitest run --config vitest.config.ts <file>`。

### Task B1: 自证首见托管条目物化 canonical 记录 + ai_import 重报丢弃

**Files:**
- Modify: `server/src/modules/agent/reported.ts:335-375` (upsertReportedVibeSessions 循环头)
- Test: 既有 reported 测试文件(`rg -l "upsertReportedVibeSessions" server/src --tests` 定位),追加用例

- [ ] **Step 1: 失败测试**(四例)
  1. `raw={id:"local-abc", managed_conversation_id:"local-abc", origin:"dashboard", provider:"agentcore", binding_state:"confirmed", source_session_id:"agentcore-1-1", …}` 且库中无 local-abc → 落库记录 **id=="local-abc"**(非 ai_import 壳)、sourceSessionId=="agentcore-1-1"、origin=="dashboard"。
  2. 同 raw 但库中已有 local-abc → 走别名合并(回归现状:status 保护/标题守卫语义不变)。
  3. `raw.id=="claude_<uuid>"` 且 `managed_conversation_id:"ai_9x"` 指向不存在记录 → 仍跳过( forged/stale 守卫保留)。
  4. `raw.id=="ai_import_x"`(无 managed)→ 整条丢弃(不铸壳、不物化)。
- [ ] **Step 2: 失败确认**
- [ ] **Step 3: 实现**(managed 分支重构)
```ts
    // 二阶壳/治愈前残留:ai_import_ 不是任何内核原生 id,再铸壳只会复活
    // 拆分会话;agent 端 heal 负责并回 canonical(2026-11-07)。有意代价:
    // 未被绑定也未被吸收的壳自此冻结磁盘驱动的元数据刷新(messageCount/
    // status/updatedAt)——可接受的降级,换取拆分卡永不复活。
    if (raw.id.startsWith('ai_import_')) continue;
    const session = buildReportedAiSession(userId, deviceId, raw);
    if (raw.managed_conversation_id) {
      const managed = aiSessions.get(raw.managed_conversation_id);
      if (
        managed &&
        (managed.userId !== userId ||
          managed.deviceId !== deviceId ||
          managed.id.startsWith('ai_import_'))
      ) {
        continue;
      }
      if (!managed) {
        // 自证首见:agentcore 条目 raw.id===managed_conversation_id(journal
        // 头行 session_id 即会话 id)——物化 canonical 记录而非壳。异形条目
        // (claude 条目的 managed 指手机会话)记录必已存在(绑定以它为先决),
        // 缺失仍是 forged/stale,维持跳过。
        if (raw.id !== raw.managed_conversation_id) continue;
        session.id = raw.managed_conversation_id;
      } else {
        session.id = managed.id;
        session.createdAt = managed.createdAt;
        if (raw.binding_state !== 'confirmed') {
          session.sourceSessionId = managed.sourceSessionId;
        }
      }
    }
```
  (原 phoneSessionOwnsCliSource 守卫与后续 upsert 流程不动——managed 条目本就跳过该守卫。)
- [ ] **Step 4: 目标测试文件通过 + `npm run typecheck`**
- [ ] **Step 5: 等确认提交**

### Task B2: 壳吸收清理 — 物化/别名时收编 masquerade 壳

**Files:**
- Modify: `server/src/modules/ai/bindings.ts:349-403` (reconcileImportedDuplicate 尾部抽公共函数)
- Modify: `server/src/modules/agent/reported.ts` (新增 cleanupMasqueradeShells + 接线;upsertReportedVibeSessions 改 async,两处调用点 handler.ts/statusSync.ts 加 await)
- Test: 同 B1 测试文件 + bindings 测试

- [ ] **Step 1: 失败测试**:库中预置壳 `ai_import_<hash>`(sourceSessionId=="local-abc", deviceId 同, transcript 2 条)→ 上报自证条目物化 local-abc 后 → 壳被删、redirect(ai_import_<hash>→local-abc)落库、壳 transcript 并入 canonical、广播 `ai.session.deleted`。
- [ ] **Step 2: 失败确认**
- [ ] **Step 3: 实现**
  - 从 reconcileImportedDuplicate 抽 `async function absorbDuplicateSession(canonical, duplicate)`(mergeReportedTranscript+messageCount+rememberAiMessages+upsertAiSessionRedirect+Repository.delete+enqueueEntityDelete+rememberAudit+publishToMobiles deleted),reconcile 复用;新函数:
```ts
async function cleanupMasqueradeShells(canonical: AiSession): Promise<void> {
  const stale = [...aiSessions.values()].filter(candidate =>
    candidate.id.startsWith('ai_import_') &&
    candidate.userId === canonical.userId &&
    candidate.deviceId === canonical.deviceId &&
    candidate.sourceSessionId === canonical.id,
  );
  for (const duplicate of stale) {
    await absorbDuplicateSession(canonical, duplicate, 'ai.session.import_masquerade_reconciled');
  }
}
```
  - upsertReportedVibeSessions 改 `async`,在 managed 物化/别名落库 changed 后 `await cleanupMasqueradeShells(nextSession)`;两处调用点补 `await`(handler.ts:505 handleAgentMessage 内、statusSync.ts)。
- [ ] **Step 4: 目标测试+typecheck+`npx vitest run --config vitest.config.ts`(全量)**
- [ ] **Step 5: 等确认提交**

### Task B3: agentcore 壳 id 冒充 source 的绑定铸造防线

**Files:**
- Modify: `server/src/modules/ai/bindings.ts:123` (ensureAiProviderBinding source 分支前)
- Test: bindings 测试文件追加

- [ ] **Step 1: 失败测试**:session `{provider:"agentcore", sourceSessionId:"local-abc"}` 无既有绑定 → ensureAiProviderBinding 返回 undefined(不铸 confirmed 绑定);`sourceSessionId:"goal_x"` 同样 undefined(评审:phone-born 命名空间不止 local-/ai_);对照 `sourceSessionId:"agentcore-1-1"` → 照常铸 confirmed(回归)。
- [ ] **Step 2: 失败确认**
- [ ] **Step 3: 实现**
```ts
  if (session.sourceSessionId) {
    // agentcore 壳 id 冒充防线(2026-11-07 错位事故):agentcore 原生句柄形如
    // agentcore-<ts>-<n>(loop.go newSessionID 唯一铸造点);凡不以该前缀开头
    // 的 sourceSessionId(local-*/ai_*/goal_* 等一切会话 id 命名空间,评审
    // 指出勿枚举遗漏)都不是内核句柄——拿它铸 confirmed resume 绑定等于把
    // 错位钉死。降级 discover。
    if (
      provider === 'agentcore' &&
      !session.sourceSessionId.startsWith('agentcore-')
    ) {
      return undefined;
    }
```
- [ ] **Step 4: 测试+typecheck**
- [ ] **Step 5: 等确认提交**

---

## Part C — 终验(不写码)

- [ ] alianggate: `go build ./... && go vet ./app/... && gofmt -l app/http/services | wc -l`=0;`go test ./app/http/services/ -count=1`(对照 flake 基线)
- [ ] PhoneServer: `npm run typecheck && npx vitest run --config vitest.config.ts`
- [ ] 双端交叉冒烟清单(部署后手测,记录到 handover):
  1. 桌面 dashboard 新建 agentcore 会话发一条 → 手机 vibe/项目页 ≤60s 可见,卡带 dashboard 角标,id=`local-*`。**先在手机 WS 在线状态下验**(updated 广播插卡);若不可见,说明撞了「手机端 ai.session.created 不插卡+前台不重拉」的客户端半边——把「手机端前台恢复重拉快照」从明确不做的批次提前,不算本计划缺陷。
  2. 手机进该会话发消息 → 桌面原对话实时长出内容(journal 同一文件),手机无新卡。
  3. 存量错位:部署新 agent 后一次 hello → 桌面拆分卡消失(ai_import journal 并回),手机旧壳卡消失(redirect+deleted)。
  4. 手机自建会话续聊回归正常(resume 同 native)。
- [ ] 部署顺序任意;发布说明写明「旧壳 id 链接经 redirect 自动解析,无需迁移」。

## 明确不做(YAGNI)

- 手机端「前台恢复重拉快照」(S1 客户端半边,另立批次;本计划服务端 updated 广播+60s hello 已覆盖 WS 在线场景)。
- confirmBinding 同 id 让渡例外(H4 窄路径;主链修复后自然绕开)。
- 测试污染 ~/.aliang/ai-sessions 的治理(独立卫生批次)。
