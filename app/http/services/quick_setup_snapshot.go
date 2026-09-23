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
	// spec §8）。进循环前独立算出 config 的判定结果——不复用循环内先算先记的
	// 状态，消除对 catalog 声明顺序（config 必须排在 auth 之前）的隐式耦合。
	codexConfigManaged := false
	if softwareDef.Code == "codex" {
		for _, fileDef := range softwareDef.Files {
			if fileDef.Code != "config" {
				continue
			}
			codexConfigManaged = quickSetupManagedByAliang(
				softwareDef.Code,
				fileDef.Format,
				quickSetupSnapshotFileContent(softwareDef.Code, fileDef, targetUser.homeDir),
			)
			break
		}
	}
	for _, fileDef := range softwareDef.Files {
		entry := models.QuickSetupConfigStateFile{
			Path:   fileDef.DefaultPath,
			Format: fileDef.Format,
		}
		if resolved, resolveErr := resolveQuickSetupApplyPath(softwareDef.Code, fileDef.DefaultPath, targetUser.homeDir); resolveErr == nil {
			// Stat 决定 Exists/Size/ModifiedAt；Content 单独被上限/读失败门控（超限或
			// 读时变大只导致内容不回传，不再折叠成 Exists=false）。
			if info, statErr := os.Stat(resolved); statErr == nil && info.Mode().IsRegular() {
				entry.Exists = true
				entry.Size = info.Size()
				entry.ModifiedAt = info.ModTime().Format(time.RFC3339)
				entry.Content = quickSetupSnapshotFileContent(softwareDef.Code, fileDef, targetUser.homeDir)
			}
			// Stat 失败/非普通文件时按不存在展示：纯展示语义，无安全决策依赖此折叠。
		}
		if softwareDef.Code == "codex" && fileDef.Code == "auth" {
			entry.ManagedByAliang = codexConfigManaged
		} else {
			entry.ManagedByAliang = quickSetupManagedByAliang(softwareDef.Code, fileDef.Format, entry.Content)
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

// Restore 按 software 整体还原原始配置（spec §6.3-3/§8）。
// 与 Apply 互斥：全程持有 quickSetupApplyMu，防止与 apply 的备份/写入交错破坏 manifest。
func (s *QuickSetupService) Restore(softwareCode string) (models.QuickSetupRestoreResponse, error) {
	software := strings.ToLower(strings.TrimSpace(softwareCode))
	if software == "" {
		return models.QuickSetupRestoreResponse{}, errors.New("software is required")
	}
	softwareDef, ok := findQuickSetupSoftware(software)
	if !ok {
		return models.QuickSetupRestoreResponse{}, fmt.Errorf("software is not valid: %s", software)
	}
	if strings.TrimSpace(quickSetupAuthorizationHeaderFn()) == "" {
		return models.QuickSetupRestoreResponse{}, ErrQuickSetupUnauthenticated
	}
	targetUser, err := quickSetupTargetUserFn()
	if err != nil {
		return models.QuickSetupRestoreResponse{}, fmt.Errorf("resolve quick setup user: %w", err)
	}

	quickSetupApplyMu.Lock()
	defer quickSetupApplyMu.Unlock()

	return restoreQuickSetupSoftware(targetUser, softwareDef.Code)
}

// quickSetupSnapshotFileContent 读取单个托管配置文件的内容快照（ConfigState 循环
// 与 codex auth 跟随判定共用）：路径解析失败、非普通文件、超过
// quickSetupMaxApplyFileBytes 或读盘失败一律返回空串——内容为空的 managed 判定走
// false 路径。Exists/Size/ModifiedAt 由调用方 Stat 决定，与此处读结果解耦。
func quickSetupSnapshotFileContent(softwareCode string, fileDef models.QuickSetupSoftwareFile, homeDir string) string {
	resolved, err := resolveQuickSetupApplyPath(softwareCode, fileDef.DefaultPath, homeDir)
	if err != nil {
		return ""
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Size() > quickSetupMaxApplyFileBytes {
		return ""
	}
	raw, err := os.ReadFile(resolved)
	if err != nil || int64(len(raw)) > quickSetupMaxApplyFileBytes {
		return ""
	}
	return string(raw)
}

// quickSetupManagedByAliang 判断磁盘上的配置内容是否由本网关写入（spec §8 启发式）：
//   - claude-code settings.json：env.ANTHROPIC_BASE_URL 指向本网关任一接入地址；
//   - codex config.toml（format=toml）：含 [model_providers.aliang] 段表头；
//   - opencode opencode.json：任一 provider 条目的 options.baseURL 指向本网关。
//
// codex auth.json 不在本函数判定（ConfigState 进循环前预先算出 config.toml 的结果
// 供 auth 复用，不依赖 catalog 声明顺序）。
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
// 精确匹配未命中且 URL 带端口时，对两个公网接入域名补「去端口后的裸域名」比较——
// api.aliang.one:8443 这类自定义端口形式同样指向本网关。loopback 保持精确匹配不剥
// 端口：剥了会把用户本地 127.0.0.1:xxxx 的 ollama/LM Studio 误标 managed。
func quickSetupURLHostManaged(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return false
	}
	host := strings.ToLower(parsed.Host)
	if _, managed := quickSetupGatewayHosts()[host]; managed {
		return true
	}
	if parsed.Port() != "" {
		bare := strings.ToLower(parsed.Hostname())
		if bare == strings.ToLower(quickSetupInferenceHost) || bare == strings.ToLower(quickSetupControlPlaneHost) {
			return true
		}
	}
	return false
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
