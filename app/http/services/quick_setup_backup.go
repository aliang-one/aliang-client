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

// loadQuickSetupManifest：不存在返回空 manifest；存在但解析失败返回错误（fail-safe，
// 上游必须拒绝 apply，防止把我们的配置当「原始配置」重新备份，spec §6.3-5）。
func loadQuickSetupManifest(homeDir string) (quickSetupManifest, error) {
	m := quickSetupManifest{Version: 1}
	raw, err := os.ReadFile(quickSetupManifestPath(homeDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m, nil
		}
		return m, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return quickSetupManifest{Version: 1}, fmt.Errorf("quick setup backup manifest is corrupt (%s): %w", quickSetupManifestPath(homeDir), err)
	}
	return m, nil
}

func saveQuickSetupManifest(homeDir string, m quickSetupManifest) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeConfigFile(quickSetupManifestPath(homeDir), string(raw)+"\n")
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
func backupQuickSetupFiles(homeDir string, software string, files []quickSetupPreparedFile) ([]models.QuickSetupBackupInfo, error) {
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
			backupRel := filepath.Join(".aliang", "quick-setup", "backups", software, filepath.Base(file.path))
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
		if err := saveQuickSetupManifest(homeDir, m); err != nil {
			return nil, fmt.Errorf("save backup manifest failed: %w", err)
		}
	}
	return infos, nil
}

// restoreQuickSetupSoftware 按 software 整体还原（spec §6.3-3）：
// existed_before=true 复制备份回原路径；false 删除文件；成功条目从 manifest 清除并删除备份文件。
func restoreQuickSetupSoftware(homeDir, software string) (models.QuickSetupRestoreResponse, error) {
	resp := models.QuickSetupRestoreResponse{}
	m, err := loadQuickSetupManifest(homeDir)
	if err != nil {
		return resp, err
	}
	kept := m.Backups[:0]
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
		if entry.BackupPath != "" {
			_ = os.Remove(expandQuickSetupHomePath(entry.BackupPath, homeDir))
		}
	}
	m.Backups = kept
	if err := saveQuickSetupManifest(homeDir, m); err != nil {
		return resp, err
	}
	return resp, nil
}
