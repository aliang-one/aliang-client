package tcp

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"syscall"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
)

// 直连腿拨号负缓存 + single-flight 的唯一口径。背景——被墙域的浏览器重试
// 风暴(每秒重连)此前把 TUN TCP 512 并发闸打满(全程数千次 connectex 超时
// dial),进程在 4 核小内存机上被 SCM 判无响应强杀(2026-11-19 0.196 五次
// 7034 静默死亡的根因)。闸门两级:
//  1. single-flight(在途去重):同键真拨在途期间,新到连接全部秒拒——
//     堵「首拨超时窗口」:拨号自身要挂满 30s 超时,没有这级时该窗口内
//     每秒重试都各开一条真拨,风暴在首个失败落地前原样复现;
//  2. 负缓存(冷却窗):拨号失败后同键 TTL 内不再真拨——把每键的真实
//     拨号压到每 TTL 至多一次(经在途登记后该不变量字面成立)。
const (
	dialNegCacheTTL     = 60 * time.Second
	dialNegCacheMaxKeys = 4096
)

// ErrDialNegCached 是闸门秒拒的哨兵(冷却窗或在途):Handle 与入口包装层
// 的错误日志据此把这类「预期失败」降级 Debug,不灌 ERROR 刷屏。
var ErrDialNegCached = errors.New("dial negative-cached")

// DialNegCachedError 携带拒绝原因与(冷却窗时)剩余时长,日志里能直接看出
// 还要等多久才恢复真拨。
type DialNegCachedError struct {
	Remaining time.Duration // 冷却窗剩余;在途去重时无意义
	InFlight  bool          // true=同键真拨在途;false=TTL 冷却窗
}

func (e *DialNegCachedError) Error() string {
	if e.InFlight {
		return "dial negative-cached: an attempt to this destination is already in flight, fast-fail until it settles"
	}
	return fmt.Sprintf("dial negative-cached: recent attempt to this destination failed, fast-fail for another %s", e.Remaining.Round(time.Millisecond))
}

func (e *DialNegCachedError) Unwrap() error { return ErrDialNegCached }

// negEntry 只存失败时刻:是否仍在窗口由 failedAt+TTL 对 now 判定,读侧算
// 剩余量,免写侧更新。
type negEntry struct {
	failedAt time.Time
}

// dialNegCache 是直连腿的拨号闸(进程级单例 dialNegCacheShared)。线程
// 安全;容量封顶 maxKeys,满时整表清仓后重插——键空间是域名/IP+端口,规
// 模有限,整表清仓比维护 LRU 简单且够用(最坏代价是清仓后 TTL 内多拨一
// 轮,不损正确性)。
type dialNegCache struct {
	mu       sync.Mutex
	entries  map[string]negEntry // 冷却窗:键 → 失败时刻
	inflight map[string]struct{} // single-flight:真拨在途键集
	ttl      time.Duration
	maxKeys  int
}

func newDialNegCache(ttl time.Duration, maxKeys int) *dialNegCache {
	return &dialNegCache{
		entries:  make(map[string]negEntry),
		inflight: make(map[string]struct{}),
		ttl:      ttl,
		maxKeys:  maxKeys,
	}
}

// deny 是 begin 拒绝的载荷:nil 即放行。
type deny struct {
	remaining time.Duration
	inFlight  bool
}

// begin 判闸并(放行时)登记在途。约定:settle 必须且只能对 begin 放行
// 的键调用一次——在途登记的清理完全依赖它。
func (c *dialNegCache) begin(key string) *deny {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		if remain := c.ttl - time.Since(e.failedAt); remain > 0 {
			return &deny{remaining: remain}
		}
		delete(c.entries, key) // 过期惰性清除
	}
	if _, ok := c.inflight[key]; ok {
		return &deny{inFlight: true}
	}
	c.inflight[key] = struct{}{}
	return nil
}

// settle 收束一次 begin 放行的真拨:释放在途登记;失败记入冷却窗(满容
// 整表清仓,见 struct 注释);成功则连冷却条目一并清权——目的地刚被实证
// 可达,不得因并发窗口里的旧失败被误拒(成功清权也覆盖过期边界的竞态)。
func (c *dialNegCache) settle(key string, failed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.inflight, key)
	if !failed {
		delete(c.entries, key)
		return
	}
	if len(c.entries) >= c.maxKeys {
		c.entries = make(map[string]negEntry, c.maxKeys/4)
	}
	c.entries[key] = negEntry{failedAt: time.Now()}
}

// reject 直查冷却窗(不入闸,测试与诊断用)。
func (c *dialNegCache) reject(key string) (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return 0, false
	}
	if remain := c.ttl - time.Since(e.failedAt); remain > 0 {
		return remain, true
	}
	return 0, false
}

// dialNegCacheKey 是闸的键口径:域名优先(被污染解析每次给不同 IP,域名
// 才是稳定键),无域名回退 DstIP;端口必带——443 被墙不代表同主机其他端
// 口也死。
func dialNegCacheKey(metadata *M.Metadata) string {
	if metadata == nil {
		return ""
	}
	host := metadata.HostName
	if host == "" && metadata.DstIP.IsValid() {
		host = metadata.DstIP.String()
	}
	if host == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d", host, metadata.DstPort)
}

// isLocalSideDialFailure 区分「我方出口不健康」与「目的死了」:DNS 解析
// 失败与网络 down/unreachable 是本机链路的信号,不入负缓存——否则 Wi-Fi
// 抖 5 秒会把摸过的目的地整批钉死一个 TTL。GFW 型被墙(静默丢包)只以
// 超时/连接重置形态出现,仍入缓存。已声明取舍:本机资源饥饿型的超时与
// 被墙超时在 dial 层不可分辨,同走缓存——风暴治理优先于个体误伤(TTL
// 有界,自愈)。
func isLocalSideDialFailure(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	return errors.Is(err, syscall.ENETDOWN) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH)
}

// dialNegCacheShared 进程级单例:直连拨号路径全走它(测试经
// resetDialNegCacheForTest 清场)。
var dialNegCacheShared = newDialNegCache(dialNegCacheTTL, dialNegCacheMaxKeys)

func resetDialNegCacheForTest() {
	dialNegCacheShared = newDialNegCache(dialNegCacheTTL, dialNegCacheMaxKeys)
}
