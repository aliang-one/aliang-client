package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Claude Code 会给没想出标题的会话写一个派生占位名 "<项目目录slug>-<2位>"。
// Source 字段诞生之前的缓存条目没有 provenance，旧逻辑一律当"用户改名"，
// 于是这些占位名被盖上 title_updated_at，永久短路手机端的自动命名
// （线上实测：95 条缓存里 91 条 source 为空，38 条会话卡在 slug 标题）。
// 这组测试锁住"形态即判据"的兜底。

func TestAgentRenameLooksLikeDerivedSlug(t *testing.T) {
	cases := []struct {
		name        string
		title       string
		projectPath string
		want        bool
	}{
		{"命中：plan-maker-96", "plan-maker-96", "/home/liang/MyProgram/AiProject/plan-maker", true},
		{"命中：alianggate-99", "alianggate-99", "/Users/mac/MyProgram/GoProgram/nursor/alianggate", true},
		{"命中：tmp-4f", "tmp-4f", "/home/liang/MyProgram/tmp", true},
		{"命中：大写标题", "Plan-Maker-96", "/home/liang/MyProgram/AiProject/plan-maker", true},
		{"命中：Windows 反斜杠路径", "drone1-3e", `D:\Unreal Projects\drone1`, true},
		{"命中：路径带尾斜杠", "aliangboard-5e", "/home/liang/MyProgram/AiProject/aliangboard/", true},
		{"不命中：后缀三位", "plan-maker-969", "/home/liang/MyProgram/AiProject/plan-maker", false},
		{"不命中：后缀一位", "plan-maker-9", "/home/liang/MyProgram/AiProject/plan-maker", false},
		{"不命中：项目名不符", "aliangboard-96", "/home/liang/MyProgram/AiProject/plan-maker", false},
		{"不命中：真实标题", "修复登录超时", "/home/liang/MyProgram/AiProject/plan-maker", false},
		{"不命中：手机改写名", "plan-maker-96 调优", "/home/liang/MyProgram/AiProject/plan-maker", false},
		{"不命中：缺少项目路径", "plan-maker-96", "", false},
		{"不命中：中文目录名无法成 slug", "项目-96", "/home/liang/项目", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, agentRenameLooksLikeDerivedSlug(c.title, c.projectPath))
		})
	}
}

// 空 source 的历史条目：slug 形态判为生成名，普通名字保持"用户改名"的旧语义。
func TestAgentRenameEntryExplicitLegacySlugIsGenerated(t *testing.T) {
	slugEntry := agentRenameCacheEntry{
		Name:        "plan-maker-96",
		Origin:      agentRenameOriginLocal,
		ProjectPath: "/home/liang/MyProgram/AiProject/plan-maker",
	}
	assert.False(t, agentRenameEntryExplicit(slugEntry),
		"legacy derived slug must not count as a deliberate rename")

	namedEntry := agentRenameCacheEntry{
		Name:        "登录超时排查",
		Origin:      agentRenameOriginLocal,
		ProjectPath: "/home/liang/MyProgram/AiProject/plan-maker",
	}
	assert.True(t, agentRenameEntryExplicit(namedEntry),
		"legacy entry with a real-looking title keeps its deliberate semantics")

	// 显式 source 永远按 source 判定，形态无关。
	assert.True(t, agentRenameEntryExplicit(agentRenameCacheEntry{
		Name:        "plan-maker-96",
		Origin:      agentRenameOriginLocal,
		Source:      "user",
		ProjectPath: "/home/liang/MyProgram/AiProject/plan-maker",
	}))
	// 手机改名永远显式。
	assert.True(t, agentRenameEntryExplicit(agentRenameCacheEntry{
		Name:   "plan-maker-96",
		Origin: agentRenameOriginPhone,
	}))
}

// 上报给 PhoneServer 的来源：slug 形态必须报 derived，否则服务端会把
// title_updated_at 盖上并短路自动命名。
func TestWireAgentRenameSourceLegacySlugReportsDerived(t *testing.T) {
	assert.Equal(t, "derived", wireAgentRenameSource(agentRenameCacheEntry{
		Name:        "plan-maker-96",
		Origin:      agentRenameOriginLocal,
		ProjectPath: "/home/liang/MyProgram/AiProject/plan-maker",
	}))
	assert.Equal(t, "user", wireAgentRenameSource(agentRenameCacheEntry{
		Name:        "登录超时排查",
		Origin:      agentRenameOriginLocal,
		ProjectPath: "/home/liang/MyProgram/AiProject/plan-maker",
	}))
	// 已有 source 的条目原样透传。
	assert.Equal(t, "auto", wireAgentRenameSource(agentRenameCacheEntry{
		Name:   "自动标题",
		Origin: agentRenameOriginLocal,
		Source: "auto",
	}))
	// 手机改名仍是 user。
	assert.Equal(t, "user", wireAgentRenameSource(agentRenameCacheEntry{
		Name:   "plan-maker-96",
		Origin: agentRenameOriginPhone,
	}))
}
