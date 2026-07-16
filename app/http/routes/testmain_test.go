package routes

import (
	"os"
	"testing"

	"aliang.one/nursorgate/processor/config"
)

func TestMain(m *testing.M) {
	// Route integration tests exercise the real logout service. Keep its local
	// Agent side effects inside the test process instead of hitting :56433.
	config.DefaultUserAgentAddr = "127.0.0.1:0"
	os.Exit(m.Run())
}
