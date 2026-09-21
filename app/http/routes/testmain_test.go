package routes

import (
	"os"
	"testing"

	"aliang.one/nursorgate/internal/testisolate"
	"aliang.one/nursorgate/processor/config"
)

func TestMain(m *testing.M) {
	// Route integration tests exercise the real logout service. Keep its local
	// Agent side effects inside the test process instead of hitting :56433.
	config.DefaultUserAgentAddr = "127.0.0.1:0"
	// Keep logger/cache/state writes out of the real ~/.aliang (the logout
	// service and AGENT-BOOT paths log through the file logger).
	cleanupState := testisolate.RedirectUserStateDir()

	code := m.Run()
	cleanupState()
	os.Exit(code)
}
