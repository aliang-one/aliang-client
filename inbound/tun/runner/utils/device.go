package utils

import (
	"fmt"
	"regexp"
	"runtime"
	"strconv"

	"aliang.one/nursorgate/common/logger"
)

const defaultAliangTunName = "AliangGate"

// GetDefaultTunName returns the default TUN device name for the current OS.
// macOS must use system-assigned utun names, while Windows and Linux can use
// a stable custom alias.
func GetDefaultTunName() string {
	switch runtime.GOOS {
	case "darwin":
		return getAvailableUtunDevice()
	case "linux":
		return defaultAliangTunName
	case "windows":
		return getAvailableWintunDevice()
	default:
		return defaultAliangTunName
	}
}

func getAvailableUtunDevice() string {
	cmd := GetRunCommand("ifconfig")
	output, err := cmd.Output()
	if err != nil {
		logger.Error(fmt.Sprintf("Error running ifconfig: %v", err))
		return "utun99"
	}

	existingUtuns := make(map[int]bool)
	re := regexp.MustCompile(`utun(\d+):`)
	matches := re.FindAllStringSubmatch(string(output), -1)

	for _, match := range matches {
		if len(match) > 1 {
			if num, err := strconv.Atoi(match[1]); err == nil {
				existingUtuns[num] = true
			}
		}
	}

	for i := 0; i < 100; i++ {
		if !existingUtuns[i] {
			return fmt.Sprintf("utun%d", i)
		}
	}

	logger.Warn("All utun0-99 devices are in use. Returning utun999.")
	return "utun999"
}

func getAvailableWintunDevice() string {
	return defaultAliangTunName
}

// GetDefaultInterface returns the system default physical interface.
func GetDefaultInterface() (string, error) {
	switch runtime.GOOS {
	case "windows":
		return getWindowsDefaultInterface()
	case "darwin":
		return getDarwinDefaultInterface()
	case "linux":
		return getLinuxDefaultInterface()
	default:
		return "", fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}
}
