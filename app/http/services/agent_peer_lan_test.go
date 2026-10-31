package services

// 设备互访阶段 2 增量 2(spec §6)LAN 直连数据面单测:候选解析容错、B 端
// listener 一次一生/token 常量时间核验/大小漂移拒服、A 端指纹钉扎直连往返、
// sha 失配与失败阶段分类。真 TLS 真端口(listener 绑 :0),无 mock 网络。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustWriteLanFixture(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "capture.pcap")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func lanListenMsg(requestID, transferID, token, path string, size int64, hintIP string) map[string]interface{} {
	msg := map[string]interface{}{
		"type": "peer.lan.listen", "request_id": requestID, "transfer_id": transferID,
		"token": token, "source_path": path, "size_bytes": float64(size),
	}
	if hintIP != "" {
		msg["advertise_ip"] = hintIP
	}
	return msg
}

func TestParseAgentLanDirectCandidates(t *testing.T) {
	if got := parseAgentLanDirectCandidates(map[string]interface{}{}); got != nil {
		t.Fatalf("empty frame must yield nil, got %+v", got)
	}
	good := map[string]interface{}{
		"direct_candidates": []interface{}{
			map[string]interface{}{"type": "relay_cos"},
			map[string]interface{}{"type": "lan_direct", "addr": "192.168.1.10:4501",
				"token": "tok", "fingerprint": strings.Repeat("ab", 32)},
		},
	}
	cand := parseAgentLanDirectCandidates(good)
	if cand == nil || cand.Addr != "192.168.1.10:4501" || cand.Token != "tok" {
		t.Fatalf("good candidate not parsed: %+v", cand)
	}

	for name, frame := range map[string]map[string]interface{}{
		"ipv6": {"direct_candidates": []interface{}{map[string]interface{}{
			"type": "lan_direct", "addr": "[::1]:4501", "token": "t", "fingerprint": strings.Repeat("ab", 32)}}},
		"bad_port": {"direct_candidates": []interface{}{map[string]interface{}{
			"type": "lan_direct", "addr": "192.168.1.10:0", "token": "t", "fingerprint": strings.Repeat("ab", 32)}}},
		"short_fp": {"direct_candidates": []interface{}{map[string]interface{}{
			"type": "lan_direct", "addr": "192.168.1.10:4501", "token": "t", "fingerprint": "abcd"}}},
		"empty_token": {"direct_candidates": []interface{}{map[string]interface{}{
			"type": "lan_direct", "addr": "192.168.1.10:4501", "token": "", "fingerprint": strings.Repeat("ab", 32)}}},
	} {
		if got := parseAgentLanDirectCandidates(frame); got != nil {
			t.Fatalf("%s candidate must be rejected, got %+v", name, got)
		}
	}
}

// lanServeTestServer 起一次真实的 B 端 listener(handleAgentPeerLanListen),
// 返回 result 帧与候选。listener 单用途,测试内只拨一次。
func lanServeTestServer(t *testing.T, svc *AgentService, content []byte, opts map[string]interface{}) (result map[string]interface{}, cand *agentLanDirectCandidate) {
	t.Helper()
	path := mustWriteLanFixture(t, content)
	sum := sha256.Sum256(content)
	transferID := "tr_lantest0001"
	token := "test-token-1234567890"

	msg := lanListenMsg("req-lan-1", transferID, token, path, int64(len(content)), "")
	for k, v := range opts {
		msg[k] = v
	}
	var captured map[string]interface{}
	writeJSON := func(payload interface{}) error {
		raw, _ := json.Marshal(payload)
		captured = map[string]interface{}{}
		_ = json.Unmarshal(raw, &captured)
		return nil
	}
	svc.handleAgentPeerLanListen(msg, writeJSON)
	if captured == nil {
		t.Fatal("listen result frame not written")
	}
	if captured["ok"] == true {
		cand = &agentLanDirectCandidate{
			Addr:        captured["addr"].(string),
			Token:       token,
			Fingerprint: captured["fingerprint"].(string),
		}
	}
	_ = sum
	return captured, cand
}

func TestAgentPeerLanListen_RoundTripDirectDownload(t *testing.T) {
	svc := peerToolsTestEnv(t, nil)
	content := []byte(strings.Repeat("lan-transfer-bytes-", 4000))
	sum := sha256.Sum256(content)

	result, cand := lanServeTestServer(t, svc, content, nil)
	if result["ok"] != true {
		t.Fatalf("listen failed: %+v", result)
	}
	if cand == nil || cand.Addr == "" || len(cand.Fingerprint) != 64 {
		t.Fatalf("bad candidate: %+v", result)
	}

	partial := filepath.Join(t.TempDir(), "tr_lantest0001.pcap.partial")
	ident := agentTransferIdentity{
		RequestID: "req-lan-1", TransferID: "tr_lantest0001",
		SizeBytes: int64(len(content)), SHA256: hex.EncodeToString(sum[:]),
	}
	size, sha, err := attemptAgentTransferLANDownload(context.Background(), ident, cand, partial, func(interface{}) error { return nil })
	if err != nil {
		t.Fatalf("direct download failed: %v", err)
	}
	if size != int64(len(content)) || sha != hex.EncodeToString(sum[:]) {
		t.Fatalf("download mismatch size=%d sha=%s", size, sha)
	}
	got, err := os.ReadFile(partial)
	if err != nil || string(got) != string(content) {
		t.Fatalf("partial content mismatch err=%v len=%d", err, len(got))
	}

	// 单用途:listener 已收摊,第二条连接拨号/握手必败。
	_, _, err = attemptAgentTransferLANDownload(context.Background(), ident, cand, filepath.Join(t.TempDir(), "second.partial"), func(interface{}) error { return nil })
	if err == nil {
		t.Fatal("second connection must fail (single-use listener)")
	}
}

func TestAgentPeerLanListen_TokenMismatchSilentlyDropped(t *testing.T) {
	svc := peerToolsTestEnv(t, nil)
	content := []byte("payload")
	_, cand := lanServeTestServer(t, svc, content, nil)
	cand.Token = "wrong-token"

	partial := filepath.Join(t.TempDir(), "x.partial")
	ident := agentTransferIdentity{RequestID: "r", TransferID: "tr_lantest0001",
		SizeBytes: int64(len(content)), SHA256: strings.Repeat("ff", 32)}
	_, _, err := attemptAgentTransferLANDownload(context.Background(), ident, cand, partial, func(interface{}) error { return nil })
	if err == nil {
		t.Fatal("mismatched token must fail the download")
	}
	// B 端静默断开(不回 ok:false 头)——错误应归类为 connect 阶段。
	if stage := classifyAgentLanFailStage(err); stage != "connect" {
		t.Fatalf("stage = %s, want connect", stage)
	}
}

func TestAgentPeerLanListen_Refusals(t *testing.T) {
	svc := peerToolsTestEnv(t, nil)
	content := []byte("payload")

	// 大小漂移:登记 size 与磁盘不符 → 拒服(A 落 COS)。
	path := mustWriteLanFixture(t, content)
	result, _ := lanServeTestServer(t, svc, content, map[string]interface{}{"size_bytes": float64(99999)})
	if result["ok"] != false || result["fail_reason"] != "size_drift" {
		t.Fatalf("size drift must refuse: %+v", result)
	}

	// 源文件不存在。
	result, _ = lanServeTestServer(t, svc, content, map[string]interface{}{"source_path": filepath.Join(t.TempDir(), "gone.bin")})
	if result["ok"] != false || result["fail_reason"] != "source_unreadable" {
		t.Fatalf("missing file must refuse: %+v", result)
	}
	_ = path

	// 非法 transfer_id(路径穿越形态)。
	result, _ = lanServeTestServer(t, svc, content, map[string]interface{}{"transfer_id": "../evil"})
	if result["ok"] != false || result["fail_reason"] != "bad_transfer_id" {
		t.Fatalf("bad transfer_id must refuse: %+v", result)
	}
}

func TestAttemptAgentTransferLanDownload_ShaMismatchAndStages(t *testing.T) {
	svc := peerToolsTestEnv(t, nil)
	content := []byte("real-content")
	_, cand := lanServeTestServer(t, svc, content, nil)

	partial := filepath.Join(t.TempDir(), "mismatch.partial")
	ident := agentTransferIdentity{
		RequestID: "r", TransferID: "tr_lantest0001",
		SizeBytes: int64(len(content)), SHA256: strings.Repeat("00", 32), // 故意不符
	}
	_, _, err := attemptAgentTransferLANDownload(context.Background(), ident, cand, partial, func(interface{}) error { return nil })
	if !errors.Is(err, errAgentTransferShaMismatch) {
		t.Fatalf("want sha mismatch, got %v", err)
	}
	if stage := classifyAgentLanFailStage(err); stage != "mid_transfer" {
		t.Fatalf("sha mismatch stage = %s, want mid_transfer", stage)
	}

	// probe 阶段:死端口拨号失败。
	dead := &agentLanDirectCandidate{Addr: "127.0.0.1:1", Token: "t", Fingerprint: strings.Repeat("ab", 32)}
	_, _, err = attemptAgentTransferLANDownload(context.Background(), ident, dead, filepath.Join(t.TempDir(), "dead.partial"), func(interface{}) error { return nil })
	if stage := classifyAgentLanFailStage(err); stage != "probe" {
		t.Fatalf("dial fail stage = %s, want probe (err=%v)", stage, err)
	}
}

func TestAttemptAgentTransferLanDownload_SkipPaths(t *testing.T) {
	// 归档 → skip;partial 已有偏移 → skip(COS 的 Range 续传接管)。
	ident := agentTransferIdentity{RequestID: "r", TransferID: "tr_x", IsDirectoryArchive: true}
	cand := &agentLanDirectCandidate{Addr: "192.168.1.1:1", Token: "t", Fingerprint: strings.Repeat("ab", 32)}
	_, _, err := attemptAgentTransferLANDownload(context.Background(), ident, cand, filepath.Join(t.TempDir(), "a.partial"), func(interface{}) error { return nil })
	if !errors.Is(err, errAgentTransferLanSkip) {
		t.Fatalf("archive must skip, got %v", err)
	}

	dir := t.TempDir()
	partial := filepath.Join(dir, "existing.partial")
	if err := os.WriteFile(partial, []byte("prefix"), 0o644); err != nil {
		t.Fatal(err)
	}
	ident = agentTransferIdentity{RequestID: "r", TransferID: "tr_x"}
	_, _, err = attemptAgentTransferLANDownload(context.Background(), ident, cand, partial, func(interface{}) error { return nil })
	if !errors.Is(err, errAgentTransferLanSkip) {
		t.Fatalf("existing partial must skip, got %v", err)
	}
}

func TestAgentLanAdvertiseAddr_PrefersHint(t *testing.T) {
	// 本机必有非回环 IPv4(增量 1 采集器);提示不存在时也必须给出地址。
	snap := collectAgentNetSnapshot()
	if len(snap.Interfaces) == 0 {
		t.Skip("no non-loopback IPv4 on this host")
	}
	addr := agentLanAdvertiseAddr("203.0.113.99", 4501)
	if addr == "" || !strings.Contains(addr, ":4501") {
		t.Fatalf("advertise addr = %q", addr)
	}
	hinted := agentLanAdvertiseAddr(snap.Interfaces[0].IPv4, 4502)
	if !strings.HasPrefix(hinted, snap.Interfaces[0].IPv4+":") {
		t.Fatalf("hint ip must win: %q", hinted)
	}
	_ = fmt.Sprint()
}
