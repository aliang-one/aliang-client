package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/agentcore"
)

func writeSubagentDef(t *testing.T, home, filename, body string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(body), 0o644); err != nil {
		t.Fatalf("写定义失败: %v", err)
	}
}

func TestAgentCoreSubagentDefsIn(t *testing.T) {
	home := t.TempDir()
	writeSubagentDef(t, home, "reviewer.md",
		"---\nname: reviewer\ndescription: 代码评审\nmodel: sonnet\ntools: Read, Grep\n---\n你是评审员。")
	writeSubagentDef(t, home, "fallback.md", "没有 frontmatter 的正文。")
	writeSubagentDef(t, home, "broken.md", "---\nname: [ broken\n")
	writeSubagentDef(t, home, "notagent.txt", "忽略非 md")

	defs := agentCoreSubagentDefsIn(home)
	if len(defs) != 3 {
		t.Fatalf("条目数 = %d, want 3（非 md 忽略）: %+v", len(defs), defs)
	}
	byName := map[string]agentCoreSubagentDef{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	r := byName["reviewer"]
	if r.Description != "代码评审" || len(r.ToolsWhitelist) != 2 || r.ToolsWhitelist[0] != "Read" {
		t.Fatalf("frontmatter 解析错误: %+v", r)
	}
	if r.Model != "sonnet" {
		t.Fatalf("model 解析错误: %+v", r)
	}
	if !strings.Contains(r.Body, "评审员") {
		t.Fatalf("正文缺失: %+v", r)
	}
	if byName["fallback"].Body == "" {
		t.Fatalf("无 frontmatter 文件应保留回落名+正文")
	}
	// broken.md：frontmatter 未闭合 → 名回落文件名，全文回落正文。
	b := byName["broken"]
	if b.Name != "broken" || b.Body != "---\nname: [ broken" {
		t.Fatalf("未闭合 frontmatter 回落语义错误: %+v", b)
	}
}

func TestAgentCoreSubagentFrontmatterOnlyStub(t *testing.T) {
	// 有 frontmatter 但正文为空：Body 必须是空串，
	// 绝不能把整块 frontmatter 泄漏进指令文本。
	def, ok := agentCoreSubagentFrontmatter("stub.md", "---\nname: stub\ndescription: 只有头\n---\n")
	if !ok {
		t.Fatalf("frontmatter-only stub 应是有效定义")
	}
	if def.Name != "stub" || def.Description != "只有头" {
		t.Fatalf("头字段解析错误: %+v", def)
	}
	if def.Body != "" || strings.Contains(def.Body, "---") {
		t.Fatalf("空正文泄漏 frontmatter: %q", def.Body)
	}
}

func TestAgentCoreSubagentBodyTruncation(t *testing.T) {
	content := "---\nname: big\n---\n" + strings.Repeat("x", 9*1024)
	def, ok := agentCoreSubagentFrontmatter("big.md", content)
	if !ok {
		t.Fatalf("应有效")
	}
	if got := len(def.Body); got != 8*1024 {
		t.Fatalf("正文截断 = %d, want 8192", got)
	}
}

func TestAgentCoreSubagentToolsBlockListFailsOpen(t *testing.T) {
	// YAML 块列表不被行式解析支持：白名单为空=全量放行（Warn 留痕）。
	def, ok := agentCoreSubagentFrontmatter("blk.md", "---\nname: blk\ntools:\n  - Read\n  - Grep\n---\nx")
	if !ok {
		t.Fatalf("应有效")
	}
	if len(def.ToolsWhitelist) != 0 {
		t.Fatalf("块列表不应解析出白名单: %+v", def.ToolsWhitelist)
	}
}

func TestAgentCoreSubagentDefsCap(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 30; i++ {
		writeSubagentDef(t, home, fmt.Sprintf("agent-%02d.md", i), "---\nname: a\n---\nx")
	}
	if got := len(agentCoreSubagentDefsIn(home)); got != 24 {
		t.Fatalf("cap = %d, want 24（确定性截断）", got)
	}
}

func TestAgentCoreSubagentBridgeName(t *testing.T) {
	reserved := map[string]bool{"Read": true, "Bash": true}
	seen := map[string]bool{}
	n1 := agentCoreSubagentBridgeName("Read", reserved, seen)
	if n1 == "Read" || seen[n1] != true {
		t.Fatalf("内核保留名必须消解: %q", n1)
	}
	n2 := agentCoreSubagentBridgeName("Read", reserved, seen)
	if n2 == n1 {
		t.Fatalf("重名必须序号消解: %q vs %q", n1, n2)
	}
	if got := agentCoreSubagentBridgeName("a:b/c", reserved, seen); strings.ContainsAny(got, ":/") {
		t.Fatalf("清洗失效: %q", got)
	}
}

func TestAgentCoreSubagentBridgeExecute(t *testing.T) {
	up, srv := newAgentCoreUpstream(t, agentCoreTextTurn("评审完成：无问题", ""))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")
	_ = up

	client := agentcore.NewClient(agentCoreBaseURL())
	var journaled []string
	bridge := &agentCoreSubagentBridge{
		client:      client,
		mainModel:   "glm-5.2",
		defModel:    "sonnet", // 非 glm → 回落主模型（策略）
		name:        "reviewer",
		description: "代码评审",
		body:        "你是评审员。",
		projectPath: t.TempDir(),
		approve: func(ctx context.Context, call agentcore.ApprovalRequest) (agentcore.ApprovalResponse, error) {
			return agentcore.ApprovalResponse{Approved: true}, nil
		},
		onToolCall: func(tool, summary string, isErr bool) { journaled = append(journaled, tool) },
	}
	res := bridge.Execute(context.Background(), map[string]interface{}{"task": "评审 main.go"}, nil)
	if res.IsError || !strings.Contains(res.Content, "评审完成") {
		t.Fatalf("子 run 结果错误: %+v", res)
	}
	// Task 调用本身也记行（spec §7）。
	if len(journaled) != 1 || journaled[0] != "reviewer" {
		t.Fatalf("Task 行 journal 缺失: %v", journaled)
	}
}

func TestAgentCoreSubagentBridgeDepthOne(t *testing.T) {
	bridge := &agentCoreSubagentBridge{name: "reviewer"}
	reg := bridge.subRegistry(nil, true)
	if _, ok := reg.Get("reviewer"); ok {
		t.Fatalf("子 registry 不得包含子 agent 桥（depth=1 结构保证）")
	}
	for _, name := range []string{"Read", "Write", "Edit", "Bash", "PowerShell", "Grep", "Glob", "LS"} {
		if _, ok := reg.Get(name); !ok {
			t.Fatalf("子 registry 应含内置 %s", name)
		}
	}
}

func TestAgentCoreSubagentToolsWhitelist(t *testing.T) {
	bridge := &agentCoreSubagentBridge{name: "w", toolsWhitelist: []string{"Read", "Grep"}}
	reg := bridge.subRegistry(nil, false)
	for _, name := range []string{"Read", "Grep", "Write", "Bash", "LS"} {
		_, ok := reg.Get(name)
		want := name == "Read" || name == "Grep"
		if ok != want {
			t.Fatalf("白名单 %s: have=%v want=%v", name, ok, want)
		}
	}
}

func TestAgentCoreSubagentModelPolicy(t *testing.T) {
	cases := []struct{ defModel, mainModel, want string }{
		{"", "glm-5.2", "glm-5.2"},
		{"glm-5.3", "glm-5.2", "glm-5.3"},
		{"sonnet", "glm-5.2", "glm-5.2"}, // claude 系忽略回落
	}
	for _, c := range cases {
		if got := agentCoreSubagentModel(c.defModel, c.mainModel); got != c.want {
			t.Fatalf("model(%q,%q)=%q want %q", c.defModel, c.mainModel, got, c.want)
		}
	}
}

// TestAgentCoreSubagentBridgeFailureTolerance 锁死失败容忍（spec §8）。
func TestAgentCoreSubagentBridgeFailureTolerance(t *testing.T) {
	bridge := &agentCoreSubagentBridge{
		client:    agentcore.NewClient("http://127.0.0.1:1"), // 不可达：client 内部重试 ~3.5s 后 err
		mainModel: "glm-5.2",
		name:      "reviewer",
		body:      "x",
	}
	res := bridge.Execute(context.Background(), map[string]interface{}{"task": "评审"}, nil)
	if !res.IsError || !strings.Contains(res.Content, "子 agent 执行失败") {
		t.Fatalf("失败容忍语义错误: %+v", res)
	}
}
