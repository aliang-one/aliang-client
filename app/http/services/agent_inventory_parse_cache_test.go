package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

// 摘要级按文件解析缓存的回归:
//   1) 命中必须返回深拷贝——调用方（rename 叠加、DisplayMessageCount 回灌）
//      会原地改写返回值，污染缓存会把上一个调用方的改写泄漏给下一次扫描;
//   2) 追加内容（size/mtime 变化）必须失效重读，否则新消息永远不上报;
//   3) 同尺寸不同 mtime 必须失效重读（mtime 是缓存契约的一半）。
//
// 背景：liang-dev 实测冷扫描 30s+、agent 进程 CPU 常驻 ~120%，缓存的正确性
// 是这次性能修复的前提。

type parseCacheFixture struct {
	home      string
	jsonlPath string
	sid       string
}

func newParseCacheFixture(t *testing.T) parseCacheFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	projectPath := filepath.Join(home, "work", "myproject")
	require.NoError(t, os.MkdirAll(projectPath, 0o700))
	encoded := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
	claudeDir := filepath.Join(home, ".claude", "projects", encoded)
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))

	fixture := parseCacheFixture{
		home:      home,
		jsonlPath: filepath.Join(claudeDir, "cache-fixture-sid.jsonl"),
		sid:       "cache-fixture-sid",
	}
	fixture.writeTranscript(t, cacheFixtureMessage("2026-06-13T02:00:00Z", "first question"))
	return fixture
}

func cacheFixtureMessage(ts, text string) string {
	return `{"timestamp":"` + ts + `","type":"user","cwd":"cwd","sessionId":"cache-fixture-sid","gitBranch":"main","message":{"role":"user","content":[{"type":"text","text":"` + text + `"}]}}` + "\n"
}

// writeTranscript 重写整个 jsonl，并把 mtime 拨到 base+5s，保证与之前 Stat
// 到的缓存键必然不同（不受文件系统 mtime 精度影响）。
func (f parseCacheFixture) writeTranscript(t *testing.T, lines ...string) {
	t.Helper()
	require.NoError(t, os.WriteFile(f.jsonlPath, []byte(strings.Join(lines, "")), 0o600))
	future := time.Now().Add(5 * time.Second)
	require.NoError(t, os.Chtimes(f.jsonlPath, future, future))
}

func (f parseCacheFixture) appendLine(t *testing.T, line string) {
	t.Helper()
	file, err := os.OpenFile(f.jsonlPath, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = file.WriteString(line)
	require.NoError(t, file.Close())
	require.NoError(t, err)
	future := time.Now().Add(5 * time.Second)
	require.NoError(t, os.Chtimes(f.jsonlPath, future, future))
}

func collectParseCacheSession(t *testing.T) models.AgentVibeSession {
	t.Helper()
	sessions := collectClaudeVibeSessions(nil)
	require.Len(t, sessions, 1, "fixture must produce exactly one claude session")
	return sessions[0]
}

func TestAgentVibeParseCacheReturnsIndependentCopies(t *testing.T) {
	newParseCacheFixture(t)

	first := collectParseCacheSession(t)
	require.NotNil(t, first.DisplayMessageCount)
	require.NotEmpty(t, first.Transcript)

	// 模拟调用方的原地改写：rename 叠加改 Title，回灌改 DisplayMessageCount，
	// transcript 原地改内容。
	first.Title = "MUTATED"
	*first.DisplayMessageCount = 999
	first.Transcript[0].Content = "MUTATED"

	second := collectParseCacheSession(t)
	assert.NotEqual(t, "MUTATED", second.Title, "cached session must not leak caller title mutations")
	require.NotNil(t, second.DisplayMessageCount)
	assert.NotEqual(t, 999, *second.DisplayMessageCount, "cached DisplayMessageCount pointee must be copied")
	require.NotEmpty(t, second.Transcript)
	assert.NotEqual(t, "MUTATED", second.Transcript[0].Content, "cached transcript must not leak caller mutations")
}

func TestAgentVibeParseCacheInvalidatesOnAppend(t *testing.T) {
	fixture := newParseCacheFixture(t)

	before := collectParseCacheSession(t)
	require.Len(t, before.Transcript, 1)

	fixture.appendLine(t, cacheFixtureMessage("2026-06-13T02:01:00Z", "second question"))
	after := collectParseCacheSession(t)

	assert.Equal(t, before.MessageCount+1, after.MessageCount, "appended message must bump the count")
	require.Len(t, after.Transcript, 2)
	assert.Contains(t, after.Transcript[1].Content, "second question", "appended content must reach the hot window")
}

func TestAgentVibeParseCacheSameSizeDifferentMtimeRereads(t *testing.T) {
	fixture := newParseCacheFixture(t)

	before := collectParseCacheSession(t)
	require.Len(t, before.Transcript, 1)

	// 同字节数替换文本（"first question" → "first quesitOn" 等长）+ mtime 前拨：
	// 缓存契约是 (size, mtime) 二元组，mtime 变化即失效。
	swapped := strings.Replace(cacheFixtureMessage("2026-06-13T02:00:00Z", "first question"), "first question", "first QUESTION", 1)
	require.Equal(t, len(cacheFixtureMessage("2026-06-13T02:00:00Z", "first question")), len(swapped), "fixture must keep byte size identical")
	fixture.writeTranscript(t, swapped)

	after := collectParseCacheSession(t)
	require.Len(t, after.Transcript, 1)
	assert.Contains(t, after.Transcript[0].Content, "first QUESTION", "same-size content change with new mtime must be re-read")
}
