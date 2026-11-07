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
	client    *cascadeClient
	clientErr error // 监控协程在 conn 死亡时写入;非 nil 即死
	lastFail  time.Time
	failCount int
	active    atomic.Int64 // 当前经此链打开的 channel 数;原子:选链读与释放写并发
}

func newChain(dialFn func(ctx context.Context) (*cascadeClient, error)) *chain {
	return &chain{dialFn: dialFn}
}

// get 返回存活 client;死了/没建则重建(受退避约束)。
// 同一条链的重建互斥(全程持 c.mu),并发 get 后到者直接复用新 client。
// 监控只看最终跳的 Wait——前跳死亡会级联拆掉最终跳传输,足够触发重建
//(spec 审查已确认此取舍)。
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
	if !c.lastFail.IsZero() {
		if wait := chainBackoff(c.failCount); time.Since(c.lastFail) < wait {
			return nil, fmt.Errorf("ssh chain rebuilding, backoff %s", wait)
		}
	}
	cl, err := c.dialFn(ctx)
	if err != nil {
		c.lastFail = time.Now()
		c.failCount++
		return nil, err
	}
	c.client = cl
	c.clientErr = nil
	c.failCount = 0
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

// drop 收走死链(仅当当前 client 已被标记死亡)。
func (c *chain) drop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil && c.clientErr != nil {
		_ = c.client.Close()
		c.client = nil
	}
}

func (c *chain) addActive() { c.active.Add(1) }

// releaseActive 与骨架的「>0 才减」语义等价:CAS 循环保证不落到负值
//(负值会让该链在最少活跃选择中被永久偏选)。
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

// pool 是 N 条独立链的集合;每次 acquire 选活跃 channel 数最少的链
//(同分从 next 轮转起点扫,保证均匀)。已知取舍(与设计文档一致):
//   - 选链在 p.mu 内快照即出,建链在 chain.mu 内——一次慢拨号(握手至多
//     15s)不锁住其余链的 acquire,只有同一条链的重建互斥;
//   - 没有「全部断时现建救急链」的特设机制:语义等价于任一链退避期满
//     即重建(懒),acquire 撞上退避窗口就返回错误,由调用方决定重试;
//   - 选链只看活跃数不看健康度:死链退避期中仍可能被选中,此时 acquire
//     报错而不是静默换链(多链 failover 不在本池承诺内)。
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

// acquire 选活跃数最少的链并取得 client;被选链处于退避窗口时返回错误。
// 返回的 chain 供调用方在 channel 关闭时 releaseActive 配对减数。
// 锁序恒为 p.mu → chain.mu(选链持 p.mu 时只做原子读,不嵌套取 chain.mu),
// get/addActive/close 路径只取 chain.mu,无反向嵌套。
func (p *pool) acquire(ctx context.Context) (*cascadeClient, *chain, error) {
	p.mu.Lock()
	var best *chain
	for i := 0; i < len(p.chains); i++ {
		idx := (p.next + i) % len(p.chains)
		c := p.chains[idx]
		if best == nil || c.active.Load() < best.active.Load() {
			best = c
		}
	}
	p.next = (p.next + 1) % len(p.chains)
	p.mu.Unlock()
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
