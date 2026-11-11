// hostkey.go 承担 mesh 出口对端身份的 fail-closed 钉扎:PeerSpec.HostKey
// 空一律拒绝(不做 TOFU,host key 首连未钉扎即断),支持两种配置格式。
// 两种解析分支不许合并(SPEC §10.4)——"SHA256:xxx" 走指纹比对(基 64
// 摘要等价),否则按 authorized_keys 行 ParseAuthorizedKey 后做 Marshal
// 字节比对;指纹分支不解析密钥、字节分支不做摘要,语义与错误信息各自独立。
package mesh

import (
	"bytes"
	"fmt"
	"net"
	"strings"

	gossh "golang.org/x/crypto/ssh"
)

// pinnedHostKey 返回按 want 钉扎的 HostKeyCallback,闭包在 ExitClient
// 构造期创建一次并随缓存键失效——坏配置每次连接都拒绝,不因重试翻案。
func pinnedHostKey(want string) gossh.HostKeyCallback {
	if want == "" {
		return func(string, net.Addr, gossh.PublicKey) error {
			return fmt.Errorf("mesh: peer host key not pinned (fail-closed)")
		}
	}
	if strings.HasPrefix(want, "SHA256:") {
		fp := strings.TrimSpace(want)
		return func(_ string, _ net.Addr, got gossh.PublicKey) error {
			if gossh.FingerprintSHA256(got) != fp {
				return fmt.Errorf("mesh: host key mismatch (fingerprint)")
			}
			return nil
		}
	}
	pinned, _, _, _, err := gossh.ParseAuthorizedKey([]byte(want))
	if err != nil {
		return func(string, net.Addr, gossh.PublicKey) error {
			return fmt.Errorf("mesh: bad host_key config: %w", err)
		}
	}
	wantBytes := pinned.Marshal()
	return func(_ string, _ net.Addr, got gossh.PublicKey) error {
		if !bytes.Equal(got.Marshal(), wantBytes) {
			return fmt.Errorf("mesh: host key mismatch")
		}
		return nil
	}
}
