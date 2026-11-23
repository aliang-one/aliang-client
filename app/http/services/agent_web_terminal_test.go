package services

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

// newTestVaultAt 是后续任务复用的标准测试夹具:同一 path 上的多个实例
// 通过文件共享状态,模拟「重启重载」。经 WithPath 构造,从不触碰开发机
// 真实状态文件。minAttemptInterval=0 跳过尝试节流。
// frozen 锚定非零时刻:Verify 的 lastAttempt 用 IsZero 当「未尝试过」哨兵,
// 零值时钟会让真实尝试也被当成零点(仅测试现象;生产 now=time.Now 恒非零)。
func newTestVaultAt(t *testing.T, path string) *webTerminalVault {
	t.Helper()
	p := path
	v := newWebTerminalVaultWithPath(func() (string, error) { return p, nil })
	v.frozen = time.Now()
	v.now = func() time.Time { return v.frozen }
	v.minAttemptInterval = 0
	return v
}

func TestWebTerminalEnabledDefaultsFalseAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-terminal.json")
	v := newTestVaultAt(t, path)
	if v.Enabled() {
		t.Fatal("默认必须关闭(零值=false):全网默认开 shell 是红线")
	}
	v.SetEnabled(true)
	if !v.Enabled() {
		t.Fatal("SetEnabled(true) 后应开启")
	}
	v2 := newTestVaultAt(t, path)
	if !v2.Enabled() {
		t.Fatal("重启(重新加载)后应保持开启")
	}
	v2.SetEnabled(false)
	if v3 := newTestVaultAt(t, path); v3.Enabled() {
		t.Fatal("关闭后重载应保持关闭")
	}
}

func TestWebTerminalSetupRejectsBadFormAndDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-terminal.json")
	v := newTestVaultAt(t, path)
	if err := v.Setup("12a4"); err == nil {
		t.Fatal("非 4 位数字应拒")
	}
	if err := v.Setup("12345"); err == nil {
		t.Fatal("超长应拒")
	}
	if err := v.Setup("1234"); err != nil {
		t.Fatalf("合法 setup 应过: %v", err)
	}
	if err := v.Setup("5678"); err != errWebTerminalAlreadySet {
		t.Fatalf("重复 setup 应 errWebTerminalAlreadySet, got %v", err)
	}
	if v.Status()["pin_set"] != true {
		t.Fatal("setup 后 pin_set 应为 true")
	}
}

func TestWebTerminalChangeRequiresOldPin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-terminal.json")
	v := newTestVaultAt(t, path)
	_ = v.Setup("1234")
	if err := v.Change("9999", "5678"); err != errWebTerminalBadPin {
		t.Fatalf("错误旧 PIN 应 errWebTerminalBadPin, got %v", err)
	}
	if err := v.Change("1234", "abc"); err != errWebTerminalBadPinForm {
		t.Fatalf("新 PIN 非法格式应 errWebTerminalBadPinForm, got %v", err)
	}
	if err := v.Change("1234", "5678"); err != nil {
		t.Fatalf("change 应过: %v", err)
	}
	if _, _, _, err := v.Verify("5678"); err != nil {
		t.Fatalf("新 PIN 应通过 Verify: %v", err)
	}
	if _, _, _, err := v.Verify("1234"); err == nil {
		t.Fatal("旧 PIN 应失效")
	}
	// 上方 Verify 往返已是主证;此处 AlreadySet 仅廉价双保险(顺带钉死「change 不清 PIN」):
	if err := v.Setup("9090"); err != errWebTerminalAlreadySet {
		t.Fatal("change 后 PIN 仍处已设置态")
	}
}

func TestWebTerminalResetClearsToUnset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-terminal.json")
	v := newTestVaultAt(t, path)
	_ = v.Setup("1234")
	v.Reset()
	if v.Status()["pin_set"] != false {
		t.Fatal("reset 后应回到未设置态")
	}
	if err := v.Setup("4321"); err != nil {
		t.Fatalf("reset 后可重新 setup: %v", err)
	}
}

func TestWebTerminalStatusShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-terminal.json")
	v := newTestVaultAt(t, path)
	st := v.Status()
	if st["enabled"] != false || st["pin_set"] != false || st["locked"] != false || st["lock_remaining_seconds"] != 0 {
		t.Fatalf("未设置态 Status 形状不符: %v", st)
	}
	_ = v.Setup("1234")
	if v.Status()["pin_set"] != true {
		t.Fatal("setup 后 pin_set 应 true")
	}
}

func TestWebTerminalLockoutLadder(t *testing.T) {
	cases := []struct {
		fails int
		want  time.Duration
	}{
		{0, 0}, {1, 0}, {4, 0}, {5, time.Minute}, {6, 5 * time.Minute}, {7, 5 * time.Minute},
		{8, 30 * time.Minute}, {9, 30 * time.Minute}, {10, time.Hour},
		{14, time.Hour}, {15, 24 * time.Hour}, {16, 24 * time.Hour},
	}
	for _, c := range cases {
		if got := webTerminalLockoutFor(c.fails); got != c.want {
			t.Fatalf("failCount=%d: got %v want %v", c.fails, got, c.want)
		}
	}
}

func TestWebTerminalVerifyLadderAndRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-terminal.json")
	v := newTestVaultAt(t, path)
	_ = v.Setup("1234")
	// 连错 4 次:不锁,剩 1 次机会(5-已失败次数)
	for i := 0; i < 4; i++ {
		v.frozen = v.frozen.Add(time.Second)
		_, lockRem, left, err := v.Verify("0000")
		if err != errWebTerminalBadPin || lockRem != 0 || left != 4-i {
			t.Fatalf("第%d次错: err=%v lockRem=%d left=%d", i+1, err, lockRem, left)
		}
	}
	// 第 5 次:恰好触发锁档→本次即 locked+剩余秒(契约:触发档的失败先计数再判档)
	v.frozen = v.frozen.Add(time.Second)
	_, lockRem, _, err := v.Verify("0000")
	if err != errWebTerminalLocked || lockRem != 60 {
		t.Fatalf("第5次错应锁 60s: err=%v lockRem=%d", err, lockRem)
	}
	// 锁定中正确 PIN 也拒且不消耗
	v.frozen = v.frozen.Add(time.Second)
	if _, lockRem, _, err := v.Verify("1234"); err != errWebTerminalLocked || lockRem <= 0 {
		t.Fatal("锁定中正确 PIN 也拒且不消耗")
	}
	// 过锁期后正确 PIN 恢复,计数清零
	v.frozen = v.frozen.Add(2 * time.Minute)
	token, lockRem, _, err := v.Verify("1234")
	if err != nil || token == "" || lockRem != 0 {
		t.Fatalf("过锁期应恢复: token=%q err=%v", token, err)
	}
	if st := v.Status(); st["locked"] != false {
		t.Fatal("成功后 locked 应 false")
	}
}

func TestWebTerminalVerifyTooFast(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-terminal.json")
	v := newTestVaultAt(t, path)
	v.minAttemptInterval = time.Second
	_ = v.Setup("1234")
	_, _, _, _ = v.Verify("0000")
	v.frozen = v.frozen.Add(100 * time.Millisecond)
	if _, _, _, err := v.Verify("1234"); err != errWebTerminalTooFast {
		t.Fatalf("1s 内第二次尝试应 errWebTerminalTooFast, got %v", err)
	}
	v.frozen = v.frozen.Add(2 * time.Second)
	if _, _, _, err := v.Verify("1234"); err != nil {
		t.Fatalf("过间隔应放行: %v", err)
	}
}

// 质量评审补强:锁定计数跨实例(重启)存续——状态文件存在的意义
func TestWebTerminalLockoutSurvivesReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-terminal.json")
	v := newTestVaultAt(t, path)
	_ = v.Setup("1234")
	for i := 0; i < 5; i++ {
		v.frozen = v.frozen.Add(time.Second)
		_, _, _, _ = v.Verify("0000")
	}
	if v.Status()["locked"] != true {
		t.Fatal("5 次错应已锁")
	}
	// 重启:同路径新实例,锁定状态必须还在
	v2 := newTestVaultAt(t, path)
	v2.frozen = v.frozen
	if v2.Status()["locked"] != true {
		t.Fatal("重启后锁定状态应存续(计数持久化)")
	}
	if _, lockRem, _, err := v2.Verify("1234"); err != errWebTerminalLocked || lockRem <= 0 {
		t.Fatal("重启后 Verify 仍应被锁")
	}
}

func TestWebTerminalTokenSlideAndExpire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-terminal.json")
	v := newTestVaultAt(t, path)
	v.tokenIdleTTL = time.Minute
	_ = v.Setup("1234")
	token, _, _, err := v.Verify("1234")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !v.ValidateToken(token) {
		t.Fatal("新令牌应有效")
	}
	// 活动续期:每 30s 一次活动,3 分钟后仍有效
	for i := 0; i < 6; i++ {
		v.frozen = v.frozen.Add(30 * time.Second)
		if !v.ValidateToken(token) {
			t.Fatalf("滑动续期后 %d 秒应仍有效", 30*(i+1))
		}
	}
	// 停止活动:超 TTL 失效(惰性过期:不等 sweep,读时即删)
	v.frozen = v.frozen.Add(2 * time.Minute)
	if v.ValidateToken(token) {
		t.Fatal("超 TTL 应失效")
	}
}

func TestWebTerminalSweepExpiresAndLocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-terminal.json")
	v := newTestVaultAt(t, path)
	v.tokenIdleTTL = time.Minute
	killed := false
	v.onLock = func() { killed = true }
	_ = v.Setup("1234")
	token, _, _, _ := v.Verify("1234")
	v.frozen = v.frozen.Add(2 * time.Minute)
	if !v.sweepOnce() {
		t.Fatal("有过期令牌时 sweepOnce 应 true")
	}
	if v.ValidateToken(token) {
		t.Fatal("sweep 后令牌应全灭")
	}
	if !killed {
		t.Fatal("sweep 触发 Lock 应联动 onLock(closeAll)")
	}
	if v.sweepOnce() {
		t.Fatal("无过期令牌时 sweepOnce 应 false")
	}
}

func TestWebTerminalResetAndDisableInvalidateTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-terminal.json")
	v := newTestVaultAt(t, path)
	_ = v.Setup("1234")
	token, _, _, _ := v.Verify("1234")
	v.Reset()
	if v.ValidateToken(token) {
		t.Fatal("reset 后令牌应失效")
	}
	_ = v.Setup("1234")
	token, _, _, _ = v.Verify("1234")
	v.SetEnabled(false)
	if v.ValidateToken(token) {
		t.Fatal("总开关关应作废令牌")
	}
	// 空令牌守卫钉子(质量评审):空串不得经查表路径,直接拒绝。
	if v.ValidateToken("") {
		t.Fatal("空令牌必须直接拒绝")
	}
}

// —— Task 5:DispatchTerminalFrame(接线后引擎分派口)——

// newDispatchTestService 构造仅含 terminal/webTerminal 两翼的最小 AgentService。
// fake 复用 agent_terminal_attach_test.go 的 fakeTerminalSpawner;webTerminal 走
// 临时路径(测试纪律:不触碰开发机真实状态文件;DispatchTerminalFrame 的
// enabled 闸由入参闭包给出,测试中不实际消费 vault)。
func newDispatchTestService(t *testing.T) (*AgentService, *fakeTerminalSpawner) {
	t.Helper()
	mgr := newAgentTerminalManager()
	spawner := &fakeTerminalSpawner{}
	mgr.startProcess = spawner.start
	return &AgentService{
		terminal: mgr,
		webTerminal: newWebTerminalVaultWithPath(func() (string, error) {
			return filepath.Join(t.TempDir(), "web-terminal.json"), nil
		}),
	}, spawner
}

func TestDispatchTerminalFrameGateMatrix(t *testing.T) {
	dir := t.TempDir()
	setAgentAuthorizedExecutionDirectoriesCache([]string{dir})
	t.Cleanup(func() { setAgentAuthorizedExecutionDirectoriesCache(nil) })

	svc, spawner := newDispatchTestService(t)
	coll, write := newPayloadCollector()
	deny := func() bool { return false }
	allow := func() bool { return true }

	// create/input/resize 在 deny 门下:各回一帧 disabled 错误,不碰引擎。
	for _, typ := range []string{models.AgentEventTerminalCreate, models.AgentEventTerminalInput, models.AgentEventTerminalResize} {
		svc.DispatchTerminalFrame(map[string]interface{}{"type": typ, "session_id": "s1"}, write, nil, deny)
	}
	errs := coll.ofTypes(models.AgentEventTerminalError)
	if len(errs) != 3 {
		t.Fatalf("deny 门应各回一帧 error, got %d: %v", len(errs), errs)
	}
	for _, e := range errs {
		if !strings.Contains(fmt.Sprint(e["error"]), "remote terminal is disabled") {
			t.Fatalf("deny 错误帧语义不符: %v", e["error"])
		}
	}
	if got := spawner.spawned(); got != 0 {
		t.Fatalf("deny 下 create 不得起 shell, spawned=%d", got)
	}

	// close 恒放行:关闭自身永远合法(会话不存在也回 exit 回执)。
	svc.DispatchTerminalFrame(map[string]interface{}{"type": models.AgentEventTerminalClose, "session_id": "s1"}, write, nil, deny)
	if frames := coll.ofTypes(models.AgentEventTerminalExit); len(frames) != 1 {
		t.Fatalf("deny 下 close 仍应执行(exit 回执), got %d", len(frames))
	}

	// 未知类型:unsupported 错误帧(allow 也不该到达引擎)。
	svc.DispatchTerminalFrame(map[string]interface{}{"type": "bogus.type", "session_id": "s1"}, write, nil, allow)
	errs = coll.ofTypes(models.AgentEventTerminalError)
	if len(errs) != 4 {
		t.Fatalf("bogus 类型应回 unsupported error, got %d", len(errs))
	}
	if !strings.Contains(fmt.Sprint(errs[3]["error"]), "unsupported terminal frame type") {
		t.Fatalf("bogus 错误帧语义不符: %v", errs[3]["error"])
	}
	if got := spawner.spawned(); got != 0 {
		t.Fatalf("bogus 类型不得起 shell, spawned=%d", got)
	}
}

func TestDispatchTerminalFrameCreateAsyncOrdering(t *testing.T) {
	dir := t.TempDir()
	setAgentAuthorizedExecutionDirectoriesCache([]string{dir})
	t.Cleanup(func() { setAgentAuthorizedExecutionDirectoriesCache(nil) })

	svc, spawner := newDispatchTestService(t)
	// fake 进程必须长活(同 TestWriteWaitsForInFlightCreate 的教训):默认
	// wait() 立即返回会让「输入竞速 create」退化成「对已死会话输入」。
	blockExit := make(chan struct{})
	spawner.blockExit = blockExit
	t.Cleanup(func() { close(blockExit) })
	coll, write := newPayloadCollector()
	allow := func() bool { return true }

	svc.DispatchTerminalFrame(map[string]interface{}{
		"type": models.AgentEventTerminalCreate, "session_id": "s1", "shell": "", "cwd": dir, "rows": 24, "cols": 80,
	}, write, nil, allow)
	// 紧随其后的 input:inline 走 write,由 beginCreate 的同步登记等待在途
	// create,而不是报 not found——这就是 DispatchTerminalFrame 要保住的契约。
	svc.DispatchTerminalFrame(map[string]interface{}{
		"type": models.AgentEventTerminalInput, "session_id": "s1", "data": "ls\n",
	}, write, nil, allow)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if spawner.spawned() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if spawner.spawned() == 0 {
		t.Fatal("create 应被异步执行(fake starter 未被调)")
	}
	if created := coll.ofTypes(models.AgentEventTerminalCreated); len(created) != 1 {
		t.Fatalf("create 应回 terminal.created, got %d", len(created))
	}
	for _, e := range coll.ofTypes(models.AgentEventTerminalError) {
		if strings.Contains(fmt.Sprint(e["error"]), "not found") {
			t.Fatalf("紧随 create 的 input 不得报 not found(beginCreate 等待契约破): %v", e["error"])
		}
	}
}

// ——— Task 7:WebTerminalRunCommand 授权矩阵 ———

// newRunCommandTestService 命令矩阵夹具:vault 走临时文件路径(已开启+PIN
// 1234),terminal 用真 manager(killall 腿走 closeAll);ai=nil → 审计 no-op,
// 测试零 journal 落盘。
func newRunCommandTestService(t *testing.T) *AgentService {
	t.Helper()
	v := newTestVaultAt(t, filepath.Join(t.TempDir(), "web-terminal.json"))
	v.SetEnabled(true)
	if err := v.Setup("1234"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	return &AgentService{terminal: newAgentTerminalManager(), webTerminal: v}
}

func TestWebTerminalRunCommandAuthMatrix(t *testing.T) {
	svc := newRunCommandTestService(t)
	v := svc.webTerminal
	valid, _, _, err := v.Verify("1234")
	if err != nil {
		t.Fatalf("铸令牌: %v", err)
	}
	reasonOf := func(resp map[string]interface{}) string { return fmt.Sprint(resp["reason"]) }

	// 表序即执行序:lock 会 Lock() 清空全部令牌,故「killall 有效令牌」等
	// 依赖令牌存活的用例必须排在 lock 之前。
	cases := []struct {
		name     string
		action   string
		token    string
		loopback bool
		enabled  *bool
		wantOK   bool
		want     string
	}{
		{"change 坏令牌拒", "change", "bogus", false, nil, false, "bad_token"},
		{"killall 非 loopback 空令牌拒", "killall", "", false, nil, false, "bad_token"},
		{"killall 非 loopback 有效令牌过", "killall", valid, false, nil, true, ""},
		{"killall loopback 空令牌过", "killall", "", true, nil, true, ""},
		{"lock 空令牌非 loopback 过(安全方向)", "lock", "", false, nil, true, ""},
		{"lock 坏令牌非 loopback 拒", "lock", "bogus", false, nil, false, "bad_token"},
		{"gates 缺 enabled 拒", "gates", "", true, nil, false, "bad_request"},
		{"未知动作拒", "bogus.action", "", true, nil, false, "unknown_action"},
	}
	for _, tc := range cases {
		resp := svc.WebTerminalRunCommand(tc.action, "1234", "1234", "1234", tc.enabled, tc.token, tc.loopback)
		if tc.wantOK {
			if resp["ok"] != true {
				t.Fatalf("%s: 应 ok=true, got %v (reason=%s)", tc.name, resp["ok"], reasonOf(resp))
			}
			continue
		}
		if resp["ok"] != false {
			t.Fatalf("%s: 应 ok=false, got %v", tc.name, resp["ok"])
		}
		if got := reasonOf(resp); got != tc.want {
			t.Fatalf("%s: reason=%q want %q", tc.name, got, tc.want)
		}
	}

	// verify 快乐路径(夹具节流=0,重复 Verify 不触 too_fast):成功回真令牌。
	resp := svc.WebTerminalRunCommand("verify", "1234", "", "", nil, "", true)
	if resp["ok"] != true {
		t.Fatalf("verify 快乐路径应 ok=true, got %v (reason=%s)", resp["ok"], reasonOf(resp))
	}
	if tok, _ := resp["token"].(string); tok == "" {
		t.Fatal("verify 成功应携带非空 token")
	}
}

// TestWebTerminalSweepSparesBoxWhileValidTokenRemains 浏览器刷新常态:旧令牌
// 过期时新令牌仍活跃(持续帧复验滑动续期)——只删过期项,不得关箱连带杀活终端
// (2026-11-23 sweepOnce 误杀修复的回归钉)。
func TestWebTerminalSweepSparesBoxWhileValidTokenRemains(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-terminal.json")
	v := newTestVaultAt(t, path)
	v.tokenIdleTTL = time.Minute
	killed := false
	v.onLock = func() { killed = true }
	_ = v.Setup("1234")
	oldToken, _, _, _ := v.Verify("1234") // 刷新前的旧令牌
	v.frozen = v.frozen.Add(50 * time.Second)
	newToken, _, _, _ := v.Verify("1234") // 刷新后换发的新令牌
	// 新令牌持续活跃(每帧复验=滑动续期),把闲置锚点推到 110s
	for v.frozen.Before(v.frozen.Add(0)) || v.frozen.Unix() < time.Unix(0, 0).Unix() {
		break
	}
	v.frozen = v.frozen.Add(60 * time.Second)
	if !v.ValidateToken(newToken) {
		t.Fatal("活跃新令牌应验真并续期")
	}
	v.frozen = v.frozen.Add(40 * time.Second) // 旧令牌 age≈150s>60s;新令牌闲置 40s<60s
	if v.sweepOnce() {
		t.Fatal("仍有有效令牌时 sweepOnce 不得触发关箱")
	}
	if killed {
		t.Fatal("未关箱不得联动 onLock(closeAll)")
	}
	if !v.ValidateToken(newToken) {
		t.Fatal("活跃新令牌必须幸存")
	}
	if v.ValidateToken(oldToken) {
		t.Fatal("过期旧令牌必须已被回收")
	}
}
