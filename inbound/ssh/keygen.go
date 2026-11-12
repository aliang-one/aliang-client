package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// GeneratedIngressKey 是「一键生成并启用」的人用密钥对载荷(spec
// 2026-11-12 §1.1)。json tag 一律 snake_case,与 hostkey 载荷同口径
// (handler 测试断言线格式字段名,防 Go 字段名直出)。
type GeneratedIngressKey struct {
	PublicKey         string `json:"public_key"`         // authorized_keys 单行,含注释尾巴
	PrivateKey        string `json:"private_key"`        // OpenSSH PEM 私钥块原文
	Fingerprint       string `json:"fingerprint"`        // SHA256:... base64(与 host key 指纹同口径)
	Algorithm         string `json:"algorithm"`          // 恒 "ssh-ed25519"
	Comment           string `json:"comment"`            // aliang-ingress-<hostname>-<时间戳>
	SuggestedFilename string `json:"suggested_filename"` // aliang-ingress-<hostname>-<日期>.pem
}

// ingressFilenameUnsafe 把 hostname 里不适合进文件名/Content-Disposition 的
// 字符折叠为 '-'(保守白名单:字母数字点下划线破折号)。
var ingressFilenameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// sanitizeIngressHost 清洗 hostname 供注释与文件名使用:非法段折叠 '-',
// 清洗后为空(全非法字符/纯分隔符)回落 "node",并掐掉首尾 '-'。
func sanitizeIngressHost(host string) string {
	host = strings.TrimSpace(host)
	cleaned := ingressFilenameUnsafe.ReplaceAllString(host, "-")
	cleaned = strings.Trim(cleaned, "-")
	if cleaned == "" {
		return "node"
	}
	return cleaned
}

// GenerateIngressKey 生成一对 ed25519 人用密钥:公钥编为标准 authorized_keys
// 单行(注释 aliang-ingress-<hostname>-<时间戳>,密钥列表里一眼可辨),私钥
// 编为 OpenSSH PEM。与 generateHostKey 同源同款套路(ed25519+MarshalPrivateKey)。
// 纯函数无副作用:不读不写任何文件、不碰 config、不计数——调用方(端点)
// 只把结果返回一次,私钥服务端不落盘(spec §2 已拍板,丢了重新生成)。
func GenerateIngressKey() (*GeneratedIngressKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519: %w", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		return nil, fmt.Errorf("ingress key signer: %w", err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "aliang ingress user key")
	if err != nil {
		return nil, fmt.Errorf("marshal ingress key: %w", err)
	}

	host := "node"
	if h, herr := os.Hostname(); herr == nil && strings.TrimSpace(h) != "" {
		host = sanitizeIngressHost(h)
	}
	now := time.Now()
	comment := fmt.Sprintf("aliang-ingress-%s-%s", host, now.Format("20060102150405"))
	pubLine := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(signer.PublicKey()))) + " " + comment

	return &GeneratedIngressKey{
		PublicKey:         pubLine,
		PrivateKey:        string(pem.EncodeToMemory(block)),
		Fingerprint:       gossh.FingerprintSHA256(signer.PublicKey()),
		Algorithm:         signer.PublicKey().Type(),
		Comment:           comment,
		SuggestedFilename: fmt.Sprintf("aliang-ingress-%s-%s.pem", host, now.Format("20060102")),
	}, nil
}
