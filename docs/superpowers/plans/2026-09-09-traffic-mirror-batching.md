# 流量镜像批量聚合升级 实施计划（Traffic Mirror Batching）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 traffic mirror 转发管道从"每条消息一个 HTTP POST"升级为批量聚合 POST（新端点 `/mirror/batch`），消除高流量下的海量小请求瓶颈。

**Architecture:** 客户端（Go）forwarder 单 worker 攒批——满 200 条 / 累计 512KB（按逐条 marshal 后字节数）/ 距上次 flush 200ms 三条件任一触发整批发送，批量端点由配置 target 推导（尾部去 `/`、幂等追加 `/batch`）；服务端（Node/Express）新增 `/mirror/batch` 逐条复用现有 `handleMirrorMessage`，单条失败不影响整批。fire-and-forget 语义不变。

**Tech Stack:** Go 1.x（标准库 + httptest）；Node.js/Express + ws；测试：go test、test-ingest.js（手跑集成脚本）。

**Spec:** `docs/superpowers/specs/2026-09-09-traffic-mirror-batching-design.md`（已批准）

**仓库说明（两个仓库，都要动）：**
- 客户端：`/Users/mac/MyProgram/GoProgram/nursor/alianggate`（Go，按用户工作流用 git worktree 开发）
- 服务端：`/Users/mac/MyProgram/AiProgram/MonodirCursorTraffic`（Node，main 分支干净，直接切分支）

**提交规范（客户端仓库）：** 全中文标题「新增：/修复：」风格，先 `git log` 看近期风格；每步 commit 是计划的一部分，执行前已获用户同意。服务端仓库 commit 前同样先看 `git log` 风格。

---

### Task 0: 准备工作区

**Files:** 无代码改动，仅分支/工作区准备。

- [ ] **Step 1: 客户端仓库建 worktree**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
git stash -u   # 若有未提交改动；干净则跳过
git worktree add .claude/worktrees/feat-mirror-batch -b feat/mirror-batch
# 若执行了 stash：
# git -C .claude/worktrees/feat-mirror-batch stash pop 不可用（stash 属于原仓），
# 正确做法：回原仓 git stash pop，然后在 worktree 内重新应用所需文件；
# 工作区干净时无需任何 stash 操作。
```

后续客户端 Task 全部在 `/Users/mac/MyProgram/GoProgram/nursor/alianggate/.claude/worktrees/feat-mirror-batch` 下执行。

- [ ] **Step 2: 服务端仓库切分支**

```bash
cd /Users/mac/MyProgram/AiProgram/MonodirCursorTraffic
git checkout -b feat/mirror-batch-endpoint
```

- [ ] **Step 3: 确认两仓库测试基线可用**

```bash
# 客户端（worktree 内）
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate/.claude/worktrees/feat-mirror-batch
go build ./... && go test ./processor/mirror/... ./processor/tcp/... 2>&1 | tail -5
# 预期：build 成功；mirror 包无测试文件（会显示 no test files），tcp 包测试 PASS
```

---

### Task 1: 服务端 `/mirror/batch` 端点（TDD）

**Files:**
- Modify: `/Users/mac/MyProgram/AiProgram/MonodirCursorTraffic/src/server/web-server.js`（`ingestStats` 初始化 ~L43-51；`_setupIngestRoutes()` ~L82 之后）
- Modify: `/Users/mac/MyProgram/AiProgram/MonodirCursorTraffic/config.js`（`ingest` 段）
- Test: `/Users/mac/MyProgram/AiProgram/MonodirCursorTraffic/test-ingest.js`

- [ ] **Step 1: 在 test-ingest.js 末尾添加批量端点测试函数（先写失败测试）**

在文件的测试调度区（参照现有 `testUnifiedBasic` 等函数的注册方式，文件底部主流程里追加调用）：

```javascript
async function testBatchEndpoint() {
  console.log('\n=== Test N: POST /mirror/batch (mixed batch, invalid msg, 400) ===\n');

  // 1) 混合批次：flow_start + 2 chunks + flow_end
  const flowId = 'batch-test-' + Date.now();
  const batch = {
    messages: [
      { event_type: 'flow_start', flow_id: flowId, client_addr: '127.0.0.1:1', server_addr: '1.2.3.4:443', host_name: 'batch.test' },
      { flow_id: flowId, direction: 'request', offset: 0, payload: toBase64('GET / HTTP/1.1\r\nHost: batch.test\r\n\r\n'), timestamp: Date.now() },
      { flow_id: flowId, direction: 'response', offset: 0, payload: toBase64('HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nhi'), timestamp: Date.now() },
      { event_type: 'flow_end', flow_id: flowId, client_to_server_bytes: 34, server_to_client_bytes: 40, duration_ms: 10 },
    ],
  };
  const r1 = await post('/mirror/batch', batch);
  console.log('  mixed batch:', JSON.stringify(r1.body));
  if (r1.status !== 200 || !r1.body.ok) throw new Error('batch should be accepted');
  if (r1.body.accepted !== 4) throw new Error('expected accepted=4, got ' + r1.body.accepted);

  // 2) 批内含非法消息：accepted/failed 计数正确，不影响其他消息
  const r2 = await post('/mirror/batch', { messages: [
    { event_type: 'flow_start', flow_id: 'batch-bad-1', client_addr: '127.0.0.1:2' },
    { event_type: 'flow_start' }, // 缺 flow_id → 应 failed
  ]});
  console.log('  batch with invalid msg:', JSON.stringify(r2.body));
  if (r2.status !== 200 || !r2.body.ok) throw new Error('batch with invalid msg should still return ok=true');
  if (r2.body.accepted !== 1 || r2.body.failed !== 1) throw new Error('expected accepted=1 failed=1, got ' + JSON.stringify(r2.body));

  // 3) messages 非数组 → 400
  const r3 = await post('/mirror/batch', { messages: 'not-an-array' });
  console.log('  non-array messages:', r3.status);
  if (r3.status !== 400) throw new Error('non-array messages should 400');

  console.log('  ✓ batch endpoint tests passed');
}
```

注意：① 若 `handleMirrorMessage` 对缺 flow_id 的处理是 `ok:false` 返回而非抛错，failed 计数应基于 `result.ok === false`（实现要同时覆盖两种路径）；② `toBase64` 已在文件内存在；③ 请求体 `Content-Length` 由 `post()` 处理；④ 文件内已有 `assert(cond, msg)` 辅助函数，新用例优先用 `assert(...)` 而非裸 `throw`，与既有风格一致。

- [ ] **Step 2: 启动服务端，运行测试，确认新用例失败**

```bash
cd /Users/mac/MyProgram/AiProgram/MonodirCursorTraffic
node src/index.js &          # 后台启动；若 3000 端口被占用先停掉旧实例
sleep 1
node test-ingest.js 2>&1 | tail -20
# 预期：新用例报错（404 / batch should be accepted）；既有用例全部通过
kill %1
```

- [ ] **Step 3: 实现端点**

`config.js` 的 `ingest` 段新增：

```javascript
    // Maximum number of messages accepted in one /mirror/batch request
    maxBatchSize: 500,
```

`web-server.js` 两处修改：

(a) `ingestStats` 初始化加一行：

```javascript
    this.ingestStats = {
      totalMessages: 0,
      totalChunks: 0,
      totalFlowStarts: 0,
      totalFlowEnds: 0,
      totalBatches: 0,
      totalErrors: 0,
      lastMessageTime: null,
    };
```

(b) `_setupIngestRoutes()` 内、`/mirror` 路由之后追加：

```javascript
    /**
     * POST /mirror/batch
     *
     * Batched ingest from the Go agent's traffic mirror forwarder.
     * Body: { "messages": [ <same shapes as POST /mirror>, ... ] }
     * Each message is processed independently; one bad message never
     * fails the whole batch.
     */
    this.app.post('/mirror/batch', (req, res) => {
      const body = req.body;
      const messages = body && body.messages;

      if (!Array.isArray(messages)) {
        return res.status(400).json({ ok: false, error: 'messages must be an array' });
      }

      const maxBatchSize = config.ingest?.maxBatchSize || 500;
      if (messages.length > maxBatchSize) {
        return res.status(400).json({
          ok: false,
          error: `batch too large: ${messages.length} > ${maxBatchSize}`,
        });
      }

      let accepted = 0;
      let failed = 0;
      for (const msg of messages) {
        try {
          if (!msg || typeof msg !== 'object') throw new Error('message must be an object');
          const result = this.flowManager.handleMirrorMessage(msg);
          this.ingestStats.totalMessages++;
          if (result && result.ok === false) {
            failed++;
            this.ingestStats.totalErrors++;
            continue;
          }
          accepted++;
          if (result.action === 'flow_start') this.ingestStats.totalFlowStarts++;
          else if (result.action === 'flow_end') this.ingestStats.totalFlowEnds++;
          else this.ingestStats.totalChunks++;
        } catch (_err) {
          failed++;
          this.ingestStats.totalErrors++;
        }
      }

      this.ingestStats.totalBatches++;
      this.ingestStats.lastMessageTime = Date.now();
      res.json({ ok: true, accepted, failed });
    });
```

- [ ] **Step 4: 重启服务端，运行全部测试，确认通过**

```bash
cd /Users/mac/MyProgram/AiProgram/MonodirCursorTraffic
kill %1 2>/dev/null; node src/index.js &
sleep 1
node test-ingest.js 2>&1 | tail -30
# 预期：所有用例（含新 batch 用例）✓ 通过
kill %1
```

- [ ] **Step 5: 提交（服务端仓库）**

```bash
cd /Users/mac/MyProgram/AiProgram/MonodirCursorTraffic
git log --oneline -5   # 确认该仓库提交风格
git add config.js src/server/web-server.js test-ingest.js
git commit -m "新增 /mirror/batch 批量 ingest 端点（逐条处理、单条失败不影响整批）"
```

---

### Task 2: 客户端 batchTarget 推导函数（TDD）

**Files:**
- Create: `.claude/worktrees/feat-mirror-batch/processor/mirror/forwarder_test.go`
- Modify: `.claude/worktrees/feat-mirror-batch/processor/mirror/forwarder.go`

- [ ] **Step 1: 写失败测试**

创建 `processor/mirror/forwarder_test.go`：

```go
package mirror

import "testing"

func TestBatchTarget(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain mirror path", "http://h:3000/mirror", "http://h:3000/mirror/batch"},
		{"trailing slash", "http://h:3000/mirror/", "http://h:3000/mirror/batch"},
		{"already batched", "http://h:3000/mirror/batch", "http://h:3000/mirror/batch"},
		{"no path", "http://h:3000", "http://h:3000/batch"},
	}
	for _, tc := range cases {
		if got := batchTarget(tc.in); got != tc.want {
			t.Errorf("%s: batchTarget(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: 运行确认编译失败**

```bash
go test ./processor/mirror/ -run TestBatchTarget -v
# 预期：编译错误 undefined: batchTarget
```

- [ ] **Step 3: 实现 batchTarget 并接入 InitGlobalForwarder**

`forwarder.go` 增加 import `"strings"`，新增函数：

```go
// batchTarget derives the batch ingest endpoint from the configured target.
// Trailing "/" is trimmed first; "/batch" is appended unless already present.
func batchTarget(target string) string {
	t := strings.TrimRight(strings.TrimSpace(target), "/")
	if strings.HasSuffix(t, "/batch") {
		return t
	}
	return t + "/batch"
}
```

`InitGlobalForwarder` 中 `target: cfg.Target` 改为 `target: batchTarget(cfg.Target)`。

- [ ] **Step 4: 运行确认通过**

```bash
go test ./processor/mirror/ -run TestBatchTarget -v
# 预期：PASS（4 个子用例）
```

- [ ] **Step 5: 提交**

```bash
git add processor/mirror/forwarder.go processor/mirror/forwarder_test.go
git commit -m "新增：流量镜像批量端点推导 batchTarget（尾部斜杠防御 + /batch 幂等追加）"
```

---

### Task 3: 客户端攒批 worker（TDD）

**Files:**
- Modify: `.claude/worktrees/feat-mirror-batch/processor/mirror/forwarder.go`（重写 `run`/`send`、`Enqueue` 加计数、新增批量常量与 envelope）
- Test: `.claude/worktrees/feat-mirror-batch/processor/mirror/forwarder_test.go`

- [ ] **Step 1: 写失败测试**

追加到 `forwarder_test.go`（`waitFor` 轮询辅助避免固定 sleep 的脆弱性）：

```go
package mirror

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type capturedBatch struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (c *capturedBatch) append(b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]byte, len(b))
	copy(cp, b)
	c.bodies = append(c.bodies, cp)
}

func (c *capturedBatch) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.bodies)
}

func (c *capturedBatch) decodedMessages(t *testing.T) []map[string]interface{} {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]interface{}
	for _, b := range c.bodies {
		var env struct {
			Messages []map[string]interface{} `json:"messages"`
		}
		if err := json.Unmarshal(b, &env); err != nil {
			t.Fatalf("decode batch body: %v (body=%s)", err, b)
		}
		out = append(out, env.Messages...)
	}
	return out
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func newTestForwarder(t *testing.T, cap batches int, h http.HandlerFunc) (*Forwarder, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	prev := GetGlobalForwarder()
	t.Cleanup(func() { globalMu.Lock(); globalForwarder = prev; globalMu.Unlock() })
	f := &Forwarder{
		target: batchTarget(ts.URL),
		client: &http.Client{Timeout: 2 * time.Second},
		ch:     make(chan MirrorMessage, cap),
		done:   make(chan struct{}),
	}
	go f.run()
	return f, ts
}

func chunkAt(seq uint64) *StreamChunk {
	return &StreamChunk{FlowID: "f1", Direction: DirectionRequest, Offset: seq, Seq: seq, Payload: []byte("x")}
}

func TestForwarderFlushesWhenBatchFull(t *testing.T) {
	store := new(capturedBatch)
	f, _ := newTestForwarder(t, 1024, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		store.append(b)
		w.WriteHeader(http.StatusOK)
	})
	for i := 0; i < batchMaxMessages; i++ {
		f.Enqueue(chunkAt(uint64(i)))
	}
	waitFor(t, 2*time.Second, func() bool { return store.count() > 0 })
	msgs := store.decodedMessages(t)
	if len(msgs) != batchMaxMessages {
		t.Fatalf("expected %d messages, got %d", batchMaxMessages, len(msgs))
	}
	time.Sleep(100 * time.Millisecond) // 确认没有多余批次
	if got := store.count(); got != 1 {
		t.Fatalf("expected exactly 1 batch POST, got %d", got)
	}
	f.Stop()
}

func TestForwarderFlushesOnTimeout(t *testing.T) {
	store := new(capturedBatch)
	f, _ := newTestForwarder(t, 1024, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		store.append(b)
		w.WriteHeader(http.StatusOK)
	})
	f.Enqueue(chunkAt(1))
	f.Enqueue(chunkAt(2))
	waitFor(t, 2*time.Second, func() bool { return store.count() > 0 })
	msgs := store.decodedMessages(t)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages after flush interval, got %d", len(msgs))
	}
	f.Stop()
}

func TestForwarderStopDrainsPending(t *testing.T) {
	store := new(capturedBatch)
	f, _ := newTestForwarder(t, 1024, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		store.append(b)
		w.WriteHeader(http.StatusOK)
	})
	for i := 0; i < 5; i++ {
		f.Enqueue(chunkAt(uint64(i)))
	}
	f.Stop() // 立即 Stop，尾批必须被排空发出
	waitFor(t, 2*time.Second, func() bool { return store.count() > 0 })
	msgs := store.decodedMessages(t)
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages drained after Stop, got %d", len(msgs))
	}
}

func TestForwarderPreservesOrder(t *testing.T) {
	store := new(capturedBatch)
	f, _ := newTestForwarder(t, 1024, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		store.append(b)
		w.WriteHeader(http.StatusOK)
	})
	for i := 0; i < batchMaxMessages; i++ {
		f.Enqueue(chunkAt(uint64(i)))
	}
	waitFor(t, 2*time.Second, func() bool { return store.count() > 0 })
	msgs := store.decodedMessages(t)
	for i, m := range msgs {
		if int(m["seq"].(float64)) != i {
			t.Fatalf("order broken at %d: got seq %v", i, m["seq"])
		}
	}
	f.Stop()
}

func TestForwarderCountsDroppedWhenBlocked(t *testing.T) {
	store := new(capturedBatch)
	gate := make(chan struct{})
	f, _ := newTestForwarder(t, 16, func(w http.ResponseWriter, r *http.Request) {
		<-gate // 首次 flush 时卡住 worker
		b, _ := io.ReadAll(r.Body)
		store.append(b)
		w.WriteHeader(http.StatusOK)
	})
	// Enqueue 非阻塞：突发 100 条远超容量 16，入队瞬间即产生丢弃
	//（worker 要等 200ms 定时器才首次 flush，入队期间必然有消息落不进 channel）
	for i := 0; i < 100; i++ {
		f.Enqueue(chunkAt(uint64(i)))
	}
	if f.Dropped() == 0 {
		t.Fatal("expected dropped counter > 0 when queue overflows")
	}
	close(gate)
	f.Stop()
}

func TestForwarderServerErrorDoesNotBlock(t *testing.T) {
	store := new(capturedBatch)
	f, _ := newTestForwarder(t, 1024, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		store.append(b)
		w.WriteHeader(http.StatusInternalServerError)
	})
	for i := 0; i < 10; i++ {
		f.Enqueue(chunkAt(uint64(i)))
	}
	f.Stop() // Stop 必须正常返回，不被 500 卡死
}
```

- [ ] **Step 2: 运行确认失败**

```bash
go test ./processor/mirror/ -v 2>&1 | tail -20
# 预期：编译错误 undefined: batchMaxMessages / f.Dropped / (可能 f.run 未导出签名不符)
```

- [ ] **Step 3: 重写 forwarder.go 实现攒批**

完整替换后的关键结构（保持既有 `InitGlobalForwarder`/`GetGlobalForwarder`/`Stop` 不变）：

```go
const (
	forwarderChannelSize     = 1024
	forwarderHTTPTimeout     = 10 * time.Second
	forwarderMaxIdleConns    = 4
	forwarderIdleConnTimeout = 30 * time.Second

	batchMaxMessages   = 200
	batchMaxBytes      = 512 * 1024
	batchFlushInterval = 200 * time.Millisecond
)

// Forwarder asynchronously sends MirrorMessages to a target HTTP endpoint.
type Forwarder struct {
	target  string // derived batch endpoint (batchTarget(cfg.Target))
	client  *http.Client
	ch      chan MirrorMessage
	done    chan struct{}
	once    sync.Once
	dropped atomic.Uint64
}

// Dropped returns the number of messages discarded because the queue was full.
func (f *Forwarder) Dropped() uint64 { return f.dropped.Load() }

// Enqueue adds a message to the forwarding queue. Non-blocking: drops if full.
func (f *Forwarder) Enqueue(msg MirrorMessage) {
	if f == nil || msg == nil {
		return
	}
	select {
	case f.ch <- msg:
	default:
		f.dropped.Add(1)
		logger.Debug("[Mirror] Forwarder queue full, dropping message")
	}
}

// run batches messages from the channel and POSTs them to the batch endpoint.
// Order is preserved: single worker, pending slice in arrival order.
func (f *Forwarder) run() {
	defer close(f.done)

	pending := make([]json.RawMessage, 0, batchMaxMessages)
	pendingBytes := 0
	timer := time.NewTimer(batchFlushInterval)
	defer timer.Stop()

	flush := func() {
		if len(pending) == 0 {
			return
		}
		body, err := json.Marshal(batchEnvelope{Messages: pending})
		if err != nil {
			logger.Debug("[Mirror] Failed to marshal batch: " + err.Error())
		} else {
			f.post(body)
		}
		pending = pending[:0]
		pendingBytes = 0
		timer.Reset(batchFlushInterval)
	}

	for {
		select {
		case msg, ok := <-f.ch:
			if !ok {
				flush() // channel closed by Stop(): drain the tail batch
				return
			}
			data, err := json.Marshal(msg)
			if err != nil {
				logger.Debug("[Mirror] Failed to marshal message: " + err.Error())
				continue
			}
			pending = append(pending, json.RawMessage(data))
			pendingBytes += len(data)
			if len(pending) >= batchMaxMessages || pendingBytes >= batchMaxBytes {
				flush()
			}
		case <-timer.C:
			flush()
		}
	}
}

// batchEnvelope is the wire format of POST /mirror/batch.
type batchEnvelope struct {
	Messages []json.RawMessage `json:"messages"`
}

// post sends one batch body. Fire-and-forget: failures are logged and dropped.
func (f *Forwarder) post(body []byte) {
	resp, err := f.client.Post(f.target, "application/json", bytes.NewReader(body))
	if err != nil {
		logger.Debug("[Mirror] Failed to send batch: " + err.Error())
		return
	}
	// Drain a bounded amount of the body so keep-alive connections are reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
}
```

import 变更：新增 `"encoding/json"`、`"io"`、`"sync/atomic"`、`"strings"`；删除不再使用的 `"net/http"` 之外多余项自查（`"bytes"` 保留）。删除旧的 `run`/`send`。

注意 `pending = pending[:0]` 复用底层数组：`json.Marshal(batchEnvelope{...})` 在读取 `[]json.RawMessage` 内容后才返回，`flush` 里 marshal 完成后才截断，无数据竞争。

- [ ] **Step 4: 运行全部 mirror 测试确认通过**

```bash
go test ./processor/mirror/ -v 2>&1 | tail -20
# 预期：TestBatchTarget + 6 个新测试全部 PASS
go test -race ./processor/mirror/ 2>&1 | tail -3
# 预期：PASS（无数据竞争）
```

- [ ] **Step 5: 提交**

```bash
git add processor/mirror/forwarder.go processor/mirror/forwarder_test.go
git commit -m "新增：流量镜像转发器攒批发送（200条/512KB/200ms 三条件触发 + drain 恢复 keep-alive + 丢弃计数）"
```

---

### Task 4: e2e 测试改造与 keep-alive 验证

**Files:**
- Modify: `.claude/worktrees/feat-mirror-batch/processor/tcp/route_override_test.go:239-344`（`TestHandle_MirrorsConfiguredTrafficEvenWhenRouteIsDirect`）
- Test: `.claude/worktrees/feat-mirror-batch/processor/mirror/forwarder_test.go`（追加 keep-alive 用例）

- [ ] **Step 1: e2e 测试改为解包断言**

现测试对 body 做 `strings.Contains(s, '"event_type":"flow_start"')` 子串匹配 —— 批量信封下碰巧仍通过，改为显式解包 `{"messages":[...]}` 后逐条判断，语义才正确：

```go
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		gotStart := false
		gotChunk := false
		for _, body := range bodies {
			var env struct {
				Messages []struct {
					EventType string `json:"event_type"`
					Direction string `json:"direction"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(body, &env); err != nil {
				continue
			}
			for _, m := range env.Messages {
				if m.EventType == "flow_start" {
					gotStart = true
				}
				if m.Direction == "request" {
					gotChunk = true
				}
			}
		}
		mu.Unlock()
		if gotStart && gotChunk {
			break
		}
		select {
		case body := <-recvCh:
			_ = body
		case <-deadline:
			t.Fatalf("expected mirrored flow_start and request chunk to be forwarded, got bodies=%d", len(bodies))
		}
	}
```

（`json.Unmarshal` 报错时 `continue` 跳过即可；该测试的 handler 不区分路径，`/batch` POST 自然落入。）同时补 import `"encoding/json"`（若未有）。

- [ ] **Step 2: 追加 keep-alive 连接复用测试（drain 生效验证）**

追加到 `forwarder_test.go`：

```go
func TestForwarderReusesConnections(t *testing.T) {
	var connMu sync.Mutex
	newConns := 0
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	ts.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			connMu.Lock()
			newConns++
			connMu.Unlock()
		}
	}
	ts.Start()
	t.Cleanup(ts.Close)
	prev := GetGlobalForwarder()
	t.Cleanup(func() { globalMu.Lock(); globalForwarder = prev; globalMu.Unlock() })

	f := &Forwarder{
		target: batchTarget(ts.URL),
		client: &http.Client{Timeout: 2 * time.Second},
		ch:     make(chan MirrorMessage, 1024),
		done:   make(chan struct{}),
	}
	go f.run()

	// 三批消息（每批间隔超过 flush interval），全部完成后应复用同一连接
	for round := 0; round < 3; round++ {
		for i := 0; i < 5; i++ {
			f.Enqueue(chunkAt(uint64(round*10 + i)))
		}
		time.Sleep(250 * time.Millisecond)
	}
	f.Stop()

	connMu.Lock()
	defer connMu.Unlock()
	if newConns == 0 {
		t.Fatal("expected at least one connection")
	}
	if newConns > 2 {
		t.Fatalf("drain should enable keep-alive reuse; got %d new connections, want <= 2", newConns)
	}
}
```

import 补 `"net"`。注意：`newTestForwarder` 辅助函数基于 `httptest.NewServer`，此用例需要 `NewUnstartedServer` 以设置 `ConnState`，故独立建 forwarder。

- [ ] **Step 3: 运行相关包全部测试**

```bash
go test -race ./processor/mirror/ ./processor/tcp/ 2>&1 | tail -5
# 预期：全部 PASS（含改造后的 e2e）
```

- [ ] **Step 4: 提交**

```bash
git add processor/tcp/route_override_test.go processor/mirror/forwarder_test.go
git commit -m "修复：镜像 e2e 断言改为批量信封解包 + 新增 keep-alive 连接复用测试"
```

---

### Task 5: 文档与全量回归

**Files:**
- Modify: `docs/tutorials/zh_CN/usage-guide.md`（`traffic_mirror` 示例处，~L856-863 附近）

- [ ] **Step 1: usage-guide 加一行说明**

在 `traffic_mirror.target` 配置示例（形如 `http://172.16.159.219:3000/mirror`）附近追加：

```markdown
> 注意：`target` 路径后缀保持 `/mirror`。客户端会自动在末尾追加 `/batch` 走批量上报，
> 请勿自行把配置改成 `/mirror/batch`（客户端有幂等保护，但保持默认写法最清晰）。
```

- [ ] **Step 2: 全量构建与测试**

```bash
go build ./... && go test ./... 2>&1 | tail -10
# 预期：build 成功；无 FAIL（既有失败若与本次改动无关，记录并说明，不擅自修）
```

- [ ] **Step 3: 提交**

```bash
git add docs/tutorials/zh_CN/usage-guide.md
git commit -m "文档：traffic_mirror.target 保持 /mirror 后缀说明（客户端自动追加 /batch）"
```

---

### Task 6: 冒烟联调（人工验证，需 agent 运行环境）

**Files:** 无代码改动。

- [ ] **Step 1: 启动抓包服务端**

```bash
cd /Users/mac/MyProgram/AiProgram/MonodirCursorTraffic && node src/index.js
# 确认输出 Ingest: POST http://localhost:3000/mirror
```

- [ ] **Step 2: 配置 agent 镜像并产生流量**

在 agent 的 customer 配置中启用：

```json
{
  "customer": {
    "traffic_mirror": {
      "enabled": true,
      "target": "http://127.0.0.1:3000/mirror",
      "domains": ["*.cursor.sh"]
    }
  }
}
```

触发一次匹配域名的真实请求。

- [ ] **Step 3: 验证**

1. 服务端日志/dashboard（http://localhost:3000）出现完整 flow：flow_start → chunks → flow_end
2. `GET /api/stats`：`totalBatches` > 0 且 `totalErrors` 无异常增长
3. 高频请求（连续触发几十次）下：无 `[Mirror] Forwarder queue full` 日志、flow 无 unexpected gap 标记
4. agent 侧真实流量转发不受镜像影响（延迟无感知劣化）

- [ ] **Step 4: 合并收尾（两仓库，需用户确认后执行）**

```bash
# 客户端 worktree 合回 master（按用户工作流：验证后合并，推送前信息定稿）
# 服务端 feat/mirror-batch-endpoint 合回 main
```

---

## 部署顺序提醒（来自 spec 第 10 节）

**先服务端后客户端**：新客户端打 `/mirror/batch`，旧端点保留所以旧客户端不受影响；反之若客户端先升级而服务端未就绪，批量 POST 会 404（客户端只丢镜像数据，主链路不受影响，但抓包会断流）。
