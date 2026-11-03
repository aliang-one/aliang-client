package services

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

func fileWriteSHA(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// 覆盖已有文件:内容替换、mode 沿用、结果带 sha/size。
func TestAgentFileWriteOverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "main.go")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := agentFileWritePayload(map[string]interface{}{
		"request_id": "req-1", "project_path": dir, "path": "main.go", "content": "new content",
	})
	if got["ok"] != true {
		t.Fatalf("want ok, got %v", got)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "new content" {
		t.Fatalf("content not replaced: %q", data)
	}
	info, _ := os.Stat(target)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode not preserved: %v", info.Mode().Perm())
	}
	if got["sha256"] != fileWriteSHA(t, target) || got["size_bytes"] != int64(len("new content")) {
		t.Fatalf("bad result payload: %v", got)
	}
	if got["type"] != models.AgentEventFileWriteResult || got["request_id"] != "req-1" {
		t.Fatalf("bad envelope: %v", got)
	}
}

// 新建文件:父目录(=TempDir)已存在,落 0644。
func TestAgentFileWriteCreateNew(t *testing.T) {
	dir := t.TempDir()
	got := agentFileWritePayload(map[string]interface{}{
		"request_id": "req-2", "project_path": dir, "path": "new.txt", "content": "hello",
	})
	if got["ok"] != true {
		t.Fatalf("want ok, got %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

// 父目录缺失 → target_not_found(不做递归 mkdir)。
func TestAgentFileWriteParentMissing(t *testing.T) {
	dir := t.TempDir()
	got := agentFileWritePayload(map[string]interface{}{
		"request_id": "req-3", "project_path": dir, "path": "no/such/dir/f.txt", "content": "x",
	})
	if got["error_code"] != "target_not_found" {
		t.Fatalf("want target_not_found, got %v", got)
	}
}

// 解码后超 512KiB → too_large(与 file.read 的 agentProjectFileReadMaxByte 对称)。
func TestAgentFileWriteTooLarge(t *testing.T) {
	dir := t.TempDir()
	got := agentFileWritePayload(map[string]interface{}{
		"request_id": "req-4", "project_path": dir, "path": "big.txt",
		"content": strings.Repeat("a", 512*1024+1),
	})
	if got["error_code"] != "too_large" {
		t.Fatalf("want too_large, got %v", got)
	}
}

// 未知编码 → bad_encoding;base64 往返成功。
func TestAgentFileWriteEncoding(t *testing.T) {
	dir := t.TempDir()
	bad := agentFileWritePayload(map[string]interface{}{
		"request_id": "req-5", "project_path": dir, "path": "a.txt", "content": "x", "encoding": "hex",
	})
	if bad["error_code"] != "bad_encoding" {
		t.Fatalf("want bad_encoding, got %v", bad)
	}

	target := filepath.Join(dir, "bin.dat")
	raw := []byte{0x00, 0xff, 0xfe, 0x01}
	enc := base64.StdEncoding.EncodeToString(raw)
	ok := agentFileWritePayload(map[string]interface{}{
		"request_id": "req-6", "project_path": dir, "path": "bin.dat", "content": enc, "encoding": "base64",
	})
	if ok["ok"] != true {
		t.Fatalf("base64 write failed: %v", ok)
	}
	gotBytes, _ := os.ReadFile(target)
	if string(gotBytes) != string(raw) {
		t.Fatalf("base64 roundtrip mismatch")
	}
}

// expected_sha256 乐观锁:不符 → conflict;相符 → 成功。
func TestAgentFileWriteConflict(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "c.txt")
	if err := os.WriteFile(target, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	conflict := agentFileWritePayload(map[string]interface{}{
		"request_id": "req-7", "project_path": dir, "path": "c.txt", "content": "v2",
		"expected_sha256": "deadbeef",
	})
	if conflict["error_code"] != "conflict" {
		t.Fatalf("want conflict, got %v", conflict)
	}
	if data, _ := os.ReadFile(target); string(data) != "v1" {
		t.Fatal("conflict must not mutate the file")
	}

	good := agentFileWritePayload(map[string]interface{}{
		"request_id": "req-8", "project_path": dir, "path": "c.txt", "content": "v2",
		"expected_sha256": fileWriteSHA(t, target),
	})
	if good["ok"] != true {
		t.Fatalf("want ok with matching sha, got %v", good)
	}
}

// 悬空 symlink 拒写(无法验证落点在守卫内)→ path_forbidden。
func TestAgentFileWriteDanglingSymlinkRejected(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "evil")
	if err := os.Symlink(filepath.Join(dir, "nonexistent-target"), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	got := agentFileWritePayload(map[string]interface{}{
		"request_id": "req-9", "project_path": dir, "path": "evil", "content": "x",
	})
	if got["error_code"] != "path_forbidden" {
		t.Fatalf("want path_forbidden, got %v", got)
	}
}

// content 键缺失=畸形帧 → bad_request(防静默清空目标文件);显式空串=合法清空。
func TestAgentFileWriteContentPresence(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := agentFileWritePayload(map[string]interface{}{
		"request_id": "req-miss", "project_path": dir, "path": "f.txt",
	})
	if missing["error_code"] != "bad_request" {
		t.Fatalf("want bad_request, got %v", missing)
	}
	if data, _ := os.ReadFile(target); string(data) != "keep" {
		t.Fatal("malformed frame must not mutate the file")
	}
	cleared := agentFileWritePayload(map[string]interface{}{
		"request_id": "req-clear", "project_path": dir, "path": "f.txt", "content": "",
	})
	if cleared["ok"] != true {
		t.Fatalf("explicit empty content must be allowed, got %v", cleared)
	}
	if data, _ := os.ReadFile(target); len(data) != 0 {
		t.Fatalf("explicit clear failed: %q", data)
	}
}
