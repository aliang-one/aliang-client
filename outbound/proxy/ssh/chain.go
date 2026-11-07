package ssh

import (
	"context"
	"fmt"
	"net"
	"time"

	"aliang.one/nursorgate/inbound/tun/dialer"
	gossh "golang.org/x/crypto/ssh"
)

// nodeConfig 是链上一个 SSH 节点(跳板或终点)的连接参数。
type nodeConfig struct {
	hostport      string
	user          string
	password      string
	privateKey    string
	keyPassphrase string
}

func finalNode(hostport, user, pass, key, keyPass string) nodeConfig {
	return nodeConfig{hostport: hostport, user: user, password: pass, privateKey: key, keyPassphrase: keyPass}
}

// authMethods 返回节点可用认证方式;私钥解析失败返回错误(New 时即校验,
// fail-fast——运行期不再有解析路径)。
// Password 在前、KeyboardInteractive 兜底:服务器不广播的认证方式 x/crypto
// 会在收到失败应答的 allowed-methods 列表后跳过,不会白发一次交互请求,
// 因此对只支持 password 的服务器没有额外往返;对只支持 keyboard-interactive
// 的服务器(常见于禁用 password 的加固 sshd)则靠它认证成功。
func authMethods(n nodeConfig) ([]gossh.AuthMethod, error) {
	var methods []gossh.AuthMethod
	if n.password != "" {
		methods = append(methods, gossh.Password(n.password))
		methods = append(methods, gossh.KeyboardInteractive(
			func(_ string, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = n.password
				}
				return answers, nil
			}))
	}
	if n.privateKey != "" {
		var signer gossh.Signer
		var err error
		if n.keyPassphrase != "" {
			signer, err = gossh.ParsePrivateKeyWithPassphrase([]byte(n.privateKey), []byte(n.keyPassphrase))
		} else {
			signer, err = gossh.ParsePrivateKey([]byte(n.privateKey))
		}
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		methods = append(methods, gossh.PublicKeys(signer))
	}
	return methods, nil
}

// chainDialer 负责把一条完整链(可选逐跳穿透)建立成最终 *gossh.Client。
type chainDialer struct {
	hosts *KnownHosts
	nodes []nodeConfig // jumps + final
}

func newChainDialer(final nodeConfig, jumps []nodeConfig, hosts *KnownHosts) *chainDialer {
	nodes := make([]nodeConfig, 0, len(jumps)+1)
	nodes = append(nodes, jumps...)
	nodes = append(nodes, final)
	return &chainDialer{hosts: hosts, nodes: nodes}
}

// handshakeTimeout 约束单跳的完整握手:version 协商+密钥交换+用户认证。
const handshakeTimeout = 15 * time.Second

// dialChain 建链:第 0 跳 TCP 直连;后续跳经前一跳的 direct-tcpip 到下一跳
// 的 sshd,再在其上跑 NewClientConn——即 OpenSSH ProxyJump 语义。
// 中途任何一跳失败都会关闭已建的前序跳,不留半开链。
func (d *chainDialer) dialChain(ctx context.Context) (*gossh.Client, error) {
	var client *gossh.Client
	for i, n := range d.nodes {
		if err := ctx.Err(); err != nil {
			closeClient(client)
			return nil, err
		}
		auths, err := authMethods(n)
		if err != nil {
			closeClient(client)
			return nil, fmt.Errorf("hop %d auth %s: %w", i, n.hostport, err)
		}
		var underlying net.Conn
		if i == 0 {
			underlying, err = dialer.DialContext(ctx, "tcp", n.hostport)
		} else {
			underlying, err = client.DialContext(ctx, "tcp", n.hostport) // 带 ctx(v0.48 签名)
		}
		if err != nil {
			closeClient(client)
			return nil, fmt.Errorf("hop %d dial %s: %w", i, n.hostport, err)
		}
		// ClientConfig.Timeout 只在 gossh.Dial 的 TCP 段生效,直连
		// NewClientConn 不吃它;手动给底层 conn 设 deadline 让整段握手
		// 受 handshakeTimeout 约束,握手完立即清除,不污染后续读写。
		// 通道适配 conn 的 SetDeadline 可能是 no-op(见 sshtest.WrapChannel),
		// 尽力而为,不因设不上而阻断。
		_ = underlying.SetDeadline(time.Now().Add(handshakeTimeout))
		cc, chans, reqs, derr := gossh.NewClientConn(underlying, n.hostport, &gossh.ClientConfig{
			User:            n.user,
			Auth:            auths,
			HostKeyCallback: d.hosts.CallbackFor(n.hostport),
		})
		_ = underlying.SetDeadline(time.Time{})
		if derr != nil {
			_ = underlying.Close()
			closeClient(client)
			return nil, fmt.Errorf("hop %d handshake %s: %w", i, n.hostport, derr)
		}
		client = gossh.NewClient(cc, chans, reqs)
	}
	return client, nil
}

func closeClient(c *gossh.Client) {
	if c != nil {
		_ = c.Close()
	}
}
