# 统一入口协议分派实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 56432 成为唯一入口监听口:accept 后嗅探分派 ssh / socks5 / mesh 帧 / http 四类;普通流量进既有规则管线(本节点=出口),mesh 流量走组网语义(终点→本机规则,非终点→续传);56441/56442 退役。

**Architecture:** 原地改造分派器(spec D5):`inbound/sniff` 新包做嗅探+回放,`inbound/http` 的连接入口改 `dispatchConn`;SSH 经 `HandleIngressConn` 注入缝、mesh 经 `MeshConnHandler` 接线钩子;监听生命周期=代理运行态(D4 修订,run service 链零改动);新建 `inbound/socks5`。

**Tech Stack:** Go 标准库 net/bufio、golang.org/x/crypto/ssh(既有)、processor/tcp 既有管线。

**Spec:** `docs/superpowers/specs/2026-11-09-unified-ingress-protocol-dispatch-design.md`(先读,§3 决策表+§5 嗅探规格+§6 改造清单是本计划的事实来源)

**执行纪律:**
- 工作树:按仓库惯例先建 worktree(`git worktree add .worktrees/unified-ingress -b feat/unified-ingress-dispatch`),所有改动在工作树内;主仓 `dist/` 不搬,go build/test 不依赖它。
- 提交:每任务一次提交,标题「新增:/修复:」全中文,结尾加 `Co-Authored-By: Claude Code <noreply@anthropic.com>`。**开始执行前需用户明确批准**(仓库 commit 纪律)。
- 已知预存 flake(auth 基线 5/10、agent_ai 子进程超时三测试):失败时先确认是否基线预存,禁止复跑洗白。
- 新测试文件不得带 `_windows` 后缀(会静默跳过),写完先 `go test -list` 确认真实执行。

---

## 里程碑 M1:嗅探+分派核心(56432 变统一口,旧口暂留双轨)

### Task 1: `inbound/sniff` 嗅探包

**Files:**
- Create: `inbound/sniff/sniff.go`
- Test: `inbound/sniff/sniff_test.go`

- [ ] **Step 1: 写失败测试**(表驱动六样本:SSH- 前缀 / socks 0x05 05 00 / `{"` JSON / HTTP POST / 未知二进制 0xFF 0xFE... / 超时;每样本断言 kind + 回放无损=经返回 conn 能读回完整原始字节流)

```go
package sniff

import (
	"bytes"
	"net"
	"testing"
	"time"
)

func TestSniffClassifiesAndReplays(t *testing.T) {
	cases := []struct {
		name  string
		input []byte
		want  Kind
	}{
		{"ssh banner", []byte("SSH-2.0-OpenSSH_9\r\n"), KindSSH},
		{"socks greeting", []byte{0x05, 0x02, 0x00, 0x02}, KindSocks},
		{"mesh json frame", []byte(`{"from":"dev-a","class":"ai","target":"h:443","via":["dev-a"],"hops":0}` + "\n" + "payload"), KindMesh},
		{"http request", []byte("POST /v1/messages HTTP/1.1\r\nHost: x\r\n\r\n"), KindHTTP},
		{"unknown binary", []byte{0xFF, 0xFE, 0x00, 0x01}, KindHTTP},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			go func() { _, _ = client.Write(tc.input) }()
			kind, c, err := Sniff(server)
			if err != nil {
				t.Fatalf("Sniff: %v", err)
			}
			if kind != tc.want {
				t.Fatalf("kind = %v, want %v", kind, tc.want)
			}
			got, err := io.ReadAll(c) // net.Pipe 写端已关才 EOF——改用写端关闭协程
			if err != nil {
				t.Fatalf("replay read: %v", err)
			}
			if !bytes.Equal(got, tc.input) {
				t.Fatalf("replay mismatch: got %q", got)
			}
		})
	}
}

func TestSniffTimeoutCloses(t *testing.T) {
	old := sniffTimeout
	sniffTimeout = 50 * time.Millisecond
	defer func() { sniffTimeout = old }()
	client, server := net.Pipe()
	defer client.Close()
	_, _, err := Sniff(server)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
```

注意(必须照做,勿照抄片段):`net.Pipe` 无 EOF 除非写端关闭——回放断言前必须 `client.Close()`(放进 goroutine 写完后关),且测试文件需 import `io`。建议抽 helper:`func pipeWith(input []byte) (net.Conn /*server侧*/, net.Conn /*client侧*/)` + `func writeAndClose(...)`。超时样本不进入回放断言。

- [ ] **Step 2: 跑测试确认失败**:`go test ./inbound/sniff/ -v` → 预期 FAIL(包不存在则先建最小骨架)
- [ ] **Step 3: 写实现**

```go
// Package sniff 实现统一入口协议嗅探(spec §5):读前 4 字节分类协议族,
// 已读字节经回放缓存无损交给下游。本包不依赖任何协议包,不做门禁——
// 分类器只分类,准入归各协议处理器。
package sniff

import (
	"bufio"
	"fmt"
	"net"
	"time"
)

type Kind int

const (
	KindUnknown Kind = iota // 嗅探失败:调用方断连
	KindSSH                 // "SSH-" 版本串
	KindSocks               // 0x05 socks5 greeting(第二字节=方法数≤0x7F 收窄)
	KindMesh                // "{" JSON 帧头候选(完整解析归 HandleMeshConn)
	KindHTTP                // 其余:按 HTTP 处理
)

func (k Kind) String() string { /* Unknown/SSH/Socks/Mesh/HTTP */ }

// sniffTimeout 读窗口;var 为测试缝(默认 2s 即生产值)。
var sniffTimeout = 2 * time.Second

// Sniff 分类协议族。返回的 conn 后续 Read 先消费已缓冲字节再落到底层,
// 对下游完全透明;调用方负责关闭。首段为空(超时/对端直断)返回错误。
func Sniff(conn net.Conn) (Kind, net.Conn, error) {
	_ = conn.SetReadDeadline(time.Now().Add(sniffTimeout))
	br := bufio.NewReader(conn)
	head, err := br.Peek(4)
	_ = conn.SetReadDeadline(time.Time{})
	if len(head) == 0 {
		return KindUnknown, nil, fmt.Errorf("sniff: no data: %w", err)
	}
	return classify(head), &sniffedConn{Conn: conn, r: br}, nil
}

func classify(head []byte) Kind {
	switch {
	case len(head) >= 4 && string(head[:4]) == "SSH-":
		return KindSSH
	case len(head) >= 2 && head[0] == 0x05 && head[1] <= 0x7F:
		return KindSocks
	case head[0] == '{':
		return KindMesh
	default:
		return KindHTTP
	}
}

type sniffedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *sniffedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
```

- [ ] **Step 4: 跑测试确认通过**:`go test ./inbound/sniff/ -v`
- [ ] **Step 5: 提交**:`git add inbound/sniff && git commit`(新增:统一入口嗅探包——四协议族分类+已读字节回放)

### Task 2: `inbound/ssh` 注入缝(监听暂留,M3 拆)

**Files:**
- Modify: `inbound/ssh/server.go`(handleConn 处,约 :290)
- Modify: `inbound/ssh/reconcile.go`
- Test: `inbound/ssh/server_test.go`(新增用例,既有不动)

- [ ] **Step 1: 写失败测试**(用既有 sshtest 对端模式:起测试 TCP listener→accept→`srv.ServeConn(ctx, conn)`→断言 bare 直连 direct-tcpip 全链通;再断言 `HandleIngressConn` 在 reconcile 停态返回 false)

```go
func TestServeConnBareInjection(t *testing.T) {
	srv, _ := startTestServer(t, rc) // 既有构造 server_test.go:40,签名 (t, rc RunConfig):NewServer(rc, tmpDir hostkey)+Start
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	go func() {
		c, err := l.Accept()
		if err == nil {
			srv.ServeConn(context.Background(), c)
		}
	}()
	// 用 sshtest 客户端对 l.Addr() 发起公钥认证 + direct-tcpip,断言注入到
	// handler 的 Metadata 与既有 bare 用例一致(复用既有断言 helper)。
}
```

- [ ] **Step 2: 跑测试确认失败**(`ServeConn` 未定义)
- [ ] **Step 3: 实现**——`server.go`:

```go
// ServeConn 承载一条外部注入的已 accept 连接(统一口分派缝,spec §6.4):
// 限速/握手 deadline/双形态/通道收敛与 listener 路径完全同语义。阻塞至
// 连接结束;Stop 的快照拆除与 wg 排水同样覆盖注入连接。入口未运行时
// 关连接返回(与「入口未启动」语义等价)。
func (s *Server) ServeConn(ctx context.Context, conn net.Conn) {
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	if !running {
		_ = conn.Close()
		return
	}
	signer, err := s.ensureHostKey()
	if err != nil {
		logger.Debug(fmt.Sprintf("SSH ingress serve conn: host key: %v", err))
		_ = conn.Close()
		return
	}
	s.wg.Add(1)
	s.handleConn(ctx, conn, s.serverConfig(signer))
}
```

`reconcile.go` 加导出缝(读侧持 reconcileMu 快照实例,serve 在锁外):

```go
// HandleIngressConn 统一口分派缝(spec §6.4):SSH 连接交在跑实例处理。
// 返回 false=入口未配置(期望停),调用方断连——不回显任何协议错误。
func HandleIngressConn(conn net.Conn) bool {
	reconcileMu.Lock()
	srv := reconcileSrv
	reconcileMu.Unlock()
	if srv == nil {
		return false
	}
	srv.ServeConn(context.Background(), conn)
	return true
}
```

- [ ] **Step 4: 跑包测试确认全绿**:`go test ./inbound/ssh/ -v`(既有 Start 路径用例必须保持通过——双轨暂态)
- [ ] **Step 5: 提交**(新增:SSH 入口连接注入缝 ServeConn/HandleIngressConn——监听路径语义不变)

### Task 3: `inbound/http` 分派器改造(56432 变统一口)

**Files:**
- Modify: `inbound/http/server.go`(`handleRawConnection` :129)
- Create: `inbound/http/dispatch.go`
- Test: `inbound/http/dispatch_test.go`(新增;handler_test.go 既有不动)

- [ ] **Step 1: 写失败测试**(表驱动:对 `dispatchConn` 喂四类样本;ssh 分支注入 `HandleIngressConn` 返回 false 时连接被关;mesh 分支 `MeshConnHandler=nil` 断连;HTTP 样本走既有 CONNECT 流程——用本地 httptest 式断言或最小 fake;socks 分支暂断连)

- [ ] **Step 2: 确认失败**
- [ ] **Step 3: 实现** `dispatch.go`:

```go
// MeshConnHandler 统一口 mesh 帧分派钩子(spec §6.2):装配层(core boot)
// 经 tunnel.WireUnifiedIngressMeshDispatch 接线为 HandleMeshConn(carrier=lan);
// nil=未装配,连接断开。inbound 层不反向 import app 层。
var MeshConnHandler func(conn net.Conn)

// dispatchConn 统一入口分派(spec §4):嗅探→按协议族移交。conn 关闭归
// 本函数(defer),各处理器内再关为幂等双关(与既有 server.go 同模式)。
func dispatchConn(conn net.Conn) {
	defer conn.Close()
	kind, c, err := sniff.Sniff(conn)
	if err != nil {
		logger.Debug(fmt.Sprintf("[DISPATCH] sniff failed from %s: %v", conn.RemoteAddr(), err))
		return
	}
	switch kind {
	case sniff.KindSSH:
		// ssh_ingress 未启用:直接断连,不回显协议错误(spec §4 钉死)。
		_ = sshin.HandleIngressConn(c)
	case sniff.KindMesh:
		if MeshConnHandler == nil {
			logger.Debug("[DISPATCH] mesh frame rejected: handler not wired")
			return
		}
		MeshConnHandler(c) // 拒绝行/注入语义归 HandleMeshConn,返回即终
	case sniff.KindSocks:
		handleSocksConn(c) // M2 落地前为占位:Debug+return(与旧「非 HTTP 断连」等价)
	default:
		handleHTTPConn(c)
	}
}
```

`server.go`:`handleRawConnection` 拆为 `handleHTTPConn(conn net.Conn)`(原函数体)**保留** `reader := bufio.NewReader(c)`(评审补:`http.ReadRequest` 需要 `*bufio.Reader`,透明代理分支还要把 reader 传给 `HandleHttpConnection`——对 sniffedConn 再包一层 bufio 即可,双层缓冲读路径无损),`go dispatchConn(conn)` 替换原 `go handleRawConnection(conn)`。

- [ ] **Step 4: 包测试+`go build ./...` 全绿**
- [ ] **Step 5: 提交**(新增:56432 统一入口分派器——嗅探后移交 ssh/mesh/http,socks 占位)

### Task 4: 绑定统一 `UnifiedIngressListenAddr` + 变更重启

**Files:**
- Modify: `processor/config/defaults.go`(新增函数)
- Modify: `inbound/http/server.go`(:47 绑定点)
- Modify: `app/http/services/customer_config_service.go`(reconcile 钩子)
- Modify: `cmd/core.go:188`、`cmd/start.go:123`、`cmd/tray.go:77`(同族调用点各加一行)
- Test: `processor/config/defaults_test.go`(新增用例)

- [ ] **Step 1: 失败测试**:mesh 开→`0.0.0.0:56432`;mesh 关/未配置→`JoinHostPort(ServiceBindHost(),56432)`(临时改 serviceBindHost 验证;注意用既有测试注入缝,没有就 set/unset 全局配置快照)

```go
func UnifiedIngressListenAddr() string {
	host := ServiceBindHost()
	if cfg := GetGlobalConfig(); cfg != nil && cfg.Customer != nil && cfg.Customer.Mesh != nil && cfg.Customer.Mesh.EffectiveEnabled() {
		host = "0.0.0.0" // mesh 开=LAN 组网可达(现状 LAN 载体 parity,spec D2)
	}
	return net.JoinHostPort(host, strconv.Itoa(DefaultHTTPProxyPort))
}
```

- [ ] **Step 2: 失败 → Step 3: 实现函数;`StartMitmHttp` 的 `config.HTTPProxyListenAddr()` 改 `config.UnifiedIngressListenAddr()`(日志同步)**
- [ ] **Step 4: 绑定变更重启**:`customer_config_service.go` 新增 `ReconcileUnifiedIngressBinding(cfg)`(与 `ReconcileSSHIngressFromConfig` 同层):期望地址≠`httpServer.CurrentListenAddr()` 且 listener 在跑→`StopHttpProxy()+go StartMitmHttp()`。需要给 inbound/http 加只读 `CurrentListenAddr() string`(httpMutex 下读)。四个调用点(配置提交协调路径+core/start/tray 防漂移)各加一行。
- [ ] **Step 5: 包测试 + services 包既有测试全绿 → 提交**(修复:统一入口绑定跟随启用面——mesh 开 0.0.0.0,变更即重启)

---

## 里程碑 M2:socks5 入站

### Task 5: `inbound/socks5` 包

**Files:**
- Create: `inbound/socks5/socks5.go`
- Test: `inbound/socks5/socks5_test.go`

- [ ] **Step 1: 失败测试**(表驱动):no-auth 协商成功;客户端不含 0x00→reply 0xFF 断连;CONNECT IPv4/域名/IPv6 三类解析断言 Metadata;BIND(0x02)/UDP ASSOCIATE(0x03)→reply 0x07;会话租约失败→reply 0x02;喂管线断言 `tcp.GetHandler().Handle` 收到 conn+Metadata(测试注入 handler seam)
- [ ] **Step 2: 失败**
- [ ] **Step 3: 实现**(RFC1928 常量+状态机;门禁顺序=先服务面后租约;handler seam 用包级 var 供测试注入,生产由 dispatch 接线为 `tcp.GetHandler().Handle`)

```go
// Package socks5 实现统一入口的 socks5 入站(spec §6.3):仅 CONNECT,
// 仅 no-auth,门禁=会话租约(与 HTTP 同档)。UDP ASSOCIATE/BIND 显式
// reply 命令不支持。喂 processor/tcp 统一管线后本节点视作出口。
package socks5

// TCPHandleFunc 与 TCPConnHandler.Handle 同签名(含 ctx),测试注入缝。
type TCPHandleFunc func(ctx context.Context, conn net.Conn, md *M.Metadata) error

// TCPHandler 测试覆写用;nil=生产路径,调用期惰性走 tcp.GetHandler().Handle
// (评审补:避免包 init 期触发 tcp 单例装配)。
var TCPHandler TCPHandleFunc

func dialHandler() TCPHandleFunc {
	if TCPHandler != nil {
		return TCPHandler
	}
	return func(ctx context.Context, conn net.Conn, md *M.Metadata) error {
		return tcp.GetHandler().Handle(ctx, conn, md)
	}
}

const (
	socksVer      = 0x05
	authNone      = 0x00
	authNoAccept  = 0xFF
	cmdConnect    = 0x01
	repSuccess    = 0x00
	repNotAllowed = 0x02 // connection not allowed by ruleset(无会话)
	repCmdUnsupp  = 0x07 // command not supported
)

// Handler 处理一条嗅探为 socks 的连接(已含回放缓存)。阻塞至连接结束;
// 关闭归本函数。dispatch 独占调用。
func Handler(conn net.Conn) { defer conn.Close(); serve(conn) }

func serve(conn net.Conn) {
	// 1) greeting: VER NMETHODS METHODS…;不含 0x00 → write reply(VER,0xFF) 断连
	// 2) 请求: VER CMD RSV ATYP ADDR PORT;CMD!=CONNECT → reply 0x07 断连
	//    ATYP: 0x01 IPv4 / 0x03 域名(1B len) / 0x04 IPv6;畸形→断连
	// 3) reply(VER,REP,RSV,ATYP=0x01,BND=0.0.0.0:0)
	// 4) 会话租约:auth.GetSessionAuthority().AcquireProxyLease(失败→重写 reply 0x02 后断连;
	//    注意 reply 只能写一次——租约检查提前到 reply 之前做,失败直接 0x02)
	// 5) 构 M.Metadata{Network:TCP, HostName/DstPort, SrcIP/SrcPort, DstIP(若 IP)}
	//    ConnID 用独立序号段 "socks-N"(对齐 mesh-N/tun- 惯例)
	// 6) dialHandler()(ctx, conn, metadata) 喂统一管线(含 ctx;错误即断连)
}
```

实现注意:成功 reply 在**租约检查之后**写(避免写两次 reply);reply 前可先读完请求段。

- [ ] **Step 4: 全绿 → Step 5: 提交**(新增:socks5 入站——CONNECT-only+会话门禁,喂统一管线)

### Task 6: dispatch 接 socks

**Files:**
- Modify: `inbound/http/dispatch.go`(KindSocks 分支)
- Modify: `inbound/http/dispatch_test.go`

- [ ] **Step 1: 测试**:dispatch 喂 socks 样本→socks5.Handler 被调(fake handler seam 断言)
- [ ] **Step 2: 失败 → Step 3: 实现**:Task 3 占位分支替换为 `socks5.Handler(c)` 即可——**缝归 socks5 包(`TCPHandler` var + `dialHandler()` 惰性默认,见 Task 5),dispatch 不做任何赋值**(评审定案:在 dispatch 侧赋值会重新引入 import 期单例装配)
- [ ] **Step 4: 全绿 → Step 5: 提交**(新增:统一口分派接入 socks5 处理器)

---

## 里程碑 M3:旧口退役+收口

### Task 7: SSH 独立监听退役

**Files:**
- Modify: `inbound/ssh/server.go`(Start :83-108、acceptLoop :256-277、Addr :150-157)
- Modify: `inbound/ssh/reconcile.go`(runIdentity 去 port、Bind host 注释更新)
- Modify: `inbound/ssh/config.go`(RunConfig.Port 标废弃注释)
- Test: `inbound/ssh/server_test.go`、`reconcile_test.go`(迁移)

- [ ] **Step 1: 测试迁移先行**:既有「Start 随机口+拨 Addr」用例改为「测试 listener accept→ServeConn 注入」(抽 helper `serveViaListener(t, srv)` 供复用);reconcile 测试去 port 断言
- [ ] **Step 2: 实现**:Start 去 `net.Listen`/acceptLoop/`l.Addr` 日志,保留 ensureHostKey+ctx+running 置位(注释:统一口承载监听,spec §6.4);acceptLoop 删除;`Addr()` 删除(调用方 grep 清理);`runIdentity` 去 port 字段与比较项;`RunConfig.Port` 注释「已废弃:统一口 56432 承载,运行面忽略(下批次清配置 schema)」;reconcile 注释的 Bind host 段改指统一口绑定策略
- [ ] **Step 3: `go test ./inbound/ssh/ -v` 全绿(迁移后用例等价覆盖)→ Step 4: `go build ./...` → Step 5: 提交**(修复:SSH 入口退役独立监听——统一口注入承载,host key/限速/双形态零改动)

### Task 8: mesh LAN 载体退役 + beacon 指统一口 + 分派接线

**Files:**
- Modify: `app/tunnel/mesh_assembly.go`(DefaultMeshLANPort :44、meshLAN.listener :130、startMeshLANLocked :214、teardownMeshLANLocked :239、listenMeshLAN :258、**MeshLANPort :273-280**)
- Create: `app/tunnel/mesh_dispatch.go`(WireUnifiedIngressMeshDispatch)
- Modify: `cmd/core.go`(:182 后加一行)
- Modify: `app/http/handlers/mesh_handler.go`(:37 `lanPort: tunnel.MeshLANPort`,不动代码——值随 MeshLANPort 变)
- Test: `app/tunnel/mesh_assembly_test.go`(迁移)、`app/http/handlers/mesh_handler_test.go`(:183 断言 56441→56432)

- [ ] **Step 1: 失败测试**:装配 desired=true 后 `lanbeacon.Default` 广播端口=56442→改断言 56432(`config.DefaultHTTPProxyPort`);无 listener 字段;`WireUnifiedIngressMeshDispatch` 接线后 `http.MeshConnHandler!=nil` 且喂 JSON 帧头→fake deps 注入(复用 mesh_inbound 测试模式)

- [ ] **Step 2: 实现**:

```go
// mesh_dispatch.go — 统一口 mesh 帧分派接线(spec §6.5):conn→HandleMeshConn
// (carrier=lan)。core boot 调用一次;mesh 关闭态由 HandleMeshConn 帧门禁
// 拒收,无需在此判断。设备身份每连接现读(闭包)。
func WireUnifiedIngressMeshDispatch(deviceID func() string) {
	httpIngress.MeshConnHandler = func(conn net.Conn) {
		selfID := strings.TrimSpace(deviceID())
		_ = tcp.HandleMeshConn(conn, meshproto.CarrierLAN, relay.ProductionDeps(selfID)())
	}
}
```

`startMeshLANLocked` 收缩为 beacon-only(`lanbeacon.NewBeacon(selfID, config.DefaultHTTPProxyPort, meshLAN.beaconPort)`;beacon 失败=本次不置 running,下周期重试);`teardownMeshLANLocked` 去 listener 段;`listenMeshLAN`/`DefaultMeshLANPort`/`meshLAN.listener` 删除;`relay` import 若仅 LAN 用则删(manager.go 的 `config.meshRelay` 走自己的构造,不动)。

**MeshLANPort() 改造(:273-280,评审补)**:函数体改返回 `config.DefaultHTTPProxyPort`(语义=LAN 载体承载口即统一口;不再读已删除的 listener 字段),**doc comment 一并改写**(原「0=未装配/未启动」语义随常量化失效,注释与行为不得矛盾)。下游 `/api/mesh/status` 的 `lan_listen_port` 字段**保留**,值随之为 56432(仪表盘消费面零改动);`mesh_handler_test.go:183` 的 56441 断言可同步改 56432——评审核实该用例注入的是假 lanPort 闭包,与真实函数解耦,改与不改测试都绿,顺手改即可。
`cmd/core.go:182` 后:`tunnel.WireUnifiedIngressMeshDispatch(services.GetSharedAgentService().CurrentDeviceID)`。

- [ ] **Step 3: `go test ./app/tunnel/... ./app/mesh/... -v` 全绿 → Step 4: 提交**(修复:mesh LAN 载体退役——统一口分派承接,beacon 广播统一口)

### Task 9: A 侧 LAN 拨号双端口候选

**Files:**
- Modify: `outbound/proxy/mesh/mesh.go`(:128 拨号循环)
- Test: `outbound/proxy/mesh/mesh_test.go`(新增候选展开用例)

- [ ] **Step 1: 失败测试**:`lanDialCandidates("1.2.3.4:56441")`→`["1.2.3.4:56441","1.2.3.4:56432"]`;`:56432`→单条;畸形(无端口)→原样单条
- [ ] **Step 2: 实现**:

```go
// lanDialCandidates 手填地址双端口候选(spec §6.7):原样优先(旧版对端
// 56441 直达),追加统一口候选(手填旧口+对端已升级的场景);已是统一口
// 或畸形地址不展开。beacon 发现项带实际端口,同函数受益(追加项与实际
// 端口不同才追加)。
func lanDialCandidates(addr string) []string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == strconv.Itoa(config.DefaultHTTPProxyPort) {
		return []string{addr}
	}
	return []string{addr, net.JoinHostPort(host, strconv.Itoa(config.DefaultHTTPProxyPort))}
}
```

拨号循环改为候选展开逐项尝试(失败即试下一项,保持既有 lastErr 汇总)。`outbound/proxy/mesh` import `processor/config`(低层,无环)。

- [ ] **Step 3: 全绿 → Step 4: 提交**(修复:mesh A 侧 LAN 拨号双端口候选——兼容未升级对端与手填旧口)

### Task 10: G5 拒绝行统一(SSH mesh 形态)

**Files:**
- Modify: `processor/tcp/mesh_inbound.go`(:467 writeMeshRejectLine)
- Modify: `inbound/ssh/mesh_inject.go`(:16-28 码表、writeMeshRejectLine :181、injectMeshFrame 各调用点)
- Test: `processor/tcp/mesh_inbound_test.go`、`inbound/ssh/mesh_inject_test.go`(断言改纯文本)

- [ ] **Step 1: 先读两个既有实现**(processor/tcp 版收 `net.Conn`+`err`,写 `err.Error()+"\n"`;ssh 版收 `gossh.Channel`+code,写 JSON)。processor/tcp 加导出薄壳:

```go
// WriteMeshRejectLine 导出给 SSH 载体形态复用的 mesh 拒绝行(spec §6.4 G5
// 收口):纯文本一行,行格式与 TCP 载体一致;写入尽力而为。
func WriteMeshRejectLine(conn net.Conn, reason string) {
	msg := strings.ReplaceAll(strings.TrimSpace(reason), "\n", " ")
	_ = conn.SetWriteDeadline(time.Now().Add(meshRejectWriteTimeout))
	_, _ = conn.Write([]byte(msg + "\n"))
}
```

内部 `writeMeshRejectLine` 改为 `WriteMeshRejectLine(conn, err.Error())` 委托。

- [ ] **Step 2: ssh 侧**:删 JSON 码表与 `writeMeshRejectLine(ch, code)`;新 helper 用既有 `channelConn{Channel:ch, remote:connRemote, local:占位 net.TCPAddr{}}` 包 channel 后调 `tcphandler.WriteMeshRejectLine(conn, reason)`;各调用点 reason 对齐 TCP 载体短语(loop/forward disabled/forwarder not allowed/hops exceeded/dns target unsupported)。dns: 拒绝分支照旧保留。
- [ ] **Step 3: 两包测试断言改纯文本行 → 全绿 → Step 4: 提交**(修复:SSH mesh 形态拒绝行统一为 TCP 载体纯文本——收口集成账 G5)

### Task 11: 仪表盘/文案

**Files:**
- Modify: `app/website/src/components/settings/SshIngressSettings.vue`(端口输入移除/标废弃,展示统一口)
- Modify: `app/website/src/components/SettingsPage.vue`(如引用端口字段)
- Modify: i18n 文案文件(`app/website/src` 下 grep `ssh_ingress`/`sshIngress` 键定位,五处副本纪律:加字段逐一 grep 旧键名同步)

- [ ] **Step 1: grep 定位端口消费点**:`grep -rn "ssh_ingress\|sshIngress" app/website/src --include="*.vue" --include="*.ts" --include="*.json"`
- [ ] **Step 2: 改造**:端口输入框移除(或禁用+「已统一至 56432」提示);卡片端口展示改常量 56432;LAN 手填地址提示文案改「统一口 56432 或依赖发现」;新增键全量 grep 同步(避免漏第五处)
- [ ] **Step 3: 前端测试**(vitest,若该组件有测试则更新;无则跑既有套件确认不破)→ **Step 4: 提交**(修复:仪表盘 SSH 入口卡片对齐统一口——端口展示与文案)

### Task 12: 全量回归收口

- [ ] **Step 1: 全仓构建+静态**:`go build ./... && go vet ./... && gofmt -l .`(gofmt 应无输出)
- [ ] **Step 2: 定向测试**:`go test ./inbound/... ./processor/tcp/... ./processor/config/... ./app/mesh/... ./app/tunnel/... ./outbound/proxy/mesh/... ./app/http/services/... -count=1`
- [ ] **Step 3: 发布说明要点核对**(spec §8 五条逐项在 CHANGELOG/发布说明草稿落位)
- [ ] **Step 4: 提交**(若无零散改动则并入前次;不强制空提交)

---

## 风险与回退

- 每任务独立提交,任一里程碑后可回退到上一提交;M1-M3 间双轨暂态(旧口仍在)保证每步可运行。
- 最大风险点:Task 3 分派改造破坏既有 HTTP 路径——`inbound/http` 既有测试(handler_test.go)+ 新增 dispatch 测试双保险;Task 7 测试迁移量大,迁移先行。
- 56441 退役后「旧版 A→新版 B」LAN 直连断(混合版本,D1 已接受,发布说明承担);「新版 A→旧版 B」由 Task 9 双候选兼容。
