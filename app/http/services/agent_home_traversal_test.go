package services

import (
	"strings"
	"testing"
)

// TestHomeRootTraversalReason 覆盖家目录根遍历的拦截判据。
//
// 判据要「拦得准」而不是「拦得多」：`find ~` 必须拦（它扫进 ~/Music、
// ~/Pictures 会弹 TCC 框并卡死），但 `find ~/MyProgram/proj -name x` 这种
// 已经指明子目录的搜索必须放行——那是正常的范围明确操作。
func TestHomeRootTraversalReason(t *testing.T) {
	// 固定家目录，避免测试依赖真实机器路径。
	old := agentcoreHomeDir
	agentcoreHomeDir = func() (string, error) { return "/Users/tester", nil }
	defer func() { agentcoreHomeDir = old }()

	blocked := []string{
		`find ~ -maxdepth 4 -iname "*aliang*"`,
		`find ~ -maxdepth 4 -iname "*aliang*" -not -path "*/node_modules/*" 2>/dev/null | head -50`,
		`ls -d ~/*aliang* ~/*/*aliang* 2>/dev/null`,
		`ls ~`,
		`ls -la ~/`,
		`grep -rn foo ~`,
		`grep -rl "x" ~/*`,
		`du -sh ~`,
		`find / -name x`,
		`find /Users -name x`,
		`find /Users/tester -maxdepth 2 -name x`,
		`cd /tmp && find ~ -name y`,
		`mdfind -onlyin ~ "kind:folder"`,
	}
	for _, command := range blocked {
		if reason := homeRootTraversalReason(command); reason == "" {
			t.Errorf("应当拦截: %s", command)
		}
	}

	allowed := []string{
		`find . -maxdepth 3 -type d`,
		`find ~/MyProgram/proj -name "*.go"`,
		`find /Users/tester/MyProgram/proj -name "*.go"`,
		`ls -la`,
		`ls src/components`,
		`grep -rn foo ./src`,
		`grep -rn foo /Users/tester/MyProgram/proj`,
		`cat ~/.zsh_history`,
		`git status`,
		`go test ./...`,
		`rm -rf ./build 2>/dev/null`,
	}
	for _, command := range allowed {
		if reason := homeRootTraversalReason(command); reason != "" {
			t.Errorf("不应拦截: %s（理由: %s）", command, reason)
		}
	}
}

// TestHomeRootTraversalReasonIsActionable 拒绝理由必须是可执行的改道建议，
// 否则模型只会原样重试。
func TestHomeRootTraversalReasonIsActionable(t *testing.T) {
	reason := homeRootTraversalReason("find ~ -maxdepth 4")
	if reason == "" {
		t.Fatal("find ~ 未被拦截")
	}
	for _, want := range []string{"隐私库", "项目目录", "find ."} {
		if !strings.Contains(reason, want) {
			t.Errorf("拒绝理由缺少关键信息 %q: %s", want, reason)
		}
	}
}

// TestShellPolicyRejectsHomeTraversal 验证拦截真的接到了执行链路上
// （agentCoreShellPolicy.Allow 是 runShell 唯一的放行判据）。
func TestShellPolicyRejectsHomeTraversal(t *testing.T) {
	old := agentcoreHomeDir
	agentcoreHomeDir = func() (string, error) { return "/Users/tester", nil }
	defer func() { agentcoreHomeDir = old }()

	p := &agentCoreShellPolicy{}
	if allowed, reason := p.Allow(`find ~ -maxdepth 4 -iname "*aliang*"`); allowed || reason == "" {
		t.Fatalf("find ~ 应被拒绝并给出理由，实际 allowed=%v reason=%q", allowed, reason)
	}
	if allowed, reason := p.Allow(`find . -name "*.go"`); !allowed {
		t.Fatalf("项目内搜索不应被拒绝: %s", reason)
	}
}
