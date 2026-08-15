package cmd

import (
	"os"
	"strings"
	"testing"

	"aliang.one/nursorgate/processor/setup"
)

func TestEnsureCoreRuntimeEnvironmentSetsDaemonPaths(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", "")
	t.Setenv("ALIANG_CACHE_DIR", "")
	t.Setenv("ALIANG_LOG_DIR", "")
	t.Setenv("ALIANG_SOCKET_PATH", "")

	ensureCoreRuntimeEnvironment()

	if got := strings.TrimSpace(os.Getenv("ALIANG_DATA_DIR")); got != setup.CoreDataDir() {
		t.Fatalf("ALIANG_DATA_DIR = %q, want %q", got, setup.CoreDataDir())
	}
	if got := strings.TrimSpace(os.Getenv("ALIANG_CACHE_DIR")); got != setup.CoreDataDir() {
		t.Fatalf("ALIANG_CACHE_DIR = %q, want %q", got, setup.CoreDataDir())
	}
	if got := strings.TrimSpace(os.Getenv("ALIANG_LOG_DIR")); got != setup.CoreLogDir() {
		t.Fatalf("ALIANG_LOG_DIR = %q, want %q", got, setup.CoreLogDir())
	}

	wantSocket := strings.TrimSpace(setup.CoreSocketPath())
	if wantSocket != "" {
		if got := strings.TrimSpace(os.Getenv("ALIANG_SOCKET_PATH")); got != wantSocket {
			t.Fatalf("ALIANG_SOCKET_PATH = %q, want %q", got, wantSocket)
		}
	}
}
