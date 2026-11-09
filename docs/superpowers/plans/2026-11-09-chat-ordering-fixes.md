# 聊天消息排序/吞流四缺陷修复 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复 2026-11-09 消息架构审计定案的四处 REAL 缺陷:手机网页排序器乱序(①)、仪表盘草稿对账吞流守卫(②)、设备壳快照替换吞流(③)、agent 快照与 delta 无序仲裁(④)。

**Architecture:** ①在 PhoneServer web 抽纯排序模块对齐服务端 `mergeAiMessages` 语义(双 index 才比 index,否则 timestamp);②③在仪表盘 `useAliangChat.js` 补 running 守卫与 running 气泡保留;④在 agent Go 侧把订阅快照改为「先读 journal→再订阅→经 pump 队列下发」,与 delta 同队列 FIFO 保序。

**Tech Stack:** Vue3 composable + vitest(仓A app/website)、React + vitest(仓B web/src)、Go(仓A app/http/services)。

**两仓worktree:** 仓A `.worktrees/chat-order-fix`(分支 `fix/chat-order`);仓B `AliangPhoneServer/.worktrees/chat-order-fix`(分支 `fix/chat-line-order`)。worktree 放仓内使 node_modules 向上解析到主检出。

**提交纪律:** 按用户 CLAUDE.md,所有 commit 步骤必须等用户明确批准后执行,计划内只到「测试绿」为止。

---

### Task 0: 双仓 worktree

- [x] **Step 0.1** 仓A(alianggate,当前 master):
  ```bash
  cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
  git worktree add .worktrees/chat-order-fix -b fix/chat-order
  ```
- [x] **Step 0.2** 仓B(AliangPhoneServer,主分支 main):
  ```bash
  cd /Users/mac/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer
  git worktree add .worktrees/chat-order-fix -b fix/chat-line-order
  ```

---

### Task 1(仓B,缺陷①): 手机网页排序器删非对称 index 分支

**Files:**
- Create: `web/src/aiLineOrder.ts`(仓B worktree 内)
- Modify: `web/src/App.tsx` 两处 sort 比较器(`mergeTranscriptLinesFromSessions` ~7140 行、`mergeAiLines` ~7154 行)+ `areAiLinesEqual` + 两处 `index:` 字面量
- Test: `web/src/aiLineOrder.test.ts`

> **实装与计划草稿的重大偏离(两轮对抗评审结果,最终落成为准)**:
> 计划草稿原案是**单函数 pair 级比较器** `compareAiLines(left, right)`——「双边有 index 才比 index,否则落 timestamp」。
> 第二轮对抗评审证明该案**非法**:index(position)与 timestamp 在生产库上存在 47,095 对倒挂(docs: `db/aiMessages.ts:88-94`),pair 级切键必然构成**非传递环**。
> 反例(评审实证):`A={ts:00:02, 无 index}`、`B={ts:00:03, index:0}`、`C={ts:00:01, index:1}` → `A<B`、`B<C`、`A>C`,6 种输入排列产出 3 种不同结果。
> 落成改为**数组级** `sortAiLines(lines)`,整数组同构判定后统一到**单一键**,杜绝环:
> - 全无 index(纯直播)→ timestamp 序;
> - 全有 index(纯服务端历史)→ position 序(保住 DB 权威);
> - 混合 → 给无 index 行在相邻时间锚点之间**插值出合成 index**,再统一按 index 排。
>
> 同时修正一条对旧实现的事实判断:旧内联比较器的三条 index 分支是**合法**的 block 字典序(`(hasIndex?0:1, hasIndex?index:ts)`),不是非传递——它是**合法但错误**(把直播行恒排到扫盘行之后 → `[问1,问2,答1,答2]`)。
> 另修两处评审定谳的伴生缺陷:
> - `areAiLinesEqual` 未比较 `index` → index 到位但显示序未变时判等,新结果被 `current` 原样丢弃,服务端 position 永久进不了 state(评审 D-2);
> - `App.tsx` 两处 `index: message.index` 字面量恒写 `undefined` → `{...existing, ...line}` 合并会把已有 position 覆盖掉(评审 D-3),改为条件展开。

- [x] **Step 1.1 写测试** `web/src/aiLineOrder.test.ts`(12 例全绿;含**区分力断言**):

```ts
// 关键:fixture 必须让新实现与旧实现给出不同结果,否则测试零区分力。
// 第一版 fixture 把答1 ts 写在问2 之后,新旧都对 → 无效(评审实证)。
const q1 = line('q1', '2026-01-01T00:00:01Z', 0);   // 扫盘,有 index
const a1 = line('a1', '2026-01-01T00:00:02Z');      // 直播行,无 index —— 夹在中间
const q2 = line('q2', '2026-01-01T00:00:03Z', 2);
const a2 = line('a2', '2026-01-01T00:00:04Z');

it('断言完整排序结果 = [问1, 答1, 问2, 答2](旧比较器给出 [问1,问2,答1,答2])', () => {
  expect(ids(sortAiLines([q1, a1, q2, a2]))).toEqual(['q1', 'a1', 'q2', 'a2']);
});

it('该 fixture 确实能区分修复前后(旧比较器结果为病态序)', () => {
  // legacyCompare 为本文件内旧比较器的复制品,仅用于证明区分力。
  expect(ids([...[q1, a1, q2, a2]].sort(legacyCompare))).toEqual(['q1', 'q2', 'a1', 'a2']);
  expect(ids([...[q1, a1, q2, a2]].sort(legacyCompare)))
    .not.toEqual(ids(sortAiLines([q1, a1, q2, a2])));
});
```

其余用例:纯历史 position 权威(构造 ts 与 index 反向,断言按 index 且显式 `not.toEqual` 时间序)、纯直播时间序、**排列稳定性穷举**(3 行池 6 排列 / 4 行混合池 24 排列,断言 `outputs.size === 1`)、退化输入(空/单行/null、不可解析 ts、NaN/Infinity index、重复 index)。

- [x] **Step 1.2 跑测试确认失败**:`cd 仓B worktree && npx vitest run web/src/aiLineOrder.test.ts` → FAIL(模块不存在)。

- [x] **Step 1.3 实现** `web/src/aiLineOrder.ts`(三态单键,见文件头两轮评审记述):

```ts
export function sortAiLines<T extends AiLineLike>(lines: T[]): T[] {
  const rows = Array.isArray(lines) ? [...lines] : [];
  if (rows.length <= 1) return rows;
  const indexedCount = rows.filter(hasIndex).length;
  if (indexedCount === 0) return rows.sort(byTimeThenId);                  // 纯直播
  if (indexedCount === rows.length) {                                     // 纯历史:position 权威
    return rows.sort((l, r) => (l.index! - r.index!) || l.id.localeCompare(r.id));
  }
  // 混合:按时间在相邻锚点间插值出合成 index,再统一按 index 排
  const anchors = rows.filter(hasIndex).sort((l, r) => l.index! - r.index!);
  const keyOf = new Map<AiLineLike, number>();
  for (const anchor of anchors) keyOf.set(anchor, anchor.index!);
  for (const row of rows) { /* 找时间上前后最近的锚点 → 中点 / 锚点±1 / +Infinity */ }
  return rows.sort((l, r) => { /* 按 keyOf;同键落 byTimeThenId */ });
}
```

- [x] **Step 1.4 跑测试确认通过**(同 Step 1.2 命令 → 12 passed)。

- [x] **Step 1.5 App.tsx 换用**:顶部 `import { sortAiLines } from './aiLineOrder.js'`;两处 `Array.from(byId.values()).sort(内联比较器)` 整体替换为 `sortAiLines(Array.from(byId.values()))`。**另必改两处**(评审 D-2/D-3):
  - `areAiLinesEqual` 追加 `line.index === other.index`;
  - `mergeTranscriptLinesFromSessions` 与 `aiMessagesToLines` 的 `index: message.index` 改为 `...(typeof message.index === 'number' ? { index: message.index } : {})`。

- [x] **Step 1.6 全量回归**:`npx tsc -p web/tsconfig.json --noEmit` + `npx vitest run`(仓B worktree)→ 全绿(实测 365 文件 / 3113 测试)。

---

### Task 2(仓A,缺陷②): 草稿对账补 running 气泡守卫

**Files:**
- Modify: `app/website/src/composables/useAliangChat.js:51-57`(`draftNeedsJournalReconcile`)
- Test: `app/website/src/composables/useAliangChat.reconcile.test.js`

- [x] **Step 2.1 写失败测试**(在既有 reconcile 测试文件追加,import 风格随既有文件):

```js
it('本地草稿存在 running 气泡时不对账(流式气泡保护,2026-11-09)', () => {
  const conv = {
    aiSessionId: 'local-x',
    deviceLoadedAt: 0, // TTL 外
    messages: [{ role: 'assistant', content: '', status: 'running', ts: Date.now() }],
  };
  expect(draftNeedsJournalReconcile(conv, [{ id: 'local-x' }])).toBe(false);
});
```

- [x] **Step 2.2 跑测试确认失败**:`npx vitest run src/composables/useAliangChat.reconcile.test.js`(仓A worktree 的 app/website 下)→ FAIL(当前守卫只查 conv.running,本地草稿恒 undefined → 返回 true)。

- [x] **Step 2.3 实现**——`draftNeedsJournalReconcile` 首段守卫改为:

```js
  if (!conv || conv.running) return false;
  // 流式气泡保护(2026-11-09 审计 REAL-high):running 标记只在设备壳上置位,
  // 本地草稿流式中恒 undefined——以「数组里有 running 气泡」为准,重建会把
  // 直播气泡换成 journal 全 done 块级快照,后续 delta 全被 done 屏障吞掉。
  if (Array.isArray(conv.messages)
    && conv.messages.some(m => m && m.role === 'assistant' && m.status === 'running')) return false;
```

- [x] **Step 2.4 跑测试确认通过** + 该文件既有用例全绿。

---

### Task 3(仓A,缺陷③): 设备壳快照/详情替换保留 running 气泡

**Files:**
- Modify: `app/website/src/composables/useAliangChat.js:1145`(applyDeviceTranscript 加 running 参数)、`:1196`(applyDeviceSnapshotTranscript 同款)、`:1064/1275`(调用点)、`:1327`(快照调用点传 `e.status === 'running'`)
- Test: `app/website/src/composables/useAliangChat.transcript.test.js`

- [x] **Step 3.1 写失败测试**:

```js
it('快照替换在会话 running 时保留尾部 running 占位(delta 有落点)', () => {
  // applyDeviceSnapshotTranscript 经 composable 内部函数间接驱动:
  // 构造设备壳 + 触发 handleDeviceSnapshot(e.status='running'),
  // 断言替换后尾气泡 status==='running' 且 content===''。
});
```
(实现时按该测试文件的既有挂载方式落成可执行代码;断言目标:替换后 `shell.messages` 尾气泡 running。)

- [x] **Step 3.2 跑测试确认失败**。

- [x] **Step 3.3 实现**:

```js
  // 会话仍在跑时的尾部 running 占位(2026-11-09 审计 REAL-high):快照/详情
  // 整体替换把气泡全置 done,而 journal 快照天然缺在途回合内容(turn 行回合末
  // 才落盘),没有占位则替换后到来的 delta 命中 ensureAssistantBubble 的 done
  // 屏障被静默吞掉(表现为答案消失)。占位仅展示,绝不进喂入流。
  export function deviceRunningTailBubble(ts) {
    return { role: 'assistant', content: '', status: 'running', ts };
  }

  // running 尾占位补齐:仅当已有尾气泡不是 running 时补(幂等)。
  function ensureRunningTail(messages, fallbackTs) {
    const last = messages[messages.length - 1];
    if (!last || last.role !== 'assistant' || last.status !== 'running') {
      messages.push(deviceRunningTailBubble(fallbackTs));
    }
  }
```
- **实装落点与计划不同(收编)**:行→气泡映射本体收进模块级纯函数
  `deviceDetailTranscriptToMessages(rows, provider, fallbackTs, running = false)`
  与 `deviceSnapshotTranscriptToMessages(rows, provider, fallbackTs, running = false)`,
  在**纯函数内**补占位;`applyDeviceTranscript` / `applyDeviceSnapshotTranscript`
  退化为薄壳只做装配与 `now()` 注入。这样单测直接命中生产代码路径(不再测一份
  影子实装)——计划原案的 `ensureTrailingRunningBubble(bubbles)` 等价保留为
  `ensureRunningTail(messages, ...)`。
- 调用点:`:1117` reconcile 路径不传(缺陷②守卫已挡,且 apply 前再复查一次);
  `:1259` openDeviceSession 传 `!!live.running`;`:1314` handleDeviceSnapshot 传
  `e.status === 'running'`

- [x] **Step 3.4 跑测试确认通过** + transcript 测试文件既有用例全绿。

- [x] **Step 3.5 已知残窗记录**(不修,注释说明):订阅瞬间在途回合的已消费 delta 文本会被不含该回合的快照冲掉,占位保住续流,文本待回合末终态自愈;订阅前在途内容属可接受窗口。

---

### Task 4(仓A,缺陷④): agent 订阅快照「先读→再订→队列下发」

**Files:**
- Modify: `app/http/services/agent_ai_sockets.go:244-304`(emitSessionSnapshot 拆 build/deliver)
- Modify: `app/http/services/agent_remote_ws.go:931-933`(远端)与 `:1135-1137`(本地透传)两处订阅序
- Test: `app/http/services/agent_ai_sockets_test.go`

- [x] **Step 4.1 写失败测试**(随既有测试文件的 manager 构造/journal 注入方式;核心断言):

```go
// 场景1:订阅前已入队的 delta,快照必须排在它之后(FIFO,不再内联抢先)
// 场景2:订阅后才产生的 delta,必须排在快照之后(先读后订序仲裁)
// 两条经 drain s.out 断言载荷序列 [delta, snapshot] / [snapshot, delta]。
```

- [x] **Step 4.2 跑测试确认失败**(现实现内联直写,无队列参与)。

- [x] **Step 4.3 实现**:

```go
// buildSessionSnapshotPayload 组装快照载荷(纯读 journal/审批/状态,不触 socket)。
func (m *agentAIManager) buildSessionSnapshotPayload(sessionID string) map[string]interface{} { ...原构建段整体迁移... }

// enqueueSessionSnapshot 把已构建载荷投进 socket pump 队列,与 delta 同通道
// FIFO 保序(2026-11-09 审计:内联直写可抢在已入队 delta 之前,客户端先收到
// 全 done 快照再收 delta,delta 被 done 屏障吞掉)。socket 不存在(本地半订阅
// 极端场景)回落直写,保持只读握手语义。
func (m *agentAIManager) enqueueSessionSnapshot(socketID string, payload map[string]interface{}, writeJSON agentTerminalWriter) {
	if payload == nil {
		return
	}
	m.mu.Lock()
	s := m.aiSockets[socketID]
	m.mu.Unlock()
	if s != nil && s.alive() {
		s.enqueue(payload)
		return
	}
	if writeJSON != nil {
		_ = writeJSON(payload)
	}
}
```

- **实装与计划签名差异**:落成时 `sessionID` 参数从 `enqueueSessionSnapshot` 移除
  (载荷已构建,该参数只是冗余;`socketID == ""` 的判空已由 `buildSessionSnapshotPayload`
  的 `sessionID == "" → nil` 提前挡住,故 payload==nil 早退即等价)。另加 `s.alive()`
  守卫:注册表里可能残留已关 socket,入队永远送不出去(白丢快照),此时应回落直写。

- 两处订阅序改为(远端 site 用 `agentAIRemoteSocketID`,本地 site 用 `socketID`):

```go
		// 快照先读后订(2026-11-09 审计):journal 读取发生在订阅前——订阅后
		// fanOut 才可能向本 socket 投递 delta,而快照载荷此后才入队,队列 FIFO
		// 保证订阅后的 delta 恒在快照之后;先订后读的旧序里,已入队 delta 会被
		// 晚到的全 done 快照整体替换冲掉(吞流根因,配前端 running 气泡保留)。
		payload := s.ai.buildSessionSnapshotPayload(sessionID)
		s.ai.subscribeAISocket(socketID, sessionID)
		s.ai.enqueueSessionSnapshot(socketID, payload, writeJSON)
```

- `emitSessionSnapshot` 直接删除(grep 确认除两处调用点外无引用),两处调用点同步
  改写;`agent_ai_journal_history.go:4/:86` 的注释同批改名。
- **残余两窗(已写入代码注释,均自愈)**:①构建→订阅之间落进 journal 的 delta
  既不在快照也没订阅到(微秒级,回合末终态快照/详情补齐);②本连接即 origin 时
  delta 走 `fanOutWrite` 内联直写不经队列,可与队列里的快照竞争——该形态前端
  本就有本地 running 气泡,无吞流后果。

- [x] **Step 4.4 跑测试确认通过**:`go test ./app/http/services/ -run 'Snapshot|Socket' -count=1`(worktree 仓A 根)。

- [x] **Step 4.5 全量回归**:`go build ./... && go test ./app/http/services/ -count=1`(预存 flake 若现,按记忆单包复跑判别,不洗白)。

---

### 收尾

- [x] 双仓 `git status` 呈现改动清单,向用户汇报,**等待用户批准后再 commit**(全中文标题「修复:」)。
  → 已获批准并提交:仓A `1bbeea0`(四缺陷主体)+ `7428415`(master 同步合并,冲突二处两侧共存);
  仓B `3e39035`(排序器)+ `45114ae`(main 同步合并)。两仓主分支均已 ff 到位。

**实测证据(2026-11-09 收尾)**:

| 验证项 | 命令 | 结果 |
|---|---|---|
| 仓A Go 编译 | `go build ./...` | exit 0 |
| 仓A Go 定向 | `go test ./app/http/services/ -run 'Snapshot\|Socket' -count=3 -v` | 6/6 PASS(两测试 ×3) |
| 仓A Go 全量 | `go test ./app/http/services/ -count=1` | `ok ... 177.745s`,0 失败(日志 /tmp/services-final.log) |
| 仓A 前端 | `app/website && npx vitest run` | 40 文件 / 431 测试全绿 |
| 仓B 类型 | `npx tsc -p web/tsconfig.json --noEmit` | exit 0 |
| 仓B 前端 | `npx vitest run` | 365 文件 / 3113 测试全绿(3 文件 / 18 测试 skip) |
| 仓B 排序器 | `npx vitest run web/src/aiLineOrder.test.ts` | 12/12 PASS |

**改动规模**:仓A 7 文件 `+294 / −142`;仓B 1 改(`App.tsx`) + 2 新增(`aiLineOrder.ts` / `aiLineOrder.test.ts`)。
