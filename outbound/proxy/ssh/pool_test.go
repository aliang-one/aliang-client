package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/outbound/proxy/ssh/sshtest"
	gossh "golang.org/x/crypto/ssh"
)

// fakeConn 是 gossh.Conn 的最小测试假体:Wait 广播关闭,其余方法桩化。
// 经 gossh.NewClient 包装成真 *gossh.Client 后即可塞进 cascadeClient——
// 池只依赖 Wait/Close 语义,生产类型因此零改动、零接口缝隙。
type fakeConn struct {
	closeOnce sync.Once
	closed    chan struct{}
}

func newFakeConn() *fakeConn { return &fakeConn{closed: make(chan struct{})} }

func (f *fakeConn) User() string          { return "fake" }
func (f *fakeConn) SessionID() []byte     { return nil }
func (f *fakeConn) ClientVersion() []byte { return nil }
func (f *fakeConn) ServerVersion() []byte { return nil }
func (f *fakeConn) RemoteAddr() net.Addr  { return nil }
func (f *fakeConn) LocalAddr() net.Addr   { return nil }

func (f *fakeConn) SendRequest(string, bool, []byte) (bool, []byte, error) {
	return false, nil, errors.New("fakeConn: no global requests")
}

func (f *fakeConn) OpenChannel(string, []byte) (gossh.Channel, <-chan *gossh.Request, error) {
	return nil, nil, errors.New("fakeConn: no channels")
}

func (f *fakeConn) Close() error {
	f.closeOnce.Do(func() { close(f.closed) })
	return nil
}

// Wait 必须广播:close 后全部等待者同时醒——链监控协程与 NewClient 内部
// 的收尾协程都在等同一个 conn。
func (f *fakeConn) Wait() error {
	<-f.closed
	return nil
}

// fakeCascadeClient 构造零网络的 *cascadeClient。chans/reqs 传已关闭通道,
// NewClient 的两个消费协程立即退出,只留 Wait 协程等 Close。
func fakeCascadeClient() *cascadeClient {
	chans := make(chan gossh.NewChannel)
	close(chans)
	reqs := make(chan *gossh.Request)
	close(reqs)
	return &cascadeClient{Client: gossh.NewClient(newFakeConn(), chans, reqs)}
}

// waitUntil 轮询断言 cond 在时限内变真,否则以 msg 失败。
func waitUntil(t *testing.T, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout: %s", msg)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPoolLeastActiveDispatch(t *testing.T) {
	baseline := runtime.NumGoroutine()

	dials := make([]int, 3)
	built := make([]*cascadeClient, 3)
	p := newPool(3, func(i int) func(context.Context) (*cascadeClient, error) {
		return func(context.Context) (*cascadeClient, error) {
			dials[i]++
			built[i] = fakeCascadeClient()
			return built[i], nil
		}
	})
	defer p.close()

	// 链 0 背上两个在途 channel,成为最忙链。
	p.chains[0].addActive()
	p.chains[0].addActive()

	cl, picked, err := p.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if picked == p.chains[0] {
		t.Fatal("acquire dispatched to the busiest chain 0; want least-active")
	}
	if picked != p.chains[1] && picked != p.chains[2] {
		t.Fatalf("acquire returned chain outside the pool: %p", picked)
	}
	idx := -1
	for i, c := range p.chains {
		if c == picked {
			idx = i
		}
	}
	if cl != built[idx] {
		t.Fatalf("acquire returned client %p, want the one dialed for chain %d", cl, idx)
	}
	if dials[0] != 0 || dials[idx] != 1 {
		t.Fatalf("dial counts = %v; want chain0=0, picked chain=%d", dials, idx)
	}
	if got := picked.active.Load(); got != 1 {
		t.Fatalf("picked chain active = %d, want 1", got)
	}

	p.close()
	assertNoGoroutineLeak(t, baseline)
}

func TestPoolRebuildAfterClientDeath(t *testing.T) {
	_, addr := sshtest.Start(t, sshtest.WithUsername("u"), sshtest.WithPassword("p"))
	baseline := runtime.NumGoroutine()

	d := newChainDialer(finalNode(addr, "u", "p", "", ""), nil, newTestKnownHosts(t))
	dials := 0
	p := newPool(1, func(int) func(context.Context) (*cascadeClient, error) {
		return func(ctx context.Context) (*cascadeClient, error) {
			dials++
			return d.dialChain(ctx)
		}
	})
	defer p.close()

	cl1, _, err := p.acquire(context.Background())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if dials != 1 {
		t.Fatalf("dials = %d, want 1", dials)
	}

	// 手动触发死亡:级联关闭底层,监控协程应标记 clientErr。
	if err := cl1.Close(); err != nil {
		t.Fatalf("kill client: %v", err)
	}
	waitUntil(t, "monitor goroutine must mark clientErr after conn death", func() bool {
		p.chains[0].mu.Lock()
		defer p.chains[0].mu.Unlock()
		return p.chains[0].clientErr != nil
	})

	cl2, ch, err := p.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire after death must rebuild: %v", err)
	}
	if dials != 2 {
		t.Fatalf("dials = %d, want 2 (rebuild)", dials)
	}
	if cl2 == cl1 {
		t.Fatal("acquire must return a rebuilt client, not the dead one")
	}
	ch.mu.Lock()
	fresh := ch.client == cl2 && ch.clientErr == nil && ch.failCount == 0 && ch.lastFail.IsZero()
	ch.mu.Unlock()
	if !fresh {
		t.Fatal("chain must hold the fresh client with clientErr cleared and failCount reset")
	}

	p.close()
	assertNoGoroutineLeak(t, baseline)
}

func TestPoolBackoff(t *testing.T) {
	dials := 0
	boom := errors.New("dial boom")
	p := newPool(1, func(int) func(context.Context) (*cascadeClient, error) {
		return func(context.Context) (*cascadeClient, error) {
			dials++
			return nil, boom
		}
	})
	defer p.close()

	if _, _, err := p.acquire(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("first acquire err = %v, want the dial error", err)
	}
	if dials != 1 {
		t.Fatalf("dials = %d, want 1", dials)
	}

	// 退避窗口内:直接拒绝,且不触发新拨号。
	_, _, err := p.acquire(context.Background())
	if err == nil {
		t.Fatal("second acquire within the backoff window must be rejected")
	}
	if !strings.Contains(err.Error(), "backoff") {
		t.Fatalf("error must mention backoff, got: %v", err)
	}
	if dials != 1 {
		t.Fatalf("dials = %d after in-window rejection, want 1 (no new dial)", dials)
	}
}

// TestChainBackoffSchedule 钉退避表与窗口边界:指数表 30s 封顶;
// 上次失败一旦跨出窗口,get 必须再次拨号(退避只挡窗口内)。
func TestChainBackoffSchedule(t *testing.T) {
	for _, tc := range []struct {
		failCount int
		want      time.Duration
	}{
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{4, 16 * time.Second},
		{5, 30 * time.Second}, // 1s<<5 = 32s,被钳到 30s
		{8, 30 * time.Second},
	} {
		if got := chainBackoff(tc.failCount); got != tc.want {
			t.Fatalf("chainBackoff(%d) = %s, want %s", tc.failCount, got, tc.want)
		}
	}

	dials := 0
	c := newChain(func(context.Context) (*cascadeClient, error) {
		dials++
		return nil, errors.New("still failing")
	})
	if _, err := c.get(context.Background()); err == nil {
		t.Fatal("first get must fail")
	}
	if dials != 1 {
		t.Fatalf("dials = %d, want 1", dials)
	}
	// 回拨 lastFail 到窗口之外,模拟时间流逝越过边界。
	c.mu.Lock()
	c.lastFail = c.lastFail.Add(-(chainBackoff(c.failCount) + time.Second))
	c.mu.Unlock()
	if _, err := c.get(context.Background()); err == nil {
		t.Fatal("get past the window must dial again (and fail again)")
	}
	if dials != 2 {
		t.Fatalf("dials = %d, want 2", dials)
	}
}

// TestPoolSkipsBackingOffChain 钉 spec §4「其余链接流量」:链 0 死且重建
// 失败进入退避(active=0 恒最小),链 1/2 健康(active=2)——若选链只按
// 严格最少活跃,死链 0 会吸走全部新连接;必须跳过退避窗口中的链。
// 修前此用例必红(审查者实证:健康链在场时 ok=0/failed=6)。
func TestPoolSkipsBackingOffChain(t *testing.T) {
	baseline := runtime.NumGoroutine()

	dials := make([]int, 3)
	built := make([]*cascadeClient, 3)
	p := newPool(3, func(i int) func(context.Context) (*cascadeClient, error) {
		return func(context.Context) (*cascadeClient, error) {
			dials[i]++
			if i == 0 {
				return nil, errors.New("chain 0 down")
			}
			built[i] = fakeCascadeClient()
			return built[i], nil
		}
	})
	defer p.close()

	// 链 0 重建失败进入退避(死链,active=0)。
	if _, err := p.chains[0].get(context.Background()); err == nil {
		t.Fatal("chain 0 dialFn must fail to prime backoff")
	}
	// 健康链各背 2 个在途 channel:严格最少活跃下 active=0 的死链必被选中。
	p.chains[1].addActive()
	p.chains[1].addActive()
	p.chains[2].addActive()
	p.chains[2].addActive()

	picked := make(map[*chain]int)
	for n := 0; n < 6; n++ {
		cl, ch, err := p.acquire(context.Background())
		if err != nil {
			t.Fatalf("acquire %d must succeed with healthy chains present, got: %v", n+1, err)
		}
		if ch == p.chains[0] {
			t.Fatalf("acquire %d landed on the backing-off chain 0", n+1)
		}
		idx := -1
		for i, c := range p.chains {
			if c == ch {
				idx = i
			}
		}
		if cl != built[idx] {
			t.Fatalf("acquire %d returned client %p, want the one dialed for chain %d", n+1, cl, idx)
		}
		picked[ch]++
	}
	if len(picked) != 2 {
		t.Fatalf("want both healthy chains to share traffic, got %d distinct chains", len(picked))
	}
	if dials[0] != 1 {
		t.Fatalf("chain 0 dialed %d times, want 1 (no retry inside backoff window)", dials[0])
	}
	if dials[1] != 1 || dials[2] != 1 {
		t.Fatalf("healthy chain dials = %v, want [_,1,1] (lazy reuse, no rebuild)", dials)
	}

	p.close()
	assertNoGoroutineLeak(t, baseline)
}

// TestPoolAllChainsBackoff 钉全链退避的报错语义(骨架契约原文:全部链都
// 在退避窗口时才返回错误),且窗口内不触发新拨号。
func TestPoolAllChainsBackoff(t *testing.T) {
	dials := 0
	p := newPool(2, func(int) func(context.Context) (*cascadeClient, error) {
		return func(context.Context) (*cascadeClient, error) {
			dials++
			return nil, errors.New("dial boom")
		}
	})
	defer p.close()

	for i := range p.chains {
		if _, err := p.chains[i].get(context.Background()); err == nil {
			t.Fatalf("chain %d get must fail", i)
		}
	}
	if dials != 2 {
		t.Fatalf("dials = %d, want 2 (one per chain)", dials)
	}

	_, _, err := p.acquire(context.Background())
	if err == nil {
		t.Fatal("acquire with all chains in backoff must fail")
	}
	if !strings.Contains(err.Error(), "all") || !strings.Contains(err.Error(), "backoff") {
		t.Fatalf("error must state all-chains backoff, got: %v", err)
	}
	if dials != 2 {
		t.Fatalf("dials = %d after all-backoff rejection, want 2 (no new dial)", dials)
	}
}

// TestChainWaitNilFoldsToErrClosed 钉监控协程的 nil→折算分支:干净关闭时
// Wait 返回 nil,必须折算成 errChainClosed 标记死亡——否则 clientErr 留
// nil,死 client 被 get 永久复用(fakeConn 的 Wait 恰好返 nil,正中场景)。
func TestChainWaitNilFoldsToErrClosed(t *testing.T) {
	dials := 0
	c := newChain(func(context.Context) (*cascadeClient, error) {
		dials++
		return fakeCascadeClient(), nil
	})

	cl1, err := c.get(context.Background())
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	_ = cl1.Close() // 干净关闭:fakeConn.Wait 返回 nil
	waitUntil(t, "monitor must mark clientErr even when Wait returns nil", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.clientErr != nil
	})
	c.mu.Lock()
	folded := errors.Is(c.clientErr, errChainClosed)
	c.mu.Unlock()
	if !folded {
		t.Fatalf("clientErr = %v, want errChainClosed", c.clientErr)
	}

	cl2, err := c.get(context.Background())
	if err != nil {
		t.Fatalf("get after clean close must rebuild: %v", err)
	}
	if cl2 == cl1 || dials != 2 {
		t.Fatalf("must rebuild a fresh client, dials = %d", dials)
	}
	// 收尾:关掉新 client,不留 fake 的 Wait 协程。
	c.mu.Lock()
	_ = cl2.Close()
	c.client = nil
	c.mu.Unlock()
}

func TestPoolSingle(t *testing.T) {
	for _, size := range []int{1, 0} {
		t.Run(fmt.Sprintf("poolSize=%d", size), func(t *testing.T) {
			baseline := runtime.NumGoroutine()
			dials := 0
			var built *cascadeClient
			p := newPool(size, func(int) func(context.Context) (*cascadeClient, error) {
				return func(context.Context) (*cascadeClient, error) {
					dials++
					built = fakeCascadeClient()
					return built, nil
				}
			})
			defer p.close()

			if len(p.chains) != 1 {
				t.Fatalf("pool size %d must clamp to exactly 1 chain, got %d", size, len(p.chains))
			}
			cl1, _, err := p.acquire(context.Background())
			if err != nil {
				t.Fatalf("first acquire: %v", err)
			}
			cl2, _, err := p.acquire(context.Background())
			if err != nil {
				t.Fatalf("second acquire: %v", err)
			}
			if cl1 != cl2 || cl1 != built {
				t.Fatal("single chain must lazy-reuse the same live client")
			}
			if dials != 1 {
				t.Fatalf("dials = %d, want 1 (no rebuild while alive)", dials)
			}
			if got := p.chains[0].active.Load(); got != 2 {
				t.Fatalf("active = %d, want 2 (both acquires in flight)", got)
			}
			p.chains[0].releaseActive()
			if got := p.chains[0].active.Load(); got != 1 {
				t.Fatalf("active after release = %d, want 1", got)
			}

			p.close()
			assertNoGoroutineLeak(t, baseline)
		})
	}
}
