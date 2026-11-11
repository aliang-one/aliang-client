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
	j.ensureSessionHeader("conv-1", "/tmp/proj", "glm-5.2", "", "2026-10-26T00:00:00Z", "phone", nil, nil, "")
	j.ensureSessionHeader("conv-1", "/tmp/proj", "glm-5.2", "", "2026-10-26T01:00:00Z", "phone", nil, nil, "")
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
	j.ensureSessionHeader("conv-1", "/p", "m", "", "2026-10-26T00:00:00Z", "phone", nil, nil, "")
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
	j.ensureSessionHeader("conv-ok", "/p", "m", "", "2026-10-26T00:00:00Z", "phone", nil, nil, "")
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
	j.ensureSessionHeader("x", "/p", "m", "", "now", "phone", nil, nil, "") // 不 panic
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
	j.ensureSessionHeader("conv-tool", "/tmp/p", "glm-5.2", "", "2026-10-26T01:00:00Z", "phone", nil, nil, "")
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
		j.ensureSessionHeader(fmt.Sprintf("conv-%03d", i), "/p", "m", "", "2026-10-26T00:00:00Z", "phone", nil, nil, "")
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

	j.ensureSessionHeader("s-headless", "/proj", "glm-5.3", "", "2026-10-28T01:00:00Z", "dashboard", nil, nil, "")

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
	j.ensureSessionHeader("s-headless", "/proj", "glm-5.3", "", "2026-10-28T01:00:00Z", "dashboard", nil, nil, "")
	data2, _ := os.ReadFile(path)
	if len(strings.Split(strings.TrimSpace(string(data2)), "\n")) != 2 {
		t.Fatal("幂等补写被破坏")
	}
}

func TestEnsureSessionHeaderOriginPhone(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.ensureSessionHeader("s-origin", "/proj", "m", "", "2026-10-28T00:00:00Z", "phone", nil, nil, "")
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

// 辅助:写一份标准 journal(头行 + msg(u1) + turn + msg(a1) + end + msg(u2-目标) + turn + msg(a2) + end)
func writeTruncateFixture(t *testing.T, j *agentCoreJournal, sessionID string) {
	t.Helper()
	j.ensureSessionHeader(sessionID, "/proj", "glm-5.3", "", "2026-10-31T00:00:00Z", "dashboard", nil, nil, "")
	j.appendUser(sessionID, "u1", "第一问", "2026-10-31T00:01:00Z")
	j.appendAssistantTurn(sessionID, 1, "中间叙述", "", "end_turn", nil)
	j.appendAssistant(sessionID, "a1", "第一答", "2026-10-31T00:02:00Z")
	j.appendEnd(sessionID, "success", "")
	j.appendUser(sessionID, "u2", "第二问", "2026-10-31T00:03:00Z")
	j.appendAssistantTurn(sessionID, 2, "叙述二", "", "end_turn", nil)
	j.appendAssistant(sessionID, "a2", "第二答", "2026-10-31T00:04:00Z")
	j.appendEnd(sessionID, "success", "")
}

func TestJournalTruncateBeforeKeepsPrefixWithAttachedRows(t *testing.T) {
	j := newAgentCoreJournal(t.TempDir())
	const sid = "s-trunc-1"
	writeTruncateFixture(t, j, sid)

	applied, err := j.truncateBefore(sid, "u2")
	if err != nil || !applied {
		t.Fatalf("truncate: applied=%v err=%v", applied, err)
	}
	hist, _, err := readAgentCoreJournalHistoryFrom(j.dir, sid)
	if err != nil {
		t.Fatal(err)
	}
	// 保留区间 = u1 + 中间叙述(turn 行,2026-11-05 读侧补 turn 支持)+ a1(a1 是
	// u1 区间宿主行);u2 之后全部消失。旧断言期望 2 条是 turn 支持合入前的
	// 陈旧语义（预存失败 dc2bfa0 基线），本批次修 reader 时一并对齐。
	if len(hist) != 3 || hist[0].MessageID != "u1" || hist[1].Content != "中间叙述" || hist[2].MessageID != "a1" || hist[2].Content != "第一答" {
		t.Fatalf("unexpected history: %+v", hist)
	}
}

func TestJournalTruncateBeforeIsIdempotent(t *testing.T) {
	j := newAgentCoreJournal(t.TempDir())
	const sid = "s-trunc-2"
	writeTruncateFixture(t, j, sid)
	if _, err := j.truncateBefore(sid, "u2"); err != nil {
		t.Fatal(err)
	}
	applied, err := j.truncateBefore(sid, "u2") // 已截过:目标行不在,幂等成功
	if err != nil || applied {
		t.Fatalf("second truncate should be no-op: applied=%v err=%v", applied, err)
	}
}

func TestJournalTruncateBeforeMissingTargetAndFile(t *testing.T) {
	j := newAgentCoreJournal(t.TempDir())
	const sid = "s-trunc-3"
	writeTruncateFixture(t, j, sid)
	// 不匹配的 id:无操作不报错
	if applied, err := j.truncateBefore(sid, "nope"); err != nil || applied {
		t.Fatalf("missing target: applied=%v err=%v", applied, err)
	}
	// 文件缺失:幂等成功
	if applied, err := j.truncateBefore("ghost", "u1"); err != nil || applied {
		t.Fatalf("missing file: applied=%v err=%v", applied, err)
	}
}

func TestJournalTruncateBeforeKeepsHeadWhenFirstMsgIsTarget(t *testing.T) {
	j := newAgentCoreJournal(t.TempDir())
	const sid = "s-trunc-4"
	writeTruncateFixture(t, j, sid)
	if _, err := j.truncateBefore(sid, "u1"); err != nil {
		t.Fatal(err)
	}
	hist, head, err := readAgentCoreJournalHistoryFrom(j.dir, sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 0 {
		t.Fatalf("expect empty history, got %+v", hist)
	}
	if head.projectPath != "/proj" {
		t.Fatalf("head lost: %+v", head)
	}
}

func TestJournalTruncateBeforeSteerRowFollowsPosition(t *testing.T) {
	j := newAgentCoreJournal(t.TempDir())
	const sid = "s-trunc-5"
	// 手工排布(fixture 的行序固定,无法把 steer 行插进 u1 区间):steer 是
	// 带 message_id 的 kind=msg 行,物理位置在 u1 区间内(a1 之后、u2 之前)。
	j.ensureSessionHeader(sid, "/proj", "glm-5.3", "", "2026-10-31T00:00:00Z", "dashboard", nil, nil, "")
	j.appendUser(sid, "u1", "第一问", "ts")
	j.appendAssistant(sid, "a1", "第一答", "ts")
	j.appendUser(sid, "steer-1", "插话", "ts")
	j.appendUser(sid, "u2", "第二问", "ts")
	j.appendAssistant(sid, "a2", "第二答", "ts")
	if _, err := j.truncateBefore(sid, "u2"); err != nil {
		t.Fatal(err)
	}
	hist, _, _ := readAgentCoreJournalHistoryFrom(j.dir, sid)
	// 保留侧断言(关键):u1/a1/steer 三行按位置归属 u1 区间,全保。
	if len(hist) != 3 || hist[2].MessageID != "steer-1" {
		t.Fatalf("steer row inside kept interval must survive: %+v", hist)
	}
}

// 无头文件（首行即 msg 行，缺 session 头）：truncate 仍按位置语义工作——
// 首行既可作保留前缀，也可作截断目标（不造头，读侧本就容忍无头）。
func TestJournalTruncateBeforeHeadlessFile(t *testing.T) {
	j := newAgentCoreJournal(t.TempDir())
	const sid = "s-trunc-6"
	raw := `{"kind":"msg","role":"user","message_id":"u1","text":"第一问","ts":"ts"}` + "\n" +
		`{"kind":"msg","role":"assistant","message_id":"a1","text":"第一答","ts":"ts"}` + "\n" +
		`{"kind":"msg","role":"user","message_id":"u2","text":"第二问","ts":"ts"}` + "\n"
	if err := os.WriteFile(filepath.Join(j.dir, agentCoreJournalFileName(sid)), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	// 首行作保留前缀:truncate u2 → 保留 u1+a1,结果仍无头。
	if _, err := j.truncateBefore(sid, "u2"); err != nil {
		t.Fatal(err)
	}
	hist, head, err := readAgentCoreJournalHistoryFrom(j.dir, sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 || hist[0].MessageID != "u1" || hist[1].MessageID != "a1" {
		t.Fatalf("headless keep-prefix: %+v", hist)
	}
	if head.projectPath != "" {
		t.Fatalf("headless file must not gain a session header: %+v", head)
	}

	// 首行作截断目标:truncate u1 → 整文件清空（连头都没有,历史为空）。
	if _, err := j.truncateBefore(sid, "u1"); err != nil {
		t.Fatal(err)
	}
	hist, _, _ = readAgentCoreJournalHistoryFrom(j.dir, sid)
	if len(hist) != 0 {
		t.Fatalf("headless first-line-target must empty the file: %+v", hist)
	}
}

// 纯图消息（text 空但带附件）必须整条落 journal——这是手机图在网页显示链的
// 治本断点①（spec 2026-11-05-chat-image-pipeline §3.2）。旧守卫「text 空
// 直接 return」会让纯图消息连一行都不存在。
func TestAgentCoreJournalPureImageRowLands(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.appendUser("s-img", "m-img", "", "2026-11-05T00:00:00Z", []agentAIJournalAttachment{
		{Name: "photo.jpg", Ref: "s-img/m-img-0.jpg", Mime: "image/jpeg", Bytes: 1234, Origin: "phone"},
	})
	lines := readJournalLines(t, dir, "s-img")
	var msgRows []map[string]interface{}
	for _, row := range lines {
		if row["kind"] == "msg" {
			msgRows = append(msgRows, row)
		}
	}
	if len(msgRows) != 1 {
		t.Fatalf("pure-image row must land: %d msg rows", len(msgRows))
	}
	row := msgRows[0]
	if row["text"] != "" {
		t.Fatalf("text = %v, want empty", row["text"])
	}
	atts, ok := row["attachments"].([]interface{})
	if !ok || len(atts) != 1 {
		t.Fatalf("attachments = %v, want 1 entry", row["attachments"])
	}
	entry, _ := atts[0].(map[string]interface{})
	if entry["ref"] != "s-img/m-img-0.jpg" || entry["origin"] != "phone" {
		t.Fatalf("attachment entry = %v", entry)
	}
}

// 空文本且无附件的行依旧不落（旧守卫语义保留——防止空行垃圾）。
func TestAgentCoreJournalEmptyRowStillDropped(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.appendUser("s-empty", "m-empty", "", "2026-11-05T00:00:00Z")
	// 无行=连会话文件都不应创建；存在则必须 0 行（两种形态都算「没落」）。
	if _, err := os.Stat(filepath.Join(dir, agentCoreJournalFileName("s-empty"))); err == nil {
		if lines := readJournalLines(t, dir, "s-empty"); len(lines) != 0 {
			t.Fatalf("empty text+no-attachment row must not land: %v", lines)
		}
	}
}

// 带字+附件的行：text 与 attachments 同落。
func TestAgentCoreJournalTextWithAttachments(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.appendUser("s-both", "m-both", "看这张图", "2026-11-05T00:00:00Z", []agentAIJournalAttachment{
		{Name: "shot.png", Ref: "s-both/m-both-0.png", Mime: "image/png", Origin: "web"},
	})
	lines := readJournalLines(t, dir, "s-both")
	var msgRows []map[string]interface{}
	for _, row := range lines {
		if row["kind"] == "msg" {
			msgRows = append(msgRows, row)
		}
	}
	if len(msgRows) != 1 {
		t.Fatalf("msg rows = %d", len(msgRows))
	}
	if msgRows[0]["text"] != "看这张图" {
		t.Fatalf("text = %v", msgRows[0]["text"])
	}
	atts, _ := msgRows[0]["attachments"].([]interface{})
	if len(atts) != 1 || atts[0].(map[string]interface{})["origin"] != "web" {
		t.Fatalf("attachments = %v", msgRows[0]["attachments"])
	}
}

// journal history 读侧透传：纯图行带 attachments 进 hist（Content 空）——
// 网页 detail/快照据此渲染缩略图（spec §3.3）。
func TestJournalHistoryPureImageRowKeepsAttachments(t *testing.T) {
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.appendUser("s-hist-img", "m-h1", "", "2026-11-05T00:00:00Z", []agentAIJournalAttachment{
		{Name: "photo.jpg", Ref: "s-hist-img/m-h1-0.jpg", Mime: "image/jpeg", Bytes: 42, Origin: "phone"},
	})
	hist, _, err := readAgentCoreJournalHistoryFrom(dir, "s-hist-img")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 {
		t.Fatalf("hist = %+v, want 1", hist)
	}
	if hist[0].Content != "" {
		t.Fatalf("content = %q, want empty", hist[0].Content)
	}
	if len(hist[0].Attachments) != 1 || hist[0].Attachments[0].Ref != "s-hist-img/m-h1-0.jpg" || hist[0].Attachments[0].Name != "photo.jpg" {
		t.Fatalf("attachments = %+v", hist[0].Attachments)
	}
}

// 子 agent 三行写侧契约（subagent-panel spec）：start/finish 行 kind=subagent
// 带 task_id，tool 关联行沿用 kind=tool 但带 subagent_task_id——读侧按 task_id
// 聚合轨迹。taskID 含回合 messageID 消歧（spec H1）。
func TestAppendSubagentLines(t *testing.T) {
	j := newAgentCoreJournal(t.TempDir())
	sid := "sess-sub"
	taskID := "agentcore-msg7-researcher-1" // 含回合 messageID 消歧(spec H1)
	j.appendSubagentStart(sid, taskID, "researcher", "加密调研", "调研 XXX 的加密方案")
	j.appendSubagentTool(sid, taskID, "Grep", "grep 摘要", false)
	j.appendSubagentFinish(sid, taskID, "completed", "结论摘要",
		"完整报告全文", 8123, 45230)

	lines := readJournalLines(t, j.dir, sid)
	var start, tool, finish map[string]interface{}
	for _, m := range lines {
		switch m["kind"] {
		case "subagent":
			if m["event"] == "start" {
				start = m
			} else {
				finish = m
			}
		case "tool":
			if m["subagent_task_id"] == taskID {
				tool = m
			}
		}
	}
	if start == nil || start["name"] != "researcher" || start["description"] != "加密调研" || start["task_id"] != taskID {
		t.Fatalf("start 行缺失/字段错: %v", start)
	}
	if start["task"] != "调研 XXX 的加密方案" {
		t.Fatalf("start 行 task 字段缺失/错: %v", start["task"])
	}
	if tool == nil || tool["tool"] != "Grep" || tool["is_error"] != false || tool["summary"] != "grep 摘要" {
		t.Fatalf("tool 关联行缺失/字段错: %v", tool)
	}
	if finish == nil || finish["status"] != "completed" || finish["result"] != "完整报告全文" ||
		finish["summary"] != "结论摘要" || finish["total_tokens"] != float64(8123) || finish["duration_ms"] != float64(45230) || finish["task_id"] != taskID {
		t.Fatalf("finish 行缺失/字段错: %v", finish)
	}
}

func TestJournalUnfinishedSubagents(t *testing.T) {
	// 中断注入数据面（feat/subagent-interrupted-injection）：start 无 finish
	// 的子 agent 返回对账摘要（工具计数+最后工具+task 文本）；有 finish 的
	// 排除；行序=时间序。
	j := newAgentCoreJournal(t.TempDir())
	sid := "sess-unfinished"
	j.appendSubagentStart(sid, "t-done", "researcher", "调研", "任务A")
	j.appendSubagentFinish(sid, "t-done", "completed", "结论", "全文", 10, 5)
	j.appendSubagentStart(sid, "t-dead", "reviewer", "评审", "任务B")
	j.appendSubagentTool(sid, "t-dead", "Grep", "s1", false)
	j.appendSubagentTool(sid, "t-dead", "Read", "s2", false)

	got := j.unfinishedSubagents(sid)
	if len(got) != 1 {
		t.Fatalf("未终态数 = %d, want 1: %+v", len(got), got)
	}
	e := got[0]
	if e.TaskID != "t-dead" || e.Name != "reviewer" || e.Task != "任务B" ||
		e.ToolCalls != 2 || e.LastTool != "Read" {
		t.Fatalf("对账摘要字段错: %+v", e)
	}

	// 无该会话文件 → 空。
	if got := j.unfinishedSubagents("sess-none"); got != nil {
		t.Fatalf("无会话应返回 nil: %+v", got)
	}
}

func TestAppendSubagentTurnSidechain(t *testing.T) {
	// L2 子 run sidechain（2026-11-09）：子 run 的 LLM 往来此前只在内存——
	// 进程一死，跑了半小时的调研成果全丢（journal 只剩工具名列表）。落成
	// kind=subagent_turn 行后崩溃可查、为 L3 续跑铺路。行内带 task_id 关联，
	// text rune 安全截断。
	j := newAgentCoreJournal(t.TempDir())
	sid := "sess-sidechain"
	taskID := "agentcore-m1-researcher-1"
	j.appendSubagentTurn(sid, taskID, 1, "第一轮：我先看看目录", "", 100)
	j.appendSubagentTurn(sid, taskID, 2, "第二轮：找到线索了", "思考链", 250)

	lines := readJournalLines(t, j.dir, sid)
	turns := make([]map[string]interface{}, 0, 2)
	for _, m := range lines {
		if m["kind"] == "subagent_turn" {
			turns = append(turns, m)
		}
	}
	if len(turns) != 2 {
		t.Fatalf("subagent_turn 行数 = %d, want 2: %v", len(turns), lines)
	}
	if turns[0]["task_id"] != taskID || turns[0]["turn"] != float64(1) ||
		turns[0]["text"] != "第一轮：我先看看目录" {
		t.Fatalf("首轮字段错: %v", turns[0])
	}
	if turns[1]["thinking"] != "思考链" {
		t.Fatalf("thinking 丢失: %v", turns[1])
	}
	if turns[1]["tokens"] != float64(250) {
		t.Fatalf("tokens 丢失: %v", turns[1])
	}
}

func TestUnfinishedSubagentsReportsLastText(t *testing.T) {
	// 中断注入增强（L2 联动）：有 sidechain 时应带出子 agent 最后一轮说的话，
	// 主 LLM 重派时能接上下文，而不是只知道「跑了 3 个工具」。
	j := newAgentCoreJournal(t.TempDir())
	sid := "sess-last"
	j.appendSubagentStart(sid, "t1", "researcher", "调研", "查加密方案")
	j.appendSubagentTurn(sid, "t1", 1, "先读配置文件", "", 50)
	j.appendSubagentTurn(sid, "t1", 2, "结论草稿：AES-GCM 更合适", "", 80)

	got := j.unfinishedSubagents(sid)
	if len(got) != 1 {
		t.Fatalf("未终态数 = %d", len(got))
	}
	if !strings.Contains(got[0].LastText, "AES-GCM 更合适") {
		t.Fatalf("LastText 未带出最后一轮文本: %q", got[0].LastText)
	}
}

func TestJournalUnfinishedSubagentsIncremental(t *testing.T) {
	// 审计修复（2026-11-10 ④）：装配扫描增量化——同一游标跨调用消化追加行，
	// 结果与全量扫描等价；残尾（半行）先忽略、补全后识别；游标偏移推进精确。
	j := newAgentCoreJournal(t.TempDir())
	sid := "sess-incr"

	j.appendSubagentStart(sid, "t1", "researcher", "调研", "任务A")
	if got := j.unfinishedSubagents(sid); len(got) != 1 || got[0].TaskID != "t1" {
		t.Fatalf("首轮未终态 = %+v", got)
	}

	// 追加后第二轮：t1 已终态 + 新增 t2 → 只报新的。
	j.appendSubagentFinish(sid, "t1", "completed", "结论", "全文", 10, 5)
	j.appendSubagentStart(sid, "t2", "reviewer", "评审", "任务B")
	if got := j.unfinishedSubagents(sid); len(got) != 1 || got[0].TaskID != "t2" {
		t.Fatalf("增量轮未终态 = %+v", got)
	}

	// 残尾：写入半行（无换行，模拟写中断）→ 本轮忽略；补全后半行拼回识别。
	path := filepath.Join(j.dir, agentCoreJournalFileName(sid))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("打开 journal 失败: %v", err)
	}
	f.WriteString(`{"kind":"subagent","event":"start","task_id":"t3","name":"coder","task":"任务C"`)
	f.Close()
	if got := j.unfinishedSubagents(sid); len(got) != 1 || got[0].TaskID != "t2" {
		t.Fatalf("残尾轮应忽略半行 = %+v", got)
	}
	f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("打开 journal 失败: %v", err)
	}
	f.WriteString("}\n")
	f.Close()
	got := j.unfinishedSubagents(sid)
	if len(got) != 2 {
		t.Fatalf("补全后未终态数 = %d, want 2 (t2+t3): %+v", len(got), got)
	}
	// 行序=时间序：t2 先于 t3。
	if got[0].TaskID != "t2" || got[1].TaskID != "t3" {
		t.Fatalf("行序错乱: %+v", got)
	}

	// 外部整文件改写（首行漂移）→ 粗校验命中整份重扫，不残留旧游标的鬼条目。
	if err := os.WriteFile(path, []byte(`{"kind":"session","session_id":"`+sid+`"}`+"\n"), 0o600); err != nil {
		t.Fatalf("重写 journal 失败: %v", err)
	}
	if got := j.unfinishedSubagents(sid); got != nil {
		t.Fatalf("改写后应整份重扫返回空: %+v", got)
	}
}

func TestJournalUnfinishedSubagentsTruncateInvalidates(t *testing.T) {
	// truncateBefore 整文件重写 → 增量游标必须精确失效；失效后重扫结果与
	// 全量扫描等价（不因游标残留漏报/多报）。
	j := newAgentCoreJournal(t.TempDir())
	sid := "sess-trunc"
	j.appendMsg(sid, "user", "u1", "第一问", "2026-11-10T00:00:01Z")
	j.appendMsg(sid, "user", "u2", "第二问", "2026-11-10T00:00:02Z")
	j.appendSubagentStart(sid, "t1", "researcher", "调研", "任务A")
	if got := j.unfinishedSubagents(sid); len(got) != 1 || got[0].TaskID != "t1" {
		t.Fatalf("截断前未终态 = %+v", got)
	}
	applied, err := j.truncateBefore(sid, "u2")
	if err != nil || !applied {
		t.Fatalf("truncateBefore = (%v, %v)", applied, err)
	}
	// truncateBefore 保留 u2 之前（=u1），u2 及其后的 t1 start 行已随重写
	// 丢弃。失效后整份重扫必须返回空——若游标残留，旧游标越过新文件末尾
	// 什么都读不到、却会把内存里的旧 entries 当结果吐出（=鬼条目 t1）。
	if got := j.unfinishedSubagents(sid); got != nil {
		t.Fatalf("截断后应整份重扫返回空（不得残留旧游标鬼条目）: %+v", got)
	}
	// 失效后的新一轮游标继续增量：追加 t2 → 只报 t2。
	j.appendSubagentStart(sid, "t2", "reviewer", "评审", "任务B")
	if got := j.unfinishedSubagents(sid); len(got) != 1 || got[0].TaskID != "t2" {
		t.Fatalf("截断后增量轮 = %+v", got)
	}
}

func TestJournalHealInterruptedSubagents(t *testing.T) {
	// 复核修复（2026-11-10 ⑤ CONFIRMED P2）：进程硬崩溃留下 start 无 finish
	// 僵尸行，读侧降级收紧后不再覆盖（lastEndLine < startLine）。装配期对账
	// 补落 interrupted finish 行后：对账列表清空 + journal 有补记行 + 读侧
	// （详情装配）显示 interrupted——面板不再永卡「运行中」。
	j := newAgentCoreJournal(t.TempDir())
	sid := "sess-heal"
	j.appendSubagentStart(sid, "t-zombie", "researcher", "调研", "崩溃前派的活")
	j.appendEnd(sid, "success", "") // 老回合 end 行（收紧后不再误降级僵尸）

	got := j.unfinishedSubagents(sid)
	if len(got) != 1 || got[0].TaskID != "t-zombie" {
		t.Fatalf("补记前应报出僵尸: %+v", got)
	}
	// 先算后补（顺序反了列表恒空）。
	j.healInterruptedSubagents(sid, got)

	// 对账列表闭合：幂等（下次装配不再重复补记）。
	if got := j.unfinishedSubagents(sid); got != nil {
		t.Fatalf("补记后对账应返回空: %+v", got)
	}
	// journal 有补记 finish 行（status=interrupted）。
	var healed map[string]interface{}
	for _, line := range readJournalLines(t, j.dir, sid) {
		if line["kind"] == "subagent" && line["event"] == "finish" && line["task_id"] == "t-zombie" {
			healed = line
		}
	}
	if healed == nil || healed["status"] != "interrupted" {
		t.Fatalf("补记 finish 行缺失/状态错: %v", healed)
	}
	// 详情读侧闭合：手动落头行文件形态（含 start+end+补记 finish）显示 interrupted。
	path := filepath.Join(j.dir, agentCoreJournalFileName(sid))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 journal 失败: %v", err)
	}
	withHeader := `{"kind":"session","session_id":"` + sid + `","project_path":"/tmp/p","created_at":"2026-11-10T00:00:00Z"}` + "\n" + string(data)
	if err := os.WriteFile(path, []byte(withHeader), 0o600); err != nil {
		t.Fatalf("落头行失败: %v", err)
	}
	sess := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{})
	if len(sess.Subagents) != 1 || sess.Subagents[0].Status != "interrupted" {
		t.Fatalf("补记后详情读侧应显示 interrupted: %+v", sess.Subagents)
	}
}
