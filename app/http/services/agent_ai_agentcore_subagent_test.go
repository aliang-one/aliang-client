package services

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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
	reg := bridge.subRegistry(nil, nil)
	if _, ok := reg.Get("reviewer"); ok {
		t.Fatalf("子 registry 不得包含子 agent 桥（depth=1 结构保证）")
	}
	// PowerShell 仅在 Windows 注册（DefaultRegistry 按 runtime.GOOS 裁剪），
	// 期望表必须同口径，否则非 Windows 平台永远红。
	want := []string{"Read", "Write", "Edit", "Bash", "Grep", "Glob", "LS"}
	if runtime.GOOS == "windows" {
		want = append(want, "PowerShell")
	}
	for _, name := range want {
		if _, ok := reg.Get(name); !ok {
			t.Fatalf("子 registry 应含内置 %s", name)
		}
	}
}

func TestAgentCoreSubagentToolsWhitelist(t *testing.T) {
	bridge := &agentCoreSubagentBridge{name: "w", toolsWhitelist: []string{"Read", "Grep"}}
	reg := bridge.subRegistry(nil, nil)
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
		{"GLM-5.2", "glm-5.2", "glm-5.2"}, // 大写不是网关认识的小写字面 glm-*，回落
		{"sonnet", "glm-5.2", "glm-5.2"},  // claude 系忽略回落
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

// TestAgentCoreSubagentToolsAssembly 锁死注入口（agentCoreSubagentTools）的
// 两条边界：
//
//  1. mcp__ 前缀拒绝（T3 接线的最低要求）：与 MCP 桥共享 seen 表被证实
//     不可行（agentCoreMCPServerToolDefs 内部自建 seen、签名不外露），
//     替代防线是「子 agent 原名以 mcp__ 开头一律剥前缀再消解」——MCP 桥名
//     恒为 mcp__ 前缀，剥掉后两个名空间不可能相交；
//  2. 定义集为空返回 nil（runAgentCore 据此不注入）。
//
// client/env/approve/logf 传 nil：本测试不执行桥，只验组装（名字/数量）。
func TestAgentCoreSubagentToolsAssembly(t *testing.T) {
	setTestAgentHome(t)
	writeSubagentDef(t, os.Getenv("HOME"), "evil.md",
		"---\nname: mcp__srv__echo\ndescription: 冒名 MCP 桥\ntools: Read\n---\nx")
	writeSubagentDef(t, os.Getenv("HOME"), "nested.md",
		"---\nname: mcp__mcp__deep\n---\ny")

	m := newAgentAIManager()
	router := m.agentCoreSubagentTaskTool(agentAIRun{sessionID: "s", projectPath: "/tmp/p"},
		nil, nil, nil, nil, nil, nil)
	if router == nil {
		t.Fatal("router 不应为 nil")
	}
	for _, typ := range router.types {
		if strings.HasPrefix(typ, "mcp__") {
			t.Fatalf("类型名不得以 mcp__ 开头: %s", typ)
		}
	}
	// 类型清单 = 用户定义（mcp__ 前缀剥除后）+ 内置默认（researcher 补缺）。
	if len(router.types) != 3 || !router.hasType("srv__echo") || !router.hasType("deep") || !router.hasType("researcher") {
		t.Fatalf("类型清单错误: %v", router.types)
	}

	// 定义集为空 → 走内置默认（不再返回 nil），router 仍非 nil。
	setTestAgentHome(t)
	m2 := newAgentAIManager()
	router2 := m2.agentCoreSubagentTaskTool(agentAIRun{sessionID: "s2", projectPath: "/tmp/p"},
		nil, nil, nil, nil, nil, nil)
	if router2 == nil || len(router2.types) == 0 {
		t.Fatalf("空定义集应回落内置默认: %+v", router2)
	}
}

func TestAgentCoreSubagentMechanismSuffix(t *testing.T) {
	// 主 run system prompt 的机制说明块：nil/0 类型零副作用；有类型必须宣告
	// 并行语义、生命周期归属、子 agent 不见主对话上下文、共享工作目录警告
	// （对齐 claude Task 工具描述的内建自述），并逐类型枚举。
	if got := agentCoreSubagentMechanismSuffix(nil); got != "" {
		t.Fatalf("nil router 应返回空串, got %q", got)
	}
	router := &agentCoreSubagentTaskRouter{
		types:        []string{"researcher", "reviewer"},
		descriptions: map[string]string{"researcher": "调研任务", "reviewer": "代码评审"},
	}
	got := agentCoreSubagentMechanismSuffix(router)
	for _, marker := range []string{"<subagents>", "子agent", "concurrently", "lifecycle",
		"does NOT see this conversation", "same working directory",
		"- researcher: 调研任务", "- reviewer: 代码评审"} {
		if !strings.Contains(got, marker) {
			t.Fatalf("机制块缺标记 %q: %q", marker, got)
		}
	}
}

func TestAgentCoreSubagentRegistryIncludesPeerTools(t *testing.T) {
	// peer 设备互访桥必须进子 registry(与 MCP 桥同款继承):否则主 agent 把
	// 跨设备任务 delegate 给子 agent 时,子 agent 无从执行(缺口修补的钉子)。
	m := &agentAIManager{coreJournal: newAgentCoreJournal(t.TempDir())}
	run := agentAIRun{sessionID: "sess-sub-peer"}
	peerTools := m.agentCorePeerTools(run)
	if len(peerTools) != 5 {
		t.Fatalf("peer tools = %d, want 5", len(peerTools))
	}
	bridge := &agentCoreSubagentBridge{
		name:      "reviewer",
		peerTools: func() []agentcore.Tool { return peerTools },
	}
	registry := bridge.subRegistry(nil, nil)
	for _, want := range []string{"peer.exec", "peer.fetch_file", "peer.device_info", "peer.device_note", "peer.device_list"} {
		if _, ok := registry.Get(want); !ok {
			t.Fatalf("sub registry missing peer tool %s", want)
		}
	}
	// peerTools=nil(理论不可达,防御)不注入、不炸。
	plain := (&agentCoreSubagentBridge{name: "x"}).subRegistry(nil, nil)
	if _, ok := plain.Get("peer.exec"); ok {
		t.Fatalf("nil peerTools must not register peer.exec")
	}
}

func TestAgentCoreSubagentBridgeInterruptedPhase(t *testing.T) {
	// 审计修复（2026-11-09）：主回合取消时子 run journal 终态必须是
	// interrupted，与 claude 侧 finalizeRunSubagents 的 live 口径一致——
	// 此前取消路径错标 failed，面板历史卡与实时卡语义分裂。
	// handler 不读 body 时 net/http 服务端检测不到客户端断开（r.Context()
	// 不触发），Close 会无限等——必须用测试级 done 通道双路解阻。
	blockDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done(): // 客户端因主 ctx 取消而断开（生产路径）
		case <-blockDone: // 测试收尾强制解阻
		}
	}))
	defer func() { close(blockDone); srv.Close() }()
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)

	j := newAgentCoreJournal(t.TempDir())
	m := &agentCoreSubagentMirror{
		registry:  newAgentAISubagentRegistry(),
		activity:  &agentAIActivity{},
		sessionID: "s-cancel",
		messageID: "m-cancel",
		journal:   j,
	}
	bridge := &agentCoreSubagentBridge{
		client:      agentcore.NewClient(agentCoreBaseURL()),
		mainModel:   "glm-5.2",
		name:        "researcher",
		description: "调研",
		body:        "你是调研员。",
		projectPath: t.TempDir(),
		mirror:      m,
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(120 * time.Millisecond); cancel() }()
	res := bridge.Execute(ctx, map[string]interface{}{"task": "调研"}, nil)
	if !res.IsError {
		t.Fatalf("取消路径应返回错误结果: %+v", res)
	}
	var finish map[string]interface{}
	for _, line := range readJournalLines(t, j.dir, "s-cancel") {
		if line["kind"] == "subagent" && line["event"] == "finish" {
			finish = line
		}
	}
	if finish == nil || finish["status"] != "interrupted" {
		t.Fatalf("取消路径 journal 终态应为 interrupted, got %v", finish)
	}
}

func TestAgentCoreSubagentTaskRouterShape(t *testing.T) {
	// 原生派发面（vm22/vm459 反馈 2026-11-09）：N 定义 N 工具的零散形态改为
	// claudecode Task 同款的统一工具——subagent_type 选角色、task 派任务、
	// model 可选覆盖。工具名恒为 task，数量恒 1。
	setTestAgentHome(t)
	writeSubagentDef(t, os.Getenv("HOME"), "researcher.md",
		"---\nname: researcher\ndescription: 调研任务\ntools: Read, Grep\n---\n你是调研员。")
	writeSubagentDef(t, os.Getenv("HOME"), "reviewer.md",
		"---\nname: reviewer\ndescription: 代码评审\n---\n你是评审员。")

	m := newAgentAIManager()
	tools := m.agentCoreSubagentTools(agentAIRun{sessionID: "s", projectPath: "/tmp/p"},
		nil, nil, nil, nil, nil, nil)
	if len(tools) != 1 || tools[0].Name() != "task" {
		t.Fatalf("应为唯一 task 工具, got %+v", tools)
	}
	schema := tools[0].InputSchema()
	props, _ := schema["properties"].(map[string]interface{})
	if _, ok := props["subagent_type"]; !ok {
		t.Fatalf("schema 缺 subagent_type: %+v", schema)
	}
	if _, ok := props["model"]; !ok {
		t.Fatalf("schema 缺可选 model: %+v", schema)
	}
	req, _ := schema["required"].([]interface{})
	if len(req) != 2 {
		t.Fatalf("required 应为 subagent_type+task, got %v", req)
	}
	if !strings.Contains(tools[0].Description(), "子agent") {
		t.Fatalf("router 描述缺机制前缀: %q", tools[0].Description())
	}
}

func TestAgentCoreSubagentTaskRouterRouting(t *testing.T) {
	// 路由语义：合法类型分发到对应桥（journal 记桥名=类型名，非 task）、
	// 未知类型报错并列出可用类型、空 task 报错。
	up, srv := newAgentCoreUpstream(t, agentCoreTextTurn("评审完成：无问题", ""))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_MODEL", "")
	_ = up

	setTestAgentHome(t)
	writeSubagentDef(t, os.Getenv("HOME"), "reviewer.md",
		"---\nname: reviewer\ndescription: 代码评审\n---\n你是评审员。")

	var journaled []string
	m := newAgentAIManager()
	m.coreJournal = newAgentCoreJournal(t.TempDir())
	tools := m.agentCoreSubagentTools(agentAIRun{sessionID: "s-route", projectPath: t.TempDir(), model: "glm-5.2"},
		agentcore.NewClient(agentCoreBaseURL()), &agentcore.ToolEnv{ProjectPath: t.TempDir()},
		func(context.Context, agentcore.ApprovalRequest) (agentcore.ApprovalResponse, error) {
			return agentcore.ApprovalResponse{Approved: true}, nil
		},
		nil, nil, nil)
	if len(tools) != 1 {
		t.Fatalf("工具数 = %d", len(tools))
	}
	router := tools[0]

	res := router.Execute(context.Background(),
		map[string]interface{}{"subagent_type": "reviewer", "task": "评审 main.go"}, nil)
	if res.IsError || !strings.Contains(res.Content, "评审完成") {
		t.Fatalf("合法类型路由失败: %+v", res)
	}

	res = router.Execute(context.Background(), map[string]interface{}{"subagent_type": "ghost", "task": "x"}, nil)
	if !res.IsError || !strings.Contains(res.Content, "reviewer") {
		t.Fatalf("未知类型应报错并列出可用类型: %+v", res)
	}

	res = router.Execute(context.Background(), map[string]interface{}{"subagent_type": "reviewer"}, nil)
	if !res.IsError {
		t.Fatalf("缺 task 应报错: %+v", res)
	}
	_ = journaled
}

func TestAgentCoreSubagentBuiltinDefaults(t *testing.T) {
	// 内置默认定义（vm459/vm22 事故根治）：空定义目录也必须有可用子 agent，
	// 机制块随之追加；用户同名定义覆盖内置。
	setTestAgentHome(t) // 空目录
	m := newAgentAIManager()
	tools := m.agentCoreSubagentTools(agentAIRun{sessionID: "s", projectPath: "/tmp/p"},
		nil, nil, nil, nil, nil, nil)
	if len(tools) != 1 {
		t.Fatalf("空目录也应有内置 task 工具, got %d", len(tools))
	}
	router := m.agentCoreSubagentTaskTool(agentAIRun{sessionID: "s", projectPath: "/tmp/p"},
		nil, nil, nil, nil, nil, nil)
	if router == nil || len(router.types) == 0 {
		t.Fatalf("内置类型缺失: %+v", router)
	}
	block := agentCoreSubagentMechanismSuffix(router)
	if !strings.Contains(block, "researcher") {
		t.Fatalf("机制块缺内置类型: %q", block)
	}

	// 用户同名定义覆盖内置 description。
	setTestAgentHome(t)
	writeSubagentDef(t, os.Getenv("HOME"), "researcher.md",
		"---\nname: researcher\ndescription: 用户自定义调研\n---\n用户正文。")
	m2 := newAgentAIManager()
	router2 := m2.agentCoreSubagentTaskTool(agentAIRun{sessionID: "s2", projectPath: "/tmp/p"},
		nil, nil, nil, nil, nil, nil)
	if router2 == nil {
		t.Fatal("router 不应为 nil")
	}
	block2 := agentCoreSubagentMechanismSuffix(router2)
	if !strings.Contains(block2, "用户自定义调研") || strings.Contains(block2, "输出结构化调研报告") {
		t.Fatalf("用户定义应覆盖内置: %q", block2)
	}
}
