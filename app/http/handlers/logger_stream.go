package handlers

import (
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
)

// websocket upgrader for log streaming
var logUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		host := strings.Split(r.Host, ":")[0]
		return host == "localhost" || host == "127.0.0.1"
	},
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
