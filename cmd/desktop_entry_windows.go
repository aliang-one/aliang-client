//go:build windows

package cmd

import (
	"os"
	"strings"

	"aliang.one/nursorgate/app/tray"
)

func MaybeRunWindowsCompanionFromArgs() bool {
	if len(os.Args) < 2 {
		return false
	}

	if !strings.EqualFold(strings.TrimSpace(os.Args[1]), "companion") {
		return false
	}

	tray.RunCompanion()
	return true
}
