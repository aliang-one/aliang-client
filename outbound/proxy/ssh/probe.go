// probe.go — SSH 出站链路探测（客户端「测试连接」的核心）：用与真实代理
// 完全相同的链构建路径（authMethods→dialChain 语义：认证→握手→TOFU 校验）
// 建一条一次性链，然后在最终跳上开一个 direct-tcpip 通道到探测目标并做
// 一次真实往返（写读各一），往返成功才算连通——通道打开但数据不通的
// 故障形态（2026-11-15 排查实录：Mac 侧 direct 拨号缺 DstIP 致秒 EOF）
// 在这里就能暴露，而不是等用户流量踩坑。
//
// 探测是独立短连接：不复用池、不留后台状态，探测完即关。失败原因分层
// 返回（阶段+原始错误），前端据此显示「哪一步挂了」。
package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// ProbeTarget 是探测目标：连通判定=对该目标完成一次 TCP 往返。
// 缺省 1.1.1.1:53（公共 DNS 的 TCP 面板，全球可达、几乎不缓存连接）。
type ProbeTarget struct {
	Host string
	Port uint16
}

// DefaultProbeTarget 是 Host 为空时的缺省目标。
func DefaultProbeTarget() ProbeTarget {
	return ProbeTarget{Host: "1.1.1.1", Port: 53}
}

// probeHandshakeTimeout 是单跳握手超时（与 chain.go handshakeTimeout 同值；
// 独立声明避免探测代码依赖生产常量的隐式耦合）。
const probeHandshakeTimeout = 15 * time.Second

// probeRoundTripTimeout 是探测往返（写→读）的预算上限。
const probeRoundTripTimeout = 8 * time.Second

// probeStage 是探测失败的阶段标签（前端映射「哪一步挂了」的文案）。
type probeStage string

const (
	probeStageAuth      probeStage = "auth"      // 凭据解析/认证被拒
	probeStageDial      probeStage = "dial"      // TCP 拨号失败（不可达/端口拒绝）
	probeStageHandshake probeStage = "handshake" // SSH 版本协商/KEX/主机钥校验
	probeStageChannel   probeStage = "channel"   // direct-tcpip 通道打开被拒
	probeStageRoundTrip probeStage = "roundtrip" // 通道开了但数据不通
)

// ProbeResult 是一次探测的完整结论。OK=true 时 LatencyMillis=握手+往返
// 总耗时；OK=false 时 Stage/Error 指向失败环节。
type ProbeResult struct {
	OK            bool       `json:"ok"`
	Stage         probeStage `json:"stage,omitempty"`      // 失败阶段（ok 时为空）
	Error         string     `json:"error,omitempty"`      // 原始错误文本（ok 时为空）
	LatencyMillis int64      `json:"latency_millis"`       // 成功总耗时
	HostKeyFpr    string     `json:"host_key_fingerprint"` // 服务器 host key 指纹（成功握手即有）
}

// Probe 用给定节点链（jumps+final）做一次完整连通探测。hosts 为 nil 时
// 跳过 TOFU 校验（配置尚未保存、首次探测的形态）；非 nil 时与真实链路
// 同源校验（mismatch 会如实报 handshake 失败——这正是「测试连接」要暴露的）。
func Probe(final NodeConfig, jumps []NodeConfig, hosts *KnownHosts, target ProbeTarget) ProbeResult {
	if target.Host == "" || target.Port == 0 {
		target = DefaultProbeTarget()
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), probeHandshakeTimeout*time.Duration(len(jumps)+1)+probeRoundTripTimeout)
	defer cancel()

	clients, last, stage, err := probeConnect(ctx, final, jumps, hosts)
	defer closeProbeClients(clients)
	if err != nil {
		return ProbeResult{OK: false, Stage: stage, Error: err.Error()}
	}
	latency := time.Since(start).Milliseconds()

	fpr := ""
	if last.serverKey != nil {
		fpr = gossh.FingerprintSHA256(last.serverKey)
	}
	if err := probeRoundTrip(last.client, target); err != nil {
		return ProbeResult{OK: false, Stage: probeStageRoundTrip, Error: err.Error(), LatencyMillis: latency, HostKeyFpr: fpr}
	}
	return ProbeResult{OK: true, LatencyMillis: latency, HostKeyFpr: fpr}
}

// probeHop 是一跳的产物：client + 握手时服务端出示的公钥（TOFU 记录与
// 指纹展示都从这来）。
type probeHop struct {
	client    *gossh.Client
	serverKey gossh.PublicKey
}

// probeConnect 逐跳建链（ProxyJump 语义，与 chainDialer.dialChain 同款；
// 独立实现而非复用 dialChain：探测需要逐跳区分失败阶段与暴露最后一跳的
// 服务器公钥，dialChain 只回笼统错误）。任何一跳失败即关闭全部已建跳。
// 返回的 last.serverKey 是最终跳握手时服务端出示的公钥（指纹展示/TOFU
// 对账用；握手失败时为 nil）。
func probeConnect(ctx context.Context, final NodeConfig, jumps []NodeConfig, hosts *KnownHosts) ([]*gossh.Client, *probeHop, probeStage, error) {
	nodes := make([]NodeConfig, 0, len(jumps)+1)
	nodes = append(nodes, jumps...)
	nodes = append(nodes, final)

	var clients []*gossh.Client
	var lastKey gossh.PublicKey
	for i, n := range nodes {
		if err := ctx.Err(); err != nil {
			return clients, nil, probeStageDial, err
		}
		auths, err := authMethods(n)
		if err != nil {
			return clients, nil, probeStageAuth, fmt.Errorf("node %s: %w", n.hostPort(), err)
		}
		var underlying net.Conn
		if i == 0 {
			d := net.Dialer{}
			underlying, err = d.DialContext(ctx, "tcp", n.hostPort())
		} else {
			underlying, err = clients[i-1].DialContext(ctx, "tcp", n.hostPort())
		}
		if err != nil {
			return clients, nil, probeStageDial, fmt.Errorf("hop %d %s: %w", i, n.hostPort(), err)
		}
		_ = underlying.SetDeadline(time.Now().Add(probeHandshakeTimeout))
		// HostKeyCallback 同步执行（KEX 期间）：闭包捕获本跳公钥，握手成功
		// 后即为本跳服务器身份；最后一跳的值即对外展示的指纹来源。
		var hopKey gossh.PublicKey
		hostKeyCb := func(_ string, _ net.Addr, key gossh.PublicKey) error {
			hopKey = key
			if hosts == nil {
				return nil // 未落库形态（未保存配置的首测）：跳过 TOFU
			}
			return hosts.CallbackFor(n.hostPort())(n.hostPort(), nil, key)
		}
		cc, chans, reqs, derr := gossh.NewClientConn(underlying, n.hostPort(), &gossh.ClientConfig{
			User:            n.User,
			Auth:            auths,
			HostKeyCallback: hostKeyCb,
		})
		_ = underlying.SetDeadline(time.Time{})
		if derr != nil {
			_ = underlying.Close()
			return clients, nil, probeStageHandshake, fmt.Errorf("hop %d %s: %w", i, n.hostPort(), derr)
		}
		lastKey = hopKey
		clients = append(clients, gossh.NewClient(cc, chans, reqs))
	}
	if len(clients) == 0 {
		return clients, nil, probeStageDial, errors.New("empty chain")
	}
	return clients, &probeHop{client: clients[len(clients)-1], serverKey: lastKey}, "", nil
}

// probeRoundTrip 在最终跳上开 direct-tcpip 通道到探测目标并做一次写入。
// 判定语义：OpenChannel 成功（对端接受=服务端到目标的三次握手由其代拨）
// + Write 成功（通道写入窗口活着）=链路连通。不等待读方向——目标（如
// DNS TCP 对垃圾字节）可能永不回显也不关连接，等待会挂死探测；而
// 「服务端代拨失败」会以 OpenChannel 被拒（ConnectionFailed）同步返回，
// 「通道开了但数据路径断」的 EOF 形态会在 Write 侧立即报错（半关后写入
// 失败），两类故障都被写路径覆盖。
func probeRoundTrip(client *gossh.Client, target ProbeTarget) error {
	payload := gossh.Marshal(directTCPIPReq{
		Addr:       target.Host,
		Port:       uint32(target.Port),
		OriginAddr: "127.0.0.1",
		OriginPort: 0,
	})
	// 通道名 "direct-tcpip"：RFC 4254 §7.2，与生产 DialContext 的字面量同值
	// （chain.go 无导出常量，sshtest 同款字面量先例）。
	ch, _, err := client.OpenChannel("direct-tcpip", payload)
	if err != nil {
		return err
	}
	defer func() { _ = ch.Close() }()

	if _, err := ch.Write([]byte{0x00, 0x01, 0x02, 0x03}); err != nil {
		return fmt.Errorf("probe write: %w", err)
	}
	return nil
}

// closeProbeClients 从最后一跳往前关闭（失败路径清场，dialChain 同款）。
func closeProbeClients(clients []*gossh.Client) {
	for i := len(clients) - 1; i >= 0; i-- {
		_ = clients[i].Close()
	}
}
