package storage

import (
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

func TestUIPromptStateStore_UpsertAndGetByKey(t *testing.T) {
	store, err := NewUIPromptStateStoreWithDBPath(t.TempDir() + "/ui-prompts.db")
	if err != nil {
		t.Fatalf("create ui prompt state store failed: %v", err)
	}

	firstSeenAt := time.Now().UTC().Truncate(time.Second)
	if err := store.Upsert(models.UIPromptState{
		PromptKey: "deep_mode_tun_conflict_notice_v1",
		SeenAt:    firstSeenAt,
	}); err != nil {
		t.Fatalf("upsert ui prompt state failed: %v", err)
	}

	state, err := store.GetByKey("deep_mode_tun_conflict_notice_v1")
	if err != nil {
		t.Fatalf("get ui prompt state failed: %v", err)
	}
	if state == nil {
		t.Fatal("expected prompt state to exist")
	}
	if state.SeenAt.Unix() != firstSeenAt.Unix() {
		t.Fatalf("unexpected seen_at after first upsert: got=%v want=%v", state.SeenAt, firstSeenAt)
	}

	updatedSeenAt := firstSeenAt.Add(5 * time.Minute)
	if err := store.Upsert(models.UIPromptState{
		PromptKey: "deep_mode_tun_conflict_notice_v1",
		SeenAt:    updatedSeenAt,
	}); err != nil {
		t.Fatalf("second upsert ui prompt state failed: %v", err)
	}

	updated, err := store.GetByKey("deep_mode_tun_conflict_notice_v1")
	if err != nil {
		t.Fatalf("get updated ui prompt state failed: %v", err)
	}
	if updated == nil {
		t.Fatal("expected updated prompt state to exist")
	}
	if updated.SeenAt.Unix() != updatedSeenAt.Unix() {
		t.Fatalf("unexpected seen_at after second upsert: got=%v want=%v", updated.SeenAt, updatedSeenAt)
	}
}
