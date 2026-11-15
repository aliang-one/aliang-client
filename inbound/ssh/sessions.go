// sessions.go — 接入客户端会话台账（客户端连接状态功能，服务端侧）：
// 每条 SSH 入口连接在握手成功后登记一条 session，断开时落关闭时间并转入
// 已关闭环形历史（定长）。服务端仪表盘据此展示「谁接进来了」（来源 IP、
// 凭证形态 bare/mesh、mesh 对端 device_id、认证/接入/最近活跃时间、活跃
// 通道数）。只观测不参与决策——登记/续期/关闭都由 server.go 的连接生命
// 周期驱动，台账自身无 IO、无网络，纯内存。
package ssh

import (
	"sync"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// sessionHistoryCap 是已关闭会话环形历史的容量（超出挤掉最旧的）。
// 与连接明细（statistic 300 条）同量级：状态页轮询 5s，300 条足够覆盖
// 「刚断开谁」的排查窗口。
const sessionHistoryCap = 300

// Session 是一条已认证 SSH 入口连接的运行态快照（JSON 形态即 API 载荷）。
// 写点：握手成功后的登记点一次性写入身份字段；LastSeen/Channels 由通道
// 生命周期续期；ClosedAt/Duration 在拆除点落定。读侧拿拷贝。
type Session struct {
	SourceIP    string `json:"source_ip"`       // 客户端来源 IP（连接层 RemoteAddr）
	User        string `json:"user"`            // 客户端自报的 SSH 用户名（审计参考）
	Form        string `json:"form"`            // 凭证形态：bare=人用凭证 / mesh=组网节点
	DeviceID    string `json:"device_id"`       // mesh 形态的对端节点标识（bare 恒空）
	Fingerprint string `json:"fingerprint"`     // 客户端公钥 SHA256 指纹（"SHA256:xxx"，踢出/拉黑的公共身份）
	AuthAt      int64  `json:"auth_at_unix"`    // 认证成功时刻（Unix 秒）
	LastSeen    int64  `json:"last_seen_unix"`  // 最近一次通道活动时刻（Unix 秒）
	Channels    int64  `json:"active_channels"` // 当前打开的通道数
	Duration    int64  `json:"duration_millis"` // 在线时=至今；已关闭=关闭-认证（毫秒）
	ClosedAt    int64  `json:"closed_at_unix"`  // 拆除时刻（Unix 秒）；0=仍在线
	Kicked      bool   `json:"kicked"`          // 被管理端踢出/黑名单补拦而断（true 时非自然掉线）
}

// sshSession 是台账里的可变运行态实体：store 回指登记它的台账（句柄方法
// 经它落锁）；值拷贝（.Session）即对外快照。零值句柄（nil store）全部
// 方法安全——测试替身可直接构造 sshSession{} 使用。
type sshSession struct {
	Session
	store   *sessionStore
	blockFn func(BlockQuery) bool // 活视图黑名单查询（Server.blockedNow）；nil=不判
	sshConn gossh.Conn            // 底层 SSH 连接（登记时写入；踢出/替换时关闭）
}

// sessionStore 是一个入口 Server 的进程内会话台账。全部读写走 st.mu；
// 热路径（通道开/关）只有 map 定位+字段读写，无 IO。
type sessionStore struct {
	mu     sync.Mutex
	active map[*sshSession]*sshSession // 在线会话（指针即 key，天然幂等）
	closed []Session                   // 已关闭环形历史（新→旧）
}

func newSessionStore() *sessionStore {
	return &sessionStore{active: make(map[*sshSession]*sshSession)}
}

// register 登记一条已认证连接并返回会话句柄。meta 取握手元数据（User/
// RemoteAddr），form/deviceID/fingerprint 取认证 Permissions（与
// serveChannels 的观测面同源同值）。conns 是底层 SSH 连接（踢出/同节点
// 替换时关闭它——关闭解除 serveChannels 阻塞，走 defer sess.close() 的
// 正常拆除路径落关闭历史）。blockFn 是黑名单活视图（Server.blockedNow）。
//
// 同一 mesh 节点（同 device_id）重连而旧连接未断时替换旧会话：旧连接关闭
// （Kicked=true，客户端表现为连接被断；防同节点双连接的路由抖动）。
func (st *sessionStore) register(meta gossh.ConnMetadata, form, deviceID, fingerprint string, blockFn func(BlockQuery) bool, conns gossh.Conn) *sshSession {
	if st == nil {
		return nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now().Unix()
	if form == FormMesh && deviceID != "" {
		for old := range st.active {
			s := st.active[old]
			if s.Form == FormMesh && s.DeviceID == deviceID {
				s.Kicked = true
				s.markClosedLocked(now)
				if old.sshConn != nil {
					_ = old.sshConn.Close()
				}
				delete(st.active, old)
				break // device_id 唯一（公钥层已防重复），替换一处即止
			}
		}
	}
	sess := &sshSession{Session: Session{
		SourceIP:    remoteIP(meta.RemoteAddr()),
		User:        meta.User(),
		Form:        form,
		DeviceID:    deviceID,
		Fingerprint: fingerprint,
		AuthAt:      now,
		LastSeen:    now,
	}, store: st, blockFn: blockFn, sshConn: conns}
	st.active[sess] = sess
	return sess
}

// openChannel 通道打开：活跃通道计数+1 并续期最近活跃时间。
func (sess *sshSession) openChannel() {
	if sess == nil || sess.store == nil {
		return
	}
	st := sess.store
	st.mu.Lock()
	if s, ok := st.active[sess]; ok {
		s.Channels++
		s.LastSeen = time.Now().Unix()
	}
	st.mu.Unlock()
}

// closeChannel 通道关闭：活跃通道计数-1（防负）并续期最近活跃时间。
func (sess *sshSession) closeChannel() {
	if sess == nil || sess.store == nil {
		return
	}
	st := sess.store
	st.mu.Lock()
	if s, ok := st.active[sess]; ok {
		if s.Channels > 0 {
			s.Channels--
		}
		s.LastSeen = time.Now().Unix()
	}
	st.mu.Unlock()
}

// close 拆除会话：从在线表移除、落关闭时刻与时长、转入环形历史头。
// 未登记/已拆除的句柄是 no-op（句柄可能来自已被 Stop 清场的实例）。
func (sess *sshSession) close() {
	if sess == nil || sess.store == nil {
		return
	}
	st := sess.store
	st.mu.Lock()
	if s, ok := st.active[sess]; ok {
		delete(st.active, sess)
		s.markClosedLocked(time.Now().Unix())
	}
	st.mu.Unlock()
}

// markClosedLocked 落关闭终态并转入环形历史头。调用方须持 st.mu；会话须
// 已从在线表移除（或由调用方随后移除）。Kicked 已被置位的（踢出/替换路径
// 预标记）保持不变——自然掉线不会覆盖踢出标记。
func (s *sshSession) markClosedLocked(now int64) {
	s.ClosedAt = now
	s.Duration = (now - s.AuthAt) * 1000 // 秒→毫秒
	if s.Duration < 0 {
		s.Duration = 0
	}
	s.Channels = 0
	s.store.closed = append([]Session{s.Session}, s.store.closed...)
	if len(s.store.closed) > sessionHistoryCap {
		s.store.closed = s.store.closed[:sessionHistoryCap]
	}
}

// Kick 管理端踢出：置 Kicked 标记、落关闭历史并断开底层 SSH 连接（通道
// goroutine 随之解除阻塞走正常拆除路径）。目标须在线（按 fingerprint 精确
// 匹配；空指纹不操作）。返回是否确实断开了一条在线会话。
func (st *sessionStore) Kick(fingerprint string) bool {
	if st == nil || fingerprint == "" {
		return false
	}
	st.mu.Lock()
	var victim *sshSession
	for s := range st.active {
		if s.Fingerprint == fingerprint {
			victim = s
			break
		}
	}
	if victim == nil {
		st.mu.Unlock()
		return false
	}
	victim.Kicked = true
	victim.markClosedLocked(time.Now().Unix())
	conn := victim.sshConn
	delete(st.active, victim)
	st.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	return true
}

// liveDurationMillis 在线时长快照：在线=至今；已关闭=拆除-认证。
func (sess *sshSession) liveDurationMillis() int64 {
	if sess == nil {
		return 0
	}
	if sess.ClosedAt > 0 {
		return sess.Duration
	}
	d := (time.Now().Unix() - sess.AuthAt) * 1000
	if d < 0 {
		return 0
	}
	return d
}

// ActiveSessions 返回在线会话快照（按认证时间新→旧）。nil store / 空 →
// 空数组（恒非 nil，API 序列化不出现 null）。
func (st *sessionStore) ActiveSessions() []Session {
	out := make([]Session, 0)
	if st == nil {
		return out
	}
	st.mu.Lock()
	for _, s := range st.active {
		snap := s.Session
		snap.Duration = s.liveDurationMillis()
		out = append(out, snap)
	}
	st.mu.Unlock()
	// 新→旧：AuthAt 降序（插入排序——数量小、近乎有序时近 O(n)）
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].AuthAt > out[j-1].AuthAt; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ClosedSessions 返回已关闭环形历史快照（新→旧）。恒非 nil。
func (st *sessionStore) ClosedSessions() []Session {
	out := make([]Session, 0)
	if st == nil {
		return out
	}
	st.mu.Lock()
	out = append(out, st.closed...)
	st.mu.Unlock()
	return out
}
