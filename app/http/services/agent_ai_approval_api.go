package services

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

func (s *AgentService) HandleAIApprovalHook(ctx context.Context, sessionID string, messageID string, token string, payload map[string]interface{}) (map[string]interface{}, error) {
	if s == nil || s.ai == nil {
		return claudeApprovalHookDecision(claudeApprovalHookEventName(payload), false, "Aliang AI approval service is not ready."), errors.New("AI approval service is not ready")
	}
	return s.ai.handleClaudeApprovalHook(ctx, sessionID, messageID, token, payload)
}

func SetAgentAIApprovalHookBaseURL(raw string) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return
	}
	agentAIApprovalHookBaseURLMu.Lock()
	agentAIApprovalHookBaseURL = raw
	agentAIApprovalHookBaseURLMu.Unlock()
}

func agentAIApprovalHookURL(sessionID string, messageID string, token string) string {
	agentAIApprovalHookBaseURLMu.RLock()
	base := agentAIApprovalHookBaseURL
	agentAIApprovalHookBaseURLMu.RUnlock()
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = UserAgentBaseURL()
	}
	values := url.Values{}
	values.Set("session_id", sessionID)
	values.Set("message_id", messageID)
	values.Set("token", token)
	return base + "/api/agent/ai/approval-hook?" + values.Encode()
}

func currentAgentAIApprovalHookBaseURL() string {
	agentAIApprovalHookBaseURLMu.RLock()
	base := agentAIApprovalHookBaseURL
	agentAIApprovalHookBaseURLMu.RUnlock()
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = UserAgentBaseURL()
	}
	return base
}

// 撤回截断四类拒绝的机器可读 code（ai.error 载荷的 error_code 键，供
// server/前端精确分支，先例 externalTUIBusyErrCode 的 ErrorCode() 机制）。

const (
	truncateErrCodeRunActive          = "truncate_run_active"
	truncateErrCodeJournalUnavailable = "truncate_journal_unavailable"
	truncateErrCodeFailed             = "truncate_failed"
	truncateErrCodeRebuildFailed      = "truncate_rebuild_failed"
)

// agentAITruncateError 是 create() 截断路径的拒绝错误：带 code 字段的单一
// 类型覆盖四成因，agentAIErrorPayload 经 ErrorCode() 发出 error_code。
// 消息文本与旧版 errors.New/fmt.Errorf 逐字一致（行为不变，仅多一个键）。

type agentAITruncateError struct {
	code string
	msg  string
	err  error // 可选底层成因（truncate/journal 读失败），Unwrap 透出
}

func (e *agentAITruncateError) Error() string {
	if e.err != nil {
		return e.msg + ": " + e.err.Error()
	}
	return e.msg
}

func (e *agentAITruncateError) ErrorCode() string { return e.code }

func (e *agentAITruncateError) Unwrap() error { return e.err }
