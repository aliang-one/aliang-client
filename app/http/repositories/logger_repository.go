package repositories

import (
	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/common/logger"
)

// LoggerRepositoryImpl provides access to logger functionality
type LoggerRepositoryImpl struct {
	logService       *services.LogService
	logConfigService *services.LogConfigService
}

// NewLoggerRepository creates a new logger repository instance
func NewLoggerRepository() *LoggerRepositoryImpl {
	return &LoggerRepositoryImpl{
		logService:       services.NewLogService(),
		logConfigService: services.NewLogConfigService(),
	}
}

// GetLogs retrieves logs with filtering
func (lr *LoggerRepositoryImpl) GetLogs(params models.LogsQueryParams) []models.LogEntryResponse {
	return lr.logService.GetLogs(params)
}

// ClearLogs clears the log buffer
func (lr *LoggerRepositoryImpl) ClearLogs() error {
	return lr.logService.ClearLogs()
}

// UpdateLogLevel updates the log level
func (lr *LoggerRepositoryImpl) UpdateLogLevel(levelStr string) (logger.LogLevelType, error) {
	return lr.logService.UpdateLogLevel(levelStr)
}

// SubscribeLogStream subscribes to real-time log stream
func (lr *LoggerRepositoryImpl) SubscribeLogStream() (<-chan *logger.LogEntry, func()) {
	return lr.logService.SubscribeLogStream()
}

// GetConfig retrieves the current logger configuration
func (lr *LoggerRepositoryImpl) GetConfig() map[string]interface{} {
	return lr.logConfigService.GetConfig()
}

// UpdateConfig updates logger configuration
func (lr *LoggerRepositoryImpl) UpdateConfig(req models.LogConfigRequest) error {
	return lr.logConfigService.UpdateConfig(req)
}
