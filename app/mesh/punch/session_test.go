// session_test.go 见证载体会话缓存(Task 8)的六面:Put→Acquire 复用同一
// 会话(ref++)且并发双 Lease 各自 OpenStream 互不干扰/Acquire 撞死会话
// (B 侧 TTL 收摊)→驱逐+返回未建立/Release 归零后空闲计时到点即收摊/
// ref>0 期间时钟推进远超空闲窗也不收摊(在飞连接不被回收)/KeepAlive 双
// 参数协作(dialer 侧 DialOpts.KeepAlive→quic.Config 15s + cache 路径
// 强制置真)/多 goroutine Acquire/Release 并发安全。全部 loopback,复用
// tls_test/dialer_test 既有 helper。
//
// punchEchoHandler 纪律在本文件全程保持:凡真 DialPeer 打通的会话,其
// token 首流(session.Stream)与每条开的流都要完成一次 4 字节往返——B 侧
// handler 阻塞读若等到会话死亡会 t.Errorf,喂饱即无残留。
package punch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

// fakeClock 原子读、单点推进的注入时钟(SkipMemo.now 的并发安全版):
// 测试推进空闲计时不真睡 5 分钟,-race 下多 goroutine 读也安全。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Now()} }

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

// punchCacheDial 用真 DialPeer 打通一条可入缓存的会话(token 门禁过门),
// 标准收摊随测试;token 首流不在此喂——各用例按需回显(见文件头纪律)。
func punchCacheDial(t *testing.T, token string) (*PunchSession, net.Addr, PunchTLSConfig) {
	t.Helper()
	addr, cfg, _ := punchListenerEnv(t, token, 5*time.Second, punchEchoHandler(t))
	session, err := DialPeer(punchDialerUDPConn(t), DialOpts{
		Token:      token,
		TLS:        cfg,
		Candidates: func() []string { return []string{addr.String()} },
	})
	if err != nil {
		t.Fatalf("缓存测试拨号失败: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Conn.CloseWithError(0, "")
		_ = session.Transport.Close()
	})
	return session, addr, cfg
}

// ---------------------------------------------------------------------------
// (a) Put→Acquire 复用同一会话(ref++);并发双 Lease 各自 OpenStream 独立
// ---------------------------------------------------------------------------

// TestSessionCachePutAcquireReusesSession Put 入缓后 Acquire 必须拿到同一
// 会话指针(复用而非重拨),ref 计数准确;两个并发持有的 Lease 各自
// OpenStream 开出的流互不干扰——各自回显各自载荷,串流/误关都会在此现形。
func TestSessionCachePutAcquireReusesSession(t *testing.T) {
	const peer = "peer-cache-a"
	session, _, _ := punchCacheDial(t, "punch-session-token")

	cache := NewSessionCache()
	lease1 := cache.Put(peer, session)
	if lease1 == nil {
		t.Fatal("Put 活会话必须返回持有 1 个引用的 Lease,却得到 nil")
	}
	punchEchoRoundtrip(t, session.Stream, "tok1") // 喂饱 token 首流的 B 侧 handler(文件头纪律)

	lease2 := cache.Acquire(peer)
	if lease2 == nil {
		t.Fatal("活会话在缓存里,Acquire 必须复用而非返回未建立")
	}
	if lease2.entry.session != session {
		t.Fatal("Acquire 复用的必须是 Put 入缓的同一会话(重拨即复用语义失守)")
	}

	// 并发双 Lease 各自开流:流互不干扰,各自回显自己的 4 字节载荷。
	// 手工往返而非 punchEchoRoundtrip——t.Fatalf 不能在非测试 goroutine 里调。
	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	for i, payload := range []string{"aaa1", "bbb2"} {
		lease := []*Lease{lease1, lease2}[i]
		wg.Add(1)
		go func(l *Lease, payload string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			stream, err := l.OpenStream(ctx)
			if err != nil {
				errCh <- fmt.Errorf("并发 Lease 开流失败: %w", err)
				return
			}
			if err := stream.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
				errCh <- fmt.Errorf("设读围栏失败: %w", err)
				return
			}
			if _, err := stream.Write([]byte(payload)); err != nil {
				errCh <- fmt.Errorf("写载荷 %q 失败: %w", payload, err)
				return
			}
			back := make([]byte, len(payload))
			if _, err := io.ReadFull(stream, back); err != nil {
				errCh <- fmt.Errorf("读 %q 回显失败: %w", payload, err)
				return
			}
			if string(back) != payload {
				errCh <- fmt.Errorf("回显串流: 得到 %q, 期望 %q", string(back), payload)
				return
			}
			errCh <- nil
		}(lease, payload)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}

	lease1.Release()
	lease2.Release()
	if entry := cache.sessions[peer]; entry == nil || entry.refs != 0 {
		t.Fatalf("双 Lease 全部 Release 后 ref 应归零,得到 %+v", entry)
	}
}

// ---------------------------------------------------------------------------
// (b) Acquire 撞死会话(B 侧关停)→驱逐+返回未建立
// ---------------------------------------------------------------------------

// TestSessionCacheAcquireEvictsDeadSession B 侧关停本条会话 → 客户端
// Context().Done 关闭 → Acquire 必须驱逐(关 Transport+删表)并返回未建立
// (nil),让上层现场重拨——死会话检测是缓存主路径(B 侧 TTL=90s 硬寿命)。
// 死亡制造用「B 侧对单条会话 CloseWithError」:CONNECTION_CLOSE 真发到
// A 侧,Done 可观测——而 listener TTL 收摊走的 Transport.Close 是静默关停
// (quic-go 语义:不发 CONNECTION_CLOSE 给对端),A 侧要等自身空闲超时
// (~30s)才观察到,不可短窗断言(详见 session.go 文件头)。
func TestSessionCacheAcquireEvictsDeadSession(t *testing.T) {
	const peer = "peer-cache-b"
	serverCert, serverFP := testPunchCert(t, "punch-die-server")
	clientCert, clientFP := testPunchCert(t, "punch-die-client")
	cfg := PunchTLSConfig{
		ServerCert:        serverCert,
		ClientCert:        clientCert,
		ServerFingerprint: serverFP,
		ClientFingerprint: clientFP,
		ExpectedToken:     "punch-session-token",
	}
	addr, accepted := punchTestServer(t, ServerTLSConfig(cfg), func(*quic.Stream) {})

	// A 侧现场组一条可入缓存的会话(真 Transport+真握手):本用例的对象是
	// 死亡后的驱逐路径,出生路径不经 DialPeer,免受其存活确认窗与 B 侧
	// 关停时序相互追赶的抖动。
	udpConn := punchDialerUDPConn(t)
	tr := &quic.Transport{Conn: udpConn}
	t.Cleanup(func() { _ = tr.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	qconn, err := tr.Dial(ctx, addr, ClientTLSConfig(cfg), nil)
	if err != nil {
		t.Fatalf("现场拨号失败: %v", err)
	}
	session := &PunchSession{Transport: tr, Conn: qconn}

	cache := NewSessionCache()
	if lease := cache.Put(peer, session); lease == nil {
		t.Fatal("活会话入缓存被拒")
	}

	// B 侧关停本条会话:CONNECTION_CLOSE 到达 A 侧,Done 关闭。
	var bConn *quic.Conn
	select {
	case bConn = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("等 B 侧 Accept 到会话超时")
	}
	_ = bConn.CloseWithError(0, "punch 测试:B 侧主动关停会话")
	select {
	case <-session.Conn.Context().Done(): // 客户端侧亲历会话死亡
	case <-time.After(5 * time.Second):
		t.Fatal("B 侧关停后客户端会话未死亡(死检测的前提不成立)")
	}

	if lease := cache.Acquire(peer); lease != nil {
		lease.Release()
		t.Fatal("死会话必须被驱逐并返回未建立(nil),却复用了死会话")
	}
	if len(cache.sessions) != 0 {
		t.Fatalf("死会话应被逐出缓存,表内仍剩 %d 项", len(cache.sessions))
	}
	if cache.Acquire(peer) != nil {
		t.Fatal("驱逐后再次 Acquire 应稳定返回未建立(不是一次性运气)")
	}
}

// ---------------------------------------------------------------------------
// (c) Release 归零→空闲计时到点→会话关;未到点仍可复用
// ---------------------------------------------------------------------------

// TestSessionCacheIdleReapAfterRelease Release 归零起空闲计时(注入时钟,
// SkipMemo.now 先例):未到点 Acquire 仍复用;推进过 punchSessionIdleTimeout
// 后的下一次缓存操作(惰性回收)必须收摊会话——Transport.Close 的可观测
// 死亡就是见证。
func TestSessionCacheIdleReapAfterRelease(t *testing.T) {
	const peer = "peer-cache-c"
	session, _, _ := punchCacheDial(t, "punch-session-token")

	cache := NewSessionCache()
	clk := newFakeClock()
	cache.now = clk.Now
	lease := cache.Put(peer, session)
	punchEchoRoundtrip(t, session.Stream, "tok1")
	lease.Release() // ref 归零,空闲计时起点=clk 基准

	// 空闲未到期:Acquire 仍须复用(回收只能发生在到点或死亡)。
	lease = cache.Acquire(peer)
	if lease == nil || lease.entry.session != session {
		t.Fatal("空闲未到期的会话仍应可复用,却返回未建立/换了会话")
	}
	lease.Release()

	// 推进过 5min 空闲窗,再触缓存操作 → 惰性回收收摊会话。
	clk.Advance(punchSessionIdleTimeout + time.Second)
	if l := cache.Acquire(peer); l != nil {
		l.Release()
		t.Fatal("空闲超时的会话应被回收,Acquire 应返回未建立")
	}
	select {
	case <-session.Conn.Context().Done():
	case <-time.After(3 * time.Second):
		t.Fatal("空闲回收应收摊会话(Transport.Close→连接死亡),连接仍活")
	}
	if len(cache.sessions) != 0 {
		t.Fatalf("回收后表应清空,仍剩 %d 项", len(cache.sessions))
	}
}

// ---------------------------------------------------------------------------
// (c 续) ref>0 期间不关:时钟推进远超空闲窗,在飞会话照样活
// ---------------------------------------------------------------------------

// TestSessionCacheHoldsSessionWhileRefsHeld ref>0 期间会话绝不被空闲回收
// (「保活只为在飞连接服务」的回收侧对偶):时钟推进 10 倍空闲窗,Acquire
// 仍复用、开流往返仍通;全部 Release 归零+推进过窗后才收摊。
func TestSessionCacheHoldsSessionWhileRefsHeld(t *testing.T) {
	const peer = "peer-cache-d"
	session, _, _ := punchCacheDial(t, "punch-session-token")

	cache := NewSessionCache()
	clk := newFakeClock()
	cache.now = clk.Now
	lease1 := cache.Put(peer, session)
	punchEchoRoundtrip(t, session.Stream, "tok1")
	lease2 := cache.Acquire(peer) // ref=2:两条 Lease 在飞

	clk.Advance(punchSessionIdleTimeout * 10) // 远超空闲窗
	lease3 := cache.Acquire(peer)             // ref=3:回收扫描必须跳过 refs>0
	if lease3 == nil || lease3.entry.session != session {
		t.Fatal("ref>0 期间的会话不得被空闲回收,Acquire 应照常复用")
	}
	// 在飞最强见证:开流+往返仍通(若被误收摊,开流/读必败)。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := lease3.OpenStream(ctx)
	if err != nil {
		t.Fatalf("ref>0 期间开流失败(会话被误回收?): %v", err)
	}
	punchEchoRoundtrip(t, stream, "hold")

	lease1.Release()
	lease2.Release()
	lease3.Release()
	clk.Advance(punchSessionIdleTimeout + time.Second)
	if cache.Acquire(peer) != nil {
		t.Fatal("归零+空闲超窗后应回收,Acquire 应返回未建立")
	}
	select {
	case <-session.Conn.Context().Done():
	case <-time.After(3 * time.Second):
		t.Fatal("归零+超窗后会话应收摊,连接仍活")
	}
}

// ---------------------------------------------------------------------------
// (d) KeepAlive 双参数协作:dialer 侧 DialOpts.KeepAlive→quic.Config;
//      cache 侧 DialAndPut 强制置真 + 活会话在场不重拨
// ---------------------------------------------------------------------------

// TestSessionCacheKeepAliveInvariant 保活不变式的三面锁:
// dialer 侧——KeepAlive=false 不带 quic.Config(一次性拨号不保活),=true
// 带 15s KeepAlivePeriod 且不夹带其他覆盖项;cache 侧——DialAndPut 无论
// 调用方传什么都强制置真(缓存路径的义务);端到端——cache 路径真拨号
// 打通+活会话在场时二次 DialAndPut 不重拨。
func TestSessionCacheKeepAliveInvariant(t *testing.T) {
	t.Run("dialer 侧 DialOpts.KeepAlive→quic.Config", func(t *testing.T) {
		if got := punchDialQUICConfig(DialOpts{}); got != nil {
			t.Fatalf("非缓存路径(KeepAlive=false)不应带 quic.Config,得到 %+v", got)
		}
		got := punchDialQUICConfig(DialOpts{KeepAlive: true})
		if got == nil {
			t.Fatal("缓存路径(KeepAlive=true)必须带 quic.Config")
		}
		if got.KeepAlivePeriod != punchKeepAlivePeriod {
			t.Fatalf("保活周期应为 %v,得到 %v", punchKeepAlivePeriod, got.KeepAlivePeriod)
		}
		if punchKeepAlivePeriod != 15*time.Second {
			t.Fatalf("保活周期常量应为 15s,得到 %v", punchKeepAlivePeriod)
		}
		// 只置保活一字段,其余覆盖项一概不带——行为面保持与未传 Config 一致,
		// 默认值交给 quic-go populateConfig。
		cfg := *got
		cfg.KeepAlivePeriod = 0
		if !reflect.DeepEqual(cfg, quic.Config{}) {
			t.Fatalf("除保活外不应夹带其他覆盖项: %+v", cfg)
		}
	})
	t.Run("cache 侧 DialAndPut 强制置真", func(t *testing.T) {
		cache := NewSessionCache()
		var seen []bool
		cache.dial = func(_ *net.UDPConn, opts DialOpts) (*PunchSession, error) {
			seen = append(seen, opts.KeepAlive)
			return nil, errors.New("punch cache 测试:注入拨号即失败")
		}
		if _, err := cache.DialAndPut("peer-cache-e", nil, DialOpts{Token: "x"}); err == nil {
			t.Fatal("注入拨号失败应透传错误,DialAndPut 却报成功")
		}
		if len(seen) != 1 || !seen[0] {
			t.Fatalf("cache 路径拨号必须强制 KeepAlive=true(调用方传 false 也置真),记录到 %v", seen)
		}
	})
	t.Run("cache 侧端到端+活会话不重拨", func(t *testing.T) {
		const peer = "peer-cache-f"
		// handler 按流分职:首流=过门流(DialAndPut 已 FIN,余量空 EOF,
		// 排水即退);后续流=数据面,回显。punchEchoHandler 的「首流即
		// 4 字节数据」是 DialPeer 直拨旧契约,与此处不合。
		var streamN atomic.Int32
		tokFirstThenEcho := func(c net.Conn) {
			if streamN.Add(1) == 1 {
				_, _ = io.Copy(io.Discard, c)
				return
			}
			punchEchoHandler(t)(c)
		}
		addr, cfg, _ := punchListenerEnv(t, "punch-session-token", 5*time.Second, tokFirstThenEcho)
		cache := NewSessionCache()
		var dials atomic.Int32
		cache.dial = func(conn *net.UDPConn, opts DialOpts) (*PunchSession, error) {
			dials.Add(1)
			if !opts.KeepAlive {
				return nil, errors.New("cache 路径未置真 KeepAlive(不变式失守)")
			}
			return DialPeer(conn, opts)
		}
		l1, err := cache.DialAndPut(peer, punchDialerUDPConn(t), DialOpts{
			Token:      "punch-session-token",
			TLS:        cfg,
			Candidates: func() []string { return []string{addr.String()} },
		})
		if err != nil {
			t.Fatalf("DialAndPut 端到端拨号失败: %v", err)
		}
		// 数据面自第二流起(DialAndPut 已 FIN 首流——token 行消费完即收口,
		// 防 B 侧 handler 空等版本串成僵尸;Task 14 实锤),经 Lease 开新流。
		s1, err := l1.OpenStream(context.Background())
		if err != nil {
			t.Fatalf("DialAndPut 后首条数据流开流失败: %v", err)
		}
		punchEchoRoundtrip(t, s1, "tok1")

		// 活会话在场:二次 DialAndPut 必须复用缓存,不再拨号。
		l2, err := cache.DialAndPut(peer, punchDialerUDPConn(t), DialOpts{Token: "应被忽略不拨"})
		if err != nil {
			t.Fatalf("活会话在场的 DialAndPut 不应失败: %v", err)
		}
		if l2.entry.session != l1.entry.session {
			t.Fatal("二次 DialAndPut 应复用缓存会话而非重拨")
		}
		if dials.Load() != 1 {
			t.Fatalf("活会话在场时不得重拨,实际拨了 %d 次", dials.Load())
		}

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		stream, err := l2.OpenStream(ctx)
		if err != nil {
			t.Fatalf("DialAndPut 出让的 Lease 开流失败: %v", err)
		}
		punchEchoRoundtrip(t, stream, "dap2")
		l1.Release()
		l2.Release()
	})
}

// ---------------------------------------------------------------------------
// (e) 并发安全:多 goroutine Acquire/Release(-race 面)
// ---------------------------------------------------------------------------

// TestSessionCacheConcurrentAcquireRelease 三路并发锤缓存:8 goroutine 对
// 同 peer 高频 Acquire/Release(hit 路径+ref 计数),4 goroutine 敲不存在
// peer(miss+惰性回收扫描),2 goroutine 走 DialAndPut 注入失败拨号
// (Acquire→dial→err 全链)。收摊后 ref 必须精确归零——多退/少退都在此现形。
func TestSessionCacheConcurrentAcquireRelease(t *testing.T) {
	const peer = "peer-cache-g"
	session, _, _ := punchCacheDial(t, "punch-session-token")

	cache := NewSessionCache()
	lease0 := cache.Put(peer, session)
	punchEchoRoundtrip(t, session.Stream, "tok1")

	cache2 := NewSessionCache()
	cache2.dial = func(_ *net.UDPConn, _ DialOpts) (*PunchSession, error) {
		return nil, errors.New("punch cache 测试:注入拨号恒败")
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if l := cache.Acquire(peer); l != nil {
					l.Release()
				}
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if l := cache.Acquire("peer-cache-missing"); l != nil {
					l.Release()
				}
			}
		}()
	}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if _, err := cache2.DialAndPut("peer-cache-h", nil, DialOpts{Token: "x"}); err == nil {
					t.Errorf("注入拨号恒败,DialAndPut 必须带错返回")
				}
			}
		}()
	}
	wg.Wait()

	lease0.Release()
	if entry := cache.sessions[peer]; entry == nil || entry.refs != 0 {
		t.Fatalf("全部 Release 后 ref 应精确归零,得到 %+v(计数被并发写坏?)", entry)
	}
}

// ---------------------------------------------------------------------------
// (e 续) 并发同 Lease 双 Release:幂等语义在并发下的硬性要求
// ---------------------------------------------------------------------------

// TestSessionCacheConcurrentSameLeaseRelease 同一 Lease 被多 goroutine 同时
// Release(并混入 OpenStream):Task 9 的 defer 清理+连接关闭双触发是自然
// 写法,Release 必须并发幂等——重复 Release 不多退 ref、不 panic、不重置
// 空闲计时。修复前:锁外读 l.entry 与锁内置 nil 成对竞态(-race 报 DATA
// RACE),双双通过锁外检查后后进锁者对 nil 表项解引用直接 SIGSEGV。
// 每轮用 Acquire 现造新 Lease 再闸门齐放 4×Release+4×OpenStream,拉满
// 交叠窗;轮末断言 ref 恰为 0(多退成负/重复归零计时都会现形)。
func TestSessionCacheConcurrentSameLeaseRelease(t *testing.T) {
	const peer = "peer-cache-i"
	// 容错回显 handler:并发用例不保证喂饱每条开出的流,读面遇错静默退
	// (punchEchoHandler 会在会话死亡时 t.Errorf,不适用于本用例)。
	tolerant := func(c net.Conn) {
		buf := make([]byte, 4)
		for {
			if _, err := io.ReadFull(c, buf); err != nil {
				return
			}
			_, _ = c.Write(buf)
		}
	}
	addr, cfg, _ := punchListenerEnv(t, "punch-session-token", 5*time.Second, tolerant)
	session, err := DialPeer(punchDialerUDPConn(t), DialOpts{
		Token:      "punch-session-token",
		TLS:        cfg,
		Candidates: func() []string { return []string{addr.String()} },
	})
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Conn.CloseWithError(0, "")
		_ = session.Transport.Close()
	})

	cache := NewSessionCache()
	lease0 := cache.Put(peer, session)
	lease0.Release() // 归零,让每轮 Acquire 的 ref 记账自封闭(1→0)

	for round := 0; round < 50; round++ {
		lease := cache.Acquire(peer)
		if lease == nil {
			t.Fatalf("第 %d 轮 Acquire 失败(活会话不该 miss)", round)
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		for j := 0; j < 4; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				lease.Release()
			}()
		}
		for j := 0; j < 4; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if stream, err := lease.OpenStream(ctx); err == nil {
					_ = stream.Close()
				}
			}()
		}
		close(start) // 闸门齐放:8 个 goroutine 同时冲同一 Lease
		wg.Wait()
		if entry := cache.sessions[peer]; entry == nil || entry.refs != 0 {
			t.Fatalf("第 %d 轮:同 Lease 并发重复 Release 应只生效一次(ref 应恰为 0),得到 %+v", round, entry)
		}
	}
}

// ---------------------------------------------------------------------------
// (f) 孤儿表项语义:顶替驱逐 + 跨顶替 Release 自关
// ---------------------------------------------------------------------------

// punchCacheTwinSessions 起 B 侧容错回显 listener 并打通两条会话(各持
// 独立打洞 socket;同 listener 多会话是 Task 6 既有面):孤儿语义两用例的
// 公共底座。流面喂饱纪律不适用于孤儿用例(被顶替/自关的会话其流面天然
// 悬置),handler 读面遇错静默退、读 4 字节回显 4 字节。
func punchCacheTwinSessions(t *testing.T) (*PunchSession, *PunchSession) {
	t.Helper()
	tolerant := func(c net.Conn) {
		buf := make([]byte, 4)
		for {
			if _, err := io.ReadFull(c, buf); err != nil {
				return
			}
			_, _ = c.Write(buf)
		}
	}
	addr, cfg, _ := punchListenerEnv(t, "punch-session-token", 5*time.Second, tolerant)
	dial := func() *PunchSession {
		t.Helper()
		session, err := DialPeer(punchDialerUDPConn(t), DialOpts{
			Token:      "punch-session-token",
			TLS:        cfg,
			Candidates: func() []string { return []string{addr.String()} },
		})
		if err != nil {
			t.Fatalf("孤儿语义用例拨号失败: %v", err)
		}
		t.Cleanup(func() {
			_ = session.Conn.CloseWithError(0, "")
			_ = session.Transport.Close()
		})
		return session
	}
	return dial(), dial()
}

// TestSessionCacheIdleEntryClosedOnReplacement 顶替驱逐:空闲(ref=0)表项
// 被同 peer 新会话顶替时,旧会话必须在 Put 当场关(Transport.Close→连接
// 死亡)——不是退回表里等 5min 空闲回收。若此路径失守,被顶替的旧会话
// 要靠 B 侧 TTL(90s)才死,静默泄漏一条保活连接。
func TestSessionCacheIdleEntryClosedOnReplacement(t *testing.T) {
	const peer = "peer-cache-j"
	sessionA, sessionB := punchCacheTwinSessions(t)

	cache := NewSessionCache()
	leaseA := cache.Put(peer, sessionA)
	leaseA.Release() // ref 归零进入空闲计时(远未到点,5min)

	leaseB := cache.Put(peer, sessionB) // 顶替:旧表项无人引用
	if leaseB == nil {
		t.Fatal("顶替 Put 新会话被拒")
	}
	// 旧会话必须当场死:Done 在秒级围栏内关闭即证「即关而非空闲计时」
	//(空闲路要等 5min,测试时窗内绝无可能)。
	select {
	case <-sessionA.Conn.Context().Done():
	case <-time.After(3 * time.Second):
		t.Fatal("被顶替的空闲表项应即关(Transport.Close→连接死亡),旧会话仍活(静默泄漏)")
	}
	// 新表项完好且在表:开流往返通,表槽指向新会话。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := leaseB.OpenStream(ctx)
	if err != nil {
		t.Fatalf("顶替后的新会话开流失败: %v", err)
	}
	punchEchoRoundtrip(t, stream, "rep1")
	if entry := cache.sessions[peer]; entry == nil || entry.session != sessionB {
		t.Fatalf("表槽应指向顶替者 sessionB,得到 %+v", entry)
	}
	leaseB.Release()
}

// TestSessionCacheOrphanReleaseClosesReplacedEntry 跨顶替 Release 自关:
// 旧表项仍被持有(ref>0)时被同 peer 新会话顶替——顶替瞬间旧会话不得被
// 关(refcount 保护:持有者还在用),持有者最后的 Release 归零时发现自己
// 已被顶替,当场自关收尾;新表项全程完好可用。若此路径失守,被顶替的
// 持有会话在 Release 后变无主会话,泄漏至 B 侧 TTL 才死。
func TestSessionCacheOrphanReleaseClosesReplacedEntry(t *testing.T) {
	const peer = "peer-cache-k"
	sessionA, sessionB := punchCacheTwinSessions(t)

	cache := NewSessionCache()
	leaseA := cache.Put(peer, sessionA) // ref=1,持有中
	leaseB := cache.Put(peer, sessionB) // 顶替持有中的表项:旧表项成孤儿但不当场关

	select {
	case <-sessionA.Conn.Context().Done():
		t.Fatal("持有中的会话被顶替时不得当场关(refcount 保护失守)")
	case <-time.After(150 * time.Millisecond):
	}

	leaseA.Release() // 孤儿表项最后的 Release:归零当场自关
	select {
	case <-sessionA.Conn.Context().Done():
	case <-time.After(3 * time.Second):
		t.Fatal("孤儿表项最后的 Release 应当场自关,旧会话仍活(无主泄漏)")
	}
	// 新表项全程未受牵连:开流往返通、表槽指向新会话、Release 后归零。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := leaseB.OpenStream(ctx)
	if err != nil {
		t.Fatalf("跨顶替后的新会话开流失败(被旧表项收摊误伤?): %v", err)
	}
	punchEchoRoundtrip(t, stream, "orp2")
	if entry := cache.sessions[peer]; entry == nil || entry.session != sessionB {
		t.Fatalf("表槽应指向顶替者 sessionB,得到 %+v", entry)
	}
	leaseB.Release()
	if entry := cache.sessions[peer]; entry == nil || entry.refs != 0 {
		t.Fatalf("新表项 Release 后 ref 应归零,得到 %+v", entry)
	}
}
