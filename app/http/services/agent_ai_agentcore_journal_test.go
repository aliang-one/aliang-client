package services

// 格式契约由写读往返（fixture 用真实写侧）+ 逐字段断言守护，不做黄金行
// 精确锁定——写读同仓同发布，键漂移会静默双端一致，属有意取舍；若未来
// 跨进程消费本格式，先补黄金行测试。

import (
	"bytes"
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
	j.ensureSessionHeader("conv-1", "/tmp/proj", "glm-5.2", "", "2026-10-26T00:00:00Z", "phone")
	j.ensureSessionHeader("conv-1", "/tmp/proj", "glm-5.2", "", "2026-10-26T01:00:00Z", "phone")
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
	j.ensureSessionHeader("conv-1", "/p", "m", "", "2026-10-26T00:00:00Z", "phone")
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

// 写侧残行防护（审计 ③）：上一进程死在写一半 → 文件尾无换行。若 O_APPEND
// 直接追加，新行会粘在残行尾巴上双双损坏。断言：补写封口后新行仍是完整
// 可解析的 JSON，残行片段被隔离在单独一行（reader 侧按解析失败跳过）。
func TestAgentCoreJournalRepairsTornTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, agentCoreJournalFileName("conv-torn"))
	torn := `{"kind":"session","created_at":"2026-10-26T00:00:00Z"}` + "\n" + `{"kind":"msg","role":"assi`
	if err := os.WriteFile(path, []byte(torn), 0o600); err != nil {
		t.Fatalf("写残行 fixture: %v", err)
	}
	j := newAgentCoreJournal(dir)
	j.appendUser("conv-torn", "msg-1", "你好", "2026-10-26T00:00:01Z")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 journal: %v", err)
	}
	// 宽松解析（模拟 reader 的半行容忍）：坏行跳过，好行必须完好。
	var parsed []map[string]interface{}
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		var row map[string]interface{}
		if json.Unmarshal([]byte(line), &row) == nil {
			parsed = append(parsed, row)
		}
	}
	if len(parsed) != 2 || parsed[0]["kind"] != "session" {
		t.Fatalf("可解析行 = %v, want [session, 新 user 行]", parsed)
	}
	newRow := parsed[len(parsed)-1]
	if newRow["role"] != "user" || newRow["text"] != "你好" || newRow["message_id"] != "msg-1" {
		t.Fatalf("新行被残行污染: %v", newRow)
	}
	// 文件尾必须是换行（下一次追加仍安全，不依赖本次修补的偶然性）。
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		t.Fatalf("文件尾无换行: %q", raw)
	}
}

// 正常路径不引入多余空行：写侧只在残行时补封口。
func TestAgentCoreJournalNoExtraNewlinesOnHealthyTail(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.ensureSessionHeader("conv-ok", "/p", "m", "", "2026-10-26T00:00:00Z", "phone")
	j.appendUser("conv-ok", "msg-1", "你好", "2026-10-26T00:00:01Z")
	j.appendEnd("conv-ok", "success", "")
	lines := readJournalLines(t, dir, "conv-ok") // 空行会被跳过但 JSON 必须全部可解析
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3: %v", len(lines), lines)
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
	j.ensureSessionHeader("x", "/p", "m", "", "now", "phone") // 不 panic
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
	}, "先停一下")
	m.journalSteerUserMessage("conv-steer", agentAISteerMessage{
		MessageID: "steer-1",
		Content:   "先停一下",
		CreatedAt: created,
	}, "先停一下")
	// 投递内容带附件后缀/手机契约包装时，journal 仍须落用户原文（spec §5）：
	// steer.Content 是拼接产物，不是真实用户 content。
	delivered := wrapAgentAIPhoneContract("再看看日志\n\n[附件] log.txt")
	m.journalSteerUserMessage("conv-steer", agentAISteerMessage{
		MessageID: "steer-2",
		Content:   delivered,
		CreatedAt: created,
	}, "再看看日志")
	lines := readJournalLines(t, agentCoreJournalDir(), "conv-steer")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2（同 id 去重）: %v", len(lines), lines)
	}
	if lines[0]["role"] != "user" || lines[0]["text"] != "先停一下" {
		t.Errorf("第一条 = %v", lines[0])
	}
	if lines[1]["text"] != "再看看日志" {
		t.Errorf("第二条应落原文而非投递拼接产物: %v", lines[1])
	}
	if lines[0]["ts"] != "2026-10-26T12:00:00Z" {
		t.Errorf("ts = %v, want 消息自身 CreatedAt", lines[0]["ts"])
	}
}

func TestAgentCoreJournalAppendTool(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.ensureSessionHeader("conv-tool", "/tmp/p", "glm-5.2", "", "2026-10-26T01:00:00Z", "phone")
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
		j.ensureSessionHeader(fmt.Sprintf("conv-%03d", i), "/p", "m", "", "2026-10-26T00:00:00Z", "phone")
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

// steer 先于 run 到达的历史竞态（spec §4.3）：文件由 appendUser 先建、无头行，
// 后续 tool 轨迹会被读侧丢弃。ensureSessionHeader 必须补头且保留原内容、幂等。
func TestEnsureSessionHeaderBackfillsHeadlessFile(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	// 模拟 steer 先于 run：文件已存在但无头行
	path := filepath.Join(dir, agentCoreJournalFileName("s-headless"))
	os.WriteFile(path, []byte(`{"kind":"msg","role":"user","message_id":"m1","text":"hi","ts":"2026-10-28T00:00:00Z"}`+"\n"), 0o600)

	j.ensureSessionHeader("s-headless", "/proj", "glm-5.3", "", "2026-10-28T01:00:00Z", "dashboard")

	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var head map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &head); err != nil {
		t.Fatalf("首行非法: %v", err)
	}
	if head["kind"] != "session" {
		t.Fatalf("首行应补为 session 头,实际 %v", head["kind"])
	}
	if head["origin"] != "dashboard" {
		t.Fatalf("origin 未落: %v", head["origin"])
	}
	if len(lines) != 2 {
		t.Fatalf("原内容应保留,实际 %d 行", len(lines))
	}
	// 幂等：再调一次不重复
	j.ensureSessionHeader("s-headless", "/proj", "glm-5.3", "", "2026-10-28T01:00:00Z", "dashboard")
	data2, _ := os.ReadFile(path)
	if len(strings.Split(strings.TrimSpace(string(data2)), "\n")) != 2 {
		t.Fatal("幂等补写被破坏")
	}
}

func TestEnsureSessionHeaderOriginPhone(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.ensureSessionHeader("s-origin", "/proj", "m", "", "2026-10-28T00:00:00Z", "phone")
	data, _ := os.ReadFile(filepath.Join(dir, agentCoreJournalFileName("s-origin")))
	var head map[string]interface{}
	_ = json.Unmarshal(bytes.Split(data, []byte("\n"))[0], &head)
	if head["origin"] != "phone" {
		t.Fatalf("origin=phone 未落: %v", head)
	}
}

func TestAgentCoreJournalOriginFor(t *testing.T) {
	if got := agentCoreJournalOriginFor("remote", ""); got != "phone" {
		t.Fatalf("remote 应推 phone,实际 %q", got)
	}
	if got := agentCoreJournalOriginFor("aisock-3", ""); got != "dashboard" {
		t.Fatalf("aisock-* 应推 dashboard,实际 %q", got)
	}
	if got := agentCoreJournalOriginFor("", "phone"); got != "phone" {
		t.Fatalf("surface 兜底 phone,实际 %q", got)
	}
	if got := agentCoreJournalOriginFor("", ""); got != "unknown" {
		t.Fatalf("双空应 unknown,实际 %q", got)
	}
}
