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
	"strings"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/mesh/punch"
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
