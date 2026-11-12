// tls.go 承载 punch QUIC 隧道的信任件:双向证书指纹钉扎(自签 1h 证书、
// 不进 RootCAs、无 TOFU 回退的 fail-closed 校验)与 token 首流门禁。
// 证书机器不在本包——B/A 两侧各自由 services 层的
// generateAgentLanCertificate 现签(agent_peer_lan.go:130,直接返回
// tls.Certificate+指纹,无 PEM 中转),本包只收最终件做钉扎装配;指纹
// 格式与 agentLan 生态同构(叶子 DER 的 sha256 小写 hex),靠约定对齐
// 而非 import——services 依赖 punch,方向单向,punch 保持零 app/http
// 依赖。

package punch

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
)

// punchALPN 是 punch QUIC 隧道的 ALPN。quic-go 要求两端 tls.Config 的
// NextProtos 非空且一致,常量集中本包,Server/Client 两个构造函数同源
// 写入——ALPN 漂移=握手失败,不给两端各写一串留错位空间。
const punchALPN = "nursor-punch/1"

// punchTokenGateTimeout 是 TokenGate 的读围栏:QUIC 握手成功但连上不说
// 话的慢速占用最多等这么久(照 agentLan 5s 握手围栏口径,agent_peer_
// lan.go:57)。包变量而非配置项,仅为同包测试能调小避免真等 5s(stun.go
// 的 classifyDiscoverTimeout 先例),生产语义是常量。
var punchTokenGateTimeout = 5 * time.Second

// punchTokenLineLimit 是 token 行上限(不含结尾换行)。合法 token 是云端
// 下发的一次性随机串,远短于此;超限即错位或恶意,直接拒——照 agentLan
// 首行 4KB 上限的口径(agent_peer_lan.go:60 agentLanHeadlineLimit)。
const punchTokenLineLimit = 4 << 10

// punchFingerprintHexLen sha256 指纹的 hex 定长(64 字符)。
const punchFingerprintHexLen = sha256.Size * 2

// PunchTLSConfig 收拢 punch 隧道的全部信任件:两侧现签证书 + 两个钉扎
// 指纹 + 一次性 token。同一份配置在 A/B 两端各取所需(services 层组装,
// Task 10):B 侧 listener 用 ServerTLSConfig,A 侧 dialer 用
// ClientTLSConfig,字段共用。
type PunchTLSConfig struct {
	// ServerCert B 侧现签 1h 证书(generateAgentLanCertificate 现签直返,
	// 无 PEM 中转)。
	ServerCert tls.Certificate
	// ClientCert A 侧现签证书:QUIC 双向 mTLS 的客户端一半。
	ClientCert tls.Certificate
	// ClientFingerprint B 钉 A 的指纹:叶子 DER 的 sha256 小写 hex,与
	// agentLan 生态同构(agent_peer_lan.go:130 生成、:347 比对)。
	ClientFingerprint string
	// ServerFingerprint A 钉 B 的指纹:由 session.result 帧从 B 带回。
	ServerFingerprint string
	// ExpectedToken 一次性 token:云端协商下发。TokenGate 门禁显式收参,
	// 这里集中承载便于 services 层一次组装、两端同源。
	ExpectedToken string
}

// ServerTLSConfig 装配 B 侧(listener)的 tls.Config:要求客户端出示
// 证书并钉 ClientFingerprint。ClientAuth 取 RequireAnyClientCert 而非
// RequireAndVerifyClientCert——自签 1h 证书不进 RootCAs,系统链校验必然
// 失败,链校验在这套生态里没有裁决权;信任裁决全权交给
// VerifyPeerCertificate 的指纹钉扎(fail-closed,无 TOFU)。
func ServerTLSConfig(c PunchTLSConfig) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{c.ServerCert},
		NextProtos:   []string{punchALPN},
		ClientAuth:   tls.RequireAnyClientCert,
		// 客户端无证书由 RequireAnyClientCert 先拦;有证书则钉扎裁决。
		VerifyPeerCertificate: punchCertVerifier(c.ClientFingerprint),
	}
}

// ClientTLSConfig 装配 A 侧(dialer)的 tls.Config:出示 ClientCert 并钉
// ServerFingerprint。InsecureSkipVerify 关掉系统链校验(自签生态,校验
// 权在指纹钉扎);verifier 里无证书/指纹串非法/比对失败一律 error——
// fail-closed,无 TOFU 回退,与仓内 hostkey fail-closed 纪律同向。
func ClientTLSConfig(c PunchTLSConfig) *tls.Config {
	return &tls.Config{
		Certificates:          []tls.Certificate{c.ClientCert},
		NextProtos:            []string{punchALPN},
		InsecureSkipVerify:    true,
		VerifyPeerCertificate: punchCertVerifier(c.ServerFingerprint),
	}
}

// punchCertVerifier 构造指纹钉扎校验器,语义照抄 agentLan 的
// agentLanTLSFingerprintVerifier(agent_peer_lan.go:347):对端叶子 DER
// 的 sha256 小写 hex 与钉扎值常量时间比对。三重 fail-closed:对端无
// 证书、钉扎串非法(非 64 位 hex)、比对失败,一律返回 error 使握手
// 失败。钉扎值比对前统一小写——services 层下发本就是小写 hex,宽松
// 半档只为 wire 大小写漂移不至于炸握手(agentLan 候选侧 ToLower 同款
// 防御,agent_peer_lan.go:105)。
func punchCertVerifier(pinned string) func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("punch tls: 对端未出示证书")
		}
		if !validPunchFingerprint(pinned) {
			return fmt.Errorf("punch tls: 钉扎指纹非法(须 %d 位 hex)", punchFingerprintHexLen)
		}
		sum := sha256.Sum256(rawCerts[0])
		got := hex.EncodeToString(sum[:])
		if subtle.ConstantTimeCompare([]byte(got), []byte(strings.ToLower(pinned))) != 1 {
			// 回显 got 前 16 位只为日志取证,与 agentLan verifier 同款。
			return fmt.Errorf("punch tls: 指纹不匹配 got=%s", got[:16])
		}
		return nil
	}
}

// validPunchFingerprint 校验钉扎指纹串形态:恰 64 位 hex(大小写都收,
// 比对前统一小写)。空串、错长、非 hex 一律 false——verifier 据此
// fail-closed,配置错误在握手面炸出来而不是静默放行。
func validPunchFingerprint(fp string) bool {
	if len(fp) != punchFingerprintHexLen {
		return false
	}
	for i := 0; i < len(fp); i++ {
		c := fp[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}

// TokenGate 是 punch 隧道的第一道应用层门禁:QUIC 握手与双向指纹钉扎
// 都过了之后,A 在首条流上先写一行 token,B 侧调用本函数读一行、常量
// 时间比对——照抄 agentLan 首行门禁语义(agent_peer_lan.go:204)。
//
// 分工铁律:本函数只判定,不关门——比对失败返回 error,关流/关连接由
// 调用方(listener,Task 6)负责;比对通过则清零读围栏、把流原样交还
// (Task 6 语义:过门后的 stream 就是一条裸连接,后续字节属上层协议)。
//
// 读法刻意逐字节读到 '\n' 为止而不是 bufio:bufio 会预读缓冲,把紧随
// token 行到达的字节(如 SSH 客户端的版本串)吞进自己的缓冲里丢给无人
// 认领——quic.Stream 内部自有接收缓冲,逐字节读没有系统调用放大。
// 行尾容忍一档 '\r'(净效果等同按行读),期望 token 为空串时直接拒:
// 空串对空串在常量时间比对里是「相等」,门禁不能因此形同虚设。
func TokenGate(stream *quic.Stream, token string) error {
	if stream == nil {
		return errors.New("punch token gate: stream 为 nil")
	}
	if token == "" {
		return errors.New("punch token gate: 期望 token 为空串——空串过门=门禁形同虚设,fail-closed")
	}
	if err := stream.SetReadDeadline(time.Now().Add(punchTokenGateTimeout)); err != nil {
		return fmt.Errorf("punch token gate: 设读围栏失败: %w", err)
	}
	line := make([]byte, 0, 128)
	one := make([]byte, 1)
	for {
		n, err := stream.Read(one)
		if err != nil {
			return fmt.Errorf("punch token gate: 读 token 行失败: %w", err)
		}
		if n == 0 {
			continue
		}
		if one[0] == '\n' {
			break
		}
		line = append(line, one[0])
		if len(line) > punchTokenLineLimit {
			return errors.New("punch token gate: token 行超限")
		}
	}
	received := strings.TrimSuffix(string(line), "\r")
	if subtle.ConstantTimeCompare([]byte(received), []byte(token)) != 1 {
		return errors.New("punch token gate: token 不匹配")
	}
	if err := stream.SetReadDeadline(time.Time{}); err != nil {
		return fmt.Errorf("punch token gate: 清读围栏失败: %w", err)
	}
	return nil
}
