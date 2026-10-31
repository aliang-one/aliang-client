package services

// AliangCode（agentcore）技能注入：把 ~/.claude/skills 与已启用插件的
// skills/*/SKILL.md 以「名称 + 描述 + 文件路径」注入内核 system prompt。
//
// 设计取舍（渐进披露，对齐 Claude Code 的做法）：
// 只注入清单，不注入技能正文。模型看到「有什么技能、干什么用、文件在哪」，
// 任务匹配时用 Read 工具打开对应 SKILL.md 再照做。这样 20 个技能也只占
// 几十行 prompt，而不是把每个技能全文塞进上下文。
//
// 开关：
//   - 全局：customer.agentcore.skills_enabled（默认开启）
//   - 会话：ai.session.create 的 disabled_skills（按名字关掉单个技能）
//
// 此前内核完全没有 skill 能力（agent_integrations.go 的 KernelSupported 恒
// false），设置页只能把技能标成「暂不支持」。本文件补上这条链路。

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"aliang.one/nursorgate/processor/config"
)

// agentCoreSkill 是一条可注入的技能。
type agentCoreSkill struct {
	Name        string
	Description string
	Path        string
}

// agentCoreSkillsFor 收集当前生效的技能清单。
//
// disabled 是会话级关闭名单（按注入名精确匹配）。返回按名字排序，保证
// 同一份配置下 system prompt 稳定——不稳定的 prompt 会打掉上游的 prompt
// cache（内核 cache.go 的断点按内容前缀算）。
func agentCoreSkillsFor(home string, disabled []string) []agentCoreSkill {
	if strings.TrimSpace(home) == "" {
		return nil
	}
	off := make(map[string]struct{}, len(disabled))
	for _, name := range disabled {
		if name = strings.TrimSpace(name); name != "" {
			off[name] = struct{}{}
		}
	}

	var skills []agentCoreSkill
	seen := make(map[string]struct{})

	add := func(root, prefix string) {
		entries, err := os.ReadDir(root)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			skillMd := filepath.Join(root, entry.Name(), "SKILL.md")
			if _, err := os.Stat(skillMd); err != nil {
				continue
			}
			fm, _ := parseSlashFrontmatter(skillMd)
			name := strings.TrimSpace(fm.name)
			if name == "" {
				name = entry.Name()
			}
			name = prefix + name
			if _, dup := seen[name]; dup {
				continue
			}
			// 用户显式关了 model invocation 的技能不注入（与 claude 同语义）。
			if !fm.isModelInvocable() {
				continue
			}
			if _, off := off[name]; off {
				continue
			}
			seen[name] = struct{}{}
			skills = append(skills, agentCoreSkill{
				Name:        name,
				Description: strings.TrimSpace(fm.description),
				Path:        skillMd,
			})
		}
	}

	homeClaude := filepath.Join(home, ".claude")
	add(filepath.Join(homeClaude, "skills"), "")
	for _, inst := range enabledUserScopePluginInstallsIn(home) {
		add(filepath.Join(inst.installPath, "skills"), inst.pluginName+":")
	}

	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills
}

// agentCoreSkillsPromptBlock 生成注入 system prompt 的技能清单块。
// 无技能 / 全局关闭时返回空串（调用方直接拼接，空串即无副作用）。
func (m *agentAIManager) agentCoreSkillsPromptBlock(run agentAIRun) string {
	if !config.GetGlobalConfig().EffectiveAgentCoreSkillsEnabled() {
		return ""
	}
	skills := agentCoreSkillsFor(agentHome(), run.disabledSkills)
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n<available_skills>\n")
	b.WriteString("These skills are available in this session. Each entry gives a name, what it is for, and the path to its SKILL.md. ")
	b.WriteString("When a task matches a skill, read that SKILL.md with the Read tool first, then follow its instructions. ")
	b.WriteString("Do not guess a skill's procedure from its name alone.\n")
	for _, s := range skills {
		if s.Description == "" {
			fmt.Fprintf(&b, "- %s (read %s)\n", s.Name, s.Path)
			continue
		}
		fmt.Fprintf(&b, "- %s: %s (read %s)\n", s.Name, s.Description, s.Path)
	}
	b.WriteString("</available_skills>\n")
	return b.String()
}
