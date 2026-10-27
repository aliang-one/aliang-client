package services

import (
	"strings"
	"testing"
)

// 这一组锁死「手机端上下文窗口设置」在宿主侧的三个落点：
//  1. withClaudeAutoCompact 把窗口钉成 CLI 的 --autocompact 参数
//     （位置必须正确：argv 模式下插在 prompt 之前，stdin 模式下追加尾部）；
//  2. claude 的 compact_boundary 帧 / 斜杠命令标记被翻译成用户可读的增量；
//  3. agentcore 的历史上限按窗口换算（否则大窗口设置是空话），
//     且 /compact 在空历史上本地收敛、不发网络请求。

func seedClaudeAutoCompactProbe(t *testing.T, path string, available bool) {
	t.Helper()
	// 不存在的路径：executableProbeCacheKey 退化为 path 本身，测试可直接种缓存，
	// 避免真的去 exec 一个假 claude。
	key := executableProbeCacheKey(path)
	if key != path {
		t.Fatalf("fake 路径的缓存键 %q != path %q（测试前提被破坏）", key, path)
	}
	claudeAutoCompactProbeCache.Store(key, available)
	t.Cleanup(func() { claudeAutoCompactProbeCache.Delete(key) })
}

func TestWithClaudeAutoCompactInsertsFlag(t *testing.T) {
	path := "/nonexistent/claude-ctx-window-argv"
	seedClaudeAutoCompactProbe(t, path, true)

	tool := &agentAITool{
		path:         path,
		args:         []string{"--print", "--verbose", "hello prompt"},
		outputFormat: agentAIOutputClaudeStreamJSON,
	}
	got := withClaudeAutoCompact(tool, 200_000)
	want := []string{"--print", "--verbose", "--autocompact", "200000", "hello prompt"}
	if strings.Join(got.args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", got.args, want)
	}
	// 原 tool 不得被就地修改（调用方可能复用/重试）。
	if len(tool.args) != 3 || tool.args[2] != "hello prompt" {
		t.Fatalf("原 tool.args 被就地修改: %#v", tool.args)
	}
}

func TestWithClaudeAutoCompactStdinAppendsTail(t *testing.T) {
	path := "/nonexistent/claude-ctx-window-stdin"
	seedClaudeAutoCompactProbe(t, path, true)

	tool := &agentAITool{
		path:         path,
		args:         []string{"--print", "--resume", "r1"},
		stdinPrompt:  "hello from stdin",
		outputFormat: agentAIOutputClaudeStreamJSON,
	}
	got := withClaudeAutoCompact(tool, 150_000)
	want := []string{"--print", "--resume", "r1", "--autocompact", "150000"}
	if strings.Join(got.args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", got.args, want)
	}
}

func TestWithClaudeAutoCompactSkips(t *testing.T) {
	supported := "/nonexistent/claude-ctx-window-supported"
	seedClaudeAutoCompactProbe(t, supported, true)
	unsupported := "/nonexistent/claude-ctx-window-unsupported"
	seedClaudeAutoCompactProbe(t, unsupported, false)

	cases := []struct {
		name   string
		tool   *agentAITool
		window int
	}{
		{"nil tool", nil, 200_000},
		{"空 args", &agentAITool{path: supported, outputFormat: agentAIOutputClaudeStreamJSON}, 200_000},
		{"非 claude 输出格式", &agentAITool{path: supported, args: []string{"run"}, outputFormat: agentAIOutputCodexJSON}, 200_000},
		{"窗口未设置", &agentAITool{path: supported, args: []string{"--print", "p"}, outputFormat: agentAIOutputClaudeStreamJSON}, 0},
		{"旧版 CLI 不认 --autocompact", &agentAITool{path: unsupported, args: []string{"--print", "p"}, outputFormat: agentAIOutputClaudeStreamJSON}, 200_000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := withClaudeAutoCompact(c.tool, c.window)
			if got != c.tool {
				t.Fatalf("应原样返回（不复制、不加参数）")
			}
		})
	}
}

func TestWithClaudeAutoCompactClampsWindow(t *testing.T) {
	path := "/nonexistent/claude-ctx-window-clamp"
	seedClaudeAutoCompactProbe(t, path, true)

	tool := &agentAITool{
		path:         path,
		args:         []string{"--print", "p"},
		outputFormat: agentAIOutputClaudeStreamJSON,
	}
	got := withClaudeAutoCompact(tool, 50_000)
	if len(got.args) < 4 || got.args[1] != "--autocompact" || got.args[2] != "100000" {
		t.Fatalf("低于下限应钳到 100k，args = %#v", got.args)
	}
}

func TestClaudeCompactNotice(t *testing.T) {
	cases := []struct {
		name  string
		event map[string]interface{}
		want  string
	}{
		{
			name: "snake_case 数字",
			event: map[string]interface{}{
				"type": "system", "subtype": "compact_boundary",
				"compact_metadata": map[string]interface{}{
					"trigger": "manual", "pre_tokens": float64(180_000), "post_tokens": float64(20_000),
				},
			},
			want: "✓ 上下文已压缩：约 180k → 20k tokens。\n",
		},
		{
			name: "字符串 token（CLI 有版本按 String 序列化）",
			event: map[string]interface{}{
				"type": "system", "subtype": "compact_boundary",
				"compact_metadata": map[string]interface{}{
					"trigger": "auto", "pre_tokens": "150000", "post_tokens": "5000",
				},
			},
			want: "✓ 上下文已压缩：约 150k → 5k tokens。\n",
		},
		{
			name: "camelCase 兜底",
			event: map[string]interface{}{
				"type": "system", "subtype": "compact_boundary",
				"compactMetadata": map[string]interface{}{
					"trigger": "manual", "preTokens": float64(1_000_000), "postTokens": float64(42_000),
				},
			},
			want: "✓ 上下文已压缩：约 1m → 42k tokens。\n",
		},
		{
			name: "只有压缩前 token",
			event: map[string]interface{}{
				"type": "system", "subtype": "compact_boundary",
				"compact_metadata": map[string]interface{}{"pre_tokens": float64(320_000)},
			},
			want: "✓ 上下文已压缩（压缩前约 320k tokens）。\n",
		},
		{
			name: "auto 且无 token 数据",
			event: map[string]interface{}{
				"type": "system", "subtype": "compact_boundary",
				"compact_metadata": map[string]interface{}{"trigger": "auto"},
			},
			want: "✓ 上下文已自动压缩。\n",
		},
		{
			name: "无 metadata",
			event: map[string]interface{}{
				"type": "system", "subtype": "compact_boundary",
			},
			want: "✓ 上下文已压缩。\n",
		},
		{
			name:  "非 compact_boundary 帧",
			event: map[string]interface{}{"type": "assistant", "subtype": "compact_boundary"},
			want:  "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := claudeCompactNotice(c.event); got != c.want {
				t.Fatalf("notice = %q, want %q", got, c.want)
			}
		})
	}
}

func TestStripLocalCommandMarkup(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "stdout 去壳留正文",
			in:   "<local-command-stdout>✓ 上下文已压缩：约 180k → 20k tokens。</local-command-stdout>",
			want: "✓ 上下文已压缩：约 180k → 20k tokens。",
		},
		{
			name: "caveat 块整体丢弃",
			in:   "<local-command-caveat>Caveat: 这段是给模型的提示</local-command-caveat>压缩完成",
			want: "压缩完成",
		},
		{
			name: "未闭合标签容错",
			in:   "<local-command-stdout>hello",
			want: "hello",
		},
		{
			name: "普通文本只做 trim",
			in:   "  普通回复  ",
			want: "普通回复",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripLocalCommandMarkup(c.in); got != c.want {
				t.Fatalf("strip = %q, want %q", got, c.want)
			}
		})
	}
}

func TestAgentAITokenCountLabel(t *testing.T) {
	cases := []struct {
		tokens int
		want   string
	}{
		{500, "500"},
		{1000, "1k"},
		{1500, "1.5k"},
		{200_000, "200k"},
		{1_000_000, "1m"},
		{1_500_000, "1.5m"},
	}
	for _, c := range cases {
		if got := agentAITokenCountLabel(c.tokens); got != c.want {
			t.Errorf("label(%d) = %q, want %q", c.tokens, got, c.want)
		}
	}
}

func TestAgentAIHistoryLimits(t *testing.T) {
	cases := []struct {
		provider      string
		contextWindow int
		wantMessages  int
		wantChars     int
	}{
		{"agentcore", 200_000, 2000, 400_000},
		{"agentcore", 0, 2000, 400_000},      // 未设置 → 与内核 200k 默认对齐
		{"agentcore", 50_000, 1000, 200_000}, // 钳到下限 100k
		{"agentcore", 1_000_000, 10000, 2_000_000},
		{"claudecode", 200_000, agentAIHistoryDefaultMessages, agentAIHistoryDefaultChars},
		{"codex", 1_000_000, agentAIHistoryDefaultMessages, agentAIHistoryDefaultChars},
	}
	for _, c := range cases {
		gotMessages, gotChars := agentAIHistoryLimits(c.provider, c.contextWindow)
		if gotMessages != c.wantMessages || gotChars != c.wantChars {
			t.Errorf("limits(%s,%d) = (%d,%d), want (%d,%d)",
				c.provider, c.contextWindow, gotMessages, gotChars, c.wantMessages, c.wantChars)
		}
	}
}

// TestAgentAINativeCompactTurn 锁 claude 原生 /compact 的透传判定：命令必须
// 原样到达 CLI（有 --resume 才成立），其余 provider / 普通消息一律走本地路径。
func TestAgentAINativeCompactTurn(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		provider string
		resume   string
		want     bool
	}{
		{"claude 带 resume 的原生命令", "/compact", "claudecode", "resume-1", true},
		{"大小写不敏感", "/COMPACT", "claude", "resume-1", true},
		{"带参数也算", "/compact 保留最近两轮", "claudecode", "resume-1", true},
		{"无 resume 时不能透传", "/compact", "claudecode", "", false},
		{"codex 走自己的压缩通道", "/compact", "codex", "resume-1", false},
		{"agentcore 由内核管理", "/compact", "agentcore", "resume-1", false},
		{"普通消息", "帮我看看这个 bug", "claudecode", "resume-1", false},
	}
	for _, c := range cases {
		if got := agentAINativeCompactTurn(c.content, c.provider, c.resume); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// TestHandleAgentCoreCompactCommandEmptyHistory 锁本地收敛：空历史不得发任何
// 上游请求（历史判空在 agentCoreEnsureUpstream 之前），事件序列
// run.started → delta(提示语) → done。
func TestHandleAgentCoreCompactCommandEmptyHistory(t *testing.T) {
	mgr := newAgentAIManager()
	sess := newSlashTestSession()
	sess.provider = "agentcore"
	sess.history = nil
	mu, events, w := captureAIWriter(t)

	mgr.handleAgentCoreCompactCommand(sess, "msg_compact_1", w)

	started := findAIEvents(mu, events, "ai.run.started")
	if len(started) != 1 {
		t.Fatalf("ai.run.started 数量 = %d, want 1", len(started))
	}
	if started[0]["provider"] != "agentcore" || started[0]["state"] != "running" {
		t.Fatalf("run.started 载荷异常: %#v", started[0])
	}
	deltas := findAIEvents(mu, events, "ai.delta")
	if len(deltas) != 1 {
		t.Fatalf("ai.delta 数量 = %d, want 1", len(deltas))
	}
	delta, _ := deltas[0]["delta"].(string)
	if !strings.Contains(delta, "没有可压缩的上下文") {
		t.Fatalf("delta = %q, 应提示无上下文可压缩", delta)
	}
	if len(findAIEvents(mu, events, "ai.done")) != 1 {
		t.Fatalf("ai.done 数量 = %d, want 1", len(findAIEvents(mu, events, "ai.done")))
	}
	if n := len(findAIEvents(mu, events, "ai.error")); n != 0 {
		t.Fatalf("空历史不应产生 ai.error，got %d", n)
	}
}
