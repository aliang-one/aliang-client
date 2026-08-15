//go:build windows

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const maxStartupTraceSize = 128 * 1024

func writeStartupTrace(format string, args ...interface{}) {
	line := fmt.Sprintf(format, args...)
	writeTraceLine(filepath.Join(os.TempDir(), "aliang-companion.log"), line)

	programData := os.Getenv("ProgramData")
	if programData == "" {
		programData = `C:\ProgramData`
	}
	sharedDir := filepath.Join(programData, "Aliang", "logs")
	_ = os.MkdirAll(sharedDir, 0o755)
	writeTraceLine(filepath.Join(sharedDir, "aliang-service.log"), line)
}

func writeTraceLine(path, line string) {
	if info, err := os.Stat(path); err == nil && info.Size() >= maxStartupTraceSize {
		_ = os.Remove(path)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), line)
}
