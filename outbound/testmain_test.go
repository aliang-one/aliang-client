package outbound

import (
	"os"
	"testing"

	"aliang.one/nursorgate/internal/testisolate"
)

func TestMain(m *testing.M) {
	// Registry tests log through the file logger (e.g. Registry.Clear's
	// "All proxies cleared"); keep those writes out of the real ~/.aliang.
	cleanupState := testisolate.RedirectUserStateDir()

	code := m.Run()
	cleanupState()
	os.Exit(code)
}
