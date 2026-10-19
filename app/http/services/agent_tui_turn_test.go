package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"aliang.one/nursorgate/common/logger"
)

func TestTUITurnErrorCodes(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{newAgentTUITurnError(agentTUIErrUnavailable, "gone"), "tui_unavailable"},
		{newAgentTUITurnError(agentTUIErrInjectFailed, "dial"), "tui_inject_failed"},
		{newAgentTUITurnError(agentTUIErrHeld, "held"), "tui_held"},
		{newAgentTUITurnError(agentTUIErrTranscriptLost, "gone"), "tui_transcript_lost"},
	}
	for _, c := range cases {
		coder, ok := c.err.(interface{ ErrorCode() string })
		if !ok {
			t.Fatalf("%T 未实现 ErrorCode()", c.err)
		}
		if got := coder.ErrorCode(); got != c.want {
			t.Fatalf("ErrorCode() = %q, want %q", got, c.want)
		}
		if c.err.Error() == "" {
			t.Fatalf("Error() 不应为空")
		}
	}
}

// TestAgentAIErrorPayloadCarriesTUITurnCode 锁死 payload 契约：错误码必须经
// errors.As 被 agentAIErrorPayload 拾取并落到 error_code 字段上——否则服务端
// 收不到机器可读原因，只能对错误字符串做脆弱匹配。
func TestAgentAIErrorPayloadCarriesTUITurnCode(t *testing.T) {
	payload := agentAIErrorPayload("sess-1", "m-1", newAgentTUITurnError(agentTUIErrInjectFailed, "无法把消息送进终端会话"))
	if payload["type"] != "ai.error" {
		t.Fatalf("type = %v, want ai.error", payload["type"])
	}
	if payload["error_code"] != agentTUIErrInjectFailed {
		t.Fatalf("error_code = %v, want %q", payload["error_code"], agentTUIErrInjectFailed)
	}
}

func TestTUITurnDecideGates(t *testing.T) {
	// 身份门 stub（真实 ps 探测在测试环境不可控）；信号 stub 顺带清空去抖表。
	// ⚠️ lastStubbedPid 只在信号真被调用后才非零——它不是 pid 来源！
	// pid 必须取 os.Getpid()：isPidAlive 不可覆写，死 pid 会让所有用例恒 not-live。
	_ = stubExternalInterrupt(t, nil)
	pid := os.Getpid()

	// freshHome 为每个 case 造一份独立 home。共用 home 会被后续 case 的
	// writeClaudePidRecord 覆写同一个 <pid>.json，让早先的断言读到被污染的状态。
	freshHome := func(t *testing.T, status, socket string) (tuiTurnInput, string) {
		t.Helper()
		home := t.TempDir()
		writeClaudePidRecord(t, home, "sess-1", pid, status, socket)
		return tuiTurnInput{
			Home: home, Provider: "claude", NativeSessionID: "sess-1",
			JSONLPath: writeTranscriptFixture(t, home, "sess-1"),
		}, home
	}

	// 全部满足 → 有 plan
	base, _ := freshHome(t, "idle", "/tmp/fake-inbox.sock")
	if plan := tuiTurnDecide(base); plan == nil {
		t.Fatal("全部条件满足时应返回 plan")
	}

	// provider 非 claude
	p := base
	p.Provider = "codex"
	if plan := tuiTurnDecide(p); plan != nil {
		t.Fatal("codex 不应注入")
	}
	// auto 允许（imported session 默认值）
	p = base
	p.Provider = "auto"
	if plan := tuiTurnDecide(p); plan == nil {
		t.Fatal("auto 应允许")
	}
	// 无 native id
	p = base
	p.NativeSessionID = ""
	if plan := tuiTurnDecide(p); plan != nil {
		t.Fatal("fresh spawn 不应注入")
	}
	// readOnly
	p = base
	p.ReadOnly = true
	if plan := tuiTurnDecide(p); plan != nil {
		t.Fatal("readOnly 不应注入")
	}
	// goal 回合
	p = base
	p.HasGoalIdentity = true
	if plan := tuiTurnDecide(p); plan != nil {
		t.Fatal("goal 回合不应注入")
	}
	// busy
	busy, _ := freshHome(t, "busy", "/tmp/fake-inbox.sock")
	if plan := tuiTurnDecide(busy); plan != nil {
		t.Fatal("busy 不应注入")
	}
	// 无 socket
	noSock, _ := freshHome(t, "idle", "")
	if plan := tuiTurnDecide(noSock); plan != nil {
		t.Fatal("无 socket 不应注入")
	}
	// record 不存在
	if plan := tuiTurnDecide(tuiTurnInput{Home: base.Home, Provider: "claude", NativeSessionID: "nope", JSONLPath: base.JSONLPath}); plan != nil {
		t.Fatal("无记录不应注入")
	}
}

// writeClaudePidRecord 写 ~/.claude/sessions/<pid>.json，形状对齐真实 CC 记录
// （loadClaudeRenameRecords 只 glob "*.json"，键名必须与之一致）。
func writeClaudePidRecord(t *testing.T, home, sessionID string, pid int, status, socket string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	row := map[string]any{"sessionId": sessionID, "pid": pid, "status": status, "messagingSocketPath": socket}
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// transcriptFixtureDir 是 fixture 统一使用的 project 目录名。fake TUI 追加行时
// 必须用同一个目录，否则会写进一个不存在的目录。
const transcriptFixtureDir = "-tmp-proj"

// transcriptFixturePath 返回 ~/.claude/projects/<transcriptFixtureDir>/<id>.jsonl。
func transcriptFixturePath(home, sessionID string) string {
	return filepath.Join(home, ".claude", "projects", transcriptFixtureDir, sessionID+".jsonl")
}

// writeTranscriptFixture 写 fixture transcript，含一条 user 行让 tail 桥有起点
// （ccPeerSessionJSONL 依赖该布局）。
func writeTranscriptFixture(t *testing.T, home, sessionID string) string {
	t.Helper()
	path := transcriptFixturePath(home, sessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	row := `{"type":"user","uuid":"u-1","isSidechain":false,"isMeta":false,"message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(path, []byte(row), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// appendTranscriptLine 追加一行到 fixture transcript。**禁止 t.Fatal**：它会被
// fake TUI 的 accept goroutine 调用，在非测试 goroutine 里 Fatal 是 Go testing
// 违规（直接让整个进程退出，且不报告是哪个用例失败）。错误只记日志。
func appendTranscriptLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		logger.Info(fmt.Sprintf("test fixture append failed path=%s error=%v", path, err))
		return
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		logger.Info(fmt.Sprintf("test fixture write failed path=%s error=%v", path, err))
	}
}

func TestClassifyTranscriptLine(t *testing.T) {
	cases := []struct {
		name string
		line string
		want transcriptLineKind
	}{
		{"assistant text", `{"type":"assistant","uuid":"a1","isSidechain":false,"apiBlockIndex":1,"message":{"role":"assistant","content":[{"type":"text","text":"你好"}]}}`, transcriptAssistant},
		{"assistant thinking", `{"type":"assistant","uuid":"a2","isSidechain":false,"message":{"content":[{"type":"thinking","thinking":"想"}]}}`, transcriptAssistant},
		{"assistant tool_use", `{"type":"assistant","uuid":"a3","isSidechain":false,"message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`, transcriptAssistant},
		{"sidechain 忽略", `{"type":"assistant","uuid":"a4","isSidechain":true,"message":{"content":[{"type":"text","text":"子代理"}]}}`, transcriptIgnore},
		{"真实 user 回显", `{"type":"user","uuid":"u1","isMeta":false,"message":{"role":"user","content":"问题"}}`, transcriptUserEcho},
		{"tool_result 忽略", `{"type":"user","uuid":"u2","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`, transcriptIgnore},
		{"isMeta 忽略", `{"type":"user","uuid":"u3","isMeta":true,"message":{"role":"user","content":"<command-name>x</command-name>"}}`, transcriptIgnore},
		{"本地命令回显忽略", `{"type":"user","uuid":"u4","message":{"role":"user","content":"<command-name>/effort</command-name>"}}`, transcriptIgnore},
		{"local-command-caveat 忽略", `{"type":"user","uuid":"u5","message":{"role":"user","content":"<local-command-caveat>x</local-command-caveat>"}}`, transcriptIgnore},
		{"local-command-stdout 忽略", `{"type":"user","uuid":"u6","message":{"role":"user","content":"<local-command-stdout>x</local-command-stdout>"}}`, transcriptIgnore},
		{"空行", ``, transcriptIgnore},
		{"坏 JSON", `{not json`, transcriptIgnore},
		{"metadata 行", `{"type":"mode","mode":"normal","sessionId":"s"}`, transcriptIgnore},
	}
	for _, c := range cases {
		if got := classifyTranscriptLine([]byte(c.line)); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestExtractTranscriptBlocks(t *testing.T) {
	line := []byte(`{"type":"assistant","uuid":"a1","apiBlockIndex":2,"message":{"role":"assistant","content":[{"type":"thinking","thinking":"思考"},{"type":"text","text":"答案"},{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`)
	blocks := extractTranscriptBlocks(line)
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3", len(blocks))
	}
	if blocks[0].Kind != "thinking" || blocks[0].Text != "思考" {
		t.Errorf("block0 = %+v", blocks[0])
	}
	if blocks[1].Kind != "text" || blocks[1].Text != "答案" {
		t.Errorf("block1 = %+v", blocks[1])
	}
	if blocks[2].Kind != "tool_use" || blocks[2].ToolName != "Bash" {
		t.Errorf("block2 = %+v", blocks[2])
	}
	// 非 assistant 行 → 无块
	if got := extractTranscriptBlocks([]byte(`{"type":"user","message":{"content":"x"}}`)); got != nil {
		t.Errorf("user 行应无块, got %+v", got)
	}
}

// TestToolUseTouchedPaths 对齐 spawn 路径的 claudeMessageToolUseFilePaths：
// 只认 Write/Edit/MultiEdit、只取 file_path。多取键名或放宽工具名会让手机端
// 的 files_touched_count 与 spawn 路径不一致。
func TestToolUseTouchedPaths(t *testing.T) {
	blocks := []transcriptBlock{
		{Kind: "tool_use", ToolName: "Write", ToolInput: json.RawMessage(`{"file_path":"/a/b.go"}`)},
		{Kind: "tool_use", ToolName: "Edit", ToolInput: json.RawMessage(`{"file_path":"/a/b.go"}`)},
		{Kind: "tool_use", ToolName: "MultiEdit", ToolInput: json.RawMessage(`{"file_path":"/a/c.go"}`)},
		{Kind: "tool_use", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls","file_path":"/a/ignored.go"}`)},
		{Kind: "tool_use", ToolName: "Read", ToolInput: json.RawMessage(`{"file_path":"/a/read.go"}`)},
		{Kind: "text", Text: "不是工具"},
	}
	paths := toolUseTouchedPaths(blocks)
	if len(paths) != 2 || paths[0] != "/a/b.go" || paths[1] != "/a/c.go" {
		t.Fatalf("paths = %v, want [/a/b.go /a/c.go]", paths)
	}
}
