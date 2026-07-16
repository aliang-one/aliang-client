package services

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

func TestMain(m *testing.M) {
	baseDir, err := os.MkdirTemp("", "aliang-services-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv("ALIANG_AUTH_SESSION_DB", filepath.Join(baseDir, "auth.data"))
	// Package tests must never control a real desktop Agent that happens to be
	// listening on the developer machine's default port.
	config.DefaultUserAgentAddr = "127.0.0.1:0"

	code := m.Run()
	auth.StopTokenRefresh()
	auth.ResetAuthPersistenceForTest()
	_ = os.RemoveAll(baseDir)
	os.Exit(code)
}
