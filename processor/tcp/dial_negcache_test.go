package tcp

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"syscall"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
)

// 两级闸:begin 放行 → settle(失败) → 冷却窗内秒拒 → 过期放行(惰性清除)。
func TestDialNegCacheRejectWithinTTL(t *testing.T) {
	c := newDialNegCache(50*time.Millisecond, 16)
	if d := c.begin("k"); d != nil {
		t.Fatalf("empty cache must proceed, got %+v", d)
	}
	c.settle("k", true)
	d := c.begin("k")
	if d == nil || d.inFlight {
		t.Fatalf("failed key must be negcache-denied, got %+v", d)
	}
	if d.remaining <= 0 || d.remaining > 50*time.Millisecond {
		t.Fatalf("remaining = %v, want in (0, 50ms]", d.remaining)
	}
	time.Sleep(60 * time.Millisecond)
	if d := c.begin("k"); d != nil {
		t.Fatalf("expired entry must be released (lazy eviction), got %+v", d)
	}
}

// single-flight:真拨在途期间同键秒拒(InFlight=true),settle 后按成败转入
// 冷却窗或恢复放行。
func TestDialNegCacheSingleFlight(t *testing.T) {
	c := newDialNegCache(time.Second, 16)
	if d := c.begin("k"); d != nil {
		t.Fatalf("first begin must proceed, got %+v", d)
	}
	d := c.begin("k")
	if d == nil || !d.inFlight {
		t.Fatalf("in-flight key must be denied with inFlight, got %+v", d)
	}
	c.settle("k", true) // 失败收束:释放 + 记冷却
	if d := c.begin("k"); d == nil || d.inFlight {
		t.Fatalf("after failure settle, key must be negcache-denied (not in-flight), got %+v", d)
	}
	// 成功收束清权:settle(false) 后不得再被旧失败挡(白盒直调,模拟过期
	// 边界竞态下「同键真拨成功」的清权路径)。
	c.settle("k", false)
	if d := c.begin("k"); d != nil {
		t.Fatalf("success settle must clear neg entry, got %+v", d)
	}
}

// 满容整表清仓后重插:旧键全部出窗,新键在窗。
func TestDialNegCacheCapEvicts(t *testing.T) {
	c := newDialNegCache(time.Second, 2)
	for _, k := range []string{"a", "b", "c"} { // c 触发清仓后重插
		if d := c.begin(k); d != nil {
			t.Fatalf("begin %s must proceed (no entry, not inflight), got %+v", k, d)
		}
		c.settle(k, true)
	}
	if _, hit := c.reject("a"); hit {
		t.Fatalf("cap flush must evict older keys")
	}
	if _, hit := c.reject("c"); !hit {
		t.Fatalf("re-inserted key must stay cached")
	}
}

// 键口径:域名优先、无域名回退 IP、端口必带、nil 安全。
func TestDialNegCacheKey(t *testing.T) {
	if got := dialNegCacheKey(&M.Metadata{HostName: "chatgpt.com", DstPort: 443}); got != "chatgpt.com:443" {
		t.Fatalf("domain key = %q, want chatgpt.com:443", got)
	}
	if got := dialNegCacheKey(&M.Metadata{DstIP: netip.MustParseAddr("1.2.3.4"), DstPort: 80}); got != "1.2.3.4:80" {
		t.Fatalf("ip fallback key = %q, want 1.2.3.4:80", got)
	}
	if got := dialNegCacheKey(nil); got != "" {
		t.Fatalf("nil metadata key = %q, want empty", got)
	}
}

// fakeTimeout 具备超时语义但不属本机侧信号——被墙域口径(静默丢包只以
// 超时形态出现,必须照记冷却窗)。
type fakeDialTimeout struct{}

func (fakeDialTimeout) Error() string   { return "i/o timeout" }
func (fakeDialTimeout) Timeout() bool   { return true }
func (fakeDialTimeout) Temporary() bool { return false }

// 本机侧失败豁免:DNS 解析失败与网络不可达不入冷却窗(我方链路信号);
// 超时类照记(被墙域口径)。
func TestIsLocalSideDialFailure(t *testing.T) {
	if !isLocalSideDialFailure(&net.DNSError{Err: "no such host", Name: "x.example"}) {
		t.Fatalf("DNSError must count as local-side")
	}
	if !isLocalSideDialFailure(&net.OpError{Op: "dial", Err: syscall.ENETUNREACH}) {
		t.Fatalf("network unreachable must count as local-side")
	}
	if isLocalSideDialFailure(&net.OpError{Op: "dial", Err: fakeDialTimeout{}}) {
		t.Fatalf("dial timeout is destination-class, must not count as local-side")
	}
}

// 端到端:dialDirect 对死目的首次真拨(记失败),第二次秒拒并带
// ErrDialNegCached 哨兵。目的地取 127.0.0.1:1——护栏口径(连接拒绝秒回,
// 不触真网、不挂测)。
func TestDialDirectNegCacheFastFails(t *testing.T) {
	resetDialNegCacheForTest()
	t.Cleanup(resetDialNegCacheForTest)

	h := &TCPConnectionHandler{}
	metadata := &M.Metadata{
		DstIP:   netip.MustParseAddr("127.0.0.1"),
		DstPort: 1,
	}

	ctx := context.Background()
	if _, err := h.dialDirect(ctx, metadata); err == nil {
		t.Fatalf("dial to 127.0.0.1:1 must fail")
	} else if errors.Is(err, ErrDialNegCached) {
		t.Fatalf("first dial must be a real dial, got negcached sentinel: %v", err)
	}

	_, err := h.dialDirect(ctx, metadata)
	if !errors.Is(err, ErrDialNegCached) {
		t.Fatalf("second dial must fast-fail with ErrDialNegCached, got %v", err)
	}
	var neg *DialNegCachedError
	if !errors.As(err, &neg) || neg.InFlight {
		t.Fatalf("sentinel must be a cooldown deny, got %+v", neg)
	}
}

// 客户端取消不记失败:取消后的拨号错误不把目的写进冷却窗,且在途登记被
// settle 释放(后续 begin 可重新放行)。
func TestDialDirectNegCacheSkipsCanceled(t *testing.T) {
	resetDialNegCacheForTest()
	t.Cleanup(resetDialNegCacheForTest)

	h := &TCPConnectionHandler{}
	metadata := &M.Metadata{
		DstIP:   netip.MustParseAddr("127.0.0.1"),
		DstPort: 1,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 先取消再拨,制造 context.Canceled 路径

	if _, err := h.dialDirect(ctx, metadata); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled dial must surface context.Canceled, got %v", err)
	}
	if _, hit := dialNegCacheShared.reject("127.0.0.1:1"); hit {
		t.Fatalf("canceled dial must not mark destination dead")
	}
	if d := dialNegCacheShared.begin("127.0.0.1:1"); d != nil {
		t.Fatalf("inflight registration must be released after canceled settle, got %+v", d)
	}
}
