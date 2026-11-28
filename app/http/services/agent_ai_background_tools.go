package services

import (
	"context"
	"fmt"
	"strings"

	"aliang.one/nursorgate/app/agentcore"
)

// 后台 shell 查询/终止工具（2026-11-28 后台 shell 批，Task 5）。
//
// BashOutput / KillShell 两个桥工具，把宿主登记处（agentBackgroundShellRegistry，
// agent_ai_background_shell.go）暴露给内核 agentcore.Tool 面——内核 Bash 的
// run_in_background 分支返回文案指引模型用这两名消费后台任务。
//
// 形状与 peer 桥（agentCorePeerToolBridge）同款约定：
//   - per-run 无状态桥，登记处经 runAgentCore 注入（readOnly run 不注入，
//     与 peer/子 agent 同判据——后台启动是写动作）；
//   - Parallelizable=true：登记处 read/kill/list 内锁自洽，与子 agent 桥同判据；
//   - journal 落行自带（内核 OnToolCall 只记内置名集，桥接工具自带 onCall，
//     不重不漏）；summary 截断 200 与 peer 桥同款 rune 安全截断。
//
// 会话隔离：read/kill/list 以显式 sessionID（run.sessionID）判定归属，登记处
// 内部强制 `rec.session != sessionID → not found`，无需 sessionView 适配器
// （适配器只服务于内核 Begin 无会话参数的注入面）。

// agentBackgroundToolListRuneCap 是列表路径的 rune 封顶：list 行内 command 折叠
// 空白，但单条命令本身可以任意长（多 MB 单行命令会把列表撑爆），取内核
// agentcore.MaxToolOutputBytes 同量级封顶（防漂移随动）。读路径不在其列——
// 登记处单次交付的 64KB 字节封顶是权威截断（cut-mark 已随 chunk 交付，余量
// 下次读取继续），桥不二次截，见 bashOutputTool.Execute。
const agentBackgroundToolListRuneCap = int(agentcore.MaxToolOutputBytes)

// bgToolJournal 落 journal 工具行：summary 压平换行 + rune 截断 200（peer 桥
// 同款），result 落原文（journal appendTool 侧自行只存失败结果）。
func bgToolJournal(journal func(tool, summary string, isErr bool, result string), tool, out string, isErr bool) {
	if journal == nil {
		return
	}
	summary := truncateAgentText(strings.ReplaceAll(out, "\n", " "), 200)
	journal(tool, summary, isErr, out)
}

// bgToolMissingContent 构造「未找到后台任务」错误文案：列出当前会话可见 id
// 助模型自纠（打到不存在/已逐出/他Session 的 id 时不用再盲猜一轮）。
func bgToolMissingContent(reg *agentBackgroundShellRegistry, sessionID, id string) string {
	visible := reg.list(sessionID)
	shown := "（无）"
	if len(visible) > 0 {
		shown = strings.Join(visible, ", ")
	}
	return fmt.Sprintf("未找到后台任务 %s（当前可见: %s）", id, shown)
}

// agentCoreBackgroundTools 是后台 shell 查询/终止两桥的共享构造（runAgentCore
// 位置 B 与子 agent 桥 bgTools 工厂两个消费者；peerTools 先例同款——只依赖
// m 与 run，调用方无需改签名）。journal 闭包绑本 run 会话，appendTool nil
// 接收者安全（coreJournal 为空的既有测试路径直达兜底）。
//
// readOnly 守卫内置（2026-11-28 评审建议）：与 peer 先例同款在工厂内自查，
// 未来第三处调用点漏闸也不会给只读 run 注入 kill 能力。
func (m *agentAIManager) agentCoreBackgroundTools(run agentAIRun) []agentcore.Tool {
	if run.readOnly {
		return nil
	}
	bgReg := m.ensureBackgroundShells()
	bgJournal := func(tool, summary string, isErr bool, result string) {
		m.coreJournal.appendTool(run.sessionID, tool, summary, isErr, result)
	}
	return []agentcore.Tool{
		newBashOutputTool(run, bgReg, bgJournal),
		newKillShellTool(run, bgReg, bgJournal),
	}
}

// ---------------------------------------------------------------------------
// BashOutput
// ---------------------------------------------------------------------------

// bashOutputTool 列出/增量读取后台任务输出。只读：进 readonly-tools 自动
// 放行（agent_approval_policy.go，后台 shell 批 2026-11-28）。无 shell_id 时
// 列全部（模型自主发现）；带 id 时读增量。状态行由登记处拼装（read/list 返回，
// started=%ds 前 中文口径）。
type bashOutputTool struct {
	run     agentAIRun
	reg     *agentBackgroundShellRegistry
	journal func(tool, summary string, isErr bool, result string)
}

func newBashOutputTool(run agentAIRun, reg *agentBackgroundShellRegistry, journal func(tool, summary string, isErr bool, result string)) *bashOutputTool {
	return &bashOutputTool{run: run, reg: reg, journal: journal}
}

func (t *bashOutputTool) Name() string { return "BashOutput" }

func (t *bashOutputTool) Description() string {
	return "查看后台任务的输出与状态。不带 shell_id 时列出当前会话全部后台任务" +
		"（每行一条状态行）；带 shell_id 时增量返回新输出（已读过的部分不重发）" +
		"与该任务的状态行。适用于 Bash 的 run_in_background 启动的任务。"
}

func (t *bashOutputTool) InputSchema() map[string]interface{} {
	return peerToolSchema(nil, map[string]interface{}{
		"shell_id": map[string]interface{}{
			"type":        "string",
			"description": "后台任务 id（Bash run_in_background 返回的 shell_id）。省略时列出全部任务",
		},
	})
}

// Parallelizable 声明可并行：登记处 read/list 内锁自洽（与子 agent 桥同判据）。
func (t *bashOutputTool) Parallelizable() bool { return true }

func (t *bashOutputTool) Execute(ctx context.Context, input map[string]interface{}, _ *agentcore.ToolEnv) agentcore.ToolResult {
	id := strings.TrimSpace(inputStringField(input, "shell_id"))
	out, isErr := "", false
	if id == "" {
		// 无 id：列全部（创建序，每行一条状态行，行首即 id）。列表路径按
		// rune 封顶防病态超长命令行撑爆（见 agentBackgroundToolListRuneCap）。
		if lines := t.reg.list(t.run.sessionID); len(lines) == 0 {
			out = "当前没有后台任务"
		} else {
			out = truncateAgentText(strings.Join(lines, "\n"), agentBackgroundToolListRuneCap)
		}
	} else if chunk, status, ok := t.reg.read(t.run.sessionID, id); !ok {
		out, isErr = bgToolMissingContent(t.reg, t.run.sessionID, id), true
	} else {
		// 状态行 + 增量直出：登记处单次交付的 64KB 字节封顶是权威截断
		// （cut-mark 已随 chunk 交付，余量下次读取继续），桥不二次截——
		// 二次截会把 cut-mark 尾巴削掉、余量提示丢失。
		out = status + "\n" + chunk
	}
	bgToolJournal(t.journal, t.Name(), out, isErr)
	return agentcore.ToolResult{Content: out, IsError: isErr}
}

// ---------------------------------------------------------------------------
// KillShell
// ---------------------------------------------------------------------------

// killShellTool 终止后台任务：只及登记处名下进程（组杀整树），不碰任意
// PID——风险面=任务自身启动物，故与 BashOutput 同进 readonly 自动放行。
// 幂等：对已自然结束的任务安全回报（内核闭包有存活探测，死树不误杀）。
type killShellTool struct {
	run     agentAIRun
	reg     *agentBackgroundShellRegistry
	journal func(tool, summary string, isErr bool, result string)
}

func newKillShellTool(run agentAIRun, reg *agentBackgroundShellRegistry, journal func(tool, summary string, isErr bool, result string)) *killShellTool {
	return &killShellTool{run: run, reg: reg, journal: journal}
}

func (t *killShellTool) Name() string { return "KillShell" }

func (t *killShellTool) Description() string {
	return "终止一个由 Bash 的 run_in_background 启动的后台任务（进程组整树终止）。" +
		"对已自然结束的任务调用同样安全（会回报其已结束）。只能终止本会话登记在案的任务。"
}

func (t *killShellTool) InputSchema() map[string]interface{} {
	return peerToolSchema([]string{"shell_id"}, map[string]interface{}{
		"shell_id": map[string]interface{}{
			"type":        "string",
			"description": "要终止的后台任务 id（Bash run_in_background 返回的 shell_id）",
		},
	})
}

// Parallelizable 声明可并行：登记处 kill 内锁 + 闭包锁外调（与子 agent 桥同判据）。
func (t *killShellTool) Parallelizable() bool { return true }

func (t *killShellTool) Execute(ctx context.Context, input map[string]interface{}, _ *agentcore.ToolEnv) agentcore.ToolResult {
	id := strings.TrimSpace(inputStringField(input, "shell_id"))
	out, isErr := "", false
	if id == "" {
		out, isErr = "缺少 shell_id 参数（可先不带参数调 BashOutput 列出后台任务）", true
	} else if found, wasRunning := t.reg.kill(t.run.sessionID, id); !found {
		out, isErr = bgToolMissingContent(t.reg, t.run.sessionID, id), true
	} else if wasRunning {
		out = "已终止 shell_id=" + id + "。输出可用 BashOutput 追溯。"
	} else {
		out = "shell_id=" + id + " 已结束，无需终止（状态见 BashOutput）。"
	}
	bgToolJournal(t.journal, t.Name(), out, isErr)
	return agentcore.ToolResult{Content: out, IsError: isErr}
}
