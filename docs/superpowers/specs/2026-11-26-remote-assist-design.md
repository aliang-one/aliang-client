# 远程协助(Remote Assist)设计

- 日期: 2026-11-26
- 状态: 设计已与用户对齐,待拍板项确认后实现
- 范围: 客户端仪表盘一键「发起协助」→ 平台 admin 审批 → 协助会话(客户端仪表盘页面操作镜像 + 家目录终端),客户端实时围观、仅 admin 可操作
- 交付节奏: 一期一验,共三期

## 1. 背景与目标

用户(客户端设备机主)在仪表盘上点「发起协助」,平台 admin 在后台同意后,进入协助会话:admin 在自己的页面上**操作客户端的仪表盘(56431)并在客户端家目录开真终端**,客户端在自己的仪表盘里实时看到 admin 的一切操作。场景 1 对 1,无视频技术路线,带宽为事件流级别(静默 <2KB/s)。

关键现状(侦察实证,全部在 master):

- **56431 = 客户端本机仪表盘(core 管理面)**,网页终端三期已全量落地:`agentTerminalManager`(create/write/resize/close/attachLive/attachHistory/sendReplay、2MiB ring 回放、墓碑历史、闲置回收)、`guardWebTerminalGate`(cookie∥loopback 双闸)、`HandleWebTerminalStream`、FS 四端点与 mkdir/rename/delete。引擎传输无关——writer 是注入接口 `agentTerminalWriter`(`agent_terminal.go:34`)。
- **PTY cwd 家目录兜底现成**:`resolveAgentTerminalCWD` → `resolveAgentCWDFallbackHome`(`agent_terminal.go:1828`)。
- **手机远端终端腿现成**:设备↔云端 remote WS(`agent_remote_ws.go`,`EnsureRemoteConnection`/`remoteConnectionLoop`),`DispatchLocalTerminal`(`agent_remote_ws.go:932-985`)按 source 门控分派;断线走 markAllDetached 保活、30m idle 才回收——协助腿必须同语义(非网页腿的 orphan timer 语义)。
- **piko HTTP 隧道现成**:`app/tunnel` Manager(per-device endpoint + TunnelToken + RoutePublicKey 路由签名校验 + TCP bridge + `<device>.mesh` 中继端点),`Configure/Stop/WaitConnected`,状态机 connecting/connected/failed/stopped。
- **双进程代理骨架现成**:core(56431)↔ user-agent runtime(56433),`proxyIfNeeded`(普通 HTTP)/`proxyAIStream`(WS,含 Cookie 透传与透传腿同源复检 `ai_stream_proxy.go:56/:47`)/`proxyFSIfNeeded`(数据面零硬超时);双注册铁律由 `route_parity_test.go` 兜底。
- **审计约定**:boot 先扩 audit 表,高敏操作留痕。
- **会话池硬约束**:`agentMaxTerminalSessions` 全局 4,手机腿共享——协助终端必须计入同池(见 §14 坑 1)。

## 2. 需求(用户已拍板)

1. **1 对 1**:一个协助会话只涉及一台客户端设备与 admin;客户端可围观,只有 admin 可操作。
2. **双视图 Tab**:①客户端仪表盘页面(admin 真实操作,rrweb 镜像给客户端)②家目录终端(admin 输入,客户端只读回显)。
3. **无视频、低带宽、实现简单**:页面视图走 rrweb DOM 差量流,不走 WebRTC/截屏流;终端为文本流。
4. **入口**:客户端仪表盘顶栏常驻「远程协助」;admin 端有请求队列,同意后开始。
5. **只读是结构性保证**:客户端渲染的是镜像副本/只读回显,零事件派发通道;写权限仲裁在服务端。

## 3. 技术定案(与用户对齐的调研结论)

- **页面视图 = rrweb live 模式**:admin 端 iframe 加载经隧道代理的客户端仪表盘并注入 `rrweb.record`,DOM 差量 + 鼠标事件流经协助房间直播;客户端用 `rrweb.Replayer` 渲染。观众端只渲染快照副本、不派发任何事件——「双执行」问题物理性不存在;画面状态(弹窗/抽屉/输入/路由)全覆盖。
- **整页跳转兜底**:rrweb 录制随文档生命周期中断,检测整页导航后广播 nav 帧,两端重建录制/回放;SPA 路由(pushState)无需此兜底。
- **终端 = 共享 PTY 扇出**:PTY 是服务端单一份状态,天然「一写多读」;输出帧扇出全部连接,输入帧仅 admin。
- **canvas 页降级**:SSH 出口 canvas 拓扑等 canvas 重页面在镜像中降级,演示时避开(rrweb canvas 采集又重又有限),明确接受。

## 4. 总体拓扑

```
客户端仪表盘(56431)          admin 控制台                  客户端观看页(56431 内)
┌─────────────────┐   piko HTTP 隧道   ┌──────────────────────┐
│ 本机仪表盘 SPA    │◄─(路由签名校验)─  │ /assist/:id/service/* │← iframe
│        ▲        │                   │  (同源反代+剥帧头)      │   + rrweb.record
│        │        │                   │  协助操作页(Tab 两视图) │        │
│  本机 core       │                   └──────────┬───────────┘        │ WS(rr_event)
│  ·协助会话权威    │        device↔cloud remote WS(既有通道)              │
│  ·PTY+扇出       │◄────────────── 终端帧 + rrweb 帧 ──────────►│        ▼
│  ·本机代理路由    │                              ┌──────────────┐  协助观看页
└────────┬────────┘                              │ 云端协助房间   │  (rrweb.Replayer
         │ WS(本机,登录态闸)                      │ +审批队列+路由 │   +只读 xterm)
         └──────────────────────────────────────►└──────────────┘
```

- **协助房间 hub 归云端**(平台侧,admin 控制台同族部署):admin 操作连接与客户端观看连接都连它;设备侧经既有 device↔cloud remote WS 通道接入。**全部复用既有通道,零新增凭据面**。
- **rrweb 帧流向**:admin 浏览器 → 云端房间 → 设备 remote WS → 设备 core → 本机 WS → 客户端观看页。多一跳但流量 KB/s 级,换零新通道。
- **终端帧流向**:admin 输入 → 云端房间 → 设备 remote WS → `DispatchLocalTerminal(source=assist)` → PTY;输出帧设备 core 原路回云,云端房间同时扇出 admin 与观看页。

## 5. 会话状态机

```
idle ──客户端点「发起协助」──► requested ──admin 同意──► active ──┬─admin 点结束──► ended
                              │                     │           ├─客户端点结束──► ended
                              │admin 拒绝            │           ├─设备离线─────► ended
                              ▼                     │           ├─admin 掉线>10m─► ended(admin_timeout)
                            rejected                │           ├─时长超上限────► ended(duration_cap)
                              │                     │           └─核心进程重启──► ended(device_restart)
                              ▼                     └─pending 超 5 分钟未审批──► expired
                             ended
```

- **单设备单活跃会话**;requested 期间重复点击 = 更新留言不新建。会话 id 形如 `assist-<snowflake>`,全链(终端 session id、隧道路由、审计)统一前缀。
- active 上限时长默认 2h(可配);admin 操作 WS 断开 10 分钟自动结束(客户端可见倒计时提示)。
- ended 后:PTY close、协助隧道面回收、房间关闭、双端页面同步收到 `assist.ended`(带原因),入口失效。
- 设备离线中端协助:remote WS 断开即触发云端侧 ended(device_offline);设备重连后拉取终态,入口显示「会话已结束」。

## 6. WS 协议(协助房间帧族)

消息 `type` 单字段分派,角色由**服务端**按连接身份判定,前端声明的角色永不采信:

| type | 谁发 | 谁收 | 载荷 |
|---|---|---|---|
| `assist.hello` | 双端 | 房间 | 连接后首帧,服务端校验会话与角色,回 `assist.state`(含你的角色/会话状态) |
| `assist.state` | 房间 | 双端 | 会话状态广播(requested/active/ended+原因/参与者在线情况) |
| `term.input` | **仅 admin**(服务端拒其他) | 设备 | 照抄手机腿 terminal.input 形状 |
| `term.output` | 设备 | 双端 | 照抄 terminal.output |
| `term.resize` | 仅 admin | 设备 | 同上 |
| `rr.event` | 仅 admin | 观看页 | rrweb 事件增量;含 FullSnapshot 标记 |
| `rr.snapshot` | 仅 admin | 观看页 | 全量快照(观看页中途加入/重连时,房间向 admin 索取后单播) |
| `assist.nav` | 仅 admin | 观看页 | 整页跳转兜底:`{url}`,观看页清空 Replayer 等新 snapshot |
| `assist.ended` | 房间 | 双端 | `{reason}` |

- 观看页中途加入/刷新重连:房间 → admin「需要快照」→ 录制端重发 FullSnapshot(`rr.snapshot`);终端腿走 attachHistory 环形回放续屏(既有能力)。
- 入站读限:协助房间帧走 remote WS 既有 4MiB 上限;FullSnapshot 大页面几十~几百 KB,一帧内足够。

## 7. 设备侧设计(本仓)

### 7.1 会话权威与门控

- 新增 `app/http/services/agent_remote_assist.go`:会话状态机(§5)、requested/approved/ended 的云端信令收发(经 remote WS 既有帧族扩展 `assist.*` 帧)、协助终端生命周期接线。
- 总开关 `remote_assist_enabled`:设备持久化配置,**缺省 false**(装机即无此功能;照 web_terminal_enabled 的零值纪律,勿进任何缺省 true 分支)。
- 协助终端**不走保险箱 PIN**:协助会话的授权就是用户本人的显式请求(点按钮 + 请求卡片明示「管理员将获得页面操作权与家目录终端」);PIN 门控的是「本机浏览器无人值守访问」,两者威胁模型不同。此为拍板项 §13-1,默认按此执行。
- 请求入口端点 `POST /api/agent/assist/request`(客户端仪表盘,照 web-terminal 闸门链①②:总开关 + cookie∥loopback 双闸)、`GET /api/agent/assist/session`(当前会话态轮询/推送)、`POST /api/agent/assist/end`(客户端主动结束)。双注册 routes.go + agentruntime/server.go,`route_parity_test` 兜底;core 腿照 proxyIfNeeded/proxyAIStream 模板透传。

### 7.2 协助终端腿

- `DispatchLocalTerminal` 增 `source=assist` 门控分支:查 `remote_assist_enabled` + 会话 active 且 session id 匹配,拒绝回 terminal.error 复用手机腿文案。
- 会话批准时由设备 core 创建会话专属终端(cwd=家目录,session id=`assist-<id>`),**手机式 detach 保活语义**(注册进 markAllDetached 面,不进网页腿 orphanTimers);会话 ended → close。
- 输入只认 admin 腿:云端房间已仲裁(§6),设备侧对 `term.input` 复核帧内 session 归属当前 active 会话(双闸,防御云端侧缺陷)。
- 全局 4 会话池共享(§14 坑 1):协助会话创建前检查余量,不足时拒绝批准并提示(手机在用的会话不被挤掉)。

### 7.3 客户端观看代理路由

- 设备 core 新增 `GET /assist/:id/service/*`:仅 active 会话、请求方为观看角色(仪表盘登录态)时,反代到本机 56431(self-call 或直挂静态+API 同源面)。作用:与 admin 端代理路径形态**完全一致**(§8.3),使 rrweb 快照里的资产 URL 两端通用。
- 客户端观看页 WS:`GET /api/agent/assist/stream`(本机,照 HandleWebTerminalStream 闸门模板:双闸 + `aiStreamHostAllowed` 同源闸 + 透传腿复检)。

## 8. admin 控制台侧设计(平台侧,跨仓 seam)

### 8.1 请求队列

- 协助请求列表(设备名/账号/留言/时间),沿用平台既有审批卡片交互范式;同意/拒绝 → 云端 → 设备 `assist.approved/rejected` 帧。
- 审批时强制展示授权边界文案:「同意后管理员将操作该设备的仪表盘页面并在其家目录开启终端,用户可全程观看」。

### 8.2 操作页

- 路由 `/assist/:id`:Tab [页面] [终端] + 顶栏(设备名/会话计时/观看者在线指示/结束按钮)。
- 终端 Tab:xterm 只读回显 + 可输入(admin 角色),接云端房间 WS;形状照 aliangboard 每窗一条 exec WS 惯例。
- 页面 Tab:iframe 加载 §8.3 代理路径 → load 后注入 rrweb.record(父页同源直接注入,零侵入目标应用)→ `emit` 经房间 WS 上行;鼠标事件 rrweb 原生录制(回放自带光标轨迹);中途观看者加入时按房间指令重发 FullSnapshot;检测整页导航(beforeunload/新 load)→ 发 `assist.nav` → 重建录制。

### 8.3 页面代理路径(两端同构,rrweb 资产 URL 通用的关键)

- admin 控制台提供 `/assist/:id/service/*` → 云端 → piko HTTP 隧道(路由签名校验既有)→ 设备 `127.0.0.1:56431`,反代剥 `X-Frame-Options`/CSP `frame-ancestors`。
- **客户端 core 提供同名路径**(§7.3,指回本机)——两端路径形态一致,rrweb 快照中根相对资产 URL(`/assets/…`)在两端 origin 上均可解析:观看页 Replayer iframe 内的资产请求落在客户端仪表盘 origin,天然命中同一 SPA 的静态资产。
- 仪表盘 SPA 需支持 base-path(自有应用,构建期 BASE_PATH 或运行时 base 注入,小改);**代理路径前缀字符串改写否决**(脆弱,见 §12)。

## 9. 客户端仪表盘侧设计(app/website)

- **入口**:DashboardPage 顶栏「远程协助」按钮(桌面+移动端,照 showChat 按钮 `DashboardPage.vue:168/:355` 双处模式)→ 弹窗:可选留言 + 提交;requested 态显示「等待管理员确认」+ 取消按钮;approved → 跳转观看页;ended/rejected → 对应文案。
- **观看页** `/assist`:上半 Tab 同构 [页面] [终端];页面 = `rrweb.Replayer`(live:`rr.event` 即刻 apply,时间戳剥离)撑满容器按宽缩放;终端 = xterm 只读(`disableStdin`),接本机 `/api/agent/assist/stream`;顶栏「管理员正在操作」指示 + 结束协助按钮(双段确认);断线重连按 §6 快照/回放续屏。
- useEscStack 管返回层;`assist_` 前缀词条 zh+en,全角标点/「」/「你」口径。

## 10. 威胁模型与安全设计

- **授权模型**:协助 = 用户显式请求 + admin 显式同意,双向知情;请求卡片明示授权边界(页面操作权+家目录终端),审计留痕。
- **只读结构性保证**:观看页是快照副本(零事件派发)+ 只读 xterm(`disableStdin` + 服务端拒 `term.input` 双闸);观看者浏览器对目标服务**没有任何交互通道**。
- **输入可见性**:rrweb 默认明文同步输入(演示需要「操作可见」);协助会话勿输凭据——请求与操作页双侧常驻提示;预留 `mask_inputs` 会话级配置(拍板项 §13-4)。
- **攻击面收敛**:不提供公开裸链接(v1);观看页门票 = 客户端仪表盘登录态 + 会话绑定;admin 操作页门票 = 平台 admin 身份 + active 会话。隧道面仅在 active 会话期可达,ended 即回收。
- **审计**:assist/request、approve、reject、end(含原因)、terminal create/close、隧道面建立/回收;**不落键击与 rrweb 内容**(隐私+噪声)。
- **限频**:单设备 requested 态重复申请合并;终态后 60s 冷却,防骚扰 admin 队列。

## 11. 测试

- **Go**(夹具参考 `agent_terminal_attach_test.go` 族):
  - 会话状态机:全迁移矩阵含六个 ended 原因、pending 过期、单设备单活跃、重复请求合并、冷却限频。
  - 角色仲裁:非 admin 连接发 term.input/rr.event 一律拒;admin 断线 10m 自动结束;会话结束后任何帧拒。
  - 终端腿:source=assist 门控矩阵、与手机腿互不串、4 会话池余量不足拒批、ended 关 PTY、detach 保活(非 orphan timer 语义)。
  - 代理路由:非 active 会话 403、观看角色只放 GET、路径守卫。
  - 双注册:route_parity_test 红绿。
- **vitest**(cd `app/website`,worktree 已知坑):入口状态机(requested/approved/ended)、观看页 Replayer 重建(含 nav 兜底)、只读 xterm、断线重连续屏、i18n 词条。
- **真机**:LAN 客户端 + admin 控制台全链一轮:申请→审批→页面镜像(弹窗/抽屉/输入全覆盖)→终端操作→中途刷新续屏→双端结束回收;Windows 0.196(ConPTY)终端腿一轮。

## 12. 已否决路线(留档防重走)

- **视频族(WebRTC getDisplayMedia / SFU / CDP JPEG 截屏流)**:带宽与基建复杂度不符,用户明确排除。
- **档1 被动同步(手写指针+URL 跟随)**:弹窗/抽屉/输入是盲区,演示即露馅;已被 rrweb 取代。其「广播最终 URL」思想保留为 §6 nav 兜底的一部分。
- **观众端事件重放(TogetherJS 式)**:双执行 + 双方登录态错位,协同浏览产品踩过的经典坑。
- **裸公开 token 链接**:面扩大;改身份绑定 + 会话绑定。
- **VNC/RDP 族**:仅非 Web 内容需要,本场景用不上。
- **代理路径字符串改写**(改写 HTML/JS 内路径):脆弱必碎,以两端同构路径 + SPA base 支持取代。

## 13. 拍板项(默认值已给,实现按默认走)

1. 协助终端绕过保险箱 PIN(默认:绕过,显式请求即授权;备选:请求时强制输 PIN)。
2. 会话上限 2h、admin 掉线 10min 自动结束(默认按此,可调)。
3. 观看页允许多连接(默认:允许,皆只读;备选:锁单连接)。
4. `mask_inputs` 会话级配置(默认:不提供 UI,协议预留;真有敏感演示需求再加)。
5. 仪表盘 SPA base-path 改造范围(默认:构建期 BASE_PATH 支持;实现侦察后若 Vite 环境注入有更简路径则从简)。
6. 请求入口是否也放手机 App 诊断面板(默认:v1 仅仪表盘顶栏,二期再看)。

## 14. 实现注意事项(侦察实证的坑清单)

1. `agentMaxTerminalSessions` 全局 4 与手机腿共享——协助会话必须计池并在批准前检查,否则挤掉手机会话。
2. 协助终端走手机式 detach 保活语义,**勿进网页腿 orphanTimers**(网页腿是延迟孤儿回收,语义相反)。
3. remote WS 不可恢复终态会 `terminal.closeAll()`(`remoteLoopExitCleanup`)——设备重连后 assist 会话按 ended(device_restart) 收敛,勿复活 PTY。
4. 透传腿(WS 经 core)必须显式复检 `aiStreamHostAllowed` + 转发 Cookie(`ai_stream_proxy.go:56/:47` 历史踩坑)。
5. 双注册铁律:routes.go 与 agentruntime/server.go 同批提交。
6. `remote_assist_enabled` 持久化零值即 false,勿进任何缺省 true 分支(web_terminal_enabled 同款纪律)。
7. FullSnapshot 单帧可达几百 KB:确认 remote WS 4MiB 读限覆盖;rrweb emit 建议 `inlineStylesheet:false` 降帧(样式已随仪表盘静态资产两端可达,快照无需内联)。
8. worktree 跑 vitest 须 cd 到 app/website(历史坑)。
9. 提交规范:全中文提交信息,commit 须用户明示同意。

## 15. 分期与验收(一期一验)

| 期 | 内容 | 验收标准 | 工作量 |
|---|---|---|---|
| 1 | 会话状态机+审批队列+终端腿全链(admin 操作页终端 Tab + 客户端观看页终端 Tab) | 真机全链:申请→同意→admin 敲命令客户端实时见→中途刷新续屏→结束回收;非 admin 输入全拒 | ~4 人日 |
| 2 | 页面腿(两端同构代理 + rrweb 录制/回放 + nav 兜底 + base-path 改造) | 页面 Tab 镜像:路由跳转/弹窗/抽屉/输入全覆盖;观看者中途加入收快照;canvas 页明确降级 | ~4 人日 |
| 3 | 加固(审计全套、超时自动化补齐、限频、`mask_inputs` 协议位、观看页体验细节) | 审计项齐全可查;六个 ended 原因各自真实触发一轮 | ~2 人日 |
