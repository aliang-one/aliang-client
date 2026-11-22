package services

// 网页终端保险箱(spec docs/superpowers/specs/2026-11-21-web-terminal-design.md)。
// 真身在 user agent runtime(56433,loopback-only 红线):PIN/锁定阶梯/解锁令牌全在
// 本进程,core 侧只做闸门②+proxy 透传。持久化=独立 state 文件,不进 persisted.Device
// (Device 会被平台同步整体覆盖,本地开关放那里会被冲掉)。

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
	"strconv"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/internal/runtimepath"
)

const (
	webTerminalStateFilename = "web-terminal.json"
	webTerminalPinLength     = 4
	webTerminalTokenIdleTTL  = 15 * time.Minute
	webTerminalSweepInterval = 30 * time.Second
	webTerminalMinAttemptGap = time.Second
	webTerminalTokenBytes    = 32
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
	errWebTerminalNotSetUp   = errors.New("web terminal pin is not set up")
	errWebTerminalAlreadySet = errors.New("web terminal pin is already set")
	errWebTerminalBadPin     = errors.New("incorrect pin")
	errWebTerminalLocked     = errors.New("pin verification is locked")
	errWebTerminalTooFast    = errors.New("verification attempts too frequent")
	errWebTerminalBadPinForm = errors.New("pin must be exactly 4 digits")
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
	statePath          func() (string, error)
	now                func() time.Time
	frozen             time.Time // 测试时钟锚点(now 默认返回它;生产构造里 now=time.Now)
	tokenIdleTTL       time.Duration
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
	return newWebTerminalVaultWithPath(webTerminalStatePath)
}

// newWebTerminalVaultWithPath 供测试注入路径(不触碰真实状态文件);生产入口
// 恒走 newWebTerminalVault。
func newWebTerminalVaultWithPath(pathFn func() (string, error)) *webTerminalVault {
	v := &webTerminalVault{
		tokens:             make(map[string]*webTerminalToken),
		statePath:          pathFn,
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
		if os.IsNotExist(err) {
			return // 首次使用:全零值(Enabled=false,PIN 未设置)
		}
		logger.Warn(fmt.Sprintf("[WEB-TERMINAL] state_read_failed path=%s error=%v", path, err))
		return
	}
	if err := json.Unmarshal(raw, &v.state); err != nil {
		logger.Warn(fmt.Sprintf("[WEB-TERMINAL] state_parse_failed path=%s error=%v", path, err))
	}
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
	raw, err := json.MarshalIndent(v.state, "", "  ")
	if err != nil {
		return
	}
	// 原子写(tmp+rename,同 saveStateLocked):崩溃/掉电不再留下半截
	// web-terminal.json——半截文件会经 load() 静默回退零值,等价于抹掉用户
	// PIN。所有调用方持 v.mu,tmp 文件名无并发冲突。
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		logger.Warn(fmt.Sprintf("[WEB-TERMINAL] state_write_failed path=%s error=%v", tmp, err))
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		logger.Warn(fmt.Sprintf("[WEB-TERMINAL] state_rename_failed path=%s error=%v", path, err))
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

// Lock 作废全部令牌并(经 onLock)终止所有活终端。幂等;语义上 Lock 恒有效(关箱)。
func (v *webTerminalVault) Lock() {
	v.mu.Lock()
	v.tokens = make(map[string]*webTerminalToken)
	v.mu.Unlock()
	if v.onLock != nil {
		v.onLock()
	}
}

// webTerminalRandomHex 生成 nBytes 随机字节的 hex(任务2启用:PIN 盐/解锁令牌)。
func webTerminalRandomHex(nBytes int) (string, error) {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// webTerminalPinHash 盐化 PIN 哈希,拼接序=salt+":"+pin(任务2启用:SetUp/Verify)。
func webTerminalPinHash(salt, pin string) string {
	sum := sha256.Sum256([]byte(salt + ":" + pin))
	return hex.EncodeToString(sum[:])
}

// webTerminalHashEqual 恒时比较的唯一真身:Verify/Change 的 PIN 哈希比较一律
// 经它,防时序侧信道;勿在调用点内联 subtle.ConstantTimeCompare。
func webTerminalHashEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// webTerminalPinValid 校验 PIN 形态:恰 4 位数字(spec §5)。
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

// Setup 首次设置 PIN:仅在未设置态允许,设置后不可重复(改密走 Change)。
func (v *webTerminalVault) Setup(pin string) error {
	if !webTerminalPinValid(pin) {
		return errWebTerminalBadPinForm
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state.PinHash != "" {
		return errWebTerminalAlreadySet
	}
	salt, err := webTerminalRandomHex(16)
	if err != nil {
		return err
	}
	v.state.PinSalt = salt
	v.state.PinHash = webTerminalPinHash(salt, pin)
	v.state.FailCount = 0
	v.state.LockoutUntil = ""
	v.persistLocked()
	return nil
}

// Change 凭旧 PIN 换新 PIN:旧 PIN 错误返回 errWebTerminalBadPin,
// 比较恒走 webTerminalHashEqual。
// 刻意如此勿修回:Change 不检查也不清 FailCount/LockoutUntil——它不签发令牌
// 故无访问增益,知道旧 PIN 者本可坐等锁过期;若改成「Change 成功即清锁档」
// =知道旧 PIN 即可秒清 24h 锁=真绕过。已发生过一次误读,勿再修回。
func (v *webTerminalVault) Change(oldPin, newPin string) error {
	if !webTerminalPinValid(newPin) {
		return errWebTerminalBadPinForm
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state.PinHash == "" {
		return errWebTerminalNotSetUp
	}
	if !webTerminalHashEqual(webTerminalPinHash(v.state.PinSalt, oldPin), v.state.PinHash) {
		return errWebTerminalBadPin
	}
	salt, err := webTerminalRandomHex(16)
	if err != nil {
		return err
	}
	v.state.PinSalt = salt
	v.state.PinHash = webTerminalPinHash(salt, newPin)
	v.persistLocked()
	return nil
}

// Reset 清空 PIN 回未设置态+作废全部令牌(loopback-only 的调用门在 handler 层)。
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

// lockoutRemainingLocked 返回剩余锁定秒数(持 v.mu 调用);过期即自清并归零。
// 过期自清仅内存:RFC3339 时间戳是权威,重启后重新求值等价;不落盘避免 Status 轮询写盘。
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
	if v.minAttemptInterval > 0 && !v.lastAttempt.IsZero() && now.Sub(v.lastAttempt) < v.minAttemptInterval {
		return "", 0, 0, errWebTerminalTooFast
	}
	v.lastAttempt = now
	if !webTerminalHashEqual(webTerminalPinHash(v.state.PinSalt, pin), v.state.PinHash) {
		v.state.FailCount++
		// 契约(spec §11「连错 5 次 PIN 见锁定」):第 N 次失败**恰好触发锁档**
		// 时,本次即返回 locked+剩余秒(而非 bad_pin)——前端据此起倒计时;
		// 锁定档之间(如 11-14 次维持 1h)重触档同理回 locked。
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
	// 先落盘清档再铸令牌:令牌只在内存,清档才是要持久化的全部;若铸令牌
	// 失败提前返回,内存/磁盘仍一致(反过来则内存清了、磁盘还留旧锁档=分叉)。
	v.persistLocked()
	token, err := webTerminalRandomHex(webTerminalTokenBytes)
	if err != nil {
		return "", 0, 0, err
	}
	v.tokens[token] = &webTerminalToken{lastSeen: now}
	return token, 0, 0, nil
}

// Status 汇总面板状态:开关/PIN 是否已设/是否锁定/剩余锁定秒数。
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

// tokenExpiredLocked 报告令牌是否已超闲置 TTL(调用方持 v.mu;*Locked 惯例)。
// ValidateToken 的惰性过期与 sweepOnce 的批量回收共用同一判定,勿再内联。
func (v *webTerminalVault) tokenExpiredLocked(t *webTerminalToken, now time.Time) bool {
	return now.Sub(t.lastSeen) > v.tokenIdleTTL
}

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
	if v.tokenExpiredLocked(t, now) {
		delete(v.tokens, token)
		return false
	}
	t.lastSeen = now
	return true
}

// sweepOnce 回收超时令牌;有回收即触发 Lock(关箱:终止活终端+作废余令牌,
// spec §5 的「闲置自动重锁」)。返回是否有回收。startSweep 以固定节拍循环调它。
func (v *webTerminalVault) sweepOnce() bool {
	v.mu.Lock()
	now := v.now()
	expired := false
	for token, t := range v.tokens {
		if v.tokenExpiredLocked(t, now) {
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

// startSweep 常驻回收循环(Task 5 接线时启动;构造期不启,测试零 goroutine)。
func (v *webTerminalVault) startSweep() {
	safeGo("web-terminal.sweep", func() {
		ticker := time.NewTicker(webTerminalSweepInterval)
		defer ticker.Stop()
		for range ticker.C {
			v.sweepOnce()
		}
	})
}

// ——— AgentService 公开面(handler 层用)———

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

// ——— Task 8:WS 腿(handler 层用)———

// AgentTerminalErrorPayloadPublic 是 agentTerminalErrorPayload 的导出壳:
// HandleWebTerminalStream 的读循环里闲置重锁提示帧要走同一错误信封形态
// (手机腿与网页腿前端共用解析),services 内部构造器不导出故包一层。
func AgentTerminalErrorPayloadPublic(sessionID string, err error) map[string]interface{} {
	return agentTerminalErrorPayload(sessionID, err)
}

// WebTerminalAuditTerminal 落 terminal_create/terminal_close 审计行(网页腿
// 专属;手机腿审计在 remote 侧已有落点,不经过这里)。
func (s *AgentService) WebTerminalAuditTerminal(action, sessionID string) {
	s.webTerminalJournalAudit(action, "session="+sessionID, false)
}

// ——— Task 6:审计行 + Task 7:HTTP 命令分派 ———

// webTerminalJournalSession 是网页终端审计行的 journal 会话名(agentPeerExecJournalSession
// 同款约定):文件 web-terminal.jsonl 只含 kind=tool 行、无 kind=session 头行——
// 读侧扫描 parsedAny=false 整文件跳过(幽灵防御),审计留痕进磁盘但不会在仪表盘
// AI 会话列表里冒出假会话卡。
const webTerminalJournalSession = "web-terminal"

// webTerminalJournalAudit 落 kind=tool 审计行(peerExecJournalAudit 同款形态,
// session 名 web-terminal——磁盘留痕但不在仪表盘会话列表冒假卡)。nil-safe。
func (s *AgentService) webTerminalJournalAudit(action, summary string, isErr bool) {
	if s.ai == nil {
		return
	}
	s.ai.coreJournal.appendTool(webTerminalJournalSession, "web_terminal."+action, summary, isErr, "")
}

// webTerminalOkPayload / webTerminalFailPayload 是命令信封:业务失败以
// {ok:false, reason,...} 走 HTTP 200(前端要结构化读锁定秒数/剩余次数),
// 门级失败才在 handler 层给 HTTP 错误码。
func webTerminalOkPayload(extra map[string]interface{}) map[string]interface{} {
	payload := map[string]interface{}{"ok": true}
	for k, v := range extra {
		payload[k] = v
	}
	return payload
}

func webTerminalFailPayload(reason string, extra map[string]interface{}) map[string]interface{} {
	payload := map[string]interface{}{"ok": false, "reason": reason}
	for k, v := range extra {
		payload[k] = v
	}
	return payload
}

// webTerminalSetupReason / webTerminalChangeReason 把 vault 哨兵错误映射成
// 前端 reason(结构与任务 3 契约一致)。
func webTerminalSetupReason(err error) string {
	switch {
	case errors.Is(err, errWebTerminalAlreadySet):
		return "already_set"
	case errors.Is(err, errWebTerminalBadPinForm):
		return "bad_pin_form"
	default:
		return "error"
	}
}

func webTerminalChangeReason(err error) string {
	switch {
	case errors.Is(err, errWebTerminalBadPinForm):
		return "bad_pin_form"
	case errors.Is(err, errWebTerminalBadPin):
		return "bad_old_pin"
	default:
		return "error"
	}
}

// WebTerminalRunCommand 执行 vault 命令。业务失败以 {ok:false, reason,...}
// 返回(HTTP 恒 200);门级失败才走 handler 的 HTTP 错误。审计四类落点在
// 各 case 内(spec §6.4)。只应在 runtime 角色(handler 于 proxyIfNeeded 之后)
// 被调——core 侧 vault 是构造期陈旧镜像,绝不直跑。
//
// 门语义注记(spec §6.4 字面要求 vs §11 首启流程的现实解,刻意如此勿"修"回):
// gates 不要求令牌/loopback——首次 LAN 开启时根本不存在已解锁令牌,要求即
// 死锁(§11 验收第 1 步依赖);lock 允许空令牌是安全方向(上锁只会收权);
// reset 的 loopback-only 在 handler 层拦(runtime 侧天然全 loopback,拦不住)。
func (s *AgentService) WebTerminalRunCommand(action, pin, oldPin, newPin string, enabled *bool, token string, loopback bool) map[string]interface{} {
	origin := "cookie"
	if loopback {
		origin = "loopback"
	}
	switch action {
	case "state":
		// 只读快照,不落审计(读侧轮询会刷屏)。
		return webTerminalOkPayload(map[string]interface{}{"status": s.WebTerminalStatus()})
	case "setup":
		if !s.WebTerminalEnabled() {
			return webTerminalFailPayload("disabled", nil)
		}
		err := s.webTerminal.Setup(pin)
		summary := "origin=" + origin
		if err != nil {
			summary += " error=" + webTerminalSetupReason(err)
		}
		s.webTerminalJournalAudit("pin_setup", summary, err != nil)
		if err != nil {
			return webTerminalFailPayload(webTerminalSetupReason(err), nil)
		}
		return webTerminalOkPayload(nil)
	case "verify":
		if !s.WebTerminalEnabled() {
			return webTerminalFailPayload("disabled", nil)
		}
		tok, lockRem, attemptsLeft, err := s.webTerminal.Verify(pin)
		switch {
		case err == nil:
			s.webTerminalJournalAudit("pin_verify", "ok origin="+origin, false)
			return webTerminalOkPayload(map[string]interface{}{"token": tok})
		case errors.Is(err, errWebTerminalLocked):
			s.webTerminalJournalAudit("pin_verify", "locked lock_remaining_seconds="+strconv.Itoa(lockRem), true)
			return webTerminalFailPayload("locked", map[string]interface{}{"lock_remaining_seconds": lockRem})
		case errors.Is(err, errWebTerminalTooFast):
			// 节流拒绝不落审计:非用户裁决事件,高频探测下会刷屏。
			return webTerminalFailPayload("too_fast", nil)
		case errors.Is(err, errWebTerminalBadPin):
			s.webTerminalJournalAudit("pin_verify", "failed attempts_left="+strconv.Itoa(attemptsLeft), true)
			return webTerminalFailPayload("bad_pin", map[string]interface{}{"attempts_left": attemptsLeft})
		case errors.Is(err, errWebTerminalNotSetUp):
			return webTerminalFailPayload("not_set_up", nil)
		default:
			return webTerminalFailPayload("error", nil)
		}
	case "change":
		if !s.WebTerminalValidateToken(token) {
			return webTerminalFailPayload("bad_token", nil)
		}
		err := s.webTerminal.Change(oldPin, newPin)
		summary := "origin=" + origin
		if err != nil {
			summary += " error=" + webTerminalChangeReason(err)
		}
		s.webTerminalJournalAudit("pin_change", summary, err != nil)
		if err != nil {
			return webTerminalFailPayload(webTerminalChangeReason(err), nil)
		}
		return webTerminalOkPayload(nil)
	case "reset":
		// loopback-only 的调用门在 handler 层(core 腿拦截)。P3.6 真源标记
		// (X-Aliang-WT-Loopback)只修正本函数收到的 loopback 判定真伪,不改
		// 拦截位置:reset 的 core 腿 403 仍在,[NON-LOOPBACK] 高亮经 HTTP 仍
		// 不可达,维持防御性留痕。
		s.webTerminal.Reset()
		summary := "origin=" + origin
		if !loopback {
			// 防御性/HTTP 面不可达分支(handler 已 pre-proxy 403 拦死),
			// 保留给直接服务调用方留痕。
			summary += " [NON-LOOPBACK]"
		}
		s.webTerminalJournalAudit("pin_reset", summary, false)
		return webTerminalOkPayload(nil)
	case "lock":
		// 空令牌允许上锁(安全方向:只会收权);带错令牌且非 loopback 才拒。
		if token != "" && !s.WebTerminalValidateToken(token) && !loopback {
			return webTerminalFailPayload("bad_token", nil)
		}
		s.webTerminal.Lock()
		s.webTerminalJournalAudit("lock", "origin="+origin, false)
		return webTerminalOkPayload(nil)
	case "gates":
		if enabled == nil {
			return webTerminalFailPayload("bad_request", nil)
		}
		// SetEnabled(false) 内部 Lock→onLock=closeAll:关闸即杀全部活终端
		// (kill-switch,Task 5 接线)。
		s.webTerminal.SetEnabled(*enabled)
		s.webTerminalJournalAudit("gates", fmt.Sprintf("enabled=%v origin=%s", *enabled, origin), false)
		return webTerminalOkPayload(map[string]interface{}{"status": s.WebTerminalStatus()})
	case "killall":
		// 非 loopback 必须持有效令牌(空令牌=裸杀,拒);loopback 恒放行。
		// 刻意不在服务端作废调用方令牌:session kill ≠ auth revoke(锁箱才是
		// revoke,走 lock);前端以本地 invalidateToken 补偿。
		if !loopback && (token == "" || !s.WebTerminalValidateToken(token)) {
			return webTerminalFailPayload("bad_token", nil)
		}
		s.terminal.closeAll()
		s.webTerminalJournalAudit("killall", "origin="+origin, false)
		return webTerminalOkPayload(nil)
	default:
		return webTerminalFailPayload("unknown_action", nil)
	}
}
