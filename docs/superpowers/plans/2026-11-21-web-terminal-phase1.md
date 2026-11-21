# 网页终端阶段 1(单窗终端+保险箱)实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 56431 仪表盘新增「网页终端」:保险箱(4 位 PIN+锁定阶梯)门控的单窗口交互式真终端,复用既有 agentTerminalManager 引擎,一行 PTY 代码不改。

**Architecture:** 保险箱与令牌全部住在 user agent runtime(56433,loopback-only 红线),core(56431)只做闸门②(cookie∥loopback)+proxy 透传;WS 首帧强制 `terminal.auth`,每帧复验令牌;终端帧经新抽的 `DispatchTerminalFrame` 统一分派进既有引擎(手机远端腿与网页腿共用)。前端 Vue3 composable 单例 + xterm.js 单窗格。

**Tech Stack:** Go(gorilla/websocket 已有)/ creack+ConPTY(已有,不动)/ Vue3.5+Vite5+@xterm/xterm^6+@xterm/addon-fit(新装)/ vitest。

**Spec:** `docs/superpowers/specs/2026-11-21-web-terminal-design.md`(阶段 1 = spec §11 第一期)

---

## 对 spec 的五处实现细化(不改变闸门链语义,评审已确认 spec 其余部分)

1. **vault 持久化=独立 state 文件** `runtimepath.UserStateDir()/agent/web-terminal.json`,不进 `persisted.Device`——Device 会被平台同步整体覆盖(`syncRuntimeDeviceStatusLocked`),本地开关放那里有被冲掉的风险。
2. **审计=journal 行**(peerExecJournalAudit 同款 `coreJournal.appendTool`),不是「boot 扩 audit 表」——仓库已验证的审计机制就是 journal,无表可扩。session 名 `web-terminal`。
3. **管理 UI 单一表面**:总开关/PIN 管理/killall 全收在终端工作台内部(含未开启时的引导),不动设置页。
4. **令牌强制点=runtime 侧**(WS 首帧+每帧复验+命令头 `X-Web-Terminal-Token`);core 只做闸门②+透传——令牌内存态不跨进程,且 56433 本就 loopback-only。
5. **重连续屏**:`terminal.create` 带 `attach:true`(manager 语义:活会话→resize+replay+created{resumed};墓碑→历史 replay;都没有→落回新建),前端重连恒带 attach。

## 执行前置

- **worktree**:开工先建 worktree(本仓惯例=兄弟目录,参照 superpowers:using-git-worktrees;vitest 须 cd 到 app/website 跑——历史坑)。
- **提交纪律(CLAUDE.md 铁律)**:任务内**不设 git commit 步骤**;全部实现+测试绿后,Task 17 统一出提交方案,**经用户明示同意才提交**。
- 测试命令一律在**仓库根**跑 Go;vitest 在 `app/website` 下跑。

## 文件结构

```
新建 app/http/services/agent_web_terminal.go        vault(状态文件/PIN/锁定阶梯/令牌/sweep)+ DispatchTerminalFrame
新建 app/http/services/agent_web_terminal_test.go    vault+dispatch 测试
改   app/http/services/agent_service.go              接线:webTerminal 字段+onLock+sweep+公开包装
改   app/http/services/agent_remote_ws.go            远端腿 4 个 case 收敛为 DispatchTerminalFrame
新建 app/http/handlers/agent_handler_web_terminal.go  state/command 两 HTTP 端点 + WS 端点
新建 app/http/handlers/agent_handler_web_terminal_test.go 门矩阵+WS auth 测试
改   app/http/routes/routes.go                       core 侧三注册
改   app/agentruntime/server.go                      runtime 侧三注册
改   app/website/package.json                        +@xterm/xterm +@xterm/addon-fit
改   app/website/src/services/agentApi.js            +2 API
新建 app/website/src/composables/useWebTerminal.js    vault 客户端状态机(+test)
新建 app/website/src/composables/useTerminalSocket.js 每会话 WS 客户端(+test)
新建 app/website/src/components/terminal/PinGuard.vue PIN 设置/解锁/锁定倒计时
新建 app/website/src/components/terminal/TerminalPane.vue xterm 单窗格
新建 app/website/src/components/TerminalWorkspace.vue 覆盖层工作台
改   app/website/src/composables/useNavigation.js    +terminal 词
改   app/website/src/App.vue                         +TerminalWorkspace 挂载
改   app/website/src/components/DashboardPage.vue    两处入口按钮
改   app/website/src/i18n/zh.js / en.js              +term_/vault_ 词条
```

---

### Task 1: vault 骨架——状态文件+Enabled 开关+持久化

**Files:**
- Create: `app/http/services/agent_web_terminal.go`
- Test: `app/http/services/agent_web_terminal_test.go`

- [ ] **Step 1: 写失败测试**

```go
package services

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestVault(t *testing.T) *webTerminalVault {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "web-terminal.json")
	v := newWebTerminalVault()
	v.statePath = func() (string, error) { return path, nil }
	v.now = func() time.Time { return v.frozen }
	v.minAttemptInterval = 0
	return v
}

func TestWebTerminalEnabledDefaultsFalseAndPersists(t *testing.T) {
	v := newTestVault(t)
	if v.Enabled() {
		t.Fatal("默认必须关闭(零值=false):全网默认开 shell 是红线")
	}
	v.SetEnabled(true)
	if !v.Enabled() {
		t.Fatal("SetEnabled(true) 后应开启")
	}
	// 新实例从同一文件加载:持久化生效
	v2 := newTestVault(t)
	if !v2.Enabled() {
		t.Fatal("重启(重新加载)后应保持开启")
	}
	v2.SetEnabled(false)
	if v3 := newTestVault(t); v3.Enabled() {
		t.Fatal("关闭后重载应保持关闭")
	}
}
```

测试里用到 `time`/`frozen` 字段——本任务先把骨架建全(见 Step 3),测试才可编译;预期失败点在 `SetEnabled/Enabled` 未实现或未持久化。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestWebTerminalEnabledDefaultsFalse -v`
Expected: FAIL(编译错误或断言失败)

- [ ] **Step 3: 实现 vault 骨架**

新建 `app/http/services/agent_web_terminal.go`:

```go
package services

// 网页终端保险箱(spec 2026-11-21-web-terminal-design)。真身在 user agent
// runtime(56433,loopback-only 红线):PIN/锁定阶梯/解锁令牌全在本进程,
// core 侧只做闸门②+proxy 透传。持久化=独立 state 文件,不进 persisted.Device
// (Device 被平台同步整体覆盖,本地开关放那里会被冲掉)。

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/internal/runtimepath"
)

const (
	webTerminalStateFilename    = "web-terminal.json"
	webTerminalPinLength        = 4
	webTerminalTokenIdleTTL     = 15 * time.Minute
	webTerminalSweepInterval    = 30 * time.Second
	webTerminalMinAttemptGap    = time.Second
	webTerminalTokenBytes       = 32
)

// 锁定阶梯(spec §5):5次1min/6-7次5min/8-9次30min/10-14次1h/≥15次24h。
func webTerminalLockoutFor(failCount int) time.Duration {
	switch {
	case failCount < 5:
		return 0
	case failCount == 5:
		return time.Minute
	case failCount <= 7:
		return 5 * time.Minute
	case failCount <= 9:
		return 30 * time.Minute
	case failCount < 15:
		return time.Hour
	default:
		return 24 * time.Hour
	}
}

var (
	errWebTerminalNotSetUp    = errors.New("web terminal pin is not set up")
	errWebTerminalAlreadySet  = errors.New("web terminal pin is already set")
	errWebTerminalBadPin      = errors.New("incorrect pin")
	errWebTerminalLocked      = errors.New("pin verification is locked")
	errWebTerminalTooFast     = errors.New("verification attempts too frequent")
	errWebTerminalBadToken    = errors.New("invalid or expired web terminal token")
	errWebTerminalBadPinForm  = errors.New("pin must be exactly 4 digits")
)

type webTerminalState struct {
	Enabled      bool   `json:"enabled"`
	PinSalt      string `json:"pin_salt,omitempty"`
	PinHash      string `json:"pin_hash,omitempty"`
	FailCount    int    `json:"fail_count,omitempty"`
	LockoutUntil string `json:"lockout_until,omitempty"` // RFC3339;空=未锁
}

type webTerminalToken struct{ lastSeen time.Time }

type webTerminalVault struct {
	mu     sync.Mutex
	state  webTerminalState
	tokens map[string]*webTerminalToken

	lastAttempt time.Time
	onLock      func() // AgentService 注入:Lock 时 closeAll 终端

	// 可注入(测试):时钟/路径/节拍
	statePath         func() (string, error)
	now               func() time.Time
	tokenIdleTTL      time.Duration
	minAttemptInterval time.Duration
}

func webTerminalStatePath() (string, error) {
	stateDir, err := runtimepath.UserStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, "agent", webTerminalStateFilename), nil
}

func newWebTerminalVault() *webTerminalVault {
	v := &webTerminalVault{
		tokens:             make(map[string]*webTerminalToken),
		statePath:          webTerminalStatePath,
		now:                time.Now,
		tokenIdleTTL:       webTerminalTokenIdleTTL,
		minAttemptInterval: webTerminalMinAttemptGap,
	}
	v.load()
	return v
}

func (v *webTerminalVault) load() {
	path, err := v.statePath()
	if err != nil {
		logger.Warn(fmt.Sprintf("[WEB-TERMINAL] state_path_failed error=%v", err))
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return // 首次使用:全零值(Enabled=false,PIN 未设置)
	}
	_ = json.Unmarshal(raw, &v.state)
}

func (v *webTerminalVault) persistLocked() {
	path, err := v.statePath()
	if err != nil {
		logger.Warn(fmt.Sprintf("[WEB-TERMINAL] state_path_failed error=%v", err))
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		logger.Warn(fmt.Sprintf("[WEB-TERMINAL] state_mkdir_failed error=%v", err))
		return
	}
	raw, err := json.Marshal(v.state)
	if err != nil {
		return
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		logger.Warn(fmt.Sprintf("[WEB-TERMINAL] state_write_failed error=%v", err))
	}
}

func (v *webTerminalVault) Enabled() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.state.Enabled
}

func (v *webTerminalVault) SetEnabled(enabled bool) {
	v.mu.Lock()
	v.state.Enabled = enabled
	v.persistLocked()
	v.mu.Unlock()
	if !enabled {
		v.Lock()
	}
}

// Lock 作废全部令牌并(经 onLock)终止所有活终端。幂等。
func (v *webTerminalVault) Lock() {
	v.mu.Lock()
	had := len(v.tokens) > 0
	v.tokens = make(map[string]*webTerminalToken)
	v.mu.Unlock()
	if had || true { // 语义上 Lock 恒有效(关箱),onLock 恒调
		if v.onLock != nil {
			v.onLock()
		}
	}
}
```

注意 `newTestVault` 里引用的 `frozen` 字段:在骨架里补 `frozen time.Time`(测试时钟锚点,`now` 返回它,测试用 `v.frozen = v.frozen.Add(...)` 推进)。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services/ -run TestWebTerminalEnabledDefaultsFalse -v`
Expected: PASS

---

### Task 2: vault PIN 生命周期——Setup/Status/Change/Reset

**Files:**
- Modify: `app/http/services/agent_web_terminal.go`
- Test: `app/http/services/agent_web_terminal_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestWebTerminalSetupRejectsBadFormAndDuplicates(t *testing.T) {
	v := newTestVault(t)
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
	v := newTestVault(t)
	_ = v.Setup("1234")
	if _, _, _, err := v.Verify("9999"); err == nil {
		t.Fatal("错误旧 PIN 不应通过")
	}
	token, _, _, err := v.Verify("1234")
	if err != nil || token == "" {
		t.Fatalf("verify 应出令牌: %v", err)
	}
	if err := v.Change("1234", "5678"); err != nil {
		t.Fatalf("change 应过: %v", err)
	}
	if _, _, _, err := v.Verify("1234"); err == nil {
		t.Fatal("旧 PIN 应失效")
	}
	if _, _, _, err := v.Verify("5678"); err != nil {
		t.Fatalf("新 PIN 应通过: %v", err)
	}
}

func TestWebTerminalResetClearsToUnset(t *testing.T) {
	v := newTestVault(t)
	_ = v.Setup("1234")
	v.Reset()
	if v.Status()["pin_set"] != false {
		t.Fatal("reset 后应回到未设置态")
	}
	if err := v.Setup("4321"); err != nil {
		t.Fatalf("reset 后可重新 setup: %v", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run 'TestWebTerminalSetup|TestWebTerminalChange|TestWebTerminalReset' -v`
Expected: FAIL(未实现)

- [ ] **Step 3: 实现**

追加到 `agent_web_terminal.go`:

```go
func webTerminalRandomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf)
}

func webTerminalPinHash(salt, pin string) string {
	h := sha256.Sum256([]byte(salt + ":" + pin))
	return hex.EncodeToString(h[:])
}

func webTerminalPinValid(pin string) bool {
	if len(pin) != webTerminalPinLength {
		return false
	}
	for _, r := range pin {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (v *webTerminalVault) Setup(pin string) error {
	if !webTerminalPinValid(pin) {
		return errWebTerminalBadPinForm
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state.PinHash != "" {
		return errWebTerminalAlreadySet
	}
	salt := webTerminalRandomHex(16)
	v.state.PinSalt = salt
	v.state.PinHash = webTerminalPinHash(salt, pin)
	v.state.FailCount = 0
	v.state.LockoutUntil = ""
	v.persistLocked()
	return nil
}

func (v *webTerminalVault) Change(oldPin, newPin string) error {
	if !webTerminalPinValid(newPin) {
		return errWebTerminalBadPinForm
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state.PinHash == "" {
		return errWebTerminalNotSetUp
	}
	if subtle.ConstantTimeCompare(
		[]byte(webTerminalPinHash(v.state.PinSalt, oldPin)),
		[]byte(v.state.PinHash)) != 1 {
		return errWebTerminalBadPin
	}
	salt := webTerminalRandomHex(16)
	v.state.PinSalt = salt
	v.state.PinHash = webTerminalPinHash(salt, newPin)
	v.persistLocked()
	return nil
}

func (v *webTerminalVault) Reset() {
	v.mu.Lock()
	v.state.PinSalt = ""
	v.state.PinHash = ""
	v.state.FailCount = 0
	v.state.LockoutUntil = ""
	v.tokens = make(map[string]*webTerminalToken)
	v.persistLocked()
	v.mu.Unlock()
	if v.onLock != nil {
		v.onLock()
	}
}

func (v *webTerminalVault) lockoutRemainingLocked() int {
	if v.state.LockoutUntil == "" {
		return 0
	}
	until, err := time.Parse(time.RFC3339, v.state.LockoutUntil)
	if err != nil {
		return 0
	}
	d := until.Sub(v.now())
	if d <= 0 {
		v.state.LockoutUntil = ""
		return 0
	}
	return int(d.Round(time.Second).Seconds())
}

func (v *webTerminalVault) Status() map[string]interface{} {
	v.mu.Lock()
	defer v.mu.Unlock()
	remaining := v.lockoutRemainingLocked()
	return map[string]interface{}{
		"enabled":                v.state.Enabled,
		"pin_set":                v.state.PinHash != "",
		"locked":                 remaining > 0,
		"lock_remaining_seconds": remaining,
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services/ -run 'TestWebTerminalSetup|TestWebTerminalChange|TestWebTerminalReset' -v`
Expected: PASS

---

### Task 3: Verify+锁定阶梯+强制间隔

**Files:**
- Modify: `app/http/services/agent_web_terminal.go`
- Test: `app/http/services/agent_web_terminal_test.go`

- [ ] **Step 1: 写失败测试(阶梯表驱动)**

```go
func TestWebTerminalLockoutLadder(t *testing.T) {
	cases := []struct {
		fails int
		want  time.Duration
	}{
		{1, 0}, {4, 0}, {5, time.Minute}, {6, 5 * time.Minute}, {7, 5 * time.Minute},
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
	v := newTestVault(t)
	_ = v.Setup("1234")
	// 连错 4 次:不锁,剩 1 次机会
	for i := 0; i < 4; i++ {
		_, lockRem, left, err := v.Verify("0000")
		if err != errWebTerminalBadPin || lockRem != 0 || left != 4-i {
			t.Fatalf("第%d次错: err=%v lockRem=%d left=%d", i+1, err, lockRem, left)
		}
	}
	// 第 5 次:锁 1 分钟,锁定中尝试不再计数、不判 PIN
	v.frozen = v.frozen.Add(time.Second)
	_, lockRem, _, err := v.Verify("0000")
	if err != errWebTerminalLocked || lockRem != 60 {
		t.Fatalf("第5次错应锁 60s: err=%v lockRem=%d", err, lockRem)
	}
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
	v := newTestVault(t)
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
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run 'TestWebTerminalLockoutLadder|TestWebTerminalVerifyLadder|TestWebTerminalVerifyTooFast' -v`
Expected: FAIL

- [ ] **Step 3: 实现 Verify**

```go
// Verify 校验 PIN。返回:(令牌, 锁定剩余秒, 剩余尝试次数, 错误)。
// 锁定中早退与 too_fast 时不计失败次数(触发锁档的那次除外——见下方契约
// 注释,它先计数再判档);锁定中连正确 PIN 也拒(不消耗、不提前解锁)。
func (v *webTerminalVault) Verify(pin string) (string, int, int, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state.PinHash == "" {
		return "", 0, 0, errWebTerminalNotSetUp
	}
	if rem := v.lockoutRemainingLocked(); rem > 0 {
		return "", rem, 0, errWebTerminalLocked
	}
	now := v.now()
	if v.minAttemptInterval > 0 && now.Sub(v.lastAttempt) < v.minAttemptInterval && !v.lastAttempt.IsZero() {
		return "", 0, 0, errWebTerminalTooFast
	}
	v.lastAttempt = now
	if subtle.ConstantTimeCompare(
		[]byte(webTerminalPinHash(v.state.PinSalt, pin)),
		[]byte(v.state.PinHash)) != 1 {
		v.state.FailCount++
		// 契约(spec §11「连错 5 次 PIN 见锁定」):第 N 次失败**恰好触发锁档**
		// 时,本次即返回 locked+剩余秒(而非 bad_pin)——前端据此起倒计时;
		// 锁定档之间(如 11-14 次维持 1h)同理,重触档即回 locked。
		if d := webTerminalLockoutFor(v.state.FailCount); d > 0 {
			v.state.LockoutUntil = now.Add(d).Format(time.RFC3339)
			v.persistLocked()
			return "", int(d.Round(time.Second).Seconds()), 0, errWebTerminalLocked
		}
		v.persistLocked()
		left := 5 - v.state.FailCount
		if left < 0 {
			left = 0
		}
		return "", 0, left, errWebTerminalBadPin
	}
	v.state.FailCount = 0
	v.state.LockoutUntil = ""
	token := webTerminalRandomHex(webTerminalTokenBytes)
	if token == "" {
		return "", 0, 0, errors.New("token generation failed")
	}
	v.tokens[token] = &webTerminalToken{lastSeen: now}
	v.persistLocked()
	return token, 0, 0, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services/ -run 'TestWebTerminalLockoutLadder|TestWebTerminalVerifyLadder|TestWebTerminalVerifyTooFast' -v`
Expected: PASS

---

### Task 4: 解锁令牌+滑动 TTL+sweep+ValidateToken

**Files:**
- Modify: `app/http/services/agent_web_terminal.go`
- Test: `app/http/services/agent_web_terminal_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestWebTerminalTokenSlideAndExpire(t *testing.T) {
	v := newTestVault(t)
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
	// 停止活动:超 TTL 失效
	v.frozen = v.frozen.Add(2 * time.Minute)
	if v.ValidateToken(token) {
		t.Fatal("超 TTL 应失效")
	}
}

func TestWebTerminalSweepExpiresAndLocks(t *testing.T) {
	v := newTestVault(t)
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
	v := newTestVault(t)
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
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run 'TestWebTerminalToken|TestWebTerminalSweep|TestWebTerminalResetAndDisable' -v`
Expected: FAIL

- [ ] **Step 3: 实现**

```go
// ValidateToken 校验并续期(滑动 TTL):终端输入帧与 FS/命令请求都走这里,
// 活动即续期(spec §5③)。**惰性过期**:不等 30s sweep,超 TTL 的令牌在读时
// 即删即拒(否则 sweep 间隙有最长 30s 的过期令牌仍可用的宽限窗口——安全面)。
func (v *webTerminalVault) ValidateToken(token string) bool {
	if token == "" {
		return false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	t, ok := v.tokens[token]
	if !ok {
		return false
	}
	now := v.now()
	if now.Sub(t.lastSeen) > v.tokenIdleTTL {
		delete(v.tokens, token)
		return false
	}
	t.lastSeen = now
	return true
}

// sweepOnce 回收超时令牌;有回收即触发 Lock(关箱:终止活终端+作废余令牌)。
// 返回是否有回收。startSweep 以固定节拍循环调它。
func (v *webTerminalVault) sweepOnce() bool {
	v.mu.Lock()
	now := v.now()
	expired := false
	for token, t := range v.tokens {
		if now.Sub(t.lastSeen) > v.tokenIdleTTL {
			delete(v.tokens, token)
			expired = true
		}
	}
	v.mu.Unlock()
	if expired {
		v.Lock()
	}
	return expired
}

func (v *webTerminalVault) startSweep() {
	safeGo("web-terminal.sweep", func() {
		ticker := time.NewTicker(webTerminalSweepInterval)
		defer ticker.Stop()
		for range ticker.C {
			v.sweepOnce()
		}
	})
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services/ -run 'TestWebTerminalToken|TestWebTerminalSweep|TestWebTerminalResetAndDisable' -v`
Expected: PASS

---

### Task 5: AgentService 接线 + DispatchTerminalFrame 抽取(远端腿改造)

**Files:**
- Modify: `app/http/services/agent_service.go`(字段约 :153 区、构造约 :250 区)
- Modify: `app/http/services/agent_remote_ws.go:932-985`(4 个 case 收敛)
- Modify: `app/http/services/agent_web_terminal.go`(+DispatchTerminalFrame)
- Test: `app/http/services/agent_web_terminal_test.go`

- [ ] **Step 1: 写失败测试(dispatch 矩阵,夹具仿 agent_terminal_attach_test.go)**

```go
func newDispatchTestService(t *testing.T) (*AgentService, *[]string) {
	t.Helper()
	mgr := newAgentTerminalManager()
	var calls []string
	mgr.startProcess = func(sessionID, shell, cwd string, rows, cols int) (*agentTerminalSession, []io.Reader, error) {
		calls = append(calls, "start:"+sessionID)
		return newFakeTerminalSession(sessionID, rows, cols), nil, nil
	}
	return &AgentService{terminal: mgr, webTerminal: newWebTerminalVault()}, &calls
}
```

(`newFakeTerminalSession` 若 attach 测试里已有等价夹具则复用其构造;没有就以 attach/async_create 测试里的 session 构造为准复制最小版。)

```go
func TestDispatchTerminalFrameGateMatrix(t *testing.T) {
	svc, _ := newDispatchTestService(t)
	deny := func() bool { return false }
	allow := func() bool { return true }
	write := func(payload interface{}) error { return nil }

	for _, typ := range []string{models.AgentEventTerminalCreate, models.AgentEventTerminalInput, models.AgentEventTerminalResize} {
		svc.DispatchTerminalFrame(map[string]interface{}{"type": typ, "session_id": "s1"}, write, deny)
	}
	// close 不受门控
	svc.DispatchTerminalFrame(map[string]interface{}{"type": models.AgentEventTerminalClose, "session_id": "s1"}, write, deny)
	svc.DispatchTerminalFrame(map[string]interface{}{"type": "bogus.type"}, write, allow)
}

func TestDispatchTerminalFrameCreateAsyncOrdering(t *testing.T) {
	svc, calls := newDispatchTestService(t)
	// beginCreate 同步登记:input 紧随 create 应等待而非报错(async_create 语义)
	svc.DispatchTerminalFrame(map[string]interface{}{"type": models.AgentEventTerminalCreate, "session_id": "s1", "rows": 24, "cols": 80}, func(p interface{}) error { return nil }, func() bool { return true })
	svc.DispatchTerminalFrame(map[string]interface{}{"type": models.AgentEventTerminalInput, "session_id": "s1", "data": "ls\n"}, func(p interface{}) error { return nil }, func() bool { return true })
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(*calls) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(*calls) == 0 {
		t.Fatal("create 应被异步执行(fake starter 未被调)")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run 'TestDispatchTerminalFrame' -v`
Expected: FAIL(方法不存在)

- [ ] **Step 3: 实现 DispatchTerminalFrame(追加到 agent_web_terminal.go)**

```go
// DispatchTerminalFrame 是终端帧统一分派口:手机远端腿(agent_remote_ws.go)
// 与网页腿(HandleWebTerminalStream)共用,enabled 由两腿各给
// (remoteTerminalEnabled / WebTerminalEnabled)。create 离开调用方读循环
// (beginCreate 同步登记+safeGo 异步,紧随的 input 可等待);input/resize/
// close 保持 inline——输入顺序是契约。close 恒放行:关闭自身永远合法。
func (s *AgentService) DispatchTerminalFrame(msg map[string]interface{}, writeJSON agentTerminalWriter, enabled func() bool) {
	switch remoteString(msg, "type") {
	case models.AgentEventTerminalCreate:
		if !enabled() {
			_ = writeJSON(agentTerminalErrorPayload(remoteString(msg, "session_id"), errors.New("remote terminal is disabled for this device")))
			return
		}
		release := s.terminal.beginCreate(remoteString(msg, "session_id"))
		safeGo("terminal.create", func() {
			defer release()
			s.terminal.create(msg, writeJSON)
		})
	case models.AgentEventTerminalInput:
		if !enabled() {
			_ = writeJSON(agentTerminalErrorPayload(remoteString(msg, "session_id"), errors.New("remote terminal is disabled for this device")))
			return
		}
		s.terminal.write(msg, writeJSON)
	case models.AgentEventTerminalResize:
		if !enabled() {
			_ = writeJSON(agentTerminalErrorPayload(remoteString(msg, "session_id"), errors.New("remote terminal is disabled for this device")))
			return
		}
		s.terminal.resize(msg, writeJSON)
	case models.AgentEventTerminalClose:
		s.terminal.close(msg, writeJSON)
	default:
		_ = writeJSON(agentTerminalErrorPayload(remoteString(msg, "session_id"), fmt.Errorf("unsupported terminal frame type %q", remoteString(msg, "type"))))
	}
}
```

- [ ] **Step 4: AgentService 接线**

`agent_service.go`:字段区(`terminal *agentTerminalManager` :153 旁)加:

```go
	webTerminal          *webTerminalVault
```

构造区(`terminal: newAgentTerminalManager(),` :250 旁)加 `webTerminal: newWebTerminalVault(),`;构造完成后(函数体内,service 变量可用处)加:

```go
	svc.webTerminal.onLock = func() { svc.terminal.closeAll() }
	svc.webTerminal.startSweep()
```

(变量名以构造函数实际命名为准;若构造是返回字面量形式,把这三行放在返回前。)

公开包装追加到 agent_web_terminal.go:

```go
// ——— AgentService 公开面(handler 层用) ———

func (s *AgentService) WebTerminalEnabled() bool {
	if s.webTerminal == nil {
		return false
	}
	return s.webTerminal.Enabled()
}

func (s *AgentService) WebTerminalValidateToken(token string) bool {
	if s.webTerminal == nil {
		return false
	}
	return s.webTerminal.ValidateToken(token)
}

func (s *AgentService) WebTerminalStatus() map[string]interface{} {
	if s.webTerminal == nil {
		return map[string]interface{}{"enabled": false, "pin_set": false, "locked": false, "lock_remaining_seconds": 0}
	}
	return s.webTerminal.Status()
}
```

- [ ] **Step 5: 远端腿改造(agent_remote_ws.go:932-985)**

把 `AgentEventTerminalCreate/Input/Resize/Close` 四个 case 收敛为一个(case 群的 `setRemoteConnectionState(true,...)` 保留;`quotaResolved` case 原样不动——审批裁决恒须受理):

```go
	case models.AgentEventTerminalCreate, models.AgentEventTerminalInput,
		models.AgentEventTerminalResize, models.AgentEventTerminalClose:
		s.setRemoteConnectionState(true, "online", "")
		s.DispatchTerminalFrame(msg, writeJSON, s.remoteTerminalEnabled)
```

- [ ] **Step 6: 跑测试确认通过(含远端腿回归)**

Run: `go test ./app/http/services/ -run 'TestDispatchTerminalFrame|TestAgentTerminal' -v`
Expected: PASS(既有 attach/async_create/detach 测试全绿=远端腿语义无回归)

---

### Task 6: 审计(journal 行,peerExecJournalAudit 同款)

**Files:**
- Modify: `app/http/services/agent_web_terminal.go`

- [ ] **Step 1: 实现(纯增量,无独立测试——journal 落点在 Task 7/8 的 handler 测试里断言)**

```go
// webTerminalJournalAudit 落 kind=tool 审计行(peerExecJournalAudit 同款形态,
// session 名 web-terminal——磁盘留痕但不在仪表盘会话列表冒假卡)。nil-safe。
func (s *AgentService) webTerminalJournalAudit(action, summary string, isErr bool) {
	if s.ai == nil {
		return
	}
	s.ai.coreJournal.appendTool("web-terminal", "web_terminal."+action, summary, isErr, "")
}
```

审计落点(在 Task 7/8 的命令与 WS 处理里调用):
- `pin_setup`(含来源 loopback|cookie)/`pin_verify` 成败(失败带第 N 次计数)/`pin_change`/`pin_reset`(非 loopback 来源高亮标记 `[NON-LOOPBACK]`)/`lock`/`gates`/`killall`/`terminal_create`/`terminal_close`

---

### Task 7: HTTP 端点(state GET + command POST)

**Files:**
- Create: `app/http/handlers/agent_handler_web_terminal.go`
- Test: `app/http/handlers/agent_handler_web_terminal_test.go`

- [ ] **Step 1: 写失败测试(夹具镜像 agent_handler_ai_stream_gate_test.go)**

```go
package handlers

// 网页终端 HTTP 面:闸门②(cookie∥loopback)与 reset 的 loopback-only 门。
// 业务语义(阶梯/令牌)在 services 层已测;此处测门与分派形态。

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/middleware"
)

func webTerminalGateRequest(method, target string, body string, sessionCookie *http.Cookie) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Origin", "http://127.0.0.1:56431")
	req.Host = "127.0.0.1:56431"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if sessionCookie != nil {
		req.AddCookie(sessionCookie)
	}
	return req
}

func mintDashboardCookie(t *testing.T) *http.Cookie {
	t.Helper()
	mint := httptest.NewRecorder()
	mintReq := httptest.NewRequest(http.MethodPost, "/api/auth/bootstrap", nil)
	mintReq.RemoteAddr = "127.0.0.1:55555"
	if err := middleware.IssueDashboardSession(mint, mintReq); err != nil {
		t.Fatalf("签发 session: %v", err)
	}
	for _, c := range mint.Result().Cookies() {
		if c.Name == middleware.DashboardSessionCookieName {
			return c
		}
	}
	t.Fatal("未签发 session cookie")
	return nil
}

func TestWebTerminalStateRejectsHandshakeWithoutSession(t *testing.T) {
	h := NewAgentHandler(nil)
	rec := httptest.NewRecorder()
	h.HandleWebTerminalState(rec, webTerminalGateRequest(http.MethodGet, "/api/agent/web-terminal", "", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无登录态应 401, got %d", rec.Code)
	}
}

func TestWebTerminalCommandRejectsForgedCookie(t *testing.T) {
	h := NewAgentHandler(nil)
	forged := &http.Cookie{Name: middleware.DashboardSessionCookieName, Value: url.QueryEscape("forged")}
	rec := httptest.NewRecorder()
	h.HandleWebTerminalCommand(rec, webTerminalGateRequest(http.MethodPost, "/api/agent/web-terminal", `{"action":"verify","pin":"1234"}`, forged))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("伪造 cookie 应 401, got %d", rec.Code)
	}
}

func TestWebTerminalCommandResetRequiresLoopback(t *testing.T) {
	h := NewAgentHandler(nil)
	// 模拟 LAN 来源(非 loopback RemoteAddr)+ 有效 cookie:reset 必须在
	// core 腿就被 403(runtime 侧全是 loopback,只能在 core 拦)
	cookie := mintDashboardCookie(t)
	req := webTerminalGateRequest(http.MethodPost, "/api/agent/web-terminal", `{"action":"reset"}`, cookie)
	req.RemoteAddr = "192.168.1.50:55555"
	rec := httptest.NewRecorder()
	h.HandleWebTerminalCommand(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("LAN reset 应 403, got %d", rec.Code)
	}
}

func TestWebTerminalCommandRejectsWrongMethod(t *testing.T) {
	h := NewAgentHandler(nil)
	rec := httptest.NewRecorder()
	h.HandleWebTerminalState(rec, webTerminalGateRequest(http.MethodPost, "/api/agent/web-terminal", `{}`, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("应 405, got %d", rec.Code)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/handlers/ -run 'TestWebTerminal' -v`
Expected: FAIL(方法不存在)

- [ ] **Step 3: 实现 handler**

新建 `app/http/handlers/agent_handler_web_terminal.go`:

```go
package handlers

// 网页终端 HTTP 面(spec §6.1):GET state + POST command。真身在 user agent
// runtime(保险箱状态与令牌),core 侧闸门②+proxyIfNeeded 透传;reset 额外
// loopback-only(spec §2/§6.4,只能在 core 拦——runtime 侧全是 loopback)。

import (
	"encoding/json"
	"io"
	"net/http"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/middleware"
	"aliang.one/nursorgate/app/http/services"
)

// WebTerminalTokenHeader 解锁令牌随header走(plain HTTP proxy 全量转发,
// 见 copyAgentProxyHeaders);WS 腿走首帧 auth,不经header。
const WebTerminalTokenHeader = "X-Web-Terminal-Token"

func guardWebTerminalGate(w http.ResponseWriter, r *http.Request) bool {
	// 与 HandleAIStream 同款双闸:有效 dashboard session ∥ loopback。
	if !middleware.ValidateDashboardSession(r) && !middleware.IsLoopbackRequest(r) {
		middleware.RequireDashboardSession(w, r)
		return false
	}
	return true
}

// HandleWebTerminalState GET /api/agent/web-terminal → vault 状态快照。
func (h *AgentHandler) HandleWebTerminalState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !guardWebTerminalGate(w, r) {
		return
	}
	if h.proxyIfNeeded(w, r) {
		return
	}
	common.Success(w, h.service.WebTerminalStatus())
}

// HandleWebTerminalCommand POST /api/agent/web-terminal {action,...}。
// 业务失败走 Success 信封 {ok:false, reason, ...}(前端要结构化读锁定秒数/
// 剩余次数,Error 信封的 details 前端 request() 不透);真正的门(闸门②/reset
// loopback)才用 HTTP 错误码。
func (h *AgentHandler) HandleWebTerminalCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !guardWebTerminalGate(w, r) {
		return
	}
	var body struct {
		Action  string `json:"action"`
		Pin     string `json:"pin,omitempty"`
		OldPin  string `json:"old_pin,omitempty"`
		NewPin  string `json:"new_pin,omitempty"`
		Enabled *bool  `json:"enabled,omitempty"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	if err != nil || len(raw) == 0 {
		common.ErrorBadRequest(w, "Invalid request body", nil)
		return
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", nil)
		return
	}
	// reset 仅 loopback(core 腿拦截;runtime 侧天然 loopback)
	if body.Action == "reset" && !middleware.IsLoopbackRequest(r) {
		common.Error(w, common.CodeForbidden, "PIN reset is loopback only", nil)
		return
	}
	token := r.Header.Get(WebTerminalTokenHeader)
	if h.proxyIfNeeded(w, r) {
		return
	}
	resp := h.service.WebTerminalRunCommand(body.Action, body.Pin, body.OldPin, body.NewPin, body.Enabled, token, middleware.IsLoopbackRequest(r))
	common.Success(w, resp)
}
```

services 侧命令分发(追加到 agent_web_terminal.go):

```go
// WebTerminalRunCommand 执行 vault 命令。业务失败以 {ok:false, reason,...}
// 返回(HTTP 恒 200);门级失败才走 handler 的 HTTP 错误。审计四类落点在
// 各 case 内(spec §6.4)。
//
// 门语义注记(spec §6.4 字面要求 vs §11 首启流程的现实解,刻意如此勿"修"回):
// gates 不要求令牌/loopback——首次 LAN 开启时根本不存在已解锁令牌,要求即
// 死锁(§11 验收第 1 步依赖);lock 允许空令牌是安全方向(上锁只会收权);
// reset 的 loopback-only 在 handler 层拦(runtime 侧天然全 loopback,拦不住)。
func (s *AgentService) WebTerminalRunCommand(action, pin, oldPin, newPin string, enabled *bool, token string, loopback bool) map[string]interface{} {
	okPayload := func(extra map[string]interface{}) map[string]interface{} {
		out := map[string]interface{}{"ok": true}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	failPayload := func(reason string, extra map[string]interface{}) map[string]interface{} {
		out := map[string]interface{}{"ok": false, "reason": reason}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	origin := "cookie"
	if loopback {
		origin = "loopback"
	}
	switch action {
	case "state":
		return okPayload(map[string]interface{}{"status": s.WebTerminalStatus()})
	case "setup":
		if !s.WebTerminalEnabled() {
			return failPayload("disabled", nil)
		}
		err := s.webTerminal.Setup(pin)
		s.webTerminalJournalAudit("pin_setup", "origin="+origin, err != nil)
		if err == errWebTerminalAlreadySet {
			return failPayload("already_set", nil)
		}
		if err == errWebTerminalBadPinForm {
			return failPayload("bad_pin_form", nil)
		}
		if err != nil {
			return failPayload("error", nil)
		}
		return okPayload(nil)
	case "verify":
		if !s.WebTerminalEnabled() {
			return failPayload("disabled", nil)
		}
		newToken, lockRem, left, err := s.webTerminal.Verify(pin)
		if err == errWebTerminalLocked {
			s.webTerminalJournalAudit("pin_verify", "locked remaining="+itoa(lockRem), true)
			return failPayload("locked", map[string]interface{}{"lock_remaining_seconds": lockRem})
		}
		if err == errWebTerminalTooFast {
			return failPayload("too_fast", nil)
		}
		if err == errWebTerminalBadPin {
			s.webTerminalJournalAudit("pin_verify", fmt.Sprintf("failed attempts_left=%d", left), true)
			return failPayload("bad_pin", map[string]interface{}{"attempts_left": left})
		}
		if err == errWebTerminalNotSetUp {
			return failPayload("not_set_up", nil)
		}
		if err != nil {
			return failPayload("error", nil)
		}
		s.webTerminalJournalAudit("pin_verify", "ok origin="+origin, false)
		return okPayload(map[string]interface{}{"token": newToken})
	case "change":
		if !s.WebTerminalValidateToken(token) {
			return failPayload("bad_token", nil)
		}
		err := s.webTerminal.Change(oldPin, newPin)
		s.webTerminalJournalAudit("pin_change", "", err != nil)
		if err == errWebTerminalBadPinForm {
			return failPayload("bad_pin_form", nil)
		}
		if err == errWebTerminalBadPin {
			return failPayload("bad_old_pin", nil)
		}
		if err != nil {
			return failPayload("error", nil)
		}
		return okPayload(nil)
	case "reset":
		s.webTerminal.Reset()
		highlight := ""
		if !loopback {
			highlight = " [NON-LOOPBACK]"
		}
		s.webTerminalJournalAudit("pin_reset", "origin="+origin+highlight, false)
		return okPayload(nil)
	case "lock":
		if token != "" && !s.WebTerminalValidateToken(token) && !loopback {
			return failPayload("bad_token", nil)
		}
		s.webTerminal.Lock()
		s.webTerminalJournalAudit("lock", "origin="+origin, false)
		return okPayload(nil)
	case "gates":
		if enabled == nil {
			return failPayload("bad_request", nil)
		}
		s.webTerminal.SetEnabled(*enabled) // false→内部 Lock→onLock=closeAll(kill-switch)
		s.webTerminalJournalAudit("gates", fmt.Sprintf("enabled=%v origin=%s", *enabled, origin), false)
		return okPayload(map[string]interface{}{"status": s.WebTerminalStatus()})
	case "killall":
		if token == "" && !loopback {
			return failPayload("bad_token", nil)
		}
		if token != "" && !s.WebTerminalValidateToken(token) && !loopback {
			return failPayload("bad_token", nil)
		}
		s.terminal.closeAll()
		s.webTerminalJournalAudit("killall", "origin="+origin, false)
		return okPayload(nil)
	default:
		return failPayload("unknown_action", nil)
	}
}
```

(`itoa` 用 `strconv.Itoa`;import 相应补。)

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/handlers/ -run 'TestWebTerminal' -v`
Expected: PASS

---

### Task 8: WS 端点(HandleWebTerminalStream)

**Files:**
- Modify: `app/http/handlers/agent_handler_web_terminal.go`
- Test: `app/http/handlers/agent_handler_web_terminal_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestWebTerminalStreamGateMatrix(t *testing.T) {
	h := NewAgentHandler(nil)
	rec := httptest.NewRecorder()
	h.HandleWebTerminalStream(rec, webTerminalGateRequest(http.MethodGet, "/api/agent/web-terminal/stream", "", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无登录态 WS 握手应 401, got %d", rec.Code)
	}
	cookie := mintDashboardCookie(t)
	rec = httptest.NewRecorder()
	h.HandleWebTerminalStream(rec, webTerminalGateRequest(http.MethodGet, "/api/agent/web-terminal/stream", "", cookie))
	// 门过:非 runtime 角色→透传拨号失败(502/1011 形态);绝不能 401
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("持 session 握手不应 401, got %d", rec.Code)
	}
}

func TestWebTerminalStreamRejectsBadAuthFrame(t *testing.T) {
	t.Setenv(services.AgentRuntimeEnv, "1") // 强制 runtime 角色:auth 逻辑在本腿
	h := NewAgentHandler(nil)               // service=nil:auth 校验必须 nil-safe 拒绝
	srv := httptest.NewServer(http.HandlerFunc(h.HandleWebTerminalStream))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/agent/web-terminal/stream"
	// Origin 必须取 srv.URL(含随机端口)——同源闸是精确 host:port 比较,
	// 写死 56431 会对不上 httptest 的随机端口,直接被 403
	dialHeader := http.Header{"Origin": []string{srv.URL}}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, dialHeader)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.WriteJSON(map[string]interface{}{"type": "terminal.auth", "token": "bogus"})
	_, _, err = conn.ReadMessage()
	if err == nil {
		// nil-service 路径经 rejectBrowserWS 是 close 1011;真实 service 下坏令牌是 1008
		t.Fatal("坏令牌/无服务应被服务端断开,不应收到任何业务帧")
	}
}
```

(Dial 显式带 Origin 过同源闸;auth 失败断开在读侧表现为 close frame/error。)

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/handlers/ -run 'TestWebTerminalStream' -v`
Expected: FAIL

- [ ] **Step 3: 实现 WS handler**

```go
// HandleWebTerminalStream GET /api/agent/web-terminal/stream。
// core 角色:闸门②+同源闸+proxyAIStream TCP 透传(与聊天 WS 同管线)。
// runtime 角色:双闸→upgrade→首帧强制 terminal.auth(15s 内未过=close 1008)
// →读循环每帧复验令牌(闲置重锁后死连接不得继续驱动 shell;复验即滑动续期)
// →DispatchTerminalFrame(msg, write, WebTerminalEnabled)。每会话一条 WS,
// 前端重连带 attach:true 续屏(manager 语义)。
func (h *AgentHandler) HandleWebTerminalStream(w http.ResponseWriter, r *http.Request) {
	if !guardWebTerminalGate(w, r) {
		return
	}
	if !services.IsUserAgentRuntime() {
		proxyAIStream(w, r)
		return
	}
	// nil-service 防御(单测形态 NewAgentHandler(nil)):auth 校验前必须拦住,
	// 否则 nil receiver 解引用 panic(net/http 会 recover 但 1008 语义丢失)
	if h.service == nil {
		rejectBrowserWS(w, r, "web terminal unavailable")
		return
	}
	conn, err := aiStreamUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	// 入站只有小帧(auth/input/resize/close),1MiB 足够;勿照抄远端 4MiB
	// (那是为 replay 批量出站留的,出站方向与本限制无关)。
	conn.SetReadLimit(1 << 20)

	var writeMu sync.Mutex
	writeJSON := func(payload interface{}) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return conn.WriteJSON(payload)
	}

	// 首帧强制鉴权
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var authMsg map[string]interface{}
	if err := conn.ReadJSON(&authMsg); err != nil {
		return
	}
	authToken := ""
	if remoteFrameType(authMsg) == "terminal.auth" {
		authToken = terminalFrameString(authMsg, "token")
	}
	if !h.service.WebTerminalValidateToken(authToken) {
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "web terminal locked or token invalid"),
			time.Now().Add(2*time.Second))
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	for {
		var msg map[string]interface{}
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		if msg == nil {
			continue
		}
		if !h.service.WebTerminalValidateToken(authToken) {
			_ = writeJSON(agentTerminalErrorPayloadPublic("", errors.New("web terminal re-locked")))
			return
		}
		frameType := terminalFrameString(msg, "type")
		switch frameType {
		case "terminal.create":
			h.service.WebTerminalAuditTerminal("terminal_create", terminalFrameString(msg, "session_id"))
		case "terminal.close":
			h.service.WebTerminalAuditTerminal("terminal_close", terminalFrameString(msg, "session_id"))
		}
		h.service.DispatchTerminalFrame(msg, writeJSON, h.service.WebTerminalEnabled)
	}
}
```

handlers 包内小助手(agents 包的 `remoteString` 在 services,不导出——handlers 里自备两行版):

```go
func terminalFrameString(msg map[string]interface{}, key string) string {
	if msg == nil {
		return ""
	}
	v, _ := msg[key].(string)
	return v
}
```

services 侧补两个小出口:

```go
func agentTerminalErrorPayloadPublic(sessionID string, err error) map[string]interface{} {
	return agentTerminalErrorPayload(sessionID, err)
}

func (s *AgentService) WebTerminalAuditTerminal(action, sessionID string) {
	s.webTerminalJournalAudit(action, "session="+sessionID, false)
}
```

(handlers 测试的 nil-service 场景:auth 校验前有 `h.service == nil` 守卫(rejectBrowserWS),坏令牌路径在真实 service 下才走 1008;`WebTerminalValidateToken` 本身 nil-safe 指的是 service 非 nil 而 vault 字段为 nil 的形态。)

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/handlers/ -run 'TestWebTerminal' -v`
Expected: PASS

---

### Task 9: 双注册 + parity + 全量构建

**Files:**
- Modify: `app/http/routes/routes.go`(:324 旁)
- Modify: `app/agentruntime/server.go`(registerAgentRoutes 内)

- [ ] **Step 1: core 侧注册(routes.go:324 旁)**

```go
	// 网页终端(spec 2026-11-21-web-terminal-design):state/command/stream。
	// 真身在 user agent runtime(保险箱/令牌/终端管理器),core 侧闸门②+proxy
	// 透传。双注册另一处 agentruntime/server.go(缺任一侧=404 透传,老坑)。
	// 同路径双方法用方法前缀模式(同 /api/agent/ai/mcp/* 先例,防 ServeMux 覆盖)。
	register("GET /api/agent/web-terminal", h.Agent.HandleWebTerminalState, http.MethodGet)
	register("POST /api/agent/web-terminal", h.Agent.HandleWebTerminalCommand, http.MethodPost)
	register("/api/agent/web-terminal/stream", h.Agent.HandleWebTerminalStream, http.MethodGet)
```

- [ ] **Step 2: runtime 侧注册(agentruntime/server.go registerAgentRoutes 内,`/api/agent/ai/stream` 注册旁)**

```go
	// 网页终端(spec 2026-11-21-web-terminal-design):真身在此(保险箱/令牌/
	// 终端管理器住本 runtime),core 侧同名注册只做 proxy 透传。缺任一侧=404
	// (双注册教训,2026-10-21 liang-dev 实测)。
	mux.HandleFunc("GET /api/agent/web-terminal", agentHandler.HandleWebTerminalState)
	mux.HandleFunc("POST /api/agent/web-terminal", agentHandler.HandleWebTerminalCommand)
	mux.HandleFunc("/api/agent/web-terminal/stream", agentHandler.HandleWebTerminalStream)
```

(boot 日志词表 `registerAgentRoutes` 开头的 logger.Info 里补 `/api/agent/web-terminal*`。)

- [ ] **Step 3: 跑 parity 测试 + 全量构建**

Run: `go test ./app/agentruntime/ -run TestAgentRouteParity -v && go build ./... && go vet ./app/http/... ./app/agentruntime/...`
Expected: parity PASS;build/vet 零错误(parity 测试是双注册的自动护栏)

- [ ] **Step 4: Go 全量回归**

Run: `go test ./...`
Expected: 全绿(预存 flake 以仓内基线为准,见记忆 preexisting-race-flakes)

---

### Task 10: 前端依赖

- [ ] **Step 1:** `cd app/website && npm install @xterm/xterm@^6 @xterm/addon-fit`
- [ ] **Step 2:** `cd app/website && npm run build` — 确认构建不破

---

### Task 11: agentApi.js 扩展

**Files:**
- Modify: `app/website/src/services/agentApi.js`(文件尾追加)

- [ ] **Step 1: 实现**

```js
// ——— 网页终端(spec 2026-11-21-web-terminal-design)———

export async function getWebTerminalState() {
  return request('/api/agent/web-terminal', { method: 'GET' });
}

// token 由调用方(useWebTerminal)注入 header——plain HTTP proxy 全量转发。
// 业务失败不走 throw:{ok:false, reason,...} 结构化返回(前端要读锁定秒数)。
export async function webTerminalCommand(payload, token = '') {
  const headers = {};
  if (token) headers['X-Web-Terminal-Token'] = token;
  return request('/api/agent/web-terminal', {
    method: 'POST',
    body: JSON.stringify(payload),
    headers,
  });
}
```

- [ ] **Step 2:** `cd app/website && npm run build` — 确认无语法错误

---

### Task 12: useWebTerminal.js(vault 客户端状态机)

**Files:**
- Create: `app/website/src/composables/useWebTerminal.js`
- Test: `app/website/src/composables/useWebTerminal.test.js`

- [ ] **Step 1: 写失败测试**

```js
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));
const mockState = vi.fn();
const mockCommand = vi.fn();
vi.mock('../services/agentApi', () => ({
  getWebTerminalState: (...a) => mockState(...a),
  webTerminalCommand: (...a) => mockCommand(...a),
}));

import { resetForTests, useWebTerminal } from './useWebTerminal';

describe('useWebTerminal', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    resetForTests();
  });

  it('refresh 拉取状态', async () => {
    mockState.mockResolvedValue({ enabled: true, pin_set: true, locked: false, lock_remaining_seconds: 0 });
    const wt = useWebTerminal();
    await wt.refresh();
    expect(wt.status.value.enabled).toBe(true);
  });

  it('verify 成功存令牌,失败结构化透出', async () => {
    mockCommand.mockResolvedValueOnce({ ok: true, token: 't1' });
    const wt = useWebTerminal();
    const res = await wt.verify('1234');
    expect(res.ok).toBe(true);
    expect(wt.unlocked.value).toBe(true);
    expect(wt.token.value).toBe('t1');

    mockCommand.mockResolvedValueOnce({ ok: false, reason: 'locked', lock_remaining_seconds: 42 });
    const res2 = await wt.verify('9999');
    expect(res2.ok).toBe(false);
    expect(res2.lock_remaining_seconds).toBe(42);
    expect(wt.unlocked.value).toBe(true); // 失败不动既有令牌(重锁走服务端 sweep)
  });

  it('lock 清令牌', async () => {
    mockCommand.mockResolvedValue({ ok: true });
    mockState.mockResolvedValue({ enabled: true, pin_set: true, locked: false, lock_remaining_seconds: 0 });
    const wt = useWebTerminal();
    await wt.verify('1234');
    await wt.lock();
    expect(wt.unlocked.value).toBe(false);
    expect(mockCommand).toHaveBeenCalledWith({ action: 'lock' }, expect.any(String));
  });

  it('setEnabled false 走 gates 且清令牌', async () => {
    mockCommand.mockResolvedValue({ ok: true });
    mockState.mockResolvedValue({ enabled: false, pin_set: false, locked: false, lock_remaining_seconds: 0 });
    const wt = useWebTerminal();
    wt.token.value = 't';
    await wt.setEnabled(false);
    expect(mockCommand).toHaveBeenCalledWith({ action: 'gates', enabled: false }, 't');
    expect(wt.unlocked.value).toBe(false);
  });
});
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd app/website && npx vitest run src/composables/useWebTerminal.test.js`
Expected: FAIL(模块不存在)

- [ ] **Step 3: 实现**

```js
import { computed, ref } from 'vue';
import { getWebTerminalState, webTerminalCommand } from '../services/agentApi';

// 网页终端保险箱客户端状态机(模块级单例,同 useAgentStatus 形态)。
// 令牌只存内存(ref)——刷新即失,重新输 PIN;不落 localStorage(安全面)。

const token = ref('');
const status = ref(null);
const loading = ref(false);

export function resetForTests() {
  token.value = '';
  status.value = null;
  loading.value = false;
}

export function useWebTerminal() {
  const unlocked = computed(() => !!token.value);

  async function refresh() {
    status.value = await getWebTerminalState();
    return status.value;
  }

  async function command(payload) {
    return webTerminalCommand(payload, token.value);
  }

  async function setup(pin) {
    const res = await command({ action: 'setup', pin });
    if (res.ok) await refresh();
    return res;
  }

  async function verify(pin) {
    const res = await command({ action: 'verify', pin });
    if (res.ok && res.token) token.value = res.token;
    return res;
  }

  async function changePin(oldPin, newPin) {
    const res = await command({ action: 'change', old_pin: oldPin, new_pin: newPin });
    return res;
  }

  async function lock() {
    const res = await command({ action: 'lock' });
    token.value = '';
    await refresh();
    return res;
  }

  async function setEnabled(enabled) {
    const res = await command({ action: 'gates', enabled });
    if (!enabled) token.value = '';
    await refresh();
    return res;
  }

  async function killall() {
    return command({ action: 'killall' });
  }

  // 令牌失效(服务端 15 分钟闲置重锁杀终端断 WS):清本地令牌,工作台回落
  // 解锁密码盘(spec §9——不整页退出)。由 TerminalPane 的 dead 事件驱动。
  function invalidateToken() {
    token.value = '';
  }

  async function resetPin() {
    const res = await command({ action: 'reset' });
    token.value = '';
    await refresh();
    return res;
  }

  return {
    token, status, loading, unlocked,
    refresh, setup, verify, changePin, lock, setEnabled, killall, resetPin,
    invalidateToken,
  };
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd app/website && npx vitest run src/composables/useWebTerminal.test.js`
Expected: PASS

---

### Task 13: useTerminalSocket.js(每会话 WS 客户端)

**Files:**
- Create: `app/website/src/composables/useTerminalSocket.js`
- Test: `app/website/src/composables/useTerminalSocket.test.js`

- [ ] **Step 1: 写失败测试(Mock WebSocket)**

```js
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

class MockWebSocket {
  static instances = [];
  static CONNECTING = 0; static OPEN = 1; static CLOSING = 2; static CLOSED = 3;
  constructor(url) {
    this.url = url;
    this.readyState = 0;
    this.sent = [];
    MockWebSocket.instances.push(this);
  }
  send(data) { this.sent.push(data); }
  close() { this.readyState = 3; this.onclose && this.onclose({ code: 1000, reason: '' }); }
  open() { this.readyState = 1; this.onopen && this.onopen(); }
  emit(data) { this.onmessage && this.onmessage({ data: JSON.stringify(data) }); }
}
vi.stubGlobal('WebSocket', MockWebSocket);

import { createTerminalSocket } from './useTerminalSocket';

describe('createTerminalSocket', () => {
  beforeEach(() => { MockWebSocket.instances = []; vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it('open 后先发 auth 再发 create(attach:true)', () => {
    const frames = [];
    const sock = createTerminalSocket({
      token: 'tk', sessionId: 's1', rows: 24, cols: 80,
      onFrame: (f) => frames.push(f),
    });
    const ws = MockWebSocket.instances.at(-1);
    expect(ws.url).toContain('/api/agent/web-terminal/stream');
    ws.open();
    expect(JSON.parse(ws.sent[0])).toEqual({ type: 'terminal.auth', token: 'tk' });
    const create = JSON.parse(ws.sent[1]);
    expect(create.type).toBe('terminal.create');
    expect(create.session_id).toBe('s1');
    expect(create.attach).toBe(true);
    sock.dispose();
  });

  it('output/replay 帧路由到 onFrame;exit 帧回调 onExit', () => {
    const frames = [];
    let exited = false;
    const sock = createTerminalSocket({
      token: 'tk', sessionId: 's1', rows: 24, cols: 80,
      onFrame: (f) => frames.push(f),
      onExit: () => { exited = true; },
    });
    const ws = MockWebSocket.instances.at(-1);
    ws.open();
    ws.emit({ type: 'terminal.output', session_id: 's1', data: 'hello' });
    ws.emit({ type: 'terminal.replay', session_id: 's1', data: 'old', seq: 0, final: true });
    ws.emit({ type: 'terminal.exit', session_id: 's1' });
    expect(frames.map((f) => f.data)).toEqual(['hello', 'old']);
    expect(exited).toBe(true);
    sock.dispose();
  });

  it('意外断开重连(≤2 次,800ms 退避)且重连带 attach', () => {
    const sock = createTerminalSocket({
      token: 'tk', sessionId: 's1', rows: 24, cols: 80,
      onFrame: () => {}, onStatus: () => {},
    });
    const first = MockWebSocket.instances[0];
    first.open();
    // 模拟服务端断开(非用户关闭)
    first.readyState = 3;
    first.onclose({ code: 1006, reason: '' });
    vi.advanceTimersByTime(900);
    const second = MockWebSocket.instances[1];
    expect(second).toBeTruthy();
    second.open();
    expect(JSON.parse(second.sent[1]).attach).toBe(true);
    sock.dispose();
  });

  it('resize/close 走 send 且 readyState 守卫', () => {
    const sock = createTerminalSocket({ token: 'tk', sessionId: 's1', rows: 24, cols: 80, onFrame: () => {} });
    const ws = MockWebSocket.instances.at(-1);
    ws.open();
    sock.resize(30, 120);
    sock.close();
    const types = ws.sent.map((d) => JSON.parse(d).type);
    expect(types).toContain('terminal.resize');
    expect(types).toContain('terminal.close');
    sock.dispose();
  });
});
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd app/website && npx vitest run src/composables/useTerminalSocket.test.js`
Expected: FAIL

- [ ] **Step 3: 实现**

```js
// 每会话一条终端 WS(spec §6.2)。建连/超时保险丝/重连预算照抄
// useAliangChat.ensureAISocket 形态(幂等 CONNECTING 去重 + 10s 超时 + 2 次
// 800ms 退避,生产性入站帧归零预算)。首帧强制 terminal.auth;create 恒带
// attach:true——重连即续屏(manager:活会话 replay+resume/墓碑 replay)。

const TERMINAL_CONNECT_TIMEOUT_MS = 10000;
const TERMINAL_RECONNECT_LIMIT = 2;
const TERMINAL_RECONNECT_BACKOFF_MS = 800;

export function createTerminalSocket({ token, sessionId, rows, cols, onFrame, onExit, onStatus }) {
  let ws = null;
  let disposed = false;
  let reconnectAttempts = 0;
  let reconnectTimer = null;
  let connectTimer = null;
  let open = false;

  function terminalUrl() {
    const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
    return `${proto}://${window.location.host}/api/agent/web-terminal/stream`;
  }

  function handleFrame(parsed) {
    if (!parsed || !parsed.type) return;
    // 生产性入站帧=通道健康证明(归零重连预算,同聊天 WS 语义)
    reconnectAttempts = 0;
    switch (parsed.type) {
      case 'terminal.output':
      case 'terminal.replay':
        onFrame && onFrame(parsed);
        break;
      case 'terminal.created':
        onStatus && onStatus({ kind: 'created', resumed: !!parsed.resumed, pty: !!parsed.pty });
        break;
      case 'terminal.exit':
        onStatus && onStatus({ kind: 'exit' });
        onExit && onExit(parsed);
        break;
      case 'terminal.error':
        onStatus && onStatus({ kind: 'error', message: parsed.error || 'terminal error' });
        break;
      default:
        break;
    }
  }

  function connect() {
    if (disposed) return;
    try {
      ws = new WebSocket(terminalUrl());
    } catch (e) {
      scheduleReconnect();
      return;
    }
    connectTimer = setTimeout(() => {
      if (ws && ws.readyState === WebSocket.CONNECTING) {
        try { ws.close(); } catch (_) { /* 已死即跳过 */ }
      }
    }, TERMINAL_CONNECT_TIMEOUT_MS);
    ws.onopen = () => {
      clearTimeout(connectTimer);
      open = true;
      // 先 auth 后 create:服务端首帧即鉴权,WS 消息有序保证 auth 先到
      ws.send(JSON.stringify({ type: 'terminal.auth', token }));
      ws.send(JSON.stringify({ type: 'terminal.create', session_id: sessionId, rows, cols, attach: true }));
    };
    ws.onmessage = (ev) => {
      let parsed = null;
      try { parsed = JSON.parse(ev.data); } catch (_) { return; }
      handleFrame(parsed);
    };
    ws.onclose = (e) => {
      open = false;
      if (disposed) return;
      onStatus && onStatus({ kind: 'closed', code: e.code });
      // 用户主动 close() 的断开走 dispose 分支,不会进这里(afterClose 1000
      // 也可能是服务端 exit;统一按预算重连,dispose 后不再重连)
      if (reconnectAttempts < TERMINAL_RECONNECT_LIMIT) {
        reconnectAttempts += 1;
        reconnectTimer = setTimeout(() => {
          reconnectTimer = null;
          if (!disposed) connect();
        }, TERMINAL_RECONNECT_BACKOFF_MS);
      } else {
        onStatus && onStatus({ kind: 'dead' });
      }
    };
    ws.onerror = () => { /* close 事件统一处理 */ };
  }

  function scheduleReconnect() {
    if (disposed) return;
    reconnectTimer = setTimeout(() => { reconnectTimer = null; if (!disposed) connect(); }, TERMINAL_RECONNECT_BACKOFF_MS);
  }

  function send(payload) {
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(payload));
  }

  connect();

  return {
    input(data) { send({ type: 'terminal.input', session_id: sessionId, data }); },
    resize(r, c) { send({ type: 'terminal.resize', session_id: sessionId, rows: r, cols: c }); },
    close() { send({ type: 'terminal.close', session_id: sessionId }); },
    dispose() {
      disposed = true;
      if (reconnectTimer) clearTimeout(reconnectTimer);
      if (connectTimer) clearTimeout(connectTimer);
      if (ws) { try { ws.close(); } catch (_) {} }
      ws = null;
    },
  };
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd app/website && npx vitest run src/composables/useTerminalSocket.test.js`
Expected: PASS

---

### Task 14: PinGuard.vue(设置/解锁/锁定倒计时)

**Files:**
- Create: `app/website/src/components/terminal/PinGuard.vue`

- [ ] **Step 1: 实现(展示组件,状态机由 useWebTerminal 驱动;交互细节轻测试不强制)**

```vue
<template>
  <div class="flex h-full w-full items-center justify-center bg-slate-50 dark:bg-slate-950">
    <div class="w-full max-w-sm rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
      <h2 class="mb-1 text-lg font-semibold text-slate-900 dark:text-slate-100">{{ title }}</h2>
      <p v-if="subtitle" class="mb-4 text-sm text-slate-500 dark:text-slate-400">{{ subtitle }}</p>

      <!-- 锁定倒计时 -->
      <div v-if="lockRemaining > 0" class="mb-4 rounded-lg bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-950/50 dark:text-red-300">
        {{ t('vault_locked') }}<span class="font-mono">{{ lockRemaining }}</span>{{ t('vault_seconds_unit') }}
      </div>

      <label class="mb-1 block text-xs font-medium text-slate-500 dark:text-slate-400">{{ pinLabel }}</label>
      <input
        ref="pinInput"
        v-model="pin"
        type="password"
        inputmode="numeric"
        maxlength="4"
        autocomplete="off"
        :disabled="lockRemaining > 0 || busy"
        data-testid="vault-pin-input"
        class="mb-3 w-full rounded border border-slate-300 bg-white px-3 py-2 text-center font-mono text-xl tracking-[0.5em] dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
        @keydown.enter="submit"
      />
      <template v-if="mode === 'setup'">
        <label class="mb-1 block text-xs font-medium text-slate-500 dark:text-slate-400">{{ t('vault_confirm_pin') }}</label>
        <input
          v-model="pinConfirm"
          type="password"
          inputmode="numeric"
          maxlength="4"
          autocomplete="off"
          :disabled="busy"
          class="mb-3 w-full rounded border border-slate-300 bg-white px-3 py-2 text-center font-mono text-xl tracking-[0.5em] dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
          @keydown.enter="submit"
        />
      </template>

      <p v-if="error" class="mb-3 text-sm text-red-600 dark:text-red-400" data-testid="vault-error">{{ error }}</p>

      <button
        type="button"
        :disabled="busy || lockRemaining > 0"
        data-testid="vault-submit"
        class="w-full rounded bg-primary px-4 py-2 text-sm font-medium text-white disabled:opacity-50"
        @click="submit"
      >
        {{ busy ? t('vault_working') : submitLabel }}
      </button>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue';
import { useI18n } from '../../i18n';
import { useWebTerminal } from '../../composables/useWebTerminal';

const props = defineProps({
  // 'setup' | 'unlock'
  mode: { type: String, required: true },
});
const emit = defineEmits(['unlocked', 'setup-done']);

const { t } = useI18n();
const wt = useWebTerminal();
const pin = ref('');
const pinConfirm = ref('');
const error = ref('');
const busy = ref(false);
const lockRemaining = ref(0);
let ticker = null;

const title = computed(() => (props.mode === 'setup' ? t('vault_set_pin_title') : t('vault_unlock_title')));
const subtitle = computed(() => (props.mode === 'setup' ? t('vault_set_pin_desc') : ''));
const pinLabel = computed(() => (props.mode === 'setup' ? t('vault_new_pin') : t('vault_enter_pin')));
const submitLabel = computed(() => (props.mode === 'setup' ? t('vault_set_btn') : t('vault_unlock_btn')));

function startCountdown(seconds) {
  lockRemaining.value = seconds;
  if (ticker) clearInterval(ticker);
  ticker = setInterval(() => {
    lockRemaining.value -= 1;
    if (lockRemaining.value <= 0) {
      clearInterval(ticker);
      ticker = null;
    }
  }, 1000);
}

async function submit() {
  error.value = '';
  if (props.mode === 'setup' && pin.value !== pinConfirm.value) {
    error.value = t('vault_mismatch');
    return;
  }
  busy.value = true;
  try {
    const res = props.mode === 'setup' ? await wt.setup(pin.value) : await wt.verify(pin.value);
    if (res.ok) {
      pin.value = '';
      pinConfirm.value = '';
      emit(props.mode === 'setup' ? 'setup-done' : 'unlocked');
      return;
    }
    if (res.reason === 'locked') startCountdown(res.lock_remaining_seconds || 0);
    else if (res.reason === 'bad_pin') error.value = `${t('vault_wrong_pin')}(${t('vault_attempts_left')} ${res.attempts_left ?? 0})`;
    else if (res.reason === 'mismatch') error.value = t('vault_mismatch');
    else if (res.reason === 'bad_pin_form') error.value = t('vault_bad_form');
    else if (res.reason === 'disabled') error.value = t('vault_disabled');
    else if (res.reason === 'too_fast') error.value = t('vault_too_fast');
    else error.value = t('vault_error_generic');
  } finally {
    busy.value = false;
  }
}

onMounted(() => { pinInput.value?.focus?.(); });
onUnmounted(() => { if (ticker) clearInterval(ticker); });
</script>
```

(`mismatch` 分支在 submit 前已拦——保留防御;primary 色按仓内 Tailwind 配置实际类名微调。)

- [ ] **Step 2:** `cd app/website && npm run build` — 通过

---

### Task 15: TerminalPane.vue(xterm 单窗格)

**Files:**
- Create: `app/website/src/components/terminal/TerminalPane.vue`

- [ ] **Step 1: 实现**

```vue
<template>
  <div class="relative h-full w-full overflow-hidden rounded-lg border border-slate-700 bg-black" data-testid="terminal-pane">
    <div ref="hostEl" class="h-full w-full p-1"></div>
    <div v-if="statusText" class="absolute bottom-2 left-2 rounded bg-black/70 px-2 py-1 text-xs text-slate-300">
      {{ statusText }}
    </div>
  </div>
</template>

<script setup>
import { onMounted, onUnmounted, ref } from 'vue';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import { createTerminalSocket } from '../../composables/useTerminalSocket';
import { useI18n } from '../../i18n';

const props = defineProps({
  token: { type: String, required: true },
});
const emit = defineEmits(['exit', 'dead']);

const { t } = useI18n();
const hostEl = ref(null);
const statusText = ref('');
let term = null;
let fit = null;
let sock = null;
let resizeObserver = null;
let resizeTimer = null;

onMounted(() => {
  term = new Terminal({
    cursorBlink: true,
    fontSize: 13,
    fontFamily: 'Menlo, Consolas, monospace',
    theme: { background: '#000000' },
    scrollback: 5000,
  });
  fit = new FitAddon();
  term.loadAddon(fit);
  term.open(hostEl.value);
  fit.fit();

  const rows = term.rows;
  const cols = term.cols;
  sock = createTerminalSocket({
    token: props.token,
    sessionId: `web-${Math.random().toString(36).slice(2, 10)}`,
    rows,
    cols,
    onFrame: (f) => {
      if (typeof f.data === 'string' && f.data.length) term.write(f.data);
    },
    onStatus: (s) => {
      if (s.kind === 'closed') statusText.value = t('term_reconnecting');
      else if (s.kind === 'dead') { statusText.value = t('term_disconnected'); emit('dead'); }
      else if (s.kind === 'error') statusText.value = s.message;
      else if (s.kind === 'created') statusText.value = '';
    },
    onExit: () => {
      statusText.value = t('term_exit');
      emit('exit');
    },
  });
  term.onData((data) => sock && sock.input(data));

  resizeObserver = new ResizeObserver(() => {
    if (resizeTimer) clearTimeout(resizeTimer);
    resizeTimer = setTimeout(() => {
      if (!fit) return;
      try {
        fit.fit();
        sock && sock.resize(term.rows, term.cols);
      } catch (_) { /* 容器折叠时 fit 可能抛错,忽略 */ }
    }, 100);
  });
  resizeObserver.observe(hostEl.value);
});

onUnmounted(() => {
  if (resizeTimer) clearTimeout(resizeTimer);
  if (resizeObserver) resizeObserver.disconnect();
  if (sock) sock.dispose();
  if (term) term.dispose();
});
</script>
```

(sessionId 前缀 `web-` 做网页会话归因(spec §7.3 的 origin 标记以轻量形态落地);随机段防多窗冲突,阶段 2 复用。)

- [ ] **Step 2:** `cd app/website && npm run build` — 通过

---

### Task 16: TerminalWorkspace.vue + 导航接线 + i18n

**Files:**
- Create: `app/website/src/components/TerminalWorkspace.vue`
- Modify: `app/website/src/composables/useNavigation.js`
- Modify: `app/website/src/App.vue`
- Modify: `app/website/src/components/DashboardPage.vue`(两处入口)
- Modify: `app/website/src/i18n/zh.js` / `en.js`
- Test: `app/website/src/composables/useNavigation.test.js`(若已有则追加;无则新建轻量断言)

- [ ] **Step 1: useNavigation 加词**

`PAGE_RETURN_PAGES` 加 `'terminal'`;新增:

```js
function showTerminal() {
  showPage('terminal');
}
```

导出对象加 `showTerminal`。

- [ ] **Step 2: App.vue 挂载**

`<ChatPage v-if="currentPage === 'chat'" />` 旁加:

```vue
    <TerminalWorkspace v-if="currentPage === 'terminal'" />
```

import 区加 `import TerminalWorkspace from './components/TerminalWorkspace.vue';`

- [ ] **Step 3: DashboardPage 两处入口**

桌面侧栏(:168 的 showChat 按钮旁)与移动端顶栏区(:355 的 showChat 按钮旁)各加:

```vue
        <button
          type="button"
          data-testid="terminal-entry-sidebar"
          :disabled="!isAuthenticated"
          @click="showTerminal()"
          class="w-full flex items-center gap-3 px-4 py-2.5 bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-700 rounded text-sm font-medium hover:border-primary transition-colors"
        >
          <span class="material-symbols-outlined text-slate-400 text-lg">terminal</span>
          {{ t('term_open') }}
        </button>
```

(移动端那份套用 :355 现场同款类名;`showTerminal`/`t` 若未在组件 setup 解构需补。)

- [ ] **Step 4: TerminalWorkspace.vue**

```vue
<template>
  <div class="fixed inset-0 z-[900] flex flex-col bg-slate-100 dark:bg-slate-950" data-testid="terminal-workspace">
    <!-- 头部 -->
    <div class="flex items-center justify-between border-b border-slate-200 px-4 py-2 dark:border-slate-800">
      <div class="flex items-center gap-2">
        <span class="material-symbols-outlined text-slate-400">terminal</span>
        <span class="text-sm font-medium text-slate-900 dark:text-slate-100">{{ t('term_title') }}</span>
      </div>
      <div class="flex items-center gap-2">
        <button v-if="unlocked" type="button" class="rounded border border-slate-300 px-3 py-1 text-xs dark:border-slate-700 dark:text-slate-300" data-testid="terminal-change-pin" @click="showChange = !showChange">
          {{ t('vault_change_pin') }}
        </button>
        <button v-if="unlocked" type="button" class="rounded border border-red-300 px-3 py-1 text-xs text-red-600 dark:border-red-800 dark:text-red-400" data-testid="terminal-lock" @click="onLock">
          {{ t('vault_lock_now') }}
        </button>
        <button v-if="unlocked" type="button" class="rounded border border-red-300 px-3 py-1 text-xs text-red-600 dark:border-red-800 dark:text-red-400" data-testid="terminal-killall" @click="onKillall">
          {{ t('vault_killall') }}
        </button>
        <button type="button" class="rounded border border-slate-300 px-3 py-1 text-xs dark:border-slate-700 dark:text-slate-300" data-testid="terminal-close" @click="showDashboard()">
          {{ t('term_close') }}
        </button>
      </div>
    </div>

    <!-- 主体 -->
    <div class="min-h-0 flex-1 p-3">
      <!-- 未开启:引导 -->
      <div v-if="st && !st.enabled" class="flex h-full items-center justify-center">
        <div class="max-w-md rounded-xl border border-slate-200 bg-white p-6 text-center dark:border-slate-800 dark:bg-slate-900">
          <p class="mb-2 text-sm text-slate-600 dark:text-slate-300">{{ t('vault_disabled_desc') }}</p>
          <button type="button" class="rounded bg-primary px-4 py-2 text-sm text-white" data-testid="terminal-enable" @click="onEnable">
            {{ t('vault_enable') }}
          </button>
          <p v-if="enableError" class="mt-2 text-sm text-red-600">{{ enableError }}</p>
        </div>
      </div>
      <!-- 未设 PIN:设置流程 -->
      <PinGuard v-else-if="st && st.enabled && !st.pin_set" mode="setup" @setup-done="onPinReady" />
      <!-- 已设未解锁 -->
      <PinGuard v-else-if="st && st.enabled && st.pin_set && !unlocked" mode="unlock" @unlocked="onPinReady" />
      <!-- 已解锁:终端 -->
      <TerminalPane v-else-if="unlocked" :token="token" @exit="statusRefresh" @dead="onDead" />
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue';
import { useI18n } from '../i18n';
import { useNavigation } from '../composables/useNavigation';
import { useEscStack } from '../composables/useEscStack';
import { useWebTerminal } from '../composables/useWebTerminal';
import PinGuard from './terminal/PinGuard.vue';
import TerminalPane from './terminal/TerminalPane.vue';

const { t } = useI18n();
const { showDashboard } = useNavigation();
const wt = useWebTerminal();
const st = computed(() => wt.status.value);
const { token, unlocked } = wt;
const showChange = ref(false);
const enableError = ref('');
const { push: escPush, remove: escRemove } = useEscStack();
let escId = null;

onMounted(() => {
  void wt.refresh();
  // ESC 层:工作台内 Esc 退回仪表盘(spec §7.1;层由组件自管,同 ChatPage 惯例)
  escId = escPush('terminal-workspace', () => showDashboard());
});

onUnmounted(() => { if (escId !== null) escRemove(escId); });

async function onDead() {
  // 服务端闲置重锁(15m)杀终端断 WS:清本地令牌,回落解锁密码盘(spec §9
  // 「令牌过期→重新弹密码盘,不整页退出」)
  wt.invalidateToken();
  await wt.refresh();
}

async function onKillall() {
  if (!window.confirm(t('vault_killall_confirm'))) return;
  await wt.killall();
  wt.invalidateToken();
  await wt.refresh();
}

async function onEnable() {
  enableError.value = '';
  const res = await wt.setEnabled(true);
  if (!res.ok) enableError.value = t('vault_error_generic');
}

async function onPinReady() {
  await wt.refresh();
}

async function onLock() {
  await wt.lock();
}

async function statusRefresh() {
  await wt.refresh();
}
</script>
```

(改 PIN 面板 `showChange` 的展开形态实现时补一个简易内联表单调 `wt.changePin`;阶段 1 最小可用=按钮+三输入+提交,样式随现场。)

- [ ] **Step 5: i18n 词条**

`zh.js` 追加:

```js
  // -- Web Terminal --
  term_open: '打开终端',
  term_title: '终端',
  term_close: '关闭',
  term_reconnecting: '重连中…',
  term_disconnected: '连接已断开',
  term_exit: '会话已结束',
  vault_set_pin_title: '设置保险箱密码',
  vault_set_pin_desc: '网页终端是本机的完整 shell 权限,请设置 4 位数字密码保护。',
  vault_new_pin: '新密码(4 位数字)',
  vault_confirm_pin: '确认密码',
  vault_enter_pin: '输入密码',
  vault_set_btn: '设置',
  vault_unlock_btn: '解锁',
  vault_unlock_title: '输入保险箱密码',
  vault_wrong_pin: '密码错误',
  vault_attempts_left: '还可尝试',
  vault_locked: '已锁定,请等待',
  vault_seconds_unit: ' 秒',
  vault_mismatch: '两次输入不一致',
  vault_bad_form: '密码必须是 4 位数字',
  vault_too_fast: '尝试过于频繁,请稍候',
  vault_disabled: '网页终端未开启',
  vault_error_generic: '操作失败,请重试',
  vault_working: '处理中…',
  vault_lock_now: '上锁',
  vault_change_pin: '改密码',
  vault_disabled_desc: '网页终端当前未开启。开启后,持有仪表盘登录态并知道保险箱密码的人可在本页使用本机终端。',
  vault_enable: '开启网页终端',
  vault_killall: '急停(终止全部会话)',
  vault_killall_confirm: '确定终止当前设备上全部网页终端会话?',
```

`en.js` 对应英文(实现时对齐仓内英文文案风格)。

- [ ] **Step 6: 跑测试 + 构建**

Run: `cd app/website && npx vitest run src/composables/useNavigation.test.js 2>/dev/null; npx vitest run && npm run build`
Expected: 全部 PASS;build 成功

---

### Task 17: 全量回归 + 提交批(用户同意后)

- [ ] **Step 1: Go 全量**

Run: `go build ./... && go test ./...`
Expected: 全绿(预存 flake 按仓内基线豁免并注明)

- [ ] **Step 2: 前端全量**

Run: `cd app/website && npx vitest run && npm run build`
Expected: 全绿

- [ ] **Step 3: 真机冒烟清单(出包装机后,验收标准=spec §11 第一期)**

1. LAN 浏览器(172.x:56431)→ 仪表盘「打开终端」→ 设置 PIN(错格式/两次不一致各有提示)
2. 解锁 → 真 shell:`echo hello`/`top`(TUI)/resize 拉伸窗口
3. 断网(关代理或杀 agent)→ 重连 → `attach:true` 续屏
4. 连错 5 次 PIN → 锁 1 分钟倒计时;锁定中正确 PIN 也拒
5. 闲置 15 分钟(改 sweep 临时验证或等真实)→ 自动重锁 + 终端被终止
6. 总开关关闭 → 工作台显示未开启引导;重开 → PIN 仍在
7. 手机端远端终端回归:remote 腿不受影响(既有 remote WS 用例)
8. `journal` 里可见 `web-terminal` 审计行(setup/verify 成败/create)

- [ ] **Step 4: 提交批(NEEDS USER CONSENT — CLAUDE.md 铁律)**

整理 `git status`/`git diff`,按仓内中文提交规范起草提交信息(含 🤖 尾注),**列出方案征求用户同意后执行**。建议拆两笔:`新增:网页终端保险箱后端(Go)` / `新增:网页终端工作台前端(xterm+PIN)`。

---

## 已知风险与实现期注意(侦察实证)

1. `remoteTerminalEnabled` 缺省 true——**勿动**它的缺省分支;web 侧独立开关零值=false。
2. `proxyAIStream` 手写 101 不经 Upgrader——网页 WS 透传腿复用它时同源复检已在其中(`aiStreamHostAllowed`),勿删。
3. 双注册缺一侧=404 透传(route_parity_test 兜底;Task 9 跑它)。
4. WS 入站 `SetReadLimit(1MiB)`,勿照抄远端 4MiB。
5. `runtimepath` import 形态照抄 `agent_service.go` 现有 import 块。
6. 远端腿改造后必须全量跑 `TestAgentTerminal*`(attach/async_create/detach 三件套=回归护栏)。
7. vitest 在 `app/website` 目录下跑;Go 测试在仓库根跑。
8. 前端 token 只存内存,刷新即失——重新输 PIN 是安全特性不是 bug。
9. `resetPin` 本期不在 UI 暴露(loopback-only 门在服务端;LAN 上放一个必 403 的按钮徒增困惑),阶段 2 连同 loopback 探测一起做——`useWebTerminal` 里的函数保留属接口预留,非死代码。
10. **勘误(2026-11-22 实施期评审抓出,代码已按此修正)**:本稿 Task 7 的 HandleWebTerminalCommand 先 `io.ReadAll` 抽干 body 再 `proxyIfNeeded`——core 腿转发空 body 导致 LAN 全部命令 POST 断(本稿自身缺陷,非实现偏差)。正确解=解析后 `r.Body = io.NopCloser(bytes.NewReader(raw))` 回填再透传,并以单元测试钉住「解析后 body 可原样重读」不变量。仓库范式 proxy-first-then-parse 与前置门(需先读 body)冲突时的通用解法即此回填。另:killall 空令牌的服务级闸经 HTTP 不可达(runtime 侧 IsLoopbackRequest 恒真,终审 Finding A),已在 handler 预 proxy 拦截(与 reset 同款)。
