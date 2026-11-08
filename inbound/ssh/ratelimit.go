package ssh

import (
	"sync"
	"time"
)

// 认证失败退避参数:窗口 = 1s << min(fails, 5),封顶 30s
// (fails=1→2s,2→4s,3→8s,4→16s,5+→30s)。
const (
	authBackoffBase     = time.Second
	authBackoffCap      = 30 * time.Second
	authBackoffMaxShift = 5

	// limiterIdleTTL 是失败条目的闲置驱逐时限:距上次失败超过(严格 >)
	// 该时长的条目视为过期——fails 衰减归零。防止攻击停止数小时后,
	// 源 IP 仍被 30s 退避帽永久限速。
	limiterIdleTTL = time.Hour

	// limiterMaxEntries 是失败条目数上限:面向互联网的入口防分布式扫描
	// 撑出无界 map。新增条目遇上限时先整表清扫过期条目腾位,仍满则丢弃
	// 本次记账(该 IP 暂退化为不限速,换取内存有界;真实扫描潮会持续
	// 制造可被后续清扫的过期条目,退化窗口有限)。
	limiterMaxEntries = 4096
)

// authLimiter 按远端 IP 记录连续认证失败并施加指数退避窗口:窗口未过期时
// Allow 拒绝新连接(服务端在握手前直接断开);认证成功 Reset 清零;
// 超过闲置 TTL 的条目在 Allow 命中时驱逐(fails 衰减归零)。
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

// Allow 报告 ip 当前是否允许发起连接。退避窗口过期后放行(fails 保留,
// 再失败则窗口继续增长——真正的指数退避);条目闲置超过 limiterIdleTTL
// 则驱逐并放行(fails 衰减归零)。
func (l *authLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[ip]
	if !ok || e.fails == 0 {
		return true
	}
	now := l.now()
	// 闲置驱逐:过期条目视为陈旧告警,删除后按新面孔放行。
	if now.Sub(e.lastFail) > limiterIdleTTL {
		delete(l.entries, ip)
		return true
	}
	return now.Sub(e.lastFail) >= backoffWindow(e.fails)
}

// Fail 记录一次认证失败。已有条目直接更新(不增长 map);新条目在
// 条目数达上限时先清扫过期腾位,仍满则丢弃记账(内存有界取舍,
// 见 limiterMaxEntries)。
func (l *authLimiter) Fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.entries[ip]; e != nil {
		e.fails++
		e.lastFail = l.now()
		return
	}
	if len(l.entries) >= limiterMaxEntries {
		l.sweepExpiredLocked()
		if len(l.entries) >= limiterMaxEntries {
			return
		}
	}
	l.entries[ip] = &authLimitEntry{fails: 1, lastFail: l.now()}
}

// sweepExpiredLocked 整表清扫超过闲置 TTL 的条目(Fail 容量满时腾位;
// 调用方持锁)。
func (l *authLimiter) sweepExpiredLocked() {
	now := l.now()
	for ip, e := range l.entries {
		if now.Sub(e.lastFail) > limiterIdleTTL {
			delete(l.entries, ip)
		}
	}
}

// Reset 清零 ip 的失败计数(认证成功调用)。
func (l *authLimiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, ip)
}
