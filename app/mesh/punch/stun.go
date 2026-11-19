// stun.go 承载打洞前的公网自省三件套:STUN Binding 地址发现(Discover)、
// 双服务器端口比对判对称 NAT(Classify)、per-peer 对称失败记忆(SkipMemo)。
// 所有探测都经调用方传入的打洞 socket 发出——那是 NewBoundUDPConn 出厂即
// 绑物理接口的 socket,学到的公网映射必须属于这张卡;若在函数内部另建
// socket,TUN-on 下学到的是引擎换发 socket 的映射,洞会打错 5-tuple
// (与 socket.go:26 的绑定铁律同源,这是 conn 作参数的原因)。
// 默认服务器档已迁 processor/config.MeshDefaultStunServers(分层:processor
// 不得反向依赖 app);本包只做 Discover/Classify 原语,服务器列表由编排层
// 经参数传入。

package punch

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/pion/stun"
)

// discoverAttempts 单次 Discover 在总预算内的发包次数(首发+2 次重传)。
// UDP 会丢包,单发太脆;次数写死小常数,预算仍由 timeout 参数封顶。
const discoverAttempts = 3

// classifyDiscoverTimeout Classify 内单台 Discover 的预算。包变量而非
// 配置项——仅为同包测试能调小避免真等 3s,生产语义是常量。
var classifyDiscoverTimeout = 3 * time.Second

// PublicMapping 是 STUN 学到的公网映射(NAT 为本 socket 分配的外部
// ip:port),打洞时把它交换给对端做发包目标。
type PublicMapping struct {
	IP   net.IP
	Port int
}

// bindingSuccess Binding 成功应答的消息类型(方法 Binding+类 Success
// Response),用于核对应答形态。
var bindingSuccess = stun.NewType(stun.MethodBinding, stun.ClassSuccessResponse)

// Discover 经 conn 向 server 发 STUN Binding request,返回应答宣告的
// 公网映射;timeout 封顶整轮探测(含重传,预算按剩余次数均摊)。
//
// 为什么不用 pion/stun 的 stun.Client 驱动:它会在传入 conn 上装一个
// 常驻读 goroutine,Close 后仍阻塞在 Read 里——打洞 socket 后续要收
// 对端流量,残留读者会偷走打洞报文。这里只用 pion/stun 的编解码原语
// (Build/Decode/XORMappedAddress)手工驱动事务:WriteToUDP 显式发向
// server、ReadFromUDP 带 deadline 收,返回前清零 deadline,不留痕迹。
//
// 应答核验三道:来源地址==解析出的 server 地址;事务 ID 与请求一致
// (96 位随机数即 RFC 5389 的应答真实性凭据);消息类型为 Binding 成功
// 应答。地址属性先取 XOR-MAPPED-ADDRESS,老式服务器回退 MAPPED-ADDRESS。
// 注意:Discover 会临时设置 conn 的读写 deadline 并在返回前清零,调用方
// 在探测阶段不得依赖 conn 上的既有 deadline。
func Discover(conn *net.UDPConn, server string, timeout time.Duration) (PublicMapping, error) {
	if conn == nil {
		return PublicMapping{}, errors.New("stun discover: conn 为 nil")
	}
	if timeout <= 0 {
		return PublicMapping{}, fmt.Errorf("stun discover: timeout 须为正数,得到 %v", timeout)
	}
	// server 域名解析走系统 resolver(不经绑定接口的 socket),v1 接受——
	// STUN 探测包本身仍从绑定 socket 发出,映射归属不受影响。
	raddr, err := net.ResolveUDPAddr("udp4", server)
	if err != nil {
		return PublicMapping{}, fmt.Errorf("stun discover: resolve %s: %w", server, err)
	}
	defer func() {
		// 清零 deadline 是「不留痕迹」的一半;写 deadline 一并清。
		_ = conn.SetReadDeadline(time.Time{})
		_ = conn.SetWriteDeadline(time.Time{})
	}()

	deadline := time.Now().Add(timeout)
	var (
		req = stun.New()
		msg stun.Message
		buf = make([]byte, 1500)
	)
	for left := discoverAttempts; left > 0; left-- {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		budget := remaining / time.Duration(left)
		// 每次重传换新事务 ID:旧 ID 的迟到应答只配被丢弃。
		if err := req.Build(
			stun.BindingRequest,
			stun.NewTransactionIDSetter(stun.NewTransactionID()),
		); err != nil {
			return PublicMapping{}, fmt.Errorf("stun discover: build request: %w", err)
		}
		if err := conn.SetWriteDeadline(time.Now().Add(budget)); err != nil {
			return PublicMapping{}, fmt.Errorf("stun discover: set write deadline: %w", err)
		}
		if _, err := conn.WriteToUDP(req.Raw, raddr); err != nil {
			return PublicMapping{}, fmt.Errorf("stun discover: write to %s: %w", server, err)
		}
		if err := conn.SetReadDeadline(time.Now().Add(budget)); err != nil {
			return PublicMapping{}, fmt.Errorf("stun discover: set read deadline: %w", err)
		}
		for {
			n, src, err := conn.ReadFromUDP(buf)
			if err != nil {
				// 预算耗尽(socket 超时)或 socket 出错:本发作废,重传或收场。
				break
			}
			if !src.IP.Equal(raddr.IP) || src.Port != raddr.Port {
				continue // 别人家的报文(含对端早期打洞包),不消费语义、只跳过
			}
			if !stun.IsMessage(buf[:n]) {
				continue
			}
			if err := stun.Decode(buf[:n], &msg); err != nil {
				continue
			}
			if msg.TransactionID != req.TransactionID || msg.Type != bindingSuccess {
				continue
			}
			var xor stun.XORMappedAddress
			if err := xor.GetFrom(&msg); err == nil {
				return PublicMapping{IP: xor.IP, Port: xor.Port}, nil
			}
			var plain stun.MappedAddress
			if err := plain.GetFrom(&msg); err == nil {
				return PublicMapping{IP: plain.IP, Port: plain.Port}, nil
			}
			// 应答合法但缺地址属性:视同无效,继续等本发余量。
		}
	}
	return PublicMapping{}, fmt.Errorf("stun discover: %s 在 %v 内无有效应答", server, timeout)
}

// stun_probe 逐台探测的判型结论词汇(NAT 三态):symmetric=任两台应答端口
// 不同;non_symmetric=多台应答且端口一致(端口比对成立、非对称);unknown=
// 恰一台应答,端口比对不成立(降级不冤枉,真对称由 SkipMemo 失败记忆兜底)。
// 不可达台 NatType 留空——学不到映射就无从判型,空串即诚实。
const (
	NatTypeSymmetric    = "symmetric"
	NatTypeNonSymmetric = "non_symmetric"
	NatTypeUnknown      = "unknown"
)

// StunProbeEntry ClassifyWithBudget 的单台探测记录:server/应答与否/学到的
// 映射端口/判型结论/失败原因。
type StunProbeEntry struct {
	Server string
	OK     bool
	// MappedIP 仅供 Classify 薄壳回译 PublicMapping(编排层要 IP:Port 组自
	// 候选),刻意 json:"-" 冻结「不进诊断输出」意图——未来谁直接 marshal
	// 本结构也不会把本机公网 IP 以 base64 形态外泄(诊断消费方按端口比对
	// 读结论,IP 外泄无增益)。
	MappedIP   net.IP `json:"-"`
	MappedPort int
	NatType    string
	Err        string
}

// ClassifyResult ClassifyWithBudget 的产出:per-server 结果 + truncated
// (结果不完整:总预算耗尽,或对称判实即停后余台未测)+ 对称结论。
// 失败不表达为全局 error——「某台不可达」是诊断的正常结论而非服务端错误
// (逐台 Err 如实透传,照 status_ssh_test_handler 分层结论口径)。
type ClassifyResult struct {
	Results   []StunProbeEntry
	Truncated bool
	Symmetric bool
}

// ClassifyWithBudget 是 Classify 的预算化变体(语义照 Classify,唯一增量是
// 循环内查总预算):逐台 Discover,每台启动前查剩余预算,耗尽即停并标
// truncated(已测台数如实——results 只含已测台);单台预算=
// min(classifyDiscoverTimeout, 剩余)。判 symmetric 的时机与 Classify 同:
// 任两台应答端口不同即判实——判实即停,结论已定,余台不再消耗预算,余下
// 未测台以 truncated 如实标注(不假装完整)。语义单点在本包:诊断端点
// (/api/mesh/stun_probe)与打洞编排(Classify 薄壳)共用同一条循环。
//
// 与 Classify 的返回形态差异:失败不是全局 error 而是逐台 Err 记录;全部
// 不可达也不报错,调用方按 Results 自行判读(「STUN 坏」的判据点=空 memo
// +账本 unreachable,由编排层各自落账,本函数不掺和)。
//
// 防御形态:conn 为 nil 时逐台回 Err 记录不探测(不 panic);servers 为空回
// 空结果。Classify 薄壳在调用前自做同款守卫,保持其既有 error 契约不变。
func ClassifyWithBudget(conn *net.UDPConn, servers []string, totalBudget time.Duration) ClassifyResult {
	result := ClassifyResult{Results: make([]StunProbeEntry, 0, len(servers))}
	if conn == nil {
		for _, server := range servers {
			result.Results = append(result.Results, StunProbeEntry{Server: server, Err: "stun classify: conn 为 nil"})
		}
		return result
	}

	start := time.Now()
	var (
		firstPort int
		haveFirst bool
	)
	for i, server := range servers {
		remaining := totalBudget - time.Since(start)
		if remaining <= 0 {
			// 总预算耗尽:停止,余台未测以 truncated 标注。
			result.Truncated = true
			break
		}
		perServer := classifyDiscoverTimeout
		if remaining < perServer {
			perServer = remaining
		}
		mapping, err := Discover(conn, server, perServer)
		if err != nil {
			result.Results = append(result.Results, StunProbeEntry{Server: server, Err: err.Error()})
			continue
		}
		entry := StunProbeEntry{Server: server, OK: true, MappedIP: mapping.IP, MappedPort: mapping.Port}
		if !haveFirst {
			firstPort, haveFirst = mapping.Port, true
			result.Results = append(result.Results, entry)
			continue
		}
		if mapping.Port != firstPort {
			// 两台都应答且端口不同:对同一 socket,不同目的地址换来不同
			// 外部端口,正是对称 NAT 的签名(与 Classify 同判据)。
			result.Symmetric = true
			result.Results = append(result.Results, entry)
			if i < len(servers)-1 {
				result.Truncated = true // 判实即停,余台未测如实标注
			}
			break
		}
		result.Results = append(result.Results, entry)
	}

	// 判型结论按台回填(应答台共享全局结论;不可达台留空=无从判型)。
	okCount := 0
	for _, e := range result.Results {
		if e.OK {
			okCount++
		}
	}
	nat := ""
	switch {
	case result.Symmetric:
		nat = NatTypeSymmetric
	case okCount >= 2:
		nat = NatTypeNonSymmetric
	case okCount == 1:
		nat = NatTypeUnknown
	}
	for i := range result.Results {
		if result.Results[i].OK {
			result.Results[i].NatType = nat
		}
	}
	return result
}

// Classify 逐台 Discover 做端口比对判对称 NAT(既有编排层契约,返回
// (首台应答映射, symmetricSuspect, error)):任两台应答端口不同即
// symmetricSuspect=true;全部不可达报 error;恰好一台应答则判型降级为非对称
// 返回该映射——端口比对不成立不能冤枉成 symmetric,真对称导致的打洞失败由
// SkipMemo 失败记忆兜底。
//
// 实现自显式活探测批(mesh 诊断 Task 7)起改为 ClassifyWithBudget 薄壳:
// 总预算=台数×单台预算,每台启动前剩余 ≥ 单台预算(min 不降档),与旧实现
// 的逐台 classifyDiscoverTimeout 全额预算逐一等价;判实即停(early return)
// 语义原样保留,探测顺序/判据/早退时机/错误文案全同,调用方零变化。
//
// 判型采用「双服务器端口比对」这一简化式:v1 不做 RFC 5780 完整行为
// 探测(mapping-dependent filtering 等),也不做 Tailscale 式端口预测,
// 两者留待二期。Classify 只判型不落记忆——SkipMemo 按 peer 维度记账,
// 由编排层(Task 5)在判 symmetric 或打洞失败后调 Record。
func Classify(conn *net.UDPConn, servers []string) (PublicMapping, bool, error) {
	if conn == nil {
		return PublicMapping{}, false, errors.New("stun classify: conn 为 nil")
	}
	if len(servers) == 0 {
		return PublicMapping{}, false, errors.New("stun classify: servers 为空")
	}
	res := ClassifyWithBudget(conn, servers, time.Duration(len(servers))*classifyDiscoverTimeout)
	var first *StunProbeEntry
	for i := range res.Results {
		if res.Results[i].OK {
			first = &res.Results[i]
			break
		}
	}
	if first == nil {
		// 全部不可达≠symmetric:按「STUN 不可用」交给调用方落 piko,
		// 且不该写 SkipMemo(写不写由调用方决定,这里只报 error)。
		// 错误文案逐台回拼,与旧实现 errors.Join(各 Discover err)逐字同形。
		errs := make([]error, 0, len(res.Results))
		for _, e := range res.Results {
			if e.Err != "" {
				errs = append(errs, errors.New(e.Err))
			}
		}
		return PublicMapping{}, false, fmt.Errorf("stun classify: 全部 %d 台服务器不可达: %w", len(servers), errors.Join(errs...))
	}
	return PublicMapping{IP: first.MappedIP, Port: first.MappedPort}, res.Symmetric, nil
}

// skipMemoTTL 失败记忆时长:5min 内不再对该 peer 重复判型打洞,过期
// 自动给机会(NAT 环境可能已变)。
const skipMemoTTL = 5 * time.Minute

// SkipMemo 是 per-peer 的对称 NAT 失败记忆:判 symmetric(或打洞失败)后
// Record 一笔,5min 内 ShouldSkip 为 true、过期 false。零值即可用(map 与
// now 均惰性初始化);now 作字段是为测试注入时钟推进 TTL,不真睡 5 分钟。
type SkipMemo struct {
	mu    sync.Mutex
	now   func() time.Time
	until map[string]time.Time // peerID → 解禁时刻
}

func (m *SkipMemo) clock() time.Time {
	if m.now == nil {
		return time.Now()
	}
	return m.now()
}

// Record 记一笔失败(重复 Record 以最新时刻重新计时)并顺手惰性清理
// 过期条目,避免记忆表只增不减。
func (m *SkipMemo) Record(peerID string) {
	if peerID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.until == nil {
		m.until = make(map[string]time.Time)
	}
	now := m.clock()
	m.until[peerID] = now.Add(skipMemoTTL)
	for id, t := range m.until {
		if !now.Before(t) {
			delete(m.until, id)
		}
	}
}

// ShouldSkip 报告该 peer 是否仍在失败记忆期内;命中过期条目时当场清除
// (惰性清理的另一入口)并返回 false。
func (m *SkipMemo) ShouldSkip(peerID string) bool {
	if peerID == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.until[peerID]
	if !ok {
		return false
	}
	if !m.clock().Before(t) {
		delete(m.until, peerID)
		return false
	}
	return true
}

// SkipMemoEntry 快照单项;Expired=true 表示 remaining<=0(惰性清理仍归
// Record/ShouldSkip——快照零副作用,绝不删表)。
type SkipMemoEntry struct {
	PeerID           string `json:"peer_id"`
	RemainingSeconds int64  `json:"remaining_seconds"`
	Expired          bool   `json:"expired"`
}

// Snapshot memo 表纯读快照;空表≠STUN 健康(不可达不进 memo,见 classify
// 账本)——消费方必须并排读 classify 账本,勿单读。now 走 m.clock()(零值
// 兜底约定:生产以 &SkipMemo{} 零值构造,now 可 nil——直呼 m.now() 即生产
// nil-func panic)。
func (m *SkipMemo) Snapshot() []SkipMemoEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()
	out := make([]SkipMemoEntry, 0, len(m.until))
	for id, until := range m.until {
		rem := int64(until.Sub(now) / time.Second)
		out = append(out, SkipMemoEntry{PeerID: id, RemainingSeconds: rem, Expired: rem <= 0})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PeerID < out[j].PeerID })
	return out
}
