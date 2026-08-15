//go:build windows

package services

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func loadWindowsTunInterfaceSnapshotsNative() ([]tunInterfaceSnapshot, error) {
	size := uint32(15 * 1024)

	for attempt := 0; attempt < tunMaxRetryAttempts; attempt++ {
		buf := make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))

		err := windows.GetAdaptersAddresses(
			windows.AF_UNSPEC,
			windows.GAA_FLAG_INCLUDE_ALL_INTERFACES,
			0,
			first,
			&size,
		)
		if err != nil {
			if errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
				// Buffer too small, retry with new size
				continue
			}
			// Transient error, retry with backoff
			if attempt < tunMaxRetryAttempts-1 {
				delay := tunRetryBaseDelay * (1 << uint(attempt)) // Exponential backoff: 10ms, 20ms, 40ms
				time.Sleep(delay)
				continue
			}
			return nil, fmt.Errorf("GetAdaptersAddresses failed after %d attempts: %w", attempt+1, err)
		}

		// Success - parse adapter addresses
		snapshots := make([]tunInterfaceSnapshot, 0, 8)
		for adapter := first; adapter != nil; adapter = adapter.Next {
			name := strings.TrimSpace(windows.UTF16PtrToString(adapter.FriendlyName))
			description := strings.TrimSpace(windows.UTF16PtrToString(adapter.Description))
			if name == "" && description == "" {
				continue
			}

			snapshots = append(snapshots, tunInterfaceSnapshot{
				Name:        name,
				Description: description,
				Status:      windowsAdapterStatusText(adapter.OperStatus),
			})
		}

		return snapshots, nil
	}

	return nil, fmt.Errorf("获取 Windows 网络接口失败: 缓冲区调整后仍然失败")
}

func windowsAdapterStatusText(status uint32) string {
	switch status {
	case windows.IfOperStatusUp:
		return "up"
	case windows.IfOperStatusDown:
		return "down"
	case windows.IfOperStatusTesting:
		return "testing"
	case windows.IfOperStatusUnknown:
		return "unknown"
	case windows.IfOperStatusDormant:
		return "dormant"
	case windows.IfOperStatusNotPresent:
		return "not_present"
	case windows.IfOperStatusLowerLayerDown:
		return "lower_layer_down"
	default:
		return fmt.Sprintf("unknown(%d)", status)
	}
}
