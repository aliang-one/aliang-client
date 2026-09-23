package ownernotify

import (
	"os"
	"testing"

	"aliang.one/nursorgate/internal/testisolate"
)

func TestMain(m *testing.M) {
	// EnsureServer logs through the file logger and the handler-path tests run
	// the full notify pipeline; keep those writes out of the real ~/.aliang.
	// (2026-09-23: without this, overnight go test runs polluted
	// ~/.aliang/logs/aliang_core.log with owner-notify test lines.)
	cleanupState := testisolate.RedirectUserStateDir()

	code := m.Run()
	cleanupState()
	os.Exit(code)
}
