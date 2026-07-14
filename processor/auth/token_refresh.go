package user

import (
	"fmt"
	"sync"
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
}

const (
	// 默认刷新间隔（1分钟）
	defaultRefreshDuration = 1 * time.Minute
	// access token 剩余 10 分钟时开始刷新
	tokenRefreshLeadTime = 10 * time.Minute
)

// NewTokenRefresher 创建新的Token刷新器
func NewTokenRefresher() *TokenRefresher {
	return &TokenRefresher{
		refreshDuration: defaultRefreshDuration,
		done:            make(chan bool, 1),
		isRunning:       false,
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

// refreshLoop 刷新循环
func (tr *TokenRefresher) refreshLoop() {
	for {
		select {
		case <-tr.done:
			return
		case <-tr.ticker.C:
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
