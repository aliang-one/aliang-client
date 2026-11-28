//go:build !windows

package agentcore

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBackgroundRegistry 是宿主登记处的测试替身，记录内核回调序列。
type fakeBackgroundRegistry struct {
	handles []*fakeBackgroundHandle
}

// fakeBackgroundHandle 模拟宿主侧句柄：生产内核对它的访问跨三条 goroutine
// （os/exec 拷贝线程 Write、等待 goroutine Finished/SetCancel(nil)、Execute
// 返回后的测试断言线程），接口注释因此要求实现方并发安全——测试替身自己也
// 必须守同一纪律，否则 -race 红的是替身不是生产面。所有字段读写一律入锁，
// 断言统一经 Text()/State() 取锁内快照，禁直接摸字段。
type fakeBackgroundHandle struct {
	mu          sync.Mutex
	id          string
	buf         strings.Builder
	cancel      func()
	running     bool
	pid         int
	outcome     ShellOutcome
	detail      string
	finished    bool
	finishCalls int
}

// bgHandleState 是句柄状态的锁内快照。
type bgHandleState struct {
	cancel      func()
	running     bool
	pid         int
	outcome     ShellOutcome
	detail      string
	finished    bool
	finishCalls int
}

func (f *fakeBackgroundRegistry) Begin(command string) (BackgroundShellHandle, string) {
	h := &fakeBackgroundHandle{id: fmt.Sprintf("bg-%d", len(f.handles)+1)}
	f.handles = append(f.handles, h)
	return h, h.id
}
func (h *fakeBackgroundHandle) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.buf.Write(p)
}
func (h *fakeBackgroundHandle) SetCancel(fn func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cancel = fn
}
func (h *fakeBackgroundHandle) MarkRunning(pid int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.running, h.pid = true, pid
}
func (h *fakeBackgroundHandle) Finished(o ShellOutcome, d string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.finished, h.outcome, h.detail = true, o, d
	h.finishCalls++
}

// Text 返回已采集输出的锁内快照。
func (h *fakeBackgroundHandle) Text() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.buf.String()
}

// State 返回句柄状态的锁内快照。
func (h *fakeBackgroundHandle) State() bgHandleState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return bgHandleState{
		cancel:      h.cancel,
		running:     h.running,
		pid:         h.pid,
		outcome:     h.outcome,
		detail:      h.detail,
		finished:    h.finished,
		finishCalls: h.finishCalls,
	}
}

// allowAllShellPolicy 测试用全放行策略（exec 真实 shell）。
type allowAllShellPolicy struct{}

func (allowAllShellPolicy) Resolve(kind string) (string, []string, error) {
	return "/bin/bash", []string{"-c"}, nil
}
func (allowAllShellPolicy) Allow(string) (bool, string) { return true, "" }

// missingShellPolicy Resolve 返回不存在的可执行路径，构造 Start 失败。
type missingShellPolicy struct{}

func (missingShellPolicy) Resolve(kind string) (string, []string, error) {
	return "/nonexistent-shell", []string{"-c"}, nil
}
func (missingShellPolicy) Allow(string) (bool, string) { return true, "" }

func newBackgroundTestEnv(reg BackgroundShellRegistry) *ToolEnv {
	return &ToolEnv{
		ProjectPath:      ".",
		BackgroundShells: reg,
		ShellPolicy:      allowAllShellPolicy{},
	}
}

// waitFor 轮询条件至多 d，间隔 20ms（后台路径的时序断言统一入口）。
func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func TestBackgroundShellLaunchReturnsImmediately(t *testing.T) {
	reg := &fakeBackgroundRegistry{}
	env := newBackgroundTestEnv(reg)

	start := time.Now()
	res := (&bashTool{}).Execute(context.Background(), map[string]interface{}{
		"command":           "echo bg-hello",
		"run_in_background": true,
	}, env)
	elapsed := time.Since(start)
	if res.IsError {
		t.Fatalf("后台启动不应失败: %s", res.Content)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("Execute 应立即返回而不等命令结束，实测耗时 %s", elapsed)
	}
	if !strings.Contains(res.Content, "shell_id=bg-1") {
		t.Fatalf("返回应携带 shell_id=bg-1，得到: %s", res.Content)
	}
	h := reg.handles[0]
	if !waitFor(2*time.Second, func() bool { return strings.Contains(h.Text(), "bg-hello") }) {
		t.Fatalf("输出应写入登记处缓冲，实际 buf=%q", h.Text())
	}
	if !waitFor(2*time.Second, func() bool { return h.State().finished }) {
		t.Fatal("进程结束后应调 Finished 收尾")
	}
	s := h.State()
	if s.outcome != ShellCompleted {
		t.Fatalf("outcome=%q, want completed", s.outcome)
	}
	if !strings.Contains(s.detail, "退出码 0") {
		t.Fatalf("detail 应含退出码，得到: %s", s.detail)
	}
	if s.finishCalls != 1 {
		t.Fatalf("Finished 契约恰好一次，实测 %d 次", s.finishCalls)
	}
	if !s.running || s.pid <= 0 {
		t.Fatalf("应 MarkRunning 并携带正数 PID，running=%v pid=%d", s.running, s.pid)
	}
}

func TestBackgroundShellSurvivesCtxCancel(t *testing.T) {
	reg := &fakeBackgroundRegistry{}
	// 核心语义：run ctx 100ms 就超时，命令 0.5s 后才产出——后台任务必须
	// 不随 run ctx 取消而终止（run 停止/dev server 继续跑）。
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	env := newBackgroundTestEnv(reg)
	res := (&bashTool{}).Execute(ctx, map[string]interface{}{
		"command":           "sleep 0.5; echo done-late",
		"run_in_background": true,
	}, env)
	if res.IsError {
		t.Fatalf("后台启动不应失败: %s", res.Content)
	}
	if !waitFor(2*time.Second, func() bool { return ctx.Err() != nil }) {
		t.Fatal("前置：run ctx 应已超时取消")
	}
	h := reg.handles[0]
	if !waitFor(2*time.Second, func() bool { return strings.Contains(h.Text(), "done-late") }) {
		t.Fatalf("run ctx 取消后后台任务应继续跑完，buf=%q", h.Text())
	}
	if !waitFor(2*time.Second, func() bool { return h.State().finished }) {
		t.Fatal("后台任务应最终正常收尾")
	}
	s := h.State()
	if s.outcome != ShellCompleted {
		t.Fatalf("outcome=%q, want completed", s.outcome)
	}
	if s.finishCalls != 1 {
		t.Fatalf("Finished 契约恰好一次，实测 %d 次", s.finishCalls)
	}
}

func TestBackgroundShellKill(t *testing.T) {
	reg := &fakeBackgroundRegistry{}
	env := newBackgroundTestEnv(reg)

	res := (&bashTool{}).Execute(context.Background(), map[string]interface{}{
		"command":           "sleep 30",
		"run_in_background": true,
	}, env)
	if res.IsError {
		t.Fatalf("后台启动不应失败: %s", res.Content)
	}
	h := reg.handles[0]
	if !waitFor(time.Second, func() bool { return h.State().cancel != nil }) {
		t.Fatal("应已装填终止闭包（SetCancel）")
	}
	if kill := h.State().cancel; kill != nil {
		kill()
	}
	if !waitFor(2*time.Second, func() bool { return h.State().finished }) {
		t.Fatal("调用终止闭包后进程应被终止并收尾")
	}
	s := h.State()
	if s.outcome != ShellCancelled {
		t.Fatalf("outcome=%q, want cancelled", s.outcome)
	}
	if !strings.Contains(s.detail, "KillShell") {
		t.Fatalf("detail 应说明被 KillShell 终止，得到: %s", s.detail)
	}
	if s.finishCalls != 1 {
		t.Fatalf("Finished 契约恰好一次，实测 %d 次", s.finishCalls)
	}
}

func TestBackgroundShellOrphanGrandchildCompletes(t *testing.T) {
	reg := &fakeBackgroundRegistry{}
	env := newBackgroundTestEnv(reg)

	// `sleep 5 &`：主 shell 立即退出并被 reap，sleep 继承输出管道继续握——
	// watchCtx 退场后靠 WaitDelay（3s）强断 Wait，这正是后台语义的常态形态。
	res := (&bashTool{}).Execute(context.Background(), map[string]interface{}{
		"command":           "sleep 5 &",
		"run_in_background": true,
	}, env)
	if res.IsError {
		t.Fatalf("后台启动不应失败: %s", res.Content)
	}
	h := reg.handles[0]
	if !waitFor(6*time.Second, func() bool { return h.State().finished }) {
		t.Fatal("WaitDelay 到期后应收尾")
	}
	s := h.State()
	if s.outcome != ShellCompleted {
		t.Fatalf("outcome=%q, want completed", s.outcome)
	}
	if !strings.Contains(s.detail, "后台进程仍在运行") {
		t.Fatalf("detail 应说明后台进程仍在运行，得到: %s", s.detail)
	}
	if s.finishCalls != 1 {
		t.Fatalf("Finished 契约恰好一次，实测 %d 次", s.finishCalls)
	}
	// 终态后收尾：主 shell 已 reap， KillShell 走直接组杀闭包终止孙进程——
	// 不硬断言进程表（孙进程死亡靠 kill 系统调用语义与 -race 不炸收敛）。
	if kill := s.cancel; kill != nil {
		kill()
	}
	// once 保证终态后的收尾动作不会二次 Finished。
	if s2 := h.State(); s2.finishCalls != 1 {
		t.Fatalf("终态后收尾不得二次 Finished，实测 %d 次", s2.finishCalls)
	}
}

func TestBackgroundShellStartFailure(t *testing.T) {
	reg := &fakeBackgroundRegistry{}
	env := &ToolEnv{
		ProjectPath:      ".",
		BackgroundShells: reg,
		ShellPolicy:      missingShellPolicy{},
	}

	res := (&bashTool{}).Execute(context.Background(), map[string]interface{}{
		"command":           "echo hi",
		"run_in_background": true,
	}, env)
	if !res.IsError {
		t.Fatalf("Start 失败应报错，实际成功: %s", res.Content)
	}
	if !strings.Contains(res.Content, "启动失败") {
		t.Fatalf("报错应含「启动失败」，得到: %s", res.Content)
	}
	// Begin 先于 Start：登记处已有记录，终态必须收口（恰好一次 failed）。
	h := reg.handles[0]
	s := h.State()
	if s.outcome != ShellFailed {
		t.Fatalf("outcome=%q, want failed", s.outcome)
	}
	if s.finishCalls != 1 {
		t.Fatalf("Finished 契约恰好一次，实测 %d 次", s.finishCalls)
	}
}

func TestBackgroundShellNoRegistry(t *testing.T) {
	// BackgroundShells=nil：fail closed，明确报错而不是静默前台执行。
	env := &ToolEnv{ProjectPath: ".", ShellPolicy: allowAllShellPolicy{}}

	res := (&bashTool{}).Execute(context.Background(), map[string]interface{}{
		"command":           "echo should-not-run",
		"run_in_background": true,
	}, env)
	if !res.IsError {
		t.Fatalf("无登记处时应报错，实际成功: %s", res.Content)
	}
	if !strings.Contains(res.Content, "不支持后台") {
		t.Fatalf("报错应说明不支持后台命令，得到: %s", res.Content)
	}
}

func TestBackgroundShellLifetimeCap(t *testing.T) {
	reg := &fakeBackgroundRegistry{}
	// 寿命帽用时解析（stream_idle.go 同款先例），但覆盖值有 1min 下限钳制
	// （防手滑把 dev server 配成秒级寿命）。1500ms 会被钳到 1min——本用例
	// 守的就是「显式覆盖也被压回下限」这条语义：若有人删掉钳制，1.5s 的帽
	// 会在 sleep 2 结束前把它杀成 timed_out，本用例即红。TimedOut 分支的
	// 直达覆盖在 TestBackgroundShellLifetimeTimeoutDirect（直传 lifetime
	// 绕过钳制）。
	t.Setenv("ALIANG_AGENTCORE_BG_SHELL_MAX_LIFETIME", "1500ms")

	env := newBackgroundTestEnv(reg)
	res := (&bashTool{}).Execute(context.Background(), map[string]interface{}{
		"command":           "sleep 2",
		"run_in_background": true,
	}, env)
	if res.IsError {
		t.Fatalf("后台启动不应失败: %s", res.Content)
	}
	h := reg.handles[0]
	if !waitFor(5*time.Second, func() bool { return h.State().finished }) {
		t.Fatal("命令应在寿命帽内自然跑完")
	}
	s := h.State()
	if s.outcome != ShellCompleted {
		t.Fatalf("1min 下限应钳掉 1500ms 覆盖（outcome=%q, want completed）", s.outcome)
	}
	if !strings.Contains(s.detail, "退出码 0") {
		t.Fatalf("detail 应为自然退出，得到: %s", s.detail)
	}
	if s.finishCalls != 1 {
		t.Fatalf("Finished 契约恰好一次，实测 %d 次", s.finishCalls)
	}
}

func TestBackgroundShellLifetimeTimeoutDirect(t *testing.T) {
	reg := &fakeBackgroundRegistry{}
	// 直传 lifetime 绕过 1min 下限钳制：TimedOut 分支的直达覆盖（生产路径
	// 的钳制语义由 TestBackgroundShellLifetimeCap 守）。
	res := runShellBackground("/bin/bash", []string{"-c", "sleep 30"}, t.TempDir(), nil, reg, "sleep 30", 1500*time.Millisecond)
	if res.IsError {
		t.Fatalf("后台启动不应失败: %s", res.Content)
	}
	if !strings.Contains(res.Content, "shell_id=bg-1") {
		t.Fatalf("返回应携带 shell_id=bg-1，得到: %s", res.Content)
	}
	h := reg.handles[0]
	if !waitFor(3*time.Second, func() bool { return h.State().finished }) {
		t.Fatal("寿命帽（1.5s）到期后应收尾")
	}
	s := h.State()
	if s.outcome != ShellTimedOut {
		t.Fatalf("outcome=%q, want timed_out", s.outcome)
	}
	if !strings.Contains(s.detail, "寿命上限") {
		t.Fatalf("detail 应说明寿命上限到期，得到: %s", s.detail)
	}
	if s.finishCalls != 1 {
		t.Fatalf("Finished 契约恰好一次，实测 %d 次", s.finishCalls)
	}
}
