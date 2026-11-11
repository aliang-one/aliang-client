package mesh

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"

	gossh "golang.org/x/crypto/ssh"
)

// meshSSHUser 是 mesh 节点间 SSH 握手的固定用户名(无语义,仅为服务端
// 日志可读;准入只看公钥,不看用户)。
const meshSSHUser = "mesh"

// PeerSpec 一个 mesh 出口节点的全部拨号事实(配置期确定,运行期只读)。
//
// 注意:**没有 Class 字段**。流量类别是**逐连接**的,不是节点的属性——
// 同一个对端会同时承载 self/ai/custom 三类流量(classFromContext:优先
// ctx 的 WithClass,否则按 metadata 推断)。把它塞进 PeerSpec 会让「这个
// peer 属于哪一类」成为一个无解的问题。类别在 ExitClient.DialContext 内
// 按连接解析(SPEC §5.1)。
type PeerSpec struct {
	DeviceID string   // 对端 mesh 身份 ID(也是 SSH 握手的 host 标签)
	LANAddrs []string // 对端 SSH 入口地址(默认 IP:56432,SPEC §5.2)
	HostKey  string   // 对端 SSH host key 钉扎;空 = fail-closed(SPEC §10.4)
	SSHUser  string   // 固定 "mesh"
	Relay    bool     // true = 允许中继 Transport
}

// CacheKey 计算该 peer 在 ExitClient 缓存中的键(SPEC §10.4):全部影响
// 握手行为的配置值 + 私钥摘要。LANAddrs 排序后拼接(顺序无关)。
func (spec PeerSpec) CacheKey(privateKeyPEM, passphrase string) string {
	addrs := append([]string(nil), spec.LANAddrs...)
	sort.Strings(addrs)
	sum := sha256.Sum256([]byte(privateKeyPEM + "\x00" + passphrase))
	parts := []string{
		spec.DeviceID,
		strings.Join(addrs, "|"),
		spec.HostKey,
		strconv.FormatBool(spec.Relay),
		spec.SSHUser,
		hex.EncodeToString(sum[:]),
	}
	return strings.Join(parts, "\x1f")
}

// SignerFromPEM 解析本机 mesh 身份私钥(OpenSSH 或 PKCS#8 PEM,可带口令)。
func SignerFromPEM(privateKeyPEM, passphrase string) (gossh.Signer, error) {
	if strings.TrimSpace(privateKeyPEM) == "" {
		return nil, errors.New("mesh: private key not configured")
	}
	if passphrase == "" {
		return gossh.ParsePrivateKey([]byte(privateKeyPEM))
	}
	return gossh.ParsePrivateKeyWithPassphrase([]byte(privateKeyPEM), []byte(passphrase))
}
