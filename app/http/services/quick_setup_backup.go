// 原始配置落盘备份 + manifest 读写 + 一键恢复（spec §6.3）。
package services

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

const quickSetupManifestKindOriginal = "original"

// quickSetupManifestVersion 当前 manifest 格式版本；load 时严格门禁，未来升级需迁移。
const quickSetupManifestVersion = 1

type quickSetupManifestEntry struct {
	Software      string `json:"software"`
	FileCode      string `json:"file_code"`
	OriginalPath  string `json:"original_path"` // "~/..." 形式
	BackupPath    string `json:"backup_path"`   // "~/..." 形式；existed_before=false 时为空
	BackedUpAt    string `json:"backed_up_at"`  // RFC3339
	SHA256        string `json:"sha256"`
	Size          int64  `json:"size"`
	Mode          uint32 `json:"mode"`
	ExistedBefore bool   `json:"existed_before"`
	Kind          string `json:"kind"`
}

type quickSetupManifest struct {
	Version int                       `json:"version"`
	Backups []quickSetupManifestEntry `json:"backups"`
}

func quickSetupManifestPath(homeDir string) string {
	return filepath.Join(homeDir, ".aliang", "quick-setup", "backups", "manifest.json")
}

func quickSetupContractPath(homeDir, absPath string) string {
	rel, err := filepath.Rel(homeDir, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return absPath
	}
	return "~/" + filepath.ToSlash(rel)
}

// quickSetupBackupFileName 备份文件名 = <sha256(contract 路径) 前 12 hex>-<basename>。
// 同一 software 下两个同 basename 的不同路径（custom-* 允许嵌套子路径）必须不互相覆盖。
func quickSetupBackupFileName(contract, absPath string) string {
	sum := sha256.Sum256([]byte(contract))
	return hex.EncodeToString(sum[:])[:12] + "-" + filepath.Base(absPath)
}

// loadQuickSetupManifest：不存在返回空 manifest；存在但解析失败、缺失 version 字段
// （含 JSON null）或版本不兼容均返回错误（fail-safe，上游必须拒绝 apply，防止把我们
// 的配置当「原始配置」重新备份，spec §6.3-5）。
func loadQuickSetupManifest(homeDir string) (quickSetupManifest, error) {
	m := quickSetupManifest{Version: quickSetupManifestVersion}
	raw, err := os.ReadFile(quickSetupManifestPath(homeDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m, nil
		}
		return m, err
	}
	var loaded quickSetupManifest
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return quickSetupManifest{Version: quickSetupManifestVersion}, fmt.Errorf("quick setup backup manifest is corrupt (%s): %w", quickSetupManifestPath(homeDir), err)
	}
	if loaded.Version != quickSetupManifestVersion {
		return quickSetupManifest{Version: quickSetupManifestVersion}, fmt.Errorf("quick setup backup manifest version %d is not supported (%s): expected %d", loaded.Version, quickSetupManifestPath(homeDir), quickSetupManifestVersion)
	}
	return loaded, nil
}

func saveQuickSetupManifest(targetUser quickSetupTargetUser, m quickSetupManifest) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path := quickSetupManifestPath(targetUser.homeDir)
	if err := writeConfigFile(path, string(raw)+"\n"); err != nil {
		return err
	}
	return quickSetupAdjustOwnershipFn(path, targetUser)
}

func findQuickSetupManifestEntry(m quickSetupManifest, originalPath string) (quickSetupManifestEntry, bool) {
	for _, e := range m.Backups {
		if e.OriginalPath == originalPath {
			return e, true
		}
	}
	return quickSetupManifestEntry{}, false
}

// backupQuickSetupFiles 在写配置前落盘原始内容（first-backup-wins，spec §6.3-1）。
// 任何失败都必须让调用方在写配置前中止。infos 描述本轮各文件在磁盘上的状态
// （是否已存在），与是否新落了备份无关。
//
// 隐式依赖：1MB 单文件上限由调用方保证——Apply 在 quickSetupMaxApplyFileBytes
// 校验（含磁盘上已存在文件的读取）之后才调用本函数，本函数不重复检查。
func backupQuickSetupFiles(targetUser quickSetupTargetUser, software string, files []quickSetupPreparedFile) ([]models.QuickSetupBackupInfo, error) {
	homeDir := targetUser.homeDir
	m, err := loadQuickSetupManifest(homeDir)
	if err != nil {
		return nil, err
	}
	now := time.Now().Format(time.RFC3339)
	infos := make([]models.QuickSetupBackupInfo, 0, len(files))
	dirty := false
	for _, file := range files {
		contract := quickSetupContractPath(homeDir, file.path)
		existing, readErr := os.ReadFile(file.path)
		switch {
		case readErr == nil:
			if entry, has := findQuickSetupManifestEntry(m, contract); has {
				// 已有原始备份，不覆盖；仅报告本轮状态
				infos = append(infos, models.QuickSetupBackupInfo{OriginalPath: contract, BackupPath: entry.BackupPath, ExistedBefore: true})
				continue
			}
			backupRel := filepath.Join(".aliang", "quick-setup", "backups", software, quickSetupBackupFileName(contract, file.path))
			backupAbs := filepath.Join(homeDir, backupRel)
			sum := sha256.Sum256(existing)
			entry := quickSetupManifestEntry{
				Software: software, FileCode: file.code,
				OriginalPath: contract, BackupPath: quickSetupContractPath(homeDir, backupAbs),
				BackedUpAt: now, SHA256: hex.EncodeToString(sum[:]),
				Size: int64(len(existing)), ExistedBefore: true, Kind: quickSetupManifestKindOriginal,
			}
			if st, statErr := os.Stat(file.path); statErr == nil {
				entry.Mode = uint32(st.Mode().Perm())
			}
			if err := writeConfigFile(backupAbs, string(existing)); err != nil {
				return nil, fmt.Errorf("backup %s failed: %w", contract, err)
			}
			if err := quickSetupAdjustOwnershipFn(backupAbs, targetUser); err != nil {
				return nil, fmt.Errorf("backup %s failed: %w", contract, err)
			}
			m.Backups = append(m.Backups, entry)
			dirty = true
			infos = append(infos, models.QuickSetupBackupInfo{OriginalPath: contract, BackupPath: entry.BackupPath, ExistedBefore: true})
		case errors.Is(readErr, os.ErrNotExist):
			if _, has := findQuickSetupManifestEntry(m, contract); has {
				infos = append(infos, models.QuickSetupBackupInfo{OriginalPath: contract, ExistedBefore: false})
				continue
			}
			m.Backups = append(m.Backups, quickSetupManifestEntry{
				Software: software, FileCode: file.code, OriginalPath: contract,
				BackedUpAt: now, ExistedBefore: false, Kind: quickSetupManifestKindOriginal,
			})
			dirty = true
			infos = append(infos, models.QuickSetupBackupInfo{OriginalPath: contract, ExistedBefore: false})
		default:
			return nil, fmt.Errorf("backup %s failed: %w", contract, readErr)
		}
	}
	if dirty {
		if err := saveQuickSetupManifest(targetUser, m); err != nil {
			return nil, fmt.Errorf("save backup manifest failed (%s): %w", quickSetupManifestPath(homeDir), err)
		}
	}
	return infos, nil
}

// restoreQuickSetupSoftware 按 software 整体还原（spec §6.3-3）：
// existed_before=true 复制备份回原路径；false 删除文件；成功条目从 manifest 清除并删除备份文件。
//
// 顺序契约：必须先把过滤后的 manifest 持久化成功，之后才删除备份文件。若先删文件
// 而 save 失败，残留条目会以 first-backup-wins 阻断下次 apply 重新备份，导致用户
// 原始配置永久丢失；反过来 save 失败时备份文件仍在、条目仍在，restore 可安全重试
// （删除失败仅留下孤儿备份文件，无害）。
func restoreQuickSetupSoftware(targetUser quickSetupTargetUser, software string) (models.QuickSetupRestoreResponse, error) {
	homeDir := targetUser.homeDir
	resp := models.QuickSetupRestoreResponse{}
	m, err := loadQuickSetupManifest(homeDir)
	if err != nil {
		return resp, err
	}
	kept := m.Backups[:0]
	succeeded := make([]quickSetupManifestEntry, 0, len(m.Backups))
	for _, entry := range m.Backups {
		if entry.Software != software {
			kept = append(kept, entry)
			continue
		}
		originalAbs := expandQuickSetupHomePath(entry.OriginalPath, homeDir)
		switch {
		case entry.ExistedBefore:
			raw, readErr := os.ReadFile(expandQuickSetupHomePath(entry.BackupPath, homeDir))
			if readErr != nil {
				resp.Failed = append(resp.Failed, models.QuickSetupRestoreFailure{Path: entry.OriginalPath, Error: readErr.Error()})
				kept = append(kept, entry)
				continue
			}
			mode := fs.FileMode(entry.Mode)
			if mode == 0 {
				mode = 0o600
			}
			if err := writeConfigFile(originalAbs, string(raw)); err != nil {
				resp.Failed = append(resp.Failed, models.QuickSetupRestoreFailure{Path: entry.OriginalPath, Error: err.Error()})
				kept = append(kept, entry)
				continue
			}
			if err := quickSetupAdjustOwnershipFn(originalAbs, targetUser); err != nil {
				resp.Failed = append(resp.Failed, models.QuickSetupRestoreFailure{Path: entry.OriginalPath, Error: err.Error()})
				kept = append(kept, entry)
				continue
			}
			_ = os.Chmod(originalAbs, mode)
			resp.Restored = append(resp.Restored, entry.OriginalPath)
		default:
			if err := os.Remove(originalAbs); err != nil && !errors.Is(err, os.ErrNotExist) {
				resp.Failed = append(resp.Failed, models.QuickSetupRestoreFailure{Path: entry.OriginalPath, Error: err.Error()})
				kept = append(kept, entry)
				continue
			}
			resp.Deleted = append(resp.Deleted, entry.OriginalPath)
		}
		succeeded = append(succeeded, entry)
	}
	m.Backups = kept
	if err := saveQuickSetupManifest(targetUser, m); err != nil {
		return resp, err
	}
	for _, entry := range succeeded {
		if entry.BackupPath != "" {
			_ = os.Remove(expandQuickSetupHomePath(entry.BackupPath, homeDir))
		}
	}
	return resp, nil
}
