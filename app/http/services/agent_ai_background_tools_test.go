//go:build !windows

package services

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/agentcore"
)

// bgTestToolRun 是桥工具测试用 run：只填会话（桥只消费 sessionID）。
func bgTestToolRun(session string) agentAIRun {
	return agentAIRun{sessionID: session}
}

// bgTestPollRead 反复经桥工具读同一 id，直到 cond 命中——增量语义下每次
// Execute 消费游标，轮询天然累计。
func bgTestPollRead(t *testing.T, tool *bashOutputTool, env *agentcore.ToolEnv, id string, cond func(content string, isErr bool) bool) (string, bool) {
	t.Helper()
	var content string
	var isErr bool
	waitFor(t, func() bool {
		res := tool.Execute(context.Background(), map[string]interface{}{"shell_id": id}, env)
		content, isErr = res.Content, res.IsError
		return cond(content, isErr)
	})
	return content, isErr
}

// TestBashOutputListAndRead：真 registry.sessionView + 内核 Bash 后台启动，
// 桥工具无 id 列全部、带 id 增量读到输出与 completed 状态行。
func TestBashOutputListAndRead(t *testing.T) {
	reg := newAgentBackgroundShellRegistry()
	id := bgTestSpawn(t, reg, "sess-1", "echo tool-hello")
	run := bgTestToolRun("sess-1")
	tool := newBashOutputTool(run, reg, nil)
	env := &agentcore.ToolEnv{}

	// 无 shell_id：列出全部（含 id 与命令）。
	res := tool.Execute(context.Background(), map[string]interface{}{}, env)
	if res.IsError {
		t.Fatalf("列表不应失败: %s", res.Content)
	}
	if !strings.Contains(res.Content, id) || !strings.Contains(res.Content, "echo tool-hello") {
		t.Fatalf("列表应含 id 与命令，得到 %q", res.Content)
	}

	// 空 session 无记录：模型自主发现面。
	emptyTool := newBashOutputTool(bgTestToolRun("sess-empty"), reg, nil)
	res = emptyTool.Execute(context.Background(), map[string]interface{}{}, env)
	if res.IsError || strings.TrimSpace(res.Content) != "当前没有后台任务" {
		t.Fatalf("空簿应报「当前没有后台任务」，得到 %q (isErr=%v)", res.Content, res.IsError)
	}

	// 带 id：轮询增量读 tool-hello（此刻状态可能仍 running）。
	content, isErr := bgTestPollRead(t, tool, env, id, func(c string, e bool) bool {
		return !e && strings.Contains(c, "tool-hello")
	})
	if isErr {
		t.Fatalf("增量读不应失败: %s", content)
	}
	if !strings.Contains(content, "tool-hello") {
		t.Fatalf("增量应含 tool-hello，得到 %q", content)
	}

	// 收尾后状态行转 completed。
	bgTestPollRead(t, tool, env, id, func(c string, e bool) bool {
		return !e && strings.Contains(c, "completed")
	})

	// 不存在的 id：ErrorResult 带可用 id 助自纠。
	res = tool.Execute(context.Background(), map[string]interface{}{"shell_id": "bgsh-99999"}, env)
	if !res.IsError {
		t.Fatalf("未知 id 应报错，得到 %q", res.Content)
	}
	if !strings.Contains(res.Content, "未找到后台任务") || !strings.Contains(res.Content, id) {
		t.Fatalf("错误应含「未找到后台任务」与可用 id，得到 %q", res.Content)
	}
}

// TestKillShellToolKills：桥终止存活任务（终态转 cancelled）；重复 kill 回报
// 「已结束」；未知 id 报「未找到」。
func TestKillShellToolKills(t *testing.T) {
	reg := newAgentBackgroundShellRegistry()
	id := bgTestSpawn(t, reg, "sess-2", "sleep 30")
	run := bgTestToolRun("sess-2")
	kill := newKillShellTool(run, reg, nil)
	out := newBashOutputTool(run, reg, nil)
	env := &agentcore.ToolEnv{}

	// 等 running（未启动完就 kill 会拿 wasRunning=false，语义失真）。
	waitFor(t, func() bool {
		_, status, ok := reg.read("sess-2", id)
		return ok && strings.Contains(status, "running")
	})

	res := kill.Execute(context.Background(), map[string]interface{}{"shell_id": id}, env)
	if res.IsError {
		t.Fatalf("终止存活任务不应失败: %s", res.Content)
	}
	if !strings.Contains(res.Content, "已终止") || !strings.Contains(res.Content, id) {
		t.Fatalf("文案应含「已终止」与 id，得到 %q", res.Content)
	}

	// 终态经登记处确认 cancelled（桥经 read 断言同一条语义链）。
	bgTestPollRead(t, out, env, id, func(c string, e bool) bool {
		return !e && strings.Contains(c, "cancelled")
	})

	// 重复 kill：已结束，无需终止。
	res = kill.Execute(context.Background(), map[string]interface{}{"shell_id": id}, env)
	if res.IsError {
		t.Fatalf("重复 kill 不应失败: %s", res.Content)
	}
	if !strings.Contains(res.Content, "已结束，无需终止") {
		t.Fatalf("重复 kill 文案应「已结束，无需终止」，得到 %q", res.Content)
	}

	// 未知 id：ErrorResult 助自纠。
	res = kill.Execute(context.Background(), map[string]interface{}{"shell_id": "bgsh-424242"}, env)
	if !res.IsError || !strings.Contains(res.Content, "未找到后台任务") {
		t.Fatalf("未知 id 应报「未找到后台任务」，得到 %q (isErr=%v)", res.Content, res.IsError)
	}
}

// TestReadonlyToolsAllowBackgroundNames：两名落 readonly-tools 自动放行
// （balanced 策略，与 TestEvaluateReadonlyToolAutoApproves 同款判定）。
func TestReadonlyToolsAllowBackgroundNames(t *testing.T) {
	p := builtinBalancedPolicy()
	for _, tool := range []string{"BashOutput", "KillShell"} {
		d, id := evaluateApprovalPolicy(p, tool, nil)
		if d != decisionAutoApprove {
			t.Fatalf("%s 应自动放行，得到 %s（rule=%s）", tool, d, id)
		}
		if id != "readonly-tools" {
			t.Fatalf("%s 应命中 readonly-tools 规则，得到 %s", tool, id)
		}
	}
}

// TestBackgroundToolsJournalNilSafe：coreJournal=nil 的 manager（既有测试
// 路径形态）经共享工厂 agentCoreBackgroundTools 接线，appendTool 的 nil 接收
// 者守卫兜底，两工具 Execute 照常工作；journal 闭包字面 nil 时同样照常。
func TestBackgroundToolsJournalNilSafe(t *testing.T) {
	m := &agentAIManager{} // coreJournal 为 nil
	run := bgTestToolRun("jn-1")
	// 生产同款工厂：coreJournal 为 nil 时闭包直达 appendTool 兜底。
	bgTools := m.agentCoreBackgroundTools(run)
	if len(bgTools) != 2 {
		t.Fatalf("工厂应产出两件桥，实测 %d", len(bgTools))
	}
	var out, kill agentcore.Tool
	for _, tool := range bgTools {
		switch tool.Name() {
		case "BashOutput":
			out = tool
		case "KillShell":
			kill = tool
		}
	}
	if out == nil || kill == nil {
		t.Fatalf("工厂产出应含 BashOutput/KillShell，实测 %v", bgTools)
	}

	reg := m.ensureBackgroundShells()
	id := bgTestSpawn(t, reg, "jn-1", "echo jn-hello")
	env := &agentcore.ToolEnv{}

	res := out.Execute(context.Background(), map[string]interface{}{}, env)
	if res.IsError || !strings.Contains(res.Content, id) {
		t.Fatalf("journal 为 nil 管理器下列表应正常，得到 %q (isErr=%v)", res.Content, res.IsError)
	}
	waitFor(t, func() bool {
		c, status, ok := reg.read("jn-1", id)
		return ok && strings.Contains(c, "jn-hello") && strings.Contains(status, "completed")
	})
	res = kill.Execute(context.Background(), map[string]interface{}{"shell_id": id}, env)
	if res.IsError {
		t.Fatalf("journal 为 nil 管理器下 kill 应正常: %s", res.Content)
	}

	// journal 闭包字面 nil：Execute 同样照常（桥内 nil 守卫）。
	out2 := newBashOutputTool(run, reg, nil)
	if res := out2.Execute(context.Background(), map[string]interface{}{"shell_id": id}, env); res.IsError {
		t.Fatalf("journal=nil 时 read 应正常: %s", res.Content)
	}
}

// TestBashOutputReadCutMarkIntact：读路径不二次截的端到端钉子——等任务自然
// 收尾后首读即面对 >64KB 全量，登记处权威截断打 cut-mark（余量下次读取继续），
// 桥直出不得削掉标注尾巴；分次读尽拼接恰为全量载荷，一个不丢。
func TestBashOutputReadCutMarkIntact(t *testing.T) {
	reg := newAgentBackgroundShellRegistry()
	id := bgTestSpawn(t, reg, "cut-1", `head -c 70000 /dev/zero | tr '\0' x`)
	tool := newBashOutputTool(bgTestToolRun("cut-1"), reg, nil)
	env := &agentcore.ToolEnv{}

	// 经 list 观测自然收尾（list 不吃 read 游标），确保首读即触发单次截断。
	waitFor(t, func() bool {
		for _, line := range reg.list("cut-1") {
			if strings.Contains(line, id) && strings.Contains(line, "completed") {
				return true
			}
		}
		return false
	})

	res := tool.Execute(context.Background(), map[string]interface{}{"shell_id": id}, env)
	if res.IsError {
		t.Fatalf("首读不应失败: %s", res.Content)
	}
	cutMark := fmt.Sprintf(agentBackgroundShellCutMarkFmt, agentcore.MaxToolOutputBytes)
	if !strings.Contains(res.Content, cutMark) {
		t.Fatalf("首读应含权威截断标注（桥不削 cut-mark），得到尾部 %q",
			res.Content[max(0, len(res.Content)-120):])
	}

	// 余量继续读尽：载荷拼接恰为全量 70000 字节（状态行无 'x' 字符，可计数）。
	var got strings.Builder
	got.WriteString(res.Content)
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(got.String(), "x") < 70000 && time.Now().Before(deadline) {
		res = tool.Execute(context.Background(), map[string]interface{}{"shell_id": id}, env)
		if res.IsError {
			t.Fatalf("余量读取不应失败: %s", res.Content)
		}
		got.WriteString(res.Content)
	}
	if n := strings.Count(got.String(), "x"); n != 70000 {
		t.Fatalf("载荷应恰为 70000 字节（不丢不重），实得 %d", n)
	}
	if !strings.Contains(got.String(), "completed") {
		t.Fatalf("状态行应含 completed，得到 %q", got.String()[:200])
	}
}

// TestAgentCoreSubagentRegistryIncludesBackgroundTools：BashOutput/KillShell
// 必须进子 registry（TestAgentCoreSubagentRegistryIncludesPeerTools 同款缺口
// 修补钉子）：子 agent 经共享 ToolEnv 能起后台任务，内核成功文案指回这两名
// 消费；缺注入则每次后台启动都撞死胡同工具调用。
func TestAgentCoreSubagentRegistryIncludesBackgroundTools(t *testing.T) {
	m := &agentAIManager{coreJournal: newAgentCoreJournal(t.TempDir())}
	run := agentAIRun{sessionID: "sess-sub-bg"}
	bgTools := m.agentCoreBackgroundTools(run)
	if len(bgTools) != 2 {
		t.Fatalf("bg tools = %d, want 2", len(bgTools))
	}
	bridge := &agentCoreSubagentBridge{
		name:    "runner",
		bgTools: func() []agentcore.Tool { return bgTools },
	}
	registry := bridge.subRegistry(nil, nil)
	for _, want := range []string{"BashOutput", "KillShell"} {
		if _, ok := registry.Get(want); !ok {
			t.Fatalf("sub registry missing background tool %s", want)
		}
	}
	// bgTools=nil（既有测试路径/防御）不注入、不炸。
	plain := (&agentCoreSubagentBridge{name: "x"}).subRegistry(nil, nil)
	if _, ok := plain.Get("BashOutput"); ok {
		t.Fatalf("nil bgTools must not register BashOutput")
	}
}

// TestBackgroundToolsParallelizable：两桥声明可并行（登记处内锁）。
func TestBackgroundToolsParallelizable(t *testing.T) {
	reg := newAgentBackgroundShellRegistry()
	run := bgTestToolRun("px-1")
	var p agentcore.Tool = newBashOutputTool(run, reg, nil)
	var k agentcore.Tool = newKillShellTool(run, reg, nil)
	par, ok := p.(agentcore.Parallelizable)
	if !ok || !par.Parallelizable() {
		t.Fatal("BashOutput 应声明 Parallelizable=true")
	}
	par, ok = k.(agentcore.Parallelizable)
	if !ok || !par.Parallelizable() {
		t.Fatal("KillShell 应声明 Parallelizable=true")
	}
}
