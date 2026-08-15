//go:build !windows

package runner

import (
	"fmt"
	"time"
)

func checkWindowsTunStatusNative(ifname string) error {
	return fmt.Errorf("windows status helper unavailable for %s", ifname)
}

func waitForWindowsTunDeviceReady(deviceName string, timeout time.Duration) error {
	return fmt.Errorf("windows ready helper unavailable for %s after %s", deviceName, timeout)
}
