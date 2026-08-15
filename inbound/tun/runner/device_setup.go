package runner

import (
	"fmt"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/inbound/tun/runner/utils"
	"aliang.one/nursorgate/processor/config"
)

func GetDefaultDeviceConfiguration() config.EngineConf {
	// 获取默认网络接口
	defaultInterface, err := utils.GetDefaultInterface()
	if err != nil {
		logger.Error(fmt.Sprintf("Failed to get default interface: %v", err))
		defaultInterface = "en0" // 设置一个默认值
	}

	defaultEngineConf := config.EngineConf{
		MTU:       0,
		Mark:      0,
		Device:    utils.GetDefaultTunName(),
		Interface: defaultInterface,
	}
	return defaultEngineConf
}
