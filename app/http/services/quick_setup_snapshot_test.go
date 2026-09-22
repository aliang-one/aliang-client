package services

import (
	"errors"
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
