package runner

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"aliang.one/nursorgate/common/logger"
	httpServer "aliang.one/nursorgate/inbound/http"
	"aliang.one/nursorgate/inbound/tun/engine"
	utils2 "aliang.one/nursorgate/inbound/tun/runner/utils"
	"aliang.one/nursorgate/processor/config"
)

var defaultConfig config.EngineConf
var defaultGateway = "192.168.1.1"

func stopTun() {
	logger.Info("Stopping TUN service...")

	// 1. 停止 HTTP 代理
	logger.Info("Stopping HTTP proxy server...")
	httpServer.StopHttpProxy()

	// 2. 清理 TUN 路由
	if err := CleanupTunRoute(); err != nil {
		logger.Error("Failed to cleanup TUN route:", err)
	}

	// 3. 停止 TUN engine
	logger.Info("Stopping TUN engine...")
	engine.Stop()

	// 4. 关闭 TUN 接口
	if err := CleanupTunInterface(defaultConfig.Device); err != nil {
		logger.Error("Failed to cleanup TUN interface:", err)
	}
	// 5. 恢复默认网关
	if err := SetDefaultGateway(defaultGateway); err != nil {
		logger.Error("Failed to set default gateway:", err)
	}

	logger.Info("TUN service stopped successfully")
}

// CleanupTunRoute 清理 TUN 路由配置
func CleanupTunRoute() error {
	// 根据操作系统选择路由清理命令
	var routes [][]string
	switch runtime.GOOS {
	case "windows":
		routes = [][]string{
			{"route", "DELETE", "0.0.0.0", "MASK", "128.0.0.0", "10.0.0.1"},
			{"route", "DELETE", "128.0.0.0", "MASK", "128.0.0.0", "10.0.0.1"},
			{"route", "DELETE", "0.0.0.0", "MASK", "128.0.0.0", "10.0.0.2"},
			{"route", "DELETE", "128.0.0.0", "MASK", "128.0.0.0", "10.0.0.2"},
		}
	case "linux":
		routes = [][]string{
			{"ip", "route", "delete", "0.0.0.0/1", "via", "10.0.0.1"},
			{"ip", "route", "delete", "128.0.0.0/1", "via", "10.0.0.1"},
		}
	case "darwin": // macOS
		routes = [][]string{
			{"route", "-n", "delete", "-net", "1.0.0.0/8", "10.0.0.1"},
			{"route", "-n", "delete", "-net", "2.0.0.0/7", "10.0.0.1"},
			{"route", "-n", "delete", "-net", "4.0.0.0/6", "10.0.0.1"},
			{"route", "-n", "delete", "-net", "8.0.0.0/5", "10.0.0.1"},
			{"route", "-n", "delete", "-net", "32.0.0.0/3", "10.0.0.1"},
			{"route", "-n", "delete", "-net", "64.0.0.0/2", "10.0.0.1"},
			{"route", "-n", "delete", "-net", "128.0.0.0/1", "10.0.0.1"},
			{"route", "-n", "delete", "-net", "198.18.0.0/15", "10.0.0.1"},
		}
	default:
		return fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}

	// 清理路由
	for _, r := range routes {
		cmd := utils2.GetRunCommand(r[0], r[1:]...)
		if err := cmd.Run(); err != nil {
			// 忽略路由不存在的错误
			logger.Debug("Route deletion warning:", err)
			continue
		}
	}

	// 清理防火墙规则
	switch runtime.GOOS {
	case "darwin": // macOS
		// 清理 pf 规则
		cmd := utils2.GetRunCommand("pfctl", "-f", "/dev/null")
		if err := cmd.Run(); err != nil {
			logger.Debug("Failed to reset pf rules:", err)
			// 继续尝试禁用 pf
		}

		// 禁用 pf
		cmd = utils2.GetRunCommand("pfctl", "-d")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to disable pf: %w", err)
		}

	case "linux":
		// 清理 iptables 规则（假设使用 iptables）
		cmd := utils2.GetRunCommand("iptables", "-F")
		if err := cmd.Run(); err != nil {
			logger.Debug("Failed to flush iptables rules:", err)
		}
		// 可选：清理 nftables（如果使用 nftables）
		cmd = utils2.GetRunCommand("nft", "flush", "ruleset")
		if err := cmd.Run(); err != nil {
			logger.Debug("Failed to flush nftables rules:", err)
		}

	case "windows":
		// 清理 Windows 防火墙规则（假设规则名为 "TUNRule"）
		cmd := utils2.GetRunCommand("powershell", "-Command", `Get-NetFirewallRule -DisplayName "TUNRule" | Remove-NetFirewallRule`)
		if err := cmd.Run(); err != nil {
			logger.Debug("Failed to delete firewall rule:", err)
		}
	}

	return nil
}

// CleanupTunInterface 清理 TUN 接口，适配 Windows、Linux 和 macOS
func CleanupTunInterface(ifName string) error {
	switch runtime.GOOS {
	case "linux":
		// 关闭 TUN 接口
		cmd := utils2.GetRunCommand("ip", "link", "set", ifName, "down")
		if err := cmd.Run(); err != nil {
			logger.Debug("Failed to bring down interface:", err)
			// 继续尝试删除接口
		}

		// 删除 TUN 接口
		cmd = utils2.GetRunCommand("ip", "link", "delete", ifName)
		if err := cmd.Run(); err != nil {
			logger.Debug("Failed to delete interface:", err)
			return fmt.Errorf("failed to delete TUN interface %s: %w", ifName, err)
		}

	case "darwin": // macOS
		// 关闭 TUN 接口
		cmd := utils2.GetRunCommand("ifconfig", ifName, "down")
		if err := cmd.Run(); err != nil {
			logger.Error(fmt.Sprintf("Failed to bring down interface: %v", err))
			// macOS 不支持直接删除 TUN 接口，继续执行
		}

		// macOS 的 TUN 接口通常由用户态程序管理，无法通过 ifconfig destroy 删除
		// 可选：尝试通过 route 命令清理关联路由
		cmd = utils2.GetRunCommand("route", "-n", "flush")
		if err := cmd.Run(); err != nil {
			logger.Error(fmt.Sprintf("Failed to flush routes for interface: %v", err))
		}

	case "windows":
		// 禁用 TUN 接口
		cmd := utils2.GetRunCommand("powershell", "-Command", `Disable-NetAdapter -Name "`+ifName+`" -Confirm:$false`)
		if err := cmd.Run(); err != nil {
			logger.Error(fmt.Sprintf("Failed to disable interface: %v", err))
			// 继续尝试删除接口
		}

		// Windows 的 TUN 接口（如 Wintun 或 TAP-Windows）通常由驱动管理
		// 删除接口需要通过设备管理或驱动工具，netsh 不直接支持
		// 这里仅记录警告，实际删除可能依赖外部工具（如 tapinstall）
		logger.Debug("Windows TUN interface deletion requires driver-specific tools (e.g., tapinstall remove)")

	default:
		return fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}

	return nil
}

// GetDefaultGateway tries multiple methods to extract default gateway IP on macOS.
func GetDefaultGateway() (string, error) {
	methods := []func() (string, error){
		func() (string, error) {
			out, err := exec.Command("sh", "-c", "netstat -rn | grep '^default' | awk '{print $2}'").Output()
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(out)), nil
		},
		func() (string, error) {
			return utils2.RunCommandAndTrim("ipconfig", "getoption", "en0", "router")
		},
		func() (string, error) {
			return utils2.RunCommandAndTrim("ipconfig", "getoption", "en1", "router")
		},
	}

	for _, m := range methods {
		if gw, err := m(); err == nil && gw != "" {
			return gw, nil
		}
	}

	return "", fmt.Errorf("default gateway not found by any method")
}

func SetDefaultGateway(gateway string) error {
	if runtime.GOOS == "darwin" {
		cmd := utils2.GetRunCommand("route", "add", "default", gateway)
		return cmd.Run()
	}
	// if runtime.GOOS == "linux" {
	// 	cmd := utils.GetRunCommand("ip", "route", "change", "default", "via", gateway)
	// 	return cmd.Run()
	// }

	return nil
}

// monitorTunDevice 监控 TUN 设备状态
func monitorTunDevice(ifname string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var memStats runtime.MemStats
	for range ticker.C {
		// 检查设备状态
		if err := checkTunDeviceStatus(ifname); err != nil {
			logger.Warn(fmt.Sprintf("TUN 设备状态异常: %v", err))
		}

		// 获取内存统计
		runtime.ReadMemStats(&memStats)
		// 打印系统信息
		logger.Debug(fmt.Sprintf("系统信息 - Goroutines: %d, 内存使用: %v MB",
			runtime.NumGoroutine(),
			memStats.Alloc/1024/1024))
	}
}

// checkTunDeviceStatus 检查 TUN 设备状态
func checkTunDeviceStatus(ifname string) error {
	switch runtime.GOOS {
	case "windows":
		return checkWindowsTunStatus(ifname)
	case "darwin":
		return checkDarwinTunStatus(ifname)
	case "linux":
		return checkLinuxTunStatus(ifname)
	default:
		return fmt.Errorf("不支持的操作系统: %s", runtime.GOOS)
	}
}

// checkWindowsTunStatus 检查 Windows TUN 设备状态
func checkWindowsTunStatus(ifname string) error {
	return checkWindowsTunStatusNative(ifname)
}

// checkDarwinTunStatus 检查 macOS TUN 设备状态
func checkDarwinTunStatus(ifname string) error {
	cmd := utils2.GetRunCommand("ifconfig", ifname)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("获取接口状态失败: %w", err)
	}

	logger.Debug(fmt.Sprintf("TUN 设备状态: %s", string(output)))
	return nil
}

// checkLinuxTunStatus 检查 Linux TUN 设备状态
func checkLinuxTunStatus(ifname string) error {
	cmd := utils2.GetRunCommand("ip", "link", "show", ifname)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("获取接口状态失败: %w", err)
	}

	logger.Debug("TUN 设备状态: ", string(output))
	return nil
}

// waitForTunDeviceReady 等待TUN设备就绪
func waitForTunDeviceReady(deviceName string, timeout time.Duration) error {
	if runtime.GOOS == "windows" {
		return waitForWindowsTunDeviceReady(deviceName, timeout)
	}

	deadline := time.Now().Add(timeout)
	var outputStr string
	for time.Now().Before(deadline) {
		var cmd *exec.Cmd
		var checkString string

		// 根据操作系统选择命令
		switch runtime.GOOS {
		case "linux": // darwin 是 macOS
			// Linux/macOS 使用 ip link show 检查接口状态
			cmd = utils2.GetRunCommand("ip", "link", "show", deviceName)
			checkString = "UP"
		case "darwin": // macOS
			// macOS 使用多步骤验证确保设备真正就绪
			if err := checkMacOSTunReady(deviceName); err != nil {
				logger.Debug(fmt.Sprintf("macOS TUN 设备检查失败: %v, 重试中...", err))
				time.Sleep(500 * time.Millisecond)
				continue
			}
			logger.Info(fmt.Sprintf("macOS TUN 设备 %s 已完全就绪", deviceName))
			return nil

		default:
			return fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
		}

		// 执行命令获取接口状态
		output, err := cmd.CombinedOutput()
		if err != nil {
			logger.Error(fmt.Sprintf("检查接口状态失败: %v", err))
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// 检查设备是否存在且状态为预期值
		outputStr = string(output)
		// 检查输出是否包含设备名称和状态
		if strings.Contains(outputStr, deviceName) && strings.Contains(outputStr, checkString) {
			// 进一步验证网卡可用性，通过 ping 测试
			pingCmd := utils2.GetRunCommand("ping", "-n", "1", "10.0.0.1")

			if err := pingCmd.Run(); err == nil {
				logger.Info("TUN 设备已就绪 OS: ", runtime.GOOS)
				time.Sleep(500 * time.Millisecond)
				return nil
			}
			logger.Debug("Ping 测试失败，设备可能尚未完全就绪")
		}

		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("等待 TUN 设备就绪超时. %s", outputStr)
}

// checkMacOSTunReady 检查 macOS TUN 设备是否真正就绪
// 使用多步骤验证：接口标志、IP 地址、连通性测试、路由表
func checkMacOSTunReady(deviceName string) error {
	// Step 1: 检查接口是否存在并有 UP/RUNNING 标志
	cmd := utils2.GetRunCommand("ifconfig", deviceName)
	output, err := cmd.Output()
	if err != nil {
		if strings.Contains(err.Error(), "can't find interface") {
			return fmt.Errorf("interface not found")
		}
		return fmt.Errorf("failed to execute ifconfig: %w", err)
	}

	outputStr := string(output)

	// 检查 UP 标志
	if !strings.Contains(outputStr, "UP") {
		return fmt.Errorf("interface not UP")
	}

	// 检查 RUNNING 标志
	if !strings.Contains(outputStr, "RUNNING") {
		return fmt.Errorf("interface not RUNNING")
	}

	// Step 2: 验证接口 IP 地址配置是否正确（应该是 10.0.0.1 或 10.0.0.2）
	hasCorrectIP := strings.Contains(outputStr, "10.0.0.1") || strings.Contains(outputStr, "10.0.0.2")
	if !hasCorrectIP {
		return fmt.Errorf("interface IP not configured correctly (expected 10.0.0.1 or 10.0.0.2)")
	}

	// Step 3: 实际连通性测试 - ping 到 TUN 网关（带超时）
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// macOS 的 ping 命令参数：-c 1 (count), -t 1 (timeout in seconds)
	pingCmd := exec.CommandContext(ctx, "ping", "-c", "1", "-t", "1", "10.0.0.2")
	if err := pingCmd.Run(); err != nil {
		return fmt.Errorf("connectivity test failed (ping to 10.0.0.2): %w", err)
	}

	// Step 4: 验证路由表中存在 TUN 路由
	routeCmd := utils2.GetRunCommand("netstat", "-rn")
	routeOutput, err := routeCmd.Output()
	if err != nil {
		// 路由检查失败不应阻止启动，只是警告
		logger.Warn(fmt.Sprintf("无法检查路由表: %v", err))
	} else {
		// 检查至少有一条路由指向 TUN 网关
		if !strings.Contains(string(routeOutput), "10.0.0.1") {
			logger.Warn("路由表中未找到 TUN 网关路由，但设备可能仍可用")
		}
	}

	// 所有检查通过
	return nil
}
