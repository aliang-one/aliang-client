# 功能规范: 事件监听器——后台监听 → 触发会话任务

**功能分支**: `005-event-listener`
**创建时间**: 2026-11-20
**状态**: 草稿(评审迭代 3,遗留建议已清)
**输入**: 用户描述: "用户说给我监听某个事情,有结果了就触发我们这边的某个任务。另外:注册的监听事件要能展示出来,不只网页上,手机侧边栏也要展示,所以数据本地和服务端都要有;事件监听和通知要跨设备;这些特性要暴露到 LLM 中让 AI 都知道。监听注册表必须在服务端持久化(数据库),不能放内存,服务端重启不能丢状态。触发语义参考 Claude Code。"

**涉及仓库**: 本 spec 落 alianggate(agent 侧);服务端权威面落 PhoneServer 仓;手机侧栏展示落 app 仓。三仓契约字段以 §1/§1.5 为唯一权威。

## 用户场景与测试 *(必填)*

### 用户故事 1 - 对话中一句话创建监听 (优先级: P1)

用户在任意设备(手机/网页/桌面)的对话里对 AI 说:「盯住 `build.log`,出现 `ERROR` 就告诉我处理方案」。AI 调用 `listener_create` 工具注册监听器;监听器立即出现在该账号全部设备的侧边栏监听面板中。

**优先级原因**: 这是本功能的入口旅程,不打通则后续一切无从谈起。

**独立测试**: 对话中注册一个 file_tail 监听 → 手机侧栏与网页侧栏均出现该监听器 → 往目标文件追加含 `ERROR` 的行 → AI 在原会话以新回合醒来并处理。

**验收场景**:
- 给定 会话 approvalMode 为 bypass,当 用户说「盯住 build.log 的 ERROR」且 AI 调 listener_create,那么 工具返回注册成功,双端侧栏 10s 内可见该监听器,状态 active;
- 给定 监听器 active,当 目标文件追加一行含 ERROR,那么 60s 内原会话出现带 `listener:<id>` 标记的新回合,prompt 含匹配行内容。

### 用户故事 2 - 服务端重启零丢失 (优先级: P1)

服务端重启(发布/崩溃)后:全部监听器仍在,cooldown 不被重置,不会对已触发过的事件重复触发,webhook 回调 URL 继续有效,http_poll 从上次内容指纹继续比对。

**优先级原因**: 用户点名硬要求。内存态丢失 = 静默丢监听或重复轰炸,二者都不可接受。

**独立测试**: 注册 webhook + http_poll 监听各一 → 触发一次 → 重启服务端 → 列表完整、`last_fired_at`/`fired_count`/`dedupe_key` 原值、旧 webhook URL 仍可回调生效。

**验收场景**:
- 给定 服务端运行中有 2 个 active 监听器(其一已触发过),当 kill -9 服务端并重启,那么 boot 后监听器数量/状态/last_fired_at/fired_count/dedupe_key/poll_fingerprint/overflow_count 与重启前逐字段一致,且旧 webhook token/URL 重启后仍有效——发送方重试的回调恢复正常处理;
- 给定 recurring 监听器冷却中(距 last_fired_at < cooldown),当 重启后同一 fingerprint 事件再次到达,那么 拒绝触发(冷却照旧,不因重启清零)。

### 用户故事 3 - 跨设备可见与通知 (优先级: P1)

监听器在 A 设备创建,命中时:B 设备(手机侧栏)收到「监听器触发」轻通知并出现徽标;触发的 AI 回合在统一会话里,任何设备打开都能看到 AI 醒来干活的全过程。

**优先级原因**: alianggate 是跨设备产品,这是本功能与 Claude Code 原生 cron 的关键差异点。

**独立测试**: 手机创建监听 → 桌面网页命中 → 手机侧栏出现触发徽标,点开统一会话可见注入回合与 AI 后续动作。

**验收场景**:
- 给定 账号下手机与桌面均在线,当 监听器命中,那么 两侧在 5s 内都收到轻通知;手机侧栏监听面板出现触发徽标,点击进入统一会话可见注入回合;
- 给定 B 设备当时离线,当 其重新上线,那么 侧栏监听面板从 server 拉到含该次触发的列表(事件摘要 ≤20 条含本次)。

### 用户故事 4 - 四类事件源 (优先级: P2)

file_tail(本机文件/日志)、process_exit(本机命令/进程退出)、http_poll(server 轮询 URL)、webhook(server 回调端点)四类源按 §3 各自独立可用。

**优先级原因**: 覆盖「盯日志」「等任务跑完」「盯外部页面」「外部系统主动通知」四类真实诉求;每类可独立交付。

**独立测试**: 每类源各一条注册→注入→收口的最小链路单测 + 真机冒烟清单。

**验收场景**:
- 给定 file_tail 监听 active,当 文件被轮转(截断重写),那么 offset 重置从头读,不丢轮转后首条匹配;
- 给定 process_exit 监听以 expect_code=0 注册代启命令,当 命令以 0 退出,那么 触发;以非 0 退出,那么 不触发且事件记 exit_code;
- 给定 http_poll 监听 match=指纹变化,当 URL 内容变化,那么 触发一次;内容不变,那么 不触发;
- 给定 webhook 监听,当 持正确 token 的 POST 到达( body ≤64KB ),那么 触发;token 错误,那么 401 且不触发、只记审计。

### 用户故事 5 - AI 全量感知 (优先级: P2)

AI 通过 4 个工具(listener_create/list/update/delete)管理监听;事件命中时以渲染后的 prompt 醒来,prompt 自带来源标记与事件内容,AI 无需额外查询即知道「什么监听器命中了、发生了什么」。

**优先级原因**: 用户点名「暴露到 LLM 中」。AI 是触发后的执行者,感知不完整则行为不可信。

**独立测试**: 注入回合 prompt 含 `listener:<id>` 标记与 `{{event}}` 渲染结果;listener_list 返回与侧栏展示一致。

**验收场景**:
- 给定 任意命中,当 注入回合进入会话,那么 prompt 首段为监听器 id/标题/源类型/事件摘要(§1.5 契约字段),AI 后续动作无需再调 listener_list 即可开始处理;
- 给定 AI 调 listener_list,当 返回,那么 字段与侧栏面板展示同一来源(server 契约),无两套口径。

### 边界情况

- **WS 断联(事件源在 agent)**:断联期事件继续采集,入本地落盘暂存队列(上限 50 条,溢出丢最旧并记一条摘要),重连后按序重放;server 以 `dedupe_key` 幂等。**断联/发送失败不计数进 error 判据**(见 §2 上报失败语义)——长断联不得把监听器打成 error;
- **once 过期**:once 监听默认 24h 未命中即过期:置 `expired`、通知一次;注册表行保留 30 天后由 sweeper 统一清理(见下条),过期瞬间仅从活动面移除(注册时可用 `expires_at` 覆盖);
- **fired/expired 行清理**:`fired`/`expired` 行及其 `listener_events` 保留 30 天,由 server 既有 sweeper 统一清理;
- **忙会话排队**:注入走统一会话回合队列(沿用既有上限);队列溢出时事件不静默丢——监听器保持 `active` 并带 `error_note=queue_overflow`,连溢计数落库(`overflow_count`,成功注入即清零、重启保留,按 §1 铁律不驻内存)、向全部设备推通知,由用户/AI 决定重放;`overflow_count` 达 3 才置 `error`(可复活);
- **并发命中去重**:同一监听器在冷却窗口内多个事件只触发一次,`dedupe_key` 取最新 fingerprint;once 监听在触发瞬间即置 `fired`(不等回合完成),队列等待期内重复命中一律拒绝;
- **文件轮转/删除**:file_tail 检测到截断重置 offset;文件被删置 `error`(源消失),AI/用户可复活;
- **process 源与 agent 重启**:挂 pid 的监听在 agent 重启后无法重建(进程句柄失联),置 `error(error_note=agent_restarted)`;代启型监听同理不自动重跑,避免重启风暴;
- **http_poll 失败分级**:连续 5 次网络错/超时/5xx → `error`;4xx(404/401)立即 `error`(目标已死重试无意义);
- **webhook token**:长效 token 哈希落库,重启不失效;once 触发后回调返回 410 Gone;token 错误只 401+审计,不影响监听器状态;
- **外部内容注入安全**:webhook/http_poll 的外部内容一律视为数据:注入 `{{event}}` 前剥离控制字符、截断至 512 字符、以 JSON 字符串字面量嵌入模板(不参与模板解释);prompt 模板本身只来自创建时的用户/AI 输入。

## 需求 *(必填)*

### 功能需求

- **FR-1** 服务端注册表持久化:监听器全部字段(含 cooldown/last_fired_at/fired_count/dedupe_key/poll_fingerprint/webhook_token_hash/status/updated_at/overflow_count)落 server 数据库;内存只承载执行现场,不承载权威状态;
- **FR-2** 服务端 boot 重建:boot 时从 DB 重建全部 http_poll 定时器与 webhook 端点路由;重建完整性有接线审计测试;
- **FR-3** agent 本地镜像:agent 落盘 `listeners.json`(同构字段),供本机 UI/CLI 展示与离线只读;boot 先拉全量镜像、后启动 watcher(接线审计测试);
- **FR-4** 同步协议:CRUD 走 server API;下行 `listener.upsert/remove` 推镜像;乱序以 `updated_at` 单调字段最后写胜出;
- **FR-5** 事件上行与幂等:agent 命中 → WS 上行 `listener.event`,载荷 = §1.5 ListenerEvent 契约原样(无信封包装);server 判 status/cooldown/dedupe 后触发;断联暂存重放不产生重复触发;
- **FR-6** 上报失败语义:agent 侧源只有 server 永久拒绝(`reject{permanent:true}`)累计 5 次才置 error——计数场景仅限行存活时的永久拒绝(如源设备已注销);行已删除的拒绝无行可计、不计数;WS 断联/暂存不计入;server 侧 http_poll 按 §3 失败分级;webhook 校验失败不置 error;
- **FR-7** 触发注入:命中 → 渲染 `{{event}}` → 注入统一会话回合队列(空闲立即、忙排队、不并跑、不抢占),回合带 `listener:<id>` 来源标记;
- **FR-8** 跨设备通知:命中瞬间轻通知 fan-out 全部在线设备;离线设备上线后可从 server 拉到事件摘要(最近 20 条);
- **FR-9** LLM 工具面:listener_create/list/update/delete 四工具挂进 agent 工具注册表(claude/codex 走各自 MCP 配置,agentcore 走内置桥);create 返回含当前会话审批模式警示;
- **FR-10** 生命周期:once 命中即置 fired(触发瞬间,非回合完成时);recurring 默认 7 天过期;once 默认 24h 过期;fired/expired 行与事件摘要 30 天 sweeper 清理;
- **FR-11** 侧栏展示:手机侧栏监听面板(列表+状态徽标+触发徽标)与网页侧栏同源(server 契约),数据不走手机本地持久化。

### 关键实体

- **Listener**(§1 schema,权威契约):注册表行,三仓共享;
- **ListenerEvent**(§1.5 契约):事件摘要与分型 detail,上行/渲染/展示三处共用;
- **本地镜像文件** `listeners.json`:agent 侧派生物,非契约,可随时全量重建。

### §1 Listener schema(权威 = server 数据库)

**铁律:内存里允许存在的只有执行现场(goroutine),不承载任何权威状态。** 服务端重启零丢失(用户故事 2)由此保证:注册表、冷却、去重指纹、webhook 令牌、轮询基线全部是 DB 行字段,boot 时从 DB 重建全部轮询器与 webhook 端点。

```
Listener {
  id              string    // L-<uuid>,全局唯一
  account_id      string    // 跨设备归属锚(同账号全部设备可见)
  title           string    // 用户可读,如「盯 build.log 等编译报错」
  source          {
    type          enum      // file_tail | process_exit | http_poll | webhook
    device_id     string    // 事件源所在设备;file/process 必填=本机,poll/webhook 固定填 "server"
    params        json      // 按 §3 类型定义
  }
  prompt_template string    // 命中时注入会话的 prompt 模板,{{event}} 占位
  target          {
    session_id    string    // 绑定的统一会话;空 = 命中时新建会话
  }
  mode            enum      // once | recurring
  status          enum      // active | paused | fired | expired | error
  cooldown_ms     int       // 默认 600000(10min),recurring 去重窗口
  expires_at      time      // recurring 默认 now+7d;once 默认 now+24h,可覆盖
  last_fired_at   time      // DB 字段,重启后冷却照旧
  fired_count     int       // DB 字段
  dedupe_key      string    // 最近一次事件指纹,DB 字段,幂等去重
  poll_fingerprint string   // http_poll 专用:上次内容指纹,DB 字段
  webhook_token_hash string // webhook 专用:长效令牌哈希,DB 字段,重启不失效
  reject_count    int       // agent 侧源:server 永久拒绝累计,DB 字段(§2)
  overflow_count  int       // 连续队列溢出计数,DB 字段;成功注入即清零,重启保留(§忙会话排队)
  error_note      string    // 最近一次异常备注,任意状态可携带(queue_overflow 等不置 error)
  created_by      string    // ai:<session_id> | user:<device_id>
  created_at      time
  updated_at      time      // 单调递增,镜像 LWW 仲裁锚(FR-4)
}
```

- 事件摘要(供侧栏展示,最近 20 条)单独一张 `listener_events` 表,同样落库;
- 状态机单向:`active → fired`(once 命中)| `active → expired`(到期)| `任意 → error`(源消失/永久拒绝/连续失败),error 可由 AI/用户显式复活(reject_count/error_note 清零);
- 存储三份:**server DB = 权威;agent 本地 `listeners.json` = 镜像**(供本机 UI/离线只读,可全量重建);手机不落本地,走 server API。

### §1.5 ListenerEvent 契约(三仓唯一权威)

wire 契约一律 **snake_case**(server 库与 WS 原样);app 侧展示层自行 camelCase 化,映射规则机械(下划线→驼峰),不设别名。

```
ListenerEvent {
  listener_id   string
  fingerprint   string   // 去重指纹:源内容哈希(webhook=token+body哈希, poll=内容指纹, file=行哈希, process=退出码+时间桶)
  fired_at      time
  summary       string   // ≤200 字符单行,侧栏直接展示
  detail        {        // 按 source.type 分型,未列字段一律不出现在 wire
    // file_tail:  { path, matched_excerpt(≤512,已剥离控制字符) }
    // process_exit: { command 或 pid, exit_code, duration_ms }
    // http_poll:  { url, change_kind: fingerprint_changed|condition_met, matched_excerpt(≤512) }
    //             condition_met 为 1b 前向声明占位:1b 拍板放弃 JSON 条件语法时须同步删此枚举值,勿留死值
    // webhook:    { body_excerpt(≤512,已剥离控制字符) }
  }
}
```

- `{{event}}` 渲染输入 = 序列化后的 ListenerEvent(含 summary+detail);外部来源字段在入模板前已完成剥离/截断(见边界情况最后一条);
- `listener.event` 上行、注入 prompt、`listener_events` 表、手机侧栏徽标气泡,四处共用本契约,任何一侧不得私加字段。

## 成功标准 *(必填)*

### 可衡量的结果

1. **重启零丢失**:kill -9 服务端重启后,监听器全字段逐项一致(FR-1/FR-2 审计测试通过,用户故事 2 验收场景全绿);
2. **入口旅程 ≤2 分钟**:从「对 AI 说一句话」到「双端侧栏可见监听器」≤2 分钟(含 AI 工具调用),file_tail 命中到注入回合 ≤60s;
3. **跨设备一致**:任意设备创建/命中,其余在线设备 5s 内收到通知;离线设备上线后可见最近事件;
4. **AI 感知闭环**:注入 prompt 无需二次查询即可让 AI 开始处理(用户故事 5 验收场景);listener_list 与侧栏同源零口径差;
5. **幂等与风暴安全**:同一 fingerprint 重放/重连/重启均只触发一次;长断联(≥1h)不产生任何监听器 error;
6. **四源独立可交付**:每类源有独立的最小链路单测与真机冒烟项,任一源失败不阻塞其余源交付。

## 设计

### §0 语义基准(参考 Claude Code,已定案)

- **触发 = 向统一会话注入一条 prompt**,执行者始终是 AI,不是调度器直接跑任务载荷;
- 一次性(mode=once)命中即自毁(触发瞬间置 fired);循环(mode=recurring)默认 7 天过期防僵尸;once 默认 24h 过期;
- 只在会话空闲时注入;会话忙则排队,不抢占、不并跑(语义同既有 steer/回合队列);
- 与 Claude Code 的差异:注册表跨设备(server 权威)、事件源类型更丰富、通知 fan-out 到全部设备。

### §2 同步协议与上报失败语义

- **CRUD 全部走 server API**;server 落库成功后经既有 agent WS 下行推 `listener.upsert` / `listener.remove`,agent 以 `updated_at` LWW 原子写本地镜像文件;
- **事件上行**:agent 侧事件源命中 → WS 上行 `listener.event`(§1.5 契约)→ server 判 status/cooldown/dedupe → 通过则触发并更新 DB 行,拒绝则回 `listener.event.reject{listener_id, permanent, note, counted?}`(勘误 2026-11-21:原文 `{reason, permanent}` 与 server Task 4 实现不符,实发字段为 `note`;note 词汇表 {deleted, expired, paused, fired, device_unregistered} 为冻结契约);
- **上报失败语义(消解评审矛盾)**:
  - agent 侧源(file_tail/process_exit):**只有 server 永久拒绝**(`permanent:true`,行存活场景如源设备已注销;行已删除的拒绝无行可计、不计数,与 FR-6 同口径)累计 5 次才置 error;**WS 断联、发送失败、暂存队列中的事件一律不计数**——长断联不得把监听器打成 error;
  - server 侧源(http_poll):按 §3 失败分级(连续 5 次软失败置 error,4xx 立即 error);
  - webhook:被动面,token 校验失败只审计,永不置 error;
- **boot 重建**:agent boot 时**先拉全量镜像、后启动各 watcher**(接线进 boot 路径,配接线审计测试——1b8c36b 教训);server boot 时从 DB 重建全部 http_poll 定时器与 webhook 端点路由(同教训,配审计测试);
- **断联暂存**:agent 断联期间继续采集,事件入本地落盘暂存队列(上限 50 条,溢出丢最旧并记摘要),重连按序重放;server 以 `dedupe_key` 幂等。

### §3 事件源实现要点

| 类型 | 位置 | params | 要点 |
|---|---|---|---|
| file_tail | agent | `path`, `match`(关键字或正则), `max_bytes` | fsnotify + 断点续读(offset 落本地状态文件);文件截断/轮转重置 offset;匹配即上报,读侧限流 |
| process_exit | agent | `command`(由 agent 代启)或 `pid`(挂已有进程), `expect_code` | 复用 terminal/exec 面;agent 重启后 process 监听不重建(置 error,`error_note=agent_restarted`,避免重启风暴) |
| http_poll | server | `url`, `interval_s`(默认 60,下限 30), `match`(指纹变化;JSON 字段条件语法为 1b 设计决策,见交付边界), `max_bytes`(64KB) | 指纹存 DB 行;连续 5 次软失败置 error,4xx 立即 error |
| webhook | server | — | 每 listener 一个长效 token(哈希落库),回调 URL = `/api/listener/webhook/{id}/{token}`;body ≤64KB,超限 413;token 错误 401+审计;once 触发后回调 410 Gone |

### §4 触发与通知

- 命中 → server 渲染 `prompt_template`(`{{event}}` = ListenerEvent 契约 JSON,外部内容已剥离/截断)→ **注入统一会话回合队列**:空闲立即、忙排队、不并跑、不抢占;注入回合带来源标记 `listener:<id>`(journal 与前端均可识别);
- **通知 fan-out**:命中瞬间先推「监听器触发」轻事件到全部在线设备(手机侧栏徽标 + 桌面通知),AI 回合产出走既有 AI 事件通道;离线设备上线补拉事件摘要;
- once 命中即置 `fired`(触发瞬间,非回合完成);recurring 按 cooldown 去重;到期置 `expired` 并通知一次;
- **审批警示**:注入回合沿用所属会话 approvalMode;若会话为 require 且无人在线,回合撞审批卡会在 `agentAIApprovalTimeout` 被守卫判死——`listener_create` 工具返回显式警示当前会话审批模式,触发通知附「有审批卡待处理」提示。

### §5 LLM 工具面

4 个工具挂进既有 agent 工具注册表(claude/codex 经各自 MCP 配置下发,agentcore 走内置桥;schema 变更须过三内核支持矩阵):

- `listener_create(title, source{type, params}, prompt_template, mode, target_session_id?)` — 返回注册结果 + 审批模式警示;
- `listener_list()` — 含 status/last_fired_at/fired_count/error_note,与侧栏展示同源;
- `listener_update(id, ...)` — 改参数/暂停/复活 error;
- `listener_delete(id)`。

命中注入的 prompt 本身即 AI 感知通道:AI 醒来即知道监听器 id、标题、事件内容(§1.5 契约),自行决定后续动作。

### §6 实施坑清单

1. **活性**:注入回合即正常 AI run,走既有 markProgress 链路,无新增静默面;禁止为监听触发另开无守卫的裸 goroutine;
2. **boot 重建**:agent/server 双侧接线均进 boot 路径并配审计测试(见 §2);
3. **config 同步**:监听器独立于 config 文件(listeners.json + server DB),绕开 customer-config 五处副本坑;仅工具 schema 变更才需三内核同步;
4. **契约命名**:wire 全 snake_case(§1.5),app 展示层机械转 camelCase,禁别名——诊断面板批次教训。

### §7 测试策略

- server:注册表 CRUD、cooldown/dedupe 幂等、**重启重建**(kill -9→重启→全字段/冷却/指纹/令牌保留)、webhook 鉴权与 410/413/401 分支、注入排队(忙会话不并跑)、4xx/5xx 失败分级;
- agent:file_tail(fsnotify+断点续读+轮转)、process_exit(代启/挂 pid/重启置 error)、断联暂存重放幂等、boot 先镜像后启动审计、**永久拒绝计数与断联不计数的分界单测**;
- 三端契约:ListenerEvent 字段三方一致断言(agent 上行=server 落库=手机展示);
- 真机冒烟:双设备创建/命中/通知/审批警示全链路,清单出包后跑。

## 交付边界

- 本批三仓改动:alianggate(watcher/WS/工具面/镜像)、PhoneServer(API/DB/轮询/webhook/注入)、app(手机侧栏监听面板+徽标);
- **实施分期**(评审建议采纳):1a = 注册表+同步+注入+file_tail+双端侧栏展示(用户故事 1/2/3/5);1b = process_exit + http_poll + webhook(用户故事 4),其中 http_poll 的 JSON 字段条件语法在 1b 实施计划定案(本批只承诺指纹变化);1a 独立可交付可验收;
- 提交策略遵循仓规:全部改动待用户显式同意后才 commit;三仓按文件归属切分提交,禁 add -A。
