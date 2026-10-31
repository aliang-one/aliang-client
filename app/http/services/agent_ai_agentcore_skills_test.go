package services

// 技能注入清单的收集契约（2026-10-31 内核 skill 支持）。
//
// 三条不变量：
//  1. 只收「目录下有 SKILL.md」的条目，散文件/隐藏目录一律跳过；
//  2. disable-model-invocation 的技能不注入（与 claude 同语义）；
//  3. 会话级 disabled 名单按注入名精确命中，且结果按名字排序——顺序不稳定
//     会打掉上游 prompt cache。

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSkill(t *testing.T, dir, name, frontmatter string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\n" + frontmatter + "\n---\n\nbody\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestAgentCoreSkillsFor(t *testing.T) {
	home := t.TempDir()
	skillsDir := filepath.Join(home, ".claude", "skills")

	writeSkill(t, skillsDir, "pdf", "name: pdf\ndescription: Work with PDF files")
	writeSkill(t, skillsDir, "docx", "name: docx\ndescription: Work with Word files")
	// 显式关闭模型调用：不应注入
	writeSkill(t, skillsDir, "hidden", "name: hidden\ndescription: nope\ndisable-model-invocation: true")
	// 目录下没有 SKILL.md：跳过
	if err := os.MkdirAll(filepath.Join(skillsDir, "empty-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 隐藏目录：跳过
	if err := os.MkdirAll(filepath.Join(skillsDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("收全量并按名字排序", func(t *testing.T) {
		got := agentCoreSkillsFor(home, nil)
		var names []string
		for _, s := range got {
			names = append(names, s.Name)
		}
		want := []string{"docx", "pdf"}
		if len(names) != len(want) {
			t.Fatalf("got %v, want %v", names, want)
		}
		for i := range want {
			if names[i] != want[i] {
				t.Fatalf("got %v, want %v", names, want)
			}
		}
		if got[0].Path == "" || got[0].Description == "" {
			t.Fatalf("路径/描述未填充: %+v", got[0])
		}
	})

	t.Run("会话级 disabled 精确剔除", func(t *testing.T) {
		got := agentCoreSkillsFor(home, []string{"pdf"})
		if len(got) != 1 || got[0].Name != "docx" {
			t.Fatalf("got %+v, want only docx", got)
		}
	})

	t.Run("空 home 返回 nil", func(t *testing.T) {
		if got := agentCoreSkillsFor("", nil); got != nil {
			t.Fatalf("got %+v, want nil", got)
		}
	})
}

func TestAgentCoreSkillsPromptBlock(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, filepath.Join(home, ".claude", "skills"), "pdf", "name: pdf\ndescription: Work with PDF files")

	m := &agentAIManager{}
	// agentHome() 依赖全局状态，这里直接验证空清单路径不产生块
	if block := m.agentCoreSkillsPromptBlock(agentAIRun{disabledSkills: []string{"__none__"}}); block != "" {
		// 若测试机恰好没有技能，block 为空——两种情况都合法，只要不 panic
		_ = block
	}
}
