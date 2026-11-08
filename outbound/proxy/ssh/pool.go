package ssh

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// chainBackoff 返回连续第 failCount 次失败后的重拨等待:1s 起步指数翻倍,
// 32s 钳到 30s(failCount 从 1 计:首次失败后等 2s,连续失败越多等越久,
// 拨号风暴上限 30s)。
func chainBackoff(failCount int) time.Duration {
	d := time.Second << min(failCount, 5) // 1s,2s,…,32s
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

// errChainClosed 监控协程的兜底死亡标记:gossh 的 Wait 只在 conn 关闭时
// 返回,但理论上可能返回 nil(干净关闭);clientErr 若留 nil,get 会永远
// 复用死 client——统一折算成非 nil 死亡信号。
var errChainClosed = errors.New("ssh chain connection closed")

// chain 是池里一条完整链(含跳板)。get 懒建;client 死亡由监控协程标记。
type chain struct {
	mu        sync.Mutex
	dialFn    func(ctx context.Context) (*cascadeClient, error)
	client    *cascadeClient // 读写维持 c.mu(选链不看,不受拨号长持影响)
	clientErr error          // 监控协程在 conn 死亡时写入;非 nil 即死
	// 退避状态原子化(无锁可读):get 全程持 c.mu 跨拨号(黑洞主机单跳
	// 挂满 15s、双跳约 30s),任何选链侧的 c.mu 获取都会被一次慢拨号
	// 串行化整池——因此 lastFail/failCount/active 都只允许原子读。
	// 写侧仅 get 在 c.mu 内互斥写(先 Store lastFail 再 Add failCount,
	// 落后读最坏把窗口算短一档,偏「可选」,由 get 在锁内复核兜底)。
	// lastFail 存 UnixNano(0=无,成功即清),牺牲单调钟——窗口只有
	// 1~30s,墙钟跳变影响可忽略。
	lastFail  atomic.Int64
	failCount atomic.Int32
	active    atomic.Int64 // 当前经此链打开的 channel 数
}

// backoffRemaining 返回退避窗口剩余时长(true=仍在窗口内)。无锁可读:
// lastFail/failCount 原子读,两者可能来自不同瞬间(撕裂快照),只会把
// 窗口判得偏短(偏「可选」),选链据此放行后仍由 get 在锁内用同一读数
// 复核(写侧被 c.mu 互斥,get 自己的读不撕裂),不构成正确性问题。
// 成功拨号清零 lastFail,故 lastFail 非零 ⟺ 正处失败退避 streak——
// 这是选链「跳过退避链」判定与 get 自身判退避共用的唯一事实源。
func (c *chain) backoffRemaining() (time.Duration, bool) {
	ns := c.lastFail.Load()
	if ns == 0 {
		return 0, false
	}
	if remain := chainBackoff(int(c.failCount.Load())) - time.Since(time.Unix(0, ns)); remain > 0 {
		return remain, true
	}
	return 0, false
}

func newChain(dialFn func(ctx context.Context) (*cascadeClient, error)) *chain {
	return &chain{dialFn: dialFn}
}

// get 返回存活 client;死了/没建则重建(受退避约束)。
// 同一条链的重建互斥(全程持 c.mu),并发 get 后到者直接复用新 client。
// 监控只看最终跳的 Wait——前跳死亡会级联拆掉最终跳传输,足够触发重建
// (spec 审查已确认此取舍)。
func (c *chain) get(ctx context.Context) (*cascadeClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil && c.clientErr == nil {
		return c.client, nil
	}
	if c.client != nil {
		_ = c.client.Close() // 幂等(sync.Once);死链收尾
		c.client = nil
	}
	if remain, in := c.backoffRemaining(); in {
		return nil, fmt.Errorf("ssh chain rebuilding, backoff %s", remain)
	}
	cl, err := c.dialFn(ctx)
	if err != nil {
		c.lastFail.Store(time.Now().UnixNano())
		c.failCount.Add(1)
		return nil, err
	}
	c.client = cl
	c.clientErr = nil
	c.lastFail.Store(0) // 退避只属于失败 streak;成功即出窗(否则选链会错跳健康链)
	c.failCount.Store(0)
	// 监控:conn 一死立刻标记,下次 get 重建;不阻塞当前调用方。
	go func() {
		err := cl.Wait()
		if err == nil {
			err = errChainClosed
		}
		c.mu.Lock()
		if c.client == cl { // 已被新 client 顶替时不误标
			c.clientErr = err
		}
		c.mu.Unlock()
	}()
	return cl, nil
}

// drop 收走死链,返回是否真的收走:仅当 client 已被监控协程标记死亡时
// 关闭并清空,返回 true;活链或已无 client 时不动,返回 false。调用方
// (DialContext)以此区分「client 已死,值得换链重试」与「活链上的业务性
// 失败,透传不重试」(spec §4)。
func (c *chain) drop() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil && c.clientErr != nil {
		_ = c.client.Close()
		c.client = nil
		return true
	}
	return false
}

func (c *chain) addActive() { c.active.Add(1) }

// releaseActive 与骨架的「>0 才减」语义等价:CAS 循环保证不落到负值
// (负值会让该链在最少活跃选择中被永久偏选)。
func (c *chain) releaseActive() {
	for {
		n := c.active.Load()
		if n <= 0 {
			return
		}
		if c.active.CompareAndSwap(n, n-1) {
			return
		}
	}
}

// pool 是 N 条独立链的集合;每次 acquire 在「不在退避窗口」的链里选活跃
// channel 数最少的(同分从 next 轮转起点扫,保证均匀)。已知取舍(与
// spec §4 及计划拍板一致):
//   - 选链在 p.mu 内快照即出,建链在 chain.mu 内——一次慢拨号(握手至多
//     15s)不锁住其余链的 acquire,只有同一条链的重建互斥;
//   - 处于退避窗口的链(上次拨号失败后未期满)被选链跳过,死链不再吸走
//     新连接,流量由其余链接住(spec §4「其余链接流量」);
//   - 死但未进退避的链保持可选:其 get 立即重建,保留懒自愈语义;
//   - 全部链都在退避窗口时 acquire 报错并带最近剩余等待——与「全断现建
//     救急链 ≡ 任一链退避期满即重建」的拍板等价,由调用方决定重试。
type pool struct {
	mu     sync.Mutex
	chains []*chain
	next   int // 同分轮转起点
}

func newPool(size int, dialFn func(index int) func(ctx context.Context) (*cascadeClient, error)) *pool {
	if size < 1 {
		size = 1
	}
	p := &pool{}
	for i := 0; i < size; i++ {
		p.chains = append(p.chains, newChain(dialFn(i)))
	}
	return p
}

// acquire 在非退避链里选活跃数最少的并取得 client;全部链都在退避窗口
// 时返回错误(带最近剩余等待)。返回的 chain 供调用方在 channel 关闭时
// releaseActive 配对减数。
// 选链全程只做原子读(退避快照 + 活跃数),不取任何 chain.mu——get 会
// 全程持 chain.mu 跨拨号,选链若碰 chain.mu,一条慢拨号就把全池 acquire
// 排队串行化(且该链每次退避期满复发)。p.mu → chain.mu 的嵌套只剩
// close() 一处,方向恒定,get/addActive/releaseActive 只取 chain.mu,
// 无反向嵌套。
func (p *pool) acquire(ctx context.Context) (*cascadeClient, *chain, error) {
	p.mu.Lock()
	var best *chain
	var soonest time.Duration // 全部退避时,最近一条链期满还需多久
	for i := 0; i < len(p.chains); i++ {
		idx := (p.next + i) % len(p.chains)
		c := p.chains[idx]
		if remain, in := c.backoffRemaining(); in {
			if soonest == 0 || remain < soonest { // soonest==0 是未置哨兵(remain 恒>0)
				soonest = remain
			}
			continue
		}
		if best == nil || c.active.Load() < best.active.Load() {
			best = c
		}
	}
	p.next = (p.next + 1) % len(p.chains)
	p.mu.Unlock()
	if best == nil {
		return nil, nil, fmt.Errorf(
			"ssh pool: all %d chains rebuilding in backoff, soonest retry in %s",
			len(p.chains), soonest.Round(time.Millisecond))
	}
	cl, err := best.get(ctx)
	if err != nil {
		return nil, nil, err
	}
	best.addActive()
	return cl, best, nil
}

// close 关闭全部链的当前 client(级联关全部跳)。链本身保留,后续
// acquire 会懒重建——close 是「断开」不是「销毁」。
func (p *pool) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.chains {
		c.mu.Lock()
		if c.client != nil {
			_ = c.client.Close()
			c.client = nil
		}
		c.mu.Unlock()
	}
}
