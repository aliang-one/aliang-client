package logger

import (
	"testing"
)

func TestDefaultLogConfigUsesInfoInProd(t *testing.T) {
	t.Setenv("ALIANG_BUILD_MODE", "prod")

	cfg := DefaultLogConfig()
	if cfg.Level != INFO {
		t.Fatalf("DefaultLogConfig().Level = %v, want %v", cfg.Level, INFO)
	}

	httpCfg := HTTPLogConfig()
	if httpCfg.Level != INFO {
		t.Fatalf("HTTPLogConfig().Level = %v, want %v", httpCfg.Level, INFO)
	}
}

func TestDefaultLogConfigKeepsDevDefaults(t *testing.T) {
	t.Setenv("ALIANG_BUILD_MODE", "dev")

	cfg := DefaultLogConfig()
	if cfg.Level != DEBUG {
		t.Fatalf("DefaultLogConfig().Level = %v, want %v", cfg.Level, DEBUG)
	}

	httpCfg := HTTPLogConfig()
	if httpCfg.Level != TRACE {
		t.Fatalf("HTTPLogConfig().Level = %v, want %v", httpCfg.Level, TRACE)
	}
}

func TestUpdateLogLevelWithOverrideAllowsDebugInProd(t *testing.T) {
	t.Setenv("ALIANG_BUILD_MODE", "prod")

	original := GetLogConfig()
	cloned := *original
	SetLogConfig(&cloned)
	t.Cleanup(func() {
		SetLogConfig(original)
	})

	UpdateLogLevelWithOverride(DEBUG, true)

	if got := GetLogConfig().Level; got != DEBUG {
		t.Fatalf("GetLogConfig().Level = %v, want %v", got, DEBUG)
	}
}

func TestSetLogConfigWithOverrideAllowsTraceInProd(t *testing.T) {
	t.Setenv("ALIANG_BUILD_MODE", "prod")

	original := GetLogConfig()
	restore := *original
	t.Cleanup(func() {
		SetLogConfig(&restore)
	})

	cfg := DefaultLogConfig()
	cfg.Level = TRACE
	SetLogConfigWithOverride(cfg, true)

	if got := GetLogConfig().Level; got != TRACE {
		t.Fatalf("GetLogConfig().Level = %v, want %v", got, TRACE)
	}
}
