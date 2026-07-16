package cmd

import (
	"encoding/json"
	"testing"
)

func TestHandleSyncAgentAuthSkipsWithoutAuthenticatedSession(t *testing.T) {
	originalAuthorization := currentAgentRegisterAuthorizationHeader
	originalSync := syncUserAgentAfterAuthWithRetry
	currentAgentRegisterAuthorizationHeader = func() string { return "" }
	syncCalled := false
	syncUserAgentAfterAuthWithRetry = func(string) error {
		syncCalled = true
		return nil
	}
	t.Cleanup(func() {
		currentAgentRegisterAuthorizationHeader = originalAuthorization
		syncUserAgentAfterAuthWithRetry = originalSync
	})

	result, err := handleSyncAgentAuth(json.RawMessage(`{"reason":"watchdog_auth_reconcile"}`))
	if err != nil {
		t.Fatal(err)
	}
	if syncCalled {
		t.Fatal("sync should not run without an authenticated user session")
	}
	status, ok := result.(map[string]interface{})
	if !ok || status["status"] != "skipped" {
		t.Fatalf("result = %#v, want skipped status", result)
	}
}

func TestHandleSyncAgentAuthRunsWithAuthenticatedSession(t *testing.T) {
	originalAuthorization := currentAgentRegisterAuthorizationHeader
	originalSync := syncUserAgentAfterAuthWithRetry
	currentAgentRegisterAuthorizationHeader = func() string { return "Bearer test" }
	gotReason := ""
	syncUserAgentAfterAuthWithRetry = func(reason string) error {
		gotReason = reason
		return nil
	}
	t.Cleanup(func() {
		currentAgentRegisterAuthorizationHeader = originalAuthorization
		syncUserAgentAfterAuthWithRetry = originalSync
	})

	result, err := handleSyncAgentAuth(json.RawMessage(`{"reason":"watchdog_auth_reconcile"}`))
	if err != nil {
		t.Fatal(err)
	}
	if gotReason != "watchdog_auth_reconcile" {
		t.Fatalf("sync reason = %q", gotReason)
	}
	status, ok := result.(map[string]interface{})
	if !ok || status["status"] != "synced" {
		t.Fatalf("result = %#v, want synced status", result)
	}
}
