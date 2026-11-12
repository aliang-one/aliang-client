// session.go 承载载体会话缓存:A 侧打洞打通一次(DialPeer)后,把
// PunchSession 按 peer 维度缓存,跨 SSH 拨号复用——同 peer 的后续拨号各
// 开一条新流(B 侧免门契约:listener.punchServeConn 首流过门后其余流直通),
// 免逐拨重打洞。
//
// 与 SPEC §5.4 禁的「SSH client 连接池」是两层物(评审拍板记录,此处显式
// 辨析):本缓存缓存的是**载体**(QUIC 会话——打洞打通的那条到对端
// listener 的已过门通道),不是 SSH client。每条 SSH 拨号仍是一次独立
// 握手、独立 gossh.Client、独立流,SPEC §5.4 的「单连接单 SSH client,
// ExitClient 可缓存而 gossh.Client 不复用」原封不动;缓存省掉的是第二次
// 拨号的打洞+QUIC 握手,不是 SSH 握手。
//
// 会话寿命的现实约束:B 侧 listener TTL=90s 收摊 → 缓存会话有 ≤90s 硬寿命
// (端点短命是刻意安全姿态)。因此缓存收益=窗口内免重打洞,**死会话检测
// 是主路径**:Acquire 撞死会话(检测用 Conn.Context().Done(),Task 7 实证
// quic-go 把连接关闭错误挂在该 Context 的 cancel-cause 上)→驱逐(关
// Transport)+返回未建立,上层现场重拨(DialAndPut 已把这条链封成一
// 函数)。空闲回收(5min)只负责资源整洁面,不是真围栏——无人取用的
// 表项迟早随 B 侧 TTL 死掉,由下一次缓存操作的惰性回收扫走。
//
// 死亡可观测性的边界(Task 8 实测):可观测的死法(对端对单条会话
// CloseWithError/门禁拒单)会真发 CONNECTION_CLOSE,A 侧 Done 立即关闭;
// 但 B 侧 TTL 收摊走的 Transport.Close 是**静默关停**(quic-go 语义:不
// 发 CONNECTION_CLOSE 给对端),A 侧要等自身空闲超时(默认 ~30s;缓存
// 路径的 15s 保活包石沉大海同样计入无应答)才观察到死亡。这段僵尸窗口
// 内 Acquire 仍会放行会话,首次读写撞黑洞——由上层拨号围栏兜底(SSH
// 拨号本就带 deadline),缓存只对「已可观测的死亡」负责驱逐。
//
// 分工边界:本文件不做 STUN/信令/物理接口绑定(编排层),conn 的单
// Transport 铁律仍归调用方(dialer.go 文件头)——缓存只保证「同 peer 先
// 驱逐后重拨」的顺序面,同 conn 并发重拨的串行化是调用方的事。
package punch

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

// punchSessionIdleTimeout 缓存会话的空闲回收时长:ref 归零起持续本时长
// 无人取用,即关会话收摊。5min 是常量非配置项(YAGNI,与 stun.go
// skipMemoTTL 同族):真围栏是 B 侧 listener 的 90s TTL(见文件头),空闲
// 回收只负责把「其实早死了/没人再用」的表项干净利落地扫掉。
const punchSessionIdleTimeout = 5 * time.Minute

// punchKeepAlivePeriod 缓存会话的 QUIC 保活周期:15s≈常见 NAT UDP 映射
// 时限(30~60s)的一半,持续发包刷映射,让两次取用的间隙洞不塌。
//
// 「保活只为在飞连接服务」不变式(评审拍板):保活挂在拨号时的
// quic.Config 上随会话生灭——会话关(空闲回收/死会话驱逐)保活自然停,
// 不存在脱离会话的保活。与空闲回收的协作:保活让会话活到空闲窗到点,
// 回收关会话即停保活;一次性拨号(不进缓存)根本不带保活(DialOpts.
// KeepAlive=false 时 DialPeer 不传 quic.Config)。
const punchKeepAlivePeriod = 15 * time.Second

// punchDialQUICConfig 由 DialOpts 组装传给 Transport.Dial 的 quic.Config:
// 唯一的覆盖项是缓存路径的保活(KeepAlivePeriod);KeepAlive=false 返回
// nil 走 quic-go 全默认——刻意不夹带其他覆盖项,非缓存路径的行为面与
// 未传 Config 时完全一致。
func punchDialQUICConfig(opts DialOpts) *quic.Config {
	if !opts.KeepAlive {
		return nil
	}
	return &quic.Config{KeepAlivePeriod: punchKeepAlivePeriod}
}

// SessionCache 是 per-peer 的载体会话缓存(见文件头与 SPEC §5.4 辨析)。
// 并发安全;now/dial 两个注入点仅供同包测试(clock 先例=stun.go SkipMemo.
// now),生产一律经 NewSessionCache 走默认实现。零字段即默认态。
type SessionCache struct {
	mu       sync.Mutex
	now      func() time.Time                                    // 时钟注入:测试推进空闲计时不真睡 5min
	dial     func(*net.UDPConn, DialOpts) (*PunchSession, error) // 拨号注入:测试替换;零值走 DialPeer
	sessions map[string]*cachedSession
}

// NewSessionCache 建缓存:生产唯一入口(注入点是同包测试直填字段)。
func NewSessionCache() *SessionCache {
	return &SessionCache{}
}

// clock 惰性取时钟(SkipMemo.clock 同款):未注入走 time.Now。
func (c *SessionCache) clock() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

// dialFn 惰性取拨号函数:未注入走 DialPeer。
func (c *SessionCache) dialFn() func(*net.UDPConn, DialOpts) (*PunchSession, error) {
	if c.dial == nil {
		return DialPeer
	}
	return c.dial
}

// cachedSession 是一个缓存表项:会话+引用计数+空闲起点。Lease 持有的是
// 表项指针而非表槽——表槽可被顶替(并发双拨/死会话驱逐),在飞 Lease
// 照常工作,其最后的 Release 归零时发现已被顶替即当场自关(见 Release)。
type cachedSession struct {
	session   *PunchSession
	refs      int
	idleSince time.Time // ref 归零、空闲计时起点;零值=有 ref 在飞
}

// closeLocked 收摊表项会话:Transport.Close(quic-go 语义:不关调用方
// conn——conn 所有权按 DialPeer 契约归调用方体系;abrupt 终止本 Transport
// 全部会话,正是「会话死=连接死」的收场动作)。幂等,死会话再关无妨。
func (e *cachedSession) closeLocked() {
	if e.session == nil || e.session.Transport == nil {
		return
	}
	_ = e.session.Transport.Close()
}

// punchSessionDead 判会话是否已死:Done 关即死(Task 7 实证,任何死因——
// B 侧 TTL 收摊/对端关闭/本侧收摊——都走同一条 Done,无需读 cause)。
// 残缺会话(nil Conn)按死处理,给驱逐路径兜底。
func punchSessionDead(s *PunchSession) bool {
	if s == nil || s.Conn == nil {
		return true
	}
	select {
	case <-s.Conn.Context().Done():
		return true
	default:
		return false
	}
}

// Acquire 取 peer 的活会话并占一个引用:命中即复用(ref++,清空闲计时);
// 未命中(无表项/死会话/空闲超时)返回 nil=「未建立」,上层现场拨号
// (DialAndPut 已封装该链)。撞死会话是主路径(B 侧 90s 硬寿命):驱逐
// (关 Transport+删表)——即使仍有在飞 Lease 持有它也不碍事(表项指针
// 语义见 cachedSession),持有者随后 OpenStream 会撞上会话死亡的流错误,
// 其 Release 归零时自关收尾。
func (c *SessionCache) Acquire(peer string) *Lease {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reapIdleLocked()
	entry, ok := c.sessions[peer]
	if !ok {
		return nil
	}
	if punchSessionDead(entry.session) {
		entry.closeLocked()
		delete(c.sessions, peer)
		return nil
	}
	entry.refs++
	entry.idleSince = time.Time{}
	return &Lease{cache: c, peer: peer, entry: entry}
}

// Put 把 DialPeer 打通的会话入缓存,返回持有 1 个引用的 Lease(标准链:
// 拨号成功→Put→用→Release)。残缺会话拒收返回 nil:peer 为空、session
// 为 nil、或 session 的 Transport/Conn 为 nil(不查 Stream——缓存的流面
// 全走 Lease.OpenStream,首流闲置无害)——那是编程错误,宁可让调用方在
// nil 上炸出来也不入表。表里已有同 peer 旧表项时:无人引用(空闲/已死)
// →关旧让位;仍被引用(并发双拨的先到者)→不动旧持有者,新表项顶替表
// 槽,旧表项成孤儿,其最后的 Release 归零时当场自关——不泄漏也不挂死。
func (c *SessionCache) Put(peer string, session *PunchSession) *Lease {
	if peer == "" || session == nil || session.Conn == nil || session.Transport == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessions == nil {
		c.sessions = make(map[string]*cachedSession)
	}
	if old, ok := c.sessions[peer]; ok && old.refs == 0 {
		// 旧表项无人引用:关掉让位(死会话再关幂等)。
		// refs>0 的分支刻意不动作:见方法注释的孤儿语义。
		old.closeLocked()
	}
	entry := &cachedSession{session: session, refs: 1}
	c.sessions[peer] = entry
	return &Lease{cache: c, peer: peer, entry: entry}
}

// DialAndPut 封装缓存路径全链(Task 9 的 PunchDialFunc 形状):先抢缓存
// (并发双拨的败者在此收敛成复用),未命中才现场 DialPeer,成功即 Put。
// 拨号 opts 强制置真 KeepAlive——缓存会话要在取用间隙存活等 NAT 映射,
// 保活是缓存路径的义务而非调用方的记忆负担(不变式见 punchKeepAlivePeriod)。
// conn 原样转交拨号函数;同 conn 的单 Transport 铁律与拨号串行化仍归
// 调用方(见文件头)。
func (c *SessionCache) DialAndPut(peer string, conn *net.UDPConn, opts DialOpts) (*Lease, error) {
	if lease := c.Acquire(peer); lease != nil {
		return lease, nil
	}
	opts.KeepAlive = true
	session, err := c.dialFn()(conn, opts)
	if err != nil {
		return nil, err
	}
	lease := c.Put(peer, session)
	if lease == nil {
		// dial 成功却入表被拒=会话残缺(理论不可达,DialPeer 出厂件齐全):
		// 别泄漏,当场收摊并报错。
		_ = session.Transport.Close()
		return nil, errors.New("punch cache: 拨号成功但会话残缺,入缓存被拒")
	}
	return lease, nil
}

// reapIdleLocked 惰性回收(SkipMemo 先例,无后台 goroutine):只在
// Acquire(含 DialAndPut 的先抢一步)取用前顺带扫全表,ref=0 且(已死
// 或空闲超时)的表项关会话删表;Put/Release 不触发扫描——取用路径密度
// 足够,无需每写必扫。真围栏是 B 侧 90s TTL(见文件头),这里只负责
// 整洁;refs>0 的表项绝不动(在飞连接不回收)。
func (c *SessionCache) reapIdleLocked() {
	now := c.clock()
	for peer, entry := range c.sessions {
		if entry.refs > 0 {
			continue
		}
		expired := !entry.idleSince.IsZero() && !now.Before(entry.idleSince.Add(punchSessionIdleTimeout))
		if punchSessionDead(entry.session) || expired {
			entry.closeLocked()
			delete(c.sessions, peer)
		}
	}
}

// Lease 是一次取用的凭证:持有期间会话不会被空闲回收。OpenStream 开新流
// (免门契约下的裸流),Release 归还引用;Release 幂等(同一 Lease 二次
// Release 是 no-op),零值/Release 后的 Lease 再用 OpenStream 报错。
type Lease struct {
	cache *SessionCache
	peer  string
	entry *cachedSession
}

// OpenStream 在缓存会话上开一条新流并包成 net.Conn(复用包内
// punchStreamConn):每条流=一条独立上层「连接」的载体,B 侧过门后免门
// 直通;ctx 管开流围栏。会话已死时错误由 quic-go 开流面透传。
// entry 经锁内快照取得(与并发 Release 的置 nil 互斥);开流本身不持
// 缓存锁——那是可能阻塞的网络操作,不得拖住全缓存。
func (l *Lease) OpenStream(ctx context.Context) (net.Conn, error) {
	if l == nil {
		return nil, errors.New("punch cache lease: 未建立或已释放")
	}
	c := l.cache
	c.mu.Lock()
	entry := l.entry
	c.mu.Unlock()
	if entry == nil {
		return nil, errors.New("punch cache lease: 未建立或已释放")
	}
	stream, err := entry.session.Conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return &punchStreamConn{stream: stream, conn: entry.session.Conn}, nil
}

// Release 归还引用:ref--;归零起空闲计时(到点由下次缓存操作惰性回收)。
// 幂等是并发硬语义(Task 9 的 defer 清理+连接关闭双触发是自然写法):
// 同一 Lease 的并发/重复 Release 只生效一次——不多退 ref、不重置空闲
// 计时、不 panic。实现上锁外只留 nil Lease 检查,快照+置 nil 都在锁内,
// 后到者拿到 nil 快照即吞掉(锁外读 entry 会与锁内置 nil 成对竞态,
// 并发双过检查后进锁即 nil 解引用,规格审查实锤)。
// 若本表项已被顶替(并发双拨孤儿/死会话驱逐后的残留),不会再有人来取
// 它——归零当场关会话收尾,不留无主会话。
func (l *Lease) Release() {
	if l == nil {
		return
	}
	c := l.cache
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := l.entry
	l.entry = nil // 幂等锚点:锁内置空,并发二次 Release 在下面的 nil 检查吞掉
	if entry == nil {
		return
	}
	entry.refs--
	if entry.refs > 0 {
		return
	}
	if cur, ok := c.sessions[l.peer]; !ok || cur != entry {
		// 孤儿表项(表槽已被顶替/驱逐):无人再来取,当场自关。
		entry.closeLocked()
		return
	}
	entry.idleSince = c.clock()
}
