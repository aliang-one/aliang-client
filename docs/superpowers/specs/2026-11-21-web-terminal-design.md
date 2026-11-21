# 网页终端(保险箱形态)设计

- 日期: 2026-11-21
- 状态: 设计已与用户对齐,待实现
- 范围: 56431 仪表盘新增「网页终端」——保险箱门控的交互式真终端 + 多窗口 + 文件浏览(浏览/预览/上传/下载)
- 交付节奏: 一期一验,共三期

## 1. 背景与目标

本项目已有 aliangcode 网页聊天仪表盘(Vue3.5+Vite5+Tailwind3.4,服务端 Go 双进程:core 56431 管理面 + 用户态 agent runtime 56433)。目标是在仪表盘上加一个网页终端:浏览器访问 56431,经保险箱门控后在本机开真 shell,并支持多窗口与文件浏览。

关键现状(侦察实证,全部在 master):

- **后端 PTY 终端栈已全量存在**:`app/http/services/agent_terminal.go`(约 1720 行)的 agentTerminalManager 提供 create/write/resize/close/attachLive/attachHistory/sendReplay/环形回放(2MiB ring)/配额挑战/闲置回收(detached 30m / attached 24h)/墓碑历史全套;引擎**传输无关**——writer 是注入接口 `type agentTerminalWriter func(interface{}) error`(`agent_terminal.go:34`)。
- **跨平台真 PTY 已装**:unix=`creack/pty v1.1.24`(`agent_terminal_unix.go:15`),windows=ConPTY `UserExistsError/conpty v0.1.4`(`agent_terminal_windows.go:23`),pre-1809 管道兜底分流在 `agent_terminal.go:1202`(errPTYUnsupported 定义在 `:32`)。
- **协议帧族现成**:`app/http/models/agent_protocol.go:25-35` 已有 terminal.create/input/output/resize/close/exit/error/replay/sessions。
- **现有唯一驱动通道=手机远端 WS**:`agent_remote_ws.go:932-985` 分派;网页侧只有只读 scrollback GET(`routes.go:342`)+ `AgentRuntimeSessions.vue` 的 `<pre>` 只读查看器;前端零 xterm 依赖(package-lock grep=0)。
- **聊天 WS 鉴权模板现成**:`HandleAIStream`(`agent_handler.go:92`)的 cookie∥loopback 双闸(`:95-101`)+ `aiStreamHostAllowed` 同源闸(`:55/:71`)+ 非 runtime 进程 `proxyAIStream` 路径无关透传(`ai_stream_proxy.go:44`,含 Cookie 透传 `:47` 与透传腿同源复检 `:56`)。

## 2. 需求(用户已拍板)

1. **网页专属**:入口挂在 56431 仪表盘页面,与手机云端 remote WS 通道零交集。
2. **多窗口终端**:交互形态对齐 AliangBoard(`/Users/mac/MyProgram/AiProgram/AliangBoard`,Vue3+Tailwind+xterm ^6.0.0)——浮窗+底部任务栏,最小化不销毁。
3. **文件浏览**:目录树+预览+**上传+下载**;不做 mkdir/改名/删除(需要时终端里敲命令)。
4. **保险箱形态**:必须设置 4 位数字密码,带防暴力破解;保险箱门同时罩住终端与文件浏览(读任意文件与 shell 同级危险)。
5. **忘记 PIN**:loopback 打开时可重置+审计留痕。
6. **交付**:一期一验。

## 3. 威胁模型(诚实边界)

4 位 PIN 只有 10,000 种组合,其安全性完全取决于防爆破,而非密码本身。正确心智=手机锁屏:防**浏览器级机会性访问**(局域网内他人打开仪表盘、借用电脑、共享屏幕),不防已拿到本机文件系统的人(能偷哈希就能直接上机器,不需要走网页)。由此推论:

- loopback 重置 PIN 不降低安全水位(loopback 在现有双闸语义中本就=机器主人本尊)。
- 服务端锁定阶梯做扎实后,在线枚举实际不可行(前 5 次免费,之后按阶梯最坏枚举需数天到数月且全程留痕)。
- 文件上传不新增「能力类」(解锁用户有 shell 本可写任意文件),它新增的是**便捷批量写通道**,所以单独审计。

## 4. 复用与分层

新增代码各归各层,引擎层与 PTY 层零改动:

```
[新增] 门控服务层   app/http/services/agent_web_terminal_vault.go   PIN/锁定阶梯/解锁令牌/审计
[新增] 传输层       app/http/handlers/agent_handler_web_terminal.go + fs handlers(照抄聊天 WS 模板)
[共用] 分派层       DispatchLocalTerminal(msg, writer)——从手机腿(agent_remote_ws.go:932-985)抽出,两腿共用
[复用] 引擎层       agentTerminalManager(不改)
[复用] OS 层        creack/pty + ConPTY(不改)
```

新端点全部落 `/api/agent/web-terminal/*` 与 `/api/agent/fs/*`,自动进入 `app/agentruntime/route_parity_test.go` 强制双注册护栏(漏一边=测试红)。

## 5. 闸门链(六层,顺序固定)

```
① 总开关 web_terminal_enabled(设备配置,默认 false——装机即无此功能)
② 传输鉴权:cookie∥loopback 双闸 + aiStreamHostAllowed 同源闸(照抄聊天 WS;透传腿必须显式复检)
③ 保险箱:PIN 验证 → 解锁令牌(内存态,绑定 dashboard 会话,闲置 15 分钟滑动过期)
④ 逐请求:终端 WS / FS 接口验令牌
⑤ 留痕:审计(见 6.4)
⑥ 边界:遍历守卫+symlink 校验+上传文件名清洗+大小上限(+可选公网硬闸 env)
```

**锁定阶梯**(服务端计数,持久化,重启不清零;成功解锁清零;两次验证间隔服务端强制 ≥1s;锁定期内直接拒且不计次数):

| 失败次数 | 后果 |
|---|---|
| 1-4 | 正常重试,前端显示剩余次数 |
| 第 5 次 | 锁 1 分钟 |
| 第 6-7 次 | 锁 5 分钟 |
| 第 8-9 次 | 锁 30 分钟 |
| 第 10 次 | 锁 1 小时 |
| 第 11-14 次 | 维持锁 1 小时(至第 15 次升级为 24 小时) |
| 第 15 次 | 锁 24 小时+审计高亮 |

**存储**:PIN 不存明文——per-device 盐+哈希+常数时间比较(`subtle.ConstantTimeCompare`)。**状态机**:未设置→(首次进入弹设置,输两次)→已锁定→(验证通过签发令牌)→已解锁→(闲置 15 分钟滑动超时/手动上锁/总开关翻转)→已锁定(同时终止所有活终端会话并作废令牌)。**killall 急停端点**随总开关联动。

## 6. 后端设计

### 6.1 门控服务层(vault)

新文件 `app/http/services/agent_web_terminal_vault.go`(命名可按仓内惯例微调):

- 状态:GetPINState()(unset/locked/unlocked+剩余次数/锁定剩余时间)、Verify(pin)→token、Setup(pin×2)、Change(old,new)、Reset()(仅 loopback 调用,见 6.2)、Lock()、KillAll()。
- 解锁令牌:随机生成、内存 map、绑定 dashboard 会话 cookie 的 session id、闲置滑动 TTL 15 分钟(终端输入/FS 请求续期)、Lock/总开关关/Reset 时全作废。
- 持久化:PIN 哈希/盐/锁定计数与截止时间落设备持久化配置(照 `persisted.Device` 模式,参照 `agent_service.go:1337-1340` 旁的 `remote_terminal_enabled` 读写;**注意不进其缺省 true 分支**——该开关缺省是 true,实测于 `agent_service.go:1247/1338`,web_terminal_enabled 必须零值即 false)。
- 开关 API:`GET|POST /api/agent/web-terminal/gates`(照 `HandlePeerGates` 形);翻为关时调 KillAll。

### 6.2 终端 WS

- `GET /api/agent/web-terminal/stream`,**每个终端浮窗一条独立 WS**(备选方案=单 WS 多路复用,弃:每窗一条生命周期最简、断线互不影响、与 aliangboard 每窗一条 exec WS 同构、handler 形状正套 HandleAIStream 模板)。
- 双注册:`app/http/routes/routes.go:324` 旁(core 56431)+ `app/agentruntime/server.go:177` 旁(agent 56433)。
- handler `HandleWebTerminalStream` 照抄 HandleAIStream 四段:①双闸 ②Upgrader.CheckOrigin=aiStreamHostAllowed ③vault 令牌校验(总开关+令牌;**loopback 免令牌不成立——本机浏览器也走 PIN**,仅 vault 管理类接口豁免见 6.4)④`!services.IsUserAgentRuntime()` 则 `proxyAIStream(w,r)`(路径无关透传自动带 Cookie 与查询串)。
- runtime 腿:upgrade → SetReadLimit **1MiB**(入站只有小帧;勿照抄远端 4MiB——回放是出站 64KiB 分帧 `agent_terminal_replay.go:33`)→ writeMu 互斥 writeJSON+5s 写期限 → 读循环按 msg.type 分派进 `DispatchLocalTerminal` → defer Unregister+Close LIFO。
- `DispatchLocalTerminal(msg, writeJSON)`:从 `agent_remote_ws.go:932-985` 抽出共用;create 走 beginCreate+safeGo 异步、input/resize/close inline 保序;**门控参数化**(source=remote|web):手机腿查 remoteTerminalEnabled,网页腿查 webTerminalEnabled——拒绝时回 terminal.error 帧复用手机同款文案。
- 配额挑战:本地网页会话预计不触发(挑战属手机审批流);实现期验证,若本地 create 也命中挑战,网页弹确认框、quota.resolved 经 DispatchLocalTerminal 喂回。

### 6.3 FS 端点

新 handler(可并入 agent_handler 或新文件),四个端点全部走闸门链①-④:

- `GET /api/agent/fs/list?path=`:一级列表,返回 `{path, entries:[{name,type:'dir'|'file'|'symlink',size,mtime,mode}]}`(数据形状参考 `agentcore/tools_read.go:249` lsTool;比 aliangboard 的 name+type 更全)。
- `GET /api/agent/fs/read?path=`:有界读文本(上限 2MiB,超出 `truncated:true`),返回 `{path,content,truncated,binary}`(形状对齐 aliangboard;binary 用前 8KiB 空字节/NUL 探测)。
- **core→agent 转发**:FS 是普通 HTTP(非 WS hijack),core 侧走既有非 WS 的 proxyIfNeeded 分流先例(参照 `routes.go:330-334` 的 `/api/agent/ai/mcp/*` 模式)。
- `GET /api/agent/fs/download?path=`:`http.ServeContent` 流式(range 支持),模板=附件链路 `HandleAttachmentGet`(`agent_handler_attachment.go:111`:nosniff+Content-Disposition inline,此处改 attachment)。
- `POST /api/agent/fs/upload`(multipart):大小上限(建议 100MiB 可配)、文件名清洗(去路径成分+保留原名或加时间戳防覆盖)、写入时同样过路径守卫。

**路径守卫**(统一前置函数):`filepath.Clean` + 拒绝绝对路径外逃 + `agentPathInsideDirectory`(`agent_execution_guard.go:502`,可直接复用)+ **`filepath.EvalSymlinks` 后复检**(仓库先例均不处理 symlink,必须补)+ 上传文件名 sanitize。真执行在用户态 agent 进程(macOS TCC 授权 stamp 记在 agent 进程)。

### 6.4 审计

- boot 先扩 audit 表(既定约定),落点:pin/setup、pin/verify 成败、pin/change、pin/reset、terminal create/close、fs upload/download。
- list/read 不审计(高频噪声)。
- vault 管理接口(change/reset/lock/gates/killall)**仅 loopback 或已解锁令牌**可用;reset 仅 loopback。
- **setup 例外(首次引导)**:`GetPINState()==unset` 时,凡通过闸门②(cookie∥loopback 双闸)的会话即可调 setup——未设置状态不存在已解锁令牌,否则 LAN 浏览器永远无法完成首次设置(§11 验收第一步依赖此路径)。设置完成后 setup 即关闭,后续改 PIN 走 change(需旧 PIN)。**非 loopback 来源的 setup 调用在审计中高亮**(窗口有界=仅未设置态+需登录,但机主应能看到是谁引导了保险箱)。

## 7. 前端设计

### 7.1 入口与工作台

DashboardPage 加「终端」按钮(桌面+移动端顶栏两处,参照现有 showChat 按钮 `DashboardPage.vue:168`(桌面)/`:355`(移动))→ 打开全屏工作台覆盖层(照抄 ChatPage `fixed inset-0 z-[900]` 骨架),浮窗与任务栏都活在该层;useEscStack 管理退出。

### 7.2 保险箱 UI

- 状态机:未设 PIN→设置弹窗(输两次,前端一致性校验);已设→4 位密码盘;已解锁→直接进工作台。
- 锁定时显示倒计时;未锁定时显示剩余尝试次数;verify 失败行内报错。
- 设置页(vault 管理区,仅 loopback 或已解锁可见):改 PIN、上锁、killall、开关状态展示;loopback 下出「重置 PIN」。

### 7.3 多窗浮窗

`useTerminalWindows()` composable(把 aliangboard `stores/terminals.js` 的 Pinia store 换皮成模块级组合式——本仓无 Pinia):

- 窗条目 `{id,name,status:'open'|'minimized',zIndex}`;FloatingWindow 壳(拖拽/最小化/最大化/关闭)+ 底部任务栏 + 级联错位开窗 + z 序分配,全部移植 aliangboard 结构(`FloatingWindow.vue`/`TerminalTaskbar`/`zScale.js`)。
- `v-for`+`v-show` 最小化不销毁:每窗独立 WS+xterm 实例保活。
- 窗口列表持久化 localStorage(`aliang-web-terminal-windows`,照 chatSettings safeStorage 模式;**刻意打破 useNavigation 不持久化约定**),刷新恢复为 minimized。
- 每窗独立 WS:`useTerminal.js` 照抄 `useAliangChat.ensureAISocket`(`useAliangChat.js:2320-2400`)的幂等建连/10s 超时保险丝/2 次 800ms 退避;断线重连后依赖引擎 attach replay 续屏。
- xterm:`@xterm/xterm ^6 + @xterm/addon-fit`(与 aliangboard 同版本);DOM renderer,不引 webgl;onData→terminal.input、FitAddon onResize→terminal.resize、卸载→terminal.close+dispose。
- AgentRuntimeSessions.vue 现态(5s 轮询只读列表+单弹窗)保持不动,作为「历史会话查看」继续存在;其数据字段(id/cwd/last_active_at)可复用做浮窗标题。

### 7.4 文件浏览窗

- FloatingWindow 壳 860×540 + 左懒加载目录树(移植 aliangboard `FileTree/FileTreeNode/FileBrowserBody` 结构:展开态 Set+selected ref+dirCache)+ 右预览区。
- 预览:文本用 highlight.js 只读高亮(复用 `utils/chatMarkdown.js:6-14` 已注册的 javascript/typescript/go/bash/python/sql/json/yaml 8 语言,免新增 mode 包);截断/二进制徽章;下载走 Blob;上传按钮(进度+失败提示)。
- 无终端↔文件浏览联动(点文件不 cd,与 aliangboard 同级)。

### 7.5 i18n

`term_`/`vault_`/`fs_` 前缀词条,zh+en 两份,全角标点/「」/「你」口径。

## 8. 数据与持久化

| 数据 | 位置 | 备注 |
|---|---|---|
| PIN 哈希/盐/锁定计数与截止 | 设备持久化配置(boot 先扩) | 零值=未设置 |
| web_terminal_enabled | 同上 | 默认 false |
| 解锁令牌 | 内存 map | 上锁/Reset/开关关时全作废 |
| 浮窗列表 | localStorage | 刷新恢复为 minimized |
| 终端会话/滚动缓冲 | 现有引擎(内存+ring) | 不动 |
| 审计 | audit 表(扩表) | 见 6.4 |

## 9. 错误处理

- agent 离线:透传腿 close(1011,"device agent offline") 语义照抄;FS 请求走 proxy 45s 数据面期限。
- PIN:锁定中返回明确剩余秒数;被锁时密码盘置灰+倒计时。
- FS:越界/不存在/权限拒绝/超限各回明确错误码与文案;symlink 指向守卫根外一律拒。
- WS 断线:每窗独立重连(2 次 800ms 退避),引擎侧 detach 保活不杀进程,30m 闲置才回收;重连后 replay 续屏。
- 上传超大/失败:拒绝并提示,不留 partial 文件。
- 令牌过期:前端捕获令牌失效错误帧→重新弹密码盘(不整页退出)。

## 10. 测试

- **Go**(夹具参考 `app/http/services/agent_terminal_attach_test.go`/`agent_terminal_async_create_test.go`/`agent_terminal_detach_test.go`):
  - vault:锁定阶梯逐档触发(含 11-14 次维持 1h)/计数持久化恢复/成功清零/常数时间比较/token TTL 与作废语义/Reset 仅 loopback/**setup 门矩阵**(unset+闸门②通过→200;unset+无 cookie 非 loopback→401;已设置态调 setup→拒绝并提示走 change)。
  - 终端 WS:双闸矩阵(无 cookie 非 loopback=401/loopback 放行/同源 LAN+cookie 放行/跨站 Origin 拒)×两条腿(core 透传+agent 直连,透传腿 Origin 复检)/输入保序/断连保活/门控双开关语义(remote 腿与 web 腿互不串)。
  - FS:遍历攻击(`..`、绝对路径、URL 编码、symlink 逃逸)、上传文件名注入、大小上限、binary 探测。
- **vitest**(须 cd `app/website` 跑,worktree 已知坑):useTerminalWindows 状态机(open/minimize/restore/z 序/持久化恢复)、保险箱 UI 状态机、树懒加载与缓存、令牌失效重弹密码盘。
- **真机**:LAN 同源回归(172.x:56431)+ Windows 0.196(ConPTY)+ mac 各一轮。

## 11. 分期与验收(一期一验)

| 期 | 内容 | 验收标准 | 工作量 |
|---|---|---|---|
| 1 | 单窗终端+保险箱全套+审计 | LAN 浏览器开 56431→设 PIN→开真终端敲命令→断网重连续屏→连错 5 次 PIN 见锁定;总开关关闭=功能整体消失 | ~4 人日 |
| 2 | 多窗浮窗+任务栏+持久化 | 开 3 窗互不干扰,最小化保活,刷新恢复为 minimized | ~2 人日 |
| 3 | 文件浏览(树/预览/上传/下载) | 遍历攻击用例全拒,大文件截断预览+完整下载,上传成功且落审计 | ~3 人日 |

## 12. 已否决路线(留档防重走)

- **WS↔SSH 桥**:内置 SSH 入口在 Accept 前 Reject session/pty-req(`inbound/ssh/server.go` serveChannels,仅放行 direct-tcpip/mesh-exit,纯公钥),桥接须在 sshd 内实现 shell——比复用 terminal 栈贵且零收益。
- **复用 TUI 注入/AI shell 工具**:TUI 注入是 UDS 注外部 claude CLI(`agent_tui_turn.go:116`),AI shell 工具是管道+进程组杀(`agentcore/tools_shell.go`)——与交互终端无共享面。
- **复用 remoteTerminalEnabled 当网页门**:该开关缺省 true(`agent_service.go:1247/1338`),复用=全网默认开 shell;必须独立 `web_terminal_enabled` 默认 false。
- **单 WS 多路复用**:见 6.2,弃。
- **FS 端点抄 peer transfer/inbox**:方向相反(本机向远端拉取),无浏览器回传可抄;正确模板是附件链路。

## 13. 默认收进的小项(用户未反对,实现时按此执行)

PIN 固定 4 位,不留 6 位口子;前端只显剩余次数+锁定倒计时,完整尝试历史只在审计表;终端↔文件浏览不联动;入口=仪表盘按钮→全屏工作台;本地网页会话预计不触发配额挑战(留实现期验证);mkdir/改名/删除不做。

## 14. 实现注意事项(侦察实证的坑清单)

1. `remote_terminal_enabled` 缺省 true——web_terminal_enabled 持久化分支勿进任何缺省 true 路径。
2. 透传腿手写 101 不经 Upgrader,必须显式复检 `aiStreamHostAllowed`+转发 Cookie(`ai_stream_proxy.go:56/:47`,历史踩坑)。
3. 双注册铁律:routes.go 与 agentruntime/server.go 两处同批提交;route_parity_test 会兜底。
4. 入站 SetReadLimit 1MiB 勿抄远端 4MiB。
5. FS 路径守卫必须补 EvalSymlinks(仓内先例全缺)。
6. FS/终端真执行必须在用户态 agent 进程(TCC stamp)。
7. aliangboard 是 Pinia,本仓是模块级 composable——换皮移植,勿引 Pinia。
8. 窗口持久化打破 useNavigation 不持久化约定是刻意决策,新增独立 localStorage 键。
9. worktree 跑 vitest 须 cd 到 app/website(历史坑)。
10. 提交规范:全中文提交信息,commit 须用户明示同意。
