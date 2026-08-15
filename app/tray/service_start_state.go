package tray

const windowsServiceAlreadyRunningExitCode = 1056

func isAcceptableWindowsServiceStartExitCode(exitCode uint32) bool {
	return exitCode == 0 || exitCode == windowsServiceAlreadyRunningExitCode
}
