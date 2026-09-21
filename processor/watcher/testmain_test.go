package tls

import (
	"os"
	"testing"

	"aliang.one/nursorgate/internal/testisolate"
)

func TestMain(m *testing.M) {
	// HTTP wrapper/relay tests log through the file logger; keep those writes
	// out of the real ~/.aliang.
	cleanupState := testisolate.RedirectUserStateDir()

	code := m.Run()
	cleanupState()
	os.Exit(code)
}
