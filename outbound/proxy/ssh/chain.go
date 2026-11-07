package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"aliang.one/nursorgate/inbound/tun/dialer"
	gossh "golang.org/x/crypto/ssh"
)

// NodeConfig 是链上一个 SSH 节点(跳板或终点)的连接参数(导出形态,
// registry 组装用)。
type NodeConfig struct {
	Host          string // 不含端口
	Port          uint16
	User          string
	Password      string
	PrivateKey    string
	KeyPassphrase string
}

// NodeFromAddrPort 组装节点(Host+Port 分立,registry 侧来自配置解析)。
func NodeFromAddrPort(host string, port uint16, user, pass, key, keyPass string) NodeConfig {
	return NodeConfig{
		Host:          host,
		Port:          port,
		User:          user,
		Password:      pass,
		PrivateKey:    key,
		KeyPassphrase: keyPass,
	}
}

// hostPort 返回节点的 "host:port" 拨号形态(IPv6 由 JoinHostPort 加方括号)。
func (n NodeConfig) hostPort() string {
	return net.JoinHostPort(n.Host, strconv.Itoa(int(n.Port)))
}

// authMethods 返回节点可用认证方式;私钥解析失败返回错误(New 时即校验,
// fail-fast——运行期不再有解析路径)。
// Password 在前、KeyboardInteractive 兜底:服务器不广播的认证方式 x/crypto
// 会在收到失败应答的 allowed-methods 列表后跳过,不会白发一次交互请求,
// 因此对只支持 password 的服务器没有额外往返;对只支持 keyboard-interactive
// 的服务器(常见于禁用 password 的加固 sshd)则靠它认证成功。
func authMethods(n NodeConfig) ([]gossh.AuthMethod, error) {
	var methods []gossh.AuthMethod
	if n.Password != "" {
		methods = append(methods, gossh.Password(n.Password))
		methods = append(methods, gossh.KeyboardInteractive(
			func(_ string, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = n.Password
				}
				return answers, nil
			}))
	}
	if n.PrivateKey != "" {
		var signer gossh.Signer
		var err error
		if n.KeyPassphrase != "" {
			signer, err = gossh.ParsePrivateKeyWithPassphrase([]byte(n.PrivateKey), []byte(n.KeyPassphrase))
		} else {
			signer, err = gossh.ParsePrivateKey([]byte(n.PrivateKey))
		}
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		methods = append(methods, gossh.PublicKeys(signer))
	}
	return methods, nil
}

// chainDialer 负责把一条完整链(可选逐跳穿透)建立成最终 *cascadeClient
// (Close 级联关全部跳,其余语义经内嵌 *gossh.Client 透传)。
type chainDialer struct {
	hosts *KnownHosts
	nodes []NodeConfig // jumps + final
}

func newChainDialer(final NodeConfig, jumps []NodeConfig, hosts *KnownHosts) *chainDialer {
	nodes := make([]NodeConfig, 0, len(jumps)+1)
	nodes = append(nodes, jumps...)
	nodes = append(nodes, final)
	return &chainDialer{hosts: hosts, nodes: nodes}
}

// handshakeTimeout 约束单跳的完整握手:version 协商+密钥交换+用户认证。
const handshakeTimeout = 15 * time.Second

// cascadeClient 是 dialChain 的最终交付形态:内嵌 *gossh.Client 透传全部
// 语义(OpenChannel/Dial/DialContext/…,调用方只需这些),Close 替换为
// 级联关闭——先关最终跳,再从最后一跳往前依次关闭全部前序跳。任何一跳
// 关闭出错都不阻断后续跳的关闭(逐跳收集、errors.Join 汇总);幂等
// (sync.Once),重复 Close 返回首次结果。
type cascadeClient struct {
	*gossh.Client
	prior []*gossh.Client // 前序跳(不含最终跳)
	once  sync.Once
	err   error
}

func (c *cascadeClient) Close() error {
	c.once.Do(func() {
		// 非空错误带上跳序号:多跳链各跳的关闭错误可能文本相同
		//(如 "ssh: connection closed"),不归属则无法定位;空保持空,
		// errors.Join 的全空→空属性不受影响。最终跳序号 = len(prior),
		// 与 dialChain 的 hop 编号一致。
		hopErr := func(hop int, err error) error {
			if err == nil {
				return nil
			}
			return fmt.Errorf("hop %d close: %w", hop, err)
		}
		errs := []error{hopErr(len(c.prior), c.Client.Close())}
		for i := len(c.prior) - 1; i >= 0; i-- {
			errs = append(errs, hopErr(i, c.prior[i].Close()))
		}
		c.err = errors.Join(errs...)
	})
	return c.err
}

// dialChain 建链:第 0 跳 TCP 直连;后续跳经前一跳的 direct-tcpip 到下一跳
// 的 sshd,再在其上跑 NewClientConn——即 OpenSSH ProxyJump 语义。
// 中途任何一跳失败都会关闭全部已建跳(从最后一跳往前),不留半开链;
// 成功时返回级联 Close 的最终 client(见 cascadeClient)。
func (d *chainDialer) dialChain(ctx context.Context) (*cascadeClient, error) {
	// clients 全量收集每跳句柄:循环变量会被逐跳覆盖,不收集则前序跳
	// 句柄丢失,失败清场与级联关闭都依赖这份全量。
	var clients []*gossh.Client
	for i, n := range d.nodes {
		if err := ctx.Err(); err != nil {
			closeClients(clients)
			return nil, err
		}
		auths, err := authMethods(n)
		if err != nil {
			closeClients(clients)
			return nil, fmt.Errorf("hop %d auth %s: %w", i, n.hostPort(), err)
		}
		var underlying net.Conn
		if i == 0 {
			underlying, err = dialer.DialContext(ctx, "tcp", n.hostPort())
		} else {
			underlying, err = clients[i-1].DialContext(ctx, "tcp", n.hostPort()) // 带 ctx(v0.48 签名)
		}
		if err != nil {
			closeClients(clients)
			return nil, fmt.Errorf("hop %d dial %s: %w", i, n.hostPort(), err)
		}
		// ClientConfig.Timeout 只在 gossh.Dial 的 TCP 段生效,直连
		// NewClientConn 不吃它;手动给底层 conn 设 deadline 让整段握手
		// 受 handshakeTimeout 约束,握手完立即清除,不污染后续读写。
		// 现状:hop 0(TCP conn)有 15s 限制;hop ≥1 的底层是
		// client.DialContext 返回的 x/crypto chanConn,其 SetReadDeadline/
		// SetWriteDeadline 确定性返回 "ssh: tcpChan: deadline not
		// supported"——即这些跳的握手当前无超时保护(候选修复:带超时的
		// conn 包装或握手看门狗,留给后续任务)。
		_ = underlying.SetDeadline(time.Now().Add(handshakeTimeout))
		cc, chans, reqs, derr := gossh.NewClientConn(underlying, n.hostPort(), &gossh.ClientConfig{
			User:            n.User,
			Auth:            auths,
			HostKeyCallback: d.hosts.CallbackFor(n.hostPort()),
		})
		_ = underlying.SetDeadline(time.Time{})
		if derr != nil {
			_ = underlying.Close()
			closeClients(clients)
			return nil, fmt.Errorf("hop %d handshake %s: %w", i, n.hostPort(), derr)
		}
		clients = append(clients, gossh.NewClient(cc, chans, reqs))
	}
	if len(clients) == 0 {
		// newChainDialer 恒追加 final 节点,正常构造不会走到;防御性兜底。
		return nil, fmt.Errorf("empty chain")
	}
	return &cascadeClient{Client: clients[len(clients)-1], prior: clients[:len(clients)-1]}, nil
}

// closeClients 从最后一跳往前关闭全部 clients(失败路径清场:必须全量,
// 只关最近一跳会让更早的跳半开)。
func closeClients(clients []*gossh.Client) {
	for i := len(clients) - 1; i >= 0; i-- {
		_ = clients[i].Close()
	}
}
