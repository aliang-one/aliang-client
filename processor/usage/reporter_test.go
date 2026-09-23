package usage

import (
	"encoding/json"
	"errors"
	"testing"
)

type fakeWriter struct {
	messages []map[string]interface{}
	failNext bool
	// failAfter > 0 时：已成功写入 failAfter 条消息后，下一次 write 失败
	// （模拟第 failAfter+1 批推送失败）。
	failAfter int
}

func (w *fakeWriter) write(payload interface{}) error {
	if w.failNext {
		w.failNext = false
		return errors.New("fake write failure")
	}
	if w.failAfter > 0 && len(w.messages) == w.failAfter {
		return errors.New("fake write failure (failAfter)")
	}
	m, _ := payload.(map[string]interface{})
	w.messages = append(w.messages, m)
	return nil
}

func seedBuckets(t *testing.T, store *Store, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		b, _ := store.Bucket(int64(1727071200+i*3600), "claude-sonnet-4-5")
		b.Requests = int64(i + 1)
		b.InputTokens = 100
		b.OutputTokens = 50
		b.CacheReadTokens = 7
		b.CacheCreationTokens = 3
		b.ActiveSessions = 1
		b.SessionSet = `["s1"]`
		b.FirstSeen = int64(1727071200 + i*3600)
		b.LastSeen = int64(1727071500 + i*3600)
		b.Dirty = true
		if err := store.SaveBucket(b); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func TestFlushAllSendsBatchesAndClearsDirty(t *testing.T) {
	store := openTestStore(t)
	seedBuckets(t, store, 3)
	w := &fakeWriter{}
	r := NewReporter(store, func() string { return "dev-1" }, "Asia/Shanghai")

	if err := r.FlushAll(w.write); err != nil {
		t.Fatalf("FlushAll: %v", err)
	}
	if len(w.messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(w.messages))
	}
	if w.messages[0]["type"] != "usage.report" {
		t.Fatalf("type = %v", w.messages[0]["type"])
	}
	records := w.messages[0]["records"].([]map[string]interface{})
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3", len(records))
	}
	rec := records[0]
	if rec["device_id"] != "dev-1" || rec["tz"] != "Asia/Shanghai" || rec["model"] != "claude-sonnet-4-5" {
		t.Fatalf("record fields wrong: %+v", rec)
	}
	if dirty, _ := store.DirtyBuckets(); len(dirty) != 0 {
		t.Fatal("dirty should be cleared after successful flush")
	}
}

func TestFlushBatchesOver500(t *testing.T) {
	store := openTestStore(t)
	seedBuckets(t, store, 501)
	w := &fakeWriter{}
	r := NewReporter(store, func() string { return "dev-1" }, "UTC")
	if err := r.FlushAll(w.write); err != nil {
		t.Fatalf("FlushAll: %v", err)
	}
	if len(w.messages) != 2 {
		t.Fatalf("messages = %d, want 2 (500+1)", len(w.messages))
	}
}

func TestFlushFailureKeepsDirty(t *testing.T) {
	store := openTestStore(t)
	seedBuckets(t, store, 1)
	w := &fakeWriter{failNext: true}
	r := NewReporter(store, func() string { return "dev-1" }, "UTC")
	if err := r.FlushAll(w.write); err == nil {
		t.Fatal("expected error")
	}
	if dirty, _ := store.DirtyBuckets(); len(dirty) != 1 {
		t.Fatal("dirty must survive failed flush")
	}
}

// 部分失败：第二批（第 501 条）推送失败 → 仅已成功的前 500 桶按快照
// revision 条件清除，未推送的尾桶保持 dirty 下轮重推。
func TestFlushPartialFailureClearsOnlyPushedBatches(t *testing.T) {
	store := openTestStore(t)
	seedBuckets(t, store, 501)
	w := &fakeWriter{failAfter: 1}
	r := NewReporter(store, func() string { return "dev-1" }, "UTC")
	if err := r.FlushAll(w.write); err == nil {
		t.Fatal("expected error on second batch")
	}
	if len(w.messages) != 1 {
		t.Fatalf("messages = %d, want 1 successful batch before failure", len(w.messages))
	}
	dirty, _ := store.DirtyBuckets()
	if len(dirty) != 1 {
		t.Fatalf("dirty = %d rows, want 1 (only the un-pushed tail bucket)", len(dirty))
	}
	if dirty[0].HourStart != int64(1727071200+500*3600) {
		t.Fatalf("surviving dirty bucket hour_start = %d, want the 501st bucket", dirty[0].HourStart)
	}
}

// write 为 nil（未连接）时全部 no-op：不推送、不触碰 dirty、不 panic。
func TestNilWriteNoOp(t *testing.T) {
	store := openTestStore(t)
	seedBuckets(t, store, 1)
	r := NewReporter(store, func() string { return "dev-1" }, "UTC")
	if err := r.FlushDirty(nil); err != nil {
		t.Fatalf("FlushDirty(nil): %v", err)
	}
	if err := r.FlushAll(nil); err != nil {
		t.Fatalf("FlushAll(nil): %v", err)
	}
	if dirty, _ := store.DirtyBuckets(); len(dirty) != 1 {
		t.Fatal("nil write must not touch dirty state")
	}
}

// 竞态守卫：快照后桶又被追加（revision 变化）→ 本次清除失效，dirty 保留。
func TestFlushSkipsClearWhenBucketChangedMidFlight(t *testing.T) {
	store := openTestStore(t)
	seedBuckets(t, store, 1)

	// 用一个可控 writer：推送成功前先把桶再改一轮（模拟 tracker 并发追加）
	w := &fakeWriter{}
	var snapshotRevision int64
	tracking := func(payload interface{}) error {
		if err := w.write(payload); err != nil {
			return err
		}
		// 首批推送成功后立刻模拟并发追加：revision 应从 1 变 2
		b, _ := store.Bucket(1727071200, "claude-sonnet-4-5")
		snapshotRevision = b.Revision
		b.InputTokens += 999
		_ = store.SaveBucket(b)
		return nil
	}
	r := NewReporter(store, func() string { return "dev-1" }, "UTC")
	if err := r.FlushAll(tracking); err != nil {
		t.Fatalf("FlushAll: %v", err)
	}
	dirty, _ := store.DirtyBuckets()
	if len(dirty) != 1 {
		t.Fatalf("dirty must survive mid-flight append (revision guard), got %d", len(dirty))
	}
	if dirty[0].Revision != snapshotRevision+1 {
		t.Fatalf("revision should have been bumped by concurrent append: %d vs %d", dirty[0].Revision, snapshotRevision+1)
	}
	// 下轮正常清掉
	if err := r.FlushAll(w.write); err != nil {
		t.Fatalf("second flush: %v", err)
	}
	if dirty, _ = store.DirtyBuckets(); len(dirty) != 0 {
		t.Fatal("second flush should clear dirty")
	}
}

func TestRecordJSONShape(t *testing.T) {
	store := openTestStore(t)
	seedBuckets(t, store, 1)
	all, _ := store.AllBuckets()
	rec := usageRecord("dev-1", all[0], "UTC")
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var probe struct {
		DeviceID  string  `json:"device_id"`
		HourStart float64 `json:"hour_start"`
		TZ        string  `json:"tz"`
		Model     string  `json:"model"`
		Requests  float64 `json:"requests"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if probe.DeviceID != "dev-1" || probe.HourStart != 1727071200 || probe.Model != "claude-sonnet-4-5" || probe.Requests != 1 {
		t.Fatalf("shape wrong: %+v", probe)
	}
}
