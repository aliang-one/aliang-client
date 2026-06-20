package user

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"aliang.one/nursorgate/processor/config"
)

// ErrScanCodeNotFound 表示 device_code 对应的扫码行不存在或已过期（official-website 返回 404）。
var ErrScanCodeNotFound = errors.New("scan code not found or expired")

// ScanInitResult 对应 official-website POST /auth/scan/init 的响应。
// official-website 的 writeJSON 原样下发（无 {code,data} envelope），故直接平铺解析。
type ScanInitResult struct {
	DeviceCode string `json:"device_code"` // PC 密钥，仅回给 PC，用于轮询取 token；永不进二维码
	ScanCode   string `json:"scan_code"`   // 二维码内容明文（App 扫到的就是它）
	QRPayload  string `json:"qr_payload"`  // 前端渲染二维码用（== scan_code）
	ExpiresIn  int    `json:"expires_in"`  // 二维码有效期（秒）
	Interval   int    `json:"interval"`    // 建议轮询间隔（秒）
}

// ScanStatusUser 是 authorized 状态下附带的本地用户摘要。
type ScanStatusUser struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

// ScanStatusResult 对应 official-website GET /auth/scan/status 的响应。
// authorized 时携带 session_token(st_) + refresh_token(sub2api) + user，
// 使 alianggate 能落地与密码登录等价的令牌对。
type ScanStatusResult struct {
	Status       string          `json:"status"` // pending|scanned|authorized|denied|expired
	ExpiresIn    int             `json:"expires_in"`
	Interval     int             `json:"interval"`
	SessionToken string          `json:"session_token"` // authorized 时下发（st_）
	RefreshToken string          `json:"refresh_token"` // authorized 时下发（sub2api refresh_token）
	User         *ScanStatusUser `json:"user"`
}

// ScanInit 向 official-website 发起扫码登录初始化，返回 PC 端 device_code 与二维码内容。
func ScanInit() (*ScanInitResult, error) {
	urlBuilder, err := config.NewURLBuilder()
	if err != nil {
		return nil, err
	}
	initURL, err := urlBuilder.GetAuthScanInitURL()
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, initURL, bytes.NewBufferString("{}"))
	if err != nil {
		return nil, fmt.Errorf("failed to create scan init request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: apiTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send scan init request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read scan init response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("scan init returned status %d: %s", resp.StatusCode, truncateBody(body))
	}

	var result ScanInitResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse scan init response: %w", err)
	}
	if strings.TrimSpace(result.DeviceCode) == "" || strings.TrimSpace(result.QRPayload) == "" {
		return nil, fmt.Errorf("scan init response missing device_code/qr_payload: %s", truncateBody(body))
	}
	return &result, nil
}

// ScanStatus 按 device_code 轮询扫码状态。authorized 时返回 st_(session_token) + sub2api refresh_token。
func ScanStatus(deviceCode string) (*ScanStatusResult, error) {
	deviceCode = strings.TrimSpace(deviceCode)
	if deviceCode == "" {
		return nil, fmt.Errorf("device_code cannot be empty")
	}

	urlBuilder, err := config.NewURLBuilder()
	if err != nil {
		return nil, err
	}
	statusURL, err := urlBuilder.GetAuthScanStatusURL()
	if err != nil {
		return nil, err
	}

	query := url.Values{}
	query.Set("device_code", deviceCode)

	req, err := http.NewRequest(http.MethodGet, statusURL+"?"+query.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create scan status request: %w", err)
	}

	client := &http.Client{Timeout: apiTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send scan status request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read scan status response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrScanCodeNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("scan status returned status %d: %s", resp.StatusCode, truncateBody(body))
	}

	var result ScanStatusResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse scan status response: %w", err)
	}
	return &result, nil
}

// truncateBody 截断响应体用于错误信息，避免把整段 body（可能含敏感信息）打进 error。
func truncateBody(body []byte) string {
	const max = 256
	if len(body) <= max {
		return string(body)
	}
	return string(body[:max]) + "...(truncated)"
}
