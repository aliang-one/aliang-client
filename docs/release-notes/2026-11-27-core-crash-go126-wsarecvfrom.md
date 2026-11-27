# Windows core 随机崩溃退出修复——升级说明(2026-11-27)

本版修复 Windows 上后台服务(core)**随机退出**的根因:崩溃由 Go 1.26.0 标准库的 Windows UDP 异步收包缺陷触发,与业务代码无关。修复方式为构建工具链锁定 **Go 1.26.11**(1.26.0 之后的补丁版已修复该缺陷)。**Windows 用户建议升级。**

## 现象

- Windows 机器上 core 服务不定时退出(事件日志 7031/7034),正在跑的 AI 回合、仪表盘、代理随之中断;若该机未配置服务恢复动作,服务会一直停在「已停止」状态。
- 崩溃日志(`C:\ProgramData\Aliang\logs\aliang_crash.log`)特征固定:
  `fatal error: fault`,栈在 `runtime.sellock ← runtime.selectgo ← go-winio asyncIO`(IPC 读取路径),被击中的指针高 32 位被覆写为小整数 **16**。

## 根因(已字节级闭环)

Go 1.26.0 的 `internal/poll` 在 Windows UDP 收包(`RecvFrom`——DNS 解析、组网广播、打洞等全部经过它)中,把**栈上**的 sockaddr 缓冲交给异步 overlapped `WSARecvFrom`。当该 goroutine 的栈被 GC 收缩后,内核迟到的完成写(`*lpFromlen = 16`,即 IPv4 sockaddr 长度)落进**已回收并被复用的旧栈内存**(常被复用为其它 goroutine 的栈),偶发打中指针槽;core 的 IPC 读协程被击中后,下一次解引用坏指针即 `fatal error: fault`。

- **取证**:0.196 当日 7 次崩溃指纹完全一致(单次 4 字节写、值恒为 16、被写槽位于 select 帧);WER 全量 dump 分析确认通道机制(hchan/sudog/接收缓冲)、内核结构体调用等均无异常,仅该栈槽被单次写入。
- **时间线**:11-13 切换 go1.26.0 后 11-18 首次出现该崩溃族;1.25.x 不含此缺陷(相关字段在堆上)。
- **上游修复**:1.26.11 将 sockaddr 与长度打包为堆上的 `wsaRsa`,每次操作重置并延后归还(源码注释明确「When used by WSARecvFrom, wsaRsa must be on the heap」)。

## 构建与发布

- `go.mod` 新增 `toolchain go1.26.11`:GOTOOLCHAIN 默认 auto,发布流水线即使只预装 1.26.0,go 命令也会自动切换到 1.26.11 构建。
- **发版自检**:安装后运行 `aliang version`,构建行应显示 `go1.26.11`。

## 升级建议

1. Windows 用户升级后用 `aliang version` 确认工具链版本(应为 go1.26.11)。
2. 建议确认服务恢复动作已配置(崩溃后自动拉起):

   ```powershell
   sc failure alianggate reset= 86400 actions= restart/5000/restart/30000/restart/60000
   ```

3. 升级后观察数小时;若 `aliang_crash.log` 不再新增 `fatal error:`,即为修复生效。
