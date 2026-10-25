package agentcore

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
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
		"command":     stringProp("要执行的命令"),
		"description": stringProp("一句话说明这条命令做什么（用于给用户展示）"),
		"timeout":     intProp(fmt.Sprintf("超时毫秒数，默认 %d", defaultShellTimeout.Milliseconds())),
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
		"command":     stringProp("要执行的 PowerShell 命令"),
		"description": stringProp("一句话说明这条命令做什么"),
		"timeout":     intProp(fmt.Sprintf("超时毫秒数，默认 %d", defaultShellTimeout.Milliseconds())),
	}, "command")
}

func (t *powershellTool) Execute(ctx context.Context, input map[string]interface{}, env *ToolEnv) ToolResult {
	return runShell(ctx, "powershell", input, env)
}

// ---------------------------------------------------------------------------
// 共用执行骨架
// ---------------------------------------------------------------------------

// runShell 是 Bash / PowerShell 的共用实现。
func runShell(ctx context.Context, kind string, input map[string]interface{}, env *ToolEnv) ToolResult {
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

	timeout := defaultShellTimeout
	if ms, ok := inputInt(input, "timeout"); ok && ms > 0 {
		timeout = time.Duration(ms) * time.Millisecond
		if timeout > maxShellTimeout {
			timeout = maxShellTimeout
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := append(append([]string(nil), baseArgs...), command)
	cmd := exec.CommandContext(runCtx, shellPath, args...)
	if env.ProjectPath != "" {
		cmd.Dir = env.ProjectPath
	}

	var stdout, stderr strings.Builder
	// 各自独立封顶，避免 stderr 刷屏挤掉 stdout。
	cmd.Stdout = &cappedWriter{buf: &stdout, limit: maxToolOutputBytes}
	cmd.Stderr = &cappedWriter{buf: &stderr, limit: maxToolOutputBytes}

	runErr := cmd.Run()

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
		return ToolResult{Content: truncateToolOutput(sb.String()), IsError: true}
	}
	if ctx.Err() != nil {
		sb.WriteString("[命令被取消]")
		return ToolResult{Content: truncateToolOutput(sb.String()), IsError: true}
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
}

func (w *cappedWriter) Write(p []byte) (int, error) {
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
