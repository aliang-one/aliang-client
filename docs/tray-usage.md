"# System Tray Usage Guide

## 概述

Aliang 现在支持系统托盘模式，提供图形化界面来管理服务器。

## 功能特性

- ✅ **跨平台支持**: Linux, macOS, Windows
- ✅ **右键菜单**: 快速访问常用操作
- ✅ **服务管理**: 启动/停止/重启服务器
- ✅ **仪表板访问**: 一键打开 Web 界面
- ✅ **状态指示**: 实时显示服务器状态
- ✅ **优雅退出**: 安全关闭所有服务

## 使用方法

### 1. 启动系统托盘

```bash
# 基本用法
./nursorgate tray

# 使用配置文件
./nursorgate tray --config ./config.json

# 使用 Token 激活
./nursorgate tray --token your-token-here

# 组合使用
./nursorgate tray --config ./config.json --token your-token-here
```

### 2. 菜单选项

启动后，系统托盘会显示在系统托盘区域（Windows：任务栏右下角；macOS：菜单栏右侧；Linux：系统托盘）。

**右键菜单包含：**

1. **Open Dashboard**
   - 在默认浏览器中打开 Web 管理界面
   - 地址：http://localhost:56431

2. **Start Server**
   - 启动 HTTP 服务器
   - 仅在服务器停止时可用

3. **Stop Server**
   - 停止 HTTP 服务器
   - 仅在服务器运行时可用

4. **Restart Server**
   - 重启 HTTP 服务器
   - 仅在服务器运行时可用

5. **Version: AliangCore-vX.X.X**
   - 显示当前版本（只读）

6. **Quit**
   - 安全退出应用程序
   - 自动停止服务器

### 3. 启动行为

- 托盘启动后，服务器会**自动启动**
- 图标会显示"Running"状态提示
- 如果配置文件不存在，会使用默认配置

### 4. 与命令行模式对比

| 功能 | 命令行模式 (`aliang start`) | 托盘模式 (`aliang tray`) |
|------|---------------------------|--------------------------|
| 服务器自动启动 | ✅ | ✅ |
| 图形界面 | ❌ | ✅ |
| 菜单操作 | ❌ | ✅ |
| 后台运行 | ✅ (需要 `&` 或服务) | ✅ |
| 实时控制 | ❌ (需要重启) | ✅ (动态启停) |
| 适合场景 | 服务器/自动化 | 桌面应用 |

## 平台特定说明

### Linux

**依赖安装：**

```bash
# Ubuntu/Debian
sudo apt-get install libappindicator3-dev

# Fedora
sudo dnf install libappindicator-gtk3-devel

# Arch Linux
sudo pacman -S libappindicator
```

**桌面环境支持：**
- GNOME: 需要扩展（如 AppIndicator）
- KDE Plasma: 原生支持
- XFCE: 原生支持
- i3wm: 需要配置

### macOS

- 无需额外依赖
- 图标显示在菜单栏右侧
- 如果未显示，检查系统偏好设置中的通知设置

### Windows

- 无需额外依赖
- 图标显示在任务栏通知区域
- 可能需要在"隐藏图标"中找到它

## 开机自启动（可选）

### Linux (systemd)

创建服务文件 `~/.config/systemd/user/nonelane-tray.service`:

```ini
[Unit]
Description=Aliang System Tray
After=graphical-session.target

[Service]
Type=simple
ExecStart=/path/to/nursorgate tray
Restart=on-failure

[Install]
WantedBy=default.target
```

启用服务：

```bash
systemctl --user enable aliang-tray.service
systemctl --user start aliang-tray.service
```

### macOS (LaunchAgent)

创建 `~/Library/LaunchAgents/com.aliang.tray.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.aliang.tray</string>
    <key>ProgramArguments</key>
    <array>
        <string>/path/to/nursorgate</string>
        <string>tray</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
</dict>
</plist>
```

加载：

```bash
launchctl load ~/Library/LaunchAgents/com.aliang.tray.plist
```

### Windows (Registry)

创建注册表项（需要管理员权限）：

```reg
Windows Registry Editor Version 5.00

[HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Run]
"Aliang"="C:\\path\\to\\nursorgate.exe tray"
```

或使用任务计划程序。

## 自定义图标

要使用自定义图标：

1. 准备一个 PNG 图片（推荐 64x64 或 128x128 像素）
2. 替换 `app/tray/icon.png`
3. 重新编译：

```bash
go build -o dist/nursorgate cmd/nursor/main.go
```

## 故障排除

### 托盘图标不显示

**Linux:**
- 检查是否安装了 `libappindicator`
- 检查桌面环境是否支持系统托盘
- GNOME 用户需要安装 AppIndicator 扩展

**macOS:**
- 检查菜单栏是否太满
- 重启应用

**Windows:**
- 检查是否在"隐藏图标"中
- 尝试重启资源管理器

### 服务器无法启动

1. 检查端口 56431 是否被占用
2. 查看日志文件
3. 尝试命令行模式以获取详细错误信息

```bash
./nursorgate start
```

### 菜单点击无响应

- 重启应用程序
- 检查日志文件
- 确保服务器端口未被占用

## 开发说明

### 模块结构

```
app/tray/
├── tray.go           # 托盘主逻辑
├── icon.go           # 图标嵌入
├── icon.png          # 图标文件
├── generate_icon.go  # 图标生成脚本
└── README.md         # 模块文档

cmd/
└── tray.go           # 命令行入口

app/http/
└── server.go         # HTTP 服务器（含停止功能）
```

### API 参考

**启动托盘：**

```go
import "aliang.one/nursorgate/app/tray"

tray.Run()  // 阻塞直到退出
```

**HTTP 服务器控制：**

```go
import httpServer "aliang.one/nursorgate/app/http"

httpServer.StartHttpServer()  // 启动服务器
httpServer.StopHttpServer()   // 停止服务器
httpServer.IsServerRunning()  // 检查状态
```

### 扩展菜单

在 `app/tray/tray.go` 中的 `onReady()` 函数添加新菜单项：

```go
mNewFeature := systray.AddMenuItem("New Feature", "Description")

go func() {
    for {
        select {
        case <-mNewFeature.ClickedCh:
            // 处理点击事件
        }
    }
}()
```

## 最佳实践

1. **生产环境**: 使用 `start` 命令（无 GUI）
2. **开发环境**: 使用 `tray` 命令（方便调试）
3. **桌面用户**: 使用 `tray` 命令（用户体验好）
4. **服务器部署**: 使用 systemd 服务 + `start` 命令

## 更新日志

### v1.0.0 (2025-01-XX)
- ✅ 初始版本
- ✅ 跨平台支持
- ✅ 基础菜单功能
- ✅ 服务器启停控制
- ✅ 优雅退出机制
"