package services

import (
	"os"
	"testing"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

func TestLogServiceUpdateLogLevelRejectsBelowInfoInProd(t *testing.T) {
	original := cloneLogConfig(logger.GetLogConfig())
	restoreBuildMode := setBuildModeForTest(t, "prod")
	t.Cleanup(func() {
		restoreBuildMode()
		logger.SetLogConfig(original)
	})

	service := NewLogService()
	if _, err := service.UpdateLogLevel("DEBUG"); err != ErrProdLogLevelTooLow {
		t.Fatalf("UpdateLogLevel(DEBUG) error = %v, want %v", err, ErrProdLogLevelTooLow)
	}

	level, err := service.UpdateLogLevel("INFO")
	if err != nil {
		t.Fatalf("UpdateLogLevel(INFO) error = %v, want nil", err)
	}
	if level != logger.INFO {
		t.Fatalf("UpdateLogLevel(INFO) level = %v, want %v", level, logger.INFO)
	}
}

func TestLogServiceUpdateLogLevelAllowsDebugOverrideInProd(t *testing.T) {
	original := cloneLogConfig(logger.GetLogConfig())
	restoreBuildMode := setBuildModeForTest(t, "prod")
	t.Cleanup(func() {
		restoreBuildMode()
		logger.SetLogConfig(original)
	})

	service := NewLogService()
	level, err := service.UpdateLogLevelWithOverride("DEBUG", true)
	if err != nil {
		t.Fatalf("UpdateLogLevelWithOverride(DEBUG, true) error = %v, want nil", err)
	}
	if level != logger.DEBUG {
		t.Fatalf("UpdateLogLevelWithOverride(DEBUG, true) level = %v, want %v", level, logger.DEBUG)
	}
	if got := logger.GetLogConfig().Level; got != logger.DEBUG {
		t.Fatalf("logger.GetLogConfig().Level = %v, want %v", got, logger.DEBUG)
	}
}

func TestLogConfigServiceRejectsBelowInfoInProd(t *testing.T) {
	original := cloneLogConfig(logger.GetLogConfig())
	restoreBuildMode := setBuildModeForTest(t, "prod")
	t.Cleanup(func() {
		restoreBuildMode()
		logger.SetLogConfig(original)
	})

	service := NewLogConfigService()
	if err := service.UpdateConfig(models.LogConfigRequest{Level: "DEBUG"}); err != ErrProdLogLevelTooLow {
		t.Fatalf("UpdateConfig(DEBUG) error = %v, want %v", err, ErrProdLogLevelTooLow)
	}

	if err := service.UpdateConfig(models.LogConfigRequest{Level: "WARN"}); err != nil {
		t.Fatalf("UpdateConfig(WARN) error = %v, want nil", err)
	}
	if got := logger.GetLogConfig().Level; got != logger.WARN {
		t.Fatalf("logger.GetLogConfig().Level = %v, want %v", got, logger.WARN)
	}
}

func TestLogConfigServiceAllowsDebugOverrideInProd(t *testing.T) {
	original := cloneLogConfig(logger.GetLogConfig())
	restoreBuildMode := setBuildModeForTest(t, "prod")
	t.Cleanup(func() {
		restoreBuildMode()
		logger.SetLogConfig(original)
	})

	service := NewLogConfigService()
	if err := service.UpdateConfigWithOverride(models.LogConfigRequest{Level: "DEBUG"}, true); err != nil {
		t.Fatalf("UpdateConfigWithOverride(DEBUG, true) error = %v, want nil", err)
	}
	if got := logger.GetLogConfig().Level; got != logger.DEBUG {
		t.Fatalf("logger.GetLogConfig().Level = %v, want %v", got, logger.DEBUG)
	}
}

func cloneLogConfig(cfg *logger.LogConfig) *logger.LogConfig {
	if cfg == nil {
		return nil
	}

	cloned := *cfg
	return &cloned
}

func setBuildModeForTest(t *testing.T, mode string) func() {
	t.Helper()

	const key = "ALIANG_BUILD_MODE"
	previous, hadPrevious := os.LookupEnv(key)
	if err := os.Setenv(key, mode); err != nil {
		t.Fatalf("os.Setenv(%s) error = %v", key, err)
	}

	return func() {
		if !hadPrevious {
			_ = os.Unsetenv(key)
			return
		}
		_ = os.Setenv(key, previous)
	}
}
