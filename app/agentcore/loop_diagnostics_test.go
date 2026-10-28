package agentcore

import (
	"context"
	"strings"
	"testing"
)

// 这一组锁死 OnTurn 逐回合诊断快照的语义：回合快照每轮一次、回合数递增、
// 终态快照每 run 恰好一次且携带终止真因。诊断面板的实时通道与 journal 终态
// 行都消费这份数据。

// TestLoopOnTurnSnapshotsProgressAndTerminal：正常收尾的 run——回合快照随
// 回合递增（Terminal=false），终态快照一次且 Reason 为空（成功）。
func TestLoopOnTurnSnapshotsProgressAndTerminal(t *testing.T) {
	turns := []string{
		toolTurn("toolu_d1", "Stub", `{"x":"step-1"}`),
		toolTurn("toolu_d2", "Stub", `{"x":"step-2"}`),
		textTurn("干完了"),
	}
	srv := scriptServer(t, turns)
	defer srv.Close()

	registry := NewRegistry()
	registry.Register(&stubTool{name: "Stub", result: ToolResult{Content: "ok"}})

	snapshots := []TurnSnapshot{}
	res, err := Run(context.Background(), &Config{
		Client:        NewClient(srv.URL),
		Model:         "claude-x",
		Tools:         registry,
		ContextWindow: 123456,
		Hooks: LoopHooks{
			OnTurn: func(snap TurnSnapshot) { snapshots = append(snapshots, snap) },
		},
	}, []Message{TextMessage("user", "正常两步")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("不应失败: %q", res.Text)
	}

	// 3 个回合快照 + 1 个终态快照。
	if len(snapshots) != 4 {
		t.Fatalf("快照数 = %d, want 4（3 回合 + 1 终态）", len(snapshots))
	}
	for i, want := range []int{1, 2, 3} {
		got := snapshots[i]
		if got.Turns != want {
			t.Errorf("快照[%d].Turns = %d, want %d", i, got.Turns, want)
		}
		if got.Terminal {
			t.Errorf("快照[%d] 不应是终态", i)
		}
		if got.ContextWindow != 123456 {
			t.Errorf("快照[%d].ContextWindow = %d, want 123456（cfg 原值）", i, got.ContextWindow)
		}
		if got.ElapsedMs < 0 {
			t.Errorf("快照[%d].ElapsedMs = %d, 应非负", i, got.ElapsedMs)
		}
	}
	terminal := snapshots[3]
	if !terminal.Terminal {
		t.Fatal("最后一个快照必须是终态")
	}
	if terminal.Turns != 3 {
		t.Errorf("终态.Turns = %d, want 3", terminal.Turns)
	}
	if terminal.Reason != "" {
		t.Errorf("成功收尾的终态 Reason 应为空, got %q", terminal.Reason)
	}
	// 守卫计数的语义：dupStreak 从「当前签名的第 1 轮」起算（每换参数即重置
	// 为 1），正常收尾终态是 1/3 而不是 0；errStreak/truncStreak 遇成功清零。
	if terminal.DupStreak != 1 {
		t.Errorf("正常收尾终态 DupStreak = %d, want 1（当前签名第 1 轮）", terminal.DupStreak)
	}
	if terminal.ErrStreak != 0 || terminal.TruncStreak != 0 {
		t.Errorf("正常收尾 err/trunc 应清零, got err=%d trunc=%d",
			terminal.ErrStreak, terminal.TruncStreak)
	}
}

// TestLoopOnTurnTerminalCarriesGuardReason：无进展守卫判死——终态快照必须
// 携带真因与触发的守卫计数，journal end 行与面板的「为什么死」都出自这里。
func TestLoopOnTurnTerminalCarriesGuardReason(t *testing.T) {
	turns := make([]string, 0, maxIdenticalToolTurns+2)
	for i := 0; i < maxIdenticalToolTurns+2; i++ {
		turns = append(turns, toolTurn("toolu_dup", "Stub", `{"x":"always-the-same"}`))
	}
	srv := scriptServer(t, turns)
	defer srv.Close()

	registry := NewRegistry()
	registry.Register(&stubTool{name: "Stub", result: ToolResult{Content: "ok"}})

	snapshots := []TurnSnapshot{}
	res, err := Run(context.Background(), &Config{
		Client: NewClient(srv.URL),
		Model:  "claude-x",
		Tools:  registry,
		Hooks: LoopHooks{
			OnTurn: func(snap TurnSnapshot) { snapshots = append(snapshots, snap) },
		},
	}, []Message{TextMessage("user", "重复干活")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError {
		t.Fatal("无进展必须以终止帧收场")
	}

	var terminal *TurnSnapshot
	for i := range snapshots {
		if snapshots[i].Terminal {
			if terminal != nil {
				t.Fatal("终态快照多于一个")
			}
			terminal = &snapshots[i]
		}
	}
	if terminal == nil {
		t.Fatal("缺终态快照")
	}
	if !strings.Contains(terminal.Reason, "无进展") {
		t.Errorf("终态 Reason 应点名无进展, got %q", terminal.Reason)
	}
	if terminal.DupStreak != maxIdenticalToolTurns {
		t.Errorf("终态 DupStreak = %d, want %d", terminal.DupStreak, maxIdenticalToolTurns)
	}
	// 真因必须与终止帧带给手机的文案同源（finish 的 errText 前缀）。
	if !strings.HasPrefix(terminal.Reason, "模型连续") {
		t.Errorf("终态 Reason 应与 stopWithSummary 真因同源, got %q", terminal.Reason)
	}
}

// TestLoopOnTurnNilHook：钩子缺省时零开销直通（既有 Run 路径不经过任何
// 新分支）。
func TestLoopOnTurnNilHook(t *testing.T) {
	srv := scriptServer(t, []string{textTurn("好")})
	defer srv.Close()

	res, err := Run(context.Background(), &Config{
		Client: NewClient(srv.URL),
		Model:  "claude-x",
	}, []Message{TextMessage("user", "无钩子")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError || !strings.Contains(res.Text, "好") {
		t.Fatalf("无钩子路径不应受影响: %q", res.Text)
	}
}
