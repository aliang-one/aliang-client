# 56432 入口加固 + 组网链路修复批:改动记录与重构注意事项

> 日期:2026-11-25 · 状态:**已实现、已验证(单测层),未提交、未合并、未装机**
> 位置:worktree `.claude/worktrees/fix-ingress-lan-exposure-mesh-chain`,分支 `fix/ingress-lan-exposure-mesh-chain`,基线 master `3099e661`
> 用途:组网/统一入口这一块即将大重构。本文是给重构者的交接——改了什么、为什么、**哪些不变量必须原样带过去**、哪些已知问题刻意没动。

---

## 0. 一分钟版

2026-11-24 的审计(9 张只读地图 + 8 路对抗复核 + 跨机实测)坐实了两件事,本批只做「最小根治」:

1. **56432 对局域网是无鉴权开放代理,并能回环打进管理 API**(critical,跨机实证:从 Mac 打 liang-dev,经它的 socks5 出网 200、代连 `127.0.0.1:56431` 200、`POST /api/config/customer` 直达 handler)。根因:ssh_ingress 一开 56432 就绑 `0.0.0.0`,而唯一的门「会话租约」是全机一个标志——不看来源、没有目标 ACL;代拨发生在特权网关进程里,56431/56433 看到的来源恒为回环,管理面所有「回环即信任」的判定对局域网客户端全部成立。
2. **组网链路有三处实证缺陷挡着「发给下一台电脑」走不通**:中继/打洞的接收侧被拼到 `127.0.0.1:56442` 死口;`aliang --host` 启动形态不接委托缝;转发端同设备的第二条连接顶掉第一条。

用户拍板的链路语义(**本批原样保持,重构也必须保持**):

> 到 56432、本来该走 15.nat0.cn 出去的流量 → 开了下一跳后发给下一台电脑 → **由下一台自己判断**:它也开了下一跳就继续传;否则就在它本机走 `aliang` 分支,从 15.nat0.cn 出去。

对应代码:发起侧 `processor/tcp/handler.go` `handleNonTLS` 的强制 Aliang 分支 → `dialViaMeshDelegate`(payload `{h:127.0.0.1,p:56432,v:[self]}`);接收侧 `inbound/ssh/mesh_inject.go` 注入本机管线 → 再进同一强制分支:`meshDelegateEligible` 为真继续上链,否则 `RouteToALiang` 走本机隧道。

---

## 1. 本批改动(按主题)

每一条都是先写失败测试再实现(TDD),行号以本 worktree 为准。

### 1.1 统一入口代理面来源闸 — `inbound/http/dispatch.go`(新 `timeouts.go`)

- **规则**:嗅探后,`KindSSH` 以外的连接(HTTP/socks5 代理面)若来源不是回环 → 拒绝。socks5 回 `05 FF`(无可接受方法),其余回 `HTTP/1.1 403`。日志 `[DISPATCH] 拒绝非回环来源访问代理面`(Info)。
- **例外**:运维以 `--host` 显式把服务绑到非回环地址(`config.ServiceBindHost()` 非回环)时放行——这是显式的「给局域网用」意图;**ssh_ingress 把 56432 放宽到 0.0.0.0 不算**。
- **SSH 族不经此闸**:它是统一入口唯一面向局域网的协议面,mesh 只骑在它上面,准入归公钥。
- 非 TCP 来源(`net.Pipe` 等)按本机对待(进程内连接没有来源概念)。
- 实现:`proxyFaceSourceAllowed`(var,测试可注入)。测试:`dispatch_source_test.go`。

### 1.2 代理面目标守卫 — `processor/tcp/proxy_face_guard.go`(新)

- `M.Metadata` 新增 `IngressProxyFace bool json:"-"`:由 socks5 / HTTP CONNECT / 明文 HTTP 三个元数据构造点打标。
- `CheckProxyFaceDestination(md)`:带标 + 来源非回环(含 IPv4-mapped 判回环)→ 目标是回环 / 私网 / 链路本地 / 组播 / 未指定 / CGNAT(`100.64.0.0/10`)即拒;目标是「AI base-URL 形态」(回环主机名 + 端口 56432)同样拒——那会带着本机登录态进平台隧道,等于冒用本机 AI 额度。公网目标放行。返回哨兵 `ErrProxyFaceLocalDestination`。
- 接线四处(纵深,非互斥):`TCPConnectionHandler.Handle` 入口(协议分派之前,兜底)与三个入站面的**成功应答之前**——`inbound/socks5` 的 `serve`(租约检查之前,回 rep 0x02)、`inbound/http` 的 CONNECT 路径(回 403)、明文 `HandleHttpConnection`(回 403)。
- 这道闸是 `--host` 部署下堵「回环穿透」的那一道;默认部署下 1.1 已经先挡住,两层是纵深。

### 1.3 mesh-exit 目标 ACL(转发端)— 两层

- **入站口** `inbound/ssh/mesh_inject.go` `meshExitLiteralTargetAllowed`:ForwardGate 之后、Accept 之前。IP 字面量与 `localhost`:回环**仅放行 AI base-URL 形态(回环 56432)**;链路本地 / 未指定 / 组播拒绝;公网与转发端局域网放行(节点转发的本意)。拒收消息含 `target not allowed`。
- **拨号时** `processor/tcp` `dialDirect`:注入流(`MeshInjected`)的**域名**目标先解析、逐地址校验(同一个判定函数 `InjectedTargetAllowed`),再把地址**钉进 `DstIP`**——否则 `localhost:56431` 或 `nip.io` 类名字指向 127.0.0.1 就绕过入站口;钉住后拨号不再二次解析,杜绝 DNS 重绑定(与 CONNECT 面取 `ips[0]` 同款)。校验拒绝不记负缓存。哨兵 `ErrInjectedLocalDestination`。
- 域名走 toAliang / toSocks 路由时不经本校验(由平台侧 / 上游代理解析,不触碰本机回环)。

### 1.4 无 Host 请求拒绝 + 错误应答一次写成 — `inbound/http/handler.go`

- `extractMetadataFromHTTP`:缺 Host(请求行 absolute-URI 与 Host 头都空)一律拒绝。旧实现回退到「监听地址」当目标:56432 绑 `0.0.0.0` 时那是本机局域网口,非回环来源的无 Host 请求被原样重放回自己的 56432——自连接递归到 fd 耗尽(审计定为 medium,可用性问题)。
- 明文 HTTP 面的错误应答改用 `writePlainHTTPErrorResponse`(`net/http.Response.Write`,一次写成):旧写法 `WriteHeader` → `Flush` → 写体,头部发出时 `Content-Length` 已是 0,错误体被客户端按定长丢弃(回环同步管道下则卡死在写体)。CONNECT 面的 503/403 同样收敛到 `writeHTTPStatusResponse`。

### 1.5 资源防护 — `inbound/http/server.go`

- 请求头读取加期限 `httpReadTimeout`(15s):嗅探层读完首段后清掉了自己的期限,`http.ReadRequest` 此前在无期限状态下等待——发半行请求行就停住的客户端每条连接白占一个 fd + goroutine,且发生在会话租约之前。
- accept 循环对非关闭类错误退避 `acceptErrorBackoff`(100ms):裸 continue 在 fd 耗尽期间空转(实测 300ms 空转 14.8 万次)。
- 两个旋钮都是 var(测试注入短值);`listenTCP` 是监听器创建缝。

### 1.6 管理面远程访问闸 — `app/http/middleware/remote_access.go`(新),接入 `chain.go` 默认栈

- **非回环来源的 `/api/*` 请求必须携带有效 dashboard 会话 cookie,否则 401。** 放行面:非 `/api/` 路径(嵌入的仪表盘页面与静态资源)+ 登录链路 `/api/auth/login`、`/api/auth/refresh`、`/api/auth/scan/{init,status,activate}`、`/api/dashboard/session`、`/api/health`、`/api/startup/status`。
- 只认 TCP 对端地址,**不信任 `X-Forwarded-For`** 等转发头。
- 放在 `LoggingMiddleware` 之内:被拒请求照常进访问日志。
- 为什么要这道闸:`--host` 把管理面绑到非回环地址后(liang-dev 实测 `*:56431`),只有 quick-setup / 监听器 / SSE / 聊天 WS 等少数端点查 cookie,配置读写、软件配置、运行控制、终端启动命令、证书操作等**绝大多数端点完全不查**。设计意图本来就是「回环 = 本机;远程 = 必须登录拿 cookie」(登录成功时 `writeAuthResult` 下发 cookie,`POST /api/dashboard/session` 只许回环或已有 cookie),缺的只是统一执法。
- 回环来源仍免 cookie:core→agent 的回环代理腿、本机 CLI/TUI、agent 子进程的 owner 通知都无 cookie。代理面的非回环来源已被 1.1/1.2 拦住,无法再借网关代拨把自己变成回环来源。

### 1.7 日志流 WebSocket 同源 — `app/http/handlers/logger_stream.go`

- `CheckOrigin` 改用聊天 WS 的 `aiStreamHostAllowed`:带 Origin 必须真同源;不带 Origin 仍要求回环 Host。旧实现只看 Host 是否 localhost/127.0.0.1、对 Origin 看都不看——用户浏览器里任何网页都能开 `ws://127.0.0.1:56431/api/logs/stream` 读实时日志,这条向量不依赖 ssh_ingress。

### 1.8 中继/打洞接收侧(ingressfwd)— `app/mesh/ingressfwd/forward.go`

- **56442 死口**:`EffectiveSSHIngress()` 早已把 port 0 归一成哨兵 56442(2026-11-10 进主线),旧 `Target` 以「`!= 0`」判「是否配了专用口」→ 默认配置下一律拼到 `127.0.0.1:56442`(core 从不监听),流被丢。现:跟随形态(未写 / 0 / 56442)→ 统一口 56432;显式专用口在前、统一口在后(`Targets`,`dialFirstReachable` 按序试)。
- **不再以 agent 进程的配置副本判「入口是否启用」**:双进程拓扑下两份配置可能不同文件(macOS:core 读系统数据目录,agent 读用户目录),副本没有 ssh_ingress 时旧实现返回空串直接关流——而 core 的入口其实开着。现在恒有目标,是否接收入口由 core 执法。
- **拼接前只认 SSH**:先 `sniff.Sniff`,首段不是 SSH 版本串即关、不拨 core。拼接是回环拨号,core 的统一入口按 TCP 对端地址判代理面来源(1.1),对回环拨号恒放行——把 HTTP/socks5 字节拼过去等于让远端以「回环来源」身份使用代理面。
- `app/tunnel/manager.go` 的 `[MESH-RELAY]` 日志与注释同步更正(旧注释把端口哨兵拼死口误导成「入口未启用」;现在该行只在「非 SSH 流 / core 侧入口全不可达」时出现,原因看同进程 `[MESH-RELAY-FWD]`)。
- 测试:`TestTarget` 旧断言本身就是错的(钉死 56432 却实现对 56442),`TestServeOrForwardForwardsToCore` 旧写法在测试 goroutine 里同步调用 `ServeOrForward`,必然挂死到 `-timeout` panic。两者重写,新增非 SSH / 静默流丢弃、目标顺序回落。

### 1.9 `aliang --host` 启动形态补接委托缝 — `cmd/start.go`

- 补 `m.WireMeshAssemblySeams(CurrentDeviceID)`(与 `cmd/core.go` 同款),并把它加进 `TestBootMeshWiringAudit` 的 exactly-once 审计表。
- 漏接后果(审计实证):该形态进程永远不是平台 WS 属主(agent 恒以独立子进程拉起),`validateConfig` 的另一接线点在本进程永不运行 → 开「平台流量代理」后平台请求逐条 `egress: delegate active but peer source unavailable`、AI 流静默留本机(diag 仍报 active=true),**进程重启后落 SoftExpired、入口代理暂停**,直到把开关关掉。

### 1.10 会话台账去「同设备顶替」 — `inbound/ssh/sessions.go`

- `register` 不再在登记时关闭同 `device_id` 的既有连接;台账回归纯观测(每条连接一条会话),管理端踢出走 `Kick`。
- 为什么:出口侧 `ExitClient` 对**每条转发流各开一条 SSH 连接**(无池),顶替规则(2026-11-15 引入)与该模型不兼容——审计用生产 ExitClient 实测:第 2 条流建立后第 1 条 1~2ms 内 EOF,5 条并发只剩最后 1 条;匹配键是 device_id 标签(两把钥同标签同样互踢),仅认证不开通道也踢。
- 旧测试 `TestMeshReconnectReplacesStaleSession` 把这个缺陷钉成了预期,已替换为 `TestMeshSameDeviceConnectionsCoexist`(真 sshd + 两条在途 mesh-exit 流 + 一条只认证的连接)。

### 1.11 identity 假警报 — `app/http/handlers/mesh_diag_handler.go` + mesh-runbook

- 去掉「roster 已同步但缺本机条目 … 自动组网将 fail-closed」这条 mismatch:PhoneServer 的 `GET /api/agent/mesh-roster` 回包前会滤掉请求方自身(其路由测试钉死「同账号全量(滤 self)」),任何同步成功的设备 `roster_self_present` 恒为 false。字段仍如实透传(注释说明服务端契约)。
- `app/assets/skills/mesh-runbook/SKILL.md`(随二进制 embed 自愈安装到 `~/.claude/skills/mesh-runbook/`,两份已同步):第 4 步补「`roster_self_present` 恒 false 不是故障信号」;族 2 加「先剔除该恒真假警报」。注意 `TestMeshRunbookEndpointDrift` 会拒绝 SKILL.md 里出现 core 未注册的 `/api/...` 字面路径——措辞要用 `mesh-identity` 这类不带 `/api/agent/` 前缀的写法。

---

## 2. 行为变化(部署后可见)

| 场景 | 之前 | 现在 |
|---|---|---|
| ssh_ingress 开着,局域网/隧道网设备连 56432 用 HTTP CONNECT / socks5 | 放行(可出网、可回环穿透) | 拒绝(403 / `05 FF`),日志 `[DISPATCH] 拒绝非回环来源访问代理面` |
| 同上,但运维显式 `--host 0.0.0.0` | 放行 | 代理面放行;目标为本机回环/内网/AI base-URL 形态一律拒 |
| 局域网设备直接调管理 API(`--host` 部署的 `*:56431`) | 绝大多数端点无凭证可调 | 除登录链路/静态页/健康/启动状态外一律 401(远程仪表盘需先登录拿 cookie——cookie 24h、持久化) |
| 用户浏览器里的第三方网页开日志流 WS | 可读 | 403(同源闸) |
| 中继/打洞流到达 agent 进程(默认 ssh_ingress.port) | 拼到 56442 死口被丢 | 拼到 core 56432,B 侧中继/打洞可用(仅 SSH 流) |
| liang-dev 这类 `aliang --host` 部署开「平台流量代理」 | 平台请求全败、重启后 SoftExpired | 委托缝已接,行为与 core 守护进程形态一致 |
| 同一设备并发多条转发生效流 | 只剩最后一条 | 共存 |
| 仪表盘「接入客户端」台账 | 同设备恒 1 行(靠顶替) | **每条转发生效流一行**(每流一连接模型的如实呈现,可能很多行——展示层可考虑按设备聚合) |
| `/api/mesh/identity` | 健康设备也报 roster-self mismatch | 不再报 |
| 明文 HTTP 面的错误应答 | 体被丢 | 状态行+头+体一次写成 |
| 56432 上的无 Host 请求 | 自拨自(可能递归) | 400 |

**审计时用的探针(部署后应全部变成拒绝)**:`curl --socks5-hostname <设备IP>:56432 http://example.com/`、`curl --socks5-hostname <设备IP>:56432 http://127.0.0.1:56431/api/mesh/status`、`curl -H 'Host: 127.0.0.1:56431' http://<设备IP>:56432/api/mesh/status`、`curl -X POST --data '{invalid' http://<设备IP>:56431/api/config/customer`(期望 401 而非 400)。

---

## 3. 验证记录

本批收尾时跑了一轮新鲜验证(worktree,`ALIANG_DATA_DIR` 指向临时目录):

| 项 | 命令 | 结果 |
|---|---|---|
| 构建 | `go build ./...` | exit 0 |
| vet | `go vet` 12 个受影响包(inbound/http、socks5、ssh、tun/...、sniff、processor/tcp、app/mesh/...、app/http/middleware、handlers、routes、app/tunnel、cmd) | exit 0 |
| gofmt | `gofmt -l` 改动 + 新增的全部 `.go` 文件 | 无输出(干净) |
| 测试 | `go test -count=1 ./inbound/... ./processor/tcp/ ./processor/egress/ ./processor/auth/ ./outbound/proxy/mesh/ ./app/mesh/... ./app/http/middleware/ ./app/http/handlers/ ./app/http/routes/ ./app/tunnel/ ./app/agentruntime/ ./cmd/ ./test/` | 除下表 4 个预存失败外全部 `ok` |
| race | `go test -race ./inbound/http/ ./inbound/socks5/ ./inbound/ssh/ ./processor/tcp/ ./app/mesh/ingressfwd/ ./app/http/middleware/` | 全部 `ok`,exit 0 |
| 长包 | `go test ./app/http/services/ -timeout 900s` | `ok` 204.5s |

**预存失败(均在未改动的 master `3099e661` 上用同命令复现,与本批无关)**:

| 包 / 用例 | master 上的复现 | 性质 |
|---|---|---|
| `app/tunnel` `TestAssemblySeamsPunchThreeStates` | 同败 | 已知(审计基线即红) |
| `app/mesh/punch` `TestClassifyWithBudgetImmediateExpiry`(`stun_test.go:350`) | 同败 | 确定性 |
| `app/mesh/lanbeacon` `TestBeacon{BroadcastAndReceive,StopCeasesBroadcastAndListening,RestartResume}` | 同败 | 本机环境:广播 `sendto: can't assign requested address` |
| `processor/auth` `TestSessionAuthorityProxyAdmissionLinearizesWithDemotion`(`session_authority_test.go:319`,计数不齐) | 整包 ×10 败 3、单测 ×30 败 3 | 计数竞争 flake;本 worktree 空载整包 ×10 败 4(后台跑着 services 整包时 8)。该包对本次改动只有传递依赖 `inbound/tun/metadata`(新增一个 bool 字段),无行为关联 |

逐项 TDD 均先观察到失败再实现:

| 改动 | RED 时观察到的失败 |
|---|---|
| 来源闸 | `status=200, want 403`;socks `method reply=0500, want 05 FF` |
| 目标守卫 | socks5 回成功应答 `[5 0 0 1 0 0 0 0 0 0]`;CONNECT `status=200, want 403` |
| mesh-exit ACL | `undefined` → 实现后全过(拨号时域名校验:实现前 `ErrInjectedLocalDestination` 未定义) |
| 无 Host | `expected a request without Host to be rejected`;端到端读超时(请求进了管线) |
| 读期限 / accept 退避 | `半截请求头必须受读期限约束`;`spun 148816 times in 300ms` |
| 管理面访问闸 | `undefined: RemoteDashboardAccessMiddleware` |
| 日志流同源 | 6 个子用例失败(跨站放行、LAN 同源被拒等) |
| ingressfwd | `port=0 → got "127.0.0.1:56442"` + 整包 20s 超时 panic |
| start 接线 | 审计 `start.go 中 .WireMeshAssemblySeams(...) 出现 0 次` |
| 会话共存 | `write "ping-1-after-#2": EOF` |
| identity | `mismatches = [roster 已同步但缺本机条目 …]` |

**没有做的验证(必须说清)**:未装机、未在真机上跑。跨机探针的重放、中继/打洞 B 侧实测、委托链端到端、并发转发,全部要等出包装机后按第 6 节冒烟。

---

## 4. 刻意没做(留给重构)

按「最小收口」原则,以下都只定位、不实现。

### 4.1 双进程下中继/打洞的**发起侧**(A 侧)仍是死的——影响最大的一条

- 事实(审计两路独立确认):数据面进程(core 守护进程 / `aliang --host`)发起的 mesh 出口只有 LAN 载体可用。piko 代理面 URL/token 只存在于收到平台 `tunnel.configure` 的那个进程的 `tunnel.Manager`(`validateConfig` 是唯一写方,而 configure 帧只在 user-agent 进程的 WS 读循环里处理);打洞信令帧要经本进程的远端 WS writer(`currentRemoteWriterFunc()`),数据面进程里恒 nil → `agent punch: remote writer offline`。core 守护进程的 TCP 路径(规则 `mesh:<id>` 与 AI 委托)甚至不装配 `PikoTransport`(`SetMeshTransportDialSource` 只在 `validateConfig` 调用)。
- 后果:Mac ↔ liang-dev 这类跨网、且 `lan_addrs` 为空的互钉,**现有代码下开开关只会全败**并触发 SoftExpired。可用的绕法只有把对端的可达地址手工钉进 `lan_addrs`(例如 liang-dev 的 `172.16.222.1:56432`,Mac 经 tailnet 子网路由可连)。
- 我已想好但未写的设计(供重构取舍):**经 user agent 回环代拨**。
  - 新叶子包 `app/mesh/agentdial`(只依赖 net/http):客户端 `Dial(ctx, baseURL, carrier, peerID)`——向 user agent 的 `127.0.0.1:56433` 发 `GET /api/agent/mesh-dial` + `Upgrade: aliang-mesh-dial`,读 `101` 后把连接(带 bufio 回放)当 `net.Conn` 交给 x/crypto/ssh;服务端助手负责 Hijack → 调拨号函数 → 写 101 → 双向 splice,失败回 `502 + X-Aliang-Mesh-Dial-Error`。
  - user-agent 进程(`app/agentruntime/server.go` 的 `registerAgentRoutes`,已有 `loopbackOnly` 中间件)注册该端点:`carrier=piko` → `GetSharedAgentService().TunnelManager().DialPeerMesh`;`carrier=punch` → `services.AgentPeerPunchDial`。
  - 数据面进程把两条缝换成「智能」版本:中继拨号 = 本进程 Manager 已配 proxy URL → `dialMeshEndpoint`,否则代拨(piko 端点名 `<id>.mesh` 需还原成 peerID);打洞拨号 = 本进程有信令出口(frame sink 或 remote writer)→ 原路径,否则代拨。接线点放 `tunnel.WireMeshAssemblySeams`(core boot / start boot / validateConfig 三处都会调)。
  - **测试纪律(必须照做)**:`services.localUserAgentBaseURL` 在 go test 下指向隔离地址 `127.0.0.1:1`(`useragent_test_isolation.go`),新路径在测试里**绝不能拨到开发机的活体 agent**;tunnel 包若要自己的基地址,需要同款 go-test 守卫或可注入的 func var。
  - 注意双注册纪律:agentruntime 侧新增路由要查 `app/http/routes/*_test.go` 的 parity 护栏是否要求 core 侧同名注册。

### 4.2 ExitClient 每流一条 SSH 连接(无池)

- 现状:每条转发流一次独立 SSH 握手(`outbound/proxy/mesh/sshexit.go`)。本批去掉顶替后它不再「一用就坏」,但代价是 N 条流 = N 条 SSH 连接 + N 个台账会话。重构选项:同 peer 复用一条 SSH client、每流一个 channel——那样「一个节点一条连接」的台账与顶替语义才重新成立;需要处理重连、keepalive、死连接探测、`OpenChannel` 无 ctx 需 deadline 兜底。

### 4.3 其它已知、未动

- **beacon 广播被 utun 吞**(runbook 族 3):Mac 实测 `bcast_send_err` 上万、`rx=0`——发送 socket 绑 `:0` 不钉物理口,广播路由落到 Tailscale 的 utun7。修法=逐物理口定向广播(punch 包已有接口绑定 socket 的三平台实现可复用)。
- **打洞 B 侧监听器硬 TTL ≤90s、静默收摊**:长连接委托流经打洞载体会被切断(B 侧修好后这条会成真)。
- **可观测性**:委托路径不发 `[MESH-OUT] … via carrier=`(只有规则路径发),egress 委托成功零日志,B 侧注入日志是 Debug(生产钳 INFO)。排查「流量到底走没走下游」目前只能靠 `[TCP] … via RouteMeshDelegate` 与失败错误文本。
- **`/api/mesh/diag` 不如实**:`delegate.active` 只是配置投影;不报「中继拨号源/打洞信令在本进程是否可用」。建议加 `relay_dial_source` / `punch_signalling` 两字段(`local` / `user_agent` / `unavailable`)。
- **UI 缺口**:`forward_enable`(B 侧硬闸)没有任何面板开关;`mesh.enable` / `auto_peers` / `max_hops` / `mesh_authorized_keys` 也没有;有 mesh 块后「SSH 入口」从面板关不掉(保存期被 `ValidateMeshDependency` 拒,报错文案误导);SSH 卡提示「开启组网后放宽到 0.0.0.0」已过时(判据是 ssh_ingress);「平台流量代理」文案承诺「所有平台流量」且「下一跳也开着就继续传」,与实现不符(443/TLS 与 agent 通道不委托;平台自身流量到第一跳即止)。
- **安全面残余(回环信任模型)**:本批堵的是「网络来源 → 回环」这一层。**本机任何进程**仍可经回环无凭证调用管理 API,包括 `POST /api/software-config/activate`(以 core 身份写任意路径,Mac 上 core 是 root LaunchDaemon)、`POST /api/agent/tools/launch`(起任意命令)、网页终端 PIN 重置链、`PUT /api/agent/mcp/servers/{name}`(植持久命令)、`GET /api/config/customer`(明文回显 mesh 私钥/代理口令/API key);`/api/config/core` 因 `isDev = true` 硬编码进了正式包;浏览器跨站 no-cors POST(依赖 Private Network Access,未实测)。这些都需要「回环 ≠ 可信」的信任模型改造(本机配对令牌 / Origin+Host 校验 / 写接口统一鉴权),属于重构范围。
- **反向代理注意**:1.6 只认 TCP 对端地址。若在同机用 nginx 反代到 `127.0.0.1:56431`,远程用户会以回环身份出现、被当作本机信任(既有行为,未变)。
- **CI 不跑 `go test`**:`.github/workflows/tag-build-release.yml` 只有 `go build`。ingressfwd 的「引入即红 + 引入即挂」能潜伏 5 天就是因此。建议至少对确定性包加测试步骤(注意仓内已知环境敏感/预存失败:`app/tunnel TestAssemblySeamsPunchThreeStates`、`app/mesh/lanbeacon` UDP trio、`app/http/services` 若干 flake)。
- **平台侧(PhoneServer)**:`meshIdentityRoutes.ts` / `meshIdentity.ts` 的注释仍写「全量含自身」,与路由实际「滤 self」相反;`GET /api/config/customer` 是否脱敏、`/api/user-center/api-keys` 是否掩码在平台侧。
- **假绿**(未配置 mesh 却报 enable/effective=true):并行会话已修并合入 master `3099e661`,本批基线已含。

---

## 5. 重构必须原样带走的不变量

这一节是本文最重要的部分。每条后面是「违反时的现场」。

1. **代理面 ≠ 局域网面。** 统一入口上只有 SSH 族面向局域网;HTTP CONNECT / 明文 HTTP / socks5 只服务本机来源,除非运维以 `--host` 显式开放。→ 违反:局域网任意设备把网关当无鉴权代理、冒用本机 AI 额度。
2. **「来源是回环」不等于「本机发起」。** 任何能让远端让网关代拨的入口(代理面、mesh-exit、回环拼接、将来的新入站面),都必须带上来源标记并过目标守卫。管理面(56431/56433)的信任边界是 `RemoteAddr` 回环——而代拨发生在网关进程里,代拨出去的连接永远是回环。→ 违反:经 socks5 `CONNECT 127.0.0.1:56431` 拿到管理 API(审计跨机实证)。
3. **转发端只把「回环:56432」这一种回环目标交给对端。** 它是出口链路委托的合法载荷,链的末端设备据此在自己的管线上走 `aliang` 分支出网;其余回环 / 链路本地 / 未指定一律拒。域名目标必须**解析后校验并钉住**。
4. **回环拼接(splice)前必须嗅探,只搬 SSH。** → 违反:远端经中继通道把 HTTP 字节送到 core 的回环口,绕过不变量 1。
5. **链路语义是「下一跳自己决定」。** 发起侧把 56432 回环流原样(payload `h=127.0.0.1,p=56432,v=[…]`)交给下一跳;继续上链还是本地走 `aliang` 分支,由接收侧的开关与守卫决定。发起侧不注入 `Authorization-Inner`、不套 aliang 包装器——出口身份属于链的末端设备。
6. **台账纯观测。** 在「每流一条 SSH 连接」的出口模型下,任何按 device_id 的连接级顶替/去重都会掐断在途流。要「一个节点一行」,应该改出口模型(复用连接、多路 channel),不是在台账里关连接。
7. **端口哨兵翻译只有一个口径。** `ssh_ingress.port` 的 0 与 56442 都表示「跟随统一口 56432」;翻译的唯一来源是 `processor/config` 的 `SSHIngressDefaultPort` / `EffectiveSSHIngress()`。任何新代码用「`!= 0`」判「是否配了专用口」都会重演 56442 死口。
8. **远程管理面 = cookie 必需,放行面最小。** 新增登录前必须可达的端点才进 `remotePreAuthAPIPaths`;永远不信任转发头。
9. **每个 boot 形态的接线都进 `TestBootMeshWiringAudit`。** 「本体在、接线断」已有四例(start.go 漏 beacon、roster 零调用方、utun7 吞广播、本批的 start.go 漏委托缝)。
10. **roster 不含 self 是服务端契约**,不是身份不一致;身份是否上送成功看日志里的 `agent_server_call`(endpoint 以 `mesh-identity` 结尾)。
11. **测试纪律**:① `ingressfwd` 的 `ServeOrForward` 阻塞至流结束,测试必须在 goroutine 里跑;② `go test` 一律带 `-timeout`(仓内有挂死用例);③ 跑测试加 `ALIANG_DATA_DIR=$(mktemp -d)`——只有 `processor/tcp` 与 `test/` 有 TestMain 状态目录隔离,其余包会把日志写进活体 agent 的 `~/.aliang/logs/aliang_core.log`;④ 涉及 user agent 回环地址的新路径在 go test 下必须指向隔离地址;⑤ 同步管道(`net.Pipe`)同一端只能有一个读者(应答被另一个读者抢走会表现为 `unexpected EOF`)。

---

## 6. 出包装机后的冒烟清单

1. **暴露面重放**(三台都做,期望全部被拒):第 2 节末尾的四条探针。本机自测用本机局域网地址当客户端来源(非回环来源)即可复现审计条件。
2. **中继 B 侧**:liang-dev agent 日志出现 `[MESH-RELAY-FWD] local ingress absent, forwarding … -> core 127.0.0.1:56432`(不再出现 `56442`);`GET /api/status/ingress` 出现 `form=mesh` 的会话。
3. **委托链单流**(需先按 4.1 的绕法给 Mac 侧 peer 填 `172.16.222.1:56432`,或在局域网内两台设备上做):Mac 开「平台流量代理」选 liang-dev → 发起一次 agentcore 原生 run → liang-dev 日志出现 `[TCP] … via RouteToALiang` / `[AliangGate] Connecting to server 15.nat0.cn:16749`,Mac 侧 `[TCP] … via RouteMeshDelegate`;liang-dev 开关关着 → 在它本机出网(用户语义)。
4. **并发**:同一次 run 的多条并发请求(流式响应 + 并行工具调用)全部完成,liang-dev `/api/status/ingress` 多条 `form=mesh` 会话同时在线、`closed` 里无 `kicked=true`。
5. **远程仪表盘**(`--host` 部署的 liang-dev):未登录浏览器只看到登录页,登录后功能正常、日志流可读。
6. **`aliang --host` 形态开开关后重启进程**:不再落 SoftExpired。

---

## 附:本批文件清单

新增:`inbound/http/dispatch_source_test.go`、`inbound/http/resource_guard_test.go`、`inbound/http/timeouts.go`、`inbound/socks5/socks5_guard_test.go`、`inbound/ssh/mesh_inject_policy_test.go`、`processor/tcp/proxy_face_guard.go`、`processor/tcp/proxy_face_guard_test.go`、`app/http/middleware/remote_access.go`、`app/http/middleware/remote_access_test.go`、`app/http/handlers/logger_stream_test.go`。

修改:`inbound/http/{dispatch,handler,handler_test,metadata_extractor,server}.go`、`inbound/socks5/socks5.go`、`inbound/ssh/{mesh_inject,sessions,sessions_mgmt_test}.go`、`inbound/tun/metadata/metadata.go`、`processor/tcp/handler.go`、`app/mesh/ingressfwd/{forward,forward_test}.go`、`app/tunnel/manager.go`、`cmd/{start,boot_wiring_audit_test}.go`、`app/http/middleware/chain.go`、`app/http/handlers/{logger_stream,mesh_diag_handler,mesh_diag_handler_test}.go`、`app/assets/skills/mesh-runbook/SKILL.md`(另:`~/.claude/skills/mesh-runbook/SKILL.md` 已同步,自愈安装会在下次 boot 覆盖为同一份)。

未触碰:`app/website/`(无前端改动)。
