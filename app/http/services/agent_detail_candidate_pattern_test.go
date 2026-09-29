package services

import (
	"os"
	"path/filepath"
	"testing"
)

// Codex rollout 文件名是 rollout-<时间戳>-<sessionID>.jsonl —— session id 是
// 文件名中段,候选定位必须用包含匹配;精确 <id>.jsonl 模式永不命中,详情请求
// 每次都跌入 300 文件兜底全量解析(数百 MB),目标滑出各根 100-newest 窗口
// 更是直接 not found(审计 P5①)。Claude 的 <id>.jsonl 命名同样被包含模式覆盖。
func TestCandidateAgentVibeSessionFilesMatchesCodexRolloutNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	sessionID := "01a04aec-1111-2222-3333-444455556666"
	otherID := "ffffffff-aaaa-bbbb-cccc-ddddeeeeffff"

	codexDir := filepath.Join(home, ".codex", "sessions", "2026", "09", "29")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("mkdir codex: %v", err)
	}
	rollout := filepath.Join(codexDir, "rollout-2026-09-29T08-30-33-"+sessionID+".jsonl")
	if err := os.WriteFile(rollout, nil, 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}
	// 精确性反例:另一个会话的 rollout 不得被误命中。
	otherRollout := filepath.Join(codexDir, "rollout-2026-09-29T09-00-00-"+otherID+".jsonl")
	if err := os.WriteFile(otherRollout, nil, 0o644); err != nil {
		t.Fatalf("write other rollout: %v", err)
	}

	claudeDir := filepath.Join(home, ".claude", "projects", "-Users-mac-proj")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir claude: %v", err)
	}
	claudeFile := filepath.Join(claudeDir, sessionID+".jsonl")
	if err := os.WriteFile(claudeFile, nil, 0o644); err != nil {
		t.Fatalf("write claude: %v", err)
	}

	candidates := candidateAgentVibeSessionFiles(sessionID)
	found := map[string]bool{}
	for _, candidate := range candidates {
		found[candidate] = true
	}
	if !found[rollout] {
		t.Fatalf("candidates missing codex rollout %q: %v", rollout, candidates)
	}
	if !found[claudeFile] {
		t.Fatalf("candidates missing claude file %q: %v", claudeFile, candidates)
	}
	if found[otherRollout] {
		t.Fatalf("candidates wrongly include unrelated rollout %q: %v", otherRollout, candidates)
	}
}
