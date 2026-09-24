// keys 转换 / URL 派生 / 读盘 helper / presets——v2 render 链退役后的幸存者。
package services

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"aliang.one/nursorgate/app/http/models"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

// quickSetupBaseURLPresets 返回该 software 的 base_url 预设（local/public）。
// codex/opencode 的 base_url 变量值带 /v1（模板裸 {{base_url}}，/v1 属变量值）；
// claude-code/pi 不带（Claude Code/pi 自行追加 /v1/messages）。
func quickSetupBaseURLPresets(softwareCode string) (local, public string, err error) {
	root, err := quickSetupBaseURL()
	if err != nil {
		return "", "", err
	}
	local = "http://" + config.DefaultHTTPProxyAddr
	public = resolveQuickSetupInferenceBaseURL(root)
	switch softwareCode {
	case "codex", "opencode":
		local += "/v1"
		public += "/v1"
	}
	return local, public, nil
}

func toQuickSetupAPIKeys(apiKeys []auth.UserAPIKey, apiRoot string) []models.QuickSetupAPIKey {
	items := make([]models.QuickSetupAPIKey, 0, len(apiKeys))
	for _, key := range apiKeys {
		if strings.ToLower(strings.TrimSpace(key.Status)) == "inactive" {
			continue
		}

		var group *models.APIKeyGroupResponse
		if key.Group != nil {
			group = &models.APIKeyGroupResponse{
				ID:                    key.Group.ID,
				Name:                  key.Group.Name,
				Description:           key.Group.Description,
				Platform:              key.Group.Platform,
				RateMultiplier:        key.Group.RateMultiplier,
				ClaudeCodeOnly:        key.Group.ClaudeCodeOnly,
				AllowMessagesDispatch: key.Group.AllowMessagesDispatch,
			}
		}

		items = append(items, models.QuickSetupAPIKey{
			ID:              key.ID,
			Key:             key.Key,
			Name:            key.Name,
			Provider:        strings.ToLower(strings.TrimSpace(key.Provider)),
			BaseURL:         quickSetupProviderBaseURL(strings.ToLower(strings.TrimSpace(key.Provider)), apiRoot),
			Status:          key.Status,
			Masked:          key.Masked,
			SecretAvailable: key.SecretAvailable,
			Group:           group,
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Provider == items[j].Provider {
			return items[i].Name < items[j].Name
		}
		return items[i].Provider < items[j].Provider
	})

	return items
}

func quickSetupProviderBaseURL(provider string, apiRoot string) string {
	root := strings.TrimRight(strings.TrimSpace(apiRoot), "/")
	switch provider {
	case "anthropic", "openai":
		if strings.HasSuffix(root, "/v1") {
			return root
		}
		return root + "/v1"
	default:
		return root
	}
}

// quickSetupReadState 是读磁盘现有配置的三态结果（Task 9 代码评审）：
// missing = 不存在（或路径不可达：家目录为空/策略拒绝）→ 模板形态、无警告；
// unreadable = 存在但读不了（非普通文件/权限拒绝/超大小上限）→ 模板形态 + 点名警告；
// ok = 成功读出内容 → 走合并。
type quickSetupReadState int

const (
	quickSetupReadMissing quickSetupReadState = iota
	quickSetupReadUnreadable
	quickSetupReadOK
)

// quickSetupReadExistingFile 只读读取磁盘上 defaultPath 对应的现有配置文件内容。
// 任何失败都不报错——由调用方按三态决定模板兜底或点名警告（combo 的 disk 入口只
// 收 ok 态，读不到即报错）。
// 特例：0 字节的已存在文件归 missing（评审）——空文件没有内容可保留，
// MergedFromDisk 不该为 true，也不该触发解析失败警告。
func quickSetupReadExistingFile(software, defaultPath, home string) (string, quickSetupReadState) {
	home = strings.TrimSpace(home)
	if home == "" {
		return "", quickSetupReadMissing
	}
	resolved, err := resolveQuickSetupApplyPath(software, defaultPath, home)
	if err != nil {
		// 解析失败需区分实情：目标位置存在但非普通文件（resolve 的普通文件闸门
		// 拒绝，如目录占位）属于「存在但读不了」→ unreadable；其余（家目录空已
		// 前置、路径越界等策略拒绝）维持现行为 missing，避免对未知状态误报警告。
		if quickSetupTargetIsNonRegular(defaultPath, home) {
			return "", quickSetupReadUnreadable
		}
		return "", quickSetupReadMissing
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return "", quickSetupReadMissing
		}
		return "", quickSetupReadUnreadable
	}
	if !info.Mode().IsRegular() || info.Size() > quickSetupMaxApplyFileBytes {
		return "", quickSetupReadUnreadable
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return "", quickSetupReadUnreadable
	}
	if len(raw) > quickSetupMaxApplyFileBytes {
		return "", quickSetupReadUnreadable
	}
	if len(raw) == 0 {
		return "", quickSetupReadMissing
	}
	return string(raw), quickSetupReadOK
}

// quickSetupTargetIsNonRegular 判断 defaultPath 展开后目标位置是否为「存在但非
// 普通文件」（目录/设备等），供 resolve 策略拒绝时区分 unreadable 与 missing。
// stat 失败（含不存在）一律不算此情形。
func quickSetupTargetIsNonRegular(defaultPath, home string) bool {
	expanded := expandQuickSetupHomePath(defaultPath, home)
	if !filepath.IsAbs(expanded) {
		return false
	}
	info, err := os.Stat(expanded)
	if err != nil {
		return false
	}
	return !info.Mode().IsRegular()
}

// quickSetupLeafFileName 从 defaultPath 取降级警告里点名的文件名（如 auth.json，
// 评审 Minor 3——与 TOML 侧点名 config.toml 对称）。取不到有效段时原样返回。
func quickSetupLeafFileName(defaultPath string) string {
	leaf := filepath.Base(strings.TrimSpace(defaultPath))
	if leaf == "" || leaf == "." || leaf == string(filepath.Separator) || leaf == "~" {
		return defaultPath
	}
	return leaf
}

func quickSetupBaseURL() (string, error) {
	cfg := config.GetGlobalConfig()
	if cfg == nil {
		return "", errors.New("config not initialized")
	}
	baseURL := strings.TrimSpace(cfg.APIBaseURL())
	if baseURL == "" {
		return "", errors.New("config.core.api_server is required for quick setup")
	}
	return resolveQuickSetupInferenceBaseURL(baseURL), nil
}

func resolveQuickSetupInferenceBaseURL(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil || !strings.EqualFold(parsed.Hostname(), quickSetupControlPlaneHost) {
		return trimmed
	}
	host := quickSetupInferenceHost
	if port := parsed.Port(); port != "" {
		host += ":" + port
	}
	parsed.Host = host
	return strings.TrimRight(parsed.String(), "/")
}
