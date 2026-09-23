package usage

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func mustWrite(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func newTestTracker(t *testing.T, root string) *Tracker {
	t.Helper()
	store := openTestStore(t)
	return NewTracker(store, []string{root}, func() bool { return true })
}

func jsonlLineAt(ts string, model string, in, out int64, session string) string {
	return `{"type":"assistant","sessionId":"` + session + `","uuid":"u-` + session + `-` + ts + `","timestamp":"` + ts + `","message":{"model":"` + model + `","usage":{"input_tokens":` +
		strconv.FormatInt(in, 10) + `,"output_tokens":` + strconv.FormatInt(out, 10) + `}}}` + "\n"
}

func TestTrackerIncrementalScan(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "proj", "s1.jsonl")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	mustWrite(t, path,
		jsonlLineAt("2026-09-23T10:05:29.545Z", "m1", 10, 5, "sess-a")+
			`{"type":"user","sessionId":"sess-a","uuid":"x"}`+"\n"+ // 应跳过
			`{"type":"assistant",`+"\n"+ // 损坏整行：跳过
			jsonlLineAt("2026-09-23T10:30:00.000Z", "m1", 20, 8, "sess-a"))

	tr := newTestTracker(t, root)
	if err := tr.ScanOnce(); err != nil {
		t.Fatalf("ScanOnce: %v", err)
	}

	all, _ := tr.store.AllBuckets()
	if len(all) != 1 {
		t.Fatalf("buckets = %d, want 1", len(all))
	}
	b := all[0]
	if b.InputTokens != 30 || b.OutputTokens != 13 || b.Requests != 2 {
		t.Fatalf("aggregation wrong: %+v", b)
	}
	if b.ActiveSessions != 1 {
		t.Fatalf("active sessions = %d, want 1", b.ActiveSessions)
	}

	// 追加新行：增量读取不重复计数
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(jsonlLineAt("2026-09-23T10:45:00.000Z", "m1", 7, 3, "sess-a"))
	_ = f.Close()
	if err := tr.ScanOnce(); err != nil {
		t.Fatalf("ScanOnce 2: %v", err)
	}
	all, _ = tr.store.AllBuckets()
	if all[0].Requests != 3 || all[0].InputTokens != 37 {
		t.Fatalf("incremental recount wrong: %+v", all[0])
	}
}

func TestTrackerPartialTrailingLineNotConsumed(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "s2.jsonl")
	mustWrite(t, path, jsonlLineAt("2026-09-23T10:05:29.545Z", "m1", 10, 5, "s")+
		`{"type":"assistant","sessionId":"s","timestamp":"2026-09-23T10:0`) // 半行

	tr := newTestTracker(t, root)
	if err := tr.ScanOnce(); err != nil {
		t.Fatalf("ScanOnce: %v", err)
	}
	all, _ := tr.store.AllBuckets()
	if len(all) != 1 || all[0].Requests != 1 {
		t.Fatalf("want exactly 1 request counted, got %+v", all)
	}

	// 半行补全后下轮可读（补全后时间戳须为合法 RFC3339：10:05:00.000Z）
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(`5:00.000Z","message":{"model":"m1","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n")
	_ = f.Close()
	_ = tr.ScanOnce()
	all, _ = tr.store.AllBuckets()
	if all[0].Requests != 2 {
		t.Fatalf("completed line not consumed: %+v", all[0])
	}
}

func TestTrackerTruncatedFileRescans(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "s3.jsonl")
	mustWrite(t, path, jsonlLineAt("2026-09-23T10:05:29.545Z", "m1", 10, 5, "s")+
		jsonlLineAt("2026-09-23T10:06:00.000Z", "m1", 10, 5, "s"))
	tr := newTestTracker(t, root)
	_ = tr.ScanOnce()

	// 文件被截断重建：水位归零重扫（可能重复计入，spec 容忍）。
	// 关键断言：重扫后水位重新推进，新数据不漏。
	mustWrite(t, path, jsonlLineAt("2026-09-23T11:00:00.000Z", "m1", 10, 5, "s2"))
	_ = tr.ScanOnce()
	wm, _ := tr.store.GetWatermark(path)
	if wm == 0 {
		t.Fatal("watermark should be re-advanced after truncate rescan")
	}
	all, _ := tr.store.AllBuckets()
	// 断言不依赖运行机器时区：期望小时桶由 LocalHourStart 对新行时间戳推导。
	newTs, _ := time.Parse(time.RFC3339, "2026-09-23T11:00:00.000Z")
	wantHour, _ := LocalHourStart(newTs)
	var newHourRequests int64
	for _, b := range all {
		if b.HourStart == wantHour {
			newHourRequests += b.Requests
		}
	}
	if newHourRequests != 1 {
		t.Fatalf("post-truncate rescan should count new line once, got %d", newHourRequests)
	}
}

func TestTrackerMissingFileWatermarkCleaned(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "gone.jsonl")
	mustWrite(t, path, jsonlLineAt("2026-09-23T10:05:29.545Z", "m1", 10, 5, "s"))
	tr := newTestTracker(t, root)
	_ = tr.ScanOnce()
	_ = os.Remove(path)
	_ = tr.ScanOnce() // 完整走完一轮后应清理水位
	if wm, _ := tr.store.GetWatermark(path); wm != 0 {
		t.Fatalf("watermark for deleted file should be removed, got %d", wm)
	}
}

func TestLocalHourBucketAndTZ(t *testing.T) {
	ts, _ := time.Parse(time.RFC3339, "2026-09-23T10:59:59.000Z")
	hour, tz := LocalHourStart(ts)
	if tz == "" {
		t.Fatal("tz name should not be empty")
	}
	local := ts.Local()
	want := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, local.Location()).Unix()
	if hour != want {
		t.Fatalf("hour bucket = %d, want %d", hour, want)
	}
}

func TestTrackerDisabledPredicateSkips(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "s4.jsonl")
	mustWrite(t, path, jsonlLineAt("2026-09-23T10:05:29.545Z", "m1", 10, 5, "s"))
	allowed := false
	store := openTestStore(t)
	tr := NewTracker(store, []string{root}, func() bool { return allowed })
	_ = tr.ScanOnce()
	all, _ := store.AllBuckets()
	if len(all) != 0 {
		t.Fatalf("disabled tracker must not collect, got %+v", all)
	}
}

// TestTrackerMultiMegabyteLine 钉死「ReadBytes 而非 Scanner」的实现不变量：
// Scanner 默认 64KB token 上限会在超长行上炸掉/丢行，ReadBytes 无上限。
func TestTrackerMultiMegabyteLine(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "big.jsonl")
	pad := strings.Repeat("A", 2<<20) // ~2MB 填充字段（伪造 content 文本）
	bigLine := func(uuid, ts string, in, out int64) string {
		return `{"type":"assistant","sessionId":"big-sess","uuid":"` + uuid +
			`","timestamp":"` + ts + `","message":{"model":"m1","content":"` + pad +
			`","usage":{"input_tokens":` + strconv.FormatInt(in, 10) +
			`,"output_tokens":` + strconv.FormatInt(out, 10) + `}}}` + "\n"
	}
	first := jsonlLineAt("2026-09-23T10:05:29.545Z", "m1", 10, 5, "big-sess")
	full2 := bigLine("u-big-2", "2026-09-23T10:10:00.000Z", 100, 50)
	full3 := bigLine("u-big-3", "2026-09-23T10:15:00.000Z", 200, 80)
	half3 := full3[:len(full3)/2] // 半写的超长行：无结尾换行

	mustWrite(t, path, first+full2+half3)
	size1, _ := os.Stat(path)

	tr := newTestTracker(t, root)
	if err := tr.ScanOnce(); err != nil {
		t.Fatalf("ScanOnce: %v", err)
	}
	sum := func() (reqs, inTok, outTok int64) {
		all, _ := tr.store.AllBuckets()
		for _, b := range all {
			reqs += b.Requests
			inTok += b.InputTokens
			outTok += b.OutputTokens
		}
		return
	}
	reqs, inTok, outTok := sum()
	if reqs != 2 || inTok != 110 || outTok != 55 {
		t.Fatalf("after first scan: reqs=%d in=%d out=%d, want 2/110/55", reqs, inTok, outTok)
	}
	if wm, _ := tr.store.GetWatermark(path); wm != size1.Size()-int64(len(half3)) {
		t.Fatalf("watermark = %d, want %d (held back before half-written 2MB line)",
			wm, size1.Size()-int64(len(half3)))
	}

	// 补全半行：下一轮恰好计一次，水位追平文件大小
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(full3[len(full3)/2:])
	_ = f.Close()
	_ = tr.ScanOnce()
	reqs, inTok, outTok = sum()
	if reqs != 3 || inTok != 310 || outTok != 135 {
		t.Fatalf("after completion: reqs=%d in=%d out=%d, want 3/310/135", reqs, inTok, outTok)
	}
	size2, _ := os.Stat(path)
	if wm, _ := tr.store.GetWatermark(path); wm != size2.Size() {
		t.Fatalf("watermark = %d, want file size %d", wm, size2.Size())
	}
}
