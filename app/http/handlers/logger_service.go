package handlers

import (
	"sync/atomic"

	"aliang.one/nursorgate/common/logger"
)

// LogService handles log retrieval and management operations
type LogService struct{}

// NewLogService creates a new log service instance
func NewLogService() *LogService {
	return &LogService{}
}

// GetLogs retrieves logs from the buffer with filtering
func (ls *LogService) GetLogs(params LogsQueryParams) []LogEntryResponse {
	// Convert level string to LogLevelType
	var level logger.LogLevelType
	if params.Level != "" {
		parsedLevel, err := StringToLogLevelType(params.Level)
		if err == nil {
			level = parsedLevel
		} else {
			level = 0 // All levels on invalid input
		}
	}

	// Get logs from buffer
	entries := logger.GetBufferEntries(params.Limit, level, params.Source)

	// Convert to response format
	var responses []LogEntryResponse
	for _, entry := range entries {
		responses = append(responses, LogEntryResponse{
			Level:     LogLevelTypeToString(entry.Level),
			Timestamp: entry.Timestamp.Format("2006-01-02 15:04:05.000"),
			Message:   redactSensitiveLogMessage(entry.Message),
			Source:    entry.Source,
			TraceID:   entry.TraceID,
		})
	}

	return responses
}

// ClearLogs clears the log buffer
func (ls *LogService) ClearLogs() error {
	logger.ClearBuffer()
	return nil
}

// UpdateLogLevel updates only the log level
func (ls *LogService) UpdateLogLevel(levelStr string) (logger.LogLevelType, error) {
	level, err := StringToLogLevelType(levelStr)
	if err != nil {
		return 0, err
	}

	logger.UpdateLogLevel(level)
	return level, nil
}

// SubscribeLogStream subscribes to real-time log stream
// Returns a channel that receives log entries
func (ls *LogService) SubscribeLogStream() (<-chan *logger.LogEntry, func()) {
	logChan := make(chan *logger.LogEntry, 100)
	closed := int32(0) // Use atomic operations for thread safety

	observer := func(entry *logger.LogEntry) {
		// Check if channel is closed before sending
		if atomic.LoadInt32(&closed) == 1 {
			return
		}

		// Try to send without blocking
		select {
		case logChan <- entry:
			// Message sent successfully
		default:
			// Channel full, drop the entry to avoid blocking
			// This is acceptable for log streaming
		}
	}

	// Register observer
	unsubscribe := logger.GetGlobalBuffer().Subscribe(observer)

	// Return channel and cleanup function
	cleanup := func() {
		// Mark as closed atomically first
		if !atomic.CompareAndSwapInt32(&closed, 0, 1) {
			return
		}

		unsubscribe()

		// Drain any remaining messages to prevent blocking
		go func() {
			for {
				select {
				case <-logChan:
					// Drain remaining messages
				default:
					// No more messages, close the channel
					close(logChan)
					return
				}
			}
		}()
	}

	return logChan, cleanup
}
