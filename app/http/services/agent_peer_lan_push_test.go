package services

// 上传腿 LAN 直连(P1-4,角色互换)单测:候选解析容错、A 端一次性收件
// listener(落盘围栏/marker/ACK)、B 端直推往返/ACK sha 核对、upload handler
// 端到端(push 成功一行 via=lan_push;失败静默落 COS 且 lan_fail_stage 回带)。
// 真 TLS 真端口(listener 绑 :0),无 mock 网络——接收腿测试同款纪律。

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

func lanPushListenMsg(requestID, transferID, token, filename string, over map[string]interface{}) map[string]interface{} {
	msg := map[string]interface{}{
		"type": "peer.lan.push.listen", "request_id": requestID, "transfer_id": transferID,
		"token": token, "filename": filename,
	}
	for k, v := range over {
		msg[k] = v
	}
	return msg
}

// lanPushServeTestServer 起一次真实的 A 端收件 listener
// (handleAgentPeerLanPushListen),返回 result 帧。listener 单用途,只拨一次。
func lanPushServeTestServer(t *testing.T, svc *AgentService, filename string, over map[string]interface{}) (result map[string]interface{}, token string) {
	t.Helper()
	token = "push-token-1234567890"
	msg := lanPushListenMsg("req-push-1", "tr_lanpusht001", token, filename, over)
	var captured map[string]interface{}
	writeJSON := func(payload interface{}) error {
		raw, _ := json.Marshal(payload)
		captured = map[string]interface{}{}
		_ = json.Unmarshal(raw, &captured)
		return nil
	}
	svc.handleAgentPeerLanPushListen(msg, writeJSON)
	if captured == nil {
		t.Fatal("push listen result frame not written")
	}
	return captured, token
}

func pushCandidateFromResult(t *testing.T, result map[string]interface{}, token string) *agentLanDirectCandidate {
	t.Helper()
	if result["ok"] != true {
		t.Fatalf("push listen failed: %+v", result)
	}
	return &agentLanDirectCandidate{
		Addr:        result["addr"].(string),
		Token:       token,
		Fingerprint: result["fingerprint"].(string),
	}
}

func TestParseAgentLanPushCandidates(t *testing.T) {
	if got := parseAgentLanPushCandidates(map[string]interface{}{}); got != nil {
		t.Fatalf("empty frame must yield nil, got %+v", got)
	}
	good := map[string]interface{}{
		"direct_upload_candidates": []interface{}{
			map[string]interface{}{"type": "lan_push", "addr": "192.168.1.20:4501",
				"token": "tok", "fingerprint": strings.Repeat("cd", 32)},
		},
	}
	cand := parseAgentLanPushCandidates(good)
	if cand == nil || cand.Addr != "192.168.1.20:4501" || cand.Token != "tok" {
		t.Fatalf("good push candidate not parsed: %+v", cand)
	}
	for name, frame := range map[string]map[string]interface{}{
		"missing":           {},
		"wrong_type":        {"direct_upload_candidates": []interface{}{map[string]interface{}{"type": "lan_direct", "addr": "192.168.1.20:4501", "token": "t", "fingerprint": strings.Repeat("cd", 32)}}},
		"hostname":          {"direct_upload_candidates": []interface{}{map[string]interface{}{"type": "lan_push", "addr": "peer.local:4501", "token": "t", "fingerprint": strings.Repeat("cd", 32)}}},
		"short_fingerprint": {"direct_upload_candidates": []interface{}{map[string]interface{}{"type": "lan_push", "addr": "192.168.1.20:4501", "token": "t", "fingerprint": "abcd"}}},
	} {
		if got := parseAgentLanPushCandidates(frame); got != nil {
			t.Fatalf("%s push candidate must be rejected, got %+v", name, got)
		}
	}
}

func TestAgentPeerLanPushListen_RoundTripDirectPush(t *testing.T) {
	newAgentTransferEnv(t)
	svc := peerToolsTestEnv(t, nil)
	content := []byte(strings.Repeat("push-leg-bytes-", 4000))

	result, token := lanPushServeTestServer(t, svc, "capture.bin", nil)
	cand := pushCandidateFromResult(t, result, token)

	src := filepath.Join(t.TempDir(), "src.bin")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	ident := agentTransferIdentity{RequestID: "req-push-1", TransferID: "tr_lanpusht001", Filename: "capture.bin"}
	size, sha, finalPath, err := attemptAgentTransferLANPush(context.Background(), ident, cand, src, int64(len(content)), func(interface{}) error { return nil })
	if err != nil {
		t.Fatalf("direct push failed: %v", err)
	}
	sum := sha256.Sum256(content)
	if size != int64(len(content)) || sha != hex.EncodeToString(sum[:]) {
		t.Fatalf("push mismatch size=%d sha=%s", size, sha)
	}
	if finalPath == "" {
		t.Fatal("ack must carry A-side final path")
	}
	got, err := os.ReadFile(finalPath)
	if err != nil || string(got) != string(content) {
		t.Fatalf("landed content mismatch err=%v len=%d", err, len(got))
	}
	if !strings.HasSuffix(finalPath, "capture.bin") {
		t.Fatalf("landed name = %q", finalPath)
	}
	marker, err := readAgentTransferMarker("tr_lanpusht001")
	if err != nil || marker == nil || marker.State != transferMarkerStateDone || marker.Path != finalPath {
		t.Fatalf("done marker missing/mismatch: %+v err=%v", marker, err)
	}

	// 单用途:listener 已收摊,第二条连接拨号必败。
	_, _, _, err = attemptAgentTransferLANPush(context.Background(), ident, cand, src, int64(len(content)), func(interface{}) error { return nil })
	if err == nil {
		t.Fatal("second connection must fail (single-use listener)")
	}
}

func TestAgentPeerLanPushListen_Refusals(t *testing.T) {
	newAgentTransferEnv(t)
	svc := peerToolsTestEnv(t, nil)

	// 非法 transfer_id(路径穿越形态)。
	result, _ := lanPushServeTestServer(t, svc, "a.bin", map[string]interface{}{"transfer_id": "../evil"})
	if result["ok"] != false || result["fail_reason"] != "bad_transfer_id" {
		t.Fatalf("bad transfer_id must refuse: %+v", result)
	}
	// 缺 filename。
	result, _ = lanPushServeTestServer(t, svc, "", nil)
	if result["ok"] != false || result["fail_reason"] != "missing_filename" {
		t.Fatalf("missing filename must refuse: %+v", result)
	}
	// 穿越形 filename(D9 清洗必拒)。
	result, _ = lanPushServeTestServer(t, svc, "../escape.bin", nil)
	if result["ok"] != false || result["fail_reason"] != "bad_filename" {
		t.Fatalf("fence-escaping filename must refuse: %+v", result)
	}
}

func TestAgentPeerLanPush_TokenMismatchRefused(t *testing.T) {
	newAgentTransferEnv(t)
	svc := peerToolsTestEnv(t, nil)
	result, token := lanPushServeTestServer(t, svc, "x.bin", nil)
	src := filepath.Join(t.TempDir(), "src.bin")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	ident := agentTransferIdentity{RequestID: "r", TransferID: "tr_lanpusht001", Filename: "x.bin"}
	cand := pushCandidateFromResult(t, result, token)
	cand.Token = token + "-wrong"
	_, _, _, err := attemptAgentTransferLANPush(context.Background(), ident, cand, src, 7, func(interface{}) error { return nil })
	// A 端 token 不符=静默断开(不给探测者回显)——B 侧 ready 行读到 EOF。
	if err == nil {
		t.Fatal("mismatched token must fail the push")
	}
	if stage := classifyAgentLanFailStage(err); stage != "connect" {
		t.Fatalf("stage = %s, want connect", stage)
	}
}

// fakePushAckServer 起一个可控证书的假 A 端 TLS listener:过握手、放流、
// ACK 回调用方指定的 sha/size——专测 B 端「双端 sha/size 核对」闸与
// mid_transfer 分类。sendSize 是 B 声明并实发的字节数(假端按它定长收流);
// ackSHA/ackSize 是 ACK 里的回值(可故意不符)。返回候选(addr+真指纹)。
func fakePushAckServer(t *testing.T, sendSize int64, ackSHA string, ackSize int64) *agentLanDirectCandidate {
	t.Helper()
	tlsCert, fingerprint, err := generateAgentLanCertificate()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
		tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{tlsCert}})
		if err := tlsConn.HandshakeContext(context.Background()); err != nil {
			return
		}
		reader := bufio.NewReader(io.LimitReader(tlsConn, 4<<10))
		if _, err := reader.ReadBytes('\n'); err != nil { // hello
			return
		}
		_, _ = tlsConn.Write([]byte("{\"ok\":true}\n"))
		_, _ = io.CopyN(io.Discard, tlsConn, sendSize) // 定长收流——B 发完不关流等 ACK
		ack, _ := json.Marshal(map[string]interface{}{"ok": true, "sha256": ackSHA, "size_bytes": ackSize})
		_, _ = tlsConn.Write(append(ack, '\n'))
	}()
	return &agentLanDirectCandidate{
		Addr:        listener.Addr().String(),
		Token:       "tok",
		Fingerprint: fingerprint,
	}
}

func TestAttemptAgentTransferLanPush_AckMismatchAndStages(t *testing.T) {
	newAgentTransferEnv(t)
	src := filepath.Join(t.TempDir(), "src.bin")
	content := []byte("payload")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	ident := agentTransferIdentity{RequestID: "r", TransferID: "tr_acktest001", Filename: "src.bin"}
	sum := sha256.Sum256(content)

	// ACK sha 与自算不符 → mid_transfer。
	cand := fakePushAckServer(t, int64(len(content)), strings.Repeat("00", 32), int64(len(content)))
	_, _, _, err := attemptAgentTransferLANPush(context.Background(), ident, cand, src, int64(len(content)), func(interface{}) error { return nil })
	if !errors.Is(err, errAgentTransferShaMismatch) {
		t.Fatalf("want sha mismatch, got %v", err)
	}
	if stage := classifyAgentLanFailStage(err); stage != "mid_transfer" {
		t.Fatalf("sha mismatch stage = %s, want mid_transfer", stage)
	}

	// ACK size 与声明不符 → mid_transfer。
	cand = fakePushAckServer(t, int64(len(content)), hex.EncodeToString(sum[:]), 999)
	_, _, _, err = attemptAgentTransferLANPush(context.Background(), ident, cand, src, int64(len(content)), func(interface{}) error { return nil })
	if !errors.Is(err, errAgentTransferSizeMismatch) {
		t.Fatalf("want size mismatch, got %v", err)
	}

	// probe 阶段:死端口拨号失败。
	dead := &agentLanDirectCandidate{Addr: "127.0.0.1:1", Token: "t", Fingerprint: strings.Repeat("ab", 32)}
	_, _, _, err = attemptAgentTransferLANPush(context.Background(), ident, dead, src, 7, func(interface{}) error { return nil })
	if stage := classifyAgentLanFailStage(err); stage != "probe" {
		t.Fatalf("dial fail stage = %s, want probe (err=%v)", stage, err)
	}

	// 未知总量(归档 zip 流式打包)→ skip,COS 接管。
	_, _, _, err = attemptAgentTransferLANPush(context.Background(), ident, dead, src, 0, func(interface{}) error { return nil })
	if !errors.Is(err, errAgentTransferLanSkip) {
		t.Fatalf("unknown size must skip, got %v", err)
	}
}

func TestHandleAgentTransferUpload_LanPushSkipsCos(t *testing.T) {
	newAgentTransferEnv(t)
	svc := peerToolsTestEnv(t, nil)
	content := []byte(strings.Repeat("upload-leg-", 500))

	// A 端先起收件 listener(真 TLS)。
	result, token := lanPushServeTestServer(t, svc, "payload.bin", nil)
	cand := pushCandidateFromResult(t, result, token)

	src := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	msg := transferUploadMsg(t, "tr_lanpusht001", src, int64(len(content)), false, 1<<20, map[int]string{1: "http://127.0.0.1:1/put"})
	msg["direct_upload_candidates"] = []interface{}{
		map[string]interface{}{"type": "lan_push", "addr": cand.Addr, "token": cand.Token, "fingerprint": cand.Fingerprint},
	}
	capture, writeJSON := newTransferCapture()
	svc.handleAgentTransferUpload(msg, writeJSON)
	terminal := waitForTransferTerminal(t, capture)
	if terminal["ok"] != true {
		t.Fatalf("upload must succeed: %+v", terminal)
	}
	if terminal["via"] != "lan_push" {
		t.Fatalf("via = %v, want lan_push", terminal["via"])
	}
	if _, hasEtags := terminal["etags"]; hasEtags {
		t.Fatalf("lan_push result must not carry etags: %+v", terminal)
	}
	sum := sha256.Sum256(content)
	if terminal["sha256"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha = %v", terminal["sha256"])
	}
	if path, _ := terminal["path"].(string); path == "" {
		t.Fatal("lan_push result must carry A-side path")
	}
}

func TestHandleAgentTransferUpload_LanPushFallbackToCos(t *testing.T) {
	newAgentTransferEnv(t)
	svc := peerToolsTestEnv(t, nil)
	content := []byte("fallback-bytes")

	fake := newFakeTransferCOS(t, 0)
	src := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	msg := transferUploadMsg(t, "tr_lanpushfb01", src, int64(len(content)), false, 1<<20, map[int]string{1: fake.url() + "/put"})
	// 候选指向死端口:直推 probe 失败,静默落 COS。
	msg["direct_upload_candidates"] = []interface{}{
		map[string]interface{}{"type": "lan_push", "addr": "127.0.0.1:1",
			"token": "tok", "fingerprint": strings.Repeat("ab", 32)},
	}
	capture, writeJSON := newTransferCapture()
	svc.handleAgentTransferUpload(msg, writeJSON)
	terminal := waitForTransferTerminal(t, capture)
	if terminal["ok"] != true {
		t.Fatalf("COS fallback must succeed: %+v", terminal)
	}
	if terminal["via"] != "cos" {
		t.Fatalf("via = %v, want cos", terminal["via"])
	}
	if terminal["lan_fail_stage"] != "probe" {
		t.Fatalf("lan_fail_stage = %v, want probe", terminal["lan_fail_stage"])
	}
	// capture 存的是原始 map(etags 为 []map 形态而非 json 解码形态),经
	// JSON 往返归一后再断言,避免按具体容器类型硬断言。
	normalized := map[string]interface{}{}
	raw, _ := json.Marshal(terminal)
	_ = json.Unmarshal(raw, &normalized)
	etags, _ := normalized["etags"].([]interface{})
	if len(etags) == 0 {
		t.Fatalf("COS fallback must carry etags: %+v", terminal)
	}
}

func TestTransferUploadContractIncludesPushFrames(t *testing.T) {
	contract := models.DefaultAgentProtocolContract()
	sends := contract.WebSocket.ServerSends
	// transfer.upload 的 Optional 必须登记 direct_upload_candidates。
	up := findContractEvent(sends, models.AgentEventTransferUpload)
	if up == nil {
		t.Fatal("transfer.upload entry missing from ServerSends")
	}
	if !hasOptionalField(up, "direct_upload_candidates") {
		t.Fatal("transfer.upload Optional missing direct_upload_candidates")
	}
	// push listen 两帧必须在 ServerSends 登记且 Optional 齐。
	listen := findContractEvent(sends, models.AgentEventPeerLanPushListen)
	if listen == nil {
		t.Fatal("peer.lan.push.listen entry missing from ServerSends")
	}
	if !hasOptionalField(listen, "target_dir") {
		t.Fatal("peer.lan.push.listen Optional missing target_dir")
	}
	if findContractEvent(sends, models.AgentEventPeerLanPushListenResult) == nil {
		t.Fatal("peer.lan.push.listen.result entry missing from ServerSends")
	}
}

func hasOptionalField(ev *models.AgentProtocolEvent, field string) bool {
	if ev == nil {
		return false
	}
	for _, f := range ev.Optional {
		if f == field {
			return true
		}
	}
	return false
}

func findContractEvent(events []models.AgentProtocolEvent, eventType string) *models.AgentProtocolEvent {
	for i := range events {
		if events[i].Type == eventType {
			return &events[i]
		}
	}
	return nil
}

// fakePushStallServer 握手放行后永不读流——B 的 io.Copy 必然阻塞在套接字
// 缓冲满,供取消测试确定性复现「流中途取消」(回环链路太快,真 A 端会先完成)。
func fakePushStallServer(t *testing.T, release <-chan struct{}) *agentLanDirectCandidate {
	t.Helper()
	tlsCert, fingerprint, err := generateAgentLanCertificate()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
		tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{tlsCert}})
		if err := tlsConn.HandshakeContext(context.Background()); err != nil {
			return
		}
		reader := bufio.NewReader(io.LimitReader(tlsConn, 4<<10))
		if _, err := reader.ReadBytes('\n'); err != nil { // hello
			return
		}
		_, _ = tlsConn.Write([]byte("{\"ok\":true}\n"))
		<-release // 永不读流:B 阻塞在套接字缓冲满
	}()
	return &agentLanDirectCandidate{
		Addr:        listener.Addr().String(),
		Token:       "tok",
		Fingerprint: fingerprint,
	}
}

// P1 评审修:直推腿取消语义。ctx 取消必须真断流(watcher 掐 conn deadline),
// 失败映射 context.Canceled 且调用方 emit cancelled——绝不「取消后照常完成」。
func TestAttemptAgentTransferLanPush_CtxCancelMidStream(t *testing.T) {
	newAgentTransferEnv(t)
	_ = peerToolsTestEnv(t, nil)
	release := make(chan struct{})
	defer close(release)
	cand := fakePushStallServer(t, release)

	content := make([]byte, 32<<20) // 32MB:远超套接字缓冲,stall 下 io.Copy 必阻塞
	src := filepath.Join(t.TempDir(), "src.bin")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	ident := agentTransferIdentity{RequestID: "req-cancel-1", TransferID: "tr_cancelst01", Filename: "cancel.bin"}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond) // 流已阻塞,B 悬在 io.Copy
		cancel()
	}()
	_, _, _, err := attemptAgentTransferLANPush(ctx, ident, cand, src, int64(len(content)), func(interface{}) error { return nil })
	cancel()
	if err == nil {
		t.Fatal("cancelled push must fail, got success(取消后照常完成=P1 评审缺陷复现)")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err must wrap context.Canceled, got %v", err)
	}
	// A 端不得留成品/半成品(stall 假端不落盘;此处断言 B 侧收口语义)。
	inbox, err := agentTransferInboxDir()
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(inbox)
	for _, e := range entries {
		if strings.Contains(e.Name(), "cancel.bin") {
			t.Fatalf("cancelled transfer must not leave file in inbox: %s", e.Name())
		}
	}
}

// P1 评审修:A 端 slotCancel 真闸——cancelIfActive 掐 conn deadline,serve 流
// 立即失败、partial 删除、不 ACK。直拨一条裸 TLS 连接模拟 B,读侧在 cancel 后
// 必然断开。
func TestAgentPeerLanPushListen_SlotCancelKillsConn(t *testing.T) {
	newAgentTransferEnv(t)
	svc := peerToolsTestEnv(t, nil)
	result, token := lanPushServeTestServer(t, svc, "slotkill.bin", nil)
	if result["ok"] != true {
		t.Fatalf("push listen failed: %+v", result)
	}

	// 模拟 B:真 TLS 拨号 + 钉扎 + hello,读到 ok 后持连接不放。
	tlsConn := dialPushTestConn(t, result["addr"].(string), result["fingerprint"].(string), token, "tr_lanpusht001", 10)
	defer tlsConn.Close()
	reader := bufio.NewReader(tlsConn)
	if _, err := reader.ReadBytes('\n'); err != nil { // {"ok":true}
		t.Fatalf("ready line: %v", err)
	}

	// 槽在 serve goroutine 手里:cancelIfActive 触发 slotCancel → conn deadline。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if transferReceiveSlot().cancelIfActive("tr_lanpusht001") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// conn 被掐:A 的读循环失败退出并关 conn → B 的后续读收到 EOF/超时。
	tlsConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, werr := tlsConn.Write(make([]byte, 10))
	_ = tlsConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, rerr := tlsConn.Read(make([]byte, 16))
	if werr == nil && rerr == nil {
		t.Fatal("conn must be torn down after slot cancel")
	}
	// partial 不得残留
	inbox, err := agentTransferInboxDir()
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(inbox)
	for _, e := range entries {
		if strings.Contains(e.Name(), "slotkill.bin") {
			t.Fatalf("cancelled transfer must not leave partial: %s", e.Name())
		}
	}
}

// dialPushTestConn 模拟 B 端真 TLS 拨号(钉扎+hello),供 A 侧取消测试。
func dialPushTestConn(t *testing.T, addr, fingerprint, token, transferID string, size int64) *tls.Conn {
	t.Helper()
	rawConn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	tlsConn := tls.Client(rawConn, &tls.Config{
		InsecureSkipVerify:    true,
		VerifyPeerCertificate: agentLanTLSFingerprintVerifier(fingerprint),
	})
	_ = rawConn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := tlsConn.HandshakeContext(context.Background()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	hello, _ := json.Marshal(map[string]interface{}{"token": token, "transfer_id": transferID, "size_bytes": size})
	if _, err := tlsConn.Write(append(hello, '\n')); err != nil {
		t.Fatalf("hello: %v", err)
	}
	return tlsConn
}
