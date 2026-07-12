package services

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	auth "aliang.one/nursorgate/processor/auth"
)

func TestMain(m *testing.M) {
	baseDir, err := os.MkdirTemp("", "aliang-services-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv("ALIANG_AUTH_SESSION_DB", filepath.Join(baseDir, "auth.data"))

	code := m.Run()
	auth.StopTokenRefresh()
	auth.ResetAuthPersistenceForTest()
	_ = os.RemoveAll(baseDir)
	os.Exit(code)
}
