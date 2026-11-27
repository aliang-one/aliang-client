package services

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"

	"aliang.one/nursorgate/app/http/models"
)

// 2026-10-29 11:03Z 生产实锤:server 落库的 project_path 与转录 jsonl 的 cwd
// 归一化不一致(尾斜杠/大小写/未 clean)时,严格 == 把会话误判为
// "vibe session not found"。比较必须双侧 clean + 平台感知的大小写折叠。
func TestAgentVibeProjectPathMatches(t *testing.T) {
	cases := []struct {
		name        string
		requestPath string
		sessionPath string
		want        bool
	}{
		{"尾斜杠差异归一", "/tmp/work/proj", "/tmp/work/proj/", true},
		{"未clean差异归一", "/tmp/work/./proj/../proj", "/tmp/work/proj/", true},
		{"真不同路径仍拒绝", "/tmp/work/proj-a", "/tmp/work/proj-b", false},
		{"前缀包含不算同路径", "/tmp/work", "/tmp/work/proj", false},
		{"request为空跳过该门", "", "/tmp/work/proj", true},
		{"session为空不匹配非空request", "/tmp/work/proj", "", false},
		{"双侧为空视为匹配", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, agentVibeProjectPathMatches(tc.requestPath, tc.sessionPath))
		})
	}

	// 大小写:darwin/windows 默认文件系统大小写不敏感 → 折叠后相等;
	// linux 大小写敏感 → 不等。
	t.Run("大小写折叠按平台语义", func(t *testing.T) {
		got := agentVibeProjectPathMatches("/Users/Mac/Work/Proj", "/users/mac/work/proj/")
		switch runtime.GOOS {
		case "darwin", "windows":
			assert.True(t, got, "darwin/windows 应大小写不敏感")
		default:
			assert.False(t, got, "linux 应保持大小写敏感")
		}
	})
}

// 经 agentVibeSessionMatches 的集成路径:ID 命中后由 project_path 门裁决。
func TestAgentVibeSessionMatchesProjectPathGate(t *testing.T) {
	session := models.AgentVibeSession{ID: "claude_abc123", ProjectPath: "/Users/Mac/Work/Proj/"}

	// 大小写折叠按平台语义(darwin/windows 不敏感,linux 敏感——见上方同文件用例)。
	// CI 在 Linux runner 上跑,断言必须跟随平台,不能写死「应匹配」。
	samePathDifferentCase := agentVibeSessionMatches(session, "", "abc123", "/users/mac/work/proj")
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		assert.True(t, samePathDifferentCase, "darwin/windows:ID 命中 + 大小写归一后同路径应匹配")
	} else {
		assert.False(t, samePathDifferentCase, "linux:大小写敏感,不同大小写视为不同路径")
	}
	assert.True(t, agentVibeSessionMatches(session, "", "abc123", "/Users/Mac/Work/Proj"),
		"ID 命中 + 归一化(尾斜杠)后同路径应匹配")
	assert.True(t, agentVibeSessionMatches(session, "", "abc123", ""),
		"ID 命中 + 未携带 project_path 应放行")
	assert.False(t, agentVibeSessionMatches(session, "", "abc123", "/other/place"),
		"ID 命中 + 真不同路径仍拒绝")
	assert.False(t, agentVibeSessionMatches(session, "", "other-id", "/Users/Mac/Work/Proj/"),
		"ID 不命中时路径不参与判定")
	assert.False(t, agentVibeSessionMatches(models.AgentVibeSession{}, "abc123", "", ""),
		"空会话一律不匹配")
}
