package usage

import (
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStoreAt(filepath.Join(t.TempDir(), "usage.data"))
	if err != nil {
		t.Fatalf("OpenStoreAt: %v", err)
	}
	return store
}

func TestWatermarkCRUD(t *testing.T) {
	store := openTestStore(t)
	if got, _ := store.GetWatermark("/a/b.jsonl"); got != 0 {
		t.Fatalf("fresh watermark = %d, want 0", got)
	}
	if err := store.SetWatermark("/a/b.jsonl", 1234); err != nil {
		t.Fatalf("SetWatermark: %v", err)
	}
	if got, _ := store.GetWatermark("/a/b.jsonl"); got != 1234 {
		t.Fatalf("watermark = %d, want 1234", got)
	}
	if err := store.SetWatermark("/a/b.jsonl", 0); err != nil {
		t.Fatalf("SetWatermark reset: %v", err)
	}
	if got, _ := store.GetWatermark("/a/b.jsonl"); got != 0 {
		t.Fatalf("after reset watermark = %d, want 0", got)
	}
}

func TestBucketAccumulateAndDirty(t *testing.T) {
	store := openTestStore(t)

	b, err := store.Bucket(1727071200, "claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("Bucket: %v", err)
	}
	b.InputTokens += 100
	b.Requests++
	if err := store.SaveBucket(b); err != nil {
		t.Fatalf("SaveBucket: %v", err)
	}

	// 同 key 再取应是同一行（累计语义，不是新行）
	b2, _ := store.Bucket(1727071200, "claude-sonnet-4-5")
	if b2.ID != b.ID || b2.InputTokens != 100 || b2.Requests != 1 {
		t.Fatalf("bucket not accumulated: %+v", b2)
	}

	dirty, err := store.DirtyBuckets()
	if err != nil || len(dirty) != 1 {
		t.Fatalf("DirtyBuckets = %v, %v; want 1 row", dirty, err)
	}
	if err := store.ClearDirty([]int64{dirty[0].ID}); err != nil {
		t.Fatalf("ClearDirty: %v", err)
	}
	if dirty, _ = store.DirtyBuckets(); len(dirty) != 0 {
		t.Fatalf("after clear, dirty = %d rows", len(dirty))
	}
}

func TestAllBucketsAndDeleteWatermarks(t *testing.T) {
	store := openTestStore(t)
	for _, key := range [][2]interface{}{{int64(1), "m1"}, {int64(2), "m2"}} {
		b, _ := store.Bucket(key[0].(int64), key[1].(string))
		_ = store.SaveBucket(b)
	}
	all, err := store.AllBuckets()
	if err != nil || len(all) != 2 {
		t.Fatalf("AllBuckets = %d rows, %v; want 2", len(all), err)
	}

	_ = store.SetWatermark("/gone.jsonl", 10)
	if err := store.DeleteWatermarks([]string{"/gone.jsonl"}); err != nil {
		t.Fatalf("DeleteWatermarks: %v", err)
	}
	if got, _ := store.GetWatermark("/gone.jsonl"); got != 0 {
		t.Fatal("watermark not deleted")
	}
}
