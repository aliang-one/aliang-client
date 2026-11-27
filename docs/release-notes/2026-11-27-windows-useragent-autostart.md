# Windows「设备 agent 可能未运行」修复——升级说明(2026-11-27)

本版修复 Windows 客户机重启/注销后仪表盘 agent 页与 aliangcode 弹「操作失败 / 读取失败:设备 agent 可能未运行」的两处根因,并给 MSI 增加登录自启。**Windows 用户建议升级并重启一次**;macOS/Linux 用户无感。

## 修复了什么

### ① companion(托盘)登录自启(MSI 新增)

此前 Windows 重启后只有 core 后台服务自动起来(仪表盘能打开),但负责拉起并看护用户态 Agent 的托盘程序 **不会自动启动**——除非每次开机后手动双击桌面 Aliang 图标。此时仪表盘一切页面正常,唯独 Agent 页「用户态 agent」/「aliangcode」子页与手机远程功能报「设备 agent 可能未运行」。

新 MSI 在 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` 注册自启项,登录即静默拉起托盘(不弹浏览器、不打扰),托盘再拉起并看护用户态 Agent。卸载时自启项随之移除。

### ② core 守护进程误拉起「错误身份」Agent 的缺陷(2026-08 起存在)

core 服务此前在每次登录事件后可能以系统账户(LocalSystem)再拉起一个家目录错位的 Agent,与正常用户 Agent 抢端口互相顶替,造成 Agent 时好时坏。本版 core 明确不再拉起,托盘是唯一看护者。

## ⚠️ 升级注意(Windows)

1. **升级前请右键托盘图标退出 Aliang**:新版安装包会尝试自动关闭常驻的托盘/Agent 进程,但手工退出最稳;未退出的极端情形下文件替换会延后到下次重启。
2. **自启只对「执行安装的那个账户」生效**:per-machine 安装形态下,若用 A 管理员账户装、日常登录 B 账户,B 账户需手动双击一次 Aliang 图标(之后正常;如需 B 账户也自启,在 B 账户下重新执行安装包)。
3. **静默启动不打扰**:登录时托盘自动就位,不再自动弹出仪表盘浏览器窗口;面板随时从托盘菜单打开。
4. 若某次登录时后台服务异常未就绪,自启会静默跳过该轮(不弹错误框),下次登录或手动双击即恢复;极少数情况下(后台服务未在运行)自启会请求一次管理员权限来启动服务,属预期行为。

## 排障速查(升级后仍弹「设备 agent 可能未运行」时)

```powershell
# 用户态 Agent 是否在听(拒连=没起来)
curl.exe -m 5 http://127.0.0.1:56433/api/agent/protocol

# 三个进程谁在跑(core 服务 / agent / companion 托盘)
Get-Process | Where-Object {$_.Name -like '*aliang*'}
```

双击桌面 Aliang 图标 → 等 30 秒 → 刷新页面,一般即恢复。仍未恢复时收集 `%USERPROFILE%\.aliang\agent\agent.log` 反馈。
