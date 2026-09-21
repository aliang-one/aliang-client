package cmd

import (
	"os"
	"testing"

	"aliang.one/nursorgate/internal/testisolate"
)

func TestMain(m *testing.M) {
	// Startup/user-init characterization tests drive code paths (setup, config
	// apply) that log through the file logger; keep those writes out of the
	// real ~/.aliang.
	cleanupState := testisolate.RedirectUserStateDir()

	code := m.Run()
	cleanupState()
	os.Exit(code)
}
