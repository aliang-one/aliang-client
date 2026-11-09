# 统一入口协议分派设计:56432 四协议嗅探(ssh/socks5/mesh/http)+ 普通与组网分流

- 日期:2026-11-09
- 状态:已批准(设计对话定稿,待实施)
- 上游设计:docs/superpowers/specs/2026-11-08-ssh-ingress-design.md(SSH 入口)、docs/mesh-forwarding-network-design.md(mesh 转发网)
- 性质:入口面整合——把三个独立监听口收拢为一个嗅探口,不改变任何既有转发/规则语义

## 0. 背景与动机

当前节点有三个互相独立的 TCP 入口,靠「连哪个口」而非「流量是什么」来区分协议:

| 口 | 协议 | 绑定 | 生命周期 |
|---|---|---|---|
| 56432 | 仅 HTTP(CONNECT/透明) | ServiceBindHost(默认 127.0.0.1) | run service 登录态驱动+自愈 |
| 56441 | mesh LAN 载体(帧外壳) | 无条件 0.0.0.0 | mesh 总开关轮询对账,常驻 |
| 56442 | SSH 入口(双形态) | ServiceBindHost | ssh_ingress 配置 Reconcile |

三条独立监听面带来三份端口管理、三份生命周期、两处双进程竞态面(56442 抢听、host key 换钥),且 56432 对非 HTTP 流量是「ReadRequest 失败即断」,没有探测能力。目标形态:**单一入口口做协议嗅探(ssh/socks5/mesh 帧/http),嗅探后再细分普通流量与组网流量**——普通流量本节点视作出口进规则判断与转发;组网流量非终点续传、终点注入本机规则。后者的语义 mesh 分支已齐备,本设计只做入口整合,不动转发面。

## 1. 目标与非目标

**目标**
1. 56432 成为唯一入口监听口:accept 后嗅探分派 ssh / socks5 / mesh 帧 / http 四类;
2. 新建 socks5 入站(CONNECT-only,门禁与 HTTP 同档=会话租约);
3. SSH 入口退化为连接处理器(serveConn),不再自行监听;
4. mesh LAN 载体口退役,统一口直调 `tcp.HandleMeshConn(carrier="lan")`;WS 中继载体不变;
5. 监听生命周期=代理运行态(沿用现状):run service 启停/登录过期停机/自愈拉起链全部保留,手动停止代理即整口关闭;
6. 绑定地址跟随启用面自动放宽(mesh 开→0.0.0.0,否则 ServiceBindHost);
7. LAN 里新旧版本节点互通(A 侧拨号候选双端口展开)。

**非目标(v1)**
- socks5 UDP ASSOCIATE(显式 reply 不支持);
- socks5 user/pass 认证配置面(无会话场景留待后续批次);
- 56441/56442 的双轨兼容监听(一次性退役,发布说明承担迁移);
- `ssh_ingress.port` 配置字段的删除(保留字段+运行面忽略,下批次清配置面);
- SSH mesh 形态 `IngressFrameMeta` context 键与 mesh 分支 `InheritedHeaderFromContext` 的载体统一(遗留账,SSH 载体多跳续传跟随 mesh 分支集成批次);
- TUN/出站侧任何改动。

## 2. 现状关键事实(实施前提,均已核实)

| 事实 | 位置 |
|---|---|
| 56432 accept 后直接 `http.ReadRequest`,失败断连,无任何探测 | inbound/http/server.go:129-142 |
| 会话租约在连接级(`AcquireProxyLease`),无会话回 503 | inbound/http/server.go:144-158 |
| CONNECT 路径喂统一管线 `tcp.GetHandler().Handle` | inbound/http/tunnel.go:36-46 |
| `HTTPProxyListenAddr()=JoinHostPort(ServiceBindHost(),56432)`,默认回环 | processor/config/defaults.go:22,70-72 |
| 56432 生命周期由 run service 驱动,不持久、自愈拉起 | app/http/services/run.go:34,340-351;app/http/services/run_proxy_autostart.go |
| SSH 入口 `Server.Start` 自行 `net.Listen`,绑定 `ServiceBindHost:rc.Port` | inbound/ssh/server.go:90-101 |
| SSH 连接处理:限速→握手 deadline→双形态通道收敛,全部在 `handleConn` | inbound/ssh/server.go:258-330 |
| 双形态:bare(authorized_keys 人用裸流)/ mesh(mesh_authorized_keys 帧外壳) | inbound/ssh/config.go |
| SSH mesh 形态拒绝回写 **JSON 码行**,与 mesh 分支**纯文本行**分裂,注释明说合并统一时二选一 | inbound/ssh/mesh_inject.go:16-28 |
| mesh LAN 载体无条件绑 `:56441`,被占回落 `:0` | app/tunnel/mesh_assembly.go:44,258-264 |
| `relay.Serve(listener, carrier)` → `tcp.HandleMeshConn`,carrier 由调用方给 | app/mesh/relay/relay.go:44-60 |
| `HandleMeshConn`:读 JSON 帧头→AppendHop 防环→门禁→类别×载体→leftover 回放→dns 分流→注入 `deps.Handler`(本机规则管线) | processor/tcp/mesh_inbound.go:81-176 |
| mesh 帧头=一行 JSON `{"from":...}\n`,上限 4096B,`{"` 前缀可嗅探 | outbound/meshproto/header.go |
| 中继载体终止点:`upstream.Listen` WS 监听→`meshRelay.Serve(meshListener, CarrierRelay)`,不经 TCP 入口口 | app/tunnel/manager.go:427-441 |
| A 侧 LAN 拨号:`LANAddrs`(host:port)逐项尝试,来源=beacon 发现+手填兜底 | outbound/proxy/mesh/mesh.go:44,128-129,189-194 |
| beacon 广播实际 mesh 端口(UDP 56440),发现路径自动跟随实际口 | app/mesh/lanbeacon/beacon.go:32,103 |
| mesh 装配 core 进程专属(双进程同装会抢口/双 beacon) | app/tunnel/mesh_assembly.go:17-19 |

## 3. 定案决策(用户拍板)

| # | 决策 | 备选与理由 |
|---|---|---|
| D1 | **三口合一**:56432 唯一监听口,56441/56442 退役 | 双轨过渡被否(多两条监听面长期维护,与整合初衷相悖) |
| D2 | **绑定跟随启用面**:mesh 开→0.0.0.0(现状 parity);否则 ServiceBindHost | 单旋钮方案被否(mesh 用户从零配置变必须 --host,摩擦倒退) |
| D3 | **socks5 CONNECT-only + 会话门禁**,无认证配置 | UDP/密码档被否(YAGNI;客户端场景 CLI CONNECT 已全覆盖) |
| D4 | **监听生命周期=代理运行态(沿用现状,2026-11-09 二次修订)**:56432 随代理启停/登录过期启停,手动停止代理=整口关闭(mesh/SSH 入站随之下线)。「常驻监听+停服务不停口」初稿被用户推翻:沿用「断开代理即断开端口」的既有模式 | 自愈拉起链(StartProxyWhenSessionReady/StopIngressIfActive/guard)全部保留;mesh/SSH 入站可用性 subordinate 于代理运行态,发布说明明示 |
| D5 | **分派器原地改造**(方案甲):复用 inbound/http 生命周期,嗅探独立小包 | 全新统一入口包被否(56432 生命周期迁移波及 IPC/状态查询/自愈一整串);失败后二次嗅探被否(协议分类埋进 HTTP 失败路径,语义混乱) |

## 4. 架构总览

```
                    统一入口 56432(绑定=UnifiedIngressListenAddr())
                              │ accept(conn)  ← 生命周期=代理运行态(沿用现状)
                              ▼
                 ┌───── 嗅探层 sniff(前 4 字节,2s deadline,回放缓存)─────┐
                 │                                                        │
        "SSH-" 前缀            0x05 socks5 帧头         {" JSON 帧头        其余
                 │                    │                     │              │
                 ▼                    ▼                     ▼              ▼
        ssh.Server.serveConn   inbound/socks5(新)    tcp.HandleMeshConn   HTTP 路径
        (限速/双形态原样)      CONNECT→构 Metadata    (carrier="lan")     (ReadRequest
                 │                    │                     │         CONNECT/透明)
                 └────────┬───────────┴──────────┬──────────┘              │
                          ▼                      ▼                          ▼
                 processor/tcp 统一管线(规则引擎)◄────────────────────────┘
                    ├─ 普通流量 → 本节点视作出口(aliang/socks/direct/reject)【现状不变】
                    └─ mesh 流量 → 终点:注入本机规则;非终点:规则判 toNode:C
                                    → mesh 出口带 AppendHop 帧头续传【现状不变】
```

**门禁语义逐协议保持现状**:http/socks=会话租约(无会话拒绝);ssh=公钥+限速;mesh=帧门禁(SelfID/ForwardEnabled/PeerAllowed/类别×载体)。嗅探层本身无门禁——它是分类器,准入归各协议处理器。

**未定义分支钉死**:ssh_ingress 配置关(无 authorized_keys/mesh_authorized_keys)时,嗅探到 `SSH-` 前缀的连接**直接断连**(不进入 serveConn,不回显任何协议错误——与现状「入口未启动」语义等价)。

## 5. 嗅探层规格

新包 `inbound/sniff`(~100 行,不依赖任何协议包):

- **读窗口**:accept 后设 2s 读 deadline,peek 前 4 字节;超时(对端连上不说话)→断连+Debug。
- **分类规则**(互斥):
  1. `"SSH-"` 前缀 → `KindSSH`;
  2. 首字节 `0x05` 且第二字节 ≤ 0x7F(方法数合法性收窄)→ `KindSocks`;
  3. `{"` 前缀 → `KindMesh`(候选;**完整解析不归嗅探层**,移交 `HandleMeshConn` 用其既有 4096B 上限+15s deadline+拒绝行语义兜底误判);
  4. 其余 → `KindHTTP`(保持现状:`ReadRequest` 失败断连)。
- **回放**:已 peek 字节经 `sniffedConn`(bufio+已读缓存回放)无损交给下游——收口 mesh_inbound 的 leftover 回放与 inbound/http 的 `replayConn` 两个先例为一份实现。
- **误判成本**:非 mesh 的 `{"` 流收到 mesh 拒绝行后断连;首字节 0x05 的非 socks 二进制流握手失败断连——均与现状「未知流断连」等价,不劣化。HTTP 方法 token 不会以 `{`/`SSH-` 开头,无碰撞;HTTP/2 prior-knowledge 现状即不支持,不新增。

## 6. 组件改造清单

### 6.1 `inbound/sniff`(新)
嗅探+`sniffedConn`,表驱动样本可测;产出 `(Kind, net.Conn)`。

### 6.2 `inbound/http`(改造)
- **生命周期零改动(沿用现状)**:`StartMitmHttp`/`StopHttpProxy`/run service 启停/`StartProxyWhenSessionReady` 自愈/`StopIngressIfActive` 登录过期停机/`agent_ai_proxy_guard` 探针——全部原样保留。停止代理=整口关闭(mesh/SSH 入站随之下线,D4 修订定案);`stopTun` 的关 listener 步骤同样保留。
- **分派**:`handleRawConnection` 改为 `dispatchConn`:sniff → 按 Kind 移交 ssh/socks/mesh/http 处理器。ssh/mesh 处理器经包级接线钩子注入(避免 inbound→app 反向 import):`sshin.HandleIngressConn`(inbound/ssh 同层直调)与 `MeshConnHandler`(包级 var,由 app 层装配时接线,与 `tcp.GetHandler()` 单例风格同族)。
- **绑定**:`UnifiedIngressListenAddr()`(见 6.6)替换 `HTTPProxyListenAddr()`;绑定地址随 mesh 开关变化时**重启 listener**(仅影响 accept 瞬间,在途连接不受影响),由配置提交 reconcile 钩子(与 `ReconcileSSHIngressFromConfig` 同族调用点)触发。

### 6.3 `inbound/socks5`(新)
- 版本协商:仅接受 no-auth(0x00);客户端不含 no-auth → reply `0xFF` 断连。
- CONNECT 解析:IPv4/域名/IPv6 三类 atyp;BIND/UDP ASSOCIATE → reply 命令不支持(0x07)。
- 构 `M.Metadata{HostName/DstIP/DstPort}`(与 CONNECT 路径同款提取口径)→ 会话租约 → `tcp.GetHandler().Handle`;错误按 RFC1928 reply 码回写后断连。
- UDP ASSOCIATE 显式不支持(reply 0x07),不做半套。
- **socks 分支恒启用**:无独立开关,门禁只有会话租约(与 HTTP 完全同档);LAN 暴露面随统一口绑定策略(见 6.6)。

### 6.4 `inbound/ssh`(改造)
- `handleConn` 更名导出 `serveConn(ctx, conn)`:限速/握手 deadline/双形态/通道收敛**零改动**;
- 删除 `Start` 的 `net.Listen` 路径与 acceptLoop;`Start/Stop` 退化为生命周期 ctx 管理(连接快照拆除语义保留);host key 生成/指纹/落盘逻辑不变;
- `RunConfig.Port` 字段与 `customer.ssh_ingress.port` 配置字段:保留 schema、运行面忽略、注释标废弃(删字段需同步 zod 五处副本,爆炸半径不值,下批次清);
- **顺带收口 G5**:删除 `mesh_inject.go` 的 JSON 拒绝码表,改回写 mesh 分支同款 `writeMeshRejectLine` 纯文本行(注意:两处同名函数签名不同——`processor/tcp` 版收 `net.Conn`,`inbound/ssh` 版收 `gossh.Channel`;SSH 侧需一个小适配器把 Channel 包成 conn 后复用 processor/tcp 版,ssh 包内同名函数改名或删除);
- 双进程竞态红利:监听只在 core,host key 生成单点化(指纹 API 双进程只读语义不变)。

### 6.5 `app/tunnel/mesh_assembly.go`(改造)
- LAN 载体段退役:`DefaultMeshLANPort`/`listenMeshLAN`/LAN `relay.Serve` 删除;
- beacon 的 `meshPort` 参数改传统一口(56432),广播实际端口,发现路径自动跟随;
- `relay.Serve` 仅保留服务 WS 中继载体(manager.go 路径不动);
- 装配对账条件不变(mesh 总开关驱动 beacon 生命周期;统一口的 mesh 帧分派恒在,关闭态由 `HandleMeshConn` 门禁天然拒收);
- **可用性语义变化(发布说明明示)**:LAN 接收/被转发从「mesh 开即常驻」变为「代理运行中才可用」——代理停止时对端拨入被拒(节点休眠语义,与 D4 修订一致)。

### 6.6 `processor/config`(新增)
`UnifiedIngressListenAddr()`:mesh 总开关开 → `0.0.0.0:56432`;否则 `JoinHostPort(ServiceBindHost(), 56432)`。SSH 入口绑定随统一口消失,不再单独绑定。

### 6.7 A 侧 LAN 拨号兼容(`outbound/proxy/mesh`)
手填 `host:port` 候选逐项展开为 `[原样, host:56432]`(去重追加):旧版对端(56441)原样可达,新版对端回退统一口可达;beacon 发现项(带实际端口)原样直通不展开。失败拨号是 LAN 瞬时 refused,追加候选的时延可忽略。

### 6.8 仪表盘/i18n
SSH 入口卡片端口展示改统一口(56432);`ssh_ingress.port` 输入框移除或标废弃;相关文案同步(含 LAN 手填地址提示文案——手填端口应为 56432 或依赖发现)。

## 7. 错误处理(汇总)

| 场景 | 处理 |
|---|---|
| 嗅探 2s 超时 | 断连+Debug 日志 |
| mesh JSON 解析失败 | `HandleMeshConn` 拒绝行+断连(现状语义) |
| socks 握手失败/命令不支持/目标解析失败 | RFC1928 错误 reply+断连 |
| HTTP 未知流 | `ReadRequest` 失败断连(现状语义零变化) |
| ssh 限速命中/握手失败/认证拒绝 | 现状逻辑原样 |
| 绑定地址变更 | listener 重启(accept 瞬间中断,在途连接不受影响) |

## 8. 兼容与迁移(发布说明要点)

1. **56442/56441 不再监听**:升级后直连旧口被拒。LAN 组网 A→B:新版对端经 beacon 自动发现统一口;旧版对端经 6.7 双端口候选可达。手填 56441/56442 的存量配置需更新为统一口或改依赖发现。
2. **监听生命周期沿用现状(D4 修订)**:停止代理/登录过期=整口关闭,mesh LAN 接收与 SSH 入站随之下线(对端拨入被拒=节点休眠);重新启动代理/重新登录(自愈链)后恢复。此前 mesh LAN 是「mesh 开即常驻」,此次收拢为随代理启停。
3. **绑定放宽可见性**:mesh 开启后 56432 对 LAN 暴露 http/socks 面,由会话门禁拦截;发布说明明示。
4. `ssh_ingress.port` 废弃(忽略),仪表盘端口显示统一口。
5. SSH mesh 形态拒绝行从 JSON 码变纯文本行(对端解析方若有需同步——当前唯一消费方是自家 A 侧,错误仅回显)。

## 9. 测试面

- `inbound/sniff` 表驱动六样本(`SSH-`/socks `0x05`/`{`/HTTP/未知二进制/超时):分派正确+`sniffedConn` 回放无损(下游读到完整原始字节流)。
- `inbound/socks5`:no-auth 协商、无 no-auth 拒绝、CONNECT 三类 atyp 解析、BIND/UDP 拒绝 reply、会话门禁拒绝、喂管线后 Metadata 断言。
- `inbound/ssh.serveConn`:bare/mesh 两形态注入全链(复用 sshtest 对端模式)、限速命中、Stop 快照拆除、mesh 拒绝行纯文本断言(替换原 JSON 码断言)。
- 统一口 mesh 分派:JSON 帧头→`HandleMeshConn` 注入(复用 mesh_inbound fake deps);非 mesh 的 `{"` 流拒绝断连。
- 绑定三态断言(全关/仅 ssh_ingress/mesh 开)与绑定变更重启 listener。
- A 侧双端口候选展开(手填 56441 旧对端→原样+统一口两条候选;beacon 项不展开)。
- 生命周期回归:代理启停/登录过期停机/自愈拉起行为与现状一致(既有 run service 测试全绿即为回归证据);停止代理后统一口不可达。
- 回归:inbound/http + inbound/ssh + app/mesh + processor/tcp + app/http/services 既有测试全绿;`go vet` + `gofmt` 通过。

## 10. 遗留账(明确不在本批)

- `ssh_ingress.port` 与 56441 相关配置字段的 schema 清理(下批次,五处副本同步纪律);
- SSH 载体 `IngressFrameMeta` 与 mesh 分支 `InheritedHeaderFromContext` 的 context 载体统一(SSH 载体多跳续传,跟随 mesh 分支集成批次;见 inbound/ssh/mesh_inject.go 集成期注记);
- socks5 user/pass 认证与 UDP ASSOCIATE(如有真实需求再立项);
- 仪表盘统一口状态卡片(嗅探分派计数等观测面)。
