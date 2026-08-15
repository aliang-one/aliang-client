//go:build windows

package runner

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
	"unsafe"

	"aliang.one/nursorgate/common/logger"
	"golang.org/x/sys/windows"
)

type windowsTunInterfaceStatus struct {
	Name        string
	Description string
	Alias       string
	Index       uint32
	OperStatus  uint32
	AdminStatus uint32
	MTU         uint32
	Addresses   []string
}

func checkWindowsTunStatusNative(ifname string) error {
	status, err := queryWindowsTunInterfaceStatus(ifname)
	if err != nil {
		return err
	}

	logger.Debug(fmt.Sprintf(
		"Windows TUN status: name=%s alias=%s index=%d oper_status=%s admin_status=%d mtu=%d addrs=%s desc=%s",
		status.Name,
		status.Alias,
		status.Index,
		windowsOperStatusText(status.OperStatus),
		status.AdminStatus,
		status.MTU,
		strings.Join(status.Addresses, ","),
		status.Description,
	))

	return nil
}

func waitForWindowsTunDeviceReady(deviceName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	lastErr := fmt.Errorf("interface not found")

	for time.Now().Before(deadline) {
		if err := checkWindowsTunStatusNative(deviceName); err == nil {
			logger.Info("TUN 设备已就绪, OS: windows")
			time.Sleep(500 * time.Millisecond)
			return nil
		} else {
			lastErr = err
		}

		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("等待 TUN 设备就绪超时: %w", lastErr)
}

func queryWindowsTunInterfaceStatus(ifname string) (*windowsTunInterfaceStatus, error) {
	adapters, err := listWindowsTunInterfaces()
	if err != nil {
		return nil, fmt.Errorf("获取 Windows 网络接口失败: %w", err)
	}

	for _, adapter := range adapters {
		if !strings.EqualFold(adapter.Name, ifname) {
			continue
		}
		if adapter.OperStatus != windows.IfOperStatusUp {
			return nil, fmt.Errorf("TUN 设备未就绪: oper_status=%s", windowsOperStatusText(adapter.OperStatus))
		}
		if !hasExpectedTunAddress(adapter.Addresses) {
			return nil, fmt.Errorf("TUN 设备地址未就绪: addrs=%s", strings.Join(adapter.Addresses, ","))
		}
		return adapter, nil
	}

	return nil, fmt.Errorf("未找到 TUN 设备: %s", ifname)
}

func listWindowsTunInterfaces() ([]*windowsTunInterfaceStatus, error) {
	size := uint32(15 * 1024)
	for attempt := 0; attempt < 3; attempt++ {
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
				continue
			}
			return nil, err
		}

		items := make([]*windowsTunInterfaceStatus, 0, 8)
		for adapter := first; adapter != nil; adapter = adapter.Next {
			status := &windowsTunInterfaceStatus{
				Name:        strings.TrimSpace(windows.UTF16PtrToString(adapter.FriendlyName)),
				Description: strings.TrimSpace(windows.UTF16PtrToString(adapter.Description)),
				Index:       adapter.IfIndex,
				OperStatus:  adapter.OperStatus,
				Addresses:   windowsAdapterAddresses(adapter),
			}

			row := windows.MibIfRow2{
				InterfaceIndex: adapter.IfIndex,
			}
			if rowErr := windows.GetIfEntry2Ex(windows.MibIfEntryNormalWithoutStatistics, &row); rowErr == nil {
				status.Alias = strings.TrimSpace(windows.UTF16ToString(row.Alias[:]))
				status.MTU = row.Mtu
				status.AdminStatus = row.AdminStatus
				status.OperStatus = row.OperStatus
				if status.Description == "" {
					status.Description = strings.TrimSpace(windows.UTF16ToString(row.Description[:]))
				}
			}

			items = append(items, status)
		}

		return items, nil
	}

	return nil, fmt.Errorf("获取 Windows 网络接口失败: 缓冲区调整后仍然失败")
}

func windowsAdapterAddresses(adapter *windows.IpAdapterAddresses) []string {
	addrs := make([]string, 0, 2)
	for addr := adapter.FirstUnicastAddress; addr != nil; addr = addr.Next {
		ip := addr.Address.IP()
		if ip == nil {
			continue
		}

		prefixBits := int(addr.OnLinkPrefixLength)
		if ip4 := ip.To4(); ip4 != nil {
			ip = ip4
			addrs = append(addrs, (&net.IPNet{
				IP:   ip,
				Mask: net.CIDRMask(prefixBits, 32),
			}).String())
			continue
		}

		addrs = append(addrs, (&net.IPNet{
			IP:   ip,
			Mask: net.CIDRMask(prefixBits, 128),
		}).String())
	}
	return addrs
}

func hasExpectedTunAddress(addrs []string) bool {
	for _, addr := range addrs {
		ipPart := addr
		if slash := strings.IndexByte(addr, '/'); slash >= 0 {
			ipPart = addr[:slash]
		}
		if ipPart == "10.0.0.1" || ipPart == "10.0.0.2" {
			return true
		}
	}
	return false
}

func windowsOperStatusText(status uint32) string {
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
