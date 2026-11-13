package user

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/common/logger"
)

// TokenRefresher 定时刷新用户Token和信息
type TokenRefresher struct {
	ticker          *time.Ticker
	done            chan bool
	lastError       error
	lastErrorTime   time.Time
	refreshDuration time.Duration
	mu              sync.RWMutex
	isRunning       bool

	// 活性观测(2026-11-12 登出事故:循环停摆不可观测是根因放大器)。
	// lastTickAt 是「最近一次 tick 被消费」的时刻——循环楔死时它停止前进,
	// 是看门狗判定停摆的唯一直接证据;lastHeartbeatAt 是最近一次 INFO 心跳。
	lastTickAt        atomic.Int64
	lastHeartbeatAt   atomic.Int64
	heartbeatInterval time.Duration
}

const (
	// 默认刷新间隔（1分钟）
	defaultRefreshDuration = 1 * time.Minute
	// access token 剩余 2 小时时开始刷新。旧值 10 分钟意味着每 24h 只有一个
	// 10 分钟的续期窗口,一次网络抖动/进程不在线就错过,本地会话直接过期
	// (2026-10-04 liang-dev refresh_invalid 事故的放大器);2h 窗口给 1 分钟
	// tick 留出 ~120 次重试余量。
	tokenRefreshLeadTime = 2 * time.Hour
	// 心跳间隔:prod INFO 档下正常 tick 零输出(347bfa3 有意静默),停摆与空闲
	// 在日志上不可分辨。每 30 分钟一行活性心跳,把「循环活着」变成可观测事实。
	defaultHeartbeatInterval = 30 * time.Minute
	// 看门狗巡检节奏与停摆判定阈值:tick 周期 1 分钟,10 分钟无 tick 消费
	// (连续 10 次落空)= 循环楔死的实锤。
	tokenRefreshWatchdogTick   = time.Minute
	tokenRefreshStallThreshold = 10 * time.Minute
)

// NewTokenRefresher 创建新的Token刷新器
func NewTokenRefresher() *TokenRefresher {
	return &TokenRefresher{
		refreshDuration:   defaultRefreshDuration,
		done:              make(chan bool, 1),
		isRunning:         false,
		heartbeatInterval: defaultHeartbeatInterval,
	}
}

// Start 启动定时刷新
func (tr *TokenRefresher) Start() error {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	if tr.isRunning {
		return nil // 已经在运行
	}

	tr.ticker = time.NewTicker(tr.refreshDuration)
	tr.isRunning = true

	// 在后台运行刷新任务
	go tr.refreshLoop()

	logger.Info(fmt.Sprintf("Token refresher started (interval: %v)", tr.refreshDuration))
	return nil
}

// Stop 停止定时刷新
func (tr *TokenRefresher) Stop() error {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	if !tr.isRunning {
		return nil // 已经停止
	}

	tr.isRunning = false
	if tr.ticker != nil {
		tr.ticker.Stop()
	}

	// 发送停止信号
	select {
	case tr.done <- true:
	default:
	}

	logger.Info("Token refresher stopped")
	return nil
}

// IsRunning 检查是否在运行
func (tr *TokenRefresher) IsRunning() bool {
	tr.mu.RLock()
	defer tr.mu.RUnlock()
	return tr.isRunning
}

// RefreshNow 立即刷新一次
func (tr *TokenRefresher) RefreshNow() error {
	return tr.refreshUserInfo()
}

// GetLastError 获取最后的刷新错误
func (tr *TokenRefresher) GetLastError() error {
	tr.mu.RLock()
	defer tr.mu.RUnlock()
	return tr.lastError
}

// GetLastErrorTime 获取最后错误的时间
func (tr *TokenRefresher) GetLastErrorTime() time.Time {
	tr.mu.RLock()
	defer tr.mu.RUnlock()
	return tr.lastErrorTime
}

// LastTickAt 返回最近一次 tick 被消费的时刻;从未消费时返回零值时间。
// 循环楔死时它停止前进——这是看门狗停摆判定的唯一直接证据。
func (tr *TokenRefresher) LastTickAt() time.Time {
	if v := tr.lastTickAt.Load(); v != 0 {
		return time.Unix(0, v)
	}
	return time.Time{}
}

// LastHeartbeatAt 返回最近一次活性心跳发出的时刻;从未发出时返回零值时间。
func (tr *TokenRefresher) LastHeartbeatAt() time.Time {
	if v := tr.lastHeartbeatAt.Load(); v != 0 {
		return time.Unix(0, v)
	}
	return time.Time{}
}

// logHeartbeat 输出一行活性心跳:带上服务端死线与 lead 窗开启时刻(UTC,
// 防 CST/UTC 混读),让「循环活着」与「窗口何时开」成为可 grep 的事实。
func (tr *TokenRefresher) logHeartbeat() {
	if info := GetCurrentUserInfoOrLoad(); info != nil && info.ExpiresIn > 0 {
		expireAt := info.UpdatedAt.Add(time.Duration(info.ExpiresIn) * time.Second)
		logger.Info(fmt.Sprintf("TokenRefresher alive: expires_at=%s lead_window_opens=%s",
			expireAt.UTC().Format(time.RFC3339),
			expireAt.Add(-tokenRefreshLeadTime).UTC().Format(time.RFC3339)))
		return
	}
	logger.Info("TokenRefresher alive: no local session snapshot")
}

// refreshLoop 刷新循环
func (tr *TokenRefresher) refreshLoop() {
	lastHeartbeat := time.Now()
	for {
		select {
		case <-tr.done:
			return
		case <-tr.ticker.C:
			now := time.Now()
			tr.lastTickAt.Store(now.UnixNano())
			if now.Sub(lastHeartbeat) >= tr.heartbeatInterval {
				lastHeartbeat = now
				tr.lastHeartbeatAt.Store(now.UnixNano())
				tr.logHeartbeat()
			}

			if err := tr.refreshUserInfo(); err != nil {
				tr.mu.Lock()
				tr.lastError = err
				tr.lastErrorTime = time.Now()
				tr.mu.Unlock()

				logger.Warn(fmt.Sprintf("Failed to refresh user info: %v", err))
				// 继续运行，下次刷新时重试
			} else {
				tr.mu.Lock()
				tr.lastError = nil
				tr.mu.Unlock()

				logger.Debug("User info refreshed successfully")
			}
		}
	}
}

// userInfoOverdue 返回本地锚点认为会话已超过服务端死线多久(0=尚未逾期)。
// 触发链(agent 401 → 恢复刷新)上打这根标尺,能把「ticker 滑窗/漏窗」在
// 事故现场直接钉出来:逾期≈20 分钟=迟刷;逾期以小时计=锚点被解耦写污染。
func userInfoOverdue(info *UserInfo, now time.Time) time.Duration {
	if info == nil || info.ExpiresIn <= 0 {
		return 0
	}
	expireAt := info.UpdatedAt.Add(time.Duration(info.ExpiresIn) * time.Second)
	if d := now.Sub(expireAt); d > 0 {
		return d
	}
	return 0
}

// overdueDiagnostic 生成逾期会话的事故诊断文案;未逾期返回空串。
func overdueDiagnostic(info *UserInfo, now time.Time) string {
	if overdue := userInfoOverdue(info, now); overdue > 0 {
		return fmt.Sprintf("[AUTH-LAPSED] session lapsed: local anchor overdue by %s — background refresher missed its lead window; refresh will be rejected as lapsed", overdue.Round(time.Second))
	}
	return ""
}

// refreshUserInfo 刷新用户信息
func (tr *TokenRefresher) refreshUserInfo() error {
	currentInfo := GetCurrentUserInfoOrLoad()
	if currentInfo == nil {
		return fmt.Errorf("no user info to refresh")
	}

	// 仅在 access token 临近过期时才使用 refresh token，避免无谓轮换 refresh token。
	if !tr.isTokenExpired(currentInfo) {
		return nil
	}

	logger.Info("Access token expired or expiring soon, refreshing session")
	if _, err := RefreshSession(currentInfo.AccessToken); err != nil {
		return fmt.Errorf("failed to refresh session: %w", err)
	}

	return nil
}

// isTokenExpired 检查 access token 是否过期或即将过期
func (tr *TokenRefresher) isTokenExpired(info *UserInfo) bool {
	if info == nil || info.ExpiresIn <= 0 {
		return true
	}

	// 计算过期时间：更新时间 + 过期秒数 - 提前刷新缓冲
	expireTime := info.UpdatedAt.Add(time.Duration(info.ExpiresIn) * time.Second)
	expireTime = expireTime.Add(-tokenRefreshLeadTime)

	return time.Now().After(expireTime)
}
