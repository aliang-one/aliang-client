package mirror

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestBatchTarget(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain mirror path", "http://h:3000/mirror", "http://h:3000/mirror/batch"},
		{"trailing slash", "http://h:3000/mirror/", "http://h:3000/mirror/batch"},
		{"already batched", "http://h:3000/mirror/batch", "http://h:3000/mirror/batch"},
		{"batched with trailing slash", "http://h:3000/mirror/batch/", "http://h:3000/mirror/batch"},
		{"no path", "http://h:3000", "http://h:3000/batch"},
	}
	for _, tc := range cases {
		if got := batchTarget(tc.in); got != tc.want {
			t.Errorf("%s: batchTarget(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

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

func newTestForwarder(t *testing.T, capacity int, h http.HandlerFunc) (*Forwarder, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	prev := GetGlobalForwarder()
	t.Cleanup(func() { globalMu.Lock(); globalForwarder = prev; globalMu.Unlock() })
	f := &Forwarder{
		target: batchTarget(ts.URL),
		client: &http.Client{Timeout: 2 * time.Second},
		ch:     make(chan MirrorMessage, capacity),
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

func TestForwarderFlushIntervalSurvivesIdle(t *testing.T) {
	store := new(capturedBatch)
	f, _ := newTestForwarder(t, 1024, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		store.append(b)
		w.WriteHeader(http.StatusOK)
	})
	f.Enqueue(chunkAt(1))
	waitFor(t, 2*time.Second, func() bool { return store.count() == 1 })
	// 空闲一段时间：200ms 定时器会在 pending 为空时触发一次（空 flush）。
	// 若空 flush 不重新武装定时器，interval 便永久失效，后续消息再也无法按超时发出。
	time.Sleep(400 * time.Millisecond)
	f.Enqueue(chunkAt(2))
	waitFor(t, 2*time.Second, func() bool { return store.count() == 2 })
	f.Stop()
}
