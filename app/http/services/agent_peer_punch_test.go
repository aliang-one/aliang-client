package services

// mesh UDP 打洞批次 Task 10a——B 侧(peer.punch.session→listener 生命周期
// + candidates 接收)单测。信令不过真 WS:handleSession/handleCandidates
// 直调,回包经内存 sink;数据面全真(loopback UDP socket + 真 QUIC listener
// + A 侧真拨号),进程内全链。物理接口绑定/STUN 判型经注入缝替换,单测
// 不碰真外网与真物理口(真绑定链路属 Task 14 集成面)。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/mesh/punch"
	"aliang.one/nursorgate/processor/config"
	"github.com/quic-go/quic-go"
)

// ---------------------------------------------------------------------------
// 测试基建:帧 sink + 可注入 B 侧编排实例
// ---------------------------------------------------------------------------

// punchFrameSink 收集 writeJSON 回包(json round-trip 成 map,数字语义与
// 真线上一致),lanServeTestServer 同款手法。
type punchFrameSink struct {
	mu     sync.Mutex
	frames []map[string]interface{}
}

func (s *punchFrameSink) write(v interface{}) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	m := map[string]interface{}{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	s.mu.Lock()
	s.frames = append(s.frames, m)
	s.mu.Unlock()
	return nil
}

func (s *punchFrameSink) snapshot() []map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]interface{}(nil), s.frames...)
}

func (s *punchFrameSink) lastResult() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.frames) - 1; i >= 0; i-- {
		if s.frames[i]["type"] == models.AgentEventPeerPunchSessionResult {
			return s.frames[i]
		}
	}
	return nil
}

// punchSessionFrame 组 peer.punch.session 帧(线协议 Task 1 契约)。
func punchSessionFrame(requestID, sessionID, token string, ttlSeconds int64) map[string]interface{} {
	return map[string]interface{}{
		"type":        models.AgentEventPeerPunchSession,
		"request_id":  requestID,
		"session_id":  sessionID,
		"token":       token,
		"ttl_seconds": float64(ttlSeconds),
	}
}

// newPunchBForTest 造一个全注入的 B 侧编排实例:bind/classify/serve/gate/
// handler 全部可覆写,默认分类桩宣告一个 TEST-NET 映射(不碰外网)。
// 返回值第三给「测试内 bind 过的 socket 快照」(收摊断言用)。
func newPunchBForTest(t *testing.T, mods ...func(*agentPeerPunchB)) (*agentPeerPunchB, *punchFrameSink, func() []*net.UDPConn) {
	t.Helper()
	sink := &punchFrameSink{}
	var (
		sockMu  sync.Mutex
		sockets []*net.UDPConn
	)
	b := &agentPeerPunchB{
		sessions: map[string]*agentPeerPunchSession{},
		skipMemo: &punch.SkipMemo{},
		bind: func() (*net.UDPConn, string, error) {
			conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				return nil, "", err
			}
			sockMu.Lock()
			sockets = append(sockets, conn)
			sockMu.Unlock()
			return conn, "lo0", nil
		},
		servers: func() []string { return []string{"127.0.0.1:3478"} },
		classify: func(*net.UDPConn, []string) (punch.PublicMapping, bool, error) {
			return punch.PublicMapping{IP: net.IPv4(203, 0, 113, 7), Port: 45000}, false, nil
		},
		serve: punch.ServeListener,
		gate:  func() bool { return true },
		handler: func(string) func(net.Conn) {
			return func(net.Conn) {}
		},
		transferServe: agentPunchTransferServe, // 生产默认(防 purpose=transfer 用例忘注入缝时 nil panic)
	}
	for _, mod := range mods {
		mod(b)
	}
	t.Cleanup(func() { b.closeAll() })
	return b, sink, func() []*net.UDPConn {
		sockMu.Lock()
		defer sockMu.Unlock()
		return append([]*net.UDPConn(nil), sockets...)
	}
}

// punchAEnv 起 A 侧拨号件:loopback socket + transport + 现签客户端证书,
// 返回拨号函数(钉 B 指纹)。Task 6/7 punch 包测试同族,但 services 层
// 自带一份(包内测试 helper 不导出)。
func punchAEnv(t *testing.T) func(addr string, fingerprint string) (*quic.Conn, *quic.Stream) {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("起 A 侧 UDP socket 失败: %v", err)
	}
	tr := &quic.Transport{Conn: conn}
	clientCert, _, certErr := generateAgentLanCertificate()
	if certErr != nil {
		t.Fatalf("现签 A 侧证书失败: %v", certErr)
	}
	t.Cleanup(func() {
		_ = tr.Close()
		_ = conn.Close()
	})
	return func(addr string, fingerprint string) (*quic.Conn, *quic.Stream) {
		t.Helper()
		raddr, err := net.ResolveUDPAddr("udp4", addr)
		if err != nil {
			t.Fatalf("解析 B 地址 %s 失败: %v", addr, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tlsConf := punch.ClientTLSConfig(punch.PunchTLSConfig{
			ClientCert:        clientCert,
			ServerFingerprint: fingerprint,
		})
		qconn, err := tr.Dial(ctx, raddr, tlsConf, nil)
		if err != nil {
			t.Fatalf("A 侧拨号失败: %v", err)
		}
		t.Cleanup(func() { _ = qconn.CloseWithError(0, "") })
		stream, err := qconn.OpenStreamSync(ctx)
		if err != nil {
			t.Fatalf("A 侧开流失败: %v", err)
		}
		if err := stream.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("A 侧设读围栏失败: %v", err)
		}
		return qconn, stream
	}
}

// punchDialEcho 走完整 A 侧链:拨号→开流→写 token 行+载荷→读回显。
func punchDialEcho(t *testing.T, dial func(string, string) (*quic.Conn, *quic.Stream), addr, fingerprint, token, payload, wantEcho string) {
	t.Helper()
	_, stream := dial(addr, fingerprint)
	if _, err := stream.Write([]byte(token + "\n" + payload)); err != nil {
		t.Fatalf("A 侧写 token+载荷失败: %v", err)
	}
	echo := make([]byte, len(wantEcho))
	if _, err := io.ReadFull(stream, echo); err != nil {
		t.Fatalf("A 侧读回显失败: %v", err)
	}
	if string(echo) != wantEcho {
		t.Fatalf("回显不符: 得到 %q 期望 %q", string(echo), wantEcho)
	}
}

// ---------------------------------------------------------------------------
// (a) B 端收 session(门开)→ listener 起 → result → A 侧真拨通+过门+往返
// ---------------------------------------------------------------------------

func TestAgentPeerPunchSessionListenDialRoundTrip(t *testing.T) {
	const token = "punch-token-1234567890"
	echoHandler := func(string) func(net.Conn) {
		return func(c net.Conn) {
			buf := make([]byte, len("hello"))
			if _, err := io.ReadFull(c, buf); err != nil {
				return
			}
			_, _ = c.Write([]byte("world"))
		}
	}
	b, sink, _ := newPunchBForTest(t, func(bb *agentPeerPunchB) { bb.handler = echoHandler })

	b.handleSession(punchSessionFrame("pr-1", "ps-1", token, 15), sink.write, "device-b-self")

	frames := sink.snapshot()
	if len(frames) != 2 {
		t.Fatalf("应回 result+自身 candidates 两帧,得到 %d 帧: %v", len(frames), frames)
	}
	result := frames[0]
	if result["type"] != models.AgentEventPeerPunchSessionResult || result["ok"] != true {
		t.Fatalf("result 帧不符: %v", result)
	}
	if result["request_id"] != "pr-1" {
		t.Fatalf("request_id 回带不符: %v", result["request_id"])
	}
	port := int(result["port"].(float64))
	fingerprint := result["fingerprint"].(string)
	if port <= 0 || len(fingerprint) != 64 {
		t.Fatalf("port/fingerprint 不符: port=%d fingerprint=%q", port, fingerprint)
	}
	own := frames[1]
	if own["type"] != models.AgentEventPeerPunchCandidates || own["session_id"] != "ps-1" {
		t.Fatalf("B 自身候选帧不符: %v", own)
	}
	cands, _ := own["candidates"].([]interface{})
	if len(cands) != 1 || cands[0] != "203.0.113.7:45000" {
		t.Fatalf("B 自身候选应携带 STUN 映射: %v", cands)
	}

	// A 侧(Task 6/7 同族)真拨通+token 过门+数据往返。
	dial := punchAEnv(t)
	punchDialEcho(t, dial, fmt.Sprintf("127.0.0.1:%d", port), fingerprint, token, "hello", "world")
}

// ---------------------------------------------------------------------------
// (b) punch 门关 → ok:false,不开 socket
// ---------------------------------------------------------------------------

func TestAgentPeerPunchSessionGateClosed(t *testing.T) {
	var bindCalls, classifyCalls int
	var mu sync.Mutex
	b, sink, _ := newPunchBForTest(t, func(bb *agentPeerPunchB) {
		bb.gate = func() bool { return false }
		bb.bind = func() (*net.UDPConn, string, error) {
			mu.Lock()
			bindCalls++
			mu.Unlock()
			return nil, "", fmt.Errorf("must not be called")
		}
		bb.classify = func(*net.UDPConn, []string) (punch.PublicMapping, bool, error) {
			mu.Lock()
			classifyCalls++
			mu.Unlock()
			return punch.PublicMapping{}, false, fmt.Errorf("must not be called")
		}
	})

	b.handleSession(punchSessionFrame("pr-gate", "ps-gate", "tok", 15), sink.write, "device-b-self")

	frames := sink.snapshot()
	if len(frames) != 1 {
		t.Fatalf("门关应只回一帧 result,得到 %d: %v", len(frames), frames)
	}
	if frames[0]["ok"] != false || frames[0]["fail_reason"] != "punch_disabled" {
		t.Fatalf("门关 result 不符: %v", frames[0])
	}
	mu.Lock()
	defer mu.Unlock()
	if bindCalls != 0 || classifyCalls != 0 {
		t.Fatalf("门关不得绑 socket/判型: bind=%d classify=%d", bindCalls, classifyCalls)
	}
}

// ---------------------------------------------------------------------------
// (c) enabled-device 门禁:注册表覆盖 + dispatch 拒绝路径
// ---------------------------------------------------------------------------

func TestPeerPunchFramesRequireEnabledDevice(t *testing.T) {
	if !remoteAgentMessageRequiresEnabledDevice(models.AgentEventPeerPunchSession) {
		t.Fatal("peer.punch.session 必须挂 enabled-device 门禁(与 peer.lan.listen 同族)")
	}
	if !remoteAgentMessageRequiresEnabledDevice(models.AgentEventPeerPunchCandidates) {
		t.Fatal("peer.punch.candidates 必须挂 enabled-device 门禁(打洞服务族控制面)")
	}
}

func TestHandleRemoteAgentMessage_PeerPunchDisabledDeviceGate(t *testing.T) {
	peerExecTestEnv(t)
	service := NewAgentService()
	service.mu.Lock()
	service.state.Enabled = false
	service.state.Registered = false
	service.mu.Unlock()

	for _, frame := range []map[string]interface{}{
		punchSessionFrame("pr-dis", "ps-dis", "tok", 15),
		{"type": models.AgentEventPeerPunchCandidates, "session_id": "ps-dis", "candidates": []interface{}{"1.2.3.4:5"}},
	} {
		var written []map[string]interface{}
		writeJSON := func(v interface{}) error {
			if m, ok := v.(map[string]interface{}); ok {
				written = append(written, m)
			}
			return nil
		}
		service.handleRemoteAgentMessage(context.Background(), frame, writeJSON)
		if len(written) != 1 || written[0]["type"] != models.AgentEventError {
			t.Fatalf("禁用设备的 %s 必须被门禁拒为单帧 error,得到 %v", frame["type"], written)
		}
		if msg := fmt.Sprint(written[0]["error"]); !strings.Contains(msg, "disabled") {
			t.Fatalf("error 应点明 disabled: %q", msg)
		}
	}
}

// ---------------------------------------------------------------------------
// (d) candidates 到达 → 探测 Dial 起火(错误被吞)+ listener 仍活着
// ---------------------------------------------------------------------------

func TestAgentPeerPunchCandidatesProbeFiresAndListenerAlive(t *testing.T) {
	// 探测节流调小(包变量先例:punch 包 classifyDiscoverTimeout),测试不真等。
	oldTimeout, oldInterval := agentPunchProbeAttemptTimeout, agentPunchProbeRoundInterval
	agentPunchProbeAttemptTimeout, agentPunchProbeRoundInterval = 60*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() {
		agentPunchProbeAttemptTimeout, agentPunchProbeRoundInterval = oldTimeout, oldInterval
	})

	const token = "punch-token-probe"
	b, sink, _ := newPunchBForTest(t, func(bb *agentPeerPunchB) {
		bb.handler = func(string) func(net.Conn) {
			return func(c net.Conn) {
				buf := make([]byte, len("ping"))
				if _, err := io.ReadFull(c, buf); err != nil {
					return
				}
				_, _ = c.Write([]byte("pong"))
			}
		}
	})
	b.handleSession(punchSessionFrame("pr-probe", "ps-probe", token, 15), sink.write, "device-b-self")
	result := sink.lastResult()
	if result == nil || result["ok"] != true {
		t.Fatalf("session 未立起: %v", result)
	}
	port := int(result["port"].(float64))
	fingerprint := result["fingerprint"].(string)

	// 假 A:普通 UDP socket 数包——B 侧探测 Dial 的 QUIC Initial 落在这里。
	fakeA, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("起假 A socket 失败: %v", err)
	}
	t.Cleanup(func() { _ = fakeA.Close() })
	var gotInitials int
	var fakeMu sync.Mutex
	go func() {
		buf := make([]byte, 1500)
		for {
			n, _, err := fakeA.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n > 0 {
				fakeMu.Lock()
				gotInitials++
				fakeMu.Unlock()
			}
		}
	}()

	// 死端口+畸形候选混入:探测面必须吞掉一切错误不炸不回帧。
	b.handleCandidates(map[string]interface{}{
		"type":       models.AgentEventPeerPunchCandidates,
		"session_id": "ps-probe",
		"candidates": []interface{}{fakeA.LocalAddr().String(), "127.0.0.1:1", "not-an-addr"},
	})

	deadline := time.After(3 * time.Second)
	for {
		fakeMu.Lock()
		seen := gotInitials
		fakeMu.Unlock()
		if seen > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("探测 Dial 未起火(假 A 一个包都没收到)")
		case <-time.After(20 * time.Millisecond):
		}
	}

	// 未知 session 的 candidates:静默丢弃,不影响在册会话。
	b.handleCandidates(map[string]interface{}{
		"type": models.AgentEventPeerPunchCandidates, "session_id": "ps-unknown",
		"candidates": []interface{}{"127.0.0.1:9"},
	})

	// listener 仍活着:探测错误没拖垮接收面,真拨通+往返照旧。
	dial := punchAEnv(t)
	punchDialEcho(t, dial, fmt.Sprintf("127.0.0.1:%d", port), fingerprint, token, "ping", "pong")
	if n := b.sessionCount(); n != 1 {
		t.Fatalf("在册会话应为 1,得到 %d", n)
	}
}

// ---------------------------------------------------------------------------
// (e) 状态清理:TTL 到点收摊 + 同 session_id 顶替式显式收摊
// ---------------------------------------------------------------------------

func TestAgentPeerPunchSessionTTLCleanupAndReplace(t *testing.T) {
	b, sink, sockets := newPunchBForTest(t)

	// TTL=1s:done 关→reaper 清表+关 socket。
	b.handleSession(punchSessionFrame("pr-ttl", "ps-ttl", "tok", 1), sink.write, "device-b-self")
	if result := sink.lastResult(); result == nil || result["ok"] != true {
		t.Fatalf("session 未立起: %v", sink.lastResult())
	}
	deadline := time.After(6 * time.Second)
	for b.sessionCount() != 0 {
		select {
		case <-deadline:
			t.Fatalf("TTL 到点后会话表未清空,残留 %d", b.sessionCount())
		case <-time.After(50 * time.Millisecond):
		}
	}
	first := sockets()[0]
	if _, _, err := first.ReadFromUDP(make([]byte, 16)); err == nil {
		t.Fatal("TTL 收摊后 socket 应已关闭,ReadFromUDP 仍成功")
	}

	// 显式收摊=同 session_id 顶替(线协议 v1 无 close 帧,重试/重发即顶替):
	// 旧 socket 即刻关闭、表内恰一项、新 result 正常回。
	b.handleSession(punchSessionFrame("pr-r1", "ps-re", "tok", 30), sink.write, "device-b-self")
	b.handleSession(punchSessionFrame("pr-r2", "ps-re", "tok", 30), sink.write, "device-b-self")
	old := sockets()[1]
	if _, _, err := old.ReadFromUDP(make([]byte, 16)); err == nil {
		t.Fatal("顶替后旧 session 的 socket 应已关闭")
	}
	if n := b.sessionCount(); n != 1 {
		t.Fatalf("顶替后表内应恰 1 项,得到 %d", n)
	}
	if result := sink.lastResult(); result == nil || result["request_id"] != "pr-r2" || result["ok"] != true {
		t.Fatalf("新 session result 不符: %v", result)
	}
}

// ---------------------------------------------------------------------------
// (f) 顺序硬约束:bind → classify → serve(QUIC listen 最后),transport 单例
// ---------------------------------------------------------------------------

func TestAgentPeerPunchSessionOrderingBindClassifyListen(t *testing.T) {
	var (
		mu      sync.Mutex
		seq     []string
		gotOpts punch.ListenerOpts
	)
	done := make(chan struct{})
	t.Cleanup(func() { close(done) }) // 放 reaper goroutine 出收摊等待
	b, sink, _ := newPunchBForTest(t, func(bb *agentPeerPunchB) {
		bb.bind = func() (*net.UDPConn, string, error) {
			conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			mu.Lock()
			seq = append(seq, "bind")
			mu.Unlock()
			return conn, "lo0", err
		}
		bb.classify = func(*net.UDPConn, []string) (punch.PublicMapping, bool, error) {
			mu.Lock()
			seq = append(seq, "classify")
			mu.Unlock()
			return punch.PublicMapping{IP: net.IPv4(203, 0, 113, 7), Port: 45000}, false, nil
		}
		bb.serve = func(_ *net.UDPConn, opts punch.ListenerOpts) (int, <-chan struct{}, error) {
			mu.Lock()
			seq = append(seq, "serve")
			gotOpts = opts
			mu.Unlock()
			return 45678, done, nil
		}
	})

	b.handleSession(punchSessionFrame("pr-ord", "ps-ord", "tok", 15), sink.write, "device-b-self")

	mu.Lock()
	defer mu.Unlock()
	if len(seq) != 3 || seq[0] != "bind" || seq[1] != "classify" || seq[2] != "serve" {
		t.Fatalf("顺序必须 bind→classify→serve(STUN 探测窗口先于 QUIC listen,倒序会偷包): %v", seq)
	}
	if gotOpts.Transport == nil {
		t.Fatal("serve 必须收到调用方预建 transport(单 Transport 铁律的出让面)")
	}
	sess := b.sessionByID("ps-ord")
	if sess == nil || sess.transport != gotOpts.Transport {
		t.Fatal("注册表里的 transport 必须就是交給 listener 的同一实例(探测 Dial 单例)")
	}
	if result := sink.lastResult(); result == nil || result["ok"] != true || int(result["port"].(float64)) != 45678 {
		t.Fatalf("result 应携带 serve 返回端口: %v", result)
	}
}

// ---------------------------------------------------------------------------
// (g) Classify 记账铁律:STUN 不可达不写 memo;symmetric 写 memo 且照常继续
// ---------------------------------------------------------------------------

func TestAgentPeerPunchClassifyMemoRules(t *testing.T) {
	t.Run("stun_unreachable_no_memo_continue", func(t *testing.T) {
		var serveCalled bool
		b, sink, _ := newPunchBForTest(t, func(bb *agentPeerPunchB) {
			bb.classify = func(*net.UDPConn, []string) (punch.PublicMapping, bool, error) {
				return punch.PublicMapping{}, false, fmt.Errorf("stun classify: 全部 2 台服务器不可达")
			}
			bb.serve = func(_ *net.UDPConn, opts punch.ListenerOpts) (int, <-chan struct{}, error) {
				serveCalled = true
				done := make(chan struct{})
				// 注入桩的 done 永无人关闭:reaper <-done 挂死即 goroutine
				// 泄漏,测试结束前收摊(symmetric 子测试同款先例)。
				t.Cleanup(func() { close(done) })
				return 45678, done, nil
			}
		})
		b.handleSession(punchSessionFrame("pr-stun", "ps-stun", "tok", 15), sink.write, "self-id")

		if b.skipMemo.ShouldSkip("self-id") {
			t.Fatal("STUN 不可达≠symmetric,铁律:不得写 SkipMemo")
		}
		if !serveCalled {
			t.Fatal("STUN 不可达必须照常继续(打洞照试,A 侧兜底)")
		}
		if result := sink.lastResult(); result == nil || result["ok"] != true {
			t.Fatalf("STUN 不可达时 session 仍应立起: %v", result)
		}
		for _, f := range sink.snapshot() {
			if f["type"] == models.AgentEventPeerPunchCandidates {
				t.Fatal("判型失败无映射可发,不应回自身 candidates 帧")
			}
		}
	})

	t.Run("symmetric_suspect_memo_but_continue", func(t *testing.T) {
		var serveCalled bool
		done := make(chan struct{})
		t.Cleanup(func() { close(done) })
		b, sink, _ := newPunchBForTest(t, func(bb *agentPeerPunchB) {
			bb.classify = func(*net.UDPConn, []string) (punch.PublicMapping, bool, error) {
				return punch.PublicMapping{IP: net.IPv4(203, 0, 113, 7), Port: 45000}, true, nil
			}
			bb.serve = func(_ *net.UDPConn, opts punch.ListenerOpts) (int, <-chan struct{}, error) {
				serveCalled = true
				return 45678, done, nil
			}
		})
		b.handleSession(punchSessionFrame("pr-sym", "ps-sym", "tok", 15), sink.write, "self-id")

		if !b.skipMemo.ShouldSkip("self-id") {
			t.Fatal("判 symmetricSuspect 必须写 SkipMemo(编排层记账职责在 B 侧)")
		}
		if !serveCalled {
			t.Fatal("B 侧对称 NAT 仍可能被 A 打通,必须照常继续(v1 不做 EIF 过滤判型)")
		}
		if result := sink.lastResult(); result == nil || result["ok"] != true {
			t.Fatalf("symmetricSuspect 下 session 仍应立起: %v", result)
		}
		last := sink.lastResult()
		_ = last
		found := false
		for _, f := range sink.snapshot() {
			if f["type"] == models.AgentEventPeerPunchCandidates {
				found = true
			}
		}
		if !found {
			t.Fatal("symmetricSuspect 仍有映射,candidates 帧照发(A 侧照试)")
		}
	})
}

// ---------------------------------------------------------------------------
// 帧面校验:request_id 缺失静默丢;session_id/token 越界拒
// ---------------------------------------------------------------------------

func TestAgentPeerPunchSessionFrameValidation(t *testing.T) {
	b, sink, _ := newPunchBForTest(t)

	// 缺 request_id:静默丢帧(照 peer.lan.listen 先例),不回任何帧。
	b.handleSession(punchSessionFrame("", "ps-x", "tok", 15), sink.write, "self")
	if frames := sink.snapshot(); len(frames) != 0 {
		t.Fatalf("缺 request_id 应静默丢帧,却回了: %v", frames)
	}

	b.handleSession(punchSessionFrame("pr-v1", "", "tok", 15), sink.write, "self")
	if f := sink.lastResult(); f == nil || f["fail_reason"] != "bad_session_id" {
		t.Fatalf("空 session_id 应拒: %v", f)
	}

	b.handleSession(punchSessionFrame("pr-v2", "ps-x", strings.Repeat("x", 200), 15), sink.write, "self")
	if f := sink.lastResult(); f == nil || f["fail_reason"] != "bad_token" {
		t.Fatalf("超长 token 应拒: %v", f)
	}
}

// ---------------------------------------------------------------------------
// A 侧(Task 10b):cache-first 拨号源。假 B=真 ServeListener+信令回程直连
// (handleResultFrame/handleCandidatesFrame),数据面全真。
// ---------------------------------------------------------------------------

// punchFakeB 全功能假 B:onFrame 收 A 侧自发帧——session 帧→起 loopback 真
// listener(计数 session 帧数供缓存命中断言)→回 result+candidates;
// candidates 帧(A 侧自发)忽略;listener 收摊由 onFrame 内起的 reaper
// goroutine 自理。
type punchFakeB struct {
	t             *testing.T
	a             *agentPeerPunchA
	mu            sync.Mutex
	sessionFrames int
}

func (f *punchFakeB) onFrame(payload map[string]interface{}) error {
	switch payload["type"] {
	case models.AgentEventPeerPunchSession:
		f.mu.Lock()
		f.sessionFrames++
		f.mu.Unlock()
		token, _ := payload["token"].(string)
		sessionID, _ := payload["session_id"].(string)
		requestID, _ := payload["request_id"].(string)
		cert, fp, err := generateAgentLanCertificate()
		if err != nil {
			f.t.Errorf("假 B 现签证书失败: %v", err)
			return nil
		}
		sock, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			f.t.Errorf("假 B 绑定失败: %v", err)
			return nil
		}
		echo := func(conn net.Conn) {
			_, _ = io.Copy(conn, conn)
		}
		port, done, err := punch.ServeListener(sock, punch.ListenerOpts{
			TTL:     30 * time.Second,
			Token:   token,
			TLS:     punch.PunchTLSConfig{ServerCert: cert, ExpectedToken: token},
			Handler: echo,
		})
		if err != nil {
			f.t.Errorf("假 B 起听失败: %v", err)
			return nil
		}
		f.a.handleResultFrame(map[string]interface{}{
			"type":        models.AgentEventPeerPunchSessionResult,
			"request_id":  requestID,
			"ok":          true,
			"port":        float64(port),
			"fingerprint": fp,
		})
		f.a.handleCandidatesFrame(map[string]interface{}{
			"type":       models.AgentEventPeerPunchCandidates,
			"session_id": sessionID,
			"candidates": []interface{}{fmt.Sprintf("127.0.0.1:%d", port)},
		})
		safeGo("punch_fake_b_reaper", func() {
			<-done
			_ = sock.Close()
		})
	}
	return nil
}

func (f *punchFakeB) countSessionFrames() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessionFrames
}

// newPunchAForTest 造全注入 A 侧编排实例:send 缝路由给假 B,bind/classify
// 桩不碰外网(loopback 绑定+TEST-NET 宣告),cert/dialPeer 走生产件。
func newPunchAForTest(t *testing.T, mods ...func(*agentPeerPunchA)) (*agentPeerPunchA, *punchFakeB) {
	t.Helper()
	fake := &punchFakeB{t: t}
	a := &agentPeerPunchA{
		pendingResult:     map[string]chan map[string]interface{}{},
		pendingCandidates: map[string]chan []string{},
		candidateStash:    map[string][]string{},
		sockets:           map[string]*net.UDPConn{},
		skipMemo:          &punch.SkipMemo{},
		cache:             punch.NewSessionCache(),
		gate:              func() bool { return true },
		bind: func() (*net.UDPConn, string, error) {
			conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			return conn, "lo0", err
		},
		servers: func() []string { return []string{"127.0.0.1:3478"} },
		classify: func(*net.UDPConn, []string) (punch.PublicMapping, bool, error) {
			return punch.PublicMapping{IP: net.IPv4(203, 0, 113, 9), Port: 45001}, false, nil
		},
		cert: generateAgentLanCertificate,
		send: func(payload map[string]interface{}) error {
			return fake.onFrame(payload)
		},
	}
	for _, mod := range mods {
		mod(a)
	}
	fake.a = a // 回程通道:假 B 的 result/candidates 帧喂给本实例 pending 表
	t.Cleanup(a.closeAll)
	return a, fake
}

func punchAWaitPayload(requestID string) map[string]interface{} {
	return map[string]interface{}{"type": models.AgentEventPeerPunchSessionResult, "request_id": requestID, "ok": false}
}

func TestAgentPeerPunchADialSourceFullChainAndCacheHit(t *testing.T) {
	a, fake := newPunchAForTest(t)

	conn1, err := a.dialSource(context.Background(), "dev-b")
	if err != nil {
		t.Fatalf("首拨失败: %v", err)
	}
	if _, err := conn1.Write([]byte("hello")); err != nil {
		t.Fatalf("首拨写失败: %v", err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn1, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("首拨回显错: %q %v", buf, err)
	}
	_ = conn1.Close()

	if n := fake.countSessionFrames(); n != 1 {
		t.Fatalf("首拨应恰发一帧 session,实发 %d", n)
	}

	// 第二拨走缓存:不开新 session(对端握手计数不增=不开新 listener),
	// 数据面照通(同会话第二条流,免门直通)。
	conn2, err := a.dialSource(context.Background(), "dev-b")
	if err != nil {
		t.Fatalf("二拨(缓存)失败: %v", err)
	}
	if _, err := conn2.Write([]byte("two")); err != nil {
		t.Fatalf("二拨写失败: %v", err)
	}
	buf2 := make([]byte, 3)
	if _, err := io.ReadFull(conn2, buf2); err != nil || string(buf2) != "two" {
		t.Fatalf("二拨回显错: %q %v", buf2, err)
	}
	_ = conn2.Close()

	if n := fake.countSessionFrames(); n != 1 {
		t.Fatalf("二拨应走缓存(session 帧仍为 1),实发 %d", n)
	}
}

func TestAgentPeerPunchAClassifyMemoRules(t *testing.T) {
	t.Run("classify_error_no_record", func(t *testing.T) {
		a, _ := newPunchAForTest(t, func(a *agentPeerPunchA) {
			a.classify = func(*net.UDPConn, []string) (punch.PublicMapping, bool, error) {
				return punch.PublicMapping{}, false, context.DeadlineExceeded
			}
		})
		if _, err := a.dialSource(context.Background(), "dev-b"); err != nil {
			t.Fatalf("classify error 应不记账且继续打洞: %v", err)
		}
		if a.skipMemo.ShouldSkip("dev-b") {
			t.Fatal("classify error 不得写 SkipMemo(不可达≠symmetric)")
		}
	})
	t.Run("symmetric_record_and_skip", func(t *testing.T) {
		a, fake := newPunchAForTest(t, func(a *agentPeerPunchA) {
			a.classify = func(*net.UDPConn, []string) (punch.PublicMapping, bool, error) {
				return punch.PublicMapping{}, true, nil
			}
		})
		if _, err := a.dialSource(context.Background(), "dev-b"); err == nil {
			t.Fatal("symmetric 应返回错误(落 piko)")
		}
		if !a.skipMemo.ShouldSkip("dev-b") {
			t.Fatal("symmetric 必须写 SkipMemo(per-peer 记账)")
		}
		if n := fake.countSessionFrames(); n != 1 {
			t.Fatalf("symmetric 只应烧一次 session 帧,实发 %d", n)
		}
		// memo 生效:第二次拨号不入打洞(session 帧不增)
		if _, err := a.dialSource(context.Background(), "dev-b"); err == nil {
			t.Fatal("memo 命中应继续拒绝")
		}
		if n := fake.countSessionFrames(); n != 1 {
			t.Fatalf("memo 命中不应再发 session 帧,实发 %d", n)
		}
	})
}

func TestAgentPeerPunchAResultPendingAndTimeout(t *testing.T) {
	t.Run("unknown_result_no_panic", func(t *testing.T) {
		a, _ := newPunchAForTest(t)
		a.handleResultFrame(punchAWaitPayload("no-such-request"))
	})
	t.Run("result_timeout", func(t *testing.T) {
		restored := agentPunchResultWait
		agentPunchResultWait = 150 * time.Millisecond
		t.Cleanup(func() { agentPunchResultWait = restored })
		a, _ := newPunchAForTest(t, func(a *agentPeerPunchA) {
			a.send = func(map[string]interface{}) error { return nil } // 假 B 吞帧不回
		})
		if _, err := a.dialSource(context.Background(), "dev-b"); err == nil {
			t.Fatal("result 超时应失败(落 piko)")
		}
	})
}

func TestAgentPeerPunchADialSourceSerializes(t *testing.T) {
	a, fake := newPunchAForTest(t)
	// 首帧 session 发出即放行信号:并发第二拨与首拨竞争 dialMu,后至者
	// 等锁后走双重检查快路径——session 帧总数=1 为串行+缓存的机械锁。
	var once sync.Once
	fakeSlowReply := make(chan struct{})
	orig := a.send
	a.send = func(payload map[string]interface{}) error {
		if payload["type"] == models.AgentEventPeerPunchSession {
			once.Do(func() { close(fakeSlowReply) })
		}
		return orig(payload)
	}
	errCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			conn, err := a.dialSource(context.Background(), "dev-b")
			if err == nil {
				_ = conn.Close()
			}
			errCh <- err
		}()
	}
	<-fakeSlowReply // 首帧已出
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("并发拨号第 %d 路失败: %v", i+1, err)
		}
	}
	if n := fake.countSessionFrames(); n != 1 {
		t.Fatalf("串行+缓存应只发一帧 session,实发 %d", n)
	}
}

func TestAgentPeerPunchADialTimeoutNoLeak(t *testing.T) {
	a, _ := newPunchAForTest(t, func(a *agentPeerPunchA) {
		// 假 B 给出黑洞候选:拨号烧预算后失败,socket 必须归还(不泄漏)。
		a.send = func(payload map[string]interface{}) error {
			if payload["type"] == models.AgentEventPeerPunchCandidates {
				payload["candidates"] = []interface{}{"127.0.0.1:1"}
			}
			// session 帧自答 result+候选(黑洞候选让拨号在预算内烧完)。
			if payload["type"] == models.AgentEventPeerPunchSession {
				go func() {
					a.handleResultFrame(map[string]interface{}{
						"type":        models.AgentEventPeerPunchSessionResult,
						"request_id":  payload["request_id"],
						"ok":          true,
						"port":        float64(1),
						"fingerprint": "0000000000000000000000000000000000000000000000000000000000000000",
					})
					a.handleCandidatesFrame(map[string]interface{}{
						"type":       models.AgentEventPeerPunchCandidates,
						"session_id": payload["session_id"],
						"candidates": []interface{}{"127.0.0.1:1"},
					})
				}()
			}
			return nil
		}
	})
	if _, err := a.dialSource(context.Background(), "dev-b"); err == nil {
		t.Fatal("黑洞候选应失败")
	}
	a.mu.Lock()
	n := len(a.sockets)
	a.mu.Unlock()
	if n != 0 {
		t.Fatalf("失败路径应归还 socket,残留 %d", n)
	}
}

func TestAgentPeerPunchAGateOff(t *testing.T) {
	a, fake := newPunchAForTest(t, func(a *agentPeerPunchA) {
		a.gate = func() bool { return false }
	})
	if _, err := a.dialSource(context.Background(), "dev-b"); err == nil {
		t.Fatal("门关应直接错误")
	}
	if n := fake.countSessionFrames(); n != 0 {
		t.Fatalf("门关不应发任何帧,实发 %d", n)
	}
}

// agentPunchConfiguredServers 读配置取 STUN 列表:配置空回落默认档,配置了
// 原样返回(transfer-punch-stun 批次)。全局配置摆置沿
// agent_ai_agentcore_skills_test.go 同款:SetGlobalConfig + t.Cleanup 回滚
// (仓内无 swapGlobalMeshConfig 这类现成缝)。
func TestAgentPunchConfiguredServers(t *testing.T) {
	withCfg := func(t *testing.T, mesh *config.MeshConfig) {
		old := config.GetGlobalConfig()
		config.SetGlobalConfig(&config.Config{Customer: &config.CustomerConfig{Mesh: mesh}})
		t.Cleanup(func() { config.SetGlobalConfig(old) })
	}
	withCfg(t, &config.MeshConfig{}) // 空 mesh → 回落默认档
	got := agentPunchConfiguredServers()
	if len(got) != 2 || got[0] != "stun.miwifi.com:3478" {
		t.Fatalf("空配置应回落默认档, got %v", got)
	}
	// 副档字面锁(评审 P1):Google 副档拍板为主域 stun.l.google.com,勿手滑
	// 改回 stun1 次域——默认档形态是实测定稿,测试钉死。
	if got[1] != "stun.l.google.com:19302" {
		t.Fatalf("默认档副档必须为 stun.l.google.com:19302(勿改回 stun1 次域), got %v", got)
	}
	withCfg(t, &config.MeshConfig{StunServers: []string{"stun.custom:3478"}})
	if got := agentPunchConfiguredServers(); len(got) != 1 || got[0] != "stun.custom:3478" {
		t.Fatalf("配置了应原样返回, got %v", got)
	}
	// nil 全局配置(评审 P2):启动早期/配置未装配时不 panic,回落默认档。
	// SetGlobalConfig(nil) 先例见 processor/tcp/route_override_test.go。
	oldNil := config.GetGlobalConfig()
	config.SetGlobalConfig(nil)
	t.Cleanup(func() { config.SetGlobalConfig(oldNil) })
	got = agentPunchConfiguredServers()
	if len(got) != 2 || got[0] != "stun.miwifi.com:3478" {
		t.Fatalf("nil 全局配置应回落默认档, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// 传输腿复合键(transfer-punch-stun Task 5):同 peer 的 mesh 会话与传输
// 会话在缓存/sock 表互不可见——mesh 会话喂传输流=喂错 handler,反之亦然
// (计划期裁决 4)。
// ---------------------------------------------------------------------------

// TestAgentPunchTransferCacheKeyIsolation 锁键形态契约:mesh 键=裸 peerID
// (存量形态不变),transfer 键带前缀;purpose 缺省显式化为 mesh(帧面语义:
// 老版本 A 侧发的无 purpose 帧 = mesh)。
func TestAgentPunchTransferCacheKeyIsolation(t *testing.T) {
	key := punchCacheKey("dev-1", "transfer")
	if key == "dev-1" {
		t.Fatal("transfer 键必须带前缀,不得与 mesh 键同形")
	}
	if punchCacheKey("dev-1", "") != "dev-1" {
		t.Fatal("mesh 键形态不变(裸 peerID)")
	}
	if purposeOrMesh("") != "mesh" || purposeOrMesh("transfer") != "transfer" {
		t.Fatal("purposeOrMesh 缺省须显式化为 mesh")
	}
}

// TestAgentPunchTransferDialFullChainAndIsolation 全链锁复合键与一次性拨号
// 的真实隔离行为(键形态之外的机械面):同 peer 先 mesh 拨通,再 transfer
// 拨——缓存不得命中 mesh 会话(session 帧再起一帧),session 帧须捎带
// purpose=transfer 与 transfer_id;transfer 拨是一次性拨号(质量评审
// 裁决 5):不入缓存、二拨必发新 session 帧、echo 照常(每传输独立信令,
// B 侧接收上下文单次消费);mesh 会话不被 transfer 拨顶替(缓存/顶替类
// 用例零回归的传输侧对偶)。
func TestAgentPunchTransferDialFullChainAndIsolation(t *testing.T) {
	a, fake := newPunchAForTest(t)
	orig := a.send
	var transferFrame map[string]interface{}
	a.send = func(payload map[string]interface{}) error {
		if payload["type"] == models.AgentEventPeerPunchSession && payload["purpose"] == "transfer" {
			transferFrame = payload
		}
		return orig(payload)
	}

	// mesh 首拨(裸 peerID 键)。
	connMesh, err := a.dialSource(context.Background(), "dev-b")
	if err != nil {
		t.Fatalf("mesh 首拨失败: %v", err)
	}
	_ = connMesh.Close()
	if n := fake.countSessionFrames(); n != 1 {
		t.Fatalf("mesh 首拨应恰发一帧 session,实发 %d", n)
	}

	// transfer 拨:同 peer 不同用途——缓存/sock 表互不可见,必须重开打洞。
	connTr, err := a.dialSourceOpts(context.Background(), "dev-b", punchDialOpts{
		budget:     agentTransferPunchBudget,
		purpose:    "transfer",
		transferID: "tr-1",
	})
	if err != nil {
		t.Fatalf("transfer 拨失败: %v", err)
	}
	_ = connTr.Close()
	if n := fake.countSessionFrames(); n != 2 {
		t.Fatalf("transfer 拨不得复用 mesh 会话(session 帧应为 2),实发 %d", n)
	}
	if transferFrame == nil {
		t.Fatal("transfer 拨的 session 帧未捕获")
	}
	if transferFrame["purpose"] != "transfer" || transferFrame["transfer_id"] != "tr-1" {
		t.Fatalf("transfer session 帧缺 purpose/transfer_id: %v", transferFrame)
	}
	// 一次性拨号不入缓存(裁决 5 机械面):transfer 键 Acquire 必空。
	if lease := a.cache.Acquire(punchCacheKey("dev-b", "transfer")); lease != nil {
		lease.Release()
		t.Fatal("传输腿一次性拨号不得入缓存(transfer 键 Acquire 应为 nil)")
	}

	// transfer 二拨:一次性拨号(裁决 5)——不得复用 t-1 会话,必发新
	// session 帧(frames 2→3);新会话新门禁,数据面(echo)照常工作。
	connTr2, err := a.dialSourceOpts(context.Background(), "dev-b", punchDialOpts{
		purpose:    "transfer",
		transferID: "tr-2",
	})
	if err != nil {
		t.Fatalf("transfer 二拨(一次性)失败: %v", err)
	}
	if _, err := connTr2.Write([]byte("tr")); err != nil {
		t.Fatalf("transfer 二拨写失败: %v", err)
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(connTr2, buf); err != nil || string(buf) != "tr" {
		t.Fatalf("transfer 二拨回显错: %q %v", buf, err)
	}
	_ = connTr2.Close()
	if n := fake.countSessionFrames(); n != 3 {
		t.Fatalf("transfer 二拨必发新 session 帧(应为 3),实发 %d", n)
	}

	// mesh 二拨:mesh 键缓存未被 transfer 拨顶替(session 帧仍为 3)。
	connMesh2, err := a.dialSource(context.Background(), "dev-b")
	if err != nil {
		t.Fatalf("mesh 二拨(缓存)失败: %v", err)
	}
	_ = connMesh2.Close()
	if n := fake.countSessionFrames(); n != 3 {
		t.Fatalf("transfer 拨不得顶替 mesh 会话(session 帧应仍为 3),实发 %d", n)
	}
}

// ---------------------------------------------------------------------------
// purpose 路由(transfer-punch-stun Task 7):session 帧的 purpose 决定过门流
// 的去向——缺省/mesh=b.handler 缝(ssh 入口,既有语义零变化),transfer=
// 接收上下文注册表(agent_peer_lan.go)换 transferServe 缝喂 lan_push 协议体。
// ---------------------------------------------------------------------------

// punchSessionFrameWithPurpose 组带 purpose/transfer_id 的 session 帧
// (Task 5 契约:新 A 侧恒发显式 purpose;老帧缺省=mesh)。
func punchSessionFrameWithPurpose(requestID, sessionID, token string, ttlSeconds int64, purpose, transferID string) map[string]interface{} {
	msg := punchSessionFrame(requestID, sessionID, token, ttlSeconds)
	if purpose != "" {
		msg["purpose"] = purpose
	}
	if transferID != "" {
		msg["transfer_id"] = transferID
	}
	return msg
}

// TestAgentPunchPurposeTransferRoutesToTransferServe purpose=transfer 的会话:
// 注册表里的过门流必须进 transferServe 缝(带上正确 transfer_id 与上下文),
// 绝不落 b.handler(ssh 入口——用途错配=数据喂错 handler)。
func TestAgentPunchPurposeTransferRoutesToTransferServe(t *testing.T) {
	const token = "punch-token-xfer01"
	const transferID = "tr_punchxfer01"
	var (
		mu            sync.Mutex
		transferCalls int
		gotTransferID string
		gotToken      string
		handlerCalls  int
	)
	b, sink, _ := newPunchBForTest(t, func(bb *agentPeerPunchB) {
		bb.handler = func(string) func(net.Conn) {
			return func(net.Conn) {
				mu.Lock()
				handlerCalls++
				mu.Unlock()
			}
		}
		bb.transferServe = func(tid string, tctx agentPunchTransferContext) func(net.Conn) {
			mu.Lock()
			transferCalls++
			gotTransferID = tid
			gotToken = tctx.token
			mu.Unlock()
			return func(c net.Conn) {
				buf := make([]byte, len("data"))
				if _, err := io.ReadFull(c, buf); err != nil {
					return
				}
				_, _ = c.Write([]byte("served"))
			}
		}
	})
	registerAgentPunchTransferContext(transferID, agentPunchTransferContext{
		token:     token,
		filename:  "x.bin",
		targetDir: "",
		maxBytes:  4096,
		writeJSON: func(interface{}) error { return nil },
	}, 30*time.Second)

	b.handleSession(punchSessionFrameWithPurpose("pr-xfer", "ps-xfer", token, 15, "transfer", transferID), sink.write, "device-b-self")
	result := sink.lastResult()
	if result == nil || result["ok"] != true {
		t.Fatalf("transfer 会话应正常立起: %v", result)
	}
	port := int(result["port"].(float64))
	fingerprint := result["fingerprint"].(string)

	dial := punchAEnv(t)
	punchDialEcho(t, dial, fmt.Sprintf("127.0.0.1:%d", port), fingerprint, token, "data", "served")

	mu.Lock()
	defer mu.Unlock()
	if transferCalls != 1 || gotTransferID != transferID || gotToken != token {
		t.Fatalf("过门流必须进 transferServe 缝(transfer_id/token 正确): calls=%d id=%s token=%s", transferCalls, gotTransferID, gotToken)
	}
	if handlerCalls != 0 {
		t.Fatalf("purpose=transfer 不得落 b.handler(ssh 入口),被触 %d 次", handlerCalls)
	}
}

// TestAgentPunchPurposeDefaultAndMeshRouteToIngressHandler 缺省与显式 mesh
// 都走 b.handler 缝(既有语义零变化);transferServe 缝不得被触。
func TestAgentPunchPurposeDefaultAndMeshRouteToIngressHandler(t *testing.T) {
	const token = "punch-token-mesh01"
	var (
		mu            sync.Mutex
		handlerCalls  int
		transferCalls int
	)
	b, sink, _ := newPunchBForTest(t, func(bb *agentPeerPunchB) {
		bb.handler = func(string) func(net.Conn) {
			return func(c net.Conn) {
				mu.Lock()
				handlerCalls++
				mu.Unlock()
				buf := make([]byte, len("hi"))
				if _, err := io.ReadFull(c, buf); err != nil {
					return
				}
				_, _ = c.Write([]byte("ok"))
			}
		}
		bb.transferServe = func(string, agentPunchTransferContext) func(net.Conn) {
			mu.Lock()
			transferCalls++
			mu.Unlock()
			return func(net.Conn) {}
		}
	})

	dial := punchAEnv(t)
	for i, tc := range []struct{ name, purpose string }{
		{"absent", ""},
		{"explicit_mesh", "mesh"},
	} {
		sessionID := fmt.Sprintf("ps-mesh-%d", i)
		b.handleSession(punchSessionFrameWithPurpose("pr-mesh-"+tc.name, sessionID, token, 15, tc.purpose, ""), sink.write, "device-b-self")
		result := sink.lastResult()
		if result == nil || result["ok"] != true {
			t.Fatalf("[%s] 会话应正常立起: %v", tc.name, result)
		}
		port := int(result["port"].(float64))
		fingerprint := result["fingerprint"].(string)
		punchDialEcho(t, dial, fmt.Sprintf("127.0.0.1:%d", port), fingerprint, token, "hi", "ok")
	}

	mu.Lock()
	defer mu.Unlock()
	if handlerCalls != 2 {
		t.Fatalf("缺省与显式 mesh 各一条流都应进 b.handler,实进 %d", handlerCalls)
	}
	if transferCalls != 0 {
		t.Fatalf("非 transfer 会话不得触 transferServe 缝,被触 %d 次", transferCalls)
	}
}

// TestAgentPunchPurposeTransferWithoutContextFails 声明传输用途却无接收
// 上下文(listen 未发生/已被消费/TTL 摘除):fail-loud 回
// transfer_context_missing,绝不静默喂 ssh 入口;serve 不得被触。
func TestAgentPunchPurposeTransferWithoutContextFails(t *testing.T) {
	var serveCalled bool
	b, sink, _ := newPunchBForTest(t, func(bb *agentPeerPunchB) {
		bb.serve = func(*net.UDPConn, punch.ListenerOpts) (int, <-chan struct{}, error) {
			serveCalled = true
			return 0, nil, fmt.Errorf("must not be called")
		}
	})
	b.handleSession(punchSessionFrameWithPurpose("pr-ctxmiss", "ps-ctxmiss", "tok", 15, "transfer", "tr_punchmiss01"), sink.write, "self")
	result := sink.lastResult()
	if result == nil || result["ok"] != false || result["fail_reason"] != "transfer_context_missing" {
		t.Fatalf("无上下文的 transfer 会话必须 fail transfer_context_missing: %v", result)
	}
	if serveCalled {
		t.Fatal("上下文缺失须在起听前早退,serve 不得被触")
	}
}

// TestAgentPunchPurposeUnknownRejected 未知 purpose 拒帧(缺省=mesh 与显式
// transfer 之外的用途一律 fail bad_purpose)。
func TestAgentPunchPurposeUnknownRejected(t *testing.T) {
	var serveCalled bool
	b, sink, _ := newPunchBForTest(t, func(bb *agentPeerPunchB) {
		bb.serve = func(*net.UDPConn, punch.ListenerOpts) (int, <-chan struct{}, error) {
			serveCalled = true
			return 0, nil, fmt.Errorf("must not be called")
		}
	})
	b.handleSession(punchSessionFrameWithPurpose("pr-bp", "ps-bp", "tok", 15, "telemetry", ""), sink.write, "self")
	if result := sink.lastResult(); result == nil || result["ok"] != false || result["fail_reason"] != "bad_purpose" {
		t.Fatalf("未知 purpose 应拒帧 bad_purpose: %v", result)
	}
	if serveCalled {
		t.Fatal("未知 purpose 须早退,serve 不得被触")
	}
}

// TestAgentPunchPurposeTransferBadTransferIDFails purpose=transfer 但
// transfer_id 非法(路径穿越形态):fail bad_transfer_id,须在取上下文之前
// 早退(不消费表项),serve 不得被触。
func TestAgentPunchPurposeTransferBadTransferIDFails(t *testing.T) {
	var serveCalled bool
	b, sink, _ := newPunchBForTest(t, func(bb *agentPeerPunchB) {
		bb.serve = func(*net.UDPConn, punch.ListenerOpts) (int, <-chan struct{}, error) {
			serveCalled = true
			return 0, nil, fmt.Errorf("must not be called")
		}
	})
	b.handleSession(punchSessionFrameWithPurpose("pr-btid", "ps-btid", "tok", 15, "transfer", "../evil"), sink.write, "self")
	if result := sink.lastResult(); result == nil || result["ok"] != false || result["fail_reason"] != "bad_transfer_id" {
		t.Fatalf("非法 transfer_id 应拒帧 bad_transfer_id: %v", result)
	}
	if serveCalled {
		t.Fatal("非法 transfer_id 须早退,serve 不得被触")
	}
}

// TestAgentPunchTransferServeFailurePutsContextBack serve 失败放回:上下文
// 在 serve 之前取出(Handler 是 serve 入参,先于 serve 求值),serve 失败须
// 在 listen_failed 分支放回表项——单次消费语义归成功路径独占。
func TestAgentPunchTransferServeFailurePutsContextBack(t *testing.T) {
	const transferID = "tr_punchback01"
	var transferCalls int
	b, sink, _ := newPunchBForTest(t, func(bb *agentPeerPunchB) {
		bb.serve = func(*net.UDPConn, punch.ListenerOpts) (int, <-chan struct{}, error) {
			return 0, nil, fmt.Errorf("serve boom")
		}
		bb.transferServe = func(string, agentPunchTransferContext) func(net.Conn) {
			transferCalls++
			return func(net.Conn) {}
		}
	})
	registerAgentPunchTransferContext(transferID, agentPunchTransferContext{
		token:    "tok-back",
		filename: "back.bin",
	}, 30*time.Second)

	b.handleSession(punchSessionFrameWithPurpose("pr-back", "ps-back", "tok", 15, "transfer", transferID), sink.write, "self")
	result := sink.lastResult()
	if result == nil || result["ok"] != false || result["fail_reason"] != "listen_failed" {
		t.Fatalf("serve 失败应回 listen_failed: %v", result)
	}
	// 放回断言:serve 失败后注册表必须仍能取到同一上下文。
	tctx, ok := takeAgentPunchTransferContext(transferID)
	if !ok || tctx.token != "tok-back" || tctx.filename != "back.bin" {
		t.Fatalf("serve 失败必须放回表项(单次消费归成功路径): ok=%t ctx=%+v", ok, tctx)
	}
	// 再取必空:放回的是同一个单次消费表项,不是复制。
	if _, ok := takeAgentPunchTransferContext(transferID); ok {
		t.Fatal("放回后的表项仍是单次消费,二次 take 必须为空")
	}
	if transferCalls != 1 {
		t.Fatalf("transferServe 缝应被求值恰一次(Handler 是 serve 入参): %d", transferCalls)
	}
}

// ---------------------------------------------------------------------------
// 公网直挂判据 + result 声明(punch-public-peer Task 2):isPublicMapping
// 纯函数(snapshot 注入,机器无关)/ firstNonLoopbackIPv4 兜底地址源 /
// handleSession 的 publicPeer 两点计算(自动判据 ∨ public_listen 配置)与
// STUN-独立逃生门候选。
// ---------------------------------------------------------------------------

// TestIsPublicMapping 纯函数三分支:STUN 映射 IP 命中任一非回环接口 IPv4 →
// 公网直挂;不命中(典型 NAT/DNAT)→ 非公网;空映射 → 非公网。
func TestIsPublicMapping(t *testing.T) {
	snap := agentNetSnapshot{Interfaces: []agentNetInterface{
		{IPv4: "203.0.113.7"},
		{IPv4: "192.168.1.10"},
	}}
	if !isPublicMapping(punch.PublicMapping{IP: net.ParseIP("203.0.113.7"), Port: 3478}, snap) {
		t.Fatal("映射=接口公网地址应判 public")
	}
	if isPublicMapping(punch.PublicMapping{IP: net.ParseIP("198.51.100.9"), Port: 3478}, snap) {
		t.Fatal("映射≠任何接口地址(典型 NAT)不应判 public")
	}
	if isPublicMapping(punch.PublicMapping{IP: nil}, snap) {
		t.Fatal("空映射不应判 public")
	}
}

// TestFirstNonLoopbackIPv4 兜底地址源:有非回环 IPv4 → 返回之(跳过前置
// 回环项);全回环/空快照 → 空串(调用方留空候选,退化落中继)。
func TestFirstNonLoopbackIPv4(t *testing.T) {
	loopbackFirst := agentNetSnapshot{Interfaces: []agentNetInterface{
		{IPv4: "127.0.0.1"},
		{IPv4: "192.0.2.10"},
	}}
	if got := firstNonLoopbackIPv4(loopbackFirst); got != "192.0.2.10" {
		t.Fatalf("应跳过回环返回首个非回环 IPv4: 得到 %q", got)
	}
	allLoopback := agentNetSnapshot{Interfaces: []agentNetInterface{{IPv4: "127.0.0.1"}}}
	if got := firstNonLoopbackIPv4(allLoopback); got != "" {
		t.Fatalf("全回环应返回空串: 得到 %q", got)
	}
	if got := firstNonLoopbackIPv4(agentNetSnapshot{}); got != "" {
		t.Fatalf("空快照应返回空串: 得到 %q", got)
	}
}

// punchPublicListenCfg 摆全局 customer.mesh 配置(t.Cleanup 回滚),
// TestAgentPunchConfiguredServers 同款手法。
func punchPublicListenCfg(t *testing.T, mesh *config.MeshConfig) {
	t.Helper()
	old := config.GetGlobalConfig()
	config.SetGlobalConfig(&config.Config{Customer: &config.CustomerConfig{Mesh: mesh}})
	t.Cleanup(func() { config.SetGlobalConfig(old) })
}

// TestHandleSessionPublicListenFallbackCandidate STUN-独立逃生门(评审
// Issue-1):public_listen=true 且 classify 失败(STUN 全挂/云 EIP 接口私网)
// → 自候选回落接口快照(首个非回环 IPv4:监听口),candidates 帧照发且
// result 捎带 public:true;对照 public_listen=false 同样 classify 失败 →
// 无候选帧、result 不带 public 键。
func TestHandleSessionPublicListenFallbackCandidate(t *testing.T) {
	expectIface := firstNonLoopbackIPv4(collectAgentNetSnapshot())
	if expectIface == "" {
		t.Skip("本机无非回环 IPv4,接口快照兜底候选无料可验")
	}

	t.Run("public_listen_on_falls_back_to_iface_snapshot", func(t *testing.T) {
		punchPublicListenCfg(t, &config.MeshConfig{PublicListen: boolPtr(true)})
		b, sink, _ := newPunchBForTest(t, func(bb *agentPeerPunchB) {
			bb.classify = func(*net.UDPConn, []string) (punch.PublicMapping, bool, error) {
				return punch.PublicMapping{}, false, fmt.Errorf("stun classify: 全部服务器不可达")
			}
			bb.serve = func(_ *net.UDPConn, opts punch.ListenerOpts) (int, <-chan struct{}, error) {
				done := make(chan struct{})
				t.Cleanup(func() { close(done) }) // 注入桩 done 永无人关,收摊防 goroutine 泄漏
				return 45678, done, nil
			}
		})

		b.handleSession(punchSessionFrame("pr-pub1", "ps-pub1", "tok", 15), sink.write, "device-b-self")

		result := sink.lastResult()
		if result == nil || result["ok"] != true {
			t.Fatalf("public_listen 会话应正常立起: %v", result)
		}
		if result["public"] != true {
			t.Fatalf("public_listen=true 时 result 必须捎带 public 声明: %v", result)
		}
		port := int(result["port"].(float64))
		var candFrame map[string]interface{}
		for _, f := range sink.snapshot() {
			if f["type"] == models.AgentEventPeerPunchCandidates {
				candFrame = f
			}
		}
		if candFrame == nil {
			t.Fatal("public_listen=true 且判型失败必须有接口快照兜底 candidates 帧")
		}
		cands, _ := candFrame["candidates"].([]interface{})
		if len(cands) != 1 {
			t.Fatalf("兜底候选应恰一条,得到 %v", cands)
		}
		host, candPort, err := net.SplitHostPort(cands[0].(string))
		if err != nil {
			t.Fatalf("兜底候选应为 host:port 形态: %v", cands[0])
		}
		if host != expectIface {
			t.Fatalf("兜底候选地址应为接口快照首个非回环 IPv4 %q,得到 %q", expectIface, host)
		}
		if candPort != strconv.Itoa(port) {
			t.Fatalf("兜底候选端口应等于监听口 %d,得到 %q", port, candPort)
		}
	})

	t.Run("public_listen_off_no_fallback_frame", func(t *testing.T) {
		punchPublicListenCfg(t, &config.MeshConfig{}) // 默认关(nil)
		b, sink, _ := newPunchBForTest(t, func(bb *agentPeerPunchB) {
			bb.classify = func(*net.UDPConn, []string) (punch.PublicMapping, bool, error) {
				return punch.PublicMapping{}, false, fmt.Errorf("stun classify: 全部服务器不可达")
			}
		})

		b.handleSession(punchSessionFrame("pr-pub0", "ps-pub0", "tok", 15), sink.write, "device-b-self")

		result := sink.lastResult()
		if result == nil || result["ok"] != true {
			t.Fatalf("对照会话应正常立起: %v", result)
		}
		if _, has := result["public"]; has {
			t.Fatalf("public_listen 未声明时 result 不得带 public 键: %v", result)
		}
		for _, f := range sink.snapshot() {
			if f["type"] == models.AgentEventPeerPunchCandidates {
				t.Fatal("public_listen=false 且判型失败不得回兜底 candidates 帧(既有语义零变化)")
			}
		}
	})
}

// TestAgentPeerPunchSessionResultPublicAutoCriterion 自动判据接线:STUN
// 映射 IP 命中本机真实接口地址(直挂)→ result 捎带 public:true,无需
// public_listen 配置;映射不命中(典型 NAT)→ 不带 public 键。
func TestAgentPeerPunchSessionResultPublicAutoCriterion(t *testing.T) {
	snap := collectAgentNetSnapshot()
	if len(snap.Interfaces) == 0 {
		t.Skip("no non-loopback IPv4 on this host")
	}
	punchPublicListenCfg(t, &config.MeshConfig{}) // public_listen 关,纯自动判据

	t.Run("mapping_matches_iface_declares_public", func(t *testing.T) {
		b, sink, _ := newPunchBForTest(t, func(bb *agentPeerPunchB) {
			bb.classify = func(*net.UDPConn, []string) (punch.PublicMapping, bool, error) {
				// 映射=本机真实接口地址(直挂形态)。
				return punch.PublicMapping{IP: net.ParseIP(snap.Interfaces[0].IPv4), Port: 45000}, false, nil
			}
		})
		b.handleSession(punchSessionFrame("pr-auto1", "ps-auto1", "tok", 15), sink.write, "device-b-self")
		result := sink.lastResult()
		if result == nil || result["ok"] != true {
			t.Fatalf("会话应正常立起: %v", result)
		}
		if result["public"] != true {
			t.Fatalf("映射=接口地址(直挂)时 result 必须捎带 public: %v", result)
		}
	})

	t.Run("mapping_off_iface_no_public_key", func(t *testing.T) {
		b, sink, _ := newPunchBForTest(t) // 默认桩宣告 TEST-NET 映射,不命中任何接口
		b.handleSession(punchSessionFrame("pr-auto0", "ps-auto0", "tok", 15), sink.write, "device-b-self")
		result := sink.lastResult()
		if result == nil || result["ok"] != true {
			t.Fatalf("会话应正常立起: %v", result)
		}
		if _, has := result["public"]; has {
			t.Fatalf("典型 NAT 映射不得带 public 键: %v", result)
		}
	})
}
