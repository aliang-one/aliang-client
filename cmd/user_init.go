package cmd

import (
	"errors"
	"fmt"

	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/common/logger"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/runtime"
)

// InitializeUser 初始化用户信息和Token激活
// 返回error仅在token激活失败时触发，导致启动过程失败
// 其他情况（无token或激活成功）都返回nil，允许启动继续
func InitializeUser(token string) error {
	// 获取全局启动状态
	startupState := runtime.GetStartupState()

	// Step 1: 如果提供了token参数，尝试激活
	if token != "" {
		logger.Info("Token provided, attempting to activate...")
		userInfo, err := auth.ActivateToken(token)

		if err == nil {
			// 激活成功（可能是远程激活或本地回退）
			logger.Info(fmt.Sprintf("User activated successfully: %s (status: %s)", userInfo.Username, userInfo.Status))
			// 标记为有本地用户信息（激活成功后会自动保存到本地）
			config.SetHasLocalUserInfo(true)

			// 登录成功即视为可启动
			startupState.SetFetchSuccess(true)
			startupState.SetStatus(runtime.READY)
			if err := services.RequestUserAgentSyncAfterAuth("token_activation"); err != nil {
				logger.Warn(fmt.Sprintf("Agent device registration failed after token activation: %v", err))
			}
			logger.Info("User activated successfully, status: READY")
			return nil // 激活成功，继续启动
		} else {
			// 激活失败 → 返回error导致启动失败
			errMsg := fmt.Sprintf("Token activation failed: %v", err)
			logger.Error(errMsg)
			config.SetHasLocalUserInfo(false)
			startupState.SetFetchSuccess(false)
			startupState.SetStatus(runtime.UNCONFIGURED)
			return errors.New(errMsg)
		}
	} else {
		// Step 2: 没有提供token，尝试加载本地用户信息
		if err := loadLocalUserInfo(); err == nil {
			userInfo := auth.GetCurrentUserInfo()
			if userInfo != nil {
				logger.Info(fmt.Sprintf("Local user info loaded successfully: %s (status: %s)", userInfo.Username, userInfo.Status))
				// 标记为有本地用户信息
				config.SetHasLocalUserInfo(true)
				logger.Debug("Local user info loaded")
				if err := services.RequestUserAgentSyncAfterAuth("startup_restore"); err != nil {
					logger.Warn(fmt.Sprintf("Agent device registration failed after startup restore: %v", err))
				}
			}
		} else {
			logger.Debug("No local user info found, starting without user authentication")
			// 标记为没有本地用户信息
			config.SetHasLocalUserInfo(false)
			startupState.SetFetchSuccess(false)
			// 保持UNCONFIGURED状态（由determineInitialStartupStatus()已设置）
		}
		return nil // 无token时总是继续启动（无论是否加载本地用户成功）
	}
}

// loadLocalUserInfo 加载本地用户信息
// 返回error仅指加载失败，不指fetch失败（fetch失败允许系统继续启动）
func loadLocalUserInfo() error {
	_, err := auth.RestoreSession()
	if err != nil {
		return err
	}

	// 更新运行时状态
	// 获取启动状态以跟踪fetch结果
	startupState := runtime.GetStartupState()

	// 有本地用户信息即可进入 READY
	startupState.SetFetchSuccess(true)
	startupState.SetStatus(runtime.READY)

	return nil
}
