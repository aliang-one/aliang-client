package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

// writeAgentCoreJournalFixture 用真实 journal 写侧造测试文件（保证格式一致）。
func writeAgentCoreJournalFixture(t *testing.T, dir, sessionID, projectPath string) {
	t.Helper()
	j := newAgentCoreJournal(dir)
	j.ensureSessionHeader(sessionID, projectPath, "glm-5.2", "", "2026-10-26T08:00:00Z", "phone", nil, nil, "")
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

func TestReadAgentCoreSessionMetaOriginBackfill(t *testing.T) {
	// 清单条目回填 journal 头行 origin：phone 起会话标 phone（跨端续聊分流依据），
	// 旧头行（无 origin 键）与无头行文件保持空串。fixture 走真实写侧
	// ensureSessionHeader（origin="phone"），保证头行格式与生产一致。
	// setTestAgentHome 必须最先调：writeCoreJournalRawFixture 经 agentCoreJournalDir()
	// 解析真实家目录，晚隔离会把 fixture 写进开发者 ~/.aliang（幽灵卡片污染）。
	setTestAgentHome(t)
	dir := t.TempDir()
	writeAgentCoreJournalFixture(t, dir, "conv-origin", "/tmp/po")
	path := filepath.Join(dir, agentCoreJournalFileName("conv-origin"))
	session := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: 24})
	if session.Origin != "phone" {
		t.Fatalf("Origin = %q, want phone（头行回填）", session.Origin)
	}

	// 旧文件：头行没有 origin 键（阶段 1 之前的落盘格式）。
	legacyPath := writeCoreJournalRawFixture(t, "conv-origin-legacy",
		`{"kind":"session","session_id":"conv-origin-legacy","project_path":"/tmp/pl","model":"m","created_at":"2026-10-26T01:00:00Z"}`,
		`{"kind":"msg","role":"user","message_id":"u1","text":"旧会话","ts":"2026-10-26T01:00:05Z"}`,
	)
	legacy := readAgentCoreSessionMetaWithOptions(legacyPath, agentVibeSessionReadOptions{Limit: 24})
	if legacy.Origin != "" {
		t.Fatalf("旧头行 Origin = %q, want 空串", legacy.Origin)
	}

	// 无头行文件（steer 先到，只有 msg 行）：origin 无处可读,保持空串。
	headlessDir := t.TempDir()
	j := newAgentCoreJournal(headlessDir)
	j.appendUser("conv-origin-headless", "s1", "插话", "2026-10-26T08:00:00Z")
	headless := readAgentCoreSessionMetaWithOptions(filepath.Join(headlessDir, agentCoreJournalFileName("conv-origin-headless")), agentVibeSessionReadOptions{Limit: 24})
	if headless.Origin != "" {
		t.Fatalf("无头行 Origin = %q, want 空串", headless.Origin)
	}

	// 清单扫描路（collectAgentCoreVibeSessions → parse cache）同样携带 origin,
	// 且二次扫描命中 (size,mtime) 缓存后不丢字段。
	setTestAgentHome(t)
	writeAgentCoreJournalFixture(t, agentCoreJournalDir(), "conv-collect-origin", "/tmp/pc")
	entries := collectAgentCoreVibeSessions(nil)
	var hit *models.AgentVibeSession
	for i := range entries {
		if entries[i].ID == "conv-collect-origin" {
			hit = &entries[i]
		}
	}
	if hit == nil {
		t.Fatalf("collect 未命中 fixture 会话: %v", entries)
	}
	if hit.Origin != "phone" {
		t.Fatalf("collect 条目 Origin = %q, want phone", hit.Origin)
	}
	cached := collectAgentCoreVibeSessions(nil)
	for i := range cached {
		if cached[i].ID == "conv-collect-origin" && cached[i].Origin != "phone" {
			t.Fatalf("缓存扫描后 Origin = %q, want phone（缓存不得投毒）", cached[i].Origin)
		}
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

// TestCollectAgentCoreVibeSessionsAppliesRenameCache 收口改名断链：手机经
// ai.session.rename 改名 agentcore 会话时,agent 写侧把改名同时以「会话 id」
// 为键落 rename 缓存（native 键对裸 id 卡不可达）,清单组装处按会话 id 兜底
// 覆盖标题——优先级 rename 缓存 > journal 首条消息截断。
func TestCollectAgentCoreVibeSessionsAppliesRenameCache(t *testing.T) {
	setTestAgentHome(t)
	cache.ResetCacheDirForTest()
	dir := agentCoreJournalDir()
	writeAgentCoreJournalFixture(t, dir, "conv-renamed", "/tmp/p1")
	writeAgentCoreJournalFixture(t, dir, "conv-plain", "/tmp/p2")

	path, err := agentRenameCachePath()
	require.NoError(t, err)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	require.NoError(t, saveAgentRenameCacheFile(path, map[string]agentRenameCacheEntry{
		"conv-renamed": {
			Name:        "手机新标题",
			Origin:      "phone",
			Source:      "phone",
			UpdatedAt:   now,
			ProjectPath: "/tmp/p1",
			SkipMirror:  true,
		},
	}))

	sessions := collectAgentCoreVibeSessions(nil)
	byID := map[string]models.AgentVibeSession{}
	for _, s := range sessions {
		byID[s.ID] = s
	}
	renamed, ok := byID["conv-renamed"]
	if !ok {
		t.Fatalf("missing conv-renamed in %v", sessions)
	}
	if renamed.Title != "手机新标题" {
		t.Errorf("Title = %q, want 手机新标题（rename 缓存优先于 journal 首条消息）", renamed.Title)
	}
	if renamed.TitleSource != "user" {
		t.Errorf("TitleSource = %q, want user（phone origin 归一为用户意图）", renamed.TitleSource)
	}
	if renamed.TitleUpdatedAt == "" {
		t.Error("TitleUpdatedAt 为空,服务端 latest-of-writer 守卫将失去锚点")
	}
	plain, ok := byID["conv-plain"]
	if !ok {
		t.Fatalf("missing conv-plain in %v", sessions)
	}
	if plain.Title != "第一句" {
		t.Errorf("未改名卡 Title = %q, want journal 首条消息「第一句」", plain.Title)
	}
}

// TestReadAgentCoreSessionMetaEndErrorSystemBar（G5 问题悬空）：详情转录必须
// 把 journal end(error) 行折成 role=system 的系统条（人话终态文案），失败提问
// 不再悬空。核对项：①系统条按转录序插在失败位置（tool/turn 同款行序计数器，
// Index 单调）；②不计 MessageCount/displayCount（系统条是轨迹性说明行，与
// tool 行同口径——PhoneServer 消息数过滤宇宙已按 role=system 排除）；③成功
// /取消终态不产条；④无头行文件不产条（session.ID 空时无归属，与 tool/turn
// 同款损失模式）。
func TestReadAgentCoreSessionMetaEndErrorSystemBar(t *testing.T) {
	setTestAgentHome(t)
	path := writeCoreJournalRawFixture(t, "sess-endbar",
		`{"kind":"session","session_id":"sess-endbar","project_path":"/tmp/p","model":"m","created_at":"2026-11-05T01:00:00Z"}`,
		`{"kind":"msg","role":"user","message_id":"u1","text":"查一下","ts":"2026-11-05T01:00:05Z"}`,
		`{"kind":"turn","turn":1,"role":"assistant","stop_reason":"tool_use","text":"正在查看相关文件…","ts":"2026-11-05T01:00:06Z","tools":["Bash"]}`,
		`{"kind":"end","status":"error","error":"工具连续 3 次执行失败，疑似陷入重试循环，已停止","diagnostics":{"turns":3,"reason":"工具连续 3 次执行失败，疑似陷入重试循环"},"ts":"2026-11-05T01:00:07Z"}`,
		`{"kind":"msg","role":"user","message_id":"u2","text":"再试一次","ts":"2026-11-05T01:01:00Z"}`,
		`{"kind":"end","status":"success","ts":"2026-11-05T01:01:10Z"}`,
	)
	session := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: agentVibeTranscriptMaxMessages, IncludePageMeta: true})
	// 计数口径：msg+turn 行计数（user×2 + turn×1 = 3），系统条不计数。
	if session.MessageCount != 3 || session.DisplayMessageCount == nil || *session.DisplayMessageCount != 3 {
		t.Fatalf("系统条不得计入对话计数: %d %v", session.MessageCount, session.DisplayMessageCount)
	}
	want := []struct {
		role    string
		id      string
		index   int
		content string
	}{
		{"user", "u1", 0, "查一下"},
		{"assistant", "turn-000001", 1, ""},
		{"system", "end-000001", 2, "这一轮执行失败：工具连续 3 次执行失败，疑似陷入重试循环"},
		{"user", "u2", 3, "再试一次"},
	}
	if len(session.Transcript) != len(want) {
		t.Fatalf("Transcript = %d 行, want %d: %+v", len(session.Transcript), len(want), session.Transcript)
	}
	for i, w := range want {
		msg := session.Transcript[i]
		if msg.Role != w.role || msg.ID != w.id || msg.Index != w.index {
			t.Fatalf("Transcript[%d] = %+v, want role=%s id=%s index=%d", i, msg, w.role, w.id, w.index)
		}
		if w.content != "" && msg.Content != w.content {
			t.Fatalf("Transcript[%d].Content = %q, want %q", i, msg.Content, w.content)
		}
	}
	// 系统条带 end 行时间戳（气泡相对时间用它）。
	if session.Transcript[2].Timestamp != normalizeAgentTime("2026-11-05T01:00:07Z") {
		t.Fatalf("系统条时间戳错: %+v", session.Transcript[2])
	}
}

// 成功终态-only 文件：任何位置都不得出现系统条（与快照读侧同款守卫）。
func TestReadAgentCoreSessionMetaSuccessEndNoSystemBar(t *testing.T) {
	setTestAgentHome(t)
	path := writeCoreJournalRawFixture(t, "sess-endok",
		`{"kind":"session","session_id":"sess-endok","project_path":"/tmp/p","model":"m","created_at":"2026-11-05T01:00:00Z"}`,
		`{"kind":"msg","role":"user","message_id":"u1","text":"问","ts":"2026-11-05T01:00:05Z"}`,
		`{"kind":"end","status":"success","ts":"2026-11-05T01:00:09Z"}`,
	)
	session := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: agentVibeTranscriptMaxMessages})
	for _, msg := range session.Transcript {
		if msg.Role == "system" {
			t.Fatalf("成功终态不得产系统条: %+v", session.Transcript)
		}
	}
}

// 无头行文件（steer 先到）：系统条与 tool/turn 同款跳过——session.ID 在解析期
// 尚空，无归属依据；诊断 enrich 不受影响（既有口径不变）。
func TestReadAgentCoreSessionMetaEndBarHeadlessFileSkipped(t *testing.T) {
	setTestAgentHome(t)
	path := writeCoreJournalRawFixture(t, "sess-endheadless",
		`{"kind":"msg","role":"user","message_id":"u1","text":"问","ts":"2026-11-05T01:00:05Z"}`,
		`{"kind":"end","status":"error","error":"上游失联","ts":"2026-11-05T01:00:09Z"}`,
	)
	session := readAgentCoreSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: agentVibeTranscriptMaxMessages})
	if session.ID == "" {
		t.Fatalf("无头行文件应兜底文件名 ID: %+v", session)
	}
	for _, msg := range session.Transcript {
		if msg.Role == "system" {
			t.Fatalf("无头行文件的系统条应跳过(无归属): %+v", session.Transcript)
		}
	}
	if session.Diagnostics == nil || session.Diagnostics.Error != "上游失联" {
		t.Fatalf("诊断 enrich 口径不得受系统条改动影响: %+v", session.Diagnostics)
	}
}
