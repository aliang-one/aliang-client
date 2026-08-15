"# 默认托盘模式 - 快速指南

## ✅ 已完成的修改

### 1. **默认启动方式改为托盘模式**
- 运行 `./nursorgate` 默认以托盘模式启动（无需指定 `tray` 命令）
- 原来的 `./nursorgate start` 仍然可用（纯命令行模式）

### 2. **托盘图标状态支持**
- **灰色图标**：服务器停止状态
- **彩色图标**：服务器运行状态
- 图标会根据服务器状态自动切换

### 3. **移除托盘旁边的文字**
- ✅ 已移除 `SetTitle("Aliang")`
- 托盘只显示图标，不显示文字
- 状态信息通过 Tooltip 显示

## 🚀 使用方法

### 默认托盘模式

```bash
# 直接运行（推荐）
./nursorgate

# 或使用配置文件
./nursorgate --config ./config.json

# 或使用 token
./nursorgate --token your-token
```

### 纯命令行模式（无 GUI）

```bash
# 使用 start 子命令
./nursorgate start
./nursorgate start --config ./config.json
```

## 🎨 托盘图标状态

| 状态 | 图标颜色 | Tooltip | 菜单状态 |
|------|---------|---------|---------|
| **已停止** | 灰色 | Aliang - Stopped | Start: ✅<br>Stop: ❌<br>Restart: ❌ |
| **运行中** | 彩色（蓝色） | Aliang - Running | Start: ❌<br>Stop: ✅<br>Restart: ✅ |

## 📋 测试清单

### 基础功能测试

```bash
# 1. 编译
make build
# 或
go build -o dist/nursorgate cmd/nursor/main.go

# 2. 运行（托盘模式）
./dist/nursorgate

# 3. 观察托盘图标
# - 应该显示在系统托盘区域
# - 初始状态应该是彩色图标（服务器自动启动）
# - Tooltip 应该显示 "Aliang - Running"

# 4. 右键菜单测试
# - Open Dashboard: 应该打开浏览器
# - Stop Server: 图标应该变灰色
# - Start Server: 图标应该变彩色
# - Restart Server: 图标应该先变灰再变彩色

# 5. 退出测试
# - 点击 Quit: 应该安全退出
```

### 图标状态切换测试

1. **启动应用**
   - ✅ 图标应该是彩色（蓝色）
   - ✅ Tooltip: "Aliang - Running"

2. **停止服务器**（右键 → Stop Server）
   - ✅ 图标应该变成灰色
   - ✅ Tooltip: "Aliang - Stopped"
   - ✅ Stop 和 Restart 菜单应该禁用

3. **启动服务器**（右键 → Start Server）
   - ✅ 图标应该变成彩色
   - ✅ Tooltip: "Aliang - Running"
   - ✅ Start 菜单应该禁用

4. **重启服务器**（右键 → Restart Server）
   - ✅ 图标应该先变灰再变彩色
   - ✅ 服务器应该重启成功

### 不同启动方式测试

```bash
# 测试 1: 默认托盘模式
./dist/nursorgate
# 应该显示托盘图标

# 测试 2: 带 config 文件
./dist/nursorgate --config ./config.json
# 应该加载配置文件并显示托盘

# 测试 3: 带 token
./dist/nursorgate --token test-token
# 应该尝试激活用户并显示托盘

# 测试 4: 纯命令行模式
./dist/nursorgate start
# 不应该显示托盘，直接运行服务器
```

## 🔧 故障排除

### 图标不显示

**Linux:**
```bash
# 检查依赖
sudo apt-get install libappindicator3-dev

# GNOME 用户需要安装扩展
# 安装 AppIndicator 扩展
```

**macOS:**
- 检查菜单栏是否有空间
- 尝试重启应用

**Windows:**
- 检查隐藏的托盘图标
- 尝试重启资源管理器

### 图标状态不切换

1. 检查日志输出
2. 确认 HTTP 服务器确实启动/停止
3. 检查 `app/tray/icon-active.png` 和 `icon-inactive.png` 是否存在

### 编译错误

```bash
# 清理并重新编译
go clean
go mod tidy
go build -o dist/nursorgate cmd/nursor/main.go
```

## 📁 文件结构

```
app/tray/
├── tray.go               # 托盘主逻辑（已更新图标切换）
├── icon.go               # 图标嵌入（已更新双图标）
├── icon-active.png       # 彩色图标（运行状态）
├── icon-inactive.png     # 灰色图标（停止状态）
└── generate_icon.go      # 图标生成脚本

cmd/
├── root.go              # 根命令（默认启动 tray 模式）
├── start.go             # start 子命令（纯命令行模式）
└── tray.go              # tray 子命令（显式托盘模式）

app/http/
└── server.go            # HTTP 服务器（含启停功能）
```

## 🎯 行为对比

| 命令 | 模式 | 托盘图标 | 适用场景 |
|------|------|---------|---------|
| `./nursorgate` | 托盘模式（默认） | ✅ | 桌面应用 |
| `./nursorgate tray` | 托盘模式（显式） | ✅ | 桌面应用 |
| `./nursorgate start` | 命令行模式 | ❌ | 服务器部署 |

## 💡 提示

1. **推荐使用默认托盘模式**
   ```bash
   ./nursorgate  # 简单直接
   ```

2. **服务器部署使用 start 命令**
   ```bash
   ./nursorgate start  # 无 GUI，适合后台运行
   ```

3. **自定义图标**
   - 替换 `app/tray/icon-active.png`（彩色）
   - 替换 `app/tray/icon-inactive.png`（灰色）
   - 重新编译

4. **查看托盘状态**
   - 鼠标悬停在图标上查看 Tooltip
   - 灰色 = 停止
   - 彩色 = 运行

## 🎉 完成！

所有功能已经实现：
- ✅ 默认托盘模式启动
- ✅ 图标状态切换（灰/彩）
- ✅ 移除托盘文字
- ✅ 纯命令行模式保留

现在可以直接运行 `./dist/nursorgate` 测试！"