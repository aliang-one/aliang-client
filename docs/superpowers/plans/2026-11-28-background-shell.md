# 后台 shell(run_in_background + BashOutput/KillShell)实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 aliangcode 自研内核支持 Claude Code 同款的后台命令:`Bash(run_in_background=true)` 立即返回 shell_id、`BashOutput` 增量读取输出、`KillShell` 终止;任务不随 run 停止而终止(归属会话级管理器);同时补齐 Windows 进程树组杀。

**Architecture:** 三刀。刀1=把 `services/command_windows.go` 的 Job Object 组杀下沉到 `app/agentcore`(services→agentcore 依赖已存在,方向合法),`exec_windows.go` 接入,统一 `newToolCommandContext` 签名返回 `(cmd, tree)`。刀2=`agentcore.ToolEnv` 新增窄接口 `BackgroundShells`(宿主实现,ShellPolicy 同款模式),`runShell` 分支后台路径(进程用独立 bgCtx,不随 run ctx 取消);services 层新增 `agentBackgroundShellRegistry`(复用 `terminalRingBuffer`)+ `BashOutput`/`KillShell` 两个桥工具(peer 桥同款)。刀3=`ai.stop` 从裸 `cancel()` 改走 `session.guard.kill("user_stop")`,补上 [ai.run.kill] 轨迹与 30s 强拆兜底(挂账 TODO `agent_ai_run_msg.go:553`)。

**Tech Stack:** Go 1.26;`golang.org/x/sys/windows`(已有依赖);`terminalRingBuffer`(已有,同包复用)。

**背景约束(实现者必读):**
- `app/agentcore` **禁止 import services**;反向(services→agentcore)已大量存在,合法。
- 工具名用 Claude 规范名(`tools.go:15-21`):新工具命名 `BashOutput`/`KillShell`(Claude Code 历史标准名,模型认知成本最低)。
- **本项目规矩:任务步骤里没有 commit。所有提交必须等用户明示**——计划末尾统一向用户请求提交。
- 基线:worktree `.worktrees/background-shell`,分支 `feat/background-shell`,HEAD `8fedb4dd`。
- Windows 侧行为本机(darwin)只能 `GOOS=windows go build/vet` 验证编译;真行为验证挂账装机步(0.196 机器 peer_exec 或出包后)。
- 服务层测试基线注意:services 包较大,先跑相关子集,收尾全量。

---

## Task 1: Windows 进程树组杀下沉到 agentcore

**Files:**
- Create: `app/agentcore/proctree_windows.go`(//go:build windows)
- Create: `app/agentcore/proctree_other.go`(//go:build !windows,空壳,**导出同面 API**——两平台方法集必须一致,否则 build-tag-free 共享调用点 `agent_peer_exec.go` 只能在一侧编译)
- Create: `app/agentcore/proctree_windows_test.go`(//go:build windows,本机不编译;装机/Windows 真机跑,验证 Job 组杀覆盖孙进程与 KILL_ON_JOB_CLOSE 兜底)
- Modify: `app/http/services/command_windows.go`(删除本地实现,委托 agentcore;`tree.ok` 改 `tree.Ok()`)
- Modify: `app/http/services/command_nonwindows.go`(POSIX 侧同名 struct 同步替换为别名,保证两平台方法集一致)
- Modify: `app/http/services/agent_peer_exec.go`(共享调用点方法名导出化)

- [ ] **Step 1.1: 写 `proctree_windows.go`**

把 `services/command_windows.go:57-146` 的 `processGroupTree` 原样搬迁,导出改名,注释保留:

```go
//go:build windows

package agentcore

import (
	"errors"
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ProcessGroupTree 是 Windows 的进程树组杀句柄(Job Object 实现):创建
// KILL_ON_JOB_CLOSE 的 Job → 进程启动后 Attach 入树 → 超时/取消 KillTree
// 组杀(覆盖 Job 内全部后台化子孙)→ Release 兜底(agent 进程崩溃/被杀时
// 句柄由内核回收,Job 连带终止整棵树)。POSIX 对应物是 Setpgid + 组杀
// SIGKILL(exec_unix.go newToolCommandContext),两平台共用同一调用面。
//
// 为什么放在 agentcore 而不是 common:内核工具执行必须自带进程纪律
// (组杀),而 agentcore 禁止 import services——services 的同款实现
// (command_windows.go)够不着。services→agentcore 依赖已存在,故实现
// 沉到本包,services 反向委托。
type ProcessGroupTree struct {
	job windows.Handle
	ok  bool
}

// NewProcessGroupTree 创建带 KILL_ON_JOB_CLOSE 的空 Job。失败(权限/句柄耗尽)
// 返回 ok=false 的空树:调用方降级为直接子进程 kill + WaitDelay 盖帽,不阻断执行。
func NewProcessGroupTree() *ProcessGroupTree {
	t := &ProcessGroupTree{}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil || job == windows.InvalidHandle {
		return t
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return t
	}
	t.job = job
	t.ok = true
	return t
}

// Attach 把已启动的直接子进程挂进 Job,必须在 Start 之后、超时窗口之前调用:
// 之后子进程派生的全部子孙自动入树(TerminateJobObject / KILL_ON_JOB_CLOSE 均
// 覆盖);Attach 之前已存在的孤儿子进程不在树内,靠 WaitDelay 盖帽兜底。
// 尽力而为:失败只降级为直接子进程 kill,不阻断主流程。
func (t *ProcessGroupTree) Attach(cmd *exec.Cmd) error {
	if !t.ok || cmd == nil || cmd.Process == nil {
		return nil
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("open process for job assign: %w", err)
	}
	defer windows.CloseHandle(ph)
	return windows.AssignProcessToJobObject(t.job, ph)
}

// KillTree 组杀:TerminateJobObject 覆盖 Job 内全部子孙进程(直接子进程
// 与后台化孙进程一并终止)。作为 cmd.Cancel 安装。
func (t *ProcessGroupTree) KillTree() error {
	if !t.ok {
		return errors.New("process group job object unavailable")
	}
	return windows.TerminateJobObject(t.job, 1)
}

// Release 收尾关闭 Job 句柄。KILL_ON_JOB_CLOSE 语义:正常路径这里是显式收尾;
// agent 进程异常退出没走到 Release 时,句柄由内核回收,Job 内进程树被连带
// 终止——Close 即兜底。
func (t *ProcessGroupTree) Release() {
	if !t.ok {
		return
	}
	_ = windows.CloseHandle(t.job)
	t.ok = false
}

// Ok 报告 Job 是否可用(false=创建失败,NewProcessGroupTree 已自动降级)。
// services 的 newBackgroundCommandTreeContext 用它决定是否覆盖 Cancel。
func (t *ProcessGroupTree) Ok() bool { return t.ok }
```

- [ ] **Step 1.2: 写 `proctree_other.go` 空壳**

```go
//go:build !windows

package agentcore

import (
	"errors"
	"os/exec"
)

// ProcessGroupTree 是非 Windows 平台的空壳:组杀语义由 exec_unix.go 的
// Setpgid + kill(-pgid) 在 cmd.Cancel 内完成,无需独立句柄。存在它是为了让
// newToolCommandContext 在两平台返回统一签名(cmd, tree),调用方无需分叉。
type ProcessGroupTree struct{}

func NewProcessGroupTree() *ProcessGroupTree { return &ProcessGroupTree{} }
func (t *ProcessGroupTree) Attach(*exec.Cmd) error   { return nil }
func (t *ProcessGroupTree) KillTree() error          { return errors.New("process group tree is unix-callback based") }
func (t *ProcessGroupTree) Release()                 {}
func (t *ProcessGroupTree) Ok() bool                 { return false }
```

- [ ] **Step 1.3: services 两平台同步委托(方法集必须一致)**

⚠️ `agent_peer_exec.go` 的共享调用点是 build-tag-free 的——POSIX 与 Windows 两侧的 `processGroupTree` 方法集若不一致,`go build ./...` 或 `GOOS=windows go build` 必有一侧编不过。

1. `app/http/services/command_windows.go`:删除 `processGroupTree` 类型及 `newProcessGroupTree`(57-133 行),文件头 import 加 `"aliang.one/nursorgate/app/agentcore"`,留薄别名与转发:

```go
// processGroupTree 已下沉到 agentcore(2026-11-28,内核工具与宿主后台命令
// 共用同一 Job 实现):这里只留转发。
type processGroupTree = agentcore.ProcessGroupTree

func newProcessGroupTree() *processGroupTree { return agentcore.NewProcessGroupTree() }
```

并把本文件 `newBackgroundCommandTreeContext` 内的 `if tree.ok { cmd.Cancel = tree.killTree }` 改为 `if tree.Ok() { cmd.Cancel = tree.KillTree }`(`ok` 是跨包不可见的 unexported 字段)。⚠️ 删 57-133 行后同步移除孤儿 import:`errors`/`fmt`/`unsafe`/`golang.org/x/sys/windows`(darwin 侧 `go build` 不会报,`GOOS=windows` 才报,编译器引导即可)。

2. `app/http/services/command_nonwindows.go`:POSIX 侧本地 `processGroupTree` struct(44-53 行,unexported attach/release)同样替换为别名+转发:

```go
// processGroupTree 已下沉到 agentcore(与 Windows 同一面):POSIX 上是空壳,
// 组杀语义在 exec_unix.go 的 cmd.Cancel 内。
type processGroupTree = agentcore.ProcessGroupTree

func newProcessGroupTree() *processGroupTree { return agentcore.NewProcessGroupTree() }
```

3. 全 services 调用点(`agent_peer_exec.go` 等)统一改导出名:`.attach(` → `.Attach(`、`.killTree(` → `.KillTree(`、`.release()` → `.Release()`、`.ok` → `.Ok()`。`newBackgroundCommandTreeContext` 保留在 services。(注:HideWindow 最终也进了内核 shell——Task 2 的 exec_windows.go 三层纪律之一,两侧语义已统一。)

- [ ] **Step 1.4: Windows 侧装机验证测试**

`app/agentcore/proctree_windows_test.go`(darwin 不编译;出包/0.196 真机跑 `go test -run TestProcessGroupTree`):

```go
//go:build windows

package agentcore

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestProcessGroupTreeKillTree 验证 Job 组杀覆盖孙进程:cmd /C 起的 ping 是
// 孙进程形态,KillTree 后 Wait 必须在 5s 内返回。
func TestProcessGroupTreeKillTree(t *testing.T) {
	tree := NewProcessGroupTree()
	if !tree.Ok() {
		t.Skip("Job Object 不可用(权限/句柄),降级路径不在本测覆盖")
	}
	cmd := exec.Command("cmd", "/C", "ping -n 30 127.0.0.1 > nul")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Cancel = tree.KillTree
	cmd.WaitDelay = 3 * time.Second
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := tree.Attach(cmd); err != nil {
		t.Fatalf("attach: %v", err)
	}
	_ = tree.KillTree()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("KillTree 后 Wait 未在 5s 内返回")
	}
	tree.Release()
}
```

- [ ] **Step 1.5: 验证**

```bash
go build ./... && echo BUILD_OK
GOOS=windows go build ./app/agentcore/ && echo WIN_BUILD_OK_AGENTCORE
go test ./app/http/services/ -run 'TestPeerExec' -count=1
```
预期:全部通过(`newBackgroundCommandTreeContext` 的行为不变)。
⚠️ 注:`GOOS=windows go build ./app/http/services/` 在 darwin 开发机因**预存**的 `app/http/storage/quick_setup_combo_store.go`→mattn/go-sqlite3(cgo 门控)无法整包过——与本批无关,完整 services Windows 编译随打包 runner 落地;本机只验 agentcore 包。

---

## Task 2: exec_windows 接入树杀,统一 newToolCommandContext 签名

**Files:**
- Modify: `app/agentcore/exec_windows.go`
- Modify: `app/agentcore/exec_unix.go`(签名改为返回二元组)
- Modify: `app/agentcore/tools_shell.go`(唯一调用点 + 后台分支同一处,Task 3 一并改)
- Test: `app/agentcore/exec_unix_test.go`(已有,回归)

- [ ] **Step 2.1: 改 `exec_unix.go`**

```go
// newToolCommandContext 构造工具执行用的命令(原注释全保留),返回
// (cmd, tree):unix 的组杀在 cmd.Cancel 内完成,tree 是空壳占位。
func newToolCommandContext(ctx context.Context, name string, args ...string) (*exec.Cmd, *ProcessGroupTree) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// (原 41-58 行注释与逻辑不动)
		if cmd.Process == nil {
			return nil
		}
		pid := cmd.Process.Pid
		if err := cmd.Process.Kill(); err != nil {
			return err
		}
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		return nil
	}
	cmd.WaitDelay = toolCommandWaitDelay
	return cmd, &ProcessGroupTree{}
}
```

- [ ] **Step 2.2: 改 `exec_windows.go`**

```go
//go:build windows

package agentcore

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

// toolCommandWaitDelay 见 exec_unix.go 的同名常量。
const toolCommandWaitDelay = 3 * time.Second

// newToolCommandContext 构造工具执行用的命令,返回 (cmd, tree)。
//
// 2026-11-28 起接入 Job Object 树杀(proctree_windows.go):此前 Windows 只有
// WaitDelay 兜底——shell 被杀后孙进程仍握着继承来的 stdout 管道,复合命令
// (`a; b | c`)与后台化后代(`cmd &`)成为孤儿,与 POSIX 侧 2026-11-01 事故
// 同形态。三层纪律:
//  1. HideWindow:exec 任意 console 程序不再闪 cmd 窗口(services 同款);
//  2. Cancel:Job 可用时组杀整树(KillTree),否则降级直接子进程 kill;
//  3. WaitDelay:仍有漏网进程握管道时 3s 强断 Wait。
//
// 返回的 tree 由调用方在 cmd.Start() 成功后 Attach、Wait 返回后 Release。
func newToolCommandContext(ctx context.Context, name string, args ...string) (*exec.Cmd, *ProcessGroupTree) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.WaitDelay = toolCommandWaitDelay
	tree := NewProcessGroupTree()
	if tree.ok {
		cmd.Cancel = tree.KillTree
	} else {
		cmd.Cancel = func() error {
			if cmd.Process != nil {
				return cmd.Process.Kill()
			}
			return nil
		}
	}
	return cmd, tree
}
```

- [ ] **Step 2.3: 改 `tools_shell.go` 调用点(前台路径)**

`runShell` 内(150-194 行):

```go
	args := append(append([]string(nil), baseArgs...), command)
	cmd, tree := newToolCommandContext(runCtx, shellPath, args...)
	if projectDir != "" {
		cmd.Dir = projectDir
	}
	if env.Env != nil {
		cmd.Env = env.Env
	}
	// (stdout/stderr 接线不变)
	runErr := cmd.Start()
	if runErr == nil {
		if err := tree.Attach(cmd); err != nil {
			// 尽力而为:Attach 失败降级为 Cancel 内的直接子进程 kill,不阻断。
			_ = err
		}
		// (OnShellStart 块不变)
		runErr = cmd.Wait()
	}
	defer tree.Release()
```

注:`defer tree.Release()` 放 `runErr := cmd.Start()` 之后同层即可(Start 失败也安全——Release 对空树是 no-op;Job 句柄在 Wait 返回后关闭)。

- [ ] **Step 2.4: 验证**

```bash
go build ./... && GOOS=windows go build ./app/agentcore/ && echo OK
go vet ./app/agentcore/ && echo VET_OK
GOOS=windows go vet ./app/agentcore/ && echo WIN_VET_OK
go test ./app/agentcore/ -run 'TestRunShell' -count=1 -v 2>&1 | tail -8
```
预期:既有三测(GroupKillBoundsWallTime/嵌套 shell/后台子进程输出回填)全 PASS。

---

## Task 3: agentcore 后台分支(ToolEnv.BackgroundShells + Bash run_in_background)

**Files:**
- Modify: `app/agentcore/tools.go`(ToolEnv 加字段 + 导出 MaxToolOutputBytes)
- Modify: `app/agentcore/tools_shell.go`(schema 加参数 + runShell 后台分支)
- Test: `app/agentcore/tools_shell_background_test.go`(新)

- [ ] **Step 3.1: ToolEnv 加窄接口(tools.go)**

在 `ToolEnv` 结构体的 `Env []string` 字段后追加:

```go
	// BackgroundShells 是宿主注入的后台 shell 登记处(ShellPolicy 同款模式:
	// 管理器在 services 层,本包禁止 import services,故做成窄接口)。
	// nil = 本会话不支持后台执行,Bash 的 run_in_background 参数报错。
	//
	// 生命周期归属:后台进程用内核自建的独立 ctx(maxBackgroundShellLifetime
	// 封顶),**不随 run ctx 取消而终止**——run 停止/dev server 继续跑是本
	// 特性的存在意义。收口责任在宿主(会话管理器 closeAll/进程退出 Shutdown)。
	BackgroundShells BackgroundShellRegistry
```

同文件 ShellPolicy 接口后追加:

```go
// BackgroundShellRegistry 是宿主注入的后台 shell 登记处(窄接口)。
type BackgroundShellRegistry interface {
	// Begin 登记一条即将启动的后台 shell,返回句柄与唯一 id。内核在进程
	// Start 前调用;Start 成功后 MarkRunning,输出持续 Write,结束调 Finished。
	Begin(command string) (BackgroundShellHandle, string)
}

// BackgroundShellHandle 是一条后台 shell 的内核侧句柄。
type BackgroundShellHandle interface {
	// ID 返回登记 id(工具返回给模型的 shell_id)。
	ID() string
	// Write 合并接收 stdout/stderr(实现方必须并发安全:os/exec 对两根
	// 管道各起一条拷贝 goroutine)。
	Write(p []byte) (int, error)
	// SetCancel 装填终止闭包(ctx cancel → cmd.Cancel 树杀)。KillShell
	// 类工具经宿主间接调用;进程结束后内核置 nil 防误杀句柄复用进程。
	SetCancel(func())
	// MarkRunning 在进程成功启动后调用,携带 PID(展示用)。
	MarkRunning(pid int)
	// Finished 在进程 Wait 返回后调用,恰好一次。outcome 语义同 ShellOutcome;
	// detail 是人类可读的收尾说明(退出码/超时/被终止)。
	Finished(outcome ShellOutcome, detail string)
}
```

文件尾(`maxToolOutputBytes` 定义后)追加导出(对齐 `MaxParallelToolCalls` 先例,services 侧工具引用同一数字防漂移):

```go
// MaxToolOutputBytes 导出单次工具回填上限,services 侧桥工具(BashOutput 等)
// 引用同一数字,避免手抄漂移。
const MaxToolOutputBytes = maxToolOutputBytes
```

- [ ] **Step 3.2: 后台生命周期常量(tools_shell.go 文件头)**

```go
// maxBackgroundShellLifetime 是单条后台命令的进程寿命上限。对齐终端子系统
// 的 24h idle 帽;到期内核按 TimedOut 收尾。可用环境变量覆盖(与
// ALIANG_AGENTCORE_STREAM_IDLE_TIMEOUT 同款解析,见 stream_idle.go)。
var maxBackgroundShellLifetime = 24 * time.Hour

func init() {
	if v := strings.TrimSpace(os.Getenv("ALIANG_AGENTCORE_BG_SHELL_MAX_LIFETIME")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > time.Minute {
			maxBackgroundShellLifetime = d
		}
	}
}
```

注:文件已 import os/strings/time,无新增 import。

- [ ] **Step 3.3: Bash/PowerShell schema 加参数**

两个工具的 `InputSchema()` 的 objectSchema props 里各加一行:

```go
		"run_in_background": boolProp("true=后台运行:立即返回 shell_id,不等待命令结束。输出用 BashOutput 增量读取,用 KillShell 终止。适合 dev server / watch / 长构建。后台模式忽略 timeout,进程寿命默认 24h(可用环境变量调整);不随本回合停止而终止。"),
```

- [ ] **Step 3.4: runShell 后台分支**

在 runShell 里 `runCtx, cancel := context.WithTimeout(ctx, timeout)` **之前**(timeout 解析之后)插入分支——复用此前全部 preamble(command 校验/ShellPolicy Allow/Resolve/projectDir):

```go
	// ---- 后台分支:登记 → 独立 ctx 启动 → 立即返回;等待在 goroutine ----
	// 不吃 run ctx:run 停止(dev server 继续跑)是本特性的存在意义。
	// 审批/白名单已在上方完成:后台启动与前台同策略,不会绕闸。
	if inputBool(input, "run_in_background") {
		if env.BackgroundShells == nil {
			return ErrorResult("当前会话不支持后台命令")
		}
		toolName := "Bash"
		if kind == "powershell" {
			toolName = "PowerShell"
		}
		return runShellBackground(toolName, shellPath, append(append([]string(nil), baseArgs...), command), projectDir, env.Env, env.BackgroundShells, command)
	}
```

(变量命名:`toolName` 独立声明,勿与参数 kind 撞名;`command` 原样透传给登记处,勿从 args 反推。)

文件尾追加实现:

```go
// runShellBackground 是 runShell 的后台路径:进程纪律(组杀/WaitDelay)与
// 前台完全同源(newToolCommandContext),唯一区别是 ctx 独立于 run、输出写
// 进宿主环、Wait 挪到 goroutine。
func runShellBackground(tool, shellPath string, args []string, dir string, cmdEnv []string, registry BackgroundShellRegistry, command string) ToolResult {
	handle, id := registry.Begin(command)
	bgCtx, bgCancel := context.WithTimeout(context.Background(), maxBackgroundShellLifetime)
	cmd, tree := newToolCommandContext(bgCtx, shellPath, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if cmdEnv != nil {
		cmd.Env = cmdEnv
	}
	// 单一合并写入口(stdout/stderr 按到达顺序交织,与 Claude Code 后台任务
	// 同形态);handle 自身并发安全。
	cmd.Stdout = handle
	cmd.Stderr = handle

	runErr := cmd.Start()
	if runErr != nil {
		handle.Finished(ShellFailed, fmt.Sprintf("启动失败: %v", runErr))
		bgCancel()
		tree.Release()
		return ErrorResult("后台命令启动失败: %v", runErr)
	}
	_ = tree.Attach(cmd) // 尽力而为,失败降级 Cancel 直接子进程 kill
	handle.MarkRunning(cmd.Process.Pid)
	handle.SetCancel(func() { bgCancel() })
	// 刻意不接 OnShellStart/进度卡:后台任务的可视面是 BashOutput(模型侧)+
	// 终态在登记处状态行;进度卡语义是「同步等待中的观察」,与立即返回的后台
	// 形态不匹配。接了反而出现永不 finish 的卡(v1 明确裁掉,见计划尾部)。

	go func() {
		runErr := func() (waitErr error) {
			defer func() {
				// panic 兜底:等待 goroutine 不许拖垮宿主进程(内核在宿主进程内)。
				if r := recover(); r != nil {
					handle.Finished(ShellFailed, fmt.Sprintf("内部错误: %v", r))
					bgCancel() // 触发 Done,让外层的 Release 在一切退出路径上都执行
				}
			}()
			return cmd.Wait()
		}()
		switch {
		case errors.Is(runErr, exec.ErrWaitDelay):
			// 直接子命令已结束、后代仍握管道:后台语义下这正是常态
			// (nohup/dev server),按 Completed 收尾。
			handle.Finished(ShellCompleted, "主命令已退出(后台进程仍在运行)")
		case bgCtx.Err() == context.DeadlineExceeded:
			handle.Finished(ShellTimedOut, fmt.Sprintf("寿命上限 %s 到期,已终止", maxBackgroundShellLifetime))
		case bgCtx.Err() != nil:
			handle.Finished(ShellCancelled, "已被 KillShell 终止")
		case runErr != nil:
			if exitErr, ok := runErr.(*exec.ExitError); ok {
				handle.Finished(ShellFailed, fmt.Sprintf("退出码 %d", exitErr.ExitCode()))
			} else {
				handle.Finished(ShellFailed, fmt.Sprintf("等待失败: %v", runErr))
			}
		default:
			handle.Finished(ShellCompleted, "退出码 0")
		}
		handle.SetCancel(nil) // 进程已终:防句柄复用后误杀

		// ⚠️ Release 的时机契约(Task 2 spec 评审定谳):**不得在 Wait 返回后
		// 立即 Release**——Windows 侧 Release 关 Job 句柄,KILL_ON_JOB_CLOSE
		// 会连带终止仍存活的后台子孙(`cmd &`/dev server),违背后台承诺。
		// 句柄保持开放,直到 bgCtx 结束(KillShell 或寿命帽)——此时「树死或
		// 被杀」二选一,收尾均正确。agent 进程崩溃时句柄由内核回收 +
		// KILL_ON_JOB_CLOSE 仍兜底整树终止(Task 1 Release 注释同契约)。
		<-bgCtx.Done()
		tree.Release()
		bgCancel()
	}()

	return ToolResult{Content: fmt.Sprintf(
		"后台任务已启动 shell_id=%s(PID %d)。用 BashOutput(shell_id=\"%s\") 增量读取输出;" +
			"用 KillShell(shell_id=\"%s\") 终止。任务不随本回合停止而终止。",
		id, cmd.Process.Pid, id, id)}
}
```

- [ ] **Step 3.5: 写失败测试先行 `tools_shell_background_test.go`**

```go
package agentcore

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeBackgroundRegistry 是宿主登记处的测试替身,记录内核回调序列。
type fakeBackgroundRegistry struct {
	handles []*fakeBackgroundHandle
}

type fakeBackgroundHandle struct {
	id       string
	buf      strings.Builder
	cancel   func()
	running  bool
	pid      int
	outcome  ShellOutcome
	detail   string
	finished bool
}

func (f *fakeBackgroundRegistry) Begin(command string) (BackgroundShellHandle, string) {
	h := &fakeBackgroundHandle{id: fmt.Sprintf("bg-%d", len(f.handles)+1)}
	f.handles = append(f.handles, h)
	return h, h.id
}
func (h *fakeBackgroundHandle) ID() string                       { return h.id }
func (h *fakeBackgroundHandle) Write(p []byte) (int, error)      { return h.buf.Write(p) }
func (h *fakeBackgroundHandle) SetCancel(fn func())              { h.cancel = fn }
func (h *fakeBackgroundHandle) MarkRunning(pid int)              { h.running, h.pid = true, pid }
func (h *fakeBackgroundHandle) Finished(o ShellOutcome, d string) {
	h.finished, h.outcome, h.detail = true, o, d
}

func newBashEnvWithBackground(reg BackgroundShellRegistry) *ToolEnv {
	return &ToolEnv{
		ProjectPath:     ".",
		BackgroundShells: reg,
		ShellPolicy:     allowAllShellPolicy{},
	}
}

// allowAllShellPolicy 测试用全放行策略(exec 真实 shell)。
type allowAllShellPolicy struct{}
func (allowAllShellPolicy) Resolve(kind string) (string, []string, error) {
	return "/bin/bash", []string{"-c"}, nil
}
func (allowAllShellPolicy) Allow(string) (bool, string) { return true, "" }
```

测试用例(每个独立 `t.Run`):
1. `TestBackgroundShellLaunchReturnsImmediately`:command=`echo bg-hello`,断言 Execute 立即返回(耗时 < 2s)、Content 含 `shell_id=bg-1`、`strings.Contains(h.buf.String(), "bg-hello")` 轮询至多 2s、最终 `Finished(ShellCompleted)`。
2. `TestBackgroundShellSurvivesCtxCancel`:ctx 带 100ms cancel;command=`sleep 0.5; echo done-late`;断言 Execute 返回后 ctx 已取消,但轮询 2s 内 buf 含 `done-late` 且 outcome=Completed(核心语义:不随 run ctx 死)。
3. `TestBackgroundShellKill`:启动 `sleep 30`,调 `h.cancel()`,断言 2s 内 Finished(ShellCancelled)。
4. `TestBackgroundShellNoRegistry`:env.BackgroundShells=nil 时返回 IsError 且文案含「不支持后台」。
5. `TestBackgroundShellLifetimeCap`:`t.Setenv("ALIANG_AGENTCORE_BG_SHELL_MAX_LIFETIME","2s")` — 注意 init() 已跑,改为直接改包变量:`old := maxBackgroundShellLifetime; maxBackgroundShellLifetime = 1500 * time.Millisecond; t.Cleanup(...)`;command=`sleep 30`,断言 3s 内 Finished(ShellTimedOut)。

- [ ] **Step 3.6: 跑测试**

```bash
go test ./app/agentcore/ -run 'TestBackgroundShell' -count=1 -v 2>&1 | tail -12
```
预期:5 个用例全 PASS。全量 `go test ./app/agentcore/ -count=1` 回归。

---

## Task 4: services 后台登记处(agentBackgroundShellRegistry)

**Files:**
- Create: `app/http/services/agent_ai_background_shell.go`
- Test: `app/http/services/agent_ai_background_shell_test.go`

- [ ] **Step 4.1: 先写失败测试**

核心用例(真进程):
1. `TestBackgroundShellRegistryBeginReadKill`:Begin("echo reg-hello") → 假内核手工模拟(⚠️ `bashTool` 在 agentcore 是 unexported——services 测试经 `agentcore.DefaultRegistry().Get("Bash")` 取工具再 `Execute(ctx, input, &agentcore.ToolEnv{BackgroundShells: reg, ShellPolicy: ...})`)→ `read()` 轮询拿到 `reg-hello`、status=completed、二次 read 空(delta 语义)。
2. `TestBackgroundShellRegistryIncremental`:长输出命令 `printf 'a\nb\nc\n'` 分两次 read 各拿到增量。
3. `TestBackgroundShellRegistryKillRunning`:Begin+启动 `sleep 30` → kill → 数秒内 status=cancelled。
4. `TestBackgroundShellRegistryList`:同 session 两条(一跑一完),list 只返回本 session、含 status。
5. `TestBackgroundShellRegistryCloseAll`:CloseAll 后 running→cancelled、后续 read 报 not found。
6. `TestBackgroundShellRegistryEvictDead`:cap=2(测试注入小 cap),第三条完成后最早的死记录被逐出。

- [ ] **Step 4.2: 实现 `agent_ai_background_shell.go`**

```go
package services

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/agentcore"
)

// 后台 shell 登记处:宿主侧生命周期归属(2026-11-28 后台 shell 批)。
//
// 职责边界:内核(agentcore)拥有进程纪律(组杀/WaitDelay/独立 ctx 与寿命帽),
// 本登记处只负责「登记簿 + 输出环 + 增量读 + 终止转发 + 收口」:
//   - 跨 run 存活:run 结束/被停,后台进程继续跑(本特性的存在意义);
//   - 输出环复用 terminalRingBuffer(与终端子系统同款滚动语义);
//   - 记录收口:AgentService.Shutdown → manager.closeAllWithReason 旁路调用
//     CloseAll;死记录超量惰性逐出(容量对齐 sessionApprovals 的 64 上限风格)。
type agentBackgroundShellRegistry struct {
	mu     sync.Mutex
	shells map[string]*agentBackgroundShell
	order  []string // 死记录逐出序(创建序)
	seq    int
	// capForTest 允许测试注入小容量;0=默认 64。
	capForTest int
}

// agentBackgroundShellMaxRecords 是已结束记录的保留上限(防长寿命进程累积)。
const agentBackgroundShellMaxRecords = 64

// agentBackgroundShellRingBytes 是单条后台 shell 的输出环容量。对齐终端环
// 64KB 下限,默认给 256KB(dev server 启动横幅量级)。
const agentBackgroundShellRingBytes = 256 * 1024

type agentBackgroundShell struct {
	id      string
	session string
	command string
	created time.Time

	mu      sync.Mutex
	ring    *terminalRingBuffer
	cursor  int // 已读偏移(snapshot 长度);环逐出时重置
	cancel  func()
	running bool
	pid     int
	outcome agentcore.ShellOutcome
	detail  string
}
```

实现方法(全部带 mu;handle 接口实现挂在 shell 上):

```go
func newAgentBackgroundShellRegistry() *agentBackgroundShellRegistry {
	return &agentBackgroundShellRegistry{shells: map[string]*agentBackgroundShell{}}
}

func (r *agentBackgroundShellRegistry) Begin(command string) (agentcore.BackgroundShellHandle, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	id := fmt.Sprintf("bgsh-%d", r.seq)
	s := &agentBackgroundShell{
		id: id, command: command, created: time.Now(),
		ring: newTerminalRingBuffer(agentBackgroundShellRingBytes),
	}
	r.shells[id] = s
	r.order = append(r.order, id)
	r.evictDeadLocked()
	return s, id
}

// evictDeadLocked 在 Begin 时惰性逐出:已结束记录超上限时从最旧开始删。
func (r *agentBackgroundShellRegistry) evictDeadLocked() {
	cap := r.capForTest
	if cap <= 0 {
		cap = agentBackgroundShellMaxRecords
	}
	var dead []string
	for _, id := range r.order {
		s := r.shells[id]
		if s == nil {
			continue
		}
		s.mu.Lock()
		finished := !s.running
		s.mu.Unlock()
		if finished {
			dead = append(dead, id)
		}
	}
	for len(dead) > cap {
		victim := dead[0]
		dead = dead[1:]
		delete(r.shells, victim)
	}
}
```

handle 接口(shell 自身实现 agentcore.BackgroundShellHandle):ID/Write(→ring.push)/SetCancel/MarkRunning/Finished(照接口语义写,注意 Finished 后 running=false)。

查询面:

```go
// read 返回自上次读取以来的新增输出与状态行;环已逐出旧字节时带 evicted 提示。
// notFound 时 ok=false。
func (r *agentBackgroundShellRegistry) read(sessionID, id string) (chunk, status string, ok bool)
// kill 终止指定 shell(仅本 session 可杀);返回 (found, wasRunning)。
func (r *agentBackgroundShellRegistry) kill(sessionID, id string) (bool, bool)
// list 返回本 session 全部记录的状态行(模型可见面)。
func (r *agentBackgroundShellRegistry) list(sessionID string) []string
// closeAll 终止全部存活进程并清空记录(进程退出收口)。
func (r *agentBackgroundShellRegistry) closeAll(reason string)
```

实现要点(文件头 import 以最终实现为准——上方骨架片段的 context/os/strings 等若未被完整实现用到则删):
- `read`:`s.ring.snapshot()` 与 `s.cursor` 比对;`len(data) > cursor` → 新增 `data[cursor:]`(上限 `agentcore.MaxToolOutputBytes`,超出截断并标注);`len(data) < cursor` → 环逐出,返回全量 + 前缀 `[输出环已滚动,早期字节被丢弃]`;status 行拼 `running(pid)/outcome detail/created`。
- `kill`:`s.cancel != nil` 时调用并置 nil;返回前 `wasRunning = s.running`。
- `closeAll`:遍历调用 kill;`r.shells = map{}; r.order = nil`。

- [ ] **Step 4.3: 挂到 agentAIManager**

`agent_ai_types.go` struct 加字段:

```go
	// backgroundShells 是后台 shell 登记处(2026-11-28 后台 shell 批):跨 run
	// 存活的进程归属地。惰性初始化(backgroundShells());收口走
	// backgroundShellsCloseAll(AgentService.Shutdown 旁路)。
	backgroundShells *agentBackgroundShellRegistry
```

`agent_ai_background_shell.go` 尾部加:

```go
// ensureBackgroundShells 惰性构造(sessionApprovals 的内联判空同款风格;⚠️
// 方法名不能与字段 backgroundShells 撞名——Go 同类型禁止字段与方法同名)。
func (m *agentAIManager) ensureBackgroundShells() *agentBackgroundShellRegistry {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.backgroundShells == nil {
		m.backgroundShells = newAgentBackgroundShellRegistry()
	}
	return m.backgroundShells
}

// backgroundShellsCloseAll 供 AgentService.Shutdown 调用(进程退出真关停,
// RUN-03 同语义)。manager 为 nil 时 no-op。
func (m *agentAIManager) backgroundShellsCloseAll(reason string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	reg := m.backgroundShells
	m.mu.Unlock()
	if reg != nil {
		reg.closeAll(reason)
	}
}
```

`agent_service.go` `Shutdown()` 的 `s.ai.closeAllWithReason` 块后追加:

```go
	if s.ai != nil {
		s.ai.backgroundShellsCloseAll("agent_shutdown")
	}
```

- [ ] **Step 4.4: 跑测试**

```bash
go test ./app/http/services/ -run 'TestBackgroundShellRegistry' -count=1 -v 2>&1 | tail -14
```

---

## Task 5: BashOutput / KillShell 桥工具 + 注册

**Files:**
- Create: `app/http/services/agent_ai_background_tools.go`
- Modify: `app/http/services/agent_approval_policy.go`(readonly-tools 白名单加两名)
- Test: `app/http/services/agent_ai_background_tools_test.go`

- [ ] **Step 5.1: 两个桥工具**

peer 桥同款形状(agentCorePeerToolBridge 参考),独立小类型(journal 经构造注入,m.runAgentCore 注册点已有 m.coreJournal 闭包先例 `:1739`):

```go
// bashOutputTool 列出/增量读取后台任务。只读:进 readonly-tools 自动放行
// (无 shell_id 时列出全部,模型自主发现;带 id 时读增量)。
// killShellTool 终止后台任务:只影响登记处名下的进程,不碰任意 PID。
```

- BashOutput:Name `BashOutput`;schema `{shell_id?: string}`;Execute:
  - 无 id → `reg.list(run.sessionID)` 拼表(空表回「当前没有后台任务」);
  - 有 id → `reg.read(run.sessionID, id)`;not found → ErrorResult(列出可用 id 帮助自纠);
  - 输出统一 `truncateAgentText` rune 截断兜底。
- KillShell:Name `KillShell`;schema `{shell_id: string}`(required);Execute → `reg.kill(...)`;未找到→ErrorResult;已结束→Content 带当时 outcome;杀成功→Content「已终止 shell_id=...,输出可用 BashOutput 追溯」。
- 均实现 `Parallelizable() bool { return true }`(纯读/登记处内锁,与子 agent 桥同判据)。

- [ ] **Step 5.2: 白名单**

`agent_approval_policy.go:1321-1324` readonly-tools 列表追加 `"BashOutput", "KillShell"`(注释补一句:KillShell 只及于登记处名下进程,风险面=任务自身启动物)。

- [ ] **Step 5.3: 注册点 `runAgentCore`(两处,顺序硬约束)**

**位置 A**——`kernelEnv` 构造(`:1784-1789`)之后、`mainToolEnv := *kernelEnv`(`:1805`)**之前**(硬约束:子 agent 桥拿的是 mainToolEnv 拷贝,赋值晚于拷贝子 agent 就拿不到登记处):

```go
	// 后台 shell 登记(2026-11-28 后台 shell 批):登记处 manager 级(跨 run
	// 存活)。readOnly run 不注入(与 peer/子agent 同判据——启动后台进程是
	// 写动作;Bash 的 run_in_background 参数亦无登记处可用,内核层自报不支持)。
	if !run.readOnly {
		kernelEnv.BackgroundShells = m.ensureBackgroundShells()
	}
```

⚠️ 设计拍板(评审确认项):子 agent 桥拿的是 `&mainToolEnv`,因此**子 agent 也能启动后台任务**——有意为之:子 run 自己也是 run,「任务不随 run 停止而终止」对子 run 同样成立,登记处是 manager 级共享归属地。若未来要禁,在 `:1805` 拷贝后对桥用 env 清字段即可,勿现在做。

**位置 B**——listener 注入块(`:1865-1867`)之后,注册两个桥工具:

```go
	// 后台 shell 查询/终止工具:per-run 无状态桥,指向 manager 级登记处。
	if !run.readOnly && kernelEnv.BackgroundShells != nil {
		registry.Register(newBashOutputTool(run, kernelEnv.BackgroundShells, journal))
		registry.Register(newKillShellTool(run, kernelEnv.BackgroundShells, journal))
	}
```

- [ ] **Step 5.4: 测试**

1. `TestBashOutputListAndRead`:真 registry + 假 run(agentAIRun 零值+sessionID),经 `agentcore.DefaultRegistry().Get("Bash")` 后台启动 echo,再经 bashOutputTool 读到输出(直接调 Execute,不走整 run)。
2. `TestKillShellToolKills`:sleep 30 → KillShell → 状态转 cancelled。
3. `TestReadonlyToolsAllowBackgroundNames`:断言 readonly 判定对 "BashOutput"/"KillShell" 返回 auto-allow(沿用 `:1321` 附近既有测试的写法)。

```bash
go test ./app/http/services/ -run 'TestBashOutput|TestKillShell|TestReadonly' -count=1
```

---

## Task 6: ai.stop 走 guard(刀 3)

**Files:**
- Modify: `app/http/services/agent_ai_run_msg.go`(`stop`,`:542-563`)
- Test: `app/http/services/agent_ai_run_guard_test.go`(追加用例)

- [ ] **Step 6.1: 改 stop**

`stop()` 的加锁段(`:542`)顺带取 guard:

```go
	cancel := session.cancel
	guard := session.guard // 与 attachRunGuard 同锁(m.mu);nil=TUI 回合等未挂场景
```

`:553-557` 的 TODO 块替换为:

```go
	// 2026-11-28:用户主动停止改走 guard.kill——补上 [ai.run.kill] 判死轨迹
	// 与 30s 强拆兜底(此前裸 cancel,卡死回合停不掉也无轨迹,挂账
	// TODO(ai-run-resilience) 就业)。guard 未挂载(TUI 回合/竞态)回退裸
	// cancel,行为与旧版一致。
	if guard != nil {
		guard.kill("user_stop")
	} else if cancel != nil {
		cancel()
	} else if sourceSessionID != "" {
		// (原分支注释保留:外部 TUI best-effort interrupt)
		interruptExternalClaudeTurn(externalTUIHome(), sourceSessionID)
	}
```

- [ ] **Step 6.2: 测试**

追加 `TestStopRoutesThroughGuardKill`:构造 manager+session(参照 guard 既有测试的构造方式),挂 guard(session.guard 赋值),调 stop;断言:`activity.killReason == "user_stop"`、cancel 被 canceled、日志/force-finalize 路径激活(用短 `agentAIKillGrace` 测试变量验证强拆不会在 markDone 后触发)。对照用例:guard==nil 时行为同旧(cancel 生效、无 panic)。

```bash
go test ./app/http/services/ -run 'TestStop' -count=1 -v 2>&1 | tail -6
```

---

## Task 7: 全量验证

- [ ] **Step 7.1**:`go build ./... && GOOS=windows go build ./app/agentcore/ ./app/http/services/ && go vet ./app/agentcore/ ./app/http/services/`
- [ ] **Step 7.2**:`go test ./app/agentcore/ ./app/http/services/ ./common/... -count=1`(全量回归;services 已知预存 race flake 若出现,对照主仓同测试判定是否预存)
- [ ] **Step 7.3**:机制说明自查——`run_in_background` 的模型可见面=Bash schema 描述 + 两工具描述,无需 system prompt 后缀(与子agent 批不同,无生命周期宣告需求;BashOutput/KillShell 语义自解释)。
- [ ] **Step 7.4**:向用户报告:变更清单、测试结果、Windows 真机验证挂账项(Job 树杀/后台任务真行为,装机或 peer_exec 0.196 验)、提交请求(等明示)。

## 明确不做(YAGNI)

- 完成推送通知(ai.run.notice)——BashOutput 轮询已可用,通知属 P1 打磨
- 后台任务进手机 UI 卡片——工具结果即模型可见面,v1 不动前端
- SIGTERM 宽限梯——仓内 shell 路径直 KILL 是有意取舍(注释在案),需求出现再议
- 跨设备后台任务同步——登记处单机语义
- 活后台任务并发帽——内核帽需 Begin 加 error 路径或宿主审批层拍板;现状仅审批闸挡,64 条死记录逐出上限不限制活进程数。质量评审 2026-11-28 挂账:模型循环 run_in_background 可无限堆,出现滥用再议帽
- 登记处已知限制(Task 4 质量评审挂账):evictDeadLocked O(n) 扫 order(cap=64 无所谓);Shutdown-ensure 微窗口(快照 nil 后并发建簿不受收口,closeAllWithReason 已先停 run,可接受);两次 read 间 >256KB 突发仍可能丢早期段(单调 written 计数已把「回灌掩盖」静默形态堵死,剩余为绝对偏移跟踪才可解的固有取舍);登记处 8 用例 `!windows` 标签,Windows 机群不跑(内核组杀原语有 windows 专属测试覆盖)

## Windows 装机清单(Task 7 一并执行)

- `go test -run 'TestProcessGroupTree|TestNewToolCommandContext'`(proctree 原语+接线层)
- **Release 时机契约**的 Windows 侧自动化测试(两平台现均无测试盖住「Release 延后到 bgCtx.Done」——Task 2 评审修正的核心契约,质量评审 2026-11-28 挂账)
- PowerShell 腿 run_in_background + KillShell 真机端到端(后台分支 Windows 行为目前仅编译级验证)
