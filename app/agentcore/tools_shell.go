package agentcore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// shell 工具：Bash / PowerShell。
//
// 两者共用同一套执行骨架（起进程、超时、输出截断、退出码），差异只在
// 「用哪个 shell 启动」和「怎么引用命令」。shell 的定位与命令白名单由宿主
// 通过 ToolEnv.ShellPolicy 注入 —— 内核禁止 import services，不能直接调
// agent_execution_guard.go 里的实现。
//
// 没配 ShellPolicy 时 fail closed：拒绝执行，而不是默认放行。

// defaultShellTimeout 是单条命令的默认超时。
//
// 30 秒是有意的保守值：这是「给模型跑构建/测试以外的小命令」的场景，
// 长任务应当拆成多次调用或让用户自己跑。超时后可被 timeout 参数覆盖。
const defaultShellTimeout = 30 * time.Second

// maxShellTimeout 是 timeout 参数的上限。
const maxShellTimeout = 10 * time.Minute

// defaultBackgroundShellLifetime 是单条后台命令的进程寿命上限默认值。对齐
// 终端子系统的 24h idle 帽；到期内核按 TimedOut 收尾。环境变量
// ALIANG_AGENTCORE_BG_SHELL_MAX_LIFETIME 可覆盖，用时解析（stream_idle.go
// 同款先例——包级 init 读死会让同进程内 t.Setenv 用例失效；解析与钳制都在
// runShell 后台分支）；低于 1 分钟的覆盖值钳到 1 分钟，防手滑把 dev server
// 配成秒级寿命。
const defaultBackgroundShellLifetime = 24 * time.Hour

// ShellOutcome describes how a started shell command ended.
type ShellOutcome string

const (
	ShellCompleted ShellOutcome = "completed"
	ShellFailed    ShellOutcome = "failed"
	ShellCancelled ShellOutcome = "cancelled"
	ShellTimedOut  ShellOutcome = "timed_out"
)

// ---------------------------------------------------------------------------
// Bash
// ---------------------------------------------------------------------------

type bashTool struct{}

func (t *bashTool) Name() string { return "Bash" }

func (t *bashTool) Description() string {
	return "在项目目录下执行一条 shell 命令，返回 stdout/stderr 与退出码。" +
		"用于运行构建、测试、git 等。不要用它做文件读写 —— 有专门的 Read/Write/Edit。"
}

func (t *bashTool) InputSchema() map[string]interface{} {
	return objectSchema(map[string]interface{}{
		"command":               stringProp("要执行的命令"),
		"description":           stringProp("一句话说明这条命令做什么（用于给用户展示）"),
		"timeout":               intProp(fmt.Sprintf("超时毫秒数，默认 %d", defaultShellTimeout.Milliseconds())),
		"progress_operation_id": stringProp("可选：本条命令对应的 progress start 返回的 operation_id。命令的真实结果会更新到该进度卡上，无需再对它调用 finish。"),
		"run_in_background":     boolProp("true=后台运行：立即返回 shell_id，不等待命令结束。输出用 BashOutput 增量读取，用 KillShell 终止。适合 dev server / watch / 长构建。后台模式忽略 timeout，进程寿命默认 24h（可用环境变量调整）；不随本回合停止而终止。"),
	}, "command")
}

func (t *bashTool) Execute(ctx context.Context, input map[string]interface{}, env *ToolEnv) ToolResult {
	return runShell(ctx, "bash", input, env)
}

// ---------------------------------------------------------------------------
// PowerShell
// ---------------------------------------------------------------------------

type powershellTool struct{}

func (t *powershellTool) Name() string { return "PowerShell" }

func (t *powershellTool) Description() string {
	return "在 Windows 上执行一条 PowerShell 命令。非 Windows 平台会失败。"
}

func (t *powershellTool) InputSchema() map[string]interface{} {
	return objectSchema(map[string]interface{}{
		"command":               stringProp("要执行的 PowerShell 命令"),
		"description":           stringProp("一句话说明这条命令做什么"),
		"timeout":               intProp(fmt.Sprintf("超时毫秒数，默认 %d", defaultShellTimeout.Milliseconds())),
		"progress_operation_id": stringProp("可选：本条命令对应的 progress start 返回的 operation_id。命令的真实结果会更新到该进度卡上，无需再对它调用 finish。"),
		"run_in_background":     boolProp("true=后台运行：立即返回 shell_id，不等待命令结束。输出用 BashOutput 增量读取，用 KillShell 终止。适合 dev server / watch / 长构建。后台模式忽略 timeout，进程寿命默认 24h（可用环境变量调整）；不随本回合停止而终止。"),
	}, "command")
}

func (t *powershellTool) Execute(ctx context.Context, input map[string]interface{}, env *ToolEnv) ToolResult {
	return runShell(ctx, "powershell", input, env)
}

// ---------------------------------------------------------------------------
// 共用执行骨架
// ---------------------------------------------------------------------------

// fallbackCancelDirectKill 是「树杀不可用/入树失败」时的降级 Cancel：至少
// 杀直接子进程，WaitDelay 兜底孙进程。三处消费：前台 runShell 入树失败、
// 后台 runShellBackground 入树失败、exec_windows.go Job 创建失败分支。
//
// 降级语义：Windows 上入树失败（OpenProcess 被安全软件拒等）时 Cancel 已绑
// KillTree、杀的是空 Job——超时/取消会漏掉直接子进程；覆盖为直接 kill 把
// 「至少杀直接子进程」的旧行为拿回来（POSIX 空壳 Attach 恒 nil，前台不会
// 走到这）。覆盖发生在 Start 返回后、ctx 到期前的 µs 级窗口内，形式上与
// watchCtx 读 Cancel 存在竞争窗；后果有界——即使撞窗杀空树，watchCtx 在
// Cancel 返回后仍会等满 WaitDelay 3s 并补杀直接子进程（exec.go watchCtx），
// 不放跑不挂死。
func fallbackCancelDirectKill(cmd *exec.Cmd) func() error {
	return func() error {
		if cmd.Process != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}

// runShell 是 Bash / PowerShell 的共用实现。
func runShell(ctx context.Context, kind string, input map[string]interface{}, env *ToolEnv) (result ToolResult) {
	command := strings.TrimSpace(inputString(input, "command"))
	if command == "" {
		return ErrorResult("缺少 command 参数")
	}
	if env == nil || env.ShellPolicy == nil {
		// fail closed：没注入策略就不执行。默认放行等于把「内核没配好」
		// 变成「内核可任意执行命令」。
		return ErrorResult("当前会话未启用命令执行（未配置 shell 策略）")
	}

	if allowed, reason := env.ShellPolicy.Allow(command); !allowed {
		if strings.TrimSpace(reason) == "" {
			reason = "命令不在允许范围内"
		}
		return ErrorResult("拒绝执行: %s", reason)
	}

	shellPath, baseArgs, err := env.ShellPolicy.Resolve(kind)
	if err != nil {
		return ErrorResult("%v", err)
	}

	// ProjectPath 先展开 ~ 再落 cmd.Dir(2026-11-25 线上事故):os/exec 不做
	// 波浪号展开,字面 "~" 会让每条命令在执行前就死在 chdir(darwin 呈现为
	// fork/exec ENOENT,Windows 呈现为「chdir ~: The system cannot find the
	// file specified.」),模型无从理解、只会盲试,直到触发防重试循环保险丝
	// 整局报废。目录不存在时同样直接给出可行动的报错,不再甩 cryptic 的
	// chdir 失败。
	projectDir := ""
	if env.ProjectPath != "" {
		expanded, expandErr := ExpandHome(strings.TrimSpace(env.ProjectPath))
		if expandErr != nil {
			expanded = strings.TrimSpace(env.ProjectPath)
		}
		if info, statErr := os.Stat(expanded); statErr != nil || !info.IsDir() {
			return ErrorResult("项目目录不存在或不可达: %s（请改用存在的目录，或先让用户创建它）", expanded)
		}
		projectDir = expanded
	}

	timeout := defaultShellTimeout
	if ms, ok := inputInt(input, "timeout"); ok && ms > 0 {
		timeout = time.Duration(ms) * time.Millisecond
		if timeout > maxShellTimeout {
			timeout = maxShellTimeout
		}
	}

	args := append(append([]string(nil), baseArgs...), command)

	// ---- 后台分支：登记 → 独立 ctx 启动 → 立即返回；等待在 goroutine ----
	// 不吃 run ctx：run 停止（dev server 继续跑）是本特性的存在意义。
	// 审批/白名单已在上方完成：后台启动与前台同策略，不会绕闸。
	if inputBool(input, "run_in_background") {
		if env.BackgroundShells == nil {
			return ErrorResult("当前会话不支持后台命令（请去掉 run_in_background 改前台执行）")
		}
		// 寿命帽用时解析（见 defaultBackgroundShellLifetime 注释）。
		lifetime := agentcoreEnvDuration("ALIANG_AGENTCORE_BG_SHELL_MAX_LIFETIME", defaultBackgroundShellLifetime)
		if lifetime <= time.Minute {
			lifetime = time.Minute // 钳制语义保持：显式覆盖也压回 1min 下限
		}
		return runShellBackground(shellPath, args, projectDir, env.Env, env.BackgroundShells, command, lifetime)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 必须走 newToolCommandContext（Setpgid + 组杀 + WaitDelay）：裸
	// exec.CommandContext 只杀直接子进程，复合命令（`a; b | c`）fork 出的孙
	// 进程会继承 stdout 管道并成为孤儿，导致 cmd.Run() 在超时后仍然无限等待。
	// 详见 exec_unix.go 的文件注释与 2026-11-01 线上事故。
	cmd, tree := newToolCommandContext(runCtx, shellPath, args...)
	if projectDir != "" {
		cmd.Dir = projectDir
	}
	if env.Env != nil {
		cmd.Env = env.Env
	}

	var stdout, stderr strings.Builder
	// 各自独立封顶，避免 stderr 刷屏挤掉 stdout。
	stdoutWriter := &cappedWriter{buf: &stdout, limit: maxToolOutputBytes}
	stderrWriter := &cappedWriter{buf: &stderr, limit: maxToolOutputBytes}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	runErr := cmd.Start()
	if runErr == nil {
		if err := tree.Attach(cmd); err != nil {
			// 入树失败（Windows 上 OpenProcess 被安全软件拒等）：降级为直接
			// kill，理由与竞争窗分析见 fallbackCancelDirectKill。
			cmd.Cancel = fallbackCancelDirectKill(cmd)
		}
		if env.OnShellStart != nil {
			tool := "Bash"
			if kind == "powershell" {
				tool = "PowerShell"
			}
			outputBytes := func() int64 { return stdoutWriter.bytes.Load() + stderrWriter.bytes.Load() }
			if finish := env.OnShellStart(tool, strings.TrimSpace(inputString(input, "progress_operation_id")), outputBytes); finish != nil {
				defer func() {
					switch {
					case result.TimedOut:
						finish(ShellTimedOut)
					case result.IsError && ctx.Err() != nil:
						finish(ShellCancelled)
					case result.IsError:
						finish(ShellFailed)
					default:
						finish(ShellCompleted)
					}
				}()
			}
		}
		runErr = cmd.Wait()
	}
	// Release 必须在 Wait 返回之后（并发契约见 proctree_windows.go）。Start
	// 失败路径同样要收尾：POSIX 空壳是字面 no-op；Windows 上 Job 已建成未
	// Attach 时 Release 是关句柄（KILL_ON_JOB_CLOSE 下树内无进程，纯资源
	// 收尾）——行为正确性在于「不漏句柄」。
	tree.Release()

	var sb strings.Builder
	if s := stdout.String(); s != "" {
		sb.WriteString(s)
		if !strings.HasSuffix(s, "\n") {
			sb.WriteString("\n")
		}
	}
	if s := stderr.String(); s != "" {
		sb.WriteString("[stderr]\n")
		sb.WriteString(s)
		if !strings.HasSuffix(s, "\n") {
			sb.WriteString("\n")
		}
	}

	if runCtx.Err() == context.DeadlineExceeded {
		sb.WriteString(fmt.Sprintf("[命令超时，已终止（上限 %s）]", timeout))
		return ToolResult{Content: truncateToolOutput(sb.String()), IsError: true, TimedOut: true}
	}
	if ctx.Err() != nil {
		sb.WriteString("[命令被取消]")
		return ToolResult{Content: truncateToolOutput(sb.String()), IsError: true}
	}

	// WaitDelay 到期：命令**自己已经结束**，只是还有后代进程（`cmd &`、nohup、
	// dev server）握着输出管道没关。这不是失败——必须把已采集的输出回填，并
	// 说明它可能不完整。此前这里会落到下面的通用失败分支，把 stdout 整段丢掉
	// 并报「执行失败」，模型于是重试，每重试一次再漏一个后台孤儿进程。
	if errors.Is(runErr, exec.ErrWaitDelay) {
		sb.WriteString(fmt.Sprintf("\n[命令已结束，但仍有后台子进程在运行并占用输出管道，" +
			"上面的输出可能不完整。]"))
		isErr := cmd.ProcessState == nil || !cmd.ProcessState.Success()
		return ToolResult{Content: truncateToolOutput(sb.String()), IsError: isErr}
	}

	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			sb.WriteString(fmt.Sprintf("[退出码 %d]", exitErr.ExitCode()))
			return ToolResult{Content: truncateToolOutput(sb.String()), IsError: true}
		}
		// 起进程就失败（shell 路径不存在等），没有输出可回填。
		return ErrorResult("执行失败: %v", runErr)
	}

	if sb.Len() == 0 {
		return ToolResult{Content: "（命令执行成功，无输出）"}
	}
	return ToolResult{Content: truncateToolOutput(sb.String())}
}

// cappedWriter 是带上限的写入器。超过上限后丢弃后续内容并置截断标记，
// 避免一条 `yes` 把内存吃光。
type cappedWriter struct {
	buf       *strings.Builder
	limit     int
	truncated bool
	bytes     atomic.Int64
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	w.bytes.Add(int64(len(p)))
	if w.truncated {
		return len(p), nil
	}
	remaining := w.limit - w.buf.Len()
	if remaining <= 0 {
		w.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		w.buf.Write(p[:remaining])
		w.truncated = true
		return len(p), nil
	}
	w.buf.Write(p)
	return len(p), nil
}

// runShellBackground 是 runShell 的后台路径：进程纪律（组杀/WaitDelay）与
// 前台完全同源（newToolCommandContext），唯一区别是 ctx 独立于 run、输出写
// 进宿主登记处、Wait 挪到 goroutine。lifetime 由调用方解析并钳制后传入——
// 包内小写函数无外部消费者，测试直传短帽绕过 1min 下限直达 TimedOut 分支。
func runShellBackground(shellPath string, args []string, dir string, cmdEnv []string, registry BackgroundShellRegistry, command string, lifetime time.Duration) ToolResult {
	handle, id := registry.Begin(command)
	// Finished 契约「恰好一次」：panic 兜底、终态 switch、Start 失败是三条
	// 收敛路径（彼此互斥但分处 goroutine 内外），once 保证宿主登记处任何
	// 情况下只收到一次终态。
	var finishOnce sync.Once
	finish := func(o ShellOutcome, d string) {
		finishOnce.Do(func() { handle.Finished(o, d) })
	}
	bgCtx, bgCancel := context.WithTimeout(context.Background(), lifetime)
	cmd, tree := newToolCommandContext(bgCtx, shellPath, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if cmdEnv != nil {
		cmd.Env = cmdEnv
	}
	// 单一合并写入口（stdout/stderr 按到达顺序交织，与 Claude Code 后台任务
	// 同形态）；handle 自身并发安全。
	cmd.Stdout = handle
	cmd.Stderr = handle

	runErr := cmd.Start()
	if runErr != nil {
		finish(ShellFailed, fmt.Sprintf("启动失败: %v", runErr))
		bgCancel()
		tree.Release()
		return ErrorResult("后台命令启动失败: %v", runErr)
	}
	if err := tree.Attach(cmd); err != nil {
		// 入树失败降级：见 fallbackCancelDirectKill（与前台 runShell 同款）。
		cmd.Cancel = fallbackCancelDirectKill(cmd)
	}
	kill := newProcessGroupKiller(cmd, tree)
	handle.MarkRunning(cmd.Process.Pid)
	handle.SetCancel(func() {
		kill()     // 直接组杀：watchCtx 退场（主进程被 reap）后依然有效
		bgCancel() // 关 ctx：驱动 Done→Release 侧收尾
	})
	// 刻意不接 OnShellStart/进度卡：后台任务的可视面是 BashOutput（模型侧）+
	// 终态在登记处状态行；进度卡语义是「同步等待中的观察」，与立即返回的后台
	// 形态不匹配（v1 明确裁掉）。

	go func() {
		waitErr := func() (e error) {
			defer func() {
				// panic 兜底：等待 goroutine 不许拖垮宿主进程（内核在宿主进程内）。
				if r := recover(); r != nil {
					finish(ShellFailed, fmt.Sprintf("内部错误: %v", r))
					bgCancel() // 触发 Done，让外层的 Release 在一切退出路径上都执行
				}
			}()
			return cmd.Wait()
		}()
		switch {
		case errors.Is(waitErr, exec.ErrWaitDelay):
			// 直接子命令已结束、后代仍握管道：后台语义下这正是常态
			// （nohup/dev server），按 Completed 收尾。
			finish(ShellCompleted, "主命令已退出（后台进程仍在运行）")
		case bgCtx.Err() == context.DeadlineExceeded:
			finish(ShellTimedOut, fmt.Sprintf("寿命上限 %s 到期，已终止", lifetime))
		case bgCtx.Err() != nil:
			finish(ShellCancelled, "已被 KillShell 终止")
		case waitErr != nil:
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				finish(ShellFailed, fmt.Sprintf("退出码 %d", exitErr.ExitCode()))
			} else {
				finish(ShellFailed, fmt.Sprintf("等待失败: %v", waitErr))
			}
		default:
			finish(ShellCompleted, "退出码 0")
		}

		// ⚠️ Release 的时机契约（Task 2 spec 评审定谳）：**不得在 Wait 返回后
		// 立即 Release**——Windows 侧 Release 关 Job 句柄，KILL_ON_JOB_CLOSE
		// 会连带终止仍存活的后台子孙（`cmd &`/dev server），违背后台承诺。
		// 句柄保持开放，直到 bgCtx 结束（KillShell 或寿命帽）——此时「树死或
		// 被杀」二选一，收尾均正确。agent 进程崩溃时句柄由内核回收 +
		// KILL_ON_JOB_CLOSE 仍兜底整树终止（Task 1 Release 注释同契约）。
		<-bgCtx.Done() // 必达：KillShell（kill→bgCancel）与寿命帽到期都会关 Done
		tree.Release()
		// 终态后 armed 一直保持到这里的收尾点才解除——KillShell/寿命帽/
		// closeAll 对 `&` 孙进程全程有效。armed 窗=[自然死亡 … Done]，最长
		// 24h，期间 pid 复用险由 newProcessGroupKiller 的 kill(-pid,0) 存活
		// 探测覆盖（Windows 按 Job 身份杀，免疫复用险）；解除后再调由实现
		// 方安全忽略（接口契约）。ctx 已在 Done 上，无需尾调 bgCancel。
		handle.SetCancel(nil)
	}()

	return ToolResult{Content: fmt.Sprintf(
		"后台任务已启动 shell_id=%s（PID %d）。用 BashOutput(shell_id=\"%s\") 增量读取输出；"+
			"用 KillShell(shell_id=\"%s\") 终止。任务不随本回合停止而终止。",
		id, cmd.Process.Pid, id, id)}
}
