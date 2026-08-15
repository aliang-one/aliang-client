package utils

import (
	"fmt"
	"net"
	"strings"
)

// getWindowsDefaultInterface 获取 Windows 默认网络接口
func getWindowsDefaultInterface() (string, error) {
	// 用 PowerShell 获取默认路由的 InterfaceIndex
	cmd := GetRunCommand("powershell", "-Command", `
	$defaultRoute = Get-NetRoute -DestinationPrefix "0.0.0.0/0" |
	                Sort-Object RouteMetric |
	                Select-Object -First 1;
	if ($defaultRoute -ne $null) {
		$index = $defaultRoute.InterfaceIndex
		$adapter = Get-NetAdapter | Where-Object { $_.InterfaceIndex -eq $index -and $_.Status -eq "Up" }
		if ($adapter) {
			Write-Output $adapter.Name
		}
	}
	`)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get default interface: %w", err)
	}

	ifName := strings.TrimSpace(string(output))
	if ifName != "" &&
		!strings.Contains(strings.ToLower(ifName), "loopback") &&
		!strings.Contains(strings.ToLower(ifName), "virtual") &&
		!strings.Contains(strings.ToLower(ifName), "vethernet") {
		return ifName, nil
	}

	// fallback: 遍历启用的物理接口（排除 loopback 和虚拟）
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("failed to get interfaces: %w", err)
	}

	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagLoopback == 0 {
			if !strings.Contains(strings.ToLower(iface.Name), "virtual") &&
				!strings.Contains(strings.ToLower(iface.Name), "vethernet") {
				addrs, err := iface.Addrs()
				if err != nil {
					continue
				}
				for _, addr := range addrs {
					if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
						return iface.Name, nil
					}
				}
			}
		}
	}

	return "", fmt.Errorf("no suitable network interface found")
}

// getDarwinDefaultInterface 获取 macOS 默认网络接口
func getDarwinDefaultInterface() (string, error) {
	// 使用 route 命令获取默认路由接口
	cmd := GetRunCommand("route", "-n", "get", "default")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get default route: %w", err)
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "interface:") {
			fields := strings.Fields(line)
			if len(fields) > 1 {
				return fields[len(fields)-1], nil
			}
		}
	}

	// 如果没有找到默认路由，尝试获取活动的网络接口
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("failed to get interfaces: %w", err)
	}

	for _, iface := range interfaces {
		// 检查接口是否启用且不是回环接口
		if iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagLoopback == 0 {
			// 尝试获取接口的 IP 地址
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				// 检查是否是 IPv4 地址
				if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
					return iface.Name, nil
				}
			}
		}
	}

	return "", fmt.Errorf("no suitable network interface found")
}

// getLinuxDefaultInterface 获取 Linux 默认网络接口
func getLinuxDefaultInterface() (string, error) {
	// 使用 ip 命令获取默认路由接口
	cmd := GetRunCommand("ip", "route", "get", "8.8.8.8")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get default route: %w", err)
	}

	// 解析输出找到接口名称
	fields := strings.Fields(string(output))
	for i, field := range fields {
		if field == "dev" && i+1 < len(fields) {
			return fields[i+1], nil
		}
	}

	// 如果没有找到默认路由，尝试获取活动的网络接口
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("failed to get interfaces: %w", err)
	}

	for _, iface := range interfaces {
		// 检查接口是否启用且不是回环接口
		if iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagLoopback == 0 {
			// 尝试获取接口的 IP 地址
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				// 检查是否是 IPv4 地址
				if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
					return iface.Name, nil
				}
			}
		}
	}

	return "", fmt.Errorf("no suitable network interface found")
}
