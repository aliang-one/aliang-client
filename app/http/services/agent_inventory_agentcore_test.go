package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

// writeAgentCoreJournalFixture 用真实 journal 写侧造测试文件（保证格式一致）。
func writeAgentCoreJournalFixture(t *testing.T, dir, sessionID, projectPath string) {
	t.Helper()
	j := newAgentCoreJournal(dir)
	j.ensureSessionHeader(sessionID, projectPath, "glm-5.2", "2026-10-26T08:00:00Z", "phone")
	base := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	j.appendUser(sessionID, "m1", "第一句", base.Format(time.RFC3339))
	j.appendAssistant(sessionID, "m1-assistant", "第一答", base.Add(time.Minute).Format(time.RFC3339))
	j.appendUser(sessionID, "m2", "第二句", base.Add(2*time.Minute).Format(time.RFC3339))
	j.appendAssistant(sessionID, "m2-assistant", "第二答", base.Add(3*time.Minute).Format(time.RFC3339))
	j.appendEnd(sessionID, "success", "")
}

// writeCoreJournalRawFixture 自写原始 JSONL 行（既有 writeAgentCoreJournalFixture
// 内容固定不支持自定义行，这里新建一个小 helper）。
func writeCoreJournalRawFixture(t *testing.T, sessionID string, lines ...string) string {
	t.Helper()
	dir := agentCoreJournalDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir 失败: %v", err)
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("写 fixture 失败: %v", err)
	}
	return path
}

func TestReadAgentCoreSessionMetaToolRows(t *testing.T) {
	setTestAgentHome(t)
	path := writeCoreJournalRawFixture(t, "sess-tools",
		`{"kind":"session","session_id":"sess-tools","project_path":"/tmp/p","model":"m","created_at":"2026-10-26T01:00:00Z"}`,
		`{"kind":"msg","role":"user","message_id":"u1","text":"查一下","ts":"2026-10-26T01:00:05Z"}`,
		`{"kind":"tool","tool":"mcp__s__lookup","summary":"q=hi","is_error":false,"ts":"2026-10-26T01:00:06Z"}`,
		`{"kind":"msg","role":"assistant","message_id":"a1","text":"结果","ts":"2026-10-26T01:00:09Z"}`,
	)
	session := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: agentVibeTranscriptMaxMessages, IncludePageMeta: true})
	if session.MessageCount != 2 || session.DisplayMessageCount == nil || *session.DisplayMessageCount != 2 {
		t.Fatalf("tool 行不得计入对话计数: %d %v", session.MessageCount, session.DisplayMessageCount)
	}
	toolRows := 0
	for _, msg := range session.Transcript {
		if msg.Role == "tool" {
			toolRows++
			if msg.Content != "mcp__s__lookup q=hi" {
				t.Fatalf("tool 行渲染错误: %+v", msg)
			}
		}
	}
	if toolRows != 1 {
		t.Fatalf("Transcript 应含 1 条 tool 行, got %d", toolRows)
	}
	// 全序断言：锁死「单一转录行计数器」Index 口径——user(0) → tool(tool-000001,1)
	// → assistant(2)。若将来退回 MessageCount-1 口径,tool 行与第二条 msg 会
	// 撞出相同 Index,本断言照红。
	if len(session.Transcript) != 3 {
		t.Fatalf("Transcript = %d 行, want 3: %+v", len(session.Transcript), session.Transcript)
	}
	wantOrder := []struct {
		role  string
		id    string
		index int
	}{
		{"user", "u1", 0},
		{"tool", "tool-000001", 1},
		{"assistant", "a1", 2},
	}
	for i, w := range wantOrder {
		msg := session.Transcript[i]
		if msg.Role != w.role || msg.ID != w.id || msg.Index != w.index {
			t.Fatalf("Transcript[%d] = %+v, want role=%s id=%s index=%d", i, msg, w.role, w.id, w.index)
		}
	}
	// tool 行作 before 游标：热窗分页的 NextBeforeMessageID 指向 tool 行（工具
	// 行 ID 与 msg ID 同空间,分页原语对二者一视同仁）,以它为游标重读只剩其
	// 前的 [user]。
	paged := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: 2, IncludePageMeta: true})
	if paged.TranscriptPage == nil || !paged.TranscriptPage.HasMore || paged.TranscriptPage.NextBeforeMessageID != "tool-000001" {
		t.Fatalf("Limit=2 首读分页 = %+v, want HasMore+NextBefore=tool-000001", paged.TranscriptPage)
	}
	if len(paged.Transcript) != 2 || paged.Transcript[0].Role != "tool" || paged.Transcript[1].Role != "assistant" {
		t.Fatalf("Limit=2 首读转录 = %+v, want 尾窗 [tool,assistant]", paged.Transcript)
	}
	before := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: 2, BeforeMessageID: "tool-000001", IncludePageMeta: true})
	if len(before.Transcript) != 1 || before.Transcript[0].Role != "user" {
		t.Fatalf("tool 游标重读 = %+v, want 仅 [user]", before.Transcript)
	}
	if before.TranscriptPage == nil || before.TranscriptPage.HasMore {
		t.Fatalf("tool 游标重读分页 = %+v, want HasMore=false", before.TranscriptPage)
	}
}

func TestReadAgentCoreToolRowContentTruncated(t *testing.T) {
	setTestAgentHome(t)
	long := strings.Repeat("x", agentVibeTranscriptMaxContentRunes+10)
	path := writeCoreJournalRawFixture(t, "sess-tool-long",
		`{"kind":"session","session_id":"sess-tool-long","project_path":"/tmp/p","model":"m","created_at":"2026-10-26T01:00:00Z"}`,
		`{"kind":"tool","tool":"t","summary":"`+long+`","ts":"2026-10-26T01:00:06Z"}`,
	)
	session := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: 24})
	var toolContent string
	for _, msg := range session.Transcript {
		if msg.Role == "tool" {
			toolContent = msg.Content
		}
	}
	if toolContent == "" {
		t.Fatal("tool 行未渲染")
	}
	// reader 不信任写侧的家规:超长 summary 与 msg 行同规截断到 max runes。
	if got := len([]rune(toolContent)); got != agentVibeTranscriptMaxContentRunes {
		t.Fatalf("tool Content = %d runes, want %d", got, agentVibeTranscriptMaxContentRunes)
	}
	if !strings.HasPrefix(toolContent, "t ") {
		t.Fatalf("截断应保留前缀 \"t \": %q...", toolContent[:4])
	}
}

func TestReadAgentCoreSessionMetaSummary(t *testing.T) {
	dir := t.TempDir()
	writeAgentCoreJournalFixture(t, dir, "conv-a", "/tmp/proj-a")
	path := filepath.Join(dir, agentCoreJournalFileName("conv-a"))

	session := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: 24})
	if session.ID != "conv-a" {
		t.Fatalf("ID = %q, want conv-a（裸会话 ID,与活跃覆盖层同空间）", session.ID)
	}
	if session.Provider != "agentcore" || session.Tool != "agentcore" || session.Status != "closed" || session.Mode != "vibe" {
		t.Errorf("摘要字段 = %+v", session)
	}
	if session.ProjectPath != "/tmp/proj-a" || session.Model != "glm-5.2" {
		t.Errorf("project/model = %q/%q", session.ProjectPath, session.Model)
	}
	if session.Title != "第一句" {
		t.Errorf("Title = %q, want 首条 user", session.Title)
	}
	if session.MessageCount != 4 {
		t.Errorf("MessageCount = %d, want 4（end 行不计）", session.MessageCount)
	}
	if session.DisplayMessageCount == nil || *session.DisplayMessageCount != 4 {
		t.Errorf("DisplayMessageCount = %v, want 4", session.DisplayMessageCount)
	}
	if session.UpdatedAt != "2026-10-26T08:03:00Z" {
		t.Errorf("UpdatedAt = %q, want 最后一条真实消息 ts（不是 mtime/end 行）", session.UpdatedAt)
	}
	if len(session.Transcript) != 4 || session.Transcript[0].Role != "user" {
		t.Errorf("Transcript = %+v", session.Transcript)
	}
}

func TestReadAgentCoreSessionMetaHotWindow(t *testing.T) {
	dir := t.TempDir()
	writeAgentCoreJournalFixture(t, dir, "conv-w", "/p")
	path := filepath.Join(dir, agentCoreJournalFileName("conv-w"))
	session := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: 2})
	if session.MessageCount != 4 || len(session.Transcript) != 2 {
		t.Fatalf("count=%d transcript=%d, want 4/2（热窗只留尾部）", session.MessageCount, len(session.Transcript))
	}
	if session.Transcript[0].Content != "第二句" {
		t.Errorf("热窗首条 = %q, want 第二句", session.Transcript[0].Content)
	}
}

func TestReadAgentCoreSessionMetaScanDirsDrop(t *testing.T) {
	dir := t.TempDir()
	writeAgentCoreJournalFixture(t, dir, "conv-f", "/tmp/other-proj")
	path := filepath.Join(dir, agentCoreJournalFileName("conv-f"))
	session := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: 24, ScanDirs: []string{"/tmp/allowed"}})
	if session.ID != "" {
		t.Fatalf("scanDirs 外的会话应整体丢弃, got %+v", session)
	}
}

func TestReadAgentCoreSessionMetaHeadlessFile(t *testing.T) {
	// 无头行（steer 先于 run 到达的文件）:ID 兜底取文件名,其余字段空。
	dir := t.TempDir()
	j := newAgentCoreJournal(dir)
	j.appendUser("conv-headless", "s1", "插话", "2026-10-26T08:00:00Z")
	path := filepath.Join(dir, agentCoreJournalFileName("conv-headless"))
	session := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: 24})
	if session.ID != "conv-headless" || session.MessageCount != 1 {
		t.Fatalf("无头行文件解析 = %+v", session)
	}
	if session.CreatedAt == "" || session.UpdatedAt == "" {
		t.Errorf("无头行时 CreatedAt/UpdatedAt 应回落到首条消息 ts: %+v", session)
	}
}

func TestReadAgentCoreSessionMetaHalfLineTolerance(t *testing.T) {
	dir := t.TempDir()
	writeAgentCoreJournalFixture(t, dir, "conv-h", "/p")
	path := filepath.Join(dir, agentCoreJournalFileName("conv-h"))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("追加打开 journal: %v", err)
	}
	if _, err := f.WriteString(`{"kind":"msg","role":"user","tex`); err != nil { // 崩溃残行
		t.Fatalf("写入残行: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("关闭 journal: %v", err)
	}
	session := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: 24})
	if session.MessageCount != 4 {
		t.Fatalf("残行应被跳过, MessageCount = %d, want 4", session.MessageCount)
	}
}

func TestReadAgentCoreSessionMetaFullyCorruptFileSkipped(t *testing.T) {
	// 整文件损坏（每行都是垃圾字节）：文件名兜底不得凭空造出
	// MessageCount=0 的幽灵卡,reader/collect 都要整体跳过。
	// 对照：headless 文件有合法 msg 行,仍必须能解析（见上方测试）。
	dir := t.TempDir()
	path := filepath.Join(dir, agentCoreJournalFileName("conv-corrupt"))
	garbage := []byte("\x00\x01binary junk\nnot json at all\n{\"kind\":\"msg\",\"role\":\"user\",\"tex\n]]]garbage[[[\n")
	if err := os.WriteFile(path, garbage, 0o600); err != nil {
		t.Fatalf("写入损坏文件: %v", err)
	}
	if session := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: 24}); session.ID != "" {
		t.Fatalf("整文件损坏应返回空结构体, got %+v", session)
	}

	setTestAgentHome(t)
	if err := os.MkdirAll(agentCoreJournalDir(), 0o700); err != nil {
		t.Fatalf("建 journal 目录: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentCoreJournalDir(), agentCoreJournalFileName("conv-corrupt")), garbage, 0o600); err != nil {
		t.Fatalf("写入损坏文件: %v", err)
	}
	for _, session := range collectAgentCoreVibeSessions(nil) {
		if session.ID == "conv-corrupt" {
			t.Fatalf("collect 不得产出整文件损坏的幽灵卡: %+v", session)
		}
	}
}

func TestCollectAgentCoreVibeSessionsMergeAndCache(t *testing.T) {
	setTestAgentHome(t)
	dir := agentCoreJournalDir()
	writeAgentCoreJournalFixture(t, dir, "conv-1", "/tmp/p1")
	writeAgentCoreJournalFixture(t, dir, "conv-2", "/tmp/p2")

	sessions := collectAgentCoreVibeSessions(nil)
	if len(sessions) != 2 {
		t.Fatalf("collect = %d, want 2", len(sessions))
	}
	// 缓存不投毒：第二次扫描（可能命中 (size,mtime) 缓存）结果与首次一致。
	// 缓存命中语义本身由 agent_inventory_parse_cache_test.go 覆盖。
	again := collectAgentCoreVibeSessions(nil)
	if len(again) != 2 || again[0].ID != sessions[0].ID {
		t.Fatalf("缓存扫描不一致: %v vs %v", again, sessions)
	}
	// scanDirs 过滤。
	filtered := collectAgentCoreVibeSessions([]string{"/tmp/p1"})
	if len(filtered) != 1 || filtered[0].ID != "conv-1" {
		t.Fatalf("scanDirs 过滤 = %v", filtered)
	}
}

func TestCollectAgentVibeSessionsIncludesAgentCore(t *testing.T) {
	setTestAgentHome(t)
	writeAgentCoreJournalFixture(t, agentCoreJournalDir(), "conv-merge", "/tmp/pm")
	sessions := collectAgentVibeSessions(nil)
	found := false
	for _, s := range sessions {
		if s.ID == "conv-merge" && s.Provider == "agentcore" {
			found = true
		}
	}
	if !found {
		t.Fatal("collectAgentVibeSessions 三路合并未包含 agentcore 条目")
	}
}

func TestFindAgentVibeSessionDetailAgentCoreEndToEnd(t *testing.T) {
	setTestAgentHome(t)
	writeAgentCoreJournalFixture(t, agentCoreJournalDir(), "conv-detail", "/tmp/pd")

	session := findAgentVibeSessionDetail("conv-detail", "", "/tmp/pd", agentVibeSessionReadOptions{Limit: 24, IncludePageMeta: true})
	if session.ID != "conv-detail" {
		t.Fatalf("详情未命中: %+v（枚举根或分发分支漏配）", session)
	}
	if len(session.Transcript) != 4 || session.Transcript[0].Content != "第一句" {
		t.Errorf("转录 = %+v", session.Transcript)
	}
	if session.TranscriptPage == nil || session.TranscriptPage.TotalCount != 4 {
		t.Errorf("分页元数据 = %v", session.TranscriptPage)
	}
	// 未命中路径不误报。
	missing := findAgentVibeSessionDetail("conv-nope", "", "", agentVibeSessionReadOptions{Limit: 24})
	if missing.ID != "" {
		t.Errorf("不存在的会话应返回空: %+v", missing)
	}
	// candidate 枚举路(带 sourceSessionID)也要命中——上面裸 ID 走的是兜底路。
	hit := findAgentVibeSessionDetail("", "conv-detail", "/tmp/pd", agentVibeSessionReadOptions{Limit: 24})
	if hit.ID != "conv-detail" {
		t.Errorf("candidate 枚举路未命中: %+v", hit)
	}
}

func TestOverlayMergesAgentCoreActiveWithFileEntry(t *testing.T) {
	// 运行中：活跃覆盖层与文件条目同裸 ID → 合成一条，不出双卡。
	setTestAgentHome(t)
	writeAgentCoreJournalFixture(t, agentCoreJournalDir(), "conv-live", "/tmp/pl")
	fileEntries := collectAgentCoreVibeSessions(nil)
	if len(fileEntries) != 1 {
		t.Fatalf("fixture = %v", fileEntries)
	}
	active := []models.AgentVibeSession{{
		ID:       "conv-live",
		Provider: "agentcore",
		Status:   "running",
	}}
	merged := overlayActiveAgentVibeSessions(agentSyncSnapshot{VibeSessions: fileEntries}, active, nil)
	if len(merged.VibeSessions) != 1 {
		t.Fatalf("合并后 = %d 条, want 1（同 ID 不得双卡）", len(merged.VibeSessions))
	}
	if merged.VibeSessions[0].Status != "running" {
		t.Errorf("合并后 status = %q, want running", merged.VibeSessions[0].Status)
	}
}
