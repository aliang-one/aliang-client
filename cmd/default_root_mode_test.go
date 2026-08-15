package cmd

import "testing"

func TestDecideWindowsDefaultLaunchMode_WithArguments_PrefersCLI(t *testing.T) {
	mode := decideWindowsDefaultLaunchMode([]string{"aliang.exe", "--config", "custom.json"}, false)
	if mode != defaultRootLaunchModeCLI {
		t.Fatalf("launch mode = %q, want %q", mode, defaultRootLaunchModeCLI)
	}
}

func TestDecideWindowsDefaultLaunchMode_WithConsoleAndNoArgs_PrefersCLI(t *testing.T) {
	mode := decideWindowsDefaultLaunchMode([]string{"aliang.exe"}, true)
	if mode != defaultRootLaunchModeCLI {
		t.Fatalf("launch mode = %q, want %q", mode, defaultRootLaunchModeCLI)
	}
}

func TestDecideWindowsDefaultLaunchMode_WithoutConsoleAndNoArgs_PrefersGUI(t *testing.T) {
	mode := decideWindowsDefaultLaunchMode([]string{"aliang.exe"}, false)
	if mode != defaultRootLaunchModeGUI {
		t.Fatalf("launch mode = %q, want %q", mode, defaultRootLaunchModeGUI)
	}
}
