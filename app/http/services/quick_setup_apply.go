package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"aliang.one/nursorgate/app/http/models"
)

func (s *QuickSetupService) Apply(req models.QuickSetupApplyRequest) (*models.QuickSetupApplyResponse, error) {
	software := strings.ToLower(strings.TrimSpace(req.Software))
	if software == "" {
		return nil, errors.New("software is required")
	}
	if strings.TrimSpace(quickSetupAuthorizationHeaderFn()) == "" {
		return nil, ErrQuickSetupUnauthenticated
	}
	if len(req.Files) == 0 {
		return nil, errors.New("files are required")
	}
	if len(req.Files) > quickSetupMaxApplyFiles {
		return nil, fmt.Errorf("files are not valid: maximum is %d", quickSetupMaxApplyFiles)
	}
	targetUser, err := quickSetupTargetUserFn()
	if err != nil {
		return nil, fmt.Errorf("resolve quick setup user: %w", err)
	}

	prepared := make([]quickSetupPreparedFile, 0, len(req.Files))
	seenPaths := make(map[string]struct{}, len(req.Files))
	for _, file := range req.Files {
		targetPath := strings.TrimSpace(file.Path)
		if targetPath == "" {
			return nil, errors.New("file path is required")
		}
		if len(file.Content) > quickSetupMaxApplyFileBytes {
			return nil, fmt.Errorf("file content is not valid: exceeds %d bytes", quickSetupMaxApplyFileBytes)
		}

		resolvedPath, err := resolveQuickSetupApplyPath(software, targetPath, targetUser.homeDir)
		if err != nil {
			return nil, err
		}
		declared, err := validateQuickSetupApplyFile(software, file, resolvedPath, targetUser.homeDir)
		if err != nil {
			return nil, err
		}
		if _, exists := seenPaths[resolvedPath]; exists {
			return nil, fmt.Errorf("file path is not valid: duplicate target %s", targetPath)
		}
		seenPaths[resolvedPath] = struct{}{}
		// code 供备份 manifest 的 file_code 使用（Task 10）；内置软件取 catalog 声明
		// （validate 已完成查找并回传，避免二次多趟 EvalSymlinks），custom-*（无
		// catalog 定义）退化为文件名。
		fileCode := filepath.Base(resolvedPath)
		if declared.Code != "" {
			fileCode = declared.Code
		}
		prepared = append(prepared, quickSetupPreparedFile{code: fileCode, path: resolvedPath, content: file.Content})
	}

	quickSetupApplyMu.Lock()
	defer quickSetupApplyMu.Unlock()

	backups := make([]quickSetupFileBackup, len(prepared))
	for i, file := range prepared {
		content, readErr := os.ReadFile(file.path)
		if readErr == nil {
			if len(content) > quickSetupMaxApplyFileBytes {
				return nil, fmt.Errorf("existing config is not valid: %s exceeds %d bytes", file.path, quickSetupMaxApplyFileBytes)
			}
			backups[i] = quickSetupFileBackup{existed: true, content: content}
			continue
		}
		if !os.IsNotExist(readErr) {
			return nil, readErr
		}
	}

	// 落盘备份先于任何配置写入（spec §6.3-4）：备份失败 → 整个 Apply 失败且零写入。
	// corrupt manifest 等异常在 backupQuickSetupFiles 内 fail-safe（spec §6.3-5）。
	backupInfos, err := backupQuickSetupFiles(targetUser, software, prepared)
	if err != nil {
		return nil, err
	}

	written := make([]string, 0, len(prepared))
	for i, file := range prepared {
		writeErr := quickSetupWriteConfigFileFn(file.path, file.content)
		if writeErr == nil {
			writeErr = quickSetupAdjustOwnershipFn(file.path, targetUser)
		}
		if writeErr != nil {
			if rollbackErr := rollbackQuickSetupFiles(prepared[:i+1], backups[:i+1], targetUser); rollbackErr != nil {
				return nil, fmt.Errorf("write config failed: %w; rollback failed: %v", writeErr, rollbackErr)
			}
			return nil, writeErr
		}
		written = append(written, file.path)
	}

	return &models.QuickSetupApplyResponse{
		Software: software,
		Written:  written,
		Backups:  backupInfos,
	}, nil
}

// validateQuickSetupApplyFile 返回内置软件命中的 catalog 声明文件，供调用方复用
// （备份 file_code）；custom-* 无 catalog 定义，返回零值。
func validateQuickSetupApplyFile(software string, file models.QuickSetupApplyFile, resolvedPath string, home string) (models.QuickSetupSoftwareFile, error) {
	content := strings.TrimSpace(file.Content)
	if content == "" {
		return models.QuickSetupSoftwareFile{}, errors.New("file content is not valid: content cannot be empty")
	}

	format := strings.ToLower(strings.TrimSpace(file.Format))
	kind := strings.ToLower(strings.TrimSpace(file.Kind))
	if strings.HasPrefix(software, "custom-") {
		if kind != "" && kind != "file" {
			return models.QuickSetupSoftwareFile{}, fmt.Errorf("file kind is not valid: %s", file.Kind)
		}
		if format == "json" {
			return models.QuickSetupSoftwareFile{}, validateQuickSetupJSON(content)
		}
		return models.QuickSetupSoftwareFile{}, nil
	}

	definition, ok := findQuickSetupSoftware(software)
	if !ok {
		return models.QuickSetupSoftwareFile{}, fmt.Errorf("software is not valid: %s", software)
	}
	declared, ok, err := quickSetupDeclaredFileForPath(definition, resolvedPath, home)
	if err != nil {
		return models.QuickSetupSoftwareFile{}, err
	}
	if !ok {
		return models.QuickSetupSoftwareFile{}, errors.New("file path is not valid: target is not declared by the selected software")
	}
	if format != "" && !strings.EqualFold(format, declared.Format) {
		return models.QuickSetupSoftwareFile{}, fmt.Errorf("file format is not valid: expected %s", declared.Format)
	}
	if kind != "" && !strings.EqualFold(kind, declared.Kind) {
		return models.QuickSetupSoftwareFile{}, fmt.Errorf("file kind is not valid: expected %s", declared.Kind)
	}

	if strings.EqualFold(declared.Format, "json") {
		if err := validateQuickSetupJSON(content); err != nil {
			return models.QuickSetupSoftwareFile{}, err
		}
	}
	if software == "opencode" {
		if err := validateQuickSetupOpenCode(content); err != nil {
			return models.QuickSetupSoftwareFile{}, fmt.Errorf("OpenCode config is not valid: %w", err)
		}
	}
	return declared, nil
}

func validateQuickSetupJSON(content string) error {
	var value interface{}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("file content is not valid JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("file content is not valid JSON: multiple JSON values are not allowed")
	}
	return nil
}

func rollbackQuickSetupFiles(files []quickSetupPreparedFile, backups []quickSetupFileBackup, targetUser quickSetupTargetUser) error {
	var rollbackErr error
	for i := len(files) - 1; i >= 0; i-- {
		if backups[i].existed {
			if err := writeConfigFile(files[i].path, string(backups[i].content)); err != nil {
				rollbackErr = errors.Join(rollbackErr, err)
			} else if err := quickSetupAdjustOwnershipFn(files[i].path, targetUser); err != nil {
				rollbackErr = errors.Join(rollbackErr, err)
			}
			continue
		}
		if err := os.Remove(files[i].path); err != nil && !os.IsNotExist(err) {
			rollbackErr = errors.Join(rollbackErr, err)
		}
	}
	return rollbackErr
}
