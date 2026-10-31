package services

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lineageEntry 是测试与实现共用的最小解析视图：只关心链路三要素。
type lineageEntry struct {
	Uuid       string `json:"uuid"`
	ParentUuid string `json:"parentUuid"`
	Entrypoint string `json:"entrypoint"`
	Type       string `json:"type"`
}

func writeLineageLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func lineageLine(uuid, parent, entrypoint, typ string) string {
	e := map[string]string{
		"uuid":       uuid,
		"type":       typ,
		"entrypoint": entrypoint,
	}
	if parent != "" {
		e["parentUuid"] = parent
	}
	b, _ := json.Marshal(e)
	return string(b)
}

func readLineageLines(t *testing.T, path string) []lineageEntry {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []lineageEntry
	for _, ln := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		var e lineageEntry
		if err := json.Unmarshal(ln, &e); err != nil {
			t.Fatalf("line no longer valid json: %v (%s)", err, ln[:60])
		}
		out = append(out, e)
	}
	return out
}

// 分叉形态 = 交互(cli)链尖之后，agent 无头回合(sdk-cli)以兄弟分支追加——
// 2026-09-26 b1f82c61 / 2026-09-28 3d06bb04 两次实测的病灶。
func TestAppendLineageMarkerStitchesDiverged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeLineageLines(t, path, []string{
		lineageLine("u1", "", "cli", "user"),
		lineageLine("u2", "u1", "cli", "assistant"),
		lineageLine("u3", "u2", "cli", "system"), // cli 链尖
		lineageLine("u4", "u3", "sdk-cli", "user"),
		lineageLine("u5", "u4", "sdk-cli", "assistant"), // 文件真实尖端
	})
	if err := appendLineageMarker(path, "sid-1", "/proj", "电话回合已并入"); err != nil {
		t.Fatal(err)
	}
	entries := readLineageLines(t, path)
	if len(entries) != 6 {
		t.Fatalf("entries = %d, want 6 (5 + marker)", len(entries))
	}
	marker := entries[len(entries)-1]
	if marker.Entrypoint != "cli" {
		t.Fatalf("marker entrypoint = %q, want cli (resume 只沿最新 cli 链回溯)", marker.Entrypoint)
	}
	if marker.ParentUuid != "u5" {
		t.Fatalf("marker parent = %q, want file tip u5", marker.ParentUuid)
	}
	if marker.Uuid == "" {
		t.Fatal("marker must carry a uuid")
	}
}

func TestAppendLineageMarkerNoOpWhenLinear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeLineageLines(t, path, []string{
		lineageLine("u1", "", "cli", "user"),
		lineageLine("u2", "u1", "sdk-cli", "assistant"),
		lineageLine("u3", "u2", "cli", "user"), // 最新 cli 链已含文件尖端
	})
	before, _ := os.ReadFile(path)
	if err := appendLineageMarker(path, "sid-1", "/proj", "n/a"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("linear history must not be touched")
	}
}

func TestAppendLineageMarkerNoOpWithoutCliHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeLineageLines(t, path, []string{
		lineageLine("u1", "", "sdk-cli", "user"),
		lineageLine("u2", "u1", "sdk-cli", "assistant"),
	})
	before, _ := os.ReadFile(path)
	if err := appendLineageMarker(path, "sid-1", "/proj", "n/a"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("sdk-only history has no interactive chain to stitch — must stay untouched")
	}
}

func TestAppendLineageMarkerIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeLineageLines(t, path, []string{
		lineageLine("u1", "", "cli", "user"),
		lineageLine("u2", "u1", "sdk-cli", "assistant"),
	})
	if err := appendLineageMarker(path, "sid-1", "/proj", "n/a"); err != nil {
		t.Fatal(err)
	}
	afterFirst, _ := os.ReadFile(path)
	if err := appendLineageMarker(path, "sid-1", "/proj", "n/a"); err != nil {
		t.Fatal(err)
	}
	afterSecond, _ := os.ReadFile(path)
	if !bytes.Equal(afterFirst, afterSecond) {
		t.Fatal("second pass must be a no-op (marker already extends the cli chain)")
	}
}

// 1MB+ 病态行不得冻结谱系分析（v1.1.41 事故的回归护栏）。
func TestAppendLineageMarkerSurvivesHugeLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	huge := `{"uuid":"big","parentUuid":"u2","entrypoint":"cli","type":"user","content":"` +
		strings.Repeat("x", 2*1024*1024) + `"}`
	writeLineageLines(t, path, []string{
		lineageLine("u1", "", "cli", "user"),
		lineageLine("u2", "u1", "cli", "assistant"),
		huge,
		lineageLine("u4", "u2", "sdk-cli", "assistant"), // 与大行分叉
	})
	if err := appendLineageMarker(path, "sid-1", "/proj", "n/a"); err != nil {
		t.Fatal(err)
	}
	entries := readLineageLines(t, path)
	last := entries[len(entries)-1]
	if last.ParentUuid != "u4" || last.Entrypoint != "cli" {
		t.Fatalf("marker after huge line wrong: parent=%q entrypoint=%q, want u4/cli", last.ParentUuid, last.Entrypoint)
	}
}

// continuedInLine 构造一条 continued-in 标记行（CC /fork / continuation 落盘
// 形态，2026-10-30 1dc69343 jsonl 实测）。
func continuedInLine(target string) string {
	b, _ := json.Marshal(map[string]string{
		"type":                 "continued-in",
		"continuedInSessionId": target,
	})
	return string(b)
}

func metaLine(typ string) string {
	b, _ := json.Marshal(map[string]string{"type": typ})
	return string(b)
}

func userLine(typ string, isMeta bool) string {
	e := map[string]interface{}{
		"type":    "user",
		"isMeta":  isMeta,
		"message": map[string]interface{}{"role": "user", "content": "继续。"},
	}
	b, _ := json.Marshal(e)
	return string(b)
}

// TestCcSessionContinuedInTargetRetired 锁死事故形态：continued-in 标记之后
// 只有 queue-operation 与元数据行（入队永不 dequeue 的 2026-10-30 形态），
// 必须判定退役并返回续接 id。
func TestCcSessionContinuedInTargetRetired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.jsonl")
	writeLineageLines(t, path, []string{
		lineageLine("u1", "", "cli", "user"),
		lineageLine("a1", "u1", "cli", "assistant"),
		continuedInLine("9bd1d36d-151c-48d9-8e36-7b0bea135e91"),
		metaLine("queue-operation"),
		metaLine("last-prompt"),
		metaLine("mode"),
		metaLine("permission-mode"),
		metaLine("cost-state"),
	})
	id, retired := ccSessionContinuedInTarget(path)
	if !retired {
		t.Fatalf("标记后仅有入队/元数据行，应判定退役")
	}
	if id != "9bd1d36d-151c-48d9-8e36-7b0bea135e91" {
		t.Fatalf("id = %q", id)
	}
}

// TestCcSessionContinuedInTargetStillActive 锁死活会话形态：标记后仍有真实
// 对话（旧会话被继续使用）不得重定向。
func TestCcSessionContinuedInTargetStillActive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.jsonl")
	writeLineageLines(t, path, []string{
		lineageLine("u1", "", "cli", "user"),
		continuedInLine("fork-target"),
		userLine("", false),
		lineageLine("a2", "", "cli", "assistant"),
		metaLine("permission-mode"),
	})
	if _, retired := ccSessionContinuedInTarget(path); retired {
		t.Fatalf("标记后存在真实对话活动，不应判定退役")
	}
}

// TestCcSessionContinuedInTargetToolResultNoise 锁死 [2] 评审定谳：标记后的
// tool_result 形态 user 行是噪声（classifyTranscriptLine 判 ignore），不得
// 击穿退役判定——否则一条噪声行让退役会话永远重定向失败，事故形态回归。
func TestCcSessionContinuedInTargetToolResultNoise(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.jsonl")
	toolResult, _ := json.Marshal(map[string]interface{}{
		"type": "user",
		"message": map[string]interface{}{
			"role":    "user",
			"content": []map[string]string{{"type": "tool_result", "content": "ok"}},
		},
	})
	writeLineageLines(t, path, []string{
		lineageLine("u1", "", "cli", "user"),
		continuedInLine("fork-target"),
		string(toolResult),
		metaLine("cost-state"),
	})
	id, retired := ccSessionContinuedInTarget(path)
	if !retired {
		t.Fatalf("tool_result 噪声行不应击穿退役判定")
	}
	if id != "fork-target" {
		t.Fatalf("id = %q", id)
	}
}

// TestCcSessionContinuedInTargetNegative 无标记/空目标/坏行容忍。
func TestCcSessionContinuedInTargetNegative(t *testing.T) {
	dir := t.TempDir()
	if _, retired := ccSessionContinuedInTarget(filepath.Join(dir, "missing.jsonl")); retired {
		t.Fatalf("文件不存在不应判定退役")
	}
	path := filepath.Join(dir, "plain.jsonl")
	writeLineageLines(t, path, []string{
		lineageLine("u1", "", "cli", "user"),
		lineageLine("a1", "u1", "cli", "assistant"),
		"not-json-at-all",
	})
	if _, retired := ccSessionContinuedInTarget(path); retired {
		t.Fatalf("无 continued-in 标记不应判定退役")
	}
	path2 := filepath.Join(dir, "empty-target.jsonl")
	writeLineageLines(t, path2, []string{continuedInLine("")})
	if _, retired := ccSessionContinuedInTarget(path2); retired {
		t.Fatalf("空续接 id 不应判定退役")
	}
}

// TestCcFollowContinuedInChain 覆盖链跟随：A→B 活、A→B→C、环、断链。
func TestCcFollowContinuedInChain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := filepath.Join(home, ".claude", "projects", "prj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(id string, lines []string) {
		writeLineageLines(t, filepath.Join(proj, id+".jsonl"), lines)
	}
	// ① A 退役→B，B 活：链尖 = B
	write("a", []string{lineageLine("au", "", "cli", "user"), continuedInLine("b")})
	write("b", []string{lineageLine("bu", "", "cli", "user")})
	id, retired := ccFollowContinuedInChain(home, "a", agentAIContinuedInMaxHops)
	if !retired || id != "b" {
		t.Fatalf("① got id=%q retired=%v", id, retired)
	}
	// ② A→B→C 全退役：链尖 = C
	write("b", []string{continuedInLine("c")})
	write("c", []string{lineageLine("cu", "", "cli", "user")})
	id, retired = ccFollowContinuedInChain(home, "a", agentAIContinuedInMaxHops)
	if !retired || id != "c" {
		t.Fatalf("② got id=%q retired=%v", id, retired)
	}
	// ③ 环：c→a，必须终止且不 panic
	write("c", []string{continuedInLine("a")})
	id, retired = ccFollowContinuedInChain(home, "a", agentAIContinuedInMaxHops)
	if !retired || id == "" {
		t.Fatalf("③ 环场景应返回非空 id, got id=%q retired=%v", id, retired)
	}
	// ④ 断链：A→ghost（jsonl 不存在）——回退到最近可恢复的祖先 A 本身
	// （调用方 id==current 守卫自然退化成不重定向，绝不把 resume 指向幽灵 id）
	write("a", []string{continuedInLine("ghost")})
	id, retired = ccFollowContinuedInChain(home, "a", agentAIContinuedInMaxHops)
	if !retired || id != "a" {
		t.Fatalf("④ got id=%q retired=%v", id, retired)
	}
}

// TestRedirectAgentAIContinuedSession 锁定 runUserMessage 的重定向入口行为：
// claude 族退役目标被改写（session 与 manager 同步），非 claude 族原样返回。
func TestRedirectAgentAIContinuedSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := filepath.Join(home, ".claude", "projects", "prj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	writeLineageLines(t, filepath.Join(proj, "old-native.jsonl"), []string{
		lineageLine("u1", "", "cli", "user"),
		continuedInLine("new-native"),
		metaLine("queue-operation"),
	})
	writeLineageLines(t, filepath.Join(proj, "new-native.jsonl"), []string{
		lineageLine("u2", "", "cli", "user"),
	})
	m := newAgentAIManager()
	session := &agentAISession{id: "ai_sess1", resumeSessionID: "old-native"}
	m.sessions[session.id] = session
	// 绑定指向退役 id：重定向必须同帧换绑，否则 confirmBinding 以旧 id 为期望
	// 值，重定向后的 spawn 全部死于 "native session id mismatch"（[5] 评审定谳）。
	m.bindings[session.id] = agentAIBindingRecord{
		ConversationID: session.id, Provider: "claude",
		NativeSessionID: "old-native", State: "confirmed", BindingVersion: 2,
	}

	if got := m.redirectAgentAIContinuedSession(session, "claude"); got != "new-native" {
		t.Fatalf("claude 族应重定向, got %q", got)
	}
	if session.resumeSessionID != "new-native" || m.sessions[session.id].resumeSessionID != "new-native" {
		t.Fatalf("session/manager 恢复点未同步: %q / %q", session.resumeSessionID, m.sessions[session.id].resumeSessionID)
	}
	if got := m.bindings[session.id].NativeSessionID; got != "new-native" {
		t.Fatalf("绑定未换绑: %q", got)
	}
	if v := m.bindings[session.id].BindingVersion; v != 2 {
		t.Fatalf("换绑不得动 BindingVersion: %d", v)
	}
	// 非 claude 族不探测
	session2 := &agentAISession{id: "ai_sess2", resumeSessionID: "old-native"}
	m.sessions[session2.id] = session2
	if got := m.redirectAgentAIContinuedSession(session2, "codex"); got != "old-native" {
		t.Fatalf("codex 不应重定向, got %q", got)
	}
	// 活会话不重定向
	session3 := &agentAISession{id: "ai_sess3", resumeSessionID: "new-native"}
	m.sessions[session3.id] = session3
	if got := m.redirectAgentAIContinuedSession(session3, "claude"); got != "new-native" {
		t.Fatalf("活会话不应重定向, got %q", got)
	}
}
