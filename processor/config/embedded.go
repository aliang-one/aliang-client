package config

import _ "embed"

//go:embed config.default.json
var defaultConfigData []byte

// GetDefaultConfigBytes 返回嵌入的默认配置字节数据。
func GetDefaultConfigBytes() []byte {
	return defaultConfigData
}
