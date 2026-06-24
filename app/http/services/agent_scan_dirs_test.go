package services

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPathUnderAnyScanDir(t *testing.T) {
	cases := []struct {
		name string
		path string
		dirs []string
		want bool
	}{
		{"path under dir", "/a/b", []string{"/a"}, true},
		{"path equals dir", "/a", []string{"/a"}, true},
		{"nested deep", "/a/b/c/d", []string{"/a"}, true},
		{"path outside dir", "/c/d", []string{"/a"}, false},
		{"path above dir", "/a", []string{"/a/b"}, false},
		// prefix trick: /a 不能误匹配 /ab
		{"prefix trick ab vs a", "/ab", []string{"/a"}, false},
		{"prefix trick abc vs ab", "/abc", []string{"/ab"}, false},
		{"no dirs", "/a/b", nil, false},
		{"empty dir skipped", "/a/b", []string{""}, false},
		{"dot dir skipped", "/a/b", []string{"."}, false},
		{"multiple dirs match second", "/y/z", []string{"/a", "/y"}, true},
		{"multiple dirs none match", "/z", []string{"/a", "/y"}, false},
		// 路径清理
		{"unclean path", "/a/./b", []string{"/a"}, true},
		{"trailing slash dir", "/a/b", []string{"/a/"}, true},
		{"dir with trailing slash equals", "/a", []string{"/a/"}, true},
		{"whitespace dir trimmed", "/a/b", []string{"  /a  "}, true},
		{"empty path", "", []string{"/a"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pathUnderAnyScanDir(tc.path, tc.dirs)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestReadCodexSessionMetaScanDirsEarlyFilter 验证读到 cwd 后立即按 ScanDirs 过滤：
// 不在目录内的会话直接返回空（不读 transcript）。
func TestReadCodexSessionMetaScanDirsEarlyFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	content := "{\"timestamp\":\"2025-01-01T00:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"s1\",\"cwd\":\"/projects/foo\",\"model\":\"gpt\"}}\n" +
		"{\"timestamp\":\"2025-01-01T00:00:01Z\",\"type\":\"message\",\"payload\":{\"role\":\"user\",\"content\":\"hello\"}}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// 不过滤：正常读取，含 transcript
	plain := readCodexSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: 10})
	assert.NotEmpty(t, plain.ID, "无 ScanDirs 应正常读取")
	assert.Equal(t, "/projects/foo", plain.ProjectPath)

	// ScanDirs 不含 cwd：早过滤，返回空
	dropped := readCodexSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: 10, ScanDirs: []string{"/somewhere-else"}})
	assert.Empty(t, dropped.ID, "cwd 不在 ScanDirs 应被早过滤丢弃")

	// ScanDirs 含 cwd：保留
	kept := readCodexSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: 10, ScanDirs: []string{"/projects"}})
	assert.NotEmpty(t, kept.ID, "cwd 在 ScanDirs 应保留")
	assert.Equal(t, "/projects/foo", kept.ProjectPath)
}
