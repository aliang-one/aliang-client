package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aliang.one/nursorgate/common/cache"
)

// CC 注入行回归：Claude Code 会把三类 harness 工件以 type=user 写进会话
// jsonl——Skill 工具装载的 skill 全文（isMeta+sourceToolUseID+content 数组）、
// /命令回显（<local-command-stdout>，部分 CLI 版本无 isMeta）、系统提醒
// （<system-reminder>，isMeta）。readClaudeSessionMetaWithOptions 必须把它们
// 挡在 transcript 之外，否则手机会把几千字 skill 文档渲染成用户气泡
// （生产实锤：ai_import_c7e27e3a871200ca03 的 pos0/pos3/pos23，源会话
// f7a089fc @ AliangBoard）。同时必须保住两条既有契约：
//   - 索引照旧自增：被丢行只让位、不挪后续消息的 Index（stableAgentID 与
//     已入库消息一致，server 按 id upsert 才不会重复存储）；
//   - 真实用户行不受影响（含正文恰好提及 meta 标签的行）。

// writeCCMetaFixture 建一个最小 ~/.claude/projects 工程，行内 %CWD% 会被
// 替换成真实 cwd/sessionId 片段。
func writeCCMetaFixture(t *testing.T, lines []string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	projectPath := filepath.Join(home, "work", "myproject")
	require.NoError(t, os.MkdirAll(projectPath, 0o700))
	encodedCwd := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
	claudeDir := filepath.Join(home, ".claude", "projects", encodedCwd)
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	const sid = "ccmeta-sid"
	cwd := `"cwd":"` + projectPath + `","sessionId":"` + sid + `","gitBranch":"main"`
	full := make([]string, 0, len(lines))
	for _, line := range lines {
		full = append(full, strings.ReplaceAll(line, "%CWD%", cwd))
	}
	return writeClaudeTranscriptFixture(t, claudeDir, sid, full, time.Now())
}

func ccMetaTranscript(t *testing.T, path string) (roles []string, contents []string, indices []int) {
	t.Helper()
	session := readClaudeSessionMetaWithOptions(path, agentVibeSessionReadOptions{})
	require.NotEmpty(t, session.ID)
	for _, m := range session.Transcript {
		roles = append(roles, m.Role)
		contents = append(contents, m.Content)
		indices = append(indices, m.Index)
	}
	return roles, contents, indices
}

func TestClaudeSessionMetaDropsSkillInjectionRow(t *testing.T) {
	path := writeCCMetaFixture(t, []string{
		`{"timestamp":"2026-06-13T02:00:00Z","type":"user",%CWD%,"message":{"role":"user","content":"帮我调 skill 排查问题"}}`,
		`{"timestamp":"2026-06-13T02:01:00Z","type":"assistant",%CWD%,"message":{"role":"assistant","content":[{"type":"text","text":"好，先调 skill。"}]}}`,
		// 生产实证形状：isMeta+sourceToolUseID+content 数组，纯 skill 全文
		`{"timestamp":"2026-06-13T02:02:00Z","type":"user","isMeta":true,"sourceToolUseID":"toolu_01abc",%CWD%,"message":{"role":"user","content":[{"type":"text","text":"Base directory for this skill: /Users/mac/.claude/plugins/cache/x/5.0.5/skills/systematic-debugging\n\n# Systematic Debugging\n\nNO FIXES WITHOUT ROOT CAUSE."}]}}`,
		`{"timestamp":"2026-06-13T02:03:00Z","type":"assistant",%CWD%,"message":{"role":"assistant","content":[{"type":"text","text":"按流程走。"}]}}`,
	})

	roles, contents, indices := ccMetaTranscript(t, path)
	assert.Equal(t, []string{"user", "assistant", "assistant"}, roles)
	assert.NotContains(t, strings.Join(contents, "\n"), "Base directory for this skill",
		"skill 注入正文绝不能进 transcript（手机会把它渲染成用户气泡）")
	// meta 行只让位：后续消息 Index 仍按“占位自增”语义（0,1,3），与已入库消息对齐
	assert.Equal(t, []int{0, 1, 3}, indices)
}

func TestClaudeSessionMetaDropsLocalCommandStdoutRow(t *testing.T) {
	path := writeCCMetaFixture(t, []string{
		`{"timestamp":"2026-06-13T02:00:00Z","type":"user",%CWD%,"message":{"role":"user","content":"把 effort 调到 max"}}`,
		// /effort 回显：部分 CLI 版本该行没有 isMeta，只能靠前缀识别
		`{"timestamp":"2026-06-13T02:01:00Z","type":"user",%CWD%,"message":{"role":"user","content":"<local-command-stdout>Set effort level to max (this session only)"}}`,
		`{"timestamp":"2026-06-13T02:02:00Z","type":"assistant",%CWD%,"message":{"role":"assistant","content":[{"type":"text","text":"已调。"}]}}`,
	})

	roles, contents, indices := ccMetaTranscript(t, path)
	assert.Equal(t, []string{"user", "assistant"}, roles)
	assert.NotContains(t, strings.Join(contents, "\n"), "<local-command-stdout>")
	assert.Equal(t, []int{0, 2}, indices)
}

func TestClaudeSessionMetaDropsSystemReminderRow(t *testing.T) {
	path := writeCCMetaFixture(t, []string{
		`{"timestamp":"2026-06-13T02:00:00Z","type":"user",%CWD%,"message":{"role":"user","content":"看看集群"}}`,
		`{"timestamp":"2026-06-13T02:05:00Z","type":"user","isMeta":true,%CWD%,"message":{"role":"user","content":"<system-reminder> The user named this session \"termianl resets\". </system-reminder>"}}`,
		`{"timestamp":"2026-06-13T02:06:00Z","type":"assistant",%CWD%,"message":{"role":"assistant","content":[{"type":"text","text":"好的。"}]}}`,
	})

	roles, contents, indices := ccMetaTranscript(t, path)
	assert.Equal(t, []string{"user", "assistant"}, roles)
	assert.NotContains(t, strings.Join(contents, "\n"), "<system-reminder>")
	assert.Equal(t, []int{0, 2}, indices)
}

func TestClaudeSessionMetaKeepsGenuineRowsMentioningMetaTags(t *testing.T) {
	path := writeCCMetaFixture(t, []string{
		// 真实用户正文恰好“提及”标签（非行首前缀）——绝不能被前缀规则误杀
		`{"timestamp":"2026-06-13T02:00:00Z","type":"user",%CWD%,"message":{"role":"user","content":"这个会话里有个 <system-reminder> 提醒，帮我看看"}}`,
		`{"timestamp":"2026-06-13T02:01:00Z","type":"user",%CWD%,"message":{"role":"user","content":"- <local-command-stdout> 出现在日志第一行"}}`,
	})

	roles, _, indices := ccMetaTranscript(t, path)
	assert.Equal(t, []string{"user", "user"}, roles)
	assert.Equal(t, []int{0, 1}, indices)
}

func TestClaudeSessionMetaStillDropsSlashCommandRecords(t *testing.T) {
	// 守住既有行为：重构 commandRecord 判定后，原本的两个前缀照旧生效；
	// 评审语料实锤部分 CLI 版本（2.1.156/2.1.280）写 /命令行以 <command-message>
	// 开头且无 isMeta，同属 slash 流水，必须一并挡住。
	path := writeCCMetaFixture(t, []string{
		`{"timestamp":"2026-06-13T02:00:00Z","type":"user",%CWD%,"message":{"role":"user","content":"<command-name>/effort</command-name>\n<command-message>effort</command-message>\n<command-args>max</command-args>"}}`,
		`{"timestamp":"2026-06-13T02:01:00Z","type":"user",%CWD%,"message":{"role":"user","content":"<local-command-caveat>cmd output is localized</local-command-caveat>"}}`,
		`{"timestamp":"2026-06-13T02:02:00Z","type":"user",%CWD%,"message":{"role":"user","content":"<command-message>review</command-message>\n<command-name>/review</command-name>\n<command-args></command-args>"}}`,
		`{"timestamp":"2026-06-13T02:03:00Z","type":"user",%CWD%,"message":{"role":"user","content":"真正的问题"}}`,
	})

	roles, contents, _ := ccMetaTranscript(t, path)
	assert.Equal(t, []string{"user"}, roles)
	assert.Equal(t, []string{"真正的问题"}, contents)
}
