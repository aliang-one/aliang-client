package services

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentTranscriptWatchTracksOnlySelectedFile(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "proj")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(dir, "native-a.jsonl")
	other := filepath.Join(dir, "native-b.jsonl")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(selected, "first\n")
	at := time.Unix(100, 0)
	watches := newAgentTranscriptWatches(home)
	watches.touch("ai_import_a", "native-a", "claude", at)
	var sent []string
	notify := func(id string) error {
		sent = append(sent, id)
		return nil
	}
	watches.poll(at, notify)
	write(other, "unrelated\n")
	watches.poll(at.Add(time.Second), notify)
	write(selected, "first\nsecond\n")
	watches.poll(at.Add(2*time.Second), notify)
	watches.touch("ai_import_a", "native-a", "claude", at.Add(3*time.Second))
	watches.poll(at.Add(3*time.Second), notify)
	write(selected, "first\nsecond\nthird\n")
	watches.poll(at.Add(3*time.Second+agentTranscriptWatchLease), notify)

	if len(sent) != 2 || sent[0] != "ai_import_a" || sent[1] != "ai_import_a" {
		t.Fatalf("want initial catch-up and one watched change, got %v", sent)
	}
}

func TestAgentTranscriptWatchRetriesUnsentChangeAndCapsEntries(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".aliang", "ai-sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ai_dashboard.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(100, 0)
	watches := newAgentTranscriptWatches(home)
	watches.touch("ai_dashboard", "", "agentcore", at)
	for i := 0; i < maxAgentTranscriptWatches*2; i++ {
		watches.touch(string(rune('a'+i)), "", "agentcore", at)
	}
	if len(watches.entries) != maxAgentTranscriptWatches {
		t.Fatalf("watch count %d exceeds expected capacity", len(watches.entries))
	}
	attempts := 0
	notify := func(sessionID string) error {
		if sessionID != "ai_dashboard" {
			t.Fatalf("unexpected notification for %s", sessionID)
		}
		attempts++
		if attempts == 1 {
			return errors.New("write failed")
		}
		return nil
	}
	watches.poll(at, notify)
	watches.poll(at.Add(time.Second/2), notify)
	watches.poll(at.Add(time.Second), notify)
	watches.poll(at.Add(2*time.Second), notify)
	if attempts != 2 {
		t.Fatalf("want one retry after failure, got %d attempts", attempts)
	}
}
