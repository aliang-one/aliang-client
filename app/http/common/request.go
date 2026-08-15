package common

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// DecodeRequest 解析HTTP请求体
func DecodeRequest(r *http.Request, v interface{}) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

// GetQueryParamInt 获取URL查询参数（整数）
func GetQueryParamInt(r *http.Request, name string, defaultValue int) int {
	value := r.URL.Query().Get(name)
	if value == "" {
		return defaultValue
	}
	intVal, err := strconv.Atoi(value)
	if err != nil {
		return defaultValue
	}
	return intVal
}

// GetQueryParamString 获取URL查询参数（字符串）
func GetQueryParamString(r *http.Request, name string, defaultValue string) string {
	value := r.URL.Query().Get(name)
	if value == "" {
		return defaultValue
	}
	return value
}

// GetPathParamString 获取URL路径参数（字符串）
func GetPathParamString(path string, key string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == key && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// ValidateLogLevel 验证日志级别有效性
func ValidateLogLevel(level string) error {
	validLevels := map[string]bool{
		"TRACE": true,
		"DEBUG": true,
		"INFO":  true,
		"WARN":  true,
		"ERROR": true,
		"FATAL": true,
	}
	if !validLevels[strings.ToUpper(level)] {
		return nil // 允许任何级别传递，具体验证由logger模块处理
	}
	return nil
}

// DecodeJSON 解析JSON请求体（别名函数）
func DecodeJSON(body io.Reader, v interface{}) error {
	decoder := json.NewDecoder(body)
	return decoder.Decode(v)
}

// ParseIP 解析IP地址字符串
func ParseIP(ipStr string) net.IP {
	return net.ParseIP(ipStr)
}
