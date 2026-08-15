package logger

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"aliang.one/nursorgate/common/cache"
	"aliang.one/nursorgate/common/version"
)

// LogLevel represents logging level
type LogLevelType int

const (
	TRACE LogLevelType = iota
	DEBUG
	INFO
	WARN
	ERROR
	FATAL
	PANIC
)

// LogConfig consolidates all logger configuration
type LogConfig struct {
	// Log level for main logger
	Level LogLevelType
	// Error deduplication time window
	ErrorWindow time.Duration
	// Maximum error count within time window
	MaxErrorCount int
	// Cleanup interval for expired errors
	CleanupInterval time.Duration
	// File logging path (main logger)
	FileLogPath string
	// Enable file rotation
	EnableFileRotation bool
	// Max log file size in bytes
	MaxLogSize int64
	// Max number of backups
	MaxLogBackups int
	// Sentry DSN
	SentryDSN string
	// Enable Sentry integration
	EnableSentry bool
}

// DefaultLogConfig returns default configuration
// Logs are stored in ~/.aliang/logs/ (or ALIANG_CACHE_DIR if set)
func DefaultLogConfig() *LogConfig {
	logDir, err := cache.GetCacheSubdir("logs")
	if err != nil {
		// Fallback to temp directory if cache system unavailable
		logDir = filepath.Join(os.TempDir(), "aliang", "logs")
	}

	return &LogConfig{
		Level:              sanitizeLogLevel(DEBUG),
		ErrorWindow:        1 * time.Hour,
		MaxErrorCount:      4,
		CleanupInterval:    2 * time.Hour,
		FileLogPath:        filepath.Join(logDir, "aliang_core.log"),
		EnableFileRotation: true,
		MaxLogSize:         10 * 1024 * 1024, // 10MB
		MaxLogBackups:      5,
		SentryDSN:          os.Getenv("SENTRY_DSN"),
		EnableSentry:       os.Getenv("SENTRY_DSN") != "",
	}
}

// HTTPLogConfig returns HTTP logger configuration
// Logs are stored in ~/.aliang/logs/ (or ALIANG_CACHE_DIR if set)
func HTTPLogConfig() *LogConfig {
	logDir, err := cache.GetCacheSubdir("logs")
	if err != nil {
		// Fallback to temp directory if cache system unavailable
		logDir = filepath.Join(os.TempDir(), "aliang", "logs")
	}

	return &LogConfig{
		Level:              sanitizeLogLevel(TRACE),
		FileLogPath:        filepath.Join(logDir, "aliang_http.log"),
		EnableFileRotation: true,
		MaxLogSize:         10 * 1024 * 1024, // 10MB
		MaxLogBackups:      3,
	}
}

// Global configuration instance
var (
	globalLogConfig   = DefaultLogConfig()
	globalLogConfigMu sync.RWMutex
)

// GetLogConfig returns current configuration
func GetLogConfig() *LogConfig {
	globalLogConfigMu.RLock()
	defer globalLogConfigMu.RUnlock()
	return globalLogConfig
}

// SetLogConfig updates the global configuration and propagates changes to existing loggers
func SetLogConfig(config *LogConfig) {
	SetLogConfigWithOverride(config, false)
}

// SetLogConfigWithOverride updates the global configuration and optionally
// allows debug/trace levels even in prod builds.
func SetLogConfigWithOverride(config *LogConfig, allowProdLowLevel bool) {
	globalLogConfigMu.Lock()
	defer globalLogConfigMu.Unlock()
	if config != nil {
		config.Level = normalizeLogLevel(config.Level, allowProdLowLevel)
		globalLogConfig = config
		// Propagate config to existing mainLogger instance if already created
		if ml, ok := mainLoggerInstance.(*mainLogger); ok && ml != nil {
			ml.config = config
		}
	}
}

// UpdateLogLevel updates the log level dynamically
func UpdateLogLevel(level LogLevelType) {
	UpdateLogLevelWithOverride(level, false)
}

// UpdateLogLevelWithOverride updates the log level dynamically and optionally
// allows debug/trace levels even in prod builds.
func UpdateLogLevelWithOverride(level LogLevelType, allowProdLowLevel bool) {
	globalLogConfigMu.Lock()
	defer globalLogConfigMu.Unlock()
	globalLogConfig.Level = normalizeLogLevel(level, allowProdLowLevel)
}

func sanitizeLogLevel(level LogLevelType) LogLevelType {
	return normalizeLogLevel(level, false)
}

func normalizeLogLevel(level LogLevelType, allowProdLowLevel bool) LogLevelType {
	if allowProdLowLevel {
		return level
	}
	if version.IsProdBuild() && level < INFO {
		return INFO
	}
	return level
}

// Legacy compatibility - keep ErrorDedupConfig for backward compatibility
type ErrorDedupConfig struct {
	ErrorWindow     time.Duration
	MaxErrorCount   int
	CleanupInterval time.Duration
}

// SetErrorDedupConfig updates configuration (legacy function)
func SetErrorDedupConfig(config *ErrorDedupConfig) {
	if config != nil {
		globalLogConfigMu.Lock()
		defer globalLogConfigMu.Unlock()
		globalLogConfig.ErrorWindow = config.ErrorWindow
		globalLogConfig.MaxErrorCount = config.MaxErrorCount
		globalLogConfig.CleanupInterval = config.CleanupInterval
	}
}
