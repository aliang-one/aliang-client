package services

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// 三道版本门(档位守卫/审批钩子策略/slash 列表)必须使用同一阈值——
// 2026-10-16 评审曾发现守卫放宽到 2.1.268 而钩子策略仍卡 2.2.0(永假门):
// full 档加载用户 settings 的 ask 规则却接旧 PreToolUse 桥,手机批了也被
// claude 拒绝。本测试用同一版本阶梯钉死「守卫放行 ⟺ 策略走 PermissionRequest」。
func TestTierGuardAndStrategyGatesAligned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh-script version probes are unix-only; windows coverage tracked separately")
	}
	for _, tc := range []struct {
		version   string
		gateOpen  bool
	}{
		{"2.1.17", false},
		{"2.1.267", false},
		{"2.1.268", true},
		{"2.1.286", true},
		{"2.1.300", true},
	} {
		path := filepath.Join(t.TempDir(), "claude-"+tc.version)
		if err := os.WriteFile(path, []byte("#!/bin/sh\necho \""+tc.version+" (Claude Code)\"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		policy := parseAgentAIClaudeRemotePolicy(map[string]interface{}{"claude_remote_policy": map[string]interface{}{"trust_level": "full"}})
		kept := applyClaudeTierVersionGuard(&agentAITool{path: path}, policy, "s-align")
		strategy := claudeApprovalHookStrategyForVersion(tc.version)
		guardOpen := kept.trustTier == "full"
		strategyOpen := strategy == claudeApprovalHookPermissionRequestHTTP
		if guardOpen != tc.gateOpen || strategyOpen != tc.gateOpen {
			t.Errorf("version %s: guardOpen=%v strategyOpen=%v, want both %v", tc.version, guardOpen, strategyOpen, tc.gateOpen)
		}
	}
}
