package services

// TUI 执行回合的机器可读错误码。与 agentExternalTUIBusyError 同构：实现
// ErrorCode() interface，由 agentAIErrorPayload 经 errors.As 自动拾取，挂到
// ai.error 的 error_code 字段上（agent_ai.go:7613-7617）——服务端与手机据此
// 分支，无需对错误字符串做脆弱匹配。
const (
	agentTUIErrUnavailable    = "tui_unavailable"     // 注入前判定中途失效（TUI 退出/记录消失）或回合超上限
	agentTUIErrInjectFailed   = "tui_inject_failed"   // 拨号或写入 UDS 失败
	agentTUIErrHeld           = "tui_held"            // 接收方（bypass 门）静默扣留注入消息
	agentTUIErrTranscriptLost = "tui_transcript_lost" // tail 期间 transcript 消失
)

// agentTUITurnError 是注入 TUI 回合的结构化失败。msg 面向人（日志/手机提示），
// code 面向程序。
type agentTUITurnError struct {
	code string
	msg  string
}

func (e agentTUITurnError) Error() string { return e.msg }

func (e agentTUITurnError) ErrorCode() string { return e.code }

func newAgentTUITurnError(code, msg string) error {
	return agentTUITurnError{code: code, msg: msg}
}
