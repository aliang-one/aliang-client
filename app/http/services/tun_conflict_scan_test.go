package services

import (
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

func TestDedupeTunInterfaceSnapshots(t *testing.T) {
	t.Run("removes duplicate entries", func(t *testing.T) {
		items := []tunInterfaceSnapshot{
			{Name: "Ethernet 2", Description: "Wintun Userspace Tunnel", Status: "up"},
			{Name: "ethernet 2", Description: "wintun userspace tunnel", Status: "running"}, // Duplicate (case-insensitive)
			{Name: "Wi-Fi", Description: "Intel Wi-Fi", Status: "up"},
		}

		result := dedupeTunInterfaceSnapshots(items)
		if len(result) != 2 {
			t.Fatalf("expected 2 snapshots after deduplication, got %d", len(result))
		}
	})

	t.Run("skips empty entries", func(t *testing.T) {
		items := []tunInterfaceSnapshot{
			{Name: "", Description: "", Status: "up"},
			{Name: "Valid", Description: "Valid Desc", Status: "up"},
		}

		result := dedupeTunInterfaceSnapshots(items)
		if len(result) != 1 {
			t.Fatalf("expected 1 snapshot, got %d", len(result))
		}
		if result[0].Name != "Valid" {
			t.Fatalf("expected 'Valid', got %q", result[0].Name)
		}
	})
}

func TestDetectTunConflictInterfaces(t *testing.T) {
	snapshots := []tunInterfaceSnapshot{
		{Name: "utun2", Status: "running"},
		{Name: "en0", Status: "running"},
		{Name: "Ethernet 2", Description: "Wintun Userspace Tunnel", Status: "up"},
		{Name: "wg0", Status: "up"},
		{Name: "Wi-Fi", Description: "Intel Wi-Fi", Status: "up"},
	}

	conflicts := detectTunConflictInterfaces(snapshots)
	if len(conflicts) != 3 {
		t.Fatalf("expected 3 conflicts, got %d: %#v", len(conflicts), conflicts)
	}

	expected := map[string]string{
		"utun2":      "utun interface",
		"Ethernet 2": "wintun adapter",
		"wg0":        "wireguard adapter",
	}

	for _, item := range conflicts {
		wantReason, ok := expected[item.Name]
		if !ok {
			t.Fatalf("unexpected conflict entry: %#v", item)
		}
		if item.MatchReason != wantReason {
			t.Fatalf("match reason mismatch for %s: got=%q want=%q", item.Name, item.MatchReason, wantReason)
		}
	}
}

func TestScanTunConflictInterfacesSetsRecommendation(t *testing.T) {
	originalLoader := tunInterfaceSnapshotLoader
	defer func() {
		tunInterfaceSnapshotLoader = originalLoader
	}()
	tunInterfaceSnapshotLoader = func() ([]tunInterfaceSnapshot, string) {
		return nil, ""
	}

	result := ScanTunConflictInterfaces()
	if !result.Supported {
		t.Fatal("expected scan result to be supported")
	}
	if result.Platform == "" {
		t.Fatal("expected platform to be populated")
	}
}

type fakeTunConflictPromptStore struct {
	state     *models.UIPromptState
	getErr    error
	upsertErr error
	upserts   []models.UIPromptState
}

func (f *fakeTunConflictPromptStore) GetByKey(promptKey string) (*models.UIPromptState, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.state == nil {
		return nil, nil
	}
	copyValue := *f.state
	return &copyValue, nil
}

func (f *fakeTunConflictPromptStore) Upsert(state models.UIPromptState) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.upserts = append(f.upserts, state)
	copyValue := state
	f.state = &copyValue
	return nil
}

func TestGetTunConflictPromptStatus(t *testing.T) {
	originalFactory := tunConflictPromptStoreFactory
	originalLoader := tunInterfaceSnapshotLoader
	defer func() {
		tunConflictPromptStoreFactory = originalFactory
		tunInterfaceSnapshotLoader = originalLoader
	}()

	tunInterfaceSnapshotLoader = func() ([]tunInterfaceSnapshot, string) {
		return nil, ""
	}

	t.Run("first time prompts even without conflicts", func(t *testing.T) {
		store := &fakeTunConflictPromptStore{}
		tunConflictPromptStoreFactory = func() tunConflictPromptStore { return store }

		result := GetTunConflictPromptStatus()
		if !result.ShouldPrompt {
			t.Fatal("expected first-time prompt to be shown")
		}
		if !result.FirstTimePrompt {
			t.Fatal("expected first_time_prompt=true")
		}
		if result.PromptReason != "first_time" {
			t.Fatalf("unexpected prompt reason: %q", result.PromptReason)
		}
		if len(store.upserts) != 1 {
			t.Fatalf("expected first-time prompt state to be persisted once, got %d", len(store.upserts))
		}
	})

	t.Run("after first time prompt only conflict triggers modal", func(t *testing.T) {
		store := &fakeTunConflictPromptStore{
			state: &models.UIPromptState{
				PromptKey: tunConflictPromptKey,
				SeenAt:    time.Now(),
			},
		}
		tunConflictPromptStoreFactory = func() tunConflictPromptStore { return store }

		result := GetTunConflictPromptStatus()
		if result.ShouldPrompt {
			t.Fatalf("expected no prompt without conflicts after first-time notice, got %#v", result)
		}
		if result.FirstTimePrompt {
			t.Fatal("expected first_time_prompt=false after the first notice")
		}
	})

	t.Run("existing conflict still prompts after first time", func(t *testing.T) {
		tunInterfaceSnapshotLoader = func() ([]tunInterfaceSnapshot, string) {
			return []tunInterfaceSnapshot{
				{Name: "Ethernet 2", Description: "Wintun Userspace Tunnel", Status: "up"},
			}, ""
		}
		store := &fakeTunConflictPromptStore{
			state: &models.UIPromptState{
				PromptKey: tunConflictPromptKey,
				SeenAt:    time.Now(),
			},
		}
		tunConflictPromptStoreFactory = func() tunConflictPromptStore { return store }

		result := GetTunConflictPromptStatus()
		if !result.ShouldPrompt {
			t.Fatal("expected conflict prompt after first-time notice")
		}
		if result.FirstTimePrompt {
			t.Fatal("expected conflict-driven prompt, not first-time prompt")
		}
		if result.PromptReason != "virtual_adapter_detected" {
			t.Fatalf("unexpected prompt reason: %q", result.PromptReason)
		}
		if len(result.Interfaces) == 0 {
			t.Fatal("expected conflict interfaces to be returned")
		}
	})
}
