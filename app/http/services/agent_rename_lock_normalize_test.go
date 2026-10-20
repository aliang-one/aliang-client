package services

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rename_cache.json 是整表读改写：WS 读循环 goroutine 的手机改名与 inventory
// ticker/HTTP handler 的 observed 合并若不加锁交错，后写者会用不含对方条目的
// 旧整表覆盖先写者——手机条目没有本地来源，丢了就是永久丢（R5）。

// TestUpdateAgentRenameCacheConcurrentRMW pins the mutex: N concurrent
// read-modify-write cycles must all survive (before the mutex, later cycles
// overwrote earlier ones' whole-file saves).
func TestUpdateAgentRenameCacheConcurrentRMW(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	const workers, perWorker = 8, 25
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				sid := "sid-concurrent-" + string(rune('a'+worker)) + "-" + time.Duration(i).String()
				_ = updateAgentRenameCache(func(entries map[string]agentRenameCacheEntry) bool {
					mergeAgentRenameEntry(entries, sid, agentRenameCacheEntry{
						Name:      "名" + sid,
						Origin:    agentRenameOriginLocal,
						Source:    "user",
						UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
					})
					return true
				})
			}
		}(w)
	}
	wg.Wait()

	cached, err := loadAgentRenameCacheFile(mustRenameCachePath(t))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(cached), workers*perWorker,
		"every concurrent read-modify-write cycle must survive the whole-file save")
}

func mustRenameCachePath(t *testing.T) string {
	t.Helper()
	path, err := agentRenameCachePath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	return path
}

// TestHandleRemoteAIRenameSurvivesConcurrentCacheWrites runs the wire rename
// against a hammering background writer: the phone entry must still be on disk
// afterwards.
func TestHandleRemoteAIRenameSurvivesConcurrentCacheWrites(t *testing.T) {
	service := newEnabledRenameService(t)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			sid := "sid-hammer-" + time.Duration(i).String()
			_ = updateAgentRenameCache(func(entries map[string]agentRenameCacheEntry) bool {
				mergeAgentRenameEntry(entries, sid, agentRenameCacheEntry{
					Name:      "hammer",
					Origin:    agentRenameOriginLocal,
					Source:    "user",
					UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
				})
				return true
			})
		}
	}()

	acks := dispatchPhoneRename(t, service, "conv-1", "手机改名并发安全")
	close(stop)
	wg.Wait()

	require.Len(t, acks, 1)
	assert.Equal(t, true, acks[0]["accepted"])
	cached, err := loadAgentRenameCacheFile(mustRenameCachePath(t))
	require.NoError(t, err)
	require.Contains(t, cached, renameTestNativeID, "the phone rename must survive concurrent whole-file saves")
	assert.Equal(t, "手机改名并发安全", cached[renameTestNativeID].Name)
}

// TestNormalizeAgentRenameTitle pins CC-parity normalization (R7): control
// chars stripped, whitespace folded, 200-code-point cap.
func TestNormalizeAgentRenameTitle(t *testing.T) {
	assert.Equal(t, "build[2Jdone", normalizeAgentRenameTitle("build\n\x1b[2Jdone"))
	assert.Equal(t, "多 空 格", normalizeAgentRenameTitle("  多  空 格  "))
	assert.Equal(t, "ab", normalizeAgentRenameTitle("a\tb")) // \t is a control char, stripped
	assert.Equal(t, "正常标题", normalizeAgentRenameTitle("正常标题"))
	assert.Empty(t, normalizeAgentRenameTitle("  \n \x1b "))

	long := strings.Repeat("标", 250)
	assert.Equal(t, strings.Repeat("标", 200), normalizeAgentRenameTitle(long))
}

// TestHandleRemoteAIRenameNormalizesTitle proves the wire title is cleaned at
// every persistence boundary: cache, transcript custom-title line, sidecar.
func TestHandleRemoteAIRenameNormalizesTitle(t *testing.T) {
	service := newEnabledRenameService(t)
	home := os.Getenv("HOME")
	seedRenameTranscript(t, home, "-home-liang-demo", renameTestNativeID,
		`{"type":"user","message":{"role":"user","content":"hi"}}`)

	acks := dispatchPhoneRename(t, service, "conv-1", "build\n\x1b[2Jdone")
	require.Len(t, acks, 1)
	assert.Equal(t, true, acks[0]["accepted"])

	cached, err := loadAgentRenameCacheFile(mustRenameCachePath(t))
	require.NoError(t, err)
	require.Contains(t, cached, renameTestNativeID)
	assert.Equal(t, "build[2Jdone", cached[renameTestNativeID].Name,
		"cache must store the normalized title")

	raw, err := os.ReadFile(renameTranscriptPath(home, "-home-liang-demo", renameTestNativeID))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "\\u001b",
		"the mirrored transcript line must not carry the raw ESC")
	assert.Contains(t, string(raw), `"customTitle":"build[2Jdone"`)

	sidecar := sidecarPathFor(renameTranscriptPath(home, "-home-liang-demo", renameTestNativeID), renameTestNativeID)
	sidecarRaw, err := os.ReadFile(sidecar)
	require.NoError(t, err)
	assert.Equal(t, `{"customTitle":"build[2Jdone"}`, string(sidecarRaw))
}

// TestHandleRemoteAIRenameRejectsControlOnlyTitle: a title that is empty after
// cleanup must be rejected, not stored as an empty name.
func TestHandleRemoteAIRenameRejectsControlOnlyTitle(t *testing.T) {
	service := newEnabledRenameService(t)
	acks := dispatchRename(t, service, map[string]interface{}{
		"type":              "ai.session.rename",
		"session_id":        "conv-1",
		"title":             "  \n \x1b ",
		"source_session_id": renameTestNativeID,
		"provider":          "claude",
	})
	require.Len(t, acks, 1)
	assert.Equal(t, false, acks[0]["accepted"])
}
