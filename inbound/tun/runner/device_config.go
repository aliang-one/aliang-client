package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"aliang.one/nursorgate/common/logger"
	utils2 "aliang.one/nursorgate/inbound/tun/runner/utils"
	"aliang.one/nursorgate/processor/setup"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

type RouteEntry struct {
	DestinationPrefix string `json:"DestinationPrefix"`
	NextHop           string `json:"NextHop"`
	RouteMetric       int    `json:"RouteMetric"`
}

// convertGBKToUTF8 将 GBK 编码转换为 UTF-8
func convertGBKToUTF8(s string) (string, error) {
	reader := transform.NewReader(bytes.NewReader([]byte(s)), simplifiedchinese.GBK.NewDecoder())
	d, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(d), nil
}

func ConfigureTunInterface(ifname string) error {
	logger.Info(fmt.Sprintf("[INFO] Configuring TUN interface on %s", runtime.GOOS))
	switch runtime.GOOS {
	case "windows":
		return configureWindowsTunInterface(ifname)
	case "darwin":
		return configureDarwinTunInterface(ifname)
	case "linux":
		return configureLinuxTunInterface(ifname)
	default:
		return fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}
}

func ConfigureTunRoute() error {
	logger.Info(fmt.Sprintf("[INFO] Configuring TUN routes on %s", runtime.GOOS))
	switch runtime.GOOS {
	case "windows":
		return configureWindowsTunRoute()
	case "darwin":
		return configureDarwinTunRoute()
	case "linux":
		return configureLinuxTunRoute()
	default:
		return fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}
}

func configureWindowsTunInterface(ifname string) error {
	// 使用 netsh 命令配置接口
	commands := [][]string{
		// 设置静态 IP
		{"powershell", "-Command", `New-NetIPAddress -InterfaceAlias "` + ifname + `" -IPAddress 10.0.0.1 -PrefixLength 24`},

		// 设置 Metric
		{"powershell", "-Command", `Set-NetIPInterface -InterfaceAlias "` + ifname + `" -InterfaceMetric 1`},

		// 启用接口
		{"powershell", "-Command", `Enable-NetAdapter -Name "` + ifname + `"`},
	}

	preCheckCmd := utils2.GetRunCommand("powershell", "-Command", `Get-NetAdapter | Select Name, InterfaceAlias, Status`)
	_, err := preCheckCmd.CombinedOutput()
	if err != nil {
		logger.Error(fmt.Sprintf("failed to get net adapter: %v", err))
		commands = [][]string{
			{"netsh", "interface", "ipv4", "set", "address", "name=" + ifname, "static", "10.0.0.1", "255.255.255.0"},
			{"netsh", "interface", "ipv4", "set", "interface", "name=" + ifname, "metric=1"},
			{"netsh", "interface", "ipv4", "set", "interface", "name=" + ifname, "admin=enabled"},
		}
	}

	if !setup.IsRoot() {
		UpdateStartupProgress("starting", "requesting_permission", 52, "Requesting Windows administrator permission to configure the virtual adapter.", "", true)
	}

	for _, cmd := range commands {
		if err := runWindowsPrivilegedCommand(cmd[0], cmd[1:]...); err != nil {
			errStr := fmt.Sprintf("netsh command failed: %v", err)
			logger.Error(errStr)
			return errors.New(errStr)
		}
	}
	return nil
}

func configureDarwinTunInterface(ifname string) error {
	if err := utils2.RunCommand("ifconfig", ifname, "10.0.0.1", "10.0.0.2", "up"); err != nil {
		errStr := fmt.Sprintf("ifconfig failed: %v", err)
		logger.Error(errStr)
		return errors.New(errStr)
	}
	return nil
}

func configureLinuxTunInterface(ifname string) error {
	commands := [][]string{
		{"ip", "addr", "add", "10.0.0.1/24", "dev", ifname},
		{"ip", "link", "set", "dev", ifname, "up"},
		{"ip", "route", "add", "10.0.0.0/24", "dev", ifname},
	}

	for _, cmd := range commands {
		if err := utils2.RunCommand(cmd[0], cmd[1:]...); err != nil {
			return fmt.Errorf("ip command failed: %w", err)
		}
	}
	return nil
}

func GetDefaultGatewayWithPowerShell() (string, error) {
	cmd := utils2.GetRunCommand("powershell", "-Command", `
	  $routes = @(Get-NetRoute -DestinationPrefix '0.0.0.0/0' | Sort-Object RouteMetric | Select-Object DestinationPrefix,NextHop,RouteMetric);
	  $routes | ConvertTo-Json -Depth 3
	`)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}

	var routes []RouteEntry
	if out[0] == '[' {
		if err := json.Unmarshal(out, &routes); err != nil {
			return "", err
		}
	} else {
		var single RouteEntry
		if err := json.Unmarshal(out, &single); err != nil {
			return "", err
		}
		routes = append(routes, single)
	}
	if len(routes) == 0 {
		return "", fmt.Errorf("no default route found")
	}
	return routes[0].NextHop, nil
}

func getDefaultGatewayInUnix() (string, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("route", "-n", "get", "default")
	} else if runtime.GOOS == "linux" {
		cmd = exec.Command("ip", "route")
	} else {
		return "", fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}

	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return "", err
	}

	output := out.String()

	if runtime.GOOS == "darwin" {
		re := regexp.MustCompile(`gateway: (\d+\.\d+\.\d+\.\d+)`)
		matches := re.FindStringSubmatch(output)
		if len(matches) > 1 {
			return matches[1], nil
		}
	} else if runtime.GOOS == "linux" {
		lines := strings.Split(output, "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "default") {
				fields := strings.Fields(line)
				if len(fields) >= 3 {
					return fields[2], nil
				}
			}
		}
	}

	return "", fmt.Errorf("could not parse default gateway")
}

// GetDefaultGatewayForTUN tries to extract default gateway IP on all platforms.
// It uses multiple fallback methods for Windows (PowerShell, netsh, ipconfig, route print)
// and Unix-specific commands for macOS/Linux.
func GetDefaultGatewayForTUN() (string, error) {
	var defaultGateway string
	var defaultRouteMetric int = 999999 // 设置一个较大的初始值

	if runtime.GOOS != "windows" {
		return getDefaultGatewayInUnix()
	}
	defaultGateway, err := GetDefaultGatewayWithPowerShell()
	if err != nil {
		logger.Error("Failure in method GetDefaultGatewayWithPowerShell: ", err)
	}

	if defaultGateway == "" {
		// 保存当前默认路由
		cmd := utils2.GetRunCommand("netsh", "interface", "ipv4", "show", "route")
		output, err := cmd.CombinedOutput()
		if err != nil {
			logger.Error("failed to get current routes with 'netsh interface': ", err)
		} else {
			// 转换输出编码
			outputStr, err := utils2.AutoConvertEncoding(output)
			if err != nil {
				logger.Error("failed to convert encoding: ", err)
			} else {
				// 解析输出找到默认路由
				lines := strings.Split(outputStr, "\n")
				// 跳过表头
				startParsing := false
				for _, line := range lines {
					// 跳过空行
					if strings.TrimSpace(line) == "" {
						continue
					}

					// 找到表头后的分隔线
					if strings.Contains(line, "-------") {
						startParsing = true
						continue
					}
					// 开始解析路由表
					if startParsing {
						fields := strings.Fields(line)
						if len(fields) >= 6 {
							// 检查是否是默认路由 (0.0.0.0/0)
							if fields[3] == "0.0.0.0/0" {
								// 解析跃点数
								metric := 0
								fmt.Sscanf(fields[2], "%d", &metric)

								// 选择跃点数最小的路由作为默认路由
								if metric < defaultRouteMetric {
									defaultRouteMetric = metric
									defaultGateway = fields[5] // 网关/接口名称在最后一列
								}
							}
						}
					}
				}
			}
		}
	}
	// 如果没有找到默认路由，尝试使用 ipconfig 命令
	if defaultGateway == "" {
		cmd := utils2.GetRunCommand("ipconfig")
		output, err := cmd.CombinedOutput()
		if err != nil {
			logger.Error("failed to excute 'ipconfig': ", err)
		} else {
			outputStr, err := utils2.AutoConvertEncoding(output)
			if err != nil {
				logger.Error("failed to convert `ipconfig` encoding: ", err)
			} else {
				// 查找默认网关
				lines := strings.Split(outputStr, "\n")
				for _, line := range lines {
					if strings.Contains(line, "默认网关") || strings.Contains(line, "Default Gateway") {
						fields := strings.Fields(line)
						for i, field := range fields {
							if field == ":" && i+1 < len(fields) {
								defaultGateway = fields[i+1]
								break
							}
						}
					}
				}
			}
		}

	}

	if defaultGateway == "" {
		// 如果仍然找不到默认网关，尝试使用 route print 命令
		cmd := utils2.GetRunCommand("route", "print")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("failed to get route print: %w", err)
		}

		outputStr, err := utils2.AutoConvertEncoding(output)
		if err != nil {
			logger.Error("failed to convert route print encoding: ", err)
		} else {
			lines := strings.Split(outputStr, "\n")
			for _, line := range lines {
				if strings.Contains(line, "0.0.0.0") {
					fields := strings.Fields(line)
					if len(fields) >= 4 {
						defaultGateway = fields[2]
						break
					}
				}
			}
		}

	}

	return defaultGateway, nil
}

func configureWindowsTunRoute() error {
	defaultGateway, err := GetDefaultGatewayForTUN()
	if err != nil {
		return err
	}

	if defaultGateway == "" {
		newErr := fmt.Errorf("无法找到默认网关，请检查网络连接")
		logger.Error(newErr)
		return newErr
	}

	logger.Info(fmt.Sprintf("找到默认网关: %s", defaultGateway))

	tunIfIndex, err := getWindowsTunInterfaceIndexByAddress("10.0.0.1")
	if err != nil {
		return fmt.Errorf("获取 TUN 接口索引失败: %w", err)
	}
	logger.Info(fmt.Sprintf("使用 TUN 接口索引: %d", tunIfIndex))

	if !setup.IsRoot() {
		UpdateStartupProgress("starting", "requesting_permission", 88, "Requesting Windows administrator permission to configure TUN routes.", "", true)
	}

	// 删除现有默认路由
	commands := [][]string{
		{"route", "delete", "0.0.0.0", "mask", "128.0.0.0", "10.0.0.1", "if", strconv.Itoa(tunIfIndex)},
		{"route", "delete", "128.0.0.0", "mask", "128.0.0.0", "10.0.0.1", "if", strconv.Itoa(tunIfIndex)},
	}

	for _, cmd := range commands {
		if err := runWindowsPrivilegedCommand(cmd[0], cmd[1:]...); err != nil {
			logger.Error(fmt.Sprintf("删除路由失败: %v", err))
		}
	}

	// 添加新的路由
	commands = [][]string{
		{"route", "add", "0.0.0.0", "mask", "128.0.0.0", "10.0.0.1", "metric", "1", "if", strconv.Itoa(tunIfIndex)},
		{"route", "add", "128.0.0.0", "mask", "128.0.0.0", "10.0.0.1", "metric", "1", "if", strconv.Itoa(tunIfIndex)},
		// 添加回原默认网关的路由，但优先级较低
	}

	for _, cmd := range commands {
		if err := runWindowsPrivilegedCommand(cmd[0], cmd[1:]...); err != nil {
			err = fmt.Errorf("添加路由失败: %w", err)
			logger.Error(err)
			return err
		}
	}

	return nil
}

func getWindowsTunInterfaceIndexByAddress(address string) (int, error) {
	cmd := utils2.GetRunCommand("powershell", "-NoProfile", "-Command",
		`$idx = Get-NetIPAddress -IPAddress "`+address+`" -AddressFamily IPv4 -ErrorAction SilentlyContinue | Select-Object -First 1 -ExpandProperty InterfaceIndex; if ($idx) { Write-Output $idx }`)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("failed to query interface index for %s: %w", address, err)
	}

	indexText := strings.TrimSpace(string(output))
	if indexText == "" {
		return 0, fmt.Errorf("no interface index found for %s", address)
	}

	index, err := strconv.Atoi(indexText)
	if err != nil {
		return 0, fmt.Errorf("invalid interface index %q for %s: %w", indexText, address, err)
	}

	return index, nil
}

func runWindowsPrivilegedCommand(name string, args ...string) error {
	if setup.IsRoot() {
		return utils2.RunCommand(name, args...)
	}
	return utils2.RunCommandElevated(name, args...)
}

func configureDarwinTunRoute() error {
	// nursorRouter := model.NewAllowProxyDomain()
	// allowdToGateUrls := nursorRouter.ToGateDomains

	var routes [][]string
	hasFakeIP := false

	// 如果没有匹配到 fakeip，就 fallback 用默认劫持
	if !hasFakeIP {
		routes = [][]string{
			{"route", "-n", "add", "-net", "1.0.0.0/8", "10.0.0.1"},
			{"route", "-n", "add", "-net", "2.0.0.0/7", "10.0.0.1"},
			{"route", "-n", "add", "-net", "4.0.0.0/6", "10.0.0.1"},
			{"route", "-n", "add", "-net", "8.0.0.0/5", "10.0.0.1"},
			{"route", "-n", "add", "-net", "32.0.0.0/3", "10.0.0.1"},
			{"route", "-n", "add", "-net", "64.0.0.0/2", "10.0.0.1"},
			{"route", "-n", "add", "-net", "128.0.0.0/1", "10.0.0.1"},
			{"route", "-n", "add", "-net", "198.18.0.0/15", "10.0.0.1"},
		}
	}

	// 执行路由命令
	for _, r := range routes {
		if err := utils2.RunCommand(r[0], r[1:]...); err != nil {
			errStr := fmt.Sprintf("route add failed: %v", err)
			logger.Error(errStr)
			return errors.New(errStr)
		}
	}
	return nil
}

// 判断是否是 198.18.x.x/15
func isFakeIP(ip net.IP) bool {
	return ip[0] == 198 && (ip[1] == 18 || ip[1] == 19)
}

func configureLinuxTunRoute() error {
	// 保存当前默认路由
	cmd := utils2.GetRunCommand("ip", "route", "show", "default")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get current routes: %w", err)
	}

	// 解析输出找到默认路由
	lines := strings.Split(string(output), "\n")
	var defaultGateway string
	for _, line := range lines {
		if strings.Contains(line, "default via") {
			fields := strings.Fields(line)
			for i, field := range fields {
				if field == "via" && i+1 < len(fields) {
					defaultGateway = fields[i+1]
					break
				}
			}
		}
	}

	if defaultGateway == "" {
		return fmt.Errorf("no default gateway found")
	}

	// 删除现有默认路由
	commands := [][]string{
		{"ip", "route", "del", "0.0.0.0/1", "via", "10.0.0.1"},
		{"ip", "route", "del", "128.0.0.0/1", "via", "10.0.0.1"},
	}

	for _, cmd := range commands {
		if err := utils2.RunCommand(cmd[0], cmd[1:]...); err != nil {
			errStr := fmt.Sprintf("Failed to delete route: %v", err)
			logger.Error(errStr)
		}
	}

	// 添加新的路由
	commands = [][]string{
		{"ip", "route", "add", "0.0.0.0/1", "via", "10.0.0.1", "metric", "1"},
		{"ip", "route", "add", "128.0.0.0/1", "via", "10.0.0.1", "metric", "1"},
		// 添加回原默认网关的路由，但优先级较低
	}

	for _, cmd := range commands {
		if err := utils2.RunCommand(cmd[0], cmd[1:]...); err != nil {
			errStr := fmt.Sprintf("ip route add failed: %v", err)
			logger.Error(errStr)
			return errors.New(errStr)
		}
	}

	return nil
}
