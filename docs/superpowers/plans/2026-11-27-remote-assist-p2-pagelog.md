# 远程协助 P2(rrweb 页面腿)实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 协助会话页面视图:admin 经同构代理真实操作客户端仪表盘,rrweb DOM 差量流直播到客户端观看页(Tab [页面][终端]),弹窗/抽屉/输入全保真,零事件派发(无双执行)。

**Architecture:** admin 控制台 iframe 加载经隧道同构代理的客户端仪表盘(同源→父页注入 `rrweb.record`),事件流经云端房间→设备 remote WS→设备 core→本机 WS→观看页 `rrweb.Replayer`(live 模式)。**路径前缀取定值 `/assist/service/`**(spec §8.3 的 `/assist/:id/service/*` 简化:动态 id 进不了 Vite 静态 base;会话绑定走两端各自上下文——设备侧单活跃会话天然唯一,admin 端由其 UI 态选择设备)。仪表盘以**相对 base(`./`)+ API 前缀自检**支持任意挂载前缀。

**Tech Stack:** Go(gorilla,既有 assist 房间管线)、rrweb ^2.x(新 npm dep)、Vue3.5+Vite5。

**Spec:** `docs/superpowers/specs/2026-11-26-remote-assist-design.md` §6/§8.3/§9;P1 已合 master(11 任务,HEAD 339c7423 之后)。

**范围边界:**
- 本计划只做本仓:协议扩展+设备侧中继+客户端 core 代理路由+SPA 相对化+观看页 Tab/Replayer。**admin 控制台侧(代理路由/record 注入/房间快照调度)是平台仓,附录 A 契约交付**。
- 真机端到端验收仍需平台侧就绪;完成线=设备侧全绿+前端 vitest 全绿+契约锚定。
- 实现前 `go build ./...` 基线;vitest 一律 `cd app/website`。

---

## 关键设计决策(预裁决,实现不再问)

1. **前缀定值 `/assist/service/`**(去 :id):Vite `base` 可静态化;两端路径形态一致使 rrweb 快照内资产 URL 两端可解析;会话唯一性由「单设备单活跃会话」保证。
2. **相对 base(`base: './'`)**:仪表盘无 URL 路由(App.vue 状态机切页),相对引用天然兼容任意挂载前缀;index.html 手写的根相对引用(`/icon.svg`、`/fonts.css`、`/fonts/…`)同步改相对。**API 调用不能相对化**(`/api/*` 必须落仪表盘自身 origin 的 core)——新增运行时检测:`location.pathname` 以 `/assist/service/` 开头时,agentApi 全部调用加该前缀(集中一处 wrapper,不散改)。
3. **rrweb 帧三个新类型**(进 assist 帧族,复用 P1 管线):`assist.rr.event`(admin→观看端,增量)、`assist.rr.snapshot`(全量快照,观看端加入/重连时由云端向 admin 索取后单播,设备只中继)、`assist.nav`(整页跳转兜底,admin 端检测 document 级导航后发,观看端清空 Replayer 等新 snapshot)。设备侧语义=纯中继:三类帧经 `handleRemoteAgentMessage` 新 case → fan 到 view sinks(复用 Task 7 扇出);**不经状态机**(rr 流不影响会话生命周期)。
4. **观看端生命周期不变**:assist.end+close-1000 仍是唯一终态;rr 流只是画面内容。
5. **canvas 页降级**:SSH 出口拓扑等 canvas 重页 rrweb 采集受限——观看端对该页接受画面缺失,不加 canvas 插件(P1 spec 已拍板)。
6. **录像隐私**:P1 spec 拍板「输入明文同步(演示需要)」;`mask_inputs` 协议位预留不实现。

## 文件结构总览

| 动作 | 文件 | 职责 |
|---|---|---|
| 修改 | `app/http/models/agent_protocol.go` | 三个新帧常量+锚定测试 |
| 修改 | `app/http/services/agent_remote_ws.go` + `agent_remote_assist.go` | rr/nav 帧分派 case→view 扇出中继(薄,无业务逻辑) |
| 修改 | `app/http/handlers/agent_handler_assist.go`(或新文件) | 客户端 core 同构代理路由 `/assist/service/*`(GET,会话闸,剥 X-Frame-Options/CSP frame-ancestors,反代→127.0.0.1:56431) |
| 修改 | `app/website/vite.config.js`(base './')+`index.html`(引用相对化)+`agentApi.js`(前缀 wrapper) | SPA 任意前缀挂载 |
| 修改 | `app/website/package.json`(+rrweb) | 新依赖 |
| 修改 | `AssistWatchPage.vue` | Tab [页面][终端]+rrweb Replayer 集成+nav 处理 |
| 修改 | `app/http/routes/routes.go` + `app/agentruntime/server.go` | 双注册 |

**不动**:状态机、终端腿、信令、观看 WS 骨架(P1 产物;rr 流搭在既有 view 扇出上)。

---

### Task 1: 帧协议扩展+设备侧中继

**Files:** Modify `agent_protocol.go`/`agent_remote_ws.go`/`agent_remote_assist.go`;Test `agent_remote_assist_test.go`

- [ ] 三个常量(`assist.rr.event`/`assist.rr.snapshot`/`assist.nav`)+锚定测试(照 Task 1 P1 模式)
- [ ] `handleRemoteAgentMessage` 新 case:三帧型→`s.assist.RelayRrFrame(msg)`→fan 到 view sinks(照 terminal.output 扇出形状;**无回帧、不触状态机**);enabled 门加三帧型
- [ ] 测试:approved 后注入 rr.event→view probe 收到;nav 帧→同;disabled→拒
- [ ] Commit `远程协助P2:rrweb帧族+设备侧中继`

### Task 2: 客户端 core 同构代理路由

**Files:** Modify/Create handler + 双注册;Test handlers 包

- [ ] `GET /assist/service/*`:guardWebTerminalGate 同族闸→会话闸(requested/active)→仅 GET(其余 405)→**转发前剥离 `/assist/service` 前缀**(`/assist/service/api/x`→`/api/x`、`/assist/service/assets/y`→`/assets/y`——评审承重勘误:不剥离则每个资产/API 请求 404,且 SPA 回退根 handler 对缺失资产返回 200+index.html=静默坏页;列出的其他测试都抓不住它的缺失,必须专测)→反代 `http://127.0.0.1:56431`(零硬超时 client,照 proxyFSIfNeeded 惯例,但转发路径用剥离后的而非 RequestURI 原样)→**剥响应头** `X-Frame-Options`/`Content-Security-Policy` 的 frame-ancestors 指令→透传其余
- [ ] runtime 腿自指(loopback)不代理死循环防护(前缀剥离本身已消除回环面,保留显式守卫)
- [ ] 测试:闸矩阵/仅 GET/剥头断言/会话闸/**前缀剥离判别测试**(GET `/assist/service/assets/styles.css` 断言上游收到 `/assets/styles.css`)
- [ ] Commit `远程协助P2:客户端同构代理路由`

### Task 3: 仪表盘 SPA 相对化改造

**Files:** Modify `vite.config.js`/`index.html`/`agentApi.js`(+相关组件内绝对路径 grep 清扫)

- [ ] `base: './'`;index.html 手写引用相对化(icon/fonts/tailwind/styles)
- [ ] agentApi 前缀 wrapper:`assistPrefix()` = pathname 以 `/assist/service/` 开头时返回该前缀,所有端点调用统一过它
- [ ] **全库 grep 绝对路径清扫**:组件里 `fetch('/api`、`/assets`、`/icon` 等硬编码逐一过 wrapper 或改相对;**WS URL 三处硬编码同扫**(评审勘误:`useAssist.js:195-199` assistStreamUrl、`useTerminalSocket.js:17-20` terminalUrl、`useAliangChat.js:2647` aiSocketUrl 都是 `${proto}://${host}/api/...` 形——镜像 iframe 内不过前缀则聊天流/终端/协助全降级;**P2 预裁决:镜像 iframe 内的 WS 功能降级为已知限制**,只要求 assist 流自身(观看页宿主直连,不经镜像)完好,其余 WS 不追)
- [ ] Task 4 落地注:rrweb live 模式构造后须 `replayer.startLive()`(官方 live-mode recipe)
- [ ] 验证:根路径挂载零回归(vitest 全套+vite build+人工 dev 起服冒烟);`/assist/service/` 前缀挂载 dev 冒烟(资产+API 双通)
- [ ] dev 冒烟机制(**评审勘误**:`base: './'` 下 vite dev 无法挂前缀):用构建产物 dist + Task 2 的 Go 代理(或极简 node 反代)冒烟,不用 vite dev
- [ ] Commit `远程协助P2:仪表盘SPA相对化(任意前缀挂载+API前缀自检)`

### Task 4: 观看页 Tab+rrweb Replayer

**Files:** Modify `AssistWatchPage.vue`/`package.json`(+rrweb)/i18n;Test `assistWatch.test.js` 扩展

- [ ] Tab [页面][终端](终端为 P1 现状;页面 Tab 挂 Replayer 容器)
- [ ] rrweb Replayer 集成:`rr.event`→`replayer.addEvent`(live);`rr.snapshot`→重建 Replayer;`assist.nav`→清空待新 snapshot;快照缺失期显示「等待画面同步」占位
- [ ] 事件流动:useAssist frames 已收全部扇出帧——页面按 type 分流(terminal.*→xterm 现状不变;rr.*→Replayer;assist.nav→重置)
- [ ] i18n:tab 页面/终端+等待同步文案
- [ ] vitest:rr.event 渲染(mock rrweb)、nav 重置、tab 切换、终端流不受影响
- [ ] Commit `远程协助P2:观看页Tab+rrweb实时回放`

### Task 5: 收尾

- [ ] 全量回归(Go assist 家族 -race+全包+前端全套+build)
- [ ] 附录 A 契约扩展(admin 端:record 注入/代理路由/快照调度/nav 检测)文本交协调者落计划
- [ ] Commit `远程协助P2:收尾(全量回归+契约扩展)`

## 附录:P2 帧契约扩展(平台侧)

- admin→设备(经云端房间),**帧字面量以此为准**(spec §6 的 `rr.event/rr.snapshot` 已定名为 assist 前缀形态,平台侧必须发这些精确字符串):`assist.rr.event{session_id, data(rrweb事件JSON)}`、`assist.rr.snapshot{session_id, data(全量快照JSON)}`、`assist.nav{session_id, url}`;设备侧纯中继扇出到观看端,零回帧、零状态机交互。
- 观看端加入/重连时:云端房间向 admin 索取 rr.snapshot(调度在平台侧),设备中继单播。
- admin 端 record 生命周期:iframe load 后注入(rrweb.record({emit}));整页导航=录制中断→发 assist.nav→重载 iframe→重注入→新 FullSnapshot。
- 代理路径:`/assist/service/*`(定值,无 :id)→piko 隧道→设备 56431;剥 X-Frame-Options。
