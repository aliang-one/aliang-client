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

	t.Run("model sets session model", func(t *testing.T) {
		mgr := newAgentAIManager()
		sess := newSlashTestSession()
		_, _, w := captureAIWriter(t)
		if !mgr.handleLocalSlashCommand(sess, "msg_2", "/model gpt-5.2", "claudecode", w) {
			t.Fatal("expected /model to be handled")
		}
		if sess.model != "gpt-5.2" {
			t.Errorf("model = %q, want gpt-5.2", sess.model)
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

	t.Run("compact replies headless unsupported", func(t *testing.T) {
		mgr := newAgentAIManager()
		sess := newSlashTestSession()
		mu, events, w := captureAIWriter(t)
		if !mgr.handleLocalSlashCommand(sess, "msg_3", "/compact", "claudecode", w) {
			t.Fatal("expected /compact to be handled")
		}
		deltas := findAIEvents(mu, events, "ai.delta")
		if len(deltas) != 1 {
			t.Fatalf("expected 1 ai.delta, got %d", len(deltas))
		}
		delta, _ := deltas[0]["delta"].(string)
		if !strings.Contains(delta, "headless") {
			t.Errorf("expected delta to mention headless, got %q", delta)
		}
		// /compact must NOT mutate continuity state.
		if sess.resumeSessionID != "resume-abc" {
			t.Errorf("resumeSessionID changed to %q; /compact should be side-effect-free", sess.resumeSessionID)
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
