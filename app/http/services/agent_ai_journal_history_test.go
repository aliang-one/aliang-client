package services

// journal 历史读回解析器测试（原 Task 11 计划交付，前置到订阅快照任务）。
// 格式契约由真实写侧 fixture + 读回断言守护：写读同仓同发布，键漂移会静默
// 双端一致（与 agent_ai_agentcore_journal_test.go 同一取舍）。

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadAgentCoreJournalHistory(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.ensureSessionHeader("s-hist", "/proj", "glm-5.3", "2026-10-28T00:00:00Z")
	j.appendUser("s-hist", "m1", "第一问", "2026-10-28T00:01:00Z")
	j.appendAssistant("s-hist", "a1", "第一答", "2026-10-28T00:02:00Z")
	j.appendTool("s-hist", "Bash", "ls", false) // 工具行：轨迹不是消息，解析器必须跳过
	j.appendUser("s-hist", "m2", "第二问", "2026-10-28T00:03:00Z")
	j.appendEnd("s-hist", "success", "")
	// 残行容忍：上一进程可能死在写一半（写侧只在新写入时补换行封口，残行
	// 本身留在文件里）。模拟手工追加一个无换行的半行 JSON。
	f, err := os.OpenFile(filepath.Join(dir, agentCoreJournalFileName("s-hist")), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("打开 journal: %v", err)
	}
	if _, err := f.WriteString(`{"kind":"msg","role":"user"`); err != nil {
		t.Fatalf("写残行: %v", err)
	}
	f.Close()

	hist, head, err := readAgentCoreJournalHistoryFrom(dir, "s-hist")
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if len(hist) != 3 {
		t.Fatalf("应回 3 条消息(user/a/user),实际 %d: %+v", len(hist), hist)
	}
	if hist[0].Role != "user" || hist[0].Content != "第一问" {
		t.Fatalf("首条错: %+v", hist[0])
	}
	if hist[0].MessageID != "m1" || hist[0].CreatedAt.IsZero() {
		t.Fatalf("首条 message_id/ts 解析错: %+v", hist[0])
	}
	if hist[1].Role != "assistant" || hist[1].Content != "第一答" {
		t.Fatalf("第二条错: %+v", hist[1])
	}
	if hist[2].Content != "第二问" {
		t.Fatalf("第三条错: %+v", hist[2])
	}
	if head.projectPath != "/proj" || head.model != "glm-5.3" {
		t.Fatalf("头信息错: %+v", head)
	}
}

func TestReadAgentCoreJournalHistoryMissing(t *testing.T) {
	// 便捷包装扫默认 journal 根：不存在的会话必须报错（调用方回退
	// lazy-create / 快照降级）。只读探测，会话名 "no-such" 不可能与真机
	// ~/.aliang/ai-sessions 里的真实会话撞名。
	if _, _, err := readAgentCoreJournalHistory("no-such"); err == nil {
		t.Fatal("不存在应报错(调用方回退 lazy-create)")
	}
}

func TestReadAgentCoreJournalHistoryFromMissing(t *testing.T) {
	// From 变体（供快照按 manager 自己的 journal 目录读）：目录为空或文件
	// 缺失一律报错，绝不静默回空历史——空历史与「读不到」语义不同。
	if _, _, err := readAgentCoreJournalHistoryFrom("", "s-x"); err == nil {
		t.Fatal("空目录应报错")
	}
	if _, _, err := readAgentCoreJournalHistoryFrom(t.TempDir(), "s-x"); err == nil {
		t.Fatal("文件缺失应报错")
	}
}
