package cmd

type defaultRootLaunchMode string

const (
	defaultRootLaunchModeCLI defaultRootLaunchMode = "cli"
	defaultRootLaunchModeGUI defaultRootLaunchMode = "gui"
)

func decideWindowsDefaultLaunchMode(argv []string, hasConsole bool) defaultRootLaunchMode {
	if len(argv) > 1 {
		return defaultRootLaunchModeCLI
	}
	if hasConsole {
		return defaultRootLaunchModeCLI
	}
	return defaultRootLaunchModeGUI
}
