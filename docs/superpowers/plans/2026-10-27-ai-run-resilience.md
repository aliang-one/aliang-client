# AI run 链路韧性加固 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让任何卡死的 AI 回合都能在确定的有界时间内被判死、发出含原因的终态错误（重试权留给用户），并让 kill 链路全程可观测；同时补齐平台通道 401 的续期、退避与限频告警。

**Architecture:** 四层各自补洞，互不耦合。L1 给内核 SSE 加块间空闲超时并把 `io.Pipe` 写侧改成可被 ctx 解除；L2 在现有 `agentAIRunEmitter` 终态闸之上建一个 per-run guardian（kill 日志 → grace → 强拆），把硬顶改造成「引擎活跃时长」；L3 用 `lastActivityAt`（排除进度心跳）+ 等待态豁免实现 stale 自检与 steer 活性窗；L4 在 `callAgentServerWithAuthorization` 公共层做 401 刷新重试、端点退避与限频告警。设计依据见 `docs/superpowers/specs/2026-10-27-ai-run-resilience-design.md`（v3）。

**Tech Stack:** Go 1.2x、`net/http`、`io.Pipe`、`bufio.Scanner`、`sync/atomic`、现有 `logger`、现有 `agentAIRunEmitter` / `agentAIActivity` / `resolveEnvDuration` 设施。

---

## 全局约束（动手前必读）

1. **一律 worktree + 分支**，禁止在主工作区 master 上改。
2. 提交标题全中文，前缀用「新增：」/「修复：」，不用 `feat:`/`fix:`。推送前信息必须定稿（master 禁强推）。
3. 每个 commit 结尾加：
   ```
   Co-Authored-By: Claude Code <noreply@anthropic.com>
   ```
4. 新 worktree 首次 `go build` 前需从主仓拷贝 `app/website/dist`：
   ```bash
   cp -R /Users/mac/MyProgram/GoProgram/nursor/alianggate/app/website/dist ./app/website/dist
   ```
5. worktree 内 gopls 的全量误报（`undefined: AgentService` 等）一律无视，以 `go build` / `go test` 为准。
6. 基线既有失败（`tool-registry-service-users`、`TestSessionAuthorityProxyAdmissionLinearizesWithDemotion`、`TestStartProxyWhenSessionReady`、`TestHandleAgentAuthRejectedNotificationRules`、agent_ai.go 子进程 2s/5s 高并行偶败）**不是回归**，单包复跑即绿。
7. 设计不变量：**一个 run 恰好一个终态**（靠 `agentAIRunEmitter.terminal` 锁存）；**判死不自动重跑**。

---

## File Structure

**新增**

| 文件 | 职责 |
|---|---|
| `app/agentcore/stream_idle.go` | SSE 块间空闲超时读取器 + 专属错误类型 + env 解析 |
| `app/agentcore/stream_idle_test.go` | 半开流注入、超时不重试 |
| `app/http/services/agent_ai_run_guard.go` | per-run guardian：kill 观测、grace 强拆、stale 扫描、steer 活性窗常量 |
| `app/http/services/agent_ai_run_guard_test.go` | 强拆/锁存/副作用守卫/scanner/`recordingWriter` 夹具（后续 steer 测试复用它） |
| `app/http/services/agent_ai_agentcore_steer_test.go` | steer 活性窗、等待态豁免、drain 停窗、follow-up 同窗（Task 7 起建） |
| `app/http/services/agent_channel_health.go` | 401 分类、端点退避、限频告警 |
| `app/http/services/agent_channel_health_test.go` | 刷新重试、退避、限频告警 |

**修改**

| 文件 | 改动 |
|---|---|
| `app/agentcore/client.go` | `Client.streamIdle` 字段 + `WithStreamIdleTimeout`；`streamOnce` 包 idle reader；`isRetryable` 排除 `*streamIdleError` |
| `app/http/services/agent_ai.go` | `agentAIActivity` 增加 `approvalWaitStartedAt` / `approvalWaitFor` / `staleDeathReason`；`agentAISession.guard` 字段；`startAIWatchdog` 包 kill 观测；watchdog 硬顶改引擎活跃累计；`clearRunning` 清 guard |
| `app/http/services/agent_ai_agentcore.go` | `Emit` 可取消；steer 控制加活性窗；僵尸非帧副作用 runSeq 守卫 |
| `app/http/services/agent_execution_guard.go` | `agentAIHardCeiling` 默认 48h→2h，注释按新口径重写 |
| `app/http/services/agent_service.go` | `callAgentServerWithAuthorization` 拆出 `doCallAgentServer` + 401 分支 |

**阶段划分**

- **P0 止血（Task 1–8）**：L1 全部 + L2 全部 + §3.3.1/§3.3.2/§3.3.3。
- **P1 反馈闭环（Task 9–10）**：steer 活性窗（含 follow-up 覆盖）。
- **P2 通道健康（Task 11–14）**：L4 全部。
- **P3 跨仓对齐（Task 15）**：契约与真机验证清单（不改代码）。

---

## P0 止血

### Task 0: 建立 worktree

- [ ] **Step 1: 建分支**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
git worktree add .worktrees/ai-run-resilience -b fix/ai-run-resilience
cd .worktrees/ai-run-resilience
cp -R /Users/mac/MyProgram/GoProgram/nursor/alianggate/app/website/dist ./app/website/dist
```

- [ ] **Step 2: 确认基线可编译**

Run: `go build ./app/...`
Expected: 无输出（成功）。

> 后续所有命令都在 `.worktrees/ai-run-resilience` 下执行。

---

### Task 1: SSE 块间空闲超时（错误类型 + 读取器）

**Files:**
- Create: `app/agentcore/stream_idle.go`
- Test: `app/agentcore/stream_idle_test.go`

- [ ] **Step 1: 写失败测试**

`app/agentcore/stream_idle_test.go`：

```go
package agentcore

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestIdleTimeoutReaderReturnsStreamIdleError(t *testing.T) {
	pr, pw := io.Pipe()
	r := newIdleTimeoutReader(pr, 40*time.Millisecond)
	defer r.Close()

	// 先给一个字节，再永久静默（半开流形态）。
	go func() { _, _ = pw.Write([]byte("a")) }()

	buf := make([]byte, 8)
	if n, err := r.Read(buf); err != nil || n != 1 {
		t.Fatalf("first read = (%d, %v), want (1, nil)", n, err)
	}

	start := time.Now()
	_, err := r.Read(buf)
	if err == nil {
		t.Fatal("second read returned nil error, want streamIdleError")
	}
	var idleErr *streamIdleError
	if !errors.As(err, &idleErr) {
		t.Fatalf("error = %v (%T), want *streamIdleError", err, err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("idle detection took %s, want < 1s", elapsed)
	}
}

func TestIdleTimeoutReaderDisabledWhenWindowZero(t *testing.T) {
	pr, pw := io.Pipe()
	r := newIdleTimeoutReader(pr, 0)
	defer r.Close()

	go func() {
		time.Sleep(30 * time.Millisecond)
		_, _ = pw.Write([]byte("ok"))
	}()

	buf := make([]byte, 8)
	n, err := r.Read(buf)
	if err != nil || string(buf[:n]) != "ok" {
		t.Fatalf("read = (%q, %v), want (\"ok\", nil)", buf[:n], err)
	}
}

func TestStreamIdleErrorIsNotRetryable(t *testing.T) {
	err := &streamIdleError{window: 90 * time.Second}
	if isRetryable(err) {
		t.Fatal("streamIdleError must NOT be retryable: a retry would re-emit start/text frames")
	}
	if !strings.Contains(err.Error(), "停滞") {
		t.Fatalf("error message = %q, want it to mention 停滞", err.Error())
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/agentcore/ -run TestIdleTimeoutReader -v`
Expected: FAIL，`undefined: newIdleTimeoutReader` / `undefined: streamIdleError`。

- [ ] **Step 3: 写实现**

`app/agentcore/stream_idle.go`：

```go
package agentcore

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// streamIdleTimeout 是 SSE 两次字节到达之间的最长静默时间。
//
// 上游协议带 ping 保活，正常的长时间回合不会静默这么久；一旦窗口内一个字节
// 都没到，说明连接已半开或上游停滞，必须中断读取——否则 Read 会永久阻塞，
// 整轮 run 卡死在 agentcore.Run 内部（2026-10-27 事故形态）。
//
//	ALIANG_AGENTCORE_STREAM_IDLE_TIMEOUT  (time.ParseDuration). Default 90s. 0=禁用（仅测试用）。
var streamIdleTimeout = agentcoreEnvDuration("ALIANG_AGENTCORE_STREAM_IDLE_TIMEOUT", 90*time.Second)

func agentcoreEnvDuration(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return def
	}
	return d
}

// streamIdleError 表示「上游流停滞」：窗口内没有收到任何字节。
//
// 它被显式标记为不可重试（见 isRetryable）。原因：此时本尝试可能已经把
// start/text 等事件帧下发给了宿主，整轮重跑会让手机看到重复内容。
type streamIdleError struct{ window time.Duration }

func (e *streamIdleError) Error() string {
	return fmt.Sprintf("agentcore: 上游流停滞（%s 内未收到任何数据）", e.window)
}

// idleTimeoutReader 在两次成功读取之间超过 window 没有字节到达时关闭底层
// body，让阻塞中的 Read 立即返回错误。
//
// 为什么不用 SetReadDeadline：io.Reader 没有 deadline 概念，而 resp.Body 的
// 具体类型随 transport（h1/h2/代理）变化，拿不到稳定的 net.Conn。关闭 body
// 是唯一与传输实现无关的解除手段。
//
// 读到字节即重置计时器；timeout 触发时先置位再关闭，Read 侧据置位把关闭导致的
// 错误翻译成 *streamIdleError。
type idleTimeoutReader struct {
	body   io.ReadCloser
	window time.Duration

	mu       sync.Mutex
	timedOut bool
	closed   bool
	timer    *time.Timer
}

func newIdleTimeoutReader(body io.ReadCloser, window time.Duration) *idleTimeoutReader {
	r := &idleTimeoutReader{body: body, window: window}
	if window > 0 {
		r.timer = time.AfterFunc(window, r.fire)
	}
	return r
}

func (r *idleTimeoutReader) fire() {
	r.mu.Lock()
	r.timedOut = true
	r.mu.Unlock()
	// 关闭底层 body 会解开阻塞中的 Read（http 连接随即拆掉）。
	_ = r.body.Close()
}

func (r *idleTimeoutReader) Read(p []byte) (int, error) {
	n, err := r.body.Read(p)
	if n > 0 {
		r.reset()
	}
	if err != nil {
		r.mu.Lock()
		timedOut := r.timedOut
		r.mu.Unlock()
		if timedOut {
			return n, &streamIdleError{window: r.window}
		}
	}
	return n, err
}

func (r *idleTimeoutReader) reset() {
	if r.timer == nil {
		return
	}
	r.timer.Reset(r.window)
}

func (r *idleTimeoutReader) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()
	if r.timer != nil {
		r.timer.Stop()
	}
	return r.body.Close()
}
```

- [ ] **Step 4: 跑测试**

Run: `go test ./app/agentcore/ -run 'TestIdleTimeoutReader' -v`
Expected: 2 个用例 PASS。注意 `-run` 是按名字子串匹配，`TestStreamIdleErrorIsNotRetryable` **不会被执行**（不是失败）——它依赖 Task 2 的 `isRetryable` 改动，跑它这一步会红，属于预期，别当回归。

确认第三个用例确实被跳过：`go test ./app/agentcore/ -run 'TestStreamIdleErrorIsNotRetryable' -v` → 本步预期 FAIL（`newIdleTimeoutReader` 之外还缺 `isRetryable` 分支），Task 2 后再转绿。

---

### Task 2: 接入 streamOnce + 排除重试

**Files:**
- Modify: `app/agentcore/client.go:57-89`（Client 结构 + NewClient）、`app/agentcore/client.go:271-283`（isRetryable）、`app/agentcore/client.go:302-335`（streamOnce）、`app/agentcore/client.go:422-431`（parseSSE 错误翻译）

- [ ] **Step 1: 写失败测试**

追加到 `app/agentcore/stream_idle_test.go`：

```go
func TestStreamDoesNotRetryOnStreamStall(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// 头部与一帧之后永久静默：半开流。
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"model\":\"x\"}}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := NewClient(srv.URL).WithStreamIdleTimeout(60 * time.Millisecond)
	err := c.Stream(context.Background(), &MessagesRequest{
		Model:     "x",
		MaxTokens: 16,
		Messages:  []Message{{Role: "user", Content: "hi"}},
	}, func(StreamEvent) error { return nil })

	var idleErr *streamIdleError
	if !errors.As(err, &idleErr) {
		t.Fatalf("Stream error = %v (%T), want *streamIdleError", err, err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("upstream hits = %d, want 1 (stream stall must not retry)", got)
	}
}
```

需要的 import 追加：`context`、`net/http`、`net/http/httptest`、`sync/atomic`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/agentcore/ -run TestStreamDoesNotRetryOnStreamStall -v`
Expected: FAIL，`c.WithStreamIdleTimeout undefined`。

- [ ] **Step 3: 改 Client 结构**

`app/agentcore/client.go`，`Client` 结构体加字段：

```go
type Client struct {
	baseURL string
	http    *http.Client
	// apiKey 是用户自己的平台 API key（sk-...）。走 56432 平台代理时同样要带：
	// 网关用它识别「用哪把 key」，不带就会落到分组兜底、对非该组的账号 403。
	// 留空只在没有可用 key 时发生——此时行为退回到「由代理决定用哪把」。
	apiKey string
	// streamIdle 是 SSE 块间空闲窗口；0 = 禁用（测试用）。由 NewClient 从
	// streamIdleTimeout 环境变量解析结果播种。
	streamIdle time.Duration
}
```

`NewClient` 里加一行：

```go
	return &Client{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		streamIdle: streamIdleTimeout,
		http: &http.Client{
```

新增 option（放在 `WithAPIKey` 之后）：

```go
// WithStreamIdleTimeout 覆盖 SSE 块间空闲窗口（0=禁用）。测试用。
func (c *Client) WithStreamIdleTimeout(d time.Duration) *Client {
	c.streamIdle = d
	return c
}
```

- [ ] **Step 4: streamOnce 包一层 reader**

把 `streamOnce` 的成功分支：

```go
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("agentcore: 请求上游失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &httpStatusError{
			status:     resp.StatusCode,
			body:       string(raw),
			retryAfter: parseRetryAfter(resp.Header.Get("retry-after")),
		}
	}

	return parseSSE(ctx, resp.Body, onEvent)
```

改为：

```go
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("agentcore: 请求上游失败: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &httpStatusError{
			status:     resp.StatusCode,
			body:       string(raw),
			retryAfter: parseRetryAfter(resp.Header.Get("retry-after")),
		}
	}

	// 成功响应才装块间空闲闸：错误体是一次性读完的，不需要。
	reader := newIdleTimeoutReader(resp.Body, c.streamIdle)
	defer reader.Close()
	return parseSSE(ctx, reader, onEvent)
```

- [ ] **Step 5: isRetryable 排除停滞**

`isRetryable` 开头插入：

```go
func isRetryable(err error) bool {
	// 块间停滞不是瞬态：整条流已经断在半开状态，重试只会把本尝试已经下发的
	// start/text 事件帧重复推给手机。
	var idleErr *streamIdleError
	if errors.As(err, &idleErr) {
		return false
	}
	var statusErr *httpStatusError
	...
```

- [ ] **Step 6: parseSSE 原样上抛停滞错误**

把 `parseSSE` 末尾：

```go
	if err := scanner.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("agentcore: 读取流失败: %w", err)
	}
```

改为：

```go
	if err := scanner.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		// 停滞错误原样上抛：包一层会让 isRetryable 的 errors.As 之外的多处
		// 判定多绕一环，且错误文案会变成「读取流失败: ...」掩盖真因。
		var idleErr *streamIdleError
		if errors.As(err, &idleErr) {
			return idleErr
		}
		return fmt.Errorf("agentcore: 读取流失败: %w", err)
	}
```

- [ ] **Step 7: 跑测试**

Run: `go test ./app/agentcore/ -v`
Expected: 全绿（含 `TestStreamIdleErrorIsNotRetryable`）。

- [ ] **Step 8: 提交**

```bash
git add app/agentcore/stream_idle.go app/agentcore/stream_idle_test.go app/agentcore/client.go
git commit -m "$(cat <<'EOF'
新增：agentcore 内核 SSE 块间空闲超时（停滞不可重试）

上游半开时 Read 会永久阻塞，整轮 run 卡死在 agentcore.Run 内部。
窗口内收不到任何字节即中断读取并返回专属错误，且显式排除既有重试循环，
避免重复下发已发出的 start/text 事件帧。

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Emit 可被 ctx 解除

**Files:**
- Modify: `app/http/services/agent_ai_agentcore.go:770-894`
- Test: `app/http/services/agent_ai_agentcore_emit_test.go`（新建）

- [ ] **Step 1: 写失败测试**

`app/http/services/agent_ai_agentcore_emit_test.go`：

```go
package services

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestAgentCoreEmitUnblocksOnContextCancel(t *testing.T) {
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	emit, stop := newAgentCoreEmit(ctx, pw)
	defer stop()

	// 不消费 pipeReader：写侧必然阻塞。
	done := make(chan error, 1)
	go func() { done <- emit([]byte("frame")) }()

	select {
	case <-done:
		t.Fatal("emit returned before cancel; write should be blocked")
	case <-time.After(50 * time.Millisecond):
	}

	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("emit returned nil after cancel, want closed-pipe error")
		}
	case <-time.After(time.Second):
		t.Fatal("emit stayed blocked after ctx cancel")
	}
	_ = pr.Close()
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestAgentCoreEmitUnblocksOnContextCancel -v`
Expected: FAIL，`undefined: newAgentCoreEmit`。

- [ ] **Step 3: 实现**

`app/http/services/agent_ai_agentcore.go` 末尾（或 `runAgentCore` 之前）新增：

```go
// newAgentCoreEmit 返回一个可被 ctx 解除的 Emit 函数。
//
// 内核的每一帧都经 io.Pipe 交给流式管线；pipeWriter.Write 会一直阻塞到读端
// 消费（读端还有限流器与网络写出）。若读端卡住，写侧就永久阻塞，agentcore.Run
// 再也返回不了——看门狗的 cancel 也就形同虚设（2026-10-27 事故的候选卡点）。
//
// 这里用一条守护 goroutine 把 ctx.Done 翻译成 pipe 关闭，让阻塞中的 Write
// 立刻返回 io.ErrClosedPipe，Run 得以退出。stop 必须 defer 调用，否则
// ctx 永不取消的路径（正常完成）会泄漏这条 goroutine。
func newAgentCoreEmit(ctx context.Context, writer *io.PipeWriter) (emit func([]byte) error, stop func()) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = writer.CloseWithError(ctx.Err())
		case <-done:
		}
	}()
	var once sync.Once
	stop = func() { once.Do(func() { close(done) }) }
	emit = func(frame []byte) error {
		_, err := writer.Write(frame)
		return err
	}
	return emit, stop
}
```

- [ ] **Step 4: 接线到 cfg**

`runAgentCore` 里，`cfg := &agentcore.Config{` 之前插入：

```go
	emitFrame, stopEmit := newAgentCoreEmit(ctx, pipeWriter)
	defer stopEmit()
```

并把 `Emit:` 字段改成：

```go
		Emit: emitFrame,
```

- [ ] **Step 5: 跑测试**

Run: `go test ./app/http/services/ -run TestAgentCoreEmit -v`
Expected: PASS。

- [ ] **Step 6: 跑该包既有 agentcore 用例防回归**

Run: `go test ./app/http/services/ -run 'TestRunAgentCore|TestAgentCore|TestAgentCoreManagerJournalSteerUserMessage' -count=1`
Expected: 全绿（个别偶败单包复跑即绿）。

> 正则必须带 `TestRunAgentCore`：真正的端到端用例叫 `TestRunAgentCoreSendsImageAttachmentUpstream`，`-run` 是连续子串匹配，只写 `TestAgentCoreRun` 一个都匹配不到。

- [ ] **Step 7: 提交**

```bash
git add app/http/services/agent_ai_agentcore.go app/http/services/agent_ai_agentcore_emit_test.go
git commit -m "$(cat <<'EOF'
修复：内核 Emit 写侧可被 ctx 解除，避免 cancel 失效

io.Pipe 写侧不感知 ctx，读端卡住时 agentcore.Run 永不返回，看门狗 kill
形同虚设。改为守护 goroutine 在 ctx 取消时关闭 pipe，让阻塞的 Write
立刻返回。

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: kill 可观测 + grace 强拆 + guardian

**Files:**
- Create: `app/http/services/agent_ai_run_guard.go`
- Modify: `app/http/services/agent_ai.go:270-306`（session 字段）、`app/http/services/agent_ai.go:1285-1290`（startAIWatchdog）、`app/http/services/agent_ai.go:3635-3668`（clearRunning）、`app/http/services/agent_ai.go:2557-2567`（注册 guard）、`app/http/services/agent_ai.go:1886-1890`（/clear 复位强拆标记）、`app/http/services/agent_ai_agentcore.go:681`（done 标记）
- Test: `app/http/services/agent_ai_run_guard_test.go`

- [ ] **Step 1: 写失败测试**

`app/http/services/agent_ai_run_guard_test.go`：

```go
package services

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

type recordingWriter struct {
	mu       sync.Mutex
	payloads []map[string]interface{}
}

func (w *recordingWriter) write(value interface{}) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if payload, ok := value.(map[string]interface{}); ok {
		w.payloads = append(w.payloads, payload)
	}
	return nil
}

func (w *recordingWriter) snapshot() []map[string]interface{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]map[string]interface{}(nil), w.payloads...)
}

func newGuardTestManager(t *testing.T) (*agentAIManager, *agentAISession, *agentAIRunGuard, *recordingWriter) {
	t.Helper()
	m := newAgentAIManager()
	activity := newAgentAIActivity()
	cancelCount := int32(0)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	session := &agentAISession{
		id:       "sess-1",
		runSeq:   1,
		cancel:   func() { atomic.AddInt32(&cancelCount, 1); cancel() },
		activity: activity,
	}
	m.sessions[session.id] = session

	run := agentAIRun{
		sessionID: session.id,
		runID:     "run-1",
		messageID: "msg-1",
		runSeq:    session.runSeq,
		activity:  activity,
		cancel:    session.cancel,
	}
	rec := &recordingWriter{}
	guard := m.attachRunGuard(run, rec.write)
	return m, session, guard, rec
}

func TestRunGuardKillLogsAndForceFinalizesAfterGrace(t *testing.T) {
	oldGrace := agentAIKillGrace
	agentAIKillGrace = 40 * time.Millisecond
	t.Cleanup(func() { agentAIKillGrace = oldGrace })

	m, session, guard, rec := newGuardTestManager(t)
	guard.kill("stalled")

	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("terminal emitted before grace elapsed: %d", got)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		payloads := rec.snapshot()
		if len(payloads) == 1 {
			if payloads[0]["type"] != models.AgentEventAIError {
				t.Fatalf("terminal type = %v, want ai.error", payloads[0]["type"])
			}
			if payloads[0]["error_code"] != "turn_stalled" {
				t.Fatalf("error_code = %v, want turn_stalled", payloads[0]["error_code"])
			}
			if !strings.Contains(remoteString(payloads[0], "error"), "判死") {
				t.Fatalf("error message = %q, want 判死", remoteString(payloads[0], "error"))
			}
			m.mu.Lock()
			idle := m.sessions[session.id].cancel == nil
			m.mu.Unlock()
			if !idle {
				t.Fatal("session still has cancel set after force finalize")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no terminal after grace: %d payloads", len(rec.snapshot()))
}

func TestRunGuardNoForceFinalizeWhenRunReturns(t *testing.T) {
	oldGrace := agentAIKillGrace
	agentAIKillGrace = 60 * time.Millisecond
	t.Cleanup(func() { agentAIKillGrace = oldGrace })

	_, _, guard, rec := newGuardTestManager(t)
	guard.kill("idle_timeout")
	guard.markDone()

	time.Sleep(150 * time.Millisecond)
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("terminal emitted after run returned normally: %d", got)
	}
}

func TestRunGuardKillIsIdempotent(t *testing.T) {
	oldGrace := agentAIKillGrace
	agentAIKillGrace = 30 * time.Millisecond
	t.Cleanup(func() { agentAIKillGrace = oldGrace })

	_, _, guard, rec := newGuardTestManager(t)
	guard.markDone() // 抑制强拆，只看 cancel 次数
	oldCancel := guard.cancel
	calls := int32(0)
	guard.cancel = func() { atomic.AddInt32(&calls, 1); oldCancel() }

	guard.kill("a")
	guard.kill("b")
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("cancel called %d times, want 1", got)
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("terminal = %d, want 0 (done already closed)", got)
	}
}

func TestRunSideEffectsFencedAfterForceFinalize(t *testing.T) {
	m := newAgentAIManager()
	session := &agentAISession{id: "sess-2", runSeq: 2, cancel: func() {}}
	m.sessions[session.id] = session

	if !m.agentAIRunSideEffectsAllowed("sess-2", 2) {
		t.Fatal("live run must be allowed")
	}
	// 强拆打标：只对本 runSeq 生效。生产端唯一的写入点是 finalizeStalledGuard
	// 身份校验临界区内的内联赋值，这里直接构造同一个标记。
	m.mu.Lock()
	session.finalizedRunSeq = 2
	m.mu.Unlock()
	if m.agentAIRunSideEffectsAllowed("sess-2", 2) {
		t.Fatal("force-finalized run must be fenced")
	}
	// Retry 起新回合（runSeq 推进）后，旧标记不得牵连新回合。
	session.runSeq = 3
	if !m.agentAIRunSideEffectsAllowed("sess-2", 3) {
		t.Fatal("a fresh runSeq must not inherit the previous run's fence")
	}
	session.runSeq = 2
	if m.agentAIRunSideEffectsAllowed("sess-2", 2) {
		t.Fatal("stale runSeq must stay fenced")
	}
	// 无 cancel 的夹具会话（单测/TUI 形态）默认必须放行——判据是显式标记。
	legacy := &agentAISession{id: "sess-legacy", runSeq: 1}
	m.sessions[legacy.id] = legacy
	if !m.agentAIRunSideEffectsAllowed("sess-legacy", 1) {
		t.Fatal("session without cancel must not be treated as a zombie")
	}
	if !m.agentAIRunSideEffectsAllowed("missing-session", 9) {
		t.Fatal("unregistered session (tests/TUI) must keep legacy behaviour")
	}
	_ = errors.Is
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run 'TestRunGuard|TestRunSideEffectsFenced' -v`
Expected: FAIL，`undefined: attachRunGuard` / `agentAISession has no field guard` / `agentAISession has no field finalizedRunSeq`。

- [ ] **Step 3: 写 guardian**

`app/http/services/agent_ai_run_guard.go`：

```go
package services

import (
	"context"
	"fmt"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
)

// agentAIKillGrace 是 cancel 之后等待 run 自然退出的宽限期。超过它仍未返回，
// guardian 强制收尾（发终态 + 会话复位），僵尸后续帧由 emitter 终态闸丢弃。
//
//	ALIANG_AI_KILL_GRACE  (time.ParseDuration). Default 30s.
var agentAIKillGrace = resolveEnvDuration("ALIANG_AI_KILL_GRACE", 30*time.Second)

// agentAIRunGuard 是一个运行中回合的收尾守卫。它把「kill → 宽限 → 强拆」
// 串成一条带观测的链路，并给 stale 扫描器提供同一入口。
//
// 终态唯一性不在这里保证：emit 是 run 的 agentAIRunEmitter.emit，自带终态锁存。
type agentAIRunGuard struct {
	m         *agentAIManager
	sessionID string
	runSeq    int
	runID     string
	messageID string
	activity  *agentAIActivity
	cancel    context.CancelFunc
	// emit 是该 run 的 emitter.emit：自带终态锁存，保证任何路径恰好一个终态。
	emit agentTerminalWriter
	done chan struct{}

	doneOnce     sync.Once
	killOnce     sync.Once
	finalizeOnce sync.Once
}

func newAgentAIRunGuard(m *agentAIManager, run agentAIRun, emit agentTerminalWriter) *agentAIRunGuard {
	return &agentAIRunGuard{
		m:         m,
		sessionID: run.sessionID,
		runSeq:    run.runSeq,
		runID:     run.runID,
		messageID: run.messageID,
		activity:  run.activity,
		cancel:    run.cancel,
		emit:      emit,
		done:      make(chan struct{}),
	}
}

// attachRunGuard 把 guard 挂到会话上（runSeq 校验后才挂，防止窜号）。
func (m *agentAIManager) attachRunGuard(run agentAIRun, emit agentTerminalWriter) *agentAIRunGuard {
	if m == nil || run.activity == nil || run.cancel == nil {
		return nil
	}
	guard := newAgentAIRunGuard(m, run, emit)
	m.mu.Lock()
	if session := m.sessions[run.sessionID]; session != nil && session.runSeq == run.runSeq {
		session.guard = guard
	}
	m.mu.Unlock()
	return guard
}

func (m *agentAIManager) runGuardForActivity(activity *agentAIActivity) *agentAIRunGuard {
	if m == nil || activity == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, session := range m.sessions {
		if session.activity == activity && session.guard != nil {
			return session.guard
		}
	}
	return nil
}

// markRunGuardDone 标记该回合的 goroutine 已返回。强拆宽限据此判断要不要动手。
func (m *agentAIManager) markRunGuardDone(sessionID string, runSeq int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	session := m.sessions[sessionID]
	var guard *agentAIRunGuard
	if session != nil && session.runSeq == runSeq {
		guard = session.guard
	}
	m.mu.Unlock()
	if guard != nil {
		guard.markDone()
	}
}

func (g *agentAIRunGuard) markDone() {
	if g == nil {
		return
	}
	g.doneOnce.Do(func() { close(g.done) })
}

// kill 是唯一的判死入口：记录原因 → 打日志 → cancel → 起强拆宽限。
// 幂等（看门狗与 stale 扫描器可能同时命中同一个 run）。
func (g *agentAIRunGuard) kill(reason string) {
	if g == nil {
		return
	}
	g.killOnce.Do(func() {
		g.activity.setKillReason(reason)
		logger.Warn(fmt.Sprintf(
			"[ai.run.kill] reason=%s session=%s runSeq=%d run=%s runFor=%s idleFor=%s progressIdleFor=%s awaiting=%t pendingToolUses=%d activeSubagents=%d grace=%s",
			reason,
			g.sessionID,
			g.runSeq,
			g.runID,
			time.Since(g.activity.runStart).Truncate(time.Second),
			g.activity.idleFor().Truncate(time.Second),
			g.activity.progressIdleFor().Truncate(time.Second),
			g.activity.awaiting(),
			g.activity.pendingToolUses.Load(),
			g.activity.activeSubagents.Load(),
			agentAIKillGrace,
		))
		g.cancel()
		g.armForceFinalize(reason)
	})
}

func (g *agentAIRunGuard) armForceFinalize(reason string) {
	if agentAIKillGrace <= 0 {
		return
	}
	time.AfterFunc(agentAIKillGrace, func() {
		select {
		case <-g.done:
			return // 正常收敛，无需强拆
		default:
		}
		g.forceFinalize(reason)
	})
}

// forceFinalize 在宽限期内 run 仍未返回时强制收尾：发恰好一个终态错误帧，
// 并把会话复位到 idle（cancel/steer/approval 全部清掉）。
//
// emit 放在 goroutine 里：僵尸可能正卡在 emitter 互斥锁或底层写侧，强拆本身
// 绝不能被它拖住——会话复位才是「手机不再显示运行中」的关键。
//
// 已知且接受的竞态：clearRunning 发出的 idle 状态帧与这里 turn_stalled 错误帧
// 都过同一个 emitter 终态闸，谁先谁得。「恰好一个终态」的不变量不变，但若状态
// 帧先赢，客户端只会看到回到 idle、看不到错误文案——判死原因仍可从
// [ai.run.force_finalize] 日志与 killReason 追溯。要消掉这个不确定性，得让
// clearRunning 接受「本次复位的原因」并合并进同一帧，属于后续增强。
func (g *agentAIRunGuard) forceFinalize(reason string) {
	if g == nil {
		return
	}
	g.finalizeOnce.Do(func() {
		g.activity.setKillReason(reason)
		logger.Warn(fmt.Sprintf(
			"[ai.run.force_finalize] session=%s runSeq=%d run=%s reason=%s runFor=%s idleFor=%s",
			g.sessionID, g.runSeq, g.runID, reason,
			time.Since(g.activity.runStart).Truncate(time.Second),
			g.activity.idleFor().Truncate(time.Second),
		))
		payload := agentAIErrorPayload(g.sessionID, g.messageID,
			fmt.Errorf("回合已停止响应（%s），已判死，请重试", reason))
		payload["error_code"] = "turn_stalled"
		payload["status"] = "stalled"
		go func() { _ = g.emit(payload) }()
		// 复位对外意味着「手机已回到 idle」，此后僵尸的任何迟返回都必须被
		// 非帧副作用守卫挡住（Task 8）。打标记与复位的身份校验在同一个临界区
		// 内完成（finalizeStalledGuard）：/clear 复用 runSeq 后，迟到的强拆
		// 绝不能拆掉新的合法回合。
		if code, ok := g.m.finalizeStalledGuard(g, g.emit); !ok {
			detail := "该回合已不是会话当前回合"
			switch code {
			case "session_missing":
				detail = "会话已不存在"
			case "guard_replaced":
				detail = "guard 已被新回合顶替"
			case "run_seq_moved":
				detail = "runSeq 已推进"
			}
			logger.Warn(fmt.Sprintf(
				"[ai.run.force_finalize] guard_not_current session=%s runSeq=%d run=%s reason=%s code=%s（%s，跳过复位）",
				g.sessionID, g.runSeq, g.runID, reason, code, detail,
			))
		}
	})
}

// wrapKillCancel 把看门狗的裸 cancel 换成带观测与强拆升级的版本。
//
// 刻意保持 agentAIWatchdogLoop 的签名不变（9 处测试直接调它），改在这里注入。
// 非 agentcore 回合（TUI）没有 guard，退化为「只打日志 + cancel」。
func (m *agentAIManager) wrapKillCancel(activity *agentAIActivity, cancel context.CancelFunc) context.CancelFunc {
	return func() {
		if activity == nil {
			cancel()
			return
		}
		reason := activity.killReasonOr("unknown")
		if guard := m.runGuardForActivity(activity); guard != nil {
			guard.kill(reason)
			return
		}
		logger.Warn(fmt.Sprintf(
			"[ai.run.kill] reason=%s (no guard: TUI/legacy path) runFor=%s idleFor=%s progressIdleFor=%s awaiting=%t pendingToolUses=%d activeSubagents=%d",
			reason,
			time.Since(activity.runStart).Truncate(time.Second),
			activity.idleFor().Truncate(time.Second),
			activity.progressIdleFor().Truncate(time.Second),
			activity.awaiting(),
			activity.pendingToolUses.Load(),
			activity.activeSubagents.Load(),
		))
		cancel()
	}
}

// agentAIRunSideEffectsAllowed 是僵尸非帧副作用的 runSeq 守卫。
//
// 判据是**强拆时打的显式标记** finalizedRunSeq，不是 session.cancel == nil：
// 单测/TUI 路径注册的会话本来就没有 cancel，若拿它当判据，会把合法回合的
// history/journal/resume 写一起挡掉（既有 6 个 agentcore 用例直接变红）。
// 未注册会话（单测/TUI 路径）返回 true，保持既有行为。
func (m *agentAIManager) agentAIRunSideEffectsAllowed(sessionID string, runSeq int) bool {
	if m == nil {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil {
		return true
	}
	if session.runSeq != runSeq {
		return false
	}
	return session.finalizedRunSeq != runSeq
}

// 刻意不提供 markRunFinalized 之类的方法：标记的唯一写入点是
// finalizeStalledGuard 身份校验临界区内的 session.finalizedRunSeq = g.runSeq
// （见 Step 3）。抽成方法会让「无身份校验也能打标」变成合法调用面，
// agentAIRunSideEffectsAllowed 的守卫语义就靠不住了。
//
// 标记不随 clearRunning 清零：按 runSeq 记账，Retry 起新回合（runSeq 推进）
// 后旧标记自动不再命中。
//
// 唯一例外是 /clear：它把 runSeq 归零（agent_ai.go 的 /clear 分支），下一回合
// 可能复用同一个 runSeq，此时旧标记会误命中、把合法回合的 history/journal/
// resume 写一起丢掉。所以 /clear 分支必须同时把 finalizedRunSeq 清零
// （见 Step 8；这条约束也写在 agentAIRunSideEffectsAllowed 的注释里）。
```

- [ ] **Step 4: 加 session 字段**

`app/http/services/agent_ai.go`，`agentAISession` 结构体里 `activity *agentAIActivity` 之后加：

```go
	// guard 是本回合的收尾守卫（kill 观测 + 宽限强拆）。clearRunning 清空。
	guard *agentAIRunGuard
	// finalizedRunSeq 是「该 runSeq 已被强拆收尾」的显式标记（0 = 无）。
	// 非帧副作用守卫靠它识别僵尸迟返回；不用 cancel == nil 判别，是因为
	// 单测/TUI 路径注册的会话本来就没有 cancel，会把合法回合一起挡掉
	// （会打红 6 个既有用例）。按 runSeq 记账，下一回合 runSeq 推进后自然失效，
	// 不需要在起跑路径手工清零。
	finalizedRunSeq int
```

- [ ] **Step 5: startAIWatchdog 包一层**

```go
func (m *agentAIManager) startAIWatchdog(ctx context.Context, activity *agentAIActivity, cancel context.CancelFunc) {
	if activity == nil || cancel == nil {
		return
	}
	go agentAIWatchdogLoop(ctx, activity, m.wrapKillCancel(activity, cancel), agentAIIdleWindow, agentAIHardCeiling, agentAINoProgressWindow, agentAIToolWaitCeiling, agentAISubagentWaitCeiling, agentAIIdleCheckInterval)
}
```

- [ ] **Step 6: 注册 guard（run 起跑处）**

`app/http/services/agent_ai.go:2558` 附近，把 `m.startAIWatchdog(ctx, activity, cancel)` 之前插入：

```go
	guard := m.attachRunGuard(run, emitter.emit)
	_ = guard
	m.startAIWatchdog(ctx, activity, cancel)
```

> 若该处 `emitter` 变量名不同，用 `session.activeWriter` 的同一个 emitter 实例；`attachRunGuard` 内部需要的就是 `emitter.emit`（终态锁存的那一层）。

- [ ] **Step 7: clearRunning 收尾 guard**

`clearRunning` 里 `session.activity = nil` 之后加：

```go
		if session.guard != nil {
			// 正常收尾也必须关闭 done。guard 挂在所有 provider 的回合上，而
			// markRunGuardDone 只有 agentcore 路径调用；缺了这一步，codex/claude
			// 回合被合法 kill 后 30s 还会再跑一次多余的强拆（终态被 emitter 闸
			// 丢弃，但会多一条 force_finalize 日志 + 一个多余 goroutine）。
			session.guard.markDone()
			session.guard = nil
		}
```

- [ ] **Step 8: /clear 同步复位强拆标记**

`app/http/services/agent_ai.go:1886-1890` 的 `applyLocalSlashCommand` → `case "clear":` 里，`session.runSeq = 0` 之后紧跟一行：

```go
		session.finalizedRunSeq = 0
```

**为什么必须加**：`/clear` 把 `runSeq` 归零，而不是推进。若不复位标记，「runSeq=1 被强拆 → /clear → 下一回合又拿到 runSeq=1」这条序列会让 `agentAIRunSideEffectsAllowed` 把**合法**的新回合一并判成僵尸，`appendAssistantHistory` / `journal.appendAssistant` / `setAgentAIResumeSessionIDIfEmpty` 全被静默丢弃——手机上看就是「这轮没有历史、续不上」。`/clear` 执行时该会话没有在跑的回合，清零是安全的。

同一文件 `agent_ai_run_guard_test.go` 追加回归用例（加在 `TestRunSideEffectsFencedAfterForceFinalize` 之后）：

```go
func TestClearResetsFinalizedRunSeq(t *testing.T) {
	m := newAgentAIManager()
	session := &agentAISession{id: "sess-clear", runSeq: 1, cancel: func() {}}
	m.sessions[session.id] = session
	// 直接构造「已被强拆收尾」的标记（生产端写入点在 finalizeStalledGuard
	// 身份校验的同一临界区）。
	m.mu.Lock()
	session.finalizedRunSeq = 1
	m.mu.Unlock()
	if m.agentAIRunSideEffectsAllowed("sess-clear", 1) {
		t.Fatal("precondition: force-finalized run must be fenced")
	}

	m.applyLocalSlashCommand(session, "clear", "", "agentcore")

	if session.runSeq != 0 || session.finalizedRunSeq != 0 {
		t.Fatalf("after /clear runSeq=%d finalizedRunSeq=%d, want 0/0",
			session.runSeq, session.finalizedRunSeq)
	}
	// /clear 之后复用同一 runSeq 是合法回合，绝不能被旧标记挡住。
	if !m.agentAIRunSideEffectsAllowed("sess-clear", 1) {
		t.Fatal("a run after /clear must not inherit the previous fence")
	}
}
```

- [ ] **Step 9: runAgentCore 标记 done**

`app/http/services/agent_ai_agentcore.go:681` 附近，已有的 `defer m.clearRunning(run.sessionID, run.runSeq, writeJSON)` 之后加：

```go
	// LIFO：本行后注册，先于 clearRunning 执行——guard 必须在会话复位前把自己
	// 标记为「已正常返回」，否则宽限计时器会误判成僵尸。
	defer m.markRunGuardDone(run.sessionID, run.runSeq)
```

- [ ] **Step 10: 跑测试**

Run: `go test ./app/http/services/ -run 'TestRunGuard|TestRunSideEffectsFenced' -v`
Expected: PASS。

- [ ] **Step 11: 全包回归**

Run: `go test ./app/http/services/ -count=1 -timeout 600s`
Expected: 除基线既有失败外全绿。

- [ ] **Step 12: 提交**

```bash
git add app/http/services/agent_ai_run_guard.go app/http/services/agent_ai_run_guard_test.go app/http/services/agent_ai.go app/http/services/agent_ai_agentcore.go
git commit -m "$(cat <<'EOF'
新增：回合 guardian——kill 可观测、宽限强拆、非帧副作用 runSeq 守卫

看门狗 kill 此前零日志且 cancel 可能无效，僵尸回合永久悬空。现在每次
判死都留 [ai.run.kill] 轨迹；cancel 后 30s 未返回即强制收尾（发恰好一个
终态错误 + 会话复位）；僵尸后续的 history/journal/resume 写被 runSeq 守卫丢弃。

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: 硬顶改「引擎活跃时长」+ 默认 2h

**Files:**
- Modify: `app/http/services/agent_execution_guard.go:30-85`（旧口径注释）、`app/http/services/agent_execution_guard.go:92-154`（默认值 + 三处注释）、`app/http/services/agent_ai.go:1294-1354`（逐拍累计）
- Test: `app/http/services/agent_ai_watchdog_test.go`（追加）、`app/http/services/agent_execution_guard_test.go:39-64`（既有默认值断言同步改）

- [ ] **Step 1: 写失败测试**

追加到 `app/http/services/agent_ai_watchdog_test.go`：

```go
func TestAgentAIWatchdogHardCeilingExcludesWaitingStates(t *testing.T) {
	// 硬顶只看引擎活跃时长：整段处于工具等待态的回合不应被硬顶杀掉。
	activity := newAgentAIActivity()
	activity.beginToolUseWait()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	killed := make(chan string, 1)
	loopCancel := func() { killed <- activity.killReasonOr("") }

	go agentAIWatchdogLoop(ctx, activity, loopCancel,
		// idle 分支是唯一没有 `>0` 禁用开关的（条件为
		// `!idlePaused() && idleFor() > idleWindow`），传 0 反而「一 tick 就杀」。
		// 所以用 time.Hour 让它永不触发，与既有 TestAgentAIWatchdogLoopHardCeilingKills 写法一致。
		time.Hour,           // idleWindow: 实际关闭
		20*time.Millisecond, // hardCeiling: 引擎活跃 20ms
		0,                   // noProgressWindow: 关
		0,                   // toolWaitCeiling: 关（本用例只验硬顶口径）
		0,                   // subagentCeiling: 关
		5*time.Millisecond,
	)

	select {
	case reason := <-killed:
		t.Fatalf("hard ceiling killed a pure tool-wait run (reason=%q)", reason)
	case <-time.After(120 * time.Millisecond):
		// 期望：等待态不计入引擎时钟，硬顶不触发
	}

	activity.endToolUseWait()

	select {
	case reason := <-killed:
		if reason != "hard_ceiling" {
			t.Fatalf("kill reason = %q, want hard_ceiling", reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hard ceiling did not fire after the run left the waiting state")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestAgentAIWatchdogHardCeilingExcludesWaitingStates -v`
Expected: FAIL，`hard ceiling killed a pure tool-wait run`（旧口径按 runStart 计）。

- [ ] **Step 3: 改 watchdog 计时口径**

`agentAIWatchdogLoop` 开头：

```go
func agentAIWatchdogLoop(ctx context.Context, activity *agentAIActivity, cancel context.CancelFunc, idleWindow, hardCeiling, noProgressWindow, toolWaitCeiling, subagentCeiling, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// 引擎活跃时钟：逐拍累加，但处于任一合法等待态（审批/工具/子代理）的拍
	// 不计入。口径见 spec §3.2.4——等待态各自有独立天花板，硬顶只管真实
	// 干活时长，否则 3h 的子代理批处理会被 2h 硬顶误杀。
	lastTick := time.Now()
	var engineActive time.Duration
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			delta := now.Sub(lastTick)
			lastTick = now
			if !activity.idlePaused() {
				engineActive += delta
			}
```

**不要新增第二个 `case` 子句**（会 duplicate case 编译失败）：把原来的 `case <-ticker.C:` 这一行**替换成**上面 `case now := <-ticker.C:` 开头那一段（含 `delta`/`lastTick`/`engineActive` 四行），其后的各分支判断（idle → noProgress → toolWait → subagent）保持原样不动。

最后一条硬顶判断改为：

```go
			if hardCeiling > 0 && engineActive > hardCeiling {
				activity.setKillReason("hard_ceiling")
				cancel()
				return
			}
```

- [ ] **Step 4: 改默认值与注释（4a–4f 逐处外科手术，`var ( … )` 结构与其余变量一律不动）**

`app/http/services/agent_execution_guard.go`。**不要整块替换 `var ( … )`**——那个块里有 5 个变量，只动下面 4a–4e 这几处；另有一处既有单测必须同步改（4f，`agent_execution_guard_test.go`）。

**4a.** `agentAIHardCeiling` 的默认值：`48*time.Hour` → `2*time.Hour`。

```go
	agentAIHardCeiling     = resolveEnvDuration("ALIANG_AI_HARD_CEILING", 2*time.Hour)
```

**4b.** 把 `var (` **上方**那段注释（从 `// agentAIApprovalTimeout bounds` 到 `// fall back to the default (see resolveEnvDuration, which is unit-tested).`）整段换成：

```go
// agentAIApprovalTimeout bounds how long a single approval request waits for a
// human decision before being cancelled (→ denied). Env-configurable:
//
//	ALIANG_AI_APPROVAL_TIMEOUT  (time.ParseDuration form, e.g. "24h", "30m"). Default 24h.
//
// agentAIHardCeiling is the runaway backstop for ENGINE-ACTIVE time: the
// watchdog accumulates elapsed time tick by tick and deliberately does NOT
// count ticks spent in a legitimate waiting state (awaiting a human approval,
// a pending tool result, or an active subagent). Each waiting state has its
// own bound (agentAIApprovalTimeout / agentAIToolWaitCeiling /
// agentAISubagentWaitCeiling), so a long approval, a 3h subagent batch or a
// 10min tool call can never be clipped by this ceiling — only time actually
// spent running the engine counts. Env-configurable:
//
//	ALIANG_AI_HARD_CEILING  (time.ParseDuration form). Default 2h.
//
// Both resolve once at process start; blank/unparseable/non-positive values
// fall back to the default (see resolveEnvDuration, which is unit-tested).
```

**4b2.** 第一个 `var (`（源码 30-85，变量 `agentAIIdleWindow` 那个块）里也有一段旧语义，在 `agentAIIdleWindow` 上方注释的 `:66-68`：

```go
	// hard ceiling (agentAIHardCeiling) is a runaway backstop that fires from run
	// start regardless of activity; it MUST exceed agentAIApprovalTimeout or a
	// long approval wait is clipped by the ceiling first. A run/session ending or
```

换成：

```go
	// hard ceiling (agentAIHardCeiling) is a runaway backstop for ENGINE-ACTIVE
	// time: ticks spent awaiting a human decision, a tool result, or a subagent
	// are excluded (each has its own bound), so it carries no ordering
	// constraint against agentAIApprovalTimeout. A run/session ending or
```

（原文「fires from run start regardless of activity」「MUST exceed agentAIApprovalTimeout」正是新口径下不再成立的两句，留着重则自相矛盾。）

**4c.** `agentAIToolWaitCeiling` 注释末尾这 4 行（源码 136-139）：

```go
	// Keep it below agentAIHardCeiling — that backstop fires on total elapsed
	// run time, so a value above it neutralizes this guard. Approval waits
	// never count against this ceiling (the clock is paused while a human
	// decision is pending) and remain bounded by agentAIApprovalTimeout.
```

换成：

```go
	// 无需再与 agentAIHardCeiling 比较大小：硬顶只累加引擎活跃时长，工具等待
	// 的拍不计入，2h 硬顶永远不会在这条等待里先触发（它会由本上限或 stale
	// 扫描器收敛）。审批等待的拍同样不计入本计时（挂起即停表），由
	// agentAIApprovalTimeout 管辖。
```

**4c2.** 同一段注释上方 `:130` 还有一句旧口径（工具等待里说「会一直挂到硬顶」——新口径下硬顶不计等待拍，永不触发）：

```go
	// "running" until the hard ceiling. Env:
```

换成：

```go
	// "running" until the stale-run sweeper reclaims it. Env:
```

**4d.** `agentAISubagentWaitCeiling` 注释末尾这 4 行（源码 149-152）：

```go
	// Explicitly "0s" disables. Keep below agentAIHardCeiling (48h) — same
	// reasoning as the tool-wait ceiling. Active-subagent time also pauses the
	// idle/no-progress guards (idlePaused), so without this bound a marquee
	// subagent would pin the run until the hard ceiling.
```

换成：

```go
	// Explicitly "0s" disables. No ordering constraint against
	// agentAIHardCeiling any more — the hard ceiling counts only engine-active
	// ticks, so subagent-wait time is never clipped by it. Active-subagent
	// time also pauses the idle/no-progress guards (idlePaused), so without
	// this bound a marquee subagent would pin the run indefinitely.
```

**4e.** `app/http/services/agent_ai.go:1324` 的 tool-wait 注释里同一句旧口径：

```go
			// subagent pins the run until the hard ceiling while the phone
```

换成：

```go
			// subagent pins the run indefinitely while the phone
```

**4f.** 既有默认值用例 `app/http/services/agent_execution_guard_test.go:39-64`（`TestAgentAITimeoutDefaults`）会因 4a **变红**（`:52` 断言 48h、`:55` 断言硬顶 > 审批超时，2h > 24h 为假直接 `t.Fatalf`）。把 `:52-57` 这 6 行：

```go
	if agentAIHardCeiling != 48*time.Hour {
		t.Fatalf("default agentAIHardCeiling = %s, want 48h", agentAIHardCeiling)
	}
	if agentAIHardCeiling <= agentAIApprovalTimeout {
		t.Fatalf("agentAIHardCeiling (%s) must exceed agentAIApprovalTimeout (%s)", agentAIHardCeiling, agentAIApprovalTimeout)
	}
```

换成：

```go
	if agentAIHardCeiling != 2*time.Hour {
		t.Fatalf("default agentAIHardCeiling = %s, want 2h", agentAIHardCeiling)
	}
	// 不再断言 agentAIHardCeiling > agentAIApprovalTimeout：硬顶只累加引擎
	// 活跃时长，审批挂起的拍被排除，审批窗口由 agentAIApprovalTimeout 独立管辖。
```

`:58-63` 的两条断言（toolWait == 1h、toolWait < hardCeiling）在新默认值下仍然成立（1h < 2h），**保持不动**。

- [ ] **Step 5: 跑测试**

Run: `go test ./app/http/services/ -run 'TestAgentAIWatchdog|TestAgentAITimeoutDefaults' -v`
Expected: 全绿（含既有 HardCeilingKills 与刚改的默认值用例）。

- [ ] **Step 6: 提交**

```bash
git add app/http/services/agent_execution_guard.go app/http/services/agent_execution_guard_test.go app/http/services/agent_ai.go app/http/services/agent_ai_watchdog_test.go
git commit -m "$(cat <<'EOF'
修复：硬顶改为引擎活跃时长口径，默认 48h 降到 2h

旧口径按 run 起点计总时长，把长审批/长子代理当成 runaway；直接把 48h
降到 2h 又会误杀合法等待。现在逐拍累加，等待态（审批/工具/子代理）的
拍不计入，各自天花板继续独立生效。

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: 等待态时钟补全 + stale running 扫描器

**Files:**
- Modify: `app/http/services/agent_ai.go:1040-1130`（activity 字段与方法）、`app/http/services/agent_ai_run_guard.go`（扫描器）
- Test: `app/http/services/agent_ai_run_guard_test.go`（追加）

- [ ] **Step 1: 写失败测试**

追加到 `app/http/services/agent_ai_run_guard_test.go`：

```go
func TestStaleScannerKillsNonWaitingRunEvenWithHeartbeat(t *testing.T) {
	oldThreshold := agentAIStaleRunThreshold
	agentAIStaleRunThreshold = 5 * time.Minute
	t.Cleanup(func() { agentAIStaleRunThreshold = oldThreshold })

	m, session, guard, rec := newGuardTestManager(t)
	oldGrace := agentAIKillGrace
	agentAIKillGrace = 20 * time.Millisecond
	t.Cleanup(func() { agentAIKillGrace = oldGrace })

	// 只让「进度心跳」在流：心跳不经 activity，lastActivityAt 仍是旧的。
	guard.activity.lastActivityAt.Store(time.Now().Add(-10 * time.Minute).UnixNano())
	guard.activity.lastProgressAt.Store(time.Now().Add(-10 * time.Minute).UnixNano())

	m.scanStaleRuns()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(rec.snapshot()) == 1 {
			if got := rec.snapshot()[0]["error_code"]; got != "turn_stalled" {
				t.Fatalf("error_code = %v, want turn_stalled", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("scanner did not converge the stale run (session=%s)", session.id)
}

func TestStaleScannerSparesHealthyToolWait(t *testing.T) {
	m, _, guard, rec := newGuardTestManager(t)
	guard.activity.beginToolUseWait()
	guard.activity.lastActivityAt.Store(time.Now().Add(-30 * time.Minute).UnixNano())

	m.scanStaleRuns()

	time.Sleep(100 * time.Millisecond)
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("healthy tool wait was killed (payloads=%d)", got)
	}
}

func TestStaleDeathReasonApprovalOverflow(t *testing.T) {
	a := newAgentAIActivity()
	a.setAwaitingApproval(true)
	a.approvalWaitStartedAt.Store(time.Now().Add(-25 * time.Hour).UnixNano())

	reason, dead := a.staleDeathReason(5*time.Minute, 24*time.Hour, time.Hour, 6*time.Hour)
	if !dead || reason != "approval_timeout" {
		t.Fatalf("staleDeathReason = (%q, %t), want (approval_timeout, true)", reason, dead)
	}

	// 3h 审批（> 2h 硬顶）既不该被 stale 判死，也不该被审批天花板判死。
	b := newAgentAIActivity()
	b.setAwaitingApproval(true)
	b.approvalWaitStartedAt.Store(time.Now().Add(-3 * time.Hour).UnixNano())
	if reason, dead := b.staleDeathReason(5*time.Minute, 24*time.Hour, time.Hour, 6*time.Hour); dead {
		t.Fatalf("3h approval was judged dead (reason=%q)", reason)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run 'TestStaleScanner|TestStaleDeathReason' -v`
Expected: FAIL，`undefined: agentAIStaleRunThreshold` / `scanStaleRuns` / `approvalWaitStartedAt`。

- [ ] **Step 3: activity 加审批等待时钟**

`app/http/services/agent_ai.go`，`agentAIActivity` 结构体 `toolWaitStartedAt atomic.Int64` 之前加：

```go
	// approvalWaitStartedAt 是当前这段「等人工决策」的开始时刻（0=不在等）。
	// 与 toolWaitStartedAt 互斥：审批期间工具时钟清零，反之亦然。审批等待
	// 由 agentAIApprovalTimeout 单独约束，stale 扫描器也要能读到它。
	approvalWaitStartedAt atomic.Int64
```

`setAwaitingApproval` 改为：

```go
func (a *agentAIActivity) setAwaitingApproval(v bool) {
	if a == nil {
		return
	}
	a.awaitingApproval.Store(v)
	if v {
		// Approval time is human time, not tool time: pause the tool-wait
		// clock so a slow decision never trips the tool-wait ceiling.
		a.approvalWaitStartedAt.Store(time.Now().UnixNano())
		a.toolWaitStartedAt.Store(0)
	} else {
		a.approvalWaitStartedAt.Store(0)
		if a.pendingToolUses.Load() > 0 {
			// Decision resolved but the tool is still in flight: re-arm with a
			// fresh ceiling window for the remaining execution.
			a.toolWaitStartedAt.Store(time.Now().UnixNano())
		}
	}
	a.bump()
}

// approvalWaitFor 报告当前这段审批等待已持续多久，0 = 不在等待。
func (a *agentAIActivity) approvalWaitFor() time.Duration {
	if a == nil {
		return 0
	}
	ts := a.approvalWaitStartedAt.Load()
	if ts == 0 {
		return 0
	}
	return time.Since(time.Unix(0, ts))
}
```

在 `idlePaused()` 之后加：

```go
// staleDeathReason 判定这个 run 是否已经「无响应死」，返回原因码与结论。
//
// 口径（spec §3.3.1/§3.3.2）：
//   - 非等待态：lastActivityAt 距今超过 staleThreshold → stalled。activity 只由
//     真实事件帧/审批帧/steer ack 刷新，ai.run.progress 心跳不参与（否则心跳
//     会把一切判死判据击穿）。
//   - 等待态：不按 stale 阈值判死，只服各自的既有天花板——审批
//     approvalTimeout、工具 toolCeiling、子代理 subagentCeiling。
func (a *agentAIActivity) staleDeathReason(staleThreshold, approvalTimeout, toolCeiling, subagentCeiling time.Duration) (string, bool) {
	if a == nil {
		return "", false
	}
	if a.awaiting() {
		if approvalTimeout > 0 && a.approvalWaitFor() > approvalTimeout {
			return "approval_timeout", true
		}
		return "", false
	}
	if a.pendingToolUses.Load() > 0 || a.activeSubagents.Load() > 0 {
		if toolCeiling > 0 && a.toolWaitFor() > toolCeiling {
			return "tool_wait_timeout", true
		}
		if subagentCeiling > 0 && a.subagentWaitFor() > subagentCeiling {
			return "subagent_wait_timeout", true
		}
		return "", false
	}
	if staleThreshold > 0 && a.idleFor() > staleThreshold {
		return "stalled", true
	}
	return "", false
}
```

- [ ] **Step 4: 加扫描器**

追加到 `app/http/services/agent_ai_run_guard.go`：

```go
// agentAIStaleRunThreshold 是「非等待态下多久没有任何活性事件就判死」。
//
//	ALIANG_AI_STALE_RUN  (time.ParseDuration). Default 5min.
var agentAIStaleRunThreshold = resolveEnvDuration("ALIANG_AI_STALE_RUN", 5*time.Minute)

// agentAIStaleScanInterval 是 stale running 自检的扫描周期。
var agentAIStaleScanInterval = time.Minute

// startStaleRunScanner 为这个 manager 启动唯一一条扫描 goroutine。
//
// 只在第一次注册 guard 时启动（sync.Once），扫到进程退出为止：进程级兜底，
// 不绑定任何单回合的 ctx。
//
// 生命周期取舍：goroutine 不随 manager 回收，测试里新建的 manager 会各留一条
// 常驻扫描（ticker 1min 一次，空转开销可忽略）。这是刻意接受的——生产只有
// 一个全局 manager；扫描周期已是 package 级 var，测试需要时可以调大。
func (m *agentAIManager) startStaleRunScanner() {
	if m == nil {
		return
	}
	m.scanOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(agentAIStaleScanInterval)
			defer ticker.Stop()
			for range ticker.C {
				m.scanStaleRuns()
			}
		}()
	})
}

// scanStaleRuns 扫描所有运行中会话，把已判死的交给同一个 guardian kill 通道。
//
// 不自己发终态：终态唯一性由 emitter 终态闸保证，扫描器只是「发现者」。
func (m *agentAIManager) scanStaleRuns() {
	if m == nil {
		return
	}
	type victim struct {
		guard  *agentAIRunGuard
		reason string
	}
	var victims []victim
	m.mu.Lock()
	for _, session := range m.sessions {
		guard := session.guard
		// guard != nil 才是「这是个在跑的真回合」的真正判据（Task 4 起所有
		// provider 回合都会挂 guard）。session.cancel == nil 是历史遗留的
		// 廉价前置条件，只为跳过从未起跑的会话，不要把它当成僵尸判据去「优化」。
		if guard == nil || session.cancel == nil || guard.activity == nil {
			continue
		}
		reason, dead := guard.activity.staleDeathReason(
			agentAIStaleRunThreshold,
			agentAIApprovalTimeout,
			agentAIToolWaitCeiling,
			agentAISubagentWaitCeiling,
		)
		if dead {
			victims = append(victims, victim{guard: guard, reason: reason})
		}
	}
	m.mu.Unlock()
	for _, v := range victims {
		v.guard.kill(v.reason)
	}
}
```

- [ ] **Step 5: manager 加 scanOnce 字段并启动**

`agentAIManager` 结构体末尾加：

```go
	// scanOnce 保证 stale 扫描器每个 manager 只启动一条。
	scanOnce sync.Once
```

`attachRunGuard` 末尾（`m.mu.Unlock()` 之后）加：

```go
	m.startStaleRunScanner()
```

- [ ] **Step 6: 跑测试**

Run: `go test ./app/http/services/ -run 'TestStaleScanner|TestStaleDeathReason' -v`
Expected: PASS。

- [ ] **Step 7: 提交**

```bash
git add app/http/services/agent_ai.go app/http/services/agent_ai_run_guard.go app/http/services/agent_ai_run_guard_test.go
git commit -m "$(cat <<'EOF'
新增：stale running 自检扫描器 + 审批等待时钟

每分钟扫描运行中会话：非等待态 5min 无任何活性事件即判死，交给 guardian
收尾；等待态各服自己的天花板。进度心跳不参与活性口径，避免心跳击穿判死。

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: 活性口径补全（steer ack 计入）

**Files:**
- Modify: `app/http/services/agent_ai_agentcore.go:1010-1060`
- Test: `app/http/services/agent_ai_agentcore_steer_test.go`（**新建**，Task 9/10 往同一文件追加）

- [ ] **Step 1: 新建测试文件并写失败测试**

`app/http/services/agent_ai_agentcore_steer_test.go` **当前不存在，本步新建**。先写文件头：

```go
package services

import (
	"testing"
	"time"
)

// 依赖：recordingWriter 定义在同包的 agent_ai_run_guard_test.go（Task 4），
// 所以本 Task 必须排在 Task 4 之后执行。
```

再在文件里加用例：

```go
func TestCoreSteerDrainBumpsActivity(t *testing.T) {
	activity := newAgentAIActivity()
	activity.lastActivityAt.Store(time.Now().Add(-time.Hour).UnixNano())

	rec := &recordingWriter{}
	run := agentAIRun{sessionID: "sess-1", runSeq: 1, messageID: "msg-1", activity: activity}
	control := newAgentAICoreSteerControl(run, rec.write)
	control.activity = activity

	if _, err := control.deliver(agentAISteerMessage{MessageID: "steer-1", Content: "hi"}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if got := control.drain(); len(got) != 1 {
		t.Fatalf("drain = %d messages, want 1", len(got))
	}
	if activity.idleFor() > time.Minute {
		t.Fatalf("drain did not refresh lastActivityAt (idleFor=%s)", activity.idleFor())
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestCoreSteerDrainBumpsActivity -v`
Expected: FAIL，`drain did not refresh lastActivityAt`。

- [ ] **Step 3: 实现**

`agentAICoreSteerControl` 结构体加字段：

```go
	// activity 用于把「插话被取走」计入 run 活性（spec §3.3.1）：内核 drain 发生
	// 在下一次 LLM 调用前，那一瞬间说明回合确实活着。
	activity *agentAIActivity
```

`drain` 的循环里，`emitAgentAISteerAck(...)` 之前加：

```go
		if c.activity != nil {
			c.activity.bump()
		}
```

- [ ] **Step 4: 接线**

`runAgentCore` 里 `coreSteer := newAgentAICoreSteerControl(run, writeJSON)`（第 820 行）之后、**`m.attachCoreSteer(...)`（第 821 行）之前**加：

```go
	coreSteer.activity = run.activity
```

> 顺序是要点：`attachCoreSteer` 一旦执行，别的 goroutine 就可能通过 `coreSteerFor` 拿到这个 control 并用它。字段必须在挂载**之前**赋完，否则存在「已挂载但 `activity` 还是 nil」的窗口。

- [ ] **Step 5: 跑测试**

Run: `go test ./app/http/services/ -run TestCoreSteer -v`
Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add app/http/services/agent_ai_agentcore.go app/http/services/agent_ai_agentcore_steer_test.go
git commit -m "$(cat <<'EOF'
修复：steer 被取走时刷新回合活性

drain 发生在下一次 LLM 调用前，是回合活着的直接证据；此前只发 applied
ack 不刷活性，会让 stale 判据偏保守。

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: 僵尸非帧副作用加守卫

**Files:**
- Modify: `app/http/services/agent_ai_agentcore.go:963-966`
- Test: `app/http/services/agent_ai_agentcore_run_test.go`（追加）

> 行号基准：成功收尾段目前是 963-966 四行
> （`m.appendAssistantHistory` / `journal.appendAssistant` / `endStatus = "success"` /
> `m.setAgentAIResumeSessionIDIfEmpty`）。**`journal.appendEnd`（686-687 的 defer）
> 和 `finalizeRunSubagents`（985）不在守卫范围内**：前者必须无条件写，只让它的
> `status` 如实反映「本轮未落库」；后者必须无条件跑，否则被丢弃的回合会留下悬空
> 的子代理 emitter 与看门狗。

- [ ] **Step 1: 写失败测试（端到端 + 可观测）**

追加到 `app/http/services/agent_ai_agentcore_run_test.go`。这个用例跑**真实**的一轮
成功 run，但在第一帧 `ai.delta` 时打上强拆标记（模拟「宽限期到点，僵尸却还在
流式输出」），然后断言三处非帧副作用都没落地：

```go
// TestZombieSuccessWritesAreDiscarded 锁死 Task 8 的接线本身。
//
// 只断言谓词（agentAIRunSideEffectsAllowed）是空转的：真正会回归的是
// 「守卫忘了包住成功收尾段」。所以这里跑完整的一轮 run，在流式途中打上强拆
// 标记，再检查 history / resume / journal 三处都没被僵尸污染。
// 删掉 Task 8 加的守卫后，本用例必然变红。
func TestZombieSuccessWritesAreDiscarded(t *testing.T) {
	setTestAgentHome(t)
	_, srv := newAgentCoreUpstream(t, agentCoreTextTurn("你好", "，世界"))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")

	m := newAgentAIManager()
	const sessionID = "conv-agentcore-zombie"
	const runSeq = 7
	registerTestAISession(t, m, &agentAISession{
		id:      sessionID,
		runSeq:  runSeq,
		cancel:  func() {},
		history: []agentAIMessage{{Role: "user", Content: "跑一半被强拆"}},
	})

	log := &agentCoreEventLog{}
	fenced := false
	write := func(v interface{}) error {
		if !fenced {
			if frame, ok := v.(map[string]interface{}); ok {
				if typ, _ := frame["type"].(string); typ == models.AgentEventAIDelta {
					// 强拆发生在僵尸仍在输出之后：这正是迟返回写污染的窗口。
					// 直接构造 finalizeStalledGuard 会写的同一个标记。
					m.mu.Lock()
					if sess := m.sessions[sessionID]; sess != nil && sess.runSeq == runSeq {
						sess.finalizedRunSeq = runSeq
					}
					m.mu.Unlock()
					fenced = true
				}
			}
		}
		return log.write(v)
	}

	run := agentAIRun{
		sessionID:   sessionID,
		runSeq:      runSeq,
		messageID:   "msg-zombie",
		provider:    "agentcore",
		model:       "glm-5.2",
		projectPath: t.TempDir(),
		activity:    newAgentAIActivity(),
	}
	m.runAgentCore(context.Background(), run, write)

	if !fenced {
		t.Fatal("用例没触发强拆标记（没收到 ai.delta），断言无效")
	}

	// 1) 历史不得追加 assistant：否则 Retry 新回合会把僵尸的产出当成自己的上文。
	m.mu.Lock()
	history := append([]agentAIMessage(nil), m.sessions[sessionID].history...)
	resume := m.sessions[sessionID].resumeSessionID
	m.mu.Unlock()
	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1（僵尸的 assistant 输出必须被丢弃）", len(history))
	}
	if resume != "" {
		t.Fatalf("resumeSessionID = %q, want 空（僵尸不得为下一回合留恢复点）", resume)
	}

	// 2) journal 不得出现 assistant 行，end 行必须如实记 error。
	rows := readAgentCoreJournalRows(t, sessionID)
	for _, row := range rows {
		if role, _ := row["role"].(string); role == "assistant" {
			t.Fatalf("journal 出现僵尸 assistant 行：%v", row)
		}
	}
	last := rows[len(rows)-1]
	if status, _ := last["status"].(string); status != "error" {
		t.Fatalf("journal end status = %q, want error（被丢弃的回合不能记成功）", status)
	}
	// 3) 终态不受影响：守的是非帧副作用，不是帧流水。
	if got := log.count(models.AgentEventAIDone); got != 1 {
		t.Fatalf("ai.done 数量 = %d, want 1（事件 %v）", got, log.types())
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run 'TestZombieSuccessWritesAreDiscarded' -count=1 -v`
Expected: FAIL，`history len = 2, want 1`——守卫还没接到成功收尾段。

- [ ] **Step 3: 接到 runAgentCore 成功路径**

`app/http/services/agent_ai_agentcore.go:963-966`，把这三处非帧副作用整体包进守卫
（`endStatus = "success"` 一并包进去）：

```go
	if m.agentAIRunSideEffectsAllowed(run.sessionID, run.runSeq) {
		m.appendAssistantHistory(run.sessionID, run.runSeq, run.messageID, assistantOutput)
		journal.appendAssistant(run.sessionID, agentAssistantMessageID(run.messageID), assistantOutput, time.Now().UTC().Format(time.RFC3339))
		endStatus = "success"
		m.setAgentAIResumeSessionIDIfEmpty(run.sessionID, run.runSeq, capturedResumeSessionID)
	} else {
		// 僵尸迟返回：强拆之后 runSeq 未变但已打 finalizedRunSeq 标记，这些非帧
		// 副作用会污染 Retry 新回合的历史/恢复点，必须丢弃（spec §2.4 / §3.2.2）。
		// endStatus 保持默认 "error"：deferred journal.appendEnd 必须体现
		// 「这一轮没有落库的成功结果」，否则 journal 会出现 end(success) 却查无
		// assistant 行的自相矛盾记录。
		logger.Warn(fmt.Sprintf("[ai.run.zombie_discard] session=%s runSeq=%d run=%s reason=stale_side_effects",
			run.sessionID, run.runSeq, run.runID))
	}
```

> 三处调用的实际名称/参数以该段现有代码为准，只加外层守卫与 else 分支，
> 不改内部参数。守卫**不要**包住后面的 `journal.appendEnd`（defer 在 686-687）、
> `finalizeRunSubagents`（985）、`ai.done`（986）——那些必须无条件执行。

- [ ] **Step 4: 跑测试**

Run: `go test ./app/http/services/ -run 'TestZombieSuccessWritesAreDiscarded|TestRunSideEffectsFenced|TestRunAgentCore|TestAgentCore' -count=1 -v`
Expected: PASS（含既有的 `TestRunAgentCoreEmitsSingleDone` #8「history 追加 assistant」
与四个 journal 用例——它们注册的会话本来就没有 cancel，正是 Step 2 里
`finalizedRunSeq` 判据要保护的那批，若守卫误用 `cancel == nil`，这里会一起变红）。

- [ ] **Step 5: P0 全量回归**

Run: `go test ./app/... -count=1 -timeout 900s`
Expected: 除基线既有失败外全绿。

- [ ] **Step 6: 提交**

```bash
git add app/http/services/agent_ai_agentcore.go app/http/services/agent_ai_agentcore_run_test.go
git commit -m "$(cat <<'EOF'
修复：僵尸迟返回的非帧副作用按 runSeq 守卫丢弃

强拆之后 runSeq 未变但 finalizedRunSeq 已打标，僵尸的 history/journal/
resume 写会污染 Retry 新回合。成功收尾段统一过谓词，被拒时留
zombie_discard 日志。

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

## P1 反馈闭环

### Task 9: steer 活性窗 + `turn_stalled` 反馈

**Files:**
- Modify: `app/http/services/agent_ai_run_guard.go`（常量）、`app/http/services/agent_ai_agentcore.go:1001-1060`、`app/http/services/agent_ai_agentcore.go:820`
- Test: `app/http/services/agent_ai_agentcore_steer_test.go`（追加）

- [ ] **Step 1: 写失败测试**

```go
// queueLenOf 读队列长度（同包测试用，走同一把锁）。
func queueLenOf(c *agentAICoreSteerControl) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queue)
}

// pendingLenOf 读在册定时器数量（同包测试用），用来证明窗口确实被停掉。
func pendingLenOf(c *agentAICoreSteerControl) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}

func TestCoreSteerLivenessWindowEmitsTurnStalled(t *testing.T) {
	oldWindow := steerLivenessWindow
	steerLivenessWindow = 40 * time.Millisecond
	t.Cleanup(func() { steerLivenessWindow = oldWindow })

	activity := newAgentAIActivity()
	rec := &recordingWriter{}
	run := agentAIRun{sessionID: "sess-1", runSeq: 1, messageID: "msg-1", activity: activity}
	control := newAgentAICoreSteerControl(run, rec.write)
	control.activity = activity
	stalled := make(chan string, 1)
	control.onStalled = func(reason string) { stalled <- reason }

	if _, err := control.deliver(agentAISteerMessage{MessageID: "steer-9", Content: "hi"}); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	select {
	case <-stalled:
	case <-time.After(2 * time.Second):
		t.Fatal("liveness window did not fire")
	}

	payloads := rec.snapshot()
	if len(payloads) != 1 {
		t.Fatalf("acks = %d, want 1", len(payloads))
	}
	if payloads[0]["result"] != "error" || payloads[0]["code"] != "turn_stalled" {
		t.Fatalf("ack = %v, want result=error code=turn_stalled", payloads[0])
	}
}

func TestCoreSteerLivenessWindowSparesToolWait(t *testing.T) {
	oldWindow := steerLivenessWindow
	steerLivenessWindow = 40 * time.Millisecond
	t.Cleanup(func() { steerLivenessWindow = oldWindow })

	activity := newAgentAIActivity()
	activity.beginToolUseWait()

	rec := &recordingWriter{}
	run := agentAIRun{sessionID: "sess-1", runSeq: 1, messageID: "msg-1", activity: activity}
	control := newAgentAICoreSteerControl(run, rec.write)
	control.activity = activity
	control.onStalled = func(string) { t.Error("must not judge dead during a legal waiting state") }
	// 确定性停窗，避免定时器在测试返回之后触发 t.Error（会 panic）。
	t.Cleanup(func() { control.close("not_running", "", "") })

	_, _ = control.deliver(agentAISteerMessage{MessageID: "steer-10", Content: "hi"})
	time.Sleep(150 * time.Millisecond)
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("stall ack emitted during tool wait: %d", got)
	}
	// 等待态也不许从队列里摘掉消息：解冻后它必须还能被 drain 取走。
	if queueLenOf(control) != 1 {
		t.Fatalf("queued = %d, want 1 (message must survive a legal wait)", queueLenOf(control))
	}
}

// spec §5 #11：等审批期间是无响应还是合法静默，必须靠 idlePaused 区分，
// 不能只看「多久没动静」——人可能一晚上没点那个卡片。
func TestCoreSteerLivenessWindowSparesApprovalWait(t *testing.T) {
	oldWindow := steerLivenessWindow
	steerLivenessWindow = 40 * time.Millisecond
	t.Cleanup(func() { steerLivenessWindow = oldWindow })

	activity := newAgentAIActivity()
	activity.setAwaitingApproval(true)
	defer activity.setAwaitingApproval(false)

	rec := &recordingWriter{}
	run := agentAIRun{sessionID: "sess-1", runSeq: 1, messageID: "msg-1", activity: activity}
	control := newAgentAICoreSteerControl(run, rec.write)
	control.activity = activity
	control.onStalled = func(string) { t.Error("must not judge dead while an approval is pending") }
	// 确定性停窗，避免定时器在测试返回之后触发 t.Error（会 panic）。
	t.Cleanup(func() { control.close("not_running", "", "") })

	_, _ = control.deliver(agentAISteerMessage{MessageID: "steer-12", Content: "hi"})
	time.Sleep(150 * time.Millisecond)
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("stall ack emitted during approval wait: %d", got)
	}
	if queueLenOf(control) != 1 {
		t.Fatalf("queued = %d, want 1 (message must survive approval wait)", queueLenOf(control))
	}
}

// spec §5 #12 的对应面：子代理在跑时同样属于合法等待，不能被活性窗判死。
func TestCoreSteerLivenessWindowSparesSubagentWait(t *testing.T) {
	oldWindow := steerLivenessWindow
	steerLivenessWindow = 40 * time.Millisecond
	t.Cleanup(func() { steerLivenessWindow = oldWindow })

	activity := newAgentAIActivity()
	activity.activeSubagents.Store(1)
	defer activity.activeSubagents.Store(0)

	rec := &recordingWriter{}
	run := agentAIRun{sessionID: "sess-1", runSeq: 1, messageID: "msg-1", activity: activity}
	control := newAgentAICoreSteerControl(run, rec.write)
	control.activity = activity
	control.onStalled = func(string) { t.Error("must not judge dead while a subagent is running") }
	// 确定性停窗，避免定时器在测试返回之后触发 t.Error（会 panic）。
	t.Cleanup(func() { control.close("not_running", "", "") })

	_, _ = control.deliver(agentAISteerMessage{MessageID: "steer-13", Content: "hi"})
	time.Sleep(150 * time.Millisecond)
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("stall ack emitted during subagent wait: %d", got)
	}
	if queueLenOf(control) != 1 {
		t.Fatalf("queued = %d, want 1 (message must survive subagent wait)", queueLenOf(control))
	}
}

func TestCoreSteerDrainStopsLivenessTimer(t *testing.T) {
	oldWindow := steerLivenessWindow
	steerLivenessWindow = 60 * time.Millisecond
	t.Cleanup(func() { steerLivenessWindow = oldWindow })

	activity := newAgentAIActivity()
	rec := &recordingWriter{}
	run := agentAIRun{sessionID: "sess-1", runSeq: 1, messageID: "msg-1", activity: activity}
	control := newAgentAICoreSteerControl(run, rec.write)
	control.activity = activity
	control.onStalled = func(string) { t.Error("drained steer must not be reported stalled") }

	_, _ = control.deliver(agentAISteerMessage{MessageID: "steer-11", Content: "hi"})
	_ = control.drain()
	// 承重断言：抽掉 drain 里的 stopLivenessLocked，这条就会红。只靠下面的
	// 负向 sleep 断言证明不了「窗已停」（expire 见队列空也会静默返回）。
	if got := pendingLenOf(control); got != 0 {
		t.Fatalf("pending timers = %d, want 0 after drain", got)
	}
	time.Sleep(150 * time.Millisecond)

	for _, p := range rec.snapshot() {
		if p["code"] == "turn_stalled" {
			t.Fatalf("unexpected turn_stalled ack: %v", p)
		}
	}
}

func TestCoreSteerLivenessWindowSparesActiveRun(t *testing.T) {
	// 回合仍在出字（delta 会 bump lastActivityAt）时，即便插话还没被 drain，
	// 也不能判死：否则一次超过窗口时长的大模型调用就会杀掉健康回合。
	// 把窗口调到 1 小时再直接调 expire，做到零 sleep、零竞态。
	oldWindow := steerLivenessWindow
	steerLivenessWindow = time.Hour
	t.Cleanup(func() { steerLivenessWindow = oldWindow })

	rec := &recordingWriter{}
	activity := newAgentAIActivity()
	run := agentAIRun{sessionID: "sess-active", runSeq: 1, messageID: "msg-active", activity: activity}
	control := newAgentAICoreSteerControl(run, rec.write)
	control.activity = activity
	stalled := false
	control.onStalled = func(string) { stalled = true }
	t.Cleanup(func() { control.close("not_running", "", "") })

	activity.bump() // 模拟刚出过字
	if _, err := control.deliver(agentAISteerMessage{MessageID: "a", Content: "插话"}); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	control.expire("a")

	if got := queueLenOf(control); got != 1 {
		t.Fatalf("queued = %d, want 1 (active run must keep the steer)", got)
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("acks = %d, want 0 (active run must not be reported stalled)", got)
	}
	if stalled {
		t.Fatal("onStalled called for an actively generating run")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestCoreSteerLiveness -v`
Expected: FAIL，`undefined: steerLivenessWindow` / `control.onStalled undefined`。

- [ ] **Step 3: 加常量**

`app/http/services/agent_ai_run_guard.go`：

```go
// steerLivenessWindow 是一条插话从「排队成功」到「被 drain 取走」的最长等待。
//
// 默认与 agentAIStaleRunThreshold 对齐（5min），不要调小：agentcore 传输层的
// 重试链（ResponseHeaderTimeout 120s × 最多 3 次 + 退避）全程不 bump 活性，
// 最坏合法静默可达 6min。窗口一旦短于它，就会抢在 stale 扫描器前面误杀「本会
// 自愈」的回合（例如第一次头部停滞 120s、第二次慢首字 90s，合计 210s 后成功）。
// 对齐之后本窗是扫描器判据的严格子集（多一条「排队时长 ≥ 窗」），只会更晚、
// 不会更早判死。
//
//	ALIANG_AGENTCORE_STEER_LIVENESS  (time.ParseDuration). Default 5min.
var steerLivenessWindow = resolveEnvDuration("ALIANG_AGENTCORE_STEER_LIVENESS", 5*time.Minute)
```

- [ ] **Step 4: 控制结构加活性窗**

`agentAICoreSteerControl` 结构体加字段：

```go
	// onStalled 在活性窗超时且回合处于非等待态时被调用（参数是判死原因），
	// 由 runAgentCore 接到 guardian.kill。
	// nil = 完全不起活性窗（既不发失败 ack 也不判死），仅供测试使用。
	onStalled func(reason string)
	// pending 记每条排队插话的活性窗计时器，drain/close 时停掉。
	pending map[string]*time.Timer
```

`deliver` 成功分支改为：

```go
	c.queue = append(c.queue, msg)
	c.armLivenessLocked(msg.MessageID)
	return "queued", nil
```

新增方法：

```go
// armLivenessLocked 给一条刚入队的插话起活性窗。调用方必须持有 c.mu。
//
// activity 为 nil 时直接不起窗（fail-safe）：没有活性就分不清「死」和「在等
// 审批」，宁可不判死也不误杀合法等待。生产路径在 attachCoreSteer 之前一定
// 赋好 activity，所以这只在接线错误时退化。
func (c *agentAICoreSteerControl) armLivenessLocked(messageID string) {
	if steerLivenessWindow <= 0 || c.onStalled == nil || c.activity == nil {
		return
	}
	if c.pending == nil {
		c.pending = make(map[string]*time.Timer)
	}
	if timer := c.pending[messageID]; timer != nil {
		timer.Stop()
	}
	c.pending[messageID] = time.AfterFunc(steerLivenessWindow, func() { c.expire(messageID) })
}

// expire 在活性窗到期时检查插话是否仍未被取走。
//
// 仍在队列、回合非等待态、且已静默满一个窗口 → 判死：给手机一条失败 ack
// （code=turn_stalled），再触发 guardian 收尾。两种合法静默都不误报：
//   1) 等待态（工具/子代理/审批）——各自天花板 + stale 扫描器收敛；
//   2) 回合仍在出字——delta 每帧 bump lastActivityAt，静默时长还没满窗口。
// 判据与 stale 扫描器同口径：「非等待态 且 已静默 ≥ 一个完整窗口」才是死亡
// （spec §3.3.4）。少了第 2 条会把一次超过窗口的大模型调用当成死亡，误杀健康回合。
func (c *agentAICoreSteerControl) expire(messageID string) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	// 合法静默：消息必须留在队列里等着被 drain 取走，只把这条窗口作废
	// （不重置，避免静默等待被无限续期）。
	if c.activity != nil && (c.activity.idlePaused() || c.activity.idleFor() < steerLivenessWindow) {
		delete(c.pending, messageID)
		c.mu.Unlock()
		return
	}
	// 判死即消费：在持锁段把它从队列里摘掉，否则解锁后 drain 可能同一瞬间
	// 取走它并发一条 applied ack，造成同一 messageID 先 applied 后 error 双帧。
	stillQueued := false
	kept := c.queue[:0]
	for _, item := range c.queue {
		if item.MessageID == messageID {
			stillQueued = true
			continue
		}
		kept = append(kept, item)
	}
	c.queue = kept
	delete(c.pending, messageID)
	write := c.write
	sessionID := c.sessionID
	runSeq := c.runSeq
	onStalled := c.onStalled
	idle := time.Duration(0)
	if c.activity != nil {
		idle = c.activity.idleFor()
	}
	c.mu.Unlock()

	if !stillQueued {
		return
	}
	// onStalled 的退化分支（拿不到 guard 时直接 cancel）本身不打日志，这条是
	// 线上定位「用户说被判死但翻不到记录」的唯一入口。
	logger.Warn(fmt.Sprintf("[ai.steer.stalled] session=%s run_seq=%d message_id=%s idle=%s guard_mounted=%t",
		sessionID, runSeq, messageID, idle, onStalled != nil))
	emitAgentAISteerAck(write, sessionID, messageID, "error", "回合无响应已判死，请重试", "turn_stalled")
	if onStalled != nil {
		onStalled("steer_stalled")
	}
}

// stopLivenessLocked 停掉若干条插话的活性窗。调用方必须持有 c.mu。
func (c *agentAICoreSteerControl) stopLivenessLocked(ids []string) {
	for _, id := range ids {
		if timer := c.pending[id]; timer != nil {
			timer.Stop()
			delete(c.pending, id)
		}
	}
}
```

`drain` 里 `c.mu.Unlock()` 之前插入：

```go
	ids := make([]string, 0, len(queued))
	for _, item := range queued {
		ids = append(ids, item.MessageID)
	}
	c.stopLivenessLocked(ids)
```

`close` **已存在**（`agent_ai_agentcore.go:1116`，`clearRunning` 调用它）——**不要重写、不要改签名**，只在持锁段末尾插一个停表循环：

改动前（1120-1124）：

```go
	c.mu.Lock()
	queued := c.queue
	c.queue = nil
	c.closed = true
	c.mu.Unlock()
```

改动后：

```go
	c.mu.Lock()
	queued := c.queue
	c.queue = nil
	c.closed = true
	// 关通道时把所有未到期的活性窗一并停掉：否则已 close 的通道上还会有
	// AfterFunc 在一个窗口（默认 5min）后醒来读 c.activity，造成无谓的判定与日志。
	for id, timer := range c.pending {
		timer.Stop()
		delete(c.pending, id)
	}
	c.mu.Unlock()
```

> 只插这 4 行。`close` 里现有的 `queued`/`closed`/ack 循环一律保持原样。
> 这里刻意内联、不复用 `stopLivenessLocked`：本处要清空的是**整张** `c.pending`，
> 就地边遍历边删比先物化 key 切片再回查更直接（`stopLivenessLocked` 用于
> 按 id 定向停表，两处场景不同）。

- [ ] **Step 5: 接线 onStalled**

`runAgentCore` 里，保持在 **`m.attachCoreSteer(...)` 之前**那个位置（Task 7 已经把 `coreSteer.activity` 放在这里，本步只补 `onStalled`）：

```go
	coreSteer.activity = run.activity
	coreSteer.onStalled = func(reason string) {
		if guard := m.runGuardForActivity(run.activity); guard != nil {
			guard.kill(reason)
			return
		}
		if run.cancel != nil {
			run.activity.setKillReason(reason)
			run.cancel()
		}
	}
```

> 两个字段都要在挂载前赋完——`attachCoreSteer` 之后 control 就可被其他 goroutine 取用，late-assign 会留下「已可见但回调为 nil」的窗口，那正是活性窗触发时静默丢判死的地方。

- [ ] **Step 6: 跑测试**

Run: `go test ./app/http/services/ -run 'TestCoreSteer' -v`
Expected: PASS（含 Task 7 的用例）。

- [ ] **Step 7: 提交**

```bash
git add app/http/services/agent_ai_run_guard.go app/http/services/agent_ai_agentcore.go app/http/services/agent_ai_agentcore_steer_test.go
git commit -m "$(cat <<'EOF'
新增：steer 活性窗与 turn_stalled 反馈

插话入队即回 queued，回合死掉后无人消费、手机零反馈。现在排队超过
一个活性窗（默认 5min，与 stale 扫描器对齐）且回合处于非等待态、且仍
无出字即回失败 ack（code=turn_stalled）并触发判死；
工具/子代理/审批等合法等待态不误报。

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: follow-up 同窗口覆盖（验证 + 补测）

**Files:**
- Test: `app/http/services/agent_ai_agentcore_steer_test.go`（追加）
- Modify: `app/http/services/agent_ai_agentcore.go:977-986`（仅当 follow-up 走了别的取数路径才需要改）

- [ ] **Step 1: 确认 follow-up 与 steer 共用队列**

Run: `grep -n "GetFollowUpMessages\|drainAgentCoreSteering" -A 6 app/http/services/agent_ai_agentcore.go`
Expected: 两个 hook 都调用 `m.drainAgentCoreSteering(run)`，即共用 `coreSteer.drain()`。

- [ ] **Step 2: 写覆盖测试**

```go
func TestCoreSteerFollowUpShareLivenessWindow(t *testing.T) {
	// follow-up 与 steer 共用 coreSteer.drain()，因此 Task 9 的窗口天然覆盖
	// follow-up：入队即起窗，drain 即停窗。这里验证「一批 drain 取完、第二次
	// 为空」的语义未被窗口改动破坏（窗口只在 expire 时才动队列）。
	rec := &recordingWriter{}
	activity := newAgentAIActivity()
	run := agentAIRun{sessionID: "sess-1", runSeq: 1, messageID: "msg-1", activity: activity}
	control := newAgentAICoreSteerControl(run, rec.write)
	control.activity = activity
	control.onStalled = func(string) { t.Error("drained steer must not be reported stalled") }

	_, _ = control.deliver(agentAISteerMessage{MessageID: "a", Content: "1"})
	_, _ = control.deliver(agentAISteerMessage{MessageID: "b", Content: "2"})
	if got := control.drain(); len(got) != 2 {
		t.Fatalf("drain = %d, want 2", len(got))
	}
	if got := control.drain(); len(got) != 0 {
		t.Fatalf("second drain = %d, want 0", len(got))
	}
	if queueLenOf(control) != 0 {
		t.Fatalf("queued = %d, want 0 after drain", queueLenOf(control))
	}
	if got := rec.snapshot(); len(got) != 2 {
		t.Fatalf("applied acks = %d, want 2", len(got))
	}
}
```

- [ ] **Step 3: 跑测试**

Run: `go test ./app/http/services/ -run TestCoreSteerFollowUp -v`
Expected: PASS。

- [ ] **Step 4: 提交**

```bash
git add app/http/services/agent_ai_agentcore_steer_test.go
git commit -m "$(cat <<'EOF'
新增：补充 follow-up 与 steer 共用活性窗的覆盖测试

收尾追补的 GetFollowUpMessages 与 steer 同队列，Task 9 的窗口天然覆盖；
补测试钉住「入队起窗、drain 停窗」的语义。

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

## P2 通道健康

### Task 11: 401 定位（端点 × 频率）

**Files:**
- Modify: `app/http/services/agent_service.go:1864-1872`

- [ ] **Step 1: 在 401 分支补结构化日志**

`callAgentServerWithAuthorization` 的非 2xx 分支里，`resp.StatusCode == http.StatusUnauthorized` 时：

```go
		if resp.StatusCode == http.StatusUnauthorized {
			logger.Warn(fmt.Sprintf("[AGENT-CHANNEL] 401 endpoint=%s has_auth=%t method=%s",
				sanitizeAgentEndpoint(endpoint), authHeader != "", method))
			if authHeader != "" {
				return nil, agentUserAuthRejectedError{status: resp.StatusCode, body: string(raw)}
			}
		}
```

- [ ] **Step 2: 真机跑一轮，按端点聚合**

Run（在出问题的机器上）：
```bash
grep "\[AGENT-CHANNEL\] 401" ~/.aliang/agent/agent.log \
  | sed -E 's/.*endpoint=([^ ]+).*/\1/' | sort | uniq -c | sort -rn
```
Expected: 得到「端点 × 次数」清单，确认退避覆盖面（事故已知一路是 `/api/transfers/inbox`，随 hello/registered_ack 周期触发）。

> 这条命令在 Task 12 之后依然可用：日志前缀不变，只是多了 `consecutive=` / `backoff=` 字段，`sed` 仍按 `endpoint=` 抽取。Task 12 会删掉本步这行打点、由 `record401` 统一输出（同前缀），所以聚合口径不会断。

- [ ] **Step 3: 提交**

```bash
git add app/http/services/agent_service.go
git commit -m "$(cat <<'EOF'
新增：REST 通道 401 按端点打点日志

为定位凭据过期风暴的产生方，在公共层按 endpoint 记录 401，便于按
「端点 × 频率」聚合，确认退避覆盖面。

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 12: 401 刷新重试一次

**Files:**
- Create: `app/http/services/agent_channel_health.go`
- Modify: `app/http/services/agent_service.go:1816-1875`
- Test: `app/http/services/agent_channel_health_test.go`

- [ ] **Step 1: 写失败测试**

`app/http/services/agent_channel_health_test.go`：

```go
package services

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCallAgentServerRetriesOnceWithRefreshedHeader(t *testing.T) {
	agentChannelHealthInstance.reset()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Header.Get("Authorization") == "Bearer stale" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer srv.Close()

	oldRefresher := agentChannelRefreshHeader
	agentChannelRefreshHeader = func() (string, error) { return "Bearer fresh", nil }
	t.Cleanup(func() { agentChannelRefreshHeader = oldRefresher })

	svc := &AgentService{client: srv.Client()}
	raw, err := svc.callAgentServerWithAuthorization(http.MethodPost, srv.URL, map[string]interface{}{"a": 1}, "Bearer stale")
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("empty response body")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("upstream calls = %d, want 2 (401 then refreshed retry)", got)
	}
}

func TestCallAgentServerSetsEndpointBackoffAfterRepeated401(t *testing.T) {
	agentChannelHealthInstance.reset()
	oldWriter := agentChannelAlertWriter
	agentChannelAlertWriter = func(map[string]interface{}) error { return nil }
	t.Cleanup(func() {
		agentChannelAlertWriter = oldWriter
		agentChannelHealthInstance.reset()
	})

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	oldRefresher := agentChannelRefreshHeader
	agentChannelRefreshHeader = func() (string, error) { return "", nil }
	t.Cleanup(func() { agentChannelRefreshHeader = oldRefresher })

	svc := &AgentService{client: srv.Client()}
	for i := 0; i < agentChannelAlertThreshold; i++ {
		_, _ = svc.callAgentServerWithAuthorization(http.MethodPost, srv.URL, nil, "Bearer stale")
	}
	if agentChannelHealthInstance.allow(srv.URL, "Bearer stale") {
		t.Fatal("endpoint must be in backoff after repeated 401")
	}

	// 门面的 allow 闸门必须承重：删掉闸门这段就红。
	_, err := svc.callAgentServerWithAuthorization(http.MethodPost, srv.URL, nil, "Bearer stale")
	var backoff agentChannelBackoffError
	if !errors.As(err, &backoff) {
		t.Fatalf("err = %v, want agentChannelBackoffError", err)
	}
	if got := atomic.LoadInt32(&calls); got != int32(agentChannelAlertThreshold) {
		t.Fatalf("upstream calls = %d, want %d (退避期内不得再发请求)", got, agentChannelAlertThreshold)
	}
}

func TestAgentChannelBackoffBypassedAfterCredentialRotation(t *testing.T) {
	agentChannelHealthInstance.reset()
	oldWriter := agentChannelAlertWriter
	agentChannelAlertWriter = func(map[string]interface{}) error { return nil }
	t.Cleanup(func() {
		agentChannelAlertWriter = oldWriter
		agentChannelHealthInstance.reset()
	})

	const endpoint = "https://api.example.com/rotated"
	for i := 0; i < agentChannelAlertThreshold; i++ {
		agentChannelHealthInstance.record401(endpoint, "Bearer stale")
	}
	if agentChannelHealthInstance.allow(endpoint, "Bearer stale") {
		t.Fatal("同凭据仍应被退避挡住")
	}
	if !agentChannelHealthInstance.allow(endpoint, "Bearer fresh") {
		t.Fatal("凭据轮转后应放行")
	}
	if !agentChannelHealthInstance.allow(endpoint, "Bearer stale") {
		t.Fatal("放行后应清掉该端点退避")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run 'TestCallAgentServer' -v`
Expected: FAIL，`undefined: agentChannelHealthInstance`。

- [ ] **Step 3: 写实现**

`app/http/services/agent_channel_health.go`：

```go
package services

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
)

const (
	// agentChannelAlertThreshold 是触发「平台凭据过期」设备级告警的最小连续 401 次数。
	agentChannelAlertThreshold = 3
	// agentChannelAlertWindow 是「持续 401」的告警时间阈值。
	agentChannelAlertWindow = 5 * time.Minute
	// agentChannelAlertInterval 是同类告警的最小间隔（限频）。
	agentChannelAlertInterval = 30 * time.Minute
	// agentChannelBackoffBase / Max 是 endpoint 级 401 退避的指数参数。
	agentChannelBackoffBase = 30 * time.Second
	agentChannelBackoffMax  = 10 * time.Minute
)

// agentChannelRefreshHeader 刷新本地 token 并返回新的 Authorization 头；
// 声明为 var 便于测试替换，生产路径调用的就是这个名字。
var agentChannelRefreshHeader = func() (string, error) {
	return ensureFreshRegisterAuthHeaderForPush()
}

// 边界说明（重要，别把这条链路当成「401 自愈」）：
// ensureFreshRegisterAuthHeaderForPush → auth.EnsureFreshAccessTokenForPush 只在
// token **临近过期** 时才真刷新；token 被服务端提前吊销但仍未过期时，它会把同一个
// Authorization 头原样返回。因此这里的「刷新后重试一次」在这种场景下会再吃一个
// 401，然后由退避 + 告警收敛——**用户可感知的结果是「收到告警」，不是「自动恢复」**。
// 本轮不做强制刷新：processor/auth 没有导出 force-refresh（pushRefreshNow 未导出），
// 强行加会牵扯 owner 判定与 token store 写入，超出止血范围；告警已足以让用户重新登录。

type agentChannelEndpointState struct {
	consecutive int
	nextAt      time.Time
	// authFingerprint 是触发退避时所用 Authorization 头的指纹（不是原文）。
	// 只用于识别「凭据已轮转，旧退避作废」，不足以还原凭据。
	authFingerprint string
}

// agentChannelHealth 汇总平台 REST 通道的健康状态：连续 401 计数、端点退避、
// 告警限频。所有方法都零值安全。
type agentChannelHealth struct {
	mu          sync.Mutex
	consecutive int
	firstAt     time.Time
	lastAlertAt time.Time
	endpoints   map[string]*agentChannelEndpointState
}

var agentChannelHealthInstance = &agentChannelHealth{}

// allow 判断这个 endpoint 当前是否允许发请求（处于退避窗口内则拒绝）。
//
// 若当前凭据与触发退避时的凭据不同，说明用户已重新登录/续期，退避立即
// 作废：否则告警引导用户重新登录后，注册仍会被残余退避挡住最多一个窗口。
func (h *agentChannelHealth) allow(endpoint string, authHeader string) bool {
	if h == nil {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.endpoints[endpoint]
	if state == nil || state.nextAt.IsZero() {
		return true
	}
	if agentChannelAuthFingerprint(authHeader) != state.authFingerprint {
		delete(h.endpoints, endpoint)
		return true
	}
	return !time.Now().Before(state.nextAt)
}

// agentChannelAuthFingerprint 取 Authorization 头的短指纹（8 字节），只做相等比较。
func agentChannelAuthFingerprint(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(header))
	return hex.EncodeToString(sum[:8])
}

// record401 记一次 401：推进端点指数退避，并在「连续 ≥3 次」或「持续 >5min」
// 时发一条限频告警。authHeader 是本次被拒的 Authorization 头。
func (h *agentChannelHealth) record401(endpoint string, authHeader string) {
	if h == nil {
		return
	}
	now := time.Now()
	h.mu.Lock()
	if h.firstAt.IsZero() {
		h.firstAt = now
	}
	h.consecutive++
	if h.endpoints == nil {
		h.endpoints = make(map[string]*agentChannelEndpointState)
	}
	state := h.endpoints[endpoint]
	if state == nil {
		state = &agentChannelEndpointState{}
		h.endpoints[endpoint] = state
	}
	state.consecutive++
	state.authFingerprint = agentChannelAuthFingerprint(authHeader)
	backoff := agentChannelBackoffBase << (state.consecutive - 1)
	if backoff > agentChannelBackoffMax || backoff <= 0 {
		backoff = agentChannelBackoffMax
	}
	state.nextAt = now.Add(backoff)

	shouldAlert := (h.consecutive >= agentChannelAlertThreshold || now.Sub(h.firstAt) > agentChannelAlertWindow) &&
		(h.lastAlertAt.IsZero() || now.Sub(h.lastAlertAt) >= agentChannelAlertInterval)
	if shouldAlert {
		h.lastAlertAt = now
	}
	// 所有要在锁外使用的值都必须在锁内拷成局部变量：state 是指向 map 内
	// 结构体的指针，解锁后再读 state.consecutive 会与并发的 record401 构成
	// 数据竞争（-race 下必报）。
	total := h.consecutive
	firstAt := h.firstAt
	stateConsecutive := state.consecutive
	h.mu.Unlock()

	logger.Warn(fmt.Sprintf("[AGENT-CHANNEL] 401 endpoint=%s consecutive=%d total=%d backoff=%s",
		sanitizeAgentEndpoint(endpoint), stateConsecutive, total, backoff))
	if shouldAlert {
		agentChannelAlert(firstAt, total)
	}
}

// recordSuccess 清掉连续计数与端点退避。
func (h *agentChannelHealth) recordSuccess(endpoint string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.consecutive = 0
	h.firstAt = time.Time{}
	if state := h.endpoints[endpoint]; state != nil {
		delete(h.endpoints, endpoint)
	}
	h.mu.Unlock()
}

func (h *agentChannelHealth) reset() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.consecutive = 0
	h.firstAt = time.Time{}
	h.lastAlertAt = time.Time{}
	h.endpoints = map[string]*agentChannelEndpointState{}
	h.mu.Unlock()
}

// agentChannelAlertWriter 是告警的实际发送通道；声明为 var 便于测试替换。
//
// 这里必须用 init() 赋值，不能在声明处写初始化表达式：Go 的包初始化依赖分析
// 会顺着初始化表达式引用的函数体继续追踪，而发送逻辑引用 GetSharedAgentService
// → NewAgentService → … → agentTransferFetchInbox（包级 func var）→
// callAgentServer → 本文件 record401，形成 initialization cycle 编译错误。
// init() 内的赋值不参与依赖分析，这条边因此在编译期被截断。
var agentChannelAlertWriter func(map[string]interface{}) error

func init() {
	agentChannelAlertWriter = func(payload map[string]interface{}) error {
		svc := GetSharedAgentService()
		if svc == nil {
			return errors.New("no shared agent service")
		}
		writer := svc.currentRemoteWriter()
		if writer == nil {
			return errors.New("agent is not connected")
		}
		return writer(payload)
	}
}

// agentChannelAlert 把「设备在线但平台凭据过期」发成设备级告警帧。
//
// 走当前 WS 连接（writeJSON 的同一个 live writer）：没有连接时只记日志，
// 不重试——设备离线时本来也送不到，等它上线自然会重试到新的 401 计数。
func agentChannelAlert(since time.Time, total int) {
	payload := map[string]interface{}{
		"type":            "agent.channel.alert",
		"code":            "platform_credential_expired",
		"message":         "设备在线但平台凭据过期，文件传输等功能暂不可用",
		"consecutive_401": total,
		"since":           since.UTC().Format(time.RFC3339),
	}
	if agentChannelAlertWriter == nil {
		logger.Warn("[AGENT-CHANNEL] alert skipped: writer not installed")
		return
	}
	if err := agentChannelAlertWriter(payload); err != nil {
		logger.Warn(fmt.Sprintf("[AGENT-CHANNEL] alert send failed: %v", err))
		return
	}
	logger.Warn(fmt.Sprintf("[AGENT-CHANNEL] alert sent consecutive_401=%d since=%s", total, payload["since"]))
}

// agentChannelBackoffError 表示该 endpoint 正处于 401 退避窗口内。
type agentChannelBackoffError struct {
	endpoint string
}

func (e agentChannelBackoffError) Error() string {
	return fmt.Sprintf("agent server endpoint %s is backing off after repeated 401", e.endpoint)
}
```

> **`agentChannelAlertWriter` 为什么必须在 `init()` 里赋值**：`agentTransferFetchInbox`
> / `agentTransferFetchDownloadURL` 等是包级 `func var`，Go 的初始化依赖分析会把
> 它的函数体一路追下去。若在声明处写
> `var agentChannelAlertWriter = func(...) { … GetSharedAgentService() … }`，依赖链
> 会按 `agentTransferFetchInbox → callAgentServer → record401 → agentChannelAlert
> → GetSharedAgentService → NewAgentService → … → runRemoteAgentSession → …
> → agentTransferFetchInbox` 闭环，`go build` 直接报
> `initialization cycle for agentTransferFetchInbox`（实测两条同构，`go vet` 同报）。
> `init()` 内的赋值不参与依赖分析，是标准破环手法，运行时语义完全不变——**别把它
> 「简化」回声明处初始化**。

- [ ] **Step 4: 拆出 doCallAgentServer**

`app/http/services/agent_service.go`，把现有 `callAgentServerWithAuthorization` 的请求构造/发送/非 2xx 处理整体挪进 `doCallAgentServer`（内容完全照搬，只在 401 分支返回 typed error），然后外层改为：

> **同时删掉 Task 11 加的那行 `[AGENT-CHANNEL] 401 endpoint=... has_auth=... method=...` 打点。** 现在 401 统一由 `record401` 打点（带 consecutive/backoff），留着它会让每次 401 打两条日志、且聚合命令口径混乱。Task 11 只是过渡观测手段，本步起由健康状态机的日志取代。

```go
func (s *AgentService) callAgentServerWithAuthorization(method string, endpoint string, payload interface{}, authHeader string) ([]byte, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, errors.New("agent server endpoint is empty")
	}
	if !agentChannelHealthInstance.allow(endpoint, authHeader) {
		return nil, agentChannelBackoffError{endpoint: sanitizeAgentEndpoint(endpoint)}
	}

	raw, err := s.doCallAgentServer(method, endpoint, payload, authHeader)
	if err == nil {
		agentChannelHealthInstance.recordSuccess(endpoint)
		return raw, nil
	}
	var rejected agentUserAuthRejectedError
	if !errors.As(err, &rejected) {
		return nil, err
	}

	// 401：本地续期一次再重试一次（spec §3.4.1）。
	//
	// 刻意不用 RequestUserAgentSyncAfterAuth：那条路自己会走
	// callAgentServer 的注册路径，同步调用会在这里递归。本地刷新 token
	// 不经过 server 调用，安全。
	rejectedAuth := authHeader
	fresh, refreshErr := agentChannelRefreshHeader()
	if refreshErr == nil && strings.TrimSpace(fresh) != "" && strings.TrimSpace(fresh) != strings.TrimSpace(authHeader) {
		raw2, err2 := s.doCallAgentServer(method, endpoint, payload, fresh)
		if err2 == nil {
			agentChannelHealthInstance.recordSuccess(endpoint)
			logger.Info(fmt.Sprintf("[AGENT-CHANNEL] 401 recovered after refresh endpoint=%s", sanitizeAgentEndpoint(endpoint)))
			return raw2, nil
		}
		var rejected2 agentUserAuthRejectedError
		if !errors.As(err2, &rejected2) {
			// 第二次失败不是凭据问题（网络/服务端异常），不能记成 401：
			// 否则会把网络抖动算进凭据退避，还会触发误导性的「凭据过期」告警。
			return nil, err2
		}
		rejectedAuth = fresh
	} else if refreshErr != nil {
		logger.Warn(fmt.Sprintf("[AGENT-CHANNEL] 401 本地续期失败，不重试 endpoint=%s: %v",
			sanitizeAgentEndpoint(endpoint), refreshErr))
	} else {
		logger.Warn(fmt.Sprintf("[AGENT-CHANNEL] 401 续期未轮转凭据（token 未临近过期，或被服务端吊销但未过期），不重试 endpoint=%s",
			sanitizeAgentEndpoint(endpoint)))
	}
	agentChannelHealthInstance.record401(endpoint, rejectedAuth)
	return nil, err
}
```

- [ ] **Step 5: 跑测试**

Run: `go test ./app/http/services/ -run 'TestCallAgentServer|TestAgentChannel' -v`
Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add app/http/services/agent_channel_health.go app/http/services/agent_channel_health_test.go app/http/services/agent_service.go
git commit -m "$(cat <<'EOF'
新增：REST 401 本地续期后重试一次 + 端点指数退避

凭据过期后没有任何续期与退避，401 风暴可连续数小时。现在公共层收到
401 先本地刷新 token 重试一次，仍失败则对该端点按 30s 起指数退避
（上限 10min）；成功即清零。

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 13: 限频告警

**Files:**
- Test: `app/http/services/agent_channel_health_test.go`（追加）
- Modify: `app/http/models/agent_protocol.go`（登记 `agent.channel.alert` + `ai.error.error_code`）
- Modify: `app/http/services/agent_channel_health.go`（Task 12 已实现 `agentChannelAlert` 且发送通道已可注入；本 Task 只需核对，不改生产代码）

- [ ] **Step 1: 写测试**

```go
func TestAgentChannelAlertIsRateLimited(t *testing.T) {
	agentChannelHealthInstance.reset()

	oldWriter := agentChannelAlertWriter
	var alerts int32
	agentChannelAlertWriter = func(map[string]interface{}) error {
		atomic.AddInt32(&alerts, 1)
		return nil
	}
	t.Cleanup(func() {
		agentChannelAlertWriter = oldWriter
		agentChannelHealthInstance.reset()
	})

	for i := 0; i < 20; i++ {
		agentChannelHealthInstance.record401("https://api.example.com/x", "Bearer stale")
	}
	if got := atomic.LoadInt32(&alerts); got != 1 {
		t.Fatalf("alerts = %d, want 1 (30min throttle)", got)
	}
}
```

- [ ] **Step 2: 确认告警发送通道已可注入（本步不写生产代码）**

Task 12 已经把发送通道做成可注入的包级 var，并且**必须**用 `init()` 赋值
（原因见 Task 12 Step 3 的说明：在声明处初始化会触发 `initialization cycle`
编译错误）。本步只需核对 `app/http/services/agent_channel_health.go` 里已存在这三段，
形状如下，**不要改写成声明处初始化**：

```go
// agentChannelAlertWriter 是告警的实际发送通道；声明为 var 便于测试替换。
//
// 这里必须用 init() 赋值，不能在声明处写初始化表达式：Go 的包初始化依赖分析
// 会顺着初始化表达式引用的函数体继续追踪，而发送逻辑引用 GetSharedAgentService
// → NewAgentService → … → agentTransferFetchInbox（包级 func var）→
// callAgentServer → 本文件 record401，形成 initialization cycle 编译错误。
// init() 内的赋值不参与依赖分析，这条边因此在编译期被截断。
var agentChannelAlertWriter func(map[string]interface{}) error

func init() {
	agentChannelAlertWriter = func(payload map[string]interface{}) error {
		svc := GetSharedAgentService()
		if svc == nil {
			return errors.New("no shared agent service")
		}
		writer := svc.currentRemoteWriter()
		if writer == nil {
			return errors.New("agent is not connected")
		}
		return writer(payload)
	}
}
```

`agentChannelAlert` 内部只经 `agentChannelAlertWriter(payload)` 发送，**不得**直连
`svc.currentRemoteWriter()`——否则测试替身收不到告警，本任务的限频用例会断言在
「零告警」上而失败（或更糟：假绿）。`"errors"` 也已由 Task 12 引入。

若这三段缺失（说明 Task 12 被回退过），停下来报告，不要自行重贴整段。

- [ ] **Step 3: 登记协议字段**

`app/http/models/agent_protocol.go` 三处（只动这三处，别的地方不要碰）：

**3a.** 事件常量表里 `AgentEventAIError` 之后加一行：

```go
	AgentEventAIError                 = "ai.error"
	AgentEventChannelAlert            = "agent.channel.alert" // agent→cloud: 设备级通道告警(平台凭据过期等),30min 限频;与 ai.* 无关,手机不得据此判设备离线
```

**3b.** 事件 schema 列表里 `AgentEventAIError` 那一条**之后**加一条：

```go
				{Type: AgentEventChannelAlert, Required: []string{"type", "code", "message", "since"}, Optional: []string{"consecutive_401"}},
```

**3c.** 同一条 `AgentEventAIError` 的 `Optional` 里补 `"error_code"`：

```go
				{Type: AgentEventAIError, Required: []string{"type", "session_id", "error", "run_id", "event_seq"}, Optional: []string{"message_id", "error_status", "error_type", "error_code", "retry_attempt", "retry_max", "detail"}},
```

> `agentAIErrorPayload` 在错误实现 `ErrorCode()` 时早就写了 `error_code`，判死帧也手工塞了
> `error_code=turn_stalled`——契约表漏登记只会让 Task 15 的文档与代码对不上，不会有运行时后果。

- [ ] **Step 4: 跑测试**

Run: `go test ./app/http/services/ -run 'TestAgentChannelAlert' -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_channel_health.go app/http/services/agent_channel_health_test.go app/http/models/agent_protocol.go
git commit -m "$(cat <<'EOF'
新增：告警限频测试与 agent.channel.alert 协议登记

补 TestAgentChannelAlertIsRateLimited（钉住阈值 3 与 30min 限频、校验
payload 字段）与 TestAgentChannelAlertRearmsAfterInterval（窗口过后能再发）；
发送侧改用 models.AgentEventChannelAlert 常量，避免与协议表脱钩；把
ai.error 早已在发的 error_code 一并补齐进协议表。

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

> 执行记录：限频逻辑本身在前一任务（Task 12）已落地，本任务实际产出是测试与协议登记，故标题按实际内容改写；另新增前置小提交「修复：空凭据调用不得清掉端点退避」（`allow()` 中指纹为空时不做轮转判定）。

---

### Task 14: P2 全量回归

- [ ] **Step 1: 跑测试**

Run: `go test ./app/http/services/ ./app/agentcore/ -count=1 -timeout 900s`
Expected: 除基线既有失败外全绿。

- [ ] **Step 2: 编译**

Run: `go build ./app/...`
Expected: 无输出。

---

## P3 跨仓对齐

### Task 15: 契约文档 + 真机验证清单

**Files:**
- Create: `docs/superpowers/reviews/2026-10-27-ai-run-resilience-contract.md`
- Modify: `app/http/models/agent_protocol.go`（`ai.error` 的 Optional 补 `status`）

- [ ] **Step 0: 先把协议表与契约文档对齐**

判死终态帧实际带 `status:"stalled"`（`agent_ai_run_guard.go:225`）但表里从未登记，而下面的契约文档会写到它。先给 `AgentEventAIError` 的 Optional 追加 `"status"` 并单独提交「新增：ai.error 补登 status 字段」，再写文档，避免文档与机器可读表不一致。

- [ ] **Step 1: 写契约文档**

```markdown
# AI run 韧性 — 跨仓契约（客户端 → 服务端/手机）

## 新增/变更的帧

### 1. 判死终态
```json
{"type":"ai.error","session_id":"...","message_id":"...assistant","error":"回合已停止响应（stalled），已判死，请重试","error_code":"turn_stalled","status":"stalled"}
```
- 语义：一个 run 的**唯一终态**；服务端/手机必须把会话切到 Failed 并允许 Retry。
- Retry = 用户重发消息 = 新 runSeq `--resume` 原生会话，上下文不丢（agent 侧已支持）。
- 服务端不得自动重试。

### 2. steer 失败反馈
```json
{"type":"ai.steer.ack","session_id":"...","message_id":"...","result":"error","error":"回合无响应已判死，请重试","code":"turn_stalled"}
```
- 手机必须把该条插话标为失败（可重发），不得静默忽略。

### 3. 平台通道告警
```json
{"type":"agent.channel.alert","code":"platform_credential_expired","message":"设备在线但平台凭据过期，文件传输等功能暂不可用","consecutive_401":3,"since":"2026-10-27T03:00:32Z"}
```
- 限频 30min 一条。手机据此提示用户重新登录，不得据此把设备标离线。

## 服务端 sweeper 对齐
- agent 现在保证任何判死路径都发终态；服务端 sweeper 只需以终态为准，不再需要「超时无帧即判死」的兜底猜测（保留也无害）。
- `ai.run.progress` 心跳**不**代表回合活着（内部已被排除出活性口径），服务端不得以心跳存在与否作为判死依据。

## 真机验证清单
1. 正常回合：对话完整，无重复帧。
2. 人为断上游（防火墙丢包）90s 内手机收到错误终态；Retry 后上下文连续。
3. 长工具调用 7min：不被判死，正常完成。
4. 审批挂起 3h：不被硬顶误杀；24h 后判死。
5. 持续 401 场景：收到 `agent.channel.alert`（30min 内最多一条）。
6. 回合中插话且回合已死：收到 `turn_stalled` ack。
```

- [ ] **Step 2: 提交**

```bash
git add docs/superpowers/reviews/2026-10-27-ai-run-resilience-contract.md
git commit -m "$(cat <<'EOF'
新增：AI run 韧性跨仓契约与真机验证清单

钉住三条新帧（判死终态、steer 失败、通道告警）的语义与限频，以及
服务端 sweeper 不得以进度心跳判活的约束。

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

## 完成标准

1. `go build ./app/...` 无输出。
2. `go test ./app/agentcore/ ./app/http/services/ -count=1` 除基线既有失败外全绿。
3. 设计 §5 的 12 条验收项逐条落到用例上并通过（新增用例 26 个，分布在 Task 1/2/3/4/5/6/7/8/9/10/12/13；Task 11 是观测步骤，无单测）。其中 §5 #11「等审批时插话不许误判」由 `TestCoreSteerLivenessWindowSparesApprovalWait` 覆盖；#12「长跑子代理不许被硬顶掐掉」由 `TestCoreSteerLivenessWindowSparesSubagentWait` 与 `TestAgentAIWatchdogHardCeilingExcludesWaitingStates` 两侧覆盖，而它的另一半「纯引擎活跃 2h 必杀」沿用既有用例 `TestAgentAIWatchdogLoopHardCeilingKills`（本计划只改了默认值 48h→2h 与注释，未改该用例本身，跑 P0 全量回归时它必须绿）。Task 4 另加 `TestClearResetsFinalizedRunSeq` 覆盖「强拆标记不得跨 `/clear` 误伤新回合」；Task 9 另加 `TestCoreSteerLivenessWindowSparesActiveRun` 钉住「仍在出字的长回合不得被判死」（判死口径 = 非等待态 **且** 已静默满一个窗口，与 stale 扫描器同口径）。
4. 日志里能看到的轨迹：`[ai.run.kill]`、`[ai.run.force_finalize]`、`[ai.run.zombie_discard]`、`[AGENT-CHANNEL]`。
5. 分支合并前按「P0 → P1 → P2 → P3」顺序，每个 Task 单独 commit。

---

# Task 16（终审响应，计划外追加）

**来源**：15 个任务全部完成后，跨任务终审（审 `git diff cbcd242..HEAD`，31 提交 / 20 文件 / +3443−93）给出 **阻断 0、应修 3、记录 3**。本节记录这三条的处置。

**为什么计划外**：这 3 条都不是单任务视角能看到的——它们是「两个子系统接缝处」的缺陷（L4 合成的错误类型 × 既有注册状态机；L2 的强制收尾 × emitter 的锁范围；初始化接缝 × Go 初始化语义）。计划里没有对应的验收项。

## 应修-1：合成的 backoff 错误被注册链当成「服务端明确拒绝」

- 事实：`agentChannelBackoffError.Error()` = `"agent server endpoint %s is backing off after repeated 401"`，不含 `isRetryableAgentAuthSyncError` 的任何 marker → `classifyRegisterFailure` 返 false → `agent_service.go:1686` 执行 `Registered=false`。那段代码正上方记着 2026-10-08 那次 16 小时静默离线的成因就是网络抖动翻 `Registered=false`。另 `syncNow` 把它压成 `server_unavailable`（真相是凭据过期），`recoverOrExpireAfterRegisterAuthRejection` 因不是 typed 401 而不触发。
- 处置：新增 `isAgentChannelBackoffError`；`classifyRegisterFailure` 在 typed 401 判定之后早返回 `true`（本地退避 = 等价瞬时链路故障）；`syncNow` 遇退避**不覆盖** `LastSyncStatus`；审计全部 `classifyRegisterFailure` 调用点与按错误类型分支的状态落点。**不**触发自愈刷新（进入退避前已 refresh-once）。
- 测试：`classifyRegisterFailure(backoff)` == true；typed 401 仍 false；退避窗内 register/sync 后 `Registered` 仍 true 且 `LastSyncStatus` 未被改写。

## 应修-2：`forceFinalize` 的终态投递在它描述的场景里不可观测 + goroutine 泄漏

- 事实：注释承诺「会留下 `terminal_not_delivered` 日志」，但 `emit` 是 `defer e.mu.Unlock()` 且锁覆盖 `e.write`；僵尸卡写侧 = `e.mu` 已被持有 → `g.emit` 阻塞在 `Lock()` 不返回 → `err != nil` 分支永不执行。那条裸 goroutine 无 timeout/无 select，永久泄漏。
- 处置：抽出 `emitTerminalBounded(emit, timeout)`（`done chan error` + `select` + 哨兵 `errAgentAITerminalBlocked`），`forceFinalize` 超时打 `[ai.run.force_finalize] terminal_blocked`（Error 级）；保留 `terminal_not_delivered`（emit 返回错误）——两条是不同失败。超时复用 kill grace 30s，**不新增 env**。改写 `:207-209` 注释为三条路的真话。
- 测试：阻塞 emit + 50ms 超时 → 迅速返回哨兵；emit 报错原样返回；nil 返回 nil；`forceFinalize` 自身立即返回。

**Task 16 执行结果（2026-10-28 收口）**：4 个提交——`d4ca5e6`（应修-1 + e2e 回归 + enable 路径同类扩展）、`2819d1b`（应修-2 + 应修-4 L1 端到端回归 + `currentStreamIdleWindow` 可测化改造）、`8f38013`（应修-3：**主会话独立复现实验推翻终审判断——initialization cycle 是真的**，编译器 exit=1，环经 agentTransferFetchInbox ↔ agentChannelAlertWriter 两个包级 var 闭合，保留 init() 并把注释换成编译器实证）、`b0bde31`（复核 3 条记录的注释对齐）。复核结论：无阻断无应修，四处披露全部接受。

## 应修-3：`agentChannelAlertWriter` 的 `init()` 接缝注释在说谎

- 事实：注释声称 `var x = func(){...}` 会因函数体被初始化依赖分析追踪而成环。按 Go 规范，func literal 的函数体不在初始化期执行，依赖分析不穿过闭包体——**大概率无环**。若真无环，`init()` 是无谓间接，并让 `agentChannelAlert` 的 nil 分支成为生产不可达死代码。
- 处置：**实验定谳**。改成包级 var 直接赋值后 `go build`；绿则删 `init()` 与推测性注释（nil 分支按 grep 结果保留为测试替身或删除）；真的报环则保留 `init()` 并把注释换成编译器原文 + 精确定位成环的那条边。结论写进提交正文。

## 应修-4（终审建议）：L1 端到端回归用例

- 现状：只有 L1 单元、终态闸单元、`clearRunning` 单元三块，没有焊起来的整合用例。
- 处置：httptest 假上游写一帧后**保持连接不关**（FIN 会走 EOF 正常收尾，测不到 L1）→ 短空闲超时 → 断言 emitter **恰好一条** `ai.error` 且 `error` 非空。

## 记录（不修，留档）

1. **L1 计时从 reader 构造起算**，涵盖 TTFT 而不仅是分块间隔。长思考模型冷启动可能被误判为停滞。真机若遇误杀，先调 `ALIANG_AGENTCORE_STREAM_IDLE_TIMEOUT`，别改逻辑。
2. **干净 EOF 但无 `message_stop` 仍当成功收尾**：`parseSSE` 只在 `message_stop` 提前 return，EOF 也是正常终止 → 上游中途关流、半截文本被当完整结果。改动前就存在，本次未扩大也未收窄。真机 smoke「人工掐上游」必须掐成**静默**才测到 L1；掐成 FIN 走的是另一条路。
3. **TUI 回合在 `attachRunGuard` 之前返回**，不在 L3 扫描覆盖内，只有引擎内看门狗兜底。与手机端目标无关。

## 终审对残余风险的排序（真机优先级）

1. **楔死写侧**（唯一能让手机「一直转」的路径）= 原始 bug 观感的唯一复现口。构造「手机断网/后台挂起但 TCP 未拆」，确认服务端收敛 + 重连补投 + `terminal_blocked` 可 grep。
2. **`ai.error` vs idle 状态帧落序**：弱网反复触发 L1，观察是否出现「回合已停但无错误文案」的空回合。若稳定复现，考虑让 `ai.status=idle` 帧携带终态摘要，而不是让两个写路径各自为政。
3. **TTFT 误杀**：用最慢的思考型模型跑冷启动，实测首 token 时间是否逼近 90s。
4. **401 风暴后的状态文案**：连造 3 次 401，确认手机显示「凭据过期」而非「服务器不可达」，且重新登录后 `allow()` 指纹立刻解封。
5. **掐上游 vs 掐 FIN**：见记录 2。
