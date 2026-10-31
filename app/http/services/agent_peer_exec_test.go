package services

// 设备互访阶段 1（spec §5.2）B 端执行器单测。覆盖：正常回显/超时 kill/输出
// 封顶截断/workdir 不存在/串行 busy/最小 env 断言（不含 ANTHROPIC 等敏感键）。

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/common/cache"
)

func peerExecTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	t.Cleanup(cache.ResetCacheDirForTest)
}

func TestPeerExec_NormalCommandEcho(t *testing.T) {
	exitCode, stdout, stderr, truncated, _, failReason := runPeerExecCommand("echo peer-hello", t.TempDir(), 10*time.Second)
	if exitCode != 0 || failReason != "" {
		t.Fatalf("exit=%d fail=%q stderr=%q", exitCode, failReason, stderr)
	}
	if !strings.Contains(stdout, "peer-hello") {
		t.Fatalf("stdout=%q missing echo", stdout)
	}
	if truncated {
		t.Fatal("truncated must be false for small output")
	}
}

func TestPeerExec_NonZeroExitIsRealCode(t *testing.T) {
	if peerExecShellSemanticsSupported(t) {
		return
	}
	exitCode, _, _, _, _, failReason := runPeerExecCommand("exit 7", t.TempDir(), 10*time.Second)
	if exitCode != 7 {
		t.Fatalf("exit=%d, want real code 7", exitCode)
	}
	if failReason != "" {
		t.Fatalf("fail_reason=%q, normal termination must not carry one", failReason)
	}
}

func TestPeerExec_TimeoutKills(t *testing.T) {
	if peerExecShellSemanticsSupported(t) {
		return
	}
	started := time.Now()
	exitCode, _, _, _, _, failReason := runPeerExecCommand("sleep 30", t.TempDir(), 800*time.Millisecond)
	elapsed := time.Since(started)
	if exitCode != -1 || failReason != peerExecFailTimeout {
		t.Fatalf("exit=%d fail=%q, want -1/timeout", exitCode, failReason)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("kill took %v, deadline not enforced", elapsed)
	}
}

func TestPeerExec_OutputCapTruncation(t *testing.T) {
	if peerExecShellSemanticsSupported(t) {
		return
	}
	// 400KB 输出，超过 256KiB 封顶：截断置位、体积恒等于封顶值，且进程不被
	// 管道背压拖死（bounded buffer 持续排水）。
	exitCode, stdout, _, truncated, _, failReason :=
		runPeerExecCommand("yes a | head -c 400000", t.TempDir(), 15*time.Second)
	if exitCode != 0 || failReason != "" {
		t.Fatalf("exit=%d fail=%q", exitCode, failReason)
	}
	if !truncated {
		t.Fatal("truncated must be true when output exceeds cap")
	}
	if len(stdout) != peerExecOutputCap {
		t.Fatalf("stdout len=%d, want cap %d", len(stdout), peerExecOutputCap)
	}
}

func TestPeerExec_WorkdirMissing(t *testing.T) {
	if peerExecShellSemanticsSupported(t) {
		return
	}
	missing := t.TempDir() + "/definitely/not/here"
	exitCode, _, _, _, _, failReason := runPeerExecCommand("echo hi", missing, 5*time.Second)
	// 执行器层面：目录缺失由调用方（handleAgentPeerExecRequest）先校验并回
	// workdir_missing；runPeerExecCommand 收到缺失目录时 start 失败兜底。
	if exitCode != -1 || (failReason != peerExecFailStartFailed && failReason != "") {
		t.Fatalf("exit=%d fail=%q", exitCode, failReason)
	}
}

func TestPeerExec_WorkdirExpandHome(t *testing.T) {
	if peerExecShellSemanticsSupported(t) {
		return
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := peerExecWorkdir("~/sub"); got != home+"/sub" {
		t.Fatalf("peerExecWorkdir(~/sub) = %q, want %q", got, home+"/sub")
	}
	if got := peerExecWorkdir(""); got != home {
		t.Fatalf("peerExecWorkdir(\"\") = %q, want home %q", got, home)
	}
}

func TestPeerExec_HandlerWorkdirMissingReason(t *testing.T) {
	peerExecTestEnv(t)
	missing := t.TempDir() + "/nope"
	var wrote map[string]interface{}
	svc := &AgentService{}
	svc.handleAgentPeerExecRequest(map[string]interface{}{
		"type":             "peer.exec.request",
		"request_id":       "req-wd-1",
		"source_device_id": "device-a",
		"command":          "echo hi",
		"workdir":          missing,
	}, func(p interface{}) error {
		wrote = p.(map[string]interface{})
		return nil
	})
	if wrote == nil {
		t.Fatal("no result written")
	}
	if wrote["fail_reason"] != peerExecFailWorkdirMissing {
		t.Fatalf("fail_reason=%v, want workdir_missing", wrote["fail_reason"])
	}
	if wrote["exit_code"] != -1 {
		t.Fatalf("exit_code=%v, want -1", wrote["exit_code"])
	}
	if wrote["type"] != "peer.exec.result" {
		t.Fatalf("type=%v", wrote["type"])
	}
	if wrote["request_id"] != "req-wd-1" {
		t.Fatalf("request_id=%v", wrote["request_id"])
	}
	if wrote["target_device_id"] == nil || wrote["target_device_id"] == "" {
		t.Fatal("target_device_id required by contract")
	}
}

func TestPeerExec_SerialBusy(t *testing.T) {
	peerExecTestEnv(t)
	// 直接占住进程级互斥锁，模拟另一条 peer exec 在跑：第二条必须立即回
	// busy（不排队、不执行）。
	agentPeerExecMu.Lock()
	defer agentPeerExecMu.Unlock()

	var wrote map[string]interface{}
	svc := &AgentService{}
	started := time.Now()
	svc.handleAgentPeerExecRequest(map[string]interface{}{
		"type":             "peer.exec.request",
		"request_id":       "req-busy-1",
		"source_device_id": "device-a",
		"command":          "echo should-not-run",
	}, func(p interface{}) error {
		wrote = p.(map[string]interface{})
		return nil
	})
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("busy reply took %v, must be immediate", elapsed)
	}
	if wrote == nil {
		t.Fatal("no result written")
	}
	if wrote["fail_reason"] != peerExecFailBusy {
		t.Fatalf("fail_reason=%v, want busy", wrote["fail_reason"])
	}
	if wrote["exit_code"] != -1 {
		t.Fatalf("exit_code=%v, want -1", wrote["exit_code"])
	}
}

func TestPeerExec_HandlerMissingCommand(t *testing.T) {
	peerExecTestEnv(t)
	var wrote map[string]interface{}
	svc := &AgentService{}
	svc.handleAgentPeerExecRequest(map[string]interface{}{
		"type":             "peer.exec.request",
		"request_id":       "req-empty-cmd",
		"source_device_id": "device-a",
		"command":          "   ",
	}, func(p interface{}) error {
		wrote = p.(map[string]interface{})
		return nil
	})
	if wrote["fail_reason"] != peerExecFailBadRequest {
		t.Fatalf("fail_reason=%v, want bad_request", wrote["fail_reason"])
	}
}

func TestPeerExec_HandlerMissingRequestIDIsSilentlyDropped(t *testing.T) {
	peerExecTestEnv(t)
	called := false
	svc := &AgentService{}
	svc.handleAgentPeerExecRequest(map[string]interface{}{
		"type":             "peer.exec.request",
		"source_device_id": "device-a",
		"command":          "echo hi",
	}, func(p interface{}) error {
		called = true
		return nil
	})
	if called {
		t.Fatal("missing request_id must not produce an uncorrelatable reply")
	}
}

func TestClampPeerExecTimeoutMs(t *testing.T) {
	cases := map[int]int{
		0:       peerExecTimeoutDefaultMs,
		-5:      peerExecTimeoutDefaultMs,
		500:     peerExecTimeoutMinMs,
		1000:    1000,
		42_000:  42_000,
		900_000: peerExecTimeoutMaxMs,
		600_000: 600_000,
		600_001: 600_000,
	}
	for in, want := range cases {
		if got := clampPeerExecTimeoutMs(in); got != want {
			t.Fatalf("clampPeerExecTimeoutMs(%d)=%d, want %d", in, got, want)
		}
	}
}

func TestPeerExecEnv_MinimalAllowlist(t *testing.T) {
	// 污染进程 env：LLM 网关凭据类键绝不允许流入 peer 子进程环境。
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "sk-leak-me-not")
	t.Setenv("ANTHROPIC_BASE_URL", "https://leak.example.com")
	t.Setenv("OPENAI_API_KEY", "sk-openai-leak")
	t.Setenv("ALIANG_FORWARDED_TOKEN", "leak")
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMPDIR", "/tmp")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LANG", "en_US.UTF-8")

	env := agentPeerExecEnv()
	allowed := map[string]bool{"PATH": true, "HOME": true, "TERM": true, "LANG": true, "TMPDIR": true}
	seen := map[string]bool{}
	for _, entry := range env {
		idx := strings.IndexByte(entry, '=')
		if idx <= 0 {
			t.Fatalf("malformed env entry %q", entry)
		}
		key := entry[:idx]
		if !allowed[key] {
			t.Fatalf("env key %q outside minimal allowlist (full env inheritance leaks LLM credentials)", key)
		}
		if seen[key] {
			t.Fatalf("duplicate env key %q", key)
		}
		seen[key] = true
		if strings.Contains(entry, "leak") {
			t.Fatalf("leaked value in env entry %q", entry)
		}
	}
	for key := range allowed {
		if !seen[key] {
			t.Fatalf("missing allowlisted env key %q", key)
		}
	}
}

func TestPeerExecEnv_CommandEnvHasNoSecrets(t *testing.T) {
	if peerExecShellSemanticsSupported(t) {
		return
	}
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "sk-leak-me-not")
	t.Setenv("ANTHROPIC_BASE_URL", "https://leak.example.com")

	exitCode, stdout, stderr, _, _, failReason :=
		runPeerExecCommand("env | sort", t.TempDir(), 10*time.Second)
	if exitCode != 0 || failReason != "" {
		t.Fatalf("exit=%d fail=%q stderr=%q", exitCode, failReason, stderr)
	}
	if strings.Contains(stdout, "ANTHROPIC") {
		t.Fatalf("peer child env leaked ANTHROPIC credentials:\n%s", stdout)
	}
	if !strings.Contains(stdout, "HOME=") || !strings.Contains(stdout, "PATH=") {
		t.Fatalf("peer child env missing minimal keys:\n%s", stdout)
	}
	// 行数封顶断言：最小集恒 5 键（env 输出可能带 shell 自加的 _/SHLVL/PWD，
	// 用「无第三方大写键泛滥」的宽松上界防回归到全量继承）。
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) > 12 {
		t.Fatalf("peer child env has %d lines, minimal set should stay tiny:\n%s", len(lines), stdout)
	}
}

func TestPeerExecShellCommand_UsesTrustedShell(t *testing.T) {
	shellPath, args, err := agentPeerShellCommand("echo hi")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if !isTrustedAgentShellPath(shellPath) {
		t.Fatalf("shell %q is not in a trusted location", shellPath)
	}
	if len(args) == 0 {
		t.Fatal("shell args empty")
	}
	if !strings.Contains(strings.Join(args, " "), "echo hi") {
		t.Fatalf("command not passed to shell: %v", args)
	}
}

// skipPeerExecPOSIX 对 Windows 跳过依赖 POSIX shell 语义的用例（跨平台测试
// 名避开 GOOS 尾段，防隐式跳过陷阱——见 memory「Go 隐式 GOOS 后缀测试陷阱」）。
// peerExecShellSemanticsSupported 报告当前平台是否具备这些用例依赖的 POSIX
// shell 语义（echo/yes|head/exit N/env）。Windows 走 powershell -Command，
// 语义不同，跳过；测试名不带 GOOS 尾段，避免「改名即静默跳过」陷阱。
func peerExecShellSemanticsSupported(t *testing.T) bool {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Log("skip: POSIX shell semantics unavailable on windows")
		return true
	}
	return false
}

func TestPeerExec_BackgroundChildHoldingPipe_TimeoutReleasesSlot(t *testing.T) {
	// 对抗审查 blocker 1：nohup 式后台孙进程继承 stdout 管道（nohup 在管道上
	// 不重定向）——裸 CommandContext 的 Wait 会阻塞在管道 EOF，超时 kill 只杀
	// 直接子进程，串行槽永久 busy。组杀+WaitDelay 必须让本次执行按时返回，
	// 且随后槽位立即可复用。
	if peerExecShellSemanticsSupported(t) {
		return
	}
	peerExecTestEnv(t)
	svc := &AgentService{}
	svc.ai = newAgentAIManager()

	var mu sync.Mutex
	var replies []map[string]interface{}
	writeJSON := func(p interface{}) error {
		mu.Lock()
		defer mu.Unlock()
		replies = append(replies, p.(map[string]interface{}))
		return nil
	}

	started := time.Now()
	svc.handleAgentPeerExecRequest(map[string]interface{}{
		"type":             "peer.exec.request",
		"request_id":       "req-nohup-1",
		"source_device_id": "device-a",
		"command":          "nohup sleep 30 &",
		"timeout_ms":       float64(1000),
	}, writeJSON)
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Fatalf("hanging pipes held the handler for %v (group kill + WaitDelay must cap it)", elapsed)
	}
	mu.Lock()
	first := replies[len(replies)-1]
	mu.Unlock()
	if first["fail_reason"] != peerExecFailTimeout {
		t.Fatalf("fail_reason = %v, want timeout", first["fail_reason"])
	}

	// 槽位已释放：紧接着的正常命令必须真的执行（exit 0），不得回 busy。
	svc.handleAgentPeerExecRequest(map[string]interface{}{
		"type":             "peer.exec.request",
		"request_id":       "req-nohup-2",
		"source_device_id": "device-a",
		"command":          "echo again",
		"timeout_ms":       float64(10000),
	}, writeJSON)
	mu.Lock()
	second := replies[len(replies)-1]
	mu.Unlock()
	if second["exit_code"] != 0 {
		t.Fatalf("second run exit_code = %v (slot leaked as busy?): %v", second["exit_code"], second["fail_reason"])
	}
}

func TestPeerExec_SignaledOutcome(t *testing.T) {
	// should-fix 10/11：非超时窗口被信号杀 → fail_reason=signaled（不得留空
	// 违背文件头契约）；判定以 ProcessState 为第一判据，不被 ctx 窗口带偏。
	if peerExecShellSemanticsSupported(t) {
		return
	}
	exitCode, _, _, _, _, failReason :=
		runPeerExecCommand("kill -TERM $$", t.TempDir(), 10*time.Second)
	if failReason != peerExecFailSignaled {
		t.Fatalf("fail_reason = %q, want signaled", failReason)
	}
	if exitCode != -1 {
		t.Fatalf("exit_code = %d, want -1", exitCode)
	}
}

func TestPeerExec_NestedShellNaturalExitIsRealCode(t *testing.T) {
	// 对抗审查任务 1b：kill 标志引入后的「不误报」回归——命令自然退出码不得被
	// 当成超时。嵌套 `sh -c "exit 7"`（外层可信 shell 再起一层 sh）在 ctx 给足
	// 时必须回真实 exit_code=7 且无 fail_reason。
	if peerExecShellSemanticsSupported(t) {
		return
	}
	exitCode, _, _, _, _, failReason :=
		runPeerExecCommand(`sh -c "exit 7"`, t.TempDir(), 10*time.Second)
	if exitCode != 7 {
		t.Fatalf("exit=%d, want real code 7 (not -1)", exitCode)
	}
	if failReason != "" {
		t.Fatalf("fail_reason=%q, natural termination must not carry one", failReason)
	}
}

func TestPeerExec_ClassifyKillFlagIsFirstCriterion(t *testing.T) {
	// 对抗审查任务 1b 单元级：kill 标志是第一判据。用真实 ExitError（外部信号
	// 杀，Signaled() 为 true）+ 标志置位，验证分类走 timeout 而不是 signaled；
	// 标志未置位时同一 runErr 必须如实回 signaled（第二判据才轮到 Signaled）。
	// Windows 上 Signaled() 恒 false，自家 TerminateProcess 只能靠本标志识别
	// ——此用例锁死「标志优先于 Signaled」的顺序。
	if peerExecShellSemanticsSupported(t) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runErr := exec.Command("sh", "-c", "kill -TERM $$").Run()
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) || exitErr.ProcessState == nil {
		t.Fatalf("expected ExitError from signaled child, got %v", runErr)
	}

	flag := new(atomic.Bool)
	flag.Store(true)
	code, reason := classifyPeerExecOutcome(runErr, ctx, flag)
	if code != -1 || reason != peerExecFailTimeout {
		t.Fatalf("kill-flag set: exit=%d fail=%q, want -1/timeout (flag must precede Signaled)", code, reason)
	}

	code, reason = classifyPeerExecOutcome(runErr, ctx, new(atomic.Bool))
	if code != -1 || reason != peerExecFailSignaled {
		t.Fatalf("kill-flag unset: exit=%d fail=%q, want -1/signaled", code, reason)
	}

	// 零值安全：nil 标志不得 panic（老调用面兼容）。
	code, reason = classifyPeerExecOutcome(runErr, ctx, nil)
	if code != -1 || reason != peerExecFailSignaled {
		t.Fatalf("nil flag: exit=%d fail=%q, want -1/signaled", code, reason)
	}
}

func TestPeerExec_ClassifyNaturalExitSurvivesExpiredDeadline(t *testing.T) {
	// kill 标志引入后的第二个「不误报」：命令恰在超时瞬间自然退出（标志未置位
	// ——Cancel 只在进程未退出时才被触发）而 ctx 已到期，真实退出码必须原样
	// 保留，不得被 ctx.Err() 兜底吞成 timeout。
	if peerExecShellSemanticsSupported(t) {
		return
	}
	runErr := exec.Command("sh", "-c", "exit 7").Run()
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("expected ExitError exit=7, got %v", runErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond) // 确保已到期
	if ctx.Err() == nil {
		t.Fatal("test setup: deadline did not elapse")
	}
	code, reason := classifyPeerExecOutcome(runErr, ctx, new(atomic.Bool))
	if code != 7 || reason != "" {
		t.Fatalf("exit=%d fail=%q, want real code 7 with no fail_reason at expired deadline", code, reason)
	}
}

func TestPeerExecJournalAudit_CoversEarlyExits(t *testing.T) {
	// should-fix 13：busy / workdir_missing 早退路径也要落审计行。
	peerExecTestEnv(t)
	svc := &AgentService{}
	svc.ai = newAgentAIManager()

	agentPeerExecMu.Lock()
	svc.handleAgentPeerExecRequest(map[string]interface{}{
		"type": "peer.exec.request", "request_id": "r-busy",
		"source_device_id": "device-a", "command": "echo x",
	}, func(p interface{}) error { return nil })
	agentPeerExecMu.Unlock()

	svc.handleAgentPeerExecRequest(map[string]interface{}{
		"type": "peer.exec.request", "request_id": "r-wd",
		"source_device_id": "device-a", "command": "echo x",
		"workdir": t.TempDir() + "/missing",
	}, func(p interface{}) error { return nil })

	raw, err := os.ReadFile(agentCoreJournalDir() + "/" + agentPeerExecJournalSession + ".jsonl")
	if err != nil {
		t.Fatalf("audit journal not written: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "outcome=busy") || !strings.Contains(text, "fail_reason=busy") {
		t.Fatalf("busy path not audited:\n%s", text)
	}
	if !strings.Contains(text, "outcome=workdir_missing") {
		t.Fatalf("workdir_missing path not audited:\n%s", text)
	}
}
