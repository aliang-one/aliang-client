package test

// mesh UDP 打洞批次 Task 14——端到端集成测试:mock 信令桥双编排全链
// (test/mesh_integration_test.go 同包同风格,黑盒只消费导出 API)。
//
// 拓扑(全 loopback,零外呼):
//
//	A 侧编排体(services 共享单例,经 Task 14 导出面拨号)
//	  │ 自发帧(session/candidates)
//	  ▼
//	mock 信令桥(拓扑等价 PhoneServer:session 帧按 target_device_id 路由到
//	B 并剥路由信封;result/candidates 回程路由;loopback 无 NAT,B 的候选
//	=按 result 端口合成的本地地址——生产对应 B 经 STUN 学到的公网映射)
//	  │ services.AgentPeerPunchHandleFrame 投递面
//	  ▼
//	B 侧编排体(services 共享单例:绑 socket→STUN 判型→起 QUIC listener)
//	  │ 过门流喂 sshin.HandleIngressConn(生产 handler)
//	  ▼
//	真 inbound/ssh 入口(sshin.Reconcile 装配,mesh 凭证+ForwardGate)
//	  │ mesh-exit channel({"t":"dns"}:代解析分支,查询 localhost 零外呼)
//	  ▼
//	A 侧 mesh.ExitClient(真装配 LAN→udp→piko,udp 载体=PunchDialFunc 即
//	services.AgentPeerPunchDial)在打洞流上完成真 SSH 握手+DNS 往返
//
// 与 10a/10b 包内单测的区别:那边自持注入实例(bind/classify/serve 全桩),
// 这里走 services 导出面驱动的共享单例——bind/servers/classify/gate 全是
// 生产件,仅两点环境级替换:punch.DefaultResolver(导出变量,换绑 loopback
// 口)与 config.SetGlobalConfig(开 customer.mesh.punch_enable)。真 STUN
// 判型从 loopback 口对公网地址必然快速失败(sendto unreachable,实测
// ~170ms),两侧按「不可达≠symmetric 不记账,打洞照试」铁律继续——这正
// 是生产弱网路径,候选缺失由桥的合成候选补(loopback 无 NAT,见上)。
//
// 环境限制:物理口绑定在 linux 需 CAP_NET_RAW(SO_BINDTODEVICE),非 root
// 环境显式 skip(punch 包 socket_test.go 同款口径);非 darwin/linux 平台
// 无绑定实现,skip。
//
// 已知边界(刻意不测,punch/session.go 文件头已立档):B 侧 TTL 收摊是静默
// 关停,A 侧缓存会话有 ~30s 僵尸窗口——同 peer 立即重拨会先撞僵尸(OpenStream
// 本地成功、首写黑洞),由 ExitClient 的 SSH 握手 deadline 兜底落 piko(该
// 回落语义正是 (d1)/(d2) 两用例的断言面)。故 (c) 的「下次拨号」用新 peer
// 验证 B 侧收摊后的服务能力,同 peer 僵尸窗口不在本文件重复立档。
//
// 超时纪律:所有拨号/读写一律局部 watchdog bound(到点 Fatal 不挂死);
// 最长单用例 (d1) 信令等待 10s(services 包级常量 agentPunchResultWait,
// 未导出不可覆写),bound 20s。全套实测 ~17s。

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/app/mesh/punch"
	sshin "aliang.one/nursorgate/inbound/ssh"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	meshproto "aliang.one/nursorgate/outbound/meshproto"
	mesh "aliang.one/nursorgate/outbound/proxy/mesh"
	"aliang.one/nursorgate/processor/config"

	"github.com/miekg/dns"
	gossh "golang.org/x/crypto/ssh"
)

// ===== 夹具:mock 信令桥 =====

// punchSignalingBridge 内存信令桥(PhoneServer 替身)。方向判别按帧型:
// session=A 发(唯一带路由信封的帧型)/ result=B 发 / candidates 在本拓扑
// 恒为 A 发——loopback 口对公网 STUN 必然判型失败,B 按契约无映射可发、
// 从不发 candidates(B→A 方向的候选由桥在 result 后合成投递,见 sink)。
// 计数与帧记录供缓存复用/契约断言;模式开关供负路径用例。
type punchSignalingBridge struct {
	mu             sync.Mutex
	aSessionFrames []map[string]interface{} // A→桥:session 帧(路由信封原样保留)
	aCandidates    []map[string]interface{} // A→桥:candidates 帧(loopback 下常为空候选表)
	deliveredToB   []map[string]interface{} // 桥→B 已投递(session 帧已剥信封)
	resultsToA     []map[string]interface{} // B→桥→A:result 帧
	synthToA       [][]string               // 桥→A 合成候选(生产对应 B 的 STUN 公网映射)
	peerSessions   map[string]int           // 对端 → session 帧计数(缓存复用断言)
	sessByRequest  map[string]string        // request_id → session_id(server 的会话关联记账)

	dropSession    bool // 负路径 (d1):信令无响应(session 帧不投 B)
	deadCandidates bool // 负路径 (d2):给 A 投死候选(不可达端口)
}

func newPunchSignalingBridge() *punchSignalingBridge {
	return &punchSignalingBridge{
		peerSessions:  map[string]int{},
		sessByRequest: map[string]string{},
	}
}

// punchNormalizeFrame 线型归一:真 WS 线上帧是 JSON(数字=float64、数组=
// []interface{}),而进程内直传的 Go map 带 []string/int 等原生形态——
// remoteStringSlice 只认 []interface{},桥作为 PhoneServer 等价物,出入
// 一律过 JSON round-trip(10a 包内 punchFrameSink.write 同款手法),保证
// 投递面吃到的帧与生产 WS 读路径逐字同型。
func punchNormalizeFrame(v map[string]interface{}) map[string]interface{} {
	raw, err := json.Marshal(v)
	if err != nil {
		return v // 理论不可达(map[string]interface{} 恒可序列化)
	}
	out := map[string]interface{}{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return v
	}
	return out
}

// punchCopyMap 浅拷贝帧(桥的记录不与编排体的读写共享底层数据)。
func punchCopyMap(src map[string]interface{}) map[string]interface{} {
	dst := make(map[string]interface{}, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// punchPortOf result 帧的 port 字段(进程内传递是 int;按数值类型宽容解)。
func punchPortOf(msg map[string]interface{}) int {
	switch v := msg["port"].(type) {
	case int:
		return v
	case float64:
		return int(v)
	case int64:
		return int(v)
	}
	return 0
}

// sink 是接进 services.AgentPeerPunchSetFrameSink 的帧出口:A 侧 send 与
// B 侧回帧共用(双角色同进程),按帧型路由。记录与投递分离——锁内只记账,
// HandleFrame 投递在锁外(投递会同步驱动 B 侧编排、嵌套回调本 sink)。
func (br *punchSignalingBridge) sink(payload map[string]interface{}) error {
	payload = punchNormalizeFrame(payload) // 记录与投递同用线型(见 punchNormalizeFrame)
	switch payload["type"] {
	case models.AgentEventPeerPunchSession:
		br.mu.Lock()
		br.aSessionFrames = append(br.aSessionFrames, punchCopyMap(payload))
		peer, _ := payload["target_device_id"].(string)
		br.peerSessions[peer]++
		req, _ := payload["request_id"].(string)
		sid, _ := payload["session_id"].(string)
		br.sessByRequest[req] = sid
		drop := br.dropSession
		br.mu.Unlock()
		if drop {
			return nil // 负路径 (d1):信令无响应——B 侧从未见过这帧
		}
		// server 契约(Task 13):按 target_device_id 寻址 B,转发前剥信封
		// ——B 收到的帧与 Task 1 锁定契约逐字一致。
		frame := punchCopyMap(payload)
		delete(frame, "target_device_id")
		br.deliverSession(frame)
	case models.AgentEventPeerPunchSessionResult:
		br.mu.Lock()
		br.resultsToA = append(br.resultsToA, punchCopyMap(payload))
		dead := br.deadCandidates
		br.mu.Unlock()
		services.AgentPeerPunchHandleFrame(payload) // result 按 request_id 进 A 侧 pending
		if ok, _ := payload["ok"].(bool); ok && punchPortOf(payload) > 0 {
			// 生产行为等价物:B 把 Classify 学到的公网映射经 candidates 回发
			// (server 按 session_id 转给 A)。loopback 无 NAT,「映射」=B 的
			// listener 本地地址;负路径 (d2) 投死候选。
			port := punchPortOf(payload)
			cand := fmt.Sprintf("127.0.0.1:%d", port)
			if dead {
				cand = "127.0.0.1:1" // 无人监听:烧穿 A 侧 2.5s 拨号预算
			}
			req, _ := payload["request_id"].(string)
			br.mu.Lock()
			sid := br.sessByRequest[req]
			br.synthToA = append(br.synthToA, []string{cand})
			br.mu.Unlock()
			services.AgentPeerPunchHandleFrame(punchNormalizeFrame(map[string]interface{}{
				"type":       models.AgentEventPeerPunchCandidates,
				"session_id": sid,
				"candidates": []string{cand},
			}))
		}
	case models.AgentEventPeerPunchCandidates:
		// loopback 不变量:恒为 A→B(B 判型必败无映射可发,见结构体注)。
		br.mu.Lock()
		br.aCandidates = append(br.aCandidates, punchCopyMap(payload))
		br.mu.Unlock()
		br.deliverCandidates(payload)
	}
	return nil
}

// deliverSession 把 session 帧(须已剥信封)投进 B 侧编排体并记账。
func (br *punchSignalingBridge) deliverSession(frame map[string]interface{}) {
	frame = punchNormalizeFrame(frame)
	br.mu.Lock()
	br.deliveredToB = append(br.deliveredToB, punchCopyMap(frame))
	br.mu.Unlock()
	services.AgentPeerPunchHandleFrame(frame)
}

// deliverCandidates 把 candidates 帧投给双端接收面(server 按 session_id
// 双向转发的等价物:投递面内部同时喂 B 侧注册表与 A 侧 pending)。
func (br *punchSignalingBridge) deliverCandidates(frame map[string]interface{}) {
	frame = punchNormalizeFrame(frame)
	br.mu.Lock()
	br.deliveredToB = append(br.deliveredToB, punchCopyMap(frame))
	br.mu.Unlock()
	services.AgentPeerPunchHandleFrame(frame)
}

// deliverReapSession 投一帧同 session_id 的短 TTL session——生产语义=
// 「服务端重试/WS 重连重发」:B 侧 register 顶替旧会话(旧 listener 即刻
// 收摊,v1 无 close 帧,顶替即显式收摊路径),新 listener 按短 TTL 到点。
func (br *punchSignalingBridge) deliverReapSession(sessionID string, ttlSeconds int64) {
	br.deliverSession(map[string]interface{}{
		"type":        models.AgentEventPeerPunchSession,
		"request_id":  fmt.Sprintf("pi-reap-%d", time.Now().UnixNano()),
		"session_id":  sessionID,
		"token":       "pi-reap-token",
		"ttl_seconds": ttlSeconds,
	})
}

// ---- 观测面(锁内快照) ----

func (br *punchSignalingBridge) sessionFramesForPeer(peer string) int {
	br.mu.Lock()
	defer br.mu.Unlock()
	return br.peerSessions[peer]
}

func (br *punchSignalingBridge) totalSessionFrames() int {
	br.mu.Lock()
	defer br.mu.Unlock()
	return len(br.aSessionFrames)
}

func (br *punchSignalingBridge) lastSessionIDForPeer(peer string) string {
	br.mu.Lock()
	defer br.mu.Unlock()
	for i := len(br.aSessionFrames) - 1; i >= 0; i-- {
		if p, _ := br.aSessionFrames[i]["target_device_id"].(string); p == peer {
			sid, _ := br.aSessionFrames[i]["session_id"].(string)
			return sid
		}
	}
	return ""
}

func (br *punchSignalingBridge) resultCount() int {
	br.mu.Lock()
	defer br.mu.Unlock()
	return len(br.resultsToA)
}

func (br *punchSignalingBridge) resultAt(i int) map[string]interface{} {
	br.mu.Lock()
	defer br.mu.Unlock()
	return punchCopyMap(br.resultsToA[i])
}

func (br *punchSignalingBridge) synthCandidates() [][]string {
	br.mu.Lock()
	defer br.mu.Unlock()
	out := make([][]string, len(br.synthToA))
	copy(out, br.synthToA)
	return out
}

func (br *punchSignalingBridge) aSessionFrameAt(i int) map[string]interface{} {
	br.mu.Lock()
	defer br.mu.Unlock()
	return punchCopyMap(br.aSessionFrames[i])
}

// aCandidatesCount A 侧发出的 candidates 帧数(loopback 下 A 判型必败,
// 帧里是空候选表——但帧本身必须发,B 侧等待面才有着落)。
func (br *punchSignalingBridge) aCandidatesCount() int {
	br.mu.Lock()
	defer br.mu.Unlock()
	return len(br.aCandidates)
}

// deliveredFramesOfType 桥→B 已投递帧里取指定帧型(契约断言用)。
func (br *punchSignalingBridge) deliveredFramesOfType(frameType string) []map[string]interface{} {
	br.mu.Lock()
	defer br.mu.Unlock()
	var out []map[string]interface{}
	for _, f := range br.deliveredToB {
		if t, _ := f["type"].(string); t == frameType {
			out = append(out, punchCopyMap(f))
		}
	}
	return out
}

func (br *punchSignalingBridge) setDropSession(drop bool) {
	br.mu.Lock()
	br.dropSession = drop
	br.mu.Unlock()
}

func (br *punchSignalingBridge) setDeadCandidates(dead bool) {
	br.mu.Lock()
	br.deadCandidates = dead
	br.mu.Unlock()
}

// ===== 夹具:环境(resolver/config/sink/入口)与拨号件 =====

// punchIngressNode 真 inbound/ssh 入口(reconcile 装配,B 侧 punch 流的
// 生产 handler 经 sshin.HandleIngressConn 喂进的就是这个实例)。
type punchIngressNode struct {
	addr   string // 专用口监听地址
	hostFP string // host key SHA256 指纹(PeerSpec.HostKey 钉扎直用)
}

// startPunchReconciledIngress 起一台 reconcile 管理的真入口(mesh 凭证=
// A 身份公钥,ForwardGate 开——mesh-exit dns 分支要过闸)。与 mesh 集成
// 测试的 startIngressNode 区别:那边 NewServer 直起(不进 reconcile 全局,
// HandleIngressConn 看不见),这边必须走 Reconcile——punch 流的落点是
// HandleIngressConn,不是专用口 TCP。
func startPunchReconciledIngress(t *testing.T, admittedMeshPub gossh.PublicKey) *punchIngressNode {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成入口 host key 失败: %v", err)
	}
	hostSigner, err := gossh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatalf("构造 host signer 失败: %v", err)
	}
	block, err := gossh.MarshalPrivateKey(hostPriv, "punch integration ingress host key")
	if err != nil {
		t.Fatalf("序列化 host key 失败: %v", err)
	}
	hostKeyPath := filepath.Join(t.TempDir(), "ingress_host_key")
	if err := os.WriteFile(hostKeyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("写 host key 失败: %v", err)
	}

	rc := sshin.RunConfig{
		MeshKeys:    []sshin.MeshKey{{Key: admittedMeshPub, DeviceID: "dev-pi-a"}},
		ForwardGate: func() bool { return true }, // dns 分支要过闸
	}
	for attempt := 0; attempt < 3; attempt++ {
		rc.Port = meshGrabFreePort(t)
		sshin.Reconcile(&rc, hostKeyPath, func() string { return "dev-pi-b" })
		if sshin.IsRunning() && sshin.Addr() != "" {
			t.Cleanup(func() { sshin.Reconcile(nil, "", nil) })
			return &punchIngressNode{
				addr:   sshin.Addr(),
				hostFP: gossh.FingerprintSHA256(hostSigner.PublicKey()),
			}
		}
	}
	t.Fatal("reconcile 入口三次尝试均未起听(端口被抢占?)")
	return nil
}

// punchLoopbackIface 平台 loopback 接口名;空串=平台不支持(测试 skip)。
func punchLoopbackIface() string {
	switch runtime.GOOS {
	case "darwin":
		return "lo0"
	case "linux":
		return "lo"
	default:
		return ""
	}
}

// punchEnv 一次集成用例的全套环境:桥+入口+A 身份。逐用例新建,防共享
// 单例(services 侧 A/B 编排体、SessionCache)的状态跨用例串味;每用例
// 用独立 peerID,缓存命中面互不干扰。
type punchEnv struct {
	bridge  *punchSignalingBridge
	ingress *punchIngressNode
	aPEM    string
}

// newPunchEnv 装配环境并挂全部清理:resolver 换 loopback、config 开
// punch、sink 接桥、reconcile 入口。预检绑定失败(linux 非 root 缺
// CAP_NET_RAW)按环境限制 skip。
func newPunchEnv(t *testing.T) *punchEnv {
	t.Helper()

	// ① resolver → loopback 口(punch.DefaultResolver 是导出变量,测试整体
	// 替换——bind 链其余部分(平台绑定选项/OS 分配端口)保持生产件)。
	iface := punchLoopbackIface()
	if iface == "" {
		t.Skipf("平台 %s 无物理口绑定实现,punch 集成测试不适用", runtime.GOOS)
	}
	oldResolver := punch.DefaultResolver
	punch.DefaultResolver = func() (string, error) { return iface, nil }
	t.Cleanup(func() { punch.DefaultResolver = oldResolver })
	preflight, _, err := punch.NewBoundUDPConn(nil)
	if err != nil {
		if runtime.GOOS == "linux" && strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("linux SO_BINDTODEVICE 需 CAP_NET_RAW,当前环境无权限: %v", err)
		}
		t.Fatalf("loopback 绑定预检失败: %v", err)
	}
	_ = preflight.Close()

	// ② config 开 punch 载体门(customer.mesh.punch_enable;agentPunchConfig
	// -Enabled 只读本位——mesh 总开关的合取在保存期校验与装配门,不经此路)。
	punchOn := true
	oldCfg := config.GetGlobalConfig()
	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{Mesh: &config.MeshConfig{PunchEnable: &punchOn}},
	})
	t.Cleanup(func() { config.SetGlobalConfig(oldCfg) })

	// ③ 桥接进信令出口(生产恒 nil,清理复位)。
	bridge := newPunchSignalingBridge()
	services.AgentPeerPunchSetFrameSink(bridge.sink)
	t.Cleanup(func() { services.AgentPeerPunchSetFrameSink(nil) })

	// ④ B 侧真入口 + A 侧 mesh 身份。
	aPEM, _, aPub := meshKeyPair(t)
	ingress := startPunchReconciledIngress(t, aPub)
	return &punchEnv{bridge: bridge, ingress: ingress, aPEM: aPEM}
}

// newExitClient 造真装配 ExitClient:LAN 恒死(127.0.0.1:1)逼装配序走
// udp;udp 载体=services.AgentPeerPunchDial(生产同源拨号源);piko 按
// 用例给桩(d 系)或不给(a/b/c/e 系)。
func (e *punchEnv) newExitClient(t *testing.T, peerID string, pikoDial mesh.PikoDialFunc) *mesh.ExitClient {
	t.Helper()
	client, err := mesh.NewExitClient(mesh.PeerSpec{
		DeviceID: peerID,
		LANAddrs: []string{"127.0.0.1:1"}, // 必败:UDP 打洞才有出场机会
		HostKey:  e.ingress.hostFP,
		Relay:    true,
	}, e.aPEM, "", pikoDial, services.AgentPeerPunchDial)
	if err != nil {
		t.Fatalf("NewExitClient: %v", err)
	}
	return client
}

// punchDialBounded 有界 traced 拨号(挂死保护;bound 到点 Fatal)。dns
// 目标:B 侧 dns 分支只读帧不进 tcp 管线,查询 localhost 走 /etc/hosts,
// 外呼面为零。类别显式 self:custom 禁公共中继,piko Admit 需要。
func punchDialBounded(t *testing.T, client *mesh.ExitClient, bound time.Duration) (net.Conn, mesh.DialTrace) {
	t.Helper()
	type dialResult struct {
		conn  net.Conn
		trace mesh.DialTrace
		err   error
	}
	ch := make(chan dialResult, 1)
	go func() {
		ctx := mesh.WithClass(context.Background(), meshproto.ClassSelf)
		c, tr, e := client.DialContextTraced(ctx, &M.Metadata{HostName: meshproto.DNSTarget, DstPort: 53})
		ch <- dialResult{c, tr, e}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("DialContextTraced: %v", r.err)
		}
		return r.conn, r.trace
	case <-time.After(bound):
		t.Fatalf("DialContextTraced 在 %v 内未返回(挂死?)", bound)
		return nil, mesh.DialTrace{}
	}
}

// punchDNSRoundTrip 打洞流上的真数据往返:SSH 已由 ExitClient 握毕,写
// DNS 查询帧(localhost A)读回响应,断言 NOERROR+127.0.0.1 记录——数据
// 面字节真正穿过了 QUIC 打洞流。
func punchDNSRoundTrip(t *testing.T, conn net.Conn) {
	t.Helper()
	query := new(dns.Msg)
	query.SetQuestion("localhost.", dns.TypeA)
	query.RecursionDesired = true
	wire, err := query.Pack()
	if err != nil {
		t.Fatalf("pack 查询失败: %v", err)
	}
	if err := meshWriteDNSFrame(conn, wire); err != nil {
		t.Fatalf("写查询帧失败: %v", err)
	}
	respBytes := meshReadDNSFrameBounded(t, conn, 5*time.Second)
	var resp dns.Msg
	if err := resp.Unpack(respBytes); err != nil {
		t.Fatalf("解响应失败 %q: %v", respBytes, err)
	}
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("rcode=%s, 期望 NOERROR", dns.RcodeToString[resp.Rcode])
	}
	found := false
	for _, rr := range resp.Answer {
		if a, ok := rr.(*dns.A); ok && a.A.Equal(net.IPv4(127, 0, 0, 1)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("answers=%v, 期望含 localhost 的 127.0.0.1 A 记录", resp.Answer)
	}
}

// punchUDPRefused 闭环 UDP 探测端口死亡:connected socket 写一字节后读,
// macOS/linux loopback 对无监听端口回 ICMP refused(实测 ~50µs);活着的
// QUIC listener 对非 QUIC 字节静默丢弃,读超时。true=端口已无人监听。
func punchUDPRefused(addr string, within time.Duration) bool {
	c, err := net.Dial("udp", addr)
	if err != nil {
		return false
	}
	defer c.Close()
	if _, err := c.Write([]byte("pi")); err != nil {
		return false
	}
	_ = c.SetReadDeadline(time.Now().Add(within))
	buf := make([]byte, 16)
	_, err = c.Read(buf)
	return err != nil && strings.Contains(err.Error(), "connection refused")
}

// ===== (a) 全链正路径 =====

// TestPunchIntegrationFullChain A 拨 dev-pi-b:桥路由→B 起 listener→
// 候选交换→真打洞(loopback QUIC)→SSH 握手→mesh-exit dns 往返,全程
// 走 udp 载体(载体轨迹 Used=="udp");对端 session 帧恰一次。
func TestPunchIntegrationFullChain(t *testing.T) {
	env := newPunchEnv(t)
	const peer = "dev-pi-b-a"
	client := env.newExitClient(t, peer, nil) // 本用例无 piko:udp 必须自己成

	conn, trace := punchDialBounded(t, client, 15*time.Second)
	defer conn.Close()

	if trace.Used != "udp" {
		t.Fatalf("载体轨迹 Used=%q, 期望 \"udp\"", trace.Used)
	}
	if trace.UDPErr != nil {
		t.Fatalf("udp 承载成功时 UDPErr 应为 nil, 得到 %v", trace.UDPErr)
	}
	punchDNSRoundTrip(t, conn)

	if got := env.bridge.sessionFramesForPeer(peer); got != 1 {
		t.Fatalf("对端 %s 的 session 帧数=%d, 期望恰 1", peer, got)
	}
}

// ===== (b) SessionCache 复用 =====

// TestPunchIntegrationSessionCacheReuse 第二次拨号不开新会话:桥的
// session 帧计数不增(A 侧 SessionCache 命中,B 侧 listener 免重起),
// 数据往返照通(第二条流走 B 侧免门直通)。
func TestPunchIntegrationSessionCacheReuse(t *testing.T) {
	env := newPunchEnv(t)
	const peer = "dev-pi-b-reuse"

	conn1, trace1 := punchDialBounded(t, env.newExitClient(t, peer, nil), 15*time.Second)
	if trace1.Used != "udp" {
		t.Fatalf("首拨载体 Used=%q, 期望 \"udp\"", trace1.Used)
	}
	punchDNSRoundTrip(t, conn1)
	_ = conn1.Close() // 只关 SSH 层:QUIC 会话留在 A 侧缓存里等复用

	conn2, trace2 := punchDialBounded(t, env.newExitClient(t, peer, nil), 15*time.Second)
	defer conn2.Close()
	if trace2.Used != "udp" {
		t.Fatalf("二拨载体 Used=%q, 期望 \"udp\"(缓存路径也算 udp 承载)", trace2.Used)
	}
	punchDNSRoundTrip(t, conn2)

	if got := env.bridge.sessionFramesForPeer(peer); got != 1 {
		t.Fatalf("缓存复用失败:session 帧数=%d, 期望仍为 1(第二次拨号不应重打洞)", got)
	}
}

// ===== (c) TTL 收摊 =====

// TestPunchIntegrationTTLCollapse B 侧 listener 生命周期到点可观测 +
// 收摊后服务能力不塌:
//  1. 首拨打通(bridge 记 session_id 与首 listener 端口 P1);
//  2. 桥投同 session_id 短 TTL 顶替帧(生产=服务端重试/重发)——旧
//     listener 即刻收摊(P1 端口死亡可探),新 listener 按短 TTL 到点
//     (P2 随之死亡);
//  3. 收摊之后的拨号(fresh peer,避开同 peer 的 ~30s 僵尸窗口——见文件
//     头已知边界)开新 session(总帧数 +1)照常打通。
func TestPunchIntegrationTTLCollapse(t *testing.T) {
	env := newPunchEnv(t)
	const peer = "dev-pi-b-ttl"

	conn1, _ := punchDialBounded(t, env.newExitClient(t, peer, nil), 15*time.Second)
	punchDNSRoundTrip(t, conn1)
	_ = conn1.Close()

	sessID := env.bridge.lastSessionIDForPeer(peer)
	if sessID == "" {
		t.Fatal("桥未记录到首拨 session 帧,无法构造顶替收摊")
	}
	p1 := punchPortOf(env.bridge.resultAt(0))
	if p1 <= 0 {
		t.Fatalf("首 result 端口=%d, 期望正数", p1)
	}

	// 顶替收摊:旧 listener 即刻死,新 listener TTL=1s。
	env.bridge.deliverReapSession(sessID, 1)
	deadline := time.Now().Add(5 * time.Second)
	for env.bridge.resultCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if env.bridge.resultCount() < 2 {
		t.Fatal("顶替帧未在 5s 内产生第二条 result(替换被拒?)")
	}
	p2 := punchPortOf(env.bridge.resultAt(1))
	if p2 <= 0 || p2 == p1 {
		t.Fatalf("顶替 result 端口=%d, 期望正数且≠旧端口 %d(顶替瞬间双活)", p2, p1)
	}

	// 旧 listener 已死:端口回 refused(死亡可观测)。
	if !punchUDPRefused(fmt.Sprintf("127.0.0.1:%d", p1), time.Second) {
		t.Fatalf("顶替后旧端口 %d 应无人监听(refused)", p1)
	}
	// 新 listener TTL 到点:同样死亡可观测(1s TTL + 收摊余量)。
	time.Sleep(1600 * time.Millisecond)
	if !punchUDPRefused(fmt.Sprintf("127.0.0.1:%d", p2), time.Second) {
		t.Fatalf("TTL 到点后端口 %d 应无人监听(refused)", p2)
	}

	// 收摊后服务能力:fresh peer 新 session(总帧数 1→2)照常打通。
	conn2, trace := punchDialBounded(t, env.newExitClient(t, "dev-pi-b-ttl-next", nil), 15*time.Second)
	defer conn2.Close()
	if trace.Used != "udp" {
		t.Fatalf("收摊后新拨载体 Used=%q, 期望 \"udp\"", trace.Used)
	}
	punchDNSRoundTrip(t, conn2)
	if got := env.bridge.totalSessionFrames(); got != 2 {
		t.Fatalf("收摊后总 session 帧数=%d, 期望 2(首拨+新拨各一)", got)
	}
}

// ===== (d) 负路径:信令无响应 → 装配层语义落 piko =====

// TestPunchIntegrationSignalSilenceFallsToPiko B 不回 result(桥吞掉
// session 帧=信令无响应)→ A 侧 result 等待预算(包级常量 10s)耗尽报错
// → ExitClient 装配序落 piko(真 sshd 落点:桩拨回真入口的 TCP 专用口)。
// 断言负路径本体:Used=="piko" && UDPErr 带 result timeout 痕迹(Task 12
// 载体轨迹契约:静默回落必须留痕),数据照通。
func TestPunchIntegrationSignalSilenceFallsToPiko(t *testing.T) {
	env := newPunchEnv(t)
	const peer = "dev-pi-b-silent"
	env.bridge.setDropSession(true)

	var pikoCalls atomic.Int32
	pikoStub := func(ctx context.Context, endpointID string) (net.Conn, error) {
		if endpointID != peer+".mesh" {
			return nil, fmt.Errorf("piko 端点=%q, 期望 %q", endpointID, peer+".mesh")
		}
		pikoCalls.Add(1)
		var d net.Dialer
		return d.DialContext(ctx, "tcp", env.ingress.addr) // 真 sshd 落点
	}
	client := env.newExitClient(t, peer, pikoStub)
	if names := client.TransportNames(); len(names) != 3 || names[0] != "lan" || names[1] != "udp" || names[2] != "piko" {
		t.Fatalf("装配序=%v, 期望 [lan udp piko]", names)
	}

	start := time.Now()
	conn, trace := punchDialBounded(t, client, 20*time.Second)
	defer conn.Close()

	if trace.Used != "piko" {
		t.Fatalf("信令无响应后载体 Used=%q, 期望 \"piko\"", trace.Used)
	}
	if trace.UDPErr == nil || !strings.Contains(trace.UDPErr.Error(), "result timeout") {
		t.Fatalf("UDPErr=%v, 期望含 result timeout(回落留痕)", trace.UDPErr)
	}
	if elapsed := time.Since(start); elapsed < 9*time.Second {
		t.Fatalf("result 等待预算未烧满即回落(elapsed=%v, 预算 10s)", elapsed)
	}
	if pikoCalls.Load() != 1 {
		t.Fatalf("piko 拨号次数=%d, 期望恰 1", pikoCalls.Load())
	}
	punchDNSRoundTrip(t, conn) // piko 腿数据照通(经真入口)
}

// TestPunchIntegrationDeadCandidatesFallToPiko 信令通、候选死:B 起
// listener 回 result,桥给 A 投不可达候选 → A 侧真打洞烧穿 2.5s 拨号预算
// (errPunchTimeout 哨兵)→ 落 piko。断言 errors.Is(UDPErr,
// punch.ErrPunchTimeout) 与 Used=="piko",数据照通。(B 侧这次起的
// listener 无人拨,TTL 90s 自然收摊——会话 id 独立,不扰他用例。)
func TestPunchIntegrationDeadCandidatesFallToPiko(t *testing.T) {
	env := newPunchEnv(t)
	const peer = "dev-pi-b-deadcand"
	env.bridge.setDeadCandidates(true)

	var pikoCalls atomic.Int32
	pikoStub := func(ctx context.Context, endpointID string) (net.Conn, error) {
		pikoCalls.Add(1)
		var d net.Dialer
		return d.DialContext(ctx, "tcp", env.ingress.addr)
	}
	client := env.newExitClient(t, peer, pikoStub)

	conn, trace := punchDialBounded(t, client, 15*time.Second)
	defer conn.Close()

	if trace.Used != "piko" {
		t.Fatalf("候选死亡后载体 Used=%q, 期望 \"piko\"", trace.Used)
	}
	if !errors.Is(trace.UDPErr, punch.ErrPunchTimeout) {
		t.Fatalf("UDPErr=%v, 期望 errors.Is 命中 punch.ErrPunchTimeout(2.5s 预算哨兵)", trace.UDPErr)
	}
	if pikoCalls.Load() != 1 {
		t.Fatalf("piko 拨号次数=%d, 期望恰 1", pikoCalls.Load())
	}
	punchDNSRoundTrip(t, conn)

	// 打洞失败不烧失败记忆:classifyErr 不写 SkipMemo(铁律),后续同信令
	// 链路若恢复(候选有效)仍可直连——本文件不重复验证,10b 包内单测已立档。
}

// ===== (e) 桥的契约行为 =====

// TestPunchIntegrationBridgeContract 信令桥的路由契约逐条锁定:
//   - A 出发的 session 帧带路由信封 target_device_id(Task 13 线协议);
//   - 桥投给 B 的 session 帧已剥信封(B 侧契约=Task 1 锁定帧型,无该键);
//   - B 的 result 原样回程路由到 A(ok/port/fingerprint 齐);
//   - candidates 双向:B→A(桥按 result 端口合成,生产对应 STUN 公网映射)
//     与 A→B(A 的候选帧已投递;loopback 下 A 判型必败、候选表为空,B 侧
//     按契约合法丢弃——路由正确性以「帧到达 B」为准,内容语义在 10a 立档)。
func TestPunchIntegrationBridgeContract(t *testing.T) {
	env := newPunchEnv(t)
	const peer = "dev-pi-b-contract"

	conn, _ := punchDialBounded(t, env.newExitClient(t, peer, nil), 15*time.Second)
	defer conn.Close()
	punchDNSRoundTrip(t, conn) // 全链通是契约断言的前提

	// A→桥:信封在(Task 13:server 靠它寻址 B)。
	out := env.bridge.aSessionFrameAt(0)
	if out["type"] != models.AgentEventPeerPunchSession {
		t.Fatalf("A 出发帧 type=%v, 期望 peer.punch.session", out["type"])
	}
	if out["target_device_id"] != peer {
		t.Fatalf("A 出发帧 target_device_id=%v, 期望 %q", out["target_device_id"], peer)
	}
	// 桥→B:信封已剥(B 收到的帧无该键)。
	toB := env.bridge.deliveredFramesOfType(models.AgentEventPeerPunchSession)
	if len(toB) != 1 {
		t.Fatalf("投给 B 的 session 帧数=%d, 期望 1", len(toB))
	}
	if _, has := toB[0]["target_device_id"]; has {
		t.Fatalf("投给 B 的 session 帧仍带 target_device_id(信封未剥): %v", toB[0])
	}
	for _, key := range []string{"type", "request_id", "session_id", "token", "ttl_seconds"} {
		if _, has := toB[0][key]; !has {
			t.Fatalf("投给 B 的 session 帧缺契约必填键 %q: %v", key, toB[0])
		}
	}
	// B→A:result 原样回程(ok/port/fingerprint 齐——fingerprint 被 A 钉扎
	// 且握手成功,往返已证;这里再锁帧型字段)。
	res := env.bridge.resultAt(0)
	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("result ok=%v, 期望 true", res["ok"])
	}
	if punchPortOf(res) <= 0 {
		t.Fatalf("result port=%v, 期望正数", res["port"])
	}
	if fp, _ := res["fingerprint"].(string); fp == "" {
		t.Fatal("result fingerprint 为空(A 侧钉扎无从谈起)")
	}
	// candidates 双向路由:B→A=桥合成候选(与 result 端口一致)。
	synth := env.bridge.synthCandidates()
	if len(synth) != 1 || len(synth[0]) != 1 || synth[0][0] != fmt.Sprintf("127.0.0.1:%d", punchPortOf(res)) {
		t.Fatalf("桥→A 合成候选=%v, 期望 [%s 的 loopback 候选]", synth, fmt.Sprintf("127.0.0.1:%d", punchPortOf(res)))
	}
	// A→B:A 的 candidates 帧已投递(loopback 下常为空候选表,B 合法丢弃)。
	if got := env.bridge.aCandidatesCount(); got != 1 {
		t.Fatalf("A 发出的 candidates 帧数=%d, 期望 1", got)
	}
	aToB := env.bridge.deliveredFramesOfType(models.AgentEventPeerPunchCandidates)
	if len(aToB) != 1 {
		t.Fatalf("投给 B 的 candidates 帧数=%d, 期望 1(A 的回程候选)", len(aToB))
	}
	if sid, _ := aToB[0]["session_id"].(string); sid == "" {
		t.Fatal("投给 B 的 candidates 帧缺 session_id")
	}
}
