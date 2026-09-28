package services

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lineageEntry 是测试与实现共用的最小解析视图：只关心链路三要素。
type lineageEntry struct {
	Uuid       string `json:"uuid"`
	ParentUuid string `json:"parentUuid"`
	Entrypoint string `json:"entrypoint"`
	Type       string `json:"type"`
}

func writeLineageLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func lineageLine(uuid, parent, entrypoint, typ string) string {
	e := map[string]string{
		"uuid":       uuid,
		"type":       typ,
		"entrypoint": entrypoint,
	}
	if parent != "" {
		e["parentUuid"] = parent
	}
	b, _ := json.Marshal(e)
	return string(b)
}

func readLineageLines(t *testing.T, path string) []lineageEntry {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []lineageEntry
	for _, ln := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		var e lineageEntry
		if err := json.Unmarshal(ln, &e); err != nil {
			t.Fatalf("line no longer valid json: %v (%s)", err, ln[:60])
		}
		out = append(out, e)
	}
	return out
}

// 分叉形态 = 交互(cli)链尖之后，agent 无头回合(sdk-cli)以兄弟分支追加——
// 2026-09-26 b1f82c61 / 2026-09-28 3d06bb04 两次实测的病灶。
func TestAppendLineageMarkerStitchesDiverged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeLineageLines(t, path, []string{
		lineageLine("u1", "", "cli", "user"),
		lineageLine("u2", "u1", "cli", "assistant"),
		lineageLine("u3", "u2", "cli", "system"), // cli 链尖
		lineageLine("u4", "u3", "sdk-cli", "user"),
		lineageLine("u5", "u4", "sdk-cli", "assistant"), // 文件真实尖端
	})
	if err := appendLineageMarker(path, "sid-1", "/proj", "电话回合已并入"); err != nil {
		t.Fatal(err)
	}
	entries := readLineageLines(t, path)
	if len(entries) != 6 {
		t.Fatalf("entries = %d, want 6 (5 + marker)", len(entries))
	}
	marker := entries[len(entries)-1]
	if marker.Entrypoint != "cli" {
		t.Fatalf("marker entrypoint = %q, want cli (resume 只沿最新 cli 链回溯)", marker.Entrypoint)
	}
	if marker.ParentUuid != "u5" {
		t.Fatalf("marker parent = %q, want file tip u5", marker.ParentUuid)
	}
	if marker.Uuid == "" {
		t.Fatal("marker must carry a uuid")
	}
}

func TestAppendLineageMarkerNoOpWhenLinear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeLineageLines(t, path, []string{
		lineageLine("u1", "", "cli", "user"),
		lineageLine("u2", "u1", "sdk-cli", "assistant"),
		lineageLine("u3", "u2", "cli", "user"), // 最新 cli 链已含文件尖端
	})
	before, _ := os.ReadFile(path)
	if err := appendLineageMarker(path, "sid-1", "/proj", "n/a"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("linear history must not be touched")
	}
}

func TestAppendLineageMarkerNoOpWithoutCliHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeLineageLines(t, path, []string{
		lineageLine("u1", "", "sdk-cli", "user"),
		lineageLine("u2", "u1", "sdk-cli", "assistant"),
	})
	before, _ := os.ReadFile(path)
	if err := appendLineageMarker(path, "sid-1", "/proj", "n/a"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("sdk-only history has no interactive chain to stitch — must stay untouched")
	}
}

func TestAppendLineageMarkerIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeLineageLines(t, path, []string{
		lineageLine("u1", "", "cli", "user"),
		lineageLine("u2", "u1", "sdk-cli", "assistant"),
	})
	if err := appendLineageMarker(path, "sid-1", "/proj", "n/a"); err != nil {
		t.Fatal(err)
	}
	afterFirst, _ := os.ReadFile(path)
	if err := appendLineageMarker(path, "sid-1", "/proj", "n/a"); err != nil {
		t.Fatal(err)
	}
	afterSecond, _ := os.ReadFile(path)
	if !bytes.Equal(afterFirst, afterSecond) {
		t.Fatal("second pass must be a no-op (marker already extends the cli chain)")
	}
}

// 1MB+ 病态行不得冻结谱系分析（v1.1.41 事故的回归护栏）。
func TestAppendLineageMarkerSurvivesHugeLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	huge := `{"uuid":"big","parentUuid":"u2","entrypoint":"cli","type":"user","content":"` +
		strings.Repeat("x", 2*1024*1024) + `"}`
	writeLineageLines(t, path, []string{
		lineageLine("u1", "", "cli", "user"),
		lineageLine("u2", "u1", "cli", "assistant"),
		huge,
		lineageLine("u4", "u2", "sdk-cli", "assistant"), // 与大行分叉
	})
	if err := appendLineageMarker(path, "sid-1", "/proj", "n/a"); err != nil {
		t.Fatal(err)
	}
	entries := readLineageLines(t, path)
	last := entries[len(entries)-1]
	if last.ParentUuid != "u4" || last.Entrypoint != "cli" {
		t.Fatalf("marker after huge line wrong: parent=%q entrypoint=%q, want u4/cli", last.ParentUuid, last.Entrypoint)
	}
}
