package services

// 格式契约由写读往返（fixture 用真实写侧）+ 逐字段断言守护，不做黄金行
// 精确锁定——写读同仓同发布，键漂移会静默双端一致，属有意取舍；若未来
// 跨进程消费本格式，先补黄金行测试。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readJournalLines(t *testing.T, dir, sessionID string) []map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, agentCoreJournalFileName(sessionID)))
	if err != nil {
		t.Fatalf("读取 journal: %v", err)
	}
	var out []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var row map[string]interface{}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("行解析失败 %q: %v", line, err)
		}
		out = append(out, row)
	}
	return out
}

func TestAgentCoreJournalHeaderWrittenOnce(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.ensureSessionHeader("conv-1", "/tmp/proj", "glm-5.2", "2026-10-26T00:00:00Z")
	j.ensureSessionHeader("conv-1", "/tmp/proj", "glm-5.2", "2026-10-26T01:00:00Z")
	lines := readJournalLines(t, dir, "conv-1")
	if len(lines) != 1 || lines[0]["kind"] != "session" {
		t.Fatalf("lines = %v, want 恰好 1 条 session 头行", lines)
	}
	if lines[0]["project_path"] != "/tmp/proj" || lines[0]["model"] != "glm-5.2" {
		t.Errorf("头行字段 = %v", lines[0])
	}
	// 首次写生效：第二次 ensureSessionHeader 传了 01:00，文件里必须仍是 00:00，
	// 防止实现退化成无条件覆盖（会截断追加历史）。
	if lines[0]["created_at"] != "2026-10-26T00:00:00Z" {
		t.Errorf("created_at = %v, want 首次的 2026-10-26T00:00:00Z", lines[0]["created_at"])
	}
}

func TestAgentCoreJournalAppendOrderAndDedup(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.ensureSessionHeader("conv-1", "/p", "m", "2026-10-26T00:00:00Z")
	j.appendUser("conv-1", "msg-1", "你好", "2026-10-26T00:00:01Z")
	j.appendUser("conv-1", "msg-1", "你好", "2026-10-26T00:00:01Z") // 去重
	j.appendAssistant("conv-1", "msg-1-assistant", "你好，世界", "2026-10-26T00:00:02Z")
	j.appendEnd("conv-1", "success", "")
	lines := readJournalLines(t, dir, "conv-1")
	if len(lines) != 4 {
		t.Fatalf("lines = %d, want 4（重复 user 被去重）: %v", len(lines), lines)
	}
	wantKinds := []interface{}{"session", "msg", "msg", "end"}
	for i, kind := range wantKinds {
		if lines[i]["kind"] != kind {
			t.Errorf("lines[%d].kind = %v, want %v", i, lines[i]["kind"], kind)
		}
	}
	if lines[1]["role"] != "user" || lines[1]["text"] != "你好" || lines[1]["message_id"] != "msg-1" {
		t.Errorf("user 行 = %v", lines[1])
	}
	if lines[2]["role"] != "assistant" || lines[2]["text"] != "你好，世界" {
		t.Errorf("assistant 行 = %v", lines[2])
	}
	if lines[3]["status"] != "success" {
		t.Errorf("end 行 = %v", lines[3])
	}
}

func TestAgentCoreJournalFileNameSanitized(t *testing.T) {
	got := agentCoreJournalFileName(`a/b:c..d`)
	if got != "a_b_c..d.jsonl" {
		t.Fatalf("文件名 = %q, want a_b_c..d.jsonl", got)
	}
	if agentCoreJournalFileName("conv-1") != "conv-1.jsonl" {
		t.Fatal("正常 id 不应被改写")
	}
	long := strings.Repeat("x", 200)
	if len(agentCoreJournalFileName(long)) > 130 {
		t.Fatal("超长 id 未截断")
	}
}

func TestAgentCoreJournalNilSafeAndNoDir(t *testing.T) {
	var j *agentCoreJournal
	j.ensureSessionHeader("x", "/p", "m", "now") // 不 panic
	j.appendUser("x", "m1", "t", "now")
	j.appendEnd("x", "success", "")
	empty := newAgentCoreJournal("") // dir 为空 = 禁用
	empty.appendUser("x", "m1", "t", "now")
}

func TestAgentCoreManagerJournalSteerUserMessage(t *testing.T) {
	setTestAgentHome(t)
	m := newAgentAIManager()
	created := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	m.journalSteerUserMessage("conv-steer", agentAISteerMessage{
		MessageID: "steer-1",
		Content:   "先停一下",
		CreatedAt: created,
	})
	m.journalSteerUserMessage("conv-steer", agentAISteerMessage{
		MessageID: "steer-1",
		Content:   "先停一下",
		CreatedAt: created,
	})
	m.journalSteerUserMessage("conv-steer", agentAISteerMessage{
		MessageID: "steer-2",
		Content:   "再看看日志",
		CreatedAt: created,
	})
	lines := readJournalLines(t, agentCoreJournalDir(), "conv-steer")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2（同 id 去重）: %v", len(lines), lines)
	}
	if lines[0]["role"] != "user" || lines[0]["text"] != "先停一下" {
		t.Errorf("第一条 = %v", lines[0])
	}
	if lines[1]["text"] != "再看看日志" {
		t.Errorf("第二条 = %v", lines[1])
	}
	if lines[0]["ts"] != "2026-10-26T12:00:00Z" {
		t.Errorf("ts = %v, want 消息自身 CreatedAt", lines[0]["ts"])
	}
}

func TestAgentCoreJournalAppendTool(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.ensureSessionHeader("conv-tool", "/tmp/p", "glm-5.2", "2026-10-26T01:00:00Z")
	j.appendTool("conv-tool", "mcp__s__lookup", "q=hi", false)
	j.appendTool("conv-tool", "mcp__s__lookup", "q=hi", false) // 完全相同实参:行为级证明 tool 行不去重
	rows := readJournalLines(t, dir, "conv-tool")
	if len(rows) != 3 { // 头 + 2 tool 行（相同实参仍两行、无 message_id）
		t.Fatalf("行数 = %d, want 3: %v", len(rows), rows)
	}
	if rows[1]["kind"] != "tool" || rows[1]["tool"] != "mcp__s__lookup" || rows[1]["is_error"] != false {
		t.Fatalf("tool 行错误: %v", rows[1])
	}
	if rows[2]["kind"] != "tool" || rows[2]["summary"] != "q=hi" || rows[2]["is_error"] != false {
		t.Fatalf("相同实参的第二次调用应独立落第二行(不去重): %v", rows[2])
	}
	// nil 接收者与空 sessionID 安全（与 appendMsg 家族守卫同款）。
	var nilJ *agentCoreJournal
	nilJ.appendTool("conv-x", "mcp__s__lookup", "q", false)
	j.appendTool("", "mcp__s__lookup", "q", false)
}

func TestAgentCoreJournalPruneKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	for i := 0; i < agentCoreJournalMaxFiles+5; i++ {
		j.ensureSessionHeader(fmt.Sprintf("conv-%03d", i), "/p", "m", "2026-10-26T00:00:00Z")
		time.Sleep(2 * time.Millisecond) // mtime 可区分
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != agentCoreJournalMaxFiles {
		t.Fatalf("文件数 = %d, want %d", len(entries), agentCoreJournalMaxFiles)
	}
	if _, err := os.Stat(filepath.Join(dir, "conv-000.jsonl")); !os.IsNotExist(err) {
		t.Fatal("最旧的 conv-000 应被 prune")
	}
	if _, err := os.Stat(filepath.Join(dir, agentCoreJournalFileName(fmt.Sprintf("conv-%03d", agentCoreJournalMaxFiles+4)))); err != nil {
		t.Fatal("最新的会话文件不应被 prune")
	}
}
