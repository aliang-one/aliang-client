package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	authuser "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/runtime"
)

func TestStartupStatusIncludesCanonicalSessionSnapshot(t *testing.T) {
	authority := authuser.ResetSessionAuthorityForTest()
	authority.NotifyLoggedIn(&authuser.UserInfo{ID: 7, Username: "snapshot-user"})
	t.Cleanup(func() { authuser.ResetSessionAuthorityForTest() })

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/startup/status", nil)
	NewStartupHandler().HandleStartupStatus(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, _ := envelope["data"].(map[string]interface{})
	session, _ := data["session"].(map[string]interface{})
	user, _ := session["user"].(map[string]interface{})
	if session["type"] != "session_snapshot" || session["state"] != "active" || user["username"] != "snapshot-user" {
		t.Fatalf("startup session snapshot=%#v", session)
	}
	if session["instance_id"] == "" || session["revision"] == nil {
		t.Fatalf("startup session version missing: %#v", session)
	}
}

func TestEffectiveStartupAuthSnapshotHidesUserAfterHardInvalid(t *testing.T) {
	status, fetchSuccess, user := effectiveStartupAuthSnapshot(
		runtime.READY,
		true,
		&authuser.UserInfo{Username: "stale-user"},
		authuser.StateHardInvalid,
	)

	if status != runtime.UNCONFIGURED || fetchSuccess || user != nil {
		t.Fatalf("snapshot = status=%v fetch=%t user=%#v, want UNCONFIGURED/false/nil", status, fetchSuccess, user)
	}
}

func TestEffectiveStartupAuthSnapshotKeepsSoftExpiredUser(t *testing.T) {
	wantUser := &authuser.UserInfo{Username: "recovering-user"}
	status, fetchSuccess, user := effectiveStartupAuthSnapshot(
		runtime.READY,
		true,
		wantUser,
		authuser.StateSoftExpired,
	)

	if status != runtime.CONFIGURING || fetchSuccess || user != wantUser {
		t.Fatalf("snapshot = status=%v fetch=%t user=%#v, want CONFIGURING/false/original user", status, fetchSuccess, user)
	}
}

func TestGetSuggestedActions_UsesSessionLoginSemanticsForConfiguring(t *testing.T) {
	actions := getSuggestedActions(runtime.CONFIGURING)
	want := []string{
		"GET /api/auth/session - Read current session recovery state",
		"POST /api/auth/login - Login if no local session",
		"GET /api/startup/status - Check authentication progress",
	}

	if !reflect.DeepEqual(actions, want) {
		t.Fatalf("unexpected actions for CONFIGURING:\nwant=%v\ngot=%v", want, actions)
	}
}

func TestGetStatusTransitionInfo_UsesAuthSessionWording(t *testing.T) {
	info := getStatusTransitionInfo(runtime.CONFIGURING)

	description, ok := info["description"].(string)
	if !ok {
		t.Fatalf("description missing or not string: %v", info)
	}
	if description != "Authentication in progress" {
		t.Fatalf("unexpected CONFIGURING description: %q", description)
	}

	transitions, ok := info["possible_transitions"].([]string)
	if !ok {
		t.Fatalf("possible_transitions missing or wrong type: %T %#v", info["possible_transitions"], info["possible_transitions"])
	}

	wantTransitions := []string{
		"→ READY (session restore or login success)",
		"→ UNCONFIGURED (authentication failed, no local session)",
	}
	if !reflect.DeepEqual(transitions, wantTransitions) {
		t.Fatalf("unexpected CONFIGURING transitions:\nwant=%v\ngot=%v", wantTransitions, transitions)
	}
}
