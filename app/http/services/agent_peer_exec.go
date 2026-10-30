package services

// 设备互访阶段 1（spec 2026-10-29-device-peer-link-and-profile-design §5.2）：
// B 端一次性命令执行器。A 设备的 LLM 调 peer_exec 工具 → 服务端编排 → 云 WS
// peer.exec.request 帧 → 本执行器 → peer.exec.result 帧。
//
// 安全边界（评审定案，勿放宽）：
//   - 一次性 exec 而非 PTY 会话：无状态、天然幂等、免跨中继生命周期管理；
//   - 同设备同时只跑 1 个（进程级互斥锁 TryLock 原子占位），忙即回 busy；
//   - 子进程 env 是最小集（PATH/HOME/TERM/LANG/TMPDIR），严禁继承 agent 进程
//     全量 env——其中含 LLM 网关凭据（ANTHROPIC_* 等），继承即跨设备泄漏；
//   - shell 走 resolveAgentShell 可信路径解析（isTrustedAgentShellPath 白名单
//     + 可执行校验），PATH 上被替换的 shell 不受信任；
//   - 输出 stdout/stderr 各 256KiB 封顶（溢出丢弃并置 truncated=true，累计
//     溢出超 4×cap 停止排水，靠超时组杀收尾——二级熔断防无界排水）；
//   - workdir 必须存在（不存在回 workdir_missing，不代创建）；
//   - 每次执行落 journal 审计行（含 busy/bad_request/workdir_missing 等早退
//     路径；服务端 audit_logs 是主审计面，本地留痕兜底）。
//
// 进程纪律：必须走 newBackgroundCommandContext（仓内既定封装），裸
// exec.CommandContext 是已知的串行槽杀手——子进程后台化的孙进程继承 stdout
// 管道（nohup x & 在管道上不重定向），直接子进程退出后 cmd.Wait() 仍阻塞在
// 管道 EOF，ctx kill 只杀直接子进程 → defer Unlock 永不执行 → 本设备
// peer-exec 槽永久 busy 直到进程重启。Setpgid + Cancel 组杀(-pgid) +
// WaitDelay 三件套保证：超时杀整组、孙进程管道随之关闭、最坏 WaitDelay
// 后 Wait 强制返回、串行槽必然释放。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
	"aliang.one/nursorgate/common/logger"
)

const (
	peerExecTimeoutDefaultMs = 30_000
	peerExecTimeoutMinMs     = 1_000
	peerExecTimeoutMaxMs     = 600_000
	// peerExecOutputCap 是单流（stdout 或 stderr）封顶字节数，对齐契约
	// 「stdout/stderr 各≤256KiB」。超出部分丢弃（不是停止读：继续排水防止
	// 子进程写满管道阻塞到超时才死），并置 truncated=true。
	peerExecOutputCap = 256 << 10
	// peerExecDrainBreakMultiplier 是排水二级熔断：单流累计丢弃超过
	// 4×cap 后 Write 开始报错停读（子进程会写满管道挂住，由超时组杀收尾），
	// 防止恶意进程用无限输出把排水循环拖满整个超时窗口。
	peerExecDrainBreakMultiplier = 4
	// peerExecAuditCommandMax 审计行里命令文本的截断长度（rune 安全）：审计
	// 要能对账，但不能把一条超长命令原样落盘。
	peerExecAuditCommandMax = 300
)

// peerExecFailReason 契约 fail_reason 取值（open set：服务端/客户端都可追加）。
const (
	peerExecFailBusy           = "busy"
	peerExecFailStartFailed    = "start_failed"
	peerExecFailWorkdirMissing = "workdir_missing"
	peerExecFailTimeout        = "timeout"
	peerExecFailBadRequest     = "bad_request"
	peerExecFailSignaled       = "signaled"
)

// agentPeerExecMu 是同设备串行执行的进程级占位锁。设备=单 agent 进程，进程级
// 互斥即设备级互斥；TryLock 原子占位，忙立即回 busy（不排队）。
var agentPeerExecMu sync.Mutex

// handleAgentPeerExecRequest 校验并执行一条 peer.exec.request，回写
// peer.exec.result。request_id 缺失时无法与服务端 RPC 关联（回了也会被丢），
// 静默丢弃 + Warn。所有早退路径（bad_request/busy/workdir_missing）与正常
// 执行一律落 journal 审计行。
func (s *AgentService) handleAgentPeerExecRequest(msg map[string]interface{}, writeJSON func(interface{}) error) {
	requestID := remoteString(msg, "request_id")
	sourceDeviceID := remoteString(msg, "source_device_id")
	command := remoteString(msg, "command")
	if requestID == "" {
		logger.Warn("[AGENT-PEER] peer_exec_request dropped reason=missing_request_id")
		return
	}
	reply := func(payload map[string]interface{}) {
		if writeJSON == nil {
			return
		}
		_ = writeJSON(payload)
	}
	audit := func(outcome string, exitCode int, failReason string, durationMs int64) {
		s.peerExecJournalAudit(sourceDeviceID, outcome, exitCode, failReason, durationMs, command)
	}
	if strings.TrimSpace(command) == "" {
		reply(agentPeerExecResultPayload(requestID, s.currentDeviceID(), -1, "", "", false, 0, peerExecFailBadRequest))
		audit("bad_request", -1, peerExecFailBadRequest, 0)
		return
	}

	// 串行占位：先于一切耗时动作（含 workdir 校验）占位，保证「同一时刻
	// 至多一条在跑」不依赖执行时长。
	if !agentPeerExecMu.TryLock() {
		reply(agentPeerExecResultPayload(requestID, s.currentDeviceID(), -1, "", "", false, 0, peerExecFailBusy))
		audit("busy", -1, peerExecFailBusy, 0)
		return
	}
	defer agentPeerExecMu.Unlock()

	timeoutMs := clampPeerExecTimeoutMs(remoteInt(msg, "timeout_ms", peerExecTimeoutDefaultMs))
	workdir := peerExecWorkdir(remoteString(msg, "workdir"))
	if workdir == "" {
		// $HOME 都解析不出来（异常环境）：无法保证执行语境，按启动失败回。
		reply(agentPeerExecResultPayload(requestID, s.currentDeviceID(), -1, "", "", false, 0, peerExecFailStartFailed))
		audit("start_failed", -1, peerExecFailStartFailed, 0)
		return
	}
	if info, err := os.Stat(workdir); err != nil || !info.IsDir() {
		reply(agentPeerExecResultPayload(requestID, s.currentDeviceID(), -1, "", "", false, 0, peerExecFailWorkdirMissing))
		audit("workdir_missing", -1, peerExecFailWorkdirMissing, 0)
		return
	}

	exitCode, stdout, stderr, truncated, durationMs, failReason := runPeerExecCommand(command, workdir, time.Duration(timeoutMs)*time.Millisecond)
	reply(agentPeerExecResultPayload(requestID, s.currentDeviceID(), exitCode, stdout, stderr, truncated, durationMs, failReason))
	audit("executed", exitCode, failReason, durationMs)
}

// agentPeerExecJournalSession 是 peer 执行审计行的 journal 会话名：文件
// peer-exec.jsonl 只含 kind=tool 行、无 kind=session 头行——读侧扫描
// parsedAny=false 整文件跳过（agent_inventory_agentcore.go 幽灵防御），审计
// 留痕进磁盘但不会在仪表盘 AI 会话列表里冒出假会话卡。
const agentPeerExecJournalSession = "peer-exec"

// peerExecJournalAudit 落一条 kind=tool 审计行。nil-safe（coreJournal 为空时
// no-op）；尽力而为，失败只 Warn 不影响主流程。
func (s *AgentService) peerExecJournalAudit(sourceDeviceID, outcome string, exitCode int, failReason string, durationMs int64, command string) {
	if s.ai == nil {
		return
	}
	s.ai.coreJournal.appendTool(
		agentPeerExecJournalSession,
		"peer.exec",
		agentPeerExecAuditSummary(sourceDeviceID, outcome, exitCode, failReason, durationMs, command),
		failReason != "" || exitCode != 0,
	)
}

func agentPeerExecResultPayload(requestID, targetDeviceID string, exitCode int, stdout, stderr string, truncated bool, durationMs int64, failReason string) map[string]interface{} {
	payload := map[string]interface{}{
		"type":             models.AgentEventPeerExecResult,
		"request_id":       requestID,
		"target_device_id": targetDeviceID,
		"exit_code":        exitCode,
	}
	if stdout != "" {
		payload["stdout"] = stdout
	}
	if stderr != "" {
		payload["stderr"] = stderr
	}
	if truncated {
		payload["truncated"] = true
	}
	if durationMs > 0 {
		payload["duration_ms"] = durationMs
	}
	if failReason != "" {
		payload["fail_reason"] = failReason
	}
	return payload
}

// clampPeerExecTimeoutMs 钳 [1000,600000]，缺省 30000（契约）。
func clampPeerExecTimeoutMs(v int) int {
	if v <= 0 {
		return peerExecTimeoutDefaultMs
	}
	if v < peerExecTimeoutMinMs {
		return peerExecTimeoutMinMs
	}
	if v > peerExecTimeoutMaxMs {
		return peerExecTimeoutMaxMs
	}
	return v
}

// peerExecWorkdir 解析执行工作目录：显式 workdir 优先（做 ~ 展开，与终端路径
// 语义一致）；缺省 $HOME。
func peerExecWorkdir(raw string) string {
	workdir := strings.TrimSpace(raw)
	if workdir == "" {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return ""
		}
		return home
	}
	if expanded, err := cache.ExpandHomePath(workdir); err == nil && strings.TrimSpace(expanded) != "" {
		return expanded
	}
	return workdir
}

// runPeerExecCommand 用可信 shell 一次性执行 command，返回
// (exitCode, stdout, stderr, truncated, durationMs, failReason)。
// 正常结束回真实 exit_code（含非零）；无法启动回 -1+start_failed；超时组杀回
// -1+timeout；被外部信号杀（且非自家超时）回 -1+signaled。
func runPeerExecCommand(command, workdir string, timeout time.Duration) (int, string, string, bool, int64, string) {
	shellPath, args, err := agentPeerShellCommand(command)
	if err != nil {
		logger.Warn(fmt.Sprintf("[AGENT-PEER] peer_exec shell resolve failed error=%v", err))
		return -1, "", "", false, 0, peerExecFailStartFailed
	}

	// 进程纪律（见文件头）：Setpgid+组杀+WaitDelay 由 newBackgroundCommandContext
	// 统一提供，Windows 侧带 HideWindow（不闪 powershell 窗口）。
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := newBackgroundCommandContext(ctx, shellPath, args...)
	cmd.Dir = workdir
	cmd.Env = agentPeerExecEnv()

	stdoutBuf := &peerExecBoundedBuffer{max: peerExecOutputCap}
	stderrBuf := &peerExecBoundedBuffer{max: peerExecOutputCap}
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf

	started := time.Now()
	runErr := cmd.Run()
	durationMs := time.Since(started).Milliseconds()

	exitCode, failReason := classifyPeerExecOutcome(cmd, runErr, ctx)
	truncated := stdoutBuf.isTruncated() || stderrBuf.isTruncated()
	return exitCode, stdoutBuf.String(), stderrBuf.String(), truncated, durationMs, failReason
}

// classifyPeerExecOutcome 把 cmd.Run 的结局翻译成 (exit_code, fail_reason)。
// 判定顺序刻意以 ProcessState.Signaled() 为第一判据（而非 ctx.Err()）：命令
// 恰好在超时瞬间正常退出时，ctx.Err() 已非 nil，先看 ctx 会把真实退出码误报
// 成 timeout。
func classifyPeerExecOutcome(cmd *exec.Cmd, runErr error, ctx context.Context) (int, string) {
	if runErr == nil {
		return 0, ""
	}
	var exitErr *exec.ExitError
	signaled := peerExecProcessSignaled(runErr, &exitErr)
	switch {
	case signaled && ctx.Err() != nil:
		// 自家超时组杀（SIGKILL 打在整组上）：按 timeout 报告，不把 kill 信号
		// 码当业务退出码。
		return -1, peerExecFailTimeout
	case signaled:
		// 外部信号杀（且非超时窗口）：如实报告。
		return -1, peerExecFailSignaled
	case errors.As(runErr, &exitErr) && exitErr.ExitCode() >= 0:
		// shell 本体正常退出（命令自身失败）：回真实退出码，非 fail_reason。
		return exitErr.ExitCode(), ""
	case errors.Is(runErr, exec.ErrWaitDelay):
		// 组杀已落地但孙进程管道拖过 WaitDelay 才被强断——结局等同超时窗口
		// 内未收敛；ctx 已到期按 timeout，否则归启动/收尾失败。
		if ctx.Err() != nil {
			return -1, peerExecFailTimeout
		}
		return -1, peerExecFailStartFailed
	case ctx.Err() != nil:
		// 兜底：ctx 到期但进程既非 signaled 也非正常退出（极端时序）。
		return -1, peerExecFailTimeout
	default:
		return -1, peerExecFailStartFailed
	}
}

// peerExecProcessSignaled 报告进程是否被信号终止。os.ProcessState 没有导出
// Signaled：非 Windows 经 Sys().(syscall.WaitStatus).Signaled() 精确判定；
// Windows 的 Sys() 为 nil，退回 ExitCode()==-1（Go 约定：被信号杀/未退出时
// 恒 -1，本场景子进程必然已退出）。
func peerExecProcessSignaled(runErr error, exitErr **exec.ExitError) bool {
	if !errors.As(runErr, exitErr) || *exitErr == nil || (*exitErr).ProcessState == nil {
		return false
	}
	if ws, ok := (*exitErr).Sys().(syscall.WaitStatus); ok {
		return ws.Signaled()
	}
	return (*exitErr).ExitCode() == -1
}

// agentPeerShellCommand 按平台挑 shell 并构造参数：
//   - 非 Windows：defaultAgentShell()（zsh/bash/sh）经 resolveAgentShell 全套
//     校验（白名单基名 + 可执行 + isTrustedAgentShellPath 可信目录）后 `-c`；
//   - Windows：powershell `-Command`（契约钉死，cmd.exe 的引号转义不可靠）。
func agentPeerShellCommand(command string) (string, []string, error) {
	if runtime.GOOS == "windows" {
		shellPath, err := resolveAgentShell("powershell.exe")
		if err != nil {
			return "", nil, err
		}
		return shellPath, []string{"-NoProfile", "-NonInteractive", "-Command", command}, nil
	}
	shellPath, err := resolveAgentShell("")
	if err != nil {
		return "", nil, err
	}
	return shellPath, []string{"-c", command}, nil
}

// agentPeerExecEnv 是 peer 子进程的最小环境集。逐键取值——只读这些白名单键，
// 不继承 agent 进程其余 env（其中含 LLM 网关凭据等敏感项，继承即跨设备泄漏）。
// Windows 注：HOME/TERM/LANG 非必需键，设置了也无害；SystemRoot 等系统键缺失
// 会影响部分 GUI 子进程，属可接受的最小集取舍（契约钉死五键）。
func agentPeerExecEnv() []string {
	env := make([]string, 0, 5)
	path := strings.TrimSpace(os.Getenv("PATH"))
	if path == "" {
		if runtime.GOOS == "windows" {
			path = `C:\Windows\System32`
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
	return env
}

func agentPeerHomeValue() string {
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return home
	}
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		return home
	}
	return ""
}

// peerExecBoundedBuffer 是带封顶的输出容器：
//   - 写满即丢弃后续字节并置 truncated，但继续消费 Write 排水——防止子进程
//     写满管道缓冲后阻塞，把一次超封顶的快命令拖成超时；
//   - 单流累计丢弃超过 max×peerExecDrainBreakMultiplier 后 Write 开始报错
//     停读（二级熔断）：排水循环不再陪恶意进程耗满整个超时窗口，子进程会
//     写满管道挂住，由超时组杀收尾。
type peerExecBoundedBuffer struct {
	mu            sync.Mutex
	buf           bytes.Buffer
	max           int
	truncated     bool
	overflowBytes int
	broken        bool
}

func (b *peerExecBoundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.broken {
		return 0, errors.New("peer exec output drain circuit broken")
	}
	if b.max <= 0 {
		b.buf.Write(p)
		return len(p), nil
	}
	remaining := b.max - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		b.overflowBytes += len(p)
		if b.overflowBytes > b.max*peerExecDrainBreakMultiplier {
			b.broken = true
			return 0, errors.New("peer exec output drain circuit broken")
		}
		return len(p), nil
	}
	if len(p) > remaining {
		b.buf.Write(p[:remaining])
		b.truncated = true
		b.overflowBytes = len(p) - remaining
		if b.overflowBytes > b.max*peerExecDrainBreakMultiplier {
			b.broken = true
			return len(p), errors.New("peer exec output drain circuit broken")
		}
		return len(p), nil
	}
	b.buf.Write(p)
	return len(p), nil
}

func (b *peerExecBoundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *peerExecBoundedBuffer) isTruncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

// agentPeerExecAuditSummary 组装 journal 审计行 summary：来源设备 + 终态 +
// 截断后的命令文本（rune 安全截断）。命令可能含用户敏感内容，审计对账需要
// 它，但不落全量。
func agentPeerExecAuditSummary(sourceDeviceID, outcome string, exitCode int, failReason string, durationMs int64, command string) string {
	state := fmt.Sprintf("exit=%d", exitCode)
	if failReason != "" {
		state = fmt.Sprintf("exit=%d fail_reason=%s", exitCode, failReason)
	}
	if outcome != "" && outcome != "executed" {
		// 早退路径（busy/bad_request/workdir_missing/start_failed）显式标出，
		// 与真实执行过的行可区分。
		state = fmt.Sprintf("%s outcome=%s", state, outcome)
	}
	cmd := truncateAgentText(strings.TrimSpace(command), peerExecAuditCommandMax)
	return fmt.Sprintf("source=%s %s duration_ms=%d cmd=%s", strings.TrimSpace(sourceDeviceID), state, durationMs, cmd)
}
