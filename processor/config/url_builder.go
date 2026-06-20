package config

import (
	"fmt"
	"net/url"
)

// URLBuilder 提供统一的 API URL 构建功能
type URLBuilder struct {
	cfg *Config
}

// NewURLBuilder 创建新的 URL 构建器
func NewURLBuilder() (*URLBuilder, error) {
	cfg := GetGlobalConfig()
	if cfg == nil {
		return nil, fmt.Errorf("config not initialized")
	}
	return &URLBuilder{cfg: cfg}, nil
}

// GetTokenActivateURL 获取 Token 激活 URL
func (ub *URLBuilder) GetTokenActivateURL() (string, error) {
	return ub.getAndValidateURL(ub.cfg.GetTokenActivateURL())
}

// GetPlanStatusURL 获取 Plan 状态 URL
func (ub *URLBuilder) GetPlanStatusURL() (string, error) {
	return ub.getAndValidateURL(ub.cfg.GetPlanStatusURL())
}

func (ub *URLBuilder) GetAuthLoginURL() (string, error) {
	return ub.getAndValidateURL(ub.cfg.GetAuthLoginURL())
}

func (ub *URLBuilder) GetAuthRefreshURL() (string, error) {
	return ub.getAndValidateURL(ub.cfg.GetAuthRefreshURL())
}

func (ub *URLBuilder) GetAuthLogoutURL() (string, error) {
	return ub.getAndValidateURL(ub.cfg.GetAuthLogoutURL())
}

func (ub *URLBuilder) GetAuthMeURL() (string, error) {
	return ub.getAndValidateURL(ub.cfg.GetAuthMeURL())
}

func (ub *URLBuilder) GetUserProfileURL() (string, error) {
	return ub.getAndValidateURL(ub.cfg.GetUserProfileURL())
}

// GetAuthScanInitURL 扫码登录初始化（PC 换 device_code + 二维码 scan_code）。
func (ub *URLBuilder) GetAuthScanInitURL() (string, error) {
	return ub.getAndValidateURL(ub.cfg.GetAuthScanInitURL())
}

// GetAuthScanStatusURL 扫码登录状态轮询（按 device_code）。
func (ub *URLBuilder) GetAuthScanStatusURL() (string, error) {
	return ub.getAndValidateURL(ub.cfg.GetAuthScanStatusURL())
}

func (ub *URLBuilder) GetUserUpdateURL() (string, error) {
	return ub.getAndValidateURL(ub.cfg.GetUserUpdateURL())
}

func (ub *URLBuilder) GetSubscriptionsSummaryURL() (string, error) {
	return ub.getAndValidateURL(ub.cfg.GetSubscriptionsSummaryURL())
}

func (ub *URLBuilder) GetSubscriptionsProgressURL() (string, error) {
	return ub.getAndValidateURL(ub.cfg.GetSubscriptionsProgressURL())
}

func (ub *URLBuilder) GetRedeemURL() (string, error) {
	return ub.getAndValidateURL(ub.cfg.GetRedeemURL())
}

func (ub *URLBuilder) GetUserAPIKeysURL() (string, error) {
	return ub.getAndValidateURL(ub.cfg.GetUserAPIKeysURL())
}

func (ub *URLBuilder) GetAvailableGroupsURL() (string, error) {
	return ub.getAndValidateURL(ub.cfg.GetAvailableGroupsURL())
}

// GetInboundsURL 获取 Inbounds URL
func (ub *URLBuilder) GetInboundsURL() (string, error) {
	return ub.getAndValidateURL(ub.cfg.GetInboundsURL())
}

// getAndValidateURL 通用的 URL 获取和验证方法
func (ub *URLBuilder) getAndValidateURL(fullURL string) (string, error) {
	if fullURL == "" {
		return "", fmt.Errorf("API URL not configured in config.core.api_server")
	}

	if !IsValidURL(fullURL) {
		return "", fmt.Errorf("invalid API URL: %s", fullURL)
	}

	return fullURL, nil
}

// IsValidURL 验证 URL 是否合法
func IsValidURL(urlString string) bool {
	parsedURL, err := url.Parse(urlString)
	return err == nil && parsedURL.Scheme != "" && parsedURL.Host != ""
}
