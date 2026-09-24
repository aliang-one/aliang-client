package services

import (
	"encoding/json"
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
			Description:        "Writes the Aliang gateway into the Claude Code env settings block.",
			SupportedProviders: []string{"anthropic"},
			Files: []models.QuickSetupSoftwareFile{
				{
					Code:        "settings",
					Label:       "settings.json",
					FileName:    "settings.json",
					DefaultPath: "~/.claude/settings.json",
					Format:      "json",
					Kind:        "file",
					Description: "Claude Code user settings; carries the gateway env block.",
				},
			},
		},
		{
			Code:               "pi",
			Name:               "Pi",
			Description:        "Generate Pi models.json with a custom Aliang gateway provider plus settings.json defaults for provider and model.",
			SupportedProviders: []string{"anthropic", "openai"},
			Files: []models.QuickSetupSoftwareFile{
				{
					Code:        "models",
					Label:       "models.json",
					FileName:    "models.json",
					DefaultPath: "~/.pi/agent/models.json",
					Format:      "json",
					Kind:        "file",
					Description: "Pi custom provider definition (Aliang gateway).",
				},
				{
					Code:        "settings",
					Label:       "settings.json",
					FileName:    "settings.json",
					DefaultPath: "~/.pi/agent/settings.json",
					Format:      "json",
					Kind:        "file",
					Description: "Pi global settings (default provider/model).",
				},
			},
		},
	}
}

// quickSetupCodexProviderID 是快速配置组合统一切换到的 codex provider id（spec §7；
// 自 quick_setup_merge.go 迁入，同包内 snapshot/catalog 模板继续引用）。
const quickSetupCodexProviderID = "aliang"

// quickSetupTOMLQuote 把值编码为 TOML 基本字符串（转义引号/反斜杠/控制字符）。
// 自 v2 merge 引擎迁入：catalog 的空白 codex 模板（quickSetupBlankCodexFiles）是
// 唯一调用方。
func quickSetupTOMLQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// quickSetupComboBlankTemplates 返回 software 的空白组合模板（含 {{base_url}}/
// {{api_key}}/{{model}} 占位符，spec §6.3）。返回的文件顺序/Code 与该 software 的
// Files 声明一一对应；未知 software 返回 nil。占位符按文件天然分布（如 codex 的
// {{api_key}} 只在 auth.json）。全部模板统一只写 {{base_url}} 占位符，不内嵌 /v1
// 拼接逻辑——变量值带不带 /v1（codex/opencode 带、claude/pi 不带）由 configure 表单
// 的预设项写进 variables.base_url（spec §6，Task 6/12 落地），模板只负责引用。
func quickSetupComboBlankTemplates(softwareCode string) []models.QuickSetupComboFile {
	switch softwareCode {
	case "claude-code":
		return quickSetupBlankClaudeFiles()
	case "codex":
		return quickSetupBlankCodexFiles()
	case "opencode":
		return quickSetupBlankOpenCodeFiles()
	case "pi":
		return quickSetupBlankPiFiles()
	default:
		return nil
	}
}

// quickSetupBlankClaudeFiles 生成 claude-code 的空白组合模板（settings.json）。
// base_url 变量值不带 /v1（Claude Code 自行追加 /v1/messages）。
func quickSetupBlankClaudeFiles() []models.QuickSetupComboFile {
	env := struct {
		BaseURL   string `json:"ANTHROPIC_BASE_URL"`
		AuthToken string `json:"ANTHROPIC_AUTH_TOKEN"`
		Model     string `json:"ANTHROPIC_MODEL"`
	}{BaseURL: "{{base_url}}", AuthToken: "{{api_key}}", Model: "{{model}}"}
	payload := struct {
		Env interface{} `json:"env"`
	}{Env: env}
	return []models.QuickSetupComboFile{
		{Code: "settings", Content: quickSetupBlankJSON(payload)},
	}
}

// quickSetupBlankCodexFiles 生成 codex 的空白组合模板（config.toml + auth.json）。
// config.toml 的 [model_providers.aliang] 段形态与 v2 merge 引擎（mergeCodexTOML，
// 已随 render 链退役）的模板形态语义一致；base_url 变量值由 configure 预设带 /v1。
func quickSetupBlankCodexFiles() []models.QuickSetupComboFile {
	configTOML := strings.Join([]string{
		"model = \"{{model}}\"",
		"model_provider = " + quickSetupTOMLQuote(quickSetupCodexProviderID),
		"approval_policy = \"never\"",
		"",
		"[model_providers." + quickSetupCodexProviderID + "]",
		"name = \"Aliang Gateway\"",
		"base_url = \"{{base_url}}\"",
		"env_key = \"OPENAI_API_KEY\"",
		"wire_api = \"responses\"",
	}, "\n") + "\n"
	auth := struct {
		OpenAIAPIKey string `json:"OPENAI_API_KEY"`
	}{OpenAIAPIKey: "{{api_key}}"}
	return []models.QuickSetupComboFile{
		{Code: "config", Content: configTOML},
		{Code: "auth", Content: quickSetupBlankJSON(auth)},
	}
}

// quickSetupBlankOpenCodeFiles 生成 opencode 的空白组合模板（opencode.json）。
// npm 写死 anthropic 的 SDK 包字面量（与 v2 quickSetupOpenCodeProviderNPM
// 的 anthropic 分支一致；该函数 Task 9 随 render.go 删除，模板不得引用它）。
func quickSetupBlankOpenCodeFiles() []models.QuickSetupComboFile {
	options := struct {
		BaseURL string `json:"baseURL"`
		APIKey  string `json:"apiKey"`
	}{BaseURL: "{{base_url}}", APIKey: "{{api_key}}"}
	provider := struct {
		NPM     string      `json:"npm"`
		Options interface{} `json:"options"`
	}{NPM: "@ai-sdk/anthropic", Options: options}
	payload := struct {
		Schema   string      `json:"$schema"`
		Model    string      `json:"model"`
		Provider interface{} `json:"provider"`
	}{
		Schema:   "https://opencode.ai/config.json",
		Model:    quickSetupCodexProviderID + "/{{model}}",
		Provider: map[string]interface{}{quickSetupCodexProviderID: provider},
	}
	return []models.QuickSetupComboFile{
		{Code: "config", Content: quickSetupBlankJSON(payload)},
	}
}

// quickSetupBlankPiFiles 生成 pi 的空白组合模板（models.json + settings.json）。
// base_url 变量值不带 /v1——pi 的 anthropic-messages API 自行追加 /v1/messages。
func quickSetupBlankPiFiles() []models.QuickSetupComboFile {
	entry := struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}{ID: "{{model}}", Name: "{{model}}"}
	provider := struct {
		Name    string      `json:"name"`
		BaseURL string      `json:"baseUrl"`
		APIKey  string      `json:"apiKey"`
		API     string      `json:"api"`
		Models  interface{} `json:"models"`
	}{
		Name:    "Aliang Gateway",
		BaseURL: "{{base_url}}",
		APIKey:  "{{api_key}}",
		API:     "anthropic-messages",
		Models:  []interface{}{entry},
	}
	modelsPayload := struct {
		Providers interface{} `json:"providers"`
	}{Providers: map[string]interface{}{quickSetupCodexProviderID: provider}}
	settings := struct {
		DefaultProvider string `json:"defaultProvider"`
		DefaultModel    string `json:"defaultModel"`
	}{DefaultProvider: quickSetupCodexProviderID, DefaultModel: "{{model}}"}
	return []models.QuickSetupComboFile{
		{Code: "models", Content: quickSetupBlankJSON(modelsPayload)},
		{Code: "settings", Content: quickSetupBlankJSON(settings)},
	}
}

// quickSetupBlankJSON 把静态模板结构体序列化为 2 空格缩进的 JSON 文本。
// 输入只含字符串/数组/map 字面量，MarshalIndent 不可失败；兜底返回空串
// 由模板测试立即暴露。
func quickSetupBlankJSON(v interface{}) string {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	return string(raw)
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
	allowed, err := quickSetupBuiltInPathAllowed(software, canonicalTarget, home)
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", errors.New("file path is not valid: target is not a declared config file for this software")
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
		return filepath.Join(home, ".claude"), nil
	case "pi":
		return filepath.Join(home, ".pi", "agent"), nil
	default:
		return "", fmt.Errorf("quick setup software %q is not supported", software)
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
	"pi":          {cliNames: []string{"pi"}, dirs: []string{".pi"}},
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
