// sshexit.go 承担 mesh 出口的 SSH 侧客户端:ExitClient 到单一 mesh 出口
// 节点建一条 mesh-exit SSH channel(每条连接一次独立 SSH 握手,无池——
// SPEC §5.4 三条硬冲突),LAN 载体优先、Piko 中继兜底,准入在拨号前判定;
// exitConn 把 gossh.Channel 适配为 net.Conn。host key 钉扎与私钥解析复用本包
// hostkey.go / spec.go。
package mesh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"

	gossh "golang.org/x/crypto/ssh"
)

// ChannelMeshExit 是 mesh 出口在 SSH 上的自管 channel 名(B 侧 serveChannels
// 的 form×channel 矩阵同源常量,在线协议,SPEC §4;两侧字面量由集成测试互锁)。
const ChannelMeshExit = "mesh-exit"

// meshExitMaxExtra 是 mesh-exit ExtraData 服务端上限(与 B 侧同值;正常
// payload 恒 < 100B)。
const meshExitMaxExtra = 512

// meshExitHandshakeTimeout 界定 ExitClient 拨号+握手窗口:gossh.NewClientConn
// 不吃 ClientConfig.Timeout(该字段只对 gossh.Dial 的 TCP 段生效,SPEC §5.1),
// 必须手动 SetDeadline。var 仅为测试注入短超时。
var meshExitHandshakeTimeout = 10 * time.Second

var errNoTransportAdmits = errors.New("mesh: no transport admits this peer/class")

// pickErr 返回第一个非 nil 的错误(全 nil 返回 nil)。
func pickErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// ExitClient 到单一 mesh 出口节点的 SSH 出口客户端(SPEC §5.1 完整定义)。
// 生命周期:构造期解析私钥/钉扎/Transport 切片(一次性),DialContext 逐
// 连接解析类别并遍历 Transport。缓存策略由调用方决定(主路径缓存在
// processor/tcp 的 TCPConnectionHandler,SPEC §5.1「正确落点」)。
type ExitClient struct {
	spec       PeerSpec
	transports []Transport // 有序;LAN→udp→piko(SPEC §7.3,打洞居中)
	identity   gossh.Signer
	hostKeyCB  gossh.HostKeyCallback
	timeout    time.Duration
}

// NewExitClient 解析私钥与钉扎并组装 transports。装配序恒为 LAN→udp→piko
// (SPEC §7.3:LAN 可达时走 LAN,piko 恒兜底,UDP 打洞居中):punchDial=nil
// (gate 关)时不放 UDPTransport,relay=true 且 pikoDial=nil 时不放
// PikoTransport(等价中继未接线,落到无该载体的语义,SPEC §7.3)。两 gate
// 的把关层数不同(Task 12 契约修正):udp 是双层——构造期不进切片是第一
// 层,UDPTransport.Admit 的 dial!=nil 防御分支是第二层;piko 只有装配
// 单层——relay 位+接线态决定进切片,PikoTransport.Admit 不再查接线态。
func NewExitClient(spec PeerSpec, privateKeyPEM, passphrase string, pikoDial PikoDialFunc, punchDial PunchDialFunc) (*ExitClient, error) {
	signer, err := SignerFromPEM(privateKeyPEM, passphrase)
	if err != nil {
		return nil, fmt.Errorf("mesh: identity key: %w", err)
	}
	transports := []Transport{LANTransport{}}
	if punchDial != nil {
		transports = append(transports, UDPTransport{dial: punchDial})
	}
	if spec.Relay && pikoDial != nil {
		transports = append(transports, PikoTransport{dial: pikoDial})
	}
	user := spec.SSHUser
	if user == "" {
		user = meshSSHUser
	}
	return &ExitClient{
		spec:       spec,
		transports: transports,
		identity:   signer,
		hostKeyCB:  pinnedHostKey(spec.HostKey),
		timeout:    meshExitHandshakeTimeout,
	}, nil
}

// TransportNames 返回装配载体名的有序表(LAN→udp→piko 装配序的实际子集):
// 载体可见性观测面(跨包测试断言/诊断),不参与拨号决策。
func (c *ExitClient) TransportNames() []string {
	names := make([]string, 0, len(c.transports))
	for _, t := range c.transports {
		names = append(names, t.Name())
	}
	return names
}

// DialTrace 一次 mesh 拨号的载体轨迹(载体可见性日志的观测面,mesh-udp-
// punch Task 12 评审定谳:回落成功时 udp 失败原因不能随之丢弃——「静默
// 回落」必须有痕迹)。
type DialTrace struct {
	Used   string // 承载本次拨号的载体名("lan"/"udp"/"piko");未到载体连接=空串
	UDPErr error  // udp 载体失败原因;未装配/未尝试/其承载成功 = nil
}

// DialContext 建一条 mesh-exit 连接(每条连接一次独立 SSH 握手,无池,
// SPEC §5.1)。类别逐连接解析;载体准入在拨号前判定(Admit 唯一调用点)。
// 载体轨迹观测走 DialContextTraced(本方法是它的丢轨迹包装,Dialer 接口
// 语义不变)。
func (c *ExitClient) DialContext(ctx context.Context, md *M.Metadata) (net.Conn, error) {
	conn, _, err := c.DialContextTraced(ctx, md)
	return conn, err
}

// DialContextTraced DialContext 的带载体轨迹版:返回值补本次拨号最终走的
// 载体与 udp 失败原因,供链头日志(processor/tcp [MESH-OUT] 载体可见性)
// 与诊断消费;轨迹随调用返回,不落在客户端实例上(无跨连接状态)。
func (c *ExitClient) DialContextTraced(ctx context.Context, md *M.Metadata) (net.Conn, DialTrace, error) {
	var trace DialTrace
	class := classFromContext(ctx, md)

	payload := exitPayload(md)
	if payload == nil {
		return nil, trace, errUnbuildableTarget // 不可构造目标:error,不发空 payload(SPEC §5.1.1)
	}

	// ConnID 透传给 PikoTransport 的生产拨号(piko 侧逐连接日志,既有缝)。
	if md != nil && md.ConnID != "" {
		ctx = WithConnID(ctx, md.ConnID)
	}

	var raw net.Conn
	var directErr, relayErr error // direct 桶=lan+udp 两直连载体共桶(末次失败覆盖)
	var admitted bool
	for _, t := range c.transports {
		if !t.Admit(class, c.spec) {
			continue // 准入在拨号前,不消耗拨号预算
		}
		admitted = true
		conn, err := t.DialSSHD(ctx, c.spec)
		if err == nil {
			raw = conn
			trace.Used = t.Name()
			break
		}
		if _, isUDP := t.(UDPTransport); isUDP {
			trace.UDPErr = err // 回落成功也要留痕(静默回落排查重点)
		}
		if t.IsRelay() {
			relayErr = err
		} else {
			directErr = err
		}
	}
	if raw == nil {
		if !admitted {
			// 携带端点与类别上下文(%w 保 errors.Is 契约):消费方日志从此能
			// 定位「哪个 peer 的哪类流量没有可用载体」,不再是无来源裸句。
			return nil, trace, fmt.Errorf("mesh: no transport admits %s (class=%s): %w", c.spec.DeviceID, class, errNoTransportAdmits)
		}
		if relayErr != nil && directErr != nil {
			return nil, trace, fmt.Errorf("mesh: no SSH transport to %s (direct_last_err: %v, relay_last_err: %w)",
				c.spec.DeviceID, directErr, relayErr)
		}
		return nil, trace, fmt.Errorf("mesh: no SSH transport to %s: %w", c.spec.DeviceID, pickErr(directErr, relayErr))
	}

	if err := raw.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		_ = raw.Close()
		return nil, trace, fmt.Errorf("mesh: set handshake deadline to %s: %w", c.spec.DeviceID, err)
	}
	cc, chans, reqs, err := gossh.NewClientConn(raw, c.spec.DeviceID, &gossh.ClientConfig{
		User:            c.spec.SSHUser,
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(c.identity)},
		HostKeyCallback: c.hostKeyCB,
	})
	if err != nil {
		_ = raw.Close()
		return nil, trace, fmt.Errorf("mesh: ssh handshake to %s: %w", c.spec.DeviceID, err)
	}
	client := gossh.NewClient(cc, chans, reqs)

	// OpenChannel 窗口 deadline(评审 I1):握手完成不能清 deadline 裸奔——
	// OpenChannel 仍阻塞在 raw 的读上,黑洞对端(握手正常完成但不回
	// channelOpenConfirmation)会把调用方 goroutine 永久挂死,正是 deadline
	// 要防的死法挪了个位置。窗口复用同一超时罩住 OpenChannel(SetDeadline
	// 整体替换旧 deadline 即换窗),返回(成功或失败)即清——established
	// 长连接不得带 deadline。
	if err := raw.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		_ = client.Close()
		return nil, trace, fmt.Errorf("mesh: set open-channel deadline to %s: %w", c.spec.DeviceID, err)
	}
	ch, chReqs, err := client.OpenChannel(ChannelMeshExit, payload)
	_ = raw.SetDeadline(time.Time{}) // OpenChannel 已返回,窗口即清(成功失败同路)
	if err != nil {
		_ = client.Close()
		return nil, trace, fmt.Errorf("mesh: open %s channel to %s: %w", ChannelMeshExit, c.spec.DeviceID, err)
	}
	go gossh.DiscardRequests(chReqs) // 必须消费,否则连接挂死(SPEC §5.1)

	return newExitConn(ch, client), trace, nil
}

// exitConn 把 gossh.Channel 适配为 net.Conn。自写而非复用 inbound/ssh 的
// channelConn(未导出且其 deadline 为 no-op 先例不可跨包)。Close 连带关
// SSH client(每条 mesh-exit 连接独占一条 SSH 连接)。
type exitConn struct {
	ch         gossh.Channel
	client     *gossh.Client
	localAddr  net.Addr
	remoteAddr net.Addr
}

func newExitConn(ch gossh.Channel, client *gossh.Client) *exitConn {
	c := &exitConn{ch: ch, client: client}
	if client != nil {
		c.remoteAddr = client.RemoteAddr()
		c.localAddr = client.LocalAddr()
	}
	return c
}

func (c *exitConn) Read(b []byte) (int, error)  { return c.ch.Read(b) }
func (c *exitConn) Write(b []byte) (int, error) { return c.ch.Write(b) }
func (c *exitConn) Close() error {
	err := c.ch.Close()
	if cerr := c.client.Close(); err == nil {
		err = cerr
	}
	return err
}
func (c *exitConn) LocalAddr() net.Addr              { return c.localAddr }
func (c *exitConn) RemoteAddr() net.Addr             { return c.remoteAddr }
func (c *exitConn) SetDeadline(time.Time) error      { return nil } // SSH 通道无 deadline 语义(已知限制,SPEC §16)
func (c *exitConn) SetReadDeadline(time.Time) error  { return nil }
func (c *exitConn) SetWriteDeadline(time.Time) error { return nil }

// (DialMeshAddr 已随出口链路改造删除,2026-11-12:旧离线兜底的裸地址拨号
// 通道不再有生产消费方——委托路径复用 getMeshExitForExecution 的
// ExitClient 装配,地址/钉扎/身份全从 peers 条目继承,无独立裸地址形态。)
