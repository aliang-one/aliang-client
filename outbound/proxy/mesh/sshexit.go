// sshexit.go 承担 mesh 出口的 SSH 侧客户端:ExitClient 到单一 mesh 出口
// 节点建一条 mesh-exit SSH channel(每条连接一次独立 SSH 握手,无池——
// SPEC §5.4 三条硬冲突),LAN 载体优先、Piko 中继兜底,准入在拨号前判定;
// exitConn 把 gossh.Channel 适配为 net.Conn;DialMeshAddr 是无 peer 记录
// 裸地址的离线兜底拨号(SPEC §7.2)。host key 钉扎与私钥解析复用本包
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
	transports []Transport // 有序;LAN 优先,Piko 兜底(SPEC §7.3)
	identity   gossh.Signer
	hostKeyCB  gossh.HostKeyCallback
	timeout    time.Duration
}

// NewExitClient 解析私钥与钉扎并组装 transports。relay=true 且 pikoDial=nil
// 时不放 PikoTransport(等价中继未接线,落到 LAN-only 语义,SPEC §7.3)。
func NewExitClient(spec PeerSpec, privateKeyPEM, passphrase string, pikoDial PikoDialFunc) (*ExitClient, error) {
	signer, err := SignerFromPEM(privateKeyPEM, passphrase)
	if err != nil {
		return nil, fmt.Errorf("mesh: identity key: %w", err)
	}
	transports := []Transport{LANTransport{}}
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

// DialContext 建一条 mesh-exit 连接(每条连接一次独立 SSH 握手,无池,
// SPEC §5.1)。类别逐连接解析;载体准入在拨号前判定(Admit 唯一调用点)。
func (c *ExitClient) DialContext(ctx context.Context, md *M.Metadata) (net.Conn, error) {
	class := classFromContext(ctx, md)

	payload := exitPayload(md)
	if payload == nil {
		return nil, errUnbuildableTarget // 不可构造目标:error,不发空 payload(SPEC §5.1.1)
	}

	// ConnID 透传给 PikoTransport 的生产拨号(piko 侧逐连接日志,既有缝)。
	if md != nil && md.ConnID != "" {
		ctx = WithConnID(ctx, md.ConnID)
	}

	var raw net.Conn
	var lanErr, relayErr error
	var admitted bool
	for _, t := range c.transports {
		if !t.Admit(class, c.spec) {
			continue // 准入在拨号前,不消耗拨号预算
		}
		admitted = true
		conn, err := t.DialSSHD(ctx, c.spec)
		if err == nil {
			raw = conn
			break
		}
		if t.IsRelay() {
			relayErr = err
		} else {
			lanErr = err
		}
	}
	if raw == nil {
		if !admitted {
			// 携带端点与类别上下文(%w 保 errors.Is 契约):消费方日志从此能
			// 定位「哪个 peer 的哪类流量没有可用载体」,不再是无来源裸句。
			return nil, fmt.Errorf("mesh: no transport admits %s (class=%s): %w", c.spec.DeviceID, class, errNoTransportAdmits)
		}
		if relayErr != nil && lanErr != nil {
			return nil, fmt.Errorf("mesh: no SSH transport to %s (lan_last_err: %v, relay_last_err: %w)",
				c.spec.DeviceID, lanErr, relayErr)
		}
		return nil, fmt.Errorf("mesh: no SSH transport to %s: %w", c.spec.DeviceID, pickErr(lanErr, relayErr))
	}

	if err := raw.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("mesh: set handshake deadline to %s: %w", c.spec.DeviceID, err)
	}
	cc, chans, reqs, err := gossh.NewClientConn(raw, c.spec.DeviceID, &gossh.ClientConfig{
		User:            c.spec.SSHUser,
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(c.identity)},
		HostKeyCallback: c.hostKeyCB,
	})
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("mesh: ssh handshake to %s: %w", c.spec.DeviceID, err)
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
		return nil, fmt.Errorf("mesh: set open-channel deadline to %s: %w", c.spec.DeviceID, err)
	}
	ch, chReqs, err := client.OpenChannel(ChannelMeshExit, payload)
	_ = raw.SetDeadline(time.Time{}) // OpenChannel 已返回,窗口即清(成功失败同路)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("mesh: open %s channel to %s: %w", ChannelMeshExit, c.spec.DeviceID, err)
	}
	go gossh.DiscardRequests(chReqs) // 必须消费,否则连接挂死(SPEC §5.1)

	return newExitConn(ch, client), nil
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

// DialMeshAddr 拨一个无 peer 记录的裸地址(离线兜底专用,SPEC §7.2)。
// addr 指向对端 SSH 入口 56432(不是已删除的 mesh 监听口);identity 为
// 本机 mesh 身份(B 的 mesh_authorized_keys 准入);hostKey 为兜底节点
// 钉扎,空 = fail-closed。LAN-only(单载体,不试中继)。payload 由调用方
// 经 ExitPayloadForMetadata(md) 构造;nil 按不可构造拒绝(OpenChannel 前)。
func DialMeshAddr(ctx context.Context, addr, hostKey string, identity gossh.Signer, payload []byte) (net.Conn, error) {
	if identity == nil {
		return nil, errors.New("mesh: fallback dial requires node identity key")
	}
	if payload == nil {
		return nil, errUnbuildableTarget
	}
	raw, err := dialLAN(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("mesh: fallback dial %s: %w", addr, err)
	}
	if err := raw.SetDeadline(time.Now().Add(meshExitHandshakeTimeout)); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("mesh: set handshake deadline to %s: %w", addr, err)
	}
	cc, chans, reqs, hErr := gossh.NewClientConn(raw, addr, &gossh.ClientConfig{
		User:            meshSSHUser,
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(identity)},
		HostKeyCallback: pinnedHostKey(hostKey),
	})
	if hErr != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("mesh: fallback ssh handshake to %s: %w", addr, hErr)
	}
	client := gossh.NewClient(cc, chans, reqs)
	// OpenChannel 窗口 deadline(同 DialContext 的评审 I1 加固):黑洞对端不得
	// 把 OpenChannel 挂死在无 deadline 的 conn 读上;返回(成功或失败)即清。
	if err := raw.SetDeadline(time.Now().Add(meshExitHandshakeTimeout)); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("mesh: set open-channel deadline to %s: %w", addr, err)
	}
	ch, chReqs, oErr := client.OpenChannel(ChannelMeshExit, payload)
	_ = raw.SetDeadline(time.Time{}) // OpenChannel 已返回,窗口即清(成功失败同路)
	if oErr != nil {
		_ = client.Close()
		return nil, fmt.Errorf("mesh: fallback open %s channel to %s: %w", ChannelMeshExit, addr, oErr)
	}
	go gossh.DiscardRequests(chReqs)
	return newExitConn(ch, client), nil
}
