# 组网收敛设计:mesh 作为 SSH 代理链出口

- 日期:2026-11-10(六轮修订:用户决策 → 首轮评审 → 二轮代码级对抗 → 三轮安全/前端复核 → 四轮全量实施面复核 → 五轮逐行代码核验)
- 状态:已实施(2026-11-12,见 plans/2026-11-10-mesh-as-ssh-proxy-chain.md;分支 fix/mesh-ssh-proxy-chain)
- 上游设计见 §1.4(含取代关系清单)
- **行号引用约定**:本 spec 的行号均已逐行核验过;但行号会随代码漂移,实施时请以**符号名**为准(`grep -n "func X"`),行号只作快速定位的提示。

## 0. 一句话

> **mesh 收敛为「一组命名的 SSH 节点 + 一条『这批流量从哪个节点出』的规则语法」。出口节点把收到的流注入自己的入站管线。**

它不是协议、不是载体、不是独立组件——是 `proxy_rules` 里的一个出口目标,复用 SSH 的**握手、身份、加密**,但**不复用 `outbound/proxy/ssh` 的连接池**(理由见 §5.4)。

**一期范围 = 单跳**(A→B,B 即出口)。多跳留作后续批次(见 §16)。

## 1. 背景

### 1.1 现状的三处重复

| 能力 | SSH 侧 | mesh 侧 | 重复性质 |
|---|---|---|---|
| 逐跳 TCP 穿透 | `outbound/proxy/ssh/chain.go:133 dialChain` | `outbound/meshproto` 帧协议 + `processor/tcp/mesh_inbound.go` AppendHop | 同一件事两套代码 |
| 加密通道 | SSH 传输层 | piko 中继 + LAN 明文 + SSH 载体三重 | 载体层三份不统一 |
| 入站形态区分 | `inbound/ssh` 公钥身份 → `ingress_form` | mesh 帧头自报 `from` 字段 | 身份可信性更差的那套在跑 |

### 1.2 复杂度的真实成本

原 design §3.3 的核心卖点是「B 侧注入管线,多跳免费获得」。实现代价是:

- `meshproto.Header` 的 `from/class/target/via/hops` 五字段跨线传递;
- `AppendHop` + path vector + 跳数帽三层防环(design §7);
- 两套帧实现并存(`outbound/meshproto` 4096B 上限 / `inbound/ssh/meshframe.go` 64KB 上限);
- `ContextKeyIngressFrameMeta` 无生产消费方,SSH 载体这一跳的 path vector **实际是断的**(全仓仅 `mesh_inject.go:162` 写入、零读取)。

即:**保留了多跳的复杂度,却没拿到多跳的真实收益,还留下一个防环缺口。** 本设计用「单跳 + 配置期链」换掉「运行期 path vector」,把这块复杂度整体删掉。

### 1.3 已知缺陷(本设计一并修正)

- **身份自报**:B 侧 ACL 比对帧头 `from` 字段(`mesh_inbound.go:135`),客户端可任意声称自己是 dev-a;
- **载体准入双轨且不对称**:`ClassCarrierAllowed`(`outbound/meshproto/header.go:150`)只识别 `lan`/`relay`。B 侧 `mesh_inbound.go:140-144` **确实**做了类别×载体准入(纵深防御第二道),但 SSH 载体这一跳(`inbound/ssh/mesh_inject.go`)**完全不做**——同一能力在两条载体上行为不同;
- **`dns:` 在 SSH 载体被拒**(`mesh_inject.go:85`),TCP 载体支持——行为分裂;
- **host key 是 TOFU**:`chain.go:169` 用 `d.hosts.CallbackFor(...)`,`KnownHosts.CallbackFor`(`known_hosts.go:36-63`)查不到就写入,LAN 上任何人可冒充对端;
- **LAN 可达面由 mesh 开关决定**:`defaults.go:92-99 UnifiedIngressListenAddrFor` 在 mesh 开启时把 56432 绑 `0.0.0.0`。这是新模型下最危险的一处遗留(§5.5)。

### 1.4 本设计取代的条款(逐条)

| 文档 | 被取代的条款 | 取代内容 |
|---|---|---|
| `docs/mesh-forwarding-network-design.md` | §3.1/§3.2 帧协议与载体 A/B 三分 | §4 两 channel + §7 Transport |
| 同上 | §3.3「B 规则参与多跳 → 逐跳动态改道」 | 一期单跳;§5 的「语义零损失」**仅指出口跳** |
| 同上 | §4 流量三级分类的载体准入 | §7 `Transport.Admit`(A 侧)+ 公钥准入(B 侧) |
| 同上 | §7 防环三层(path vector/跳数帽) | §6 静态检查 + 注入标记 |
| 同上 | §9.4 兜底 | 保留语义,底盘换 SSH(§7.2) |
| `specs/2026-11-08-ssh-ingress-design.md` | §2/§5 形态②=mesh 帧外壳 | §4 `mesh-exit` channel(ExtraData payload) |
| 同上 | §2 ACL =「来源 device_id ∈ customer.mesh.peers」 | §10.1 `mesh_authorized_keys` 公钥比对 |
| 同上 | §5 `dns:` 在 SSH 载体拒连 | §9 `dns:` 成为 `mesh-exit` payload 变体 |
| `specs/2026-11-09-unified-ingress-protocol-dispatch-design.md` | §5 四协议(ssh/socks5/**mesh**/http) | §3 mesh 并入 SSH,降为三协议 |
| 同上 | §1 遗留账「SSH 载体 path vector 断」 | §6 载体不复存在,该账自然消解 |

## 2. 核心决策记录(已拍板)

| # | 决策点 | 结论 |
|---|---|---|
| 1 | mesh 定位 | **全收进 SSH**:mesh 是 SSH 代理链的规则语法与出口目标,不自建协议 |
| 2 | 一期范围 | **单跳**(A→B)。多跳移出 v1(§16) |
| 3 | 载体层 | **piko 降为 Transport**:`<deviceID>.mesh` 端点改跑 SSH 握手 |
| 4 | UDP 打洞 | **留 Transport 抽象缝,不实现** |
| 5 | 出口语义 | **自定义 channel `mesh-exit` + 注入本机入站管线**(保留 B 的规则/MITM/socks5/customer.proxy) |
| 6 | 防环 | **静态检查 + 注入标记禁再走 mesh**;运行期 path vector 全删 |
| 7 | DNS 代转发 | **保留**,作为 `mesh-exit` payload 变体,不进 B 管线 |
| 8 | 与 `ssh_ingress` 关系 | **依赖**:开 mesh 必须开 SSH 入口;LAN 可达面改由 `ssh_ingress` 决定(§5.5) |
| 9 | 配置面 | `customer.proxy` 与 `customer.mesh.peers` **不合并** |
| 10 | A 侧身份凭证 | **节点级 `customer.mesh.private_key`**(新增字段) |
| 11 | 对端身份校验 | **`peers[].host_key` 显式钉扎**,禁用 TOFU(fail-closed) |
| 12 | 失败语义 | **不变**:错误回源,仅链头执行兜底与失败策略 |
| 13 | SSH 栈复用边界 | **只复用握手/身份/加密原语,不复用 `outbound/proxy/ssh` 的池与 `chain.go`**(§5.4) |
| 14 | 让渡型能力门禁 | `forward_enable` **仍是硬闸**,`ForwardGate==nil` 语义由「全放行」改为 **fail-closed**(§10.1) |
| 15 | 出口链路(2026-11-12 拍板) | **toggle=无条件链到下一跳**:fallback 收敛为 `{enable,next_hop}`,地址/钉扎从 peers 条目继承;离线信号门控整族删除——开关即常驻改道,不按本机能否直连平台做限制(§18) |
| 16 | 链式防环 | **via 经停链**:每个委托跳把本机 device_id 追加进链随 payload 传递;接收端发现自己在链里即就地处理(回环终止),链长 ≥ `max_hops` 即就地处理(放大上限)——`max_hops` 从占位复活为经停上限(§6.4/§18) |

## 3. 三层职责

| 层 | 归属 | mesh 不再拥有 |
|---|---|---|
| **入口 · 协议分派** | 56432 嗅探 `SSH-` / `0x05` / HTTP(**三协议**) | `{"` json 帧嗅探分支 `sniff.KindMesh` 整条删除 |
| **入站 · 形态分流** | SSH 入口按公钥类型 → `bare` / `mesh`,接 form×channel 矩阵 | 自建监听口、拒绝行、帧头解析、`HandleMeshConn` |
| **出口 · 出口目标** | `toNode:<deviceID>` 走独立 mesh SSH 出口客户端(§5.4) | 自写 carrier 选择(LAN 优先/piko 兜底) |

**关键收益:mesh 帧协议从入站侧彻底消失。**

### 3.1 地基:形态区分器已存在

- `inbound/ssh/config.go:23-44` `RunConfig` 已有 `MeshKeys []gossh.PublicKey`(**本次改为 `[]MeshKey`**,§10.2)、`Port`、`LocalNodeID`、`ForwardGate`;
- `server.go:283 publicKeyCallback` 已按公钥身份写 `Permissions.Extensions["ingress_form"]`(`FormMesh`/`FormBare`);
- `server.go:439 serveChannels` 已按 form 分流。

mesh 不新建身份体系——**它只是 SSH 入口的第二个 channel 形态**。

## 4. 通道协议

### 4.1 两个 channel

`serveChannels`(`inbound/ssh/server.go:439`)从「仅 `direct-tcpip`」扩为放行两个 channel。**payload 一律走 SSH channel 的 ExtraData**(`newCh.ExtraData()`),不走 channel 数据流首行。

理由:ExtraData 无长度歧义、无首段预读坑、与现有 `direct-tcpip`(`server.go:459` 读 `ExtraData()`)同构;首行字节方案要在服务端再造一层 bufio,正是旧 `newMeshChannelConn`/`meshChannelConn` 的 `preread` 回放机制(`mesh_inject.go:197-233`)的来源。

**已核实可行**:`mux.handleChannelOpen` 在推入 `incomingChannels` **之前**就 `newChannel(chanType, inbound, msg.TypeSpecificData)`,所以 Accept 之前可读 ExtraData;`client.OpenChannel(name, data)` 的第二参数经 `mux.OpenChannel → newChannel(..., extra) → TypeSpecificData` 直达对端。

| channel | form | ExtraData(JSON,无换行) | 服务端动作 | 进管线 |
|---|---|---|---|---|
| `direct-tcpip` | `bare` | RFC 4254 §7.2 原样 | 现状不变 | 是 |
| **`mesh-exit`** TCP | `mesh` | `{"t":"tcp","h":"example.com","p":443}` | 构 Metadata | 是 |
| **`mesh-exit`** DNS | `mesh` | `{"t":"dns"}` | 代解析单问单答 | **否**(§9) |

> DNS 变体**没有 `h`/`q` 字段**:A 侧构造 Metadata 时根本不知道域名是哪条(`inbound/tun/tunnel/dns_forward.go:198-204` 只传 `HostName: meshproto.DNSTarget` = `"dns:"`),域名只在 `[2B 长度][DNS 报文]` 帧内。B 侧自己从报文里解问。

**ExtraData 长度上限**:服务端必须显式 cap(`len(extra) > 512 → reject`)。SSH 单包理论上限虽约 32KB,但畸形对端不应把解析成本推满;正常 payload 恒 < 100B。

**注入落点(已核实的现成同构先例)**:`inbound/ssh/channel.go:73-88` 的 bare 分支就是自建 `*M.Metadata` → `tcphandler.GetHandler().Handle(ctx, newChannelConn(ch, msg), metadata)`。`mesh-exit` TCP 分支照抄这个形态,但**不能复用 `newChannelConn(ch, msg)`**——那里 `msg` 是 `directTCPIPMsg`(RFC 4254 §7.2 解出的四元组),而 `mesh-exit` 的 payload 是 `{"t":"tcp","h":...,"p":...}`。

**直接复用既有的 `channelConn` 结构体**(`channel.go:102`,未导出但同包,字段可直填),不需要新造适配器类型:

```go
// inbound/ssh/mesh_inject.go — 重写后的注入主体
md := &M.Metadata{
    Network:      M.TCP,
    ConnID:       fmt.Sprintf("sshing-%d", atomic.AddUint64(&sshIngressConnCounter, 1)),
    HostName:     p.H,          // ExtraData 里的 h(已拆端口,§5.1.1)
    DstPort:      p.P,          // ExtraData 里的 p
    Route:        tcphandler.RouteMesh,   // 决策前的统计口径(§4.1 附注)
    MeshInjected: true,         // §6.2 环路守卫
}
md.SrcIP, md.SrcPort = netAddrToIPPort(connRemote)   // 源地址取连接层,不取 payload
tcphandler.GetHandler().Handle(ctx, &channelConn{
    Channel: ch,
    local:   &net.TCPAddr{IP: net.ParseIP(p.H), Port: int(p.P)},  // host 非 IP → IP 为 nil,仅供日志
    remote:  connRemote,                                          // 连接层实际对端
}, md)
```

> 与现有的 `newMeshChannelConn`(`mesh_inject.go:209`)比:那个函数存在的唯一理由是**回放 bufio 预读**(帧头与首段载荷可能一次写出,帧协议解析已把首段吃进缓冲)。ExtraData 方案没有预读,`meshChannelConn`/`newMeshChannelConn` 及其 `preread` 机制整块删除(§13.1)。
>
> `host` 不是 IP 时 `net.ParseIP` 返回 nil,`LocalAddr` 的 IP 部分为 nil——只是日志/统计用的展示地址,管线路由走 `md.HostName`,不受影响(与 bare 分支 `channel.go:110` 同样的近似)。

> `ConnID` 用包内原子计数器,**不是** channel id——`gossh.NewChannel` 同样不暴露 channel id(`NewChannel` 只有 `ChannelType/ExtraData/Accept/Reject` 四个方法)。计数器写法与既有 `mesh_inject.go:155` 完全一致,沿用即可。

**注入时的 Metadata 两字段**:`Route = RouteMesh`(该常量保留,见 §12.1)保证路由决策前的消费方(`processor/statistic/ai_activity.go:121` 的 AI 活动统计口径)行为不变;`MeshInjected = true` 是 §6.2 的环路守卫,且不被 `tls.go:343` 覆写。

> `inbound/ssh` **已经 import** `processor/tcp`(`mesh_inject.go:17 tcphandler`),故直接引用 `tcphandler.RouteMesh` 无环。§12.1 因此要求 `RouteMesh` 常量**留在 `processor/tcp`**(不搬走),否则这里要新造一个同值常量,两处漂移。

### 4.2 form × channel 矩阵

公钥身份决定 form,form 决定可用 channel:

| form | 允许的 channel | 越界处理 |
|---|---|---|
| `bare` | `direct-tcpip` | `mesh-exit` → 拒 `"channel not allowed for this credential form"` |
| `mesh` | `mesh-exit` | `direct-tcpip` → 同上拒 |
| 未知 | 全拒 | `UnknownChannelType` |

**实现注意(已核实行号)**:当前 `serveChannels:441-444` 在**读 form 之前**就 Reject 非 `direct-tcpip`。矩阵实现必须先读 `perms.Extensions[extIngressForm]`,再按 form 判 channel 白名单。这个顺序调整是 §4 的隐含改动点。

### 4.3 流量类别为什么不上线

原 `Header.Class`(self/ai/custom)判定载体准入。新模型下:

- **A 侧**在拨号前判定(`Transport.Admit`,§7),这是唯一决策点。类别**逐连接**解析(`classFromContext(ctx, md)`,§5.1),不是 peer 属性;
- **B 侧**原有的第二道纵深(`mesh_inbound.go:140-144 ClassCarrierAllowed`)随入站帧路径一并删除——B 侧不再有"载体"概念,进来的都是已认证 peer 的 `mesh-exit`。

所以类别**不跨线**,降为 **A 侧本地谓词**。`From` 同理——身份由公钥承载。

> `InferClass` / `WithClass` / `WithInheritedHeader` 的 A 侧语义**保留**(`dns_forward.go:204` 用 `ClassCustom`、`egress.go:126` 用 `ClassSelf`),只是不再序列化。

## 5. 数据流(单跳)

```
A 命中 x.com,mesh:dev-b
  └─ ExitClient: Transport 选路(LAN 直拨 / piko)→ SSH 握手到 B:56432
       │    客户端身份 = customer.mesh.private_key
       │    服务端校验 = B 的 ssh_ingress.mesh_authorized_keys
       │    A 校验 B = peers[].host_key 钉扎(非 TOFU,fail-closed)
       └─ OpenChannel("mesh-exit", {"t":"tcp","h":"x.com","p":443})
            └─ B: serveChannels 认出 form=mesh → 构 Metadata{Route:RouteMesh, MeshInjected:true}
                 └─ tcphandler.GetHandler().Handle(...)  ← 与 bare 分支同一入口
                      └─ B 的规则 / MITM / socks5 / customer.proxy 全生效
                         (原 design §3.3 的**出口跳**语义零损失)
```

### 5.1 `ExitClient`(完整定义,评审 BLOCKER-2)

```go
// outbound/proxy/mesh/sshexit.go
type ExitClient struct {
    spec       PeerSpec
    transports []Transport          // 有序;LAN 优先,Piko 兜底(§7.3)
    identity   gossh.Signer         // 由 customer.mesh.private_key 解析(§10.5)
    hostKeyCB  gossh.HostKeyCallback // pinnedHostKey(spec.HostKey)(§10.4)
    timeout    time.Duration         // 握手 deadline,默认 10s
}

func (c *ExitClient) DialContext(ctx context.Context, md *M.Metadata) (net.Conn, error) {
    // 载体准入在拨号前判定(§4.3):custom 类别禁走公共中继。
    // 这是 Admit 的唯一调用点——漏掉它等于丢掉该安全约束。
    //
    // 类别是**逐连接**的(不是 peer 属性):沿用现行 `classFromContext`
    // (mesh.go:230),优先 ctx 里 egress/AI 侧显式 WithClass 的值,否则按
    // metadata 推断 ai/custom。PeerSpec 不携带类别(§7.0)。
    class := classFromContext(ctx, md)

    payload := exitPayload(md)
    if payload == nil {
        return nil, errUnbuildableTarget   // 不可构造目标:error,不发空 payload(§5.1.1)
    }

    // Transport 遍历:LAN 优先、Piko 兜底。逐个尝试,全部失败才回源。
    // 保留「LAN 全败 → 中继也不可用」因果链(现行 mesh.go:167-176 同款):
    // 两段失败同报,排障时能区分「对端 LAN 不可达」与「中继端点未放行」。
    // 错误用 %w 包住其中一个,保住上层 errors.Is/As(mesh.go:157/175 既有约定)。
    var raw net.Conn
    var lanErr, relayErr error
    var admitted bool
    for _, t := range c.transports {
        if !t.Admit(class, c.spec) { continue }   // 准入在拨号前,不消耗拨号预算
        admitted = true
        conn, err := t.DialSSHD(ctx, c.spec)
        if err == nil { raw = conn; break }
        if t.Name() == "piko" { relayErr = err } else { lanErr = err }
    }
    if raw == nil {
        // 可达的两种场景:① custom 类别 + relay-only peer(无 LAN 地址)
        // ② 有 LAN 地址但全部拨号失败且中继不可用。前者 errNoTransportAdmits,
        // 后者带因果链。空地址 peer 由 LANTransport.Admit 拦下,归入 ①。
        if !admitted { return nil, errNoTransportAdmits }
        if relayErr != nil && lanErr != nil {
            return nil, fmt.Errorf("mesh: no SSH transport to %s (lan_last_err: %v, relay_last_err: %w)",
                c.spec.DeviceID, lanErr, relayErr)
        }
        return nil, fmt.Errorf("mesh: no SSH transport to %s: %w", c.spec.DeviceID, pickErr(lanErr, relayErr))
    }

    // 关键:gossh.NewClientConn 不吃 ClientConfig.Timeout(该字段只对
    // gossh.Dial 的 TCP 段生效,见 chain.go:157-160 的既有注释),
    // 且 ctx 取消不会中断阻塞读。必须自己设 deadline,
    // 否则对端半开/慢握手会把 AI 主路径挂到进程退出。
    if err := raw.SetDeadline(time.Now().Add(c.timeout)); err != nil {
        _ = raw.Close()
        return nil, err
    }
    cc, chans, reqs, err := gossh.NewClientConn(raw, c.spec.DeviceID, &gossh.ClientConfig{
        User:            c.spec.SSHUser,       // 固定 "mesh"
        Auth:            []gossh.AuthMethod{gossh.PublicKeys(c.identity)},
        HostKeyCallback: c.hostKeyCB,
    })
    if err != nil { _ = raw.Close(); return nil, err }
    _ = raw.SetDeadline(time.Time{})           // 握手完成,清 deadline
    client := gossh.NewClient(cc, chans, reqs)

    ch, chReqs, err := client.OpenChannel("mesh-exit", payload)
    if err != nil { _ = client.Close(); return nil, err }
    go gossh.DiscardRequests(chReqs)           // 必须消费,否则连接挂死

    return newExitConn(ch, client), nil        // Close 时连带关 client
}
```

**归属与顺序(钉死,不留二义)**:`ExitClient` 持有 `[]Transport`,遍历与 `Admit` 都在 `DialContext` 内完成;`Peer` 只做 `PeerSpec` 的装配(**不缓存 `ExitClient`、不持 Transport 切片**,见下条)。顺序恒为 **LAN 在前、Piko 在后**(与 §3 表、现行 `mesh.go:129-167` 的 LAN→relay 顺序一致)——Piko 在前会把同网段快路径降级成"先绕公网中继再回头"。

两个错误变量就地定义在包内:`var errUnbuildableTarget = errors.New("mesh: target unbuildable from metadata")`、`var errNoTransportAdmits = errors.New("mesh: no transport admits this peer/class")`(名字刻意不含 "class"——它同时覆盖「无 LAN 地址」这种非类别原因)。`pickErr(lanErr, relayErr)` 是 3 行小助手:返回第一个非 nil 的错误。

**类别逐连接解析,不落 `PeerSpec`**:沿用现行 `classFromContext(ctx, md)`(`mesh.go:230-238`)——先看 ctx 里的 `WithClass`,没有就按 metadata 推断 ai/custom(`InferClass` 同源)。这是**必须**的:同一个 peer 在不同连接上承载不同类别的流量,把 Class 固化进 `PeerSpec` 会让 `Admit` 判定失真(custom 流量会在 ai 的 peer 上被放行到公共中继)。

**`ExitClient` 的生命周期(钉死)**:`identity`(解析私钥)与 `hostKeyCB`(解析钉扎)都是构造期一次性的,逐拨号重解析是无谓开销,故 `ExitClient` **必须缓存**。缓存**不能挂 `Peer`**——`getMeshProxyForExecution`(`handler.go:1476-1497`)每次拨号都 `meshproxy.NewPeer(target)` 造一个新的,挂在 `Peer` 上等于逐拨号新建。

**正确落点**:缓存挂在 `processor/tcp` 的 `TCPConnectionHandler` **结构体字段**上(`GetHandler()` 是 `sync.Once` 单例,见 `singleton.go:21`),`map[string]*meshproxy.ExitClient` + `sync.RWMutex`。**不放包级变量**——包级变量在 `ResetHandler`(测试用)后仍然存活,会造成测试间串味;结构体字段随实例一起重置。键与失效见 §10.4 末的「缓存键与失效」。

**`Transport` 的错误归属可改用类型标记**:`t.Name() == "piko"` 是字符串耦合;实施时可在 `Transport` 接口加 `IsRelay() bool`(2 行),让错误分桶不再依赖名字字面量。不是阻塞项。

- `OpenChannel` 返回的 `<-chan *Request` **必须被消费**,否则挂死(`x/crypto/ssh` `connection.go:61-63` 原文 "The Go channel must be serviced, or the connection will hang");
- **OpenChannel 也有 deadline 窗口(实施期加固,评审 I1)**:握手清 deadline 后 `OpenChannel` 本身仍是无 ctx 阻塞读——对端握手完成后黑洞(banner 交换后断流)会把调用方 goroutine 永久挂死,等于把 deadline 要防的死法从握手期挪到开通道期。实施版在 `OpenChannel` 前后设/清第二个 deadline 窗口(复用同一 timeout),`DialMeshAddr` 同款;§5.1 代码块中「握手完成,清 deadline」一行按此语义读;
- **`exitConn` 自写**,不"复用 `channelConn`"——后者是 `package ssh` 未导出(`inbound/ssh/channel.go:102`),且服务端版三个 `SetDeadline` 是 no-op。参照其先例(`:102-121`)自写,保留对 `*gossh.Client` 的所有权;
- `ConnID` 用 `fmt.Sprintf("sshing-%d", atomic.AddUint64(&meshExitConnCounter, 1))`——客户端 `OpenChannel` 返回的 `ch` **没有导出 channel id**,不能拼真实 id;
- **单连接单 SSH client,不做连接池**:每条 `mesh-exit` 连接一次握手,`ExitClient` 可复用但 `gossh.Client` 不复用。SSH 层的连接复用(`outbound/proxy/ssh/pool.go`)需要 channel 多路复用与泄漏回收,是**独立一批**的事,不在本期。**单跳下 `outbound/proxy/ssh/chain.go` 完全不被调用**(§5.4)。

#### 5.1.1 `exitPayload` 必须拆端口(评审 BLOCKER)

`md.HostName` **不保证是裸域名**:CONNECT 路径下它本身就是 `host:port` 形态。依据是 `outbound/proxy/mesh/mesh.go:346` 的既有处理——`targetFromMetadata` 先 `meshproto.SplitTarget(host)` 探一次,`port != ""` 就**原样返回 host**,注释原文:「HostName 已含端口(如 CONNECT 的 host:port 形态),直用」。

若 `exitPayload` 直接抄 `h = md.HostName`、`p = md.DstPort`,会产出 `{"h":"x.com:443","p":443}`;B 侧按 `net.JoinHostPort("x.com:443", 443)` 拼出 `[x.com:443]:443`,**全部 mesh 出口连不上**。

**正确做法:把 `targetFromMetadata` 的逻辑原样搬成 `hostPortFromMetadata`**(它返回 `host:port` 字符串,正是 `net.SplitHostPort` 的输入形态),`exitPayload` 只在它之上拆一次。`targetFromMetadata` 本身随帧协议一起删除(§12),逻辑不丢:

```go
// hostPortFromMetadata 由 targetFromMetadata(mesh.go:331-354)原样搬迁,
// 唯一改动是返回值由「拼接后的 target 串」改为「(host, port) 二元组」。
// 三种输入形态必须保持原行为:
//   HostName 已含端口(host:port)  → 原样拆分
//   HostName 裸域名 / 裸 IP        → DstPort(0 → 443)
//   HostName 空 + DstIP 有效       → DstIP:DstPort(0 → 443)
func hostPortFromMetadata(md *M.Metadata) (host, port string, ok bool) {
    if md == nil { return "", "", false }
    h := strings.TrimSpace(md.HostName)
    if h == "" {
        if !md.DstIP.IsValid() { return "", "", false }
        h = md.DstIP.String()
    }
    // HostName 自身可能已带端口:先按 host:port 解析,成功后直接采用。
    if ph, pp, err := net.SplitHostPort(h); err == nil && pp != "" {
        return ph, pp, true
    }
    p := int(md.DstPort)
    if p == 0 { p = 443 }
    return h, strconv.Itoa(p), true
}

func exitPayload(md *M.Metadata) []byte {
    if md == nil { return nil }                // 防御:nil md 不可构造目标
    // dns: 特殊目标在 hostPortFromMetadata 之前判定,否则 "dns:" 会被
    // SplitHostPort 判失败、再被拼成 "dns::443"。
    if meshproto.IsDNSTarget(strings.TrimSpace(md.HostName)) {
        return []byte(`{"t":"dns"}`)          // 无 h/p,见 §4.1 / §9
    }
    host, portStr, ok := hostPortFromMetadata(md)
    if !ok { return nil }                      // 调用方按「无法构造目标」拒绝
    port, err := strconv.ParseUint(portStr, 10, 16)
    if err != nil || port == 0 { return nil }  // 端口非法同样按不可构造处理
    b, _ := json.Marshal(exitMeta{T: "tcp", H: host, P: uint16(port)})
    return b
}
```

> `exitMeta.P` 是 `uint16`(JSON 数字),与 §4.1 的 `{"p":443}` 一致。**不要把它写成字符串**——B 侧按 `uint16` 反序列化,字符串会直接解析失败。

**与旧实现的两点差异(必须知晓,不是笔误)**:

1. 旧 `targetFromMetadata` 用 `meshproto.SplitTarget(host)` 探测端口,新写法用标准库 `net.SplitHostPort`。差异在裸 IPv6 字面量:`SplitTarget` 认识 `[::1]:443` 也认识不带中括号的形态,`SplitHostPort` **只认带中括号的**;不带中括号的裸 IPv6 会走到 `JoinHostPort` 分支被正确加括号。**结果一致**,但新写法依赖"不加括号的 IPv6 会被当作 host 处理"这一路径,§15.1 必须有裸 IPv6 用例钉住。
2. `exitPayload` 返回 `nil` 表示"目标不可构造",**`ExitClient.DialContext`(§5.1)在拨号前检查 nil 并直接返回 error**,不能发出空 payload——B 侧会当作畸形帧拒绝,错误信息反而更难读。

§15.1 的 ExtraData 用例要覆盖 `HostName` 的四形态:裸域名 / `domain:port` / 裸 IPv4 / 裸 IPv6。

### 5.2 拨号端口(已核实)

`ssh_ingress.port` 语义(`config/types.go:1071-1073` 的 0→默认归一 + `customer_config_service.go:366-371` + `server.go:44-48`):

| `port` | 监听形态 | mesh 可达面 |
|---|---|---|
| `0`(或 56442 哨兵) | 不自持监听,由 56432 统一口分派 | **56432**(绑定面见 §5.5) |
| `>0` | 另开专用监听,地址 = `ServiceBindHost():port` | **仍须填 56432** |

**关键事实**:`reconcile.go:98-101` 注释明确"专用口监听 `ServiceBindHost`(**默认 127.0.0.1**)…跟随统一口时可达面继承 56432 绑定策略";`server.go:109-118` 用 `config.ServiceBindHost()` 构造 `net.Listen`。

所以:

- `peers[].lan_addrs` **一律填对端 `IP:56432`**,即使对端配了专用口——专用口默认只听回环,LAN 根本拨不进来;
- `Port>0` 时 `server.go:123` 日志明确 "dedicated listener on %s (unified 56432 dispatch also active)",两个监听同时活着,走 56432 无损失;
- **删除 `lanDialCandidates` 的双端口展开**(`outbound/proxy/mesh/mesh.go:203-209`)——`config.DefaultHTTPProxyPort`=`56432`(`defaults.go:16`),旧展开会先试一个必然失败的口;
- LAN beacon 广播的就是 56432(`mesh_assembly.go:211`)。

### 5.3 可用性(分形态)

| `ssh_ingress.port` | mesh 入站可用性 |
|---|---|
| `0` | `ssh_ingress.enable` ∧ **代理运行态**(56432 生命周期,2026-11-09 spec D4)∧ **`mesh.forward_enable`** |
| `>0` | `ssh_ingress.enable` ∧ **`mesh.forward_enable`**(专用口随入口开关存活,与代理运行态无关,`server.go:44-48`)——**但仅在 `--host` 放宽到 LAN 可拨时才是独立入口** |

> **`port>0` 行的前置条件(不可省)**:专用口默认绑 `ServiceBindHost`=**127.0.0.1**(`reconcile.go:98-101`),而 §5.2 规定 `lan_addrs` 一律填对端 `IP:56432`。所以默认绑定下,对端能拨到的仍只有 56432,**可用性等同于 `port=0`**。只有运维显式把 `--host` 放宽到 LAN 可拨、且把专用口写进 `lan_addrs` 时,该行才成立。漏掉这个前提会得出"配了专用口就不怕停代理"的错误结论。

**新增的可用性收窄只发生在默认形态(port=0)**:手动停止代理 = 56432 关闭 = mesh 入站一并下线。§14.1 发布说明必须写明,并注明专用口形态不受此影响。

> `forward_enable` 这一列必须真的被接线,否则整张表失真——见 §10.1。

### 5.4 为什么不复用 `outbound/proxy/ssh`(已核实的三条硬冲突)

| 冲突 | 事实 |
|---|---|
| **host key 钉扎** | `chain.go:169` 硬编码 `HostKeyCallback: d.hosts.CallbackFor(n.hostPort())`;`NodeConfig`(`chain.go:18-25`)无 HostKey 字段;`KnownHosts.CallbackFor`(`known_hosts.go:36-63`)查不到就 append(TOFU) |
| **非 `direct-tcpip` channel** | `outbound/proxy/ssh/ssh.go:122` 硬编码 `OpenChannel("direct-tcpip", payload)`;`pool`/`chain`/`cascadeClient`/`acquire` 全小写不导出,包外拿不到 `*gossh.Client` |
| **池语义** | `outbound/registry.go:170-176` 按 `customer.proxy` **单实例**注册 `ssh`。mesh 需要**每 peer 一条独立链**(不同私钥、不同 host key、不同端口)。共用会让 `known_hosts` 混合、钉扎无法按 peer 区分 |

**结论**:`outbound/proxy/mesh` 内新增 `ExitClient`(约 150 行)。**`outbound/proxy/ssh` 整个包零改动**——单跳不走 `chainDialer`。

代价:多写约 40 行建链样板(含 §5.1 的 deadline 处理)。收益:三条冲突一次性消失,`registry` 不参与、`known_hosts` 不被污染。

### 5.5 LAN 可达面改由 `ssh_ingress` 决定(评审 MAJOR-1)

`defaults.go:92-99` 现在这样决定 56432 的绑定面:

```go
func UnifiedIngressListenAddrFor(cfg *Config) string {
    host := ServiceBindHost()                    // 默认 127.0.0.1
    if cfg != nil && cfg.Customer != nil && cfg.Customer.Mesh != nil &&
        cfg.Customer.Mesh.EffectiveEnabled() {
        host = "0.0.0.0"                         // ← 老语义:mesh 开 = 绑全网
    }
    return net.JoinHostPort(host, fmt.Sprintf("%d", DefaultHTTPProxyPort))
}
```

新模型下 LAN 可达面应由 **`ssh_ingress`** 决定,这行会造成两个方向同时错:

- 开 `ssh_ingress` 但关 mesh(§10.3 允许,只单向约束)→ 56432 只绑回环 → `lan_addrs` 全拨不通;
- 开 mesh 但**没开** `ssh_ingress` → 56432 白绑 `0.0.0.0`,把 HTTP 代理面暴露到 LAN。

#### 5.5.1 换判据不是一行改完(评审 MAJOR-5:对称风险)

把判据换成 `SSHIngress.IsEnabled()` 会带来**对称的新风险**:开 SSH 入口、关 mesh 的用户,56432 绑 `0.0.0.0` 后,**HTTP CONNECT 面也一并暴露到 LAN**。虽然 `inbound/http/server.go:196 AcquireProxyLease` / `CanProxy` 仍要求会话租约,但「只想用 SSH 入口的人被动把 HTTP 代理开到 LAN」不是用户预期的。

**定案:A. 判据换成 `SSHIngress.IsEnabled()`,并在发布说明明写该对称风险。**

理由:
- 旧判据(看 mesh)在新模型下**两边都错**,必须动;
- 拆绑定面(SSH 自己监听、56432 只绑回环)是**更大**的设计改动,会把 2026-11-09 统一入口的整体收益折回去,且需要另开一批;一期不做;
- 会话租约是既有硬闸 —— LAN 上无会话者拿不到代理。风险等级 = 「有会话的 LAN 邻居可用你的 HTTP 代理」,与今天 mesh 用户的既有处境同档。

发布说明两条都要写(§14.1):开 mesh 关 ssh → 不再白暴露;开 ssh 关 mesh → HTTP 代理面随 SSH 入口一起暴露到 LAN(会话租约仍在)。

#### 5.5.2 调用方清单(已核实)

`ReconcileUnifiedIngressBinding` 的真实调用点:

| 位置 | 性质 |
|---|---|
| `app/http/services/customer_config_service.go:190`(实现在 `:297-310`) | 提交回调,主路径 |
| **`cmd/core.go:196`** | **daemon `initializeCoreSubsystems` 主路径** |
| `cmd/start.go:126` | 命令行启动 |
| `cmd/tray.go:80` | 托盘启动 |

`app/http/services/run.go:44` **不是调用点**,只是一段说明注释。

**必须同步改的注释(不是功能,但不改会误导下一个人)**:

- `customer_config_service.go:297-305` 的「刻意的『模式无关』:绑定面只由 mesh 总开关决定(D2)」——改判据后这句失效;
- `defaults.go:92-99` 函数自身的注释;
- `run.go:44` / `cmd/core.go:196` / `cmd/start.go:126` / `cmd/tray.go:80` 四处提及「随 mesh 总开关变化」的说明。

> 功能上所有调用方都走 `defaults.go`,判据改一处即全局生效。上表是**注释同步**清单,不是功能改动清单。

## 6. 防环

### 6.1 静态检查(保存期)

全部落在 `MeshConfig.Validate`(`types.go:912`)或同位的提交路径校验里,**保存时拒绝,不在运行期静默降级**:

| 检查 | 位置 | 文案要点 |
|---|---|---|
| 目标节点 = 本机 | `ValidateMesh`(既有,`types.go:958`) | 已有的"self"拒绝 |
| `mesh:<deviceID>` 的 deviceID 必须存在于 `customer.mesh.peers` | `ValidateMesh` | 新 |
| 启用 mesh 且 peers 非空 → `private_key` 必填 | `MeshConfig.Validate` | 新(§10.5) |
| 每个 peer 的 `host_key` 必填 | `MeshConfig.Validate` | 新(§10.4,MAJOR-3) |
| **`fallback.enable` 为真且 `fallback.addr` 非空 → `fallback.host_key` 必填** | `MeshConfig.Validate` | 新(**MAJOR-10**;与 §10.4 的 fail-closed 配套——否则兜底路径永远连不上,且错误在连接期才暴露) |
| `toNode:<id>:fallback:<id2>` 的 `id2` 同样纳入 host_key 能力校验 | `routing_schema.go:179-186` | 新(§13.1) |

**新字段必须同样 `strings.TrimSpace` 就地规整(MAJOR-16)**。`MeshConfig.Validate` 现有代码对 `peer.DeviceID`(`:922`)与 `peer.LANAddrs[j]`(`:930`)做了就地规整,注释明说「校验与落盘同一份值,不留给下游『校验过的形态≠存储的形态』的缝」。新字段照办:

```go
peer.HostKey = strings.TrimSpace(peer.HostKey)
if peer.HostKey == "" { return fmt.Errorf("customer.mesh.peers[%d].host_key required (peer host key must be pinned, no TOFU)", i) }
// ...
c.PrivateKey = strings.TrimSpace(c.PrivateKey)
c.KeyPassphrase = strings.TrimSpace(c.KeyPassphrase)   // 注意:passphrase 有意的首尾空格极罕见,与 private_key 同规整可接受
if c.Fallback != nil && *c.Fallback.Enable == true {
    c.Fallback.HostKey = strings.TrimSpace(c.Fallback.HostKey)
    // fallback.addr 非空时 host_key 必填(空 addr 已在既有分支拒)
}
```

> `KeyPassphrase` 的 TrimSpace 有一个真实代价:若用户 passphrase 真的以空格开头/结尾,会被静默改掉导致解不开钥。**接受**——与 `proxy.key_passphrase` 现有的处理口径一致,且 passphrase 首尾空格本身是配置卫生问题。但**必须在 §10.5 写明**这一点,否则是"配了但连不上"的隐坑。

### 6.2 运行期:注入标记(评审 BLOCKER 2 修正)

**不能复用 `Metadata.Route`**。`processor/tcp/tls.go:343` 在路由决策时把 `metadata.Route` 覆写成 `toNode:<deviceID>` 并返回 `RouteToNode`;`handler.go:726 dialByRoute → :740 return h.dialViaMesh` 读到的 `Route` 已是决策串,`RouteMesh` 早被覆盖。

修法:新增**不会被决策覆写**的字段。

```go
// inbound/tun/metadata/metadata.go — 追加到 Metadata
// MeshInjected 标记本连接由 mesh-exit 注入(公钥认证的对端转发而来)。
// 路由决策全程不写该字段(tls.go:343 只写 Route),故注入标记可存活到
// dialViaMesh。json:"-" 不入任何线格式。
MeshInjected bool `json:"-"`
```

**证据(已按实况修正)**:主链 `Handle`(`handler.go:302`)之后全程透传同一 `*M.Metadata`,到 `handleTLS`/`handleNonTLS` → `dialByRoute` **无重建**。全仓其余 15 处 `&M.Metadata{...}` 重建点均为各入站入口或旁路:

`egress.go:127`、`tls.go:190`、`relay.go:487`、`mesh_inbound.go:485`、`dns_forward.go:199`、`dialer_resolver.go:77`、`socks5.go:228`、`tunnel/tcp.go:34`、`tunnel/udp.go:24`、**`inbound/ssh/channel.go:73`(新注入点的同构先例)**、`mesh_inject.go:153`、`inbound/http/handler.go:74`、`metadata_extractor.go:17,76`、`shadowsocks.go:256`。

其中只有 `inbound/ssh/mesh_inject.go` 是新注入点,必须自行置 `MeshInjected = true`。

注入时置 `md.MeshInjected = true`;以下三处 dial 入口检查并拒绝(经核实为完整集合):

1. `dialViaMesh`(`handler.go:1400` 顶部)——这一处同时覆盖内部的 `fallback:<id2>` 分支(`:1455-1467` 直接调 `fbPeer.DialContext`);
2. `dialMeshFallbackNode`(`handler.go:1519`,调用点 `:492`、`:1428`)——离线兜底;
3. `meshOfflineRedirectForAI`(`route_override.go:52` 函数体,实参在 `handler.go:491`)——AI 离线改道。

拒绝文案:`"mesh: injected traffic must not re-enter mesh (loop guard)"`,错误回源。

因为链条由配置期定死,**该限制无损**:需要 A→C→D 就配成对应节点。

**两条已核实的非环路路径,实施时明确不改**:

- `processor/egress/egress.go:127 platformDialContext` 新建 Metadata——只服务 `PlatformClient` 自身流量,不经入站管线;
- `inbound/tun/tunnel/dns_forward.go:199-205` 同样新建——是链头自己的 TUN DNS 代转发,合法。**B 侧收到的 `dns:` payload 不进管线(§4.1 表"否")**,故不存在 DNS 二跳。

**一处顺手加固(可选,非必需)**:`processor/rules/engine.go:350` 的"跳过域绑定缓存"名单目前只有 `RouteToNode`/`RouteMeshFallback`,不含 `RouteMesh`。

但**这个加固是纯冗余**(MAJOR-7 核实):`StoreBinding` 的调用点只有两处(`handler.go:404`、`:468`),两处都在 `metadata.DNSInfo != nil && DNSInfo.ShouldCache` 的守卫之内;而 `DNSInfo` 只在 `inbound/tun/metadata/metadata.go:114,139` 两处 `SetHostName`/`SetIP` 内构造,mesh 注入路径(`inbound/ssh/mesh_inject.go:153-158` 与未来 `ExitClient` 对端)是**直接赋值 `HostName`**,不走 `SetHostName`,`DNSInfo` 恒 nil。所以 `RouteMesh` 根本到不了那个 switch。

**结论:不加**。理由是把"当前不可达"的防御写进代码,等于给下一个人一个"这里已经有防护了"的错误信号,反而掩盖了真正的约束(`DNSInfo` 由 `SetHostName` 构造)。改为在 `engine.go:350` 上方补一行注释说明这个隐含前提,比加分支更诚实。

> 若将来真要给注入连接补 `DNSInfo`(例如为了补齐域绑定缓存),那一刻必须同时加 `RouteMesh` 分支 —— 这条注释就是给那个时候的人看的。

### 6.3 删除的运行期防线

`AppendHop` / `Via` / `Hops` / `MaxHopsCap` / `DefaultHops` / 环路自查 / 跳数帽:

- `outbound/meshproto/header.go`(`appendHop`/`via`/`hops` 等,**均为未导出**);
- `inbound/ssh/meshframe.go`(整文件删);
- `processor/tcp/mesh_inbound.go`(三层防线随文件删);
- `inbound/ssh/mesh_inject.go`(Via 自查与跳数帽);
- `ContextKeyIngressFrameMeta` / `InheritedHeaderFromContext` 两套断链接缝。

`customer.mesh.max_hops` 字段**保留**:从运行期跳数帽降为**保存期链长度上限**(一期单跳恒为 1,无实际约束力,保留以兼容二期形态)。常量 `DefaultHops`/`MaxHopsCap` **留在 `outbound/meshproto`**(§12.1)。

## 7. Transport 抽象(打洞的缝)

### 7.0 `PeerSpec`(接口闭合)

```go
// outbound/proxy/mesh
// PeerSpec 一个 mesh 出口节点的全部拨号事实(配置期确定,运行期只读)。
//
// 注意:**没有 Class 字段**。流量类别是**逐连接**的,不是节点的属性——
// 同一个对端会同时承载 self/ai/custom 三类流量(现行 mesh.go:94 的
// `class := classFromContext(ctx, md)`:优先 ctx 的 WithClass,否则按
// metadata 推断)。把它塞进 PeerSpec 会让「这个 peer 属于哪一类」成为
// 一个无解的问题。类别在 ExitClient.DialContext 内按连接解析(§5.1)。
type PeerSpec struct {
    DeviceID string   // 对端 mesh 身份 ID(也是 SSH 握手的 host 标签)
    LANAddrs []string // 对端 SSH 入口地址(默认 IP:56432,见 §5.2)
    HostKey  string   // 对端 SSH host key 钉扎;空 = fail-closed(§10.4)
    SSHUser  string   // 固定 "mesh"
    Relay    bool     // true = 允许中继 Transport
}
```

`HostKey` **不在 Transport 里消费**:`DialSSHD` 只回 conn,握手归 `ExitClient`(§5.1),钉扎以 `cfg.HostKeyCallback` 形式交给 SSH 层。

### 7.1 Transport 接口

```go
// Transport 提供一条到对端 sshd 的、尚未握手的 net.Conn。无状态,可跨拨号复用。
type Transport interface {
    Name() string                                        // "lan" / "piko" / 未来 "udp"
    Admit(class string, peer PeerSpec) bool              // 载体准入:custom 禁走公共中继;LAN 还需有地址
    DialSSHD(ctx context.Context, peer PeerSpec) (net.Conn, error)
}
```

| Transport | 拨号目标 | `Admit(class, peer)` |
|---|---|---|
| `LANTransport` | `peer.LANAddrs` 顺序尝试 | `len(peer.LANAddrs) > 0`(全类别)——无地址时返回 false,让空地址的 peer 落到 `errNoTransportAdmits`,而不是报一个含 0 个地址的费解错误 |
| `PikoTransport` | 私有常量 `meshRelayEndpointSuffix`(`mesh.go:36-38`)拼出 `<deviceID>.mesh`,再调注入的 dial func | `class ∈ {self, ai}`(切片里已按 `Relay` 排除了未开中继的 peer,故此处只需判类别) |
| `UDPTransport`(未来) | 打洞后的 UDP 承载 | 全类别 |

> 已核实的签名:注入的 dial func 是 `PikoDialFunc func(ctx, endpointID string) (net.Conn, error)`(`mesh.go:34`,`app/tunnel/mesh_dial.go:83-99` 的 `dialMeshEndpoint` 同签名)。**入参是拼好的 endpointID,不是裸 `deviceID`** —— 拼装责任在 `PikoTransport`。

### 7.2 离线兜底的裸地址入口

离线兜底(`customer.mesh.fallback.addr`)是一个**无 peer 的裸 host:port**(`handler.go:1519-1526` 现经 `PeerTarget{DeviceID:"mesh-fallback", LANAddrs:[addr]}` 直拨)。新模型下需要专门入口,**保持 LAN-only 语义**:

```go
// DialMeshAddr 拨一个无 peer 记录的裸地址(离线兜底专用)。
// addr 指向对端 **SSH 入口 56432**,不是已删除的 mesh 监听口(§14.2 第 6 项)。
// hostKey 来自 customer.mesh.fallback.host_key;空 = fail-closed(§10.4)。
func DialMeshAddr(ctx context.Context, addr, hostKey string) (net.Conn, error)
```

`dialMeshFallbackNode`(`handler.go:1519`)改为调它。**已核实与新增函数无命名/语义冲突**(前者是 `*TCPConnectionHandler` 方法)。

**hostKey 从哪来(必须钉死,否则兜底路径的钉扎恒为空)**:
`config.MeshFallbackActiveForClass(cfg, class) (addr string, ok bool)`(`types.go:885`)现有签名**只回地址**,`meshFallbackAddrForClass`(`handler.go:1510-1512`)与 `dialMeshFallbackNode(ctx, md, addr)` 手里都没有 host_key 来源。

**决策:不扩 `MeshFallbackActiveForClass` 的返回值**(它有 **3 个生产调用方**(`egress.go:100`、`route_override.go:56`、`handler.go:1512`)+ **10 处测试断言**(全在 `mesh_fallback_gate_test.go`),扩三元组是纯噪声)。改为新增一个 3 行的只读助手,由 `dialMeshFallbackNode` 自己取:

```go
// processor/config/types.go — 新增
// MeshFallbackHostKey 返回兜底节点的 SSH host key 钉扎(未配置=空串=fail-closed)。
func MeshFallbackHostKey(cfg *Config) string {
    if cfg == nil || cfg.Customer == nil || cfg.Customer.Mesh == nil || cfg.Customer.Mesh.Fallback == nil {
        return ""
    }
    return strings.TrimSpace(cfg.Customer.Mesh.Fallback.HostKey)
}
```

`dialMeshFallbackNode` 改为:

```go
func (h *TCPConnectionHandler) dialMeshFallbackNode(ctx context.Context, md *M.Metadata, addr string) (net.Conn, error) {
    return meshproxy.DialMeshAddr(ctx, addr, config.MeshFallbackHostKey(config.GetGlobalConfig()))
}
```

> 走共享助手而不是在 `dialMeshFallbackNode` 内就地拆 nil 链:同一段 4 层 nil 判护在 `MeshFallbackActiveForClass` 里已写过一遍,再抄一遍就是两份会漂移的副本。

> **语义变更必须写进发布说明**:今日 `fallback.addr` 指的是对端裸 mesh 帧协议口;新模型下必须改填 SSH 入口 56432,否则拨通后 A 会拿 SSH 版本串去撞一个不存在的监听。

### 7.3 Transport 装配点(架构缺口补全)

| 事实 | 落点 |
|---|---|
| **piko dial func** | **保留现有缝,只改名**:`SetMeshPikoDialSource` → `SetMeshTransportDialSource(func() PikoDialFunc)`,消费方从 `PeerTarget.PikoDial` 改为构造 `PikoTransport{dial: fn}`。`processor/tcp` 仍不能 import `app/tunnel`(反向依赖),缝的存在理由不变。生产调用点唯一(`manager.go:335`)。**`fn == nil` 时构造期就必须拒绝**(`PikoTransport{dial: nil}` 是非法状态):`ExitClient` 组装时若 `spec.Relay && fn == nil`,直接不把 `PikoTransport` 放进切片(等价于 relay 未接线),让它落到 LAN-only 语义——现行 `getMeshProxyForExecution` 的 `PeerTarget` 字面量里就是 `if dial := meshPikoDialFromSource(); dial != nil { target.PikoDial = dial }`(`handler.go:1494-1496`),不要退化成"admit 后 nil-panic" |
| **`PeerSpec` 构造** | `processor/tcp/handler.go:1476-1500 getMeshProxyForExecution` 是**唯一构造点**(现读 `config.GetGlobalConfig().Customer.Mesh.Peers`),必须重写为构造 `PeerSpec`。**签名不变**(仍收 `deviceID`,不接 `ctx`/`md`)——类别**不进** `PeerSpec`(§7.0),由 `ExitClient.DialContext` 逐连接解析 |
| **`ExitClient` 归属与缓存** | 缓存挂在 `TCPConnectionHandler` 的**结构体字段**上(不是 `Peer`——`Peer` 是逐拨号新建的;也不是包级变量——会跨 `ResetHandler` 存活,见 §5.1「生命周期」),`map[string]*ExitClient` + `sync.RWMutex`;键含私钥摘要(§10.4 末)。每次拨号都构造新 `PeerSpec` 值(廉价拷贝),缓存按值键命中 |
| **Transport 实例归属** | 无状态,由 `ExitClient` 构造期按 `spec.Relay` 决定串哪个:**`relay=true → []Transport{LAN, Piko}`;`relay=false → []Transport{LAN}`**。顺序**恒为 LAN 在前**——与 §3 表、现行 `mesh.go:129-167` 的 LAN→relay 一致。遍历与 `Admit` 判定都在 `ExitClient.DialContext` 内(§5.1);`Peer` **不**持 Transport 切片,只做 `PeerSpec` 装配(不缓存 `ExitClient`) |

`app/tunnel/mesh_assembly.go:87-94 egressPeer()` 也构造 `PeerTarget`,同步改为 `PeerSpec`。**`outbound/registry.go` 不注册 mesh**(§5.4)。

## 8. piko 降为 Transport

`app/tunnel/manager.go:426-443` 现为 `upstream.Listen(ctx, "<id>.mesh")` → `meshRelay.Serve(listener, CarrierRelay)`。改为把 accept 到的连接喂 SSH 入口:

```go
meshListener, meshErr := upstream.Listen(ctx, MeshEndpointID(config.deviceID))
if meshErr != nil {
    reportMeshRelayDisabled(...)                 // 软失败语义不变
} else {
    endpoint := MeshEndpointID(config.deviceID)
    go func() { <-ctx.Done(); _ = meshListener.Shutdown() }()
    go func() {
        for {
            conn, err := meshListener.Accept()
            if err != nil { return }
            // 每条 yamux stream = 一条 SSH 连接,必须并发(§8.1);
            // conn 作参数传入避免闭包捕获。
            go func(c net.Conn) {
                // 导出名:sshin.WithRemoteLabel(package ssh 内实现)
                c = sshin.WithRemoteLabel(c, "piko-"+endpoint)   // 无冒号,见 §8.2
                if !sshin.HandleIngressConn(c) {
                    log.Printf("[MESH-RELAY] ingress not running, dropped conn %s", c.RemoteAddr())
                }
            }(conn)
        }
    }()
}
```

### 8.1 评审修正的四个引用错误

| 原稿 | 实际 |
|---|---|
| `server.go:331 现成的注入缝` | 对外入口是 `inbound/ssh/reconcile.go:173 HandleIngressConn(conn net.Conn) bool`;`Server.ServeConn` 是内部方法 |
| `acceptLoop(meshListener)` | 不存在这种导出函数;需自写 accept 循环(如上) |
| 同步调用 `HandleIngressConn` | **会阻塞到 SSH 会话结束**(`ServeConn:331 → handleConn:365`,非 goroutine),串行 accept 会让 B 侧退化为单并发 —— **必须每连接起 goroutine** |
| 静默丢弃 | `HandleIngressConn` 返回 `false`(= `reconcileSrv == nil`,入口未配置/未启动)时按 WARN 记账 |

### 8.2 限速串桶(成因与修法)

**实际成因**:yamux `Session.RemoteAddr`(`yamux@v0.1.6/addr.go:45-49` 的 `hasAddr` 探测)先探测底层 conn 是否实现 `hasAddr`;piko 的 `*websocket.Conn` **实现了** `RemoteAddr`(`piko@v0.10.0/pkg/websocket/conn.go:235`),所以拿到的是**底层 WebSocket 对端地址 = piko 服务器 IP:port**。于是所有中继 peer 的 `remoteIP`(`server.go:485-493`)都返回同一个 piko 服务器 IP → 共用一个限速键。

(原稿说 `RemoteAddr()` 恒为 `"yamux:remote"` —— 不准确。)

**修法的坑**:label **绝不能含冒号**。实测 `net.SplitHostPort("piko:dev-b.mesh")` 会成功(`host="piko"`, `port="dev-b.mesh"`, `err=nil`),于是 `remoteIP` 返回 `"piko"` —— 修法完全失效。正确写法 `"piko-<endpointID>"`,无冒号 → `SplitHostPort` 报错 → `remoteIP` 走 `return a.String()` 原样返回 → 限速键按 peer 隔离。

**已知副作用(必须写进测试钉住)**:包装后 `conn.RemoteAddr()` 变成 `piko-dev-b.mesh`,而 `netAddrToIPPort` 调用点(`injectBareStream` `channel.go:83`;`mesh-exit` 注入路径同款)会 `SplitHostPort` 失败 → **SrcIP/SrcPort 归零**,peer 来源在统计/审计面丢失。这与 piko 载体现状一致(今天也取不到真实 peer 地址),但**必须显式记录**而非默默回归。

**测试必须断言 `remoteIP(labeledAddr)` 的返回值**及 `SrcIP` 归零行为,不能只断言 `RemoteAddr()`(否则假绿)。

### 8.3 收益与边界

NAT 后的节点**在入站侧**照样能被当跳板(能力不丢)。注意:这只解决 hop 0 的 Transport 问题;`PikoTransport` 只作用于**发起方到第一跳**的拨号,不改变 §16 的多跳限制。

## 9. DNS 代转发(保留)

`mesh-exit` 的 payload 变体:`{"t":"dns"}`(无 `h`/`q`,理由见 §4.1 附注)。

B 侧读一个 `[2B 大端长度][DNS 报文]` 帧 → 解析 → 系统解析器 → 回 `[2B 长度][响应]` → 关 channel。

`processor/tcp/mesh_inbound.go` 以下函数**整段搬运,仅需导出化**(首字母大写;已逐一核对签名,全部不含 `meshproto.Header`):

- `resolveDNSQuery:270` / `resolveDNSQuestion:308` / `dnsRcodeForResolveError:296` / `dnsRRHeader:392`
- `dnsPTRNameToAddr:399` / `splitSRVName:431` / `servfailDNSResponse:443`
- `readDNSLengthFrame:237` / `writeDNSLengthFrame:255`

A 侧对偶实现 `inbound/tun/tunnel/dns_forward.go:258-282`,线格式 `[2B 大端][载荷]` 一致。

保留理由:design §6 层1 是离线场景地基——A 内核拿不到真实 IP 时 TCP 连接根本不会发起,域名直转没有触发机会。砍掉它,离线设备上的应用层 DNS 就死了。

**`dns:` payload 不进 B 的入站管线**(§4.1 表),因此不存在 DNS 二跳回环(§6.2)。

顺带修掉 §1.3 的分裂:SSH 载体不再拒绝 `dns:`。

> **现状记账(2026-11-12 接线完成)**:`SetMeshDNSForwardPeerSource` 已由 app/tunnel 装配接线(`wireMeshAssemblySeams` → `meshDNSForwardPeer`,规则驱动走 `routing.MeshDNSForwardTarget` 10s memo)。**激活按载体可用性分层**:relay-only peer 编译放行但运行期 fail-open(TUN DNS 恒 ClassCustom、PikoTransport 拒 custom——激活后只会 SERVFAIL 风暴);peer 有 LAN 地址才装配 ExitClient。§17 对应条目已闭。

## 10. 入站身份与 ACL

### 10.1 身份来源变更 + 门禁接线(评审 BLOCKER-1)

| | 旧 | 新 |
|---|---|---|
| 转发方身份 | 帧头 `from` 字段,**客户端自报** | SSH 公钥身份,**不可伪造** |
| 转发方 ACL | `customer.mesh.peers[].device_id` 字符串比对 | `ssh_ingress.mesh_authorized_keys` 公钥比对 |
| 门禁总开关 | `customer.mesh.forward_enable`(主闸在 `relay.go:85`) | **不变**,但接线点搬到 `ForwardGate` |
| 服务端身份(对端校验) | 无(LAN 明文裸 TCP) | `peers[].host_key` 钉扎(§10.4) |

**必须接线,否则 `forward_enable` 变成死开关**。今日 `forward_enable` 有两道闸:主闸 `app/mesh/relay/relay.go:85 deps.ForwardEnabled = mesh.EffectiveForwardEnabled()` 与旁路 `ForwardGate`(恒 nil = 全放行)。relay 删除后若不复用路径接线,只剩"公钥在 `mesh_authorized_keys` 里"这一道准入 —— 只要公钥被填入,该节点就**无条件**可用作出口,与"让渡型能力必须显式开启"(`types.go:850-855` 注释)的既定契约相反。

修法四条:

1. `RunConfig.ForwardGate` 签名收窄为 `func() bool`(`config.go:39-43`;只留总开关)。**`allowed []string` 名单职能整体删除** —— ACL 已由 `MeshKeys` 公钥列表承担,两套 ACL 并存只会制造"公钥进了但名单没加"的第二类失配。连带删除 `mesh_inject.go:187-195 forwarderAllowed` 与 `meshRejectNotAllowedFmt` 常量。
2. ~~`buildSSHIngressRunConfig` 的签名必须扩参~~**【实施期修正(六轮评审后质量评审 Critical 纠偏):闭包不得捕获按次提交的 `mesh` 参数,改读全局活值】**。原设计论证的漏洞:`Reconcile` 的 `runIdentity` 相等性比对(端口/凭证/host key 路径)**不含 ForwardGate**——只切 `customer.mesh.forward_enable`、不动 `ssh_ingress` 凭证/端口的常规仪表盘操作,会构造出携带新闭包的新 `RunConfig`,然后被 `Reconcile` 的幂等 no-op 短路:**旧 Server 继续持有旧闭包,门禁切换在进程重启前永不生效**。原稿「闭包不会跨越到下一次提交之后仍有意义的场景」的指针生命周期论证漏掉了这条 no-op 路径。
   修法:**签名不扩参**(维持单参),`ForwardGate` 闭包连接期读全局:

   ```go
   // ForwardGate 连接期读全局活值(实施期修正):按次提交捕获会被 Reconcile
   // 的身份相等 no-op 冻结在旧 Server 里(forward_enable 切换永不生效);
   // 全局读是真活视图——切了下一次连接就生效。GetGlobalConfig 读取有锁保护,
   // 并发提交窗口的「读到最新值」正是想要的语义,与 processor/tcp 消费路径
   // (getMeshProxyForExecution 等)同口径。nil 链 = mesh 未配置 = false(fail-closed)。
   gate = func() bool {
       cfg := config.GetGlobalConfig()
       if cfg == nil || cfg.Customer == nil || cfg.Customer.Mesh == nil {
           return false
       }
       return cfg.Customer.Mesh.EffectiveForwardEnabled()
   }
   ```

   依赖方向已核实可行:`inbound/ssh` 本就 import `processor/config`(`server.go` Start 用 `config.ServiceBindHost()`),无新环。
3. **`ForwardGate == nil` 的语义从「全放行」翻转为「fail-closed」**:`mesh_inject.go:94` 的 `if rc.ForwardGate != nil { ... }` 改为 `if rc.ForwardGate == nil || !rc.ForwardGate() { reject(forward-disabled) }`。
4. `ReconcileSSHIngressFromConfig` 的注释(`:280-283`)与 `buildSSHIngressRunConfig` 内的 `:358` 注释必须整段重写:这两段描述的是**本次改动前的合并缝**(「ForwardGate 留 nil=全放行(凭证列表即 ACL)——…随组网分支落地后改注入门禁函数」),改完后该语义已不存在,留着会误导下一个人把 nil 当放行。

**必须同步改的测试(3 处,都会编译失败或断言失效)**:

| 位置 | 现状 | 改法 |
|---|---|---|
| `app/http/services/ssh_ingress_reconcile_test.go:243-245` | `if rc.ForwardGate != nil { t.Fatal("ForwardGate must stay nil ...") }` | **删除该断言**,改为断言 `ForwardGate != nil` 且返回期望布尔(反向) |
| `inbound/ssh/mesh_inject_test.go:300-345 TestMeshFormForwardGate` | 表驱动四例,含 `{"nil gate allows (merge seam)", nil, ""}`;`gate func() (bool, []string)` | 签名改 `func() bool`;**`nil gate` 用例的期望从「过闸」翻转为「拒绝**」;删除 `"gate true without forwarder rejects"` 例(名单职能已删) |
| `inbound/ssh/mesh_inject_test.go:158` | 注释「ForwardGate nil=全放行(合并集成缝:本分支凭证列表即 ACL)」 | 改为「nil=fail-closed;生产恒注入活闭包」 |

`inbound/ssh` 包**不读 `customer.mesh`**——保持 `reconcile.go:1-5` 包注释声明的契约(配置由 app 层翻译成 `RunConfig`)。

**`ForwardGate` 与 `LocalNodeID` 都不参与 `runIdentity` 比对**(`reconcile.go:35-37` 注释已明确闭包按身份比对会退化成"每次提交必重启")。这意味着**改 `forward_enable` 不会重启入口实例**——正确,因为门禁是连接期求值的活闭包。

### 10.2 device_id 恢复

ACL 用公钥后,日志/审计仍需要人类可读身份。复用 OpenSSH authorized_keys 尾注释:

```
ssh-ed25519 AAAAC3Nza... dev-b
```

但**当前代码丢弃了 comment**——`app/http/services/customer_config_service.go:378` 是 `pub, _, _, _, err := gossh.ParseAuthorizedKey(...)`(第 2 返回值即 comment)。

修法:

```go
// inbound/ssh/config.go
type MeshKey struct {
    Key      gossh.PublicKey
    DeviceID string   // authorized_keys 尾注释;空则回落指纹前 16 字符
}
```

`RunConfig.MeshKeys []MeshKey`(**注意:字段名沿用 `MeshKeys`,但元素类型从 `[]gossh.PublicKey` 变为 `[]MeshKey`**);运行时 `localID`/日志用 `DeviceID`。

**连带改动面(全部必须同步,否则编译失败)**:

| 位置 | 现状 | 改法 |
|---|---|---|
| `inbound/ssh/server.go:283-298 publicKeyCallback` | `keyInList(key, s.rc.MeshKeys)` | `meshKeyFor(key, s.rc.MeshKeys)` 返回 `(MeshKey, bool)`,命中后把 `DeviceID` 一并放入 `Permissions.Extensions`(新增 `extMeshDeviceID` 键),供通道层取用 |
| `inbound/ssh/server.go:302-309 keyInList` | `[]gossh.PublicKey` | 拆两个:裸钥仍用 `keyInList`,mesh 用新的 `meshKeyFor` |
| `inbound/ssh/reconcile.go:37,50,55-70` | `meshKeys [][]byte` + `marshalKeyIdentity(rc.MeshKeys)` | `marshalMeshKeyIdentity([]MeshKey)` = `Marshal() + "\x00" + DeviceID` 逐项拼接后排序 |
| `app/http/services/customer_config_service.go:344,375-386` | `buildSSHIngressRunConfig`(`:344`)与 `parseSSHIngressAuthorizedKeys`(`:375`)返回 `[]gossh.PublicKey`,用于两个列表 | **拆两路**:`authorized_keys` 仍走原函数;`mesh_authorized_keys` 走新 `parseSSHIngressMeshKeys`(保留 comment) |
| `app/http/services/ssh_ingress_reconcile_test.go`(多处) | 构造 `MeshKeys: []gossh.PublicKey{...}` | 改为 `[]ssh.MeshKey{{Key: ..., DeviceID: ...}}` |
| **`inbound/ssh/reconcile_test.go:181,189`** | `MeshKeys: []gossh.PublicKey{meshPub}` (两处 `Reconcile(&RunConfig{...})` 字面量) | **必须同步**:加 `DeviceID` 字段,如 `[]MeshKey{{Key: meshPub, DeviceID: "dev-b"}}`。不同步 = 编译红 |
| **`inbound/ssh/server_test.go:365,388,432`** | `MeshKeys: []gossh.PublicKey{meshPub}` / `{dualPub}` (三处 `RunConfig` 字面量) | **必须同步**,同上。`:432` 的双钥用例还可顺手钉住"两个不同 DeviceID 各自透传" |
| `inbound/ssh/mesh_inject_test.go`(**8 处**:`:156,246,260,273,290,324,359,392`) | `MeshKeys: []gossh.PublicKey{meshPub}` | 该文件本就按 §13.4 整块重写(帧协议路径删除),`MeshKey` 化随重写一并落地 |

> 全仓非测试构造点只有一处 `app/http/services/customer_config_service.go:357`(已在上表)。**测试文件漏一个就是编译红**——`grep -rn "MeshKeys:" --include="*_test.go"` 应有且仅有上述 `reconcile_test.go`/`server_test.go`/`mesh_inject_test.go` 三个文件。

> `Permissions.Extensions` 多一个键是**进程内数据,不上线**——`Extensions` 由服务端 `PublicKeyCallback` 产生、服务端 `serveChannels` 消费,不参与任何线上协议。故不构成兼容性问题。

**`runIdentity` 需纳入 DeviceID**(`reconcile.go:36-42` 定义,`:50-51` 只比对 `key.Marshal()`)。当前只改注释(不改密钥字节)不触发重建 → 日志身份陈旧。比对改为 `Marshal() + DeviceID` 的哈希。

### 10.3 依赖关系(决策 #8)

开 mesh **必须**开 SSH 入口。但**两条路径的处置必须不同**(MAJOR-6):

| 路径 | 入口 | 处置 |
|---|---|---|
| 保存期 | `customer_config_service.go:119`(与 `ValidateMesh` 同位) | **硬拒**,返回 `CustomerConfigValidationError` |
| 启动期 | `cmd/config.go:219 ApplyConfig → cfg.Validate()`(`types.go:1440-1444`) | **降级 + Warn**,绝不阻断 boot |

**启动期不能硬拒**。存量用户的配置文件可能有 `mesh.enable=true` 且从未配过 `ssh_ingress`;若把依赖检查塞进 `Config.Validate()`,升级后 `ApplyConfig` 返回错误 → **整个 daemon 起不来**。这是配置校验最不该产生的结果(§14.3 承诺"不需要迁移"也会被推翻)。

- **落点**:新增 `Config.ValidateMeshDependency() error`,`*Config` 接收者(`types.go:958 ValidateMesh` 同款),类型边界内同时看得见 `Customer.Mesh` 与 `Customer.SSHIngress`;
- **保存期**在 `customer_config_service.go:119` 之后调用,`mesh.EffectiveEnabled() && !sshIngress.IsEnabled()` → 返回错误,文案「组网依赖 SSH 入口(ssh_ingress.enable)」;
- **启动期**不调这个函数,**改在 mesh 的「生效开关」上做降级**:新增 `Config.MeshEffectivelyEnabled() bool`(或等价命名),语义 = `Mesh.EffectiveEnabled() && SSHIngress.IsEnabled()`。**所有 mesh 消费方改调这个新方法**,让降级一处生效、不需要逐点改。已知消费方:`dialViaMesh` 决策路径、`meshForwardAvailable`(编译期,§13.2)、`MeshFallbackActiveForClass`(`types.go:869-890`)、`UnifiedIngressListenAddrFor`(§5.5,直接换成 `SSHIngress.IsEnabled()` 即可)。

  ```go
  // MeshEffectivelyEnabled mesh 出口的生效开关 = 用户开关 ∧ SSH 入口可用。
  // 依赖降级(§10.3):SSH 入口未启用时 mesh 无出口载体,恒 false——
  // 存量配置(mesh 开、ssh 未配)升级后 mesh 静默降级为关,而不是
  // 启动失败。保存期由 ValidateMeshDependency 硬拒,两条路径不重复报错。
  func (c *Config) MeshEffectivelyEnabled() bool {
      if c == nil || c.Customer == nil { return false }
      if !c.Customer.Mesh.EffectiveEnabled() { return false }
      return c.Customer.SSHIngress.IsEnabled()
  }
  ```

  > `MeshConfig.EffectiveEnabled()` 本身**语义不变**(nil 指针安全 + 三态指针,大量调用方在 `*MeshConfig` 语境使用)。新方法是在它之上加一层 `Config` 级依赖判定,不是替换。
  >
  > `MeshFallbackActiveForClass`(`types.go:885`)**接收者已是 `*Config`**,类型边界内看得见 `SSHIngress`,直接把内部 `:890` 的 `mesh.EffectiveEnabled()` 换成 `cfg.MeshEffectivelyEnabled()` 即可,签名不动。

  **需要改判据的调用方(已全量核实,共 5 处 + 1 处本就是新判据)**:

  | 位置 | 现有形态 | 改法 |
  |---|---|---|
  | `processor/config/types.go:890` `MeshFallbackActiveForClass` | 已有 `*Config` | 换 `cfg.MeshEffectivelyEnabled()` |
  | `processor/routing/snapshot_mesh.go:151` `meshForwardAvailable` | 已有 `*Config`(`:146` 签名) | 换新方法 |
  | `app/tunnel/mesh_assembly.go:202` | `cfg.Customer.Mesh.EffectiveEnabled()` | 换 `cfg.MeshEffectivelyEnabled()` |
  | `app/tunnel/mesh_assembly.go:73` | 局部 `mesh` 变量(beacon 装配) | 该处是 `customer.mesh` 视角,**保持 `EffectiveEnabled()`**——beacon 是否广播与本机能否作出口是两件事,不应被 SSH 依赖连坐 |
  | `app/http/handlers/mesh_handler.go:97` | 状态展示 `resp.Enable` | **保持 `EffectiveEnabled()`**——展示用户配置值,不是生效值;但同响应里补一个 `Effective` 字段区分两者(否则用户看到"已启用"却不通,没法定位) |
  | `processor/config/defaults.go:95` | 绑定判据 | 直接换成 `SSHIngress.IsEnabled()`(§5.5),不经过 mesh 方法 |

  这条降级必须写进发布说明(§14.1):**mesh 开但 ssh_ingress 未配的用户,升级后 mesh 不再生效**。

- 仪表盘:开组网卡片时若 SSH 入口未启用 → 卡片内提示并提供「一并开启」;不静默改写用户配置;
- 发布说明明示该依赖(§14.1)。

`peers[].device_id` 与 `mesh_authorized_keys` 尾注释**不强制一致**(A 侧目标表 vs B 侧准入表),但仪表盘在两者不一致时给**警告级提示**(非阻断)。

### 10.4 对端 host key 钉扎

新模型下 A 侧不经 `chainDialer`(§5.4),钉扎落在 `ExitClient` 自己的 `HostKeyCallback` 上:

```go
type MeshPeer struct {
    DeviceID string   `json:"device_id"`
    LANAddrs []string `json:"lan_addrs,omitempty"`
    Relay    *bool    `json:"relay,omitempty"`
    // HostKey 对端 ssh_ingress 的 host key。两种写法都接受:
    //   1) authorized_keys 行,如 "ssh-ed25519 AAAA... comment"
    //   2) SHA256 指纹,如 "SHA256:AbCdEf..."
    // 空 = 该 peer 不可用(fail-closed,不做 TOFU)。
    // 仪表盘「SSH 入口」卡片展示本机指纹(server.go:219 HostKeyFingerprint)。
    HostKey string `json:"host_key,omitempty"`
}

func pinnedHostKey(want string) gossh.HostKeyCallback {
    if want == "" {
        return func(string, net.Addr, gossh.PublicKey) error {
            return fmt.Errorf("mesh: peer host key not pinned (fail-closed)")
        }
    }
    if strings.HasPrefix(want, "SHA256:") {
        fp := strings.TrimSpace(want)
        return func(_ string, _ net.Addr, got gossh.PublicKey) error {
            if gossh.FingerprintSHA256(got) != fp {
                return fmt.Errorf("mesh: host key mismatch (fingerprint)")
            }
            return nil
        }
    }
    pinned, _, _, _, err := gossh.ParseAuthorizedKey([]byte(want))
    if err != nil {
        return func(string, net.Addr, gossh.PublicKey) error {
            return fmt.Errorf("mesh: bad host_key config: %w", err)
        }
    }
    wantBytes := pinned.Marshal()
    return func(_ string, _ net.Addr, got gossh.PublicKey) error {
        if !bytes.Equal(got.Marshal(), wantBytes) {
            return fmt.Errorf("mesh: host key mismatch")
        }
        return nil
    }
}
```

> 两种输入形态是**两条不同解析分支**(指纹比对 vs `ParseAuthorizedKey` + `Marshal()` 字节比对),实施时不能合并。
>
> **错误闭包只随 `ExitClient` 重建,失败结果不落盘**:上面 `ParseAuthorizedKey` 失败时返回的闭包捕获了 `err`,在 `ExitClient` 构造期求值一次、缓存进 `ExitClient.hostKeyCB`(§5.1)——**不是每连接重算**,但也**不是跨配置存活**。这个"永久拒绝"的闭包被该 `ExitClient` 的所有后续连接复用,直到配置修正、`HostKey` 值变化使缓存键(下条)不匹配、`ExitClient` 重建为止。这是**期望行为**(配置错了就该一直拒,而不是每次拨号重新解析、重新失败);§15.1 的 `pinnedHostKey` 坏配置用例应断言"连错两次都拒",间接钉住"缓存的是闭包而非每连接重算"。
>
> **`ExitClient` 的缓存键与失效**:缓存在 `TCPConnectionHandler` 上(§5.1),`map[string]*ExitClient`,键由**全部影响握手行为的配置值**拼成:`DeviceID | LANAddrs(排序后拼接) | HostKey | Relay | SSHUser | sha256(PrivateKey+"\x00"+KeyPassphrase)`。
>
> **私钥必须进键**:`PrivateKey`/`KeyPassphrase` 不进 `PeerSpec`(它们是节点身份,不是 peer 属性),但换了私钥后旧 `ExitClient` 里缓存的 `gossh.Signer` 就废了。把摘要并进键,换钥自然导致键不匹配 → 新 `ExitClient`,无需额外的 flush 钩子。
>
> **失效机制**:无 TTL、无定时清理。键不匹配即重建;旧条目在下一次写入同键时被覆盖。配置提交后 `PeerSpec` 值或私钥摘要变化 → 键变化 → 重建。**这是唯一的重建路径**——没有 reconcile 钩子去主动清表,`ExitClient` 也不随 reconcile 被动重建;所谓"与 reconcile 同节奏"仅指配置变更经由键不匹配自然生效。§10.5「改私钥 → `ExitClient` 重建」即由此实现(经 `sha256(PrivateKey+passphrase)` 进键)。

**代价**:配置多一步(§14.2 第 3 项)。**收益**:中间人无法冒充,安全叙事真正闭环。

### 10.5 A 侧身份凭证

```go
type MeshConfig struct {
    Enable        *bool               `json:"enable,omitempty"`
    ForwardEnable *bool               `json:"forward_enable,omitempty"`
    MaxHops       int                 `json:"max_hops,omitempty"`
    Peers         []MeshPeer          `json:"peers,omitempty"`
    Fallback      *MeshFallbackConfig `json:"fallback,omitempty"`
    // PrivateKey 本节点的 mesh 身份私钥(PEM,OpenSSH 或 PKCS#8)。
    // 公钥分发到各对端的 ssh_ingress.mesh_authorized_keys(尾注释=本机 device_id)。
    // 空 = 本节点不能作为转发发起方(仍可作为被转发方)。
    PrivateKey    string `json:"private_key,omitempty"`
    KeyPassphrase string `json:"key_passphrase,omitempty"`
}
```

- **落盘**:配置即存储(与 `customer.proxy.private_key` 同口径),不另行生成文件;
- **`key_passphrase` 的 TrimSpace 副作用**:`MeshConfig.Validate` 会对它做 `strings.TrimSpace`(与 `peer.DeviceID` 同款就地规整,§6.1)。若 passphrase 首尾真有空格,**会被静默改掉导致解不开钥**。接受该代价(与 `proxy.key_passphrase` 既有口径一致),但必须在字段注释与仪表盘提示里写明;
- **明文回读**:`handleGetCustomerConfig`(`config_handler.go:134-150`)返回整个 `customer` 对象,故该私钥与 `customer.proxy.private_key` 一样**经 `/api/config/customer` 明文回读**。这是节点身份私钥(撤销需全局轮换),风险高于一次性代理密码——**本期与既有口径保持一致**,脱敏需连带改 `proxy.private_key`,不在本期范围;
- **轮换**:改私钥 → `sha256(PrivateKey+passphrase)` 进缓存键(§10.4),键不匹配 → 下一次拨号重建 `ExitClient`。**不依赖 reconcile 主动清表**——配置热更新只是让键变化的条件之一。
- **撤销粒度**:节点级一把钥,撤销单个 peer 需轮换全局钥。可接受(对称组网 peer 数少),将来如需细粒度再扩 `peers[].private_key`。

`MeshConfig.Validate` 增:启用 mesh 且 peers 非空时 `PrivateKey` 必填。

## 11. 规则语法

```conf
x.com,mesh:dev-b              # A 经 B 出口
x.com,mesh:dev-b,deny         # 带失败策略
dns,mesh:dev-b                # DNS 代转发
```

失败策略(`direct|deny|fallback:<id2>`)语义与语法**零变化**。

语法层面**不引入 `>` 链形态**(那属于 §16 二期)。`splitMeshTail`(`processor/routing/snapshot_mesh.go:66-115`)**零改动**。

> 二期引入链形态时:`splitMeshTail` 的 `tailParts` 判定可原样复用(已验证 `x.com,mesh:dev-b>dev-c,deny` 能正常解析);但下游两道闸要同步——`meshForwardAvailable`(`snapshot_mesh.go:146-160`)按精确 deviceID 比对,`meshDNSForwardTargetFromSnapshot`(`:287-319`)要回查 peer 的 `LANAddrs`。链形态需按 `>` 拆分逐段校验。

前端 `app/website/src/components/settings/RulesSettings.vue` 的 `splitMeshRuleTail`(约 650-710 行)**零改动**(32 处 `mesh` 全是尾段解析,已核实)。

## 12. 包处置:meshproto 不迁不删,只砍帧协议

原稿两个错误决定:(a)「`meshproto` 整删」会造成编译失败;(b)「`DefaultHops`/`MaxHopsCap` 迁到 `processor/config`」会让常量两处分居、包职责名不副实。

**处置:常量全留 `outbound/meshproto`,只删帧协议部分,不迁任何常量。**

`processor/config` 本来就 import `outbound/meshproto`(`types.go:16`),方向恒为 `meshproto ← config`,不成环也不需要动。

### 12.1 保留

| 符号 | 现存消费方 |
|---|---|
| `ClassSelf` / `ClassAI` / `ClassCustom` | `processor/config/types.go:946,948`、`processor/egress/egress.go:100,126`、`processor/tcp/route_override.go:56`、`inbound/tun/tunnel/dns_forward.go:204` |
| `DefaultHops` / `MaxHopsCap` | `processor/config/types.go:861,863,916,917`(`EffectiveMaxHops` / `Validate`) |
| `DNSTarget` / `IsDNSTarget` | `inbound/tun/tunnel/dns_forward.go:201`、`outbound/proxy/mesh/mesh.go:336` |
| `SplitTarget` | `outbound/proxy/mesh/mesh.go:346`(用途随 `DialContext` 重写而调整,先保留) |

### 12.2 删除

`Header` / `Parse` / `Encode` / `appendHop` / `via` / `hops` / `hasControlByte`(**未导出**) / `MaxHeaderBytes`(唯一非测试消费方 `mesh_inbound.go:95`,随该文件删) / `CarrierLAN` / `CarrierRelay` / `ClassCarrierAllowed`。

- `ClassCarrierAllowed` 的语义迁至 `Transport.Admit`(§7.1);
- `CarrierLAN`/`CarrierRelay` 常量消失(载体由 Transport 类型表达)。

包注释改为「mesh 语义常量包」,不再自称帧协议。

## 13. 删除与改动清单

### 13.1 后端 Go

| 文件 | 行数 | 处置 |
|---|---|---|
| `outbound/meshproto/header.go` | 155 | **只删帧协议**(§12.2),常量留;约 -60 行 |
| `inbound/ssh/meshframe.go` | 101 | **整删**;`meshframe` 相关测试同删 |
| `processor/tcp/mesh_inbound.go` | 529 | **删约 330**(DNS 约 200 行迁至新 `mesh-exit` handler;`HandleMeshConn`/AppendHop/门禁/`meshMetadataFromTarget`/`WriteMeshRejectLine`/`writeMeshRejectLine` 删);`RouteMesh` 常量保留 |
| `app/mesh/relay/relay.go` | 102 | **整删**(piko 端改 `HandleIngressConn`) |
| `app/tunnel/mesh_dispatch.go` | 28 | **整删**;`cmd/core.go:186` 的调用同删 |
| `inbound/sniff/sniff.go` | — | 删 `KindMesh` 与 `'{'` 分类分支;**iota 连带**:`KindUnknown=0,KindSSH,KindSocks,KindMesh,KindHTTP` 删中间项会让 `KindHTTP:4→3`,需同步 `String()`(`:33-45`)、`sniff_test.go:84`、以及 `inbound/http/dispatch.go:5-6` 的文件头注释 |
| `inbound/http/dispatch.go` | 61 | 删 `KindMesh` 分支与 `MeshConnHandler` 变量 |
| **`inbound/http/server.go:85`** | — | **必改**:日志文案"嗅探 SSH/socks5/mesh/HTTP" 随 `KindMesh` 删除失真 |
| `outbound/proxy/mesh/mesh.go` | 354 | **重写约 150 行**(`PeerSpec` + Transport 遍历 + `DialMeshAddr`);删 `lanDialCandidates` 双端口展开;新增 `sshexit.go` 约 **150 行**(§5.1 完整定义) |
| `inbound/ssh/mesh_inject.go` | 233 | **重写约 120 行**(读 ExtraData + 注入 + ExtraData cap);`ForwardGate` 语义翻转为 fail-closed;`meshChannelConn`/`newMeshChannelConn` 及其 `preread` 回放机制(`:197-233`)**整块删除**(ExtraData 方案无 bufio 预读) |
| `inbound/ssh/server.go` | — | `serveChannels` 重排顺序(form 先于 channel 白名单)+ form×channel 矩阵 |
| `inbound/ssh/config.go` | — | `MeshKeys []MeshKey`;`ForwardGate` 收窄为 `func() bool`(**删除 `allowed []string`**,§10.1) |
| `inbound/ssh/channel.go` | — | 新增导出 `WithRemoteLabel(net.Conn, string) net.Conn`(§8.2) |
| `inbound/ssh/reconcile.go` | — | `runIdentity` 纳入 `MeshKey.DeviceID`(`:36-56`,`marshalKeyIdentity` → `marshalMeshKeyIdentity`);`ReconcileSSHIngressFromConfig` 的 `:280-283` 与 `buildSSHIngressRunConfig` 的 `:358` 两处「ForwardGate 留 nil=全放行」注释整段重写(§10.1 第 4 条) |
| `inbound/tun/metadata/metadata.go` | — | 增 `MeshInjected bool` |
| `processor/tcp/handler.go` | — | 三处注入检查(§6.2);`getMeshProxyForExecution:1476` 改 `PeerSpec`(**签名不变**,类别不进 PeerSpec);`dialMeshFallbackNode:1519` 改 `DialMeshAddr` + `config.MeshFallbackHostKey`;`SetMeshPikoDialSource` → `SetMeshTransportDialSource`;**新增 `ExitClient` 缓存为 `TCPConnectionHandler` 的「结构体字段」**(`map[string]*meshproxy.ExitClient` + `sync.RWMutex`;键含私钥摘要,§5.1/§10.4)。**不放包级变量**——会跨 `ResetHandler` 存活致测试串味 |
| `processor/config/types.go` | — | `MeshConfig` 增 `PrivateKey`/`KeyPassphrase`;`MeshPeer` 增 `HostKey`;`MeshFallbackConfig` 增 `HostKey`;新字段 `TrimSpace` 规整 + 必填校验(§6.1);**新增 `Config.ValidateMeshDependency()` 与 `Config.MeshEffectivelyEnabled()`**(§10.3);**新增 `MeshFallbackHostKey(cfg)` 只读助手**(§7.2);`MeshFallbackActiveForClass:885` 内部改用新方法(签名不变) |
| **`processor/config/defaults.go:92-99`** | — | **必改**:`UnifiedIngressListenAddrFor` 的绑定面判据由 `Mesh.EffectiveEnabled()` 改为 `SSHIngress.IsEnabled()`(§5.5);对称风险见 §5.5.1 |
| `processor/rules/engine.go:350` | — | **不改逻辑,只加注释**(§6.2 末):说明 `RouteMesh` 不可达的前提是 `DNSInfo` 只由 `SetHostName` 构造 |
| **`processor/routing/snapshot_mesh.go`** | 320 | **小改**(§13.2) |
| **`processor/config/routing_schema.go:179-186`** | — | **必改**:`toNode:<id>:fallback:<id2>` 的 `id2` 纳入 host_key 能力校验(§6.1) |
| **`outbound/proxy/proto/proto.go:19` / `mesh.go:75`** | — | **必改**:`Protocol: proto.Mesh` 在 `DialContext` 重写后确认仍成立(`mesh_assembly_test.go:173` 依赖) |
| **`app/http/handlers/mesh_handler.go:58-72, 97`** | — | **必改**:`Peers []config.MeshPeer` 直接序列化 → `host_key` 自动出现在 `/api/mesh/status`(新增曝露面,需确认可接受);`resp.Enable` **保持 `EffectiveEnabled()`**(展示用户配置值),同响应**新增 `effective` 布尔**(`cfg.MeshEffectivelyEnabled()`,`:88-99` 已有 `*Config`),否则 SSH 依赖降级后用户看到「已启用」却不通,无从定位 |
| **`app/http/routes/routes.go:335-342`** | — | 核对 `/api/mesh/status`、`/api/mesh/lan_peers` 语义随 `forward_enable` 接线而变化 |
| **`inbound/tun/tunnel/dns_forward.go`** | 308 | **必改**:`SetMeshDNSForwardPeerSource(fn func() *meshproxy.Peer)`(`:293`)签名随 `Peer` 重写而变;`meshDNSForwardPeer()`(`:300`)随之;`WithClass(ClassCustom)`(`:204`)保留 |
| **`inbound/tun/tunnel/udp.go`** | — | **必改(MAJOR-15)**:`:38-44` 的 `viaMeshDNS` 是 TUN UDP/53 走 mesh 的**唯一触发点**,`t.meshDNSForwardPeer()` 返回非 nil 即置 `metadata.Route = "RouteMeshDNS"` 并改用 `newDNSForwardPacketConn`;`:74` 的 `if metadata.DstPort == 53 && !viaMeshDNS` 是它的反条件。`meshDNSForwardPeer` 返回类型随 `Peer` → `PeerSpec` 变化,这两处必须同步;`RouteMeshDNS` 字符串**保留**(它是 TUN 侧溯源标记,与注入标记 `MeshInjected` 无关) |
| **`inbound/tun/tunnel/tunnel.go`** | — | **必改**:`meshDNSForwardFn func() *meshproxy.Peer`(`:62`)同上;`SetMeshDNSForwardPeerSource`(`:293`)与 `meshDNSForwardPeer`(`:300`)的锁保护结构不变 |
| **`app/tunnel/mesh_assembly.go`** | 254 | **必改**:`egressPeer()`(`:87-94`)构造 `PeerTarget` → `PeerSpec` |
| **`app/tunnel/mesh_dial.go`** | 121 | **必改**:`dialMeshEndpoint` / `PikoDialFunc` 接 `PikoTransport` |
| **`app/tunnel/manager.go`** | — | **必改**:`:317 SetMeshSelfIDSource` 与 `:335 SetMeshPikoDialSource` 的唯一生产调用点;`:426-443` 换 accept 循环(§8) |
| **`app/tunnel/bridge.go:168-175`** | — | **不改代码,只改注释**(MAJOR-12):`MeshEndpointID` 的注释写「B-side mesh receive listens on」——新模型下 `.mesh` endpoint 收的是**完整的 SSH 传输流**(`HandleIngressConn`),不是 mesh 帧。注释必须改成"SSH ingress over piko relay",否则下一个人会以为这里还在解 mesh 帧 |
| **`app/tunnel/piko_logger.go:55-61`** | — | **不改代码,只改注释**(MAJOR-13):`reportMeshRelayDisabled` 的「relay carrier simply stays unavailable」改为「mesh 出口的中继 Transport 不可用;LAN Transport 与其他 SSH 入口形态不受影响」——失败面变小了,旧注释描述过宽 |
| **`app/http/services/customer_config_service.go:270,344`** | — | **必改**:`buildSSHIngressRunConfig` 签名扩参 `(effective, mesh)`(`:270` 传 `cfg.Customer.Mesh`)+ 内部传活的 `ForwardGate` 闭包(§10.1);`:378` 保留 `ParseAuthorizedKey` 的 comment 返回值(§10.2) |
| `outbound/proxy/ssh/chain.go` | 192 | **零改动**(mesh 不经 `chainDialer`,§5.4) |
| `outbound/proxy/ssh/`(ssh.go/pool.go) | — | **零改动**(同上);`outbound/registry.go` 不注册 mesh |
| `app/mesh/lanbeacon/` | 731 | **保留**(发现候选仍需要) |
| `processor/egress/egress.go` | — | `PeerTarget` 字段收敛需同步 `:115-142`;`platformDialContext:127` 新建 Metadata 的行为不变(§6.2) |
| `processor/tcp/mesh_offline.go` | 49 | **保留**(离线信号缝不变,`agent_ai_agentcore.go:321` 仍在消费) |
| `processor/tcp/relay.go:487` | — | 记账:`SimpleRelayFunc` 无生产调用方(死代码),本次不删但不扩大改动面 |

### 13.2 `snapshot_mesh.go` 的两处小改

| 位置 | 现状 | 问题与修法 |
|---|---|---|
| `meshDNSForwardTargetFromSnapshot:287-319` | `len(peer.LANAddrs) == 0 → ok=false` | 新模型下 peer 可**只有 piko 中继**(跨 NAT),DNS 代转发会被静默关闭(fail-open 回落公网 DNS),与 §9 定位矛盾。改为:无 LAN 地址也放行,交 Transport 选 piko |
| `meshForwardAvailable:146-160` | `mesh.EffectiveEnabled()` + 只查 `Peers[].DeviceID` | 两处改:① `EffectiveEnabled()` → `cfg.MeshEffectivelyEnabled()`(§10.3 的 SSH 依赖降级,一处生效);② 补 `HostKey != ""` 与 `PrivateKey != ""` 检查(该 peer 缺钉扎/缺身份 = 能力不成立)。**注意**:该函数在**编译期**调用(`classifyEntryForCompile:171`,`tls.go:311` 每连接重编译、无缓存),因此**不需要**改配置后重编译触发;但"条件不满足 → 静默跳过"会让存量 `mesh:` 规则无声消失 —— 这也是 §6.1 把校验前移到**保存期**的理由 |
| 同文件 `:24 SnapshotTargetMeshPrefix` | `const SnapshotTargetMeshPrefix = "toNode:"`,与 `config.MeshRuleTargetPrefix` 同值(有测试 `snapshot_mesh_test.go:473` 钉住) | **零改动**。§6.1 的链校验加在 `routing_schema.go:179-186`,不影响此常量 |

### 13.3 前端(BLOCKER-3 + BLOCKER-2 + MAJOR-8/11/14,全部必改)

前端有**两处**独立的字段白名单,任一处漏改都会静默抹掉新字段。两处都必须改。

#### 13.3.1 第一处:`MeshSettings.js` 归一化白名单

```js
// MeshSettings.js:48-54  peers.push({ device_id, lan_addrs, relay })   ← host_key 被丢
// MeshSettings.js:71-75  return { enable, classes, addr }              ← fallback.host_key 被丢
// MeshSettings.js:83-89  return { enable, forward_enable, max_hops, peers, fallback }
//                                                                       ← private_key/key_passphrase 被丢
```

必改:`normalizeMeshPeers` 透传 `host_key`(空串不落键)、`normalizeMeshFallback` 透传 `host_key`、`normalizeMeshConfig` 透传 `private_key` / `key_passphrase`。

#### 13.3.2 第二处:`MeshSettings.vue` 保存载荷(**MAJOR-8**)

`MeshSettings.vue:770-783` 的 `save()` 先用 spread 展开 `this.config.mesh`,**再用字面量对象覆盖 `fallback`**:

```js
const mesh = normalizeMeshConfig({
  ...(this.config?.mesh || {}),
  fallback: {                             // ← 显式重建,不含 host_key
    enable: !!this.form.fallbackEnabled,
    addr: String(this.form.fallbackAddr || '').trim(),
    classes: MESH_FALLBACK_CLASSES.filter((cls) => this.form.classes[cls])
  }
});
```

**只改 13.3.1 不够**——`this.config.mesh.fallback` 被这个字面量整体替换,`normalizeMeshFallback` 拿到的 `source.host_key` 已经是 `undefined`,透传也无从透传。必改:把 `host_key: this.config?.mesh?.fallback?.host_key` 并入该字面量(或改成 `fallback: { ...this.config?.mesh?.fallback, enable, addr, classes }`)。

#### 13.3.3 清空路径不可达(BLOCKER-2)

`SettingsPage.vue:503-534 buildCustomerConfigPatch` 有两个独立缺陷,合起来使 `mesh.private_key` **永远无法清空**:

1. `buildConfigPatch`(`:536-543`)对字符串先 `trim()`,`!trimmed` 直接返回 `undefined` = 「未改动」;
2. 显式清空的补救循环(`:512-519`)**只覆盖** `patch.proxy` 的四个键(`username`/`password`/`private_key`/`key_passphrase`);`:523-529` 另有一条**只覆盖 `mesh.fallback.addr`** 的补救。

所以用户在组网卡片把私钥框清空 → `normalizeMeshConfig` 产出 `private_key: ''` → `buildConfigPatch` 判为未改动 → patch 里没有该键 → 后端 deep-merge 保留旧私钥。**用户以为撤销了密钥,实际没有。**

必改:在 `:531` 的 `applyAgentCoreLLMClearing` 之前补一段 mesh 凭据清空,与 `:512-519` 同构:

```js
// mesh 凭据清空同款:private_key/key_passphrase/fallback.host_key 为空串
// 是显式撤销意图,buildConfigPatch 会当「未改动」跳过,必须显式带出。
for (const [key, nextVal, curVal] of [
  ['private_key',    normalizedNext.mesh?.private_key,        normalizedCurrent.mesh?.private_key],
  ['key_passphrase', normalizedNext.mesh?.key_passphrase,     normalizedCurrent.mesh?.key_passphrase]
]) {
  if (!String(nextVal ?? '') && String(curVal ?? '')) {
    patch.mesh = isPlainObject(patch.mesh) ? patch.mesh : {};
    patch.mesh[key] = '';
  }
}
const nextFallbackHostKey    = String(normalizedNext.mesh?.fallback?.host_key ?? '');
const currentFallbackHostKey = String(normalizedCurrent.mesh?.fallback?.host_key ?? '');
if (!nextFallbackHostKey && currentFallbackHostKey) {
  patch.mesh = isPlainObject(patch.mesh) ? patch.mesh : {};
  patch.mesh.fallback = isPlainObject(patch.mesh.fallback) ? patch.mesh.fallback : {};
  patch.mesh.fallback.host_key = '';
}
```

**`peers[].host_key` 的清空不走这条**:`peers` 是数组,`buildConfigPatch` 走 `:545-548` 整体替换分支(不等则整数组进 patch),空串自然带出,无需特判。**但要确认 `normalizeMeshPeers` 没把空 `host_key` 剔掉**——否则「清空某 peer 的 host_key」会被归一化成「该 peer 没有 host_key」,两者在后端都是空,结果正确,可接受。

#### 13.3.4 UI 输入位

`MeshSettings.vue` 增三个输入位:peer 行内 `host_key`、兜底行 `host_key`、节点身份私钥(+ passphrase)。**验收标准**:填 → 保存 → 刷新页面仍在;清空 → 保存 → 刷新页面仍为空。

#### 13.3.5 存量测试会硬失败(MAJOR-11)

`app/website/tests/e2e/mesh-settings.spec.js:184-188` 用 `toEqual` 整对象比对 `mesh.fallback`:

```js
expect(savedBody.customer.mesh.fallback).toEqual({ enable: true, addr: '192.168.1.20:56441', classes: ['ai'] });
```

一旦 `normalizeMeshFallback` 透传 `host_key`(哪怕是 `''`),这条断言就**必然失败**。同文件 `:193-195` 的 `toBeUndefined()` 系列(`mesh.enable`/`forward_enable`/`peers`)也会因为 patch 结构变化需要复核。这属于**预期内的测试更新**,不是回归——但实施时必须同步改,否则 CI 红。

#### 13.3.6 i18n(MAJOR-14)

新增输入位需要文案,`zh.js:966-999` 与 `en.js` 同段落(`mesh_*` 前缀区)各补三条:

| key | zh | en |
|---|---|---|
| `mesh_peerHostKey` | 对端 host key | Peer host key |
| `mesh_fallbackHostKey` | 兜底节点 host key | Fallback host key |
| `mesh_privateKey` | 节点身份私钥 | Node identity key |

另加 placeholder / hint 各两条(照 `mesh_fallbackAddrPlaceholder` 的既有格式)。**漏 en.js 会走 vue-i18n 回落显示 key 名**,不是崩溃但用户可见。

#### 13.3.7 边界:本期是否做 UI

若本期不做 UI,**必须**在 §14.2 写明「只能手改配置文件,仪表盘保存会覆盖密钥字段」——但即便如此,13.3.1/13.3.2 的**透传**仍是必改项(否则手改的配置点一次保存就没了),只有 13.3.4 的输入位可以推迟。

### 13.4 测试与文档

- 删除或重写:`processor/tcp/mesh_inbound_test.go`、`processor/tcp/handler_mesh_test.go`、`outbound/proxy/mesh/mesh_test.go`、`inbound/ssh/mesh_inject_test.go`、`app/tunnel/mesh_dial_test.go`;
- **编译必改(不是删,是改类型)**:`inbound/ssh/reconcile_test.go:181,189` 与 `inbound/ssh/server_test.go:365,388,432` 的 `MeshKeys: []gossh.PublicKey{...}` 必须改成 `[]MeshKey{...}`(§10.2 表);漏改即编译红;
- **`test/mesh_integration_test.go`(1387 行)整篇按帧协议写,需整体重写**;
- 修正断言:`ssh_ingress_reconcile_test.go:243`(删"ForwardGate 必须为 nil")、`mesh_inject_test.go:158`(注释语义翻转)、`mesh_assembly_test.go:173`(`proto.Mesh`)、**`app/website/tests/e2e/mesh-settings.spec.js:184-188`(`toEqual` 整对象比对必失,§13.3.5)**;
- **新增 `processor/config` 测试**:`ValidateMeshDependency` 四组合(mesh×ssh × 开关×关)、新字段 TrimSpace、`host_key`/`private_key` 必填、`fallback.host_key` 条件必填、`MeshFallbackHostKey` 的 nil 链与 TrimSpace;
- 新增:Transport 测试、form×channel 矩阵穷举、`MeshKey` 解析、host key 钉扎(含冒充失败 + 两种输入格式)、注入标记防环、`remoteIP(labeledAddr)` 与 `SrcIP` 归零断言(§8.2)、前端 `normalizeMesh*` 字段透传 + `buildCustomerConfigPatch` 清空路径(§13.3.3)、`EffectiveEnabled` 的 SSH 依赖降级(§10.3)。

### 13.5 规模估算

| 类别 | 行数 |
|---|---|
| 净删(上表删项合计) | 约 **-940** |
| 新增(`ExitClient` 150 + Transport 2 实现 + `PeerSpec` + `mesh-exit` handler + `MeshKey` + 钉扎 + `WithRemoteLabel` + `DialMeshAddr` + `ValidateMeshDependency`) | 约 **+500** |
| 前端(§13.3) | 约 **+80** |
| **净额** | 约 **-440** |

## 14. 迁移与破坏性变更

### 14.1 发布说明必写

> **新版 mesh 与旧版不互通** —— mesh 帧协议已删除,改走 SSH 自管 channel。升级必须**全节点同步**。
>
> 跨版本现象:
> - **LAN 载体**:旧 A 拨 56432 后先写 JSON 帧头,新 B 嗅探 `{` 已不再识别为 mesh(`sniff.KindMesh` 删除)→ 落 `handleRawConnection` → `ReadRequest` 失败 → **对端静默断连**;
> - **piko 载体**:旧 A 写 JSON,新 B 等 `SSH-` → **SSH 握手失败**。
>
> **组网现在依赖 SSH 入口**:需同时开启 `ssh_ingress`,并将各转发方公钥填入 `mesh_authorized_keys`。
>
> **⚠️ 未填齐密钥前,`mesh:` 规则会静默失效**:升级后先不要急着改规则,按 §14.2 补齐 `private_key` 与各 peer 的 `host_key`。缺 `host_key` 的 peer 能力校验不通过,该 peer 的 `mesh:` 规则在编译期被跳过 —— 不报错、不弹窗,规则列表里看着还在但永不命中。(因此本设计把校验前移到**保存期**,让问题在用户动手保存时暴露而非在流量面无声失效。)
>
> **可用性收窄(仅默认形态)**:`ssh_ingress.port=0` 时,mesh 入站随代理运行态启停(统一口 56432 生命周期)。手动停止代理 = mesh 入站一并下线。`port>0` 的专用口形态不受此影响,但**专用口默认只绑回环**(`ServiceBindHost`,默认 127.0.0.1),不可作为 LAN 入口,`lan_addrs` 仍须填 56432。
>
> **56432 的 LAN 可达面改由 `ssh_ingress` 决定**:今日是「开 mesh → 56432 绑 0.0.0.0」。改为「开 ssh_ingress → 56432 绑 0.0.0.0」。本版本修正两个方向的错配,但**引入一个对称的新暴露面**:
>   - 开 ssh、关 mesh → 56432 绑 0.0.0.0,**HTTP 代理面(socks5/CONNECT)随之暴露到 LAN**(会话租约仍在,无会话者拿不到代理);
>   - 开 mesh、关 ssh → **mesh 静默失效**(见下条),56432 回到回环。
>
> **⚠️ mesh 开但 `ssh_ingress` 未配 → 升级后 mesh 不再生效**:mesh 的出口载体已收敛到 SSH,没有 SSH 入口就没有 mesh。`EffectiveEnabled` 在启动期对这一组合降级为关(不阻断 boot);保存期则**硬拒**(§10.3)。存量这么配的用户升级后组网会静默停摆 —— 这是有意的 fail-closed,不是 bug。
>
> **`forward_enable` 的门禁语义收紧**:旧版「`ForwardGate` 为 nil = 全放行」,新版**nil = 拒绝**。装配层恒注入活闭包,故正常路径无感知;但任何绕过 `buildSSHIngressRunConfig` 直构造 `RunConfig` 的测试/内部调用会被拒。
>
> **`key_passphrase` 会做 TrimSpace**(§10.5):passphrase 首尾若真有空格会被改掉导致解不开钥。与 `proxy.key_passphrase` 既有口径一致。

### 14.2 升级后必需人工动作

1. 开启 `ssh_ingress`(`ssh_ingress.enable = true`);
2. 生成本机 mesh 身份密钥对,私钥填入 `customer.mesh.private_key`,**公钥(带本机 device_id 尾注释)分发给每个对端**,填入其对端的 `ssh_ingress.mesh_authorized_keys`;
3. 对每个 peer,把**对端**的 SSH 入口公钥填入本机 `customer.mesh.peers[].host_key`(对端仪表盘「SSH 入口」卡片的指纹即来源);
4. 检查 `peers[].lan_addrs` 的端口确为 **56432**(不是专用口,§5.2);
5. 跨网段/跨 NAT 的对端确认 `relay=true` 且平台侧已放行 `<deviceID>.mesh` + 下发 `piko_proxy_url`;
6. **离线兜底**:`fallback.addr` 改填对端 `IP:56432`(SSH 入口),并新增 `fallback.host_key`。**旧值指向已删除的 mesh 监听口,不改则兜底必不通**;
7. 若使用仪表盘配置:确认前端版本已含 §13.3 的字段透传,否则**手改配置文件**(旧前端保存会抹掉密钥字段)。

### 14.3 不需要迁移

- `proxy_rules` 的 `mesh:<deviceID>` 单跳语法**完全不变**;
- `customer.mesh.peers`(新增 `host_key` 除外)/ `fallback` / `forward_enable` 语义不变;
- 仪表盘组网画布(节点/连线/chips)结构不变,仅新增三个输入位(§13.3)。

## 15. 测试策略

### 15.1 单元

- `mesh-exit` ExtraData 编解码(畸形/超长(>512)/控制字节/缺字段/`t` 未知);
- **`hostPortFromMetadata` 四形态**(§5.1.1):裸域名 / `domain:port` / 裸 IPv4 / **裸 IPv6**(`SplitHostPort` 不认无中括号形态,必须钉住) → 期望 `(host, port)` 二元组;
- `exitPayload` 目标不可构造时返回 `nil`(不是空 payload);**且 `ExitClient.DialContext` 在 `exitPayload` 返回 nil 时返回 `errUnbuildableTarget` 且不发起任何拨号**(钉住 §5.1.1 的硬规则——这是 Issue「nil payload」修复的落点);
- form×channel 矩阵穷举(bare×2 / mesh×2 / 未知);
- Transport `Admit(class, peer)` 矩阵:custom × {lan ✓, piko ✗};self/ai × {lan ✓, piko ✓};**`len(LANAddrs)==0` 时 lan ✗**(不论类别);
- **`errNoTransportAdmits` 的**三**个可达组合**(缺一不可,枚举器别做弱):①`len(LANAddrs)==0 ∧ Relay=false`(任意类别);②`len(LANAddrs)==0 ∧ Relay=true ∧ class=custom`;③`len(LANAddrs)==0 ∧ Relay=true ∧ class=self|ai ∧ piko dial 未接线`(≈退化后等价于 ①)。逐个断言返回该错误且**零拨号**;
- **`PikoTransport` 不进切片**:`spec.Relay=true` 但 `SetMeshTransportDialSource(nil)` 时,`ExitClient.transports` 长度为 1(LAN),不出现 admit-then-nil-panic;
- **`ExitClient` 缓存键**:改 `PrivateKey` 后同键不命中(重建);只改 `LANAddrs` 顺序但集合相同时仍命中(键排序规整);`key_passphrase` 变化也导致键变化;
- **Transport 顺序**:relay=true 的 peer,LanAddr 可达时**走 LAN 不走 piko**(钉住 LAN 优先,§7.3);LAN 全失败才降级 piko;
- `MeshKey` 解析:带注释/不带注释/尾注释含空格/跨列表同钥;
- host key 钉扎:**两种输入格式各一组**(authorized_keys 行 / `SHA256:` 指纹)× {正确 ✓,错误 ✗,空 fail-closed};
- 注入标记:带 `MeshInjected` 的连接在三个入口(链头 mesh/兜底/AI 改道)全部拒绝;
- `ForwardGate`:返回 false 时拒绝;`nil` 时 **fail-closed**(不是全放行);
- DNS payload 变体:`resolveDNSQuestion` 类型矩阵;
- `WithRemoteLabel` + `remoteIP`:限速键按 peer 隔离;**且 `SrcIP`/`SrcPort` 归零**(钉住已知行为,TCP 注入用例)。
- `pinnedHostKey` 坏配置(非法 key 串)时返回拒绝而非 panic。

### 15.2 集成(两节点本地回环)

- 单跳:A 起真 SSH transport,B 起真 `inbound/ssh`,验证 ExtraData 目标帧注入 + 规则命中 + MITM 生效;
- **并发**:两个 peer 同时经 piko 连入,验证不串行(§8.1);
- 形态隔离:mesh 凭证开 `direct-tcpip` → 拒;bare 凭证开 `mesh-exit` → 拒;
- 环路阻断:注入流量命中 mesh 规则 → 拒(错误回源);
- host key 冒充:自签另一密钥冒充 B → A 拒绝连接;
- **握手挂死**:对端 accept 后不回 SSH banner → A 侧在 `timeout` 内失败返回(§5.1 deadline);
- piko transport:NAT 对端可被当跳板(入站侧);
- DNS 代转发经 `mesh-exit` 往返;
- 失败语义:对端不可达 → 错误回源,链头执行兜底与失败策略;
- ingress 未运行时 piko 连接被 WARN 记账(非静默)。

### 15.3 真机冒烟

两台设备同网段:SSH 入口开启 + 双向密钥互填(含 host_key 钉扎)+ `x.com,mesh:<peer>` 规则,验证 AI 流量与自身流量经对端出口。

## 16. 非目标(v1)

- **多跳 / `mesh-relay` channel / `>` 链语法**:一期单跳。二期引入时需补:`mesh-relay` channel(ExtraData 只接受 `{"peer":"dev-id"}`,**不接受任意 host:port**,防已认证 peer 诱导 B 裸拨内网)、链解析、`meshForwardAvailable` 逐段校验、中继跳建链改造;
- **UDP 打洞实现**:只留 Transport 接口;
- **逐跳动态决策**;
- **mesh 独立于 ssh_ingress**;
- **`customer.proxy` 与 `customer.mesh.peers` 合并**;
- **`dns:` 以外的 UDP 转发**;
- **自定义 channel 的多路复用**:每连接一条 SSH channel;
- **`CloseWrite` 半关闭传播**:`channelConn`(`inbound/ssh/channel.go:112-121`)与新 `exitConn` **两端**的 `SetDeadline` 都将是 no-op,relay 依赖的「半关闭后不无限等」保护在**整条 mesh 链**上失效。**沿用已知限制**(与 SSH 出口同源),二期视需要加;
- **`private_key` / `host_key` 的 API 脱敏**:与 `customer.proxy.private_key` 同口径明文回读(§10.5);
- **`SrcIP` 记账**:piko 载体下 peer 来源地址不可知(§8.2),不因此新增透传机制。

## 17. 开放项

- **链尾出口策略表达能力**:链尾用自身默认出口。将来若需「同一链不同目标不同出口」,需扩 payload 或规则语法——一期不做。
- **piko 带宽审计**:一期无观测手段。
- **仪表盘画布**:新模型下单跳是"节点对",画布现结构仍适用;二期引入链后需要线性序列表达。
- **平台自动交换密钥**:`private_key`/`host_key` 目前靠人工粘贴。平台台账已有 device 记录,可做自动分发——一期不做。
- **`MeshKey.DeviceID` 与 `peers[].device_id` 的强制一致性**:一期只警告不阻断(两侧本就可以不同设备)。
- **`SplitTarget` 的存亡**:`mesh_inbound.go:501` 的消费方随文件删除,只剩 `mesh.go:346` 一处;`targetFromMetadata` 会被 `hostPortFromMetadata`(§5.1.1,改用标准库 `SplitHostPort`)取代,届时全仓可能再无 `SplitTarget` 调用方——一期先保留,实施时若确无用再删。
- **DNS 代转发的生产接线**:~~仍属既有 Task 9 未完成清单~~**已完成(2026-11-12)**:app/tunnel `meshDNSForwardPeer` 规则驱动装配 + `wireMeshAssemblySeams` 接线 `tunnel.T()`。激活按载体可用性分层——peer 有 LAN 地址才激活;relay-only peer fail-open 走本机 DNS(TUN DNS 恒 ClassCustom,PikoTransport 拒 custom,§9 记账已更新)。
- **`/api/mesh/status` 会新增 `host_key` 字段**:这是既有 `Peers []config.MeshPeer` 透传的自然结果,不带私钥。需在实施时确认可接受,或显式剔除。
- **出口链路的 TLS/MITM 边界**:TLS toAliang 流量在 MITM 后本地已解密,委托载体要求「到目标的原始流」——TLS 流量不适用委托,仍走本机隧道(§18.2;与旧离线兜底行为一致,是有意的形态边界而非缺陷)。

## 18. 出口链路改造(2026-11-12 用户拍板,替代离线兜底)

### 18.1 决策

用户原话定死语义:「我要的开关效果就是跳转到 dev-b,不管我们的电脑能不能直连,不做什么能联网不给走 b 的限制」。

- **触发为常驻语义**:fallback.enable 显式 true 即无条件改道,**离线信号门控整族删除**(`SetOfflineSignal`/`SetMeshOfflineSignal`/`TunnelSessionActive` 缝/`meshOfflineRedirectForAI`/`MeshOfflineAIRedirectActive`)。
- **配置收敛**:`MeshFallbackConfig` 从 `{enable,classes,addr,host_key}` 收敛为 `{enable,next_hop}`——下一跳必须是 peers 中**已钉扎**的节点(保存期 `Validate` 强制:next_hop 必填/须指向 peers/该 peer 须有 host_key;`ValidateMesh` 加 next_hop≠self 自指拒绝),地址与钉扎从 peers 条目继承。
- **取舍已写进发布说明**:开关开 = 出口完全依赖下一跳,下一跳挂了对外流量失败、错误回源,**不回落 direct**(静默绕链会让「开了开关却没走链」不可见,fail-loud)。
- **覆盖面**:toAliang 类明文流量(AI 56432 + 非 TLS toAliang 命中)+ 平台自身出口(processor/egress)。**规则流量(toNode)零变化**——链头失败策略(direct/deny/fallback:<id2>)照旧,不再叠加兜底段。

### 18.2 执行链

- **配置面**(processor/config):`MeshDelegateNextHop()` 替代 `MeshFallbackActiveForClass`(布尔+peer 二值返回);`MeshFallbackHostKey` 删除。
- **tcp 面**(processor/tcp `mesh_delegate.go` 新文件):`meshDelegateGuard` = 本机 id 未接线 fail-closed ∧ via 含自己拒 ∧ `len(via) >= EffectiveMaxHops` 拒;`dialViaMeshDelegate` 复用 `getMeshExitForExecution` 的 ExitClient 装配(LAN/Piko 载体、钉扎、身份与规则路径同一套),拨出前 `append(via, selfID)`;`dialAliangOrDelegate` 是明文形态专属入口(TLS/MITM 路径不委托)。本机 id 缝 `SetMeshSelfIDSource`(装配层=Manager.deviceID,与 inbound/ssh LocalNodeID 同源)。
- **链式推进**:接收端(inbound/ssh handleMeshExitChannel)解析 payload `v` → `md.MeshVia`;注入连接守卫放行时再上链 = 链式转发的推进方式,回环/超限由守卫拒掉就地处理。payload `exitMeta` 增 `Via []string`(键 `v`,512 ExtraData 上限内)。
- **egress 面**:`meshDelegateActive()` 门 + peer 源从 `Peers[0]` 改 `MeshDelegateNextHop`;拨号失败如实失败。探针降级消费方(app/http/services agentCoreEnsureUpstream)改 `tcp.MeshDelegateActive()` 同源判定。
- **域绑定缓存**:processor/rules `StoreBinding` skip 名单 `RouteMeshFallback`→`RouteMeshDelegate`(mesh 路由连接不毒化缓存,防 EvaluateRoute 复活时静默绕过链路)。
- **UI**:MeshSettings 兜底卡 → 出口链路卡(toggle + 下一跳 peer 下拉,未钉扎行标注;addr/candidate/host_key/classes 四输入位删除;lan_peers 候选拉取随点选输入位一并移除)。
- **清理**:`DialMeshAddr`(裸地址拨号,唯一消费方是旧兜底路径)删除;`DialMeshAddr` 守卫矩阵测试随删(空钉扎/nil 身份守卫归 ExitClient 同名矩阵)。

### 18.3 已知边界(记录,不修)

- **TLS toAliang 不上链**:MITM 后本地已解密,委托载体要求原始流——TLS 流量仍走本机隧道(链在 TLS 流量处终止,与旧兜底行为一致)。链式转发对 TLS 流量的完整支持需要端到端形态改造,不在本批。
- **平台 api 委托到达 B 后按 B 的规则处理**:A 的平台出口流量经 mesh 到 B 是普通 host:port 注入连接,B 按自己的规则引擎决策(toAliang 规则命中 → B 出隧道);B 也开着开关时,其 toAliang 明文命中会继续上链(链式推进),TLS 命中在 B 的 MITM 处终止。
- **旧键残留**:存量配置里的 `fallback.addr/host_key/classes` 在保存时随字面量整体替换清除;读取侧(js 归一化与后端结构体)都不认这些键,手改配置文件残留旧键无副作用。

- ~~**`MeshFallbackActiveForClass` 的签名**~~:**已随出口链路改造删除(2026-11-12,§18)**——整个离线信号门控族(助手/离线信号缝×2/`MeshFallbackHostKey`)被 `MeshDelegateNextHop` 取代,地址与钉扎从 peers 条目继承,不再有独立 fallback.addr/host_key 形态。
- ~~**`customer.mesh.max_hops` 一期无消费方**~~:**已复活为消费方(2026-11-12,§18)**——链式转发的经停上限:接收端拒绝 `len(via) >= EffectiveMaxHops()` 的再上链,守卫命中即就地处理(链的终点语义,不报错)。
