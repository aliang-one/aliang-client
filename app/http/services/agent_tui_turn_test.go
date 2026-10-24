package services

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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
		{newAgentTUITurnError(agentTUIErrAttachment, "download"), "tui_attachment_failed"},
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
		{"turn_duration 结束标记", `{"type":"system","subtype":"turn_duration","uuid":"t1","isSidechain":false,"isMeta":false,"durationMs":1682807,"timestamp":"2026-10-24T00:30:11.922Z"}`, transcriptTurnEnd},
		{"stop_hook_summary 结束标记", `{"type":"system","subtype":"stop_hook_summary","uuid":"t2","isSidechain":false,"timestamp":"2026-10-24T00:30:11.919Z"}`, transcriptTurnEnd},
		{"compact_boundary 忽略", `{"type":"system","subtype":"compact_boundary","uuid":"t3","timestamp":"2026-10-24T00:08:08.963Z"}`, transcriptIgnore},
		{"away_summary 忽略", `{"type":"system","subtype":"away_summary","uuid":"t5"}`, transcriptIgnore},
		{"sidechain system 忽略", `{"type":"system","subtype":"turn_duration","uuid":"t4","isSidechain":true,"durationMs":5}`, transcriptIgnore},
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

func TestRunTUITurnHappyPath(t *testing.T) {
	// 身份门 stub（真实 ps 探测不可控）。pid 用 os.Getpid()：isPidAlive 不可覆写。
	_ = stubExternalInterrupt(t, nil)
	pid := os.Getpid()
	// 心跳是 10s 间隔，测试里不必等它；把轮询间隔压到最短加快用例。
	origPoll := tuiTurnPollInterval
	tuiTurnPollInterval = 20 * time.Millisecond
	defer func() { tuiTurnPollInterval = origPoll }()

	home := t.TempDir()
	sock := ccPeerShortSocketPath(t) // macOS sun_path 103 字节上限
	// fake TUI：收帧后向 transcript 追加 user 回显 + text 块 + tool_use 块
	frames := make(chan string, 8)
	startFakeTUI(t, sock, frames, func(content string) {
		path := transcriptFixturePath(home, "sess-1")
		appendTranscriptLine(t, path, `{"type":"user","uuid":"echo","isMeta":false,"message":{"role":"user","content":`+jsonString(content)+`}}`)
		// text 的值直接用 jsonString 的输出（它自带引号），不要再包一层引号——
		// 否则拼出 ""手机消息"" 让整行 JSON 损坏、被分类器静默忽略。
		appendTranscriptLine(t, path, `{"type":"assistant","uuid":"a1","apiBlockIndex":0,"message":{"content":[{"type":"text","text":`+jsonString("收到："+content)+`}]}}`)
		appendTranscriptLine(t, path, `{"type":"assistant","uuid":"a2","apiBlockIndex":1,"message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"echo hi","cwd":"/tmp"}}]}}`)
	})
	writeClaudePidRecord(t, home, "sess-1", pid, "idle", sock)
	jsonl := writeTranscriptFixture(t, home, "sess-1")

	m := newAgentAIManager() // agent_ai.go:879。⚠️ 不能用 &agentAIManager{}：其 m.sessions 是 nil map，registerAISessionLocked 会 panic
	var mu sync.Mutex
	var events []map[string]any
	write := func(v any) error {
		mu.Lock()
		defer mu.Unlock()
		if p, ok := v.(map[string]any); ok {
			events = append(events, p)
		}
		return nil
	}
	session := &agentAISession{id: "sess-1", projectPath: t.TempDir(), provider: "claude", resumeSessionID: "sess-1"}
	m.registerAISessionLocked(session)
	emitter := m.runEmitter(agentAIRun{runID: "run-1", sessionID: "sess-1", messageID: "m-1"}, write)

	m.runTUITurn(context.Background(), session, agentAIRun{
		runID: "run-1", sessionID: "sess-1", messageID: "m-1",
		projectPath: session.projectPath, provider: "claude",
		resumeSessionID: "sess-1",
	}, "手机消息", &tuiTurnPlan{
		Home: home, NativeSessionID: "sess-1", SocketPath: sock, PID: pid,
		JSONLPath: jsonl, JSONLOffset: mustSize(t, jsonl),
	}, emitter)

	mu.Lock()
	defer mu.Unlock()
	// 帧内容：注入的是原始 content，不是 buildAgentAIPrompt 包装文本
	if len(frames) == 0 {
		t.Fatal("未注入任何帧")
	}
	frame := <-frames
	if !strings.Contains(frame, "手机消息") {
		t.Fatalf("注入帧缺少用户内容: %s", frame)
	}
	if strings.Contains(frame, "You are continuing an Aliang") {
		t.Fatalf("注入帧被 buildAgentAIPrompt 污染: %s", frame)
	}
	// 事件序列
	var seq []string
	for _, e := range events {
		seq = append(seq, fmt.Sprint(e["type"]))
		if e["run_id"] != "run-1" {
			t.Errorf("事件缺 run_id: %v", e)
		}
	}
	// ⚠️ 该期望序列依赖 fake TUI 的写入顺序：user 回显 → text 块 → tool_use 块。
	// 顺序写成 text→tool_use→text 会让断言失败（块按 transcript 出现顺序派发），
	// 那是 fixture 写错了，不是实现错了。
	want := []string{"ai.run.started", "ai.delta", "ai.delta", "ai.command", "ai.done"}
	if strings.Join(seq, ",") != strings.Join(want, ",") {
		t.Fatalf("事件序列 = %v, want %v", seq, want)
	}
	// user 回显的 delta 走 channel=user；assistant 走 channel=assistant
	if events[1]["channel"] != "user" {
		t.Errorf("user 回显 channel = %v, want user", events[1]["channel"])
	}
	if events[2]["channel"] != "assistant" {
		t.Errorf("assistant channel = %v, want assistant", events[2]["channel"])
	}
	// ai.command 必填 item_id + status
	if events[3]["item_id"] == nil || events[3]["status"] != "started" {
		t.Errorf("ai.command 缺 item_id/status: %v", events[3])
	}
	if events[3]["command"] != "Bash" {
		t.Errorf("ai.command command = %v", events[3]["command"])
	}
	// 终态带 executor 标记与统计
	done := events[len(events)-1]
	if done["executor"] != "tui" {
		t.Errorf("ai.done 缺 executor=tui: %v", done)
	}
	if done["files_touched_count"] == nil {
		t.Errorf("ai.done 缺 files_touched_count")
	}
}

// TestRunTUITurnInjectsAttachmentSuffix 锁死：附件清单必须拼进注入文本。
func TestRunTUITurnInjectsAttachmentSuffix(t *testing.T) {
	_ = stubExternalInterrupt(t, nil)
	pid := os.Getpid()
	origPoll := tuiTurnPollInterval
	tuiTurnPollInterval = 20 * time.Millisecond
	defer func() { tuiTurnPollInterval = origPoll }()

	home := t.TempDir()
	sock := ccPeerShortSocketPath(t)
	frames := make(chan string, 4)
	startFakeTUI(t, sock, frames, func(string) {
		path := transcriptFixturePath(home, "sess-1")
		appendTranscriptLine(t, path, `{"type":"user","uuid":"echo","isMeta":false,"message":{"role":"user","content":"看图"}}`)
		appendTranscriptLine(t, path, `{"type":"assistant","uuid":"a1","apiBlockIndex":0,"message":{"content":[{"type":"text","text":"好"}]}}`)
	})
	writeClaudePidRecord(t, home, "sess-1", pid, "idle", sock)
	jsonl := writeTranscriptFixture(t, home, "sess-1")

	m := newAgentAIManager()
	session := &agentAISession{id: "sess-1", projectPath: t.TempDir(), provider: "claude", resumeSessionID: "sess-1"}
	m.registerAISessionLocked(session)
	emitter := m.runEmitter(agentAIRun{runID: "r", sessionID: "sess-1", messageID: "m"}, func(any) error { return nil })

	m.runTUITurn(context.Background(), session, agentAIRun{
		runID: "r", sessionID: "sess-1", messageID: "m",
		projectPath: session.projectPath, provider: "claude", resumeSessionID: "sess-1",
		attachments: []agentAIAttachment{{Type: "image", Path: "/tmp/pic.png", Name: "pic.png"}},
	}, "看图", &tuiTurnPlan{
		Home: home, NativeSessionID: "sess-1", SocketPath: sock, PID: pid,
		JSONLPath: jsonl, JSONLOffset: mustSize(t, jsonl),
	}, emitter)

	frame := <-frames
	if !strings.Contains(frame, "看图") || !strings.Contains(frame, "/tmp/pic.png") {
		t.Fatalf("注入帧缺附件清单: %s", frame)
	}
}

// startFakeTUI 起一个 unix listener，收到帧后回调 onContent(content)。
func startFakeTUI(t *testing.T, sock string, frames chan<- string, onContent func(string)) {
	t.Helper()
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					line := sc.Text()
					frames <- line
					var f struct {
						Type    string `json:"type"`
						Message struct {
							Content string `json:"content"`
						} `json:"message"`
					}
					if json.Unmarshal([]byte(line), &f) == nil && f.Type == "user" {
						onContent(f.Message.Content)
					}
				}
			}(c)
		}
	}()
}

// jsonString 把字符串安全编码为 JSON 字符串字面量（含引号），供测试手工拼
// transcript 行。
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// mustSize 返回文件当前字节数，用于构造 tail 起点。
func mustSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

// waitFor 轮询等待条件成立（测试里替代裸 sleep）。
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within 5s")
}

// TestRunUserMessageRoutesToTUIWhenIdle 锁死接线：TUI 空闲时 runUserMessage 必须
// 改走注入，而不是 spawn。注入被观测为「往 socket 写了帧」+「发出 executor=tui 的
// ai.run.started」。
func TestRunUserMessageRoutesToTUIWhenIdle(t *testing.T) {
	_ = stubExternalInterrupt(t, nil)
	pid := os.Getpid()
	origPoll := tuiTurnPollInterval
	tuiTurnPollInterval = 20 * time.Millisecond
	defer func() { tuiTurnPollInterval = origPoll }()

	home := t.TempDir()
	sock := ccPeerShortSocketPath(t)
	frames := make(chan string, 8)
	startFakeTUI(t, sock, frames, func(content string) {
		path := transcriptFixturePath(home, "sess-1")
		appendTranscriptLine(t, path, `{"type":"user","uuid":"echo","isMeta":false,"message":{"role":"user","content":`+jsonString(content)+`}}`)
		appendTranscriptLine(t, path, `{"type":"assistant","uuid":"a1","apiBlockIndex":0,"message":{"content":[{"type":"text","text":`+jsonString("收到："+content)+`}]}}`)
	})
	writeClaudePidRecord(t, home, "sess-1", pid, "idle", sock)
	writeTranscriptFixture(t, home, "sess-1")

	t.Setenv("HOME", home) // agentHome()/externalTUIHome() 读 HOME

	m := newAgentAIManager()
	var mu sync.Mutex
	var events []map[string]any
	write := func(v any) error {
		mu.Lock()
		defer mu.Unlock()
		if p, ok := v.(map[string]any); ok {
			events = append(events, p)
		}
		return nil
	}
	session := &agentAISession{
		id: "sess-1", projectPath: t.TempDir(), provider: "claude",
		resumeSessionID: "sess-1", mode: "vibe",
	}
	m.registerAISessionLocked(session)
	emitter := m.runEmitter(agentAIRun{runID: "run-route", sessionID: "sess-1", messageID: "m-route"}, write)

	// 直接走 runUserMessage（message()/runStart() 都汇入这里，一处接线全覆盖）。
	if err := m.runUserMessage(session, "run-route", "m-route", "手机消息", "claude", nil, nil, emitter, nil); err != nil {
		t.Fatalf("runUserMessage error = %v", err)
	}

	// 注入了帧
	select {
	case frame := <-frames:
		if !strings.Contains(frame, "手机消息") {
			t.Fatalf("注入帧缺少用户内容: %s", frame)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("未注入任何帧——路由没走到 TUI 执行器")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) == 0 {
		t.Fatal("未发出任何事件")
	}
	if events[0]["type"] != "ai.run.started" || events[0]["executor"] != "tui" {
		t.Fatalf("首个事件 = %v, want ai.run.started/executor=tui", events[0])
	}
}

// TestRunUserMessageFallsBackToSpawnWithoutTUI 锁死反向：没有 TUI 记录时路由必须
// 返回 nil，runUserMessage 仍走 spawn 路径（这里以必然失败的 spawn 观测——错误
// 来自 spawn 层，而非 tui_* 错误码）。
func TestRunUserMessageFallsBackToSpawnWithoutTUI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // 无 pid record → 路由必然不命中

	m := newAgentAIManager()
	var events []map[string]any
	write := func(v any) error {
		if p, ok := v.(map[string]any); ok {
			events = append(events, p)
		}
		return nil
	}
	session := &agentAISession{
		id: "sess-1", projectPath: t.TempDir(), provider: "claude",
		resumeSessionID: "sess-1", mode: "vibe",
	}
	m.registerAISessionLocked(session)
	emitter := m.runEmitter(agentAIRun{runID: "run-spawn", sessionID: "sess-1", messageID: "m-spawn"}, write)

	// provider=claude 但 PATH 里没有 claude CLI（测试环境）→ spawn 层报错。
	err := m.runUserMessage(session, "run-spawn", "m-spawn", "hello", "claude", nil, nil, emitter, nil)
	if err != nil {
		t.Fatalf("runUserMessage error = %v", err)
	}
	for _, e := range events {
		if code, ok := e["error_code"].(string); ok && strings.HasPrefix(code, "tui_") {
			t.Fatalf("无 TUI 时却走了 TUI 路径（error_code=%s）", code)
		}
		if e["executor"] == "tui" {
			t.Fatal("无 TUI 时却标了 executor=tui")
		}
	}
}

// TestRunTUITurnStopSignalsTUIOnce 锁死停止语义：ai.stop 取消 ctx 时必须
// ① 对 TUI pid 恰好发一次 SIGINT（=一次 Esc，TUI 保持打开）
// ② 发出**终态** ai.status(stopped)——非终态的 stopping 会让服务端把 run 挂到
// 10 分钟 stale sweeper 才收尾，卡片被标成失败（2026-10-20 事故）
// ③ 清理 session 占用，后续回合可跑。
func TestRunTUITurnStopSignalsTUIOnce(t *testing.T) {
	// stub 同时替换信号与身份门，并清空去抖表；lastStubbedPid 记录被信号的 pid。
	lastPid := stubExternalInterrupt(t, nil)
	pid := os.Getpid() // ⚠️ lastStubbedPid 不是 pid 来源（信号未调用时恒 0）

	home := t.TempDir()
	sock := ccPeerShortSocketPath(t)
	startFakeTUI(t, sock, make(chan string, 4), func(string) {}) // 只收帧，不写 transcript
	writeClaudePidRecord(t, home, "sess-1", pid, "idle", sock)
	jsonl := writeTranscriptFixture(t, home, "sess-1")

	m := newAgentAIManager()
	var mu sync.Mutex
	var events []map[string]any
	write := func(v any) error {
		mu.Lock()
		defer mu.Unlock()
		if p, ok := v.(map[string]any); ok {
			events = append(events, p)
		}
		return nil
	}
	session := &agentAISession{id: "sess-1", projectPath: t.TempDir(), provider: "claude", resumeSessionID: "sess-1"}
	m.registerAISessionLocked(session)
	emitter := m.runEmitter(agentAIRun{runID: "r", sessionID: "sess-1", messageID: "m"}, write)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.runTUITurn(ctx, session, agentAIRun{
			runID: "r", sessionID: "sess-1", messageID: "m",
			projectPath: session.projectPath, provider: "claude", resumeSessionID: "sess-1",
		}, "x", &tuiTurnPlan{
			Home: home, NativeSessionID: "sess-1", SocketPath: sock, PID: pid,
			JSONLPath: jsonl, JSONLOffset: mustSize(t, jsonl),
		}, emitter)
	}()
	// 等到 started 发出后停止（events 有 mutex 保护，安全跨 goroutine 读）
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) > 0
	})
	cancel()
	<-done

	// 恰好一次 SIGINT，且打在我们记录的 TUI pid 上
	if got := lastStubbedPid(lastPid); got != pid {
		t.Fatalf("SIGINT pid = %d, want %d（恰好一次，且是 TUI 的 pid）", got, pid)
	}
	mu.Lock()
	defer mu.Unlock()
	sawStopped := false
	for _, e := range events {
		if e["type"] == "ai.status" && e["status"] == "stopped" {
			sawStopped = true
		}
	}
	if !sawStopped {
		t.Fatal("未发出终态 ai.status(stopped)")
	}
	// session 占用已清理，后续回合可跑
	m.mu.Lock()
	busy := session.cancel != nil
	m.mu.Unlock()
	if busy {
		t.Fatal("session.cancel 未清理")
	}
}

// TestRunTUITurnFailures 覆盖三条失败路径。核心不变量：失败一律以 ai.error +
// 机器可读 error_code 收尾，且**不回退 spawn**（一次回合只走一条路）。
func TestRunTUITurnFailures(t *testing.T) {
	_ = stubExternalInterrupt(t, nil)
	pid := os.Getpid() // isPidAlive 不可覆写，pid 必须真实存活
	origPoll := tuiTurnPollInterval
	tuiTurnPollInterval = 20 * time.Millisecond
	defer func() { tuiTurnPollInterval = origPoll }()

	// setup 返回「可直接跑的上下文」，home/jsonl/sock 一并带回。
	setup := func(t *testing.T, socketUp, held bool) (*agentAIManager, *agentAISession, *agentAIRunEmitter, string, string, func() []map[string]any) {
		home := t.TempDir()
		sock := ccPeerShortSocketPath(t)
		if socketUp {
			onContent := func(string) {}
			if held {
				// 形态对齐 cc_peer_messaging_test.go:387 的既有 fixture：CC 在
				// transcript 尾部追加一条含 "Held peer message" 的 system 行。
				onContent = func(string) {
					appendTranscriptLine(t, transcriptFixturePath(home, "sess-1"),
						`{"type":"system","subtype":"informational","content":"Held peer message — not delivered to Claude"}`)
				}
			}
			startFakeTUI(t, sock, make(chan string, 4), onContent)
		}
		writeClaudePidRecord(t, home, "sess-1", pid, "idle", sock)
		jsonl := writeTranscriptFixture(t, home, "sess-1")
		m := newAgentAIManager()
		var mu sync.Mutex
		var events []map[string]any
		write := func(v any) error {
			mu.Lock()
			defer mu.Unlock()
			if p, ok := v.(map[string]any); ok {
				events = append(events, p)
			}
			return nil
		}
		session := &agentAISession{id: "sess-1", projectPath: t.TempDir(), provider: "claude", resumeSessionID: "sess-1"}
		m.registerAISessionLocked(session)
		emitter := m.runEmitter(agentAIRun{runID: "r", sessionID: "sess-1", messageID: "m"}, write)
		return m, session, emitter, home, jsonl, func() []map[string]any {
			mu.Lock()
			defer mu.Unlock()
			return append([]map[string]any(nil), events...)
		}
	}

	run := func(m *agentAIManager, session *agentAISession, emitter *agentAIRunEmitter, home, sock, jsonl string) {
		m.runTUITurn(context.Background(), session, agentAIRun{
			runID: "r", sessionID: "sess-1", messageID: "m",
			projectPath: session.projectPath, provider: "claude", resumeSessionID: "sess-1",
		}, "x", &tuiTurnPlan{
			Home: home, NativeSessionID: "sess-1", SocketPath: sock,
			PID: pid, JSONLPath: jsonl, JSONLOffset: mustSize(t, jsonl),
		}, emitter)
	}

	assertLastError := func(t *testing.T, events []map[string]any, wantCode string) {
		t.Helper()
		if len(events) == 0 {
			t.Fatal("未发出任何事件")
		}
		last := events[len(events)-1]
		if last["type"] != "ai.error" {
			t.Fatalf("last type = %v, want ai.error (events=%v)", last["type"], events)
		}
		if last["error_code"] != wantCode {
			t.Fatalf("error_code = %v, want %s (last=%v)", last["error_code"], wantCode, last)
		}
	}

	t.Run("inject_failed", func(t *testing.T) {
		// socket 不存在 → 拨号即失败，且不得回退 spawn。
		m, session, emitter, home, jsonl, events := setup(t, false, false)
		run(m, session, emitter, home, "", jsonl)
		assertLastError(t, events(), "tui_inject_failed")
	})

	t.Run("held", func(t *testing.T) {
		m, session, emitter, home, jsonl, events := setup(t, true, true)
		run(m, session, emitter, home, sockPathFrom(home), jsonl)
		assertLastError(t, events(), "tui_held")
	})

	t.Run("transcript_lost", func(t *testing.T) {
		m, session, emitter, home, jsonl, events := setup(t, true, false)
		sock := sockPathFrom(home)
		// fake TUI 收帧后不写任何行；注入成功即删掉 transcript，让 tail 首轮失败。
		// 经 ccPeerDialInject 旋钮同步串行通知，无跨 goroutine 竞争。
		injected := make(chan struct{})
		origInject := ccPeerDialInject
		ccPeerDialInject = func(socketPath, token, digest, fromMode string) error {
			err := origInject(socketPath, token, digest, fromMode)
			select {
			case <-injected:
			default:
				close(injected)
			}
			return err
		}
		defer func() { ccPeerDialInject = origInject }()
		go func() {
			<-injected
			_ = os.Remove(jsonl)
		}()
		run(m, session, emitter, home, sock, jsonl)
		assertLastError(t, events(), "tui_transcript_lost")
	})
}

// sockPathFrom 反推 setup 用的 socket 路径。ccPeerShortSocketPath 每次调用都建
// 新临时目录，所以 setup 里那次调用的路径只能从 pid 记录里读回来。
func sockPathFrom(home string) string {
	records := loadClaudeRenameRecords(home)
	for _, rec := range records {
		if rec.MessagingSocketPath != "" {
			return rec.MessagingSocketPath
		}
	}
	return ""
}

// TestReadTranscriptFromHalfLine 锁死 tail 桥的边界行为：半行必须留在文件里
// 等下一轮，绝不吐出半个 JSON（否则分类器静默忽略，delta 就丢了）。
func TestReadTranscriptFromHalfLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	full := []byte(`{"type":"user","uuid":"u1","message":{"content":"a"}}` + "\n" + `{"type":"assistant","uuid":"a1","message":{"content":[{"type":"text","text":"b"}]}}`)
	if err := os.WriteFile(path, full, 0o600); err != nil {
		t.Fatal(err)
	}
	// 首轮：只有第一行是完整的
	lines, offset, err := readTranscriptFrom(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("首轮读到 %d 行, want 1（半行必须留下）", len(lines))
	}
	firstLen := int64(len(`{"type":"user","uuid":"u1","message":{"content":"a"}}` + "\n"))
	if offset != firstLen {
		t.Fatalf("offset = %d, want %d", offset, firstLen)
	}
	// 补齐换行后，第二轮应读到第二行
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	lines2, _, err := readTranscriptFrom(path, offset)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines2) != 1 {
		t.Fatalf("第二轮读到 %d 行, want 1", len(lines2))
	}
	if got := transcriptLineUUID(lines2[0]); got != "a1" {
		t.Fatalf("第二行 uuid = %q, want a1", got)
	}
	// 无新数据 → 零行、offset 不变
	lines3, offset3, err := readTranscriptFrom(path, offset+int64(len(lines2[0]))+1)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines3) != 0 {
		t.Fatalf("无新数据却读到 %d 行", len(lines3))
	}
	if offset3 != offset+int64(len(lines2[0]))+1 {
		t.Fatalf("offset 不该变")
	}
	// 文件不存在 → 报错（执行器据此发 tui_transcript_lost）
	if _, _, err := readTranscriptFrom(filepath.Join(t.TempDir(), "missing.jsonl"), 0); err == nil {
		t.Fatal("文件不存在应报错")
	}
}

// TestFeedTUITurnActivity 锁死「tail 桥必须喂活跃度」——2026-10-20 事故的根因：
// activity 创建后从不 bump，idle 看门狗只看到 run 起点，10 分钟就把合法的长
// 回合当卡死杀掉。
func TestFeedTUITurnActivity(t *testing.T) {
	stale := time.Now().Add(-time.Hour).UnixNano()
	fresh := func() *agentAIActivity {
		a := newAgentAIActivity()
		a.lastActivityAt.Store(stale)
		a.lastProgressAt.Store(stale)
		return a
	}
	t.Run("assistant text 块同时刷新活跃与进展", func(t *testing.T) {
		a := fresh()
		feedTUITurnActivity(a, []byte(`{"type":"assistant","uuid":"a1","message":{"content":[{"type":"text","text":"hi"}]}}`))
		if a.idleFor() > time.Minute {
			t.Fatalf("text 块未刷新活跃度: idleFor=%s", a.idleFor())
		}
		if a.progressIdleFor() > time.Minute {
			t.Fatalf("text 块未刷新进展: progressIdleFor=%s", a.progressIdleFor())
		}
	})
	t.Run("tool_use 暂停 idle 判定，tool_result 恢复", func(t *testing.T) {
		a := fresh()
		feedTUITurnActivity(a, []byte(`{"type":"assistant","uuid":"a2","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]}}`))
		if !a.idlePaused() {
			t.Fatal("tool_use 后应暂停 idle 判定（工具在跑不是卡死）")
		}
		if got := a.pendingToolUses.Load(); got != 1 {
			t.Fatalf("pendingToolUses = %d, want 1", got)
		}
		// tool_result 行被 classifyTranscriptLine 判成 ignore，但活跃度仍必须喂。
		feedTUITurnActivity(a, []byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`))
		if a.idlePaused() {
			t.Fatal("tool_result 后应恢复 idle 判定")
		}
		if got := a.pendingToolUses.Load(); got != 0 {
			t.Fatalf("pendingToolUses = %d, want 0", got)
		}
		if a.idleFor() > time.Minute {
			t.Fatalf("tool_result 未刷新活跃度: idleFor=%s", a.idleFor())
		}
	})
	t.Run("坏行不 panic、不改工具计数", func(t *testing.T) {
		a := fresh()
		feedTUITurnActivity(a, nil)
		feedTUITurnActivity(a, []byte("   "))
		feedTUITurnActivity(a, []byte("{not json"))
		if got := a.pendingToolUses.Load(); got != 0 {
			t.Fatalf("坏行改了工具计数: %d", got)
		}
		// nil activity 必须安全（spawn 路径可能没有 activity）
		feedTUITurnActivity(nil, []byte(`{"type":"assistant"}`))
	})
}

// TestRunTUITurnDownloadsImageAttachments（2026-10-23 方案 A + 持久缓存）：
// TUI 注入通道是纯文本，图片附件必须先下载成本地文件、以本地路径注入（TUI
// 侧的 claude 用 Read 工具读图）。锁死三件事：①注入帧列本地路径而非
// URL/data URL；②帧里带 Read 指示；③缓存文件在回合结束后保留，供同一会话
// 后续回合再次 Read。
func TestRunTUITurnDownloadsImageAttachments(t *testing.T) {
	_ = stubExternalInterrupt(t, nil)
	pid := os.Getpid()
	origPoll := tuiTurnPollInterval
	tuiTurnPollInterval = 20 * time.Millisecond
	defer func() { tuiTurnPollInterval = origPoll }()

	home := t.TempDir()
	cacheDir := t.TempDir()
	t.Setenv("ALIANG_ATTACHMENT_CACHE_DIR", cacheDir)
	sock := ccPeerShortSocketPath(t)
	// 1×1 PNG；data URL 直接解码，不联网，host 白名单仅作为「已配置」占位。
	const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	wantBytes, err := base64.StdEncoding.DecodeString(pngB64)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	injectedPath := ""
	fileDuringTurn := false
	frames := make(chan string, 8)
	startFakeTUI(t, sock, frames, func(content string) {
		mu.Lock()
		defer mu.Unlock()
		if !strings.Contains(content, "shot.png") {
			return
		}
		if strings.Contains(content, "data:image") {
			t.Errorf("注入帧包含原始 data URL，应替换为本地路径")
			return
		}
		if !strings.Contains(content, "请使用 Read 工具") {
			t.Errorf("带图片附件的注入帧缺 Read 指示: %s", content)
		}
		line := ""
		for _, l := range strings.Split(content, "\n") {
			if strings.HasPrefix(l, "- shot.png: ") {
				line = l
				break
			}
		}
		if line == "" {
			t.Errorf("注入帧缺附件路径清单: %s", content)
			return
		}
		p := strings.TrimSpace(strings.TrimPrefix(line, "- shot.png: "))
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("回合中读取下载文件失败: %v", err)
			return
		}
		if string(raw) != string(wantBytes) {
			t.Errorf("下载文件内容不符: got %d bytes", len(raw))
			return
		}
		injectedPath = p
		fileDuringTurn = true
		// 回写 transcript 让 tail 桥看到回显并安静收尾（否则本用例会一直空转到天上限）。
		path := transcriptFixturePath(home, "sess-1")
		appendTranscriptLine(t, path, `{"type":"user","uuid":"echo","isMeta":false,"message":{"role":"user","content":`+jsonString(content)+`}}`)
		appendTranscriptLine(t, path, `{"type":"assistant","uuid":"a1","apiBlockIndex":0,"message":{"content":[{"type":"text","text":"看到了"}]}}`)
	})
	writeClaudePidRecord(t, home, "sess-1", pid, "idle", sock)
	jsonl := writeTranscriptFixture(t, home, "sess-1")

	m := newAgentAIManager()
	var events []map[string]any
	write := func(v any) error {
		if p, ok := v.(map[string]any); ok {
			mu.Lock()
			events = append(events, p)
			mu.Unlock()
		}
		return nil
	}
	session := &agentAISession{id: "sess-1", projectPath: t.TempDir(), provider: "claude", resumeSessionID: "sess-1"}
	m.registerAISessionLocked(session)
	emitter := m.runEmitter(agentAIRun{runID: "run-1", sessionID: "sess-1", messageID: "m-1"}, write)

	m.runTUITurn(context.Background(), session, agentAIRun{
		runID: "run-1", sessionID: "sess-1", messageID: "m-1",
		projectPath: session.projectPath, provider: "claude",
		resumeSessionID: "sess-1",
		attachments: []agentAIAttachment{{
			Type: "image", Name: "shot.png",
			URL: "data:image/png;base64," + pngB64,
		}},
		attachmentAllowedHosts: []string{"cos.example.com"},
	}, "看这张图", &tuiTurnPlan{
		Home: home, NativeSessionID: "sess-1", SocketPath: sock, PID: pid,
		JSONLPath: jsonl, JSONLOffset: mustSize(t, jsonl),
	}, emitter)

	mu.Lock()
	defer mu.Unlock()
	if !fileDuringTurn || injectedPath == "" {
		t.Fatal("回合中未能确认下载文件（注入帧未带本地路径或文件不可读）")
	}
	if !strings.HasPrefix(injectedPath, cacheDir+string(os.PathSeparator)) {
		t.Fatalf("附件应落在持久缓存目录 %s，实际 %s", cacheDir, injectedPath)
	}
	if _, err := os.Stat(injectedPath); err != nil {
		t.Fatalf("回合结束后缓存文件应保留（后续回合还要 Read）: %v", err)
	}
}

// TestRunTUITurnAttachmentDownloadFails：下载失败 → ai.error(error_code=
// tui_attachment_failed)，不注入、不发 ai.done。http 明文 URL 在白名单已配置
// 的前提下必被 https 门拒绝，环境无关、结果确定。
func TestRunTUITurnAttachmentDownloadFails(t *testing.T) {
	_ = stubExternalInterrupt(t, nil)
	pid := os.Getpid()
	origPoll := tuiTurnPollInterval
	tuiTurnPollInterval = 20 * time.Millisecond
	defer func() { tuiTurnPollInterval = origPoll }()

	home := t.TempDir()
	sock := ccPeerShortSocketPath(t)
	writeClaudePidRecord(t, home, "sess-1", pid, "idle", sock)
	jsonl := writeTranscriptFixture(t, home, "sess-1")

	m := newAgentAIManager()
	var events []map[string]any
	write := func(v any) error {
		if p, ok := v.(map[string]any); ok {
			events = append(events, p)
		}
		return nil
	}
	session := &agentAISession{id: "sess-1", projectPath: t.TempDir(), provider: "claude", resumeSessionID: "sess-1"}
	m.registerAISessionLocked(session)
	emitter := m.runEmitter(agentAIRun{runID: "run-1", sessionID: "sess-1", messageID: "m-1"}, write)

	m.runTUITurn(context.Background(), session, agentAIRun{
		runID: "run-1", sessionID: "sess-1", messageID: "m-1",
		projectPath: session.projectPath, provider: "claude",
		resumeSessionID: "sess-1",
		attachments: []agentAIAttachment{{
			Type: "image", Name: "shot.png",
			URL: "http://insecure.example/shot.png",
		}},
		attachmentAllowedHosts: []string{"cos.example.com"},
	}, "看这张图", &tuiTurnPlan{
		Home: home, NativeSessionID: "sess-1", SocketPath: sock, PID: pid,
		JSONLPath: jsonl, JSONLOffset: mustSize(t, jsonl),
	}, emitter)

	var seq []string
	var errCode string
	for _, e := range events {
		seq = append(seq, fmt.Sprint(e["type"]))
		if e["type"] == "ai.error" {
			errCode, _ = e["error_code"].(string)
		}
	}
	if strings.Join(seq, ",") != "ai.run.started,ai.error" {
		t.Fatalf("事件序列 = %v, want [ai.run.started ai.error]", seq)
	}
	if errCode != agentTUIErrAttachment {
		t.Fatalf("error_code = %q, want %q", errCode, agentTUIErrAttachment)
	}
}

// TestRunTUITurnCompletesOnTurnEnd 锁死第①层完成信号：CC 亲写的
// turn_duration/stop_hook_summary 行 = 回合结束，必须立即完成——不再要求用户
// 回显、安静轮与 pid 记录 idle（记录滞留 busy 也要完成）。
// 回归背景（2026-10-24）：旧完成门三条件缺一不可，任一失配（回显分类变化 /
// 记录滞留 busy）回合就挂到看门狗或上限，手机永远「运行中」最后标失败。
func TestRunTUITurnCompletesOnTurnEnd(t *testing.T) {
	_ = stubExternalInterrupt(t, nil)
	pid := os.Getpid()
	origPoll := tuiTurnPollInterval
	tuiTurnPollInterval = 20 * time.Millisecond
	defer func() { tuiTurnPollInterval = origPoll }()
	// 上限压到 2s：若回归（完成信号失效），用例在 2s 内按 error 收尾失败，
	// 而不是挂满默认 4h。
	origCeiling := tuiTurnCeiling
	tuiTurnCeiling = 2 * time.Second
	defer func() { tuiTurnCeiling = origCeiling }()

	home := t.TempDir()
	sock := ccPeerShortSocketPath(t)
	// pid 记录全程 busy：证明完成不依赖记录翻 idle（CC 崩溃/滞留场景）。
	writeClaudePidRecord(t, home, "sess-1", pid, "busy", sock)
	// fake TUI：注入后只写 assistant 文本 + 回合结束标记，不写 user 回显——
	// 证明完成也不依赖回显（回显分类失配场景）。
	frames := make(chan string, 8)
	startFakeTUI(t, sock, frames, func(content string) {
		path := transcriptFixturePath(home, "sess-1")
		appendTranscriptLine(t, path, `{"type":"assistant","uuid":"a1","apiBlockIndex":0,"message":{"content":[{"type":"text","text":`+jsonString("完成："+content)+`}]}}`)
		appendTranscriptLine(t, path, `{"type":"system","subtype":"stop_hook_summary","uuid":"t-1","isSidechain":false,"timestamp":"2026-10-24T00:30:11.919Z"}`)
		appendTranscriptLine(t, path, `{"type":"system","subtype":"turn_duration","uuid":"t-2","isSidechain":false,"isMeta":false,"durationMs":86228,"timestamp":"2026-10-24T00:30:11.922Z"}`)
	})
	jsonl := writeTranscriptFixture(t, home, "sess-1")

	m := newAgentAIManager()
	var mu sync.Mutex
	var events []map[string]any
	write := func(v any) error {
		mu.Lock()
		defer mu.Unlock()
		if p, ok := v.(map[string]any); ok {
			events = append(events, p)
		}
		return nil
	}
	session := &agentAISession{id: "sess-1", projectPath: t.TempDir(), provider: "claude", resumeSessionID: "sess-1"}
	m.registerAISessionLocked(session)
	emitter := m.runEmitter(agentAIRun{runID: "run-1", sessionID: "sess-1", messageID: "m-1"}, write)

	m.runTUITurn(context.Background(), session, agentAIRun{
		runID: "run-1", sessionID: "sess-1", messageID: "m-1",
		projectPath: session.projectPath, provider: "claude",
		resumeSessionID: "sess-1",
	}, "手机消息", &tuiTurnPlan{
		Home: home, NativeSessionID: "sess-1", SocketPath: sock, PID: pid,
		JSONLPath: jsonl, JSONLOffset: mustSize(t, jsonl),
	}, emitter)

	mu.Lock()
	defer mu.Unlock()
	var seq []string
	var done map[string]any
	for _, e := range events {
		seq = append(seq, fmt.Sprint(e["type"]))
		if e["type"] == "ai.done" {
			done = e
		}
	}
	if strings.Join(seq, ",") != "ai.run.started,ai.delta,ai.done" {
		t.Fatalf("事件序列 = %v, want [ai.run.started ai.delta ai.done]", seq)
	}
	if done == nil {
		t.Fatal("缺 ai.done")
	}
	if got := fmt.Sprint(done["turn_duration_ms"]); got != "86228" {
		t.Errorf("turn_duration_ms = %v, want 86228", done["turn_duration_ms"])
	}
}

// TestRunTUITurnCompletesOnBusyToIdle 锁死第②层完成信号：老 CC 不写回合结束
// 标记、回显分类失配（无 user 回显）时，pid 记录 busy→idle 转变 + 安静轮仍
// 必须完成回合——不能回退成「没回显就永不完成」。
func TestRunTUITurnCompletesOnBusyToIdle(t *testing.T) {
	_ = stubExternalInterrupt(t, nil)
	pid := os.Getpid()
	origPoll := tuiTurnPollInterval
	tuiTurnPollInterval = 20 * time.Millisecond
	defer func() { tuiTurnPollInterval = origPoll }()
	origCeiling := tuiTurnCeiling
	tuiTurnCeiling = 2 * time.Second
	defer func() { tuiTurnCeiling = origCeiling }()

	home := t.TempDir()
	sock := ccPeerShortSocketPath(t)
	// events/mu 先于 fake TUI 声明：idle 翻转协程要观察 tail 是否已消费 delta。
	var mu sync.Mutex
	var events []map[string]any
	recordPath := filepath.Join(home, ".claude", "sessions", strconv.Itoa(pid)+".json")
	if err := os.MkdirAll(filepath.Dir(recordPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRecord := func(status string) {
		b, _ := json.Marshal(map[string]any{"sessionId": "sess-1", "pid": pid, "status": status, "messagingSocketPath": sock})
		if err := os.WriteFile(recordPath, b, 0o600); err != nil {
			t.Errorf("write pid record: %v", err)
		}
	}
	writeRecord("busy")
	// fake TUI：只写 assistant 文本、不写回显。注入后 held 观测窗（3s）内 tail
	// 不会启动，所以 idle 翻转必须等 tail 真正消费过 delta 再做——否则记录在
	// busy-poll 之前就变 idle，busy→idle 转变根本不会被观察到。
	frames := make(chan string, 8)
	startFakeTUI(t, sock, frames, func(content string) {
		path := transcriptFixturePath(home, "sess-1")
		appendTranscriptLine(t, path, `{"type":"assistant","uuid":"a1","apiBlockIndex":0,"message":{"content":[{"type":"text","text":`+jsonString("完成")+"}]}}")
		go func() {
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				mu.Lock()
				seen := len(events)
				mu.Unlock()
				// started + delta = tail 已跑完第一轮（含 busy-poll）。
				if seen >= 2 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			time.Sleep(60 * time.Millisecond) // 留足一轮 busy-poll 观察窗
			writeRecord("idle")
		}()
	})
	jsonl := writeTranscriptFixture(t, home, "sess-1")

	m := newAgentAIManager()
	write := func(v any) error {
		mu.Lock()
		defer mu.Unlock()
		if p, ok := v.(map[string]any); ok {
			events = append(events, p)
		}
		return nil
	}
	session := &agentAISession{id: "sess-1", projectPath: t.TempDir(), provider: "claude", resumeSessionID: "sess-1"}
	m.registerAISessionLocked(session)
	emitter := m.runEmitter(agentAIRun{runID: "run-1", sessionID: "sess-1", messageID: "m-1"}, write)

	m.runTUITurn(context.Background(), session, agentAIRun{
		runID: "run-1", sessionID: "sess-1", messageID: "m-1",
		projectPath: session.projectPath, provider: "claude",
		resumeSessionID: "sess-1",
	}, "手机消息", &tuiTurnPlan{
		Home: home, NativeSessionID: "sess-1", SocketPath: sock, PID: pid,
		JSONLPath: jsonl, JSONLOffset: mustSize(t, jsonl),
	}, emitter)

	mu.Lock()
	defer mu.Unlock()
	var seq []string
	for _, e := range events {
		seq = append(seq, fmt.Sprint(e["type"]))
	}
	if strings.Join(seq, ",") != "ai.run.started,ai.delta,ai.done" {
		t.Fatalf("事件序列 = %v, want [ai.run.started ai.delta ai.done]", seq)
	}
}

// TestSteerTUITurnQueuesIntoSocket 锁死 TUI 回合的 steer 通道：插话经 UDS 注入
// 同一个 TUI（CC 排队语义），ack=queued，内容入会话历史。回归背景：此前 TUI
// 回合的 steer 一律 unsupported，消息被静默丢弃，只剩一条「Steer failed」事件。
// 注：held 观测窗 3s 内无 held 标记才放行，用例整体 ~3s。
func TestSteerTUITurnQueuesIntoSocket(t *testing.T) {
	pid := os.Getpid()
	home := t.TempDir()
	sock := ccPeerShortSocketPath(t)
	frames := make(chan string, 8)
	startFakeTUI(t, sock, frames, func(string) {})
	jsonl := writeTranscriptFixture(t, home, "sess-1")

	m := newAgentAIManager()
	var mu sync.Mutex
	var acks []map[string]any
	session := &agentAISession{id: "sess-1", projectPath: t.TempDir(), provider: "claude", resumeSessionID: "sess-1"}
	m.registerAISessionLocked(session)
	m.mu.Lock()
	session.cancel = func() {}
	session.tuiTurn = &tuiTurnPlan{
		Home: home, NativeSessionID: "sess-1", SocketPath: sock, PID: pid,
		JSONLPath: jsonl, JSONLOffset: mustSize(t, jsonl),
	}
	m.mu.Unlock()

	m.steer(map[string]interface{}{
		"session_id": "sess-1", "message_id": "m-1", "content": "插话",
	}, func(v any) error {
		mu.Lock()
		defer mu.Unlock()
		if p, ok := v.(map[string]any); ok {
			acks = append(acks, p)
		}
		return nil
	})

	mu.Lock()
	if len(acks) != 1 || acks[0]["result"] != "queued" {
		t.Fatalf("ack = %v, want result=queued", acks)
	}
	mu.Unlock()
	select {
	case frame := <-frames:
		if !strings.Contains(frame, "插话") {
			t.Fatalf("注入帧缺少内容: %s", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未注入任何帧")
	}
	m.mu.Lock()
	n := len(session.history)
	m.mu.Unlock()
	if n != 1 {
		t.Fatalf("history = %d, want 1", n)
	}
}

// TestSteerTUITurnHeldReported 锁死 held 上报：TUI 扣留注入时 steer 必须 ack
// error/held，不能假装已送达。
func TestSteerTUITurnHeldReported(t *testing.T) {
	pid := os.Getpid()
	home := t.TempDir()
	sock := ccPeerShortSocketPath(t)
	frames := make(chan string, 8)
	startFakeTUI(t, sock, frames, func(content string) {
		// ccPeerWatchHeld 以字节匹配「Held peer message」，原样写一行。
		appendTranscriptLine(t, transcriptFixturePath(home, "sess-1"), `{"note":"Held peer message"}`)
	})
	jsonl := writeTranscriptFixture(t, home, "sess-1")

	m := newAgentAIManager()
	var mu sync.Mutex
	var acks []map[string]any
	session := &agentAISession{id: "sess-1", projectPath: t.TempDir(), provider: "claude", resumeSessionID: "sess-1"}
	m.registerAISessionLocked(session)
	m.mu.Lock()
	session.cancel = func() {}
	session.tuiTurn = &tuiTurnPlan{
		Home: home, NativeSessionID: "sess-1", SocketPath: sock, PID: pid,
		JSONLPath: jsonl, JSONLOffset: mustSize(t, jsonl),
	}
	m.mu.Unlock()

	m.steer(map[string]interface{}{
		"session_id": "sess-1", "message_id": "m-1", "content": "插话",
	}, func(v any) error {
		mu.Lock()
		defer mu.Unlock()
		if p, ok := v.(map[string]any); ok {
			acks = append(acks, p)
		}
		return nil
	})

	mu.Lock()
	defer mu.Unlock()
	if len(acks) != 1 {
		t.Fatalf("ack 数 = %d", len(acks))
	}
	if acks[0]["result"] != "error" || acks[0]["code"] != "held" {
		t.Fatalf("ack = %v, want result=error code=held", acks[0])
	}
	// held 的插话不得入历史（它没进对话）。
	m.mu.Lock()
	n := len(session.history)
	m.mu.Unlock()
	if n != 0 {
		t.Fatalf("history = %d, want 0", n)
	}
}

// TestSteerTUITurnAttachmentRejected 锁死 v1 边界：带附件的 TUI 插话明确拒绝
// （注入帧是纯文本），不静默丢附件也不静默放行。
func TestSteerTUITurnAttachmentRejected(t *testing.T) {
	pid := os.Getpid()
	home := t.TempDir()
	sock := ccPeerShortSocketPath(t)
	frames := make(chan string, 8)
	startFakeTUI(t, sock, frames, func(string) {})
	jsonl := writeTranscriptFixture(t, home, "sess-1")

	m := newAgentAIManager()
	var mu sync.Mutex
	var acks []map[string]any
	session := &agentAISession{id: "sess-1", projectPath: t.TempDir(), provider: "claude", resumeSessionID: "sess-1"}
	m.registerAISessionLocked(session)
	m.mu.Lock()
	session.cancel = func() {}
	session.tuiTurn = &tuiTurnPlan{
		Home: home, NativeSessionID: "sess-1", SocketPath: sock, PID: pid,
		JSONLPath: jsonl, JSONLOffset: mustSize(t, jsonl),
	}
	m.mu.Unlock()

	m.steer(map[string]interface{}{
		"session_id": "sess-1", "message_id": "m-1", "content": "看图",
		"attachments": []interface{}{map[string]interface{}{"type": "image", "url": "http://x/y.png"}},
	}, func(v any) error {
		mu.Lock()
		defer mu.Unlock()
		if p, ok := v.(map[string]any); ok {
			acks = append(acks, p)
		}
		return nil
	})

	mu.Lock()
	defer mu.Unlock()
	if len(acks) != 1 || acks[0]["result"] != "error" || acks[0]["code"] != "unsupported_attachment" {
		t.Fatalf("ack = %v, want result=error code=unsupported_attachment", acks)
	}
	select {
	case frame := <-frames:
		t.Fatalf("拒绝路径不得注入帧: %s", frame)
	case <-time.After(200 * time.Millisecond):
	}
}
