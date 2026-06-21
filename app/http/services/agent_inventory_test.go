package services

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsSafeAgentProjectPath(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	home = filepath.Clean(home)

	valid := []string{
		filepath.Join(home, "code", "myproject"),
		filepath.Join(home, "MyProgram", "GoProgram", "nursor", "alianggate"),
		"/tmp/some-project",
	}
	for _, p := range valid {
		assert.True(t, isSafeAgentProjectPath(p), "expected valid: %s", p)
	}

	invalid := []string{
		"",
		"/",
		home,
		// ~/Library holds per-app data containers (design/IDE/storage), not code
		// projects — e.g. Open Design drives Claude Code with cwd pointing here.
		filepath.Join(home, "Library"),
		filepath.Join(home, "Library", "Application Support", "Open Design",
			"namespaces", "release-stable", "data", "projects",
			"05bfc135-d5c5-46ee-9ac6-13826187935d"),
		// Anything inside a macOS app bundle is the app's own resource tree.
		"/Applications/Open Design.app",
		filepath.Join("/Applications", "Open Design.app", "Contents", "Resources", "app", "prebundled"),
	}
	for _, p := range invalid {
		assert.False(t, isSafeAgentProjectPath(p), "expected invalid: %s", p)
	}
}
