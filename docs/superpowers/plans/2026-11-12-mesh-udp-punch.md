# mesh UDP 打洞载体(feat/mesh-udp-punch)实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 mesh Transport 缝上新增第三载体 `udp`(STUN 地址发现 + 平台 WS 信令候选交换 + QUIC-over-punched-socket 直连),LAN 失败后先试打洞、piko 兜底;PhoneServer 同期补三个信令帧转发,全栈可真机验收。

**Architecture:** 打洞编排(信令/STUN/socket 绑定/QUIC 会话)收敛在新包 `app/mesh/punch`;`outbound/proxy/mesh` 只加一个薄适配器 `UDPTransport`(注入 dial func,镜像 PikoTransport 模式);WS 信令走既有 peer 帧家族管线(services 层);配置一枚总开关。核心物理事实:TUN-on 时自流量无天然逃逸路径,打洞 socket 必须物理接口绑定,绑定失败=载体自禁落 piko。

**Tech Stack:** quic-go v0.59.0(indirect→direct;QUIC Initial 重试即探测,quic.Stream 实现 net.Conn 喂既有 B 侧入口)、pion/stun(新增 direct)、既有 ECDSA 现签证书+指纹钉扎+一次性 token 机器(`agent_peer_lan.go`)、平台 WS peer 帧家族。

**已拍板决策(2026-11-12,评审定案):**
1. 准入类别=**全类别**(self/ai/custom,按 SPEC:560 预留语义;punch 是直连非共享中继)。
2. 交付=**全栈**(本仓 + AliangPhoneServer)。
3. 载体会话复用=**允许**(QUIC session 跨 SSH 拨号复用+新 stream;与 SPEC §5.4 禁的「SSH client 连接池」是两层物,落地时在代码注释显式辨析)。拨号预算 **2.5s fail-fast** 落 piko。
4. 提交策略=每任务一笔中文提交(「新增:/测试:/修复:」),执行前经用户点头(CLAUDE.md 铁律)。

**基线:** master=beaaeb2(已含 mesh-ssh-proxy-chain 合并,Transport 缝在 `outbound/proxy/mesh/transport.go`);worktree=`../alianggate-udp-punch`;基线测试 `./outbound/proxy/mesh/... ./app/tunnel/...` 全绿;**开工首步已丢弃脏 go.mod(恢复 HEAD),Task 2 起每任务提交干净**。

**关键既有锚点(行号会漂,以符号为准):**
- `outbound/proxy/mesh/transport.go` — `Transport` 四方法接口;`Name()` 注释明写「未来 "udp」;`LANTransport.Admit`=有地址即放行(全类别先例);`PikoTransport.Admit`=self/ai。
- `outbound/proxy/mesh/sshexit.go:NewExitClient` — `transports := []Transport{LANTransport{}}` + 条件 append Piko;`DialContext` 按 Admit+IsRelay 双桶。
- `app/http/models/agent_protocol.go` — `AgentEventPeerLanListen="peer.lan.listen"` 家族 + 契约表(Required/Optional/Emits)。
- `app/http/services/agent_remote_ws.go` — dispatch switch(`case models.AgentEventPeerLanListen:`)+ 注册 map(约 :1095)。
- `app/http/services/agent_peer_lan.go` — ECDSA P-256 现签证书(`agentLanCert`,1h TTL)、指纹钉扎(`subtle.ConstantTimeCompare`)、一次性临时 listener+TTL 收摊、首行门禁语义。
- `inbound/tun/runner/utils/device.go:GetDefaultInterface` — 默认物理接口名。
- `app/tunnel/mesh_assembly.go` — 装配线(egress peer 源/beacon 生命周期/离线信号双缝)。
- `processor/config/types.go` — `MeshConfig/MeshPeer`(:843-864)、validate 家族实名 `MeshConfig.Validate`(:985)/`Config.ValidateMeshDependency`(:962)/`Config.ValidateMesh`(:1044)、**customer 配置 allowlist 五处铁律**(加字段逐一 grep 旧字段名同步,见 memory customer-config-allowlist-five-places)。
- PhoneServer:`/Users/mac/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer`,`server/src/modules/peer/lan.ts` 为 punch 帧的仿写模板(`server/test/peerLan.test.ts` 为测试模板)。

---

## File Structure(新增/修改全景)

```
alianggate(worktree feat/mesh-udp-punch)
├── app/mesh/punch/                      ← 新包:打洞编排(不 import app/http,依赖注入信令)
│   ├── socket.go                        绑定公共逻辑+Options
│   ├── socket_darwin.go                 IP_BOUND_IF / IPV6_BOUND_IF
│   ├── socket_linux.go                  SO_BINDTODEVICE(需 CAP_NET_RAW,失败降级报错)
│   ├── socket_windows.go                IP_UNICAST_IF
│   ├── socket_other.go                  其余 GOOS 桩(返回 error,防未来加平台编译断)
│   ├── socket_test.go
│   ├── stun.go                          STUN Binding+双服务器判型+失败记忆(TTL)
│   ├── stun_test.go
│   ├── tls.go                           现签证书+指纹钉扎+token 首流门禁的 QUIC TLS 配置
│   ├── tls_test.go
│   ├── listener.go                      B 侧:绑定+quic.Listen+TTL 收摊
│   ├── listener_test.go
│   ├── dialer.go                        A 侧:候选交换回调+QUIC Dial 重试循环+2.5s 预算
│   ├── dialer_test.go
│   └── session.go                       每 peer QUIC 会话缓存+refcount+KeepAlive 15s
│       └── session_test.go
├── outbound/proxy/mesh/punch.go         UDPTransport 薄适配器(注入 dial func)
├── outbound/proxy/mesh/punch_test.go
├── outbound/proxy/mesh/spec.go          Spec 加 Punch 位并入 CacheKey(运行期开关能重建客户端)
├── outbound/proxy/mesh/sshexit.go       NewExitClient 装配序插 udp(条件 append)
├── app/http/services/agent_peer_punch.go  WS 帧 A/B 两端处理(services 层编排)
├── app/http/services/agent_peer_punch_test.go
├── app/http/services/agent_remote_ws.go   dispatch case×3 + 注册 map×3(修改)
├── app/http/models/agent_protocol.go      三帧常量+契约表(修改)
├── processor/config/types.go              MeshConfig.PunchEnable(修改;validate 实名:MeshConfig.Validate/Config.ValidateMeshDependency/Config.ValidateMesh)
├── processor/config/(validate/allowlist)  校验+五处 allowlist 同步(修改)
├── processor/tcp/handler.go               SetMeshPunchDialSource 平行缝+getMeshExitForExecution 按 punch 开关 append UDP+[MESH-OUT] 载体可见性日志(修改)
├── app/tunnel/manager.go                  manager 侧接线 punch dial 源(镜像 :311 piko 缝)(修改)
├── app/tunnel/mesh_assembly.go            punch 生命周期(修改)
├── docs/superpowers/specs/2026-11-12-mesh-udp-punch-design.md   决策记录(本计划前置评审产物,简版)
└── go.mod                                 quic-go/pion-stun 转 direct

AliangPhoneServer(独立仓,独立 worktree ../AliangPhoneServer-punch)
└── server/src/modules/peer/punch.ts       三帧 zod schema+转发(仿 lan.ts)
    server/test/peerPunch.test.ts
```

**信令线协议(三帧,挂 peer 家族命名):**
| type | 方向 | Required | Optional | 语义 |
|---|---|---|---|---|
| `peer.punch.session` | server→B(A 发起) | type, request_id, session_id, token, ttl_seconds | advertise_ip | A 请 B 起 punch listener(token=A 现生成一次性) |
| `peer.punch.session.result` | B→server→A | type, request_id, ok | port, fingerprint, fail_reason | B 绑定结果(QUIC 服务端 UDP 端口+证书指纹) |
| `peer.punch.candidates` | 双向,server 按 session_id 转发对端 | type, session_id, candidates[] | — | 双方 STUN 公网候选(host:port 数组) |

teardown=v1 无显式 close 帧:B 侧 listener TTL 到点即收摊(照抄 agentLanListen 语义);QUIC 会话层 refcount 归零+空闲 5min 即关。

---

### Task 1: 帧契约——三常量 + 契约表

**Files:**
- Modify: `app/http/models/agent_protocol.go`(常量区 :139 附近 + 契约表 :370 附近)
- Test: `app/http/models/agent_protocol_test.go`(若已有契约表测试文件则追加)

- [ ] **Step 1: 写失败测试**——仿既有契约测试,断言三常量值与契约表 Required/Optional/Emits 精确匹配(含 `peer.punch.session` Emits `peer.punch.session.result`)。
- [ ] **Step 2: 跑测试确认失败**:`go test ./app/http/models/ -run Punch -v` → 未定义。
- [ ] **Step 3: 最小实现**:

```go
AgentEventPeerPunchSession       = "peer.punch.session"        // server→B(A 发起): 请求起一次性 punch listener
AgentEventPeerPunchSessionResult = "peer.punch.session.result" // B→server→A: 绑定结果(port+fingerprint)或失败原因
AgentEventPeerPunchCandidates    = "peer.punch.candidates"     // 双向经 server 按 session_id 转发对端: STUN 公网候选交换
```

契约表三条按上文线协议表逐字落(Required/Optional/Emits)。
- [ ] **Step 4: 跑测试通过**。 - [ ] **Step 5: 提交**`测试:peer.punch 三帧契约——常量+契约表`。

### Task 2: 依赖转正——quic-go direct + pion/stun 新增

**Files:** `go.mod` / `go.sum`
- [ ] `go get github.com/quic-go/quic-go@v0.59.0 github.com/pion/stun@latest && go mod tidy`
- [ ] **哨兵检查**(memory npm-cli#4828 教训):`grep -c 'rollup-linux' package-lock.json` 应不变;`go build ./...` 全绿。
- [ ] `go test ./outbound/proxy/mesh/... ./app/tunnel/...` 仍绿。 - [ ] 提交`新增:quic-go 转 direct+pion/stun——UDP 打洞载体依赖`。

### Task 3: punch socket 物理接口绑定(三平台)

**Files:** `app/mesh/punch/socket.go` + `socket_{darwin,linux,windows}.go` + `socket_test.go`

设计:所有打洞 UDP socket 出厂即绑默认物理接口(darwin `IP_BOUND_IF`/`IPV6_BOUND_IF`、linux `SO_BINDTODEVICE`、windows `IP_UNICAST_IF`);接口名由 `inbound/tun/runner/utils/device.go:GetDefaultInterface` 解析;**绑定失败即返回错误→上层载体自禁落 piko**(不静默裸奔,TUN-on 下裸 socket 的 STUN 应答描述的是引擎换发 socket 的映射,洞打错 5-tuple——评审已定谳为致命而非风险)。

- [ ] **Step 1: 失败测试**:`NewBoundUDPConn()` 返回的 conn 的 `LocalAddr()` 端口非 0、关闭不 panic;`resolveDefaultInterface` 在测试环境(lo0/回环)返回 error 时的降级分支可注入(接口名解析函数作参数)。
- [ ] **Step 2: 失败确认** → **Step 3: 实现**:

```go
// socket.go(app/mesh/punch)
type InterfaceResolver func() (string, error) // 默认= device.GetDefaultInterface

func NewBoundUDPConn(resolve InterfaceResolver) (*net.UDPConn, string, error) {
    iface, err := resolve()
    if err != nil { return nil, "", fmt.Errorf("punch socket: resolve interface: %w", err) }
    // udp4 起步(v1 不做 IPv6 打洞;候选解析 To4 断言对齐 agent_peer_lan.go:96)
    conn, err := bindUDP4(iface)
    if err != nil { return nil, "", fmt.Errorf("punch socket: bind %s: %w", iface, err) }
    return conn, iface, nil
}
```

平台文件各自实现 `bindUDP4(iface string) (*net.UDPConn, error)`:`net.ListenConfig{Control: …}` 内 `unix.IP_BOUND_IF`(darwin)/`unix.SockSetBindToDevice`→`SO_BINDTODEVICE`(linux)/`IP_UNICAST_IF`(windows,sockaddr 结构转换)。签名与 `inbound/tun/dialer/sockopt_*.go` 同构但独立成包(inbound/tun/dialer 的函数未导出且耦合其 Options,复制 ~40 行而非引依赖环)。
- [ ] **Step 4: 通过**(darwin 本机真跑;linux/windows 文件编译过即可——`GOOS=linux go build ./app/mesh/punch/`、`GOOS=windows go build ./app/mesh/punch/`,**注意 memory go-implicit-goos-suffix-test-trap:平台文件测试不依赖 -list 幻绿,核心逻辑在 socket.go 跨平台测试**)。
- [ ] 提交`新增:punch socket 三平台物理接口绑定——绑定失败即载体自禁`。

### Task 4: STUN 地址发现 + 双服务器判型 + 失败记忆

**Files:** `app/mesh/punch/stun.go` + `stun_test.go`

- [ ] **Step 1: 失败测试**:(a) loopback 起 mini STUN responder(pion/stun 的 `stun.Agent`/手工构 XOR-MAPPED-ADDRESS 响应字节即可,不必引 pion/turn),`Discover()` 返回其宣告的 ip:port;(b) 两个 responder 宣告**不同**端口 → 判型 `symmetricSuspect=true`;(c) 相同端口 → false;(d) 失败记忆:判 symmetric 后 5min 内 `ShouldSkip()`=true,过期恢复。
- [ ] **Step 2: 失败确认** → **Step 3: 实现**:

```go
// stun.go(app/mesh/punch)
type PublicMapping struct{ IP net.IP; Port int }
func Discover(conn *net.UDPConn, server string, timeout time.Duration) (PublicMapping, error) // pion/stun Binding,经绑定 socket 发出
func Classify(conn *net.UDPConn, servers []string) (PublicMapping, bool /*symmetricSuspect*/, error) // 双服务器端口比对
type SkipMemo struct{ … } // per-peer symmetric 失败记忆,5min TTL
```

STUN 服务器默认档:`stun.l.google.com:19302` + `stun1.l.google.com:19302`(包内常量;v1 不加配置覆盖项——YAGNI,coturn 自托管是二期看数据再定的事,届时一并加配置)。失败(两台都不可达)≠ symmetric,按「STUN 不可用」处理落 piko 并**不**写 SkipMemo。
- [ ] **Step 4: 通过**。 - [ ] 提交`新增:STUN 双服务器判型+per-peer 对称 NAT 失败记忆(5min TTL)`。

### Task 5: QUIC TLS 信任件(现签证书+指纹钉扎+token 首流门禁)

**Files:** `app/mesh/punch/tls.go` + `tls_test.go`

复用 `agent_peer_lan.go` 的证书机器:同包(services)已有 `agentLanCert` 族函数——punch 在 **services 层**组装(见 Task 10),本包只收最终件:

```go
// tls.go(app/mesh/punch)
type PunchTLSConfig struct {
    ServerCert tls.Certificate         // B 侧现签 1h 证书(generateAgentLanCertificate 直接返回 tls.Certificate+指纹,不做 PEM 中转)
    ClientCert tls.Certificate         // A 侧现签
    ClientFingerprint  string          // B 钉 A 的指纹(与 agentLan 同构的 hex 指纹串)
    ServerFingerprint  string          // A 钉 B 的指纹(session.result 帧带回)
    ExpectedToken      string          // 一次性 token
}
func ServerTLSConfig(c PunchTLSConfig) *tls.Config // RequireAndVerifyClientCert+自定义 verify(指纹钉扎,subtle.ConstantTimeCompare)
func ClientTLSConfig(c PunchTLSConfig) *tls.Config  // InsecureSkipVerify+VerifyPeerCertificate 钉 B 指纹(fail-closed,无 TOFU)
func TokenGate(stream quic.Stream, token string) error // 首流首字节 token 比对,不符即关(照抄 agentLan 首行门禁语义)
```

- [ ] 失败测试:两端 loopback `tls.Dial`/QUIC 握手——指纹对→过;错→握手失败;token 错→ `TokenGate` 报错。 - [ ] 实现 → 通过 → 提交`新增:punch QUIC TLS 信任件——双向指纹钉扎 fail-closed+token 首流门禁`。

### Task 6: B 侧 punch listener

**Files:** `app/mesh/punch/listener.go` + `listener_test.go`

```go
type ListenerOpts struct {
    TTL        time.Duration        // 默认 90s,到点收摊(agentLanListen 先例语义)
    Token      string
    TLS        PunchTLSConfig
    OnCandidates func(cands []string) // 收到 A 的 candidates 帧回调(services 层转发给 A)
}
func ServeListener(conn *net.UDPConn, opts ListenerOpts) (localPort int, done <-chan struct{}, err error)
// 内部: quic.Transport{Conn: conn}.Listen(tlsServer) → accept 循环:
//   session accept → 首流 TokenGate → 之后每条 stream 就是一个"到 sshd 的连接":
//   stream 交给注入的 handler(net.Conn 语义——quic.Stream 满足)
```

- [ ] 失败测试:loopback 起 listener → `quic.Dial` 假客户端带 token → 拿 stream 写读;错 token → 连接被关;TTL 到点 listener 退出(done 关闭)。
- [ ] 通过 → 提交`新增:punch B 侧 listener——TTL 收摊+token 门禁+stream=连接`。

### Task 7: A 侧打洞编排(dial)

**Files:** `app/mesh/punch/dialer.go` + `dialer_test.go`

```go
type DialOpts struct {
    Budget        time.Duration // 2.5s fail-fast(常量 DialBudget)
    MyCandidates  []string      // services 层 STUN 后填
    PeerCandidates []string     // 信令收到后填(原子更新)
    Token, TLS…                  // 同 PunchTLSConfig
}
func Dial(ctx context.Context, conn *net.UDPConn, opts DialOpts) (quic.Connection, error)
// 循环:对每个 peer 候选 quic.Transport.Dial(每轮换随机化候选顺序);
// QUIC Initial 包即探测流量(评审定谳:无需独立 probe 包);
// 每 300ms 一轮直至预算到→errPunchTimeout(上层语义=落 piko,非错误态)
```

- [ ] 失败测试:loopback 双端(B 侧 Task 6 listener + A 侧 Dial)端到端打通;budget=50ms 无候选→errPunchTimeout。
- [ ] 通过 → 提交`新增:punch A 侧打洞编排——QUIC Initial 重试即探测+2.5s 预算`。

### Task 8: 载体会话缓存 + refcount

**Files:** `app/mesh/punch/session.go` + `session_test.go`

```go
type SessionCache struct{ … } // key=peerDeviceID → {conn quic.Connection, ref int, lastUse}
func (c *SessionCache) Acquire(peer string) (*Lease, error) // 有活会话→ref++ 复用;无→返回未建立(上层 Dial)
func (l *Lease) OpenStream() (net.Conn, error)  // quic conn.OpenStream+超时包 net.Conn 适配
func (l *Lease) Release()                        // ref--;0 起空闲计时
// 空闲 5min+KeepAlivePeriod 15s 双参数:有 ref 期间 quic.Config.KeepAlivePeriod=15s 刷 NAT 映射;
// ref=0 关会话停保活(评审不变式:「保活只为在飞连接服务」)
```

- [ ] 失败测试:两次 Acquire 同 peer 复用同一 conn;Release 归零→空闲到期关闭(计时器可注入);ref>0 不关。
- [ ] 通过 → 提交`新增:punch 载体会话缓存——跨拨号 QUIC 复用+refcount 保活不变式`。

### Task 9: UDPTransport + 装配序 + CacheKey punch 位

**Files:** `outbound/proxy/mesh/punch.go` + `punch_test.go`;Modify `sshexit.go:NewExitClient`;Modify `spec.go`(Spec 加 Punch 位)

```go
// punch.go(outbound/proxy/mesh)
type PunchDialFunc func(ctx context.Context, peerID string) (net.Conn, error) // 上层:SessionCache lease.OpenStream(未建立则现场打洞)
// UDPTransport{Name:"udp", Admit: 全类别+配置开(通过构造期入参 gate,关→不进切片,镜像 PikoTransport relay 门),
//   IsRelay:false, DialSSHD: t.dial(ctx, peer.DeviceID)}
// NewExitClient: transports := LAN;条件 append UDP{dial};再 append Piko —— 顺序 LAN→udp→piko(SPEC §7.3)
```

**CacheKey 语义(评审定谳的运行期开关问题):** `spec.CacheKey`(spec.go:38,现哈希 DeviceID/LANAddrs/HostKey/Relay/SSHUser/密钥摘要)不含 punch 态——tcp 主路径 `getMeshExitForExecution` 按 CacheKey 缓存 ExitClient,运行期翻 `punch_enable` 不会重建缓存客户端=开关静默失效到进程重启。修法:Spec 加 `Punch bool` 位并入 CacheKey 哈希;工厂(`getMeshExitForExecution`,Task 12)按调用时刻配置置位——翻开关→CacheKey 变→新客户端含/不含 UDP。

- [ ] 失败测试:Admit 三类别全放;**gate 关闭构造变体下 Admit 恒 false 的防御分支直断言**;IsRelay=false(错误进 lan 桶);装配序断言(LAN 在前、piko 恒最后、punch 开时居中);dial func 注入生效;**CacheKey:Punch=true/false 产出不同键,Punch 变化触发重建**(getMeshExitForExecution 层面的缓存行为断言放 Task 12)。
- [ ] 通过 → 提交`新增:UDPTransport 第三载体——全类别准入+LAN→udp→piko 装配序+CacheKey punch 位`。

### Task 10: WS 帧处理(A/B 两端)

**Files:** `app/http/services/agent_peer_punch.go` + test;Modify `agent_remote_ws.go`(dispatch case×**3** + 注册 map)

**dispatch 三例(评审纠偏,非两例):** B 端收 `peer.punch.session` + `peer.punch.candidates`;A 端收 `peer.punch.session.result` + `peer.punch.candidates`——session.result 仿 `case models.AgentEventPeerLanListenResult:`(agent_remote_ws.go:837)的 pending-request 机制。

B 端(`peer.punch.session`):enabled-device 门禁(照抄 handleAgentPeerLanListen 的 safeGo+门禁)→ 走 Task 3-6:`NewBoundUDPConn→Classify→ServeListener`→回 `session.result{ok,port,fingerprint}`;失败回 `{ok:false,fail_reason}`。
A 端:发 `session`(request_id+session_id+token+ttl)→ 等 result(超时=fail)→ `Classify` 得自身候选 → 发 `peer.punch.candidates` → 等 B 候选(services 层先收再回调进 DialOpts)→ Task 7 Dial → 成功则建 SessionCache lease,**同时注册 punch dial 源**(经 Task 12 的 `SetMeshPunchDialSource` 平行缝,镜像既有 `SetMeshTransportDialSource`(processor/tcp/handler.go:1358,typed `func() meshproxy.PikoDialFunc`,app/tunnel/manager.go:311 接线)的形态——punch 不改旧缝签名,加平行缝)。
`peer.punch.candidates` 双向:收帧按 session_id 路由回调。

- [ ] 失败测试:帧 payload 解析(仿 agent_peer_lan 的 parse 家族);A 端全流程与 Task 6 listener 在**进程内 loopback** 打通(信令回调用 channel 直连,不过 WS);B 端门禁拒绝路径。
- [ ] 通过 → 提交`新增:peer.punch 三帧 WS 处理——A/B 两端编排+punch dial 源注册`。

### Task 11: 配置面 + allowlist 五处同步

**Files:** `processor/config/types.go` + 校验 + **五处 allowlist(memory 铁律)**

`MeshConfig.PunchEnable bool`(零值 false=关,让渡型能力须显式开——mesh 前例);validate:开 punch 但 mesh 关→报错(挂进既有 `Config.ValidateMesh`/`Config.ValidateMeshDependency` 链,实名以 types.go:962/1044 为准);**逐一 grep `ForwardEnable`(或任一 Mesh 既有字段)定位 allowlist/前端契约副本五处逐字同步**——提交信息写明五处清单。

- [ ] 失败测试:默认关;开依赖校验;JSON round-trip 含新字段。
- [ ] 通过 → 提交`新增:customer.mesh.punch_enable 配置+校验+allowlist 五处同步`。

### Task 12: tcp 主路径接线 + 装配 + 可见性日志(评审阻塞修复核心)

**Files:** Modify `processor/tcp/handler.go`(主 mesh 拨号路径);Modify `app/tunnel/manager.go`;Modify `app/tunnel/mesh_assembly.go`

**主路径接线(不做则功能在主流量上死——评审阻塞问题①):** `getMeshExitForExecution`(handler.go:1490 调 NewExitClient)是 toNode mesh 流量(`dialViaMesh`)的 ExitClient 工厂,必须与两条装配缝(mesh_assembly 的 egressPeer/meshDNSForwardPeer)同获 UDP 载体:

1. **平行缝**:handler.go 新增 `SetMeshPunchDialSource(typed func() meshproxy.PunchDialFunc)`,镜像既有 `SetMeshTransportDialSource`(handler.go:1358,typed `func() meshproxy.PikoDialFunc`)——不改旧缝签名,punch 走平行新缝;
2. **工厂改造**:`getMeshExitForExecution` 读调用时刻配置+punch dial 源,构造 Spec 时置 `Punch` 位(Task 9 的 CacheKey 位),NewExitClient 后 append `UDPTransport` 于 LAN 与 Piko 之间;
3. **manager 接线**:app/tunnel/manager.go 在 piko 缝接线点(:311 旁)同位接线 punch dial 源(services 层 Task 10 打洞成功后写入)。

punch 组件生命周期挂 mesh 总开关(mesh_assembly,镜像 beacon 装配:开→构造,关→收摊)。

**载体可见性日志必做且落主路**(评审定谳:旧 server+新 agent 静默丢弃帧,必须有「本次拨号走的载体 lan/udp/piko + udp 失败原因」日志行——落 handler.go `[MESH-OUT]`(:1396 既有拨号失败日志点)与装配缝两处,保证真实流量路径可见)。

- [ ] 失败测试:开关闭→主路工厂产出的客户端切片无 UDP;开→居中;**翻开关→CacheKey 变→缓存重建**(getMeshExitForExecution 缓存行为断言);punch dial 源未注入→工厂不产 UDP(哨兵);[MESH-OUT] 载体日志行断言。
- [ ] 通过 → 提交`新增:punch tcp 主路径接线——平行缝+工厂 CacheKey 位+载体可见性日志`。

### Task 13: PhoneServer 三帧转发(跨仓)

**Files:** `/Users/mac/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer`(独立 worktree,兄弟目录 `/Users/mac/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer-punch`)/`server/src/modules/peer/punch.ts` + `server/test/peerPunch.test.ts`;**接线点=`server/src/modules/transfer/service.ts`(:67 经 `requestAgentPayload` 分派 agent 帧——评审核实 peer/routes.ts 不注册 agent 帧)**;schema 归属:zod 在 `server/src/schemas.ts` 与 `modules/agent/handlers/transfer.ts` 一线(lan.ts 本体是 TS interface 非 zod——punch 帧走 agent 帧管线,故 zod 加在 agent 帧层,peer/punch.ts 收编排与转发逻辑)

三帧校验(必填字段;**注意 memory:zod 非法值整包拒是既有教训——schema 只加不减,未知帧保持透传行为与现网一致**);转发逻辑:session(A→B)、session.result(B→A)、candidates(按 session_id 路由对端);审计行照抄 lan 家族。

- [ ] 失败测试(仿 peerLan.test.ts):三类帧合法载荷转发到对端设备;缺字段 400/拒;未知设备拒。
- [ ] 通过 → 提交(PhoneServer 仓,中文)`新增:peer.punch 三帧转发——打洞信令路由`。

### Task 14: 端到端集成(本仓 test/integration)

**Files:** `test/punch_integration_test.go`

双进程/双组件:两个 punch 编排实例经**本地 mock 信令桥**(实现帧收发接口的内存转发,拓扑等价 PhoneServer)+ loopback socket——验证 契约→listener→candidates→Dial→SessionCache 复用→TTL 收摊 全链;**负路径**:信令无响应→2.5s 预算落 errPunchTimeout→上层(装配层)语义为 piko 可拨。

- [ ] 通过 → 提交`测试:mesh punch 端到端——mock 信令桥 loopback 全链+超时回落`。

### Task 15: 文档 + 发布说明要点

**Files:** `docs/superpowers/specs/2026-11-12-mesh-udp-punch-design.md`(决策记录:全类别拍板/会话缓存 ADR/打洞率预期 70% 基线/iPhone 不进数据面话术);TODOS.md 记遗留(v1 无端口预测、IPv6 打洞、coturn 触发条件)。

- [ ] 提交`文档:punch 载体设计决策+遗留账`。

---

## 验收口径(评审定案,写进 spec)

- 打洞率预期锚 **70%±7.1%**(ACM IMC 2025)而非 Tailscale 自报 90%+;验收按网络族分桶,单一办公网数据不算数。
- iPhone 恒走云端控制面(四路线一致的产品边界,发布说明前置)。
- macOS 本地网络权限/Windows 防火墙弹窗被拒→UDP 静默失败恒走 piko,诊断面板暴露 UDP 可用性(诊断面板 UI 属后续批次,本期日志行先落)。
- 二期触发条件:真机打洞率数据出来后再定 coturn 自托管与端口预测。
