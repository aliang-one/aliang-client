package services

type claudeRetryInfo struct {
	has         bool
	attempt     int
	max         int
	delayMs     float64
	errorStatus int // upstream HTTP status that triggered the retry (e.g. 502)
	errorType   string
}

// claudeRunDiagnostics 聚合 claude CLI 通道的逐回合诊断快照，供进度心跳与
// run 终帧消费——诊断面板的 CLI 数据源（与 agentCoreDiagnostics 对应）。
// CLI 的用量在 finalized assistant 事件的 message.usage（anthropic 语义），
// 回合数按 assistant 帧计、result 事件的 num_turns 为权威终值；写侧是
// stdout 流 goroutine（streamStructuredAIDelta），读侧是进度 ticker 与
// runCLIPass 收尾，跨 goroutine 用互斥锁保护。nil 接收者全部安全短路。
