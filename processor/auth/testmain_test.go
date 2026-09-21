package user

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"aliang.one/nursorgate/internal/testisolate"
)

func TestMain(m *testing.M) {
	baseDir, err := os.MkdirTemp("", "aliang-auth-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv(authSessionDBPathEnv, filepath.Join(baseDir, "auth.data"))
	ResetAuthPersistenceForTest()
	// The token refresher logs through the file logger; keep those writes out
	// of the real ~/.aliang.
	cleanupState := testisolate.RedirectUserStateDir()

	code := m.Run()
	StopTokenRefresh()
	ResetAuthPersistenceForTest()
	cleanupState()
	_ = os.RemoveAll(baseDir)
	os.Exit(code)
}
