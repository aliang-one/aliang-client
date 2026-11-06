# 设备互访缺陷批次（D1-D4）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复全链路复核发现的四项缺陷——CLI provider 补 peer 五工具、契约表补登记（direct_candidates/error_code）、新增 file.write 帧族（本仓全量）、file.upload upload_url host 钉扎。

**Architecture:** 全部为 additive 契约与客户端行为：MCP bridge 复用既有 payload 函数零新增面；file.write 照 file.read 同源守卫（TCC+路径解析）+原子写+乐观锁；host 钉扎复用互传通道现成的 `agentTransferHostAllowed`。协议不动 protocol_version。

**Tech Stack:** Go 1.x 单体；MCP 官方 go-sdk v1.8.0（bridge）；WS 帧契约表（agent_protocol.go）+「四点齐」契约测试。

**Spec:** `docs/superpowers/specs/2026-11-02-peer-link-defect-batch-design.md`（先读，本计划的唯一需求来源）

**⚠️ 全局纪律（每个 Task 都适用）：**
- **提交纪律（CLAUDE.md，优先级最高）**：计划里的 Commit 步骤**必须在用户明确同意后执行**；用户未点头时跳过该步继续下一任务，最后统一征求提交意愿。
- 所有开发在 **worktree**（Task 0 创建），严禁直接改主检出。
- worktree 里 gopls 全量误报一律无视，以 `go build` / `go test` 为准（memory: worktree 构建两坑）。
- 测试安全：本仓测试自带真机护栏（v1.1.78 起 localUserAgentBaseURL 惰性指 127.0.0.1:1），但**严禁跑带 `-race` 的全量**（services/handlers 两枚基线红，memory: preexisting-race-flakes）。
- 提交信息全中文「新增：/修复：」前缀（memory: commit-message-chinese）。

---

### Task 0: worktree 就位

**Files:** 无代码改动（环境准备）

- [ ] **Step 1: 基于最新 origin/master 建 worktree**（勿假设本地 master 最新——先 fetch 再切）

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
git fetch origin
git worktree add .worktrees/peer-defect-batch -b fix/peer-defect-batch origin/master
```

- [ ] **Step 2: 首次构建前置**（embed 占位，memory: transfer-worktree-build-gotchas）

```bash
cp -R app/website/dist .worktrees/peer-defect-batch/app/website/dist 2>/dev/null || true
cd .worktrees/peer-defect-batch && go build ./... && echo BUILD-OK
```

Expected: `BUILD-OK`（若 dist 不存在导致 embed 报错，确认 cp 成功后重试）

---

### Task 1: D2 — transfer.receive 契约补登记 direct_candidates

**Files:**
- Modify: `app/http/models/agent_protocol.go:335`（AgentEventTransferReceive Optional）
- Test: `app/http/services/agent_transfer_contract_test.go`（文件末尾追加测试函数）

- [ ] **Step 1: 写失败测试**（追加到 `agent_transfer_contract_test.go` 末尾）

```go
// D2（spec 2026-11-02）：transfer.receive 实际携带 direct_candidates
// （agent_transfer_receive.go 经 parseAgentLanDirectCandidates 实读该键），
// 契约表漏登记=「实读键 vs 登记」先例复发。对齐 transfer.result 已登记
// via/lan_fail_stage 的先例。
func TestAgentTransferReceiveDirectCandidatesRegistered(t *testing.T) {
	contract := models.DefaultAgentProtocolContract()
	var rcv *models.AgentProtocolEvent
	for i := range contract.WebSocket.ServerSends {
		if contract.WebSocket.ServerSends[i].Type == models.AgentEventTransferReceive {
			rcv = &contract.WebSocket.ServerSends[i]
			break
		}
	}
	if rcv == nil {
		t.Fatal("transfer.receive missing from ServerSends")
	}
	hasCandidates := false
	for _, f := range rcv.Optional {
		if f == "direct_candidates" {
			hasCandidates = true
		}
	}
	if !hasCandidates {
		t.Fatalf("transfer.receive Optional must include direct_candidates, got %v", rcv.Optional)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestAgentTransferReceiveDirectCandidatesRegistered -v`
Expected: FAIL，`must include direct_candidates`

- [ ] **Step 3: 契约表补登记**——`app/http/models/agent_protocol.go:335` 的 `AgentEventTransferReceive` 行，Optional 从

```go
Optional: []string{"is_directory_archive", "target_dir", "object_etag", "max_bytes", "source"}
```
改为
```go
Optional: []string{"is_directory_archive", "target_dir", "object_etag", "max_bytes", "source", "direct_candidates"}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services/ -run TestAgentTransferReceiveDirectCandidatesRegistered -v`
Expected: PASS

- [ ] **Step 5: Commit**（须用户同意）

```bash
git add app/http/models/agent_protocol.go app/http/services/agent_transfer_contract_test.go
git commit -m "修复:transfer.receive 契约漏登记 direct_candidates(实读键 vs 登记漂移)"
```

---

### Task 2: D3 契约层 — file.write 帧族登记 + file.error 补 error_code

**Files:**
- Modify: `app/http/models/agent_protocol.go`（常量区 :84-91 后；ClientSends :281；ServerSends FileUploadCancel 行后）
- Test: `app/http/services/agent_transfer_contract_test.go`

- [ ] **Step 1: 写失败测试**（追加；注意 `find`/`requireField` 是既有测试**函数内闭包**非包级 helper，本测试自带局部副本）

```go
// D3（spec 2026-11-02）：file.write 帧族「四点齐」——常量/契约条目/Emits/
// 门禁（门禁在 Task 4 接线，这里只钉契约表）。
func TestAgentFileWriteContractEntriesRegistered(t *testing.T) {
	contract := models.DefaultAgentProtocolContract()

	// 局部 helper（照 TestAgentTransferContractEntriesRegistered 的闭包风格；
	// 那两个是函数内闭包，跨测试不可引用）
	find := func(events []models.AgentProtocolEvent, typ string) *models.AgentProtocolEvent {
		for i := range events {
			if events[i].Type == typ {
				return &events[i]
			}
		}
		return nil
	}
	requireField := func(t *testing.T, ev *models.AgentProtocolEvent, field string) {
		t.Helper()
		for _, f := range ev.Required {
			if f == field {
				return
			}
		}
		t.Fatalf("%s contract missing required field %q (Required=%v)", ev.Type, field, ev.Required)
	}

	if models.AgentEventFileWrite != "file.write" || models.AgentEventFileWriteResult != "file.write.result" {
		t.Fatal("file.write 常量漂移")
	}

	up := find(contract.WebSocket.ServerSends, models.AgentEventFileWrite)
	if up == nil {
		t.Fatal("file.write missing from ServerSends")
	}
	for _, field := range []string{"type", "request_id", "project_path", "path", "content"} {
		requireField(t, up, field)
	}
	hasEmits := false
	for _, e := range up.Emits {
		if e == models.AgentEventFileWriteResult {
			hasEmits = true
		}
	}
	if !hasEmits {
		t.Fatalf("file.write Emits must include file.write.result, got %v", up.Emits)
	}

	res := find(contract.WebSocket.ClientSends, models.AgentEventFileWriteResult)
	if res == nil {
		t.Fatal("file.write.result missing from ClientSends")
	}
	requireField(t, res, "ok")

	// D3+D4 同批：file.error 必须登记 error_code（错误码全集走此帧）
	fe := find(contract.WebSocket.ClientSends, models.AgentEventFileError)
	if fe == nil {
		t.Fatal("file.error missing from ClientSends")
	}
	hasCode := false
	for _, f := range fe.Optional {
		if f == "error_code" {
			hasCode = true
		}
	}
	if !hasCode {
		t.Fatalf("file.error Optional must include error_code, got %v", fe.Optional)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestAgentFileWriteContractEntriesRegistered -v`
Expected: FAIL（常量不存在，编译失败也算 FAIL）

- [ ] **Step 3: 加常量**——`agent_protocol.go` 常量区 `AgentEventFileUploadCancel`（:90）后插两行：

```go
	AgentEventFileWrite               = "file.write"        // server→agent: 手机端原生远程编辑（内联 content，原子写）
	AgentEventFileWriteResult         = "file.write.result" // agent→server: file.write 终态（ok/size_bytes/sha256）
```

- [ ] **Step 4: ClientSends 登记**——:281 的 FileError 行加 Optional，并在其后加 file.write.result：

```go
				{Type: AgentEventFileError, Required: []string{"type", "request_id", "error"}, Optional: []string{"error_code"}},
				{Type: AgentEventFileWriteResult, Required: []string{"type", "request_id", "ok"}, Optional: []string{"path", "size_bytes", "sha256"}},
```

- [ ] **Step 5: ServerSends 登记**——`AgentEventFileUploadCancel` 行后（transfer 注释块前）插：

```go
				// 手机端原生远程编辑（spec 2026-11-02 §D3）：内联 content 回写，
				// 512KiB 上限与 file.read 对称；expected_sha256 乐观锁防与 AI
				// 回合并发互踩；门禁照 file.read/upload 同列 enabled-device 清单。
				{Type: AgentEventFileWrite, Required: []string{"type", "request_id", "project_path", "path", "content"}, Optional: []string{"encoding", "expected_sha256"}, Emits: []string{AgentEventFileWriteResult, AgentEventFileError}},
```

- [ ] **Step 6: 跑测试确认通过**

Run: `go test ./app/http/services/ -run TestAgentFileWriteContractEntriesRegistered -v && go test ./app/http/models/ -v`
Expected: 全 PASS

- [ ] **Step 7: Commit**（须用户同意）

```bash
git add app/http/models/agent_protocol.go app/http/services/agent_transfer_contract_test.go
git commit -m "新增:file.write 帧族契约登记与 file.error error_code 载体"
```

---

### Task 3: D3 handler — agentFileWritePayload（原子写+乐观锁+守卫）

**Files:**
- Create: `app/http/services/agent_file_write.go`
- Modify: `app/http/services/agent_detail.go`（agentFileErrorPayload 旁加带码变体）
- Test: Create `app/http/services/agent_file_write_test.go`

- [ ] **Step 1: 写失败测试**（整个文件）

```go
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

// 覆盖已有文件：内容替换、mode 沿用、结果带 sha/size。
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

// 新建文件：父目录（=TempDir）已存在，落 0644。
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

// 父目录缺失 → target_not_found（不做递归 mkdir）。
func TestAgentFileWriteParentMissing(t *testing.T) {
	dir := t.TempDir()
	got := agentFileWritePayload(map[string]interface{}{
		"request_id": "req-3", "project_path": dir, "path": "no/such/dir/f.txt", "content": "x",
	})
	if got["error_code"] != "target_not_found" {
		t.Fatalf("want target_not_found, got %v", got)
	}
}

// 解码后超 512KiB → too_large（与 file.read 的 agentProjectFileReadMaxByte 对称）。
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

// 未知编码 → bad_encoding；base64 往返成功。
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

// expected_sha256 乐观锁：不符 → conflict；相符 → 成功。
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

// 悬空 symlink 拒写（无法验证落点在守卫内）→ path_forbidden。
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

// content 键缺失=畸形帧 → bad_request（防静默清空目标文件）；显式空串=合法清空。
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
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestAgentFileWrite -v`
Expected: 编译失败（agentFileWritePayload 未定义）

- [ ] **Step 3: 加带码错误载荷变体**——`agent_detail.go` 的 `agentFileErrorPayload` 函数后加：

```go
// agentFileErrorPayloadCode file.error 的结构化细分码变体（spec 2026-11-02
// §D3/D4）：code 契约已登记（AgentEventFileError Optional error_code），
// 报错文本照旧不回显 URL/签名类敏感信息。
func agentFileErrorPayloadCode(requestID, code string, err error) map[string]interface{} {
	payload := agentFileErrorPayload(requestID, err)
	payload["error_code"] = code
	return payload
}
```

- [ ] **Step 4: 写 handler**（整个 `agent_file_write.go`）

```go
package services

// file.write（手机端原生远程编辑，spec 2026-11-02 §D3）：
// 内联 content 回写，路径守卫与 file.read 同源（resolveAgentProjectContentPath，
// TCC GuardPath+EvalSymlinks），原子写（同目录 tmp+rename），512KiB 上限与
// file.read 对称，expected_sha256 乐观锁防与 AI 回合并发互踩。错误细分码走
// file.error 的 error_code（契约已登记）。单文件毫秒级同步完成，不进单飞槽
// （原子 rename 天然并发安全，最后写赢；带 expected_sha256 者先到先得）。

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/internal/tccguard"
)

// agentFileWriteMaxByte 与 file.read 的 agentProjectFileReadMaxByte（512KiB）
// 对称；大文件走互传通道（transfer.*），不走内联帧。
const agentFileWriteMaxByte = 512 * 1024

func agentFileWritePayload(msg map[string]interface{}) map[string]interface{} {
	requestID := remoteString(msg, "request_id")
	fail := func(code string, err error) map[string]interface{} {
		return agentFileErrorPayloadCode(requestID, code, err)
	}

	projectPath := remoteString(msg, "project_path")
	rawPath := remoteString(msg, "path")
	if strings.TrimSpace(rawPath) == "" {
		return fail("bad_request", errors.New("path is required"))
	}
	// content 键缺失=畸形帧 → bad_request 早拒（否则 utf8 解码 0 字节会
	// 静默清空目标文件）；显式空串=合法清空，放行。
	rawContent, hasContent := msg["content"].(string)
	if !hasContent {
		return fail("bad_request", errors.New("content is required"))
	}
	data, err := decodeAgentFileWriteContent(rawContent, remoteString(msg, "encoding"))
	if err != nil {
		return fail("bad_encoding", err)
	}
	if len(data) > agentFileWriteMaxByte {
		return fail("too_large", fmt.Errorf("content exceeds write limit (%d > %d bytes)", len(data), agentFileWriteMaxByte))
	}

	target, err := resolveAgentFileWritePath(projectPath, rawPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fail("target_not_found", err) // 父目录缺失（spec 错误码集）
		}
		return fail("path_forbidden", err)
	}

	if want := strings.TrimSpace(remoteString(msg, "expected_sha256")); want != "" {
		current, hashErr := hashAgentFileOnDisk(target)
		if hashErr != nil && !os.IsNotExist(hashErr) {
			return fail("io_error", hashErr)
		}
		if !strings.EqualFold(current, want) {
			return fail("conflict", fmt.Errorf("file changed on disk (expected sha256 %s, got %s)", want, current))
		}
	}

	written, err := atomicWriteAgentFile(target, data)
	if err != nil {
		return fail("io_error", err)
	}
	sum := sha256.Sum256(data)
	return map[string]interface{}{
		"type":       models.AgentEventFileWriteResult,
		"request_id": requestID,
		"ok":         true,
		"path":       filepath.ToSlash(target),
		"size_bytes": written,
		"sha256":     hex.EncodeToString(sum[:]),
	}
}

// decodeAgentFileWriteContent encoding=utf8（默认）|base64。
func decodeAgentFileWriteContent(content, encoding string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "utf8", "utf-8":
		return []byte(content), nil
	case "base64":
		return base64.StdEncoding.DecodeString(strings.TrimSpace(content))
	default:
		return nil, fmt.Errorf("unsupported encoding %q (want utf8|base64)", encoding)
	}
}

// resolveAgentFileWritePath 在 file.read 同源守卫之上补写侧语义：
// 已存在 → resolveAgentProjectContentPath 内部已做 EvalSymlinks+GuardPath，
//   但 EvalSymlinks 失败（悬空 symlink）说明落点不可验证，拒绝；
// 不存在 → 父目录必须存在且在守卫内（文件名组件原样使用，不递归 mkdir）。
func resolveAgentFileWritePath(projectPath, raw string) (string, error) {
	abs, err := resolveAgentProjectContentPath(projectPath, raw)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Lstat(abs); statErr == nil {
		if _, evalErr := filepath.EvalSymlinks(abs); evalErr != nil {
			return "", fmt.Errorf("target is a dangling symlink: %s", filepath.ToSlash(abs))
		}
		return abs, nil
	}
	dir := filepath.Dir(abs)
	resolved, evalErr := filepath.EvalSymlinks(dir)
	if evalErr != nil {
		if os.IsNotExist(evalErr) {
			// %w 包 os.ErrNotExist：handler 按 errors.Is 细分 target_not_found
			return "", fmt.Errorf("parent directory does not exist: %s: %w", filepath.ToSlash(dir), os.ErrNotExist)
		}
		return "", evalErr
	}
	if err := tccguard.GuardPath(resolved); err != nil {
		return "", err
	}
	return abs, nil
}

// hashAgentFileOnDisk 流式 sha256（预期校验面对的可能是超限大文件，不全量入内存）。
func hashAgentFileOnDisk(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// atomicWriteAgentFile 同目录 tmp+rename 原子写；已存在文件沿用原 perm，新建 0644。
// 返回 int64（契约 size_bytes 是 JSON number，仓内惯例 int64，防接口比较类型漂移）。
func atomicWriteAgentFile(target string, data []byte) (int64, error) {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm()
	}
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".aliang-filewrite-*")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return 0, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return 0, err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		cleanup()
		return 0, err
	}
	if err := os.Rename(tmpName, target); err != nil {
		cleanup()
		return 0, err
	}
	return int64(len(data)), nil
}
```

⚠️ import 路径已按仓内真实 module path 写好（models=`aliang.one/nursorgate/app/http/models`，tccguard=`aliang.one/nursorgate/internal/tccguard`，照 `agent_detail.go` 顶部 import 核对一遍即可）。

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./app/http/services/ -run TestAgentFileWrite -v`
Expected: 8 个用例全 PASS

- [ ] **Step 6: Commit**（须用户同意）

```bash
git add app/http/services/agent_file_write.go app/http/services/agent_file_write_test.go app/http/services/agent_detail.go
git commit -m "新增:file.write handler——原子写+expected_sha256 乐观锁+TCC 路径守卫"
```

---

### Task 4: D3 接线 — enabled-device 门禁 + 分发臂

**Files:**
- Modify: `app/http/services/agent_remote_ws.go`（:1026-1040 门禁清单；:836 显式臂 case）
- Modify: `app/http/services/agent_detail.go`（handleAgentDetailMessageWithAI switch）
- Test: `app/http/services/agent_transfer_contract_test.go`（TestAgentTransferGate 加一行）

- [ ] **Step 1: 写失败测试**——`TestAgentTransferGate` 的 cases 表加一行：

```go
		{models.AgentEventFileWrite, true}, // D3：写侧必须同列 enabled-device 门禁
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestAgentTransferGate -v`
Expected: FAIL（`remoteAgentMessageRequiresEnabledDevice("file.write") = false, want true`）

- [ ] **Step 3: 门禁清单登记**——`agent_remote_ws.go` `remoteAgentMessageRequiresEnabledDevice` 的 `models.AgentEventFileUpload,`（:1034）后加：

```go
		models.AgentEventFileWrite,
```

- [ ] **Step 4: 分发臂接线**——两处：

① `agent_remote_ws.go:836` 显式臂 case 列表加 `models.AgentEventFileWrite`（与 FileList/FileRead 同列；同步把 :831 注释的「四个旧工具事件」措辞更新）：

```go
	case models.AgentEventProjectDetail, models.AgentEventAISessionDetail, models.AgentEventFileList, models.AgentEventFileRead, models.AgentEventFileWrite, models.AgentEventSlashCommandsList, "file.working_tree_diff":
```

② `agent_detail.go` `handleAgentDetailMessageWithAI` switch（:44-59）加分支（放在 FileRead case 后）：

```go
	case models.AgentEventFileWrite:
		_ = writeJSON(agentFileWritePayload(msg))
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./app/http/services/ -run TestAgentTransferGate -v && go build ./...`
Expected: PASS + BUILD-OK

- [ ] **Step 6: Commit**（须用户同意）

```bash
git add app/http/services/agent_remote_ws.go app/http/services/agent_detail.go app/http/services/agent_transfer_contract_test.go
git commit -m "新增:file.write 接线——enabled-device 门禁与旧工具显式臂分发"
```

---

### Task 5: D4 — file.upload upload_url host 钉扎

**Files:**
- Modify: `app/http/services/agent_transfer_common.go:182`（匿名函数提名为默认实现）
- Modify: `app/http/services/agent_file_upload.go`（upload_url 读取点后加校验）
- Test: `app/http/services/agent_file_upload_test.go`（追加）

- [ ] **Step 1: 写失败测试**（追加到 `agent_file_upload_test.go`；该文件现有 import 块没有 `"context"`，需补一行）

```go
// D4（spec 2026-11-02）：file.upload 的 upload_url 必须过 COS 域钉扎，
// 与互传通道同族一致；报错不回显 URL（防签名泄漏）。
func TestAgentFileUploadRejectsNonCOSUploadURL(t *testing.T) {
	orig := agentTransferHostAllowed
	agentTransferHostAllowed = agentTransferHostAllowedDefault
	defer func() { agentTransferHostAllowed = orig }()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"http://bucket.cos.ap-beijing.myqcloud.com/x",     // 非 https
		"https://bucket.cos.ap-beijing.myqcloud.com:8443/x", // 显式端口
		"https://evil.example.com/x",                       // 非 COS 域
	} {
		var got map[string]interface{}
		w := func(v interface{}) error { got = v.(map[string]interface{}); return nil }
		msg := map[string]interface{}{
			"type": models.AgentEventFileUpload, "request_id": "req-pin",
			"project_path": dir, "path": "x.txt", "max_bytes": 100,
			"upload_url": bad,
		}
		handleAgentFileUploadRun(context.Background(), msg, w)
		if got["error_code"] != "upload_url_not_allowed" {
			t.Fatalf("upload_url %q: want error_code=upload_url_not_allowed, got %v", bad, got)
		}
		if strings.Contains(fmt.Sprint(got["error"]), bad) {
			t.Fatalf("error text must not echo the URL: %v", got["error"])
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestAgentFileUploadRejectsNonCOSUploadURL -v`
Expected: 编译失败（`agentTransferHostAllowedDefault` 未定义）

- [ ] **Step 3: 提名默认实现**——`agent_transfer_common.go:180` 把匿名函数改为具名 + var 指向：

```go
// agentTransferHostAllowedDefault 钉扎本体（提名为具名函数以便测试显式还原默认档）。
func agentTransferHostAllowedDefault(rawURL string) error {
	// ……原匿名函数体原样搬入，一行不改……
}

var agentTransferHostAllowed = agentTransferHostAllowedDefault
```

- [ ] **Step 4: 上传侧接入校验 + 既有测试补 stub**——`agent_file_upload.go` `handleAgentFileUploadRun` 里 `upload_url` 空值检查（:133-135）后加：

```go
	// D4（spec 2026-11-02）：与互传通道同族钉扎。报错文本不回显 URL
	// （agentTransferHostAllowed 已保证），结构化码供手机端细分提示。
	if err := agentTransferHostAllowed(uploadURL); err != nil {
		emitErrCode("upload_url_not_allowed", err)
		return
	}
```

并在该函数的 `emitErr` 定义后补一个带码闭包：

```go
	emitErrCode := func(code string, err error) {
		if werr := writeJSON(agentFileErrorPayloadCode(requestID, code, err)); werr != nil {
			logger.Warn(fmt.Sprintf("[file.upload] write error payload failed (request %s): %v", requestID, werr))
		}
	}
```

**⚠️ 既有 file.upload 测试会裸撞钉扎，必须同批补 stub**：`agent_file_upload_test.go` 的既有用例（`TestAgentFileUploadStreamsFileToPresignedURL`/`ServerErrorSurfacesStatus`/`EmitsThrottledProgress`/`SingleFlight`/`CancelAbortsInFlightPUT` 等）经 `newFakeCOSUploadServer`（:111）拿到的是 `http://127.0.0.1:<port>` 假 COS URL，且该文件**没有**替换过 `agentTransferHostAllowed`（替换它在 agent_transfer_test.go，另一条流程）——钉扎落地后它们会在 PUT 前被 `upload_url_not_allowed` 打断。两处补 stub（restore 用 `t.Cleanup`，本文件无 `t.Parallel`，串行安全）：

① `newFakeCOSUploadServer`（:111）函数体开头加：

```go
	origHost := agentTransferHostAllowed
	agentTransferHostAllowed = func(string) error { return nil }
	t.Cleanup(func() { agentTransferHostAllowed = origHost })
```

② `TestAgentFileUploadResponseHeaderHangReleasesSlot`（:492）——它自建 hang server 不走 helper——在 `resetAgentFileUploadSlot(t)` 后加同样的三行 stub。

- [ ] **Step 5: 跑测试确认通过 + 既有 upload 测试不回归**

Run: `go test ./app/http/services/ -run TestAgentFileUpload -v`
Expected: 新用例 PASS，既有用例全 PASS（假 COS URL 走 Step 4 的 stub 放行，真实钉扎逻辑由新用例覆盖）

- [ ] **Step 6: Commit**（须用户同意）

```bash
git add app/http/services/agent_transfer_common.go app/http/services/agent_file_upload.go app/http/services/agent_file_upload_test.go
git commit -m "修复:file.upload upload_url 补 COS 域钉扎(与互传通道同族一致)"
```

---

### Task 6: D1 — MCP bridge 补注册 peer 五工具

**Files:**
- Create: `app/http/services/agent_ai_tool_ask_peer.go`（五工具描述符 + handler）
- Modify: `app/http/services/agent_ai_tool_ask.go`（buildMCPServer 尾部加循环）
- Test: Create `app/http/services/agent_ai_tool_ask_peer_test.go`

- [ ] **Step 1: 写失败测试**（整个文件；映射表=spec §D1 三面命名表的规范性来源）

```go
package services

import (
	"testing"
)

// D1（spec 2026-11-02）：本桥 MCP 名 ↔ agentcore 点号名映射表断言（防三面
// 漂移）。stdio 面的对应断言在 cmd/mcp_test.go（stdio 后三个无 peer_ 前缀
// 是历史现状，勿「修正」）。
func TestAgentPeerMCPBridgeToolNamesMapAgentcoreNames(t *testing.T) {
	mapping := map[string]string{
		"peer_exec":        AgentPeerToolEventExec,
		"peer_fetch_file":  AgentPeerToolEventFetchFile,
		"peer_device_info": AgentPeerToolEventDeviceInfo,
		"peer_device_list": AgentPeerToolEventDeviceList,
		"peer_device_note": AgentPeerToolEventDeviceNote,
	}
	defs := agentPeerMCPBridgeTools()
	if len(defs) != len(mapping) {
		t.Fatalf("bridge has %d tools, mapping has %d", len(defs), len(mapping))
	}
	seen := make(map[string]bool, len(defs))
	for _, def := range defs {
		seen[def.name] = true
		if _, ok := mapping[def.name]; !ok {
			t.Fatalf("unexpected bridge tool %q", def.name)
		}
		if def.description == "" || len(def.inputSchema) == 0 {
			t.Fatalf("tool %q missing description/schema", def.name)
		}
	}
	mgr := newAgentAIManager()
	coreNames := make(map[string]bool)
	for _, tool := range mgr.agentCorePeerTools(agentAIRun{}) {
		coreNames[tool.Name()] = true
	}
	for bridgeName, coreName := range mapping {
		if !seen[bridgeName] {
			t.Fatalf("bridge missing tool %q", bridgeName)
		}
		if !coreNames[coreName] {
			t.Fatalf("agentcore bridge missing tool %q (mapping stale)", coreName)
		}
	}
}

// peerMCPText 结果语义：payload 带 error → (text,true)；否则 result 文本。
func TestAgentPeerMCPTextFromPayload(t *testing.T) {
	if text, isErr := peerMCPText(map[string]interface{}{"result": "ok"}); isErr || text != "ok" {
		t.Fatalf("got %q %v", text, isErr)
	}
	if text, isErr := peerMCPText(map[string]interface{}{"error": "boom"}); !isErr || text != "boom" {
		t.Fatalf("got %q %v", text, isErr)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestAgentPeerMCP -v`
Expected: 编译失败（agentPeerMCPBridgeTools 未定义）

- [ ] **Step 3: 写描述符文件**（整个 `agent_ai_tool_ask_peer.go`；description 逐字照抄 `agent_peer_tools.go:986-1105` 的 agentcore 同名工具，**勿改写**）

```go
package services

// D1（spec 2026-11-02）：aliang MCP bridge（`/mcp/aliang/<token>`，CLI provider
// stream-json 回合经 --mcp-config 注入的既有面）补注册 peer 五工具，使 claude
// 族 CLI 回合获得与 agentcore 内核/外部 stdio 面同一套设备互访能力。
//
// 命名映射（防漂移断言见 agent_ai_tool_ask_peer_test.go 与 cmd/mcp_test.go）：
//   agentcore 点号名（agent_peer_tools.go:46-50） ↔ 本桥（全前缀下划线） ↔ stdio 面（cmd/mcp.go，后三个无 peer_ 前缀=历史现状）
//   peer.exec            ↔ peer_exec        ↔ peer_exec
//   peer.fetch_file      ↔ peer_fetch_file  ↔ peer_fetch_file
//   peer.device_info     ↔ peer_device_info ↔ device_info
//   peer.device_list     ↔ peer_device_list ↔ device_list
//   peer.device_note     ↔ peer_device_note ↔ device_note
//
// handler 复用注册表路径同一批 payload 函数（agent_peer_tools.go:828-916），
// 门禁不变：目标设备 peer_exec_enabled（默认关）+ 云端 peer ACL + 全量审计；
// 出网凭据留在 agent 进程，CLI/LLM 接触不到。readOnly/goal-fork 回合本就不
// 注入 bridge（agent_ai.go:6695 调用点门控），与内核「readOnly 剥离 peer 工具」
// 口径一致。

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

type agentPeerMCPToolDef struct {
	name        string
	description string
	inputSchema json.RawMessage
	handle      func(ctx context.Context, args map[string]interface{}) (string, bool)
}

// peerMCPMsg payload 函数吃 msg map；注入合成 request_id（mcp_ 前缀，不与
// WS 注册表路径冲突），其余键原样透传。
func peerMCPMsg(args map[string]interface{}) map[string]interface{} {
	msg := make(map[string]interface{}, len(args)+1)
	for k, v := range args {
		msg[k] = v
	}
	msg["request_id"] = "mcp_" + uuid.NewString()
	return msg
}

// peerMCPText payload → (text, isErr)。
func peerMCPText(payload map[string]interface{}) (string, bool) {
	if errText, ok := payload["error"].(string); ok && errText != "" {
		return errText, true
	}
	text, _ := payload["result"].(string)
	return text, false
}

func agentPeerMCPBridgeTools() []agentPeerMCPToolDef {
	return []agentPeerMCPToolDef{
		{
			name: "peer_exec",
			description: "Run a one-shot command on another device owned by the same account and " +
				"return its exit code plus stdout/stderr. The remote device must have peer exec enabled. " +
				"Serial per target device: a busy target returns fail_reason=busy immediately. Also " +
				"serial per calling device: only one peer command may be in flight at a time, so run " +
				"peer calls one after another, never in parallel (concurrent calls fail with " +
				"fail_reason=rate_limited).",
			inputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"device_id":{"type":"string","description":"Target device id"},
					"command":{"type":"string","description":"Shell command to run on the target device"},
					"timeout_ms":{"type":"integer","description":"Command timeout in ms (1000-600000, default 30000; out-of-range values are clamped)"},
					"workdir":{"type":"string","description":"Working directory on the target device (must exist, default home)"}
				},
				"required":["device_id","command"]
			}`),
			handle: func(_ context.Context, args map[string]interface{}) (string, bool) {
				return peerMCPText(agentPeerExecPayload(peerMCPMsg(args)))
			},
		},
		{
			name: "peer_fetch_file",
			description: "Queue an asynchronous file fetch from another device into this machine's " +
				"transfer inbox. The source device must have peer fetch enabled. Returns a transfer_id; " +
				"completion is delivered via transfer.progress.",
			inputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"device_id":{"type":"string","description":"Source device id"},
					"remote_path":{"type":"string","description":"File path on the source device"}
				},
				"required":["device_id","remote_path"]
			}`),
			handle: func(_ context.Context, args map[string]interface{}) (string, bool) {
				return peerMCPText(agentPeerFetchFilePayload(peerMCPMsg(args)))
			},
		},
		{
			name: "peer_device_info",
			description: "Device profile snapshot: hostname, platform, agent version, load summary, " +
				"project count with recent project paths, authorized directory count. Omit device_id " +
				"for this machine; pass one for a remote device in the same account.",
			inputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"device_id":{"type":"string","description":"Target device id; omit for this machine"}
				}
			}`),
			handle: func(_ context.Context, args map[string]interface{}) (string, bool) {
				return peerMCPText(agentDeviceInfoPayload(peerMCPMsg(args)))
			},
		},
		{
			name:        "peer_device_list",
			description: "List every device in this account with id, name, platform, online status " +
				"and whether peer exec is enabled. This is the directory to pick device_id from for " +
				"the other peer tools (peer.exec / peer.fetch_file / peer.device_info / peer.device_note).",
			inputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			handle: func(_ context.Context, _ map[string]interface{}) (string, bool) {
				return peerMCPText(agentPeerDeviceListPayload(peerMCPMsg(nil)))
			},
		},
		{
			name: "peer_device_note",
			description: "Save a named note (key/value metadata) on a device. Omit device_id to note " +
				"this machine. Notes survive reconnects and are visible to every device in the account. " +
				"Key max 64 bytes, value max 4096 bytes.",
			inputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"key":{"type":"string","description":"Note key (max 64 bytes)"},
					"value":{"type":"string","description":"Note value (max 4096 bytes)"},
					"device_id":{"type":"string","description":"Target device id; omit for this machine"}
				},
				"required":["key","value"]
			}`),
			handle: func(_ context.Context, args map[string]interface{}) (string, bool) {
				return peerMCPText(agentDeviceNotePayload(peerMCPMsg(args)))
			},
		},
	}
}
```

接线（Step 4）直接在 `buildMCPServer` 尾部内联循环完成，本文件不需要额外的 wire 函数；import 照上块写（`uuid` 在 `peerMCPMsg` 用到）。

- [ ] **Step 4: buildMCPServer 尾部接线**——`agent_ai_tool_ask.go` `buildMCPServer()` 在 ask_user 的 `server.AddTool(...)` 之后、`return server` 之前加：

```go
	for _, def := range agentPeerMCPBridgeTools() {
		def := def
		server.AddTool(&mcp.Tool{Name: def.name, Description: def.description, InputSchema: def.inputSchema},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				args := map[string]interface{}{}
				if len(req.Params.Arguments) > 0 {
					if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
						return toolAskErrorResult(fmt.Sprintf("invalid %s input: %v", def.name, err)), nil
					}
				}
				text, isErr := def.handle(ctx, args)
				if isErr {
					return toolAskErrorResult(text), nil
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
			})
	}
```

（`mcp`/`json`/`fmt` 均已是该文件既有 import，无需新增。）

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./app/http/services/ -run TestAgentPeerMCP -v && go build ./...`
Expected: PASS + BUILD-OK

- [ ] **Step 6: 冒烟确认 ask_user 不回归**

Run: `go test ./app/http/services/ -run 'ToolAsk' -v`
Expected: 既有 tool_ask 用例（`TestToolAskBridge*`/`TestWithAgentToolAskBridge*`）全 PASS（五工具是纯增量注册，不影响配置注入断言）

- [ ] **Step 7: Commit**（须用户同意）

```bash
git add app/http/services/agent_ai_tool_ask_peer.go app/http/services/agent_ai_tool_ask_peer_test.go app/http/services/agent_ai_tool_ask.go
git commit -m "新增:aliang MCP bridge 补注册 peer 五工具(CLI provider 面补齐)"
```

---

### Task 7: cmd 侧映射断言（防 stdio/bridge 漂移）

**Files:**
- Test: `cmd/mcp_test.go`（追加）

- [ ] **Step 1: 写失败测试**（追加；**用字面量，不 import services**——cmd 与 services 无依赖方向）

```go
// D1（spec 2026-11-02）：stdio 面工具名 ↔ bridge 名映射断言。后三个无
// peer_ 前缀是历史现状（claude mcp add 存量用户工具引用），勿改名。
func TestPeerMCPToolNamesMapBridgeNames(t *testing.T) {
	// bridge 名（services/agent_ai_tool_ask_peer.go）
	bridgeNames := map[string]bool{
		"peer_exec": true, "peer_fetch_file": true, "peer_device_info": true,
		"peer_device_list": true, "peer_device_note": true,
	}
	// stdio 名 → bridge 名（历史映射）
	mapping := map[string]string{
		"peer_exec":       "peer_exec",
		"peer_fetch_file": "peer_fetch_file",
		"device_info":     "peer_device_info",
		"device_list":     "peer_device_list",
		"device_note":     "peer_device_note",
	}
	seen := map[string]bool{}
	for _, tool := range peerMCPTools {
		bridge, ok := mapping[tool.name]
		if !ok {
			t.Fatalf("unexpected stdio tool %q (mapping stale)", tool.name)
		}
		if !bridgeNames[bridge] {
			t.Fatalf("stdio tool %q maps to unknown bridge name %q", tool.name, bridge)
		}
		seen[tool.name] = true
	}
	if len(seen) != len(mapping) {
		t.Fatalf("stdio has %d tools, mapping has %d", len(seen), len(mapping))
	}
}
```

- [ ] **Step 2: 跑测试**

Run: `go test ./cmd/ -run TestPeerMCPToolNamesMapBridgeNames -v`
Expected: **直接 PASS**（纯断言现状——本任务是「钉住现状防漂移」，不是修 bug；若 FAIL 说明 stdio 面与 spec 映射表已漂移，停下来核对）
Expected: PASS

- [ ] **Step 3: Commit**（须用户同意）

```bash
git add cmd/mcp_test.go
git commit -m "新增:stdio 面与 bridge 面工具名映射防漂移断言"
```

---

### Task 8: 全量回归与终验

**Files:** 无新改动（验证）

- [ ] **Step 1: 全量构建 + 目标包测试**

```bash
cd .worktrees/peer-defect-batch
go build ./... && go vet ./app/http/services/ ./app/http/models/ ./cmd/
go test ./app/http/models/ ./cmd/
go test ./app/http/services/ -run 'TestAgentFileWrite|TestAgentFileUpload|TestAgentTransfer|TestAgentPeerMCP|ToolAsk|TestAgentPeerLan|TestAgentPeerExec'
```

Expected: 全 PASS。预存 flake 对照（memory：subprocess 超时族单包复跑即绿；`TestSessionAuthorityProxyAdmissionLinearizesWithDemotion` 基线 5/10 勿复跑洗白；**禁 `-race`**）。

- [ ] **Step 2: 契约快照人工复核**

```bash
go test ./app/http/services/ -run 'TestAgentTransferContractEntriesRegistered|TestAgentFileWriteContractEntriesRegistered|TestAgentTransferProtocolConstants' -v
```

Expected: 全 PASS。

- [ ] **Step 3: 汇报与提交意愿征求**

向用户汇报：变更文件清单、测试结果、预存 flake 说明。**等用户明确同意后再统一 commit/push**（若此前各任务已获逐个同意则跳过）。

---

## 验收对照（spec → 计划）

| spec 条目 | 落点 |
|---|---|
| D1 五工具+映射表+防漂移 | Task 6 + Task 7 |
| D2 direct_candidates | Task 1 |
| D3 帧契约/常量/error_code 载体 | Task 2 |
| D3 handler（守卫/原子写/乐观锁/512KiB/编码） | Task 3 |
| D3 门禁+分发臂（不进注册表） | Task 4 |
| D4 host 钉扎+error_code | Task 5 |
| 回归与验收 | Task 8 |
