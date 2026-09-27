package services

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aliang.one/nursorgate/common/cache"
)

// 超长行事故（2026-09-27，liang-dev 会话 3d06bb04）：Claude Code 会把超大
// tool_result（读大文件/长命令输出）写成单行 >1MB 的 jsonl 记录。读取器用
// bufio.Scanner（1MB 行上限）且从不检查 scanner.Err()，第一条超长行就静默
// 结束整个扫描——其后所有真实对话对周期扫描和 ai.session.detail 永久不可见，
// 手机刷新返回"fresh"但数据冻结在超长行之前。本组测试把该事故钉死：
// 超长行必须被正常解析（与大缓冲行为逐字节一致，stableAgentID 零漂移），
// 只有病态行（>16MB）才允许跳过并告警、且绝不中断后续行。

// writeRawJSONL 原样写字节（不受 writeClaudeTranscriptFixture 的 Chtimes/尾换行约束），
// trailingNewline=false 用来钉"末行无换行符"的边界。
func writeRawJSONL(t *testing.T, path string, lines []string, trailingNewline bool) string {
	t.Helper()
	content := strings.Join(lines, "\n")
	if trailingNewline {
		content += "\n"
	}
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// overlongToolResultRow 构造一条合法的 >1MB tool_result 行（真实事故形态）。
func overlongToolResultRow(ts string, cwd string, sid string, padBytes int) string {
	pad := strings.Repeat("x", padBytes)
	return `{"timestamp":"` + ts + `","type":"user",` + cwd + `,"message":{"role":"user","content":[{"type":"tool_result","content":"` + pad + `"}]}}`
}

func claudeCwdFields(projectPath string, sid string) string {
	return `"cwd":"` + projectPath + `","sessionId":"` + sid + `","gitBranch":"main"`
}

func newMaxLineTestHome(t *testing.T) (home string, claudeDir string, projectPath string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()
	projectPath = filepath.Join(home, "work", "myproject")
	require.NoError(t, os.MkdirAll(projectPath, 0o700))
	encodedCwd := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
	claudeDir = filepath.Join(home, ".claude", "projects", encodedCwd)
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))
	return home, claudeDir, projectPath
}

// TestClaudeSessionMetaParsesPastOverlongToolResultLine 是回归钉子（当前代码必红）：
// 1.1MB tool_result 行之后的三条真实消息必须照常出现在 transcript 里，索引
// 连续、stableAgentID 与"无限缓冲解析"完全一致。旧代码在超长行处静默中止，
// MessageCount 冻在 2、后半段对话全部丢失。
func TestClaudeSessionMetaParsesPastOverlongToolResultLine(t *testing.T) {
	_, claudeDir, projectPath := newMaxLineTestHome(t)

	const sid = "maxline-toolresult-sid"
	cwd := claudeCwdFields(projectPath, sid)
	lines := []string{
		`{"timestamp":"2026-06-13T02:00:00Z","type":"user",` + cwd + `,"message":{"role":"user","content":[{"type":"text","text":"Run the big read"}]}}`,
		`{"timestamp":"2026-06-13T02:01:00Z","type":"assistant",` + cwd + `,"message":{"role":"assistant","content":[{"type":"text","text":"On it."}]}}`,
		overlongToolResultRow("2026-06-13T02:02:00Z", cwd, sid, 1_100_000),
		`{"timestamp":"2026-06-13T02:03:00Z","type":"assistant",` + cwd + `,"message":{"role":"assistant","content":[{"type":"text","text":"Tool done, summary: T10 passed"}]}}`,
		`{"timestamp":"2026-06-13T02:04:00Z","type":"user",` + cwd + `,"message":{"role":"user","content":[{"type":"text","text":"Great, proceed to T11"}]}}`,
	}
	path := writeClaudeTranscriptFixture(t, claudeDir, sid, lines, time.Now())

	session := readClaudeSessionMetaWithOptions(path, agentVibeSessionReadOptions{})
	require.NotEmpty(t, session.ID, "session must survive the overlong line")
	assert.Equal(t, 5, session.MessageCount,
		"every user/assistant record keeps its index slot, including the huge tool_result")
	require.Len(t, session.Transcript, 4, "tool_result never enters the transcript, but the messages after it must all be there")

	assert.Equal(t, "Run the big read", session.Transcript[0].Content)
	assert.Equal(t, 0, session.Transcript[0].Index)
	assert.Equal(t, "On it.", session.Transcript[1].Content)
	assert.Equal(t, 1, session.Transcript[1].Index)

	// 超长行之后的消息：索引 3/4 连续（tool_result 占了 2），ID 手算对齐无限缓冲解析。
	wantPostID := stableAgentID("msg", "2026-06-13T02:03:00Z:3:Tool done, summary: T10 passed")
	assert.Equal(t, 3, session.Transcript[2].Index)
	assert.Equal(t, wantPostID, session.Transcript[2].ID,
		"post-overlong-line message ID must match an unlimited-buffer parse byte for byte")
	assert.Equal(t, "Great, proceed to T11", session.Transcript[3].Content)
	assert.Equal(t, 4, session.Transcript[3].Index)

	assert.Equal(t, "2026-06-13T02:04:00Z", normalizeAgentTime(session.UpdatedAt),
		"activity time must reach the last real message past the overlong line")
}

// TestClaudeSessionMetaMatchesUnlimitedParseOracle 用全小行 fixture 锁定解析器
// 行为与改写前逐字节一致（ID/索引/窗口/UpdatedAt 全部手算对齐）。改写必须
// 纯机械，不允许顺手动任何解析语义。
func TestClaudeSessionMetaMatchesUnlimitedParseOracle(t *testing.T) {
	_, claudeDir, projectPath := newMaxLineTestHome(t)

	const sid = "maxline-oracle-sid"
	cwd := claudeCwdFields(projectPath, sid)
	lines := []string{
		`{"timestamp":"2026-06-13T02:00:00Z","type":"user",` + cwd + `,"message":{"role":"user","content":[{"type":"text","text":"First prompt"}]}}`,
		`{"timestamp":"2026-06-13T02:01:00Z","type":"assistant",` + cwd + `,"message":{"role":"assistant","content":[{"type":"text","text":"Reply one"}]}}`,
		`{"timestamp":"2026-06-13T02:02:00Z","type":"user",` + cwd + `,"message":{"role":"user","content":[{"type":"tool_result","content":"small tool output"}]}}`,
		`{"timestamp":"2026-06-13T02:03:00Z","type":"assistant",` + cwd + `,"message":{"role":"assistant","content":[{"type":"text","text":"Reply two"}]}}`,
		`{"type":"last-prompt","lastPrompt":"a draft"}`,
	}
	path := writeClaudeTranscriptFixture(t, claudeDir, sid, lines, time.Now())

	session := readClaudeSessionMetaWithOptions(path, agentVibeSessionReadOptions{})
	require.NotEmpty(t, session.ID)
	assert.Equal(t, "claude_"+sid, session.ID)
	assert.Equal(t, 4, session.MessageCount)
	require.Len(t, session.Transcript, 3, "tool_result occupies an index slot but stays out of the window")

	type want struct {
		index int
		role  string
		text  string
		ts    string
	}
	wants := []want{
		{0, "user", "First prompt", "2026-06-13T02:00:00Z"},
		{1, "assistant", "Reply one", "2026-06-13T02:01:00Z"},
		{3, "assistant", "Reply two", "2026-06-13T02:03:00Z"},
	}
	for i, w := range wants {
		msg := session.Transcript[i]
		assert.Equal(t, w.index, msg.Index)
		assert.Equal(t, w.role, msg.Role)
		assert.Equal(t, w.text, msg.Content)
		assert.Equal(t, w.ts, normalizeAgentTime(msg.Timestamp))
		assert.Equal(t, stableAgentID("msg", w.ts+":"+itoa(w.index)+":"+w.text), msg.ID)
	}
	assert.Equal(t, "First prompt", session.Title)
	assert.Equal(t, "2026-06-13T02:03:00Z", normalizeAgentTime(session.UpdatedAt))
}

// TestClaudeSessionMetaLastLineWithoutTrailingNewline 钉末行无换行边界：
// 最后一条消息没有 \n 结尾也必须照常进 transcript。
func TestClaudeSessionMetaLastLineWithoutTrailingNewline(t *testing.T) {
	_, claudeDir, projectPath := newMaxLineTestHome(t)

	const sid = "maxline-noeol-sid"
	cwd := claudeCwdFields(projectPath, sid)
	lines := []string{
		`{"timestamp":"2026-06-13T02:00:00Z","type":"user",` + cwd + `,"message":{"role":"user","content":[{"type":"text","text":"No trailing newline"}]}}`,
		`{"timestamp":"2026-06-13T02:01:00Z","type":"assistant",` + cwd + `,"message":{"role":"assistant","content":[{"type":"text","text":"Still parsed"}]}}`,
	}
	path := filepath.Join(claudeDir, sid+".jsonl")
	writeRawJSONL(t, path, lines, false)

	session := readClaudeSessionMetaWithOptions(path, agentVibeSessionReadOptions{})
	require.NotEmpty(t, session.ID)
	assert.Equal(t, 2, session.MessageCount)
	require.Len(t, session.Transcript, 2)
	assert.Equal(t, "Still parsed", session.Transcript[1].Content)
}

// TestCodexSessionMetaParsesPastOverlongMessageLine 是 codex 读取器的镜像回归钉子
// （同一处 1MB Scanner 同类 bug，当前代码必红）：超长 assistant 行之后的消息必须照常解析。
func TestCodexSessionMetaParsesPastOverlongMessageLine(t *testing.T) {
	_, claudeDir, projectPath := newMaxLineTestHome(t)

	const sid = "maxline-codex-sid"
	pad := strings.Repeat("y", 1_100_000)
	lines := []string{
		`{"timestamp":"2026-06-13T02:00:00Z","type":"session_meta","payload":{"id":"` + sid + `","cwd":"` + projectPath + `"}}`,
		`{"timestamp":"2026-06-13T02:01:00Z","type":"user","payload":{"text":"codex first"}}`,
		`{"timestamp":"2026-06-13T02:02:00Z","type":"assistant","payload":{"text":"` + pad + `"}}`,
		`{"timestamp":"2026-06-13T02:03:00Z","type":"user","payload":{"text":"codex after the big line"}}`,
	}
	path := writeRawJSONL(t, filepath.Join(claudeDir, sid+".jsonl"), lines, true)

	session := readCodexSessionMetaWithOptions(path, agentVibeSessionReadOptions{})
	require.NotEmpty(t, session.ID, "codex session must survive the overlong line")
	assert.Equal(t, 3, session.MessageCount)
	require.Len(t, session.Transcript, 3, "the big assistant line enters the window truncated, and the tail message must not be lost")
	assert.Equal(t, "codex first", session.Transcript[0].Content)
	assert.Equal(t, "codex after the big line", session.Transcript[2].Content)
	assert.Equal(t, 2, session.Transcript[2].Index)
}

// TestClaudeSessionMetaSkipsPathologicalOverCapLine 钉病态行语义：超过
// MaxLineBytes 的行被跳过——不解析、不占索引位（类型不可知）、后续行照常。
// 这是有意的与"无限缓冲解析"的唯一偏差（仅病态行），索引因此前移一位。
func TestClaudeSessionMetaSkipsPathologicalOverCapLine(t *testing.T) {
	_, claudeDir, projectPath := newMaxLineTestHome(t)

	const sid = "maxline-overcap-sid"
	cwd := claudeCwdFields(projectPath, sid)
	lines := []string{
		`{"timestamp":"2026-06-13T02:00:00Z","type":"user",` + cwd + `,"message":{"role":"user","content":[{"type":"text","text":"Before the monster"}]}}`,
		`{"timestamp":"2026-06-13T02:01:00Z","type":"user",` + cwd + `,"message":{"role":"user","content":[{"type":"text","text":"` + strings.Repeat("z", 2000) + `"}]}}`,
		`{"timestamp":"2026-06-13T02:02:00Z","type":"assistant",` + cwd + `,"message":{"role":"assistant","content":[{"type":"text","text":"After the monster"}]}}`,
	}
	path := writeClaudeTranscriptFixture(t, claudeDir, sid, lines, time.Now())

	// cap=1024：普通行（含 tempdir 长路径，约 300 字节）必须照常解析，
	// monster 行（2000 填充 + 行开销 ≈ 2200 字节）必须被跳过。
	session := readClaudeSessionMetaWithOptions(path, agentVibeSessionReadOptions{MaxLineBytes: 1024})
	require.NotEmpty(t, session.ID)
	assert.Equal(t, 2, session.MessageCount, "the skipped monster line must not consume an index slot")
	require.Len(t, session.Transcript, 2)
	assert.Equal(t, 1, session.Transcript[1].Index, "post-monster indexes shift down by one (documented deviation)")
	wantID := stableAgentID("msg", "2026-06-13T02:02:00Z:1:After the monster")
	assert.Equal(t, wantID, session.Transcript[1].ID)
}

// TestForEachAgentSessionJSONLLineDeliversExactLines 钉 helper 行边界契约：
// 行去 \n/\r、空行不交付（两个读取器对空行的净效果与 Scanner 一致：解析失败
// 跳过）、末行无换行照常交付、visit=false 提前终止、空文件/纯换行文件零交付。
func TestForEachAgentSessionJSONLLineDeliversExactLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mixed.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("alpha\nbeta\r\n\ngamma"), 0o600))

	var got []string
	skipped := forEachAgentSessionJSONLLine(path, 0, func(line []byte) bool {
		got = append(got, string(line))
		return true
	})
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, got, "CRLF must be trimmed, empty lines must not reach visit")
	assert.Equal(t, 0, skipped)

	// visit 返回 false：第三行不得被访问。
	got = nil
	skipped = forEachAgentSessionJSONLLine(path, 0, func(line []byte) bool {
		got = append(got, string(line))
		return len(got) < 2
	})
	assert.Equal(t, []string{"alpha", "beta"}, got, "visit=false must stop delivery immediately")
	assert.Equal(t, 0, skipped)

	// 空文件与纯换行文件：零交付、零跳过。
	empty := filepath.Join(dir, "empty.jsonl")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	count := 0
	skipped = forEachAgentSessionJSONLLine(empty, 0, func(line []byte) bool { count++; return true })
	assert.Equal(t, 0, count)
	assert.Equal(t, 0, skipped)

	newlinesOnly := filepath.Join(dir, "newlines.jsonl")
	require.NoError(t, os.WriteFile(newlinesOnly, []byte("\n\n\n"), 0o600))
	count = 0
	skipped = forEachAgentSessionJSONLLine(newlinesOnly, 0, func(line []byte) bool { count++; return true })
	assert.Equal(t, 0, count)
	assert.Equal(t, 0, skipped)
}

// TestForEachAgentSessionJSONLLineSkipsOverCapAndContinues 钉跳过语义：超限行
// 不交付且计数，后续行照常按序交付。skipped 返回值就是告警断言代理（services
// 包没有 logger 测试钩子，Warn 单次由 helper 内 warned 标志保证）。
func TestForEachAgentSessionJSONLLineSkipsOverCapAndContinues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gaps.jsonl")
	over := strings.Repeat("Q", 100)
	content := strings.Join([]string{"s1", over, "s2", over, "s3"}, "\n") + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	var got []string
	skipped := forEachAgentSessionJSONLLine(path, 64, func(line []byte) bool {
		got = append(got, string(line))
		return true
	})
	assert.Equal(t, []string{"s1", "s2", "s3"}, got)
	assert.Equal(t, 2, skipped)
}

// TestForEachAgentSessionJSONLLineExactCapBoundary 钉 cap 边界：恰好 cap 字节
// 的行内容必须交付（+换行符 = cap+1 原始字节），cap+1 内容才跳过。
func TestForEachAgentSessionJSONLLineExactCapBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "boundary.jsonl")
	exact := strings.Repeat("a", 8)
	over := strings.Repeat("b", 9)
	content := exact + "\n" + over + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	var got []string
	skipped := forEachAgentSessionJSONLLine(path, 8, func(line []byte) bool {
		got = append(got, string(line))
		return true
	})
	assert.Equal(t, []string{exact}, got, "a line of exactly cap bytes content must be delivered")
	assert.Equal(t, 1, skipped, "cap+1 content must be skipped")
}

// itoa 让断言表保持一行一条的可读性。
func itoa(v int) string {
	return strconv.Itoa(v)
}
