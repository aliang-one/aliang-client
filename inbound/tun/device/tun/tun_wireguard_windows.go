package tun

import (
	"fmt"
	"time"

	"aliang.one/nursorgate/common/logger"
	"golang.zx2c4.com/wireguard/tun"
)

const (
	offset     = 0
	defaultMTU = 0 /* auto */
)

var createTUNAttemptHook func(name string, attempt int, maxRetries int, err error)

func SetCreateTUNAttemptHook(hook func(name string, attempt int, maxRetries int, err error)) {
	createTUNAttemptHook = hook
}

func createTUN(name string, mtu int) (tun.Device, error) {
	var device tun.Device
	var err error
	maxRetries := 3
	retryDelay := time.Second * 2

	for i := 0; i < maxRetries; i++ {
		// 尝试创建TUN设备
		device, err = tun.CreateTUN(name, mtu)
		if err == nil {
			if createTUNAttemptHook != nil {
				createTUNAttemptHook(name, i+1, maxRetries, nil)
			}
			logger.Info(fmt.Sprintf("成功创建TUN设备: %s", name))
			return device, nil
		}

		if createTUNAttemptHook != nil {
			createTUNAttemptHook(name, i+1, maxRetries, err)
		}

		// 记录错误
		logger.Error(fmt.Sprintf("创建TUN设备失败 (尝试 %d/%d): %v", i+1, maxRetries, err))

		// 如果不是最后一次尝试，等待后重试
		if i < maxRetries-1 {
			logger.Info(fmt.Sprintf("等待 %v 后重试...", retryDelay))
			time.Sleep(retryDelay)
			// 每次重试增加等待时间
			retryDelay *= 2
		}
	}

	// 所有重试都失败
	return nil, fmt.Errorf("创建TUN设备失败，已重试 %d 次: %v", maxRetries, err)
}
