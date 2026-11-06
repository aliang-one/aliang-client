# peer_exec Windows 四缺陷修复 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修 peer_exec B 端 Windows 三缺陷（env 缺系统键 / GBK 乱码 / 工具描述无 Windows 语义），缺陷④（丢行）挂起待诊断。

**Architecture:** `agentPeerExecEnv` 重构出平台参数化纯函数 `agentPeerExecEnvFor(goos)`（Windows 增补 8 个系统键）；`agentPeerShellCommand` 拆出纯函数 `agentPeerPowerShellArgs`（`-Command` 前置 UTF-8 OutputEncoding 语句）；四面 peer_exec 工具描述加 Windows 语义指引。契约帧/协议零改动。

**Tech Stack:** Go 标准库（无新依赖——GBK 转码不做，靠 PS 输出编码切换）。

**Spec:** `docs/superpowers/specs/2026-11-05-peer-exec-windows-defects-design.md`

**提交纪律（项目 CLAUDE.md）：** 全程不自动 commit；完成后汇报等用户拍板。

---

### Task 0: worktree 准备

- [ ] **Step 1: 建 worktree**（`.worktrees/` 惯例，已验证被 gitignore）

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
git worktree add .worktrees/peer-exec-win-fix -b fix/peer-exec-windows-defects master
```

- [ ] **Step 2: 基线验证**

```bash
go build ./app/http/services/ && go test ./app/http/services/ -run 'TestPeerExecEnv' -count=1
```
Expected: build ok + 现有 env 测试绿

---

### Task 1: env 平台键集（TDD）

**Files:**
- Modify: `app/http/services/agent_peer_exec_test.go`（新增 3 个测试）
- Modify: `app/http/services/agent_peer_exec.go`（`agentPeerExecEnv` 重构）

- [ ] **Step 1: 写失败测试**（追加到 agent_peer_exec_test.go）

```go
// TestPeerExecEnvFor_WindowsAddsSystemKeys：Windows 键集 = 五键 + 8 系统键。
// 用平台参数化纯函数在 darwin 上直接断言（规避 GOOS 尾段测试文件静默跳过陷阱）。
func TestPeerExecEnvFor_WindowsAddsSystemKeys(t *testing.T) {
	t.Setenv("PATH", "") // 触发 Windows PATH 兜底
	env := agentPeerExecEnvFor("windows")
	seen := map[string]string{}
	for _, entry := range env {
		idx := strings.IndexByte(entry, '=')
		if idx <= 0 {
			t.Fatalf("malformed env entry %q", entry)
		}
		seen[entry[:idx]] = entry[idx+1:]
	}
	for _, key := range []string{
		"PATH", "HOME", "TERM", "LANG", "TMPDIR",
		"SystemRoot", "windir", "ComSpec", "PATHEXT", "TEMP", "TMP", "USERPROFILE",
	} {
		if _, ok := seen[key]; !ok {
			t.Fatalf("windows env missing key %q", key)
		}
	}
	if seen["SystemRoot"] != `C:\Windows` {
		t.Fatalf("SystemRoot fallback = %q", seen["SystemRoot"])
	}
	if !strings.Contains(seen["PATH"], `C:\Windows\System32`) {
		t.Fatalf("windows PATH fallback = %q", seen["PATH"])
	}
	// 凭据键绝不流入
	if _, ok := seen["ANTHROPIC_AUTH_TOKEN"]; ok {
		t.Fatal("credential key leaked into windows env")
	}
}

func TestPeerExecEnvFor_DarwinKeysUnchanged(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	env := agentPeerExecEnvFor("darwin")
	allowed := map[string]bool{"PATH": true, "HOME": true, "TERM": true, "LANG": true, "TMPDIR": true}
	for _, entry := range env {
		key := entry[:strings.IndexByte(entry, '=')]
		if !allowed[key] {
			t.Fatalf("darwin env gained key %q (five-key contract broken)", key)
		}
	}
}

func TestAgentPeerPowerShellArgs_UTF8Prefix(t *testing.T) {
	args := agentPeerPowerShellArgs("Get-Date")
	if len(args) != 4 || args[0] != "-NoProfile" || args[1] != "-NonInteractive" || args[2] != "-Command" {
		t.Fatalf("unexpected powershell args %v", args)
	}
	if !strings.HasPrefix(args[3], "[Console]::OutputEncoding=[System.Text.Encoding]::UTF8; ") {
		t.Fatalf("UTF-8 prefix missing: %q", args[3])
	}
	if !strings.HasSuffix(args[3], "Get-Date") {
		t.Fatalf("original command lost: %q", args[3])
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./app/http/services/ -run 'TestPeerExecEnvFor|TestAgentPeerPowerShellArgs' -count=1
```
Expected: FAIL（函数不存在）

- [ ] **Step 3: 实现**（agent_peer_exec.go）

`agentPeerShellCommand` Windows 分支改用 `agentPeerPowerShellArgs(command)` 构造参数；新增：

```go
// peerExecPSUtf8Prefix 前置语句：PS 5.1 默认按系统 ANSI 代码页（中文系统=GBK）
// 写 stdout/stderr，Go 侧按 UTF-8 解释必乱码；该语句使 PS 自身输出转 UTF-8。
// 边界：原生 exe 按 OEM 代码页输出的解码侧仍可能乱码（spec §Fix2），彻底方案
// 需按代码页转码，本批次不做。
const peerExecPSUtf8Prefix = "[Console]::OutputEncoding=[System.Text.Encoding]::UTF8; "

// agentPeerPowerShellArgs 纯函数（darwin 可测）：PS 参数构造 + UTF-8 前缀。
// 契约钉死 powershell（cmd.exe 的引号转义不可靠）；-EncodedCommand 不动（避免
// 与丢行缺陷④的诊断互相干扰）。
func agentPeerPowerShellArgs(command string) []string {
	return []string{"-NoProfile", "-NonInteractive", "-Command", peerExecPSUtf8Prefix + command}
}

// agentPeerExecEnvFor 平台参数化纯函数：goos 可注入，darwin 测试直接断言
// windows 键集（规避 GOOS 尾段测试文件静默跳过陷阱）。键集纪律不变：逐键
// 取值不继承其余——防 LLM 网关凭据经 env 跨设备泄漏；Windows 增补键全是
// 系统变量（非凭据），缺失会导致外部 exe 解析不了/临时目录错乱。
func agentPeerExecEnvFor(goos string) []string {
	capacity := 5
	if goos == "windows" {
		capacity = 12
	}
	env := make([]string, 0, capacity)
	path := strings.TrimSpace(os.Getenv("PATH"))
	if path == "" {
		if goos == "windows" {
			path = `C:\Windows\System32;C:\Windows;C:\Windows\System32\Wbem;C:\Windows\System32\WindowsPowerShell\v1.0`
		} else {
			path = "/usr/bin:/bin:/usr/local/bin"
		}
	}
	env = append(env, "PATH="+path)
	home := agentPeerHomeValue()
	if home != "" {
		env = append(env, "HOME="+home)
	}
	term := strings.TrimSpace(os.Getenv("TERM"))
	if term == "" {
		term = "xterm"
	}
	env = append(env, "TERM="+term)
	lang := strings.TrimSpace(os.Getenv("LANG"))
	if lang == "" {
		lang = "en_US.UTF-8"
	}
	env = append(env, "LANG="+lang)
	env = append(env, "TMPDIR="+os.TempDir())
	if goos != "windows" {
		return env
	}
	// Windows 系统键：PS/外部 exe 的解析与临时目录依赖它们；值优先取 agent
	// 进程 env，缺失用标准兜底（非凭据，见文件头安全边界）。
	env = append(env, windowsEnvEntry("SystemRoot", `C:\Windows`))
	env = append(env, windowsEnvEntry("windir", `C:\Windows`))
	env = append(env, windowsEnvEntry("ComSpec", `C:\Windows\system32\cmd.exe`))
	env = append(env, windowsEnvEntry("PATHEXT", ".COM;.EXE;.BAT;.CMD"))
	env = append(env, windowsEnvEntry("TEMP", os.TempDir()))
	env = append(env, windowsEnvEntry("TMP", os.TempDir()))
	if profile := strings.TrimSpace(os.Getenv("USERPROFILE")); profile != "" {
		env = append(env, "USERPROFILE="+profile)
	} else if home != "" {
		env = append(env, "USERPROFILE="+home)
	}
	return env
}

// windowsEnvEntry 组 key=value：值取 agent 进程 env（trim 后非空），否则兜底。
func windowsEnvEntry(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return key + "=" + v
	}
	return key + "=" + fallback
}

// agentPeerExecEnv 原入口（行为不变）：注释更新为平台键集说明——POSIX 五键
// 契约不变；Windows 增补系统键（TestPeerExecEnvFor_WindowsAddsSystemKeys 钉）。
func agentPeerExecEnv() []string {
	return agentPeerExecEnvFor(runtime.GOOS)
}
```

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./app/http/services/ -run 'TestPeerExecEnv|TestAgentPeerPowerShellArgs|TestClampPeerExec' -count=1
```
Expected: PASS（新旧全绿；`TestPeerExecEnv_MinimalAllowlist` 不改自绿）

---

### Task 2: 四面工具描述加 Windows 语义

**Files:**
- Modify: `app/http/services/agent_tool_registry.go`（peer_exec 描述符）
- Modify: `app/http/services/agent_peer_tools.go`（agentcore 桥 peer_exec 工具）
- Modify: `cmd/mcp.go`（stdio 面 peer_exec 工具）
- Modify: `app/http/services/agent_ai_tool_ask_peer.go`（D1 桥 peer_exec）

- [ ] **Step 1: 四面同文追加一句**（peer_exec 的 Description 字段末尾）

```
On Windows targets the command runs under Windows PowerShell 5.1: "&&" is unavailable (use ";" or PS cmdlets), external .exe may be unavailable (prefer PS built-ins like $env:COMPUTERNAME / Get-FileHash, or absolute paths), and output is UTF-8.
```

（实现时先 grep 四处现有描述原文，逐面以最小 diff 追加；Go 字符串里的双引号按各文件现状转义。）

- [ ] **Step 2: 验证**

```bash
go test ./app/http/services/ -run 'TestAgentToolRegistry|TestAgentCorePeerTools|TestAgentPeerMCP|TestAskPeer' -count=1 && go test ./cmd/ -run 'TestPeerMCP' -count=1
```
Expected: PASS（工具名契约不受描述文本影响）

---

### Task 3: 全量验证

- [ ] **Step 1: darwin 全量编译+目标测试**

```bash
go build ./... && go test ./app/http/services/ -run 'Peer|Device' -count=1
```
Expected: PASS（对照预存 flake 基线：agent_ai 子进程超时家族不在 -run 范围内）

- [ ] **Step 2: Windows 交叉编译检查**

```bash
GOOS=windows CGO_ENABLED=0 go build ./app/http/services/
```
Expected: 编译过（Windows 分支语法/引用正确）

- [ ] **Step 3: 向用户汇报改动摘要**（不自动 commit）

---

## 真机复验清单（挂起，CHINAMI 装新包后执行）

1. `hostname` 可解析（env PATH/PATHEXT 生效）
2. PS 错误输出中文不乱码（UTF-8 前缀生效）
3. `$env:TEMP` 非空（TEMP/TMP 键生效）
4. 多语句丢行复测（对照 1.1.94 行为，缺陷④诊断输入）
