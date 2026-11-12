// stun.go 承载打洞前的公网自省三件套:STUN Binding 地址发现(Discover)、
// 双服务器端口比对判对称 NAT(Classify)、per-peer 对称失败记忆(SkipMemo)。
// 所有探测都经调用方传入的打洞 socket 发出——那是 NewBoundUDPConn 出厂即
// 绑物理接口的 socket,学到的公网映射必须属于这张卡;若在函数内部另建
// socket,TUN-on 下学到的是引擎换发 socket 的映射,洞会打错 5-tuple
// (与 socket.go:26 的绑定铁律同源,这是 conn 作参数的原因)。

package punch

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/pion/stun"
)

// 默认双服务器档:Google 公益 STUN 两台。v1 包内常量、无配置覆盖项
// (YAGNI;coturn 自托管是二期看数据再定的事,届时一并加配置)。
const (
	stunServerPrimary   = "stun.l.google.com:19302"
	stunServerSecondary = "stun1.l.google.com:19302"
)

// defaultSTUNServers 默认判型服务器对,Task 5 编排层取这对调 Classify。
var defaultSTUNServers = []string{stunServerPrimary, stunServerSecondary}

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

// Classify 逐台 Discover 做端口比对判对称 NAT:任两台应答端口不同即
// symmetricSuspect=true(返回首台应答的映射);全部不可达报 error;
// 恰好一台应答则判型降级为非对称返回该映射——端口比对不成立不能冤枉成
// symmetric,真对称导致的打洞失败由 SkipMemo 失败记忆兜底。
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

	var (
		first     PublicMapping
		haveFirst bool
		errs      []error
	)
	for _, server := range servers {
		mapping, err := Discover(conn, server, classifyDiscoverTimeout)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !haveFirst {
			first, haveFirst = mapping, true
			continue
		}
		if mapping.Port != first.Port {
			// 两台都应答且端口不同:对同一 socket,不同目的地址换来不同
			// 外部端口,正是对称 NAT 的签名。
			return first, true, nil
		}
	}
	if !haveFirst {
		// 全部不可达≠symmetric:按「STUN 不可用」交给调用方落 piko,
		// 且不该写 SkipMemo(写不写由调用方决定,这里只报 error)。
		return PublicMapping{}, false, fmt.Errorf("stun classify: 全部 %d 台服务器不可达: %w", len(servers), errors.Join(errs...))
	}
	return first, false, nil
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
