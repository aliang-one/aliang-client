package services

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/models"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

// stubQuickSetupRenderEnv 注入 Render 全链路依赖（镜像 quick_setup_service_test.go
// 的既有钩子模式）：全局 config 的 api_server、keys stub、目标用户家目录 stub。
// 显式 stub targetUser 是硬要求——否则 Render 会读到开发者本机的真实配置文件，
// 测试结果随机器状态漂移。
func stubQuickSetupRenderEnv(t *testing.T, home string, keys []auth.UserAPIKey) {
	t.Helper()
	config.ResetGlobalConfigForTest()
	config.SetGlobalConfig(&config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.example.com"},
	})
	t.Cleanup(config.ResetGlobalConfigForTest)

	previousKeys := quickSetupGetAPIKeysFn
	quickSetupGetAPIKeysFn = func() ([]auth.UserAPIKey, error) { return keys, nil }
	t.Cleanup(func() { quickSetupGetAPIKeysFn = previousKeys })

	previousUser := quickSetupTargetUserFn
	quickSetupTargetUserFn = func() (quickSetupTargetUser, error) {
		return quickSetupTargetUser{homeDir: home}, nil
	}
	t.Cleanup(func() { quickSetupTargetUserFn = previousUser })
}

// snapshotHomeTree 收集目录下所有普通文件的 内容快照，用于断言 Render 全程只读。
func snapshotHomeTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			snap[strings.TrimPrefix(path, root)] = string(raw)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestRenderClaudeSettingsEnv(t *testing.T) {
	payload := renderClaudeSettingsEnv("sk-aliang", "claude-sonnet-4-5-20250929", "https://api.aliang.one")
	// env 必须是 map[string]interface{}：深合并引擎只对这种类型逐键并入用户既有 env
	// 块（json.Unmarshal 的产物即此类型），map[string]string 会被当成标量整块替换。
	env, ok := payload["env"].(map[string]interface{})
	if !ok {
		t.Fatalf("env block missing: %#v", payload)
	}
	if env["ANTHROPIC_BASE_URL"] != "https://api.aliang.one" {
		t.Fatalf("base url: %v", env)
	}
	if env["ANTHROPIC_AUTH_TOKEN"] != "sk-aliang" {
		t.Fatal("auth token missing")
	}
	if _, has := env["ANTHROPIC_API_KEY"]; has {
		t.Fatal("must use AUTH_TOKEN, not API_KEY")
	}
	if env["ANTHROPIC_MODEL"] != "claude-sonnet-4-5-20250929" {
		t.Fatal("model missing")
	}
}

func TestRenderClaudeCodeFiles(t *testing.T) {
	softwareDef, ok := findQuickSetupSoftware("claude-code")
	if !ok {
		t.Fatal("claude-code missing from catalog")
	}
	apiKey := models.QuickSetupAPIKey{Key: "sk-test", Provider: "anthropic"}

	cases := []struct {
		name    string
		apiRoot string
	}{
		{name: "inference root", apiRoot: "https://api.aliang.one"},
		{name: "api root with /v1 suffix", apiRoot: "https://api.aliang.one/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files, notes, err := renderClaudeCodeFiles(softwareDef, apiKey, tc.apiRoot, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 1 {
				t.Fatalf("want a single file, got %d", len(files))
			}
			file := files[0]
			if file.Code != "settings" {
				t.Fatalf("code: %s", file.Code)
			}
			if file.Format != "json" {
				t.Fatalf("format: %s", file.Format)
			}
			if file.Path != "~/.claude/settings.json" {
				t.Fatalf("path: %s", file.Path)
			}
			var parsed struct {
				Env map[string]string `json:"env"`
			}
			if err := json.Unmarshal([]byte(file.Content), &parsed); err != nil {
				t.Fatal(err)
			}
			if parsed.Env["ANTHROPIC_BASE_URL"] != "https://api.aliang.one" {
				t.Fatalf("base url must not carry /v1: %v", parsed.Env)
			}
			if parsed.Env["ANTHROPIC_AUTH_TOKEN"] != "sk-test" {
				t.Fatalf("auth token missing: %v", parsed.Env)
			}
			if len(notes) == 0 {
				t.Fatal("notes must not be empty")
			}
		})
	}
}

func TestQuickSetupModeRoot(t *testing.T) {
	if got := quickSetupModeRoot("local", "https://backend.aliang.one"); got != "http://127.0.0.1:56432" {
		t.Fatalf("local root: %s", got)
	}
	if got := quickSetupModeRoot("public", "https://backend.aliang.one"); got != "https://api.aliang.one" {
		t.Fatalf("public root: %s", got)
	}
	if got := quickSetupModeRoot("", "https://backend.aliang.one"); got != "https://api.aliang.one" {
		t.Fatalf("default must be public: %s", got)
	}
	if got := quickSetupModeRoot("bogus", "https://backend.aliang.one"); got != "https://api.aliang.one" {
		t.Fatalf("unknown mode falls back to public: %s", got)
	}
	if got := quickSetupModeRoot(" Local ", "https://backend.aliang.one"); got != "http://127.0.0.1:56432" {
		t.Fatalf("mixed-case/padded mode must match local: %s", got)
	}
}

func TestQuickSetupV1SuffixPerSoftware(t *testing.T) {
	// codex/opencode: root + /v1；claude: root 原样（行为变更，spec §7.1，Task 7 已落地 claude 侧）
	if got := quickSetupProviderBaseURL("anthropic", "http://127.0.0.1:56432"); got != "http://127.0.0.1:56432/v1" {
		t.Fatalf("codex/opencode: %s", got)
	}
}

func TestQuickSetupService_Render_LocalModeSwitchesRoot(t *testing.T) {
	config.ResetGlobalConfigForTest()
	config.SetGlobalConfig(&config.Config{
		Core: &config.CoreConfig{APIServer: "https://backend.aliang.one"},
	})
	t.Cleanup(config.ResetGlobalConfigForTest)

	previous := quickSetupGetAPIKeysFn
	quickSetupGetAPIKeysFn = func() ([]auth.UserAPIKey, error) {
		return []auth.UserAPIKey{
			{ID: 22, Key: "sk-ant-real", Name: "Anthropic Key", Status: "active", Provider: "anthropic", SecretAvailable: true},
		}, nil
	}
	t.Cleanup(func() { quickSetupGetAPIKeysFn = previous })

	// Render 会读磁盘现有配置：隔离到临时家目录，防止读到开发者本机真实配置。
	stubHome := t.TempDir()
	previousUser := quickSetupTargetUserFn
	quickSetupTargetUserFn = func() (quickSetupTargetUser, error) {
		return quickSetupTargetUser{homeDir: stubHome}, nil
	}
	t.Cleanup(func() { quickSetupTargetUserFn = previousUser })

	svc := NewQuickSetupService()

	// claude：modeRoot 原样，不带 /v1（Claude Code 自行追加 /v1/messages）。
	claudeResp, err := svc.Render(models.QuickSetupRenderRequest{Software: "claude-code", KeyIDs: []int64{22}, Mode: "local"})
	if err != nil {
		t.Fatalf("render claude-code failed: %v", err)
	}
	var claudeCfg struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal([]byte(claudeResp.Variants[0].Files[0].Content), &claudeCfg); err != nil {
		t.Fatal(err)
	}
	if got, want := claudeCfg.Env["ANTHROPIC_BASE_URL"], "http://127.0.0.1:56432"; got != want {
		t.Fatalf("claude local base url = %q, want %q", got, want)
	}

	// codex：modeRoot 经 quickSetupProviderBaseURL 自动 +/v1。
	codexResp, err := svc.Render(models.QuickSetupRenderRequest{Software: "codex", KeyIDs: []int64{22}, Mode: "local"})
	if err != nil {
		t.Fatalf("render codex failed: %v", err)
	}
	if !strings.Contains(codexResp.Variants[0].Files[0].Content, "base_url = \"http://127.0.0.1:56432/v1\"") {
		t.Fatalf("codex local base_url missing: %s", codexResp.Variants[0].Files[0].Content)
	}

	// opencode：provider options.baseURL = modeRoot + /v1。
	openCodeResp, err := svc.Render(models.QuickSetupRenderRequest{Software: "opencode", KeyIDs: []int64{22}, Mode: "local"})
	if err != nil {
		t.Fatalf("render opencode failed: %v", err)
	}
	var openCfg map[string]interface{}
	if err := json.Unmarshal([]byte(openCodeResp.Variants[0].Files[0].Content), &openCfg); err != nil {
		t.Fatal(err)
	}
	providers := openCfg["provider"].(map[string]interface{})
	for id, raw := range providers {
		provider := raw.(map[string]interface{})
		options := provider["options"].(map[string]interface{})
		if got, want := options["baseURL"], "http://127.0.0.1:56432/v1"; got != want {
			t.Fatalf("%s baseURL = %#v, want %q", id, got, want)
		}
	}
}

func TestRenderMergesFromDisk(t *testing.T) {
	home := t.TempDir()
	writeBackupFixture(t, home, ".codex/config.toml", "# user\nmodel = \"gpt-4o\"\n[mcp_servers.fs]\ncommand=\"uvx\"\n")
	stubQuickSetupRenderEnv(t, home, []auth.UserAPIKey{
		{ID: 1, Key: "sk-openai-real", Name: "OpenAI Key", Status: "active", Provider: "openai", SecretAvailable: true},
	})

	before := snapshotHomeTree(t, home)
	resp, err := (&QuickSetupService{}).Render(models.QuickSetupRenderRequest{
		Software: "codex", KeyIDs: []int64{1}, Mode: "public",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Render 是只读预览：全程不得改动磁盘上任何文件。
	if after := snapshotHomeTree(t, home); !reflect.DeepEqual(before, after) {
		t.Fatalf("Render mutated the disk:\nbefore: %#v\nafter:  %#v", before, after)
	}

	if len(resp.Variants) == 0 {
		t.Fatal("no variants")
	}
	cfg := resp.Variants[0].Files[0]
	if !cfg.MergedFromDisk {
		t.Fatal("config.toml should be merged from disk")
	}
	if !strings.Contains(cfg.Content, "# user") || !strings.Contains(cfg.Content, "mcp_servers.fs") {
		t.Fatalf("user content lost:\n%s", cfg.Content)
	}
	if !strings.Contains(cfg.Content, "[model_providers.aliang]") {
		t.Fatalf("gateway section missing from merged preview:\n%s", cfg.Content)
	}
	auth := resp.Variants[0].Files[1]
	if auth.MergedFromDisk {
		t.Fatal("auth.json has no disk file")
	}
	if !strings.Contains(auth.Content, `"OPENAI_API_KEY": "sk-openai-real"`) {
		t.Fatalf("auth template missing the key:\n%s", auth.Content)
	}
}

func TestRenderFallsBackOnBrokenDiskJSON(t *testing.T) {
	home := t.TempDir()
	writeBackupFixture(t, home, ".config/opencode/opencode.json", "{broken")
	stubQuickSetupRenderEnv(t, home, []auth.UserAPIKey{
		{ID: 1, Key: "sk-openai-real", Name: "OpenAI Key", Status: "active", Provider: "openai", SecretAvailable: true},
	})

	resp, err := (&QuickSetupService{}).Render(models.QuickSetupRenderRequest{
		Software: "opencode", KeyIDs: []int64{1},
	})
	if err != nil {
		t.Fatalf("broken disk JSON must not fail the render: %v", err)
	}
	file := resp.Variants[0].Files[0]
	if file.MergedFromDisk {
		t.Fatal("unparseable disk content must not be marked merged_from_disk")
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(file.Content), &cfg); err != nil {
		t.Fatalf("fallback content must be valid template JSON: %v\n%s", err, file.Content)
	}
	if _, ok := cfg["provider"]; !ok {
		t.Fatalf("fallback template missing provider:\n%s", file.Content)
	}
	notes := strings.Join(resp.Variants[0].Notes, "\n")
	if !strings.Contains(notes, "Could not parse your existing opencode.json") {
		t.Fatalf("degraded merge warning missing from notes:\n%s", notes)
	}
}

// 对抗检查（Task 9 评审三态）：磁盘 config.toml 存在但读不了（chmod 000）→ 模板
// 形态、MergedFromDisk=false、notes 点名 config.toml 的读盘警告。root 下 chmod 000
// 仍可读，改用目录占位命中同一「非普通文件」unreadable 分支。
func TestRenderFallsBackOnUnreadableFileWithNote(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		if err := os.Mkdir(cfgPath, 0o700); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(cfgPath, []byte("model = \"gpt-4o\"\n"), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(cfgPath, 0o600) })
	}
	stubQuickSetupRenderEnv(t, home, []auth.UserAPIKey{
		{ID: 1, Key: "sk-openai-real", Name: "OpenAI Key", Status: "active", Provider: "openai", SecretAvailable: true},
	})

	resp, err := (&QuickSetupService{}).Render(models.QuickSetupRenderRequest{
		Software: "codex", KeyIDs: []int64{1},
	})
	if err != nil {
		t.Fatalf("unreadable disk file must not fail the render: %v", err)
	}
	file := resp.Variants[0].Files[0]
	if file.MergedFromDisk {
		t.Fatal("unreadable disk content must not be marked merged_from_disk")
	}
	if !strings.Contains(file.Content, "[model_providers.aliang]") {
		t.Fatalf("unreadable disk content must fall back to the template form:\n%s", file.Content)
	}
	notes := strings.Join(resp.Variants[0].Notes, "\n")
	if !strings.Contains(notes, "Could not read the existing config.toml on disk; showing a fresh template instead.") {
		t.Fatalf("unreadable file warning missing from notes:\n%s", notes)
	}
}

// 0 字节的已存在文件归 missing 语义（Task 9 评审）：无内容可保留，横幅不该说
// 「已合并」，也不该触发解析失败警告（JSON 侧空串 unmarshal 必失败，走的是本语义）。
func TestRenderZeroByteFileIsMissing(t *testing.T) {
	home := t.TempDir()
	writeBackupFixture(t, home, ".codex/config.toml", "")
	writeBackupFixture(t, home, ".codex/auth.json", "")
	stubQuickSetupRenderEnv(t, home, []auth.UserAPIKey{
		{ID: 1, Key: "sk-openai-real", Name: "OpenAI Key", Status: "active", Provider: "openai", SecretAvailable: true},
	})

	resp, err := (&QuickSetupService{}).Render(models.QuickSetupRenderRequest{
		Software: "codex", KeyIDs: []int64{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if configFile := resp.Variants[0].Files[0]; configFile.MergedFromDisk {
		t.Fatal("zero-byte config.toml must be treated as missing, not merged_from_disk")
	}
	if authFile := resp.Variants[0].Files[1]; authFile.MergedFromDisk {
		t.Fatal("zero-byte auth.json must be treated as missing, not merged_from_disk")
	}
	notes := strings.Join(resp.Variants[0].Notes, "\n")
	for _, unwanted := range []string{"Could not read the existing", "Could not parse your existing"} {
		if strings.Contains(notes, unwanted) {
			t.Fatalf("zero-byte files must not raise degradation warnings:\n%s", notes)
		}
	}
}

// 对抗检查（spec §7 单鉴权源）：磁盘 settings.json 带 hooks/permissions 与残留 env 时，
// 合并须保住 hooks/permissions 与用户自定义 env、注入我们的 env 键，并删除残留的
// ANTHROPIC_API_KEY（我们用 AUTH_TOKEN 接管鉴权，双鉴权源会造成歧义）。
func TestRenderClaudeMergesAndStripsLegacyAPIKey(t *testing.T) {
	home := t.TempDir()
	writeBackupFixture(t, home, ".claude/settings.json", `{
  "permissions": {"allow": ["Bash(ls:*)"]},
  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "say done"}]}]},
  "env": {"ANTHROPIC_API_KEY": "sk-legacy", "MY_CUSTOM": "keep-me"}
}`)
	stubQuickSetupRenderEnv(t, home, []auth.UserAPIKey{
		{ID: 1, Key: "sk-ant-real", Name: "Anthropic Key", Status: "active", Provider: "anthropic", SecretAvailable: true},
	})

	resp, err := (&QuickSetupService{}).Render(models.QuickSetupRenderRequest{
		Software: "claude-code", KeyIDs: []int64{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	file := resp.Variants[0].Files[0]
	if !file.MergedFromDisk {
		t.Fatal("settings.json should be merged from disk")
	}
	var cfg struct {
		Permissions map[string]interface{} `json:"permissions"`
		Hooks       map[string]interface{} `json:"hooks"`
		Env         map[string]string      `json:"env"`
	}
	if err := json.Unmarshal([]byte(file.Content), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Permissions) == 0 || len(cfg.Hooks) == 0 {
		t.Fatalf("user hooks/permissions lost:\n%s", file.Content)
	}
	if cfg.Env["MY_CUSTOM"] != "keep-me" {
		t.Fatalf("custom env var lost: %#v", cfg.Env)
	}
	if cfg.Env["ANTHROPIC_AUTH_TOKEN"] != "sk-ant-real" {
		t.Fatalf("gateway env not injected: %#v", cfg.Env)
	}
	if _, has := cfg.Env["ANTHROPIC_API_KEY"]; has {
		t.Fatalf("legacy ANTHROPIC_API_KEY must be stripped:\n%s", file.Content)
	}
}

// 对抗检查：磁盘是旧版 quick setup 产物（openai 表）→ 旧表保留、model_provider 切到
// aliang、[model_providers.aliang] 段生成；auth.json 同样深合并保住 ChatGPT 登录态。
func TestRenderCodexMergesLegacyQuickSetupConfig(t *testing.T) {
	home := t.TempDir()
	writeBackupFixture(t, home, ".codex/config.toml",
		"model = \"gpt-5-codex\"\nmodel_provider = \"openai\"\napproval_policy = \"never\"\n\n[model_providers.openai]\nname = \"OpenAI\"\nbase_url = \"https://api.example.com/v1\"\nwire_api = \"responses\"\n\n")
	writeBackupFixture(t, home, ".codex/auth.json",
		`{"OPENAI_API_KEY":null,"tokens":{"access_token":"at-1","account_id":"acc"},"last_refresh":"2026-01-01"}`)
	stubQuickSetupRenderEnv(t, home, []auth.UserAPIKey{
		{ID: 1, Key: "sk-openai-real", Name: "OpenAI Key", Status: "active", Provider: "openai", SecretAvailable: true},
	})

	resp, err := (&QuickSetupService{}).Render(models.QuickSetupRenderRequest{
		Software: "codex", KeyIDs: []int64{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	configFile := resp.Variants[0].Files[0]
	if !configFile.MergedFromDisk {
		t.Fatal("config.toml should be merged from disk")
	}
	for _, want := range []string{
		"[model_providers.openai]", // 用户自己的旧 provider 表原样保留
		`model_provider = "aliang"`,
		"[model_providers.aliang]",
	} {
		if !strings.Contains(configFile.Content, want) {
			t.Fatalf("missing %q in:\n%s", want, configFile.Content)
		}
	}
	authFile := resp.Variants[0].Files[1]
	if !authFile.MergedFromDisk {
		t.Fatal("auth.json should be merged from disk")
	}
	var authCfg map[string]interface{}
	if err := json.Unmarshal([]byte(authFile.Content), &authCfg); err != nil {
		t.Fatal(err)
	}
	if authCfg["OPENAI_API_KEY"] != "sk-openai-real" {
		t.Fatalf("OPENAI_API_KEY not injected: %#v", authCfg)
	}
	tokens, ok := authCfg["tokens"].(map[string]interface{})
	if !ok || tokens["access_token"] != "at-1" {
		t.Fatalf("existing tokens lost: %#v", authCfg)
	}
}

// 对抗检查：opencode 磁盘 JSON 顶层是数组（非 object）→ unmarshal 成 map 必失败 →
// 走模板降级路径，不报错且给出降级警告。
func TestRenderOpenCodeFallsBackOnArrayTopLevelJSON(t *testing.T) {
	home := t.TempDir()
	writeBackupFixture(t, home, ".config/opencode/opencode.json", "[1, 2, 3]")
	stubQuickSetupRenderEnv(t, home, []auth.UserAPIKey{
		{ID: 1, Key: "sk-openai-real", Name: "OpenAI Key", Status: "active", Provider: "openai", SecretAvailable: true},
	})

	resp, err := (&QuickSetupService{}).Render(models.QuickSetupRenderRequest{
		Software: "opencode", KeyIDs: []int64{1},
	})
	if err != nil {
		t.Fatalf("array top-level disk JSON must not fail the render: %v", err)
	}
	file := resp.Variants[0].Files[0]
	if file.MergedFromDisk {
		t.Fatal("array top-level disk JSON must not be marked merged_from_disk")
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(file.Content), &cfg); err != nil {
		t.Fatalf("fallback content must be valid template JSON: %v\n%s", err, file.Content)
	}
	if _, ok := cfg["provider"]; !ok {
		t.Fatalf("fallback template missing provider:\n%s", file.Content)
	}
	if notes := strings.Join(resp.Variants[0].Notes, "\n"); !strings.Contains(notes, "Could not parse your existing opencode.json") {
		t.Fatalf("degraded merge warning missing from notes:\n%s", notes)
	}
}

// 对抗检查（Task 6 审查红线）：磁盘 config.toml 让 mergeCodexTOML 的 TOML 闸门报错
// （未闭合多行字符串）时，Render 必须显式降级为模板形态 + 人话警告，绝不静默吞掉、
// 也绝不产出可能损坏用户配置的内容。
func TestRenderCodexFallsBackWhenTOMLGateRejects(t *testing.T) {
	home := t.TempDir()
	writeBackupFixture(t, home, ".codex/config.toml", "instructions = \"\"\"\nsome retained text\n")
	stubQuickSetupRenderEnv(t, home, []auth.UserAPIKey{
		{ID: 1, Key: "sk-openai-real", Name: "OpenAI Key", Status: "active", Provider: "openai", SecretAvailable: true},
	})

	resp, err := (&QuickSetupService{}).Render(models.QuickSetupRenderRequest{
		Software: "codex", KeyIDs: []int64{1},
	})
	if err != nil {
		t.Fatalf("TOML gate failure must not fail the render: %v", err)
	}
	file := resp.Variants[0].Files[0]
	if file.MergedFromDisk {
		t.Fatal("gate-rejected disk content must not be marked merged_from_disk")
	}
	if !strings.Contains(file.Content, "[model_providers.aliang]") {
		t.Fatalf("degraded preview must be the unified template form:\n%s", file.Content)
	}
	if strings.Contains(file.Content, "some retained text") {
		t.Fatalf("unmergeable disk content must not leak into the preview:\n%s", file.Content)
	}
	notes := strings.Join(resp.Variants[0].Notes, "\n")
	if !strings.Contains(notes, "could not be merged safely") {
		t.Fatalf("gate degradation warning missing from notes:\n%s", notes)
	}
}

// Render 是只读预览，不应因本机环境失败：解析不到目标用户时全部走模板兜底。
func TestRenderToleratesTargetUserFailure(t *testing.T) {
	stubQuickSetupRenderEnv(t, t.TempDir(), []auth.UserAPIKey{
		{ID: 1, Key: "sk-openai-real", Name: "OpenAI Key", Status: "active", Provider: "openai", SecretAvailable: true},
	})
	previousUser := quickSetupTargetUserFn
	quickSetupTargetUserFn = func() (quickSetupTargetUser, error) {
		return quickSetupTargetUser{}, errors.New("no logged-in macOS console user is available")
	}
	t.Cleanup(func() { quickSetupTargetUserFn = previousUser })

	resp, err := (&QuickSetupService{}).Render(models.QuickSetupRenderRequest{
		Software: "codex", KeyIDs: []int64{1},
	})
	if err != nil {
		t.Fatalf("target user failure must not fail the render: %v", err)
	}
	configFile := resp.Variants[0].Files[0]
	if configFile.MergedFromDisk {
		t.Fatal("template fallback must not be marked merged_from_disk")
	}
	if !strings.Contains(configFile.Content, "[model_providers.aliang]") {
		t.Fatalf("fresh install must still produce the unified aliang section:\n%s", configFile.Content)
	}
	if authFile := resp.Variants[0].Files[1]; authFile.MergedFromDisk {
		t.Fatal("auth.json template fallback must not be marked merged_from_disk")
	}
}
