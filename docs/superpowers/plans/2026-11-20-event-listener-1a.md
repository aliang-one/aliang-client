# 事件监听器 1a 实施计划(Event Listener — 功能核心)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地 spec `specs/005-event-listener/spec.md` 的 1a 分期——server 注册表(权威 DB)+ 同步协议 + file_tail 事件源 + 触发注入(复用 dispatchUserAiMessage)+ 通知 fan-out + agent 本地镜像 + LLM 工具面 + 网页侧栏面板。

**Architecture:** server 为注册表权威(全字段落 DB,内存只承载执行现场);agent 持 `listeners.json` 镜像并跑 file_tail watcher,命中上行 `listener.event`,server 判定后渲染 `{{event}}` 经 `dispatchUserAiMessage` 注入统一会话(忙→409 退避重试→放弃置 `error_note=queue_overflow`),`createNotification` fan-out 全设备。LLM 工具面双通道:agentcore 内置桥(照 peer tools 先例)+ claude/codex 走 stdio `aliang-peer` MCP(cmd/mcp.go 描述符)。网页侧栏面板吃 agent 本地代理 REST + 既有 `/api/agent/ai/stream` 事件通道。

**Tech Stack:** Go 1.x(fsnotify v1.7.0 已在 go.mod,转直依)/ TS ESM + Express 4 + zod 3 + better-sqlite3/pg 双库 + Vitest / Vue 3 SFC + JS + Vitest + Tailwind。

**Spec:** `specs/005-event-listener/spec.md`(评审迭代 3 已收敛;字段/契约唯一权威 = spec §1/§1.5,wire 全 snake_case)。

**仓库与工作流铁律:**
- 三仓各自 worktree/分支(仓规:所有开发一律 worktree;并行会话按文件归属切分,禁 add -A):
  - agent:`alianggate` @ `wt/event-listener`(基 master)
  - server:`~/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer` @ `wt/event-listener`
  - web:即 agent 仓 `app/website/`(与 agent 同仓)
- **提交策略(仓规覆盖技能默认):** 每任务末的 commit 步骤**仅在用户当场批准时执行**;未批准则跳过并在收尾汇总「待提交清单」。标题格式「新增:/修复:」全中文,尾行按 CLAUDE.md 归属协议。
- 任务依赖:Task 1→2→3→4→5→6 为 server 链;Task 7→8→9→10→11→14 为 agent 链(9 依赖 4 的上行契约;11 依赖 3);Task 12/13 依赖 3(工具落到 server REST);Task 15→16→17 依赖 11/14。跨仓并行可开,但 server 契约(1/2)先合。

---

## Task 1: Server — zod 契约(listener 域 schema)

**Files:**
- Modify: `server/src/schemas.ts`(追加,勿动既有)
- Test: `server/test/schemas.eventListener.test.ts`(新建)

- [x] **Step 1: 写失败测试**

```ts
// server/test/schemas.eventListener.test.ts
import { describe, expect, it } from 'vitest';
import {
  listenerSourceSchema, listenerCreateSchema, listenerEventUplinkSchema,
} from '../src/schemas';

describe('listener schemas', () => {
  it('接受 file_tail 最小载荷', () => {
    const parsed = listenerCreateSchema.parse({
      title: '盯 build.log',
      source: { type: 'file_tail', device_id: 'dev-1', params: { path: '/a/b.log', match: 'ERROR' } },
      prompt_template: '监听器命中:{{event}}',
      mode: 'recurring',
      target_session_id: 'sess-1',
    });
    expect(parsed.cooldown_ms).toBe(600000); // 默认值
  });
  it('1a 范围:target_session_id 必填(空→新建会话分支延后 1b)', () => {
    expect(() => listenerCreateSchema.parse({
      title: 'x', source: { type: 'file_tail', device_id: 'dev-1', params: { path: '/a', match: 'm' } },
      prompt_template: 'p', mode: 'once',
    })).toThrow();
  });
  it('file_tail 必填 path+match;device_id 必填且非 server', () => {
    expect(() => listenerCreateSchema.parse({
      title: 'x', source: { type: 'file_tail', device_id: 'server', params: {} },
      prompt_template: 'p', mode: 'once',
    })).toThrow();
  });
  it('poll/webhook 的 device_id 固定 server', () => {
    expect(() => listenerCreateSchema.parse({
      title: 'x', source: { type: 'http_poll', device_id: 'dev-1', params: { url: 'https://a' } },
      prompt_template: 'p', mode: 'recurring',
    })).toThrow();
  });
  it('上行 listener.event 契约闭合(detail 按 source 分型,多余字段剥离)', () => {
    const ev = listenerEventUplinkSchema.parse({
      listener_id: 'L-x', fingerprint: 'f'.repeat(64), fired_at: new Date().toISOString(),
      summary: 's'.repeat(200), source_type: 'file_tail',
      detail: { path: '/a', matched_excerpt: 'e', bogus: 1 },
    });
    expect(ev.detail).not.toHaveProperty('bogus'); // z.object 默认 strip
  });
});
```

- [x] **Step 2: 跑测试确认失败** — `cd AliangPhoneServer && npm run test:server -- schemas.eventListener` → FAIL(导出不存在)
- [x] **Step 3: 实现 schema**(schemas.ts 追加;枚举/默认值对齐 spec §1/§1.5:`file_tail|process_exit|http_poll|webhook`;`once|recurring`;`active|paused|fired|expired|error`;cooldown 默认 600000;`matched_excerpt` max 512;`summary` max 200;`fingerprint` 64hex;**1a:`target_session_id` 必填,zod required 承担 422 语义**)
- [x] **Step 4: 跑测试通过** — 同上命令 → PASS
- [x] **Step 5(条件提交):** `git add server/src/schemas.ts server/test/schemas.eventListener.test.ts`,标题「新增:事件监听器 zod 契约(listener 域)」

---

## Task 2: Server — DB 表 + DAO + 类委托(三处定式)

**Files:**
- Modify: `server/src/database.ts`(构造器 schema exec 块 + 委托方法)
- Modify: `server/src/postgresDatabase.ts`(同上,逐字对齐!)
- Create: `server/src/db/eventListeners.ts`(DAO,照 `db/aiOutbox.ts` 范式)
- Test: `server/test/db/eventListeners.db.test.ts`(新建,.db 后缀惯例)

表 DDL(两文件的**构造器内 schema `this.db.exec` 块**逐字追加——仓内无共享 SCHEMA_STATEMENTS 常量;sqlite exec 起于 ~:327,`ai_outbox` 在 database.ts:506):

```sql
CREATE TABLE IF NOT EXISTS event_listeners (
  id TEXT PRIMARY KEY, account_id TEXT NOT NULL, title TEXT NOT NULL,
  source_type TEXT NOT NULL, source_device_id TEXT NOT NULL, source_params TEXT NOT NULL,
  prompt_template TEXT NOT NULL, target_session_id TEXT,
  mode TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active',
  cooldown_ms INTEGER NOT NULL DEFAULT 600000, expires_at TEXT,
  last_fired_at TEXT, fired_count INTEGER NOT NULL DEFAULT 0,
  dedupe_key TEXT, poll_fingerprint TEXT, webhook_token_hash TEXT,
  reject_count INTEGER NOT NULL DEFAULT 0, overflow_count INTEGER NOT NULL DEFAULT 0,
  error_note TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_event_listeners_account ON event_listeners(account_id);
CREATE TABLE IF NOT EXISTS listener_events (
  id TEXT PRIMARY KEY, listener_id TEXT NOT NULL, fired_at TEXT NOT NULL,
  summary TEXT NOT NULL, detail TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_listener_events_listener ON listener_events(listener_id, fired_at);
```

DAO 方法集(照 `aiOutboxSqlite` 签名范式,基元注入):
`insertListener / updateListenerFields(id, fields, expectedUpdatedAt→LWW) / getListener(id) / listListenersByAccount(accountId) / deleteListener(id) / markListenerFired(id, fingerprint, now) / bumpRejectCount(id, now) / bumpOverflowCount(id, now, resetOnSuccess?) / insertListenerEvent / listRecentEvents(listenerId, 20) / sweepExpiredListeners(now, 30d)`。

状态机迁移封装在 DAO 上层(`modules/listeners/state.ts` 新建):`active→fired`(once 命中)/`→expired`(sweep)/`→error`(源消失|reject≥5|overflow≥3),复活=清 reject/overflow/error_note 回 active。

- [x] **Step 1: 失败测试**(内存 sqlite 起 `SQLiteDatabase`,insert→get 往返字段逐项断言;LWW 旧 updated_at 拒写;`markListenerFired` 幂等:同 dedupe_key 二次调用不重复 fired_count;sweep 30 天清 fired/expired 但保留 active)——照 `server/test/db/` 既有 .db.test 范式
- [x] **Step 2: 失败确认** — `npm run test:server -- eventListeners.db`
- [x] **Step 3: 实现** — 两数据库文件构造器 schema exec 块逐字追加 + `db/eventListeners.ts` DAO + 两个类委托方法(照 database.ts:2065 / postgresDatabase.ts:1853 绑定范式);**pg 版本与 sqlite 逐字对齐(仓内硬契约)**
- [x] **Step 4: 通过** → 同命令 PASS
- [x] **Step 5(条件提交):** 「新增:事件监听器表与 DAO(event_listeners/listener_events)」

---

## Task 3: Server — REST CRUD 路由

**Files:**
- Create: `server/src/modules/routes/listeners.ts`
- Modify: `server/src/index.ts`(挂载,照 :208 块 `app.use(listenersRouter)`)
- Test: `server/test/routes/listeners.test.ts`

端点(全走 `requireUserId`;行寻址端点用本地 loadOwnedListener:**404 不存在 / 403 跨账号可区分 + admin bypass**——偏离 requireResourceAccess 的 404 掩码惯例,系任务契约显式要求,Quality 评审已追认为计划内例外):
- `GET /api/listeners`(本账号全量)
- `POST /api/listeners`(listenerCreateSchema.parse;生成 `L-<uuid>`;expires_at 默认 once+24h/recurring+7d;webhook 型留 1b,此处 400 拒绝「not in 1a」)
- `PATCH /api/listeners/:id`(部分更新;bump updated_at;复活 error;**source 可整体替换但 `source.device_id` 不可变——REST 层比对原行强制,不符 409**;**expires_at 入库前归一化为 UTC `Z` 形**——zod 契约只收 Z 形,带时区偏移一律 422)
- `DELETE /api/listeners/:id`
- `GET /api/listeners/:id/events`(最近 20 条)
设备身份路由(agent 侧 REST 调用,Task 10/11 用;均 requireAgentIdentity,照 `modules/agent/routes.ts` 的 `requestDeviceId/requireAgentIdentity` 范式):
- **`GET /api/agent/listeners`**(本 JWT 账号全量——Task 10 `LoadListenerMirror` boot 全量拉取的数据源,缺失则回落本地文件会静默掩盖 FR-3,必须随本任务交付)
- `POST /api/agent/listeners`(source.device_id 强制=JWT 设备)
每个写端点成功后:Task 4 的 `publishToAgent(source_device_id, {type:'listener.upsert', listener})` + 对其余在线设备 `publishToMobiles(userId, {type:'listener.upsert', …})`(本任务先直调 publish,Task 4 完成真实判重)。

- [x] **Step 1: 失败测试**(照 `routes/` 既有范式:supertest 或直接 router 注入;覆盖 401/403/404/422 zod/201 created/200 patch LWW 冲突 409 + 设备路由 GET/POST 两端点各一例 + **PATCH 改 source.device_id → 409(不可变强制,Quality 残留账)**)
- [x] **Step 2: 失败确认** — `npm run test:server -- routes/listeners`
- [x] **Step 3: 实现** — Router + 挂载
- [x] **Step 4: 通过**
- [x] **Step 5(条件提交):** 「新增:事件监听器 REST CRUD(/api/listeners)」

---

## Task 4: Server — 同步协议(上行事件判定 + 下行推镜像)

**Files:**
- Modify: `server/src/modules/agent/handler.ts`(:223 handleAgentMessage 的 type 链加两 case)
- Create: `server/src/modules/listeners/sync.ts`(上行判定 + 下行广播)
- Test: `server/test/listeners/sync.test.ts`

上行 `listener.event`(agent WS,载荷=ListenerEvent 契约 + listener_id/account 由 WS 连接身份定,**不信任 body 的 account**):
判定顺序(全走 DB,无内存权威):
0. **`expires_at` 已过 → 置 `expired` + `createNotification`(一次性到期通知)+ 沿既有广播路径推 `listener.upsert`(status=expired,驱动 agent 停 watcher)+ reject{permanent:true, note:'expired'}**(FR-10 的 active→expired 归属此判定;1a 无 server poller,过期在事件到达时懒判定);
1. 行不存在 → 回 `listener.event.reject{permanent:true, note:'deleted'}`(**无行不计 reject_count**);
2. `status∈{paused,expired,fired}` → reject{permanent:true}, no count(**fired 终态行永不重新武装——新指纹也拒,防 fired_count 膨胀与 dedupe churn;规格评审实弹探针坐实后强制补入,Task 5 侧修复脆弱因 post-claim 行两次 fire 难辨**);
3. `dedupe_key === fingerprint` → reject{permanent:false}(重放,静默);
4. cooldown:`last_fired_at + cooldown_ms > now` → reject{permanent:false};
5. 通过 → Task 5 触发(**判定 3-4 由 Task 2 的 markListenerFired 单语句 CAS claim 收口:dedupe+cooldown 并入 WHERE 卫兵,claimed=false 即拒——并发双击窗口闭合,Quality 残留账**);源设备已注销(设备行不存在)→ reject{permanent:true, note:'device_unregistered'} 且 **bumpRejectCount**,`reject_count>=5` → status=error(error_note='rejected_5x')。

下行广播:CRUD(Task 3)成功后向 `source_device_id` `publishToAgent({type:'listener.upsert'|'listener.remove', listener|id, updated_at})`;agent 断线时不缓存(server 侧无 outbox 必要——agent boot 全量拉取兜底,见 Task 10/11)。

- [x] **Step 1: 失败测试**(fake ws 连接注入 handleAgentMessage;判定链 0-5 各一例,**含 expires_at 过期→expired+一次性通知+下行 upsert**;reject 计数边界 4→5;dedupe 重放不计)
- [x] **Step 2: 失败确认** — `npm run test:server -- listeners/sync`
- [x] **Step 3: 实现**
- [x] **Step 4: 通过**
- [x] **Step 5(条件提交):** 「新增:listener 事件上行判定与下行镜像推送」

---

## Task 5: Server — 触发引擎(注入 + 通知 + 事件摘要)

**Files:**
- Create: `server/src/modules/listeners/trigger.ts`
- Test: `server/test/listeners/trigger.test.ts`

流程(sync.ts 判定通过后调):
1. 写 `listener_events` 行(§1.5 契约:summary+detail JSON);
2. `markListenerFired(id, fingerprint, now)`(once→status=fired;recurring 更新 last_fired_at/fired_count;触发瞬间置态,不等回合完成——spec 边界「并发命中去重」);
3. 渲染 prompt:`prompt_template.replace('{{event}}', JSON.stringify({listener_id,title,source_type,summary,detail}))`(外部内容已由 agent 剥离/截断,server 再做一道控制字符剥离兜底);
4. 注入:复用 `dispatchUserAiMessage(session, input, source, direction, options)`(**target_session_id 的归属校验已在 Task 3 注册时完成:存在+属于本账号,否则 404——dispatch 可信任之,不再复查**)(真实签名:**第三位置参 `source` 携带 `listener:<id>` 标记**;`input` 取 `aiMessageSchema` 形状,content=渲染后 prompt);**`ai_session_busy`/409 → 退避重试 3 次(0s/30s/90s,setTimeout,内存执行现场)**;最终放弃 → `bumpOverflowCount` + `error_note='queue_overflow'`(≥3 连续 → status=error);成功注入 → `overflow_count=0`;
4a. **1a 范围裁定:`target_session_id` 必填**(Task 1 schema enforce,空值 422)。spec §1 的「空 = 命中新建会话」分支延后到 1b/手机计划(见「明确不做」),避免 1a 引入会话创建编排。
5. 通知:`createNotification({type:'listener.triggered', listener_id, title, summary})`(既有持久化+fan-out);
6. **30 天清理接线**:把 Task 2 的 `sweepExpiredListeners(now, 30d)` 挂进 server 既有 sweeper 入口(侦察实录 `sweepers.ts`,实现时 grep sweeper 定位精确挂点),连带 `listener_events` 30 天清理——不留 DAO 死方法;测试:fired_at 31 天前的行被清,active 保留。
agent 离线时 dispatchUserAiMessage 抛 agent_offline → 不算 overflow(源侧暂存重放兜底),事件保留 listener_events 行,`error_note='agent_offline'`,通知照发。

- [x] **Step 1: 失败测试**(mock dispatchUserAiMessage:成功/忙×3/agent_offline/once 置 fired 时机/overflow 计数与 error 升级/成功清零/空 target_session_id→422)
- [x] **Step 2: 失败确认** — `npm run test:server -- listeners/trigger`
- [x] **Step 3: 实现**
- [x] **Step 4: 通过**
- [x] **Step 5(条件提交):** 「新增:listener 触发引擎(dispatch 409 退避+queue_overflow+通知)」

---

## Task 6: Server — 重启零丢失验收测试

**Files:**
- Test: `server/test/listeners/restartRecovery.test.ts`(集成,内存 DB)

- [x] **Step 1: 写测试**(顺带补 Task 4 评审指出的两处低概率路径:claim 后行被删的 logger.warn 路径、DB 不可用抛错路径;spec 故事 2 验收场景逐条:seed 2 行(其一已触发,**expires_at 置未来**避免过期分支抢跑)→ 重建 database 实例(模拟重启)→ 逐字段断言含 overflow_count;冷却中同 fingerprint 二次事件被拒;webhook 断言留 1b 注释占位)
- [x] **Step 2: 失败确认**(应直接 PASS——本任务是把既有行为钉死的特征测试;若有 FAIL 即 Task 2-5 有内存态泄漏,修到绿)
- [x] **Step 3: 通过 + 条件提交:**「测试:listener 重启零丢失特征测试」

---

## Task 7: Agent — 本地镜像 store(listeners.json)

**Files:**
- Create: `app/http/services/agent_listener_store.go`(类型 ListenerMirror + 读写)
- Test: `app/http/services/agent_listener_store_test.go`

- 照 `saveAliangMCPRegistryRoot`(agent_mcp_registry.go:256)范式:结构体 → marshal → byte-equal 幂等跳过 → `MkdirAll(agentHome()/.aliang)` → temp+rename;
- 类型:`ListenerMirror{ID, AccountID, Title, SourceType, SourceDeviceID, SourceParams json.RawMessage, PromptTemplate, TargetSessionID, Mode, Status, CooldownMs, ExpiresAt, LastFiredAt, FiredCount, DedupeKey, OverflowCount, ErrorNote, CreatedBy, CreatedAt, UpdatedAt}`(snake_case json tag,与 spec §1 一致);`updatedAtLWW(new, old)` 仲裁函数;
- 并发:`sync.RWMutex` + 单写者;读侧 `ListMirrors()/GetMirror(id)`。

- [x] **Step 1: 失败测试**(save 幂等 byte-equal;LWW 旧拒新收;损坏 JSON 容错=返回空表+log 不 panic)
- [x] **Step 2: 失败确认** — `go test ./app/http/services/ -run TestListenerMirrorStore`
- [x] **Step 3: 实现**
- [x] **Step 4: 通过**
- [x] **Step 5(条件提交):** 「新增:agent 监听器本地镜像 store(listeners.json)」

---

## Task 8: Agent — WS 下行 listener.upsert/remove

**Files:**
- Modify: `app/http/models/agent_protocol.go`(常量 `AgentEventListenerUpsert = "listener.upsert"`, `AgentEventListenerRemove = "listener.remove"`, `AgentEventListenerEvent = "listener.event"`, `AgentEventListenerEventReject = "listener.event.reject"`)
- Modify: `app/http/services/agent_remote_ws.go`(handleRemoteAgentMessage 的 switch 加两 case;**注册表通用分支照 default 范式亦可,但此两帧需要稳定处理,直接加 case**)
- Create: `app/http/services/agent_listener_sync.go`(case 处理:LWW 后写镜像;remove 删镜像+停对应 watcher)
- Test: `app/http/services/agent_listener_sync_test.go`

- [x] **Step 1: 失败测试**(upsert 新增/更新乱序旧 updated_at 被拒/remove 停 watcher;handleRemoteAgentMessage 直调注入)
- [x] **Step 2: 失败确认** — `go test ./app/http/services/ -run TestListenerRemoteSync`
- [x] **Step 3: 实现**
- [x] **Step 4: 通过**
- [x] **Step 5(条件提交):** 「新增:agent 侧 listener.upsert/remove 下行处理与镜像同步」

---

## Task 9: Agent — file_tail watcher

**Files:**
- Create: `app/http/services/agent_listener_filetail.go`(watcher 管理 + tail 读 + 上行)
- Modify: `go.mod`(fsnotify v1.7.0 indirect → direct)
- Test: `app/http/services/agent_listener_filetail_test.go`

设计(照 journal offset 先例 agent_ai_agentcore_journal.go:142 的 per-key 小文件 + 原子写):
- `StartEventListenerWatchers()` — 遍历镜像中 `source_type=file_tail && status=active && source_device_id==本机 deviceID` 的行,每个起一个 goroutine(fsnotify watch + 起跑即从 offset 追读);
- offset 状态:`<agentHome()>/.aliang/listener-tails/<safeName(path)>.offset`(照 `agentCoreJournalFileName` 白名单 safe-name);
- 追读循环:文件增长→按行读→逐行 match(关键字或正则,`SourceParams.match`)→命中即:
  - `fingerprint = sha256(matched_line)`;
  - 构造 ListenerEvent(summary=截 200,detail{path, matched_excerpt 截 512 已剥控制字符});
  - 上行:断联时入**本地落盘暂存队列** `<agentHome()>/.aliang/listener-outbox.jsonl`(上限 50,溢出丢最旧+记摘要行),重连(`runRemoteAgentSession` 的注册成功钩子)后按序重放;
  - 收到 `listener.event.reject{permanent:true, note∈{deleted,paused,device_unregistered}}` → `bumpRejectCount`(本地镜像计数同步);≥5 → 镜像行置 error;
  - `note='expired'` 特例:**直接停 watcher、镜像置 expired、不计数**(server 判定 0 已推 `listener.upsert` 同步,此处仅乱序兜底,防过期后无谓上行死循环);
  - `note='fired'` 同款特例:**直接停 watcher、镜像置 fired、不计数**(once 命中即终态,Task 5 触发时应推 fired upsert;此为乱序/漏推兜底);
  - **WS 断联/发送失败不计数**(spec FR-6);
- 文件截断/轮转:size < offset → offset=0 重读;文件消失 → 上行 `listener.event.reject` 语义改为本地置 error(error_note='source_gone')+ 停 watcher;
- watcher 停止:`StopEventListenerWatchers()`(测试用);镜像 remove/upsert(status 或 source.params 变更)驱动 watcher 增删——**参数变更也必须重启 watcher**(Quality 残留账)。
- **Task 8 评审注记**:①hook 只收新镜像,「status/params 变更」判定用 watcher 管理器自己在册的启动参数做 diff,勿试图反推旧镜像;②空 `updated_at` 的行会过 LWW 落地——启 watcher 前须校验行有效性;③Task 8 已在帧处理处内置 panic 护栏(recover→Warn),hook panic 不会撕断读循环。

- [x] **Step 1: 失败测试**(临时目录造文件:追加命中行→断言上行帧被 fake writer 捕获;不命中不触发;轮转重读;断联入队+重连重放;reject×5 置 error;断联不计 reject)
- [x] **Step 2: 失败确认** — `go test ./app/http/services/ -run TestListenerFileTail`
- [x] **Step 3: 实现**
- [x] **Step 4: 通过** + `go build ./...` 全仓绿
- [x] **Step 5(条件提交):** 「新增:file_tail 事件源 watcher(fsnotify+断点续读+断联暂存重放)」

---

## Task 10: Agent — boot 接线 + 审计测试扩展

**Files:**
- Create: `app/http/services/agent_listener_boot.go`(`LoadListenerMirror()` = 全量拉取优先(**boot 全量镜像含同账号外设备行——spawn 闸已在 Task 9 的 syncListenerWatcherForMirror 按 source_device_id==本机 过滤,boot 路径自动受保护;镜像保留全量供 UI 展示**——**落定时合并 loadListenerMirrors/listListenerMirrors 双读口为一个(Task 7 评审残留账)**:调 server `GET /api/agent/listeners`(Task 3 设备路由)写镜像,失败回落本地文件;`StartEventListenerWatchers()` 薄包装)
- Modify: ~~`cmd/start.go` / `cmd/core.go`~~ **接线归属定案(Task 10 评审 C1,2026-11-21):监听器镜像拉取与 watcher 归 WS 持有进程 = user-agent 运行时——`app/agentruntime/server.go` StartLocalServer(与 StartAgentRegistrationKeepalive 同点)加两行;cmd/start.go 与 cmd/core.go 不接**(其 EnsureRemoteConnection 是跨进程转发;分裂身份/共享 home 拓扑下双进程共写 offset 文件会丢事件)。audit 拆两表:mesh 三件套仍查 start/core;listener 两符号查 agentruntime/server.go 恰一次。Task 11 的 cloud client 与 remoteConnectionSnapshot 沿用同一所有权;本机代理路由走既有双注册转发惯例。
- Modify: `cmd/boot_wiring_audit_test.go`(mesh 三件套符号表还原;**新增第二张表**查 `../app/agentruntime/server.go` 的两 listener 符号恰一次——**仓规:先扩表后加调用,同一提交**)
- Test: 既有 audit 测试自动覆盖;新增 `TestListenerBootOrder`(拉镜像失败不阻塞 boot,log 降级)

- [x] **Step 1: 先扩 audit symbols + 失败确认**(`go test ./cmd/ -run TestBootMeshWiringAudit` → FAIL 报 0 出现)
- [x] **Step 2: 实现两文件接线**(顺序:**Load 在前、Start 在后**,1b8c36b 教训)
- [x] **Step 3: audit PASS + `go test ./cmd/` 全绿**
- [x] **Step 4(条件提交):** 「新增:listener 镜像与 watcher 进 boot 接线(双入口+审计)」

---

## Task 11: Agent — REST 代理(/api/listeners*)+ server HTTP 客户端缝

**Files:**
- Create: `app/http/services/agent_listener_api.go`(agent→cloud HTTP client + 本地代理转发)
- Modify: `app/http/routes/routes.go`(挂本地路由)
- Test: `app/http/services/agent_listener_api_test.go`

- cloud 调用:auth 材料 = `remoteConnectionSnapshot()`(agent_remote_ws.go:1255,给 authorization+deviceID);base URL 复用 `EnsureRemoteConnection` 的 endpoint 解析(实现时从 :81 顺链取,抽 `listenerCloudBaseURL()` 小函数,失败返回错误不 panic);
- 本地端点(仪表盘用,透传语义):
  - `GET /api/listeners` → cloud GET /api/listeners
  - `POST /api/listeners` → cloud POST(**source.device_id 强制覆盖为本机 deviceID**,file/process 型)
  - `PATCH/DELETE /api/listeners/:id`、`GET /api/listeners/:id/events` 同理
- 断联语义:cloud 不可达 → 502 `{code:'listener_cloud_offline'}`(前端显示离线徽标,不静默假成功);
- 注入工具(Task 12/13)也走此 client(POST /api/agent/listeners)。

- [x] **Step 1: 失败测试**(httptest 假 cloud:透传 header 断言、device_id 覆盖断言、断联 502)
- [x] **Step 2: 失败确认** — `go test ./app/http/services/ -run TestListenerProxyAPI`(⚠️ 必须 `./app/http/services/`——`./app/http/` 包内无测试文件会空跑假绿,仓内已有此陷阱前科)
- [x] **Step 3: 实现**
- [x] **Step 4: 通过**
- [x] **Step 5(条件提交):** 「新增:监听器本地 REST 代理与 cloud 客户端缝」

---

## Task 12: Agent — LLM 工具桥(agentcore 内置面)

**Files:**
- Create: `app/http/services/agent_listener_tools.go`(4 工具的共享 handler:校验参数→调 Task 11 client→返回结果+审批模式警示)
- Modify: `app/http/services/agent_ai_agentcore.go`(照 `agentCorePeerTools` :1126 注入范式,`runAgentCore` 内 registry.Register 四枚桥;gating 沿用既有开关语义,新增总开关 `EffectiveEventListenersEnabled` 默认开)
- Test: `app/http/services/agent_listener_tools_test.go`

工具 schema(spec §5):
- `listener_create(title, source{type,device_id?,params}, prompt_template, mode, target_session_id?)` — 返回 `{id, status}` + `approval_warning`;**created_by 沿 device 面惯例记 `user:<deviceId>`;`ai:<session_id>` 溯源正式延 1b——Task 12 已具备 rc.sessionID 但 created_by 是 server 端从 JWT 生成而非客户端可传,补溯源需 server 契约变更(§1 枚举扩展+设备面透传),非客户端单侧可解**(Quality 残留账转正式 1b 项)(当 target 会话 approvalMode=require 时附警示文案);**target_session_id 缺省时 handler 回填当前会话 id**(1a server schema 必填——工具在会话内运行,缺省回填是常见调用形态);
- `listener_list()` / `listener_update(id, ...)` / `listener_delete(id)`。

- [x] **Step 1: 失败测试**(handler 校验:file_tail 强制本机 device_id、~~params schema~~ **params 内容校验刻意省略——server 422 是 schema 唯一权威,本地不建第二 schema 源(Task 12 评审 D1 确认 intentional,一次往返换零漂移)**;create 返回审批警示(词表勘误:require 非会话模式,警示对准 ask_all/read_only);target_session_id 缺省回填当前会话;fake cloud 走 Task 11 缝)
- [x] **Step 2: 失败确认** — `go test ./app/http/services/ -run TestListenerTools`
- [x] **Step 3: 实现**(桥类型照 `agentCorePeerToolBridge` 字段:name/description/schema/hint/execute/journal)
- [x] **Step 4: 通过**
- [x] **Step 5(条件提交):** 「新增:监听器 agentcore 工具桥四枚」

---

## Task 13: Agent — stdio MCP 面(claude/codex)

**Files:**
- Modify: `cmd/mcp.go`(peerMCPTools + peerMCPToolDescriptors 加四工具,照既有六工具组范式)
- Modify: `app/http/services/agent_peer_mcp.go`(`PeerMCPContractSpecs()` 加一行 spec:CoreName/StdioName/Required/Optional/Keywords)
- Test: `cmd/mcp_contract_test.go` 自动覆盖(契约双描述符一致);新增 `cmd/mcp_listener_tools_test.go`(tools/list 含四枚;tools/call 转发 Task 12 handler)

⚠️ 三内核支持矩阵:本批不动 claude/codex 的 MCP 配置生成(claudeTierMCPArgs / EnsureAliangMCPCodexMatchesRegistry 均自动带上,因走同一 registry);`agentToolRegistryRev` **不 bump**(云注册表未加工具——1b 若上云工具面再 bump)。

- [ ] **Step 1: 失败测试**(tools/list 断言四枚在列;call listener_list 走 fake 缝返回)
- [ ] **Step 2: 失败确认** — `go test ./cmd/ -run TestMCPListenerTools`;`go test ./cmd/ -run TestMCPContract` 应仍绿(契约一致)
- [ ] **Step 3: 实现**(双描述符逐字对齐 + spec 行)
- [ ] **Step 4: 通过**
- [ ] **Step 5(条件提交):** 「新增:监听器 stdio MCP 工具面(claude/codex)+契约行」

---

## Task 14: Agent — listener.triggered fan-out 到网页 socket

**Files:**
- Modify: `app/http/services/agent_remote_ws.go`(下行 switch 加 case `listener.triggered` → 调 agentAIManager 广播)
- Modify: `app/http/services/agent_ai_sockets.go`(新方法 `BroadcastListenerEvent(payload)`:向全部已注册 aiSocket enqueue,**不要求会话订阅**——监听器是账号级事件)
- Test: `app/http/services/agent_listener_broadcast_test.go`

- [x] **Step 1: 失败测试**(两个 fake socket 注册→注入下行帧→两者都收到;无订阅会话也收到)
- [x] **Step 2: 失败确认** — `go test ./app/http/services/ -run TestListenerBroadcast`
- [x] **Step 3: 实现**
- [x] **Step 4: 通过**
- [x] **Step 5(条件提交):** 「新增:listener.triggered 账号级 fan-out 到仪表盘 socket」

---

## Task 15: Web — API client + i18n

**Files:**
- Modify: `app/website/src/services/agentApi.js`(加 `getListeners/listenerCreate/listenerPatch/listenerDelete/getListenerEvents`,照 `getAgentRuntimeSessions` 范式,信封 `{code,msg,data}`)
- Modify: `app/website/src/i18n/zh.js` + `en.js`(`listener_*` 前缀:entryTitle='监听器'、panelTitle、empty='暂无监听器,在对话里对 AI 说「盯住…」即可创建。'、status_active/fired/expired/error/paused、triggeredBadge、approvalWarning、cloudOffline 等;中文全角标点+「」)
- Test: `app/website/src/utils/listenerFormat.test.js`(纯函数:`listenerStatusBadge(status)`、`formatListenerRow(row)`——抽出可测)

- [x] **Step 1: 失败测试**(纯函数:状态映射、相对时间、fired_count 显示)
- [x] **Step 2: 失败确认** — `cd app/website && npx vitest run src/utils/listenerFormat.test.js`
- [x] **Step 3: 实现**
- [x] **Step 4: 通过**
- [x] **Step 5(条件提交):** 「新增:监听器面板 API client 与文案键(listener_*)」

---

## Task 16: Web — 侧栏入口 + 监听器面板

**Files:**
- Create: `app/website/src/components/settings/ListenerSettings.vue`(面板,照 `AgentRuntimeSessions.vue` 范式:loadList+可见轮询+`showErrorDialog`;列表行=status 徽标+title+last_fired 相对时间+fired_count+error_note;error 行复活按钮=listenerPatch(id,{status:'active'});触发事件摘要最近 20 条折叠区)
- Modify: `app/website/src/components/DashboardPage.vue`(桌面快捷工具区 :149-183 加按钮 `data-testid="listener-entry-sidebar"`;移动镜像 :336-368 加 `data-testid="listener-entry-panel"`;点击开面板——**实现定案:页内 overlay modal(`listenerPanelOpen` ref + useEscLayer 同栈),非页导航——照 ScanLoginPanel/tun-start-modal 既有 refs 惯例,`aliang-chat-entry-panel` 引用系计划笔误(Task 16 评审已核)**,active 徽标数从 getListeners 轮询取。**C1 定案(Task 16 评审,2026-11-21):代理 2xx 透传必须包 `common.Success` 信封(浏览器 request() 解信封,裸 JSON 会静默变 null=永久空面板假绿),非 2xx 包 `{code:status, msg:detail}`;web 侧 API client 拆内层形状。Task 17 交接缝 = window CustomEvent(`aliang:tun-progress-*` 惯例),Task 16 面板 mounted/beforeUnmount 挂窗监听**)
- Test: `app/website/src/components/settings/ListenerSettings.test.js`(头 `vi.mock('../../i18n', …)`,只测导出纯函数+可选 mount 冒烟;DashboardPage 侧只断言 data-testid 存在,不深 mount——照仓内先例粒度)

- [x] **Step 1: 失败测试**
- [x] **Step 2: 失败确认** — `npx vitest run src/components/settings/ListenerSettings.test.js`
- [x] **Step 3: 实现**(桌面+移动两处入口都要,**漏移动端=评审必打回**)
- [x] **Step 4: 通过** + `npx vitest run` 全量绿(基线 728+)
- [x] **Step 5(条件提交):** 「新增:仪表盘监听器面板与侧栏入口(桌面+移动)」

---

## Task 17: Web — 触发通知消费(handleAIEvent case)

**Files:**
- Modify: `app/website/src/composables/useAliangChat.js`(`handleAIEvent` :3043 的早退 if 链加 `if (e.type === 'listener.triggered')` 分支:toast「监听器「{title}」已触发」+ 触发 ListenerSettings 刷新钩子;词表 :95 注释同步)
- Test: `app/website/src/composables/useAliangChat.listenerEvent.test.js`(纯 handler 注入断言;照该文件既有测试拆分粒度)

- [x] **Step 1: 失败测试**
- [x] **Step 2: 失败确认** — `npx vitest run src/composables/useAliangChat.listenerEvent.test.js`
- [x] **Step 3: 实现**
- [x] **Step 4: 通过** + 全量 vitest 绿
- [x] **Step 5(条件提交):** 「新增:仪表盘监听触发 toast 与面板联动」

---

## Task 18: 收尾验证 + 提交汇总

- [x] **Step 1: agent 仓全量** — `go build ./... && go test ./...`(`services` 全包唯一失败=TestSteerReviveViaMessageRestoresSession t.TempDir 预存 flake,隔离 3/3 绿;其余 cmd/agentruntime/routes/handlers/config 全绿)(预期:除仓内已知预存 flake 外全绿;新测试零 flake)
- [x] **Step 2: server 仓全量** — `npm run test:server`(基线 83+ 文件全绿)
- [x] **Step 3: web 全量** — `npx vitest run`(728+ 全绿)
- [x] **Step 4: 端到端冒烟(本地)** — **SMOKE_PASS**:server tsx boot(sqlite 隔离)+ 活体 WS 代理客户端(帧形逐字节照 filetail.go emit)→ 设备注册→建目标会话→设备面建监听(201,device_id 强制覆写实证)→ boot 镜像拉取含行→追匹配行→上行接受(claim-CAS 触发)→ `ai.message` 注入回合到达(`[listener:L-*]` 标记+渲染 JSON)→ DB 断言(events 1 行/notifications listener.triggered/fired_count=1 recurring 语义)→ 同指纹重放 reject{permanent:false} 无 counted。零 server 侧缺陷;覆盖缺口=local-agent.ts 不含 listener 协议(已记台账);预存 flake 台账见 Task 18 Step 1/2 结果 — server `npm run agent:local` + agent 起跑 → 手工 curl 建 file_tail 监听 → 追加命中行 → 断言:注入回合出现(mock 模式)、通知落库、网页面板可见
- [x] **Step 5: 待提交清单汇总给用户**(见下方「待提交清单」节)(三仓各自文件归属分组,禁 add -A;**全部提交须用户逐批批准**)
- [x] **Step 6: 真机冒烟清单写入 spec 附录**(出包后跑——1a 不含出包):
  1. 双设备创建(A 设备对话/手机侧栏)→ B 设备面板可见
  2. file_tail 命中 → 注入回合 + 手机侧栏徽标 + 云端面板 toast 中继(Step 6b 专项)
  3. 审批警示:require 会话注册 → 触发 → 审批卡出现/超时判死通知
  4. 断联暂存:杀 server → 追加命中行 → 重启 → 重放不重复触发
  5. 杀总开关:event_listeners.enable=false → agentcore 面+stdio 面 tools/list 均无 listener_*
  6. once 24h 过期通知;recurring 7d 过期通知
  7. cooldown 内重放不触发(重启后冷却照旧)
  8. ** Step 6b 云端面板 toast 中继**:server 对 agent→server 面板的 listener.triggered 中继(本地面板走 agent fan-out 必达,云端面板依赖此中继——Task 14 评审注记)

## 待提交清单(Task 18 Step 5 汇总,等用户逐仓批准)

**agent 仓(wt/event-listener)**: 建议按 Task 边界分 4-5 个提交——①store+sync+filetail+boot 接线(T7-10)②REST 代理+cloud client(T11)③agentcore+stdio 工具面(T12-13)④fan-out(T14)⑤web 面板+toast(T15-17);spec/plan 文档随首提交。**server 仓(wt/event-listener)**: 建议按 Task 边界分 3 个提交——①zod+DB DAO(T1-2)②REST+sync+trigger+sweeper 接线(T3-6)③listener.triggered agent 下行帧(T5 补丁)。**全部等用户显式批准后提交,禁 add -A。**
- [ ] **Step 6b(评审补):云端面板 toast 中继验证**——本地面板走 agent fan-out 必达;云端面板(连 server)依赖 server 对 agent 上行/面板通道的中继,冒烟时单独验证(Task 14 评审注记)

## 明确不做(1a 范围外)

- target_session_id 为空的「命中新建会话」分支(spec §1 语义;1a 在 schema 强制必填,1b/手机计划放开)
- process_exit / http_poll / webhook 三源(spec 1b;server 侧 webhook 端点与 DB 列留空,1b 补)
- 手机 RN 侧栏面板(消费同一 REST,另立计划 `2026-11-20-event-listener-1a-phone.md`)
- 手机端 RN 通知原生动效(1a 只落 server createNotification,手机端消费另立)
- agent 断联本地降级直注(TODOS)

## 已知风险与对策(侦察实证)

| 风险 | 对策 |
|---|---|
| dispatchUserAiMessage 忙=409 非排队 | Task 5 退避重试 3 次+overflow 计数,语义=spec queue_overflow |
| pg/sqlite schema 漂移 | Task 2 两文件逐字对齐+.db 测试双跑(仓内硬契约) |
| boot 接线遗漏 | Task 10 先扩 audit 表再加调用(1b8c36b 教训制度化) |
| reject 计数误伤(断联) | Task 9 测试显式覆盖「断联不计数」分界(FR-6) |
| 云注册表 rev 漂移 | Task 13 不动 agentToolRegistryRev;1b 上云工具面再 bump=7 |
| 手机端未在 1a | spec 故事 3 的手机展示延后,server fan-out 已就位;计划文档明示分期边界 |
