package cmd

import (
	"testing"
	"time"

	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/runtime"
)

func setupStartupStatusTestAuthEnv(t *testing.T) {
	baseDir := t.TempDir()
	t.Setenv("HOME", baseDir)
	t.Setenv("ALIANG_CACHE_DIR", baseDir)
	auth.ResetAuthPersistenceForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
	})
}

func TestDetermineInitialStartupStatus_WhenTokenProvided_ReturnsConfiguring(t *testing.T) {
	setupStartupStatusTestAuthEnv(t)

	previousDefault := IsUsingDefaultConfig()
	setUseDefaultConfig(false)
	t.Cleanup(func() {
		setUseDefaultConfig(previousDefault)
	})

	status := DetermineInitialStartupStatusForTest("token-123")
	if status != runtime.CONFIGURING {
		t.Fatalf("expected %s, got %s", runtime.CONFIGURING, status)
	}
}

func TestDetermineInitialStartupStatus_WhenNoTokenNoLocalUser_ReturnsUnconfigured(t *testing.T) {
	setupStartupStatusTestAuthEnv(t)

	previousDefault := IsUsingDefaultConfig()
	setUseDefaultConfig(false)
	t.Cleanup(func() {
		setUseDefaultConfig(previousDefault)
	})

	status := DetermineInitialStartupStatusForTest("")
	if status != runtime.UNCONFIGURED {
		t.Fatalf("expected %s, got %s", runtime.UNCONFIGURED, status)
	}
}

func TestDetermineInitialStartupStatus_WhenNoTokenWithLocalUserAndDefaultConfig_ReturnsReady(t *testing.T) {
	setupStartupStatusTestAuthEnv(t)

	if err := auth.SaveUserInfo(&auth.UserInfo{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		Username:     "tester",
		Email:        "tester@example.com",
		Status:       "active",
		UpdatedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("failed to save sqlite user info: %v", err)
	}

	previousDefault := IsUsingDefaultConfig()
	setUseDefaultConfig(true)
	t.Cleanup(func() {
		setUseDefaultConfig(previousDefault)
	})

	status := DetermineInitialStartupStatusForTest("")
	if status != runtime.READY {
		t.Fatalf("expected %s, got %s", runtime.READY, status)
	}
}

func TestDetermineInitialStartupStatus_WhenNoTokenWithLocalUserAndCustomConfig_ReturnsConfigured(t *testing.T) {
	setupStartupStatusTestAuthEnv(t)

	if err := auth.SaveUserInfo(&auth.UserInfo{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		Username:     "tester",
		Email:        "tester@example.com",
		Status:       "active",
		UpdatedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("failed to save sqlite user info: %v", err)
	}

	previousDefault := IsUsingDefaultConfig()
	setUseDefaultConfig(false)
	t.Cleanup(func() {
		setUseDefaultConfig(previousDefault)
	})

	status := DetermineInitialStartupStatusForTest("")
	if status != runtime.CONFIGURED {
		t.Fatalf("expected %s, got %s", runtime.CONFIGURED, status)
	}
}
