package models

// LogAPIResponse is the response wrapper for log endpoints
type LogAPIResponse struct {
	Code int         `json:"code"`
	Msg  string      `json:"msg"`
	Data interface{} `json:"data"`
}

// LogEntryResponse wraps a log entry for API response
type LogEntryResponse struct {
	Level     string `json:"level"`
	Timestamp string `json:"timestamp"`
	Message   string `json:"message"`
	Source    string `json:"source"`
	TraceID   string `json:"trace_id,omitempty"`
}

// LogLevelRequest is the request body for changing log level
type LogLevelRequest struct {
	Level string `json:"level"`
}

// LogConfigRequest is the request body for updating logger configuration
type LogConfigRequest struct {
	Level              string `json:"level,omitempty"`
	ErrorWindow        string `json:"errorWindow,omitempty"`
	MaxErrorCount      int    `json:"maxErrorCount,omitempty"`
	CleanupInterval    string `json:"cleanupInterval,omitempty"`
	FileLogPath        string `json:"fileLogPath,omitempty"`
	EnableFileRotation bool   `json:"enableFileRotation,omitempty"`
	MaxLogSize         int64  `json:"maxLogSize,omitempty"`
	MaxLogBackups      int    `json:"maxLogBackups,omitempty"`
	SentryDSN          string `json:"sentryDSN,omitempty"`
	EnableSentry       bool   `json:"enableSentry,omitempty"`
}

// LogsQueryParams represents query parameters for log retrieval
type LogsQueryParams struct {
	Limit  int
	Level  string
	Source string
}
