package services

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubConfigStateEnv 注入 ConfigState 依赖（镜像 quick_setup_apply_test.go 的钩子
// 模式）：非空鉴权头 + 目标用户家目录。显式 stub targetUser 是硬要求——否则
// ConfigState 会读到开发者本机的真实配置文件，测试随机器状态漂移。
func stubConfigStateEnv(t *testing.T, home string) {
	t.Helper()
	previousAuth := quickSetupAuthorizationHeaderFn
	previousTargetUser := quickSetupTargetUserFn
	quickSetupAuthorizationHeaderFn = func() string { return "Bearer test-access" }
	quickSetupTargetUserFn = func() (quickSetupTargetUser, error) {
		return quickSetupTargetUser{homeDir: home}, nil
	}
	t.Cleanup(func() {
		quickSetupAuthorizationHeaderFn = previousAuth
		quickSetupTargetUserFn = previousTargetUser
	})
}

func TestConfigState(t *testing.T) {
	home := t.TempDir()
	writeBackupFixture(t, home, ".codex/config.toml", "model=\"x\"\n[model_providers.aliang]\nname=\"Aliang Gateway\"\n")
	stubConfigStateEnv(t, home)

	state, err := (&QuickSetupService{}).ConfigState("codex")
	if err != nil {
		t.Fatal(err)
	}
	if state.Software != "codex" {
		t.Fatalf("software: %s", state.Software)
	}
	if len(state.Files) != 2 {
		t.Fatalf("files: %+v", state.Files)
	}
	cfg := state.Files[0] // 顺序与软件定义一致：config.toml 在前
	if !cfg.Exists || !cfg.ManagedByAliang {
		t.Fatalf("config state: %+v", cfg)
	}
	if cfg.Content == "" || cfg.Path != "~/.codex/config.toml" {
		t.Fatalf("config content/path: %+v", cfg)
	}
	if cfg.Format != "toml" {
		t.Fatalf("config format: %+v", cfg)
	}
	if cfg.Size <= 0 {
		t.Fatalf("config size must reflect disk file: %+v", cfg)
	}
	if cfg.ModifiedAt == "" {
		t.Fatalf("config modified_at must be set: %+v", cfg)
	}
	auth := state.Files[1]
	if auth.Exists {
		t.Fatal("auth.json should not exist")
	}
	// 整体托管语义：auth.json 跟随同 software 的 config.toml 判定结果
	if !auth.ManagedByAliang {
		t.Fatalf("auth must follow managed config.toml: %+v", auth)
	}
	if len(state.Backups) != 0 {
		t.Fatalf("backups should be empty: %+v", state.Backups)
	}
}

func TestConfigStateUnauthenticated(t *testing.T) {
	previousAuth := quickSetupAuthorizationHeaderFn
	quickSetupAuthorizationHeaderFn = func() string { return "" }
	t.Cleanup(func() { quickSetupAuthorizationHeaderFn = previousAuth })

	_, err := (&QuickSetupService{}).ConfigState("codex")
	if !errors.Is(err, ErrQuickSetupUnauthenticated) {
		t.Fatalf("err = %v, want ErrQuickSetupUnauthenticated", err)
	}
}

func TestConfigStateUnknownSoftware(t *testing.T) {
	home := t.TempDir()
	stubConfigStateEnv(t, home)

	_, err := (&QuickSetupService{}).ConfigState("bogus")
	if err == nil {
		t.Fatal("unknown software must error")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("error must name the software: %v", err)
	}

	// managed_by_aliang 负例：磁盘上 config.toml 无 aliang 段 → ManagedByAliang=false，
	// auth.json 跟随 config 同样为 false
	writeBackupFixture(t, home, ".codex/config.toml", "model = \"gpt-5\"\n")
	state, err := (&QuickSetupService{}).ConfigState("codex")
	if err != nil {
		t.Fatal(err)
	}
	if state.Files[0].ManagedByAliang {
		t.Fatalf("config without aliang section must not be managed: %+v", state.Files[0])
	}
	if state.Files[1].ManagedByAliang {
		t.Fatalf("auth follows unmanaged config: %+v", state.Files[1])
	}

	// backups 区：预置 manifest（用 backupQuickSetupFiles 生成）→ state.Backups 有条目
	writeBackupFixture(t, home, ".codex/config.toml", "user original")
	files := []quickSetupPreparedFile{{code: "config", path: filepath.Join(home, ".codex/config.toml"), content: "aliang"}}
	if _, err := backupQuickSetupFiles(quickSetupTargetUser{homeDir: home}, "codex", files); err != nil {
		t.Fatal(err)
	}
	state, err = (&QuickSetupService{}).ConfigState("codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Backups) != 1 {
		t.Fatalf("backups: %+v", state.Backups)
	}
	backup := state.Backups[0]
	if backup.OriginalPath != "~/.codex/config.toml" || backup.BackupPath == "" || backup.BackedUpAt == "" || backup.Kind != "original" {
		t.Fatalf("backup entry: %+v", backup)
	}
}

// TestConfigStateManagedByAliangClaudeCode 对抗检查（spec §8）：本机 mode=local 写出的
// settings（env.ANTHROPIC_BASE_URL=http://127.0.0.1:56432）必须判 managed=true；
// 用户自配第三方网关（base_url 指向其他域名）必须 false。
func TestConfigStateManagedByAliangClaudeCode(t *testing.T) {
	cases := []struct {
		name    string
		content string
		managed bool
	}{
		{
			name:    "local mode loopback proxy",
			content: `{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:56432","ANTHROPIC_AUTH_TOKEN":"sk-x"}}`,
			managed: true,
		},
		{
			name:    "local mode localhost alias",
			content: `{"env":{"ANTHROPIC_BASE_URL":"http://localhost:56432"}}`,
			managed: true,
		},
		{
			name:    "public inference domain",
			content: `{"env":{"ANTHROPIC_BASE_URL":"https://api.aliang.one"}}`,
			managed: true,
		},
		{
			name:    "third-party gateway",
			content: `{"env":{"ANTHROPIC_BASE_URL":"https://third-party.example.com/v1"}}`,
			managed: false,
		},
		{
			name:    "broken json",
			content: "{not json",
			managed: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeBackupFixture(t, home, ".claude/settings.json", tc.content)
			stubConfigStateEnv(t, home)

			state, err := (&QuickSetupService{}).ConfigState("claude-code")
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Files) != 1 {
				t.Fatalf("files: %+v", state.Files)
			}
			if got := state.Files[0].ManagedByAliang; got != tc.managed {
				t.Fatalf("managed = %v, want %v; file: %+v", got, tc.managed, state.Files[0])
			}
		})
	}
}

// TestConfigStateManagedByAliangOpenCode 锁定 opencode 分支：任一 provider 条目的
// options.baseURL 命中网关地址即 managed；全部指向第三方则 false。
func TestConfigStateManagedByAliangOpenCode(t *testing.T) {
	managed := `{"provider":{"aliang-openai-key":{"npm":"@ai-sdk/openai-compatible","options":{"baseURL":"https://api.aliang.one/v1","apiKey":"sk-x"}}},"model":"aliang-openai-key/gpt-5.4"}`
	thirdParty := `{"provider":{"other":{"npm":"@ai-sdk/openai-compatible","options":{"baseURL":"https://third-party.example.com/v1","apiKey":"sk-x"}}}}`

	for _, tc := range []struct {
		name    string
		content string
		managed bool
	}{
		{name: "gateway provider", content: managed, managed: true},
		{name: "third-party only", content: thirdParty, managed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeBackupFixture(t, home, ".config/opencode/opencode.json", tc.content)
			stubConfigStateEnv(t, home)

			state, err := (&QuickSetupService{}).ConfigState("opencode")
			if err != nil {
				t.Fatal(err)
			}
			if got := state.Files[0].ManagedByAliang; got != tc.managed {
				t.Fatalf("managed = %v, want %v; file: %+v", got, tc.managed, state.Files[0])
			}
		})
	}
}

// TestConfigStateManagedByAliangPi 锁定 pi 分支（spec §8）：models.json 的
// providers 键下存在 aliang 条目即 managed；settings.json 跟随 models.json 的
// 判定（整体托管语义，同 codex auth 跟随 config）——即使 settings.json 自身
// 内容不含任何指纹也判 true；models.json 无 aliang 条目/损坏时两者均 false。
func TestConfigStateManagedByAliangPi(t *testing.T) {
	cases := []struct {
		name       string
		modelsJSON string
		settings   string
		managed    bool
	}{
		{
			name:       "gateway provider",
			modelsJSON: `{"providers":{"aliang":{"name":"Aliang Gateway","baseUrl":"https://api.aliang.one","api":"anthropic-messages","models":[]}}}`,
			settings:   `{"defaultProvider":"aliang","defaultModel":"gpt-5"}`,
			managed:    true,
		},
		{
			name:       "settings follow managed models even without own fingerprint",
			modelsJSON: `{"providers":{"aliang":{"name":"Aliang Gateway"}}}`,
			settings:   `{"theme":"dark"}`,
			managed:    true,
		},
		{
			name:       "third-party provider only",
			modelsJSON: `{"providers":{"someone-else":{"name":"Other","baseUrl":"https://api.aliang.one"}}}`,
			settings:   `{"defaultProvider":"someone-else"}`,
			managed:    false,
		},
		{
			name:       "no providers key",
			modelsJSON: `{"models":[]}`,
			settings:   `{}`,
			managed:    false,
		},
		{
			name:       "broken models.json",
			modelsJSON: "{not json",
			settings:   `{"defaultProvider":"aliang"}`,
			managed:    false,
		},
		{
			name:       "providers aliang not an object still counts",
			modelsJSON: `{"providers":{"aliang":null}}`,
			settings:   `{}`,
			managed:    true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeBackupFixture(t, home, ".pi/agent/models.json", tc.modelsJSON)
			writeBackupFixture(t, home, ".pi/agent/settings.json", tc.settings)
			stubConfigStateEnv(t, home)

			state, err := (&QuickSetupService{}).ConfigState("pi")
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Files) != 2 {
				t.Fatalf("files: %+v", state.Files)
			}
			models := state.Files[0] // 顺序与软件定义一致：models.json 在前
			settings := state.Files[1]
			if got := models.ManagedByAliang; got != tc.managed {
				t.Fatalf("models managed = %v, want %v; file: %+v", got, tc.managed, models)
			}
			if got := settings.ManagedByAliang; got != tc.managed {
				t.Fatalf("settings must follow models (%v), got %v; file: %+v", tc.managed, got, settings)
			}
		})
	}

	// models.json 缺失 → models 与 settings 均 false（无内容可判，整体不托管）。
	home := t.TempDir()
	stubConfigStateEnv(t, home)
	state, err := (&QuickSetupService{}).ConfigState("pi")
	if err != nil {
		t.Fatal(err)
	}
	if state.Files[0].ManagedByAliang || state.Files[1].ManagedByAliang {
		t.Fatalf("missing models.json must leave both unmanaged: %+v", state.Files)
	}
}

// TestQuickSetupPiModelsManaged 直测 pi models.json 指纹判定：providers.aliang
// 键存在即命中（值形态不限），其余 provider 名不误伤；坏 JSON/空内容 false。
func TestQuickSetupPiModelsManaged(t *testing.T) {
	cases := []struct {
		name    string
		content string
		managed bool
	}{
		{name: "aliang provider", content: `{"providers":{"aliang":{"name":"Aliang Gateway","api":"anthropic-messages"}}}`, managed: true},
		{name: "aliang among others", content: `{"providers":{"openai":{"name":"x"},"aliang":{"name":"Aliang Gateway"}}}`, managed: true},
		{name: "third-party only", content: `{"providers":{"someone-else":{"name":"Other"}}}`, managed: false},
		{name: "suffix forgery not matched", content: `{"providers":{"aliang_other":{"name":"fake"}}}`, managed: false},
		{name: "top level array", content: `[1,2,3]`, managed: false},
		{name: "broken json", content: "{broken", managed: false},
		{name: "empty content", content: "", managed: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := quickSetupPiModelsManaged(tc.content); got != tc.managed {
				t.Fatalf("quickSetupPiModelsManaged(%q) = %v, want %v", tc.content, got, tc.managed)
			}
		})
	}
}

// TestRestoreService 端到端服务层路径（spec §6.3-3）：apply 前备份 → 磁盘被我们覆写 →
// Restore 整体还原（existed_before=true 复制回去；false 删除）并清空 manifest 条目。
func TestRestoreService(t *testing.T) {
	home := t.TempDir()
	// 预置 ~/.codex/config.toml = "user original"，auth.json 不存在
	writeBackupFixture(t, home, ".codex/config.toml", "user original")
	// 构造 prepared 并调 backupQuickSetupFiles（Task 4 helper）生成 manifest + 备份文件
	files := []quickSetupPreparedFile{
		{code: "config", path: filepath.Join(home, ".codex/config.toml"), content: "aliang"},
		{code: "auth", path: filepath.Join(home, ".codex/auth.json"), content: `{"aliang":true}`},
	}
	if _, err := backupQuickSetupFiles(quickSetupTargetUser{homeDir: home}, "codex", files); err != nil {
		t.Fatal(err)
	}
	// 模拟「已被我们配置过」：config.toml 覆写为 aliang 内容，auth.json 由我们写入
	writeBackupFixture(t, home, ".codex/config.toml", "[model_providers.aliang]\nname = \"Aliang Gateway\"\n")
	writeBackupFixture(t, home, ".codex/auth.json", `{"OPENAI_API_KEY":"aliang"}`)
	// 钩子注入 targetUser + auth header
	stubConfigStateEnv(t, home)

	res, err := (&QuickSetupService{}).Restore("codex")
	if err != nil {
		t.Fatal(err)
	}
	// config.toml 回到 "user original"
	got, readErr := os.ReadFile(filepath.Join(home, ".codex/config.toml"))
	if readErr != nil || string(got) != "user original" {
		t.Fatalf("config.toml = %q, err = %v; want user original", string(got), readErr)
	}
	// auth.json 不存在（existed_before=false → 删除）
	if _, statErr := os.Stat(filepath.Join(home, ".codex/auth.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("auth.json must be removed; stat err = %v", statErr)
	}
	// res.Restored 含 config、res.Deleted 含 auth（contract 路径形式）
	if len(res.Restored) != 1 || res.Restored[0] != "~/.codex/config.toml" {
		t.Fatalf("restored = %v, want [~/.codex/config.toml]", res.Restored)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "~/.codex/auth.json" {
		t.Fatalf("deleted = %v, want [~/.codex/auth.json]", res.Deleted)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("failed must be empty: %+v", res.Failed)
	}
	// manifest 条目已清（loadQuickSetupManifest → Backups 为空）
	m, manifestErr := loadQuickSetupManifest(home)
	if manifestErr != nil {
		t.Fatal(manifestErr)
	}
	if len(m.Backups) != 0 {
		t.Fatalf("manifest backups must be empty after restore: %+v", m.Backups)
	}
	// 备份文件本体已随之删除
	entries, walkErr := os.ReadDir(filepath.Join(home, ".aliang", "quick-setup", "backups", "codex"))
	if walkErr != nil || len(entries) != 0 {
		t.Fatalf("backup files must be removed; entries = %v, err = %v", entries, walkErr)
	}
}

func TestRestoreUnauthenticated(t *testing.T) {
	previousAuth := quickSetupAuthorizationHeaderFn
	quickSetupAuthorizationHeaderFn = func() string { return "" }
	t.Cleanup(func() { quickSetupAuthorizationHeaderFn = previousAuth })

	_, err := (&QuickSetupService{}).Restore("codex")
	if !errors.Is(err, ErrQuickSetupUnauthenticated) {
		t.Fatalf("err = %v, want ErrQuickSetupUnauthenticated", err)
	}
}

func TestRestoreUnknownSoftware(t *testing.T) {
	home := t.TempDir()
	stubConfigStateEnv(t, home)

	_, err := (&QuickSetupService{}).Restore("bogus")
	if err == nil {
		t.Fatal("unknown software must error")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("error must name the software: %v", err)
	}
}

// TestQuickSetupCodexTOMLManaged 直测 codex TOML 段表头判定（spec §8）：只认本网关
// 写入的 [model_providers.aliang] 单表头形态——数组表头（[[）跳过、注释掉的表头不
// 命中、后缀伪造段名（aliang_other）不误伤；CRLF 行尾正常命中。
func TestQuickSetupCodexTOMLManaged(t *testing.T) {
	cases := []struct {
		name    string
		content string
		managed bool
	}{
		{
			name:    "plain section header",
			content: "model = \"gpt-5\"\n[model_providers.aliang]\nname = \"Aliang Gateway\"\n",
			managed: true,
		},
		{
			name:    "CRLF line endings",
			content: "model = \"gpt-5\"\r\n[model_providers.aliang]\r\nname = \"Aliang Gateway\"\r\n",
			managed: true,
		},
		{
			name:    "array table header skipped",
			content: "[[model_providers.aliang]]\nname = \"not our written shape\"\n",
			managed: false,
		},
		{
			name:    "commented header not matched",
			content: "# [model_providers.aliang]\nmodel = \"gpt-5\"\n",
			managed: false,
		},
		{
			name:    "suffix forgery section not matched",
			content: "[model_providers.aliang_other]\nname = \"someone else\"\n",
			managed: false,
		},
		{
			name:    "empty content",
			content: "",
			managed: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := quickSetupCodexTOMLManaged(tc.content); got != tc.managed {
				t.Fatalf("quickSetupCodexTOMLManaged(%q) = %v, want %v", tc.content, got, tc.managed)
			}
		})
	}
}

// TestQuickSetupURLHostManaged 直测 URL host 判定（spec §8）：两个公网域名带自定义
// 端口（api.aliang.one:8443）也算指向本网关；域名大小写归一命中；loopback 保持精确
// 匹配——用户本地 127.0.0.1:9999（ollama/LM Studio）与无端口形式都不误标；后缀
// 伪造域名不命中。
func TestQuickSetupURLHostManaged(t *testing.T) {
	cases := []struct {
		name    string
		rawURL  string
		managed bool
	}{
		{name: "bare inference domain", rawURL: "https://api.aliang.one", managed: true},
		{name: "uppercase domain normalized", rawURL: "https://API.ALIANG.ONE/v1", managed: true},
		{name: "inference domain custom port", rawURL: "https://api.aliang.one:8443/v1", managed: true},
		{name: "control plane domain custom port", rawURL: "https://backend.aliang.one:9443", managed: true},
		{name: "loopback proxy exact", rawURL: "http://127.0.0.1:56432", managed: true},
		{name: "loopback alias exact", rawURL: "http://localhost:56432", managed: true},
		{name: "loopback other port not managed", rawURL: "http://127.0.0.1:9999", managed: false},
		{name: "loopback without port not managed", rawURL: "http://127.0.0.1", managed: false},
		{name: "suffix forgery not managed", rawURL: "https://api.aliang.one.attacker.com/v1", managed: false},
		{name: "third-party host", rawURL: "https://third-party.example.com/v1", managed: false},
		{name: "invalid url", rawURL: ":", managed: false},
		{name: "empty url", rawURL: "", managed: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := quickSetupURLHostManaged(tc.rawURL); got != tc.managed {
				t.Fatalf("quickSetupURLHostManaged(%q) = %v, want %v", tc.rawURL, got, tc.managed)
			}
		})
	}
}
