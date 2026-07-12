package user

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	baseDir, err := os.MkdirTemp("", "aliang-auth-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv(authSessionDBPathEnv, filepath.Join(baseDir, "auth.data"))
	ResetAuthPersistenceForTest()

	code := m.Run()
	StopTokenRefresh()
	ResetAuthPersistenceForTest()
	_ = os.RemoveAll(baseDir)
	os.Exit(code)
}
