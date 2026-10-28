package services

import (
	"strings"
	"testing"
)

func TestParseLocalSlashCommand(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantArgs string
		wantOK   bool
	}{
		{"", "", "", false},
		{"hello", "", "", false},
		{"/", "", "", false},
		{"/clear", "clear", "", true},
		{"/model glm-5.2", "model", "glm-5.2", true},
		{"/COMPACT", "compact", "", true}, // case-insensitive name
		{"/clear   extra  args", "clear", "extra  args", true},
	}
	for _, c := range cases {
		name, args, ok := parseLocalSlashCommand(c.in)
		if ok != c.wantOK || name != c.wantName || args != c.wantArgs {
			t.Errorf("parseLocalSlashCommand(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.in, name, args, ok, c.wantName, c.wantArgs, c.wantOK)
		}
	}
}

func newSlashTestSession() *agentAISession {
	return &agentAISession{
		id:              "s1",
		mode:            "vibe",
		projectPath:     "/proj",
		provider:        "claudecode",
		model:           "glm-5.2",
		resumeSessionID: "resume-abc",
		history:         []agentAIMessage{{Role: "user", Content: "prior"}},
		activity:        newAgentAIActivity(),
	}
}

func TestHandleLocalSlashCommand(t *testing.T) {
	t.Run("clear resets session continuity", func(t *testing.T) {
		mgr := newAgentAIManager()
		sess := newSlashTestSession()
		mu, events, w := captureAIWriter(t)
		if !mgr.handleLocalSlashCommand(sess, "msg_1", "/clear", "claudecode", w) {
			t.Fatal("expected /clear to be handled")
		}
		if sess.resumeSessionID != "" {
			t.Errorf("resumeSessionID = %q, want empty", sess.resumeSessionID)
		}
		if sess.history != nil {
			t.Errorf("history = %v, want nil", sess.history)
		}
		if len(findAIEvents(mu, events, "ai.run.started")) != 1 {
			t.Error("expected exactly one ai.run.started")
		}
		if len(findAIEvents(mu, events, "ai.delta")) != 1 {
			t.Error("expected exactly one ai.delta")
		}
		if len(findAIEvents(mu, events, "ai.done")) != 1 {
			t.Error("expected exactly one ai.done")
		}
	})

	t.Run("model is read-only because server owns configuration", func(t *testing.T) {
		mgr := newAgentAIManager()
		sess := newSlashTestSession()
		initialModel := sess.model
		mu, events, w := captureAIWriter(t)
		if !mgr.handleLocalSlashCommand(sess, "msg_2", "/model gpt-5.2", "claudecode", w) {
			t.Fatal("expected /model to be handled")
		}
		if sess.model != initialModel {
			t.Errorf("remote /model changed model from %q to %q", initialModel, sess.model)
		}
		deltas := findAIEvents(mu, events, "ai.delta")
		if len(deltas) != 1 || !strings.Contains(remoteString(deltas[0], "delta"), "手机会话工具") {
			t.Fatalf("remote /model should explain the server-owned setting: %#v", deltas)
		}
		// /model without args reports the current model and does not change it.
		sess.model = "glm-5.2"
		if !mgr.handleLocalSlashCommand(sess, "msg_2b", "/model", "claudecode", w) {
			t.Fatal("expected bare /model to be handled")
		}
		if sess.model != "glm-5.2" {
			t.Errorf("bare /model changed model to %q; want unchanged", sess.model)
		}
	})

	t.Run("compact dispatches by provider per the f720737 contract", func(t *testing.T) {
		// f720737 起 /compact 不再回「headless 不支持」：codex/agentcore 各走
		// 真实压缩，claude/claudecode 带 resume 时放行给 headless CLI 原生压缩
		//（handleLocalSlashCommand 返回 false，命令文本原样落成模型回合），
		// 无 resume 才回状态话术。这里只测 claude 两个分支——codex/agentcore
		// 分支会触达 agentCoreEnsureUpstream 的真实网络探测，不属单元测试范畴。
		mgr := newAgentAIManager()

		// claudecode + resume → 放行（返回 false = 不在本地接管），且绝不改动
		// 连续性状态——命令文本要原样到达 CLI。
		sess := newSlashTestSession()
		mu, events, w := captureAIWriter(t)
		if mgr.handleLocalSlashCommand(sess, "msg_3", "/compact", "claudecode", w) {
			t.Fatal("claudecode /compact with resume should fall through to the native CLI turn")
		}
		if sess.resumeSessionID != "resume-abc" {
			t.Errorf("resumeSessionID changed to %q; fall-through must be side-effect-free", sess.resumeSessionID)
		}
		if frames := findAIEvents(mu, events, "ai.delta"); len(frames) != 0 {
			t.Errorf("fall-through must not emit frames, got %#v", frames)
		}

		// claudecode 无 resume → 本地状态话术（还没有可压缩的原生上下文）。
		sessNoResume := newSlashTestSession()
		sessNoResume.resumeSessionID = ""
		mu2, events2, w2 := captureAIWriter(t)
		if !mgr.handleLocalSlashCommand(sessNoResume, "msg_3b", "/compact", "claudecode", w2) {
			t.Fatal("expected /compact without resume to be handled with a status reply")
		}
		deltas := findAIEvents(mu2, events2, "ai.delta")
		if len(deltas) != 1 {
			t.Fatalf("expected 1 ai.delta, got %d", len(deltas))
		}
		delta, _ := deltas[0]["delta"].(string)
		if !strings.Contains(delta, "原生上下文") {
			t.Errorf("expected delta to mention 原生上下文, got %q", delta)
		}
	})

	t.Run("prompt-style and plain text fall through", func(t *testing.T) {
		mgr := newAgentAIManager()
		sess := newSlashTestSession()
		_, _, w := captureAIWriter(t)
		for _, in := range []string{"hello world", "/review", "/unknown-cmd", "/clear"} {
			// "/clear" IS handled; the rest fall through. We only assert the
			// fall-through ones here.
			if in == "/clear" {
				continue
			}
			if mgr.handleLocalSlashCommand(sess, "msg", in, "claudecode", w) {
				t.Errorf("expected %q to fall through (not handled)", in)
			}
		}
	})
}
