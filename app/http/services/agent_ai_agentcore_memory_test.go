package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// swapAgentCoreMemoryDirForTest 把包级 store 的目录指向 t.TempDir;测试结束还原。
func swapAgentCoreMemoryDirForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	restore := AgentCoreMemory.SetDirForTest(dir)
	t.Cleanup(restore)
	return dir
}

func TestAgentCoreMemorySlug(t *testing.T) {
	got := agentCoreMemoryProjectSlug("/Users/mac/My Project/x")
	if strings.ContainsAny(got, "/") {
		t.Fatalf("slug 不得含路径分隔符: %q", got)
	}
	if got != agentCoreMemoryProjectSlug("/Users/mac/My Project/x") {
		t.Fatal("slug 必须确定")
	}
}

func TestAgentCoreMemoryRoundTrip(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	e, err := agentCoreMemory.addManual("/p/a", "提交信息全中文")
	if err != nil || e.ID != 1 || e.Source != "manual" {
		t.Fatalf("首条写入: %+v err=%v", e, err)
	}
	got := agentCoreMemory.list("/p/a")
	if len(got) != 1 || got[0].Content != "提交信息全中文" || got[0].ID != 1 {
		t.Fatalf("读回不一致: %+v", got)
	}
	// 落盘文件形态:JSON 可解析、version/project 字段在
	raw, err := os.ReadFile(filepath.Join(agentCoreMemory.dirFn(), "_p_a.json"))
	if err != nil || !strings.Contains(string(raw), `"version"`) {
		t.Fatalf("落盘形态: %v %s", err, raw)
	}
}

func TestAgentCoreMemoryHandEditedBail(t *testing.T) {
	dir := swapAgentCoreMemoryDirForTest(t)
	// 手改文件:空白项/坏 id/重复 id 全丢弃,合法项保留,trim 生效
	p := filepath.Join(dir, "_p_b.json")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(p, []byte(`{"version":1,"project":"/p/b","next_id":9,"memories":[
		{"id":3,"content":" ok ","source":"extract"},
		{"id":0,"content":"bad-id","source":"extract"},
		{"id":3,"content":"dup","source":"extract"},
		{"id":5,"content":"  ","source":"extract"}]}`), 0o644)
	got := agentCoreMemory.list("/p/b")
	if len(got) != 1 || got[0].ID != 3 || got[0].Content != "ok" {
		t.Fatalf("手改兜底: %+v", got)
	}
}

func TestAgentCoreMemoryBrokenFileBak(t *testing.T) {
	dir := swapAgentCoreMemoryDirForTest(t)
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "_p_c.json"), []byte("{not-json"), 0o644)
	_, _ = agentCoreMemory.addManual("/p/c", "after-crash")
	if _, err := os.Stat(filepath.Join(dir, "_p_c.json.bak")); err != nil {
		t.Fatalf("损坏文件必须留 .bak 现场且重建成功: %v", err)
	}
	if len(agentCoreMemory.list("/p/c")) != 1 {
		t.Fatal("重建后新写入必须可读")
	}
}

func TestAgentCoreMemoryNoHomeNoop(t *testing.T) {
	prev := agentCoreMemory.dirFn
	agentCoreMemory.dirFn = func() string { return "" }
	t.Cleanup(func() { agentCoreMemory.dirFn = prev })
	if got := agentCoreMemory.list("/p/x"); len(got) != 0 {
		t.Fatalf("dir 空=list no-op: %+v", got)
	}
	if _, err := agentCoreMemory.addManual("/p/x", "y"); err == nil {
		t.Fatal("dir 空=写必须显式报错而非静默成功")
	}
}

func TestAgentCoreMemoryCapLRU(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	for i := 1; i <= agentCoreMemoryCap; i++ {
		if _, err := agentCoreMemory.addManual("/p/cap", fmt.Sprintf("条目%03d", i)); err != nil {
			t.Fatalf("第 %d 条必须放行: %v", i, err)
		}
	}
	// 注入首条制造「最旧」,再写一条触发 LRU
	agentCoreMemory.touch("/p/cap", []int{agentCoreMemoryCap}) // id=100 变最新
	_, _ = agentCoreMemory.addManual("/p/cap", "新来的")
	got := agentCoreMemory.list("/p/cap")
	if len(got) != agentCoreMemoryCap {
		t.Fatalf("满员仍应 %d 条: %d", agentCoreMemoryCap, len(got))
	}
	for _, e := range got {
		if e.ID == 1 { // touch 把 100 顶新,id=1(未命中最旧)必被淘汰
			t.Fatal("id=1 应已被 LRU 淘汰")
		}
	}
}

func TestAgentCoreMemoryConcurrentWrite(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, _ = agentCoreMemory.addManual("/p/cc", fmt.Sprintf("并发%d", n))
		}(i)
	}
	wg.Wait()
	if got := agentCoreMemory.list("/p/cc"); len(got) != 8 {
		t.Fatalf("并发 8 写应全落: %d", len(got))
	}
}

func TestAgentCoreMemoryProjects(t *testing.T) {
	dir := swapAgentCoreMemoryDirForTest(t)
	if _, err := agentCoreMemory.addManual("/p/alpha", "甲"); err != nil {
		t.Fatalf("写甲: %v", err)
	}
	if _, err := agentCoreMemory.addManual("/p/beta", "乙"); err != nil {
		t.Fatalf("写乙: %v", err)
	}
	got := agentCoreMemory.projects()
	if len(got) != 2 || got[0].Project != "/p/alpha" || got[0].Count != 1 ||
		got[1].Project != "/p/beta" || got[1].Count != 1 {
		t.Fatalf("projects 应两项目按路径有序: %+v", got)
	}
	// 坏 JSON 文件必须被跳过而非拖垮整表
	_ = os.WriteFile(filepath.Join(dir, "zz-broken.json"), []byte("{not-json"), 0o644)
	if got := agentCoreMemory.projects(); len(got) != 2 {
		t.Fatalf("坏文件跳过后仍应 2 项目: %+v", got)
	}
}

func TestAgentCoreMemoryNextIDLift(t *testing.T) {
	dir := swapAgentCoreMemoryDirForTest(t)
	// 手改文件:memories 有 id=1、3,但 next_id=1(≤ 最大 id)——归一必须抬
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "_p_n.json"), []byte(
		`{"version":1,"project":"/p/n","next_id":1,"memories":[
		{"id":1,"content":"a","source":"extract"},
		{"id":3,"content":"b","source":"extract"}]}`), 0o644)
	_ = agentCoreMemory.list("/p/n") // 触发一次 load 归一
	e, err := agentCoreMemory.addManual("/p/n", "新条目")
	if err != nil {
		t.Fatalf("追加失败: %v", err)
	}
	if e.ID <= 3 {
		t.Fatalf("next_id 必须抬到 maxSeen+1=4,拿到 %d", e.ID)
	}
	// 再 load 一轮:新条目绝不能被去重当重复静默丢弃
	got := agentCoreMemory.list("/p/n")
	if len(got) != 3 {
		t.Fatalf("新条目不得被去重丢弃,应 3 条: %+v", got)
	}
}
