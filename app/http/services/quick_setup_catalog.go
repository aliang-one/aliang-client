package services

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/internal/runtimepath"
)

func quickSetupDeclaredFileForPath(software models.QuickSetupSoftware, resolvedPath string, home string) (models.QuickSetupSoftwareFile, bool, error) {
	for _, file := range software.Files {
		allowed, err := canonicalizeQuickSetupPath(expandQuickSetupHomePath(file.DefaultPath, home))
		if err != nil {
			return models.QuickSetupSoftwareFile{}, false, err
		}
		if resolvedPath == allowed {
			return file, true, nil
		}
	}
	return models.QuickSetupSoftwareFile{}, false, nil
}

func quickSetupSoftwares() []models.QuickSetupSoftware {
	return []models.QuickSetupSoftware{
		{
			Code:               "opencode",
			Name:               "OpenCode",
			Description:        "Generate a ready-to-edit OpenCode config with selected gateway provider combinations.",
			SupportedProviders: []string{"openai", "anthropic"},
			Files: []models.QuickSetupSoftwareFile{
				{
					Code:        "config",
					Label:       "opencode.json",
					FileName:    "opencode.json",
					DefaultPath: "~/.config/opencode/opencode.json",
					Format:      "json",
					Kind:        "file",
					Description: "Main OpenCode runtime configuration.",
				},
			},
		},
		{
			Code:               "codex",
			Name:               "Codex",
			Description:        "Prepare Codex config.toml plus auth.json so the CLI can start with your chosen provider.",
			SupportedProviders: []string{"openai", "anthropic"},
			Files: []models.QuickSetupSoftwareFile{
				{
					Code:        "config",
					Label:       "config.toml",
					FileName:    "config.toml",
					DefaultPath: "~/.codex/config.toml",
					Format:      "toml",
					Kind:        "file",
					Description: "Codex CLI configuration.",
				},
				{
					Code:        "auth",
					Label:       "auth.json",
					FileName:    "auth.json",
					DefaultPath: "~/.codex/auth.json",
					Format:      "json",
					Kind:        "file",
					Description: "Codex CLI auth cache for API-key sign-in.",
				},
			},
		},
		{
			Code:               "claude-code",
			Name:               "Claude Code",
			Description:        "Generate a shell snippet for ANTHROPIC_* environment variables plus a local helper script.",
			SupportedProviders: []string{"anthropic"},
			Files: []models.QuickSetupSoftwareFile{
				{
					Code:        "command",
					Label:       "env.sh",
					FileName:    "env.sh",
					DefaultPath: "~/.claude-code/env.sh",
					Format:      "shell",
					Kind:        "file",
					Description: "Shell snippet to export the gateway base URL and API key.",
				},
			},
		},
	}
}

func findQuickSetupSoftware(code string) (models.QuickSetupSoftware, bool) {
	normalized := strings.ToLower(strings.TrimSpace(code))
	for _, software := range quickSetupSoftwares() {
		if software.Code == normalized {
			return software, true
		}
	}
	return models.QuickSetupSoftware{}, false
}

func softwareSupportsProvider(software models.QuickSetupSoftware, provider string) bool {
	for _, candidate := range software.SupportedProviders {
		if candidate == provider {
			return true
		}
	}
	return false
}

func quickSetupLooksMaskedAPIKey(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	return strings.Contains(trimmed, "***") || strings.Contains(trimmed, "…") || strings.Contains(trimmed, "...")
}

func resolveQuickSetupApplyPath(software string, path string, home string) (string, error) {
	expanded := expandQuickSetupHomePath(path, home)
	if !filepath.IsAbs(expanded) {
		return "", errors.New("file path is not valid: an absolute path or ~/ path is required")
	}

	home = strings.TrimSpace(home)
	if home == "" {
		return "", errors.New("file path is not valid: user home directory is empty")
	}
	canonicalHome, err := canonicalizeQuickSetupPath(home)
	if err != nil {
		return "", fmt.Errorf("file path is not valid: resolve user home: %w", err)
	}
	canonicalTarget, err := canonicalizeQuickSetupPath(expanded)
	if err != nil {
		return "", fmt.Errorf("file path is not valid: %w", err)
	}
	if !quickSetupPathWithin(canonicalHome, canonicalTarget) {
		return "", errors.New("file path is not valid: target must stay within the user home directory")
	}
	allowedRoot, err := quickSetupAllowedRoot(software, home)
	if err != nil {
		return "", err
	}
	canonicalAllowedRoot, err := canonicalizeQuickSetupPath(allowedRoot)
	if err != nil {
		return "", fmt.Errorf("file path is not valid: resolve software config directory: %w", err)
	}
	if !quickSetupPathWithin(canonicalHome, canonicalAllowedRoot) || !quickSetupPathWithin(canonicalAllowedRoot, canonicalTarget) {
		return "", errors.New("file path is not valid: target must stay within the software config directory")
	}
	if !strings.HasPrefix(software, "custom-") {
		allowed, err := quickSetupBuiltInPathAllowed(software, canonicalTarget, home)
		if err != nil {
			return "", err
		}
		if !allowed {
			return "", errors.New("file path is not valid: target is not a declared config file for this software")
		}
	}
	if info, statErr := os.Stat(canonicalTarget); statErr == nil && !info.Mode().IsRegular() {
		return "", errors.New("file path is not valid: target must be a regular file")
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return "", fmt.Errorf("file path is not valid: %w", statErr)
	}
	return canonicalTarget, nil
}

func expandQuickSetupHomePath(path string, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return filepath.Join(home, path[2:])
	}
	return path
}

func quickSetupBuiltInPathAllowed(software string, target string, home string) (bool, error) {
	definition, ok := findQuickSetupSoftware(software)
	if !ok {
		return false, fmt.Errorf("software is not valid: %s", software)
	}
	for _, file := range definition.Files {
		expanded := expandQuickSetupHomePath(file.DefaultPath, home)
		allowed, err := canonicalizeQuickSetupPath(expanded)
		if err != nil {
			return false, err
		}
		if target == allowed {
			return true, nil
		}
	}
	return false, nil
}

func quickSetupAllowedRoot(software string, home string) (string, error) {
	switch software {
	case "opencode":
		return filepath.Join(home, ".config", "opencode"), nil
	case "codex":
		return filepath.Join(home, ".codex"), nil
	case "claude-code":
		return filepath.Join(home, ".claude-code"), nil
	default:
		if !strings.HasPrefix(software, "custom-") || len(software) <= len("custom-") || len(software) > 72 {
			return "", fmt.Errorf("software is not valid: %s", software)
		}
		for _, r := range software {
			if r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
				return "", fmt.Errorf("software is not valid: %s", software)
			}
		}
		return filepath.Join(home, ".aliang", "quick-setup", "custom", software), nil
	}
}

func resolveQuickSetupTargetUser() (quickSetupTargetUser, error) {
	current, err := user.Current()
	if err != nil {
		return quickSetupTargetUser{}, err
	}

	target := current
	adjustOwner := false
	if runtime.GOOS == "darwin" && current.Uid == "0" {
		output, statErr := exec.Command("/usr/bin/stat", "-f", "%Su", "/dev/console").CombinedOutput()
		if statErr != nil {
			return quickSetupTargetUser{}, fmt.Errorf("resolve macOS console user: %w", statErr)
		}
		consoleUser := strings.TrimSpace(string(output))
		if consoleUser == "" || consoleUser == "root" || consoleUser == "loginwindow" || consoleUser == "_mbsetupuser" {
			return quickSetupTargetUser{}, errors.New("no logged-in macOS console user is available")
		}
		target, err = user.Lookup(consoleUser)
		if err != nil {
			return quickSetupTargetUser{}, fmt.Errorf("lookup macOS console user %q: %w", consoleUser, err)
		}
		adjustOwner = true
	}

	homeDir := strings.TrimSpace(target.HomeDir)
	if !adjustOwner {
		homeDir, err = runtimepath.UserHomeDir()
		if err != nil {
			return quickSetupTargetUser{}, err
		}
	}
	if homeDir == "" {
		return quickSetupTargetUser{}, errors.New("user home directory is empty")
	}

	uid, uidErr := strconv.Atoi(strings.TrimSpace(target.Uid))
	gid, gidErr := strconv.Atoi(strings.TrimSpace(target.Gid))
	if uidErr != nil || gidErr != nil {
		uid, gid = -1, -1
		adjustOwner = false
	}
	return quickSetupTargetUser{
		homeDir:     homeDir,
		uid:         uid,
		gid:         gid,
		adjustOwner: adjustOwner,
	}, nil
}

func adjustQuickSetupOwnership(filePath string, target quickSetupTargetUser) error {
	if !target.adjustOwner {
		return nil
	}
	if target.uid < 0 || target.gid < 0 {
		return errors.New("quick setup target user ownership is not valid")
	}

	home := filepath.Clean(target.homeDir)
	dir := filepath.Dir(filepath.Clean(filePath))
	if !quickSetupPathWithin(home, dir) {
		return errors.New("quick setup target directory is outside the user home directory")
	}
	dirs := make([]string, 0, 4)
	for current := dir; current != home; current = filepath.Dir(current) {
		if current == filepath.Dir(current) || !quickSetupPathWithin(home, current) {
			return errors.New("quick setup target directory is outside the user home directory")
		}
		dirs = append(dirs, current)
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chown(dirs[i], target.uid, target.gid); err != nil {
			return fmt.Errorf("set config directory ownership: %w", err)
		}
	}
	if err := os.Chown(filePath, target.uid, target.gid); err != nil {
		return fmt.Errorf("set config file ownership: %w", err)
	}
	return nil
}

func canonicalizeQuickSetupPath(path string) (string, error) {
	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}

	current := absPath
	missing := make([]string, 0, 4)
	for {
		resolved, resolveErr := filepath.EvalSymlinks(current)
		if resolveErr == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(resolveErr) {
			return "", resolveErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", resolveErr
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func quickSetupPathWithin(base string, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// quickSetupLookPathCLIFn 是 lookPathCLI 的钩子变量，供单测注入。
var quickSetupLookPathCLIFn = lookPathCLI

// quickSetupDetectionHomeFn 返回安装检测用的家目录（root 场景解析桌面用户）。
var quickSetupDetectionHomeFn = func() string {
	if h, err := runtimepath.EffectiveAgentHome(); err == nil {
		if h = strings.TrimSpace(h); h != "" {
			return h
		}
	}
	h, _ := runtimepath.UserHomeDir()
	return strings.TrimSpace(h)
}

type quickSetupDetectionRule struct {
	cliNames []string
	dirs     []string // 相对家目录，正斜杠书写
}

var quickSetupDetectionRules = map[string]quickSetupDetectionRule{
	"claude-code": {cliNames: []string{"claude"}, dirs: []string{".claude"}},
	"codex":       {cliNames: []string{"codex"}, dirs: []string{".codex"}},
	"opencode":    {cliNames: []string{"opencode"}, dirs: []string{".config/opencode", ".local/share/opencode", ".opencode"}},
}

// detectQuickSetupInstalled：CLI 二进制或配置目录任一命中即视为已安装（spec §5）。
func detectQuickSetupInstalled(softwareCode, homeDir string) bool {
	rule, ok := quickSetupDetectionRules[softwareCode]
	if !ok || strings.TrimSpace(homeDir) == "" {
		return false
	}
	for _, name := range rule.cliNames {
		if _, err := quickSetupLookPathCLIFn(name); err == nil {
			return true
		}
	}
	for _, dir := range rule.dirs {
		if info, err := os.Stat(filepath.Join(homeDir, filepath.FromSlash(dir))); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}
