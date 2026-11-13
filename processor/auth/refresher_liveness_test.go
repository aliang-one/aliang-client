package user

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// TokenRefresher 活性可观测+看门狗(2026-11-12 登出事故):
// 刷新循环是单 goroutine 同步消费 tick,prod INFO 档下正常 tick 零日志,
// 停摆与空闲不可分辨;卡死后 IsRunning() 恒真堵死自愈门。本组测试锁定:
// tick 活性可读、心跳按间隔推进、看门狗能把楔死的循环重建。

func livenessTestSession(t *testing.T) {
	t.Helper()
	ResetAuthPersistenceForTest()
	// 远期会话:tick 早退(未到期不刷新),避免测试里真的发起 HTTP 刷新
	if err := SaveUserInfo(&UserInfo{
		AccessToken: "st-live",
		ExpiresIn:   86400,
		UpdatedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}
	t.Cleanup(func() {
		StopTokenRefresh()
		// 共享测试库的行会跨测试存活,显式删除防止 token 渗入后续测试。
		_ = DeleteUserInfo()
		ResetAuthPersistenceForTest()
	})
}

func TestTokenRefresherRecordsTickLiveness(t *testing.T) {
	livenessTestSession(t)

	tr := NewTokenRefresher()
	tr.refreshDuration = 20 * time.Millisecond
	if err := tr.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = tr.Stop() }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		last := tr.LastTickAt()
		if !last.IsZero() && time.Since(last) < 2*time.Second {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("LastTickAt() never advanced after ticks; tick liveness is unobservable (the 2026-11-12 blind spot)")
}

func TestTokenRefresherHeartbeatAdvances(t *testing.T) {
	livenessTestSession(t)

	tr := NewTokenRefresher()
	tr.refreshDuration = 10 * time.Millisecond
	tr.heartbeatInterval = 30 * time.Millisecond
	if err := tr.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = tr.Stop() }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		hb := tr.LastHeartbeatAt()
		if !hb.IsZero() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("heartbeat never fired; refresher health stays invisible in prod logs")
}

func TestWatchdogRebuildsStalledRefresher(t *testing.T) {
	livenessTestSession(t)

	var mu sync.Mutex
	var events []string
	SetRefreshLivenessAlarmHandler(func(event string) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	})
	t.Cleanup(func() { SetRefreshLivenessAlarmHandler(nil) })

	StopTokenRefresh()
	startTokenRefresh()
	old := GetTokenRefresher()
	if old == nil || !old.IsRunning() {
		t.Fatal("refresher not running after startTokenRefresh()")
	}

	// 伪造楔死现场:tick 活性停在 30 分钟前(远超停摆阈值)
	old.lastTickAt.Store(time.Now().Add(-30 * time.Minute).UnixNano())

	if rebuilt := rebuildStalledTokenRefresherIfNecessary(); !rebuilt {
		t.Fatal("watchdog decision did not rebuild a stalled refresher")
	}

	fresh := GetTokenRefresher()
	if fresh == old {
		t.Fatal("refresher instance not replaced; wedged loop would keep the IsRunning()=true dead gate closed forever")
	}
	if !fresh.IsRunning() {
		t.Fatal("replacement refresher not running")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) == 0 {
		t.Fatal("no liveness alarm fired on rebuild")
	}
}

func TestWatchdogLeavesHealthyRefresherAlone(t *testing.T) {
	livenessTestSession(t)

	StopTokenRefresh()
	startTokenRefresh()
	before := GetTokenRefresher()
	if before == nil || !before.IsRunning() {
		t.Fatal("refresher not running after startTokenRefresh()")
	}

	if rebuilt := rebuildStalledTokenRefresherIfNecessary(); rebuilt {
		t.Fatal("watchdog rebuilt a healthy refresher (tick liveness fresh)")
	}
	if GetTokenRefresher() != before {
		t.Fatal("healthy refresher instance was replaced")
	}
}

func TestUserInfoOverdueDiagnostic(t *testing.T) {
	now := time.Now()

	// 未逾期:锚点+ExpiresIn 还在未来
	fresh := &UserInfo{UpdatedAt: now.Add(-30 * time.Minute), ExpiresIn: 86400}
	if got := userInfoOverdue(fresh, now); got != 0 {
		t.Fatalf("userInfoOverdue(fresh session) = %v, want 0", got)
	}

	// 逾期 20 分钟:昨天登出的形态(迟 20 分钟撞零宽限)
	overdue := &UserInfo{UpdatedAt: now.Add(-24*time.Hour - 20*time.Minute).Truncate(time.Second), ExpiresIn: 86400}
	if got := userInfoOverdue(overdue, now); got < 19*time.Minute || got > 21*time.Minute {
		t.Fatalf("userInfoOverdue(20min overdue) = %v, want ~20min", got)
	}

	// 锚点被解耦写污染的形态:逾期以小时计
	lapsed := &UserInfo{UpdatedAt: now.Add(-46 * time.Hour).Truncate(time.Second), ExpiresIn: 86400}
	if got := userInfoOverdue(lapsed, now); got < 21*time.Hour || got > 23*time.Hour {
		t.Fatalf("userInfoOverdue(22h overdue) = %v, want ~22h", got)
	}

	// 无效会话:无逾期语义
	if got := userInfoOverdue(nil, now); got != 0 {
		t.Fatalf("userInfoOverdue(nil) = %v, want 0", got)
	}
	empty := &UserInfo{}
	if got := userInfoOverdue(empty, now); got != 0 {
		t.Fatalf("userInfoOverdue(no ExpiresIn) = %v, want 0", got)
	}
}

// 逾期诊断必须能在触发链上被还原成可 grep 的一句话。
func TestOverdueDiagnosticWording(t *testing.T) {
	overdue := &UserInfo{UpdatedAt: time.Now().Add(-24*time.Hour - 20*time.Minute), ExpiresIn: 86400}
	msg := overdueDiagnostic(overdue, time.Now())
	if msg == "" {
		t.Fatal("overdueDiagnostic returned empty for an overdue session")
	}
	if !containsAll(msg, "overdue", "lapsed") {
		t.Fatalf("diagnosis lacks postmortem keywords: %q", msg)
	}

	fresh := &UserInfo{UpdatedAt: time.Now(), ExpiresIn: 86400}
	if msg := overdueDiagnostic(fresh, time.Now()); msg != "" {
		t.Fatalf("overdueDiagnostic(fresh) = %q, want empty", msg)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
