# 服务管理快速入门

本文档将帮助你在 5 分钟内将 Nursorgate 安装为系统服务。

## 前提条件

- 已编译好的 Nursorgate 可执行文件
- 配置文件 `config.json`

## 快速开始

### 选项 1: 用户级服务（推荐新手）

**优点**: 不需要 sudo/admin 权限，更安全
**缺点**: 需要用户登录后才启动

#### macOS / Linux

```bash
# 1. 安装服务
aliang service install --config ~/aliang/config.json

# 2. 验证服务状态
aliang service status

# 3. (可选) 查看日志
tail -f ~/Library/Logs/nursorgate.log  # macOS
tail -f /var/log/nursorgate.log        # Linux
```

#### Windows

```powershell
# 以普通用户身份运行 PowerShell

# 1. 安装服务 (需要管理员权限，用户级暂不支持 Windows)
# Windows 只支持系统级服务
```

### 选项 2: 系统级服务（推荐生产环境）

**优点**: 开机自动启动，适合服务器
**缺点**: 需要 sudo/admin 权限

#### macOS

```bash
# 1. 安装服务
sudo aliang service install --system-wide --config /etc/aliang/config.json

# 2. 验证服务状态
sudo aliang service status

# 3. 查看日志
sudo tail -f /var/log/nursorgate.log
```

#### Linux

```bash
# 1. 安装服务
sudo aliang service install --system-wide --config /etc/aliang/config.json

# 2. 验证服务状态
sudo aliang service status

# 3. 查看日志
sudo journalctl -u nursorgate -f
```

#### Windows

```powershell
# 以管理员身份运行 PowerShell

# 1. 安装服务
aliang service install --system-wide --config C:\aliang\config.json

# 2. 验证服务状态
aliang service status

# 3. 查看服务状态 (Windows 原生命令)
sc query nursorgate
```

## 常用命令速查表

| 操作 | 用户级服务 | 系统级服务 |
|------|-----------|-----------|
| 安装 | `aliang service install` | `sudo aliang service install --system-wide` |
| 启动 | `aliang service start` | `sudo aliang service start` |
| 停止 | `aliang service stop` | `sudo aliang service stop` |
| 重启 | `aliang service restart` | `sudo aliang service restart` |
| 状态 | `aliang service status` | `sudo aliang service status` |
| 卸载 | `aliang service uninstall` | `sudo aliang service uninstall` |

## 一键安装并启动

```bash
# 用户级
aliang service install --config ~/aliang/config.json --start

# 系统级
sudo aliang service install --system-wide --config /etc/aliang/config.json --start
```

## 验证安装

### 检查服务状态

```bash
aliang service status
```

预期输出：
```
Service Status:
  Installed: true
  Running:   true
  Status:    running
  PID:       12345
```

### 手动测试

```bash
# 测试服务是否正常工作
curl http://localhost:8080/health
```

## 配置文件位置建议

### macOS

```bash
# 用户级
~/aliang/config.json
~/.aliang/config.json

# 系统级
/etc/aliang/config.json
/usr/local/etc/aliang/config.json
```

### Linux

```bash
# 用户级
~/aliang/config.json
~/.aliang/config.json

# 系统级
/etc/aliang/config.json
/etc/nursorgate/config.json
```

### Windows

```powershell
# 系统级 (Windows 只支持系统级服务)
C:\aliang\config.json
C:\ProgramData\aliang\config.json
```

## 日志位置

### macOS

```bash
# 用户级
~/Library/Logs/nursorgate.log          # 标准输出
~/Library/Logs/nursorgate.error.log    # 错误输出

# 系统级
/var/log/nursorgate.log
/var/log/nursorgate.error.log
```

### Linux

```bash
# 使用 systemd journal
journalctl -u nursorgate              # 用户级
sudo journalctl -u nursorgate         # 系统级

# 或者直接查看日志文件（如果配置了）
/var/log/nursorgate.log
/var/log/nursorgate.error.log
```

### Windows

```powershell
# 使用事件查看器 (Event Viewer)
eventvwr.msc

# 或使用 PowerShell
Get-EventLog -LogName Application -Source nursorgate
```

## 故障排除

### 问题 1: 权限被拒绝

**错误**: `Error: This operation requires root/administrator privileges.`

**解决**: 
- macOS/Linux: 在命令前加 `sudo`
- Windows: 以管理员身份运行

### 问题 2: 服务已存在

**错误**: `Error: Service is already installed.`

**解决**: 
```bash
# 先卸载
aliang service uninstall
# 再安装
aliang service install --config path/to/config.json
```

### 问题 3: 服务启动失败

**检查步骤**: 

1. 验证配置文件是否存在
```bash
ls -l /path/to/config.json
```

2. 验证可执行文件权限
```bash
ls -l /path/to/aliang
chmod +x /path/to/aliang  # 如果没有执行权限
```

3. 查看错误日志
```bash
# macOS/Linux
tail -f /var/log/nursorgate.error.log

# Linux (systemd)
sudo journalctl -u nursorgate -n 50
```

4. 手动运行测试
```bash
# 停止服务
aliang service stop

# 手动运行查看错误
aliang --config /path/to/config.json
```

## 下一步

- 📖 阅读完整文档: [服务管理详细文档](./service-management.md)
- 🔧 配置说明: [配置文件说明](./configuration.md)
- 🐛 问题反馈: [GitHub Issues](https://github.com/your-repo/issues)

## 需要帮助？

如果遇到问题：

1. 查看日志文件
2. 运行 `aliang service status` 检查状态
3. 尝试手动运行程序查看错误
4. 查阅完整文档
5. 在 GitHub 上提交 Issue

## 卸载

如果需要完全移除服务：

```bash
# 1. 停止并卸载服务
aliang service uninstall

# 2. (可选) 删除配置文件
rm -rf ~/.aliang  # 用户级
sudo rm -rf /etc/aliang  # 系统级

# 3. (可选) 删除日志文件
rm -rf ~/Library/Logs/nursorgate*  # macOS 用户级
sudo rm -rf /var/log/nursorgate*  # 系统级
```

## 更新服务

当有新版本时：

```bash
# 1. 停止服务
aliang service stop

# 2. 替换可执行文件
cp aliang-new-version /usr/local/bin/aliang

# 3. 启动服务
aliang service start

# 或直接重启
aliang service restart
```

---

**🎉 恭喜！你已经成功将 Nursorgate 安装为系统服务！**

现在你可以：
- ✅ 服务会在后台持续运行
- ✅ 开机自动启动（如果是系统级服务）
- ✅ 崩溃后自动重启
- ✅ 方便地管理服务生命周期