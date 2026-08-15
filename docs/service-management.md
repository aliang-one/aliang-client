# 跨平台服务管理功能

本文档介绍如何使用 Nursorgate 的跨平台服务管理功能，将程序安装为系统服务。

## 功能概述

Nursorgate 支持在多个平台上将程序安装为系统服务：

- **macOS**: LaunchDaemon (系统级) 或 LaunchAgent (用户级)
- **Linux**: systemd 服务 (系统级或用户级)
- **Windows**: Windows 服务 (需要管理员权限)

## 命令使用

### 1. 安装服务

#### 用户级服务安装（推荐）

```bash
# 安装为用户服务（不需要 sudo）
aliang service install --config ~/.aliang/config.json

# 安装后立即启动
aliang service install --config ~/.aliang/config.json --start
```

#### 系统级服务安装（需要管理员权限）

```bash
# macOS/Linux
sudo aliang service install --system-wide --config /etc/aliang/config.json

# Windows (以管理员身份运行)
aliang service install --system-wide --config C:\aliang\config.json

# 安装后立即启动
sudo aliang service install --system-wide --config /etc/aliang/config.json --start
```

### 2. 启动服务

```bash
# 启动用户服务
aliang service start

# 启动系统服务
sudo aliang service start
```

### 3. 停止服务

```bash
# 停止用户服务
aliang service stop

# 停止系统服务
sudo aliang service stop
```

### 4. 重启服务

```bash
# 重启用户服务
aliang service restart

# 重启系统服务
sudo aliang service restart
```

### 5. 查看服务状态

```bash
# 查看用户服务状态
aliang service status

# 查看系统服务状态
sudo aliang service status
```

输出示例：
```
Service Status:
  Installed: true
  Running:   true
  Status:    running
  PID:       12345
```

### 6. 卸载服务

```bash
# 卸载用户服务
aliang service uninstall

# 卸载系统服务
sudo aliang service uninstall
```

## 平台特定信息

### macOS

#### LaunchDaemon (系统级)
- 服务文件位置: `/Library/LaunchDaemons/org.nursor.nursorgate.plist`
- 日志位置: `/var/log/nursorgate.log` 和 `/var/log/nursorgate.error.log`
- 需要 root 权限
- 开机自动启动

#### LaunchAgent (用户级)
- 服务文件位置: `~/Library/LaunchAgents/org.nursor.nursorgate.plist`
- 日志位置: `~/Library/Logs/nursorgate.log` 和 `~/Library/Logs/nursorgate.error.log`
- 用户登录后自动启动

#### 手动管理命令

```bash
# 加载服务
launchctl load ~/Library/LaunchAgents/org.nursor.nursorgate.plist

# 卸载服务
launchctl unload ~/Library/LaunchAgents/org.nursor.nursorgate.plist

# 查看服务状态
launchctl list | grep nursorgate
```

### Linux

#### 系统级服务
- 服务文件位置: `/etc/systemd/system/nursorgate.service`
- 使用 systemd 管理
- 开机自动启动
- 需要 root 权限

#### 用户级服务
- 服务文件位置: `~/.config/systemd/user/nursorgate.service`
- 使用 systemd --user 管理
- 用户登录后自动启动

#### 手动管理命令

```bash
# 系统级服务
sudo systemctl start nursorgate
sudo systemctl stop nursorgate
sudo systemctl restart nursorgate
sudo systemctl status nursorgate
sudo systemctl enable nursorgate  # 开机自启
sudo systemctl disable nursorgate # 禁用自启

# 用户级服务
systemctl --user start nursorgate
systemctl --user stop nursorgate
systemctl --user restart nursorgate
systemctl --user status nursorgate
systemctl --user enable nursorgate
systemctl --user disable nursorgate

# 重新加载 systemd 配置
sudo systemctl daemon-reload  # 系统级
systemctl --user daemon-reload  # 用户级
```

### Windows

#### Windows 服务
- 使用 Windows Service Control Manager 管理
- 需要管理员权限
- 服务名称: `nursorgate`
- 显示名称: `Nursorgate Network Service`

#### 手动管理命令

```cmd
# 以管理员身份运行 CMD 或 PowerShell

# 启动服务
sc start nursorgate

# 停止服务
sc stop nursorgate

# 查询服务状态
sc query nursorgate

# 或者使用 PowerShell
Start-Service nursorgate
Stop-Service nursorgate
Get-Service nursorgate
```

## 故障排除

### 权限错误

**错误信息**: `Error: This operation requires root/administrator privileges.`

**解决方法**:
- macOS/Linux: 使用 `sudo` 运行命令
- Windows: 以管理员身份运行命令提示符或 PowerShell

### 服务已存在

**错误信息**: `Error: Service is already installed.`

**解决方法**:
```bash
# 先卸载现有服务
aliang service uninstall --system-wide

# 然后重新安装
aliang service install --system-wide --config /path/to/config.json
```

### 服务未安装

**错误信息**: `Error: Service is not installed.`

**解决方法**:
```bash
# 先安装服务
aliang service install --config /path/to/config.json
```

### 查看详细日志

```bash
# macOS (系统级)
tail -f /var/log/nursorgate.log
tail -f /var/log/nursorgate.error.log

# macOS (用户级)
tail -f ~/Library/Logs/nursorgate.log
tail -f ~/Library/Logs/nursorgate.error.log

# Linux (使用 journalctl)
# 系统级
sudo journalctl -u nursorgate -f

# 用户级
journalctl --user -u nursorgate -f

# Windows
# 使用事件查看器 (Event Viewer)
# 或者查看应用程序日志
```

## 实现细节

### 架构设计

```
processor/setup/
├── service_interface.go  # 统一的服务管理接口
├── utils.go              # 公共工具函数
├── templates.go          # 配置模板
├── setup.go              # 公共 API
├── setup_darwin.go       # macOS 实现
├── setup_linux.go        # Linux 实现
└── setup_windows.go      # Windows 实现
```

### 核心接口

```go
type ServiceManager interface {
    Install(options InstallOptions) error
    Uninstall() error
    Start() error
    Stop() error
    Restart() error
    Status() (*ServiceStatus, error)
    IsInstalled() bool
    GetName() string
}
```

## 开发者注意事项

### 添加新的服务管理器

如果需要支持其他平台或 init 系统：

1. 创建新的平台文件，例如 `setup_freebsd.go`
2. 实现 `ServiceManager` 接口
3. 在文件中实现 `NewServiceManager` 函数
4. Go 编译器会根据构建标签自动选择正确的实现

### 自定义服务配置

可以通过编程方式使用服务管理功能：

```go
import "aliang.one/nursorgate/processor/setup"

options := setup.InstallOptions{
    Name:           "my-service",
    DisplayName:    "My Custom Service",
    Description:    "My custom service description",
    ExecutablePath: "/usr/local/bin/my-program",
    ConfigPath:     "/etc/my-service/config.json",
    SystemWide:     true,
    StartType:      setup.StartAutomatic,
    Args:          []string{"--verbose"},
    Env:           map[string]string{"ENV": "production"},
}

err := setup.InstallService(options)
```

## 测试

### 单元测试

```bash
# 运行所有测试
go test ./processor/setup/...

# 运行特定平台的测试
go test ./processor/setup/... -tags=darwin
go test ./processor/setup/... -tags=linux
go test ./processor/setup/... -tags=windows
```

### 集成测试

在实际环境中测试：

```bash
# 安装服务
sudo ./aliang-test service install --system-wide

# 检查状态
sudo ./aliang-test service status

# 测试重启
sudo ./aliang-test service restart

# 卸载服务
sudo ./aliang-test service uninstall
```

## 安全考虑

1. **权限控制**: 系统级服务需要 root/管理员权限
2. **文件权限**: 服务配置文件权限正确设置 (644)
3. **路径验证**: 验证可执行文件和配置文件路径
4. **错误处理**: 详细的错误信息和回滚机制
5. **日志记录**: 所有关键操作都有日志记录

## 最佳实践

1. **生产环境**: 使用系统级服务 (需要 root)
2. **开发环境**: 使用用户级服务 (不需要 root)
3. **配置管理**: 将配置文件放在标准位置
4. **日志监控**: 定期检查日志文件
5. **定期更新**: 保持服务版本更新

## 常见问题

### Q: 用户级服务和系统级服务有什么区别？

A: 
- **系统级服务**: 
  - 需要 root/管理员权限
  - 开机时自动启动（用户登录前）
  - 适合生产环境和服务器

- **用户级服务**:
  - 不需要 root 权限
  - 用户登录后自动启动
  - 适合开发和测试环境

### Q: 如何更改服务配置？

A: 修改配置文件后，重启服务：
```bash
sudo aliang service restart
```

### Q: 服务启动失败怎么办？

A: 
1. 查看服务状态: `aliang service status`
2. 查看错误日志:
   - macOS: `/var/log/nursorgate.error.log`
   - Linux: `journalctl -u nursorgate -n 50`
   - Windows: 事件查看器
3. 验证配置文件路径是否正确
4. 验证可执行文件权限

## 更新日志

### v1.0.0 (2025-01-15)
- ✅ 初始实现跨平台服务管理功能
- ✅ 支持 macOS LaunchDaemon/LaunchAgent
- ✅ 支持 Linux systemd
- ✅ 支持 Windows 服务
- ✅ 提供统一的命令行接口
- ✅ 提供编程 API

## 贡献

欢迎贡献代码！请确保：
1. 添加必要的测试
2. 遵循代码规范
3. 更新文档
4. 测试所有平台

## 许可证

本项目采用与主项目相同的许可证。