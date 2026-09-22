// 当前配置磁盘快照服务：托管配置文件实时读盘 + managed_by_aliang 判定 + 原始备份
// 清单（spec §8，config-state 查看器数据源）。
package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/processor/config"
)

// ConfigState 返回某 software 托管配置文件的磁盘实时状态与原始备份清单（spec §8）。
// 与 Render 不同：这是显式查询，解析目标用户失败必须报错让用户知道，而非静默兜底。
func (s *QuickSetupService) ConfigState(softwareCode string) (models.QuickSetupConfigStateResponse, error) {
	software := strings.ToLower(strings.TrimSpace(softwareCode))
	if software == "" {
		return models.QuickSetupConfigStateResponse{}, errors.New("software is required")
	}
	softwareDef, ok := findQuickSetupSoftware(software)
	if !ok {
		return models.QuickSetupConfigStateResponse{}, fmt.Errorf("software is not valid: %s", software)
	}
	if strings.TrimSpace(quickSetupAuthorizationHeaderFn()) == "" {
		return models.QuickSetupConfigStateResponse{}, ErrQuickSetupUnauthenticated
	}
	targetUser, err := quickSetupTargetUserFn()
	if err != nil {
		return models.QuickSetupConfigStateResponse{}, fmt.Errorf("resolve quick setup user: %w", err)
	}

	resp := models.QuickSetupConfigStateResponse{
		Software: softwareDef.Code,
		Files:    make([]models.QuickSetupConfigStateFile, 0, len(softwareDef.Files)),
		Backups:  []models.QuickSetupConfigStateBackup{},
	}

	// codex auth.json 的 managed 判定跟随同 software 的 config.toml（整体托管语义，
	// spec §8）；catalog 声明 config 在 auth 之前，循环内先算先记。
	codexConfigManaged := false
	for _, fileDef := range softwareDef.Files {
		entry := models.QuickSetupConfigStateFile{
			Path:   fileDef.DefaultPath,
			Format: fileDef.Format,
		}
		if resolved, resolveErr := resolveQuickSetupApplyPath(softwareDef.Code, fileDef.DefaultPath, targetUser.homeDir); resolveErr == nil {
			if info, statErr := os.Stat(resolved); statErr == nil && info.Mode().IsRegular() {
				readable := false
				if info.Size() > quickSetupMaxApplyFileBytes {
					readable = true // 超上限：文件确实存在，只是内容不回传（Content 留空）
				} else if raw, readErr := os.ReadFile(resolved); readErr == nil && int64(len(raw)) <= quickSetupMaxApplyFileBytes {
					entry.Content = string(raw)
					readable = true
				}
				if readable {
					entry.Exists = true
					entry.Size = info.Size()
					entry.ModifiedAt = info.ModTime().Format(time.RFC3339)
				}
			}
		}
		switch {
		case softwareDef.Code == "codex" && fileDef.Code == "auth":
			entry.ManagedByAliang = codexConfigManaged
		default:
			entry.ManagedByAliang = quickSetupManagedByAliang(softwareDef.Code, fileDef.Format, entry.Content)
			if softwareDef.Code == "codex" && fileDef.Code == "config" {
				codexConfigManaged = entry.ManagedByAliang
			}
		}
		resp.Files = append(resp.Files, entry)
	}

	// 备份清单：manifest 损坏 → Backups 空数组不报错——查看是展示接口，坏 manifest
	// 不应让整个接口失败（Apply/Restore 侧的 fail-safe 拒绝行为不受影响）。
	if manifest, manifestErr := loadQuickSetupManifest(targetUser.homeDir); manifestErr == nil {
		for _, entry := range manifest.Backups {
			if entry.Software != softwareDef.Code {
				continue
			}
			resp.Backups = append(resp.Backups, models.QuickSetupConfigStateBackup{
				OriginalPath: entry.OriginalPath,
				BackupPath:   entry.BackupPath,
				BackedUpAt:   entry.BackedUpAt,
				SHA256:       entry.SHA256,
				Kind:         entry.Kind,
			})
		}
	}
	return resp, nil
}

// quickSetupManagedByAliang 判断磁盘上的配置内容是否由本网关写入（spec §8 启发式）：
//   - claude-code settings.json：env.ANTHROPIC_BASE_URL 指向本网关任一接入地址；
//   - codex config.toml（format=toml）：含 [model_providers.aliang] 段表头；
//   - opencode opencode.json：任一 provider 条目的 options.baseURL 指向本网关。
//
// codex auth.json 不在本函数判定（ConfigState 循环里复用 config.toml 的结果）。
// 内容为空或解析失败一律 false。
func quickSetupManagedByAliang(softwareCode, format, content string) bool {
	switch softwareCode {
	case "claude-code":
		return quickSetupClaudeSettingsManaged(content)
	case "codex":
		if !strings.EqualFold(strings.TrimSpace(format), "toml") {
			return false
		}
		return quickSetupCodexTOMLManaged(content)
	case "opencode":
		return quickSetupOpenCodeManaged(content)
	default:
		return false
	}
}

// quickSetupGatewayHosts 罗列本网关所有接入地址的 URL host 形式（spec §8）：本地推理
// 代理与推理面/控制面域名。56432 端口一律取自 config.DefaultHTTPProxyAddr 拆
// host:port（并补 localhost 等价形式），禁止硬编码。
func quickSetupGatewayHosts() map[string]struct{} {
	hosts := map[string]struct{}{
		strings.ToLower(quickSetupInferenceHost):    {},
		strings.ToLower(quickSetupControlPlaneHost): {},
	}
	proxy := config.DefaultHTTPProxyAddr
	if _, port, err := net.SplitHostPort(proxy); err == nil {
		hosts[strings.ToLower(proxy)] = struct{}{}
		hosts["localhost:"+port] = struct{}{}
	} else {
		hosts[strings.ToLower(proxy)] = struct{}{}
	}
	return hosts
}

// quickSetupURLHostManaged 判断 base URL 的 host（含端口形式）是否命中网关地址集合。
func quickSetupURLHostManaged(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return false
	}
	_, managed := quickSetupGatewayHosts()[strings.ToLower(parsed.Host)]
	return managed
}

func quickSetupClaudeSettingsManaged(content string) bool {
	var parsed struct {
		Env map[string]interface{} `json:"env"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return false
	}
	baseURL, ok := parsed.Env["ANTHROPIC_BASE_URL"].(string)
	if !ok {
		return false
	}
	return quickSetupURLHostManaged(baseURL)
}

func quickSetupCodexTOMLManaged(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		// 排除 [[ 数组表头：数组表不是我们写入的段形态
		if strings.HasPrefix(trimmed, "[[") {
			continue
		}
		if strings.HasPrefix(trimmed, "[model_providers."+quickSetupCodexProviderID+"]") {
			return true
		}
	}
	return false
}

func quickSetupOpenCodeManaged(content string) bool {
	var parsed struct {
		Provider map[string]struct {
			Options struct {
				BaseURL string `json:"baseURL"`
			} `json:"options"`
		} `json:"provider"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return false
	}
	for _, provider := range parsed.Provider {
		if quickSetupURLHostManaged(provider.Options.BaseURL) {
			return true
		}
	}
	return false
}
