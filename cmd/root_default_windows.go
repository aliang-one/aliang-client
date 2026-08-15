//go:build windows

package cmd

import (
	"os"

	"aliang.one/nursorgate/app/tray"
	"github.com/spf13/cobra"
	"golang.org/x/sys/windows"
)

var (
	kernel32DLL          = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleWindow = kernel32DLL.NewProc("GetConsoleWindow")
	procAttachConsole    = kernel32DLL.NewProc("AttachConsole")
)

const attachParentProcess = ^uint32(0)

func runDefaultRoot(cmd *cobra.Command, args []string) error {
	mode := decideWindowsDefaultLaunchMode(os.Args, hasWindowsConsoleInvocation())
	if mode == defaultRootLaunchModeGUI {
		tray.RunCompanion()
		return nil
	}
	return runCommandLineDefaultRoot(cmd, args)
}

func runCommandLineDefaultRoot(cmd *cobra.Command, args []string) error {
	return runStart(cmd, args)
}

func hasWindowsConsoleInvocation() bool {
	if consoleWindow, _, _ := procGetConsoleWindow.Call(); consoleWindow != 0 {
		return true
	}

	result, _, _ := procAttachConsole.Call(uintptr(attachParentProcess))
	return result != 0
}
