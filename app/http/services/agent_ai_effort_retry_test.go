package services

import (
	"errors"
	"strings"
	"testing"
)

// ---- 改动A:effort 被硬拒后的钳制重试决策 ----

func TestPlanAgentAIEffortRetry(t *testing.T) {
	cases := []struct {
		name         string
		requested    string
		levels       []string
		wantRetry    bool
		wantRetryEff string
	}{
		{"ultracode 降 max", "ultracode", []string{"low", "medium", "high", "xhigh", "max"}, true, "max"},
		{"无 max 档降 xhigh", "ultracode", []string{"low", "medium", "high", "xhigh"}, true, "xhigh"},
		{"全不支持→去掉 --effort", "ultracode", []string{"turbo"}, true, ""},
		{"清单缺失→不重试(交错误兜底)", "ultracode", nil, false, ""},
		{"请求空→不重试", "", []string{"low"}, false, ""},
	}
	for _, c := range cases {
		plan := planAgentAIEffortRetry(c.requested, c.levels)
		if plan.retry != c.wantRetry || plan.retryEffort != c.wantRetryEff {
			t.Fatalf("%s: planAgentAIEffortRetry(%q, %v) = %+v, want retry=%v effort=%q",
				c.name, c.requested, c.levels, plan, c.wantRetry, c.wantRetryEff)
		}
	}
}

// ---- 改动B:失败 cause 从 stderr 提炼可读原因 ----

func TestClaudeFailureCauseIncludesStderrEssence(t *testing.T) {
	stderr := "error: option '--effort <level>' argument 'ultracode' is invalid. It must be one of: low, medium, high, xhigh, max\n"
	cause := claudeFailureCause(errors.New("exit status 1"), claudeRetryInfo{}, stderr)
	if !strings.Contains(cause, "Claude CLI exited: exit status 1") {
		t.Fatalf("cause = %q, want bare exit prefix retained", cause)
	}
	if !strings.Contains(cause, "--effort") || !strings.Contains(cause, "ultracode") {
		t.Fatalf("cause = %q, want stderr essence included", cause)
	}
}

func TestClaudeFailureCauseBareWhenStderrEmpty(t *testing.T) {
	cause := claudeFailureCause(errors.New("exit status 1"), claudeRetryInfo{}, "  \n")
	if cause != "Claude CLI exited: exit status 1" {
		t.Fatalf("cause = %q, want legacy bare format", cause)
	}
}

func TestClaudeFailureCauseGatewayRetryUnchanged(t *testing.T) {
	ri := claudeRetryInfo{has: true, attempt: 10, max: 10, errorStatus: 502, errorType: "server_error"}
	stderr := "error: option '--effort <level>' argument 'ultracode' is invalid.\n"
	cause := claudeFailureCause(errors.New("exit status 1"), ri, stderr)
	if cause != "gateway 502 (server_error); retried 10/10" {
		t.Fatalf("cause = %q, want gateway retry format to keep precedence", cause)
	}
}

func TestConciseCLIStderrCause(t *testing.T) {
	if got := conciseCLIStderrCause("\n  \nfirst meaningful line\nsecond\n"); got != "first meaningful line" {
		t.Fatalf("conciseCLIStderrCause multiline = %q", got)
	}
	if got := conciseCLIStderrCause("   \n"); got != "" {
		t.Fatalf("conciseCLIStderrCause blank = %q, want empty", got)
	}
	long := strings.Repeat("x", 300)
	if got := conciseCLIStderrCause(long); len(got) != 200 {
		t.Fatalf("conciseCLIStderrCause long len = %d, want 200", len(got))
	}
}
