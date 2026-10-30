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

	candidates := candidateAgentVibeSessionFiles("", sessionID)
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

// 枚举根测试锁（审计 ③）：agentcore journal 读侧（详情候选 + 全量列举）的
// 扫描根必须与写侧 agentCoreJournalDir() 是同一目录。fixture 用写侧的目录
// 函数取路径——agent_detail.go 里一旦把根改回/改错字面量，两个枚举入口都
// 找不到文件，本测试即失败。
func TestAgentVibeSessionEnumerationIncludesAgentCoreJournalRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	root := agentCoreJournalDir()
	if root == "" {
		t.Fatal("agentCoreJournalDir() 为空，无法定位 journal 根")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir journal root: %v", err)
	}
	sessionID := "sess-abc"
	journalFile := filepath.Join(root, agentCoreJournalFileName(sessionID))
	if err := os.WriteFile(journalFile, []byte(`{"kind":"session"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write journal fixture: %v", err)
	}

	listContains := func(list []string, target string) bool {
		for _, item := range list {
			if item == target {
				return true
			}
		}
		return false
	}
	if candidates := candidateAgentVibeSessionFiles("", sessionID); !listContains(candidates, journalFile) {
		t.Fatalf("详情候选未覆盖 journal 根: want %q in %v", journalFile, candidates)
	}
	if all := allAgentVibeSessionDetailFiles(); !listContains(all, journalFile) {
		t.Fatalf("全量列举未覆盖 journal 根: want %q in %v", journalFile, all)
	}
}

// 2026-10-30 生产实锤回归钉:agentcore 详情请求的 source_session_id 是
// kernel-native handle(agentcore-<ts>-<n>),而 journal 文件名是手机会话 id
// (ai_<nanoid>.jsonl)——handle glob 永不命中,每次详情都跌入 4.5GB 兜底,
// 12s 围栏必炸(手机历史空白)。候选必须另按 sessionID 补一把 glob 直击。
func TestCandidateAgentVibeSessionFilesHitsJournalByPhoneSessionID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	root := agentCoreJournalDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir journal root: %v", err)
	}
	phoneID := "ai_1h_pnTOM2HIJ"
	journalFile := filepath.Join(root, agentCoreJournalFileName(phoneID))
	if err := os.WriteFile(journalFile, []byte(`{"kind":"session"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write journal fixture: %v", err)
	}

	candidates := candidateAgentVibeSessionFiles(phoneID, "agentcore-1793325934057639000-1")
	for _, candidate := range candidates {
		if candidate == journalFile {
			return
		}
	}
	t.Fatalf("sessionID glob 未命中 journal: want %q in %v", journalFile, candidates)
}
