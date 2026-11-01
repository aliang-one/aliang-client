package services

// 技能渐进披露 Read carve-out 的契约测试（2026-10-31）。
//
// 背景：agentcore 内核把 SKILL.md 路径注进 system prompt 并指示模型「先
// Read 再照做」，而技能目录在 ~/.claude 下天然出项目界——没有这个只读
// 特例，每个会话首次用技能必弹手机审批卡（验收缺陷②）。本文件锁定：
//   1. Read 打中用户 skills 根 → auto-approve（agent-skills-read-carveout）；
//   2. Read 打中已启用插件的 skills 根 → 同样豁免；
//   3. 豁免只给 Read——对技能路径的 Write 仍走 outside-project-path 审批；
//   4. Read 打中 ~/.claude 下非技能路径 → 不豁免；
//   5. Bash 引用技能路径 → 仍走 outside-project-bash（Bash 无豁免）。
// 家目录经 agentHomeForMemoryHook 注入（与 memory carve-out 同款测试注入点），
// 全程 t.TempDir，绝不碰真机 ~/.claude。

import (
	"aliang.one/nursorgate/processor/config"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeSkillsCarveoutHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	skillMd := "---\nname: pdf\ndescription: pdf skill\n---\n\nbody\n"
	if err := os.MkdirAll(filepath.Join(home, ".claude", "skills", "pdf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "skills", "pdf", "SKILL.md"), []byte(skillMd), 0o644); err != nil {
		t.Fatal(err)
	}
	// 已启用插件：registry + enabledPlugins 双门槛（fail-closed），装一个
	// user scope 插件到 <home>/plugin-install/demo。
	installPath := filepath.Join(home, "plugin-install", "demo")
	if err := os.MkdirAll(filepath.Join(installPath, "skills", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installPath, "skills", "demo", "SKILL.md"), []byte(skillMd), 0o644); err != nil {
		t.Fatal(err)
	}
	registry, _ := json.Marshal(map[string]interface{}{
		"plugins": map[string]interface{}{
			"demo@market": []map[string]interface{}{
				{"scope": "user", "installPath": installPath},
			},
		},
	})
	if err := os.MkdirAll(filepath.Join(home, ".claude", "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), registry, 0o600); err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]interface{}{
		"enabledPlugins": map[string]interface{}{"demo@market": true},
	})
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), settings, 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func withSkillsCarveoutHome(t *testing.T, home string) {
	t.Helper()
	old := agentHomeForMemoryHook
	agentHomeForMemoryHook = func() string { return home }
	t.Cleanup(func() { agentHomeForMemoryHook = old })
}

func skillsCarveoutDecision(t *testing.T, tool string, input map[string]interface{}, projectPath string) (policyDecision, string, bool) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	decision, ruleID, handled := approvalProjectBoundaryDecisionForHook(tool, raw, projectPath, nil)
	return decision, ruleID, handled
}

func TestSkillsReadCarveoutApprovesUserSkillsRoot(t *testing.T) {
	home := writeSkillsCarveoutHome(t)
	withSkillsCarveoutHome(t, home)
	projectPath := t.TempDir()

	decision, ruleID, handled := skillsCarveoutDecision(t, "read", map[string]interface{}{
		"file_path": filepath.Join(home, ".claude", "skills", "pdf", "SKILL.md"),
	}, projectPath)
	if !handled || decision != decisionAutoApprove || ruleID != agentSkillsCarveoutRuleID {
		t.Fatalf("read 技能文件应豁免, got handled=%v decision=%q ruleID=%q", handled, decision, ruleID)
	}
}

func TestSkillsReadCarveoutApprovesPluginSkillsRoot(t *testing.T) {
	home := writeSkillsCarveoutHome(t)
	withSkillsCarveoutHome(t, home)
	projectPath := t.TempDir()

	decision, ruleID, handled := skillsCarveoutDecision(t, "Read", map[string]interface{}{
		"file_path": filepath.Join(home, "plugin-install", "demo", "skills", "demo", "SKILL.md"),
	}, projectPath)
	if !handled || decision != decisionAutoApprove || ruleID != agentSkillsCarveoutRuleID {
		t.Fatalf("read 插件技能文件应豁免, got handled=%v decision=%q ruleID=%q", handled, decision, ruleID)
	}
}

func TestSkillsCarveoutDoesNotCoverWrite(t *testing.T) {
	home := writeSkillsCarveoutHome(t)
	withSkillsCarveoutHome(t, home)
	projectPath := t.TempDir()

	decision, ruleID, handled := skillsCarveoutDecision(t, "write", map[string]interface{}{
		"file_path": filepath.Join(home, ".claude", "skills", "pdf", "SKILL.md"),
	}, projectPath)
	if !handled || decision != decisionRequireApproval || ruleID != "outside-project-path" {
		t.Fatalf("写技能文件不得豁免, got handled=%v decision=%q ruleID=%q", handled, decision, ruleID)
	}
}

func TestSkillsCarveoutDoesNotCoverNonSkillClaudePaths(t *testing.T) {
	home := writeSkillsCarveoutHome(t)
	withSkillsCarveoutHome(t, home)
	projectPath := t.TempDir()

	decision, ruleID, handled := skillsCarveoutDecision(t, "read", map[string]interface{}{
		"file_path": filepath.Join(home, ".claude", "settings.json"),
	}, projectPath)
	if !handled || decision != decisionRequireApproval || ruleID != "outside-project-path" {
		t.Fatalf("读 ~/.claude 下非技能文件不得豁免, got handled=%v decision=%q ruleID=%q", handled, decision, ruleID)
	}
}

func TestSkillsCarveoutDoesNotCoverBash(t *testing.T) {
	home := writeSkillsCarveoutHome(t)
	withSkillsCarveoutHome(t, home)
	projectPath := t.TempDir()

	decision, ruleID, handled := skillsCarveoutDecision(t, "Bash", map[string]interface{}{
		"command": "cat " + filepath.Join(home, ".claude", "skills", "pdf", "SKILL.md"),
	}, projectPath)
	if !handled || decision != decisionRequireApproval || ruleID != "outside-project-bash" {
		t.Fatalf("Bash 引用技能路径不得豁免, got handled=%v decision=%q ruleID=%q", handled, decision, ruleID)
	}
}

// 总闸关闭 → 技能豁免整个失效（豁免面不得比功能面宽，评审 confirmed-3）。
func TestSkillsReadCarveoutDisabledWhenMasterOff(t *testing.T) {
	home := writeSkillsCarveoutHome(t)
	withSkillsCarveoutHome(t, home)

	oldCfg := config.GetGlobalConfig()
	fals := false
	next := &config.Config{Customer: &config.CustomerConfig{
		AgentCore: &config.AgentCoreTuningConfig{SkillsEnabled: &fals},
	}}
	t.Cleanup(func() { config.SetGlobalConfig(oldCfg) })
	config.SetGlobalConfig(next)

	decision, ruleID, handled := skillsCarveoutDecision(t, "read", map[string]interface{}{
		"file_path": filepath.Join(home, ".claude", "skills", "pdf", "SKILL.md"),
	}, t.TempDir())
	if !handled || decision != decisionRequireApproval || ruleID != "outside-project-path" {
		t.Fatalf("总闸关闭后技能读取不得豁免, got handled=%v decision=%q ruleID=%q", handled, decision, ruleID)
	}
}

// 插件 skills 根本身是 symlink → 豁免面顺链逃逸，必须 fail-closed 回落审批
// （评审 confirmed-2：installed_plugins.json 的 installPath 无任何校验）。
func TestSkillsCarveoutRefusesSymlinkedPluginRoot(t *testing.T) {
	home := writeSkillsCarveoutHome(t)
	// 把插件安装树里的 skills 换成指向家目录秘密文件的软链
	installPath := filepath.Join(home, "plugin-install", "demo")
	secret := filepath.Join(home, "secrets", "id_rsa")
	if err := os.MkdirAll(filepath.Dir(secret), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(installPath, "skills")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(secret), filepath.Join(installPath, "skills")); err != nil {
		t.Fatal(err)
	}
	withSkillsCarveoutHome(t, home)

	decision, ruleID, handled := skillsCarveoutDecision(t, "read", map[string]interface{}{
		"file_path": filepath.Join(installPath, "skills", "id_rsa"),
	}, t.TempDir())
	if !handled || decision != decisionRequireApproval || ruleID != "outside-project-path" {
		t.Fatalf("symlink skills 根不得豁免(应回落审批), got handled=%v decision=%q ruleID=%q", handled, decision, ruleID)
	}
}
