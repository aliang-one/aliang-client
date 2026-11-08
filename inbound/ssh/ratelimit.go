package ssh

import (
	"sync"
	"time"
)

// 认证失败退避参数:窗口 = 1s << min(fails, 5),封顶 30s
// (fails=1→2s,2→4s,3→8s,4→16s,5+→30s)。
const (
	authBackoffBase    = time.Second
	authBackoffCap     = 30 * time.Second
	authBackoffMaxShift = 5
)

// authLimiter 按远端 IP 记录连续认证失败并施加指数退避窗口:窗口未过期时
// Allow 拒绝新连接(服务端在握手前直接断开);认证成功 Reset 清零。
// 并发安全(单互斥锁;认证级操作频率低,锁竞争可忽略)。
type authLimiter struct {
	mu      sync.Mutex
	entries map[string]*authLimitEntry
	// now 供测试注入假时钟;生产为 time.Now。
	now func() time.Time
}

type authLimitEntry struct {
	fails    int       // 连续失败次数(窗口过期不衰减,仅成功认证清零)
	lastFail time.Time // 最近一次失败时间
}

func newAuthLimiter() *authLimiter {
	return &authLimiter{entries: make(map[string]*authLimitEntry), now: time.Now}
}

// backoffWindow 返回 fails 次失败后的封禁窗口时长。
func backoffWindow(fails int) time.Duration {
	shift := fails
	if shift > authBackoffMaxShift {
		shift = authBackoffMaxShift
	}
	w := authBackoffBase << shift
	if w <= 0 || w > authBackoffCap { // <=0 防御极端 fails 溢出
		return authBackoffCap
	}
	return w
}

// Allow 报告 ip 当前是否允许发起连接。窗口过期后放行(fails 保留,
// 再失败则窗口继续增长——真正的指数退避)。
func (l *authLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[ip]
	if !ok || e.fails == 0 {
		return true
	}
	return l.now().Sub(e.lastFail) >= backoffWindow(e.fails)
}

// Fail 记录一次认证失败。
func (l *authLimiter) Fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[ip]
	if e == nil {
		e = &authLimitEntry{}
		l.entries[ip] = e
	}
	e.fails++
	e.lastFail = l.now()
}

// Reset 清零 ip 的失败计数(认证成功调用)。
func (l *authLimiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, ip)
}
