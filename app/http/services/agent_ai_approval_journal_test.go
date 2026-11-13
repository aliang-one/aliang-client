package services

// 审批请求投递 journal 测试(L1 可靠投递,2026-11-01 三端一致性审计根因 1):
//   1. remember 落盘、ack 清除、重放原样重发(approval_id 幂等键);
//   2. journal 开关关时仅内存(不落盘);开时写 ai_approval_requests.json;
//   3. 进程重启回灌(load)→ 重放照旧(服务端 intake 幂等兜底)。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aliang.one/nursorgate/common/cache"
)

func newJournalTestManager(t *testing.T) *agentAIManager {
	t.Helper()
	// 隔离:ALIANG_CACHE_DIR 直指 TempDir(agentStatePath 非 user-agent 分支
	// 走 cache.GetCacheSubdir,单例必须重置,否则跨测试污染)。
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	t.Cleanup(cache.ResetCacheDirForTest)
	m := newAgentAIManager()
	m.approvalJournalEnabled = true
	return m
}

func approvalJournalPayload(id string) map[string]interface{} {
	return map[string]interface{}{
		"type":        "ai.approval.request",
		"approval_id": id,
		"session_id":  "s-j",
		"kind":        "command",
		"command":     "git push",
	}
}

func readJournalFileIDs(t *testing.T) []string {
	t.Helper()
	path, err := agentAIApprovalJournalPath()
	if err != nil {
		t.Fatalf("journal path: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	var values []map[string]interface{}
	if json.Unmarshal(raw, &values) != nil {
		t.Fatalf("unmarshal journal: %s", string(raw))
	}
	ids := make([]string, 0, len(values))
	for _, v := range values {
		ids = append(ids, remoteString(v, "approval_id"))
	}
	return ids
}

func TestApprovalJournalRememberAckReplay(t *testing.T) {
	m := newJournalTestManager(t)
	var sent []string
	write := func(payload interface{}) error {
		if row, ok := payload.(map[string]interface{}); ok {
			sent = append(sent, remoteString(row, "approval_id"))
		}
		return nil
	}

	m.rememberPendingApprovalRequest(approvalJournalPayload("ap-1"))
	m.rememberPendingApprovalRequest(approvalJournalPayload("ap-2"))
	ids := readJournalFileIDs(t)
	if len(ids) != 2 {
		t.Fatalf("journal file should hold 2 entries, got %v", ids)
	}

	// 重放:两条原样补发,顺序不承诺但必须齐全
	m.replayPendingApprovalRequests(write)
	joined := strings.Join(sent, ",")
	if len(sent) != 2 || !strings.Contains(joined, "ap-1") || !strings.Contains(joined, "ap-2") {
		t.Fatalf("replay should resend both, got %v", sent)
	}

	// ack 清除 ap-1 → 文件与内存同步收敛
	m.acknowledgePendingApprovalRequest("ap-1")
	ids = readJournalFileIDs(t)
	if len(ids) != 1 || ids[0] != "ap-2" {
		t.Fatalf("after ack only ap-2 remains, got %v", ids)
	}
	sent = nil
	m.replayPendingApprovalRequests(write)
	if len(sent) != 1 || sent[0] != "ap-2" {
		t.Fatalf("replay after ack should resend only ap-2, got %v", sent)
	}
}

func TestApprovalJournalDisabledKeepsMemoryOnly(t *testing.T) {
	m := newJournalTestManager(t)
	m.approvalJournalEnabled = false
	m.rememberPendingApprovalRequest(approvalJournalPayload("ap-mem"))
	if _, err := os.Stat(mustJournalPath(t)); !os.IsNotExist(err) {
		t.Fatalf("journal disabled must not write file, stat err=%v", err)
	}
	var sent []string
	m.replayPendingApprovalRequests(func(payload interface{}) error {
		if row, ok := payload.(map[string]interface{}); ok {
			sent = append(sent, remoteString(row, "approval_id"))
		}
		return nil
	})
	if len(sent) != 1 {
		t.Fatalf("in-memory entry should still replay, got %v", sent)
	}
}

func TestApprovalJournalLoadRestoresFromDisk(t *testing.T) {
	m := newJournalTestManager(t)
	m.rememberPendingApprovalRequest(approvalJournalPayload("ap-crash"))

	// 模拟进程重启:全新 manager 回灌(共享同一 ALIANG_CACHE_DIR,与第一个
	// manager 同目录——journal 文件就是跨进程载体)。
	m2 := newAgentAIManager()
	m2.approvalJournalEnabled = true
	m2.loadPendingApprovalRequests()
	var sent []string
	m2.replayPendingApprovalRequests(func(payload interface{}) error {
		if row, ok := payload.(map[string]interface{}); ok {
			sent = append(sent, remoteString(row, "approval_id"))
		}
		return nil
	})
	if len(sent) != 1 || sent[0] != "ap-crash" {
		t.Fatalf("restarted manager should replay crashed pending, got %v", sent)
	}
}

func mustJournalPath(t *testing.T) string {
	t.Helper()
	path, err := agentAIApprovalJournalPath()
	if err != nil {
		t.Fatalf("journal path: %v", err)
	}
	_ = filepath.Dir(path)
	return path
}
