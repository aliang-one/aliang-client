package services

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/common/version"
)

// Custom errors for logger operations
var (
	ErrInvalidLogLevel       = errors.New("invalid log level")
	ErrInvalidDurationFormat = errors.New("invalid duration format")
	ErrNoValidConfigFields   = errors.New("no valid configuration fields provided")
	ErrInvalidRequestBody    = errors.New("invalid request body")
	ErrProdLogLevelTooLow    = errors.New("prod build requires log level INFO or above")
)

// LogService handles log retrieval and management operations
type LogService struct{}

// NewLogService creates a new log service instance
func NewLogService() *LogService {
	return &LogService{}
}

// GetLogs retrieves logs from the buffer with filtering
func (ls *LogService) GetLogs(params models.LogsQueryParams) []models.LogEntryResponse {
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
	var responses []models.LogEntryResponse
	for _, entry := range entries {
		responses = append(responses, models.LogEntryResponse{
			Level:     LogLevelTypeToString(entry.Level),
			Timestamp: entry.Timestamp.Format("2006-01-02 15:04:05.000"),
			Message:   entry.Message,
			Source:    entry.Source,
			TraceID:   entry.TraceID,
		})
	}

	if len(responses) == 0 {
		return ls.readLogsFromFiles(params, level)
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
	return ls.UpdateLogLevelWithOverride(levelStr, false)
}

// UpdateLogLevelWithOverride updates the log level dynamically and can
// optionally allow debug/trace levels in prod builds.
func (ls *LogService) UpdateLogLevelWithOverride(levelStr string, allowProdLowLevel bool) (logger.LogLevelType, error) {
	levelUpStr := strings.ToUpper(levelStr)
	level, err := StringToLogLevelType(levelUpStr)
	if err != nil {
		return 0, err
	}
	if version.IsProdBuild() && level < logger.INFO && !allowProdLowLevel {
		return 0, ErrProdLogLevelTooLow
	}

	logger.UpdateLogLevelWithOverride(level, allowProdLowLevel)
	return level, nil
}

// SubscribeLogStream subscribes to real-time log stream
// Returns a channel that receives log entries and a cleanup function
func (ls *LogService) SubscribeLogStream() (<-chan *logger.LogEntry, func()) {
	logChan := make(chan *logger.LogEntry, 100)

	observer := func(entry *logger.LogEntry) {
		select {
		case logChan <- entry:
		default:
			// Channel full, drop the entry
		}
	}

	unsubscribe := logger.GetGlobalBuffer().Subscribe(observer)

	// Return channel and cleanup function
	cleanup := func() {
		unsubscribe()
		close(logChan)
	}

	return logChan, cleanup
}

// LogLevelTypeToString converts logger.LogLevelType to string
func LogLevelTypeToString(level logger.LogLevelType) string {
	switch level {
	case logger.TRACE:
		return "TRACE"
	case logger.DEBUG:
		return "DEBUG"
	case logger.INFO:
		return "INFO"
	case logger.WARN:
		return "WARN"
	case logger.ERROR:
		return "ERROR"
	case logger.FATAL:
		return "FATAL"
	case logger.PANIC:
		return "PANIC"
	default:
		return "UNKNOWN"
	}
}

// StringToLogLevelType converts string to logger.LogLevelType
func StringToLogLevelType(levelStr string) (logger.LogLevelType, error) {
	switch levelStr {
	case "TRACE":
		return logger.TRACE, nil
	case "DEBUG":
		return logger.DEBUG, nil
	case "INFO":
		return logger.INFO, nil
	case "WARN":
		return logger.WARN, nil
	case "ERROR":
		return logger.ERROR, nil
	case "FATAL":
		return logger.FATAL, nil
	case "PANIC":
		return logger.PANIC, nil
	default:
		return 0, ErrInvalidLogLevel
	}
}

// maskSensitiveData masks sensitive information like DSN
func maskSensitiveData(data string) string {
	if data == "" {
		return ""
	}
	if len(data) <= 8 {
		return "***"
	}
	return data[:8] + "***"
}

func (ls *LogService) readLogsFromFiles(params models.LogsQueryParams, level logger.LogLevelType) []models.LogEntryResponse {
	logDir, err := cache.GetCacheSubdir("logs")
	if err != nil {
		return []models.LogEntryResponse{}
	}

	candidates := []struct {
		source string
		path   string
	}{
		{source: "main", path: filepath.Join(logDir, "aliang_core.log")},
		{source: "http", path: filepath.Join(logDir, "aliang_http.log")},
	}

	var responses []models.LogEntryResponse
	for _, candidate := range candidates {
		if params.Source != "" && params.Source != candidate.source {
			continue
		}
		responses = append(responses, readRecentLogFileEntries(candidate.path, candidate.source, params.Limit, level)...)
	}

	sort.Slice(responses, func(i, j int) bool {
		return responses[i].Timestamp < responses[j].Timestamp
	})

	limit := params.Limit
	if limit <= 0 {
		limit = 100
	}
	if len(responses) > limit {
		responses = responses[len(responses)-limit:]
	}

	return responses
}

func readRecentLogFileEntries(path, source string, limit int, level logger.LogLevelType) []models.LogEntryResponse {
	file, err := os.Open(path)
	if err != nil {
		return []models.LogEntryResponse{}
	}
	defer file.Close()

	if limit <= 0 {
		limit = 100
	}

	scanner := bufio.NewScanner(file)
	lines := make([]string, 0, limit)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(lines) == limit {
			copy(lines, lines[1:])
			lines[len(lines)-1] = line
		} else {
			lines = append(lines, line)
		}
	}

	var responses []models.LogEntryResponse
	for _, line := range lines {
		entry, ok := parseLogLine(line, source)
		if !ok {
			continue
		}
		if level != 0 {
			parsedLevel, err := StringToLogLevelType(strings.ToUpper(entry.Level))
			if err != nil || parsedLevel != level {
				continue
			}
		}
		responses = append(responses, entry)
	}

	return responses
}

func parseLogLine(line, source string) (models.LogEntryResponse, bool) {
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 3 {
		return models.LogEntryResponse{}, false
	}

	timeParts := strings.SplitN(parts[2], ": ", 2)
	if len(timeParts) < 2 {
		return models.LogEntryResponse{}, false
	}

	timestampRaw := fmt.Sprintf("%s %s", parts[0], parts[1])
	parsedTime, err := time.Parse("2006/01/02 15:04:05", timestampRaw)
	if err != nil {
		return models.LogEntryResponse{}, false
	}

	message := timeParts[1]
	level := "INFO"
	if start := strings.Index(message, "["); start >= 0 {
		if end := strings.Index(message[start:], "]"); end > 0 {
			level = strings.ToUpper(message[start+1 : start+end])
			message = strings.TrimSpace(message[start+end+1:])
		}
	}

	return models.LogEntryResponse{
		Level:     level,
		Timestamp: parsedTime.Format("2006-01-02 15:04:05.000"),
		Message:   message,
		Source:    source,
	}, true
}
