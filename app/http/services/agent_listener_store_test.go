package services

// agent_listener_store_test.go 监听器本地镜像(~/.aliang/listeners.json)存储层单测
// (specs/005-event-listener §1/FR-3/FR-4)。home 注入沿用 mcpRegistryHome 同款
// seam:swap 包级 listenerMirrorHome 并重置镜像缓存(测试串行跑,禁 t.Parallel)。
import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withListenerMirrorHome swap home 注入点 + 清缓存;Cleanup 双向还原。
func withListenerMirrorHome(t *testing.T, home string) {
	t.Helper()
	reset := func() {
		listenerMirrorMu.Lock()
		listenerMirrorCache = nil
		listenerMirrorLoaded = false
		listenerMirrorMu.Unlock()
	}
	old := listenerMirrorHome
	listenerMirrorHome = func() string { return home }
	reset()
	t.Cleanup(func() {
		listenerMirrorHome = old
		reset()
	})
}

// sampleListenerMirror 全字段填充的基准行(source_params 用 RawMessage 直塞)。
func sampleListenerMirror(id, updatedAt string) ListenerMirror {
	return ListenerMirror{
		ID:              id,
		AccountID:       "acc-1",
		Title:           "盯 build.log 等编译报错",
		SourceType:      "file_tail",
		SourceDeviceID:  "dev-mac",
		SourceParams:    json.RawMessage(`{"path":"/tmp/build.log","match":"ERROR","max_bytes":4096}`),
		PromptTemplate:  "监听器 {{event}} 命中,请给出处理方案",
		TargetSessionID: "sess-42",
		Mode:            "recurring",
		Status:          "active",
		CooldownMs:      600000,
		ExpiresAt:       "2026-11-27T00:00:00Z",
		LastFiredAt:     "2026-11-20T08:00:00Z",
		FiredCount:      3,
		DedupeKey:       "fp-abc",
		OverflowCount:   1,
		RejectCount:     2,
		ErrorNote:       "",
		CreatedBy:       "ai:sess-42",
		CreatedAt:       "2026-11-20T00:00:00Z",
		UpdatedAt:       updatedAt,
	}
}

// compactJSON 压缩后比较(RawMessage 往返会带缩进形态差异,语义比较须压平)。
func compactJSON(t *testing.T, s string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(s)); err != nil {
		t.Fatalf("compact %q: %v", s, err)
	}
	return buf.String()
}

func listenerMirrorFilePath(t *testing.T, home string) string {
	t.Helper()
	path, err := listenerMirrorPath()
	if err != nil {
		t.Fatalf("listenerMirrorPath: %v", err)
	}
	if path != filepath.Join(home, ".aliang", "listeners.json") {
		t.Fatalf("镜像路径不符: %s", path)
	}
	return path
}

// 1. save→load 全字段往返(RawMessage 压平比较)。
func TestListenerMirrorSaveLoadRoundTrip(t *testing.T) {
	home := t.TempDir()
	withListenerMirrorHome(t, home)
	want := []ListenerMirror{
		sampleListenerMirror("L-1", "2026-11-20T10:00:00Z"),
		sampleListenerMirror("L-2", "2026-11-20T11:00:00Z"),
	}
	if err := saveListenerMirrors(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := listListenerMirrors() // Task 10 合并双读口后唯一规范读(loadListenerMirrors 已删)
	if len(got) != 2 {
		t.Fatalf("期望 2 条, got %d: %+v", len(got), got)
	}
	for i := range want {
		w, g := want[i], got[i]
		if w.ID != g.ID || w.AccountID != g.AccountID || w.Title != g.Title ||
			w.SourceType != g.SourceType || w.SourceDeviceID != g.SourceDeviceID ||
			w.PromptTemplate != g.PromptTemplate || w.TargetSessionID != g.TargetSessionID ||
			w.Mode != g.Mode || w.Status != g.Status || w.CooldownMs != g.CooldownMs ||
			w.ExpiresAt != g.ExpiresAt || w.LastFiredAt != g.LastFiredAt ||
			w.FiredCount != g.FiredCount || w.DedupeKey != g.DedupeKey ||
			w.OverflowCount != g.OverflowCount || w.RejectCount != g.RejectCount ||
			w.ErrorNote != g.ErrorNote || w.CreatedBy != g.CreatedBy ||
			w.CreatedAt != g.CreatedAt || w.UpdatedAt != g.UpdatedAt {
			t.Fatalf("第 %d 行字段往返丢失:\nwant=%+v\ngot=%+v", i, w, g)
		}
		if compactJSON(t, string(w.SourceParams)) != compactJSON(t, string(g.SourceParams)) {
			t.Fatalf("第 %d 行 source_params 往返丢失: want=%s got=%s", i, w.SourceParams, g.SourceParams)
		}
	}
}

// 2. 字节等价幂等跳写:同内容二存不重写(mtime 不动)。
func TestListenerMirrorSaveIdempotentByteEqual(t *testing.T) {
	home := t.TempDir()
	withListenerMirrorHome(t, home)
	list := []ListenerMirror{sampleListenerMirror("L-1", "2026-11-20T10:00:00Z")}
	if err := saveListenerMirrors(list); err != nil {
		t.Fatalf("首次 save: %v", err)
	}
	path := listenerMirrorFilePath(t, home)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond) // mtime 可区分
	if err := saveListenerMirrors(list); err != nil {
		t.Fatalf("二存: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("同内容二存必须幂等跳写(mtime 被重写): before=%v after=%v", before.ModTime(), after.ModTime())
	}
}

// 3. upsert LWW 仲裁:旧拒/新收/平局新胜/缺则追加。
func TestListenerMirrorUpsertLWW(t *testing.T) {
	t.Run("older rejected", func(t *testing.T) {
		home := t.TempDir()
		withListenerMirrorHome(t, home)
		if err := upsertListenerMirror(sampleListenerMirror("L-1", "2026-11-20T12:00:00Z")); err != nil {
			t.Fatal(err)
		}
		path := listenerMirrorFilePath(t, home)
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		stale := sampleListenerMirror("L-1", "2026-11-20T11:00:00Z")
		stale.Title = "过期推送"
		if err := upsertListenerMirror(stale); err != nil {
			t.Fatalf("旧 updated_at 拒收不是错误: %v", err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatal("旧 updated_at 必须拒收且文件零改动")
		}
		if m, ok := getListenerMirror("L-1"); !ok || m.Title != "盯 build.log 等编译报错" {
			t.Fatalf("拒收后镜像须保持新值: %+v", m)
		}
	})
	t.Run("newer accepted", func(t *testing.T) {
		home := t.TempDir()
		withListenerMirrorHome(t, home)
		if err := upsertListenerMirror(sampleListenerMirror("L-1", "2026-11-20T11:00:00Z")); err != nil {
			t.Fatal(err)
		}
		fresh := sampleListenerMirror("L-1", "2026-11-20T12:00:00Z")
		fresh.Status = "paused"
		if err := upsertListenerMirror(fresh); err != nil {
			t.Fatal(err)
		}
		if m, ok := getListenerMirror("L-1"); !ok || m.Status != "paused" || m.UpdatedAt != "2026-11-20T12:00:00Z" {
			t.Fatalf("新 updated_at 必须落盘: %+v", m)
		}
		// 拒收过的旧值再来一遍仍然进不来
		if err := upsertListenerMirror(sampleListenerMirror("L-1", "2026-11-20T11:00:00Z")); err != nil {
			t.Fatal(err)
		}
		if m, _ := getListenerMirror("L-1"); m.Status != "paused" {
			t.Fatalf("旧值不得回退镜像: %+v", m)
		}
	})
	t.Run("equal tie new wins", func(t *testing.T) {
		home := t.TempDir()
		withListenerMirrorHome(t, home)
		if err := upsertListenerMirror(sampleListenerMirror("L-1", "2026-11-20T12:00:00Z")); err != nil {
			t.Fatal(err)
		}
		replay := sampleListenerMirror("L-1", "2026-11-20T12:00:00Z")
		replay.ErrorNote = "queue_overflow"
		if err := upsertListenerMirror(replay); err != nil {
			t.Fatal(err)
		}
		if m, ok := getListenerMirror("L-1"); !ok || m.ErrorNote != "queue_overflow" {
			t.Fatalf("平局须新胜(server 重放胜过镜像陈旧): %+v", m)
		}
	})
	t.Run("empty oldTs is oldest", func(t *testing.T) {
		// 头注释口径:空 oldTs(镜像行缺 updated_at)视为最旧,不挡新值;双空平局新胜。
		if !updatedAtLWW("2026-11-20T10:00:00Z", "") {
			t.Fatal("空 oldTs 须视为最旧(缺字段不挡新值)")
		}
		if !updatedAtLWW("", "") {
			t.Fatal("双空平局须新胜")
		}
	})
	t.Run("absent appended", func(t *testing.T) {
		home := t.TempDir()
		withListenerMirrorHome(t, home)
		if err := upsertListenerMirror(sampleListenerMirror("L-1", "2026-11-20T10:00:00Z")); err != nil {
			t.Fatal(err)
		}
		if err := upsertListenerMirror(sampleListenerMirror("L-2", "2026-11-20T09:00:00Z")); err != nil {
			t.Fatal(err) // 缺行直接追加,不与既有行比时间
		}
		got := listListenerMirrors()
		if len(got) != 2 {
			t.Fatalf("期望 2 条, got %d", len(got))
		}
		if _, ok := getListenerMirror("L-2"); !ok {
			t.Fatal("L-2 必须已追加")
		}
	})
}

// 4. remove:在则删;缺则 no-op 不报错。
func TestListenerMirrorRemove(t *testing.T) {
	home := t.TempDir()
	withListenerMirrorHome(t, home)
	if err := upsertListenerMirror(sampleListenerMirror("L-1", "2026-11-20T10:00:00Z")); err != nil {
		t.Fatal(err)
	}
	if err := removeListenerMirror("L-1"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := listListenerMirrors(); len(got) != 0 {
		t.Fatalf("删除后应为空: %+v", got)
	}
	path := listenerMirrorFilePath(t, home)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeListenerMirror("ghost"); err != nil {
		t.Fatalf("删不存在 id 须 no-op 不是错误: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("删不存在 id 不得重写文件")
	}
}

// 5. 坏文件容忍:load 空+不 panic;后续 upsert 覆写干净文件。
func TestListenerMirrorCorruptFileRecovers(t *testing.T) {
	home := t.TempDir()
	withListenerMirrorHome(t, home)
	path := listenerMirrorFilePath(t, home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"not":"an array"`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := listListenerMirrors() // 坏文件不 panic、不报错(离线容忍)
	if len(got) != 0 {
		t.Fatalf("坏文件按空镜像处理: %+v", got)
	}
	// 坏文件 + upsert → 覆写为干净文件
	if err := upsertListenerMirror(sampleListenerMirror("L-1", "2026-11-20T10:00:00Z")); err != nil {
		t.Fatalf("坏文件后 upsert 须可恢复: %v", err)
	}
	got = listListenerMirrors()
	if len(got) != 1 || got[0].ID != "L-1" {
		t.Fatalf("恢复后应恰有一条 L-1: %+v", got)
	}
}

// 6. 缺文件:load 空+无错;首次 upsert 自动建目录与文件。
func TestListenerMirrorMissingFileFirstUpsert(t *testing.T) {
	home := t.TempDir()
	withListenerMirrorHome(t, home) // home 下无 .aliang 目录
	got := listListenerMirrors()    // 缺文件不报错
	if len(got) != 0 {
		t.Fatalf("缺文件按空镜像处理: %+v", got)
	}
	if got == nil {
		t.Fatal("空表须是非 nil 形态(Task 11 代理层据此 marshal [] 而非 null)")
	}
	if err := upsertListenerMirror(sampleListenerMirror("L-1", "2026-11-20T10:00:00Z")); err != nil {
		t.Fatalf("首次 upsert: %v", err)
	}
	listenerMirrorFilePath(t, home) // 路径断言 + 存在性由下方读取验证
	raw, err := os.ReadFile(filepath.Join(home, ".aliang", "listeners.json"))
	if err != nil {
		t.Fatalf("首次 upsert 必须建目录与文件: %v", err)
	}
	if !strings.Contains(string(raw), `"L-1"`) {
		t.Fatalf("文件内容须含 L-1: %s", raw)
	}
}
