package services

// 设备互访阶段 2 增量 2——LAN 直连文件数据面(spec 2026-10-29 §6「LAN 检测与
// 协商选路」)。
//
// 架构定位(spec §5.3 钉死,改动前重读):COS 对象仍是真值源,B 照旧
// transfer.upload;直连只加速 A 的**接收腿**。本文件是这条腿的两端:
//
//   B 端(源设备):peer.lan.listen → 一次性临时 TLS listener,自签证书 +
//   服务端签发的单用途短时 token 把门;A 连上来、token 对上,就把源文件
//   流过去。一个连接一生,TTL 到点自动收摊。
//   A 端(目标设备):transfer.receive 帧里的 direct_candidates[lan_direct] →
//   拨号、钉扎证书指纹、握手、收流;任何失败(拨号/握手/中途)都由
//   agent_transfer_receive.go 静默落回 COS 预签名下载——「现链路零改动」。
//
// 凭据模型(零新增凭据面,spec §6.1):
//   - token:服务端随机 32B,只经信令帧走 B(内存单份),≤30s 过期 + 单连接
//     单用途;A 的连接凭 B 手里的副本核验,服务端不参与在线校验。
//   - 指纹:B 每次协商现签 ECDSA P-256 自签证书,fingerprint=sha256(DER);
//     A 拿候选里的指纹钉扎(InsecureSkipVerify+手动比对),防 LAN 内嗅探/仿冒。
//
// 边界(v1 明确从简,spec §6.3):
//   - 目录归档不走直连(zip 打包兜不住单用途短时语义,服务端也不会发候选)。
//   - 不支持续传:partial 已有偏移时直接放弃 LAN 腿走 COS。
//   - 无 listener 注册表/取消联动:transfer.cancel 依赖 TTL+单连接自然收摊
//     (≤30s+传输宽限),不追杀在途连接。

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

const (
	// agentLanHandshakeTimeout 是 A↔B TLS 握手 + 首行 JSON 交换的围栏。
	agentLanHandshakeTimeout = 5 * time.Second
	// agentLanHeadlineLimit 是握手首行 JSON 的读上限(token/transfer_id 都是
	// KB 级以内,4KiB 已是百倍裕量)。
	agentLanHeadlineLimit = 4 << 10
	// agentLanServeGrace 是 TTL 到点后再给的传输宽限:握手成功即开传,短 TTL
	// 不允许把大文件拦腰掐死(GB 级 LAN 也就秒级~十几秒)。
	agentLanServeGrace = 15 * time.Minute
	// agentLanDialTimeout 是 A 侧拨号围栏(probe/connect 失败即落 COS)。
	agentLanDialTimeout = 5 * time.Second
)

// agentLanDirectCandidate 是 transfer.receive.direct_candidates[] 里的
// lan_direct 条目(A 侧视角)。
type agentLanDirectCandidate struct {
	Addr        string
	Token       string
	Fingerprint string
}

// parseAgentLanDirectCandidates 从 receive 帧提取第一个合法 lan_direct 候选;
// 无/畸形(地址非 IPv4 字面量、指纹长度不符、token 空超长)→ nil(纯 COS)。
// spec §5.4 注意:目标限 IPv4 字面量,域名解析面一律不进直连腿。
func parseAgentLanDirectCandidates(msg map[string]interface{}) *agentLanDirectCandidate {
	raw, ok := msg["direct_candidates"].([]interface{})
	if !ok {
		return nil
	}
	for _, item := range raw {
		entry, ok := item.(map[string]interface{})
		if !ok || remoteString(entry, "type") != "lan_direct" {
			continue
		}
		addr := remoteString(entry, "addr")
		token := remoteString(entry, "token")
		fingerprint := remoteString(entry, "fingerprint")
		if addr == "" || token == "" || len(token) > 128 || len(fingerprint) != 64 {
			continue
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil || net.ParseIP(host) == nil || net.ParseIP(host).To4() == nil {
			continue
		}
		if portNum, err := net.LookupPort("tcp", port); err != nil || portNum <= 0 {
			continue
		}
		return &agentLanDirectCandidate{Addr: addr, Token: token, Fingerprint: strings.ToLower(fingerprint)}
	}
	return nil
}

// ---------------------------------------------------------------------------
// B 端:peer.lan.listen → 一次性临时 TLS listener
// ---------------------------------------------------------------------------

// agentLanListenResultPayload 组 peer.lan.listen.result 帧。
func agentLanListenResultPayload(requestID string, ok bool, addr, fingerprint, failReason string) map[string]interface{} {
	payload := map[string]interface{}{
		"type":       models.AgentEventPeerLanListenResult,
		"request_id": requestID,
		"ok":         ok,
	}
	if ok {
		payload["addr"] = addr
		payload["fingerprint"] = fingerprint
	} else if failReason != "" {
		payload["fail_reason"] = failReason
	}
	return payload
}

// generateAgentLanCertificate 每次协商现签一张 ECDSA P-256 自签证书(1h 有效),
// 返回 tls.Certificate 与指纹(sha256 hex of DER)。现签而非进程级复用:指纹
// 一次性随候选下发,被盗用面收敛到单次协商窗口。
func generateAgentLanCertificate() (tls.Certificate, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, "", err
	}
	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "aliang-lan-transfer"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	sum := sha256.Sum256(der)
	return cert, hex.EncodeToString(sum[:]), nil
}

// agentLanAdvertiseAddr 决定候选里的地址:服务端提示 IP(两端同子网的重叠对,
// spec §6.1)仍在且非回环就优先用它,否则退首个非回环 IPv4(增量 1 的
// collectAgentNetSnapshot,2s TTL 缓存)。都拿不到 → 空串(放弃协商)。
func agentLanAdvertiseAddr(hintIP string, port int) string {
	snap := collectAgentNetSnapshot()
	preferred := strings.TrimSpace(hintIP)
	for _, iface := range snap.Interfaces {
		ip := net.ParseIP(iface.IPv4)
		if ip == nil || ip.IsLoopback() {
			continue
		}
		if preferred != "" && iface.IPv4 == preferred {
			return net.JoinHostPort(iface.IPv4, fmt.Sprintf("%d", port))
		}
	}
	for _, iface := range snap.Interfaces {
		ip := net.ParseIP(iface.IPv4)
		if ip == nil || ip.IsLoopback() {
			continue
		}
		return net.JoinHostPort(iface.IPv4, fmt.Sprintf("%d", port))
	}
	return ""
}

// agentLanServeOnce 是 listener 的服务体:恰好处理一条连接。token 常量时间
// 比对,不匹配静默断开(不给探测者任何回显);匹配则回首行元数据再流文件。
func agentLanServeOnce(conn net.Conn, tlsCert tls.Certificate, expectedToken, expectedTransferID, filePath string, fileSize int64) {
	defer conn.Close()
	// 握手围栏:慢连接/半开连接最多占 5s;开传后再给传输宽限。
	conn.SetDeadline(time.Now().Add(agentLanHandshakeTimeout))
	tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{tlsCert}})
	if err := tlsConn.HandshakeContext(context.Background()); err != nil {
		logger.Info(fmt.Sprintf("[AGENT-PEER-LAN] handshake failed: %v", err))
		return
	}
	reader := bufio.NewReader(io.LimitReader(tlsConn, agentLanHeadlineLimit))
	line, err := reader.ReadBytes('\n')
	if err != nil || len(line) > agentLanHeadlineLimit {
		logger.Info("[AGENT-PEER-LAN] headline read failed or oversized")
		return
	}
	var hello struct {
		Token      string `json:"token"`
		TransferID string `json:"transfer_id"`
	}
	if err := json.Unmarshal(line, &hello); err != nil {
		return
	}
	// 常量时间比对 token;transfer_id 须同帧。任一不符:静默断开。
	if subtle.ConstantTimeCompare([]byte(hello.Token), []byte(expectedToken)) != 1 ||
		hello.TransferID != expectedTransferID {
		logger.Info("[AGENT-PEER-LAN] token/transfer_id mismatch, dropping")
		return
	}
	file, err := os.Open(filePath)
	if err != nil {
		return
	}
	defer file.Close()
	// 供给前再核一次文件大小:源文件在 B 上传后被改动 → 拒服(A 落 COS,
	// 那边有上传时登记的 sha/size 做真值)。
	stat, err := file.Stat()
	if err != nil || (fileSize > 0 && stat.Size() != fileSize) {
		logger.Info(fmt.Sprintf("[AGENT-PEER-LAN] file size drift path=%q stat=%d want=%d",
			filePath, statSizeSafe(stat), fileSize))
		return
	}
	// 传输宽限覆盖 conn deadline:握手 5s 之外的流量窗口按文件大小给足。
	conn.SetDeadline(time.Now().Add(agentLanServeGrace))
	// 首行:元数据(A 据此做 size 闸),随后是原始字节流。
	head, _ := json.Marshal(map[string]interface{}{"ok": true, "size_bytes": stat.Size()})
	if _, err := tlsConn.Write(append(head, '\n')); err != nil {
		return
	}
	written, err := io.CopyN(tlsConn, file, stat.Size())
	logger.Info(fmt.Sprintf("[AGENT-PEER-LAN] served path=%q bytes=%d err=%v", filePath, written, err))
}

func statSizeSafe(stat os.FileInfo) int64 {
	if stat == nil {
		return -1
	}
	return stat.Size()
}

// handleAgentPeerLanListen 是 B 端帧入口(server→B,经 WS 显式臂 safeGo 调度)。
// 立即回 result 帧(listener 绑定成功即回,服务过程异步),server 侧 RPC 围栏
// 6s 只等绑定不等传输。
func (s *AgentService) handleAgentPeerLanListen(msg map[string]interface{}, writeJSON func(interface{}) error) {
	requestID := remoteString(msg, "request_id")
	if requestID == "" {
		logger.Warn("[AGENT-PEER-LAN] listen request dropped reason=missing_request_id")
		return
	}
	fail := func(reason string) {
		_ = writeJSON(agentLanListenResultPayload(requestID, false, "", "", reason))
	}

	transferID := remoteString(msg, "transfer_id")
	if !validAgentTransferID(transferID) {
		fail("bad_transfer_id")
		return
	}
	token := remoteString(msg, "token")
	if token == "" || len(token) > 128 {
		fail("bad_token")
		return
	}
	filePath := remoteString(msg, "source_path")
	if filePath == "" {
		fail("missing_source_path")
		return
	}
	fileSize := agentTransferInt64(msg, "size_bytes")
	stat, err := os.Stat(filePath)
	if err != nil || stat.IsDir() {
		fail("source_unreadable")
		return
	}
	if fileSize > 0 && stat.Size() != fileSize {
		// 源文件与登记大小漂移:拒服,A 落 COS(COS 上的对象是上传时冻结的)。
		fail("size_drift")
		return
	}
	ttlSeconds := agentTransferInt64(msg, "ttl_seconds")
	if ttlSeconds <= 0 || ttlSeconds > 30 {
		// 服务端契约 ≤30s(spec §6.1);越界按 30s 收敛而不是拒帧。
		ttlSeconds = 30
	}

	tlsCert, fingerprint, err := generateAgentLanCertificate()
	if err != nil {
		fail("cert_generate_failed")
		return
	}
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		fail("listen_failed")
		return
	}
	port := listener.Addr().(*net.TCPAddr).Port
	addr := agentLanAdvertiseAddr(remoteString(msg, "advertise_ip"), port)
	if addr == "" {
		_ = listener.Close()
		fail("no_lan_address")
		return
	}
	// accept 围栏:TTL 到点无人来就收摊(单用途,连过的连接由 serveOnce 兜)。
	if tcpListener, ok := listener.(*net.TCPListener); ok {
		_ = tcpListener.SetDeadline(time.Now().Add(time.Duration(ttlSeconds) * time.Second))
	}

	logger.Info(fmt.Sprintf("[AGENT-PEER-LAN] listening addr=%s transfer_id=%s ttl=%ds", addr, transferID, ttlSeconds))
	go func() {
		// 单用途:第一条连接(无论成败)后即关摊;accept 超时同样关摊。
		conn, acceptErr := listener.Accept()
		_ = listener.Close()
		if acceptErr != nil {
			logger.Info(fmt.Sprintf("[AGENT-PEER-LAN] accept window closed transfer_id=%s err=%v", transferID, acceptErr))
			return
		}
		agentLanServeOnce(conn, tlsCert, token, transferID, filePath, stat.Size())
	}()

	_ = writeJSON(agentLanListenResultPayload(requestID, true, addr, fingerprint, ""))
}

// ---------------------------------------------------------------------------
// A 端:direct_candidates[lan_direct] 直连接收
// ---------------------------------------------------------------------------

var errAgentTransferLanSkip = errors.New("lan direct skipped")

// classifyAgentLanFailStage 把直连失败映射到 spec §6.2 的阶段枚举
// (probe|connect|mid_transfer),只用于日志与审计回带——A 端行为统一是落 COS。
func classifyAgentLanFailStage(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "lan dial"):
		return "probe"
	case strings.Contains(msg, "handshake"), strings.Contains(msg, "headline"), strings.Contains(msg, "refused"):
		return "connect"
	case errors.Is(err, errAgentTransferShaMismatch), errors.Is(err, errAgentTransferSizeMismatch):
		return "mid_transfer"
	default:
		return "mid_transfer"
	}
}

// agentLanTLSFingerprintVerifier 构造指纹钉扎校验器(InsecureSkipVerify 关掉
// 系统链校验,自签证书只认候选里那一张——双向凭据的 A 侧一半)。
func agentLanTLSFingerprintVerifier(fingerprint string) func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("lan peer presented no certificate")
		}
		sum := sha256.Sum256(rawCerts[0])
		got := hex.EncodeToString(sum[:])
		if subtle.ConstantTimeCompare([]byte(got), []byte(fingerprint)) != 1 {
			return fmt.Errorf("lan peer fingerprint mismatch: got %s", got[:16])
		}
		return nil
	}
}

// attemptAgentTransferLANDownload 走 lan_direct 腿下载到 partialPath(全新下载,
// 偏移必须为 0)。成功返回 (size, sha, nil);任何失败返回非 nil error,由调用方
// 决定落 COS——本函数绝不部分成功。
func attemptAgentTransferLANDownload(ctx context.Context, ident agentTransferIdentity, cand *agentLanDirectCandidate, partialPath string, writeJSON func(interface{}) error) (int64, string, error) {
	if ident.IsDirectoryArchive {
		return 0, "", fmt.Errorf("%w: archive transfers stay on COS", errAgentTransferLanSkip)
	}
	if strings.TrimSpace(ident.SHA256) == "" {
		return 0, "", fmt.Errorf("%w: missing registered sha256", errAgentTransferLanSkip)
	}
	if offset, err := agentTransferPartialOffset(partialPath); err == nil && offset > 0 {
		// 续传不在直连腿语义内(v1 从简):有残留就交给 COS 的 Range 续传。
		return 0, "", fmt.Errorf("%w: partial exists at offset %d", errAgentTransferLanSkip, offset)
	}
	dialer := net.Dialer{Timeout: agentLanDialTimeout}
	rawConn, err := dialer.DialContext(ctx, "tcp", cand.Addr)
	if err != nil {
		return 0, "", fmt.Errorf("lan dial %s failed: %w", cand.Addr, err)
	}
	defer rawConn.Close()

	tlsConn := tls.Client(rawConn, &tls.Config{
		InsecureSkipVerify:    true, // 自签生态:校验走 VerifyPeerCertificate 指纹钉扎
		VerifyPeerCertificate: agentLanTLSFingerprintVerifier(cand.Fingerprint),
	})
	// 握手 + 首行交换共用 5s 围栏;开传后放开(ctx 取消仍是总闸)。
	rawConn.SetDeadline(time.Now().Add(agentLanHandshakeTimeout))
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return 0, "", fmt.Errorf("lan tls handshake failed: %w", err)
	}
	hello, _ := json.Marshal(map[string]interface{}{"token": cand.Token, "transfer_id": ident.TransferID})
	if _, err := tlsConn.Write(append(hello, '\n')); err != nil {
		return 0, "", fmt.Errorf("lan hello write failed: %w", err)
	}
	reader := bufio.NewReader(io.LimitReader(tlsConn, agentLanHeadlineLimit))
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return 0, "", fmt.Errorf("lan headline read failed: %w", err)
	}
	var head struct {
		Ok        bool  `json:"ok"`
		SizeBytes int64 `json:"size_bytes"`
	}
	if err := json.Unmarshal(line, &head); err != nil || !head.Ok {
		return 0, "", errors.New("lan peer refused handshake")
	}
	if ident.SizeBytes > 0 && head.SizeBytes != ident.SizeBytes {
		return 0, "", fmt.Errorf("lan size mismatch: advertised %d, registered %d: %w", head.SizeBytes, ident.SizeBytes, errAgentTransferSizeMismatch)
	}
	// 开传:传输宽限。ctx 取消(transfer.cancel / 进程退出)仍即时生效。
	rawConn.SetDeadline(time.Now().Add(agentLanServeGrace))

	file, err := os.OpenFile(partialPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()

	hasher := sha256.New()
	var received int64
	throttle := newAgentTransferThrottle(ident.SizeBytes)
	body := &progressReader{
		r: tlsConn,
		n: &received,
		onRead: func() {
			if throttle.shouldEmit(atomic.LoadInt64(&received), time.Now()) {
				_ = writeJSON(agentTransferProgressPayload(ident.RequestID, ident.TransferID, agentTransferDirectionReceive, atomic.LoadInt64(&received), ident.SizeBytes, 0))
			}
		},
	}
	var limited io.Reader = body
	if ident.MaxBytes > 0 {
		limited = io.LimitReader(body, ident.MaxBytes+1)
	}
	written, copyErr := io.Copy(io.MultiWriter(file, hasher), limited)
	if ident.MaxBytes > 0 && written > ident.MaxBytes {
		return 0, "", fmt.Errorf("%w: received %d exceeds max_bytes %d", errAgentTransferTooLarge, written, ident.MaxBytes)
	}
	if copyErr != nil {
		return 0, "", fmt.Errorf("lan stream failed after %d bytes: %w", written, copyErr)
	}
	if written != head.SizeBytes {
		return 0, "", fmt.Errorf("lan truncated: received %d, advertised %d", written, head.SizeBytes)
	}
	total := written
	if ident.SizeBytes > 0 && total != ident.SizeBytes {
		return 0, "", fmt.Errorf("size mismatch: received %d, expected %d: %w", total, ident.SizeBytes, errAgentTransferSizeMismatch)
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(sum, strings.TrimSpace(ident.SHA256)) {
		return total, sum, fmt.Errorf("%w: got %s want %s", errAgentTransferShaMismatch, sum, ident.SHA256)
	}
	_ = writeJSON(agentTransferProgressPayload(ident.RequestID, ident.TransferID, agentTransferDirectionReceive, total, ident.SizeBytes, 0))
	return total, sum, nil
}
