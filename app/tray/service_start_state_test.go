package tray

import "testing"

func TestIsAcceptableWindowsServiceStartExitCode(t *testing.T) {
	testCases := []struct {
		exitCode uint32
		want     bool
	}{
		{exitCode: 0, want: true},
		{exitCode: windowsServiceAlreadyRunningExitCode, want: true},
		{exitCode: 1, want: false},
		{exitCode: 1060, want: false},
	}

	for _, tc := range testCases {
		if got := isAcceptableWindowsServiceStartExitCode(tc.exitCode); got != tc.want {
			t.Fatalf("isAcceptableWindowsServiceStartExitCode(%d) = %v, want %v", tc.exitCode, got, tc.want)
		}
	}
}
