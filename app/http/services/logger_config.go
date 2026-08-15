package services

import (
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/common/version"
)

// LogConfigService handles logger configuration operations
type LogConfigService struct{}

// NewLogConfigService creates a new log config service instance
func NewLogConfigService() *LogConfigService {
	return &LogConfigService{}
}

// GetConfig retrieves the current logger configuration
func (lcs *LogConfigService) GetConfig() map[string]interface{} {
	config := logger.GetLogConfig()

	return map[string]interface{}{
		"level":              LogLevelTypeToString(config.Level),
		"errorWindow":        config.ErrorWindow.String(),
		"maxErrorCount":      config.MaxErrorCount,
		"cleanupInterval":    config.CleanupInterval.String(),
		"fileLogPath":        config.FileLogPath,
		"enableFileRotation": config.EnableFileRotation,
		"maxLogSize":         config.MaxLogSize,
		"maxLogBackups":      config.MaxLogBackups,
		"sentryDSN":          maskSensitiveData(config.SentryDSN),
		"enableSentry":       config.EnableSentry,
	}
}

// UpdateConfig updates the logger configuration with provided values
// Only updates fields that are provided in the request
func (lcs *LogConfigService) UpdateConfig(req models.LogConfigRequest) error {
	return lcs.UpdateConfigWithOverride(req, false)
}

// UpdateConfigWithOverride updates the logger configuration with provided
// values and can optionally allow debug/trace levels in prod builds.
func (lcs *LogConfigService) UpdateConfigWithOverride(req models.LogConfigRequest, allowProdLowLevel bool) error {
	config := logger.GetLogConfig()
	updated := false

	// Update level if provided
	if req.Level != "" {
		level, err := StringToLogLevelType(req.Level)
		if err != nil {
			return err
		}
		if version.IsProdBuild() && level < logger.INFO && !allowProdLowLevel {
			return ErrProdLogLevelTooLow
		}
		config.Level = level
		updated = true
	}

	// Update error window if provided
	if req.ErrorWindow != "" {
		d, err := time.ParseDuration(req.ErrorWindow)
		if err != nil {
			return ErrInvalidDurationFormat
		}
		config.ErrorWindow = d
		updated = true
	}

	// Update max error count if provided
	if req.MaxErrorCount > 0 {
		config.MaxErrorCount = req.MaxErrorCount
		updated = true
	}

	// Update cleanup interval if provided
	if req.CleanupInterval != "" {
		d, err := time.ParseDuration(req.CleanupInterval)
		if err != nil {
			return ErrInvalidDurationFormat
		}
		config.CleanupInterval = d
		updated = true
	}

	// Update file log path if provided
	if req.FileLogPath != "" {
		config.FileLogPath = req.FileLogPath
		updated = true
	}

	// Update enable file rotation if provided
	if req.EnableFileRotation {
		config.EnableFileRotation = req.EnableFileRotation
		updated = true
	}

	// Update max log size if provided
	if req.MaxLogSize > 0 {
		config.MaxLogSize = req.MaxLogSize
		updated = true
	}

	// Update max log backups if provided
	if req.MaxLogBackups > 0 {
		config.MaxLogBackups = req.MaxLogBackups
		updated = true
	}

	// Update Sentry DSN if provided
	if req.SentryDSN != "" {
		config.SentryDSN = req.SentryDSN
		config.EnableSentry = true
		updated = true
	}

	// Update enable Sentry if explicitly set
	if req.EnableSentry {
		config.EnableSentry = req.EnableSentry
		updated = true
	}

	if !updated {
		return ErrNoValidConfigFields
	}

	// Save updated config
	logger.SetLogConfigWithOverride(config, allowProdLowLevel)
	return nil
}
