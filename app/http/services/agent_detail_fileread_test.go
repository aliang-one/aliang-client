package services

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"aliang.one/nursorgate/app/http/models"
)

// fileReadSHA 计算磁盘上完整文件的 sha256,作为乐观锁原料的期望值。
func fileReadSHA(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// 2026-10-28 审计 #5:按 maxBytes 字节截断会把多字节字符切成半个,回包尾部
// 变乱码。trimTruncatedUTF8Tail 必须剥掉不完整的尾部序列;完整 UTF-8 原样。
func TestTrimTruncatedUTF8Tail(t *testing.T) {
	prefix := strings.Repeat("a", 8)
	hello := "你好" // 6 bytes
	raw := append([]byte(prefix), []byte(hello)...)

	// 完整 → 原样
	if got := string(trimTruncatedUTF8Tail(raw)); got != prefix+hello {
		t.Errorf("完整 UTF-8 被改动: %q", got)
	}
	// 切掉「好」的最后一个字节 → 剥掉剩余半个「好」
	cut := raw[:len(raw)-1]
	got := string(trimTruncatedUTF8Tail(cut))
	if !utf8.ValidString(got) {
		t.Errorf("截断修复后仍非法 UTF-8: %q", got)
	}
	if got != prefix+"你" {
		t.Errorf("截断修复结果 = %q, want %q", got, prefix+"你")
	}
	// 中段坏序列不属于尾部截断,原样返回(交由展示层容错)
	broken := []byte{'a', 0xff, 'b'}
	if got := string(trimTruncatedUTF8Tail(broken)); got != string(broken) {
		t.Errorf("中段坏序列被误改: %q", got)
	}
}

// 手机端文件编辑器乐观锁原料(2026-11-04):file.read.result 携带完整文件的
// sha256。小文件必须带键;截断场景 sha 必须是「完整文件」的 hash 而非截断
// 前缀的 hash;超过 8MiB 上限不带键(不做 GB 级磁盘扫描);契约 Optional 登记。

// 小文件(未截断):回包含 sha256 且等于整文件 sha256。
func TestAgentFileReadSha256SmallFile(t *testing.T) {
	dir := t.TempDir()
	name := "note.txt"
	content := bytes.Repeat([]byte("hello editor\n"), 1024) // ~13KiB < 128KiB
	target := filepath.Join(dir, name)
	if err := os.WriteFile(target, content, 0o600); err != nil {
		t.Fatal(err)
	}
	got := agentFileReadPayload(map[string]interface{}{
		"request_id": "req-sha-1", "project_path": dir, "path": name,
	})
	if got["type"] != models.AgentEventFileReadResult || got["request_id"] != "req-sha-1" {
		t.Fatalf("bad envelope: %v", got)
	}
	if got["truncated"] != false {
		t.Fatalf("small file must not be truncated: %v", got["truncated"])
	}
	sha, ok := got["sha256"].(string)
	if !ok {
		t.Fatalf("sha256 key missing or not string: %v", got["sha256"])
	}
	if want := fileReadSHA(t, target); sha != want {
		t.Fatalf("sha256 = %q, want full-file hash %q", sha, want)
	}
}

// 截断文件(200KiB > maxBytes=128KiB 但 ≤ 8MiB):truncated=true 且
// sha256 是完整文件的 hash——不是截断前缀的 hash。这是语义关键断言。
func TestAgentFileReadSha256TruncatedIsFullFileHash(t *testing.T) {
	dir := t.TempDir()
	name := "big.txt"
	content := bytes.Repeat([]byte("abcdefghij"), 20*1024) // 200KiB
	target := filepath.Join(dir, name)
	if err := os.WriteFile(target, content, 0o600); err != nil {
		t.Fatal(err)
	}
	got := agentFileReadPayload(map[string]interface{}{
		"request_id": "req-sha-2", "project_path": dir, "path": name, "max_bytes": 128 * 1024,
	})
	if got["truncated"] != true {
		t.Fatalf("want truncated=true, got %v", got["truncated"])
	}
	sha, ok := got["sha256"].(string)
	if !ok {
		t.Fatalf("sha256 key missing or not string: %v", got["sha256"])
	}
	if want := fileReadSHA(t, target); sha != want {
		t.Fatalf("truncated read must carry FULL-file sha256: got %q, want %q", sha, want)
	}
	// 前缀 hash 与完整 hash 必须不同,证明上一条断言真的区分了两者。
	prefixSum := sha256.Sum256(content[:128*1024])
	if sha == hex.EncodeToString(prefixSum[:]) {
		t.Fatal("sha256 equals the truncated-prefix hash — semantics regressed")
	}
}

// 超过 8MiB 上限:不带 sha256 键(不做全量读,避免 GB 级磁盘扫描)。
// 用 os.Truncate 造稀疏文件,不真写 8MiB+1 字节。
func TestAgentFileReadSha256SkippedOverCap(t *testing.T) {
	dir := t.TempDir()
	name := "huge.bin"
	target := filepath.Join(dir, name)
	if err := os.WriteFile(target, []byte("head"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(target, agentProjectFileReadShaCapByte+1); err != nil {
		t.Fatal(err)
	}
	got := agentFileReadPayload(map[string]interface{}{
		"request_id": "req-sha-3", "project_path": dir, "path": name, "max_bytes": 128 * 1024,
	})
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got["size_bytes"] != info.Size() {
		t.Fatalf("size_bytes = %v, want %d", got["size_bytes"], info.Size())
	}
	if got["truncated"] != true {
		t.Fatalf("oversize file must be truncated: %v", got["truncated"])
	}
	if _, present := got["sha256"]; present {
		t.Fatalf("over-cap file must NOT carry sha256 key: %v", got["sha256"])
	}
}

// 契约锚:file.read.result 的 Optional 必须登记 sha256(手机端按契约渲染)。
func TestAgentFileReadContractRegistersSha256(t *testing.T) {
	contract := models.DefaultAgentProtocolContract()
	find := func(events []models.AgentProtocolEvent, typ string) *models.AgentProtocolEvent {
		for i := range events {
			if events[i].Type == typ {
				return &events[i]
			}
		}
		return nil
	}
	ev := find(contract.WebSocket.ClientSends, models.AgentEventFileReadResult)
	if ev == nil {
		t.Fatal("file.read.result missing from ClientSends")
	}
	for _, o := range ev.Optional {
		if o == "sha256" {
			return
		}
	}
	t.Fatalf("file.read.result Optional must include sha256, got %v", ev.Optional)
}

// 截断 3 字节(整字「你」)也必须收敛干净。
func TestTrimTruncatedUTF8TailWholeRune(t *testing.T) {
	prefix := strings.Repeat("a", 5)
	raw := append([]byte(prefix), []byte("你好")...)
	cut := raw[:len(raw)-3] // 「你」完整保留,「好」整字被切
	got := string(trimTruncatedUTF8Tail(cut))
	if got != prefix+"你" {
		t.Errorf("整字截断结果 = %q, want %q", got, prefix+"你")
	}
}
