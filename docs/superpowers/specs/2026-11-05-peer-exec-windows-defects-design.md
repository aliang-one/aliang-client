# 设计：peer_exec Windows 端四缺陷修复（env 系统键/UTF-8 输出/工具语义指引）

日期：2026-11-05
状态：已按用户「继续」授权推进（缺陷根因与修法均无设计分叉）
批次性质：Go 后端小修（app/http/services），不动协议契约帧、不动 PhoneServer

## 1. 背景（2026-11-05 真人实测证据）

用 aliang-peer MCP 对 CHINAMI（Windows 22H2，agent 1.1.94，peer_exec/fetch 已开）实测跨设备能力时抓到 4 个 Windows 端缺陷：

1. **执行壳语义**：B 端走 `powershell.exe -Command`（PS 5.1），`&&` 不可用报语法错；外部 exe（`hostname`）CommandNotFound。
2. **最小 env 白名单缺 Windows 系统键**：白名单五键（PATH/HOME/TERM/LANG/TMPDIR）是 POSIX 视角——`$env:TEMP` 为空（相对路径落到盘根 C:\ 实证），SystemRoot/ComSpec/PATHEXT 缺失。
3. **GBK 乱码**：PS 5.1 的 stdout/stderr 用系统 ANSI 代码页（中文系统=GBK）编码，Go 当 UTF-8 收 → PowerShell 报错全乱码。
4. **多语句 stdout 只回末行**（`a; b; c` 只出 c）——根因未诊（CHINAMI 已离线，1.1.94 行为），本批次挂起不修。

另确认两项不在本批次：note 读回缺口在 PhoneServer（device_info 两跳 GET 服务端数据，跨仓）；PS7/pwsh 升级不做（契约改动大收益低）。

## 2. 修复设计

### Fix 1：Windows env 系统键补齐（`agentPeerExecEnv`，修缺陷①②的根）

重构出可跨平台测试的纯函数（如 `agentPeerExecEnvFor(goos string)`），Windows 分支在五键之上增补（值取 agent 进程 env、无则兜底，保持"逐键取值不继承其余"的防泄漏纪律——新增键全是系统变量，非凭据）：

| 键 | 兜底值 |
|---|---|
| `SystemRoot` | `C:\Windows` |
| `windir` | `C:\Windows` |
| `ComSpec` | `C:\Windows\system32\cmd.exe` |
| `PATHEXT` | `.COM;.EXE;.BAT;.CMD` |
| `TEMP`/`TMP` | `os.TempDir()` |
| `USERPROFILE` | `agentPeerHomeValue()` |
| PATH 兜底 | `C:\Windows\System32;C:\Windows;C:\Windows\System32\Wbem;C:\Windows\System32\WindowsPowerShell\v1.0` |

darwin 分支行为零变化（现有 `TestPeerExecEnv_MinimalAllowlist` 不动即绿）。

### Fix 2：UTF-8 输出（`agentPeerShellCommand` Windows 分支，修缺陷③）

`-Command` 命令文本前置一条语句：

```
[Console]::OutputEncoding=[System.Text.Encoding]::UTF8;
```

PS 5.1 下这使 PS 自身的 stdout/stderr 输出编码为 UTF-8（覆盖 PS cmdlet 输出与 PS 错误记录——实测乱码的主体）。**已知边界**（写注释+工具描述）：原生 exe 按 OEM 代码页输出时 PS 的解码侧仍可能乱码，彻底方案需按代码页转码，本批次不做。审计行落的是原始命令文本，前缀不进审计。

### Fix 3：peer_exec 工具描述补 Windows 语义指引（LLM 侧缓解缺陷①）

四个面各一句英文指引（描述文本无契约钉，逐面同文）：registry 描述符（`agent_tool_registry.go`）、agentcore 桥（`agent_peer_tools.go`）、stdio 面（`cmd/mcp.go`）、D1 桥（`agent_ai_tool_ask_peer.go`）——

> "On Windows targets the command runs under Windows PowerShell 5.1: `&&` is unavailable (use `;` or PS cmdlets), external .exe may be unavailable (prefer PS built-ins like `$env:COMPUTERNAME`/`Get-FileHash`, or absolute paths), and output is UTF-8."

## 3. 非目标

- 缺陷④丢行（待 CHINAMI 回线诊断，另立批次）
- note 读回（PhoneServer 跨仓）
- pwsh/PS7 升级、cmd.exe 切换（`-Command` 引号转义不可靠的结论维持）
- A 侧工具门面/注册表的任何行为变化（仅描述文本）

## 4. 验收

1. 单测：新增 env 键集纯函数测试（darwin 上直接断言 Windows 键集——**规避 GOOS 尾段测试文件静默跳过陷阱**）；现有 `TestPeerExecEnv_MinimalAllowlist`/`CommandEnvHasNoSecrets` 不改即绿。
2. `go build ./...`（darwin）+ `GOOS=windows CGO_ENABLED=0 go build ./...` 交叉编译过。
3. 契约测试族绿（peer_exec/contract/mcp 全家）。
4. 真机复验清单（**CHINAMI 装新版后**，挂起至设备回线+用户部署）：`hostname` 可解析、`&&` 语义按壳说明、中文/PS 错误输出不乱码、`$env:TEMP` 非空、多语句丢行现象复测（1.1.94 行为对照新包）。

## 5. 风险锚点

- env 键集改动受 `TestPeerExecEnv_MinimalAllowlist` 钉死——新增键只进 Windows 分支，darwin 五键不动。
- `-Command` 前缀拼接不改变失败语义（多一条恒成功语句）；-EncodedCommand 不动（避免一次改两个变量干扰缺陷④诊断）。
- 工具描述四面同文（防漂移）；描述变更不影响 stdio tools/list 的 4MiB 上限。
