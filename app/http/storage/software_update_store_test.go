package storage

import (
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

func TestSoftwareVersionUpdateStore_UpsertSnapshotAndDismissal(t *testing.T) {
	store, err := NewSoftwareVersionUpdateStoreWithDBPath(t.TempDir() + "/updates.db")
	if err != nil {
		t.Fatalf("create version update store failed: %v", err)
	}

	now := time.Now().UTC()
	err = store.UpsertSnapshot(models.SoftwareVersionUpdateSnapshot{
		Software:       "aliang-gateway",
		Platform:       "darwin",
		CurrentVersion: "v1.0.0",
		LatestVersion:  "v1.1.0",
		DownloadURL:    "https://example.com/app-v1.1.0.dmg",
		FileType:       "dmg",
		Changelog:      "Bug fixes",
		NeedsUpdate:    true,
		ForceUpdate:    false,
		Status:         "update_available",
		CheckedAt:      now,
		FirstSeenAt:    now,
		LastSeenAt:     now,
	})
	if err != nil {
		t.Fatalf("upsert snapshot failed: %v", err)
	}

	snapshot, err := store.GetSnapshot("aliang-gateway", "darwin")
	if err != nil {
		t.Fatalf("get snapshot failed: %v", err)
	}
	if snapshot == nil {
		t.Fatal("expected snapshot to exist")
	}
	if snapshot.LatestVersion != "v1.1.0" || !snapshot.NeedsUpdate {
		t.Fatalf("unexpected snapshot payload: %+v", snapshot)
	}

	err = store.UpsertDismissal(models.SoftwareVersionUpdateDismissal{
		Software:      "aliang-gateway",
		Platform:      "darwin",
		LatestVersion: "v1.1.0",
		DismissedAt:   now,
	})
	if err != nil {
		t.Fatalf("upsert dismissal failed: %v", err)
	}

	dismissal, err := store.GetDismissal("aliang-gateway", "darwin", "v1.1.0")
	if err != nil {
		t.Fatalf("get dismissal failed: %v", err)
	}
	if dismissal == nil {
		t.Fatal("expected dismissal to exist")
	}

	err = store.UpsertSnapshot(models.SoftwareVersionUpdateSnapshot{
		Software:       "aliang-gateway",
		Platform:       "darwin",
		CurrentVersion: "v1.0.0",
		LatestVersion:  "v2.0.0",
		DownloadURL:    "https://example.com/app-v2.0.0.dmg",
		FileType:       "dmg",
		Changelog:      "Security fixes",
		NeedsUpdate:    true,
		ForceUpdate:    true,
		Status:         "force_update",
		CheckedAt:      now.Add(5 * time.Minute),
		FirstSeenAt:    now.Add(5 * time.Minute),
		LastSeenAt:     now.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("upsert force update snapshot failed: %v", err)
	}

	updatedSnapshot, err := store.GetSnapshot("aliang-gateway", "darwin")
	if err != nil {
		t.Fatalf("get updated snapshot failed: %v", err)
	}
	if updatedSnapshot == nil || updatedSnapshot.LatestVersion != "v2.0.0" || !updatedSnapshot.ForceUpdate {
		t.Fatalf("unexpected updated snapshot payload: %+v", updatedSnapshot)
	}
}
