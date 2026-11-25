package handlers

import (
	"net/http"

	"github.com/gorilla/websocket"
)

// logUpgrader is the websocket upgrader for log streaming.
//
// CheckOrigin 复用聊天 WS 的同源闸 aiStreamHostAllowed(2026-11-24 审计):旧
// 实现只看 r.Host 是否为 localhost/127.0.0.1,对 Origin 头看都不看——用户
// 浏览器里任何网页都能开 ws://127.0.0.1:56431/api/logs/stream 读实时日志
// (仅按关键字脱敏),这条向量不依赖 ssh_ingress。带 Origin 必须真同源;不带
// Origin(脚本直连)仍要求回环 Host。握手鉴权另由默认中间件栈的远程访问闸
// 承担(非回环来源须带有效 dashboard 会话)。
var logUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     aiStreamHostAllowed,
}

// LogStreamHandler handles WebSocket connections for real-time log streaming
type LogStreamHandler struct {
	logService *LogService
}

// NewLogStreamHandler creates a new log stream handler instance
func NewLogStreamHandler() *LogStreamHandler {
	return &LogStreamHandler{
		logService: NewLogService(),
	}
}

// HandleLogStream handles WebSocket connection for log streaming
// Path: /api/logs/stream
func (lsh *LogStreamHandler) HandleLogStream(w http.ResponseWriter, r *http.Request) {
	conn, err := logUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Subscribe to log stream
	logChan, cleanup := lsh.logService.SubscribeLogStream()
	defer cleanup()

	// Send logs to client
	for entry := range logChan {
		response := LogEntryResponse{
			Level:     LogLevelTypeToString(entry.Level),
			Timestamp: entry.Timestamp.Format("2006-01-02 15:04:05.000"),
			Message:   redactSensitiveLogMessage(entry.Message),
			Source:    entry.Source,
			TraceID:   entry.TraceID,
		}

		if err := conn.WriteJSON(response); err != nil {
			break
		}
	}
}
